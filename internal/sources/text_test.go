package sources

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestHTMLToText(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"fs example", `<p>Hello <b>world</b></p><script>x()</script><style>p{}</style>&amp; more`, "Hello world & more"},
		{"plain collapsed", "  one\n\ttwo   three ", "one two three"},
		{"empty", "", ""},
		{"nbsp", "a&nbsp;b", "a b"},
		{"entity only", "Tom &amp; Jerry", "Tom & Jerry"},
		{"br", "a<br>b", "a b"},
		{"br self-closing", "a<br/>b<hr />c", "a b c"},
		{"end tags separate", "<li>one</li><li>two</li>", "one two"},
		{"nested script", "<div>keep<script>if (a < b) { drop() }</script>this</div>", "keep this"},
		{"uppercase style", "<STYLE>p{}</STYLE>text", "text"},
		{"stray end script", "a</script>b", "a b"},
		{"comment dropped", "a<!-- hidden -->b", "ab"},
		{"unclosed tag", "text <b", "text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := htmlToText(tt.in); got != tt.want {
				t.Errorf("htmlToText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"  one   two three four five  ", 12, "one two…"},
		{"abcdefghijklmnop qr", 10, "abcdefghij…"},
		{"short", 200, "short"},
		{"exactly ten", 11, "exactly ten"},
		{"ab cdefghijkl", 8, "ab cdefg…"},             // last space at 2, not past n/2 = 4
		{"abcd efghijkl", 8, "abcd efg…"},             // space at exactly n/2 is not used
		{"abcde fghijkl", 8, "abcde…"},                // space at 5 > 4: cut there
		{"ñññññ ñññññññ", 8, "ñññññ…"},                // the space is at byte 10 > 4
		{"señal  de  humo   espesa", 12, "señal de…"}, // collapsed first
	}
	for _, tt := range tests {
		got := truncate(tt.in, tt.n)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("truncate(%q, %d) is not valid UTF-8", tt.in, tt.n)
		}
		if n := utf8.RuneCountInString(strings.TrimSuffix(got, "…")); n > tt.n {
			t.Errorf("truncate(%q, %d) kept %d runes", tt.in, tt.n, n)
		}
	}
}

func TestParseDate(t *testing.T) {
	tests := []struct {
		in      string
		want    string // RFC 3339; "" means zero
		wantOff int    // seconds east of UTC
	}{
		// FS §7.4 rows.
		{"Tue, 24 Nov 2026 10:00:00 +0000", "2026-11-24T10:00:00Z", 0},
		{"2026-11-24T08:00:00Z", "2026-11-24T08:00:00Z", 0},
		{"24 Nov 2026 10:00:00 -0300", "2026-11-24T10:00:00-03:00", -3 * 3600},
		{"2026-11-24", "2026-11-24T00:00:00Z", 0},
		{"garbage", "", 0},
		// One row per remaining layout.
		{"Tue, 24 Nov 2026 10:00:00 GMT", "2026-11-24T10:00:00Z", 0},
		{"2026-11-24T08:00:00.123456789-03:00", "2026-11-24T08:00:00.123456789-03:00", -3 * 3600},
		{"Tue, 4 Nov 2026 10:00:00 -0300", "2026-11-04T10:00:00-03:00", -3 * 3600},
		{"Tue, 4 Nov 2026 10:00:00 UTC", "2026-11-04T10:00:00Z", 0},
		{"Tue, 24 Nov 2026 10:00 +0100", "2026-11-24T10:00:00+01:00", 3600},
		{"24 Nov 26 10:00 -0300", "2026-11-24T10:00:00-03:00", -3 * 3600},
		{"24 Nov 26 10:00 UTC", "2026-11-24T10:00:00Z", 0},
		{"2026-11-24T08:00:00", "2026-11-24T08:00:00Z", 0},
		{"  2026-11-24  ", "2026-11-24T00:00:00Z", 0},
		{"", "", 0},
	}
	for _, tt := range tests {
		got := parseDate(tt.in)
		if tt.want == "" {
			if !got.IsZero() {
				t.Errorf("parseDate(%q) = %v, want zero", tt.in, got)
			}
			continue
		}
		want := mustTime(t, time.RFC3339Nano, tt.want)
		if !got.Equal(want) {
			t.Errorf("parseDate(%q) = %v, want %v", tt.in, got, want)
		}
		if _, off := got.Zone(); off != tt.wantOff {
			t.Errorf("parseDate(%q) offset = %d, want %d", tt.in, off, tt.wantOff)
		}
	}
}
