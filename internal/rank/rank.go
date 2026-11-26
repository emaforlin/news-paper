// Package rank turns the day's candidates into an ordered list of picks:
// Prefilter (dedupe, seen, deny list, non-HTML), then a Ranker.
package rank

import (
	"context"
	"log/slog"
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

// WithFallback ranks with primary, caps the picks per domain (maxPerDomain <=
// 0 means no cap) and tops up to n from secondary. It never fails: the result
// is short only when the pool is. Either ranker may be nil.
func WithFallback(ctx context.Context, log *slog.Logger, primary, secondary Ranker,
	cands []news.Candidate, n, maxPerDomain int) []news.Pick {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	out := []news.Pick{}
	if n <= 0 {
		return out
	}
	c := newCapper(maxPerDomain)
	// admit appends picks through the cap until there are n.
	admit := func(picks []news.Pick) (added int) {
		for _, p := range picks {
			if len(out) == n {
				break
			}
			if c.admit(p) {
				out = append(out, p)
				added++
			}
		}
		return added
	}

	if primary != nil {
		picks, err := primary.Rank(ctx, cands, n)
		if err != nil {
			log.Warn("ranker failed, using fallback", "ranker", primary.Name(), "err", err)
		} else {
			log.Info("ranked", "ranker", primary.Name(), "picks", len(picks))
			admit(picks)
		}
	}
	if len(out) < n && secondary != nil {
		more, err := secondary.Rank(ctx, cands, len(cands))
		if err != nil {
			log.Warn("fallback ranker failed", "ranker", secondary.Name(), "err", err)
		} else if added := admit(more); added > 0 {
			log.Info("topped up", "ranker", secondary.Name(), "added", added)
		}
	}
	return out
}

// capper rejects repeated ids and picks over the per-domain limit.
type capper struct {
	max     int // <= 0: no limit
	ids     map[string]bool
	domains map[string]int
}

func newCapper(maxPerDomain int) *capper {
	return &capper{max: maxPerDomain, ids: map[string]bool{}, domains: map[string]int{}}
}

func (c *capper) admit(p news.Pick) bool {
	id, d := p.ID(), p.Domain()
	if c.ids[id] || (c.max > 0 && c.domains[d] >= c.max) {
		return false
	}
	c.ids[id] = true
	c.domains[d]++
	return true
}

// collapse joins whitespace runs into single spaces and trims the ends.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
