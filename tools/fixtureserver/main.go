// Command fixtureserver is a development tool: fake Hacker News, Lobsters and
// RSS sources plus a mock OpenRouter, serving the article pages found in
// -pages/<name>/source.html. It backs "make bench"; it is not part of the app.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "listen address, also used in the URLs served")
	pages := flag.String("pages", "", "directory of <name>/source.html files")
	llm := flag.String("llm", llmOK, "mock LLM behaviour: ok | fail")
	flag.Parse()

	h, err := newHandler(*pages, *addr, *llm)
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: *addr, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("fixtureserver on http://%s", *addr)
	log.Fatal(srv.ListenAndServe())
}
