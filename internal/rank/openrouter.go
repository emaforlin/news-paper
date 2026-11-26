package rank

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emaforlin/news-paper/internal/httpx"
	"github.com/emaforlin/news-paper/internal/news"
)

const (
	DefaultBaseURL = "https://openrouter.ai/api/v1"
	// DefaultTimeout applies when OpenRouter.HC is nil.
	DefaultTimeout = 90 * time.Second
	// DefaultLanguage is the language of the reasons shown on the front page.
	DefaultLanguage = "es"

	maxReply     = 1 << 20 // 1 MiB
	snippetBytes = 200
)

// ErrNotConfigured is returned without making a request.
var ErrNotConfigured = errors.New("openrouter: api_key/model not configured")

// OpenRouter ranks with a single chat completion request.
type OpenRouter struct {
	HC       *httpx.Client // nil → a client with DefaultTimeout
	BaseURL  string        // "" → DefaultBaseURL
	APIKey   string
	Model    string
	Profile  string
	Avoid    []string
	Language string       // "" → DefaultLanguage
	Log      *slog.Logger // token usage; nil → discarded
}

func (o *OpenRouter) Name() string { return "openrouter" }

// String, GoString and LogValue redact the API key.
func (o OpenRouter) String() string {
	key := ""
	if o.APIKey != "" {
		key = "[redacted]"
	}
	return fmt.Sprintf("{BaseURL:%s APIKey:%s Model:%s Language:%s}", o.BaseURL, key, o.Model, o.Language)
}

func (o OpenRouter) GoString() string { return "rank.OpenRouter" + o.String() }

func (o OpenRouter) LogValue() slog.Value { return slog.StringValue(o.String()) }

