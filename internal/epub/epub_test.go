package epub

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/emaforlin/news-paper/internal/news"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// art is UTC-3 without needing tzdata.
var art = time.FixedZone("ART", -3*60*60)

// editionDate is 05:45 at UTC-3, i.e. 08:45 UTC (spec §5 example).
var editionDate = time.Date(2026, 11, 26, 5, 45, 0, 0, art)

// goldenUUID is the spec §5.1 identifier for "Diario · 2026-11-26".
const goldenUUID = "bfa6e23d-065e-5220-8b5f-c9466a240f88"

// sampleEdition has three articles that between them exercise every optional
// field: byline/site name present or missing, with and without Reason and
// Discussion, valid/invalid/empty Language, and 1 / 14 / 60 reading minutes.
func sampleEdition() Edition {
	return Edition{
		Title:    "Diario",
		Date:     editionDate,
		Language: "en",
		Articles: []news.Article{
			{
				Pick: news.Pick{
					Candidate: news.Candidate{
						URL:        "https://www.example.com/post/1",
						Title:      "HN headline",
						Source:     "hn",
						Discussion: "https://news.ycombinator.com/item?id=1",
					},
					Reason: "Explica bien el tema",
				},
				Title:    "First article",
				Byline:   "Ada Lovelace",
				SiteName: "Example Blog",
				Language: "en-US",
				XHTML:    `<p id="m1">Hello <em>world</em> &amp; friends.</p>`,
				Words:    230, // 1 min
			},
			{
				Pick: news.Pick{
					Candidate: news.Candidate{
						URL:    "https://blog.example.org/b",
						Title:  "Feed headline",
						Source: "feed:blog",
					},
				},
				Title:    "Second article",
				Language: "english!", // invalid: falls back to Edition.Language
				XHTML:    `<p id="m2">Second body.</p>`,
				Words:    3000, // 14 min
			},
			{
				Pick: news.Pick{
					Candidate: news.Candidate{
						URL:        "https://third.example.net/c",
						Title:      "Lobsters headline",
						Source:     "lobsters",
						Discussion: "https://lobste.rs/s/abc",
					},
					Reason: "Corto y claro",
				},
				Title:    "Third article",
				SiteName: "Third Site",
				XHTML:    `<h2>Part</h2><p id="m3">Third body.</p>`,
				Words:    13800, // 60 min
			},
		},
	}
}

// hostile packs every character the escaping rules (§10) must handle.
// \x0b is invalid in XML 1.0 and must be dropped; the rest must be escaped.
const hostile = `T&T <b>"q"</b> a>b` + "\x0b"

// hostileRaw are substrings of hostile that must never appear unescaped in any entry.
var hostileRaw = []string{"T&T", "<b>", `"q"`, "a>b", "\x0b"}

// clean is what a hostile string should decode to: the same text minus
// characters that are invalid in XML.
func clean(s string) string { return strings.ReplaceAll(s, "\x0b", "") }

func hostileEdition() Edition {
	ed := sampleEdition()
	ed.Title = "Diario " + hostile
	a := &ed.Articles[0]
	a.Title = "Title " + hostile
	a.Reason = "Reason " + hostile
	a.Byline = "Byline " + hostile
	a.URL = "https://example.com/p?x=" + hostile
	a.Discussion = "https://example.com/d?x=" + hostile
	return ed
}

func emptyEdition() Edition {
	ed := sampleEdition()
	ed.Articles = nil
	return ed
}

