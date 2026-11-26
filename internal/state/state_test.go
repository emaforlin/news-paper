package state

import (
	"maps"
	"testing"
	"time"

	"github.com/emaforlin/news-paper/internal/rank"
)

var _ rank.Seen = (*State)(nil)

var now = time.Date(2026, 11, 26, 8, 40, 0, 0, time.UTC)

func TestDay(t *testing.T) {
	art := time.FixedZone("ART", -3*3600)
	tests := []struct {
		t    time.Time
		want int64
	}{
		{now, 20783},
		{time.Date(2026, 11, 26, 2, 0, 0, 0, art), 20783},
		{time.Date(1969, 12, 31, 23, 0, 0, 0, time.UTC), -1},
		{time.Date(1969, 12, 31, 0, 0, 0, 0, time.UTC), -1},
		{time.Unix(0, 0), 0},
	}
	for _, tt := range tests {
		if got := day(tt.t); got != tt.want {
			t.Errorf("day(%s) = %d, want %d", tt.t, got, tt.want)
		}
	}
}

func TestAddHasPrune(t *testing.T) {
	if s := New(); s.Seen == nil || s.Has("a") {
		t.Fatalf("New() = %+v", s)
	}
	var s State // the zero value works
	if s.Has("a") {
		t.Fatal("empty state has a")
	}
	s.Prune(now, 30)
	s.Add(now.AddDate(0, 0, -40), "a")
	s.Add(now, "a", "b", "")
	if !s.Has("a") || !s.Has("b") || s.Has("") || s.Seen["a"] != 20783 {
		t.Errorf("seen = %v", s.Seen)
	}

	s.Add(now.AddDate(0, 0, -30), "edge")
	s.Add(now.AddDate(0, 0, -31), "old")
	s.Prune(now, 30)
	if !s.Has("edge") || s.Has("old") {
		t.Errorf("after prune: %v", s.Seen)
	}
	s.Prune(now, -1)
	if want := map[string]int64{"a": 20783, "b": 20783}; !maps.Equal(s.Seen, want) {
		t.Errorf("negative retention: %v", s.Seen)
	}
}

func TestCodec(t *testing.T) {
	data, err := encode(&State{Seen: map[string]int64{"f6d20cd59f5b": 20783}})
	if err != nil || string(data) != `{"version":1,"seen":{"f6d20cd59f5b":20783}}` {
		t.Fatalf("encode = %s, %v", data, err)
	}
	if data, _ := encode(&State{}); string(data) != `{"version":1,"seen":{}}` {
		t.Errorf("encode zero = %s", data)
	}
	tests := []struct {
		src string
		n   int
		ok  bool
	}{
		{`{"version":1,"seen":{"a":1}}`, 1, true},
		{`{"seen":{"a":1}}`, 1, true},
		{`{"version":null,"seen":null}`, 0, true},
		{`{}`, 0, true},
		{`{"version":2,"seen":{}}`, 0, false},
		{`{"version":0}`, 0, false},
		{`{"seen":{"a":"x"}}`, 0, false},
		{``, 0, false},
	}
	for _, tt := range tests {
		s, err := decode([]byte(tt.src))
		if (err == nil) != tt.ok {
			t.Errorf("%s: err = %v", tt.src, err)
		}
		if tt.ok && (s.Seen == nil || len(s.Seen) != tt.n) {
			t.Errorf("%s: seen = %v", tt.src, s.Seen)
		}
	}
}
