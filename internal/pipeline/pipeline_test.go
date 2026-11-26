package pipeline

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emaforlin/news-paper/internal/news"
	"github.com/emaforlin/news-paper/internal/sources"
	"github.com/emaforlin/news-paper/internal/state"
)

var testNow = time.Date(2026, 11, 26, 8, 40, 0, 0, time.UTC)

func run(t *testing.T, r *rig, opt Options) (*Result, error) {
	t.Helper()
	if opt.Now.IsZero() {
		opt.Now = testNow
	}
	return Run(context.Background(), testConfig(t), r.deps, opt)
}

func opfLanguage(t *testing.T, data []byte) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	f, err := zr.Open("OEBPS/content.opf")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	opf, _ := io.ReadAll(f)
	_, rest, _ := strings.Cut(string(opf), "<dc:language>")
	lang, _, _ := strings.Cut(rest, "</dc:language>")
	return lang
}

func TestRunHappyPath(t *testing.T) {
	urls := []string{
		"http://a.test/ok1", "http://a.test/gone", "http://b.test/flaky",
		"http://b.test/ok2", "http://c.test/short", "http://d.test/ok3",
	}
	r := newRig(t, urls...)
	r.deps.Sources = append(r.deps.Sources, &fakeSource{name: "broken", err: errors.New("down")})
	cfg := testConfig(t)
	*cfg.Edition.Picks = 3

	res, err := Run(context.Background(), cfg, r.deps, Options{Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.calls.String(); got != "load archive send save" {
		t.Errorf("calls = %s", got)
	}
	if !res.Sent || res.Skipped || res.Candidates != 6 || res.Pool != 6 || res.Picks != 6 || res.Rejected != 2 ||
		len(res.Edition.Articles) != 3 || res.Date != "2026-11-26" {
		t.Errorf("result = %+v", res)
	}
	saved := r.store.saved
	for url, want := range map[string]bool{
		urls[0]: true, urls[1]: true, urls[2]: false, urls[3]: true, urls[4]: true, urls[5]: true,
	} {
		if got := saved.Has(news.URLID(url)); got != want {
			t.Errorf("state has %s = %v, want %v", url, got, want)
		}
	}
	if len(saved.Seen) != 5 {
		t.Errorf("state = %v", saved.Seen)
	}
	if r.mailer.from != "me@example.com" || r.mailer.to != "me@kindle.com" || len(r.mailer.sent) != 1 {
		t.Errorf("mail %s -> %s (%d)", r.mailer.from, r.mailer.to, len(r.mailer.sent))
	}
	done := r.logged(t, "run done")
	for _, k := range []string{"date", "candidates", "pool", "picks", "articles", "rejected", "bytes", "sent", "skipped", "dry_run", "took"} {
		if _, ok := done[k]; !ok {
			t.Errorf("run done lacks %q: %v", k, done)
		}
	}
}

func TestRunPrunesState(t *testing.T) {
	r := newRig(t, "http://a.test/ok")
	r.store.st = &state.State{Seen: map[string]int64{"old": 1}}
	if _, err := run(t, r, Options{}); err != nil {
		t.Fatal(err)
	}
	if r.store.saved.Has("old") {
		t.Error("old entry not pruned")
	}
}

func TestRunSavesAfterCancel(t *testing.T) {
	r := newRig(t, "http://a.test/ok")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.mailer.onSend = cancel // e.g. Ctrl-C right after the email went out

	res, err := Run(ctx, testConfig(t), r.deps, Options{Now: testNow})
	if err != nil || !res.Sent || r.store.saved == nil || r.store.saveCtxErr != nil {
		t.Errorf("sent=%v err=%v save ctx err=%v", res.Sent, err, r.store.saveCtxErr)
	}
}

func TestRunDryRun(t *testing.T) {
	r := newRig(t, "http://a.test/ok")
	r.deps.Archive, r.deps.Mailer = nil, nil
	cfg := testConfig(t)
	cfg.SMTP.Host, cfg.SMTP.From, cfg.SMTP.To = "", "", ""

	res, err := Run(context.Background(), cfg, r.deps, Options{DryRun: true, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent || r.calls.String() != "load" || len(res.EPUB) == 0 || opfLanguage(t, res.EPUB) != "en" {
		t.Errorf("sent=%v calls=%q bytes=%d", res.Sent, r.calls, len(res.EPUB))
	}
	if r.logged(t, "run done")["dry_run"] != true {
		t.Error("run done lacks dry_run=true")
	}
}

func TestRunValidateFirst(t *testing.T) {
	var fetched atomic.Int32
	r := newRig(t)
	r.deps.Sources = []sources.Source{&fakeSource{name: "s", fetch: func() { fetched.Add(1) }}}
	cfg := testConfig(t)
	cfg.SMTP.To = ""
	res, err := Run(context.Background(), cfg, r.deps, Options{})
	if err == nil || !strings.Contains(err.Error(), "config: smtp.to") || res == nil {
		t.Errorf("err = %v", err)
	}
	if fetched.Load() != 0 || r.calls.String() != "" {
		t.Errorf("fetched %d, calls %q", fetched.Load(), r.calls)
	}

	for name, strip := range map[string]func(*Deps){
		"State":   func(d *Deps) { d.State = nil },
		"Archive": func(d *Deps) { d.Archive = nil },
		"Mailer":  func(d *Deps) { d.Mailer = nil },
	} {
		r := newRig(t, "http://a.test/ok")
		strip(&r.deps)
		_, err := run(t, r, Options{})
		if err == nil || err.Error() != "pipeline: missing dependency: "+name || r.calls.String() != "" {
			t.Errorf("without %s: %v, calls %q", name, err, r.calls)
		}
	}
	if _, _, _, err := Pool(context.Background(), testConfig(t), Deps{}, testNow); err == nil {
		t.Error("Pool without a store")
	}
}

func TestRunSkipped(t *testing.T) {
	r := newRig(t, "http://a.test/ok")
	r.archive.exists = true
	res, err := run(t, r, Options{})
	if err != nil || !res.Skipped || res.Sent || r.calls.String() != "load archive" {
		t.Errorf("res=%+v err=%v calls=%q", res, err, r.calls)
	}
	if l := r.logged(t, "edition already archived, not sending"); l == nil || l["level"] != "WARN" {
		t.Errorf("warning = %v", l)
	}
}

func TestRunArchiveFails(t *testing.T) {
	r := newRig(t, "http://a.test/ok")
	r.archive.err = errors.New("disk full")
	res, err := run(t, r, Options{})
	if !errors.Is(err, r.archive.err) || res.Sent || len(res.EPUB) == 0 || r.calls.String() != "load archive" {
		t.Errorf("err=%v calls=%q", err, r.calls)
	}
}

func TestRunSendFails(t *testing.T) {
	r := newRig(t, "http://a.test/ok")
	r.mailer.err = errors.New("smtp down")
	res, err := run(t, r, Options{})
	if !errors.Is(err, r.mailer.err) || res.Sent || len(res.EPUB) == 0 || r.calls.String() != "load archive send" {
		t.Errorf("err=%v calls=%q", err, r.calls)
	}
	l := r.logged(t, "edition archived but not delivered")
	if l == nil || l["level"] != "ERROR" || !strings.Contains(l["hint"].(string), "news-paper send") {
		t.Errorf("log = %v", l)
	}
}

func TestRunSaveFails(t *testing.T) {
	r := newRig(t, "http://a.test/ok")
	r.store.saveErr = state.ErrConflict
	res, err := run(t, r, Options{})
	if !errors.Is(err, state.ErrConflict) || !res.Sent {
		t.Errorf("sent=%v err=%v", res.Sent, err)
	}
}

func TestRunAbort(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name  string
		setup func(*rig)
		want  error
		pool  int
	}{
		{"all sources fail", func(r *rig) { r.deps.Sources = []sources.Source{&fakeSource{name: "s", err: boom}} }, ErrNoCandidates, 0},
		{"empty pool", func(r *rig) {
			r.store.st = state.New()
			r.store.st.Add(testNow, news.URLID("http://a.test/gone"))
		}, ErrNoPicks, 0},
		{"every page rejected", func(*rig) {}, ErrNoArticles, 1},
		{"state load", func(r *rig) { r.store.loadErr = boom }, boom, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, "http://a.test/gone")
			tt.setup(r)
			res, err := run(t, r, Options{})
			if !errors.Is(err, tt.want) || res == nil || res.EPUB != nil || res.Pool != tt.pool {
				t.Errorf("err = %v, res = %+v", err, res)
			}
			if tt.want == boom && tt.name == "state load" && !strings.HasPrefix(err.Error(), "state load: ") {
				t.Errorf("err = %v", err)
			}
			if r.calls.String() != "load" {
				t.Errorf("calls = %q", r.calls)
			}
		})
	}
}

func TestRunEPUBError(t *testing.T) {
	var urls []string
	for i := range 100 {
		urls = append(urls, "http://a.test/ok"+strings.Repeat("x", i))
	}
	r := newRig(t, urls...)
	cfg := testConfig(t)
	*cfg.Edition.Picks = 100 // past validation on purpose: epub takes at most 99
	cfg.Edition.MaxPerDomain = -1

	res, err := Run(context.Background(), cfg, r.deps, Options{Now: testNow})
	if err == nil || !strings.HasPrefix(err.Error(), "epub: ") || res.EPUB != nil || r.calls.String() != "load" {
		t.Errorf("err=%v calls=%q", err, r.calls)
	}
}

func TestRunBuildBeforeLock(t *testing.T) {
	r := newRig(t, "http://a.test/ok")
	cfg := testConfig(t)
	cfg.SMTP.From = "a@b.test\nBcc: x@y.test"
	_, err := Run(context.Background(), cfg, r.deps, Options{Now: testNow})
	if err == nil || !strings.Contains(err.Error(), "mail: invalid From") || r.archive.puts != 0 {
		t.Errorf("err = %v, puts = %d", err, r.archive.puts)
	}
}

func TestRunTimezone(t *testing.T) {
	r := newRig(t, "http://a.test/ok")
	res, err := run(t, r, Options{Now: time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Date != "2026-11-25" || r.archive.date != "2026-11-25" || res.Edition.Date.Hour() != 23 {
		t.Errorf("date=%s archive=%s edition=%s", res.Date, r.archive.date, res.Edition.Date)
	}

	r = newRig(t, "http://a.test/ok")
	res, err = Run(context.Background(), testConfig(t), r.deps, Options{}) // Now defaults to the clock
	if err != nil || time.Since(res.Edition.Date) > time.Minute {
		t.Errorf("date=%s err=%v", res.Edition.Date, err)
	}
}

func TestRunInvalidLanguage(t *testing.T) {
	for lang, warns := range map[string]bool{"english": true, "pt-BR": false} {
		r := newRig(t, "http://a.test/ok")
		cfg := testConfig(t)
		cfg.Edition.Language = lang
		res, err := Run(context.Background(), cfg, r.deps, Options{DryRun: true, Now: testNow})
		if err != nil {
			t.Fatal(err)
		}
		l := r.logged(t, "edition.language invalid, using en")
		if (l != nil) != warns || (warns && l["language"] != lang) {
			t.Errorf("%s: warning = %v", lang, l)
		}
		if want := map[bool]string{true: "en", false: lang}[warns]; opfLanguage(t, res.EPUB) != want {
			t.Errorf("%s: dc:language = %s", lang, opfLanguage(t, res.EPUB))
		}
	}
}

func TestRankerSelection(t *testing.T) {
	var requests atomic.Int32
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"picks\":[{\"id\":1,\"reason\":\"llm\"}]}"}}]}`)
	}))
	defer llm.Close()

	tests := []struct {
		name, model, key string
		wantWarn         string
		wantRequests     int32
		wantReason       string // of the first article
	}{
		{"no model", "", "sk", "rank.model not configured, using heuristic", 0, "Popular en hn"},
		{"placeholder", "CHANGE-ME/x", "sk", "rank.model not configured, using heuristic", 0, "Popular en hn"},
		{"no key", "m/x", "", "ranker failed, using fallback", 0, "Popular en hn"},
		{"configured", "m/x", "sk-test", "", 1, "llm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests.Store(0)
			r := newRig(t, "http://a.test/ok1", "http://b.test/ok2")
			r.deps.Ranker = nil
			r.deps.LLMHTTP = routeClient(t, llm)
			cfg := testConfig(t)
			cfg.Rank.Provider, cfg.Rank.Model, cfg.Rank.APIKey = "openrouter", tt.model, ""
			if tt.key != "" {
				cfg.Rank.APIKey = "sk-test"
			}
			res, err := Run(context.Background(), cfg, r.deps, Options{DryRun: true, Now: testNow})
			if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != tt.wantRequests || res.Edition.Articles[0].Reason != tt.wantReason {
				t.Errorf("requests=%d first=%q", requests.Load(), res.Edition.Articles[0].Reason)
			}
			if tt.wantWarn != "" && r.logged(t, tt.wantWarn) == nil {
				t.Errorf("missing warning %q in %s", tt.wantWarn, r.logs)
			}
		})
	}

	t.Run("heuristic with cap", func(t *testing.T) {
		r := newRig(t, "http://a.test/ok1", "http://a.test/ok2", "http://a.test/ok3", "http://b.test/ok4", "http://c.test/ok5")
		r.deps.Ranker = nil
		cfg := testConfig(t)
		*cfg.Edition.Picks, *cfg.Edition.Overselect, cfg.Edition.MaxPerDomain = 3, 0, 1
		res, err := Run(context.Background(), cfg, r.deps, Options{DryRun: true, Now: testNow})
		if err != nil {
			t.Fatal(err)
		}
		var domains []string
		for _, a := range res.Edition.Articles {
			domains = append(domains, a.Domain())
		}
		if got := strings.Join(domains, " "); got != "a.test b.test c.test" {
			t.Errorf("domains = %s", got)
		}
	})
}

func TestRunSendReserve(t *testing.T) {
	r := newRig(t, "http://a.test/ok")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := Run(ctx, testConfig(t), r.deps, Options{Now: testNow})
	if !errors.Is(err, ErrNoArticles) || r.site.hits.Load() != 0 {
		t.Errorf("err = %v after %d page requests", err, r.site.hits.Load())
	}

	ctx, cancel = context.WithTimeout(context.Background(), sendReserve+30*time.Second)
	defer cancel()
	if _, err := Run(ctx, testConfig(t), r.deps, Options{DryRun: true, Now: testNow}); err != nil {
		t.Errorf("with time to spare: %v", err)
	}
}

func TestPool(t *testing.T) {
	var inflight, peak atomic.Int32
	var mu sync.Mutex
	fetch := func() {
		n := inflight.Add(1)
		mu.Lock()
		peak.Store(max(peak.Load(), n))
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		inflight.Add(-1)
	}
	r := newRig(t)
	r.deps.Sources = nil
	for _, u := range []string{"http://a.test/1", "http://seen.test/2", "http://x.paywall.test/3", "http://b.test/4"} {
		r.deps.Sources = append(r.deps.Sources, &fakeSource{name: u, cands: cands(u), fetch: fetch})
	}
	r.store.st = state.New()
	r.store.st.Add(testNow, news.URLID("http://seen.test/2"))
	cfg := testConfig(t)
	cfg.HTTP.Concurrency = 2
	cfg.Extract.DenyDomains = []string{"paywall.test"}

	st, fetched, pool, err := Pool(context.Background(), cfg, r.deps, testNow)
	if err != nil || st != r.store.st || fetched != 4 || len(pool) != 2 || peak.Load() != 2 {
		t.Errorf("fetched=%d pool=%d peak=%d err=%v", fetched, len(pool), peak.Load(), err)
	}
}

// With no sources injected they are built from the config, and a nil HTTP
// client and logger get defaults. Nothing is enabled, so nothing is fetched.
func TestRunDefaults(t *testing.T) {
	r := newRig(t)
	deps := Deps{State: r.store}
	_, err := Run(context.Background(), testConfig(t), deps, Options{DryRun: true, Now: testNow})
	if !errors.Is(err, ErrNoCandidates) {
		t.Errorf("err = %v", err)
	}
}
