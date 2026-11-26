package epub

import (
	"strings"
)

// uriSafe are the ASCII characters RFC 3986 allows literally anywhere in a
// URI besides the unreserved set: gen-delims (minus '#', '[' and ']', which
// escapeURI handles by position) and sub-delims.
const uriSafe = "!$&'()*+,;=:/?@"

// escapeURI percent-encodes every byte that can't appear literally in an
// RFC 3986 URI (spaces, quotes, '<', '>', control characters, non-ASCII,
// a stray '%', a second '#', brackets outside the authority), so an href is
// a valid URI even when the source URL isn't. Valid URIs, including ones
// that are already percent-encoded, come back unchanged. The result still
// needs xmlx.Attr ('&' is legal in a URI but not raw in XML).
func escapeURI(s string) string {
	authStart, authEnd := authority(s)
	var b strings.Builder
	fragment := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case isUnreserved(c) || strings.IndexByte(uriSafe, c) >= 0:
			b.WriteByte(c)
		case c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			b.WriteByte(c)
		case c == '#' && !fragment:
			fragment = true
			b.WriteByte(c)
		case (c == '[' || c == ']') && i >= authStart && i < authEnd:
			b.WriteByte(c) // IPv6 literal host
		default:
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xF])
		}
	}
	return b.String()
}

// authority returns the byte range of the "//host:port" part's host:port,
// or an empty range when s has no "scheme://".
func authority(s string) (start, end int) {
	i := strings.Index(s, "://")
	if i <= 0 {
		return 0, 0
	}
	start = i + len("://")
	end = len(s)
	if j := strings.IndexAny(s[start:], "/?#"); j >= 0 {
		end = start + j
	}
	return start, end
}

func isUnreserved(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' ||
		c == '-' || c == '.' || c == '_' || c == '~'
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}
