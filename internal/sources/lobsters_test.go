package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emaforlin/news-paper/internal/news"
)

const lobstersPayload = `[
 {"short_id":"a1","short_id_url":"https://lobste.rs/s/a1","created_at":"2026-11-25T14:03:11.000-06:00",
  "title":"Link  post","url":"https://link.example/p?utm_medium=x","score":42,
  "description":"<p>ignored</p>","description_plain":"Plain   wins",
  "comments_url":"https://lobste.rs/s/a1/link_post","tags":["go","web"]},
 {"short_id":"b2","short_id_url":"https://lobste.rs/s/b2","created_at":"garbage",
  "title":"Text post","url":"","score":7,"description":"<p>Only <em>HTML</em></p>",
  "comments_url":"https://lobste.rs/s/b2/text_post","tags":[]},
 {"short_id":"c3","short_id_url":"https://lobste.rs/s/c3","created_at":"2026-11-25T10:00:00Z",
  "title":"No comments url","url":"","score":1,"tags":null},
 {"short_id":"d4","title":"Nothing at all","url":"","score":1}
]`

func TestLobsters(t *testing.T) {
	var req *http.Request
	srv := serve(t, fixed("application/json", lobstersPayload, &req))
	got, err := (&Lobsters{URL: srv.URL}).Fetch(context.Background(), testClient(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []news.Candidate{
		{URL: "https://link.example/p", Title: "Link post", Source: "lobsters", Summary: "Plain wins", Score: 42,
			Discussion: "https://lobste.rs/s/a1/link_post", Tags: []string{"go", "web"},
			Published: time.Date(2026, 11, 25, 20, 3, 11, 0, time.UTC)},
		{URL: "https://lobste.rs/s/b2/text_post", Title: "Text post", Source: "lobsters", Summary: "Only HTML", Score: 7,
			Discussion: "https://lobste.rs/s/b2/text_post", Tags: []string{}},
		{URL: "https://lobste.rs/s/c3", Title: "No comments url", Source: "lobsters", Score: 1,
			Discussion: "https://lobste.rs/s/c3", Published: time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)},
	}
	assertCandidates(t, got, want)
	if a := req.Header.Get("Accept"); a != "application/json" {
		t.Errorf("Accept = %q", a)
	}
}

func TestLobstersLimit(t *testing.T) {
	items := make([]lobstersItem, 30)
	for i := range items {
		items[i] = lobstersItem{Title: fmt.Sprint(i), URL: fmt.Sprintf("https://x.example/%d", i)}
	}
	payload, _ := json.Marshal(items)
	srv := serve(t, fixed("application/json", string(payload), nil))

	for _, tt := range []struct{ limit, want int }{{0, DefaultLobstersLimit}, {2, 2}, {100, 30}} {
		got, err := (&Lobsters{URL: srv.URL, Limit: tt.limit}).Fetch(context.Background(), testClient(t))
		if err != nil || len(got) != tt.want {
			t.Fatalf("Limit %d: got %d, %v; want %d", tt.limit, len(got), err, tt.want)
		}
		var ts []string
		for i := range tt.want {
			ts = append(ts, fmt.Sprint(i))
		}
		if !reflect.DeepEqual(titles(got), ts) {
			t.Errorf("Limit %d: order %v", tt.limit, titles(got))
		}
	}
}

func TestLobstersErrors(t *testing.T) {
	bad := serve(t, fixed("application/json", `{"not":"a list"}`, nil))
	_, err := (&Lobsters{URL: bad.URL}).Fetch(context.Background(), testClient(t))
	if err == nil || !strings.HasPrefix(err.Error(), "decode: ") {
		t.Errorf("invalid JSON: err = %v", err)
	}
	missing := serve(t, http.NotFoundHandler())
	_, err = (&Lobsters{URL: missing.URL}).Fetch(context.Background(), testClient(t))
	if err == nil || err.Error() != "status 404" {
		t.Errorf("404: err = %v", err)
	}
	// Default URL with an already-cancelled context: nothing is dialed.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (&Lobsters{}).Fetch(ctx, testClient(t))
	if err == nil || !strings.Contains(err.Error(), DefaultLobstersURL) {
		t.Errorf("default URL: err = %v", err)
	}
}
