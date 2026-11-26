package rank

import (
	"time"

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
