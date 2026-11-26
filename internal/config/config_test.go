package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// noEnv keeps a developer's real secrets out of the tests.
func noEnv(string) string { return "" }

func load(t *testing.T, src string) *Config {
	t.Helper()
	c, err := loadBytes([]byte(src), "config.json", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func loadErr(t *testing.T, src string) string {
	t.Helper()
	c, err := loadBytes([]byte(src), "config.json", noEnv)
	if err == nil || c != nil {
		t.Fatalf("loaded %q: %+v, %v", src, c, err)
	}
	return err.Error()
}

func intp(v int) *int { return &v }

func TestLoadExamples(t *testing.T) {
	t.Setenv(EnvOpenRouterKey, "")
	t.Setenv(EnvSMTPPassword, "")
	for _, path := range []string{"../../config.example.json", "../../testdata/bench.config.json"} {
		c, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if c.Rank.APIKey != "" || c.SMTP.Password != "" {
			t.Errorf("%s carries secrets", path)
		}
	}
}

func TestDefaults(t *testing.T) {
	c := load(t, `{}`)
	want := &Config{
		Edition: Edition{Title: "Diario", Language: "en", Timezone: "America/Argentina/Buenos_Aires",
			Picks: intp(10), Overselect: intp(8), MaxPerDomain: 2},
		Sources: Sources{
			MaxAgeHours: 72,
			HackerNews:  HackerNews{APIBase: "https://hn.algolia.com/api/v1", MinPoints: intp(100), Limit: 60},
			Lobsters:    Lobsters{URL: "https://lobste.rs/hottest.json", Limit: 25},
		},
		Rank:    Rank{Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1", TimeoutSec: 90},
		Extract: Extract{MinWords: 250, MaxBodyBytes: 2097152, Concurrency: 4},
		SMTP:    SMTP{Port: 587, TLSMode: "starttls"},
		State:   State{Path: "state.json", RetentionDays: 30},
		HTTP:    HTTP{UserAgent: "diario/0.1 (+personal Kindle digest)", TimeoutSec: 25, Concurrency: 4},
		Runtime: Runtime{RunTimeoutMin: 9},
		loc:     c.loc,
	}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("defaults:\n got %+v\nwant %+v", c, want)
	}
	if got := c.Location().String(); got != "America/Argentina/Buenos_Aires" {
		t.Errorf("location = %s", got)
	}
	if f := load(t, `{"sources":{"feeds":[{"name":"a","url":"https://a.test/f"}]}}`).Sources.Feeds[0]; f.Limit != 20 {
		t.Errorf("feed limit = %d", f.Limit)
	}
}

func TestComments(t *testing.T) {
	c := load(t, "\xEF\xBB\xBF// header\n{\n  // indented\r\n\t// tab\n  \"smtp\": {\"host\": \"https://x\"}\n}\n// tail")
	if c.SMTP.Host != "https://x" {
		t.Errorf("host = %q", c.SMTP.Host)
	}

	tests := []struct{ name, src, want string }{
		{"syntax error line", "// a\n// b\n{\n  \"edition\": {\n\n    // c\n    \"title\" \"x\"\n  }\n}", "config.json:7:13: "},
		{"trailing comment", "{\n  \"state\": {\"path\": \"a\"} // x\n}", "config.json:2:"},
		{"type error line", "{\n  \"edition\": {\"picks\": \"10\"}\n}", "config.json:2:"},
		{"truncated", `{"edition":`, "config.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := loadErr(t, tt.src); !strings.HasPrefix(got, tt.want) {
				t.Errorf("err = %q, want prefix %q", got, tt.want)
			}
		})
	}
}

func TestStrict(t *testing.T) {
	tests := []struct{ src, want string }{
		{`{"edtion":{}}`, `unknown field "edtion"`},
		{`{"edition":{"fallback_utc_offset_hours":-3}}`, "fallback_utc_offset_hours"},
		{`{"extract":{"images":false}}`, "images"},
		{`{"runtime":{"mem_limit_mib":64}}`, "mem_limit_mib"},
		{`{"runtime":{"gc_percent":50}}`, "gc_percent"},
		{`{}}`, "unexpected data after"},
		{`{} {}`, "unexpected data after"},
		{`{} x`, "unexpected data after"},
		{`[]`, "must be a JSON object"},
		{`null`, "must be a JSON object"},
		{"// only comments\n", "must be a JSON object"},
		{`{"edition":{"picks":"10"}}`, "picks"},
	}
	for _, tt := range tests {
		if got := loadErr(t, tt.src); !strings.Contains(got, tt.want) || !strings.HasPrefix(got, "config.json") {
			t.Errorf("%s: err = %q, want %q", tt.src, got, tt.want)
		}
	}
}

