package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// validate checks everything needed for any run and reports every problem at
// once. It also loads the edition timezone.
func (c *Config) validate() error {
	var errs []error
	bad := func(path, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: "+format, append([]any{path}, args...)...))
	}
	positive := func(path string, v int64) {
		if v <= 0 {
			bad(path, "must be > 0, got %d", v)
		}
	}
	httpURL := func(path, raw string) {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad(path, "must be an absolute http(s) URL, got %q", raw)
		}
	}

	if p := *c.Edition.Picks; p < 1 || p > 99 {
		bad("edition.picks", "must be in 1..99, got %d", p)
	}
	if o := *c.Edition.Overselect; o < 0 {
		bad("edition.overselect", "must be >= 0, got %d", o)
	}
	loc, err := time.LoadLocation(c.Edition.Timezone)
	if err != nil {
		bad("edition.timezone", "%v", err)
	}
	c.loc = loc

	if p := c.Rank.Provider; p != ProviderOpenRouter && p != ProviderHeuristic {
		bad("rank.provider", "must be %q or %q, got %q", ProviderOpenRouter, ProviderHeuristic, p)
	}
	httpURL("rank.base_url", c.Rank.BaseURL)
	if m := c.SMTP.TLSMode; m != "starttls" && m != "tls" {
		bad("smtp.tls_mode", `must be "starttls" or "tls", got %q`, m)
	}
	if p := c.SMTP.Port; p < 1 || p > 65535 {
		bad("smtp.port", "must be in 1..65535, got %d", p)
	}

	httpURL("sources.hackernews.api_base", c.Sources.HackerNews.APIBase)
	if m := *c.Sources.HackerNews.MinPoints; m < 0 {
		bad("sources.hackernews.min_points", "must be >= 0, got %d", m)
	}
	positive("sources.hackernews.limit", int64(c.Sources.HackerNews.Limit))
	httpURL("sources.lobsters.url", c.Sources.Lobsters.URL)
	positive("sources.lobsters.limit", int64(c.Sources.Lobsters.Limit))
	names := map[string]bool{}
	for i, f := range c.Sources.Feeds {
		path := fmt.Sprintf("sources.feeds[%d]", i)
		switch {
		case f.Name == "":
			bad(path+".name", "required")
		case names[f.Name]:
			bad(path+".name", "duplicate name %q", f.Name)
		}
		names[f.Name] = true
		httpURL(path+".url", f.URL)
		positive(path+".limit", int64(f.Limit))
	}

	positive("extract.min_words", int64(c.Extract.MinWords))
	positive("extract.max_body_bytes", c.Extract.MaxBodyBytes)
	positive("extract.concurrency", int64(c.Extract.Concurrency))
	positive("http.concurrency", int64(c.HTTP.Concurrency))
	positive("http.timeout_sec", int64(c.HTTP.TimeoutSec))
	positive("rank.timeout_sec", int64(c.Rank.TimeoutSec))
	positive("state.retention_days", int64(c.State.RetentionDays))
	positive("runtime.run_timeout_min", int64(c.Runtime.RunTimeoutMin))

	return errors.Join(errs...)
}

// ValidateForSend checks what only emailing needs.
func (c *Config) ValidateForSend() error {
	var errs []error
	for _, f := range []struct{ path, value string }{
		{"smtp.host", c.SMTP.Host}, {"smtp.from", c.SMTP.From}, {"smtp.to", c.SMTP.To},
	} {
		if f.value == "" {
			errs = append(errs, fmt.Errorf("%s: required to send", f.path))
		}
	}
	if c.SMTP.To != "" && !strings.Contains(c.SMTP.To, "@") {
		errs = append(errs, errors.New(`smtp.to: must contain "@"`))
	}
	if c.SMTP.Username != "" && c.SMTP.Password == "" {
		errs = append(errs, errors.New("smtp.password: required when smtp.username is set (set "+EnvSMTPPassword+")"))
	}
	return errors.Join(errs...)
}
