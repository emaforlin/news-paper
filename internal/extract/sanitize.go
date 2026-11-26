package extract

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/emaforlin/news-paper/internal/xmlx"
)

// allowedAttrs is the element allowlist (keys) with the attributes each
// element may keep. Everything else is unwrapped or dropped.
var allowedAttrs = map[atom.Atom][]string{
	atom.P: nil, atom.Br: nil, atom.Hr: nil,
	atom.H2: nil, atom.H3: nil, atom.H4: nil, atom.H5: nil, atom.H6: nil,
	atom.Ul: nil, atom.Li: nil, atom.Dl: nil, atom.Dt: nil, atom.Dd: nil,
	atom.Ol:         {"start"},
	atom.Blockquote: nil, atom.Pre: nil, atom.Code: nil, atom.Kbd: nil, atom.Samp: nil, atom.Var: nil,
	atom.Em: nil, atom.Strong: nil, atom.B: nil, atom.I: nil, atom.U: nil, atom.S: nil,
	atom.Del: nil, atom.Ins: nil, atom.Sub: nil, atom.Sup: nil, atom.Small: nil, atom.Mark: nil,
	atom.Q: nil, atom.Cite: nil,
	atom.Abbr:  {"title"},
	atom.A:     {"href"},
	atom.Table: nil, atom.Thead: nil, atom.Tbody: nil, atom.Tfoot: nil, atom.Tr: nil, atom.Caption: nil,
	atom.Th:     {"colspan", "rowspan"},
	atom.Td:     {"colspan", "rowspan"},
	atom.Figure: nil, atom.Figcaption: nil,
}

// dropTags are removed together with their content (R3, plus head/title).
var dropTags = set(
	atom.Script, atom.Style, atom.Noscript, atom.Iframe, atom.Object, atom.Embed,
	atom.Form, atom.Input, atom.Button, atom.Select, atom.Textarea, atom.Svg, atom.Math,
	atom.Canvas, atom.Video, atom.Audio, atom.Picture, atom.Source, atom.Template,
	atom.Nav, atom.Img, atom.Head, atom.Title,
)

// phrasingOnly elements may only contain phrasing content (R5).
var phrasingOnly = set(
	atom.P, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Pre, atom.Dt,
	atom.Em, atom.Strong, atom.B, atom.I, atom.U, atom.S, atom.Sub, atom.Sup,
	atom.Small, atom.Mark, atom.Q, atom.Cite, atom.Abbr, atom.Code, atom.Kbd,
	atom.Samp, atom.Var,
)

// blockTags are unwrapped with a separator space on each side, both inside
// phrasing context (R5) and when they aren't allowlisted (R6).
var blockTags = set(
	atom.P, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6,
	atom.Ul, atom.Ol, atom.Li, atom.Dl, atom.Dt, atom.Dd, atom.Blockquote, atom.Pre, atom.Hr,
	atom.Table, atom.Thead, atom.Tbody, atom.Tfoot, atom.Tr, atom.Td, atom.Th, atom.Caption,
	atom.Figure, atom.Figcaption,
	atom.Div, atom.Section, atom.Article, atom.Header, atom.Footer, atom.Main, atom.Aside,
	atom.Address, atom.Details, atom.Summary, atom.Fieldset, atom.Hgroup, atom.Center,
)

// inlineTags are emitted phrasing elements that are not word boundaries.
var inlineTags = set(
	atom.A, atom.Em, atom.Strong, atom.B, atom.I, atom.U, atom.S, atom.Del, atom.Ins,
	atom.Sub, atom.Sup, atom.Small, atom.Mark, atom.Q, atom.Cite, atom.Abbr,
	atom.Code, atom.Kbd, atom.Samp, atom.Var,
)

// keepEmpty elements survive R11 even without content.
var keepEmpty = set(atom.Td, atom.Th)

func set(as ...atom.Atom) map[atom.Atom]bool {
	m := make(map[atom.Atom]bool, len(as))
	for _, a := range as {
		m[a] = true
	}
	return m
}

