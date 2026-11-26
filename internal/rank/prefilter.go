package rank

import (
	"net/url"
	"path"
	"strings"

	"github.com/emaforlin/news-paper/internal/news"
)

// skipExts are path extensions that point at non-HTML resources.
var skipExts = map[string]bool{
	".pdf": true, ".mp4": true, ".mp3": true, ".zip": true, ".png": true,
	".jpg": true, ".jpeg": true, ".gif": true, ".webm": true, ".mov": true,
}

// Prefilter drops unusable, already seen, denied and non-HTML candidates and
// merges duplicates (same URLID) into their first occurrence. The output
// keeps the order of first occurrence; the input is not modified. seen may
// be nil.
func Prefilter(cands []news.Candidate, seen Seen, denyDomains []string) []news.Candidate {
	deny := denySet(denyDomains)
	kept := []news.Candidate{}
	index := map[string]int{} // id → position in kept
	for _, c := range cands {
		if strings.TrimSpace(c.URL) == "" || strings.TrimSpace(c.Title) == "" {
			continue
		}
		id := c.ID()
		if i, ok := index[id]; ok {
			kept[i] = merge(kept[i], c)
			continue
		}
		if seen != nil && seen.Has(id) {
			continue
		}
		if denied(c.Domain(), deny) || skipExt(c.URL) {
			continue
		}
		index[id] = len(kept)
		kept = append(kept, c)
	}
	return kept
}

// denySet normalises the deny list: lowercase, trimmed, without "www.".
func denySet(domains []string) map[string]bool {
	set := make(map[string]bool, len(domains))
	for _, d := range domains {
		d = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), "www.")
		if d != "" {
			set[d] = true
		}
	}
	return set
}

// denied reports whether domain or any parent domain is in deny.
func denied(domain string, deny map[string]bool) bool {
	if len(deny) == 0 {
		return false
	}
	for d := domain; d != ""; {
		if deny[d] {
			return true
		}
		i := strings.IndexByte(d, '.')
		if i < 0 {
			break
		}
		d = d[i+1:]
	}
	return false
}

// skipExt reports whether the URL path ends in a non-HTML extension.
func skipExt(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return skipExts[strings.ToLower(path.Ext(u.Path))]
}

// merge folds the duplicate b into the kept candidate a.
func merge(a, b news.Candidate) news.Candidate {
	a.Score = max(a.Score, b.Score)
	a.Source = mergeSource(a.Source, b.Source)
	if a.Discussion == "" {
		a.Discussion = b.Discussion
	}
	if a.Summary == "" {
		a.Summary = b.Summary
	}
	return a
}

// mergeSource joins "+"-separated source tokens without repeating any.
func mergeSource(a, b string) string {
	var toks []string
	have := map[string]bool{}
	for _, t := range strings.Split(a+"+"+b, "+") {
		if t != "" && !have[t] {
			have[t] = true
			toks = append(toks, t)
		}
	}
	return strings.Join(toks, "+")
}
