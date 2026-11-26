package sources

import (
	"encoding/xml"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/emaforlin/news-paper/internal/news"
)

const rss2Sample = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom"
     xmlns:slash="http://purl.org/rss/1.0/modules/slash/"
     xmlns:dc="http://purl.org/dc/elements/1.1/">
<channel>
  <title>Blog &amp; Co</title>
  <link>https://blog.example.com/</link>
  <atom:link href="https://blog.example.com/feed.xml" rel="self" type="application/rss+xml"/>
  <item>
    <title>Rust &amp; Go&#8217;s&nbsp;future</title>
    <atom:link href="https://blog.example.com/feed.xml" rel="self"/>
    <link>/posts/a?utm_source=rss&amp;id=7</link>
    <description><![CDATA[<p>First <b>post</b> with <a href="/x">links</a>.</p><script>evil()</script>]]></description>
    <comments>https://blog.example.com/posts/a#comments</comments>
    <slash:comments>12</slash:comments>
    <pubDate>Tue, 24 Nov 2026 10:00:00 +0000</pubDate>
    <category>go</category>
    <category> rust </category>
    <category></category>
  </item>
  <item>
    <title>Second</title>
    <link>https://other.example.org/b#frag</link>
    <description>&lt;p&gt;Escaped &lt;em&gt;HTML&lt;/em&gt;&lt;/p&gt;</description>
    <slash:comments>3</slash:comments>
  </item>
</channel>
</rss>`

func TestParseRSS2(t *testing.T) {
	got, err := ParseFeed(strings.NewReader(rss2Sample), "https://blog.example.com/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	want := []news.Candidate{
		{
			URL:        "https://blog.example.com/posts/a?id=7",
			Title:      "Rust & Go’s future",
			Summary:    "First post with links .",
			Discussion: "https://blog.example.com/posts/a#comments",
			Published:  time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC),
			Tags:       []string{"go", "rust"},
		},
		{
			URL:     "https://other.example.org/b",
			Title:   "Second",
			Summary: "Escaped HTML",
		},
	}
	assertCandidates(t, got, want)
}

const atomSample = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom blog</title>
  <link href="https://atom.example.com/" rel="alternate"/>
  <link href="https://atom.example.com/feed.atom" rel="self"/>
  <updated>2026-11-24T09:00:00Z</updated>
  <entry>
    <title type="html">Tom &amp;amp; &lt;b&gt;Jerry&lt;/b&gt;</title>
    <link href="https://atom.example.com/c/1" rel="replies"/>
    <link href="/e/1" rel="alternate" type="text/html"/>
    <id>tag:atom.example.com,2026:1</id>
    <updated>2026-11-24T08:00:00Z</updated>
    <summary type="xhtml"><div xmlns="http://www.w3.org/1999/xhtml"><p>Inline <em>XHTML</em></p><p>summary</p></div></summary>
    <category term="go"/>
    <category term="web" label="Web"/>
  </entry>
  <entry>
    <title type="text">Using &lt;div&gt; tags</title>
    <link href="https://atom.example.com/e/2"/>
    <published>2026-11-23T08:00:00-03:00</published>
    <content type="html">&lt;p&gt;Only content&lt;/p&gt;</content>
  </entry>
</feed>`

func TestParseAtom(t *testing.T) {
	got, err := ParseFeed(strings.NewReader(atomSample), "https://atom.example.com/feed.atom")
	if err != nil {
		t.Fatal(err)
	}
	want := []news.Candidate{
		{
			URL:       "https://atom.example.com/e/1",
			Title:     "Tom & Jerry",
			Summary:   "Inline XHTML summary",
			Published: time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC),
			Tags:      []string{"go", "web"},
		},
		{
			URL:       "https://atom.example.com/e/2",
			Title:     "Using <div> tags",
			Summary:   "Only content",
			Published: time.Date(2026, 11, 23, 11, 0, 0, 0, time.UTC),
		},
	}
	assertCandidates(t, got, want)
}

