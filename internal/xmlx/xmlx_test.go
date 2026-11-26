package xmlx

import (
	"encoding/xml"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestValidRune(t *testing.T) {
	tests := []struct {
		r    rune
		want bool
	}{
		{0x0, false},
		{0x8, false},
		{0x9, true}, // tab
		{0xA, true}, // LF
		{0xB, false},
		{0xC, false},
		{0xD, true}, // CR
		{0x1F, false},
		{0x20, true},
		{'a', true},
		{0x7F, true}, // DEL is allowed by XML 1.0
		{0xD7FF, true},
		{0xD800, false}, // surrogates
		{0xDFFF, false},
		{0xE000, true},
		{0xFFFD, true},
		{0xFFFE, false},
		{0xFFFF, false},
		{0x10000, true},
		{0x10FFFF, true},
		{0x110000, false},
		{-1, false},
	}
	for _, tt := range tests {
		if got := ValidRune(tt.r); got != tt.want {
			t.Errorf("ValidRune(%#x) = %v, want %v", tt.r, got, tt.want)
		}
	}
}

func TestEscape(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "hello world", "hello world"},
		{"ampersand", "T&T", "T&amp;T"},
		{"already escaped is escaped again", "&amp;", "&amp;amp;"},
		{"angle brackets", "<b>x</b>", "&lt;b&gt;x&lt;/b&gt;"},
		{"cdata end", "]]>", "]]&gt;"},
		{"double quote", `say "hi"`, "say &quot;hi&quot;"},
		{"single quote kept", "it's", "it's"},
		{"whitespace kept", "a\tb\nc\rd", "a\tb\nc\rd"},
		{"control chars dropped", "a\x00b\x0bc\x1fd", "abcd"},
		{"invalid utf-8 dropped", "a\xffb\xc3c", "abc"},
		{"non-characters dropped", "a￾b￿c", "abc"},
		{"replacement char kept", "a�b", "a�b"},
		{"multibyte kept", "año · 日本 · 😀", "año · 日本 · 😀"},
		{"mixed", "T&T <b>\"q\"</b> a>b\x0b", "T&amp;T &lt;b&gt;&quot;q&quot;&lt;/b&gt; a&gt;b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Text(tt.in); got != tt.want {
				t.Errorf("Text(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if got := Attr(tt.in); got != tt.want {
				t.Errorf("Attr(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Whatever the input, the escaped string must be valid UTF-8, contain only
// XML 1.0 characters, and round-trip through encoding/xml as both element
// content and a double-quoted attribute value (minus the dropped characters).
// Inputs avoid tab/CR/LF, which XML parsers normalize.
func TestOutputParsesBack(t *testing.T) {
	inputs := []string{
		"",
		"plain",
		`T&T <b>"q"</b> a>b` + "\x0b",
		"&lt; not an entity &#x0; &bogus;",
		"]]> <![CDATA[ x ]]>",
		"<?xml version=\"1.0\"?><!-- c -->",
		"a\xffb\xed\xa0\x80c", // invalid UTF-8 and an encoded surrogate
		"\x00\x01\x02\x7f\u0085 ",
		"año · 日本 · 😀",
	}
	for _, in := range inputs {
		want := keepValid(in)

		esc := Text(in)
		if !utf8.ValidString(esc) {
			t.Errorf("Text(%q) is not valid UTF-8: %q", in, esc)
		}
		for _, r := range esc {
			if !ValidRune(r) {
				t.Errorf("Text(%q) contains invalid XML char %#x", in, r)
			}
		}

		var doc struct {
			Attr string `xml:"a,attr"`
			Text string `xml:",chardata"`
		}
		src := `<e a="` + Attr(in) + `">` + Text(in) + `</e>`
		if err := xml.Unmarshal([]byte(src), &doc); err != nil {
			t.Errorf("escaped %q does not parse: %v\n%s", in, err, src)
			continue
		}
		if doc.Text != want {
			t.Errorf("text round-trip of %q = %q, want %q", in, doc.Text, want)
		}
		if doc.Attr != want {
			t.Errorf("attr round-trip of %q = %q, want %q", in, doc.Attr, want)
		}
	}
}

// keepValid is the reference model: drop invalid UTF-8 and non-XML chars.
func keepValid(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if (r == utf8.RuneError && size == 1) || !ValidRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func FuzzText(f *testing.F) {
	for _, s := range []string{"", "T&T", "<b>\"q\"</b>", "\x0b", "\xff", "😀"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		esc := Text(in)
		if !utf8.ValidString(esc) {
			t.Fatalf("Text(%q) = %q: invalid UTF-8", in, esc)
		}
		if strings.ContainsAny(esc, `<>"`) {
			t.Fatalf("Text(%q) = %q: raw markup character", in, esc)
		}
		var got string
		if err := xml.Unmarshal([]byte("<e>"+esc+"</e>"), &got); err != nil {
			t.Fatalf("Text(%q) = %q does not parse: %v", in, esc, err)
		}
		// The XML parser normalizes line endings to LF.
		want := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(keepValid(in))
		if got != want {
			t.Fatalf("round-trip of %q = %q, want %q", in, got, want)
		}
	})
}
