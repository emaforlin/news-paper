package epub

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
)

// Namespaces as resolved by encoding/xml. An undeclared prefix stays as the
// bare prefix (e.g. "epub"), so comparing against these also checks that the
// documents declare their namespaces.
const (
	nsXHTML = "http://www.w3.org/1999/xhtml"
	nsXML   = "http://www.w3.org/XML/1998/namespace"
	nsEPUB  = "http://www.idpf.org/2007/ops"
	nsDC    = "http://purl.org/dc/elements/1.1/"
)

// node is a minimal DOM built on encoding/xml: just enough to assert on the
// structure and text of the generated documents.
type node struct {
	name     xml.Name
	attrs    []xml.Attr
	children []*node
	text     string // character data; only set when name.Local == ""
	order    int    // document order of elements, starting at 1
}

// parseXML strictly parses s and returns its root element. A parse failure
// fails the test, so it doubles as the well-formedness check.
func parseXML(t *testing.T, name, s string) *node {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(s))
	doc := &node{}
	stack := []*node{doc}
	n := 0
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("%s: not well-formed XML: %v", name, err)
		}
		top := stack[len(stack)-1]
		switch tok := tok.(type) {
		case xml.StartElement:
			n++
			el := &node{name: tok.Name, attrs: tok.Copy().Attr, order: n}
			top.children = append(top.children, el)
			stack = append(stack, el)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			top.children = append(top.children, &node{text: string(tok)})
		}
	}
	roots := doc.elements()
	if len(roots) != 1 {
		t.Fatalf("%s: want exactly one root element, got %d", name, len(roots))
	}
	return roots[0]
}

// attr returns the value of the attribute {space}local, or "" if absent.
// Unprefixed attributes have an empty space.
func (n *node) attr(space, local string) string {
	v, _ := n.lookupAttr(space, local)
	return v
}

func (n *node) lookupAttr(space, local string) (string, bool) {
	for _, a := range n.attrs {
		if a.Name.Space == space && a.Name.Local == local {
			return a.Value, true
		}
	}
	return "", false
}

// elements returns the direct element children.
func (n *node) elements() []*node {
	var out []*node
	for _, c := range n.children {
		if c.name.Local != "" {
			out = append(out, c)
		}
	}
	return out
}

// childrenByTag returns the direct element children with the given local name.
func (n *node) childrenByTag(local string) []*node {
	var out []*node
	for _, c := range n.elements() {
		if c.name.Local == local {
			out = append(out, c)
		}
	}
	return out
}

// find returns every descendant element (excluding n) matching pred, in document order.
func (n *node) find(pred func(*node) bool) []*node {
	var out []*node
	var walk func(*node)
	walk = func(p *node) {
		for _, c := range p.elements() {
			if pred(c) {
				out = append(out, c)
			}
			walk(c)
		}
	}
	walk(n)
	return out
}

func (n *node) byTag(local string) []*node {
	return n.find(func(c *node) bool { return c.name.Local == local })
}

func (n *node) byClass(class string) []*node {
	return n.find(func(c *node) bool { return c.hasClass(class) })
}

func (n *node) hasClass(class string) bool {
	for _, c := range strings.Fields(n.attr("", "class")) {
		if c == class {
			return true
		}
	}
	return false
}

// contains reports whether d is a descendant of n.
func (n *node) contains(d *node) bool {
	return len(n.find(func(c *node) bool { return c == d })) == 1
}

// textContent concatenates all character data under n.
func (n *node) textContent() string {
	if n.name.Local == "" {
		return n.text
	}
	var b strings.Builder
	for _, c := range n.children {
		b.WriteString(c.textContent())
	}
	return b.String()
}

// norm collapses whitespace runs, so assertions don't depend on template layout.
func norm(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// one returns the single element in nodes, failing the test otherwise.
func one(t *testing.T, what string, nodes []*node) *node {
	t.Helper()
	if len(nodes) != 1 {
		t.Fatalf("%s: want exactly 1 element, got %d", what, len(nodes))
	}
	return nodes[0]
}
