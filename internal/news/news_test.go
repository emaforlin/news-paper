package news

import (
	"regexp"
	"strings"
	"testing"
)

// cleanURLTests is shared by TestCleanURL and TestCleanURLIdempotent so that
// idempotency is checked over every row, including the spec (§3.1) examples.
var cleanURLTests = []struct {
	name string
	in   string
	want string
}{
	// Rows from spec §3.1.
	{"spec: trims, drops utm and fragment, keeps www and host case", "  https://www.Example.com/a/?utm_source=hn&id=3#top ", "https://www.Example.com/a/?id=3"},
	{"spec: drops fbclid", "https://x.com/p?fbclid=abc", "https://x.com/p"},
	{"spec: sorts query by key", "http://x.com/p?b=2&a=1", "http://x.com/p?a=1&b=2"},
	{"spec: utm prefix is case-insensitive", "https://x.com/p?UTM_Medium=x&page=2", "https://x.com/p?page=2"},
	{"spec: not a url", "not a url", "not a url"},

	{"plain url unchanged", "https://example.com/post", "https://example.com/post"},
	{"trims whitespace", "  https://example.com/post\n", "https://example.com/post"},
	{"drops fragment", "https://example.com/post#comments", "https://example.com/post"},
	{"drops utm params", "https://example.com/post?utm_source=hn&utm_medium=social", "https://example.com/post"},
	{"utm match is case-insensitive", "https://example.com/post?UTM_Source=hn", "https://example.com/post"},
	{"drops known tracking params", "https://example.com/post?fbclid=a&gclid=b&mc_cid=c&mc_eid=d&ref=e&ref_src=f&share=g", "https://example.com/post"},
	{"tracking match is case-insensitive", "https://example.com/post?FBCLID=a", "https://example.com/post"},
	{"keeps other params", "https://example.com/post?id=42&utm_source=hn", "https://example.com/post?id=42"},
	{"ref prefix alone is not tracking", "https://example.com/post?referrer=x", "https://example.com/post?referrer=x"},
	{"keeps repeated params", "https://example.com/post?tag=a&tag=b", "https://example.com/post?tag=a&tag=b"},
	{"sorts remaining params", "https://example.com/post?b=2&a=1", "https://example.com/post?a=1&b=2"},
	{"drops query and fragment together", "https://example.com/post?utm_campaign=x#top", "https://example.com/post"},
	{"keeps scheme, host and port", "http://Example.com:8080/post", "http://Example.com:8080/post"},
	{"keeps path and trailing slash", "https://example.com/a/b/", "https://example.com/a/b/"},
	{"no host returned trimmed", "  not a url  ", "not a url"},
	{"relative path returned as is", "/just/a/path?utm_source=x", "/just/a/path?utm_source=x"},
	{"unparseable returned trimmed", " http://[::1 ", "http://[::1"},
	{"keeps www and host case", "https://WWW.Example.com/post", "https://WWW.Example.com/post"},
	{"bare question mark emits no ?", "https://example.com/post?", "https://example.com/post"},
	{"empty", "", ""},
}

