// Package xmlx escapes strings for XHTML and drops characters that XML 1.0
// forbids. One stray control character makes Send-to-Kindle reject the EPUB.
package xmlx

import (
	"strings"
	"unicode/utf8"
)

// ValidRune reports whether r is allowed by the XML 1.0 Char production.
func ValidRune(r rune) bool {
	return r == 0x9 || r == 0xA || r == 0xD ||
		(r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) ||
		(r >= 0x10000 && r <= 0x10FFFF)
}

func escape(s string, quote bool) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == utf8.RuneError && size == 1:
			// invalid UTF-8 byte: drop
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"' && quote:
			b.WriteString("&quot;")
		case !ValidRune(r):
			// control characters: drop
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Text escapes element content.
func Text(s string) string { return escape(s, true) }

// Attr escapes a double-quoted attribute value (also safe as text).
func Attr(s string) string { return escape(s, true) }
