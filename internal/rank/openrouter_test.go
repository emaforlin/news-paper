package rank

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/emaforlin/news-paper/internal/news"
)

const systemGolden = `You are the editor of a one-reader morning newspaper read on a Kindle.
From the numbered candidates, choose the 2 items this reader will most want to read today.
Reader profile:
Backend engineer.
Avoid: crypto, funding
Rules:
- Prefer substantive articles and essays (things worth 5-30 minutes of reading) over product launches, link lists, job posts, videos or paywalled news.
- Diversify topics; never pick two items about the same story.
- Order best first.
Answer ONLY with JSON: {"picks":[{"id":<number>,"reason":"<max 12 words, language: es>"}]}`

const userGolden = `0. [hn 300] Postgres internals in Go — a.com
1. [lobsters 40] Good news about Google — b.com
2. [feed:dan] My post — c.com — first line second line
3. [feed:x] T — summary`

func goldenCands() []news.Candidate {
	return []news.Candidate{
		{URL: "https://a.com/x", Title: "Postgres internals in Go", Source: "hn", Score: 300},
		{URL: "https://b.com/y", Title: "Good news about Google", Source: "lobsters", Score: 40},
		{URL: "https://c.com/z", Title: "My post", Source: "feed:dan", Summary: "first line\n  second line"},
		{URL: "not a url", Title: "T", Source: "feed:x", Summary: "summary"},
	}
}

func TestOpenRouterRequest(t *testing.T) {
	o, got := mockOR(t, 200, reply(`{"picks":[{"id":1,"reason":"r"}]}`))
	o.Profile, o.Avoid = "  Backend engineer.\n", []string{"crypto", " ", "funding"}

	picks, err := o.Rank(context.Background(), goldenCands(), 2)
	if err != nil || len(picks) != 1 || picks[0].URL != "https://b.com/y" {
		t.Fatalf("picks = %+v, err = %v", picks, err)
	}
	if got.count.Load() != 1 || got.method != "POST" || got.path != "/api/v1/chat/completions" {
		t.Errorf("%d requests, %s %s", got.count.Load(), got.method, got.path)
	}
	for k, want := range map[string]string{
		"Authorization": "Bearer sk-test-123", "Content-Type": "application/json", "X-Title": "diario",
	} {
		if v := got.header.Get(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
	b := got.body
	if b["model"] != "m/x" || b["temperature"] != 0.2 || b["max_tokens"] != float64(320) {
		t.Errorf("body = %v", b)
	}
	if b["response_format"].(map[string]any)["type"] != "json_object" {
		t.Errorf("response_format = %v", b["response_format"])
	}
	msgs := b["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", msgs)
	}
	for i, want := range []struct{ role, content string }{{"system", systemGolden}, {"user", userGolden}} {
		m := msgs[i].(map[string]any)
		if m["role"] != want.role || m["content"] != want.content {
			t.Errorf("message %d = %q:\n%s\nwant\n%s", i, m["role"], m["content"], want.content)
		}
	}
}

func TestSystemPrompt(t *testing.T) {
	p := (&OpenRouter{}).systemPrompt(7)
	if !strings.Contains(p, "choose the 7 items") || !strings.Contains(p, "Reader profile:\n(no profile given)\nRules:") ||
		strings.Contains(p, "Avoid:") || !strings.Contains(p, "language: es>") {
		t.Errorf("prompt:\n%s", p)
	}
	if p := (&OpenRouter{Language: "en"}).systemPrompt(1); !strings.Contains(p, "language: en>") {
		t.Errorf("prompt:\n%s", p)
	}
}

func TestParsePicks(t *testing.T) {
	cands := goldenCands()[:3]
	tests := []struct {
		name, content string
		n             int
		want          string // "id:reason ..."
		errHas        string
	}{
		{"fenced with repeats", "Sure! ```json {\"picks\":[{\"id\":2,\"reason\":\"x\"},{\"id\":2},{\"id\":9},{\"id\":0,\"reason\":\"y\"}]} ```", 5, "2:x 0:y", ""},
		{"prose around", `Here: {"picks":[{"id":1}]} bye`, 5, "1:", ""},
		{"string and float ids", `{"picks":[{"id":"1"},{"id":2.0}]}`, 5, "1: 2:", ""},
		{"invalid ids dropped", `{"picks":[{"id":"x"},{"id":null},{"id":2.5},{"id":-1},{},{"id":""},{"id":1e99},{"id":"NaN"},{"id":true},{"id":0}]}`, 5, "0:", ""},
		{"reason collapsed", `{"picks":[{"id":0,"reason":" a\n b "}]}`, 5, "0:a b", ""},
		{"truncated to n", `{"picks":[{"id":0},{"id":1},{"id":2}]}`, 2, "0: 1:", ""},
		{"no object", "nothing here", 5, "", "no JSON object"},
		{"closing before opening", "} {", 5, "", "no JSON object"},
		{"bad json", `{"picks":[}`, 5, "", "parse reply"},
		{"all invalid", `{"picks":[{"id":9}]}`, 5, "", "no valid picks"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			picks, err := parsePicks(tt.content, cands, tt.n)
			if tt.errHas != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errHas) {
					t.Fatalf("err = %v, want %q", err, tt.errHas)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, p := range picks {
				for i, c := range cands {
					if c.URL == p.URL {
						got = append(got, fmt.Sprintf("%d:%s", i, p.Reason))
					}
				}
			}
			if strings.Join(got, " ") != tt.want {
				t.Errorf("picks = %q, want %q", strings.Join(got, " "), tt.want)
			}
		})
	}
}

