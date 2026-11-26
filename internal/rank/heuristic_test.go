package rank

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/emaforlin/news-paper/internal/news"
)

var hNow = time.Date(2026, 11, 26, 8, 40, 0, 0, time.UTC)

// fsExample is the FS §4 table, shuffled so that Rank has to reorder it.
func fsExample() ([]news.Candidate, []float64) {
	cs := []news.Candidate{
		{URL: "https://d.com/1", Title: "My blog post", Source: "feed:dan", Published: hNow.Add(-48 * time.Hour)},
		{URL: "https://c.com/1", Title: "Crypto prices", Source: "hn", Score: 900, Published: hNow},
		{URL: "https://b.com/1", Title: "Good news about Google", Source: "lobsters", Score: 40},
		{URL: "https://a.com/1", Title: "Postgres internals in Go", Source: "hn", Score: 300, Published: hNow.Add(-12 * time.Hour)},
	}
	return cs, []float64{2.00, 2.80, 3.71, 13.21}
}

func fsHeuristic() *Heuristic {
	return &Heuristic{Keywords: []string{"go", "postgres"}, Avoid: []string{"crypto"}, Now: hNow}
}

func TestHeuristicExample(t *testing.T) {
	h := fsHeuristic()
	cs, want := fsExample()
	kw, av := normTerms(h.Keywords), normTerms(h.Avoid)
	for i, c := range cs {
		if got, _ := h.score(c, kw, av); math.Abs(got-want[i]) > 0.01 {
			t.Errorf("score(%q) = %.4f, want %.2f", c.Title, got, want[i])
		}
	}

	picks, err := h.Rank(context.Background(), cs, 10)
	if err != nil {
		t.Fatal(err)
	}
	var titles, reasons []string
	for _, p := range picks {
		titles = append(titles, p.Title)
		reasons = append(reasons, p.Reason)
	}
	wantTitles := []string{"Postgres internals in Go", "Good news about Google", "Crypto prices", "My blog post"}
	wantReasons := []string{"Coincide con: go, postgres", "Popular en lobsters", "Popular en hn", "Popular en feed:dan"}
	if !reflect.DeepEqual(titles, wantTitles) {
		t.Errorf("order = %q\n want %q", titles, wantTitles)
	}
	if !reflect.DeepEqual(reasons, wantReasons) {
		t.Errorf("reasons = %q\n   want %q", reasons, wantReasons)
	}
	if !reflect.DeepEqual(picks[0].Candidate, cs[3]) {
		t.Errorf("pick does not carry the candidate: %+v", picks[0].Candidate)
	}
}

func TestMatchTerm(t *testing.T) {
	tests := []struct {
		text, term string
		want       bool
	}{
		{"postgres internals in go", "go", true},
		{"go.", "go", true},
		{"(go)", "go", true},
		{"go", "go", true},
		{"good go", "go", true},
		{"go-to guide", "go", true},
		{"google", "go", false},
		{"good news", "go", false},
		{"golang", "go", false},
		{"go2 released", "go", false},
		{"ergo", "go", false},
		{"learning c++ today", "c++", true},
		{"c++20", "c++", false},
		{"node.js 22", "node.js", true},
		{"trust me", "rust", false},
		{"rust's borrow checker", "rust", true},
		{"intro to machine learning", "machine learning", true},
		{"añu", "ñu", false},
		{"el ñu corre", "ñu", true},
		{"", "go", false},
	}
	for _, tt := range tests {
		if got := matchTerm(tt.text, tt.term); got != tt.want {
			t.Errorf("matchTerm(%q, %q) = %v, want %v", tt.text, tt.term, got, tt.want)
		}
	}
}

