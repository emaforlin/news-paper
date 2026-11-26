package rank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emaforlin/news-paper/internal/httpx"
	"github.com/emaforlin/news-paper/internal/news"
)

// fakeSeen implements Seen over a set of ids.
type fakeSeen map[string]bool

func (s fakeSeen) Has(id string) bool { return s[id] }

// cand builds a candidate with a title derived from its URL.
func cand(u, src string, score int) news.Candidate {
	return news.Candidate{URL: u, Title: "T " + u, Source: src, Score: score,
		Published: time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)}
}

func candURLs(cs []news.Candidate) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.URL)
	}
	return out
}

// testClient returns an httpx client that can only dial loopback addresses.
func testClient(t testing.TB) *httpx.Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
				return nil, fmt.Errorf("test tried to dial non-loopback %s", addr)
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &httpx.Client{HTTP: &http.Client{Transport: tr, Timeout: 5 * time.Second}}
}

// orRequest is what the mock OpenRouter saw.
type orRequest struct {
	method, path string
	header       http.Header
	body         map[string]any
	count        atomic.Int32
}

// mockOR serves one canned reply and records the requests.
func mockOR(t testing.TB, status int, body string) (*OpenRouter, *orRequest) {
	got := &orRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.count.Add(1)
		got.method, got.path, got.header = r.Method, r.URL.Path, r.Header
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &OpenRouter{HC: testClient(t), BaseURL: srv.URL + "/api/v1/", APIKey: "sk-test-123", Model: "m/x"}, got
}

// reply wraps content as a chat completion body.
func reply(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
	})
	return string(b)
}

// logCapture returns a JSON logger and a function that decodes what it wrote.
func logCapture(t testing.TB) (*slog.Logger, func() []map[string]any) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), func() []map[string]any {
		var out []map[string]any
		for dec := json.NewDecoder(bytes.NewReader(buf.Bytes())); dec.More(); {
			var m map[string]any
			if err := dec.Decode(&m); err != nil {
				t.Fatal(err)
			}
			out = append(out, m)
		}
		return out
	}
}

// fakeRanker implements Ranker and records the n it was asked for.
type fakeRanker struct {
	name  string
	picks []news.Pick
	err   error
	asked []int
}

func (f *fakeRanker) Name() string { return f.name }

func (f *fakeRanker) Rank(_ context.Context, _ []news.Candidate, n int) ([]news.Pick, error) {
	f.asked = append(f.asked, n)
	return f.picks, f.err
}
