package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	llmOK   = "ok"
	llmFail = "fail"

	feedItems = 5
)

var (
	wantRe = regexp.MustCompile(`choose the (\d+) items`)
	lineRe = regexp.MustCompile(`(?m)^(\d+)\. `)
)

type rssItem struct {
	Title string `xml:"title"`
	Link  string `xml:"link"`
}

// newHandler serves the sources, pages and mock LLM. addr is the host:port the
// returned article URLs point at.
func newHandler(pagesDir, addr, llm string) (http.Handler, error) {
	if llm != llmOK && llm != llmFail {
		return nil, fmt.Errorf("-llm must be %q or %q, got %q", llmOK, llmFail, llm)
	}
	files, err := filepath.Glob(filepath.Join(pagesDir, "*", "source.html"))
	if err != nil {
		return nil, err
	}
	if pagesDir == "" || len(files) == 0 {
		return nil, fmt.Errorf("no <name>/source.html pages in %q", pagesDir)
	}
	// Glob returns sorted paths, so ids and scores are stable.
	var names []string
	pages := map[string]string{}
	for _, f := range files {
		name := filepath.Base(filepath.Dir(f))
		names = append(names, name)
		pages[name] = f
	}
	pageURL := func(name string) string { return "http://" + addr + "/p/" + url.PathEscape(name) + "/" }
	title := func(name string) string { return "Fixture " + name }

	mux := http.NewServeMux()
	mux.HandleFunc("GET /hn/search", func(w http.ResponseWriter, _ *http.Request) {
		hits := []map[string]any{}
		for i, name := range names {
			hits = append(hits, map[string]any{"objectID": strconv.Itoa(i), "title": title(name),
				"url": pageURL(name), "points": 100 + i, "created_at_i": time.Now().Unix()})
		}
		writeJSON(w, map[string]any{"hits": hits})
	})
	mux.HandleFunc("GET /lobsters/hottest.json", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []any{})
	})
	mux.HandleFunc("GET /feed.xml", func(w http.ResponseWriter, _ *http.Request) {
		var items []rssItem
		for _, name := range names[:min(feedItems, len(names))] {
			items = append(items, rssItem{Title: title(name), Link: pageURL(name)})
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_ = xml.NewEncoder(w).Encode(struct {
			XMLName xml.Name  `xml:"rss"`
			Version string    `xml:"version,attr"`
			Title   string    `xml:"channel>title"`
			Items   []rssItem `xml:"channel>item"`
		}{Version: "2.0", Title: "fixtures", Items: items})
	})
	mux.HandleFunc("GET /p/{name}/", func(w http.ResponseWriter, r *http.Request) {
		path, ok := pages[r.PathValue("name")] // only globbed files are readable
		if !ok {
			http.NotFound(w, r)
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("POST /openrouter/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if llm == llmFail {
			http.Error(w, "mock failure", http.StatusInternalServerError)
			return
		}
		mockCompletion(w, r)
	})
	return mux, nil
}

// mockCompletion picks the first n candidates listed in the user prompt.
func mockCompletion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || len(req.Messages) != 2 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	m := wantRe.FindStringSubmatch(req.Messages[0].Content)
	if m == nil {
		http.Error(w, "no item count in the system prompt", http.StatusBadRequest)
		return
	}
	want, _ := strconv.Atoi(m[1])
	var picks []string
	for _, line := range lineRe.FindAllStringSubmatch(req.Messages[1].Content, want) {
		picks = append(picks, `{"id":`+line[1]+`,"reason":"bench"}`)
	}
	writeJSON(w, map[string]any{
		"model":   req.Model,
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": `{"picks":[` + strings.Join(picks, ",") + `]}`}}},
		"usage":   map[string]any{"prompt_tokens": len(req.Messages[1].Content) / 4, "completion_tokens": 15 * len(picks)},
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
