package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/emaforlin/news-paper/internal/httpx"
	"github.com/emaforlin/news-paper/internal/news"
)

const (
	DefaultHNAPIBase   = "https://hn.algolia.com/api/v1"
	DefaultHNMinPoints = 100
	DefaultHNLimit     = 60
	// NoMinPoints disables the score filter: a zero MinPoints means the
	// default, so "no minimum" needs its own value.
	NoMinPoints = -1

	hnWindow   = 24 * time.Hour
	hnItemURL  = "https://news.ycombinator.com/item?id="
	jsonAccept = "application/json"
)

// HackerNews lists stories from the last 24 h (ending at Now) with at least
// MinPoints, through the Algolia search API: one request. Zero fields take
// the Default* values; a negative MinPoints (NoMinPoints) means no minimum.
type HackerNews struct {
	APIBase   string
	MinPoints int
	Limit     int
	Now       time.Time
}

type hnHit struct {
	ObjectID   string `json:"objectID"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	StoryText  string `json:"story_text"`
	Points     int    `json:"points"`
	CreatedAtI int64  `json:"created_at_i"`
}

func (h *HackerNews) Name() string { return "hn" }

// searchURL builds the Algolia query. url.Values.Encode sorts the keys,
// which makes the string deterministic.
func (h *HackerNews) searchURL() string {
	base, minPoints, limit := h.APIBase, h.MinPoints, h.Limit
	if base == "" {
		base = DefaultHNAPIBase
	}
	switch {
	case minPoints == 0:
		minPoints = DefaultHNMinPoints
	case minPoints < 0:
		minPoints = 0
	}
	if limit <= 0 {
		limit = DefaultHNLimit
	}
	q := url.Values{
		"hitsPerPage":    {fmt.Sprint(limit)},
		"numericFilters": {fmt.Sprintf("created_at_i>%d,points>=%d", h.Now.Add(-hnWindow).Unix(), minPoints)},
		"tags":           {"story"},
	}
	return strings.TrimRight(base, "/") + "/search?" + q.Encode()
}

func (h *HackerNews) Fetch(ctx context.Context, hc *httpx.Client) ([]news.Candidate, error) {
	body, _, err := get(ctx, hc, h.searchURL(), jsonAccept)
	if err != nil {
		return nil, err
	}
	var res struct {
		Hits []hnHit `json:"hits"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	limit := h.Limit
	if limit <= 0 {
		limit = DefaultHNLimit
	}
	out := make([]news.Candidate, 0, min(limit, len(res.Hits)))
	for _, hit := range res.Hits {
		if len(out) == limit {
			break
		}
		discussion := ""
		if id := strings.TrimSpace(hit.ObjectID); id != "" {
			discussion = hnItemURL + url.QueryEscape(id)
		}
		link := news.CleanURL(hit.URL)
		if link == "" {
			link = discussion // Ask HN / text post
		}
		if link == "" {
			continue
		}
		out = append(out, news.Candidate{
			URL:        link,
			Title:      collapse(hit.Title),
			Source:     "hn",
			Summary:    truncate(htmlToText(hit.StoryText), summaryRunes),
			Score:      hit.Points,
			Discussion: discussion,
			Published:  time.Unix(hit.CreatedAtI, 0).UTC(),
		})
	}
	return out, nil
}
