// news package holds the structures that flow through the pipeline:
// Candidate (from sources) -> Pick (after ranking) -> Article (after extraction).

package news

import (
	"crypto/sha1"
	"encoding/hex"
	"net/url"
	"strings"
	"time"
)

// Candidate is a link found by a source, before ranking
type Candidate struct {
	URL        string    // cannonical article URL
	Title      string    // headline as given by the source
	Source     string    // thn, lobster, "feed:<name>"
	Summary    string    // plain text snippet used for ranking (may be empty)
	Score      int       // source-native popularity (HN points, Lobsters score); 0 for feeds
	Discussion string    // comments URL, if any
	Published  time.Time // zero if unknown
	Tags       []string
}

func (c Candidate) ID() string {
	return URLID(c.URL)
}

func (c Candidate) Domain() string {
	return Domain(c.URL)
}

// Pick is a Candidate selected by a ranker
type Pick struct {
	Candidate
	Reason string // one-line "why this", shown on the front page; may be empty
}

// Article is a Pick whose page was fetched and cleaned
type Article struct {
	Pick
	Title    string // title from the page; falls back to Candidate.Title
	Byline   string // author(s); may be empty
	SiteName string // publication name; may be empty
	Language string // from <html lang="...">; may be empty
	XHTML    string // sanitized, well-formed XHTML fragment (body content only)
	Words    int    // words kept after cleaning, >= 0
}

// ReadingMinutes assumes ~230 wpm, minimum 1.
func (a Article) ReadingMinutes() int {
	m := (a.Words + 229) / 230
	if m < 1 {
		return 1
	}
	return m
}

// trackingParams are dropped by CleanURL.
var trackingParams = map[string]bool{
	"fbclid":  true,
	"gclid":   true,
	"mc_cid":  true,
	"mc_eid":  true,
	"ref":     true,
	"ref_src": true,
	"share":   true,
}

// CleanURL strips the fragment and tracking query params. The result is still
// safe to fetch (scheme and host are untouched). Invalid URLs come back trimmed.
func CleanURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	u.Fragment = ""
	u.ForceQuery = false // a bare "?" carries nothing
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			lk := strings.ToLower(k)
			if strings.HasPrefix(lk, "utm_") || trackingParams[lk] {
				q.Del(k)
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// DedupeKey collapses http/https, "www." and trailing slashes so the same
// article coming from HN, Lobsters and a feed maps to one key.
func DedupeKey(raw string) string {
	u, err := url.Parse(CleanURL(raw))
	if err != nil || u.Host == "" {
		return strings.ToLower(strings.TrimSpace(raw))
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	path := strings.TrimRight(u.EscapedPath(), "/")
	key := host + path
	if u.RawQuery != "" {
		key += "?" + u.RawQuery
	}
	return key
}

func Domain(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// URLID hashes a URL's dedupe key into 12 hex chars.
func URLID(raw string) string {
	h := sha1.Sum([]byte(DedupeKey(raw)))
	return hex.EncodeToString(h[:6])
}
