package sources

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emaforlin/news-paper/internal/httpx"
	"github.com/emaforlin/news-paper/internal/news"
)

// fakeSource is a controllable Source.
type fakeSource struct {
	name  string
	items []news.Candidate
	err   error
	panic any

	wait <-chan struct{} // if set, Fetch blocks until it is closed or receives
	done chan struct{}   // if set, closed when Fetch returns

	inflight, peak *atomic.Int32   // concurrency probe
	started        chan<- struct{} // notified once Fetch is running
}

func (f *fakeSource) Name() string { return f.name }

func (f *fakeSource) Fetch(ctx context.Context, hc *httpx.Client) ([]news.Candidate, error) {
	if f.done != nil {
		defer close(f.done)
	}
	if hc == nil {
		return nil, errors.New("nil client")
	}
	if f.inflight != nil {
		n := f.inflight.Add(1)
		defer f.inflight.Add(-1)
		for p := f.peak.Load(); n > p && !f.peak.CompareAndSwap(p, n); p = f.peak.Load() {
		}
	}
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.wait != nil {
		<-f.wait
	}
	if f.panic != nil {
		panic(f.panic)
	}
	return f.items, f.err
}

func cands(src string, n int) []news.Candidate {
	out := make([]news.Candidate, n)
	for i := range out {
		out[i] = news.Candidate{URL: fmt.Sprintf("https://%s.example/%d", src, i), Source: src}
	}
	return out
}

func TestFetchAllOrder(t *testing.T) {
	for range 20 {
		// src k waits for src k+1 to finish: completion order is 2, 1, 0.
		done := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
		srcs := []Source{
			&fakeSource{name: "s0", items: cands("s0", 2), wait: done[1], done: done[0]},
			&fakeSource{name: "s1", items: cands("s1", 1), wait: done[2], done: done[1]},
			&fakeSource{name: "s2", items: cands("s2", 3), done: done[2]},
		}
		got := FetchAll(context.Background(), &httpx.Client{}, srcs, 3, nil)
		want := append(append(cands("s0", 2), cands("s1", 1)...), cands("s2", 3)...)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v\nwant %v", got, want)
		}
	}
}

func TestFetchAllFailure(t *testing.T) {
	tests := []struct {
		name    string
		bad     *fakeSource
		wantErr string
	}{
		{"error", &fakeSource{name: "bad", err: errors.New("boom")}, "boom"},
		{"panic", &fakeSource{name: "bad", panic: "kaboom"}, "panic: kaboom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			srcs := []Source{
				&fakeSource{name: "a", items: cands("a", 2)},
				tt.bad,
				&fakeSource{name: "c", items: cands("c", 1)},
			}
			got := FetchAll(context.Background(), nil, srcs, 2, jsonLogger(&buf))
			if want := append(cands("a", 2), cands("c", 1)...); !reflect.DeepEqual(got, want) {
				t.Errorf("got %v, want %v", got, want)
			}
			byName := map[string]map[string]any{}
			for _, r := range logRecords(t, &buf) {
				byName[r["source"].(string)] = r
			}
			if len(byName) != 3 {
				t.Fatalf("want one log line per source, got %v", byName)
			}
			if r := byName["bad"]; r["level"] != "WARN" || r["msg"] != "source failed" || r["err"] != tt.wantErr {
				t.Errorf("failure log = %v", r)
			}
			for name, n := range map[string]float64{"a": 2, "c": 1} {
				r := byName[name]
				if r["level"] != "INFO" || r["msg"] != "source ok" || r["items"] != n {
					t.Errorf("%s log = %v", name, r)
				}
				if _, ok := r["took"].(float64); !ok {
					t.Errorf("%s log has no numeric took: %v", name, r)
				}
			}
		})
	}
}

func TestFetchAllConcurrency(t *testing.T) {
	for _, tt := range []struct{ concurrency, want int }{{3, 3}, {1, 1}, {0, 1}, {-1, 1}} {
		t.Run(fmt.Sprint(tt.concurrency), func(t *testing.T) {
			const n = 8
			var inflight, peak atomic.Int32
			started := make(chan struct{})
			gate := make(chan struct{})
			srcs := make([]Source, n)
			for i := range srcs {
				srcs[i] = &fakeSource{name: fmt.Sprint(i), items: cands(fmt.Sprint(i), 1),
					wait: gate, inflight: &inflight, peak: &peak, started: started}
			}
			out := make(chan []news.Candidate)
			go func() { out <- FetchAll(context.Background(), nil, srcs, tt.concurrency, nil) }()

			// Fill the window, then release one source per new start.
			for range tt.want {
				<-started
			}
			for i := 0; i < n; i++ {
				gate <- struct{}{}
				if i < n-tt.want {
					<-started
				}
			}
			if got := <-out; len(got) != n {
				t.Errorf("got %d candidates, want %d", len(got), n)
			}
			if p := int(peak.Load()); p != tt.want {
				t.Errorf("peak in flight = %d, want %d", p, tt.want)
			}
		})
	}
}

