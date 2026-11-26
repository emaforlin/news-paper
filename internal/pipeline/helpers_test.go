package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emaforlin/news-paper/internal/config"
	"github.com/emaforlin/news-paper/internal/httpx"
	"github.com/emaforlin/news-paper/internal/news"
	"github.com/emaforlin/news-paper/internal/sources"
	"github.com/emaforlin/news-paper/internal/state"
)

// noSecrets keeps a developer's real secrets out of the tests.
func noSecrets(t testing.TB) {
	t.Setenv(config.EnvOpenRouterKey, "")
	t.Setenv(config.EnvSMTPPassword, "")
}

func testConfig(t testing.TB) *config.Config {
	t.Helper()
	noSecrets(t)
	cfg, err := config.LoadBytes([]byte(`{
		"rank": {"provider": "heuristic", "base_url": "http://llm.test/v1"},
		"smtp": {"host": "smtp.test", "from": "me@example.com", "to": "me@kindle.com"},
		"extract": {"concurrency": 2}
	}`), "test.json")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// routeClient dials srv whatever host the URL names, so tests can use several
// domains and no request can leave the process.
func routeClient(t testing.TB, srv *httptest.Server) *httpx.Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, srv.Listener.Addr().String())
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &httpx.Client{HTTP: &http.Client{Transport: tr, Timeout: 5 * time.Second}}
}

// site serves article pages by path: /ok…, /short…, /gone… (404) and /flaky… (500).
type site struct {
	srv  *httptest.Server
	hits atomic.Int32
}

func newSite(t testing.TB) *site {
	s := &site{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case strings.HasPrefix(r.URL.Path, "/ok"):
			_, _ = io.WriteString(w, page("Title "+r.URL.Path, 400))
		case strings.HasPrefix(r.URL.Path, "/short"):
			_, _ = io.WriteString(w, page("Short", 30))
		case strings.HasPrefix(r.URL.Path, "/gone"):
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// page is an article of about words words.
func page(title string, words int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<html lang="en"><head><title>%s</title></head><body><article><h1>%s</h1>`, title, title)
	for words > 0 {
		n := min(words, 50)
		words -= n
		b.WriteString("<p>" + strings.Repeat("database, ", n) + "</p>")
	}
	b.WriteString("</article></body></html>")
	return b.String()
}

func cand(u string, score int) news.Candidate {
	return news.Candidate{URL: u, Title: "T " + u, Source: "hn", Score: score}
}

func cands(urls ...string) []news.Candidate {
	var out []news.Candidate
	for i, u := range urls {
		out = append(out, cand(u, 1000-i))
	}
	return out
}

// calls records the order of the side effects.
type calls struct {
	mu   sync.Mutex
	list []string
}

func (c *calls) add(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list = append(c.list, name)
}

func (c *calls) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.list, " ")
}

type fakeSource struct {
	name  string
	cands []news.Candidate
	err   error
	fetch func()
}

func (f *fakeSource) Name() string { return f.name }

func (f *fakeSource) Fetch(context.Context, *httpx.Client) ([]news.Candidate, error) {
	if f.fetch != nil {
		f.fetch()
	}
	return f.cands, f.err
}

// fakeRanker returns the candidates in input order.
type fakeRanker struct{ err error }

func (fakeRanker) Name() string { return "fake" }

func (f fakeRanker) Rank(_ context.Context, cs []news.Candidate, n int) ([]news.Pick, error) {
	var out []news.Pick
	for _, c := range cs[:min(n, len(cs))] {
		out = append(out, news.Pick{Candidate: c})
	}
	return out, f.err
}

type fakeStore struct {
	calls            *calls
	st               *state.State
	loadErr, saveErr error
	saved            *state.State
	saveCtxErr       error
}

func (f *fakeStore) Load(context.Context) (*state.State, error) {
	f.calls.add("load")
	if f.st == nil {
		f.st = state.New()
	}
	return f.st, f.loadErr
}

func (f *fakeStore) Save(ctx context.Context, s *state.State) error {
	f.calls.add("save")
	f.saved, f.saveCtxErr = s, ctx.Err()
	return f.saveErr
}

type fakeArchive struct {
	calls  *calls
	exists bool
	err    error
	date   string
	puts   int
}

func (f *fakeArchive) PutIfAbsent(_ context.Context, date string, _ []byte) (bool, error) {
	f.calls.add("archive")
	f.puts++
	f.date = date
	return !f.exists && f.err == nil, f.err
}

type fakeMailer struct {
	calls    *calls
	err      error
	from, to string
	sent     [][]byte
	onSend   func()
}

func (f *fakeMailer) Send(_ context.Context, from, to string, msg []byte) error {
	f.calls.add("send")
	f.from, f.to = from, to
	f.sent = append(f.sent, msg)
	if f.onSend != nil {
		f.onSend()
	}
	return f.err
}

// rig is a Deps made of fakes around one site.
type rig struct {
	calls   *calls
	site    *site
	store   *fakeStore
	archive *fakeArchive
	mailer  *fakeMailer
	logs    *bytes.Buffer
	deps    Deps
}

func newRig(t testing.TB, urls ...string) *rig {
	c := &calls{}
	r := &rig{
		calls:   c,
		site:    newSite(t),
		store:   &fakeStore{calls: c},
		archive: &fakeArchive{calls: c},
		mailer:  &fakeMailer{calls: c},
		logs:    &bytes.Buffer{},
	}
	client := routeClient(t, r.site.srv)
	r.deps = Deps{
		HTTP: client, LLMHTTP: client,
		State: r.store, Archive: r.archive, Mailer: r.mailer,
		Log:     slog.New(slog.NewJSONHandler(r.logs, nil)),
		Sources: []sources.Source{&fakeSource{name: "s", cands: cands(urls...)}},
		Ranker:  fakeRanker{},
	}
	return r
}

// logged returns the first record with msg, or nil.
func (r *rig) logged(t testing.TB, msg string) map[string]any {
	t.Helper()
	for dec := json.NewDecoder(bytes.NewReader(r.logs.Bytes())); dec.More(); {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		if m["msg"] == msg {
			return m
		}
	}
	return nil
}
