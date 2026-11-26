package extract

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// wellFormed fails t unless frag is well-formed XML once wrapped in a root.
func wellFormed(t *testing.T, frag string) {
	t.Helper()
	if err := xmlCheck(frag); err != nil {
		t.Errorf("not well-formed: %v\n%s", err, frag)
	}
}

func xmlCheck(frag string) error {
	d := xml.NewDecoder(strings.NewReader("<root>" + frag + "</root>"))
	d.Strict = true
	for {
		_, err := d.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// inlineNames mirrors the word-boundary definition of C-9 independently of
// the implementation's tables.
var inlineNames = map[string]bool{
	"a": true, "em": true, "strong": true, "b": true, "i": true, "u": true, "s": true,
	"del": true, "ins": true, "sub": true, "sup": true, "small": true, "mark": true,
	"q": true, "cite": true, "abbr": true, "code": true, "kbd": true, "samp": true, "var": true,
}

// textOf returns the character data of frag with a space at every tag that
// isn't inline (C-9). frag must be well-formed.
func textOf(t *testing.T, frag string) string {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader("<root>" + frag + "</root>"))
	var b strings.Builder
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return b.String()
		}
		if err != nil {
			t.Fatalf("textOf: %v", err)
		}
		switch tok := tok.(type) {
		case xml.CharData:
			b.Write(tok)
		case xml.StartElement:
			if !inlineNames[tok.Name.Local] {
				b.WriteByte(' ')
			}
		case xml.EndElement:
			if !inlineNames[tok.Name.Local] {
				b.WriteByte(' ')
			}
		}
	}
}

// xnode is a minimal XML tree for checkModel. Text nodes have name "".
type xnode struct {
	name  string
	attrs []xml.Attr
	text  string
	kids  []*xnode
}

func parseTree(frag string) (*xnode, error) {
	d := xml.NewDecoder(strings.NewReader("<root>" + frag + "</root>"))
	d.Strict = true
	var stack []*xnode
	var root *xnode
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return root, nil
		}
		if err != nil {
			return nil, err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			n := &xnode{name: tok.Name.Local, attrs: tok.Attr}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, n)
			} else {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			p := stack[len(stack)-1]
			p.kids = append(p.kids, &xnode{text: string(tok)})
		}
	}
}

// modelAttrs is the allowlist as the content-model oracle sees it.
var modelAttrs = map[string][]string{
	"p": nil, "br": nil, "hr": nil, "h2": nil, "h3": nil, "h4": nil, "h5": nil, "h6": nil,
	"ul": nil, "li": nil, "dl": nil, "dt": nil, "dd": nil, "ol": {"start"},
	"blockquote": nil, "pre": nil, "code": nil, "kbd": nil, "samp": nil, "var": nil,
	"em": nil, "strong": nil, "b": nil, "i": nil, "u": nil, "s": nil, "del": nil, "ins": nil,
	"sub": nil, "sup": nil, "small": nil, "mark": nil, "q": nil, "cite": nil,
	"abbr": {"title"}, "a": {"href"},
	"table": nil, "thead": nil, "tbody": nil, "tfoot": nil, "tr": nil, "caption": nil,
	"th": {"colspan", "rowspan"}, "td": {"colspan", "rowspan"},
	"figure": nil, "figcaption": nil,
}

var (
	modelPhrasingOnly = words("p h2 h3 h4 h5 h6 pre dt em strong b i u s sub sup small mark q cite abbr code kbd samp var")
	modelBlocks       = words("p h2 h3 h4 h5 h6 ul ol li dl dt dd blockquote pre hr table thead tbody tfoot tr td th caption figure figcaption")
	modelParents      = map[string]map[string]bool{
		"li": words("ul ol"), "dt": words("dl"), "dd": words("dl"), "figcaption": words("figure"),
		"tr": words("table thead tbody tfoot"), "td": words("tr"), "th": words("tr"),
		"thead": words("table"), "tbody": words("table"), "tfoot": words("table"), "caption": words("table"),
	}
	modelOnlyKids = map[string]map[string]bool{
		"ul": words("li"), "ol": words("li"), "dl": words("dt dd"),
		"table": words("caption thead tbody tfoot tr"), "thead": words("tr"), "tbody": words("tr"),
		"tfoot": words("tr"), "tr": words("td th"),
	}
	modelMayBeEmpty = words("br hr td th")
)

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// checkModel fails t if frag breaks the (simplified) XHTML content model
// that EPUBCheck enforces. It is independent of the sanitizer's tables.
func checkModel(t *testing.T, frag string) {
	t.Helper()
	root, err := parseTree(frag)
	if err != nil {
		t.Errorf("checkModel: %v", err)
		return
	}
	for _, p := range modelProblems(root) {
		t.Errorf("content model: %s\n%s", p, frag)
	}
}

