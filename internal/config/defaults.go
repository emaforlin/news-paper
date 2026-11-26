package config

// applyDefaults fills every zero or empty value. The literals mirror the
// constants of the stage packages, which this package doesn't import; a test
// in internal/pipeline keeps them equal.
func (c *Config) applyDefaults() {
	def(&c.Edition.Title, "Diario")
	def(&c.Edition.Language, "en")
	def(&c.Edition.Timezone, "America/Argentina/Buenos_Aires")
	defPtr(&c.Edition.Picks, 10)
	defPtr(&c.Edition.Overselect, 8)
	def(&c.Edition.MaxPerDomain, 2)

	def(&c.Sources.MaxAgeHours, 72)
	hn := &c.Sources.HackerNews
	def(&hn.APIBase, "https://hn.algolia.com/api/v1")
	defPtr(&hn.MinPoints, 100)
	def(&hn.Limit, 60)
	def(&c.Sources.Lobsters.URL, "https://lobste.rs/hottest.json")
	def(&c.Sources.Lobsters.Limit, 25)
	for i := range c.Sources.Feeds {
		def(&c.Sources.Feeds[i].Limit, 20)
	}

	def(&c.Rank.Provider, ProviderOpenRouter)
	def(&c.Rank.BaseURL, "https://openrouter.ai/api/v1")
	def(&c.Rank.TimeoutSec, 90)

	def(&c.Extract.MinWords, 250)
	def(&c.Extract.MaxBodyBytes, 2<<20)
	def(&c.Extract.Concurrency, 4)

	def(&c.SMTP.Port, 587)
	def(&c.SMTP.TLSMode, "starttls")

	def(&c.State.Path, "state.json")
	def(&c.State.RetentionDays, 30)

	def(&c.HTTP.UserAgent, "diario/0.1 (+personal Kindle digest)")
	def(&c.HTTP.TimeoutSec, 25)
	def(&c.HTTP.Concurrency, 4)

	def(&c.Runtime.RunTimeoutMin, 9)
}

// def sets *v to d when *v is the zero value.
func def[T comparable](v *T, d T) {
	var zero T
	if *v == zero {
		*v = d
	}
}

// defPtr sets *v only when the key was missing or null.
func defPtr(v **int, d int) {
	if *v == nil {
		*v = &d
	}
}