func TestHeuristicTerms(t *testing.T) {
	base := news.Candidate{URL: "https://x.com/1", Title: "Machine\n learning  in Go", Source: "hn", Score: 0}
	tests := []struct {
		name string
		h    Heuristic
		c    func(news.Candidate) news.Candidate
		want float64
		hits []string
	}{
		{"duplicate and blank keywords count once", Heuristic{Keywords: []string{"Go", "go", " ", ""}}, nil, 3, []string{"go"}},
		{"phrase across collapsed whitespace", Heuristic{Keywords: []string{"MACHINE   learning"}}, nil, 3, []string{"machine learning"}},
		{"avoid per term", Heuristic{Avoid: []string{"go", "learning"}}, nil, -12, nil},
		{"keyword in tags only", Heuristic{Keywords: []string{"databases"}},
			func(c news.Candidate) news.Candidate { c.Tags = []string{"Databases"}; return c }, 3, []string{"databases"}},
		{"keyword in summary", Heuristic{Keywords: []string{"sqlite"}},
			func(c news.Candidate) news.Candidate { c.Summary = "about SQLite"; return c }, 3, []string{"sqlite"}},
		{"zero Now: no recency", Heuristic{},
			func(c news.Candidate) news.Candidate { c.Published = hNow; return c }, 0, nil},
		{"unknown date: no recency", Heuristic{Now: hNow}, nil, 0, nil},
		{"future date capped at 2", Heuristic{Now: hNow},
			func(c news.Candidate) news.Candidate { c.Published = hNow.Add(5 * time.Hour); return c }, 2, nil},
		{"old date: no bonus", Heuristic{Now: hNow},
			func(c news.Candidate) news.Candidate { c.Published = hNow.Add(-72 * time.Hour); return c }, 0, nil},
		{"negative score as 0", Heuristic{},
			func(c news.Candidate) news.Candidate { c.Score = -5; return c }, 0, nil},
		{"feed bonus", Heuristic{},
			func(c news.Candidate) news.Candidate { c.Source = "feed:x"; return c }, 2, nil},
		{"merged source, feed last", Heuristic{},
			func(c news.Candidate) news.Candidate { c.Source = "hn+feed:x"; return c }, 2, nil},
		{"merged source, feed first", Heuristic{},
			func(c news.Candidate) news.Candidate { c.Source = "feed:x+lobsters"; return c }, 2, nil},
		{"merged source without feed", Heuristic{},
			func(c news.Candidate) news.Candidate { c.Source = "hn+lobsters"; return c }, 0, nil},
		{"feed bonus counted once", Heuristic{},
			func(c news.Candidate) news.Candidate { c.Source = "feed:a+feed:b"; return c }, 2, nil},
		{"feed: only as a prefix", Heuristic{},
			func(c news.Candidate) news.Candidate { c.Source = "myfeed:x"; return c }, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base
			if tt.c != nil {
				c = tt.c(c)
			}
			got, hits := tt.h.score(c, normTerms(tt.h.Keywords), normTerms(tt.h.Avoid))
			if math.Abs(got-tt.want) > 0.01 {
				t.Errorf("score = %.4f, want %.2f", got, tt.want)
			}
			if !reflect.DeepEqual(hits, tt.hits) {
				t.Errorf("hits = %q, want %q", hits, tt.hits)
			}
		})
	}
}

func TestHeuristicBounds(t *testing.T) {
	h := &Heuristic{}
	tie := []news.Candidate{cand("https://a.com/1", "hn", 10), cand("https://b.com/1", "hn", 10), cand("https://c.com/1", "hn", 10)}
	tests := []struct {
		name  string
		cands []news.Candidate
		n     int
		want  []string
	}{
		{"ties keep input order", tie, 3, candURLs(tie)},
		{"n above len", tie, 10, candURLs(tie)},
		{"n below len", tie, 2, candURLs(tie[:2])},
		{"n zero", tie, 0, []string{}},
		{"n negative", tie, -1, []string{}},
		{"empty pool", nil, 5, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			picks, err := h.Rank(context.Background(), tt.cands, tt.n)
			if err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for _, p := range picks {
				got = append(got, p.URL)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
	if h.Name() != "heuristic" {
		t.Errorf("Name = %q", h.Name())
	}
	var _ Ranker = h
}
