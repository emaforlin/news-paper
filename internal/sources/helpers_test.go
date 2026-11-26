package sources

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
)

// testClient returns an httpx client that can only dial loopback addresses,
// so a test can never reach the real network by mistake.
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

// fixed serves body with the given content type and records the request.
func fixed(contentType, body string, got **http.Request) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			*got = r
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(body))
	}
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

func mustTime(t testing.TB, layout, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(layout, s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}