const rdfSample = `<?xml version="1.0"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"
         xmlns="http://purl.org/rss/1.0/"
         xmlns:dc="http://purl.org/dc/elements/1.1/">
  <channel rdf:about="https://rdf.example.com/">
    <title>RDF</title>
    <link>https://rdf.example.com/</link>
    <items><rdf:Seq><rdf:li rdf:resource="https://rdf.example.com/1"/></rdf:Seq></items>
  </channel>
  <item rdf:about="https://rdf.example.com/1">
    <title>One</title>
    <link>https://rdf.example.com/1</link>
    <description>First item</description>
    <dc:date>2026-11-24T08:00:00Z</dc:date>
  </item>
  <item rdf:about="https://rdf.example.com/2">
    <title>Two</title>
    <link>https://rdf.example.com/2</link>
    <dc:date>2026-11-23</dc:date>
  </item>
</rdf:RDF>`

func TestParseRDF(t *testing.T) {
	got, err := ParseFeed(strings.NewReader(rdfSample), "")
	if err != nil {
		t.Fatal(err)
	}
	want := []news.Candidate{
		{URL: "https://rdf.example.com/1", Title: "One", Summary: "First item",
			Published: time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)},
		{URL: "https://rdf.example.com/2", Title: "Two",
			Published: time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)},
	}
	assertCandidates(t, got, want)
}

func TestParseCharsets(t *testing.T) {
	tests := []struct {
		name, enc, title, want string
	}{
		{"latin1", "ISO-8859-1", "caf\xe9 se\xf1al", "café señal"},
		{"windows-1252", "windows-1252", "\x93quoted\x94 \x80", "“quoted” €"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := `<?xml version="1.0" encoding="` + tt.enc + `"?><rss><channel><item><title>` +
				tt.title + `</title><link>https://x.example/1</link></item></channel></rss>`
			got, err := ParseFeed(strings.NewReader(doc), "")
			if err != nil {
				t.Fatal(err)
			}
			if got[0].Title != tt.want || !utf8.ValidString(got[0].Title) {
				t.Errorf("title = %q, want %q", got[0].Title, tt.want)
			}
		})
	}
}

func TestParseNotAFeed(t *testing.T) {
	tests := []struct {
		name, doc string
		syntax    bool // want a *xml.SyntaxError instead of ErrNoItems
	}{
		{"xhtml page", `<!DOCTYPE html><html><head><title>Home</title><link rel="stylesheet" href="/s.css"/></head><body><p>Hello</p></body></html>`, false},
		{"empty", ``, false},
		{"empty channel", `<rss><channel/></rss>`, false},
		{"items without links", `<rss><channel><item><title>x</title></item></channel></rss>`, false},
		{"truncated before item", `<rss><channel><title>a</channel`, true},
		{"bad attribute", `<rss><a b="c></rss>`, true},
		{"broken first item", `<rss><channel><item><title>a</ti`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseFeed(strings.NewReader(tt.doc), "https://x.example/")
			if got != nil {
				t.Errorf("got %d items, want none", len(got))
			}
			var se *xml.SyntaxError
			switch {
			case tt.syntax && !errors.As(err, &se):
				t.Errorf("err = %v, want *xml.SyntaxError", err)
			case tt.syntax && !strings.HasPrefix(err.Error(), "parse feed: "):
				t.Errorf("err = %v, want parse feed prefix", err)
			case !tt.syntax && !errors.Is(err, ErrNoItems):
				t.Errorf("err = %v, want ErrNoItems", err)
			}
		})
	}
}

func TestParseTrailingGarbage(t *testing.T) {
	two := `<rss><channel>
<item><title>A</title><link>https://x.example/a</link></item>
<item><title>B</title><link>https://x.example/b</link></item>`
	tests := map[string]string{
		"garbage after items": two + `</channel></rss><<<not xml`,
		"cut inside item":     two + `<item><title>C</ti`,
		"cut inside tag":      two + `<item><tit`,
	}
	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := ParseFeed(strings.NewReader(doc), "")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0].Title != "A" || got[1].Title != "B" {
				t.Errorf("got %+v, want items A and B", got)
			}
		})
	}
}

