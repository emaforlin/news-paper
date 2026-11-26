// Package sources produces the day's raw candidates from Hacker News,
// Lobsters and RSS/Atom feeds. A failing source never fails the run: it is
// logged and skipped.
package sources

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/emaforlin/news-paper/internal/httpx"
	"github.com/emaforlin/news-paper/internal/news"
)

// Source fetches candidates from one origin.
type Source interface {
	// Name is "hn", "lobsters" or "feed:<name>"; it is used as
	// Candidate.Source and in logs.
	Name() string
	Fetch(ctx context.Context, hc *httpx.Client) ([]news.Candidate, error)
}

// maxBody caps every source response.
const maxBody = 4 << 20 // 4 MiB

// Config selects and tunes the sources. The JSON tags follow the config
// file's "sources" object, so internal/config can embed it as-is.
type Config struct {
	MaxAgeHours int            `json:"max_age_hours"` // <= 0: no age cutoff for feeds
	HackerNews  HNConfig       `json:"hackernews"`
	Lobsters    LobstersConfig `json:"lobsters"`
	Feeds       []FeedConfig   `json:"feeds"`
}

// HNConfig configures the Hacker News source; zero fields take defaults.
type HNConfig struct {
	Enabled   bool   `json:"enabled"`
	APIBase   string `json:"api_base"`
	MinPoints int    `json:"min_points"`
	Limit     int    `json:"limit"`
}

// LobstersConfig configures the Lobsters source; zero fields take defaults.
type LobstersConfig struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
	Limit   int    `json:"limit"`
}

// FeedConfig is one RSS/Atom feed; Limit <= 0 means DefaultFeedLimit.
type FeedConfig struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Limit int    `json:"limit"`
}

// FromConfig builds the enabled sources in a fixed order: Hacker News,
// Lobsters, then the feeds in config order. now anchors the HN window and
// the feeds' age cutoff.
func FromConfig(c Config, now time.Time) []Source {
	var srcs []Source
	var notBefore time.Time
	if c.MaxAgeHours > 0 {
		notBefore = now.Add(-time.Duration(c.MaxAgeHours) * time.Hour)
	}
	for _, f := range c.Feeds {
		srcs = append(srcs, &Feed{FeedName: f.Name, URL: f.URL, Limit: f.Limit, NotBefore: notBefore})
	}
	return srcs
}

// FetchAll runs every source with at most max(1, concurrency) in flight and
// returns their candidates concatenated in srcs order, whatever order they
// finish in. Failures (errors or panics) are logged and contribute nothing.
// It never fails; an empty result is valid.
func FetchAll(ctx context.Context, hc *httpx.Client, srcs []Source, concurrency int, log *slog.Logger) []news.Candidate {
	if hc == nil {
		hc = &httpx.Client{}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	results := make([][]news.Candidate, len(srcs)) // slot i belongs to srcs[i]
	sem := make(chan struct{}, max(1, concurrency))
	var wg sync.WaitGroup
	for i, s := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = run(ctx, hc, s, log)
		}()
	}
	wg.Wait()

	out := []news.Candidate{}
	for _, r := range results {
		out = append(out, r...)
	}
	return out
}

// run fetches one source and logs the outcome.
func run(ctx context.Context, hc *httpx.Client, s Source, log *slog.Logger) []news.Candidate {
	start := time.Now()
	items, err := safeFetch(ctx, hc, s)
	if err != nil {
		log.Warn("source failed", "source", s.Name(), "err", err)
		return nil
	}
	log.Info("source ok", "source", s.Name(), "items", len(items), "took", time.Since(start))
	return items
}

// safeFetch turns a panic in s.Fetch into an error.
func safeFetch(ctx context.Context, hc *httpx.Client, s Source) (items []news.Candidate, err error) {
	defer func() {
		if r := recover(); r != nil {
			items, err = nil, fmt.Errorf("panic: %v", r)
		}
	}()
	return s.Fetch(ctx, hc)
}

// get fetches rawURL with the given Accept header and the 4 MiB cap. A
// non-2xx status is an error; a truncated body is not.
func get(ctx context.Context, hc *httpx.Client, rawURL, accept string) ([]byte, *url.URL, error) {
	if hc == nil {
		hc = &httpx.Client{}
	}
	resp, err := hc.Get(ctx, rawURL, http.Header{"Accept": {accept}}, maxBody)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return resp.Body, resp.URL, nil
}
