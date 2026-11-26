// Command news-paper is the local developer CLI: run an edition, inspect the
// candidates, debug one extraction and resend an archived edition.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // edition.timezone must load on any machine

	"github.com/emaforlin/news-paper/internal/config"
	"github.com/emaforlin/news-paper/internal/mail"
)

// version is set with -ldflags "-X main.version=…".
var version = "dev"

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

const usage = `usage: news-paper [-config FILE] [-v] <command> [flags]

Commands:
  run            fetch, rank, extract, build the EPUB and email it
      -dry-run       build only: no archive, no email, no state
      -out FILE      also write the EPUB to FILE
      -archive DIR   use a file archive (idempotency lock) in DIR; without it,
                     running twice a day sends twice
  sources        fetch all sources and print the prefiltered candidates as TSV
  extract URL    extract one URL; print the sanitized XHTML and stats
  send FILE      email an existing .epub (recovery path)
      -subject S     default: derived from the file name
  version        print the version

Global flags:
  -config FILE   config path (default config.json)
  -v             debug logs

Secrets come from OPENROUTER_API_KEY and SMTP_PASSWORD.
`

// env is what the CLI takes from its process, so tests can replace it.
type env struct {
	stdout, stderr io.Writer
	newMailer      func(mail.SMTPConfig) mail.Sender // nil → mail.NewSMTP
}

func (e env) mailer(c mail.SMTPConfig) mail.Sender {
	if e.newMailer != nil {
		return e.newMailer(c)
	}
	return mail.NewSMTP(c)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	context.AfterFunc(ctx, stop) // a second signal kills the process
	code := realMain(ctx, os.Args[1:], env{stdout: os.Stdout, stderr: os.Stderr})
	stop()
	os.Exit(code)
}

// realMain parses the arguments before loading anything, so usage errors never
// depend on a config file.
func realMain(ctx context.Context, args []string, e env) int {
	global := newFlagSet("news-paper", e)
	configPath := global.String("config", "config.json", "config path")
	verbose := global.Bool("v", false, "debug logs")
	if err := global.Parse(args); err != nil {
		return parseExit(err)
	}
	if global.NArg() == 0 {
		return usageError(e, "missing command")
	}
	name, rest := global.Arg(0), global.Args()[1:]
	if name == "version" {
		if len(rest) != 0 {
			return usageError(e, "version takes no arguments")
		}
		fmt.Fprintln(e.stdout, version)
		return exitOK
	}
	cmd, code := parseCommand(name, rest, e)
	if cmd == nil {
		return code
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(e.stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load(*configPath)
	if err == nil {
		rctx, cancel := context.WithTimeout(ctx, cfg.RunTimeout())
		defer cancel()
		err = cmd(rctx, cfg, log)
	}
	switch {
	case errors.Is(err, errReported):
		return exitFailure
	case err != nil:
		log.Error(name+" failed", "err", err)
		return exitFailure
	}
	return exitOK
}

// newFlagSet reports parse errors, followed by the usage, on stderr.
func newFlagSet(name string, e env) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() { fmt.Fprint(e.stderr, usage) }
	return fs
}

func parseExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	return exitUsage
}

func usageError(e env, msg string) int {
	fmt.Fprintf(e.stderr, "news-paper: %s\n%s", msg, usage)
	return exitUsage
}
