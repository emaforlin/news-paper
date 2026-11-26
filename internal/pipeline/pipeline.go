// Package pipeline runs one edition end to end. It is the only place that
// knows the order of the stages, and it does no I/O of its own: everything
// external comes in through Deps.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/emaforlin/news-paper/internal/archive"
	"github.com/emaforlin/news-paper/internal/config"
	"github.com/emaforlin/news-paper/internal/epub"
	"github.com/emaforlin/news-paper/internal/extract"
	"github.com/emaforlin/news-paper/internal/httpx"
	"github.com/emaforlin/news-paper/internal/mail"
	"github.com/emaforlin/news-paper/internal/news"
	"github.com/emaforlin/news-paper/internal/rank"
	"github.com/emaforlin/news-paper/internal/sources"
	"github.com/emaforlin/news-paper/internal/state"
)

const (
	// sendReserve is kept off the extraction deadline so there is always time
	// to email what was gathered and save the state.
	sendReserve = 90 * time.Second
	// saveTimeout bounds the state write after a successful send.
	saveTimeout = 30 * time.Second
)

// Deps is everything Run needs from the outside world.
type Deps struct {
	HTTP    *httpx.Client // sources + extraction; nil → built from config
	LLMHTTP *httpx.Client // OpenRouter; nil → built from config
	State   state.Store
	Archive archive.Archive // not needed for a dry run
	Mailer  mail.Sender     // not needed for a dry run
	Log     *slog.Logger    // nil → discarded

	// Optional overrides, for tests. nil = build from config.
	Sources []sources.Source
	Ranker  rank.Ranker
}

// Options tune one run.
type Options struct {
	DryRun bool      // build the EPUB only: no archive, no email, no state write
	Now    time.Time // zero = time.Now()
}

// Result describes what a run did, filled as far as it got.
type Result struct {
	Date       string // YYYY-MM-DD, local to the edition timezone
	Edition    epub.Edition
	EPUB       []byte
	Candidates int // fetched
	Pool       int // after prefilter
	Picks      int
	Rejected   int
	Sent       bool
	Skipped    bool // the edition already existed in the archive
}

var (
	ErrNoCandidates = errors.New("no candidates from any source")
	ErrNoPicks      = errors.New("ranking produced no picks")
	ErrNoArticles   = errors.New("no article could be extracted")
)

// Run builds today's edition and, unless it is a dry run, archives and emails
// it. cfg must come from config.Load or config.LoadBytes. The Result is never
// nil.
func Run(ctx context.Context, cfg *config.Config, d Deps, opt Options) (*Result, error) {
	start := time.Now()
	now := opt.Now
	if now.IsZero() {
		now = start
	}
	now = now.In(cfg.Location())
	res := &Result{Date: now.Format(time.DateOnly)}

	d = d.withDefaults(cfg)
	if err := d.check(opt.DryRun); err != nil {
		return res, err
	}
	if !opt.DryRun {
		// Before spending any network or LLM money.
		if err := cfg.ValidateForSend(); err != nil {
			return res, fmt.Errorf("config: %w", err)
		}
	}

	st, fetched, pool, err := Pool(ctx, cfg, d, now)
	if err != nil {
		return res, err
	}
	res.Candidates, res.Pool = fetched, len(pool)
	if fetched == 0 {
		return res, ErrNoCandidates
	}

	picksWanted := *cfg.Edition.Picks
	heuristic := &rank.Heuristic{Keywords: cfg.Profile.Keywords, Avoid: cfg.Profile.Avoid, Now: now}
	picks := rank.WithFallback(ctx, d.Log, primaryRanker(cfg, d, heuristic), heuristic,
		pool, picksWanted+*cfg.Edition.Overselect, cfg.Edition.MaxPerDomain)
	res.Picks = len(picks)
	if len(picks) == 0 {
		return res, ErrNoPicks
	}

	got := collect(ctx, cfg, d, picks, picksWanted)
	res.Rejected = len(got.Rejected)
	// A cancelled run (signal, run timeout) stops here: what was gathered so
	// far is not today's edition.
	if err := ctx.Err(); err != nil {
		return res, fmt.Errorf("extract: %w", err)
	}
	if len(got.Articles) == 0 {
		return res, ErrNoArticles
	}

	if !epub.ValidLanguage(cfg.Edition.Language) {
		d.Log.Warn("edition.language invalid, using "+epub.DefaultLanguage, "language", cfg.Edition.Language)
	}
	res.Edition = epub.Edition{Title: cfg.Edition.Title, Date: now, Language: cfg.Edition.Language, Articles: got.Articles}
	data, err := epub.Bytes(res.Edition)
	if err != nil {
		return res, fmt.Errorf("epub: %w", err)
	}
	res.EPUB = data

	done := func() {
		d.Log.Info("run done", "date", res.Date, "candidates", res.Candidates, "pool", res.Pool,
			"picks", res.Picks, "articles", len(got.Articles), "rejected", res.Rejected, "bytes", len(res.EPUB),
			"sent", res.Sent, "skipped", res.Skipped, "dry_run", opt.DryRun, "took", time.Since(start).Round(time.Millisecond))
	}
	if opt.DryRun {
		done()
		return res, nil
	}

	// Build before taking the lock: a message that can't be built must not
	// leave an edition archived but never sent.
	msg, err := BuildMessage(cfg, Delivery{
		Subject:  res.Edition.FullTitle(),
		Body:     body(res.Edition),
		Filename: res.Edition.Filename(),
		EPUB:     res.EPUB,
		Date:     now,
	})
	if err != nil {
		return res, fmt.Errorf("build message: %w", err)
	}
	created, err := d.Archive.PutIfAbsent(ctx, res.Date, res.EPUB)
	if err != nil {
		return res, err
	}
	if !created {
		d.Log.Warn("edition already archived, not sending", "date", res.Date)
		res.Skipped = true
		done()
		return res, nil
	}
	if err := d.Mailer.Send(ctx, cfg.SMTP.From, cfg.SMTP.To, msg); err != nil {
		d.Log.Error("edition archived but not delivered", "date", res.Date, "hint",
			"delete the archived edition for "+res.Date+" and re-run, or resend it with: news-paper send <file>")
		return res, fmt.Errorf("send: %w", err)
	}
	res.Sent = true

	for _, a := range got.Articles {
		st.Add(now, a.ID())
	}
	for _, p := range got.Rejected {
		st.Add(now, p.ID())
	}
	st.Prune(now, cfg.State.RetentionDays)
	// The email is out: record it even if ctx was cancelled meanwhile, or the
	// same articles would be sent again.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
	defer cancel()
	if err := d.State.Save(sctx, st); err != nil {
		return res, fmt.Errorf("state save: %w", err)
	}
	done()
	return res, nil
}

