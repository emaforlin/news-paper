// Package rank turns the day's candidates into an ordered list of picks:
// Prefilter (dedupe, seen, deny list, non-HTML), then a Ranker.
package rank

import (
	"context"
	"strings"

	"github.com/emaforlin/news-paper/internal/news"
)

// Seen reports whether a URL id was already sent or rejected. state.State
// implements it.
type Seen interface{ Has(id string) bool }

// Ranker orders candidates and returns at most n picks, best first.
type Ranker interface {
	Name() string
	Rank(ctx context.Context, cands []news.Candidate, n int) ([]news.Pick, error)
}

// collapse joins whitespace runs into single spaces and trims the ends.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