func TestParseLinkSelection(t *testing.T) {
	tests := []struct {
		name, item, base, want string // want "" means skipped
	}{
		{"atom alternate", `<entry><link rel="replies" href="https://x.example/c"/><link rel="alternate" href="https://x.example/a"/></entry>`, "", "https://x.example/a"},
		{"atom no rel", `<entry><link href="https://x.example/a"/></entry>`, "", "https://x.example/a"},
		{"atom only self", `<entry><link rel="self" href="https://x.example/self"/></entry>`, "", ""},
		{"rss text link", `<item><link> https://x.example/r </link></item>`, "", "https://x.example/r"},
		{"rss relative", `<item><link>p/1</link></item>`, "https://x.example/blog/feed", "https://x.example/blog/p/1"},
		{"relative without base", `<item><link>/p/1</link></item>`, "", ""},
		{"relative with bad base", `<item><link>/p/1</link></item>`, "::not a url", ""},
		{"guid fallback", `<item><guid isPermaLink="false">https://x.example/g</guid></item>`, "", "https://x.example/g"},
		{"guid not a url", `<item><guid>tag:x.example,2026:1</guid></item>`, "https://x.example/", ""},
		{"link beats guid", `<item><guid>https://x.example/g</guid><link>https://x.example/l</link></item>`, "", "https://x.example/l"},
		{"no link", `<item><title>t</title></item>`, "https://x.example/", ""},
		{"javascript", `<item><link>javascript:alert(1)</link></item>`, "https://x.example/", ""},
		{"mailto", `<item><link>mailto:a@x.example</link></item>`, "https://x.example/", ""},
		{"unparsable", `<item><link>http://[::1</link></item>`, "https://x.example/", ""},
		{"tracking stripped", `<item><link>https://x.example/a?utm_medium=feed&amp;fbclid=1</link></item>`, "", "https://x.example/a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := `<feed>` + tt.item + `<entry><link href="https://x.example/sentinel"/></entry></feed>`
			got, err := ParseFeed(strings.NewReader(doc), tt.base)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tt.want == "" && len(got) != 1:
				t.Errorf("got %d items, want the item skipped", len(got))
			case tt.want != "" && (len(got) != 2 || got[0].URL != tt.want):
				t.Errorf("got %+v, want first URL %q", got, tt.want)
			}
		})
	}
}

func TestParseBOM(t *testing.T) {
	doc := "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="utf-8"?><rss><channel><item><link>https://x.example/1</link></item></channel></rss>`
	got, err := ParseFeed(strings.NewReader(doc), "")
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v; want one item", got, err)
	}
}

