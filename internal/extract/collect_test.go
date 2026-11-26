package extract

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emaforlin/news-paper/internal/news"
)

// numbered serves /0, /1, … with h(i, w, r) and returns one pick per path.
func numbered(t *testing.T, n int, h func(i int, w http.ResponseWriter, r *http.Request)) []news.Pick {
	t.Helper()
	srv := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil || i < 0 || i >= n {
			http.NotFound(w, r)
			return
		}
		h(i, w, r)
	}))
	picks := make([]news.Pick, n)
	for i := range picks {
		picks[i] = pick(fmt.Sprintf("%s/%d", srv.URL, i))
	}
	return picks
}

// articlePage is a valid page whose title identifies pick i.
func articlePage(i int) string {
	return page(pageSpec{words: 300, title: fmt.Sprintf("T%d", i)})
}

func titles(arts []news.Article) []string {
	var out []string
	for _, a := range arts {
		out = append(out, a.Title)
	}
	return out
}

func urls(picks []news.Pick) []string {
	var out []string
	for _, p := range picks {
		out = append(out, p.URL)
	}
	return out
}

func TestCollectOrder(t *testing.T) {
	const n = 4
	// Handler i answers only after handler i+1 has answered: they finish in
	// reverse ranking order.
	answered := make([]chan struct{}, n)
	for i := range answered {
		answered[i] = make(chan struct{})
	}
	picks := numbered(t, n, func(i int, w http.ResponseWriter, r *http.Request) {
		if i+1 < n {
			select {
			case <-answered[i+1]:
			case <-time.After(5 * time.Second):
				t.Errorf("handler %d: pick %d never answered", i, i+1)
			}
		}
		htmlHandler(articlePage(i))(w, r)
		w.(http.Flusher).Flush()
		close(answered[i])
	})
	e := newExtractor(t)
	e.Concurrency = n
	res := e.Collect(context.Background(), picks, 10)
	if got := strings.Join(titles(res.Articles), ","); got != "T0,T1,T2,T3" {
		t.Errorf("Articles = %s, want ranking order", got)
	}
}

func TestCollectCutoff(t *testing.T) {
	for _, conc := range []int{1, 4} {
		t.Run(fmt.Sprintf("concurrency %d", conc), func(t *testing.T) {
			var hits atomic.Int32
			picks := numbered(t, 8, func(i int, w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if i >= 3 {
					w.WriteHeader(404)
					return
				}
				htmlHandler(articlePage(i))(w, r)
			})
			e := newExtractor(t)
			e.Concurrency = conc
			res := e.Collect(context.Background(), picks, 3)
			if got := strings.Join(titles(res.Articles), ","); got != "T0,T1,T2" {
				t.Errorf("Articles = %s", got)
			}
			if len(res.Rejected) != 0 {
				t.Errorf("Rejected = %v, want none after the cutoff", urls(res.Rejected))
			}
			if conc == 1 && hits.Load() != 3 {
				t.Errorf("hits = %d, want exactly 3", hits.Load())
			}
		})
	}
}

func TestCollectMixed(t *testing.T) {
	// ok / 404 / 500 / ok / pdf / ok
	picks := numbered(t, 6, func(i int, w http.ResponseWriter, r *http.Request) {
		switch i {
		case 1:
			w.WriteHeader(404)
		case 2:
			w.WriteHeader(500)
		case 4:
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = io.WriteString(w, "%PDF-1.7")
		default:
			htmlHandler(articlePage(i))(w, r)
		}
	})
	e := newExtractor(t)
	e.Concurrency = 2
	res := e.Collect(context.Background(), picks, 10)
	if got := strings.Join(titles(res.Articles), ","); got != "T0,T3,T5" {
		t.Errorf("Articles = %s", got)
	}
	if got, want := strings.Join(urls(res.Rejected), ","), picks[1].URL+","+picks[4].URL; got != want {
		t.Errorf("Rejected = %s, want %s", got, want)
	}
}