func modelProblems(root *xnode) []string {
	var probs []string
	add := func(format string, args ...any) { probs = append(probs, fmt.Sprintf(format, args...)) }
	var walk func(n, parent *xnode, phrasing, inA, inTable bool)
	walk = func(n, parent *xnode, phrasing, inA, inTable bool) {
		allowed, ok := modelAttrs[n.name]
		if !ok {
			add("element <%s> not allowed", n.name)
		}
		for _, a := range n.attrs {
			if !slices.Contains(allowed, a.Name.Local) || a.Name.Space != "" {
				add("<%s> has attribute %s", n.name, a.Name.Local)
				continue
			}
			if msg := badAttr(a.Name.Local, a.Value); msg != "" {
				add("<%s %s=%q>: %s", n.name, a.Name.Local, a.Value, msg)
			}
		}
		if phrasing && modelBlocks[n.name] {
			add("block <%s> inside phrasing content", n.name)
		}
		if inA && n.name == "a" {
			add("nested <a>")
		}
		if inTable && n.name == "table" {
			add("nested <table>")
		}
		if want, ok := modelParents[n.name]; ok && !want[parent.name] {
			add("<%s> inside <%s>", n.name, parent.name)
		}
		if only, ok := modelOnlyKids[n.name]; ok {
			for _, k := range n.kids {
				if k.name == "" && strings.TrimSpace(k.text) != "" {
					add("text %q directly inside <%s>", k.text, n.name)
				} else if k.name != "" && !only[k.name] {
					add("<%s> directly inside <%s>", k.name, n.name)
				}
			}
		}
		if n.name == "dl" && !dlGroupsRe.MatchString(kidNames(n)) {
			add("<dl> children %q are not groups of (dt+ dd+)+", kidNames(n))
		}
		if (n.name == "figcaption" || n.name == "caption") && firstKid(parent) != n {
			add("<%s> is not the first child of <%s>", n.name, parent.name)
		}
		if !modelMayBeEmpty[n.name] && blank(n) {
			add("empty <%s>", n.name)
		}
		childPhrasing := phrasing || modelPhrasingOnly[n.name]
		for _, k := range n.kids {
			if k.name != "" {
				walk(k, n, childPhrasing, inA || n.name == "a", inTable || n.name == "table")
			}
		}
	}
	for _, k := range root.kids {
		if k.name != "" {
			walk(k, root, false, false, false)
		}
	}
	return probs
}

func badAttr(key, v string) string {
	switch key {
	case "start", "colspan", "rowspan":
		i, err := strconv.Atoi(v)
		lo, hi := math.MinInt32, math.MaxInt32
		if key == "colspan" {
			lo, hi = 1, 1000
		} else if key == "rowspan" {
			lo, hi = 0, 65534
		}
		if err != nil || i < lo || i > hi {
			return "not a valid integer"
		}
	case "href":
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "not an absolute http(s) URL"
		}
	case "title":
		if v == "" {
			return "empty"
		}
	}
	return ""
}

// dlGroupsRe is the HTML content model of <dl>: one or more groups of
// dt+ followed by dd+.
var dlGroupsRe = regexp.MustCompile(`^((dt )+(dd )+)+$`)

// kidNames lists n's element children, each followed by a space.
func kidNames(n *xnode) string {
	var b strings.Builder
	for _, k := range n.kids {
		if k.name != "" {
			b.WriteString(k.name + " ")
		}
	}
	return b.String()
}

// firstKid is the first child that is an element or non-blank text.
func firstKid(n *xnode) *xnode {
	for _, k := range n.kids {
		if k.name != "" || strings.TrimSpace(k.text) != "" {
			return k
		}
	}
	return nil
}

// blank reports whether n has no element children and only whitespace text.
func blank(n *xnode) bool {
	return firstKid(n) == nil
}