func TestParseFieldFallbacks(t *testing.T) {
	long := strings.Repeat("word ", 500)
	tests := []struct {
		name, fields string
		check        func(t *testing.T, c news.Candidate)
	}{
		{"summary from description", `<description>d</description><summary>s</summary><content>c</content>`,
			wantSummary("d")},
		{"summary from summary", `<description> </description><summary>s</summary><content>c</content>`,
			wantSummary("s")},
		{"summary from content", `<content>c</content>`, wantSummary("c")},
		{"media content skipped", `<content url="https://x.example/i.jpg"/><content>c</content>`, wantSummary("c")},
		{"summary truncated", `<description>` + long + `</description>`, func(t *testing.T, c news.Candidate) {
			if n := utf8.RuneCountInString(c.Summary); n > summaryRunes+1 || !strings.HasSuffix(c.Summary, "…") {
				t.Errorf("summary has %d runes: %q", n, c.Summary)
			}
		}},
		{"first non-empty title", `<title></title><title>Real</title>`, func(t *testing.T, c news.Candidate) {
			if c.Title != "Real" {
				t.Errorf("title = %q", c.Title)
			}
		}},
		{"pubDate first", `<pubDate>Tue, 24 Nov 2026 10:00:00 +0000</pubDate><updated>2020-01-01</updated>`,
			wantDate("2026-11-24T10:00:00Z")},
		{"published", `<published>2026-11-22</published><updated>2020-01-01</updated>`, wantDate("2026-11-22T00:00:00Z")},
		{"updated", `<updated>2026-11-21T00:00:00Z</updated><dc:date>2020-01-01</dc:date>`, wantDate("2026-11-21T00:00:00Z")},
		{"dc:date", `<dc:date>2026-11-20</dc:date>`, wantDate("2026-11-20T00:00:00Z")},
		{"garbage pubDate", `<pubDate>soon</pubDate><updated>2026-11-24T08:00:00Z</updated>`, wantDate("2026-11-24T08:00:00Z")},
		{"no date", ``, wantDate("")},
		{"no comments url", `<comments> </comments><comments>4</comments>`, func(t *testing.T, c news.Candidate) {
			if c.Discussion != "" {
				t.Errorf("discussion = %q", c.Discussion)
			}
		}},
		{"categories", `<category> </category><category term=" t "/><category term="ignored">text</category>`,
			func(t *testing.T, c news.Candidate) {
				if !reflect.DeepEqual(c.Tags, []string{"t", "text"}) {
					t.Errorf("tags = %q", c.Tags)
				}
			}},
		{"no categories", ``, func(t *testing.T, c news.Candidate) {
			if c.Tags != nil {
				t.Errorf("tags = %q, want nil", c.Tags)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := `<rss xmlns:dc="http://purl.org/dc/elements/1.1/"><channel><item><link>https://x.example/1</link>` +
				tt.fields + `</item></channel></rss>`
			got, err := ParseFeed(strings.NewReader(doc), "")
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, got[0])
		})
	}
}

func wantSummary(s string) func(*testing.T, news.Candidate) {
	return func(t *testing.T, c news.Candidate) {
		t.Helper()
		if c.Summary != s {
			t.Errorf("summary = %q, want %q", c.Summary, s)
		}
	}
}

func wantDate(s string) func(*testing.T, news.Candidate) {
	return func(t *testing.T, c news.Candidate) {
		t.Helper()
		if s == "" {
			if !c.Published.IsZero() {
				t.Errorf("published = %v, want zero", c.Published)
			}
			return
		}
		if want := mustTime(t, time.RFC3339, s); !c.Published.Equal(want) {
			t.Errorf("published = %v, want %v", c.Published, want)
		}
	}
}

// errReader yields data, then fails.
type errReader struct{ data string }

var errRead = errors.New("read failed")

func (r *errReader) Read(p []byte) (int, error) {
	if r.data == "" {
		return 0, errRead
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestParseReadError(t *testing.T) {
	_, err := ParseFeed(&errReader{`<rss><channel>`}, "")
	if !errors.Is(err, errRead) {
		t.Errorf("before any item: err = %v, want errRead", err)
	}
	got, err := ParseFeed(&errReader{`<rss><channel><item><link>https://x.example/1</link></item>`}, "")
	if err != nil || len(got) != 1 {
		t.Errorf("after one item: got %v, %v; want the item", got, err)
	}
	_, err = ParseFeed(io.MultiReader(strings.NewReader(`<rss><item><title>`), &errReader{}), "")
	if !errors.Is(err, errRead) {
		t.Errorf("inside the first item: err = %v, want errRead", err)
	}
}

func assertCandidates(t *testing.T, got, want []news.Candidate) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d candidates, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if !g.Published.Equal(w.Published) {
			t.Errorf("[%d] Published = %v, want %v", i, g.Published, w.Published)
		}
		g.Published, w.Published = time.Time{}, time.Time{}
		if !reflect.DeepEqual(g, w) {
			t.Errorf("[%d]\n got %+v\nwant %+v", i, g, w)
		}
	}
}