// Sanitize renders the children of root as an XHTML fragment that only
// contains allowlisted elements and attributes, and returns it with its word
// count. Relative links are resolved against base; with a nil base they are
// unwrapped. Sanitize does not modify root.
func Sanitize(root *html.Node, base *url.URL) (xhtml string, words int) {
	if root == nil {
		return "", 0
	}
	s := &sanitizer{base: base, boundary: true}
	s.children(root, mode{})
	out := strings.TrimSpace(string(s.buf))
	return out, countWords(out)
}

// sanitizer writes the output in one pass. Whitespace is never written
// eagerly: it is owed as pending and flushed only before more content.
type sanitizer struct {
	buf      []byte
	base     *url.URL
	pending  bool    // a separator space is owed
	boundary bool    // the last write was a non-inline tag (or nothing): no space needed
	content  int     // count of non-whitespace writes, for R11
	stack    []frame // emitted elements, innermost last
}

type frame struct {
	tag   atom.Atom
	mark  mark // state before the start tag, for rollback
	after int  // s.content right after the start tag
}

type mark struct {
	n                 int
	pending, boundary bool
}

// mode is the rendering context inherited from emitted ancestors.
type mode struct{ phrasing, pre bool }

func (m mode) child(tag atom.Atom) mode {
	return mode{phrasing: m.phrasing || phrasingOnly[tag], pre: m.pre || tag == atom.Pre}
}

func (s *sanitizer) children(n *html.Node, m mode) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		s.node(c, m)
	}
}

func (s *sanitizer) node(n *html.Node, m mode) {
	switch n.Type {
	case html.TextNode, html.RawNode:
		s.text(n.Data, m)
	case html.DocumentNode:
		s.children(n, m)
	case html.ElementNode:
		s.element(n, m)
	}
	// Comments, doctypes and error nodes are dropped (R1).
}

func tagOf(n *html.Node) atom.Atom {
	if n.DataAtom != 0 {
		return n.DataAtom
	}
	return atom.Lookup([]byte(strings.ToLower(n.Data)))
}

func (s *sanitizer) element(n *html.Node, m mode) {
	tag := tagOf(n)
	if n.Namespace != "" || dropTags[tag] { // R2, R3
		return
	}
	if tag == atom.H1 { // R4
		tag = atom.H2
	}
	if m.phrasing && blockTags[tag] { // R5
		if tag == atom.Hr {
			s.void(atom.Br)
			return
		}
		s.unwrap(n, m, !m.pre)
		return
	}
	if _, ok := allowedAttrs[tag]; !ok { // R6
		s.unwrap(n, m, blockTags[tag] && !m.pre)
		return
	}
	var href string
	if tag == atom.A {
		var ok bool
		if href, ok = s.href(n); !ok { // R8
			s.unwrap(n, m, false)
			return
		}
	}
	if tag == atom.Br || tag == atom.Hr { // R12
		s.void(tag)
		return
	}
	s.open(tag, attrs(n, tag, href))
	s.children(n, m.child(tag))
	s.close()
}

func (s *sanitizer) unwrap(n *html.Node, m mode, sep bool) {
	if sep {
		s.pending = true
	}
	s.children(n, m)
	if sep {
		s.pending = true
	}
}

// text implements §6.4: whitespace collapsing outside pre, verbatim inside.
func (s *sanitizer) text(t string, m mode) {
	if m.pre {
		if e := xmlx.Text(t); e != "" {
			s.write(e, false)
			if strings.TrimSpace(e) != "" {
				s.content++
			}
		}
		return
	}
	for t != "" {
		i := strings.IndexFunc(t, isCollapsible)
		if i != 0 {
			word := t
			if i > 0 {
				word = t[:i]
			}
			if e := xmlx.Text(word); e != "" {
				s.write(e, false)
				s.content++
			}
			if i < 0 {
				return
			}
			t = t[i:]
			continue
		}
		s.pending = true
		j := strings.IndexFunc(t, func(r rune) bool { return !isCollapsible(r) })
		if j < 0 {
			return
		}
		t = t[j:]
	}
}

func isCollapsible(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f'
}

// write appends str, flushing the pending space first when it separates
// inline content. boundary records whether str ends at a block boundary.
func (s *sanitizer) write(str string, boundary bool) {
	if s.pending && !s.boundary {
		s.buf = append(s.buf, ' ')
	}
	s.pending = false
	s.buf = append(s.buf, str...)
	s.boundary = boundary
}

