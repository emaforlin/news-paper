package rank

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/emaforlin/news-paper/internal/news"
)

// Heuristic scores candidates by popularity, profile keywords, recency and
// source type. It never fails and makes no requests.
type Heuristic struct {
	Keywords []string  // +3 each when matched
	Avoid    []string  // -6 each when matched
	Now      time.Time // anchors the recency bonus; zero → no recency term
}

func (h *Heuristic) Name() string { return "heuristic" }

// Rank returns the min(n, len(cands)) best candidates, ties in input order.
func (h *Heuristic) Rank(_ context.Context, cands []news.Candidate, n int) ([]news.Pick, error) {
	n = min(max(0, n), len(cands))
	keywords, avoid := normTerms(h.Keywords), normTerms(h.Avoid)

	scores := make([]float64, len(cands))
	matched := make([][]string, len(cands))
	order := make([]int, len(cands))
	for i, c := range cands {
		scores[i], matched[i] = h.score(c, keywords, avoid)
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })

	picks := make([]news.Pick, 0, n)
	for _, i := range order[:n] {
		reason := "Popular en " + cands[i].Source
		if len(matched[i]) > 0 {
			reason = "Coincide con: " + strings.Join(matched[i], ", ")
		}
		picks = append(picks, news.Pick{Candidate: cands[i], Reason: reason})
	}
	return picks, nil
}

// score computes the FS §4 formula over normalised terms and returns the
// keywords that matched, in keyword order.
func (h *Heuristic) score(c news.Candidate, keywords, avoid []string) (float64, []string) {
	text := collapse(strings.ToLower(c.Title + " " + c.Summary + " " + strings.Join(c.Tags, " ")))
	s := math.Log1p(float64(max(0, c.Score)))

	var hits []string
	for _, k := range keywords {
		if matchTerm(text, k) {
			s += 3
			hits = append(hits, k)
		}
	}
	for _, a := range avoid {
		if matchTerm(text, a) {
			s -= 6
		}
	}
	if !h.Now.IsZero() && !c.Published.IsZero() {
		age := max(0, h.Now.Sub(c.Published).Hours()) // a future date counts as fresh
		s += max(0, 2-age/24)
	}
	if fromFeed(c.Source) {
		s += 2
	}
	return s, hits
}

// fromFeed reports whether any token of a (possibly merged, "+"-joined)
// source is a feed, so "hn+feed:x" and "feed:x+hn" score the same.
func fromFeed(source string) bool {
	for _, tok := range strings.Split(source, "+") {
		if strings.HasPrefix(tok, "feed:") {
			return true
		}
	}
	return false
}

// normTerms lowercases and collapses terms, dropping blanks and duplicates.
func normTerms(terms []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range terms {
		t = collapse(strings.ToLower(t))
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// matchTerm reports whether term occurs in text with no letter or digit
// immediately before or after it. Every occurrence is tried, so "go" is
// found in "good go".
func matchTerm(text, term string) bool {
	for start := 0; start < len(text); {
		k := strings.Index(text[start:], term)
		if k < 0 {
			return false
		}
		k += start
		before, _ := utf8.DecodeLastRuneInString(text[:k])
		after, _ := utf8.DecodeRuneInString(text[k+len(term):])
		if !isWordRune(before) && !isWordRune(after) {
			return true
		}
		_, size := utf8.DecodeRuneInString(text[k:])
		start = k + size
	}
	return false
}

// isWordRune is true for letters and digits. utf8.RuneError, returned at the
// string boundaries, is neither.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
