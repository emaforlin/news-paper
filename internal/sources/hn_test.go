package sources

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/emaforlin/news-paper/internal/news"
)

var hnNow = time.Date(2026, 11, 26, 8, 40, 0, 0, time.UTC)

func TestHNQuery(t *testing.T) {
	tests := []struct {
		name      string
		h         HackerNews
		wantQuery string
	}{
		{"defaults", HackerNews{}, "hitsPerPage=60&numericFilters=created_at_i%3E1795596000%2Cpoints%3E%3D100&tags=story"},
		{"custom", HackerNews{MinPoints: 250, Limit: 5}, "hitsPerPage=5&numericFilters=created_at_i%3E1795596000%2Cpoints%3E%3D250&tags=story"},
		{"no minimum", HackerNews{MinPoints: NoMinPoints}, "hitsPerPage=60&numericFilters=created_at_i%3E1795596000%2Cpoints%3E%3D0&tags=story"},
		{"any negative", HackerNews{MinPoints: -50}, "hitsPerPage=60&numericFilters=created_at_i%3E1795596000%2Cpoints%3E%3D0&tags=story"},
		{"trailing slash", HackerNews{APIBase: "/"}, "hitsPerPage=60&numericFilters=created_at_i%3E1795596000%2Cpoints%3E%3D100&tags=story"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *http.Request
			srv := serve(t, fixed("application/json", `{"hits":[]}`, &req))
			h := tt.h
			h.APIBase = srv.URL + h.APIBase
			h.Now = hnNow
			got, err := h.Fetch(context.Background(), testClient(t))
			if err != nil || len(got) != 0 {
				t.Fatalf("got %v, %v", got, err)
			}
			if req.URL.Path != "/search" {
				t.Errorf("path = %q, want /search", req.URL.Path)
			}
			if req.URL.RawQuery != tt.wantQuery {
				t.Errorf("query = %q\n     want %q", req.URL.RawQuery, tt.wantQuery)
			}
			if a := req.Header.Get("Accept"); a != "application/json" {
				t.Errorf("Accept = %q", a)
			}
		})
	}
	if got := (&HackerNews{Now: hnNow}).searchURL(); !strings.HasPrefix(got, DefaultHNAPIBase+"/search?") {
		t.Errorf("default base: %s", got)
	}
}

const hnPayload = `{"hits":[
 {"objectID":"4201","title":" Show HN:  A thing ","url":"https://thing.example/x?utm_source=hn#top",
  "points":321,"story_text":null,"created_at_i":1795596000,"num_comments":10},
 {"objectID":"4202","title":"Ask HN: Why?","url":null,"points":150,
  "story_text":"<p>Long <i>question</i></p><p>here</p>","created_at_i":1795600000},
 {"objectID":"","title":"Broken","url":"","points":500,"created_at_i":1795600000},
 {"objectID":"4204","title":"Third","url":"https://third.example/","points":101,"created_at_i":1795610000}
]}`

func TestHNMapping(t *testing.T) {
	srv := serve(t, fixed("application/json", hnPayload, nil))
	got, err := (&HackerNews{APIBase: srv.URL, Now: hnNow}).Fetch(context.Background(), testClient(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []news.Candidate{
		{URL: "https://thing.example/x", Title: "Show HN: A thing", Source: "hn", Score: 321,
			Discussion: "https://news.ycombinator.com/item?id=4201", Published: time.Unix(1795596000, 0).UTC()},
		{URL: "https://news.ycombinator.com/item?id=4202", Title: "Ask HN: Why?", Source: "hn", Score: 150,
			Summary: "Long question here", Discussion: "https://news.ycombinator.com/item?id=4202",
			Published: time.Unix(1795600000, 0).UTC()},
		{URL: "https://third.example/", Title: "Third", Source: "hn", Score: 101,
			Discussion: "https://news.ycombinator.com/item?id=4204", Published: time.Unix(1795610000, 0).UTC()},
	}
	assertCandidates(t, got, want)
	for _, c := range got {
		if c.Published.Location() != time.UTC {
			t.Errorf("Published %v is not UTC", c.Published)
		}
	}

	limited, err := (&HackerNews{APIBase: srv.URL, Now: hnNow, Limit: 2}).Fetch(context.Background(), testClient(t))
	if err != nil || len(limited) != 2 {
		t.Errorf("Limit 2: got %d, %v", len(limited), err)
	}
}

func TestHNErrors(t *testing.T) {
	bad := serve(t, fixed("application/json", `{"hits":[`, nil))
	_, err := (&HackerNews{APIBase: bad.URL, Now: hnNow}).Fetch(context.Background(), testClient(t))
	if err == nil || !strings.HasPrefix(err.Error(), "decode: ") {
		t.Errorf("invalid JSON: err = %v", err)
	}
	down := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	_, err = (&HackerNews{APIBase: down.URL, Now: hnNow}).Fetch(context.Background(), testClient(t))
	if err == nil || err.Error() != "status 500" {
		t.Errorf("500: err = %v", err)
	}
}
