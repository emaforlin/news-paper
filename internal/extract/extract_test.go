package extract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
)

// asRejected returns the *Rejected in err's chain, or nil.
func asRejected(err error) *Rejected {
	var rej *Rejected
	if errors.As(err, &rej) {
		return rej
	}
	return nil
}

func TestRejectedError(t *testing.T) {
	r := &Rejected{Reason: "x"}
	if r.Error() != "rejected: x" {
		t.Errorf("Error() = %q", r.Error())
	}
	wrapped := fmt.Errorf("ctx: %w", r)
	if got := asRejected(wrapped); got != r {
		t.Errorf("errors.As through wrapping = %v", got)
	}
}

func TestExtractOK(t *testing.T) {
	srv := serve(t, htmlHandler(page(pageSpec{words: 400, title: "A fine article", lang: "en"})))
	p := pick(srv.URL + "/post")
	art, err := newExtractor(t).Extract(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if art.Words != 400 || art.Title != "A fine article" || art.Language != "en" {
		t.Errorf("article = %d words, title %q, lang %q", art.Words, art.Title, art.Language)
	}
	if strings.Contains(art.XHTML, "Home About") {
		t.Errorf("navigation leaked into XHTML: %s", art.XHTML[:200])
	}
	checkOutput(t, art.XHTML, art.Words)
}

func TestExtractRejectStatus(t *testing.T) {
	for _, code := range []int{401, 402, 403, 404, 410} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var hits atomic.Int32
			srv := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(code)
			}))
			_, err := newExtractor(t).Extract(context.Background(), pick(srv.URL))
			rej := asRejected(err)
			if rej == nil || rej.Reason != fmt.Sprintf("HTTP %d", code) {
				t.Fatalf("err = %v, want Rejected(HTTP %d)", err, code)
			}
			if hits.Load() != 1 {
				t.Errorf("hits = %d, want 1 (no retry)", hits.Load())
			}
		})
	}
}

func TestExtractTransient(t *testing.T) {
	status := func(codes ...int) http.HandlerFunc {
		var hits atomic.Int32
		return func(w http.ResponseWriter, r *http.Request) {
			i := int(hits.Add(1)) - 1
			w.WriteHeader(codes[min(i, len(codes)-1)])
		}
	}
	block := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name    string
		h       http.Handler
		ctx     context.Context
		timeout time.Duration
	}{
		{"500 twice", status(500), context.Background(), 0},
		{"429 twice", status(429), context.Background(), 0},
		{"304", status(304), context.Background(), 0},
		{"client timeout", block, context.Background(), 50 * time.Millisecond},
		{"cancelled ctx", status(200), cancelled, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := serve(t, tt.h)
			e := newExtractor(t)
			if tt.timeout > 0 {
				e.HC = testClientTimeout(t, tt.timeout)
			}
			_, err := e.Extract(tt.ctx, pick(srv.URL))
			if err == nil || asRejected(err) != nil {
				t.Fatalf("err = %v, want a transient (non-Rejected) error", err)
			}
		})
	}

	t.Run("503 then 200 succeeds", func(t *testing.T) {
		var hits atomic.Int32
		body := page(pageSpec{words: 300})
		srv := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hits.Add(1) == 1 {
				w.WriteHeader(503)
				return
			}
			htmlHandler(body)(w, r)
		}))
		if _, err := newExtractor(t).Extract(context.Background(), pick(srv.URL)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestExtractContentType(t *testing.T) {
	body := page(pageSpec{words: 300})
	tests := []struct {
		ct     string
		reject string // "" means accepted
	}{
		{"application/pdf", "not HTML: application/pdf"},
		{"image/png", "not HTML: image/png"},
		{"", ""},
		{"TEXT/HTML; charset=utf-8", ""},
		{"application/xhtml+xml", ""},
	}
	for _, tt := range tests {
		t.Run(tt.ct, func(t *testing.T) {
			srv := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header()["Content-Type"] = []string{tt.ct} // "" must stay empty, not be sniffed
				_, _ = io.WriteString(w, body)
			}))
			_, err := newExtractor(t).Extract(context.Background(), pick(srv.URL))
			if tt.reject == "" {
				if err != nil {
					t.Fatalf("err = %v, want success", err)
				}
				return
			}
			if rej := asRejected(err); rej == nil || rej.Reason != tt.reject {
				t.Fatalf("err = %v, want Rejected(%s)", err, tt.reject)
			}
		})
	}
}