func manyArticles(n int) []news.Article {
	out := make([]news.Article, n)
	for i := range out {
		out[i] = news.Article{
			Pick: news.Pick{Candidate: news.Candidate{
				URL:    fmt.Sprintf("https://example.com/%d", i),
				Source: "hn",
			}},
			Title: fmt.Sprintf("Article %d", i),
			XHTML: "<p>body</p>",
			Words: 100,
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Book helpers
// ---------------------------------------------------------------------------

type book struct {
	raw   []byte
	zr    *zip.Reader
	names []string
	files map[string]string
}

func build(t *testing.T, ed Edition) *book {
	t.Helper()
	raw, err := Bytes(ed)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("output is not a zip: %v", err)
	}
	b := &book{raw: raw, zr: zr, files: map[string]string{}}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		rc.Close()
		if _, dup := b.files[f.Name]; dup {
			t.Fatalf("duplicate zip entry %s", f.Name)
		}
		b.names = append(b.names, f.Name)
		b.files[f.Name] = buf.String()
	}
	return b
}

// doc parses the named entry, failing if it is missing or malformed.
func (b *book) doc(t *testing.T, name string) *node {
	t.Helper()
	s, ok := b.files[name]
	if !ok {
		t.Fatalf("missing entry %s", name)
	}
	return parseXML(t, name, s)
}

func chapterFile(i int) string { return fmt.Sprintf("a%02d.xhtml", i+1) }
func chapterID(i int) string   { return fmt.Sprintf("a%02d", i+1) }

func isXMLEntry(name string) bool {
	switch filepath.Ext(name) {
	case ".xml", ".opf", ".ncx", ".xhtml":
		return true
	}
	return false
}

type manifestItem struct{ href, mediaType, properties string }

func manifest(t *testing.T, b *book) (map[string]manifestItem, []string) {
	t.Helper()
	pkg := b.doc(t, "OEBPS/content.opf")
	m := one(t, "manifest", pkg.childrenByTag("manifest"))
	items := map[string]manifestItem{}
	var ids []string
	for _, it := range m.childrenByTag("item") {
		id := it.attr("", "id")
		if _, dup := items[id]; dup {
			t.Errorf("duplicate manifest id %q", id)
		}
		ids = append(ids, id)
		items[id] = manifestItem{
			href:       it.attr("", "href"),
			mediaType:  it.attr("", "media-type"),
			properties: it.attr("", "properties"),
		}
	}
	return items, ids
}

// spineHrefs resolves the spine itemrefs through the manifest.
func spineHrefs(t *testing.T, b *book) []string {
	t.Helper()
	items, _ := manifest(t, b)
	pkg := b.doc(t, "OEBPS/content.opf")
	spine := one(t, "spine", pkg.childrenByTag("spine"))
	var hrefs []string
	for _, ref := range spine.childrenByTag("itemref") {
		it, ok := items[ref.attr("", "idref")]
		if !ok {
			t.Errorf("spine idref %q not in manifest", ref.attr("", "idref"))
			continue
		}
		hrefs = append(hrefs, it.href)
	}
	return hrefs
}

// navTOC returns the hrefs and labels of the nav.xhtml table of contents.
func navTOC(t *testing.T, b *book) (hrefs, labels []string) {
	t.Helper()
	root := b.doc(t, "OEBPS/nav.xhtml")
	nav := one(t, `nav[epub:type="toc"]`, root.find(func(n *node) bool {
		return n.name.Local == "nav" && n.attr(nsEPUB, "type") == "toc"
	}))
	ol := one(t, "nav > ol", nav.childrenByTag("ol"))
	for _, li := range ol.childrenByTag("li") {
		a := one(t, "nav li > a", li.childrenByTag("a"))
		hrefs = append(hrefs, a.attr("", "href"))
		labels = append(labels, norm(a.textContent()))
	}
	return hrefs, labels
}

type navPoint struct {
	id, playOrder, label, src string
}

func ncxNavPoints(t *testing.T, b *book) []navPoint {
	t.Helper()
	root := b.doc(t, "OEBPS/toc.ncx")
	navMap := one(t, "navMap", root.childrenByTag("navMap"))
	var out []navPoint
	for _, np := range navMap.childrenByTag("navPoint") {
		label := one(t, "navLabel", np.childrenByTag("navLabel"))
		text := one(t, "navLabel > text", label.childrenByTag("text"))
		content := one(t, "navPoint > content", np.childrenByTag("content"))
		out = append(out, navPoint{
			id:        np.attr("", "id"),
			playOrder: np.attr("", "playOrder"),
			label:     norm(text.textContent()),
			src:       content.attr("", "src"),
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// §4 Container layout
// ---------------------------------------------------------------------------

func TestMimetypeIsFirstStoredEntry(t *testing.T) {
	b := build(t, sampleEdition())

	f := b.zr.File[0]
	if f.Name != "mimetype" {
		t.Fatalf("first entry = %q, want mimetype", f.Name)
	}
	if f.Method != zip.Store {
		t.Errorf("mimetype method = %d, want Store (0)", f.Method)
	}
	if f.Flags&0x8 != 0 {
		t.Errorf("mimetype has data descriptor flag (bit 3) set: flags=%#x", f.Flags)
	}
	if len(f.Extra) != 0 {
		t.Errorf("mimetype has extra field (%d bytes), want none", len(f.Extra))
	}
	if got := b.files["mimetype"]; got != "application/epub+zip" {
		t.Errorf("mimetype content = %q, want %q", got, "application/epub+zip")
	}

	// Readers sniff the raw bytes, so check the local file header itself:
	// "PK\x03\x04", flags@6, method@8, csize@18, usize@22, nameLen@26,
	// extraLen@28, name@30, then the data.
	raw := b.raw
	const content = "application/epub+zip"
	if len(raw) < 38+len(content) || !bytes.HasPrefix(raw, []byte("PK\x03\x04")) {
		t.Fatalf("output does not start with a local file header")
	}
	le := binary.LittleEndian
	if flags := le.Uint16(raw[6:]); flags&0x8 != 0 {
		t.Errorf("local header flags = %#x, data descriptor bit set", flags)
	}
	if m := le.Uint16(raw[8:]); m != 0 {
		t.Errorf("local header method = %d, want 0 (stored)", m)
	}
	if cs, us := le.Uint32(raw[18:]), le.Uint32(raw[22:]); cs != uint32(len(content)) || us != uint32(len(content)) {
		t.Errorf("local header sizes = %d/%d, want %d/%d", cs, us, len(content), len(content))
	}
	if n := le.Uint16(raw[26:]); n != uint16(len("mimetype")) {
		t.Errorf("local header name length = %d, want 8", n)
	}
	if n := le.Uint16(raw[28:]); n != 0 {
		t.Errorf("local header extra length = %d, want 0", n)
	}
	if got := string(raw[30:38]); got != "mimetype" {
		t.Errorf("local header name = %q, want mimetype", got)
	}
	if got := string(raw[38 : 38+len(content)]); got != content {
		t.Errorf("bytes after header = %q, want %q", got, content)
	}
}

func TestEntriesInOrder(t *testing.T) {
	ed := sampleEdition()
	b := build(t, ed)

	want := []string{
		"mimetype",
		"META-INF/container.xml",
		"OEBPS/content.opf",
		"OEBPS/toc.ncx",
		"OEBPS/nav.xhtml",
		"OEBPS/style.css",
		"OEBPS/front.xhtml",
		"OEBPS/a01.xhtml",
		"OEBPS/a02.xhtml",
		"OEBPS/a03.xhtml",
	}
	if !slices.Equal(b.names, want) {
		t.Fatalf("entries =\n  %q\nwant\n  %q", b.names, want)
	}

	for _, f := range b.zr.File[1:] {
		if f.Method != zip.Deflate {
			t.Errorf("%s: method = %d, want Deflate (8)", f.Name, f.Method)
		}
		if !f.Modified.Equal(ed.Date) {
			t.Errorf("%s: Modified = %v, want edition date %v", f.Name, f.Modified, ed.Date)
		}
	}
}

func TestContainerXML(t *testing.T) {
	b := build(t, sampleEdition())
	root := b.doc(t, "META-INF/container.xml")
	if root.name.Local != "container" {
		t.Fatalf("root = %q, want container", root.name.Local)
	}
	rootfiles := one(t, "rootfiles", root.childrenByTag("rootfiles"))
	rf := one(t, "rootfile", rootfiles.childrenByTag("rootfile"))
	if got := rf.attr("", "full-path"); got != "OEBPS/content.opf" {
		t.Errorf("full-path = %q, want OEBPS/content.opf", got)
	}
	if got := rf.attr("", "media-type"); got != "application/oebps-package+xml" {
		t.Errorf("media-type = %q, want application/oebps-package+xml", got)
	}
}

// ---------------------------------------------------------------------------
// §10 Encoding & well-formedness
// ---------------------------------------------------------------------------

func TestXMLDocumentsWellFormed(t *testing.T) {
	editions := map[string]Edition{
		"sample":  sampleEdition(),
		"hostile": hostileEdition(),
		"empty":   emptyEdition(),
	}
	const decl = `<?xml version="1.0" encoding="UTF-8"?>`
	for label, ed := range editions {
		t.Run(label, func(t *testing.T) {
			b := build(t, ed)
			for _, name := range b.names {
				if !isXMLEntry(name) {
					continue
				}
				s := b.files[name]
				if !utf8.ValidString(s) {
					t.Errorf("%s: not valid UTF-8", name)
				}
				if !strings.HasPrefix(s, decl) {
					t.Errorf("%s: does not start with %s", name, decl)
				}
				root := parseXML(t, name, s)
				if strings.HasSuffix(name, ".xhtml") {
					if !strings.Contains(s, "<!DOCTYPE html>") {
						t.Errorf("%s: missing <!DOCTYPE html>", name)
					}
					if root.name.Space != nsXHTML || root.name.Local != "html" {
						t.Errorf("%s: root = {%s}%s, want {%s}html", name, root.name.Space, root.name.Local, nsXHTML)
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// §5 Package document
// ---------------------------------------------------------------------------

func TestPackageDocument(t *testing.T) {
	ed := sampleEdition()
	b := build(t, ed)
	pkg := b.doc(t, "OEBPS/content.opf")

	if pkg.name.Local != "package" {
		t.Fatalf("root = %q, want package", pkg.name.Local)
	}
	for _, a := range []struct{ space, local, want string }{
		{"", "version", "3.0"},
		{"", "unique-identifier", "uid"},
		{nsXML, "lang", "en"},
	} {
		if got := pkg.attr(a.space, a.local); got != a.want {
			t.Errorf("package @%s = %q, want %q", a.local, got, a.want)
		}
	}

	meta := one(t, "metadata", pkg.childrenByTag("metadata"))
	dc := func(local string) *node {
		t.Helper()
		return one(t, "dc:"+local, meta.find(func(n *node) bool {
			return n.name.Space == nsDC && n.name.Local == local
		}))
	}
	id := dc("identifier")
	if got := id.attr("", "id"); got != "uid" {
		t.Errorf("dc:identifier @id = %q, want uid", got)
	}
	for _, c := range []struct {
		el   *node
		want string
	}{
		{id, "urn:uuid:" + goldenUUID},
		{dc("title"), "Diario · 2026-11-26"},
		{dc("language"), "en"},
		{dc("creator"), "Diario"},
		{dc("date"), "2026-11-26"},
	} {
		if got := norm(c.el.textContent()); got != c.want {
			t.Errorf("dc:%s = %q, want %q", c.el.name.Local, got, c.want)
		}
	}
	modified := one(t, `meta[property="dcterms:modified"]`, meta.find(func(n *node) bool {
		return n.name.Local == "meta" && n.attr("", "property") == "dcterms:modified"
	}))
	if got := norm(modified.textContent()); got != "2026-11-26T08:45:00Z" {
		t.Errorf("dcterms:modified = %q, want 2026-11-26T08:45:00Z (UTC)", got)
	}

	items, _ := manifest(t, b)
	want := map[string]manifestItem{
		"nav":   {"nav.xhtml", "application/xhtml+xml", "nav"},
		"ncx":   {"toc.ncx", "application/x-dtbncx+xml", ""},
		"css":   {"style.css", "text/css", ""},
		"front": {"front.xhtml", "application/xhtml+xml", ""},
		"a01":   {"a01.xhtml", "application/xhtml+xml", ""},
		"a02":   {"a02.xhtml", "application/xhtml+xml", ""},
		"a03":   {"a03.xhtml", "application/xhtml+xml", ""},
	}
	if len(items) != len(want) {
		t.Errorf("manifest has %d items, want %d: %v", len(items), len(want), items)
	}
	for id, w := range want {
		if got, ok := items[id]; !ok {
			t.Errorf("manifest missing id %q", id)
		} else if got != w {
			t.Errorf("manifest %q = %+v, want %+v", id, got, w)
		}
	}

	spine := one(t, "spine", pkg.childrenByTag("spine"))
	if got := spine.attr("", "toc"); got != "ncx" {
		t.Errorf("spine @toc = %q, want ncx", got)
	}
	var idrefs []string
	for _, ref := range spine.childrenByTag("itemref") {
		idrefs = append(idrefs, ref.attr("", "idref"))
	}
	if wantRefs := []string{"front", "a01", "a02", "a03"}; !slices.Equal(idrefs, wantRefs) {
		t.Errorf("spine idrefs = %q, want %q (nav must not be in the spine)", idrefs, wantRefs)
	}

	// No landmarks: the book must open at the front page.
	if len(pkg.childrenByTag("guide")) != 0 {
		t.Errorf("content.opf has a <guide>; spec says no landmarks")
	}
}

// ---------------------------------------------------------------------------
// §6 Navigation
// ---------------------------------------------------------------------------

func TestNavXHTML(t *testing.T) {
	ed := sampleEdition()
	b := build(t, ed)
	root := b.doc(t, "OEBPS/nav.xhtml")

	nav := one(t, `nav[epub:type="toc"]`, root.find(func(n *node) bool {
		return n.name.Local == "nav" && n.attr(nsEPUB, "type") == "toc"
	}))
	if got := nav.attr("", "id"); got != "toc" {
		t.Errorf("nav @id = %q, want toc", got)
	}
	if got := norm(one(t, "nav > h1", nav.childrenByTag("h1")).textContent()); got != "Índice" {
		t.Errorf("nav h1 = %q, want Índice", got)
	}

	hrefs, labels := navTOC(t, b)
	wantHrefs := []string{"front.xhtml", "a01.xhtml", "a02.xhtml", "a03.xhtml"}
	wantLabels := []string{"Portada", "First article", "Second article", "Third article"}
	if !slices.Equal(hrefs, wantHrefs) {
		t.Errorf("nav hrefs = %q, want %q", hrefs, wantHrefs)
	}
	if !slices.Equal(labels, wantLabels) {
		t.Errorf("nav labels = %q, want %q (Article.Title, not Candidate.Title)", labels, wantLabels)
	}
}

func TestTocNCX(t *testing.T) {
	b := build(t, sampleEdition())
	root := b.doc(t, "OEBPS/toc.ncx")

	head := one(t, "head", root.childrenByTag("head"))
	metas := map[string]string{}
	for _, m := range head.childrenByTag("meta") {
		metas[m.attr("", "name")] = m.attr("", "content")
	}
	for name, want := range map[string]string{
		"dtb:uid":            "urn:uuid:" + goldenUUID,
		"dtb:depth":          "1",
		"dtb:totalPageCount": "0",
		"dtb:maxPageNumber":  "0",
	} {
		if got, ok := metas[name]; !ok || got != want {
			t.Errorf("ncx meta %s = %q (present=%v), want %q", name, got, ok, want)
		}
	}

	docTitle := one(t, "docTitle", root.childrenByTag("docTitle"))
	if got := norm(one(t, "docTitle > text", docTitle.childrenByTag("text")).textContent()); got != "Diario · 2026-11-26" {
		t.Errorf("docTitle = %q, want FullTitle", got)
	}

	want := []navPoint{
		{"np-front", "1", "Portada", "front.xhtml"},
		{"np-0", "2", "First article", "a01.xhtml"},
		{"np-1", "3", "Second article", "a02.xhtml"},
		{"np-2", "4", "Third article", "a03.xhtml"},
	}
	if got := ncxNavPoints(t, b); !slices.Equal(got, want) {
		t.Errorf("navPoints =\n  %+v\nwant\n  %+v", got, want)
	}
}

// Manifest, spine, nav.xhtml and toc.ncx must list the same chapters in the same order.
func TestNavigationMatchesSpine(t *testing.T) {
	for _, n := range []int{0, 1, 3, 12} {
		t.Run(fmt.Sprintf("%d articles", n), func(t *testing.T) {
			ed := sampleEdition()
			ed.Articles = manyArticles(n)
			b := build(t, ed)

			spine := spineHrefs(t, b)
			want := []string{"front.xhtml"}
			for i := range n {
				want = append(want, chapterFile(i))
			}
			if !slices.Equal(spine, want) {
				t.Fatalf("spine = %q, want %q", spine, want)
			}

			navHrefs, _ := navTOC(t, b)
			if !slices.Equal(navHrefs, spine) {
				t.Errorf("nav.xhtml hrefs = %q, spine = %q", navHrefs, spine)
			}

			var ncxSrcs []string
			for _, np := range ncxNavPoints(t, b) {
				ncxSrcs = append(ncxSrcs, np.src)
			}
			if !slices.Equal(ncxSrcs, spine) {
				t.Errorf("toc.ncx srcs = %q, spine = %q", ncxSrcs, spine)
			}

			// Every chapter in the manifest exists in the zip, and vice versa.
			items, _ := manifest(t, b)
			var manifestChapters, zipChapters []string
			for id, it := range items {
				if strings.HasPrefix(id, "a") {
					manifestChapters = append(manifestChapters, "OEBPS/"+it.href)
				}
			}
			for _, name := range b.names {
				if regexp.MustCompile(`^OEBPS/a\d\d\.xhtml$`).MatchString(name) {
					zipChapters = append(zipChapters, name)
				}
			}
			slices.Sort(manifestChapters)
			if !slices.Equal(manifestChapters, zipChapters) {
				t.Errorf("manifest chapters = %q, zip chapters = %q", manifestChapters, zipChapters)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// §7 Front page
// ---------------------------------------------------------------------------

func TestFrontPage(t *testing.T) {
	ed := sampleEdition()
	b := build(t, ed)
	root := b.doc(t, "OEBPS/front.xhtml")

	masthead := one(t, ".masthead", root.byClass("masthead"))
	date := one(t, ".date", root.byClass("date"))
	stats := one(t, ".stats", root.byClass("stats"))
	headlines := one(t, ".headlines", root.byClass("headlines"))
	hrs := root.byTag("hr")

	for _, c := range []struct {
		what string
		el   *node
		tag  string
		text string
	}{
		{"masthead", masthead, "h1", "Diario"},
		{"date", date, "p", "jueves 26 de noviembre de 2026"},
		// 1 + 14 + 60 = 75 minutes.
		{"stats", stats, "p", "3 artículos · 1 h 15 min de lectura"},
	} {
		if c.el.name.Local != c.tag {
			t.Errorf("%s: tag = %q, want %q", c.what, c.el.name.Local, c.tag)
		}
		if got := norm(c.el.textContent()); got != c.text {
			t.Errorf("%s = %q, want %q", c.what, got, c.text)
		}
	}
	if headlines.name.Local != "ol" {
		t.Errorf("headlines tag = %q, want ol", headlines.name.Local)
	}
	if len(hrs) == 0 {
		t.Fatalf("front page has no <hr/>")
	}

	// Top to bottom: masthead, date, stats, hr, headlines.
	if o := []int{masthead.order, date.order, stats.order, hrs[0].order, headlines.order}; !slices.IsSorted(o) {
		t.Errorf("front page order (masthead, date, stats, hr, headlines) = %v, want ascending", o)
	}

	want := []struct{ href, title, meta, why string }{
		{"a01.xhtml", "First article", "example.com · 1 min", "Explica bien el tema"},
		{"a02.xhtml", "Second article", "blog.example.org · 14 min", ""},
		{"a03.xhtml", "Third article", "third.example.net · 1 h 00 min", "Corto y claro"},
	}
	items := headlines.childrenByTag("li")
	if len(items) != len(want) {
		t.Fatalf("headlines has %d items, want %d", len(items), len(want))
	}
	for i, li := range items {
		w := want[i]
		a := one(t, "headline link", li.childrenByTag("a"))
		if got := a.attr("", "href"); got != w.href {
			t.Errorf("headline %d href = %q, want %q", i, got, w.href)
		}
		if got := norm(a.textContent()); got != w.title {
			t.Errorf("headline %d title = %q, want %q (Article.Title)", i, got, w.title)
		}
		meta := one(t, "headline .meta", li.byClass("meta"))
		if meta.name.Local != "span" {
			t.Errorf("headline %d .meta tag = %q, want span", i, meta.name.Local)
		}
		if got := norm(meta.textContent()); got != w.meta {
			t.Errorf("headline %d meta = %q, want %q", i, got, w.meta)
		}
		whys := li.byClass("why")
		switch {
		case w.why == "" && len(whys) != 0:
			t.Errorf("headline %d has .why but Reason is empty", i)
		case w.why != "" && len(whys) != 1:
			t.Errorf("headline %d: %d .why elements, want 1", i, len(whys))
		case w.why != "":
			if whys[0].name.Local != "span" {
				t.Errorf("headline %d .why tag = %q, want span", i, whys[0].name.Local)
			}
			if got := norm(whys[0].textContent()); got != w.why {
				t.Errorf("headline %d why = %q, want %q", i, got, w.why)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// §8 Chapters
// ---------------------------------------------------------------------------

func TestChapters(t *testing.T) {
	ed := sampleEdition()
	b := build(t, ed)

	want := []struct {
		lang  string
		meta  string
		why   string
		src   string
		links []string
	}{
		{
			lang:  "en-US",
			meta:  "Ada Lovelace · Example Blog · example.com · 1 min",
			why:   "Explica bien el tema",
			src:   "Original: example.com · discusión (hn)",
			links: []string{"https://www.example.com/post/1", "https://news.ycombinator.com/item?id=1"},
		},
		{
			lang:  "en", // "english!" is invalid
			meta:  "blog.example.org · 14 min",
			src:   "Original: blog.example.org",
			links: []string{"https://blog.example.org/b"},
		},
		{
			lang:  "en", // empty
			meta:  "Third Site · third.example.net · 1 h 00 min",
			why:   "Corto y claro",
			src:   "Original: third.example.net · discusión (lobsters)",
			links: []string{"https://third.example.net/c", "https://lobste.rs/s/abc"},
		},
	}

	for i, w := range want {
		a := ed.Articles[i]
		name := "OEBPS/" + chapterFile(i)
		t.Run(chapterFile(i), func(t *testing.T) {
			root := b.doc(t, name)

			if got := root.attr(nsXML, "lang"); got != w.lang {
				t.Errorf("html @xml:lang = %q, want %q", got, w.lang)
			}
			if got := root.attr("", "lang"); got != w.lang {
				t.Errorf("html @lang = %q, want %q", got, w.lang)
			}

			head := one(t, "head", root.childrenByTag("head"))
			if got := norm(one(t, "title", head.childrenByTag("title")).textContent()); got != a.Title {
				t.Errorf("<title> = %q, want %q", got, a.Title)
			}
			h1 := one(t, "h1 (must be the only one)", root.byTag("h1"))
			if got := norm(h1.textContent()); got != a.Title {
				t.Errorf("<h1> = %q, want %q", got, a.Title)
			}

			meta := one(t, "p.meta", root.byClass("meta"))
			if meta.name.Local != "p" {
				t.Errorf(".meta tag = %q, want p", meta.name.Local)
			}
			if got := norm(meta.textContent()); got != w.meta {
				t.Errorf("meta = %q, want %q", got, w.meta)
			}

			whys := root.byClass("why")
			if w.why == "" {
				if len(whys) != 0 {
					t.Errorf("has .why but Reason is empty")
				}
			} else {
				why := one(t, "p.why", whys)
				if why.name.Local != "p" {
					t.Errorf(".why tag = %q, want p", why.name.Local)
				}
				if got := norm(why.textContent()); got != w.why {
					t.Errorf("why = %q, want %q", got, w.why)
				}
			}

			body := one(t, "div.body", root.byClass("body"))
			if body.name.Local != "div" {
				t.Errorf(".body tag = %q, want div", body.name.Local)
			}
			if !strings.Contains(b.files[name], a.XHTML) {
				t.Errorf("Article.XHTML not inserted verbatim:\n%s", b.files[name])
			}
			marker := one(t, "body marker", root.find(func(n *node) bool {
				return n.attr("", "id") == fmt.Sprintf("m%d", i+1)
			}))
			if !body.contains(marker) {
				t.Errorf("Article.XHTML is not inside div.body")
			}

			src := one(t, "p.src", root.byClass("src"))
			if src.name.Local != "p" {
				t.Errorf(".src tag = %q, want p", src.name.Local)
			}
			if got := norm(src.textContent()); got != w.src {
				t.Errorf("src = %q, want %q", got, w.src)
			}
			var links []string
			for _, l := range src.byTag("a") {
				links = append(links, l.attr("", "href"))
			}
			if !slices.Equal(links, w.links) {
				t.Errorf("src links = %q, want %q", links, w.links)
			}

			hrs := root.byTag("hr")
			if len(hrs) == 0 {
				t.Fatalf("no <hr/> before p.src")
			}
			hr := hrs[len(hrs)-1]
			order := []int{h1.order, meta.order}
			if len(whys) == 1 {
				order = append(order, whys[0].order)
			}
			order = append(order, body.order, hr.order, src.order)
			if !slices.IsSorted(order) {
				t.Errorf("chapter order (h1, meta, [why], body, hr, src) = %v, want ascending", order)
			}
		})
	}
}

func TestChapterLanguageFallback(t *testing.T) {
	tests := []struct {
		lang string
		want string
	}{
		{"en", "en"},
		{"en-US", "en-US"},
		{"ast", "ast"},
		{"zh-Hant-TW", "zh-Hant-TW"},
		{"de-1996", "de-1996"},
		{"english!", "fr"}, // spec §12 example
		{"", "fr"},
		{"e", "fr"},
		{"english", "fr"},
		{"en_US", "fr"},
		{"en-", "fr"},
		{"en-a", "fr"},
		{"en-abcdefghi", "fr"},
		{` en`, "fr"},
		{`en" onload="x`, "fr"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.lang), func(t *testing.T) {
			ed := sampleEdition()
			ed.Language = "fr" // distinct from any article value, so the fallback is visible
			ed.Articles = ed.Articles[:1]
			ed.Articles[0].Language = tt.lang
			b := build(t, ed)
			root := b.doc(t, "OEBPS/a01.xhtml")
			if got := root.attr(nsXML, "lang"); got != tt.want {
				t.Errorf("@xml:lang = %q, want %q", got, tt.want)
			}
			if got := root.attr("", "lang"); got != tt.want {
				t.Errorf("@lang = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// §10 Escaping
// ---------------------------------------------------------------------------

func TestHostileStringsAreEscaped(t *testing.T) {
	ed := hostileEdition()
	a := ed.Articles[0]
	b := build(t, ed)

	// Never raw anywhere, in any entry.
	for _, name := range b.names {
		for _, bad := range hostileRaw {
			if strings.Contains(b.files[name], bad) {
				t.Errorf("%s contains raw %q", name, bad)
			}
		}
	}

	// And it decodes back to the original text (minus invalid XML chars).
	check := func(what, got, want string) {
		t.Helper()
		if norm(got) != norm(clean(want)) {
			t.Errorf("%s = %q, want %q", what, norm(got), norm(clean(want)))
		}
	}

	pkg := b.doc(t, "OEBPS/content.opf")
	metadata := one(t, "metadata", pkg.childrenByTag("metadata"))
	dc := func(local string) string {
		return one(t, "dc:"+local, metadata.find(func(n *node) bool {
			return n.name.Space == nsDC && n.name.Local == local
		})).textContent()
	}
	check("dc:title", dc("title"), ed.FullTitle())
	check("dc:creator", dc("creator"), ed.Title)

	ncx := b.doc(t, "OEBPS/toc.ncx")
	docTitle := one(t, "docTitle", ncx.childrenByTag("docTitle"))
	check("ncx docTitle", docTitle.textContent(), ed.FullTitle())
	check("ncx navPoint label", ncxNavPoints(t, b)[1].label, a.Title)

	_, labels := navTOC(t, b)
	check("nav label", labels[1], a.Title)

	front := b.doc(t, "OEBPS/front.xhtml")
	check("masthead", one(t, ".masthead", front.byClass("masthead")).textContent(), ed.Title)
	li := one(t, "first headline", front.byClass("headlines")[0].childrenByTag("li")[:1])
	check("headline", li.childrenByTag("a")[0].textContent(), a.Title)
	check("headline why", one(t, "headline .why", li.byClass("why")).textContent(), a.Reason)

	ch := b.doc(t, "OEBPS/a01.xhtml")
	head := one(t, "head", ch.childrenByTag("head"))
	check("chapter title", one(t, "title", head.childrenByTag("title")).textContent(), a.Title)
	check("chapter h1", one(t, "h1", ch.byTag("h1")).textContent(), a.Title)
	check("chapter why", one(t, "p.why", ch.byClass("why")).textContent(), a.Reason)
	if meta := norm(one(t, "p.meta", ch.byClass("meta")).textContent()); !strings.Contains(meta, norm(clean(a.Byline))) {
		t.Errorf("chapter meta = %q, want it to contain byline %q", meta, norm(clean(a.Byline)))
	}
	links := one(t, "p.src", ch.byClass("src")).byTag("a")
	if len(links) != 2 {
		t.Fatalf("p.src has %d links, want 2", len(links))
	}
	if got := links[0].attr("", "href"); got != clean(a.URL) {
		t.Errorf("source href = %q, want %q", got, clean(a.URL))
	}
	if got := links[1].attr("", "href"); got != clean(a.Discussion) {
		t.Errorf("discussion href = %q, want %q", got, clean(a.Discussion))
	}
}

// ---------------------------------------------------------------------------
// §1 Determinism & concurrency
// ---------------------------------------------------------------------------

func TestBytesDeterministic(t *testing.T) {
	ed := sampleEdition()
	first, err := Bytes(ed)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Bytes(ed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("Bytes is not deterministic: %d vs %d bytes", len(first), len(second))
	}

	var buf bytes.Buffer
	if err := Write(&buf, ed); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), first) {
		t.Errorf("Write and Bytes produce different output")
	}
}

func TestConcurrentWrites(t *testing.T) {
	ed := sampleEdition()
	want, err := Bytes(ed)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			got, err := Bytes(ed)
			if err != nil {
				t.Error(err)
				return
			}
			if !bytes.Equal(got, want) {
				t.Error("concurrent Bytes produced different output")
			}
		})
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// §2, §5.1, §9 Goldens
// ---------------------------------------------------------------------------

func TestFullTitle(t *testing.T) {
	tests := []struct {
		title string
		date  time.Time
		want  string
	}{
		{"Diario", editionDate, "Diario · 2026-11-26"},
		// 23:30 at UTC-3 is already the 27th in UTC: the edition's own location wins.
		{"Diario", time.Date(2026, 11, 26, 23, 30, 0, 0, art), "Diario · 2026-11-26"},
		{"Mi Diario", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), "Mi Diario · 2027-01-01"},
	}
	for _, tt := range tests {
		if got := (Edition{Title: tt.title, Date: tt.date}).FullTitle(); got != tt.want {
			t.Errorf("FullTitle(%q, %v) = %q, want %q", tt.title, tt.date, got, tt.want)
		}
	}
}

func TestFilename(t *testing.T) {
	tests := []struct {
		title string
		date  time.Time
		want  string
	}{
		{"Diario", editionDate, "diario-2026-11-26.epub"},
		{"Mi  Diario", editionDate, "mi-diario-2026-11-26.epub"},
		{"DIARIO", editionDate, "diario-2026-11-26.epub"},
		{"Mi\tDiario \n Hoy", editionDate, "mi-diario-hoy-2026-11-26.epub"},
		{"Diario", time.Date(2026, 11, 26, 23, 30, 0, 0, art), "diario-2026-11-26.epub"},
	}
	for _, tt := range tests {
		if got := (Edition{Title: tt.title, Date: tt.date}).Filename(); got != tt.want {
			t.Errorf("Filename(%q, %v) = %q, want %q", tt.title, tt.date, got, tt.want)
		}
	}
}

func opfIdentifier(t *testing.T, ed Edition) string {
	t.Helper()
	b := build(t, ed)
	pkg := b.doc(t, "OEBPS/content.opf")
	id := one(t, "dc:identifier", pkg.find(func(n *node) bool {
		return n.name.Space == nsDC && n.name.Local == "identifier"
	}))
	return norm(id.textContent())
}

var uuidV5 = regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestIdentifier(t *testing.T) {
	ed := sampleEdition()
	got := opfIdentifier(t, ed)
	if want := "urn:uuid:" + goldenUUID; got != want {
		t.Errorf("identifier = %q, want golden %q", got, want)
	}

	// Stable per edition: a re-run later the same day, or with other articles, keeps it.
	rerun := sampleEdition()
	rerun.Date = time.Date(2026, 11, 26, 21, 0, 0, 0, art)
	rerun.Articles = rerun.Articles[:1]
	if again := opfIdentifier(t, rerun); again != got {
		t.Errorf("identifier changed for the same edition day: %q vs %q", again, got)
	}

	// Another day is another document.
	next := sampleEdition()
	next.Date = editionDate.AddDate(0, 0, 1)
	other := opfIdentifier(t, next)
	if other == got {
		t.Errorf("identifier did not change for a different day: %q", other)
	}
	for _, id := range []string{got, other} {
		if !uuidV5.MatchString(id) {
			t.Errorf("identifier %q is not a version-5 layout UUID", id)
		}
	}
}

func TestLongDate(t *testing.T) {
	tests := []struct {
		date time.Time
		want string
	}{
		// Rows from spec §9.
		{time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC), "jueves 26 de noviembre de 2026"},
		{time.Date(2026, 11, 29, 0, 0, 0, 0, time.UTC), "domingo 29 de noviembre de 2026"},
		{time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), "viernes 1 de enero de 2027"},
		// Remaining weekdays.
		{time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC), "lunes 23 de noviembre de 2026"},
		{time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC), "martes 24 de noviembre de 2026"},
		{time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC), "miércoles 25 de noviembre de 2026"},
		{time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC), "sábado 28 de noviembre de 2026"},
		// Uses the time's own location, not UTC.
		{time.Date(2026, 11, 26, 23, 30, 0, 0, art), "jueves 26 de noviembre de 2026"},
	}
	for _, tt := range tests {
		if got := LongDate(tt.date); got != tt.want {
			t.Errorf("LongDate(%v) = %q, want %q", tt.date, got, tt.want)
		}
	}

	months := []string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio",
		"agosto", "septiembre", "octubre", "noviembre", "diciembre"}
	for i, m := range months {
		d := time.Date(2026, time.Month(i+1), 15, 12, 0, 0, 0, time.UTC)
		if got, want := LongDate(d), " 15 de "+m+" de 2026"; !strings.HasSuffix(got, want) {
			t.Errorf("LongDate(%v) = %q, want suffix %q", d, got, want)
		}
	}
}

func TestFormatMinutes(t *testing.T) {
	tests := []struct {
		m    int
		want string
	}{
		// Rows from spec §9.
		{1, "1 min"},
		{45, "45 min"},
		{60, "1 h 00 min"},
		{105, "1 h 45 min"},
		// Edges around the hour and minute padding.
		{59, "59 min"},
		{61, "1 h 01 min"},
		{125, "2 h 05 min"},
	}
	for _, tt := range tests {
		if got := fmtMinutes(tt.m); got != tt.want {
			t.Errorf("fmtMinutes(%d) = %q, want %q", tt.m, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// §3 Errors and limits
// ---------------------------------------------------------------------------

func TestEmptyEdition(t *testing.T) {
	ed := emptyEdition()
	var buf bytes.Buffer
	if err := Write(&buf, ed); err != nil {
		t.Fatalf("Write with 0 articles: %v (must not be an error)", err)
	}
	b := build(t, ed)

	want := []string{
		"mimetype",
		"META-INF/container.xml",
		"OEBPS/content.opf",
		"OEBPS/toc.ncx",
		"OEBPS/nav.xhtml",
		"OEBPS/style.css",
		"OEBPS/front.xhtml",
	}
	if !slices.Equal(b.names, want) {
		t.Errorf("entries = %q, want %q", b.names, want)
	}
	if got := spineHrefs(t, b); !slices.Equal(got, []string{"front.xhtml"}) {
		t.Errorf("spine = %q, want only front.xhtml", got)
	}
	front := b.doc(t, "OEBPS/front.xhtml")
	if items := front.byTag("li"); len(items) != 0 {
		t.Errorf("front page has %d headlines, want 0", len(items))
	}
}

// countingWriter records how much was written to it.
type countingWriter struct{ n int }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	return len(p), nil
}

func TestArticleLimit(t *testing.T) {
	t.Run("99 is allowed", func(t *testing.T) {
		ed := sampleEdition()
		ed.Articles = manyArticles(99)
		b := build(t, ed)
		if last := b.names[len(b.names)-1]; last != "OEBPS/a99.xhtml" {
			t.Errorf("last entry = %q, want OEBPS/a99.xhtml", last)
		}
		items, ids := manifest(t, b)
		if len(ids) != len(items) {
			t.Errorf("manifest ids collide: %d items, %d unique", len(ids), len(items))
		}
		if len(items) != 4+99 {
			t.Errorf("manifest has %d items, want %d", len(items), 4+99)
		}
		for i := range 99 {
			if _, ok := items[chapterID(i)]; !ok {
				t.Errorf("manifest missing %s", chapterID(i))
			}
		}
	})

	t.Run("100 is an error and writes nothing", func(t *testing.T) {
		ed := sampleEdition()
		ed.Articles = manyArticles(100)
		var w countingWriter
		if err := Write(&w, ed); err == nil {
			t.Errorf("Write with 100 articles: got nil error")
		}
		if w.n != 0 {
			t.Errorf("Write with 100 articles wrote %d bytes, want 0", w.n)
		}
		if _, err := Bytes(ed); err == nil {
			t.Errorf("Bytes with 100 articles: got nil error")
		}
	})
}

var errBoom = errors.New("boom")

// failingWriter accepts limit bytes, then fails.
type failingWriter struct{ limit, n int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.n+len(p) > w.limit {
		k := w.limit - w.n
		w.n = w.limit
		return k, errBoom
	}
	w.n += len(p)
	return len(p), nil
}

func TestWriterErrorIsReturned(t *testing.T) {
	ed := sampleEdition()
	full, err := Bytes(ed)
	if err != nil {
		t.Fatal(err)
	}
	// Fail at the start, in the middle and on the very last byte (the
	// central directory, written on Close).
	for _, limit := range []int{0, len(full) / 2, len(full) - 1} {
		t.Run(fmt.Sprintf("fail after %d bytes", limit), func(t *testing.T) {
			err := Write(&failingWriter{limit: limit}, ed)
			if !errors.Is(err, errBoom) {
				t.Errorf("Write error = %v, want it to wrap %v", err, errBoom)
			}
		})
	}
}

// A writer error that surfaces while a template is executing is wrapped with
// the entry name (§3). The zip/flate layers buffer small entries, so the
// chapter needs a large, poorly compressible body to force writes mid-template.
func TestWriterErrorNamesEntry(t *testing.T) {
	var body strings.Builder
	body.WriteString("<p>")
	// xorshift32: deterministic, cheap, and defeats deflate.
	for x := uint32(1); body.Len() < 1<<20; {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		fmt.Fprintf(&body, "%08x", x)
	}
	body.WriteString("</p>")

	ed := sampleEdition()
	ed.Articles = ed.Articles[:1]
	ed.Articles[0].XHTML = body.String()
	full, err := Bytes(ed)
	if err != nil {
		t.Fatal(err)
	}

	err = Write(&failingWriter{limit: len(full) / 2}, ed)
	if !errors.Is(err, errBoom) {
		t.Fatalf("Write error = %v, want it to wrap %v", err, errBoom)
	}
	if !strings.Contains(err.Error(), "OEBPS/a01.xhtml") {
		t.Errorf("Write error = %q, want it to name OEBPS/a01.xhtml", err)
	}
}

// ---------------------------------------------------------------------------
// §9 Styling
// ---------------------------------------------------------------------------

func TestStyleIsKindleSafe(t *testing.T) {
	b := build(t, sampleEdition())
	css := strings.ToLower(b.files["OEBPS/style.css"])
	if strings.TrimSpace(css) == "" {
		t.Fatalf("style.css is empty")
	}
	for _, bad := range []string{"@font-face", "@import", "url(", "position", "float"} {
		if strings.Contains(css, bad) {
			t.Errorf("style.css contains %q, which is not Kindle-safe", bad)
		}
	}
}

// Generated markup only uses the classes listed in §9. Article bodies are
// excluded: their markup comes from extract.Sanitize.
func TestOnlyKnownClasses(t *testing.T) {
	known := map[string]bool{
		"masthead": true, "date": true, "stats": true, "meta": true,
		"why": true, "src": true, "headlines": true, "body": true,
	}
	b := build(t, sampleEdition())
	for _, name := range b.names {
		if !strings.HasSuffix(name, ".xhtml") {
			continue
		}
		var walk func(*node)
		walk = func(n *node) {
			for _, c := range strings.Fields(n.attr("", "class")) {
				if !known[c] {
					t.Errorf("%s: unknown class %q on <%s>", name, c, n.name.Local)
				}
			}
			if n.name.Local == "div" && n.hasClass("body") {
				return
			}
			for _, c := range n.elements() {
				walk(c)
			}
		}
		walk(b.doc(t, name))
	}
}

// ---------------------------------------------------------------------------
// epubcheck (§12): runs only when epubcheck is installed; never touches the network.
// ---------------------------------------------------------------------------

func TestEpubcheck(t *testing.T) {
	bin, err := exec.LookPath("epubcheck")
	if err != nil {
		t.Skip("epubcheck not installed")
	}
	for label, ed := range map[string]Edition{
		"sample":  sampleEdition(),
		"hostile": hostileEdition(),
		"empty":   emptyEdition(),
	} {
		t.Run(label, func(t *testing.T) {
			raw, err := Bytes(ed)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), label+".epub")
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(bin, path).CombinedOutput()
			if err != nil {
				t.Fatalf("epubcheck failed: %v\n%s", err, out)
			}
			// 0 fatals / 0 errors / 0 warnings.
			if !strings.Contains(string(out), "No errors or warnings detected") {
				t.Errorf("epubcheck reported problems:\n%s", out)
			}
		})
	}
}
