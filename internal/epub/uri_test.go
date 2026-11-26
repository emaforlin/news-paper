package epub

import "testing"

func TestEscapeURI(t *testing.T) {
	tests := []struct{ in, want string }{
		// Valid URIs are unchanged.
		{"https://example.com/a/b?x=1&y=2#frag", "https://example.com/a/b?x=1&y=2#frag"},
		{"https://example.com/a%20b?q=%C3%B1", "https://example.com/a%20b?q=%C3%B1"},
		{"https://user@example.com:8080/p;v=1/(x)*!'$,+=:@~-._", "https://user@example.com:8080/p;v=1/(x)*!'$,+=:@~-._"},
		{"http://[::1]:8080/p", "http://[::1]:8080/p"},
		{"https://news.ycombinator.com/item?id=1", "https://news.ycombinator.com/item?id=1"},
		{"", ""},
		// Invalid characters are percent-encoded.
		{`https://example.com/p?x=T&T <b>"q"</b> a>b` + "\x0b", "https://example.com/p?x=T&T%20%3Cb%3E%22q%22%3C/b%3E%20a%3Eb%0B"},
		{"https://example.com/café", "https://example.com/caf%C3%A9"},
		{"https://example.com/100%", "https://example.com/100%25"},
		{"https://example.com/%zz%4", "https://example.com/%25zz%254"},
		{"https://example.com/a#b#c", "https://example.com/a#b%23c"},
		{"https://example.com/[x]", "https://example.com/%5Bx%5D"},
		{"https://example.com/{a}|b\\c^d`e", "https://example.com/%7Ba%7D%7Cb%5Cc%5Ed%60e"},
		{"/relative [x]", "/relative%20%5Bx%5D"},
		{"://[x]", "://%5Bx%5D"},
	}
	for _, tt := range tests {
		got := escapeURI(tt.in)
		if got != tt.want {
			t.Errorf("escapeURI(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if again := escapeURI(got); again != got {
			t.Errorf("escapeURI is not idempotent on %q: %q", got, again)
		}
	}
}
