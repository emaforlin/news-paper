package mail

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"regexp"
	"strings"
	"testing"
	"time"
)

var errFail = errors.New("write failed")

// failAfter accepts n bytes in total, then fails every write.
type failAfter struct{ n int }

func (f *failAfter) Write(p []byte) (int, error) {
	if len(p) <= f.n {
		f.n -= len(p)
		return len(p), nil
	}
	k := f.n
	f.n = 0
	return k, errFail
}

type part struct {
	Header  textproto.MIMEHeader
	Raw     []byte // as transmitted (base64 text for the attachment)
	Decoded []byte // Raw after the Content-Transfer-Encoding
}

// parseMessage parses raw with the stdlib readers only and fails the test if
// it isn't a multipart message.
func parseMessage(t *testing.T, raw []byte) (mail.Header, []part) {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("Content-Type: %v", err)
	}
	if mt != "multipart/mixed" || params["boundary"] == "" {
		t.Fatalf("Content-Type = %q %v, want multipart/mixed with a boundary", mt, params)
	}
	var parts []part
	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		p, err := mr.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextRawPart: %v", err)
		}
		rawPart, err := io.ReadAll(p)
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		dec := rawPart
		if strings.EqualFold(p.Header.Get("Content-Transfer-Encoding"), "base64") {
			if dec, err = base64.StdEncoding.DecodeString(string(rawPart)); err != nil {
				t.Fatalf("base64: %v", err)
			}
		}
		parts = append(parts, part{Header: p.Header, Raw: rawPart, Decoded: dec})
	}
	return msg.Header, parts
}

// checkLines asserts that every line ends in CRLF (no bare CR or LF) and is
// at most 998 octets long.
func checkLines(t *testing.T, raw []byte) {
	t.Helper()
	if !bytes.HasSuffix(raw, []byte("\r\n")) {
		t.Errorf("message does not end in CRLF")
	}
	for i, line := range bytes.Split(raw, []byte("\r\n")) {
		if bytes.ContainsAny(line, "\r\n") {
			t.Fatalf("line %d has a bare CR or LF: %q", i+1, line)
		}
		if len(line) > maxLineLen {
			t.Fatalf("line %d is %d octets long", i+1, len(line))
		}
	}
}

var b64Line = regexp.MustCompile(`^[A-Za-z0-9+/]+=*$`)

// checkB64 asserts the base64 layout: lines of exactly 76 characters except
// the last (1–76), each ending in CRLF.
func checkB64(t *testing.T, raw []byte) {
	t.Helper()
	if len(raw) == 0 {
		return
	}
	lines := strings.Split(string(raw), "\r\n")
	if last := lines[len(lines)-1]; last != "" {
		t.Fatalf("base64 does not end in CRLF: %q", last)
	}
	lines = lines[:len(lines)-1]
	for i, l := range lines {
		if !b64Line.MatchString(l) {
			t.Fatalf("base64 line %d has invalid characters: %q", i+1, l)
		}
		if i < len(lines)-1 && len(l) != b64LineLen {
			t.Fatalf("base64 line %d is %d chars, want %d", i+1, len(l), b64LineLen)
		}
		if i == len(lines)-1 && (len(l) == 0 || len(l) > b64LineLen) {
			t.Fatalf("last base64 line is %d chars", len(l))
		}
	}
}

// headerSection returns the unfolded top-level header lines of raw.
func headerSection(t *testing.T, raw []byte) []string {
	t.Helper()
	end := bytes.Index(raw, []byte("\r\n\r\n"))
	if end < 0 {
		t.Fatal("no empty line after the headers")
	}
	var out []string
	for _, l := range strings.Split(string(raw[:end]), "\r\n") {
		if strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t") {
			out[len(out)-1] += l
			continue
		}
		out = append(out, l)
	}
	return out
}

// art is Buenos Aires time without depending on the system tz database.
var art = time.FixedZone("ART", -3*60*60)

func testMessage() Message {
	return Message{
		From:    "Diario <diario@example.com>",
		To:      "lector@kindle.com",
		Subject: "Diario · 2026-11-26",
		Body:    "Hola,\n\nAdjunto la edición de hoy: 10 artículos.\nAño, ñandú, café.\n",
		Attach: Attachment{
			Filename:    "diario-2026-11-26.epub",
			ContentType: "application/epub+zip",
			Data:        []byte("PK\x03\x04 fake epub bytes \x00\xff"),
		},
		Date: time.Date(2026, 11, 26, 5, 40, 0, 0, art),
	}
}
