package extract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeberg.org/readeck/go-readability"
	"golang.org/x/net/html"

	"github.com/emaforlin/news-paper/internal/epub"
	"github.com/emaforlin/news-paper/internal/news"
)

// TestCorpus runs the sanitizer over a directory of real pages laid out as
// <dir>/<name>/source.html, such as the test-pages of
// codeberg.org/readeck/go-readability. Each page is checked twice: the whole
// document, and the readability output (what Extract really sanitizes).
//
//	EXTRACT_CORPUS=<dir> go test -run TestCorpus ./internal/extract
//
// With EXTRACT_EPUB_OUT=<absolute dir> it also writes <dir>/corpus.epub with the
// readability articles, to be checked with `epubcheck`.
func TestCorpus(t *testing.T) {
	dir := os.Getenv("EXTRACT_CORPUS")
	if dir == "" {
		t.Skip("EXTRACT_CORPUS not set")
	}
	pages, err := filepath.Glob(filepath.Join(dir, "*", "source.html"))
	if err != nil || len(pages) == 0 {
		t.Fatalf("no pages under %s (err %v)", dir, err)
	}
	var articles []news.Article
	for _, path := range pages {
		name := filepath.Base(filepath.Dir(path))
		base := mustURL("https://example.com/" + name + "/")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(name+"/document", func(t *testing.T) {
			doc, err := html.Parse(strings.NewReader(string(raw)))
			if err != nil {
				t.Skipf("parse: %v", err)
			}
			out, words := Sanitize(doc, base)
			checkOutput(t, out, words)
		})
		t.Run(name+"/readability", func(t *testing.T) {
			doc, err := decodeAndParse(strings.NewReader(string(raw)), "")
			if err != nil {
				t.Skipf("parse: %v", err)
			}
			ps := readability.NewParser()
			art, err := ps.ParseAndMutate(doc, base)
			if err != nil || art.Node == nil {
				t.Skipf("no readable content (err %v)", err)
			}
			out, words := Sanitize(art.Node, base)
			checkOutput(t, out, words)
			if words > 0 && len(articles) < 99 && !t.Failed() {
				title := strings.TrimSpace(art.Title)
				if title == "" {
					title = name
				}
				articles = append(articles, news.Article{
					Pick:  news.Pick{Candidate: news.Candidate{URL: base.String(), Title: name, Source: "corpus"}},
					Title: title, Byline: art.Byline, SiteName: art.SiteName, Language: art.Language,
					XHTML: out, Words: words,
				})
			}
		})
	}

	if out := os.Getenv("EXTRACT_EPUB_OUT"); out != "" {
		b, err := epub.Bytes(epub.Edition{
			Title:    "Corpus",
			Date:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Language: "en",
			Articles: articles,
		})
		if err != nil {
			t.Fatalf("epub: %v", err)
		}
		path := filepath.Join(out, "corpus.epub")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s with %d articles", path, len(articles))
	}
}
