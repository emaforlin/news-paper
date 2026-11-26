package sources

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html/charset"

	"github.com/emaforlin/news-paper/internal/httpx"
	"github.com/emaforlin/news-paper/internal/news"
)

// ErrNoItems is returned by ParseFeed when the document has no usable
// <item> or <entry>.
var ErrNoItems = errors.New("no items found (not an RSS/Atom feed?)")

// textField is an RSS/Atom text construct: escaped HTML (character data or
// CDATA), inline XHTML (Atom type="xhtml") or plain text.
type textField struct {
	Type  string `xml:"type,attr"`
	Data  string `xml:",chardata"`
	Inner string `xml:",innerxml"`
}

// linkField covers both Atom <link href rel> and RSS <link>text</link>.
type linkField struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Text string `xml:",chardata"`
}

// categoryField covers RSS <category>text</category> and Atom <category term>.
type categoryField struct {
	Term string `xml:"term,attr"`
	Text string `xml:",chardata"`
}

// rawItem is the union of RSS 2.0, RSS 1.0 and Atom item fields. Tags carry
// no namespace, so encoding/xml matches them by local name in any namespace
// (dc:date → date, atom:link → link).
type rawItem struct {
	Title       []textField     `xml:"title"`
	Links       []linkField     `xml:"link"`
	GUID        string          `xml:"guid"`
	Description []textField     `xml:"description"`
	Summary     []textField     `xml:"summary"`
	Content     []textField     `xml:"content"`
	Comments    []string        `xml:"comments"`
	PubDate     string          `xml:"pubDate"`
	Published   string          `xml:"published"`
	Updated     string          `xml:"updated"`
	Date        string          `xml:"date"`
	Categories  []categoryField `xml:"category"`
}

const utf8BOM = "\xEF\xBB\xBF"

// ParseFeed reads an RSS 2.0, RSS 1.0 (RDF) or Atom document and returns its
// items in document order. Relative links are resolved against base. An XML
// error before the first item is returned; after it, parsing stops and the
// items read so far are returned.
func ParseFeed(r io.Reader, base string) ([]news.Candidate, error) {
	br := bufio.NewReader(r)
	if b, _ := br.Peek(len(utf8BOM)); string(b) == utf8BOM {
		_, _ = br.Discard(len(utf8BOM))
	}
	baseURL, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		baseURL = nil
	}

	d := xml.NewDecoder(br)
	d.Strict = false
	d.Entity = xml.HTMLEntity
	d.CharsetReader = charset.NewReaderLabel

	var items []news.Candidate
	seen := 0 // <item>/<entry> elements decoded, usable or not
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if seen == 0 {
				return nil, fmt.Errorf("parse feed: %w", err)
			}
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || (se.Name.Local != "item" && se.Name.Local != "entry") {
			continue
		}
		var raw rawItem
		if err := d.DecodeElement(&raw, &se); err != nil {
			if seen == 0 {
				return nil, fmt.Errorf("parse feed: %w", err)
			}
			break
		}
		seen++
		if c, ok := raw.candidate(baseURL); ok {
			items = append(items, c)
		}
	}
	if len(items) == 0 {
		return nil, ErrNoItems
	}
	return items, nil
}

// candidate maps the union to a Candidate. ok is false when the item has no
// usable absolute http(s) link.
func (it *rawItem) candidate(base *url.URL) (news.Candidate, bool) {
	link := resolve(base, it.link())
	if link == "" {
		return news.Candidate{}, false
	}
	c := news.Candidate{
		URL:       news.CleanURL(link),
		Title:     firstText(it.Title),
		Summary:   truncate(firstText(it.Description, it.Summary, it.Content), summaryRunes),
		Published: firstDate(it.PubDate, it.Published, it.Updated, it.Date),
	}
	for _, cm := range it.Comments {
		if cm = strings.TrimSpace(cm); strings.HasPrefix(cm, "http") {
			c.Discussion = cm
			break
		}
	}
	for _, cat := range it.Categories {
		tag := strings.TrimSpace(cat.Text)
		if tag == "" {
			tag = strings.TrimSpace(cat.Term)
		}
		if tag != "" {
			c.Tags = append(c.Tags, tag)
		}
	}
	return c, true
}

// link picks the Atom alternate href, then the RSS link text, then a guid
// that looks like a URL.
func (it *rawItem) link() string {
	for _, l := range it.Links {
		if rel := strings.TrimSpace(l.Rel); strings.TrimSpace(l.Href) != "" && (rel == "" || rel == "alternate") {
			return strings.TrimSpace(l.Href)
		}
	}
	for _, l := range it.Links {
		if s := strings.TrimSpace(l.Text); s != "" {
			return s
		}
	}
	if g := strings.TrimSpace(it.GUID); strings.HasPrefix(g, "http") {
		return g
	}
	return ""
}

// resolve returns ref resolved against base, or "" unless the result is an
// absolute http(s) URL.
func resolve(base *url.URL, ref string) string {
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.String()
}

// firstText returns the first non-empty plain text among the fields of each
// group, groups in order.
func firstText(groups ...[]textField) string {
	for _, g := range groups {
		for _, f := range g {
			if s := f.text(); s != "" {
				return s
			}
		}
	}
	return ""
}

// text converts a text construct to plain text. An explicit Atom
// type="text" is taken literally; anything else is treated as HTML, from
// the character data or, when that is blank, the inner XML (inline XHTML).
func (f textField) text() string {
	if strings.TrimSpace(f.Type) == "text" {
		return collapse(f.Data)
	}
	if strings.TrimSpace(f.Data) != "" {
		return htmlToText(f.Data)
	}
	return htmlToText(f.Inner)
}

// firstDate returns the first value that parses; zero if none does.
func firstDate(vals ...string) time.Time {
	for _, v := range vals {
		if t := parseDate(v); !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

const (
	// DefaultFeedLimit applies when Feed.Limit <= 0.
	DefaultFeedLimit = 20

	feedAccept = "application/rss+xml, application/atom+xml, application/xml;q=0.9, */*;q=0.5"
)

// Feed is an RSS 2.0, RSS 1.0 or Atom feed.
type Feed struct {
	FeedName  string    // Name() is "feed:" + FeedName ("feed:<domain>" when empty)
	URL       string    // feed URL
	Limit     int       // max items kept after the age filter; <= 0 → DefaultFeedLimit
	NotBefore time.Time // items dated before it are dropped; zero → no cutoff
}

func (f *Feed) Name() string {
	if f.FeedName == "" {
		return "feed:" + news.Domain(f.URL)
	}
	return "feed:" + f.FeedName
}

// Fetch downloads and parses the feed, drops dated items older than
// NotBefore (undated ones are kept) and keeps the first Limit, in document
// order.
func (f *Feed) Fetch(ctx context.Context, hc *httpx.Client) ([]news.Candidate, error) {
	body, final, err := get(ctx, hc, f.URL, feedAccept)
	if err != nil {
		return nil, err
	}
	items, err := ParseFeed(bytes.NewReader(body), final.String())
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultFeedLimit
	}
	name := f.Name()
	out := make([]news.Candidate, 0, min(limit, len(items)))
	for _, c := range items {
		if len(out) == limit {
			break
		}
		if !c.Published.IsZero() && c.Published.Before(f.NotBefore) {
			continue
		}
		c.Source = name
		out = append(out, c)
	}
	return out, nil
}
