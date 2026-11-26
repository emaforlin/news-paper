package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	netmail "net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emaforlin/news-paper/internal/config"
	"github.com/emaforlin/news-paper/internal/mail"
	"github.com/emaforlin/news-paper/internal/news"
)

type fakeMailer struct {
	err  error
	sent [][]byte
}

func (f *fakeMailer) Send(_ context.Context, _, _ string, msg []byte) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, msg)
	return nil
}

// subjects decodes the Subject of every message sent.
func (f *fakeMailer) subjects(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, raw := range f.sent {
		m, err := netmail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		s, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

type cli struct {
	t              *testing.T
	dir, config    string
	site           string
	mailer         *fakeMailer
	stdout, stderr bytes.Buffer
}

// newCLI writes a config whose only source is a loopback Hacker News with two
// good articles. Everything the CLI contacts is on 127.0.0.1.
func newCLI(t *testing.T) *cli {
	t.Setenv(config.EnvOpenRouterKey, "")
	t.Setenv(config.EnvSMTPPassword, "")
	var site string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/hn/search":
			var hits []map[string]any
			for i, name := range []string{"ok1", "ok2"} {
				hits = append(hits, map[string]any{"objectID": name, "title": "Story\t" + name,
					"url": site + "/" + name, "points": 200 - i, "created_at_i": time.Now().Unix()})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"hits": hits})
		case strings.HasPrefix(r.URL.Path, "/ok"), r.URL.Path == "/short":
			words := 400
			if r.URL.Path == "/short" {
				words = 30
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<html lang="en"><head><title>Page %s</title></head><body><article>`, r.URL.Path)
			for ; words > 0; words -= 50 {
				_, _ = io.WriteString(w, "<p>"+strings.Repeat("database, ", min(words, 50))+"</p>")
			}
			_, _ = io.WriteString(w, "</article></body></html>")
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	site = srv.URL

	c := &cli{t: t, dir: t.TempDir(), site: srv.URL, mailer: &fakeMailer{}}
	c.config = c.write("config.json", fmt.Sprintf(`{
		"edition": {"max_per_domain": -1},
		"sources": {"max_age_hours": -1, "hackernews": {"enabled": true, "api_base": "%s/hn"}},
		"rank": {"provider": "heuristic"},
		"smtp": {"host": "smtp.test", "from": "me@example.com", "to": "me@kindle.com"},
		"state": {"path": %q}
	}`, srv.URL, c.path("state.json")))
	return c
}

func (c *cli) path(name string) string { return filepath.Join(c.dir, name) }

func (c *cli) write(name, content string) string {
	c.t.Helper()
	if err := os.WriteFile(c.path(name), []byte(content), 0o600); err != nil {
		c.t.Fatal(err)
	}
	return c.path(name)
}

func (c *cli) exec(args ...string) int {
	c.stdout.Reset()
	c.stderr.Reset()
	return realMain(context.Background(), args, env{
		stdout: &c.stdout, stderr: &c.stderr,
		newMailer: func(mail.SMTPConfig) mail.Sender { return c.mailer },
	})
}

// run is exec with the test config.
func (c *cli) run(args ...string) int {
	return c.exec(append([]string{"-config", c.config}, args...)...)
}

func (c *cli) exists(name string) bool {
	_, err := os.Stat(c.path(name))
	return err == nil
}

func (c *cli) wantStderr(parts ...string) {
	c.t.Helper()
	for _, p := range parts {
		if !strings.Contains(c.stderr.String(), p) {
			c.t.Errorf("stderr lacks %q:\n%s", p, c.stderr.String())
		}
	}
}

func TestUsage(t *testing.T) {
	c := newCLI(t)
	for _, args := range [][]string{
		{}, {"-nope"}, {"nope"}, {"run", "-nope"}, {"run", "extra"}, {"sources", "x"},
		{"extract"}, {"extract", "ftp://x/y"}, {"extract", "/relative"}, {"extract", "http://[::1"},
		{"send"}, {"version", "x"},
	} {
		// The config file doesn't exist: usage errors must not need it.
		if code := c.exec(append([]string{"-config", c.path("missing.json")}, args...)...); code != exitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
		if !strings.Contains(c.stderr.String(), "usage: news-paper") || c.stdout.Len() != 0 {
			t.Errorf("%v: stdout %q stderr %q", args, c.stdout.String(), c.stderr.String())
		}
	}
	for _, args := range [][]string{{"-h"}, {"run", "-h"}} {
		if code := c.exec(args...); code != exitOK {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

func TestVersion(t *testing.T) {
	c := newCLI(t)
	if code := c.exec("-config", c.path("missing.json"), "version"); code != exitOK || c.stdout.String() != "dev\n" {
		t.Errorf("exit %d, %q", code, c.stdout.String())
	}
}

func TestRunDryRunOut(t *testing.T) {
	c := newCLI(t)
	out := c.path("x.epub")
	if code := c.run("-v", "run", "-dry-run", "-out", out); code != exitOK {
		t.Fatalf("exit %d: %s", code, c.stderr.String())
	}
	data, err := os.ReadFile(out)
	if err != nil || !bytes.HasPrefix(data, []byte("PK")) || !bytes.Contains(data[:80], []byte("mimetypeapplication/epub+zip")) {
		t.Errorf("epub: %v", err)
	}
	if c.exists("state.json") || len(c.mailer.sent) != 0 || c.stdout.Len() != 0 {
		t.Error("dry run had side effects")
	}
	c.wantStderr("wrote epub")

	if code := c.run("run", "-dry-run"); code != exitOK {
		t.Fatalf("exit %d: %s", code, c.stderr.String())
	}
	c.wantStderr("built and discarded")
}

// With one pick per edition, the second run still has a fresh story and gets
// as far as the archive lock.
func TestRunArchiveTwice(t *testing.T) {
	c := newCLI(t)
	data, err := os.ReadFile(c.config)
	if err != nil {
		t.Fatal(err)
	}
	c.write("config.json", strings.Replace(string(data), `"max_per_domain": -1`, `"max_per_domain": -1, "picks": 1`, 1))
	editions := c.path("editions")

	if code := c.run("run", "-archive", editions); code != exitOK {
		t.Fatalf("exit %d: %s", code, c.stderr.String())
	}
	first, err := os.ReadFile(c.path("state.json"))
	if err != nil || !strings.Contains(string(first), news.URLID(c.site+"/ok1")) {
		t.Fatalf("state = %s, %v", first, err)
	}
	if code := c.run("run", "-archive", editions); code != exitOK {
		t.Fatalf("exit %d: %s", code, c.stderr.String())
	}
	c.wantStderr("skipped: edition already archived")
	second, _ := os.ReadFile(c.path("state.json"))
	if len(c.mailer.sent) != 1 || !bytes.Equal(first, second) {
		t.Errorf("%d emails, state %s", len(c.mailer.sent), second)
	}
}

func TestRunFailure(t *testing.T) {
	c := newCLI(t)
	c.mailer.err = errors.New("smtp down")
	out := c.path("x.epub")
	if code := c.run("run", "-out", out); code != exitFailure {
		t.Fatalf("exit %d", code)
	}
	c.wantStderr(`msg="run failed"`, "smtp down")
	if c.stdout.Len() != 0 || !c.exists("x.epub") || c.exists("state.json") {
		t.Error("want -out written, nothing on stdout, no state")
	}

	if code := c.run("run", "-dry-run", "-out", c.path("no/such/dir.epub")); code != exitFailure {
		t.Errorf("exit %d", code)
	}
	c.wantStderr("write -out")
}

func TestSources(t *testing.T) {
	c := newCLI(t)
	if code := c.run("sources"); code != exitOK {
		t.Fatalf("exit %d: %s", code, c.stderr.String())
	}
	rows := strings.Split(strings.TrimSuffix(c.stdout.String(), "\n"), "\n")
	if len(rows) != 3 || rows[0] != "id\tsource\tscore\tdomain\ttitle" {
		t.Fatalf("rows = %q", rows)
	}
	for _, r := range rows[1:] {
		if f := strings.Split(r, "\t"); len(f) != 5 || !strings.HasPrefix(f[4], "Story ok") {
			t.Errorf("row = %q", r)
		}
	}
	c.wantStderr("msg=done fetched=2 after_prefilter=2")

	c.write("state.json", "{")
	if code := c.run("sources"); code != exitFailure || c.stdout.Len() != 0 {
		t.Errorf("corrupt state: exit %d, stdout %q", code, c.stdout.String())
	}
}

func TestExtract(t *testing.T) {
	c := newCLI(t)
	if code := c.run("extract", c.site+"/ok1"); code != exitOK {
		t.Fatalf("exit %d: %s", code, c.stderr.String())
	}
	first, rest, _ := strings.Cut(c.stdout.String(), "\n")
	if !strings.HasPrefix(first, `<!-- title="Page /ok1" words=`) || !strings.HasSuffix(first, `lang="en" -->`) || !strings.Contains(rest, "<p>") {
		t.Errorf("stdout = %q", first)
	}

	if code := c.run("extract", c.site+"/short"); code != exitFailure || c.stdout.Len() != 0 {
		t.Errorf("exit %d", code)
	}
	c.wantStderr("rejected: too short")
	if strings.Contains(c.stderr.String(), "extract failed") {
		t.Errorf("rejection reported twice: %s", c.stderr.String())
	}

	if code := c.run("extract", c.site+"/boom"); code != exitFailure {
		t.Errorf("exit %d", code)
	}
	c.wantStderr(`msg="extract failed"`)
}

func TestSend(t *testing.T) {
	c := newCLI(t)
	file := c.write("diario-2026-11-26.epub", "PK data")
	for _, args := range [][]string{
		{"send", file},
		{"send", c.write("2026-11-25.epub", "PK")},
		{"send", c.write("notes.epub", "PK")},
		{"send", "-subject", "Again", file},
	} {
		if code := c.run(args...); code != exitOK {
			t.Fatalf("%v: exit %d: %s", args, code, c.stderr.String())
		}
	}
	want := "Diario · 2026-11-26|Diario · 2026-11-25|notes|Again"
	if got := strings.Join(c.mailer.subjects(t), "|"); got != want {
		t.Errorf("subjects = %q", got)
	}
	if !bytes.Contains(c.mailer.sent[0], []byte("filename=diario-2026-11-26.epub")) {
		t.Errorf("attachment name missing")
	}
	if c.exists("state.json") {
		t.Error("send wrote state")
	}

	if code := c.run("send", c.path("missing.epub")); code != exitFailure {
		t.Errorf("missing file: exit %d", code)
	}
	c.mailer.err = errors.New("smtp down")
	if code := c.run("send", file); code != exitFailure {
		t.Errorf("smtp error: exit %d", code)
	}
	noTo := c.write("noto.json", `{"smtp":{"host":"h","from":"a@b.test"}}`)
	if code := c.exec("-config", noTo, "send", file); code != exitFailure {
		t.Errorf("no smtp.to: exit %d", code)
	}
	c.wantStderr("smtp.to")
	badFrom := c.write("bad.json", `{"smtp":{"host":"h","from":"a@b.test\n","to":"me@kindle.com"}}`)
	if code := c.exec("-config", badFrom, "send", file); code != exitFailure {
		t.Errorf("bad from: exit %d", code)
	}
}

func TestConfigErrors(t *testing.T) {
	c := newCLI(t)
	invalid := c.write("invalid.json", `{"edition":{"picks":0}}`)
	for _, path := range []string{c.path("missing.json"), invalid} {
		if code := c.exec("-config", path, "run"); code != exitFailure || c.stdout.Len() != 0 {
			t.Errorf("%s: exit %d", path, code)
		}
		c.wantStderr(`msg="run failed"`, path)
	}
}

func TestDefaultMailer(t *testing.T) {
	if (env{}).mailer(mail.SMTPConfig{}) == nil {
		t.Error("no default mailer")
	}
}
