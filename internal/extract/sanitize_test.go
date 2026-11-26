package extract

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var testBase = mustURL("https://example.com/blog/post")

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// parseBody parses in as a full HTML document and returns its <body>.
func parseBody(t testing.TB, in string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if b := findTag(doc, atom.Body); b != nil {
		return b
	}
	return doc
}

func findTag(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findTag(c, a); f != nil {
			return f
		}
	}
	return nil
}

type sanitizeCase struct {
	name  string
	in    string
	want  string
	words int
}

func runSanitizeCases(t *testing.T, base *url.URL, cases []sanitizeCase) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, words := Sanitize(parseBody(t, tt.in), base)
			if got != tt.want {
				t.Errorf("Sanitize(%q)\n got: %q\nwant: %q", tt.in, got, tt.want)
			}
			if words != tt.words {
				t.Errorf("words = %d, want %d", words, tt.words)
			}
			checkOutput(t, got, words)
		})
	}
}

// checkOutput asserts the properties every Sanitize output must have.
func checkOutput(t *testing.T, out string, words int) {
	t.Helper()
	wellFormed(t, out)
	if t.Failed() {
		return
	}
	checkModel(t, out)
	if n := len(strings.Fields(textOf(t, out))); n != words {
		t.Errorf("words = %d, but textOf has %d fields: %q", words, n, out)
	}
	// Text can legitimately mention "id=" (code samples), so only tags are
	// searched. Text never contains a raw "<".
	for _, tag := range tagRe.FindAllString(out, -1) {
		for _, bad := range []string{"<h1", "<script", "<img", " id=", " class=", " style=", ` href="#`} {
			if strings.Contains(tag, bad) {
				t.Errorf("tag %s contains %q", tag, bad)
			}
		}
		if onAttrRe.MatchString(tag) {
			t.Errorf("tag %s has an on* attribute", tag)
		}
	}
}

var (
	tagRe    = regexp.MustCompile(`<[^>]*>`)
	onAttrRe = regexp.MustCompile(`\son[a-z]*=`)
)

// FS §6.5.
var fsExamples = []sanitizeCase{
	{"h1 becomes h2", `<h1>Big</h1>`, `<h2>Big</h2>`, 1},
	{"attributes stripped", `<p onclick="x()" style="color:red" id="p1" class="lead">Hi</p>`, `<p>Hi</p>`, 1},
	{"relative link resolved", `<a href="/rel">r</a>`, `<a href="https://example.com/rel">r</a>`, 1},
	{"fragment link unwrapped", `<a href="#fn1">n</a>`, `n`, 1},
	{"javascript link unwrapped", `<a href="javascript:x()">j</a>`, `j`, 1},
	{"mailto link unwrapped", `<a href="mailto:a@b.c">m</a>`, `m`, 1},
	{"script dropped", `<script>x()</script>t`, `t`, 1},
	{"img dropped", `<img src="x.png" alt="y">`, ``, 0},
	{"custom element unwrapped", `<custom-el>text</custom-el>`, `text`, 1},
	{"orphan li", `<li>orphan</li>`, `<p>orphan</p>`, 1},
	{"empty li dropped", `<ul><li>ok</li> <li></li></ul>`, `<ul><li>ok</li></ul>`, 1},
	{"ol start kept, type dropped", `<ol start="3" type="a"><li>c</li></ol>`, `<ol start="3"><li>c</li></ol>`, 1},
	{"whitespace collapsed", "<p>a    b\n c</p>", `<p>a b c</p>`, 3},
	{"pre preserved", "<pre>  a   b\n  c</pre>", "<pre>  a   b\n  c</pre>", 3},
	{"invalid char dropped", "<p>a\x0bb</p>", `<p>ab</p>`, 1},
	{"block in phrasing", `<section><em><div><p>x</p><hr></div></em></section>`, `<em> x <br/></em>`, 1},
	{"dl without dd", `<dl><dt>t</dt></dl>`, `<p>t</p>`, 1},
	{"valid dl", `<dl><dt>t</dt><dd>d</dd></dl>`, `<dl><dt>t</dt><dd>d</dd></dl>`, 2},
	{"empty td kept", `<table><tr><td></td><td>x</td></tr></table>`, `<table><tbody><tr><td></td><td>x</td></tr></tbody></table>`, 1},
	{"lonely figcaption", `<figcaption>lonely</figcaption>`, `<p>lonely</p>`, 1},
	{"empty paragraphs", `<p></p><p> </p>`, ``, 0},
}

func TestSanitizeExamples(t *testing.T) {
	runSanitizeCases(t, testBase, fsExamples)
}

