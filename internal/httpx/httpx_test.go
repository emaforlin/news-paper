package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// step is one scripted server reply. drop closes the connection without
// answering, which the client sees as a network error.
type step struct {
	status int
	body   string
	drop   bool
}

// scripted serves steps in order (the last one repeats) and counts hits.
func scripted(t *testing.T, steps ...step) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(hits.Add(1)) - 1
		s := steps[min(i, len(steps)-1)]
		if s.drop {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			conn.Close()
			return
		}
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(s.body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestGetOK(t *testing.T) {
	var gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotAccept = r.UserAgent(), r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	c := New(time.Second, "test-agent")
	resp, err := c.Get(context.Background(), srv.URL+"/p", http.Header{"Accept": {"text/html"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || string(resp.Body) != "hello" || resp.Truncated {
		t.Errorf("resp = %d %q truncated=%v", resp.StatusCode, resp.Body, resp.Truncated)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html" {
		t.Errorf("Content-Type = %q", ct)
	}
	if resp.URL.String() != srv.URL+"/p" {
		t.Errorf("URL = %s", resp.URL)
	}
	if gotUA != "test-agent" || gotAccept != "text/html" {
		t.Errorf("server saw UA=%q Accept=%q", gotUA, gotAccept)
	}
}

func TestGetHeaderUserAgentWins(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.UserAgent()
	}))
	defer srv.Close()

	_, err := (&Client{}).Get(context.Background(), srv.URL, http.Header{"User-Agent": {"custom"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if gotUA != "custom" {
		t.Errorf("UA = %q, want the caller's", gotUA)
	}
}

func TestGetRetry(t *testing.T) {
	tests := []struct {
		name     string
		steps    []step
		wantErr  bool
		wantCode int
		wantBody string
		wantHits int32
	}{
		{"503 then 200", []step{{status: 503}, {status: 200, body: "ok"}}, false, 200, "ok", 2},
		{"429 then 200", []step{{status: 429}, {status: 200, body: "ok"}}, false, 200, "ok", 2},
		{"503 twice", []step{{status: 503}}, false, 503, "", 2},
		{"500 twice", []step{{status: 500, body: "oops"}}, false, 500, "", 2},
		{"404 no retry", []step{{status: 404, body: "nope"}}, false, 404, "", 1},
		{"304 no retry", []step{{status: 304}}, false, 304, "", 1},
		{"drop then 200", []step{{drop: true}, {status: 200, body: "ok"}}, false, 200, "ok", 2},
		{"drop twice", []step{{drop: true}}, true, 0, "", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, hits := scripted(t, tt.steps...)
			c := &Client{Backoff: time.Millisecond}
			resp, err := c.Get(context.Background(), srv.URL, nil, 0)
			if got := hits.Load(); got != tt.wantHits {
				t.Errorf("hits = %d, want %d", got, tt.wantHits)
			}
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %d", resp.StatusCode)
				}
				if !strings.Contains(err.Error(), "GET "+srv.URL) {
					t.Errorf("error lacks context: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tt.wantCode || string(resp.Body) != tt.wantBody {
				t.Errorf("resp = %d %q, want %d %q", resp.StatusCode, resp.Body, tt.wantCode, tt.wantBody)
			}
			if resp.StatusCode/100 != 2 && resp.Body != nil {
				t.Errorf("non-2xx body = %q, want nil", resp.Body)
			}
		})
	}
}

func TestGetTimeoutRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-r.Context().Done() // never answer; the client gives up
	}))
	defer srv.Close()

	c := &Client{HTTP: &http.Client{Timeout: 50 * time.Millisecond}}
	_, err := c.Get(context.Background(), srv.URL, nil, 0)
	if err == nil {
		t.Fatal("want timeout error")
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("hits = %d, want 2", got)
	}
}