func TestOpenRouterErrors(t *testing.T) {
	long := strings.Repeat("a", 199) + "ñ" + strings.Repeat("b", 50)
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"error object", 402, `{"error":{"message":"quota"}}`, "openrouter: quota"},
		{"html 500", 500, "<html>boom</html>", `status 500: non-JSON reply: "<html>boom</html>"`},
		{"cut mid-rune", 500, long, strings.Repeat("a", 199) + `"`},
		{"status without message", 503, `{}`, "openrouter: status 503"},
		{"no choices", 200, `{"choices":[]}`, "no choices"},
		{"not json", 200, "ok", "status 200: non-JSON"},
		{"too big", 200, `{"x":"` + strings.Repeat("a", maxReply) + `"}`, "non-JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, got := mockOR(t, tt.status, tt.body)
			_, err := o.Rank(context.Background(), goldenCands(), 2)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if !utf8.ValidString(err.Error()) || strings.Contains(err.Error(), o.APIKey) {
				t.Errorf("bad error string %q", err)
			}
			if got.count.Load() != 1 {
				t.Errorf("%d requests, want 1", got.count.Load())
			}
		})
	}
}

func TestOpenRouterNotConfigured(t *testing.T) {
	for _, mod := range []func(*OpenRouter){
		func(o *OpenRouter) { o.APIKey = "" },
		func(o *OpenRouter) { o.Model = "" },
		func(o *OpenRouter) { o.Model = "CHANGE-ME/model" },
	} {
		o, got := mockOR(t, 200, "{}")
		mod(o)
		if _, err := o.Rank(context.Background(), goldenCands(), 2); !errors.Is(err, ErrNotConfigured) || got.count.Load() != 0 {
			t.Errorf("err = %v after %d requests", err, got.count.Load())
		}
	}
	o, got := mockOR(t, 200, "{}")
	for _, call := range []func() ([]news.Pick, error){
		func() ([]news.Pick, error) { return o.Rank(context.Background(), goldenCands(), 0) },
		func() ([]news.Pick, error) { return o.Rank(context.Background(), nil, 18) },
	} {
		if picks, err := call(); picks != nil || err != nil || got.count.Load() != 0 {
			t.Errorf("picks = %v, err = %v, %d requests", picks, err, got.count.Load())
		}
	}
}

func TestOpenRouterUsage(t *testing.T) {
	tests := []struct {
		name, body string
		wantModel  string
		wantErr    bool
	}{
		{"model from reply", `{"model":"real/m","usage":{"prompt_tokens":10,"completion_tokens":2},"choices":[{"message":{"content":"{\"picks\":[{\"id\":0}]}"}}]}`, "real/m", false},
		{"invalid picks still logged", `{"usage":{"prompt_tokens":10,"completion_tokens":2},"choices":[{"message":{"content":"{}"}}]}`, "m/x", true},
		{"no usage", reply(`{"picks":[{"id":0}]}`), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, _ := mockOR(t, 200, tt.body)
			log, lines := logCapture(t)
			o.Log = log
			_, err := o.Rank(context.Background(), goldenCands(), 2)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			got := lines()
			if tt.wantModel == "" {
				if len(got) != 0 {
					t.Errorf("logs = %v", got)
				}
				return
			}
			if len(got) != 1 || got[0]["msg"] != "llm usage" || got[0]["model"] != tt.wantModel ||
				got[0]["prompt_tokens"] != float64(10) || got[0]["completion_tokens"] != float64(2) {
				t.Errorf("logs = %v", got)
			}
		})
	}
}

func TestOpenRouterSecrets(t *testing.T) {
	o := &OpenRouter{APIKey: "sk-test-secret", Model: "m/x"}
	var sb strings.Builder
	slog.New(slog.NewJSONHandler(&sb, nil)).Info("cfg", "or", o, "val", *o)
	for _, s := range []string{fmt.Sprint(o), fmt.Sprintf("%+v", o), fmt.Sprintf("%#v", o),
		fmt.Sprintf("%v %#v", *o, *o), sb.String()} {
		if strings.Contains(s, "sk-test-secret") || !strings.Contains(s, "redacted") {
			t.Errorf("leaked or not redacted: %s", s)
		}
	}
	if s := (OpenRouter{}).String(); strings.Contains(s, "redacted") {
		t.Errorf("empty key shown as redacted: %s", s)
	}
}

func TestOpenRouterDefaults(t *testing.T) {
	o := &OpenRouter{APIKey: "sk-test", Model: "m"}
	if got := o.endpoint(); got != "https://openrouter.ai/api/v1/chat/completions" {
		t.Errorf("endpoint = %s", got)
	}
	if got := o.client().HTTP.Timeout; got != DefaultTimeout {
		t.Errorf("timeout = %s", got)
	}
	if o.Name() != "openrouter" {
		t.Errorf("name = %s", o.Name())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // nothing is dialed
	if _, err := o.Rank(ctx, goldenCands(), 1); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}