// Technical spec §7.
var clarifications = []sanitizeCase{
	{"C-2 duplicate attribute", `<ol start="2" start="5"><li>a</li></ol>`, `<ol start="2"><li>a</li></ol>`, 1},
	{"C-3 invalid start", `<ol start="x"><li>a</li></ol>`, `<ol><li>a</li></ol>`, 1},
	{"C-4 stray text in ul", `<ul>x<li>a</li></ul>`, `<ul><li>x</li><li>a</li></ul>`, 2},
	{"C-4 stray block in ul", `<ul><p>p</p></ul>`, `<ul><li><p>p</p></li></ul>`, 1},
	{"C-5 late figcaption", `<figure><p>a</p><figcaption>c</figcaption></figure>`, `<figure><p>a</p><p>c</p></figure>`, 2},
	{"C-5 late caption", `<table><tr><td>a</td></tr><caption>c</caption></table>`, `<table><tbody><tr><td>a</td></tr></tbody></table>`, 1},
	{"C-6 head and title", `<html><head><title>T</title></head><body><p>b</p></body></html>`, `<p>b</p>`, 1},
	{"C-7 no separators in pre", `<pre>a<div>b</div></pre>`, `<pre>ab</pre>`, 1},
	{"C-8 div separators", `<div>a</div><div>b</div>`, `a b`, 2},
	{"C-8 span no separators", `<span>a</span><span>b</span>`, `ab`, 1},
	{"C-9 inline sub", `<p>H<sub>2</sub>O</p>`, `<p>H<sub>2</sub>O</p>`, 1},
}

func TestSanitizeClarifications(t *testing.T) {
	runSanitizeCases(t, testBase, clarifications)

	// C-1 and C-4 (tables) need trees html.Parse never builds.
	t.Run("C-1 nested a", func(t *testing.T) {
		outer := elem("a", attr("href", "/x"), text("1 "), elem("a", attr("href", "/y"), text("2")))
		got, words := Sanitize(elem("div", outer), testBase)
		if want := `<a href="https://example.com/x">1 2</a>`; got != want || words != 2 {
			t.Errorf("got %q (%d words), want %q", got, words, want)
		}
		checkOutput(t, got, words)
	})
	t.Run("C-4 stray table content", func(t *testing.T) {
		row := elem("tr", text("loose"), elem("div", text("z")), elem("td", text("a")), elem("br"))
		table := elem("table", text("x"), elem("p", text("y")), row, elem("tbody", elem("td", text("b"))))
		got, words := Sanitize(elem("div", table), testBase)
		if want := `<table><tr><td>a</td></tr></table>`; got != want || words != 1 {
			t.Errorf("got %q (%d words), want %q", got, words, want)
		}
		checkOutput(t, got, words)
	})
}

