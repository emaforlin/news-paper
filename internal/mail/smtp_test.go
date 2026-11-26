package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testUser     = "diario@example.com"
	testPassword = "s3cret-app-password"
	testFrom     = "Diario <diario@example.com>"
	testTo       = "lector@kindle.com"
)

var testMsg = []byte("Subject: hi\r\n\r\nhello\r\n")

func newTestSender(s *fakeServer, mode string, roots *x509.CertPool, user string) *smtpSender {
	c := SMTPConfig{Host: s.Host, Port: s.Port, TLSMode: mode}
	if user != "" {
		c.Username, c.Password = user, testPassword
	}
	return &smtpSender{cfg: c, roots: roots, timeout: 2 * time.Second}
}

// sendErr sends and returns the error, checking that it never leaks the
// password (AT-S12).
func sendErr(t *testing.T, ctx context.Context, snd Sender, msg []byte) error {
	t.Helper()
	err := snd.Send(ctx, testFrom, testTo, msg)
	if err != nil && strings.Contains(err.Error(), testPassword) {
		t.Fatalf("error leaks the password: %v", err)
	}
	return err
}

func checkTranscript(t *testing.T, s *fakeServer, want ...string) {
	t.Helper()
	if got := s.Transcript(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("transcript:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func authLine() string {
	return "AUTH PLAIN " + base64.StdEncoding.EncodeToString([]byte("\x00"+testUser+"\x00"+testPassword))
}

// AT-A01
func TestAddrSpec(t *testing.T) {
	ok := []struct{ in, want string }{
		{"Ema <ema@x.com>", "ema@x.com"},
		{" ema@x.com ", "ema@x.com"},
		{`"Forlin, E" <e@x.com>`, "e@x.com"},
		{"<e@x.com>", "e@x.com"},
		{"Ema < e@x.com >", "e@x.com"},
		{"e@x.com\r\n", "e@x.com"},
	}
	for _, tt := range ok {
		if got, err := addrSpec(tt.in); err != nil || got != tt.want {
			t.Errorf("addrSpec(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{"", "Ema <>", "nobody", "@x.com", "e@", "a b@x.com", "e@x.com\x00", "e@x\x7f.com", "Ema <e@x.com> extra", "a@b,c@d"} {
		if got, err := addrSpec(in); err == nil {
			t.Errorf("addrSpec(%q) = %q, want error", in, got)
		}
	}
}

// AT-A02
func TestSMTPConfigValidate(t *testing.T) {
	valid := SMTPConfig{Host: "smtp.gmail.com", Port: 587, TLSMode: ModeSTARTTLS}
	tests := []struct {
		name    string
		mod     func(*SMTPConfig)
		wantErr bool
	}{
		{"starttls", func(*SMTPConfig) {}, false},
		{"tls", func(c *SMTPConfig) { c.TLSMode, c.Port = ModeTLS, 465 }, false},
		{"port 65535", func(c *SMTPConfig) { c.Port = 65535 }, false},
		{"empty host", func(c *SMTPConfig) { c.Host = "" }, true},
		{"host with space", func(c *SMTPConfig) { c.Host = "smtp gmail.com" }, true},
		{"port 0", func(c *SMTPConfig) { c.Port = 0 }, true},
		{"port 65536", func(c *SMTPConfig) { c.Port = 65536 }, true},
		{"empty mode", func(c *SMTPConfig) { c.TLSMode = "" }, true},
		{"ssl", func(c *SMTPConfig) { c.TLSMode = "ssl" }, true},
		{"uppercase", func(c *SMTPConfig) { c.TLSMode = "STARTTLS" }, true},
	}
	for _, tt := range tests {
		c := valid
		tt.mod(&c)
		if err := c.validate(); (err != nil) != tt.wantErr {
			t.Errorf("%s: validate() = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
	}

	// Nothing is dialed for an invalid config or address.
	s := startFake(t, fakeConfig{})
	pre := []struct {
		name     string
		mode     string
		from, to string
		want     string
	}{
		{"bad mode", "ssl", testFrom, testTo, "smtp config: "},
		{"bad from", ModeTLS, "diario", testTo, "smtp from: "},
		{"bad to", ModeTLS, testFrom, "Lector <>", "smtp to: "},
	}
	for _, tt := range pre {
		err := newTestSender(s, tt.mode, nil, "").Send(context.Background(), tt.from, tt.to, testMsg)
		if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want prefix %q", tt.name, err, tt.want)
		}
	}
	if n := s.Conns(); n != 0 {
		t.Errorf("server saw %d connections, want 0", n)
	}
}

// AT-A03
func TestSMTPConfigRedacted(t *testing.T) {
	c := SMTPConfig{Host: "smtp.gmail.com", Port: 587, TLSMode: ModeSTARTTLS, Username: testUser, Password: testPassword}
	var logBuf bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuf, nil)).Info("config", "smtp", c)
	outs := map[string]string{
		"%v":   fmt.Sprintf("%v", c),
		"%+v":  fmt.Sprintf("%+v", c),
		"%#v":  fmt.Sprintf("%#v", c),
		"%s":   fmt.Sprint(c),
		"slog": logBuf.String(),
	}
	for k, out := range outs {
		if strings.Contains(out, testPassword) {
			t.Errorf("%s leaks the password: %s", k, out)
		}
		if !strings.Contains(out, testUser) || !strings.Contains(out, "[redacted]") {
			t.Errorf("%s = %s, want username and [redacted]", k, out)
		}
	}
	c.Password = ""
	if out := c.String(); strings.Contains(out, "[redacted]") || !strings.HasSuffix(out, "Password:}") {
		t.Errorf("empty password printed as %s", out)
	}
}

// AT-A04
func TestNewSMTPDefaults(t *testing.T) {
	c := SMTPConfig{Host: "smtp.gmail.com", Port: 587, TLSMode: ModeSTARTTLS, Username: "u", Password: "p"}
	s, ok := NewSMTP(c).(*smtpSender)
	if !ok {
		t.Fatalf("NewSMTP returned %T", NewSMTP(c))
	}
	if s.cfg != c || s.timeout != 60*time.Second || s.roots != nil {
		t.Errorf("NewSMTP = %+v, want cfg as given, 60s timeout, system roots", s)
	}
}

// AT-S01
func TestSendImplicitTLS(t *testing.T) {
	srvTLS, pool := loopbackCert(t)
	s := startFake(t, fakeConfig{TLS: srvTLS, ImplicitTLS: true, AdvertiseAUTH: true})
	if err := sendErr(t, context.Background(), newTestSender(s, ModeTLS, pool, testUser), testMsg); err != nil {
		t.Fatal(err)
	}
	if d := s.Data(); len(d) != 1 || !bytes.Equal(d[0], testMsg) {
		t.Errorf("Data = %q, want %q", d, testMsg)
	}
	checkTranscript(t, s,
		"EHLO localhost",
		authLine(),
		"MAIL FROM:<diario@example.com> BODY=8BITMIME",
		"RCPT TO:<lector@kindle.com>",
		"DATA",
		"QUIT",
	)
}

// AT-S02
func TestSendSTARTTLS(t *testing.T) {
	srvTLS, pool := loopbackCert(t)
	s := startFake(t, fakeConfig{TLS: srvTLS, AdvertiseSTARTTLS: true, AdvertiseAUTH: true})
	if err := sendErr(t, context.Background(), newTestSender(s, ModeSTARTTLS, pool, testUser), testMsg); err != nil {
		t.Fatal(err)
	}
	checkTranscript(t, s,
		"EHLO localhost",
		"STARTTLS",
		"EHLO localhost",
		authLine(),
		"MAIL FROM:<diario@example.com> BODY=8BITMIME",
		"RCPT TO:<lector@kindle.com>",
		"DATA",
		"QUIT",
	)
	if len(s.Data()) != 1 {
		t.Errorf("got %d messages, want 1", len(s.Data()))
	}
}

// AT-S03
func TestSendNoSTARTTLS(t *testing.T) {
	tests := []struct {
		name string
		cfg  fakeConfig
		want []string
	}{
		{"not advertised", fakeConfig{AdvertiseAUTH: true}, []string{"EHLO localhost"}},
		{"HELO-only server", fakeConfig{AdvertiseAUTH: true, Reply: map[string]string{"EHLO": "502 no EHLO"}}, []string{"EHLO localhost", "HELO localhost"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := startFake(t, tt.cfg)
			err := sendErr(t, context.Background(), newTestSender(s, ModeSTARTTLS, nil, testUser), testMsg)
			if !errors.Is(err, ErrNoStartTLS) || !strings.HasPrefix(err.Error(), "smtp starttls: ") {
				t.Fatalf("err = %v, want smtp starttls: ErrNoStartTLS", err)
			}
			checkTranscript(t, s, tt.want...)
			if s.Has("AUTH") || s.Has("MAIL") || s.Has("DATA") {
				t.Error("sent AUTH/MAIL/DATA over an unencrypted connection")
			}
		})
	}
}

// AT-S04
func TestSendNoAuth(t *testing.T) {
	srvTLS, pool := loopbackCert(t)

	t.Run("no username", func(t *testing.T) {
		s := startFake(t, fakeConfig{TLS: srvTLS, ImplicitTLS: true, AdvertiseAUTH: true})
		if err := sendErr(t, context.Background(), newTestSender(s, ModeTLS, pool, ""), testMsg); err != nil {
			t.Fatal(err)
		}
		if s.Has("AUTH") || len(s.Data()) != 1 {
			t.Errorf("AUTH sent = %v, messages = %d; want no AUTH, 1 message", s.Has("AUTH"), len(s.Data()))
		}
	})

	t.Run("AUTH not advertised", func(t *testing.T) {
		s := startFake(t, fakeConfig{TLS: srvTLS, ImplicitTLS: true})
		err := sendErr(t, context.Background(), newTestSender(s, ModeTLS, pool, testUser), testMsg)
		if !errors.Is(err, ErrNoAuth) || !strings.HasPrefix(err.Error(), "smtp auth: ") {
			t.Fatalf("err = %v, want smtp auth: ErrNoAuth", err)
		}
		if s.Has("AUTH") || s.Has("MAIL") {
			t.Error("sent AUTH/MAIL to a server without AUTH")
		}
	})
}

// AT-S05
func TestSendPhases(t *testing.T) {
	srvTLS, pool := loopbackCert(t)
	tests := []struct {
		name  string
		mode  string
		reply map[string]string
		greet string
		want  string
		code  int
	}{
		{"greeting", ModeTLS, nil, "554 go away", "smtp greeting: ", 554},
		{"hello", ModeTLS, map[string]string{"EHLO": "550 no", "HELO": "550 no"}, "", "smtp hello: ", 550},
		{"starttls", ModeSTARTTLS, map[string]string{"STARTTLS": "454 TLS not available"}, "", "smtp starttls: ", 454},
		{"auth", ModeTLS, map[string]string{"AUTH": "535 bad credentials"}, "", "smtp auth: ", 535},
		{"MAIL FROM", ModeTLS, map[string]string{"MAIL": "550 sender rejected"}, "", "smtp MAIL FROM: ", 550},
		{"RCPT TO", ModeTLS, map[string]string{"RCPT": "550 no such user"}, "", "smtp RCPT TO: ", 550},
		{"DATA", ModeTLS, map[string]string{"DATA": "554 no data"}, "", "smtp DATA: ", 554},
		{"end DATA", ModeTLS, map[string]string{".": "552 too big"}, "", "smtp end DATA: ", 552},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := startFake(t, fakeConfig{
				TLS: srvTLS, ImplicitTLS: tt.mode == ModeTLS, AdvertiseSTARTTLS: true, AdvertiseAUTH: true,
				Reply: tt.reply, Greeting: tt.greet,
			})
			err := sendErr(t, context.Background(), newTestSender(s, tt.mode, pool, testUser), testMsg)
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Fatalf("err = %v, want prefix %q", err, tt.want)
			}
			var te *textproto.Error
			if !errors.As(err, &te) || te.Code != tt.code {
				t.Errorf("err = %v, want *textproto.Error with code %d", err, tt.code)
			}
			if len(s.Data()) != 0 {
				t.Error("message recorded despite the error")
			}
		})
	}

	t.Run("dial", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		snd := &smtpSender{cfg: SMTPConfig{Host: "127.0.0.1", Port: port, TLSMode: ModeSTARTTLS}, timeout: time.Second}
		if err := sendErr(t, context.Background(), snd, testMsg); err == nil || !strings.HasPrefix(err.Error(), "smtp dial: ") {
			t.Fatalf("err = %v, want smtp dial", err)
		}
	})
}