// Pool loads the state, fetches every source and prefilters the candidates.
// Run uses it, and so can a caller that only wants to inspect them. It needs
// d.State.
func Pool(ctx context.Context, cfg *config.Config, d Deps, now time.Time) (st *state.State, fetched int, pool []news.Candidate, err error) {
	d = d.withDefaults(cfg)
	if d.State == nil {
		return nil, 0, nil, missing("State")
	}
	st, err = d.State.Load(ctx)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("state load: %w", err)
	}
	srcs := d.Sources
	if srcs == nil {
		srcs = sources.FromConfig(sourcesConfig(cfg), now)
	}
	cands := sources.FetchAll(ctx, d.HTTP, srcs, cfg.HTTP.Concurrency, d.Log)
	return st, len(cands), rank.Prefilter(cands, st, cfg.Extract.DenyDomains), nil
}

func (d Deps) withDefaults(cfg *config.Config) Deps {
	if d.HTTP == nil {
		d.HTTP = httpx.New(cfg.HTTPTimeout(), cfg.HTTP.UserAgent)
	}
	if d.LLMHTTP == nil {
		d.LLMHTTP = httpx.New(cfg.RankTimeout(), cfg.HTTP.UserAgent)
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return d
}

func (d Deps) check(dryRun bool) error {
	switch {
	case d.State == nil:
		return missing("State")
	case !dryRun && d.Archive == nil:
		return missing("Archive")
	case !dryRun && d.Mailer == nil:
		return missing("Mailer")
	}
	return nil
}

func missing(name string) error { return fmt.Errorf("pipeline: missing dependency: %s", name) }

// primaryRanker picks the ranker the config asks for. An unconfigured model
// goes straight to the heuristic.
func primaryRanker(cfg *config.Config, d Deps, heuristic rank.Ranker) rank.Ranker {
	switch {
	case d.Ranker != nil:
		return d.Ranker
	case cfg.Rank.Provider == config.ProviderHeuristic:
		return heuristic
	case cfg.Rank.Model == "" || strings.HasPrefix(cfg.Rank.Model, "CHANGE-ME"):
		d.Log.Warn("rank.model not configured, using heuristic")
		return heuristic
	}
	return &rank.OpenRouter{
		HC:      d.LLMHTTP,
		BaseURL: cfg.Rank.BaseURL,
		APIKey:  string(cfg.Rank.APIKey),
		Model:   cfg.Rank.Model,
		Profile: cfg.Profile.Description,
		Avoid:   cfg.Profile.Avoid,
		Log:     d.Log,
	}
}

// collect extracts the picks, leaving sendReserve of the deadline unused.
func collect(ctx context.Context, cfg *config.Config, d Deps, picks []news.Pick, want int) extract.Result {
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-sendReserve))
		defer cancel()
	}
	e := &extract.Extractor{
		HC:          d.HTTP,
		MinWords:    cfg.Extract.MinWords,
		MaxBody:     cfg.Extract.MaxBodyBytes,
		Concurrency: cfg.Extract.Concurrency,
		Log:         d.Log,
	}
	return e.Collect(ctx, picks, want)
}

// sourcesConfig maps the config file's sources to the sources package's.
func sourcesConfig(cfg *config.Config) sources.Config {
	s := cfg.Sources
	minPoints := 0
	if p := s.HackerNews.MinPoints; p != nil {
		minPoints = *p
		if minPoints == 0 { // an explicit 0 in the file means no minimum
			minPoints = sources.NoMinPoints
		}
	}
	out := sources.Config{
		MaxAgeHours: s.MaxAgeHours,
		HackerNews: sources.HNConfig{Enabled: s.HackerNews.Enabled, APIBase: s.HackerNews.APIBase,
			MinPoints: minPoints, Limit: s.HackerNews.Limit},
		Lobsters: sources.LobstersConfig{Enabled: s.Lobsters.Enabled, URL: s.Lobsters.URL, Limit: s.Lobsters.Limit},
	}
	for _, f := range s.Feeds {
		out.Feeds = append(out.Feeds, sources.FeedConfig{Name: f.Name, URL: f.URL, Limit: f.Limit})
	}
	return out
}
