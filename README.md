# news-paper

A daily newspaper for your Kindle. `news-paper` collects links from Hacker News, Lobsters and
RSS/Atom feeds, picks about ten that match your interests, builds an EPUB from the article text
and emails it to your `@kindle.com` address.

```
sources ──> rank ──> extract ──> EPUB ──> email
```

- **Ranking** uses an LLM through [OpenRouter](https://openrouter.ai) (one request per run). Without
  one, or if it fails, a keyword heuristic takes over, so an edition still goes out.
- **Extraction** keeps the readable text only. Paywalled and too-short pages are skipped and the
  next candidate takes their place. Images are removed.
- **No repeats:** articles that were already sent are remembered for 30 days (configurable).

> **Status:** it runs locally from the command line. The planned AWS Lambda deployment is not
> implemented yet, so scheduling a daily run is up to you.
>
> The front-page labels and the email body are in Spanish.

## Requirements

- Go 1.27.4 or newer
- An SMTP account that supports STARTTLS or implicit TLS (for example, Gmail)
- A Send to Kindle address
- Optional: an OpenRouter API key
- Optional: [epubcheck](https://www.w3.org/publishing/epubcheck/), for development

## Quick start

### 1. Configure

```sh
git clone https://github.com/emaforlin/news-paper.git
cd news-paper
cp config.example.json config.json
```

Edit `config.json`: your interests (`profile`), your sources and your SMTP settings.
The file is gitignored.

### 2. Build an edition without sending it

```sh
go run ./cmd/news-paper -config config.json run -dry-run -out today.epub
```

This needs no credentials, and it writes no state. Open `today.epub` to see what you would get.
Until `rank.model` is set, the heuristic ranker is used.

### 3. Set up email delivery

1. In your Amazon account, under *Manage Your Content and Devices → Preferences → Personal
   Document Settings*, find your Kindle address and add your sender address to the approved list.
2. In `config.json`, set `smtp.username` and `smtp.from` to your sender address, and `smtp.to`
   to your Kindle address.
3. Set the secrets as environment variables. Gmail requires an
   [app password](https://myaccount.google.com/apppasswords), not your account password.

```sh
read -rs SMTP_PASSWORD && export SMTP_PASSWORD   # type the password, then Enter
export OPENROUTER_API_KEY=...                    # optional
```

To use the LLM ranker, also set `rank.model` to an OpenRouter model ID.

Check delivery with the EPUB from step 2. `send` records nothing, so you can repeat it:

```sh
go run ./cmd/news-paper -config config.json send today.epub
```

### 4. Send today's edition

```sh
go run ./cmd/news-paper -config config.json run -archive dist/editions
```

`-archive` keeps a copy of each edition and refuses to send a second one on the same day.

## Commands

```
news-paper [-config FILE] [-v] <command> [flags]
```

| Command | What it does |
|---|---|
| `run` | Fetches, ranks, extracts, builds the EPUB and emails it |
| `run -dry-run` | Builds only: no archive, no email, no state |
| `run -out FILE` | Also writes the EPUB to `FILE` |
| `run -archive DIR` | Archives the edition in `DIR` and sends at most once per day |
| `sources` | Prints today's candidates as TSV, before ranking |
| `extract URL` | Extracts one article and prints the resulting XHTML |
| `send FILE` | Emails an existing `.epub`; `-subject` overrides the subject |
| `version` | Prints the version |

`-config` (default `config.json`) and `-v` (debug logs) go **before** the command.
Exit codes: `0` success, `1` failure, `2` wrong usage.

To install a binary instead of using `go run`:

```sh
go build -o dist/news-paper ./cmd/news-paper
```

If you schedule it (with cron or similar), run it from the project directory or use absolute paths,
since relative paths such as `state.json` are resolved against the working directory.

## Configuration

`config.json` is JSON, with comments allowed on whole lines starting with `//`. Unknown keys are
errors. [config.example.json](config.example.json) is a complete starting point.

| Key | Default | Meaning |
|---|---|---|
| `edition.title` | `Diario` | Newspaper name |
| `edition.language` | `en` | Language of the articles, used by the Kindle dictionary |
| `edition.timezone` | `America/Argentina/Buenos_Aires` | Decides the edition date |
| `edition.picks` | `10` | Articles per edition (1–99) |
| `edition.overselect` | `8` | Spare picks to replace articles that can't be extracted |
| `edition.max_per_domain` | `2` | Cap per site; negative means no cap |
| `profile.description` | | Your interests, in plain words, for the LLM |
| `profile.keywords` | | Terms the heuristic ranker favors |
| `profile.avoid` | | Topics to leave out |
| `sources.max_age_hours` | `72` | Ignore feed items older than this |
| `sources.hackernews` | disabled | `enabled`, `min_points` (100), `limit` (60) |
| `sources.lobsters` | disabled | `enabled`, `limit` (25) |
| `sources.feeds` | | List of `{"name", "url", "limit"}`; names must be unique |
| `rank.provider` | `openrouter` | `openrouter` or `heuristic` |
| `rank.model` | | OpenRouter model ID; empty means heuristic |
| `extract.min_words` | `250` | Shorter pages are treated as teasers and skipped |
| `extract.deny_domains` | | Sites to never include |
| `smtp.port` / `smtp.tls_mode` | `587` / `starttls` | Use `465` / `tls` for implicit TLS |
| `state.path` | `state.json` | Record of what was already sent |
| `state.retention_days` | `30` | How long that record is kept |
| `runtime.run_timeout_min` | `9` | Time limit for a whole command |

Secrets come from `OPENROUTER_API_KEY` and `SMTP_PASSWORD`. Keep them out of files.

## Troubleshooting

**The email failed after the edition was archived.** `-archive` stores the edition before sending,
so another `run` will not send it. Resend the stored file:

```sh
go run ./cmd/news-paper -config config.json send dist/editions/YYYY-MM-DD.epub
```

`send` does not update the state, so those articles can be picked again on a later day. The
alternative is to delete the archived file and `run` again, which builds a new edition.

**Gmail answers `535` or "Username and Password not accepted".** Use an app password.

**The email was sent but nothing reached the Kindle.** Check that the sender is on Amazon's
approved list, and look for a message from Amazon in your inbox.

**`ranking produced no picks`.** There are no unseen candidates left. This is most likely on a
second run the same day.

**A feed contributes nothing.** Its items may be older than `sources.max_age_hours`. The
`sources` command shows what survives.

**An article is missing.** `extract <URL>` shows why a page is rejected. Paywalls, teasers and
HTTP 4xx errors are permanent; timeouts and server errors are retried on the next run.

## Development

```sh
make check              # gofmt, vet, tests
make cover              # fails if a package under internal/ is below 90% coverage
make race               # tests with the race detector
make bench PAGES=<dir>  # full pipeline offline, writes dist/bench.epub
```

Tests never touch the network, send email or call OpenRouter. `PAGES` is a directory of
`<name>/source.html` files, such as the `test-pages/` directory of
[go-readability](https://codeberg.org/readeck/go-readability).

Run `epubcheck` after changing the EPUB writer or the HTML sanitizer. Send to Kindle is stricter
than most readers: a single invalid file rejects the whole edition, and nested tables make it fall
back to a fixed layout.

[AGENTS.md](AGENTS.md) describes the architecture, the invariants to preserve and the Lambda plan.
