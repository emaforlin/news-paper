package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/emaforlin/news-paper/internal/archive"
	"github.com/emaforlin/news-paper/internal/config"
	"github.com/emaforlin/news-paper/internal/extract"
	"github.com/emaforlin/news-paper/internal/httpx"
	"github.com/emaforlin/news-paper/internal/news"
	"github.com/emaforlin/news-paper/internal/pipeline"
	"github.com/emaforlin/news-paper/internal/state"
)

// command is a parsed subcommand, ready to run.
type command func(ctx context.Context, cfg *config.Config, log *slog.Logger) error

// errReported marks a failure that was already explained on stderr.
var errReported = errors.New("already reported")

// editionFile matches "<slug>-YYYY-MM-DD.epub" and the archive's
// "YYYY-MM-DD.epub".
var editionFile = regexp.MustCompile(`^(?:.+-)?(\d{4}-\d{2}-\d{2})\.epub$`)

// parseCommand returns the command, or nil and the exit code.
func parseCommand(name string, args []string, e env) (command, int) {
	fs := newFlagSet(name, e)
	var (
		dryRun  bool
		out     string
		dir     string
		subject string
		nargs   int
	)
	switch name {
	case "run":
		fs.BoolVar(&dryRun, "dry-run", false, "build only")
		fs.StringVar(&out, "out", "", "also write the EPUB to this file")
		fs.StringVar(&dir, "archive", "", "file archive directory")
	case "sources":
	case "extract":
		nargs = 1
	case "send":
		fs.StringVar(&subject, "subject", "", "email subject")
		nargs = 1
	default:
		return nil, usageError(e, fmt.Sprintf("unknown command %q", name))
	}
	if err := fs.Parse(args); err != nil {
		return nil, parseExit(err)
	}
	if fs.NArg() != nargs {
		return nil, usageError(e, fmt.Sprintf("%s takes %d argument(s), got %d", name, nargs, fs.NArg()))
	}
	arg := fs.Arg(0)

	switch name {
	case "run":
		return func(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
			return cmdRun(ctx, cfg, log, e, dryRun, out, dir)
		}, exitOK
	case "sources":
		return func(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
			return cmdSources(ctx, cfg, log, e)
		}, exitOK
	case "extract":
		if u, err := url.Parse(arg); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, usageError(e, fmt.Sprintf("extract needs an absolute http(s) URL, got %q", arg))
		}
		return func(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
			return cmdExtract(ctx, cfg, log, e, arg)
		}, exitOK
	default: // send
		return func(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
			return cmdSend(ctx, cfg, log, e, arg, subject)
		}, exitOK
	}
}

func httpClient(cfg *config.Config) *httpx.Client {
	return httpx.New(cfg.HTTPTimeout(), cfg.HTTP.UserAgent)
}

func cmdRun(ctx context.Context, cfg *config.Config, log *slog.Logger, e env, dryRun bool, out, dir string) error {
	editions := archive.NewNoop()
	if dir != "" {
		editions = archive.NewFile(dir)
	}
	res, err := pipeline.Run(ctx, cfg, pipeline.Deps{
		HTTP:    httpClient(cfg),
		LLMHTTP: httpx.New(cfg.RankTimeout(), cfg.HTTP.UserAgent),
		State:   state.NewFileStore(cfg.State.Path),
		Archive: editions,
		Mailer:  e.mailer(pipeline.SMTPConfig(cfg)),
		Log:     log,
	}, pipeline.Options{DryRun: dryRun})

	// Written even when sending failed: the EPUB is worth keeping.
	if out != "" && len(res.EPUB) > 0 {
		if werr := os.WriteFile(out, res.EPUB, 0o644); werr != nil {
			return errors.Join(err, fmt.Errorf("write -out: %w", werr))
		}
		log.Info("wrote epub", "path", out)
	}
	if err != nil {
		return err
	}
	if dryRun && out == "" {
		log.Warn("the EPUB was built and discarded")
	}
	if res.Skipped {
		log.Info("skipped: edition already archived", "date", res.Date)
	}
	return nil
}

func cmdSources(ctx context.Context, cfg *config.Config, log *slog.Logger, e env) error {
	_, fetched, pool, err := pipeline.Pool(ctx, cfg, pipeline.Deps{
		HTTP:  httpClient(cfg),
		State: state.NewFileStore(cfg.State.Path),
		Log:   log,
	}, time.Now())
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("id\tsource\tscore\tdomain\ttitle\n")
	for _, c := range pool {
		fmt.Fprintf(&buf, "%s\t%s\t%d\t%s\t%s\n", c.ID(), field(c.Source), c.Score, field(c.Domain()), field(c.Title))
	}
	if _, err := e.stdout.Write(buf.Bytes()); err != nil {
		return err
	}
	log.Info("done", "fetched", fetched, "after_prefilter", len(pool))
	return nil
}

// field keeps a value inside its TSV column.
func field(s string) string { return strings.Join(strings.Fields(s), " ") }

func cmdExtract(ctx context.Context, cfg *config.Config, log *slog.Logger, e env, raw string) error {
	ex := &extract.Extractor{
		HC:       httpClient(cfg),
		MinWords: cfg.Extract.MinWords,
		MaxBody:  cfg.Extract.MaxBodyBytes,
		Log:      log,
	}
	art, err := ex.Extract(ctx, news.Pick{Candidate: news.Candidate{URL: news.CleanURL(raw), Title: raw}})
	var rejected *extract.Rejected
	if errors.As(err, &rejected) {
		fmt.Fprintln(e.stderr, rejected)
		return errReported
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.stdout, "<!-- title=%q words=%d lang=%q -->\n%s\n", art.Title, art.Words, art.Language, art.XHTML)
	return err
}

// cmdSend emails an existing file. It touches neither state nor the archive.
func cmdSend(ctx context.Context, cfg *config.Config, log *slog.Logger, e env, path, subject string) error {
	if err := cfg.ValidateForSend(); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	base := filepath.Base(path)
	if subject == "" {
		subject = subjectFor(cfg.Edition.Title, base)
	}
	msg, err := pipeline.BuildMessage(cfg, pipeline.Delivery{
		Subject: subject, Body: base + "\n", Filename: base, EPUB: data, Date: time.Now(),
	})
	if err != nil {
		return err
	}
	if err := e.mailer(pipeline.SMTPConfig(cfg)).Send(ctx, cfg.SMTP.From, cfg.SMTP.To, msg); err != nil {
		return err
	}
	log.Info("sent", "file", path, "subject", subject)
	return nil
}

// subjectFor derives the subject of a resent file from its name.
func subjectFor(title, base string) string {
	if m := editionFile.FindStringSubmatch(base); m != nil {
		return title + " · " + m[1]
	}
	return strings.TrimSuffix(base, ".epub")
}
