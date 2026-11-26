// Package config loads the JSON configuration file, applies defaults and
// secret overrides from the environment, and validates the result. The file
// may contain whole-line "//" comments; unknown fields are errors.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	ProviderOpenRouter = "openrouter"
	ProviderHeuristic  = "heuristic"

	EnvOpenRouterKey = "OPENROUTER_API_KEY"
	EnvSMTPPassword  = "SMTP_PASSWORD"
)

// Config is the whole configuration file. Load and LoadBytes return it with
// every default applied, so the pointer fields are never nil afterwards.
type Config struct {
	Edition Edition `json:"edition"`
	Profile Profile `json:"profile"`
	Sources Sources `json:"sources"`
	Rank    Rank    `json:"rank"`
	Extract Extract `json:"extract"`
	SMTP    SMTP    `json:"smtp"`
	State   State   `json:"state"`
	HTTP    HTTP    `json:"http"`
	Runtime Runtime `json:"runtime"`

	loc *time.Location // set by validate
}

type Edition struct {
	Title        string `json:"title"`
	Language     string `json:"language"` // content language (Kindle dictionary)
	Timezone     string `json:"timezone"`
	Picks        *int   `json:"picks"`          // missing → 10; 0 is invalid
	Overselect   *int   `json:"overselect"`     // missing → 8; 0 is allowed
	MaxPerDomain int    `json:"max_per_domain"` // negative → no cap
}

type Profile struct {
	Description string   `json:"description"`
	Keywords    []string `json:"keywords"`
	Avoid       []string `json:"avoid"`
}

// Sources mirrors sources.Config, so that this package only needs the
// standard library.
type Sources struct {
	MaxAgeHours int        `json:"max_age_hours"` // negative → no cutoff
	HackerNews  HackerNews `json:"hackernews"`
	Lobsters    Lobsters   `json:"lobsters"`
	Feeds       []Feed     `json:"feeds"`
}

type HackerNews struct {
	Enabled   bool   `json:"enabled"`
	APIBase   string `json:"api_base"`
	MinPoints *int   `json:"min_points"` // missing → 100; 0 is allowed
	Limit     int    `json:"limit"`
}

type Lobsters struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
	Limit   int    `json:"limit"`
}

type Feed struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Limit int    `json:"limit"`
}

type Rank struct {
	Provider   string `json:"provider"` // ProviderOpenRouter | ProviderHeuristic
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	APIKey     Secret `json:"api_key"` // EnvOpenRouterKey overrides it
	TimeoutSec int    `json:"timeout_sec"`
}

type Extract struct {
	MinWords     int      `json:"min_words"`
	MaxBodyBytes int64    `json:"max_body_bytes"`
	DenyDomains  []string `json:"deny_domains"`
	Concurrency  int      `json:"concurrency"`
}

type SMTP struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	TLSMode  string `json:"tls_mode"` // "starttls" | "tls"
	Username string `json:"username"`
	Password Secret `json:"password"` // EnvSMTPPassword overrides it
	From     string `json:"from"`
	To       string `json:"to"`
}

type State struct {
	Path          string `json:"path"` // local CLI only
	RetentionDays int    `json:"retention_days"`
}

type HTTP struct {
	UserAgent   string `json:"user_agent"`
	TimeoutSec  int    `json:"timeout_sec"`
	Concurrency int    `json:"concurrency"`
}

type Runtime struct {
	RunTimeoutMin int `json:"run_timeout_min"`
}

// Load reads and loads the file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return LoadBytes(data, path)
}

// LoadBytes loads a configuration: comments, strict decode, defaults,
// environment overrides and validation. source labels errors.
func LoadBytes(data []byte, source string) (*Config, error) {
	return loadBytes(data, source, os.Getenv)
}

func loadBytes(data []byte, source string, getenv func(string) string) (*Config, error) {
	c, err := decode(data, source)
	if err != nil {
		return nil, err
	}
	c.applyDefaults()
	if v := getenv(EnvOpenRouterKey); v != "" {
		c.Rank.APIKey = Secret(v)
	}
	if v := getenv(EnvSMTPPassword); v != "" {
		c.SMTP.Password = Secret(v)
	}
	c.normalize()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: invalid config: %w", source, err)
	}
	return c, nil
}

func (c *Config) normalize() {
	c.Rank.BaseURL = strings.TrimRight(c.Rank.BaseURL, "/")
	for i := range c.Sources.Feeds {
		c.Sources.Feeds[i].Name = strings.TrimSpace(c.Sources.Feeds[i].Name)
	}
}

// Location is the edition timezone, or UTC for a Config that wasn't loaded.
func (c *Config) Location() *time.Location {
	if c.loc == nil {
		return time.UTC
	}
	return c.loc
}

func (c *Config) HTTPTimeout() time.Duration { return time.Duration(c.HTTP.TimeoutSec) * time.Second }

func (c *Config) RankTimeout() time.Duration { return time.Duration(c.Rank.TimeoutSec) * time.Second }

func (c *Config) RunTimeout() time.Duration {
	return time.Duration(c.Runtime.RunTimeoutMin) * time.Minute
}