// Structure rules (R7, R10, C-4, C-5) beyond the spec examples.
func TestSanitizeStructure(t *testing.T) {
	runSanitizeCases(t, testBase, []sanitizeCase{
		{"orphan dt and dd", `<div><dt>t</dt><dd>d</dd></div>`, `<p>t</p><p>d</p>`, 2},
		{"dd outside dl", `<blockquote><dd>d</dd></blockquote>`, `<blockquote><p>d</p></blockquote>`, 1},
		{"dl with only dd", `<dl><dd>d</dd></dl>`, `<p>d</p>`, 1},
		{"dl with stray text", `<dl><dt>t</dt>stray</dl>`, `<dl><dt>t</dt><dd>stray</dd></dl>`, 2},
		{"dl with only text", `<dl>text</dl>`, `text`, 1},
		{"dl implicit dd closed by dt", `<dl>x<dt>t</dt><dd>d</dd></dl>`, `<dl><dd>x</dd><dt>t</dt><dd>d</dd></dl>`, 3},
		{"nested invalid dls", `<dl><dd><dl><dt>a</dt></dl></dd></dl>`, `<p>a</p>`, 1},
		{"invalid dl re-rendered once", `<dl><blockquote><dl><dt>a</dt></dl></blockquote></dl>`, `<blockquote><p>a</p></blockquote>`, 1},
		{"empty dl", `<dl><dt></dt><dd> </dd></dl>`, ``, 0},
		{"stray runs share one li", `<ul> <li>a</li> x <b>y</b> <li>b</li></ul>`, `<ul><li>a</li><li>x <b>y</b></li><li>b</li></ul>`, 4},
		{"br in ul", `<ul><br></ul>`, `<ul><li><br/></li></ul>`, 0},
		{"li inside unwrapped div", `<ul><div><li>a</li></div></ul>`, `<ul><li>a</li></ul>`, 1},
		{"ol with dd", `<ol><dd>x</dd></ol>`, `<ol><li><p>x</p></li></ol>`, 1},
		{"blank implicit li dropped", "<ul>\x0b<li>a</li></ul>", `<ul><li>a</li></ul>`, 1},
		{"li in phrasing", `<em><ul><li>a</li><li>b</li></ul></em>`, `<em> a b</em>`, 2},
		{"first figcaption kept", `<figure><figcaption>c</figcaption><p>a</p></figure>`, `<figure><figcaption>c</figcaption><p>a</p></figure>`, 2},
		{"figcaption after text", `<figure>t<figcaption>c</figcaption></figure>`, `<figure>t<p>c</p></figure>`, 2},
		{"image-only figure", `<figure><img src="x"><figcaption>c</figcaption></figure>`, `<figure><figcaption>c</figcaption></figure>`, 1},
		{"thead and tfoot", `<table><thead><tr><th>h</th></tr></thead><tbody><tr><td>b</td></tr></tbody><tfoot><tr><td>f</td></tr></tfoot></table>`,
			`<table><thead><tr><th>h</th></tr></thead><tbody><tr><td>b</td></tr></tbody><tfoot><tr><td>f</td></tr></tfoot></table>`, 3},
		{"caption first", `<table><caption>c</caption><tr><td>a</td></tr></table>`, `<table><caption>c</caption><tbody><tr><td>a</td></tr></tbody></table>`, 2},
		{"empty table dropped", `<table><tr><td></td></tr></table>`, ``, 0},
		{"colgroup unwrapped", `<table><colgroup><col></colgroup><tr><td>a</td></tr></table>`, `<table><tbody><tr><td>a</td></tr></tbody></table>`, 1},
		{"nested list", `<ul><li>a<ul><li>b</li></ul></li></ul>`, `<ul><li>a<ul><li>b</li></ul></li></ul>`, 2},
	})
}

// Hostile inputs only need to satisfy the output properties.
var hostile = []string{
	"<p>\x00\x01\x08\x0b\x0c\x1f\x7f\ufffe\uffff text</p>",
	"<p>\xc3\x28 \xed\xa0\x80 \xf0\x28\x8c\x28</p>",
	`<p onmouseover="x()" ONCLICK="y()">a</p><a href="JAVASCRIPT:alert(1)">j</a><a href=" java&#09;script:x">k</a>`,
	`<svg><foreignObject><p>x</p></foreignObject></svg><math><mtext><p>y</p></mtext></math>`,
	`<table><td>a<table><td>b</td></table></td><caption>c</caption><caption>d</caption></table>`,
	`<ul><li><dl><dt>a<ul>x</ul></dt></dl></li></ul><dl><figure><figcaption>z</figcaption></figure></dl>`,
	`<em><figure><figcaption>c</figcaption></figure><table><caption>q</caption></table></em>`,
	`<a href="/a"><table><tr><td><a href="/b">x</a></td></tr></table></a>`,
	`<pre><p>a</p><hr><ul><li>b</li></ul></pre>`,
	`<h2><h3>a</h3></h2><h6><dl><dt>x</dt><dd>y</dd></dl></h6>`,
	`<ol start="99999999999999999999"><li>a</li></ol><td colspan="-1">x</td>`,
	`<abbr title="&#0;&#x1;">a</abbr><q cite="javascript:x">q</q>`,
	// html.Parse refuses more than 512 open elements; deeper trees are
	// built by hand in TestSanitizeDeepTree.
	strings.Repeat("<div>", 500) + "deep" + strings.Repeat("</div>", 500),
	strings.Repeat("<em>", 500) + "deep" + strings.Repeat("</em>", 500),
	strings.Repeat("<dl><dd>", 250) + "x" + strings.Repeat("</dd></dl>", 250),
	strings.Repeat("<ul>", 500) + "x" + strings.Repeat("</ul>", 500),
}

func TestSanitizeOutputProperties(t *testing.T) {
	var inputs []string
	for _, group := range [][]sanitizeCase{fsExamples, clarifications} {
		for _, c := range group {
			inputs = append(inputs, c.in)
		}
	}
	inputs = append(inputs, hostile...)
	for i, in := range inputs {
		for _, base := range []*url.URL{testBase, nil} {
			doc, err := html.Parse(strings.NewReader(in))
			if err != nil {
				t.Fatal(err)
			}
			out, words := Sanitize(doc, base)
			t.Run(fmt.Sprintf("input %d", i), func(t *testing.T) { checkOutput(t, out, words) })
		}
	}
}

