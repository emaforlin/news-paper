package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emaforlin/news-paper/internal/httpx"
	"github.com/emaforlin/news-paper/internal/news"
)

// testClient returns an httpx client that can only dial loopback addresses,
// so a test can never reach the real network by mistake.
func testClient(t testing.TB) *httpx.Client {
	return testClientTimeout(t, 5*time.Second)
}

func testClientTimeout(t testing.TB, timeout time.Duration) *httpx.Client {
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
	return &httpx.Client{HTTP: &http.Client{Transport: tr, Timeout: timeout}}
}

func TestClientIsLoopbackOnly(t *testing.T) {
	_, err := testClient(t).Get(context.Background(), "http://example.com/", nil, 0)
	if err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("err = %v, want non-loopback refusal", err)
	}
}

// serve starts a test server and closes it with the test.
func serve(t testing.TB, h http.Handler) *httptest.Server {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// htmlHandler serves body as text/html.
func htmlHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}
}

func pick(u string) news.Pick {
	return news.Pick{
		Candidate: news.Candidate{URL: u, Title: "Pick title", Source: "test", Tags: []string{"t"}},
		Reason:    "because",
	}
}

func newExtractor(t testing.TB) *Extractor {
	return &Extractor{HC: testClient(t), MinWords: DefaultMinWords, MaxBody: DefaultMaxBody}
}

// pageSpec describes a generated article page.
type pageSpec struct {
	words  int    // body words, in paragraphs of up to 40
	title  string // <title>; empty means none
	lang   string
	head   string // extra markup in <head>
	before string // markup before the first paragraph (inside the article)
	after  string // markup after the last paragraph (inside the article)
	tail   string // markup after </article>
}

var vocabulary = strings.Fields(`river stone garden window market letter engine harbor silver
	thunder lantern meadow pocket candle forest mirror ladder planet violin basket`)

// page renders spec as an HTML document whose article body has exactly
// spec.words words.
func page(spec pageSpec) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html")
	if spec.lang != "" {
		b.WriteString(` lang="` + spec.lang + `"`)
	}
	b.WriteString("><head>")
	if spec.title != "" {
		b.WriteString("<title>" + spec.title + "</title>")
	}
	b.WriteString(spec.head + "</head><body><nav>Home About</nav><article>" + spec.before)
	for i := 0; i < spec.words; {
		n := min(40, spec.words-i)
		ws := make([]string, n)
		for j := range ws {
			ws[j] = vocabulary[(i+j)%len(vocabulary)]
			if j%8 == 7 && j != n-1 {
				ws[j] += ","
			}
		}
		b.WriteString("<p>" + strings.Join(ws, " ") + ".</p>\n")
		i += n
	}
	b.WriteString(spec.after + "</article>" + spec.tail + "</body></html>")
	return b.String()
}

// logRecords decodes the JSON lines a slog.JSONHandler wrote to buf.
func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var recs []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		recs = append(recs, m)
	}
	return recs
}

func jsonLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
