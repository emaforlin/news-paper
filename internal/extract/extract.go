// Package extract turns ranked picks into readable articles: it downloads
// each page, rejects paywalls and teasers, runs readability and re-emits the
// content as Kindle-safe XHTML (see Sanitize).
//
// A *Rejected error means "never retry this URL"; any other error is
// transient and the pick may be retried another day.
package extract

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"codeberg.org/readeck/go-readability"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"github.com/emaforlin/news-paper/internal/httpx"
	"github.com/emaforlin/news-paper/internal/news"
)

const (
	DefaultMinWords    = 250
	DefaultMaxBody     = httpx.DefaultMaxBody
	DefaultConcurrency = 4

	// teaserMaxWords: below this, a teaser phrase means a cut-off article.
	teaserMaxWords = 600
	acceptHeader   = "text/html,application/xhtml+xml;q=0.9,*/*;q=0.5"
)

var (
	// paywallRe matches schema.org paywall markup anywhere in the raw page.
	paywallRe = regexp.MustCompile(`"isAccessibleForFree"\s*:\s*"?(?i:false)"?`)

	teaserPhrases = []string{
		"subscribe to continue",
		"subscribe to read",
		"already a subscriber",
		"to continue reading",
		"create a free account",
		"sign in to read",
		"this article is for subscribers",
		"become a member to read",
	}

	// rejectStatus are HTTP statuses that won't change by retrying.
	rejectStatus = map[int]bool{401: true, 402: true, 403: true, 404: true, 410: true}
)

// Rejected is a permanent outcome: the caller records the pick in state and
// never retries it. Detect it with errors.As.
type Rejected struct{ Reason string }

func (r *Rejected) Error() string { return "rejected: " + r.Reason }

func reject(format string, args ...any) *Rejected {
	return &Rejected{Reason: fmt.Sprintf(format, args...)}
}

// Extractor fetches and cleans articles. The zero value is usable; zero or
// negative fields fall back to the defaults. It is never mutated, so Extract
// and Collect are safe for concurrent use.
type Extractor struct {
	HC          *httpx.Client
	MinWords    int   // below this = teaser/paywall
	MaxBody     int64 // HTML download cap in bytes
	Concurrency int   // parallel extractions in Collect; <= 0 means 1
	Log         *slog.Logger
}

func (e *Extractor) hc() *httpx.Client {
	if e.HC != nil {
		return e.HC
	}
	return &httpx.Client{}
}

func (e *Extractor) minWords() int {
	if e.MinWords > 0 {
		return e.MinWords
	}
	return DefaultMinWords
}

func (e *Extractor) maxBody() int64 {
	if e.MaxBody > 0 {
		return e.MaxBody
	}
	return DefaultMaxBody
}

func (e *Extractor) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Extract downloads one pick and returns it as an Article. Permanent
// failures are *Rejected; network errors, timeouts and unexpected statuses
// are returned as plain (transient) errors.
func (e *Extractor) Extract(ctx context.Context, p news.Pick) (news.Article, error) {
	resp, err := e.hc().Get(ctx, p.URL, http.Header{"Accept": {acceptHeader}}, e.maxBody())
	if err != nil {
		return news.Article{}, fmt.Errorf("fetch: %w", err)
	}
	if rejectStatus[resp.StatusCode] {
		return news.Article{}, reject("HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode/100 != 2 {
		return news.Article{}, fmt.Errorf("fetch: HTTP %d", resp.StatusCode)
	}
	// A truncated body is fine: readability copes with partial documents.

	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.Contains(strings.ToLower(ct), "html") {
		return news.Article{}, reject("not HTML: %s", ct)
	}
	if paywallRe.Match(resp.Body) {
		return news.Article{}, reject("isAccessibleForFree=false")
	}

	doc, err := decodeAndParse(bytes.NewReader(resp.Body), ct)
	base := resp.URL
	resp = nil // release the raw body before readability runs
	if err != nil {
		return news.Article{}, err
	}

	// ParseAndMutate works on doc in place; FromDocument would clone it.
	ps := readability.NewParser()
	art, err := ps.ParseAndMutate(doc, base)
	if err != nil {
		return news.Article{}, reject("readability: %v", err)
	}
	if art.Node == nil {
		return news.Article{}, reject("readability: no content")
	}

	xhtml, words := Sanitize(art.Node, base)
	if words < e.minWords() {
		return news.Article{}, reject("too short (%d words): paywall/teaser?", words)
	}
	if words < teaserMaxWords && hasTeaser(art.TextContent) {
		return news.Article{}, reject("teaser phrase on short page")
	}

	title := strings.TrimSpace(art.Title)
	if title == "" {
		title = p.Title
	}
	return news.Article{
		Pick:     p,
		Title:    title,
		Byline:   strings.TrimSpace(art.Byline),
		SiteName: strings.TrimSpace(art.SiteName),
		Language: strings.TrimSpace(art.Language),
		XHTML:    xhtml,
		Words:    words,
	}, nil
}

// decodeAndParse converts the page to UTF-8 (header, BOM, <meta>, sniffing)
// and parses it as HTML5. It takes an io.Reader so tests can inject failures.
func decodeAndParse(r io.Reader, contentType string) (*html.Node, error) {
	dr, err := charset.NewReader(r, contentType)
	if err != nil {
		return nil, reject("charset: %v", err)
	}
	doc, err := html.Parse(dr)
	if err != nil {
		return nil, reject("parse: %v", err)
	}
	return doc, nil
}

func hasTeaser(text string) bool {
	text = strings.ToLower(text)
	for _, p := range teaserPhrases {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}
