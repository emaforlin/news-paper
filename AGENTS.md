# AGENTS.mdscheduler

Guide for coding agents (and humans) working in this repo.

## What this is

`news-paper` builds a daily EPUB with ~10 articles picked against an interest profile and emails
it to a `@kindle.com` address. Flow: sources (HN, Lobsters, RSS/Atom) → ranking (LLM via
OpenRouter, heuristic fallback) → readability extraction → EPUB → SMTP.

**Deploy target: AWS Lambda (arm64, `provided.al2023`) + S3 + EventBridge Scheduler.**
The CLI (`cmd/news-paper`) is for local development.

## Commands

```sh
go mod tidy                     # first time: resolves deps and go.sum
gofmt -l . && go vet ./...      # must print nothing
go test ./...                   # offline: tests must not touch the network
make check                      # gofmt + vet + test
make cover                      # fails if any internal/ package is below 90% statement coverage
make race                       # go test -race ./...

# extract: fuzz the sanitizer, and run it over a real corpus (optionally dumping an EPUB)
go test -run='^$' -fuzz=FuzzSanitize -fuzztime=60s ./internal/extract
EXTRACT_CORPUS=<dir> EXTRACT_EPUB_OUT=$PWD/dist go test -run TestCorpus ./internal/extract
for f in dist/corpus-*.epub; do epubcheck "$f"; done

go run ./cmd/news-paper -config config.json sources                      # today's candidates (TSV)
go run ./cmd/news-paper -config config.json extract <URL>                # debug extraction for one URL
go run ./cmd/news-paper -config config.json run -dry-run -out today.epub # full edition, not sent

make bench PAGES=<dir>          # full pipeline offline against tools/fixtureserver
epubcheck dist/bench.epub       # required after touching epub/ or extract/sanitize.go
```

`PAGES` is a directory of `<name>/source.html` files; the `test-pages/` corpus from
`codeberg.org/readeck/go-readability` works.

## Architecture

```
cmd/news-paper ──> internal/pipeline ──> sources → rank → extract → epub → mail
                                      └────── uses: config, news, net/http, state, xmlx, memstat
```

| Package | Responsibility |
|---|---|
| `cmd/news-paper` | Thin CLI: flags, config loading, global timeout, signals. No business logic. |
| `internal/pipeline` | Orchestrates the stages. **No I/O of its own**, so both the CLI and the Lambda handler can call it. |
| `internal/sources` | `Source` interface. HN via Algolia (1 request), Lobsters `hottest.json`, RSS/Atom with a custom `encoding/xml` parser. |
| `internal/rank` | `Prefilter` (dedupe/seen/deny/non-HTML), `OpenRouter`, `Heuristic`, `WithFallback` (top-up + per-domain cap). |
| `internal/extract` | Fetch, paywall detection (`Rejected`), readability, `Sanitize` → allowlisted XHTML. |
| `internal/epub` | Hand-rolled EPUB 3 + NCX with `archive/zip` + `text/template`. |
| `internal/mail` | MIME with a base64 attachment + SMTP send (mandatory STARTTLS or implicit TLS). |
| `internal/state` | IDs of URLs already sent or rejected, pruned by retention. |
| `internal/config` | JSON with `//` comment lines, defaults, secrets from env vars. |
| `internal/news` | `Candidate → Pick → Article` types, plus `CleanURL`, `DedupeKey`, `URLID`. |
| `internal/xmlx` | XML escaping that drops characters invalid in XML 1.0. |
| `internal/httpx` | HTTP client with timeouts, body cap, 1 retry on 5xx/429/network errors. |
| `tools/fixtureserver` | Dev only: fake feeds/HN/Lobsters and a mock OpenRouter. |

## Invariants (do not break)

1. **EPUB XHTML must be well-formed and valid.** Send-to-Kindle rejects the whole file
   (error E999) over a single problem. So:
   - every string interpolated in `internal/epub/templates.go` goes through `x` (text) or `a` (attribute);
   - article bodies come **only** from `extract.Sanitize`, never raw HTML;
   - `mimetype` is the first entry, stored, with no data descriptor (`CreateRaw`);
   - if you touch `epub/` or `sanitize.go`, run `epubcheck` and aim for 0 errors and 0 warnings.
2. **An edition always goes out.** A failing source is logged and skipped. A failing LLM **falls**
   back to `Heuristic`. One article is enough to send. Only "0 candidates", "0 picks" and
   "0 articles" abort the run.
3. **State is saved only after a successful send.** Mark sent articles and `*extract.Rejected`
   picks (paywall, teaser, 4xx), since those are permanent. Transient errors (timeouts, 5xx) are
   **not** marked: they get retried tomorrow.
4. **URL identity:** `CleanURL` is for fetching; `DedupeKey`/`URLID` are for identity and state.
   Changing `URLID` invalidates the state and resends old articles: don't change it without a migration.
