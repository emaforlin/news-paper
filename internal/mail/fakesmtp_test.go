package mail

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeConfig is the behaviour of a fakeServer. The zero value is a plain
// server that advertises nothing beyond 8BITMIME and accepts every command.
type fakeConfig struct {
	TLS               *tls.Config // server certificate (implicit TLS and STARTTLS)
	ImplicitTLS       bool
	AdvertiseSTARTTLS bool
	AdvertiseAUTH     bool
	// Reply overrides the reply to a verb ("EHLO", "RCPT", …). The key "."
	// overrides the reply after the end of DATA.
	Reply      map[string]string
	Greeting   string // "" → "220 fake ESMTP"; "-" → never greet
	StallAfter string // after replying to this verb, stop reading until cleanup
	DropOnQuit bool   // close the connection instead of replying to QUIT
}

// fakeServer is an in-process SMTP server on 127.0.0.1 speaking the minimal
// dialogue net/smtp needs. Every goroutine exits on test cleanup.
type fakeServer struct {
	fakeConfig
	Addr      string
	Host      string
	Port      int
	connected chan struct{} // one value per accepted connection

	ln   net.Listener
	done chan struct{}
	wg   sync.WaitGroup

	mu         sync.Mutex
	conns      []net.Conn
	transcript []string
	data       [][]byte
}

func startFake(t *testing.T, cfg fakeConfig) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	s := &fakeServer{
		fakeConfig: cfg,
		Addr:       ln.Addr().String(),
		Host:       addr.IP.String(),
		Port:       addr.Port,
		connected:  make(chan struct{}, 64),
		ln:         ln,
		done:       make(chan struct{}),
	}
	s.wg.Add(1)
	go s.accept()
	t.Cleanup(s.close)
	return s
}

func (s *fakeServer) accept() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, c)
		s.mu.Unlock()
		s.connected <- struct{}{}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer c.Close()
			s.serve(c)
		}()
	}
}

func (s *fakeServer) close() {
	close(s.done)
	s.ln.Close()
	s.mu.Lock()
	for _, c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// Transcript returns every command line received, in order.
func (s *fakeServer) Transcript() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.transcript...)
}

// Data returns every accepted DATA payload, dot-unstuffed, CRLF preserved.
func (s *fakeServer) Data() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.data...)
}

// Conns returns the number of accepted connections.
func (s *fakeServer) Conns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Has reports whether any received command starts with verb.
func (s *fakeServer) Has(verb string) bool {
	for _, l := range s.Transcript() {
		if strings.HasPrefix(strings.ToUpper(l), verb) {
			return true
		}
	}
	return false
}

func (s *fakeServer) serve(c net.Conn) {
	if s.ImplicitTLS {
		c = tls.Server(c, s.TLS)
	}
	tp := textproto.NewConn(c)
	if s.Greeting == "-" {
		<-s.done
		return
	}
	greeting := s.Greeting
	if greeting == "" {
		greeting = "220 fake ESMTP"
	}
	if tp.PrintfLine("%s", greeting) != nil {
		return
	}
	secure := s.ImplicitTLS
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.transcript = append(s.transcript, line)
		s.mu.Unlock()
		verb, _, _ := strings.Cut(line, " ")
		verb = strings.ToUpper(verb)

		if r, ok := s.Reply[verb]; ok {
			if tp.PrintfLine("%s", r) != nil {
				return
			}
		} else {
			switch verb {
			case "EHLO":
				ext := []string{"fake", "8BITMIME"}
				if s.AdvertiseSTARTTLS && !secure {
					ext = append(ext, "STARTTLS")
				}
				if s.AdvertiseAUTH {
					ext = append(ext, "AUTH PLAIN")
				}
				for i, e := range ext {
					sep := "-"
					if i == len(ext)-1 {
						sep = " "
					}
					if tp.PrintfLine("250%s%s", sep, e) != nil {
						return
					}
				}
			case "STARTTLS":
				if !s.AdvertiseSTARTTLS || secure {
					tp.PrintfLine("502 not supported")
					continue
				}
				if tp.PrintfLine("220 go ahead") != nil {
					return
				}
				c = tls.Server(c, s.TLS)
				tp = textproto.NewConn(c)
				secure = true
			case "AUTH":
				if !s.AdvertiseAUTH {
					tp.PrintfLine("503 AUTH not advertised")
					continue
				}
				tp.PrintfLine("235 ok")
			case "DATA":
				if tp.PrintfLine("354 go ahead") != nil {
					return
				}
				if s.StallAfter == "DATA" {
					<-s.done
					return
				}
				body, err := readData(tp.R)
				if err != nil {
					return
				}
				if r, ok := s.Reply["."]; ok {
					tp.PrintfLine("%s", r)
					continue
				}
				s.mu.Lock()
				s.data = append(s.data, body)
				s.mu.Unlock()
				tp.PrintfLine("250 queued")
			case "QUIT":
				if !s.DropOnQuit {
					tp.PrintfLine("221 bye")
				}
				return
			case "HELO", "MAIL", "RCPT", "RSET", "NOOP":
				tp.PrintfLine("250 ok")
			default:
				tp.PrintfLine("500 unknown command")
			}
		}
		if verb == s.StallAfter {
			<-s.done
			return
		}
	}
}

// readData reads a DATA payload up to the "." line, undoing dot-stuffing but
// keeping line endings as sent (textproto's DotReader rewrites CRLF to LF).
func readData(r *bufio.Reader) ([]byte, error) {
	var out bytes.Buffer
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		if bytes.Equal(line, []byte(".\r\n")) {
			return out.Bytes(), nil
		}
		if line[0] == '.' {
			line = line[1:]
		}
		out.Write(line)
	}
}