func TestFetchAllEmpty(t *testing.T) {
	if got := FetchAll(context.Background(), nil, nil, 4, nil); got == nil || len(got) != 0 {
		t.Errorf("no sources: got %#v, want empty non-nil", got)
	}
	srcs := []Source{&fakeSource{name: "x", err: errors.New("down")}}
	if got := FetchAll(context.Background(), nil, srcs, 4, nil); got == nil || len(got) != 0 {
		t.Errorf("all failing: got %#v, want empty non-nil", got)
	}
}

func TestFromConfig(t *testing.T) {
	now := time.Date(2026, 11, 26, 8, 40, 0, 0, time.UTC)
	feeds := []FeedConfig{{Name: "a", URL: "https://a.example/rss", Limit: 5}, {Name: "b", URL: "https://b.example/atom"}}

	tests := []struct {
		name      string
		cfg       Config
		wantNames []string
		notBefore time.Time
	}{
		{"feeds with age", Config{MaxAgeHours: 48, Feeds: feeds}, []string{"feed:a", "feed:b"}, now.Add(-48 * time.Hour)},
		{"feeds without age", Config{Feeds: feeds}, []string{"feed:a", "feed:b"}, time.Time{}},
		{"negative age", Config{MaxAgeHours: -1, Feeds: feeds[:1]}, []string{"feed:a"}, time.Time{}},
		{"empty", Config{}, nil, time.Time{}},
		{"all enabled", Config{
			MaxAgeHours: 48,
			HackerNews:  HNConfig{Enabled: true},
			Lobsters:    LobstersConfig{Enabled: true},
			Feeds:       feeds,
		}, []string{"hn", "lobsters", "feed:a", "feed:b"}, now.Add(-48 * time.Hour)},
		{"only lobsters", Config{Lobsters: LobstersConfig{Enabled: true}, HackerNews: HNConfig{APIBase: "x"}}, []string{"lobsters"}, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srcs := FromConfig(tt.cfg, now)
			var names []string
			for _, s := range srcs {
				names = append(names, s.Name())
				if f, ok := s.(*Feed); ok && !f.NotBefore.Equal(tt.notBefore) {
					t.Errorf("%s NotBefore = %v, want %v", s.Name(), f.NotBefore, tt.notBefore)
				}
			}
			if !reflect.DeepEqual(names, tt.wantNames) {
				t.Errorf("names = %v, want %v", names, tt.wantNames)
			}
		})
	}
	all := FromConfig(Config{
		HackerNews: HNConfig{Enabled: true, APIBase: "https://hn.test", MinPoints: 7, Limit: 9},
		Lobsters:   LobstersConfig{Enabled: true, URL: "https://lob.test/h.json", Limit: 3},
	}, now)
	if h := all[0].(*HackerNews); *h != (HackerNews{APIBase: "https://hn.test", MinPoints: 7, Limit: 9, Now: now}) {
		t.Errorf("hn = %+v", h)
	}
	if l := all[1].(*Lobsters); *l != (Lobsters{URL: "https://lob.test/h.json", Limit: 3}) {
		t.Errorf("lobsters = %+v", l)
	}
	f := FromConfig(Config{Feeds: feeds}, now)[0].(*Feed)
	if f.URL != feeds[0].URL || f.Limit != 5 || f.FeedName != "a" {
		t.Errorf("feed = %+v", f)
	}
}

func TestGet(t *testing.T) {
	var req *http.Request
	srv := serve(t, fixed("text/plain", "hello", &req))
	body, final, err := get(context.Background(), testClient(t), srv.URL+"/x", "text/plain")
	if err != nil || string(body) != "hello" || final.Path != "/x" || req.Header.Get("Accept") != "text/plain" {
		t.Errorf("get = %q, %v, %v (Accept %q)", body, final, err, req.Header.Get("Accept"))
	}

	big := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxBody+10))
	}))
	if body, _, err := get(context.Background(), testClient(t), big.URL, "*/*"); err != nil || len(body) != maxBody {
		t.Errorf("oversize body: len %d, err %v; want %d, nil", len(body), err, maxBody)
	}

	// A nil client falls back to the zero httpx.Client; the URL is invalid
	// so nothing is dialed.
	if _, _, err := get(context.Background(), nil, "://bad", "*/*"); err == nil || !strings.Contains(err.Error(), "GET") {
		t.Errorf("nil client, bad URL: err = %v", err)
	}
}
