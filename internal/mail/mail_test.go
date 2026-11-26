package mail

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func randomBytes(n int) []byte {
	r := rand.NewChaCha8([32]byte{1, 2, 3})
	b := make([]byte, n)
	r.Read(b)
	return b
}

func mustBuild(t *testing.T, m Message) []byte {
	t.Helper()
	raw, err := Build(m)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return raw
}

func decodeSubject(t *testing.T, s string) string {
	t.Helper()
	got, err := new(mime.WordDecoder).DecodeHeader(s)
	if err != nil {
		t.Fatalf("decode Subject %q: %v", s, err)
	}
	return got
}

// AT-B01 (+ AT-M01 hook: MAIL_EML_OUT=<path> writes the message for a manual check).
func TestBuildRoundTrip(t *testing.T) {
	m := testMessage()
	m.Attach.Data = randomBytes(200 << 10)
	if p := os.Getenv("MAIL_EML_OUT"); p != "" {
		if epub := os.Getenv("MAIL_EML_EPUB"); epub != "" {
			data, err := os.ReadFile(epub)
			if err != nil {
				t.Fatal(err)
			}
			m.Attach.Data = data
		}
	}
	raw := mustBuild(t, m)

	hdr, parts := parseMessage(t, raw)
	if got := decodeSubject(t, hdr.Get("Subject")); got != m.Subject {
		t.Errorf("Subject = %q, want %q", got, m.Subject)
	}
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(parts))
	}
	wantBody := strings.ReplaceAll(m.Body, "\n", "\r\n")
	if got := string(parts[0].Decoded); got != wantBody {
		t.Errorf("text part = %q, want %q", got, wantBody)
	}
	if !bytes.Equal(parts[1].Decoded, m.Attach.Data) {
		t.Errorf("attachment differs from Data (%d vs %d bytes)", len(parts[1].Decoded), len(m.Attach.Data))
	}

	if p := os.Getenv("MAIL_EML_OUT"); p != "" {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", p, len(raw))
	}
}

// AT-B02
func TestBuildHeaders(t *testing.T) {
	m := testMessage()
	raw := mustBuild(t, m)

	lines := headerSection(t, raw)
	var names []string
	values := map[string]string{}
	for _, l := range lines {
		name, value, ok := strings.Cut(l, ": ")
		if !ok {
			t.Fatalf("malformed header line %q", l)
		}
		names = append(names, name)
		values[name] = value
	}
	want := []string{"From", "To", "Subject", "Date", "Message-ID", "MIME-Version", "Content-Type"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("header order = %v, want %v", names, want)
	}
	if values["From"] != m.From || values["To"] != m.To {
		t.Errorf("From/To = %q/%q, want verbatim %q/%q", values["From"], values["To"], m.From, m.To)
	}
	d, err := time.Parse(time.RFC1123Z, values["Date"])
	if err != nil {
		t.Fatalf("Date %q: %v", values["Date"], err)
	}
	if !d.Equal(m.Date) || !strings.HasSuffix(values["Date"], " -0300") {
		t.Errorf("Date = %q, want %v in -0300", values["Date"], m.Date)
	}
	if values["MIME-Version"] != "1.0" {
		t.Errorf("MIME-Version = %q", values["MIME-Version"])
	}
	mt, params, err := mime.ParseMediaType(values["Content-Type"])
	if err != nil || mt != "multipart/mixed" || params["boundary"] == "" {
		t.Errorf("Content-Type = %q (%v)", values["Content-Type"], err)
	}
}

// AT-B03
func TestBuildMessageID(t *testing.T) {
	tests := []struct {
		from string
		want string // regexp
	}{
		{"Diario <d@x.com>", `^<[0-9a-f]{24}@x\.com>$`},
		{"d@mail.example-1.org", `^<[0-9a-f]{24}@mail\.example-1\.org>$`},
		{"diario", `^<[0-9a-f]{24}@diario\.local>$`},
		{"d@", `^<[0-9a-f]{24}@diario\.local>$`},
		{"Diario <d@x_y.com>", `^<[0-9a-f]{24}@diario\.local>$`},
		{"a@b c", `^<[0-9a-f]{24}@diario\.local>$`},
	}
	for _, tt := range tests {
		t.Run(tt.from, func(t *testing.T) {
			m := testMessage()
			m.From = tt.from
			hdr, _ := parseMessage(t, mustBuild(t, m))
			if id := hdr.Get("Message-ID"); !regexp.MustCompile(tt.want).MatchString(id) {
				t.Errorf("Message-ID = %q, want match %s", id, tt.want)
			}
		})
	}

	t.Run("unique", func(t *testing.T) {
		m := testMessage()
		h1, _ := parseMessage(t, mustBuild(t, m))
		h2, _ := parseMessage(t, mustBuild(t, m))
		if h1.Get("Message-ID") == h2.Get("Message-ID") {
			t.Errorf("Message-ID repeated: %s", h1.Get("Message-ID"))
		}
		if h1.Get("Content-Type") == h2.Get("Content-Type") {
			t.Errorf("boundary repeated: %s", h1.Get("Content-Type"))
		}
	})
}