// rawClient drives the fake server without net/smtp, so the fake is tested
// independently of the code under test.
type rawClient struct {
	t  *testing.T
	c  net.Conn
	tp *textproto.Conn
}

func dialRaw(t *testing.T, s *fakeServer) *rawClient {
	t.Helper()
	c, err := net.Dial("tcp", s.Addr)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(2 * time.Second))
	t.Cleanup(func() { c.Close() })
	return &rawClient{t: t, c: c, tp: textproto.NewConn(c)}
}

func (r *rawClient) expect(code int) string {
	r.t.Helper()
	_, msg, err := r.tp.ReadResponse(code)
	if err != nil {
		r.t.Fatalf("want %d: %v", code, err)
	}
	return msg
}

func (r *rawClient) cmd(code int, format string, args ...any) string {
	r.t.Helper()
	if err := r.tp.PrintfLine(format, args...); err != nil {
		r.t.Fatal(err)
	}
	return r.expect(code)
}

func TestFakeServer(t *testing.T) {
	t.Run("full dialogue with STARTTLS", func(t *testing.T) {
		srvTLS, pool := loopbackCert(t)
		s := startFake(t, fakeConfig{TLS: srvTLS, AdvertiseSTARTTLS: true, AdvertiseAUTH: true})
		r := dialRaw(t, s)
		r.expect(220)
		if ext := r.cmd(250, "EHLO test"); !strings.Contains(ext, "STARTTLS") || !strings.Contains(ext, "AUTH PLAIN") {
			t.Fatalf("EHLO extensions = %q", ext)
		}
		r.cmd(220, "STARTTLS")
		tc := tls.Client(r.c, &tls.Config{ServerName: "127.0.0.1", RootCAs: pool})
		r.tp = textproto.NewConn(tc)
		if ext := r.cmd(250, "EHLO test"); strings.Contains(ext, "STARTTLS") {
			t.Fatalf("STARTTLS advertised after the upgrade: %q", ext)
		}
		r.cmd(235, "AUTH PLAIN AHUAcA==")
		r.cmd(250, "MAIL FROM:<a@b>")
		r.cmd(250, "RCPT TO:<c@d>")
		r.cmd(354, "DATA")
		if _, err := r.tp.W.WriteString("line 1\r\n..dot\r\n.\r\n"); err != nil {
			t.Fatal(err)
		}
		r.tp.W.Flush()
		r.expect(250)
		r.cmd(500, "BOGUS")
		r.cmd(221, "QUIT")

		if got := s.Data(); len(got) != 1 || string(got[0]) != "line 1\r\n.dot\r\n" {
			t.Errorf("Data = %q", got)
		}
		want := []string{"EHLO test", "STARTTLS", "EHLO test", "AUTH PLAIN AHUAcA==", "MAIL FROM:<a@b>", "RCPT TO:<c@d>", "DATA", "BOGUS", "QUIT"}
		if got := s.Transcript(); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("transcript = %q, want %q", got, want)
		}
		if s.Conns() != 1 || !s.Has("AUTH") || s.Has("RSET") {
			t.Errorf("Conns = %d, Has(AUTH) = %v, Has(RSET) = %v", s.Conns(), s.Has("AUTH"), s.Has("RSET"))
		}
	})

	t.Run("nothing advertised", func(t *testing.T) {
		s := startFake(t, fakeConfig{})
		r := dialRaw(t, s)
		r.expect(220)
		r.cmd(250, "HELO test")
		r.cmd(502, "STARTTLS")
		r.cmd(503, "AUTH PLAIN x")
	})

	t.Run("canned replies", func(t *testing.T) {
		s := startFake(t, fakeConfig{Greeting: "220 custom", Reply: map[string]string{"RCPT": "550 no such user", ".": "552 too big"}})
		r := dialRaw(t, s)
		if msg := r.expect(220); msg != "custom" {
			t.Errorf("greeting = %q", msg)
		}
		r.cmd(550, "RCPT TO:<x@y>")
		r.cmd(354, "DATA")
		r.tp.W.WriteString("x\r\n.\r\n")
		r.tp.W.Flush()
		r.expect(552)
		if len(s.Data()) != 0 {
			t.Error("rejected DATA was recorded")
		}
	})

	t.Run("stall greeting", func(t *testing.T) {
		s := startFake(t, fakeConfig{Greeting: "-"})
		r := dialRaw(t, s)
		<-s.connected
		r.c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		if _, _, err := r.tp.ReadResponse(220); err == nil {
			t.Fatal("got a greeting from a stalled server")
		}
	})

	t.Run("stall after verb and drop on quit", func(t *testing.T) {
		s := startFake(t, fakeConfig{StallAfter: "MAIL"})
		r := dialRaw(t, s)
		r.expect(220)
		r.cmd(250, "MAIL FROM:<a@b>")
		r.c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		r.tp.PrintfLine("RCPT TO:<c@d>")
		if _, _, err := r.tp.ReadResponse(250); err == nil {
			t.Fatal("stalled server replied")
		}

		s2 := startFake(t, fakeConfig{DropOnQuit: true})
		r2 := dialRaw(t, s2)
		r2.expect(220)
		r2.tp.PrintfLine("QUIT")
		if _, _, err := r2.tp.ReadResponse(221); err == nil {
			t.Fatal("got 221 from a drop-on-quit server")
		}
	})
}
