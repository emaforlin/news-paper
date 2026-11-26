package extract

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// FuzzSanitize checks that any HTML yields well-formed XHTML that respects
// the content model, with a consistent word count. Seeds run in plain
// `go test`; fuzz with
//
//	go test -run=^$ -fuzz=FuzzSanitize -fuzztime=60s ./internal/extract
func FuzzSanitize(f *testing.F) {
	for _, group := range [][]sanitizeCase{fsExamples, clarifications} {
		for _, c := range group {
			f.Add(c.in)
		}
	}
	for _, in := range hostile {
		f.Add(in)
	}
	f.Fuzz(func(t *testing.T, in string) {
		doc, err := html.Parse(strings.NewReader(in))
		if err != nil {
			return // e.g. nesting deeper than the parser allows; Extract rejects those
		}
		out, words := Sanitize(doc, testBase)
		checkOutput(t, out, words)
	})
}
