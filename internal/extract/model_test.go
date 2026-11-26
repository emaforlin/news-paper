package extract

import (
	"encoding/xml"
	"errors"
	"io"
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
