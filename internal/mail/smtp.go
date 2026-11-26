package mail

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// TLS modes for SMTPConfig.TLSMode.
const (
	ModeSTARTTLS = "starttls" // plain TCP, then mandatory STARTTLS (port 587)
	ModeTLS      = "tls"      // TLS from the first byte (port 465)
)

const (
	defaultTimeout = 60 * time.Second // whole dialogue, when ctx has no deadline
	heloName       = "localhost"
)

var (
	// ErrNoStartTLS: the server doesn't offer STARTTLS in starttls mode.
	// Nothing past EHLO is sent, so credentials never travel in clear.
	ErrNoStartTLS = errors.New("server does not advertise STARTTLS")
	// ErrNoAuth: a Username is configured but the server doesn't offer AUTH.
	ErrNoAuth = errors.New("server does not advertise AUTH")
)

// SMTPConfig is the transport configuration. The pipeline maps the app
// config into it; this package doesn't import internal/config.
type SMTPConfig struct {
	Host     string
	Port     int    // usually 587 (starttls) or 465 (tls)
	TLSMode  string // ModeSTARTTLS | ModeTLS
	Username string // empty = no AUTH
	Password string
}

// String redacts the password, so printing the config never leaks it.
func (c SMTPConfig) String() string {
	return fmt.Sprintf("{Host:%s Port:%d TLSMode:%s Username:%s Password:%s}",
		c.Host, c.Port, c.TLSMode, c.Username, redact(c.Password))
}

// GoString redacts the password for %#v too.
func (c SMTPConfig) GoString() string { return "mail.SMTPConfig" + c.String() }

// LogValue redacts the password in slog output.
func (c SMTPConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("host", c.Host),
		slog.Int("port", c.Port),
		slog.String("tls_mode", c.TLSMode),
		slog.String("username", c.Username),
		slog.String("password", redact(c.Password)),
	)
}

func redact(s string) string {
	if s == "" {
		return ""
	}
	return "[redacted]"
}

func (c SMTPConfig) validate() error {
	switch {
	case c.Host == "" || strings.ContainsFunc(c.Host, unicode.IsSpace):
		return fmt.Errorf("invalid host %q", c.Host)
	case c.Port < 1 || c.Port > 65535:
		return fmt.Errorf("invalid port %d", c.Port)
	case c.TLSMode != ModeSTARTTLS && c.TLSMode != ModeTLS:
		return fmt.Errorf("invalid TLS mode %q (want %q or %q)", c.TLSMode, ModeSTARTTLS, ModeTLS)
	}
	return nil
}

// Sender delivers a raw message. from and to may be "Name <a@b>" or "a@b".
type Sender interface {
	Send(ctx context.Context, from, to string, msg []byte) error
}

// NewSMTP returns a Sender that delivers over SMTP with mandatory TLS. The
// config is validated on each Send.
func NewSMTP(c SMTPConfig) Sender {
	return &smtpSender{cfg: c, timeout: defaultTimeout}
}

// smtpSender is immutable after construction and safe for concurrent use.
type smtpSender struct {
	cfg     SMTPConfig
	roots   *x509.CertPool // nil = system roots
	timeout time.Duration  // used when ctx has no deadline
}

// Send runs one SMTP dialogue: dial, greeting, EHLO, STARTTLS (starttls
// mode), AUTH PLAIN (if Username is set), MAIL, RCPT, DATA. Errors are
// prefixed with the failing phase and wrap ctx.Err() when the context caused
// them. It never retries.
func (s *smtpSender) Send(ctx context.Context, from, to string, msg []byte) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	if err := s.cfg.validate(); err != nil {
		return fmt.Errorf("smtp config: %w", err)
	}
	fromAddr, err := addrSpec(from)
	if err != nil {
		return fmt.Errorf("smtp from: %w", err)
	}
	toAddr, err := addrSpec(to)
	if err != nil {
		return fmt.Errorf("smtp to: %w", err)
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(s.timeout)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	wrap := func(phase string, err error) error {
		cerr := ctx.Err()
		if cerr == nil && !time.Now().Before(deadline) {
			// The connection deadline equals ctx's and may fire first.
			cerr = context.DeadlineExceeded
		}
		if cerr != nil {
			return fmt.Errorf("%s: %w: %w", phase, cerr, err)
		}
		return fmt.Errorf("%s: %w", phase, err)
	}

	tc := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12, RootCAs: s.roots}
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port)))
	if err != nil {
		return wrap("smtp dial", err)
	}
	// Close the TCP socket directly. tls.Conn.Close sends close_notify with
	// its own 5 s write deadline, which would block an error return when the
	// server has stopped reading. SMTP's orderly close is QUIT, not TLS.
	defer raw.Close()
	// net/smtp takes no context: bound every read/write with the deadline,
	// and unblock them at once if ctx is cancelled earlier.
	raw.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { raw.SetDeadline(time.Now()) })
	defer stop()

	conn := raw
	if s.cfg.TLSMode == ModeTLS {
		tconn := tls.Client(raw, tc)
		if err := tconn.HandshakeContext(ctx); err != nil {
			return wrap("smtp dial", err)
		}
		conn = tconn // net/smtp sees a *tls.Conn and treats the session as encrypted
	}

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return wrap("smtp greeting", err)
	}
	if err := c.Hello(heloName); err != nil {
		return wrap("smtp hello", err)
	}

	if s.cfg.TLSMode == ModeSTARTTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return wrap("smtp starttls", ErrNoStartTLS)
		}
		if err := c.StartTLS(tc); err != nil {
			return wrap("smtp starttls", err)
		}
	}
	if s.cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return wrap("smtp auth", ErrNoAuth)
		}
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return wrap("smtp auth", err)
		}
	}
	if err := c.Mail(fromAddr); err != nil {
		return wrap("smtp MAIL FROM", err)
	}
	if err := c.Rcpt(toAddr); err != nil {
		return wrap("smtp RCPT TO", err)
	}
	w, err := c.Data()
	if err != nil {
		return wrap("smtp DATA", err)
	}
	if _, err := w.Write(msg); err != nil {
		return wrap("smtp DATA", err)
	}
	if err := w.Close(); err != nil {
		return wrap("smtp end DATA", err)
	}
	// The server accepted the message with the reply to end of DATA. A QUIT
	// failure must not turn that into an error, or the edition is resent.
	// Quit also closes the client; every error path above leaves that to
	// raw.Close.
	_ = c.Quit()
	return nil
}

// addrSpec returns the bare address of "Name <a@b>" or " a@b ".
func addrSpec(s string) (string, error) {
	a := extractAddr(s)
	if !strings.Contains(a, "@") || strings.HasPrefix(a, "@") || strings.HasSuffix(a, "@") ||
		strings.ContainsFunc(a, func(r rune) bool { return r <= ' ' || r == 0x7f || strings.ContainsRune("<>,", r) }) {
		return "", fmt.Errorf("invalid address %q", s)
	}
	return a, nil
}