func (s *sanitizer) void(tag atom.Atom) {
	s.write("<"+tag.String()+"/>", true)
	s.content++
}

func (s *sanitizer) open(tag atom.Atom, attrs string) {
	mk := mark{n: len(s.buf), pending: s.pending, boundary: s.boundary}
	s.write("<"+tag.String()+attrs+">", !inlineTags[tag])
	s.stack = append(s.stack, frame{tag: tag, mark: mk, after: s.content})
}

// close ends the innermost frame. An element without content is rolled back
// (R11) unless it's a table cell. It reports whether the element was kept.
func (s *sanitizer) close() bool {
	f := s.stack[len(s.stack)-1]
	s.stack = s.stack[:len(s.stack)-1]
	if s.content == f.after && !keepEmpty[f.tag] {
		s.rollback(f.mark)
		return false
	}
	if inlineTags[f.tag] {
		// The pending space carries over: "a <em>b </em>c" keeps its space.
		s.buf = append(s.buf, "</"+f.tag.String()+">"...)
		s.boundary = false
		return true
	}
	s.pending = false
	s.buf = append(s.buf, "</"+f.tag.String()+">"...)
	s.boundary = true
	return true
}

// rollback discards everything written since mk. Whitespace owed by the
// discarded content stays owed, so "a<em> </em>b" still reads "a b".
func (s *sanitizer) rollback(mk mark) {
	s.buf = s.buf[:mk.n]
	s.pending = mk.pending || s.pending
	s.boundary = mk.boundary
}

// href resolves an a@href (R8). Only absolute http(s) links are kept.
func (s *sanitizer) href(n *html.Node) (string, bool) {
	raw, ok := attrValue(n, "href")
	raw = strings.TrimSpace(raw)
	if !ok || raw == "" || raw[0] == '#' {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if s.base != nil {
		u = s.base.ResolveReference(u)
	}
	if sch := strings.ToLower(u.Scheme); (sch != "http" && sch != "https") || u.Host == "" {
		return "", false
	}
	return u.String(), true
}

// attrValue returns the first non-namespaced attribute named key.
func attrValue(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// attrs renders the allowed attributes of n for tag (§6.5): each name once,
// first occurrence wins, empty or invalid values omitted.
func attrs(n *html.Node, tag atom.Atom, href string) string {
	var b strings.Builder
	for _, key := range allowedAttrs[tag] {
		v := href
		if key != "href" {
			raw, ok := attrValue(n, key)
			if !ok {
				continue
			}
			v = validAttr(key, raw)
		}
		if v == "" { // R9
			continue
		}
		b.WriteString(" " + key + `="` + xmlx.Attr(v) + `"`)
	}
	return b.String()
}

// validAttr normalizes integer attributes and returns "" when invalid.
func validAttr(key, v string) string {
	lo, hi := 0, 0
	switch key {
	case "start":
		lo, hi = -1<<31, 1<<31-1
	case "colspan":
		lo, hi = 1, 1000
	case "rowspan":
		lo, hi = 0, 65534
	default:
		return v
	}
	i, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || i < lo || i > hi {
		return ""
	}
	return strconv.Itoa(i)
}

// countWords counts whitespace-separated tokens in the text of an XHTML
// fragment. Every tag except inline ones is a word boundary (§6.4).
func countWords(s string) int {
	n, inWord := 0, false
	for i := 0; i < len(s); {
		if s[i] == '<' {
			end := strings.IndexByte(s[i:], '>')
			if end < 0 {
				break
			}
			if !inlineTags[atom.Lookup([]byte(tagName(s[i+1:i+end])))] {
				inWord = false
			}
			i += end + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if unicode.IsSpace(r) {
			inWord = false
		} else if !inWord {
			n++
			inWord = true
		}
	}
	return n
}

// tagName extracts the element name from the inside of a tag: "/a", "a href=…", "br/".
func tagName(t string) string {
	t = strings.TrimPrefix(t, "/")
	if i := strings.IndexAny(t, " /"); i >= 0 {
		t = t[:i]
	}
	return t
}
