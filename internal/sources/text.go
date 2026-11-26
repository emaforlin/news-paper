package sources

import (
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// dateLayouts are tried in order by parseDate; the first that parses wins.
var dateLayouts = []string{
	time.RFC1123Z,
	time.RFC1123,
	time.RFC3339,
	time.RFC3339Nano,
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"2 Jan 2006 15:04:05 -0700",
	"Mon, 02 Jan 2006 15:04 -0700",
	time.RFC822Z,
	time.RFC822,
	"2006-01-02T15:04:05",
	"2006-01-02",
}

// parseDate returns the zero time when s matches none of dateLayouts.
func parseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// collapse joins whitespace runs into single spaces and trims the ends.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// htmlToText flattens an HTML fragment to plain text: text nodes are kept,
// script and style content is dropped, and end tags (plus br/hr) act as
// word separators.
func htmlToText(s string) string {
	if !strings.ContainsAny(s, "<&") {
		return collapse(s)
	}
	var b strings.Builder
	z := html.NewTokenizer(strings.NewReader(s))
	skip := 0 // depth inside script/style
	for {
		switch z.Next() {
		case html.ErrorToken:
			return collapse(b.String())
		case html.TextToken:
			if skip == 0 {
				b.Write(z.Text())
			}
		case html.StartTagToken:
			name, _ := z.TagName()
			switch atom.Lookup(name) {
			case atom.Script, atom.Style:
				skip++
			case atom.Br, atom.Hr:
				b.WriteByte(' ')
			}
		case html.SelfClosingTagToken:
			name, _ := z.TagName()
			if a := atom.Lookup(name); a == atom.Br || a == atom.Hr {
				b.WriteByte(' ')
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			if a := atom.Lookup(name); (a == atom.Script || a == atom.Style) && skip > 0 {
				skip--
			}
			b.WriteByte(' ')
		}
	}
}

// truncate collapses whitespace and cuts s to at most n runes, preferring
// the last space when it lies past the halfway point, and appends "…".
func truncate(s string, n int) string {
	s = collapse(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	cut, runes := s, 0
	for i := range s {
		if runes == n {
			cut = s[:i]
			break
		}
		runes++
	}
	if i := strings.LastIndexByte(cut, ' '); i > n/2 {
		cut = cut[:i]
	}
	return cut + "…"
}