// AT-B04
func TestBuildParts(t *testing.T) {
	tests := []struct {
		filename string
	}{
		{"diario-2026-11-26.epub"},
		{"diário · 26.epub"},
		{`with "quotes".epub`},
	}
	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			m := testMessage()
			m.Attach.Filename = tt.filename
			_, parts := parseMessage(t, mustBuild(t, m))
			if len(parts) != 2 {
				t.Fatalf("got %d parts", len(parts))
			}
			text, att := parts[0].Header, parts[1].Header
			if mt, p, _ := mime.ParseMediaType(text.Get("Content-Type")); mt != "text/plain" || p["charset"] != "utf-8" {
				t.Errorf("text Content-Type = %q", text.Get("Content-Type"))
			}
			if cte := text.Get("Content-Transfer-Encoding"); cte != "8bit" {
				t.Errorf("text CTE = %q", cte)
			}
			if mt, p, err := mime.ParseMediaType(att.Get("Content-Type")); err != nil || mt != "application/epub+zip" || p["name"] != tt.filename {
				t.Errorf("attachment Content-Type = %q (%v), name %q", att.Get("Content-Type"), err, p["name"])
			}
			if d, p, err := mime.ParseMediaType(att.Get("Content-Disposition")); err != nil || d != "attachment" || p["filename"] != tt.filename {
				t.Errorf("Content-Disposition = %q (%v), filename %q", att.Get("Content-Disposition"), err, p["filename"])
			}
			if cte := att.Get("Content-Transfer-Encoding"); cte != "base64" {
				t.Errorf("attachment CTE = %q", cte)
			}
		})
	}
}

// AT-B05
func TestBuildBase64Lines(t *testing.T) {
	for _, n := range []int{0, 1, 56, 57, 58, 114, 1000, 200 << 10} {
		m := testMessage()
		m.Attach.Data = randomBytes(n)
		raw := mustBuild(t, m)
		_, parts := parseMessage(t, raw)
		checkB64(t, parts[1].Raw)
		checkLines(t, raw)
		if !bytes.Equal(parts[1].Decoded, m.Attach.Data) {
			t.Errorf("n=%d: attachment differs", n)
		}
		switch n {
		case 0:
			if len(parts[1].Raw) != 0 {
				t.Errorf("n=0: raw attachment = %q, want empty", parts[1].Raw)
			}
		case 57:
			if lines := strings.Count(string(parts[1].Raw), "\r\n"); lines != 1 || len(parts[1].Raw) != b64LineLen+2 {
				t.Errorf("n=57: %d lines, %d bytes; want exactly one 76-char line", lines, len(parts[1].Raw))
			}
		}
	}
}

