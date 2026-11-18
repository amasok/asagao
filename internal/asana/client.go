// Package asana is a thin client for the Asana REST API (only the endpoints asagao uses).
package asana

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
)

const DefaultBaseURL = "https://app.asana.com/api/1.0"

type Client struct {
	BaseURL    string
	HTTP       *http.Client // must add the Authorization header (e.g. oauth2.NewClient)
	MaxRetries int
	// MaxRetryDelay caps the wait before a retry, including one asked for by Retry-After
	// (0: no cap). An interactive check sets it so that it does not keep the user waiting.
	MaxRetryDelay time.Duration
	// OnRetry, if set, is called before waiting to retry a request that got status.
	OnRetry func(status int, wait time.Duration)
	// AuthHint tells the user how to fix a 401 (e.g. how to log in again); it is added to
	// the message of 401 errors.
	AuthHint string
	// plainTasks: Asana refused the optional task fields; request tasks without them.
	plainTasks atomic.Bool
	// sleep is replaced in tests.
	sleep func(context.Context, time.Duration) error
	// clockOffset is Asana's clock minus ours in nanoseconds, from the Date header of the
	// latest response (0 until one arrived, or when the difference is within noise).
	clockOffset atomic.Int64
	clockKnown  atomic.Bool // a response with a Date header has arrived
}

// minClockOffset is the smallest difference between Asana's clock and ours that
// ClockOffset reports. The Date header has a resolution of one second and the response
// takes a moment to arrive, so smaller differences are noise.
const minClockOffset = 5 * time.Second

// ClockOffset returns how far Asana's clock is ahead of this machine's (negative when it
// is behind), measured from the Date header of the latest response. Times compared with
// Asana's timestamps (created_at, modified_since) are corrected by it, so a wrong PC clock
// does not make asagao miss what it created or changed. It is 0 before any response.
func (c *Client) ClockOffset() time.Duration {
	return time.Duration(c.clockOffset.Load())
}

// ClockKnown reports whether ClockOffset has been measured (a response with a valid Date
// header has arrived).
func (c *Client) ClockKnown() bool { return c.clockKnown.Load() }

// observeDate records the clock offset from a response's Date header.
func (c *Client) observeDate(h http.Header, received time.Time) {
	server, err := http.ParseTime(h.Get("Date"))
	if err != nil {
		return
	}
	// The header is truncated to the second: on average Asana's clock is half a second later.
	off := server.Add(500 * time.Millisecond).Sub(received)
	if off > -minClockOffset && off < minClockOffset {
		off = 0
	}
	c.clockOffset.Store(int64(off))
	c.clockKnown.Store(true)
}

func NewClient(httpClient *http.Client) *Client {
	return &Client{BaseURL: DefaultBaseURL, HTTP: httpClient, MaxRetries: 5}
}

// IsUnauthorized reports whether Asana rejected the token (model.IsUnauthorized).
func IsUnauthorized(err error) bool { return model.IsUnauthorized(err) }

// newError builds the error for a >= 400 response with body b.
func (c *Client) newError(status int, b []byte) *Error {
	e := &Error{Status: status}
	_ = json.Unmarshal(b, e)
	if status == http.StatusUnauthorized {
		e.Hint = c.AuthHint
	}
	return e
}

// IsPaymentRequired reports whether the endpoint needs a paid Asana plan (model.IsPaymentRequired).
func IsPaymentRequired(err error) bool { return model.IsPaymentRequired(err) }

type envelope struct {
	Data     json.RawMessage `json:"data"`
	Sync     string          `json:"sync"`     // GET /events
	HasMore  bool            `json:"has_more"` // GET /events
	NextPage *struct {
		Offset string `json:"offset"`
	} `json:"next_page"`
}

// do sends one request and decodes the "data" field into out. It retries on 429, and on
// 5xx for idempotent methods (see retryable).
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) (*envelope, error) {
	return c.doWith(ctx, method, path, q, body, out, func(status int) bool {
		return retryable(method, status) || (path == batchPath && status >= 500)
	})
}

// doWith is do with its own choice of the statuses to retry.
func (c *Client) doWith(ctx context.Context, method, path string, q url.Values, body, out any, retry func(status int) bool) (*envelope, error) {
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(map[string]any{"data": body}); err != nil {
			return nil, err
		}
	}
	rep, err := c.send(ctx, c.HTTP, method, u, payload, retry)
	if err != nil {
		return nil, err
	}
	if rep.status >= 400 {
		return nil, c.newError(rep.status, rep.body)
	}
	var env envelope
	if err := json.Unmarshal(rep.body, &env); err != nil {
		return nil, fmt.Errorf("asana: decode response: %w", err)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return nil, fmt.Errorf("asana: decode data: %w", err)
		}
	}
	return &env, nil
}

// reply is a response as it arrived: its status and body.
type reply struct {
	status int
	body   []byte
}

// send sends one request to u through hc and returns the response, retrying (at most
// MaxRetries times, after retryDelay) while retry says the status may be retried.
func (c *Client) send(ctx context.Context, hc *http.Client, method, u string, payload []byte, retry func(status int) bool) (*reply, error) {
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		c.observeDate(resp.Header, time.Now())
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		if retry(resp.StatusCode) && attempt < c.MaxRetries {
			d := retryDelay(resp, attempt)
			if c.MaxRetryDelay > 0 && d > c.MaxRetryDelay {
				d = c.MaxRetryDelay
			}
			if c.OnRetry != nil {
				c.OnRetry(resp.StatusCode, d)
			}
			if err := c.wait(ctx, d); err != nil {
				return nil, err
			}
			continue
		}
		return &reply{status: resp.StatusCode, body: b}, nil
	}
}

// retryable reports whether a response may be retried. 429 means the request was not
// processed. A 5xx to a POST (create, add comment, ...) may have been applied anyway, so
// repeating it could duplicate the change; only idempotent methods are retried then.
func retryable(method string, status int) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	return status >= 500 && method != http.MethodPost
}

// maxRetryAfter is the longest Retry-After taken as it is; a larger (or negative, or
// unreadable) value is ignored for the usual backoff.
const maxRetryAfter = 24 * time.Hour

func retryDelay(resp *http.Response, attempt int) time.Duration {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 && n <= int(maxRetryAfter/time.Second) {
			return time.Duration(n) * time.Second
		}
	}
	return time.Duration(1<<attempt) * time.Second
}

func (c *Client) wait(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
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

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	_, err := c.do(ctx, http.MethodGet, path, q, nil, out)
	return err
}

// list fetches every page of a collection endpoint.
func list[T any](ctx context.Context, c *Client, path string, q url.Values) ([]T, error) {
	q = cloneValues(q)
	q.Set("limit", "100")
	var all []T
	for {
		var page []T
		env, err := c.do(ctx, http.MethodGet, path, q, nil, &page)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if env.NextPage == nil || env.NextPage.Offset == "" {
			return all, nil
		}
		q.Set("offset", env.NextPage.Offset)
	}
}

func cloneValues(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}
