package rank

import (
	"reflect"
	"testing"

	"github.com/emaforlin/news-paper/internal/news"
)

func TestPrefilterScenario(t *testing.T) {
	first := cand("https://www.a.com/x/", "hn", 50)
	first.Title = "A"
	first.Tags = []string{"db"}
	dup := cand("http://a.com/x", "lobsters", 80)
	dup.Title = "A (lobsters)"
	dup.Discussion = "d"
	dup.Summary = "s"
	seenCand := cand("https://seen.com/old", "hn", 10)
	in := []news.Candidate{
		first,
		dup,
		cand("https://sub.paywall.com/p", "hn", 500),
		cand("https://b.com/paper.PDF?dl=1", "lobsters", 30),
		seenCand,
		{URL: "", Title: "x"},
		cand("https://c.com/ok", "feed:c", 0),
	}
	orig := append([]news.Candidate(nil), in...)

	got := Prefilter(in, fakeSeen{seenCand.ID(): true}, []string{"paywall.com"})

	merged := first
	merged.Score = 80
	merged.Source = "hn+lobsters"
	merged.Discussion = "d"
	merged.Summary = "s"
	want := []news.Candidate{merged, in[6]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if !reflect.DeepEqual(in, orig) {
		t.Error("input slice was modified")
	}
}

func TestMergeSource(t *testing.T) {
	tests := []struct{ a, b, want string }{
		{"feed:go", "feed:golang", "feed:go+feed:golang"},
		{"hn+lobsters", "lobsters", "hn+lobsters"},
		{"hn", "lobsters+hn+feed:x", "hn+lobsters+feed:x"},
		{"hn", "hn", "hn"},
		{"", "hn", "hn"},
		{"hn", "", "hn"},
	}
	for _, tt := range tests {
		if got := mergeSource(tt.a, tt.b); got != tt.want {
			t.Errorf("mergeSource(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}

	// Three duplicates fold into one candidate.
	in := []news.Candidate{
		cand("https://a.com/x", "hn", 1),
		cand("https://www.a.com/x", "lobsters", 3),
		cand("http://a.com/x/", "hn", 2),
	}
	got := Prefilter(in, nil, nil)
	if len(got) != 1 || got[0].Source != "hn+lobsters" || got[0].Score != 3 {
		t.Errorf("got %+v", got)
	}
}

func TestPrefilterMergeFields(t *testing.T) {
	a := cand("https://a.com/x", "hn", 5)
	a.Discussion, a.Summary = "da", "sa"
	b := cand("https://a.com/x", "lobsters", 1)
	b.Discussion, b.Summary, b.Title, b.Tags = "db", "sb", "other", []string{"t"}
	got := Prefilter([]news.Candidate{a, b}, nil, nil)
	want := a
	want.Source = "hn+lobsters"
	if !reflect.DeepEqual(got, []news.Candidate{want}) {
		t.Errorf("first non-empty must win:\n got %+v\nwant %+v", got, want)
	}
}

func TestPrefilterDeny(t *testing.T) {
	in := []news.Candidate{
		cand("https://sub.paywall.com/1", "hn", 0),
		cand("https://paywall.com/2", "hn", 0),
		cand("https://www.paywall.com/3", "hn", 0),
		cand("https://a.b.nyt.example/4", "hn", 0),
		cand("https://notpaywall.com/5", "hn", 0),
		cand("https://paywall.com.ar/6", "hn", 0),
		cand("https://localhost/7", "hn", 0),
	}
	tests := []struct {
		name string
		deny []string
		want []string
	}{
		{"parents", []string{"paywall.com", "nyt.example"}, []string{"https://notpaywall.com/5", "https://paywall.com.ar/6", "https://localhost/7"}},
		{"normalised", []string{" WWW.Paywall.com ", "", "  "}, []string{"https://a.b.nyt.example/4", "https://notpaywall.com/5", "https://paywall.com.ar/6", "https://localhost/7"}},
		{"single label", []string{"localhost"}, candURLs(in[:6])},
		{"nil", nil, candURLs(in)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := candURLs(Prefilter(in, nil, tt.deny)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %v\nwant %v", got, tt.want)
			}
		})
	}
}

func TestPrefilterRules(t *testing.T) {
	var in []news.Candidate
	for _, ext := range []string{".pdf", ".mp4", ".mp3", ".zip", ".png", ".jpg", ".jpeg", ".gif", ".webm", ".mov", ".JPG", ".Pdf"} {
		in = append(in, cand("https://x.com/file"+ext+"?q=1", "hn", 0))
	}
	keep := []string{"https://x.com/a.html", "https://x.com/plain", "https://x.com/pdf/", "https://x.com/q?f=a.pdf"}
	for _, u := range keep {
		in = append(in, cand(u, "hn", 0))
	}
	blank := cand("https://x.com/blank", "hn", 0)
	blank.Title = "   "
	noURL := cand(" ", "hn", 0)
	bad := cand("http://[::1", "hn", 0) // unparsable: not a non-HTML extension, kept
	in = append(in, blank, noURL, bad)

	// A duplicate of a seen candidate is dropped too, not merged.
	seen := cand("https://x.com/seen", "hn", 0)
	in = append(in, seen, cand("https://www.x.com/seen/", "lobsters", 9))

	got := candURLs(Prefilter(in, fakeSeen{seen.ID(): true}, nil))
	want := append(append([]string{}, keep...), "http://[::1")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
	if got := Prefilter(nil, nil, nil); got == nil || len(got) != 0 {
		t.Errorf("empty input: got %#v, want empty non-nil", got)
	}
}