// AT-S06
func TestSendTLSVerify(t *testing.T) {
	srvTLS, _ := loopbackCert(t)

	t.Run("untrusted certificate", func(t *testing.T) {
		for _, mode := range []string{ModeTLS, ModeSTARTTLS} {
			s := startFake(t, fakeConfig{TLS: srvTLS, ImplicitTLS: mode == ModeTLS, AdvertiseSTARTTLS: true, AdvertiseAUTH: true})
			// NewSMTP: system roots, which don't trust the test certificate.
			snd := NewSMTP(SMTPConfig{Host: s.Host, Port: s.Port, TLSMode: mode, Username: testUser, Password: testPassword})
			err := sendErr(t, context.Background(), snd, testMsg)
			want := map[string]string{ModeTLS: "smtp dial: ", ModeSTARTTLS: "smtp starttls: "}[mode]
			var uae x509.UnknownAuthorityError
			if !errors.As(err, &uae) || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("%s: err = %v, want %q with x509.UnknownAuthorityError", mode, err, want)
			}
			if s.Has("AUTH") {
				t.Errorf("%s: AUTH sent after a failed handshake", mode)
			}
		}
	})

	t.Run("TLS 1.1 server", func(t *testing.T) {
		old, pool := loopbackCert(t)
		old.MaxVersion = tls.VersionTLS11
		s := startFake(t, fakeConfig{TLS: old, ImplicitTLS: true, AdvertiseAUTH: true})
		err := sendErr(t, context.Background(), newTestSender(s, ModeTLS, pool, testUser), testMsg)
		if err == nil || !strings.HasPrefix(err.Error(), "smtp dial: ") || s.Has("AUTH") {
			t.Errorf("err = %v, want smtp dial handshake error and no AUTH", err)
		}
	})

	t.Run("hostname mismatch", func(t *testing.T) {
		other, pool := selfSignedCert(t, []string{"example.test"}, nil)
		s := startFake(t, fakeConfig{TLS: other, ImplicitTLS: true, AdvertiseAUTH: true})
		err := sendErr(t, context.Background(), newTestSender(s, ModeTLS, pool, testUser), testMsg)
		var he x509.HostnameError
		if !errors.As(err, &he) {
			t.Errorf("err = %v, want x509.HostnameError", err)
		}
	})
}

