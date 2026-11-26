// Package mail builds an RFC 5322 message with the edition attached and
// delivers it over SMTP to the Send-to-Kindle address. It never logs and never
// retries: the caller decides what to do with an error.
package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
	"time"
)

const (
	maxLineLen     = 998 // RFC 5322 §2.1.1, excluding CRLF
	b64LineLen     = 76  // RFC 2045 §6.8
	b64ChunkIn     = b64LineLen / 4 * 3
	foldWidth      = 78 // soft target for folded header lines
	fallbackIDHost = "diario.local"
)

// newlines normalizes CRLF, lone CR and LF to CRLF. Replacer is safe for
// concurrent use; this table is never mutated.
var newlines = strings.NewReplacer("\r\n", "\r\n", "\r", "\r\n", "\n", "\r\n")

// Attachment is the single file attached to a Message.
type Attachment struct {
	Filename    string // e.g. "diario-2026-11-26.epub"
	ContentType string // bare media type, e.g. "application/epub+zip"
	Data        []byte
}

// Message is the input of Build.
type Message struct {
	From, To string // "Name <a@b>" or "a@b", printable ASCII
	Subject  string // UTF-8
	Body     string // UTF-8 plain text
	Attach   Attachment
	Date     time.Time
}

// Build renders m as a multipart/mixed message: a UTF-8 text part followed by
// the base64 attachment. Lines end in CRLF and none exceeds 998 octets.
func Build(m Message) ([]byte, error) {
	if err := validate(m); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.Grow(base64.StdEncoding.EncodedLen(len(m.Attach.Data))/b64LineLen*(b64LineLen+2) + len(m.Body) + 4096)
	if err := writeMessage(&buf, m); err != nil {
		return nil, fmt.Errorf("mail: build: %w", err)
	}
	if n := longestLine(buf.Bytes()); n > maxLineLen {
		return nil, fmt.Errorf("mail: line of %d octets exceeds %d", n, maxLineLen)
	}
	return buf.Bytes(), nil
}

func validate(m Message) error {
	if err := checkAddrHeader("From", m.From); err != nil {
		return err
	}
	if err := checkAddrHeader("To", m.To); err != nil {
		return err
	}
	if m.Date.IsZero() {
		return errors.New("mail: zero Date")
	}
	fn := m.Attach.Filename
	if strings.TrimSpace(fn) == "" || strings.IndexFunc(fn, isCtl) >= 0 {
		return fmt.Errorf("mail: invalid attachment filename %q", fn)
	}
	// FormatMediaType returns "" for an invalid type, but accepts a bare token
	// without a subtype (valid for Content-Disposition, not Content-Type).
	ct := m.Attach.ContentType
	if !strings.Contains(ct, "/") || mime.FormatMediaType(ct, map[string]string{"name": fn}) == "" {
		return fmt.Errorf("mail: invalid attachment content type %q", m.Attach.ContentType)
	}
	return nil
}

// checkAddrHeader accepts a non-empty value made only of printable ASCII, so
// it can be written verbatim without header injection or encoding.
func checkAddrHeader(name, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("mail: empty %s", name)
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] > 0x7e {
			return fmt.Errorf("mail: invalid %s %q: only printable ASCII is allowed", name, v)
		}
	}
	return nil
}

func isCtl(r rune) bool { return r < 0x20 || r == 0x7f }

// writeMessage writes the whole message to w. It is separate from Build so
// tests can make w fail at any offset.
func writeMessage(w io.Writer, m Message) error {
	mw := multipart.NewWriter(w)

	var id [12]byte
	rand.Read(id[:]) // never fails (crypto/rand, Go ≥ 1.24)

	var h strings.Builder
	writeHeader := func(name, value string) {
		h.WriteString(name)
		h.WriteString(": ")
		h.WriteString(value)
		h.WriteString("\r\n")
	}
	writeHeader("From", m.From)
	writeHeader("To", m.To)
	writeHeader("Subject", foldHeader("Subject", mime.QEncoding.Encode("utf-8", m.Subject)))
	writeHeader("Date", m.Date.Format(time.RFC1123Z))
	writeHeader("Message-ID", "<"+hex.EncodeToString(id[:])+"@"+idDomain(m.From)+">")
	writeHeader("MIME-Version", "1.0")
	writeHeader("Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": mw.Boundary()}))
	h.WriteString("\r\n")
	if _, err := io.WriteString(w, h.String()); err != nil {
		return err
	}

	text, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": {"8bit"},
	})
	if err != nil {
		return err
	}
	if _, err := io.WriteString(text, newlines.Replace(m.Body)); err != nil {
		return err
	}

	fn := m.Attach.Filename
	att, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {mime.FormatMediaType(m.Attach.ContentType, map[string]string{"name": fn})},
		"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": fn})},
		"Content-Transfer-Encoding": {"base64"},
	})
	if err != nil {
		return err
	}
	if err := writeBase64(att, m.Attach.Data); err != nil {
		return err
	}
	return mw.Close()
}

// writeBase64 writes data as base64 in lines of exactly 76 characters (the
// last one may be shorter), each followed by CRLF.
func writeBase64(w io.Writer, data []byte) error {
	var line [b64LineLen + 2]byte
	for len(data) > 0 {
		chunk := data[:min(b64ChunkIn, len(data))]
		data = data[len(chunk):]
		n := base64.StdEncoding.EncodedLen(len(chunk))
		base64.StdEncoding.Encode(line[:n], chunk)
		line[n], line[n+1] = '\r', '\n'
		if _, err := w.Write(line[:n+2]); err != nil {
			return err
		}
	}
	return nil
}

// foldHeader folds v at spaces so that lines stay within foldWidth when
// possible. The first word always stays on the first line, and a single word
// longer than the width is never split (Build's line check catches it).
func foldHeader(name, v string) string {
	var b strings.Builder
	lineLen := len(name) + len(": ")
	for i, w := range strings.Split(v, " ") {
		if i > 0 {
			if w != "" && lineLen+1+len(w) > foldWidth {
				b.WriteString("\r\n ")
				lineLen = 1
			} else {
				b.WriteByte(' ')
				lineLen++
			}
		}
		b.WriteString(w)
		lineLen += len(w)
	}
	return b.String()
}

// idDomain returns the domain for the Message-ID: what follows the last "@"
// of From's address, or fallbackIDHost if that is empty or not a plain
// hostname.
func idDomain(from string) string {
	a := extractAddr(from)
	i := strings.LastIndexByte(a, '@')
	if i < 0 {
		return fallbackIDHost
	}
	d := a[i+1:]
	if d == "" || strings.IndexFunc(d, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-')
	}) >= 0 {
		return fallbackIDHost
	}
	return d
}

// extractAddr returns the addr-spec of "Name <a@b>" ("a@b"), or s trimmed.
// It does not validate the result.
func extractAddr(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, ">") {
		if i := strings.LastIndexByte(s, '<'); i >= 0 {
			return strings.TrimSpace(s[i+1 : len(s)-1])
		}
	}
	return s
}

// longestLine returns the length of the longest line in b, excluding its
// line terminator. Any LF ends a line, so bare LFs are not hidden.
func longestLine(b []byte) int {
	longest := 0
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		var line []byte
		if i < 0 {
			line, b = b, nil
		} else {
			line, b = b[:i], b[i+1:]
		}
		line = bytes.TrimSuffix(line, []byte{'\r'})
		longest = max(longest, len(line))
	}
	return longest
}
