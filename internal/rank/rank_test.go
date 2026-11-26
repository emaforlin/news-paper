package rank

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/emaforlin/news-paper/internal/news"
)

func picksOf(urls ...string) []news.Pick {
	var out []news.Pick
	for _, u := range urls {
		out = append(out, news.Pick{Candidate: cand(u, "hn", 1)})
	}
	return out
}

func pickURLs(ps []news.Pick) string {
	var out []string
	for _, p := range ps {
		out = append(out, strings.TrimPrefix(p.URL, "https://"))
	}
	return strings.Join(out, " ")
}

func msgs(lines []map[string]any) string {
	var out []string
	for _, l := range lines {
		out = append(out, l["level"].(string)+" "+l["msg"].(string))
	}
	return strings.Join(out, "; ")
}

var pool = []news.Candidate{
	cand("https://a.com/1", "hn", 500), cand("https://a.com/2", "hn", 400), cand("https://a.com/3", "hn", 300),
	cand("https://b.com/1", "hn", 200), cand("https://c.com/1", "hn", 100), cand("https://d.com/1", "hn", 50),
}

func TestWithFallbackPrimaryFails(t *testing.T) {
	log, lines := logCapture(t)
	primary := &fakeRanker{name: "llm", err: errors.New("boom")}
	got := WithFallback(context.Background(), log, primary, &Heuristic{}, pool, 4, 2)
	if want := "a.com/1 a.com/2 b.com/1 c.com/1"; pickURLs(got) != want {
		t.Errorf("picks = %s, want %s", pickURLs(got), want)
	}
	l := lines()
	if msgs(l) != "WARN ranker failed, using fallback; INFO topped up" || l[0]["ranker"] != "llm" || l[0]["err"] != "boom" {
		t.Errorf("logs = %v", l)
	}

	secondary := &fakeRanker{name: "s"}
	WithFallback(context.Background(), nil, primary, secondary, pool, 4, 2)
	if len(secondary.asked) != 1 || secondary.asked[0] != len(pool) {
		t.Errorf("secondary asked for %v", secondary.asked)
	}
}

func TestWithFallbackCap(t *testing.T) {
	log, lines := logCapture(t)
	primary := &fakeRanker{name: "llm", picks: picksOf(
		"https://a.com/1", "https://a.com/2", "https://a.com/3", "https://b.com/1")}
	got := WithFallback(context.Background(), log, primary, &Heuristic{}, pool, 5, 2)
	if want := "a.com/1 a.com/2 b.com/1 c.com/1 d.com/1"; pickURLs(got) != want {
		t.Errorf("picks = %s, want %s", pickURLs(got), want)
	}
	l := lines()
	if msgs(l) != "INFO ranked; INFO topped up" || l[0]["picks"] != float64(4) || l[1]["added"] != float64(2) {
		t.Errorf("logs = %v", l)
	}
}

func TestWithFallbackNoCap(t *testing.T) {
	for _, limit := range []int{0, -1} {
		got := WithFallback(context.Background(), nil, nil, &Heuristic{}, pool, 3, limit)
		if want := "a.com/1 a.com/2 a.com/3"; pickURLs(got) != want {
			t.Errorf("cap %d: picks = %s", limit, pickURLs(got))
		}
	}
}

func TestWithFallbackEdges(t *testing.T) {
	ctx := context.Background()
	short := &fakeRanker{name: "p", picks: picksOf("https://a.com/1")}
	if got := WithFallback(ctx, nil, short, nil, pool, 3, 0); len(got) != 1 {
		t.Errorf("no secondary: %s", pickURLs(got))
	}

	dups := &fakeRanker{name: "p", picks: picksOf("https://a.com/1", "https://a.com/1", "https://b.com/1", "https://c.com/1")}
	secondary := &fakeRanker{name: "s"}
	if got := WithFallback(ctx, nil, dups, secondary, pool, 2, 0); pickURLs(got) != "a.com/1 b.com/1" || len(secondary.asked) != 0 {
		t.Errorf("duplicates: %s, secondary asked %v", pickURLs(got), secondary.asked)
	}

	log, lines := logCapture(t)
	failing := &fakeRanker{name: "s", err: errors.New("down")}
	if got := WithFallback(ctx, log, short, failing, pool, 3, 0); len(got) != 1 || msgs(lines()) != "INFO ranked; WARN fallback ranker failed" {
		t.Errorf("secondary error: %s, %v", pickURLs(got), lines())
	}

	if got := WithFallback(ctx, nil, nil, &Heuristic{}, pool, 50, 0); len(got) != len(pool) {
		t.Errorf("small pool: %d", len(got))
	}
	unused := &fakeRanker{name: "p"}
	if got := WithFallback(ctx, nil, unused, secondary, pool, 0, 0); got == nil || len(got) != 0 || len(unused.asked) != 0 {
		t.Errorf("n=0: %v, primary asked %v", got, unused.asked)
	}
}

func TestWithFallbackOpenRouterDown(t *testing.T) {
	o, got := mockOR(t, 500, "oops")
	picks := WithFallback(context.Background(), nil, o, &Heuristic{}, pool, 3, 0)
	if len(picks) != 3 || got.count.Load() != 1 {
		t.Errorf("%d picks after %d requests", len(picks), got.count.Load())
	}
}
