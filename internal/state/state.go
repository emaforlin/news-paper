// Package state remembers which URL ids were already delivered or permanently
// rejected, so they never show up again.
package state

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	version       = 1
	secondsPerDay = 86400
)

// State is the set of seen URL ids. The zero value is an empty state.
type State struct {
	Seen map[string]int64 // URL id (news.URLID) -> UTC day number when it was recorded
}

// New returns an empty state.
func New() *State { return &State{Seen: map[string]int64{}} }

// Has reports whether id is recorded.
func (s *State) Has(id string) bool {
	_, ok := s.Seen[id]
	return ok
}

// Add records ids on now's day, refreshing the ones already there. Empty ids
// are ignored.
func (s *State) Add(now time.Time, ids ...string) {
	if s.Seen == nil {
		s.Seen = map[string]int64{}
	}
	d := day(now)
	for _, id := range ids {
		if id != "" {
			s.Seen[id] = d
		}
	}
}

// Prune drops the entries recorded more than retentionDays before now. A
// negative retention counts as 0.
func (s *State) Prune(now time.Time, retentionDays int) {
	oldest := day(now) - int64(max(0, retentionDays))
	for id, d := range s.Seen {
		if d < oldest {
			delete(s.Seen, id)
		}
	}
}

// day is the number of whole UTC days from the Unix epoch to t, rounded down.
func day(t time.Time) int64 {
	sec := t.Unix()
	d := sec / secondsPerDay
	if sec%secondsPerDay < 0 {
		d--
	}
	return d
}

// file is the serialized form.
type file struct {
	Version *int             `json:"version"`
	Seen    map[string]int64 `json:"seen"`
}

func encode(s *State) ([]byte, error) {
	v := version
	seen := s.Seen
	if seen == nil {
		seen = map[string]int64{}
	}
	return json.Marshal(file{Version: &v, Seen: seen})
}

// decode refuses versions it doesn't know. A missing version is 1.
func decode(data []byte) (*State, error) {
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if f.Version != nil && *f.Version != version {
		return nil, fmt.Errorf("unsupported version %d", *f.Version)
	}
	s := New()
	if f.Seen != nil {
		s.Seen = f.Seen
	}
	return s, nil
}