func TestCollectCancel(t *testing.T) {
	var active atomic.Int32
	thirdStarted := make(chan struct{})
	picks := numbered(t, 4, func(i int, w http.ResponseWriter, r *http.Request) {
		if i == 0 {
			htmlHandler(articlePage(0))(w, r)
			return
		}
		active.Add(1)
		defer active.Add(-1)
		if i == 2 {
			close(thirdStarted) // pick 2 is dispatched only after pick 0 was consumed
		}
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-thirdStarted
		cancel()
	}()
	e := newExtractor(t)
	e.Concurrency = 2

	start := time.Now()
	res := e.Collect(ctx, picks, 10)
	if d := time.Since(start); d > time.Second {
		t.Errorf("Collect took %v after cancel", d)
	}
	if got := strings.Join(titles(res.Articles), ","); got != "T0" {
		t.Errorf("Articles = %s, want only T0", got)
	}
	if len(res.Rejected) != 0 {
		t.Errorf("Rejected = %v", urls(res.Rejected))
	}
	// Every in-flight request was aborted, so its handler returns.
	deadline := time.Now().Add(time.Second)
	for active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := active.Load(); n != 0 {
		t.Errorf("%d requests still active after Collect returned", n)
	}
}

func TestCollectWindow(t *testing.T) {
	tests := []struct {
		conc, picks int
		wantMax     int32
	}{
		{3, 6, 3},
		{0, 3, 1},
		{-1, 3, 1},
		{8, 2, 2},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("concurrency %d", tt.conc), func(t *testing.T) {
			var inflight, peak atomic.Int32
			picks := numbered(t, tt.picks, func(i int, w http.ResponseWriter, r *http.Request) {
				cur := inflight.Add(1)
				defer inflight.Add(-1)
				for p := peak.Load(); cur > p && !peak.CompareAndSwap(p, cur); p = peak.Load() {
				}
				// Hold the request until the window is full (or briefly, if it
				// never fills) so overlapping requests are observable.
				deadline := time.Now().Add(100 * time.Millisecond)
				for inflight.Load() < tt.wantMax && time.Now().Before(deadline) {
					time.Sleep(2 * time.Millisecond)
				}
				htmlHandler(articlePage(i))(w, r)
			})
			e := newExtractor(t)
			e.Concurrency = tt.conc
			res := e.Collect(context.Background(), picks, 10)
			if len(res.Articles) != tt.picks {
				t.Errorf("articles = %d, want %d", len(res.Articles), tt.picks)
			}
			if got := peak.Load(); got != tt.wantMax {
				t.Errorf("peak concurrency = %d, want %d", got, tt.wantMax)
			}
		})
	}
}

func TestCollectEmpty(t *testing.T) {
	var hits atomic.Int32
	picks := numbered(t, 2, func(i int, w http.ResponseWriter, r *http.Request) { hits.Add(1) })
	e := newExtractor(t)
	for _, tt := range []struct {
		picks []news.Pick
		want  int
	}{{picks, 0}, {picks, -1}, {nil, 5}} {
		res := e.Collect(context.Background(), tt.picks, tt.want)
		if res.Articles != nil || res.Rejected != nil {
			t.Errorf("Collect(%d picks, want %d) = %+v", len(tt.picks), tt.want, res)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("hits = %d, want 0", hits.Load())
	}
}

func TestCollectLogs(t *testing.T) {
	picks := numbered(t, 3, func(i int, w http.ResponseWriter, r *http.Request) {
		switch i {
		case 0:
			htmlHandler(articlePage(0))(w, r)
		case 1:
			w.WriteHeader(410)
		default:
			w.WriteHeader(502)
		}
	})
	var buf bytes.Buffer
	e := newExtractor(t)
	e.Log = jsonLogger(&buf)
	e.Collect(context.Background(), picks, 10)

	recs := logRecords(t, &buf)
	want := []struct {
		msg, level string
		n          float64
		keys       []string
	}{
		{"extracted", "INFO", 1, []string{"domain", "words", "title"}},
		{"skipped", "INFO", 2, []string{"url", "reason"}},
		{"extract failed", "WARN", 3, []string{"url", "err"}},
	}
	if len(recs) != len(want) {
		t.Fatalf("got %d log records, want %d:\n%s", len(recs), len(want), buf.String())
	}
	for i, w := range want {
		r := recs[i]
		if r["msg"] != w.msg || r["level"] != w.level || r["n"] != w.n {
			t.Errorf("record %d = %v, want %s/%s n=%v", i, r, w.msg, w.level, w.n)
		}
		for _, k := range w.keys {
			if _, ok := r[k]; !ok {
				t.Errorf("record %q lacks key %q", w.msg, k)
			}
		}
	}
	if recs[0]["domain"] != "127.0.0.1" || recs[0]["title"] != "T0" || recs[1]["reason"] != "HTTP 410" {
		t.Errorf("unexpected values: %v / %v", recs[0], recs[1])
	}
}
