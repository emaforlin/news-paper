package extract

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/net/html"
)

// TestCorpus runs the sanitizer over a directory of real pages laid out as
// <dir>/<name>/source.html, such as the test-pages of
// codeberg.org/readeck/go-readability:
//
//	EXTRACT_CORPUS=<dir> go test -run TestCorpus ./internal/extract
func TestCorpus(t *testing.T) {
	dir := os.Getenv("EXTRACT_CORPUS")
	if dir == "" {
		t.Skip("EXTRACT_CORPUS not set")
	}
	pages, err := filepath.Glob(filepath.Join(dir, "*", "source.html"))
	if err != nil || len(pages) == 0 {
		t.Fatalf("no pages under %s (err %v)", dir, err)
	}
	for _, path := range pages {
		name := filepath.Base(filepath.Dir(path))
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			doc, err := html.Parse(f)
			if err != nil {
				t.Skipf("parse: %v", err)
			}
			out, words := Sanitize(doc, mustURL("https://example.com/"+name+"/"))
			checkOutput(t, out, words)
		})
	}
}