func TestSanitizeDeepTree(t *testing.T) {
	for _, tag := range []string{"div", "em", "blockquote"} {
		n := text("deep")
		for range 10000 {
			n = elem(tag, n)
		}
		out, words := Sanitize(elem("body", n), testBase)
		if words != 1 {
			t.Errorf("%s: words = %d", tag, words)
		}
		checkOutput(t, out, words)
	}
}

func elem(tag string, kids ...*html.Node) *html.Node {
	n := &html.Node{Type: html.ElementNode, Data: tag, DataAtom: atom.Lookup([]byte(tag))}
	for _, k := range kids {
		if k.Type == html.ErrorNode { // attribute carrier, see attr
			n.Attr = append(n.Attr, k.Attr...)
			continue
		}
		n.AppendChild(k)
	}
	return n
}

func attr(key, val string) *html.Node {
	return &html.Node{Type: html.ErrorNode, Attr: []html.Attribute{{Key: key, Val: val}}}
}

func text(s string) *html.Node { return &html.Node{Type: html.TextNode, Data: s} }

func TestSanitizeRules(t *testing.T) {
	runSanitizeCases(t, testBase, []sanitizeCase{
		// R1
		{"comment dropped", `<p>a<!-- c -->b</p>`, `<p>ab</p>`, 1},
		// R2
		{"svg foreign content dropped", `<p>a<svg><text>s</text></svg>b</p>`, `<p>ab</p>`, 1},
		{"math dropped", `<p>a <math><mi>x</mi></math> b</p>`, `<p>a b</p>`, 2},
		// R3
		{"style dropped", `<style>p{}</style><p>x</p>`, `<p>x</p>`, 1},
		{"nav and iframe dropped", `<nav>menu</nav><iframe src="x"></iframe><p>x</p>`, `<p>x</p>`, 1},
		{"form dropped", `<form><p>in</p><input value="v"></form><p>out</p>`, `<p>out</p>`, 1},
		// R4
		{"h1 in phrasing unwrapped", `<em>a<h1>b</h1></em>`, `<em>a b</em>`, 2},
		// R5
		{"a is transparent in phrasing", `<em><a href="/x">a<div>b</div></a></em>`, `<em><a href="https://example.com/x">a b</a></em>`, 2},
		{"block in a outside phrasing", `<a href="/x"><p>p</p></a>`, `<a href="https://example.com/x"><p>p</p></a>`, 1},
		{"table in phrasing unwrapped", `<em><table><tr><td>a</td><td>b</td></tr></table></em>`, `<em> a b</em>`, 2},
		{"hr in heading becomes br", `<h2>a<hr>b</h2>`, `<h2>a<br/>b</h2>`, 2},
		// R6
		{"span unwrapped without spaces", `<p><span>a</span><span>b</span></p>`, `<p>ab</p>`, 1},
		{"div unwrapped with spaces", `<div>a</div><div>b</div>`, `a b`, 2},
		{"blockquote kept", `<blockquote><p>q</p></blockquote>`, `<blockquote><p>q</p></blockquote>`, 1},
		// R8
		{"absolute link kept", `<a href="http://other.org/a?b=1&c=2">x</a>`, `<a href="http://other.org/a?b=1&amp;c=2">x</a>`, 1},
		{"protocol-relative link", `<a href="//cdn.org/p">x</a>`, `<a href="https://cdn.org/p">x</a>`, 1},
		{"ftp link unwrapped", `<a href="ftp://x.org/f">x</a>`, `x`, 1},
		{"empty href unwrapped", `<a href="  ">x</a>`, `x`, 1},
		{"missing href unwrapped", `<a name="n">x</a>`, `x`, 1},
		{"bad href unwrapped", `<a href="http://[::1">x</a>`, `x`, 1},
		{"empty link dropped", `<p>a<a href="/x"></a>b</p>`, `<p>ab</p>`, 1},
		// R9
		{"empty abbr title omitted", `<abbr title="">A</abbr>`, `<abbr>A</abbr>`, 1},
		{"abbr title kept and escaped", `<abbr title="a &quot;b&quot; <c>">A</abbr>`, `<abbr title="a &quot;b&quot; &lt;c&gt;">A</abbr>`, 1},
		// §6.5 attribute validation
		{"ol start normalized", `<ol start=" 07 "><li>a</li></ol>`, `<ol start="7"><li>a</li></ol>`, 1},
		{"ol negative start", `<ol start="-2"><li>a</li></ol>`, `<ol start="-2"><li>a</li></ol>`, 1},
		{"td spans", `<table><tr><td colspan="2" rowspan="0">a</td><td colspan="0" rowspan="x">b</td><td colspan="1001">c</td></tr></table>`,
			`<table><tbody><tr><td colspan="2" rowspan="0">a</td><td>b</td><td>c</td></tr></tbody></table>`, 3},
		{"th spans", `<table><tr><th colspan="3">h</th></tr></table>`, `<table><tbody><tr><th colspan="3">h</th></tr></tbody></table>`, 1},
		// R11
		{"empty inline keeps its space", `<p>a<em> </em>b</p>`, `<p>a b</p>`, 2},
		{"nested empties dropped", `<blockquote><p><em></em></p></blockquote>`, ``, 0},
		{"br counts as content", `<p><br></p>`, `<p><br/></p>`, 0},
		{"whitespace-only pre dropped", "<pre>   \n </pre>", ``, 0},
		// R12
		{"hr self-closed", `<p>a</p><hr><p>b</p>`, `<p>a</p><hr/><p>b</p>`, 2},
		// §6.4 text
		{"space kept after inline end", `<p>a <em>b </em>c</p>`, `<p>a <em>b</em> c</p>`, 3},
		{"no leading space in block", "<p>\n  a\n</p>", `<p>a</p>`, 1},
		{"tab and formfeed collapse", "<p>a\t\f\rb</p>", `<p>a b</p>`, 2},
		{"nbsp kept", "<p>a b</p>", "<p>a b</p>", 2},
		{"escaping", `<p>1 &lt; 2 &amp;&amp; 3 &gt; 2</p>`, `<p>1 &lt; 2 &amp;&amp; 3 &gt; 2</p>`, 7},
		{"pre escaping", "<pre>if a < b && c {\n}</pre>", "<pre>if a &lt; b &amp;&amp; c {\n}</pre>", 8},
		{"pre with inline code", "<pre><code>x  y</code></pre>", "<pre><code>x  y</code></pre>", 2},
		{"inline tags are not word boundaries", `<p>H<sub>2</sub>O</p>`, `<p>H<sub>2</sub>O</p>`, 1},
		{"invalid utf8 dropped", "<p>a\xffb</p>", "<p>ab</p>", 1},
	})
}

