package pipeline

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	netmail "net/mail"
	"strings"
	"testing"
	"time"

	"github.com/emaforlin/news-paper/internal/epub"
	"github.com/emaforlin/news-paper/internal/news"
)

func article(url, title string) news.Article {
	return news.Article{Pick: news.Pick{Candidate: news.Candidate{URL: url}}, Title: title}
}

var golden = epub.Edition{Title: "Diario", Date: testNow, Articles: []news.Article{
	article("https://www.example.com/a", "First  article\ntitle"),
	article("https://blog.example.org/b", "Second article title"),
}}

const goldenBody = "Diario — jueves 26 de noviembre de 2026\n\n" +
	"1. First article title (example.com)\n" +
	"2. Second article title (blog.example.org)\n"

func TestBody(t *testing.T) {
	if got := body(golden); got != goldenBody {
		t.Errorf("body = %q\nwant %q", got, goldenBody)
	}

	long := golden
	long.Articles = []news.Article{article("https://a.test", strings.Repeat("ñ", 2000))}
	got := body(long)
	if !strings.Contains(got, "1. "+strings.Repeat("ñ", 149)+"… (a.test)\n") {
		t.Errorf("long title not truncated: %q", got)
	}
	if _, err := BuildMessage(testConfig(t), Delivery{Subject: "s", Body: got, Filename: "a.epub", Date: testNow}); err != nil {
		t.Errorf("long title blocks the message: %v", err)
	}
}

func TestBuildMessage(t *testing.T) {
	raw, err := BuildMessage(testConfig(t), Delivery{
		Subject: golden.FullTitle(), Body: body(golden), Filename: golden.Filename(),
		EPUB: []byte("PK data"), Date: time.Date(2026, 11, 26, 5, 40, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || subject != "Diario · 2026-11-26" {
		t.Errorf("subject = %q, %v", subject, err)
	}
	if m.Header.Get("From") != "me@example.com" || m.Header.Get("To") != "me@kindle.com" {
		t.Errorf("headers = %v", m.Header)
	}
	_, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	text, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(text)
	if string(b) != strings.ReplaceAll(goldenBody, "\n", "\r\n") {
		t.Errorf("text part = %q", b)
	}
	att, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if att.FileName() != "diario-2026-11-26.epub" || !strings.HasPrefix(att.Header.Get("Content-Type"), "application/epub+zip") {
		t.Errorf("attachment %q %q", att.FileName(), att.Header.Get("Content-Type"))
	}
}
