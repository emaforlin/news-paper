package pipeline

import (
	"reflect"
	"testing"

	"github.com/emaforlin/news-paper/internal/config"
	"github.com/emaforlin/news-paper/internal/extract"
	"github.com/emaforlin/news-paper/internal/mail"
	"github.com/emaforlin/news-paper/internal/rank"
	"github.com/emaforlin/news-paper/internal/sources"
)

// config copies these literals because it imports no stage package.
func TestConfigDefaultsMatchPackages(t *testing.T) {
	noSecrets(t)
	c, err := config.LoadBytes([]byte(`{"sources":{"feeds":[{"name":"a","url":"http://a.test"}]}}`), "x")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		name      string
		got, want any
	}{
		{"extract.min_words", c.Extract.MinWords, extract.DefaultMinWords},
		{"extract.max_body_bytes", c.Extract.MaxBodyBytes, int64(extract.DefaultMaxBody)},
		{"extract.concurrency", c.Extract.Concurrency, extract.DefaultConcurrency},
		{"hackernews.api_base", c.Sources.HackerNews.APIBase, sources.DefaultHNAPIBase},
		{"hackernews.min_points", *c.Sources.HackerNews.MinPoints, sources.DefaultHNMinPoints},
		{"hackernews.limit", c.Sources.HackerNews.Limit, sources.DefaultHNLimit},
		{"lobsters.url", c.Sources.Lobsters.URL, sources.DefaultLobstersURL},
		{"lobsters.limit", c.Sources.Lobsters.Limit, sources.DefaultLobstersLimit},
		{"feeds.limit", c.Sources.Feeds[0].Limit, sources.DefaultFeedLimit},
		{"rank.base_url", c.Rank.BaseURL, rank.DefaultBaseURL},
		{"rank.timeout_sec", c.RankTimeout(), rank.DefaultTimeout},
	} {
		if v.got != v.want {
			t.Errorf("%s = %v, package default %v", v.name, v.got, v.want)
		}
	}
}

func TestMappings(t *testing.T) {
	cfg := testConfig(t)
	cfg.SMTP = config.SMTP{Host: "h", Port: 465, TLSMode: "tls", Username: "u", Password: "p"}
	want := mail.SMTPConfig{Host: "h", Port: 465, TLSMode: mail.ModeTLS, Username: "u", Password: "p"}
	if got := SMTPConfig(cfg); got != want {
		t.Errorf("smtp = %+v", got)
	}

	zero, many := 0, 150
	cfg.Sources = config.Sources{
		MaxAgeHours: 48,
		HackerNews:  config.HackerNews{Enabled: true, APIBase: "http://hn.test", MinPoints: &zero, Limit: 7},
		Lobsters:    config.Lobsters{Enabled: true, URL: "http://l.test", Limit: 3},
		Feeds:       []config.Feed{{Name: "b", URL: "http://b.test", Limit: 2}, {Name: "a", URL: "http://a.test", Limit: 1}},
	}
	got := sourcesConfig(cfg)
	wantS := sources.Config{
		MaxAgeHours: 48,
		HackerNews:  sources.HNConfig{Enabled: true, APIBase: "http://hn.test", MinPoints: sources.NoMinPoints, Limit: 7},
		Lobsters:    sources.LobstersConfig{Enabled: true, URL: "http://l.test", Limit: 3},
		Feeds:       []sources.FeedConfig{{Name: "b", URL: "http://b.test", Limit: 2}, {Name: "a", URL: "http://a.test", Limit: 1}},
	}
	if !reflect.DeepEqual(got, wantS) {
		t.Errorf("sources = %+v", got)
	}
	cfg.Sources.HackerNews.MinPoints = &many
	if got := sourcesConfig(cfg).HackerNews.MinPoints; got != 150 {
		t.Errorf("min_points = %d", got)
	}
}