func TestGetCancel(t *testing.T) {
	t.Run("during backoff", func(t *testing.T) {
		srv, hits := scripted(t, step{status: 503})
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(20*time.Millisecond, cancel)

		start := time.Now()
		_, err := (&Client{Backoff: time.Hour}).Get(ctx, srv.URL, nil, 0)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("took %v, want prompt return", d)
		}
		if got := hits.Load(); got != 1 {
			t.Errorf("hits = %d, want 1", got)
		}
	})
	t.Run("during request", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			<-r.Context().Done()
		}))
		defer srv.Close()
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(20*time.Millisecond, cancel)

		_, err := (&Client{}).Get(ctx, srv.URL, nil, 0)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if got := hits.Load(); got != 1 {
			t.Errorf("hits = %d, want 1 (no retry after cancel)", got)
		}
	})
	t.Run("already cancelled, zero backoff", func(t *testing.T) {
		srv, _ := scripted(t, step{status: 503})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := (&Client{}).Get(ctx, srv.URL, nil, 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}

func TestGetBodyCap(t *testing.T) {
	tests := []struct {
		name      string
		size      int
		max       int64
		wantLen   int
		truncated bool
	}{
		{"over cap", 11, 10, 10, true},
		{"at cap", 10, 10, 10, false},
		{"under cap", 3, 10, 3, false},
		{"default cap", DefaultMaxBody + 5, 0, DefaultMaxBody, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := scripted(t, step{status: 200, body: strings.Repeat("x", tt.size)})
			resp, err := (&Client{}).Get(context.Background(), srv.URL, nil, tt.max)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Body) != tt.wantLen || resp.Truncated != tt.truncated {
				t.Errorf("len=%d truncated=%v, want %d %v", len(resp.Body), resp.Truncated, tt.wantLen, tt.truncated)
			}
		})
	}
}

func TestGetBodyReadErrorRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			// Promise 100 bytes, send 5, then kill the connection.
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(200)
			_, _ = w.Write([]byte("short"))
			w.(http.Flusher).Flush()
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		_, _ = w.Write([]byte("full"))
	}))
	defer srv.Close()

	resp, err := (&Client{}).Get(context.Background(), srv.URL, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Body) != "full" || hits.Load() != 2 {
		t.Errorf("body=%q hits=%d, want \"full\" after 2 hits", resp.Body, hits.Load())
	}
}

func TestGetRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/a", http.RedirectHandler("/b", http.StatusFound))
	mux.Handle("/b", http.RedirectHandler("/c/post", http.StatusMovedPermanently))
	mux.HandleFunc("/c/post", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("end")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := (&Client{}).Get(context.Background(), srv.URL+"/a", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.URL.Path != "/c/post" || string(resp.Body) != "end" {
		t.Errorf("URL=%s body=%q", resp.URL, resp.Body)
	}
}

func TestGetInvalidURL(t *testing.T) {
	for _, u := range []string{"://bad", "http://[::1", "ftp\x7f://x"} {
		if _, err := (&Client{}).Get(context.Background(), u, nil, 0); err == nil {
			t.Errorf("Get(%q): want error", u)
		}
	}
}

func TestZeroValueClient(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.UserAgent()
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	var c Client
	resp, err := c.Get(context.Background(), srv.URL, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Body) != "ok" || gotUA != DefaultUserAgent {
		t.Errorf("body=%q UA=%q", resp.Body, gotUA)
	}
}

func TestNewDefaults(t *testing.T) {
	c := New(0, "")
	if c.HTTP.Timeout != DefaultTimeout || c.Backoff != DefaultBackoff || c.userAgent() != DefaultUserAgent {
		t.Errorf("New(0, \"\") = timeout %v backoff %v ua %q", c.HTTP.Timeout, c.Backoff, c.userAgent())
	}
}

func TestSleep(t *testing.T) {
	if err := sleep(context.Background(), time.Millisecond); err != nil {
		t.Errorf("sleep = %v", err)
	}
	if err := sleep(context.Background(), 0); err != nil {
		t.Errorf("sleep(0) = %v", err)
	}
}
