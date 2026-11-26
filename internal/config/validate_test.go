package config

import (
	"strings"
	"testing"
)

func TestValidateJoined(t *testing.T) {
	got := loadErr(t, `{
		"edition": {"timezone": "Mars/Olympus", "picks": 0},
		"sources": {"feeds": [
			{"name": "dan", "url": "https://a.test/f"},
			{"name": " dan ", "url": "/atom.xml"}
		]}
	}`)
	for _, want := range []string{"config.json: invalid config: ", "edition.timezone", "edition.picks",
		`sources.feeds[1].name: duplicate name "dan"`, `sources.feeds[1].url: must be an absolute http(s) URL, got "/atom.xml"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestValidateRules(t *testing.T) {
	tests := []struct{ src, want string }{ // want "" = valid
		{`{"edition":{"picks":1}}`, ""},
		{`{"edition":{"picks":99}}`, ""},
		{`{"edition":{"picks":100}}`, "edition.picks"},
		{`{"edition":{"picks":-1}}`, "edition.picks"},
		{`{"edition":{"overselect":-1}}`, "edition.overselect"},
		{`{"edition":{"language":"english"}}`, ""}, // epub falls back
		{`{"edition":{"max_per_domain":-5},"sources":{"max_age_hours":-1}}`, ""},
		{`{"edition":{"timezone":"UTC"}}`, ""},
		{`{"rank":{"provider":"heuristic"}}`, ""},
		{`{"rank":{"provider":"gpt"}}`, "rank.provider"},
		{`{"rank":{"base_url":"ftp://x"}}`, "rank.base_url"},
		{`{"rank":{"base_url":"http://[::1"}}`, "rank.base_url"},
		{`{"rank":{"model":""}}`, ""},
		{`{"smtp":{"tls_mode":"tls","port":465}}`, ""},
		{`{"smtp":{"tls_mode":"ssl"}}`, "smtp.tls_mode"},
		{`{"smtp":{"port":70000}}`, "smtp.port"},
		{`{"smtp":{"port":-1}}`, "smtp.port"},
		{`{"sources":{"hackernews":{"api_base":"hn"}}}`, "sources.hackernews.api_base"},
		{`{"sources":{"hackernews":{"min_points":-5}}}`, "sources.hackernews.min_points"},
		{`{"sources":{"hackernews":{"limit":-1}}}`, "sources.hackernews.limit"},
		{`{"sources":{"lobsters":{"url":"https://"}}}`, "sources.lobsters.url"},
		{`{"sources":{"lobsters":{"limit":-1}}}`, "sources.lobsters.limit"},
		{`{"sources":{"feeds":[{"name":"  ","url":"https://a.test"}]}}`, "sources.feeds[0].name: required"},
		{`{"sources":{"feeds":[{"name":"dan","url":"https://a.test"},{"name":"Dan","url":"https://a.test"}]}}`, ""},
		{`{"sources":{"feeds":[{"name":"a","url":"https://a.test","limit":-1}]}}`, "sources.feeds[0].limit"},
		{`{"extract":{"min_words":-1}}`, "extract.min_words"},
		{`{"extract":{"max_body_bytes":-1}}`, "extract.max_body_bytes"},
		{`{"extract":{"concurrency":-1}}`, "extract.concurrency"},
		{`{"http":{"concurrency":-1}}`, "http.concurrency"},
		{`{"http":{"timeout_sec":-1}}`, "http.timeout_sec"},
		{`{"rank":{"timeout_sec":-1}}`, "rank.timeout_sec"},
		{`{"state":{"retention_days":-1}}`, "state.retention_days"},
		{`{"runtime":{"run_timeout_min":-1}}`, "runtime.run_timeout_min"},
	}
	for _, tt := range tests {
		_, err := loadBytes([]byte(tt.src), "c", noEnv)
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("%s: %v", tt.src, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("%s: err = %v, want %q", tt.src, err, tt.want)
		}
	}
}

func TestValidateForSend(t *testing.T) {
	const full = `"host":"smtp.test","from":"a@b.test","to":"me@kindle.com"`
	tests := []struct {
		smtp string
		want []string
	}{
		{full, nil},
		{full + `,"username":"u","password":"p"`, nil},
		{full + `,"username":"u"`, []string{"smtp.password: required when smtp.username is set (set SMTP_PASSWORD)"}},
		{`"host":"h","from":"a@b.test"`, []string{"smtp.to: required to send"}},
		{`"host":"h","from":"a@b.test","to":"kindle"`, []string{`smtp.to: must contain "@"`}},
		{``, []string{"smtp.host", "smtp.from", "smtp.to"}},
	}
	for _, tt := range tests {
		c := load(t, `{"smtp":{`+tt.smtp+`}}`) // loading never requires SMTP
		err := c.ValidateForSend()
		if tt.want == nil {
			if err != nil {
				t.Errorf("%s: %v", tt.smtp, err)
			}
			continue
		}
		for _, w := range tt.want {
			if err == nil || !strings.Contains(err.Error(), w) {
				t.Errorf("%s: err = %v, want %q", tt.smtp, err, w)
			}
		}
	}
}
