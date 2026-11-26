// Package httpx is the HTTP client shared by the pipeline stages: request
// timeouts, a cap on the body size and, for GET, a single retry on transient
// failures (network errors, 429 and 5xx). Status semantics belong to the caller.
package httpx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	DefaultTimeout   = 20 * time.Second
	DefaultMaxBody   = 2 << 20 // 2 MiB
	DefaultBackoff   = 500 * time.Millisecond
	DefaultUserAgent = "news-paper/1"

	// drainLimit bounds how much of a non-2xx body is read so the
	// connection can be reused.
	drainLimit = 64 << 10
)

// defaultHTTP is used when Client.HTTP is nil. It is never mutated.
var defaultHTTP = &http.Client{Timeout: DefaultTimeout}

// Client is safe for concurrent use; it is read-only after construction.
// The zero value works: default HTTP client, default User-Agent, no backoff.
type Client struct {
	HTTP      *http.Client  // nil → shared client with DefaultTimeout
	UserAgent string        // "" → DefaultUserAgent
	Backoff   time.Duration // wait before the single retry, used as-is
}

// New returns a Client with its own http.Client and DefaultBackoff.
// timeout <= 0 means DefaultTimeout.
func New(timeout time.Duration, userAgent string) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		HTTP:      &http.Client{Timeout: timeout},
		UserAgent: userAgent,
		Backoff:   DefaultBackoff,
	}
}

// Response is a fully read HTTP response.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte   // nil for a non-2xx Get; Post keeps it for every status
	Truncated  bool     // the body was longer than maxBody and was cut
	URL        *url.URL // final URL after redirects
}

// Get sends GET rawURL with the extra headers h. It retries once, after
// Backoff, on network errors and on 429/5xx. After the retry, a failing
// status is returned as a Response with a nil error. maxBody <= 0 means
// DefaultMaxBody.
func (c *Client) Get(ctx context.Context, rawURL string, h http.Header, maxBody int64) (*Response, error) {
	if maxBody <= 0 {
		maxBody = DefaultMaxBody
	}
	resp, retry, err := c.do(ctx, http.MethodGet, rawURL, h, nil, maxBody, false)
	if retry {
		if werr := sleep(ctx, c.Backoff); werr != nil {
			return nil, fmt.Errorf("GET %s: %w", rawURL, werr)
		}
		resp, _, err = c.do(ctx, http.MethodGet, rawURL, h, nil, maxBody, false)
	}
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", rawURL, err)
	}
	return resp, nil
}

// Post sends POST rawURL with body and the extra headers h. It never retries:
// a POST may not be idempotent and may cost money. The body is read (capped at
// maxBody) for every status, because error replies carry the reason.
// maxBody <= 0 means DefaultMaxBody.
func (c *Client) Post(ctx context.Context, rawURL string, h http.Header, body []byte, maxBody int64) (*Response, error) {
	if maxBody <= 0 {
		maxBody = DefaultMaxBody
	}
	resp, _, err := c.do(ctx, http.MethodPost, rawURL, h, body, maxBody, true)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", rawURL, err)
	}
	return resp, nil
}

// do performs one request. retry reports whether the outcome is transient and
// worth a second try. keepErrorBody reads non-2xx bodies instead of draining.
func (c *Client) do(ctx context.Context, method, rawURL string, h http.Header, reqBody []byte, maxBody int64, keepErrorBody bool) (resp *Response, retry bool, err error) {
	var r io.Reader
	if reqBody != nil {
		r = bytes.NewReader(reqBody) // sets GetBody, so 307/308 replay it
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, r)
	if err != nil {
		return nil, false, err
	}
	for k, vs := range h {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", c.userAgent())
	}

	res, err := c.client().Do(req)
	if err != nil {
		return nil, ctx.Err() == nil, err
	}
	defer res.Body.Close()

	resp = &Response{StatusCode: res.StatusCode, Header: res.Header, URL: res.Request.URL}
	retry = res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500
	if res.StatusCode/100 != 2 && !keepErrorBody {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, drainLimit))
		return resp, retry, nil
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return nil, ctx.Err() == nil, fmt.Errorf("read body: %w", err)
	}
	if int64(len(body)) > maxBody {
		body = body[:maxBody]
		resp.Truncated = true
	}
	resp.Body = body
	return resp, retry, nil
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTP
}

func (c *Client) userAgent() string {
	if c.UserAgent != "" {
		return c.UserAgent
	}
	return DefaultUserAgent
}

// sleep waits for d or until ctx is done, whichever comes first.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