// AT-S07
func TestSendDeadline(t *testing.T) {
	t.Run("ctx deadline", func(t *testing.T) {
		s := startFake(t, fakeConfig{Greeting: "-"})
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := sendErr(t, ctx, newTestSender(s, ModeSTARTTLS, nil, ""), testMsg)
		if el := time.Since(start); el > time.Second {
			t.Errorf("Send took %v", el)
		}
		if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), "smtp greeting: ") {
			t.Errorf("err = %v, want smtp greeting + DeadlineExceeded", err)
		}
	})

	t.Run("fallback timeout", func(t *testing.T) {
		s := startFake(t, fakeConfig{Greeting: "-"})
		snd := newTestSender(s, ModeSTARTTLS, nil, "")
		snd.timeout = 100 * time.Millisecond
		start := time.Now()
		err := sendErr(t, context.Background(), snd, testMsg)
		if el := time.Since(start); el > time.Second {
			t.Errorf("Send took %v", el)
		}
		if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), "smtp greeting: ") {
			t.Errorf("err = %v, want smtp greeting + DeadlineExceeded", err)
		}
	})
}

// AT-S08
func TestSendCancel(t *testing.T) {
	t.Run("cancelled mid-dialogue", func(t *testing.T) {
		s := startFake(t, fakeConfig{Greeting: "-"})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-s.connected
			cancel()
		}()
		snd := newTestSender(s, ModeSTARTTLS, nil, "")
		snd.timeout = time.Hour // only the cancellation can stop it
		start := time.Now()
		err := sendErr(t, ctx, snd, testMsg)
		if el := time.Since(start); el > time.Second {
			t.Errorf("Send took %v", el)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("already cancelled", func(t *testing.T) {
		s := startFake(t, fakeConfig{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := sendErr(t, ctx, newTestSender(s, ModeSTARTTLS, nil, ""), testMsg)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if n := s.Conns(); n != 0 {
			t.Errorf("server saw %d connections, want 0", n)
		}
	})
}

// AT-S09
func TestSendStalledData(t *testing.T) {
	srvTLS, pool := loopbackCert(t)
	s := startFake(t, fakeConfig{TLS: srvTLS, ImplicitTLS: true, StallAfter: "DATA"})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	big := bytes.Repeat([]byte("0123456789abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvw\r\n"), 32<<20/72)
	start := time.Now()
	err := sendErr(t, ctx, newTestSender(s, ModeTLS, pool, ""), big)
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("Send took %v", el)
	}
	if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), "smtp DATA: ") {
		t.Errorf("err = %v, want smtp DATA + DeadlineExceeded", err)
	}
}

// AT-S10
func TestSendQuitIgnored(t *testing.T) {
	srvTLS, pool := loopbackCert(t)
	s := startFake(t, fakeConfig{TLS: srvTLS, ImplicitTLS: true, DropOnQuit: true})
	if err := sendErr(t, context.Background(), newTestSender(s, ModeTLS, pool, ""), testMsg); err != nil {
		t.Fatalf("err = %v, want nil once the message was accepted", err)
	}
	if len(s.Data()) != 1 {
		t.Errorf("got %d messages, want 1", len(s.Data()))
	}
}

// AT-S11
func TestSendDotStuffing(t *testing.T) {
	srvTLS, pool := loopbackCert(t)
	s := startFake(t, fakeConfig{TLS: srvTLS, ImplicitTLS: true})
	msg := []byte("Subject: dots\r\n\r\n.\r\n..x\r\n.hidden\r\nend\r\n")
	if err := sendErr(t, context.Background(), newTestSender(s, ModeTLS, pool, ""), msg); err != nil {
		t.Fatal(err)
	}
	if d := s.Data(); len(d) != 1 || !bytes.Equal(d[0], msg) {
		t.Errorf("Data = %q, want %q", d, msg)
	}
}

// AT-S13
func TestBuildAndSend(t *testing.T) {
	srvTLS, pool := loopbackCert(t)
	s := startFake(t, fakeConfig{TLS: srvTLS, ImplicitTLS: true, AdvertiseAUTH: true})
	m := testMessage()
	m.Attach.Data = randomBytes(150 << 10)
	raw := mustBuild(t, m)
	if err := newTestSender(s, ModeTLS, pool, testUser).Send(context.Background(), m.From, m.To, raw); err != nil {
		t.Fatal(err)
	}
	d := s.Data()
	if len(d) != 1 {
		t.Fatalf("got %d messages", len(d))
	}
	checkLines(t, d[0])
	hdr, parts := parseMessage(t, d[0])
	if got := decodeSubject(t, hdr.Get("Subject")); got != m.Subject {
		t.Errorf("Subject = %q", got)
	}
	if len(parts) != 2 || !bytes.Equal(parts[1].Decoded, m.Attach.Data) {
		t.Errorf("attachment did not survive SMTP")
	}
}

// AT-S14 (run with -race)
func TestSendConcurrent(t *testing.T) {
	srvTLS, pool := loopbackCert(t)
	s := startFake(t, fakeConfig{TLS: srvTLS, ImplicitTLS: true, AdvertiseAUTH: true})
	var snd Sender = newTestSender(s, ModeTLS, pool, testUser)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = snd.Send(context.Background(), testFrom, testTo, testMsg)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("send %d: %v", i, err)
		}
	}
	if len(s.Data()) != 4 {
		t.Errorf("got %d messages, want 4", len(s.Data()))
	}
}