5. **Languages:** `Edition.Language` is the *content* language (Kindle dictionary, default `en`).
   Front-page labels and the email body are in Spanish. Code comments are in English.
6. **Secrets** (`OPENROUTER_API_KEY`, `SMTP_PASSWORD`) only via env vars, SSM or Secrets Manager.
   Never in versioned files. `config.json`, `state.json`, `*.epub` and `dist/` are gitignored.
7. **Tests never hit the network, send email or call OpenRouter.** Use inline strings,
   `httptest` or `tools/fixtureserver`.

## Lambda migration (plan; not implemented yet)

Implement in this order, keeping these shapes:

1. **`state.Store`**: interface `Load(ctx) (*State, error)` / `Save(ctx, *State) error`.
   `FileStore` is the current implementation; add `S3Store`. `pipeline.Run` takes the store as a
   parameter instead of reading `cfg.State.Path`.
2. **`config.LoadBytes([]byte)`**, so the config can come from S3.
3. **`cmd/lambda/main.go`**: `lambda.Start(handler)`, `import _ "time/tzdata"`, logs via
   `slog.NewJSONHandler`. Build:
   `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -o bootstrap ./cmd/lambda`
   and zip `bootstrap`.
4. **S3 layout:**
   ```
   s3://<bucket>/config.json
   s3://<bucket>/state.json
   s3://<bucket>/editions/YYYY-MM-DD.epub
   ```
   Upload the day's EPUB with `If-None-Match: *` **before** sending the email: it is the
   idempotency lock. If it already exists, don't resend. This covers Lambda's async retries.
5. **Concurrent extraction** (done): `Collect` runs up to `Concurrency` extractions in a sliding
   window that **preserves ranking order** and stops once `want` articles succeed. The pipeline only
   has to pass `extract.concurrency` through.
6. **Timeouts:** `runtime.run_timeout_min` must be lower than the Lambda timeout
   (suggested: Lambda 10 min, run 9 min).
7. **Scheduler:** EventBridge Scheduler `cron(40 5 * * ? *)` with
   `ScheduleExpressionTimezone: America/Argentina/Buenos_Aires`. Lambda outside any VPC.
   The IaC tool (SAM/CDK/Terraform) is **undecided**: ask before adding infra.

Once the migration is done, delete the router legacy (see above) in a separate commit.

## Dependencies

Prefer the standard library. Currently allowed dependencies:

- `golang.org/x/net/html` (+ `charset`)
- `codeberg.org/readeck/go-readability` (v0 API, go-shiori compatible). Moving to `/v2` is optional and goes in its own change.
- **For Lambda:** `github.com/aws/aws-lambda-go` and `aws-sdk-go-v2` (`config`, `service/s3`; `ssm` or `secretsmanager` if needed), **only** inside `cmd/lambda` and `S3Store`.

Any other dependency needs a justification for why the stdlib isn't enough.
Don't add `gofeed`, `goquery` or EPUB libraries: the hand-rolled feed parser and EPUB writer are
intentional (control over the XHTML).

## Style

- Clean `gofmt` + `go vet`. `context.Context` is always the first parameter.
- Wrap errors with `fmt.Errorf("what: %w", err)`. Permanent rejections are `*extract.Rejected`.
- Log with `log/slog` key-value pairs (`log.Info("extracted", "domain", d, "words", n)`); no `fmt.Println` outside CLI output.
- No mutable global state. Read-only package-level tables (allowlists, layouts) are fine.
- Table-driven tests when there are more than 2 cases. For XML output, use the `wellFormed` and `checkModel` helpers in `extract/model_test.go`
  (`checkOutput` in `sanitize_test.go` runs every Sanitize property at once). HTTP tests use
  `testClient` (loopback-only) from `extract/helpers_test.go`.

## Recipes

- **Add a source:** implement `sources.Source` (`Name`, `Fetch`) returning `news.Candidate`s
  with `URL` passed through `news.CleanURL`; fetch through `sources.get` (Accept header, 4 MiB cap,
  non-2xx → error) and give zero-valued fields the defaults inside the source. Add its config struct
  to `sources.Config` (JSON tags as in the config file; `internal/config` embeds or maps it), register
  it in `sources.FromConfig` (order matters: it is the output order of `FetchAll`), and add a test
  with an inline payload served by `httptest` through the loopback-only `testClient`.
- **Change the ranking:** the prompt lives in `rank/openrouter.go` (`systemPrompt`/`userPrompt`).
  `parsePicks` tolerates code fences and prose around the JSON; keep it that way.
  Validate offline with `make bench`; the mock LLM is in `tools/fixtureserver`.
- **New HTML tag in article bodies:** add it to `allowedAttrs` (and to `blockTags`/`phrasingOnly`
  if applicable) in `extract/sanitize.go`, add a case to `sanitize_test.go`, and run `epubcheck`.