func TestCleanURL(t *testing.T) {
	for _, tt := range cleanURLTests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CleanURL(tt.in); got != tt.want {
				t.Errorf("CleanURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCleanURLIdempotent(t *testing.T) {
	inputs := []string{
		"https://example.com/post?b=2&a=1&utm_source=hn#frag",
		"https://example.com/a%20b?q=hello%20world",
		"http://www.example.com/",
		"not a url",
	}
	for _, tt := range cleanURLTests {
		inputs = append(inputs, tt.in)
	}
	for _, in := range inputs {
		once := CleanURL(in)
		if twice := CleanURL(once); twice != once {
			t.Errorf("CleanURL not idempotent for %q: %q then %q", in, once, twice)
		}
	}
}

// Spec §3.1: never returns an empty string for non-empty input.
func TestCleanURLNeverEmpty(t *testing.T) {
	inputs := []string{
		"https://example.com/?utm_source=hn",
		"https://example.com?fbclid=x#top",
		"#fragment",
		"?utm_source=x",
		"not a url",
		"http://[::1",
	}
	for _, tt := range cleanURLTests {
		inputs = append(inputs, tt.in)
	}
	for _, in := range inputs {
		if strings.TrimSpace(in) == "" {
			continue // trimming whitespace-only input legitimately yields ""
		}
		if got := CleanURL(in); got == "" {
			t.Errorf("CleanURL(%q) = \"\", want non-empty", in)
		}
	}
}

func TestDedupeKey(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// Rows from spec §3.2.
		{"spec: www, trailing slash, utm and fragment", "https://www.example.com/post/1/?utm_source=hn#c", "example.com/post/1"},
		{"spec: http", "http://example.com/post/1", "example.com/post/1"},
		{"spec: uppercase host and trailing slash", "https://EXAMPLE.com/post/1/", "example.com/post/1"},
		{"spec: meaningful query kept", "https://example.com/post/1?page=2", "example.com/post/1?page=2"},
		{"spec: root path becomes empty", "https://example.com/", "example.com"},
		{"spec: port kept", "https://example.com:8080/a", "example.com:8080/a"},

		{"https", "https://example.com/post", "example.com/post"},
		{"http", "http://example.com/post", "example.com/post"},
		{"strips www", "https://www.example.com/post", "example.com/post"},
		{"lowercases host", "https://WWW.Example.COM/post", "example.com/post"},
		{"keeps path case", "https://example.com/Post", "example.com/Post"},
		{"only strips www. prefix", "https://www2.example.com/post", "www2.example.com/post"},
		{"strips trailing slash", "https://example.com/post/", "example.com/post"},
		{"strips repeated trailing slashes", "https://example.com/post//", "example.com/post"},
		{"root path", "https://example.com/", "example.com"},
		{"keeps port", "https://example.com:8080/post", "example.com:8080/post"},
		{"drops fragment", "https://example.com/post#c", "example.com/post"},
		{"drops tracking params", "https://example.com/post?utm_source=hn&ref=lobsters", "example.com/post"},
		{"keeps meaningful query", "https://example.com/item?id=42", "example.com/item?id=42"},
		{"normalizes query order", "https://example.com/item?b=2&a=1", "example.com/item?a=1&b=2"},
		{"keeps escaped path", "https://example.com/a%20b", "example.com/a%20b"},
		{"no host is lowercased and trimmed", "  Not A URL ", "not a url"},
		{"unparseable is lowercased and trimmed", " HTTP://[::1 ", "http://[::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DedupeKey(tt.in); got != tt.want {
				t.Errorf("DedupeKey(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The same article reached via HN, Lobsters and a feed must collapse to one key.
func TestDedupeKeyEquivalence(t *testing.T) {
	variants := []string{
		"https://example.com/2026/11/post",
		"http://example.com/2026/11/post",
		"https://www.example.com/2026/11/post/",
		"https://WWW.EXAMPLE.COM/2026/11/post?utm_source=hackernews",
		"http://example.com/2026/11/post/?ref=lobsters#comments",
		" https://example.com/2026/11/post?fbclid=abc123 ",
	}
	want := DedupeKey(variants[0])
	for _, v := range variants[1:] {
		if got := DedupeKey(v); got != want {
			t.Errorf("DedupeKey(%q) = %q, want %q", v, got, want)
		}
	}

	distinct := []string{
		"https://example.com/2026/11/other-post",
		"https://example.org/2026/11/post",
		"https://blog.example.com/2026/11/post",
		"https://example.com/2026/11/post?page=2",
	}
	for _, d := range distinct {
		if got := DedupeKey(d); got == want {
			t.Errorf("DedupeKey(%q) = %q, should differ from %q", d, got, want)
		}
	}
}

// Spec §3.2: paths are case-sensitive, so these must not collapse.
func TestDedupeKeyPathCase(t *testing.T) {
	upper := DedupeKey("https://example.com/Post")
	lower := DedupeKey("https://example.com/post")
	if upper == lower {
		t.Errorf("DedupeKey path case collapsed: both %q", upper)
	}
}

var idPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

// Spec §3.3 / §6: 12 lowercase hex chars, equal for equal keys, and a golden
// value that catches accidental storage-format changes.
func TestURLID(t *testing.T) {
	const golden = "f6d20cd59f5b"
	if got := URLID("https://example.com/post/1"); got != golden {
		t.Errorf("URLID(%q) = %q, want golden %q (storage format changed?)", "https://example.com/post/1", got, golden)
	}

	same := []string{
		"https://example.com/post/1",
		"https://www.example.com/post/1/?utm_source=hn#c",
		"http://example.com/post/1",
		"https://EXAMPLE.com/post/1/",
	}
	for _, u := range same {
		got := URLID(u)
		if !idPattern.MatchString(got) {
			t.Errorf("URLID(%q) = %q, want 12 lowercase hex chars", u, got)
		}
		if got != golden {
			t.Errorf("URLID(%q) = %q, want %q (same DedupeKey as golden)", u, got, golden)
		}
		if again := URLID(u); again != got {
			t.Errorf("URLID(%q) not deterministic: %q then %q", u, got, again)
		}
	}

	different := []string{
		"https://example.com/post/1?page=2",
		"https://example.com/Post/1",
		"https://example.com:8080/post/1",
	}
	for _, u := range different {
		if got := URLID(u); got == golden {
			t.Errorf("URLID(%q) = %q, should differ from golden", u, got)
		}
	}
}

// Golden values pin the ID format: changing URLID invalidates saved state.
func TestCandidateIDGolden(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"canonical url", "https://example.com/post", "ec7d183f93cc"},
		{"equivalent variant", "http://www.example.com/post/?utm_source=hn#c", "ec7d183f93cc"},
		{"empty url hashes empty key", "", "da39a3ee5e6b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (Candidate{URL: tt.url}).ID(); got != tt.want {
				t.Errorf("Candidate{URL: %q}.ID() = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestCandidateID(t *testing.T) {
	urls := []string{
		"https://example.com/post",
		"https://example.com/item?id=42",
		"not a url",
		"",
	}
	for _, u := range urls {
		c := Candidate{URL: u}
		got := c.ID()
		if !idPattern.MatchString(got) {
			t.Errorf("Candidate{URL: %q}.ID() = %q, want 12 lowercase hex chars", u, got)
		}
		if want := URLID(u); got != want {
			t.Errorf("Candidate{URL: %q}.ID() = %q, want URLID = %q", u, got, want)
		}
	}
}

// ID depends only on the URL, so the same article from different sources
// (with different metadata) shares one ID.
func TestCandidateIDIgnoresMetadata(t *testing.T) {
	hn := Candidate{URL: "https://example.com/post", Title: "Post", Source: "hn", Score: 300}
	feed := Candidate{URL: "https://www.example.com/post/?utm_source=rss", Title: "A post", Source: "feed:blog"}
	if hn.ID() != feed.ID() {
		t.Errorf("IDs differ for same article: %q vs %q", hn.ID(), feed.ID())
	}

	other := Candidate{URL: "https://example.com/other", Title: "Post", Source: "hn", Score: 300}
	if hn.ID() == other.ID() {
		t.Errorf("distinct URLs share ID %q", hn.ID())
	}
}

// Rows from spec §3.4.
func TestDomain(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://www.Blog.Example.com:8443/x", "blog.example.com"},
		{"::bad", ""},
	}
	for _, tt := range tests {
		if got := Domain(tt.in); got != tt.want {
			t.Errorf("Domain(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCandidateDomain(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"plain host", "https://example.com/post", "example.com"},
		{"strips www", "https://www.example.com/post", "example.com"},
		{"lowercases before stripping www", "https://WWW.Example.COM/post", "example.com"},
		{"only strips www. prefix", "https://www2.example.com/post", "www2.example.com"},
		{"keeps subdomain", "https://blog.example.com/post", "blog.example.com"},
		{"drops port", "http://example.com:8080/post", "example.com"},
		{"ipv6 host", "http://[::1]:8080/post", "::1"},
		{"ignores query and fragment", "https://example.com/post?id=1#c", "example.com"},
		{"no host", "not a url", ""},
		{"relative path", "/just/a/path", ""},
		{"unparseable", "http://[::1", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Candidate{URL: tt.url}
			if got := c.Domain(); got != tt.want {
				t.Errorf("Candidate{URL: %q}.Domain() = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestArticleReadingMinutes(t *testing.T) {
	tests := []struct {
		name      string
		wordCount int
		want      int
	}{
		{"zero words", 0, 1},
		{"one minute", 230, 1},
		{"two minutes", 231, 2},
		{"fourteen minutes", 3000, 14},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := Article{Words: tt.wordCount}
			if got := a.ReadingMinutes(); got != tt.want {
				t.Errorf("Article{Words: %d}.ReadingMinutes() = %d, want %d", tt.wordCount, got, tt.want)
			}
		})
	}
}

// Spec §2.3: Article.Title shadows Candidate.Title; the source headline stays
// reachable. Spec §2.2: Pick (and so Article) inherits ID and Domain.
func TestArticleTitleShadowing(t *testing.T) {
	c := Candidate{URL: "https://www.example.com/post/1", Title: "Source headline"}
	a := Article{Pick: Pick{Candidate: c, Reason: "porque sí"}, Title: "Page title"}

	if a.Title != "Page title" {
		t.Errorf("a.Title = %q, want page title", a.Title)
	}
	if a.Candidate.Title != "Source headline" {
		t.Errorf("a.Candidate.Title = %q, want source headline", a.Candidate.Title)
	}
	if a.Pick.Title != "Source headline" {
		t.Errorf("a.Pick.Title = %q, want source headline", a.Pick.Title)
	}
	if a.ID() != c.ID() || a.Pick.ID() != c.ID() {
		t.Errorf("ID not inherited: article %q, pick %q, candidate %q", a.ID(), a.Pick.ID(), c.ID())
	}
	if a.Domain() != "example.com" || a.Pick.Domain() != "example.com" {
		t.Errorf("Domain not inherited: article %q, pick %q", a.Domain(), a.Pick.Domain())
	}
}