type chatRequest struct {
	Model          string         `json:"model"`
	Temperature    float64        `json:"temperature"`
	ResponseFormat responseFormat `json:"response_format"`
	MaxTokens      int            `json:"max_tokens"`
	Messages       []chatMessage  `json:"messages"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Rank asks the model for the n best candidates. It sends at most one request.
func (o *OpenRouter) Rank(ctx context.Context, cands []news.Candidate, n int) ([]news.Pick, error) {
	if o.APIKey == "" || o.Model == "" || strings.HasPrefix(o.Model, "CHANGE-ME") {
		return nil, ErrNotConfigured
	}
	if n <= 0 || len(cands) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(chatRequest{
		Model:          o.Model,
		Temperature:    0.2,
		ResponseFormat: responseFormat{Type: "json_object"},
		MaxTokens:      200 + 60*n,
		Messages: []chatMessage{
			{Role: "system", Content: o.systemPrompt(n)},
			{Role: "user", Content: userPrompt(cands)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}

	h := http.Header{
		"Authorization": {"Bearer " + o.APIKey},
		"Content-Type":  {"application/json"},
		"X-Title":       {"diario"},
	}
	resp, err := o.client().Post(ctx, o.endpoint(), h, body, maxReply)
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}

	var r chatResponse
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, fmt.Errorf("openrouter: status %d: non-JSON reply: %q", resp.StatusCode, snippet(resp.Body))
	}
	o.logUsage(r)
	if r.Error != nil && r.Error.Message != "" {
		return nil, fmt.Errorf("openrouter: %s", r.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter: status %d", resp.StatusCode)
	}
	if len(r.Choices) == 0 {
		return nil, errors.New("openrouter: reply had no choices")
	}
	return parsePicks(r.Choices[0].Message.Content, cands, n)
}

func (o *OpenRouter) client() *httpx.Client {
	if o.HC != nil {
		return o.HC
	}
	return httpx.New(DefaultTimeout, "")
}

func (o *OpenRouter) endpoint() string {
	base := o.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	return strings.TrimRight(base, "/") + "/chat/completions"
}

func (o *OpenRouter) logUsage(r chatResponse) {
	if r.Usage == nil || o.Log == nil {
		return
	}
	model := r.Model
	if model == "" {
		model = o.Model
	}
	o.Log.Info("llm usage", "model", model,
		"prompt_tokens", r.Usage.PromptTokens, "completion_tokens", r.Usage.CompletionTokens)
}

func (o *OpenRouter) systemPrompt(n int) string {
	profile := strings.TrimSpace(o.Profile)
	if profile == "" {
		profile = "(no profile given)"
	}
	var avoid []string
	for _, a := range o.Avoid {
		if a = strings.TrimSpace(a); a != "" {
			avoid = append(avoid, a)
		}
	}
	lang := o.Language
	if lang == "" {
		lang = DefaultLanguage
	}

	lines := []string{
		"You are the editor of a one-reader morning newspaper read on a Kindle.",
		fmt.Sprintf("From the numbered candidates, choose the %d items this reader will most want to read today.", n),
		"Reader profile:",
		profile,
	}
	if len(avoid) > 0 {
		lines = append(lines, "Avoid: "+strings.Join(avoid, ", "))
	}
	lines = append(lines,
		"Rules:",
		"- Prefer substantive articles and essays (things worth 5-30 minutes of reading) over product launches, link lists, job posts, videos or paywalled news.",
		"- Diversify topics; never pick two items about the same story.",
		"- Order best first.",
		`Answer ONLY with JSON: {"picks":[{"id":<number>,"reason":"<max 12 words, language: `+lang+`>"}]}`,
	)
	return strings.Join(lines, "\n")
}

// userPrompt lists the candidates, one per line, numbered from 0.
func userPrompt(cands []news.Candidate) string {
	lines := make([]string, len(cands))
	for i, c := range cands {
		line := fmt.Sprintf("%d. [%s", i, c.Source)
		if c.Score > 0 {
			line += fmt.Sprintf(" %d", c.Score)
		}
		line += "] " + c.Title
		if d := c.Domain(); d != "" {
			line += " — " + d
		}
		if s := collapse(c.Summary); s != "" {
			line += " — " + s
		}
		lines[i] = collapse(line)
	}
	return strings.Join(lines, "\n")
}

// flexInt accepts an integer, an integral float or a numeric string. Anything
// else leaves it invalid without failing the surrounding document.
type flexInt struct {
	v  int
	ok bool
}

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := string(b)
	var str string
	if json.Unmarshal(b, &str) == nil && str != "" {
		s = str
	}
	x, err := strconv.ParseFloat(s, 64)
	if err == nil && x == math.Trunc(x) && math.Abs(x) <= math.MaxInt32 {
		f.v, f.ok = int(x), true
	}
	return nil
}

// parsePicks reads the model's answer, tolerating code fences and prose
// around the JSON object.
func parsePicks(content string, cands []news.Candidate, n int) ([]news.Pick, error) {
	i, j := strings.Index(content, "{"), strings.LastIndex(content, "}")
	if i < 0 || j < i {
		return nil, errors.New("openrouter: no JSON object in reply")
	}
	var v struct {
		Picks []struct {
			ID     flexInt `json:"id"`
			Reason string  `json:"reason"`
		} `json:"picks"`
	}
	if err := json.Unmarshal([]byte(content[i:j+1]), &v); err != nil {
		return nil, fmt.Errorf("openrouter: parse reply: %w", err)
	}

	var picks []news.Pick
	seen := map[int]bool{}
	for _, p := range v.Picks {
		id := p.ID.v
		if !p.ID.ok || id < 0 || id >= len(cands) || seen[id] {
			continue
		}
		seen[id] = true
		picks = append(picks, news.Pick{Candidate: cands[id], Reason: collapse(p.Reason)})
	}
	if len(picks) == 0 {
		return nil, errors.New("openrouter: reply had no valid picks")
	}
	return picks[:min(len(picks), n)], nil
}

// snippet returns the first bytes of b, cut at a rune boundary.
func snippet(b []byte) string {
	if len(b) > snippetBytes {
		cut := snippetBytes
		for cut > 0 && !utf8.RuneStart(b[cut]) {
			cut--
		}
		b = b[:cut]
	}
	return strings.ToValidUTF8(string(b), "�")
}