func TestSanitizeNilBase(t *testing.T) {
	runSanitizeCases(t, nil, []sanitizeCase{
		{"relative unwrapped", `<a href="/rel">r</a>`, `r`, 1},
		{"absolute kept", `<a href="https://x.org/a">a</a>`, `<a href="https://x.org/a">a</a>`, 1},
	})
}

func TestSanitizeNodes(t *testing.T) {
	// Trees the parser never builds: RawNode text, DataAtom left unset,
	// a doctype, and a nil root.
	root := &html.Node{Type: html.DocumentNode}
	p := &html.Node{Type: html.ElementNode, Data: "P"}
	p.AppendChild(&html.Node{Type: html.RawNode, Data: "raw <x>"})
	inner := &html.Node{Type: html.DocumentNode} // nested documents recurse (R1)
	inner.AppendChild(p)
	root.AppendChild(&html.Node{Type: html.DoctypeNode, Data: "html"})
	root.AppendChild(inner)
	got, words := Sanitize(root, nil)
	if got != "<p>raw &lt;x&gt;</p>" || words != 2 {
		t.Errorf("got %q (%d words)", got, words)
	}
	if got, words := Sanitize(nil, nil); got != "" || words != 0 {
		t.Errorf("Sanitize(nil) = %q, %d", got, words)
	}
}

func TestSanitizeDoesNotMutate(t *testing.T) {
	in := `<h1 id="x">T</h1><dl><dt>a</dt></dl><ul>s<li>i</li></ul><a href="/r">r</a><script>x</script><p> a  b </p>`
	body := parseBody(t, in)
	var before, after bytes.Buffer
	if err := html.Render(&before, body); err != nil {
		t.Fatal(err)
	}
	Sanitize(body, testBase)
	if err := html.Render(&after, body); err != nil {
		t.Fatal(err)
	}
	if before.String() != after.String() {
		t.Errorf("input mutated:\nbefore %s\nafter  %s", before.String(), after.String())
	}
}

func TestCountWords(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a b  c", 3},
		{"<p>a</p><p>b</p>", 2},
		{"a<em>b</em>c", 1},
		{"a<br/>b", 2},
		{`<abbr title="x y z">A</abbr>`, 1},
		{"a &amp; b", 3},
		{"a b", 2},
		{"unterminated <p", 1},
	}
	for _, tt := range tests {
		if got := countWords(tt.in); got != tt.want {
			t.Errorf("countWords(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