func TestExtractPaywallMarkup(t *testing.T) {
	tests := []struct {
		jsonld string
		reject bool
	}{
		{`{"@type":"NewsArticle","isAccessibleForFree": "False"}`, true},
		{`{"@type":"NewsArticle","isAccessibleForFree":false}`, true},
		{`{"@type":"NewsArticle","isAccessibleForFree" : "false"}`, true},
		{`{"@type":"NewsArticle","isAccessibleForFree": "true"}`, false},
		{`{"@type":"NewsArticle"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.jsonld, func(t *testing.T) {
			head := `<script type="application/ld+json">` + tt.jsonld + `</script>`
			srv := serve(t, htmlHandler(page(pageSpec{words: 300, head: head})))
			_, err := newExtractor(t).Extract(context.Background(), pick(srv.URL))
			rej := asRejected(err)
			if tt.reject && (rej == nil || rej.Reason != "isAccessibleForFree=false") {
				t.Fatalf("err = %v, want paywall rejection", err)
			}
			if !tt.reject && err != nil {
				t.Fatalf("err = %v, want success", err)
			}
		})
	}
}

func TestExtractCharset(t *testing.T) {
	// "año café" in ISO-8859-1.
	latin1 := "<p>Sentinel a\xf1o caf\xe9 here.</p>"
	tests := []struct {
		name, ct, head string
	}{
		{"header", "text/html; charset=iso-8859-1", ""},
		{"meta", "text/html", `<meta charset="iso-8859-1">`},
		{"http-equiv", "text/html", `<meta http-equiv="Content-Type" content="text/html; charset=ISO-8859-1">`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := page(pageSpec{words: 300, head: tt.head, before: latin1})
			srv := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.ct)
				_, _ = io.WriteString(w, body)
			}))
			art, err := newExtractor(t).Extract(context.Background(), pick(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(art.XHTML, "año café") {
				t.Errorf("XHTML lacks decoded text: %.200s", art.XHTML)
			}
		})
	}
}

func TestDecodeAndParseErrors(t *testing.T) {
	boom := errors.New("boom")
	_, err := decodeAndParse(iotest.ErrReader(boom), "text/html")
	if rej := asRejected(err); rej == nil || !strings.HasPrefix(rej.Reason, "charset: ") {
		t.Errorf("prescan failure: err = %v, want Rejected(charset: …)", err)
	}

	r := io.MultiReader(strings.NewReader("<p>"+strings.Repeat("x", 2048)), iotest.ErrReader(boom))
	_, err = decodeAndParse(r, "text/html")
	if rej := asRejected(err); rej == nil || !strings.HasPrefix(rej.Reason, "parse: ") {
		t.Errorf("read failure: err = %v, want Rejected(parse: …)", err)
	}

	// Real input that the parser refuses: nesting beyond its limit.
	deep := strings.Repeat("<div>", 600) + "x"
	srv := serve(t, htmlHandler(deep))
	_, err = newExtractor(t).Extract(context.Background(), pick(srv.URL))
	if rej := asRejected(err); rej == nil || !strings.HasPrefix(rej.Reason, "parse: ") {
		t.Errorf("deep page: err = %v, want Rejected(parse: …)", err)
	}
}

func TestExtractNoContent(t *testing.T) {
	for _, body := range []string{"", "<html><body></body></html>", "<html><body><p>   </p></body></html>"} {
		t.Run(fmt.Sprintf("%q", body), func(t *testing.T) {
			srv := serve(t, htmlHandler(body))
			_, err := newExtractor(t).Extract(context.Background(), pick(srv.URL))
			if asRejected(err) == nil {
				t.Fatalf("err = %v, want *Rejected", err)
			}
		})
	}
}

func TestExtractThresholds(t *testing.T) {
	teaser := "<p>Subscribe to continue reading this story, it is great.</p>"
	tests := []struct {
		name     string
		spec     pageSpec
		minWords int
		reject   string // prefix; "" means accepted
	}{
		{"100 words", pageSpec{words: 100}, 0, "too short (100 words)"},
		{"249 words", pageSpec{words: 249}, 0, "too short (249 words)"},
		{"250 words", pageSpec{words: 250}, 0, ""},
		{"400 words with teaser", pageSpec{words: 400, after: teaser}, 0, "teaser phrase on short page"},
		{"400 words", pageSpec{words: 400}, 0, ""},
		{"700 words with teaser", pageSpec{words: 700, after: teaser}, 0, ""},
		{"100 words, MinWords 50", pageSpec{words: 100}, 50, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := serve(t, htmlHandler(page(tt.spec)))
			e := newExtractor(t)
			e.MinWords = tt.minWords
			art, err := e.Extract(context.Background(), pick(srv.URL))
			if tt.reject == "" {
				if err != nil {
					t.Fatalf("err = %v, want success", err)
				}
				if min := e.minWords(); art.Words < min {
					t.Errorf("Words = %d < MinWords %d", art.Words, min)
				}
				return
			}
			if rej := asRejected(err); rej == nil || !strings.HasPrefix(rej.Reason, tt.reject) {
				t.Fatalf("err = %v, want Rejected(%s…)", err, tt.reject)
			}
		})
	}
}

func TestExtractRedirectBase(t *testing.T) {
	body := page(pageSpec{words: 300, before: `<p>See <a href="x">the other post</a> for more.</p>`})
	mux := http.NewServeMux()
	mux.Handle("/a", http.RedirectHandler("/b/post", http.StatusFound))
	mux.Handle("/b/post", htmlHandler(body))
	srv := serve(t, mux)

	art, err := newExtractor(t).Extract(context.Background(), pick(srv.URL+"/a"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `<a href="` + srv.URL + `/b/x">`; !strings.Contains(art.XHTML, want) {
		t.Errorf("XHTML lacks %s: %.300s", want, art.XHTML)
	}
}

func TestExtractTruncatedBody(t *testing.T) {
	const maxBody = 8 << 10
	tail := "<p>" + strings.Repeat("padding ", 3*maxBody/8) + "</p><p>SENTINEL words after the cap.</p>"
	body := page(pageSpec{words: 300, after: tail})
	if len(body) < 3*maxBody {
		t.Fatalf("body too small: %d", len(body))
	}
	srv := serve(t, htmlHandler(body))
	e := newExtractor(t)
	e.MaxBody = maxBody
	art, err := e.Extract(context.Background(), pick(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(art.XHTML, "SENTINEL") {
		t.Error("content past MaxBody was extracted")
	}
	if !strings.Contains(art.XHTML, vocabulary[0]) || art.Words < 300 {
		t.Errorf("first part missing: %d words", art.Words)
	}
}

func TestExtractArticleFields(t *testing.T) {
	head := `<meta name="author" content="  Jane Doe  "><meta property="og:site_name" content="  The Site ">`
	srv := serve(t, htmlHandler(page(pageSpec{words: 300, lang: " es ", head: head})))
	p := pick(srv.URL)
	art, err := newExtractor(t).Extract(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(art.Pick, p) {
		t.Errorf("Pick changed: %+v", art.Pick)
	}
	if art.Title != p.Title {
		t.Errorf("Title = %q, want fallback %q", art.Title, p.Title)
	}
	if art.Byline != "Jane Doe" || art.SiteName != "The Site" || art.Language != "es" {
		t.Errorf("Byline=%q SiteName=%q Language=%q", art.Byline, art.SiteName, art.Language)
	}
	if art.Words < DefaultMinWords {
		t.Errorf("Words = %d", art.Words)
	}
	checkOutput(t, art.XHTML, art.Words)
}

func TestExtractRequest(t *testing.T) {
	var accept, ua string
	body := page(pageSpec{words: 300})
	srv := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept, ua = r.Header.Get("Accept"), r.UserAgent()
		htmlHandler(body)(w, r)
	}))
	if _, err := newExtractor(t).Extract(context.Background(), pick(srv.URL)); err != nil {
		t.Fatal(err)
	}
	if accept != acceptHeader || ua == "" {
		t.Errorf("Accept=%q User-Agent=%q", accept, ua)
	}
}

func TestExtractZeroValue(t *testing.T) {
	var e Extractor
	if e.minWords() != DefaultMinWords || e.maxBody() != DefaultMaxBody || e.hc() == nil || e.log() == nil {
		t.Errorf("zero Extractor defaults: minWords=%d maxBody=%d", e.minWords(), e.maxBody())
	}
	// The zero value works end to end (the default client reaches loopback).
	srv := serve(t, htmlHandler(page(pageSpec{words: 200})))
	_, err := e.Extract(context.Background(), pick(srv.URL))
	if rej := asRejected(err); rej == nil || !strings.HasPrefix(rej.Reason, "too short (200 words)") {
		t.Errorf("err = %v, want DefaultMinWords to reject 200 words", err)
	}
}
