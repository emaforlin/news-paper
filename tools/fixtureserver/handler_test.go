package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emaforlin/news-paper/internal/httpx"
	"github.com/emaforlin/news-paper/internal/rank"
	"github.com/emaforlin/news-paper/internal/sources"
)

// start serves two pages and returns the server.
func start(t *testing.T, llm string) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "source.html"), []byte("<p>"+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewUnstartedServer(nil)
	h, err := newHandler(dir, srv.Listener.Addr().String(), llm)
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = h
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// loopback can only dial srv.
func loopback(srv *httptest.Server) *httpx.Client {
	return &httpx.Client{HTTP: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, srv.Listener.Addr().String())
		},
	}}}
}

// The app's own clients must understand every fixture.
func TestFixtures(t *testing.T) {
	srv := start(t, llmOK)
	hc, ctx := loopback(srv), context.Background()

	cands := sources.FetchAll(ctx, hc, []sources.Source{
		&sources.HackerNews{APIBase: srv.URL + "/hn", Now: time.Now()},
		&sources.Feed{FeedName: "bench", URL: srv.URL + "/feed.xml"},
	}, 2, nil)
	if len(cands) != 4 {
		t.Fatalf("candidates = %+v", cands)
	}
	lob, err := (&sources.Lobsters{URL: srv.URL + "/lobsters/hottest.json"}).Fetch(ctx, hc)
	if err != nil || len(lob) != 0 {
		t.Errorf("lobsters = %v, %v", lob, err)
	}

	for path, status := range map[string]int{"/p/a/": 200, "/p/nope/": 404} {
		resp, err := hc.Get(ctx, srv.URL+path, nil, 0)
		if err != nil || resp.StatusCode != status {
			t.Errorf("%s: %+v, %v", path, resp, err)
		}
	}

	llm := &rank.OpenRouter{HC: hc, BaseURL: srv.URL + "/openrouter", APIKey: "k", Model: "bench/mock"}
	picks, err := llm.Rank(ctx, cands, 3)
	if err != nil || len(picks) != 3 || picks[0].Reason != "bench" || picks[0].URL != cands[0].URL {
		t.Errorf("picks = %+v, %v", picks, err)
	}
}

func TestFailingLLM(t *testing.T) {
	srv := start(t, llmFail)
	resp, err := loopback(srv).Post(context.Background(), srv.URL+"/openrouter/chat/completions", nil, []byte("{}"), 0)
	if err != nil || resp.StatusCode != 500 {
		t.Errorf("%+v, %v", resp, err)
	}
}

func TestBadSetup(t *testing.T) {
	for _, c := range []struct{ dir, llm string }{{"", llmOK}, {t.TempDir(), llmOK}, {t.TempDir(), "maybe"}} {
		if _, err := newHandler(c.dir, "x", c.llm); err == nil {
			t.Errorf("newHandler(%q, %q) accepted", c.dir, c.llm)
		}
	}
}
