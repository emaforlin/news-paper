package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/emaforlin/news-paper/internal/httpx"
	"github.com/emaforlin/news-paper/internal/news"
)

const (
	DefaultLobstersURL   = "https://lobste.rs/hottest.json"
	DefaultLobstersLimit = 25
)

// Lobsters takes the first Limit stories of the hottest listing. Zero fields
// take the Default* values.
type Lobsters struct {
	URL   string
	Limit int
}

type lobstersItem struct {
	ShortIDURL       string   `json:"short_id_url"`
	CreatedAt        string   `json:"created_at"`
	Title            string   `json:"title"`
	URL              string   `json:"url"`
	Score            int      `json:"score"`
	Description      string   `json:"description"`
	DescriptionPlain string   `json:"description_plain"`
	CommentsURL      string   `json:"comments_url"`
	Tags             []string `json:"tags"`
}

func (l *Lobsters) Name() string { return "lobsters" }

func (l *Lobsters) Fetch(ctx context.Context, hc *httpx.Client) ([]news.Candidate, error) {
	u := l.URL
	if u == "" {
		u = DefaultLobstersURL
	}
	limit := l.Limit
	if limit <= 0 {
		limit = DefaultLobstersLimit
	}
	body, _, err := get(ctx, hc, u, jsonAccept)
	if err != nil {
		return nil, err
	}
	var items []lobstersItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	out := make([]news.Candidate, 0, min(limit, len(items)))
	for _, it := range items[:min(limit, len(items))] {
		discussion := strings.TrimSpace(it.CommentsURL)
		if discussion == "" {
			discussion = strings.TrimSpace(it.ShortIDURL)
		}
		link := news.CleanURL(it.URL)
		if link == "" {
			link = discussion // text post
		}
		if link == "" {
			continue
		}
		summary := it.DescriptionPlain
		if strings.TrimSpace(summary) == "" {
			summary = htmlToText(it.Description)
		}
		var published time.Time
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(it.CreatedAt)); err == nil {
			published = t
		}
		out = append(out, news.Candidate{
			URL:        link,
			Title:      collapse(it.Title),
			Source:     "lobsters",
			Summary:    truncate(summary, summaryRunes),
			Score:      it.Score,
			Discussion: discussion,
			Published:  published,
			Tags:       it.Tags,
		})
	}
	return out, nil
}