func TestZeroVsMissing(t *testing.T) {
	tests := []struct {
		src                   string
		picks, over, min, cap int
	}{
		{`{}`, 10, 8, 100, 2},
		{`{"edition":{"picks":null,"overselect":null},"sources":{"hackernews":{"min_points":null}}}`, 10, 8, 100, 2},
		{`{"edition":{"overselect":0,"max_per_domain":0},"sources":{"hackernews":{"min_points":0}}}`, 10, 0, 0, 2},
		{`{"edition":{"picks":5,"max_per_domain":-1}}`, 5, 8, 100, -1},
	}
	for _, tt := range tests {
		c := load(t, tt.src)
		got := [4]int{*c.Edition.Picks, *c.Edition.Overselect, *c.Sources.HackerNews.MinPoints, c.Edition.MaxPerDomain}
		if want := [4]int{tt.picks, tt.over, tt.min, tt.cap}; got != want {
			t.Errorf("%s: got %v, want %v", tt.src, got, want)
		}
	}
	if got := loadErr(t, `{"edition":{"picks":0}}`); !strings.Contains(got, "edition.picks: must be in 1..99, got 0") {
		t.Errorf("err = %q", got)
	}
}

func TestEnvOverrides(t *testing.T) {
	src := []byte(`{"rank":{"api_key":"file-key"},"smtp":{"password":"file-pass"}}`)
	tests := []struct {
		env       map[string]string
		key, pass string
	}{
		{map[string]string{EnvOpenRouterKey: "env-key", EnvSMTPPassword: "env-pass"}, "env-key", "env-pass"},
		{map[string]string{EnvOpenRouterKey: "", EnvSMTPPassword: ""}, "file-key", "file-pass"},
		{nil, "file-key", "file-pass"},
	}
	for _, tt := range tests {
		c, err := loadBytes(src, "x", func(k string) string { return tt.env[k] })
		if err != nil {
			t.Fatal(err)
		}
		if string(c.Rank.APIKey) != tt.key || string(c.SMTP.Password) != tt.pass {
			t.Errorf("env %v: key %q pass %q", tt.env, string(c.Rank.APIKey), string(c.SMTP.Password))
		}
	}
	if c := load(t, `{}`); c.Rank.APIKey != "" || c.SMTP.Password != "" {
		t.Error("secrets from nowhere")
	}

	// LoadBytes reads the real environment.
	t.Setenv(EnvOpenRouterKey, "from-env")
	t.Setenv(EnvSMTPPassword, "")
	c, err := LoadBytes([]byte(`{}`), "x")
	if err != nil || string(c.Rank.APIKey) != "from-env" {
		t.Errorf("LoadBytes: %v", err)
	}
}

func TestSecretRedaction(t *testing.T) {
	c := load(t, `{"rank":{"api_key":"sk-SECRET1"},"smtp":{"password":"SECRET2"}}`)
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("c", "cfg", c, "key", c.Rank.APIKey)
	js, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{fmt.Sprint(c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprintf("%v", *c), string(js), logged.String()} {
		if strings.Contains(s, "SECRET") {
			t.Errorf("leaked: %s", s)
		}
	}
	if !strings.Contains(string(js), "[redacted]") || string(c.SMTP.Password) != "SECRET2" {
		t.Errorf("redaction changed the value or didn't show: %s", js)
	}
	if got := Secret("").String(); got != "" {
		t.Errorf("empty secret = %q", got)
	}
}

func TestHelpers(t *testing.T) {
	c := load(t, `{"http":{"timeout_sec":3},"rank":{"timeout_sec":4,"base_url":"http://x.test/v1//"},"runtime":{"run_timeout_min":5}}`)
	if c.HTTPTimeout() != 3*time.Second || c.RankTimeout() != 4*time.Second || c.RunTimeout() != 5*time.Minute {
		t.Errorf("timeouts: %s %s %s", c.HTTPTimeout(), c.RankTimeout(), c.RunTimeout())
	}
	if c.Rank.BaseURL != "http://x.test/v1" {
		t.Errorf("base url = %q", c.Rank.BaseURL)
	}
	if c.Location() != c.Location() {
		t.Error("location not cached")
	}
	if (&Config{}).Location() != time.UTC {
		t.Error("zero config location")
	}
}

func TestLoad(t *testing.T) {
	t.Setenv(EnvOpenRouterKey, "")
	t.Setenv(EnvSMTPPassword, "")
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	bad := filepath.Join(dir, "bad.json")
	for path, src := range map[string]string{good: `{}`, bad: `{"x":1}`} {
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Load(good); err != nil {
		t.Error(err)
	}
	for _, path := range []string{bad, filepath.Join(dir, "missing.json")} {
		c, err := Load(path)
		if c != nil || err == nil || !strings.HasPrefix(err.Error(), path) {
			t.Errorf("Load(%s) = %v, %v", path, c, err)
		}
	}
	_, err := LoadBytes([]byte(`{"x":1}`), "s3://b/config.json")
	if err == nil || !strings.HasPrefix(err.Error(), "s3://b/config.json: ") {
		t.Errorf("err = %v", err)
	}
}