// AT-B06
func TestBuildLineLimits(t *testing.T) {
	t.Run("long non-ASCII subject is folded", func(t *testing.T) {
		m := testMessage()
		m.Subject = strings.Repeat("ñandú ", 50) + strings.Repeat("ü", 300)
		raw := mustBuild(t, m)
		checkLines(t, raw)
		hdr, _ := parseMessage(t, raw)
		if got := decodeSubject(t, hdr.Get("Subject")); got != m.Subject {
			t.Errorf("Subject round trip:\n got %q\nwant %q", got, m.Subject)
		}
		end := bytes.Index(raw, []byte("\r\n\r\n"))
		var subj []string
		in := false
		for _, l := range strings.Split(string(raw[:end]), "\r\n") {
			switch {
			case strings.HasPrefix(l, "Subject: "):
				in = true
			case in && strings.HasPrefix(l, " "):
			default:
				in = false
			}
			if in {
				subj = append(subj, l)
			}
		}
		if len(subj) < 2 {
			t.Fatalf("Subject not folded: %q", subj)
		}
		for i, l := range subj {
			limit := foldWidth
			if i == 0 {
				limit = len("Subject: ") + 75 // one encoded-word always fits on the first line
			}
			if len(l) > limit {
				t.Errorf("Subject line %d is %d chars (> %d): %q", i+1, len(l), limit, l)
			}
		}
	})

	t.Run("long ASCII subject with spaces is folded", func(t *testing.T) {
		m := testMessage()
		m.Subject = strings.TrimSpace(strings.Repeat("news paper ", 200))
		raw := mustBuild(t, m)
		checkLines(t, raw)
		hdr, _ := parseMessage(t, raw)
		if got := hdr.Get("Subject"); got != m.Subject {
			t.Errorf("Subject round trip differs: %q", got)
		}
	})

	tests := []struct {
		name    string
		mod     func(*Message)
		wantErr bool
	}{
		{"body line of 998", func(m *Message) { m.Body = strings.Repeat("a", 998) + "\n" }, false},
		{"body line of 999", func(m *Message) { m.Body = strings.Repeat("a", 999) }, true},
		{"unbreakable ASCII subject", func(m *Message) { m.Subject = strings.Repeat("x", 1000) }, true},
		{"huge filename", func(m *Message) { m.Attach.Filename = strings.Repeat("f", 1000) + ".epub" }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testMessage()
			tt.mod(&m)
			raw, err := Build(m)
			if tt.wantErr {
				if err == nil || raw != nil || !strings.Contains(err.Error(), "exceeds 998") {
					t.Fatalf("Build = %d bytes, %v; want line-length error", len(raw), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			checkLines(t, raw)
		})
	}
}

// AT-B07
func TestBuildBodyNewlines(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a\nb", "a\r\nb"},
		{"a\r\nb", "a\r\nb"},
		{"a\rb", "a\r\nb"},
		{"a\r\r\nb\n", "a\r\n\r\nb\r\n"},
		{"a\r\nb\rc\n", "a\r\nb\r\nc\r\n"},
		{"", ""},
	}
	for _, tt := range tests {
		m := testMessage()
		m.Body = tt.in
		raw := mustBuild(t, m)
		checkLines(t, raw)
		_, parts := parseMessage(t, raw)
		if got := string(parts[0].Raw); got != tt.want {
			t.Errorf("body %q → %q, want %q", tt.in, got, tt.want)
		}
	}
}

// AT-B08
func TestBuildValidation(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*Message)
		want string
	}{
		{"empty From", func(m *Message) { m.From = "" }, "empty From"},
		{"blank To", func(m *Message) { m.To = "  " }, "empty To"},
		{"CRLF in From", func(m *Message) { m.From = "a@b\r\nBcc: x@y" }, "invalid From"},
		{"LF in To", func(m *Message) { m.To = "k@kindle.com\nX: y" }, "invalid To"},
		{"NUL in To", func(m *Message) { m.To = "k@kindle.com\x00" }, "invalid To"},
		{"DEL in From", func(m *Message) { m.From = "a\x7f@b" }, "invalid From"},
		{"non-ASCII From", func(m *Message) { m.From = "Diário <d@x.com>" }, "invalid From"},
		{"zero Date", func(m *Message) { m.Date = time.Time{} }, "zero Date"},
		{"empty filename", func(m *Message) { m.Attach.Filename = " " }, "invalid attachment filename"},
		{"control char in filename", func(m *Message) { m.Attach.Filename = "a\nb.epub" }, "invalid attachment filename"},
		{"empty content type", func(m *Message) { m.Attach.ContentType = "" }, "invalid attachment content type"},
		{"no subtype", func(m *Message) { m.Attach.ContentType = "epub" }, "invalid attachment content type"},
		{"content type with params", func(m *Message) { m.Attach.ContentType = "application/epub+zip; x=y" }, "invalid attachment content type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testMessage()
			tt.mod(&m)
			raw, err := Build(m)
			if err == nil || raw != nil {
				t.Fatalf("Build = %d bytes, %v; want error", len(raw), err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}

	t.Run("subject injection is encoded", func(t *testing.T) {
		m := testMessage()
		m.Subject = "Hola\r\nBcc: spy@example.com"
		raw := mustBuild(t, m)
		hdr, _ := parseMessage(t, raw)
		if _, ok := hdr["Bcc"]; ok {
			t.Fatal("Bcc header injected through Subject")
		}
		if got := decodeSubject(t, hdr.Get("Subject")); got != m.Subject {
			t.Errorf("Subject = %q, want %q", got, m.Subject)
		}
	})
}

// AT-B09
func TestWriteMessageErrors(t *testing.T) {
	m := testMessage()
	m.Attach.Data = randomBytes(200)
	full := mustBuild(t, m)
	for n := 0; n < len(full); n++ {
		err := writeMessage(&failAfter{n: n}, m)
		if !errors.Is(err, errFail) {
			t.Fatalf("failAfter(%d): err = %v, want errFail", n, err)
		}
	}
	if err := writeMessage(&failAfter{n: len(full)}, m); err != nil {
		t.Fatalf("failAfter(len): %v", err)
	}
}

func TestLongestLine(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"ab\r\nabcd\r\n", 4},
		{"ab\nabcde", 5},
		{"a\rbc\r\n", 4}, // a bare CR counts as content
	}
	for _, tt := range tests {
		if got := longestLine([]byte(tt.in)); got != tt.want {
			t.Errorf("longestLine(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
