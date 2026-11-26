// Package rank turns the day's candidates into an ordered list of picks:
// Prefilter (dedupe, seen, deny list, non-HTML), then a Ranker.
package rank

// Seen reports whether a URL id was already sent or rejected. state.State
// implements it.
type Seen interface{ Has(id string) bool }
