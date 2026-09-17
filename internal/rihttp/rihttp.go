// Package rihttp is the mirror's HTTP client: single-origin, self-identifying,
// rate-paced, and polite about throttling responses.
package rihttp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lkarlslund/retsinformation/internal/config"
	"github.com/lkarlslund/retsinformation/internal/limiter"
)

// StatusCodeError carries a non-2xx response so callers can branch on status.
// The body is always drained and closed by Do, so it is not readable here.
type StatusCodeError struct {
	StatusCode int
	Status     string
	URL        string
	RetryAfter time.Duration
	Body       string
}

func (e *StatusCodeError) Error() string {
	return fmt.Sprintf("%s: status %d %s", e.URL, e.StatusCode, e.Status)
}

// Client is a pacing-aware http.Client for the retsinformation origins.
type Client struct {
	http      *http.Client
	cfg       config.Config
	limiter   *limiter.Limiter
	userAgent string

	Requests atomic.Int64
	Throttle atomic.Int64 // number of 429/503 responses seen
	jitter   *mrand.Rand
}

// New builds a client. Exactly one Client should be shared per process so the
// limiter is global.
func New(cfg config.Config, lim *limiter.Limiter) *Client {
	return &Client{
		http: &http.Client{
			Timeout: cfg.RequestTimeout.D(),
			Transport: &http.Transport{
				// Deliberately conservative: one connection per host, no
				// multiplexed bursts. Slower in theory, far less likely to look
				// like an attack.
				MaxIdleConns:        2,
				MaxIdleConnsPerHost: 1,
				MaxConnsPerHost:     2,
				DisableCompression:  false,
				ForceAttemptHTTP2:   true,
			},
		},
		cfg:       cfg,
		limiter:   lim,
		userAgent: cfg.UserAgent,
		jitter:    mrand.New(mrand.NewSource(time.Now().UnixNano())),
	}
}

// GetJSON fetches url paced in the given bucket and returns the response body.
func (c *Client) GetJSON(ctx context.Context, bucket, url string, accept string) ([]byte, error) {
	resp, err := c.do(ctx, bucket, http.MethodGet, url, accept, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// PostJSON performs a paced JSON POST. The site's document endpoint requires
// POST but accepts an empty JSON object as the body.
func (c *Client) PostJSON(ctx context.Context, bucket, url string, body []byte) ([]byte, error) {
	resp, err := c.do(ctx, bucket, http.MethodPost, url, "application/json", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// do issues a request with pacing, retry, and throttle-aware backoff.
func (c *Client) do(ctx context.Context, bucket, method, url, accept string, body []byte) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, c.backoff(attempt)); err != nil {
				return nil, err
			}
		}
		if err := c.limiter.Wait(ctx, bucket); err != nil {
			return nil, err
		}

		var rdr io.Reader
		if body != nil {
			rdr = strings.NewReader(string(body))
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rdr)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.userAgent)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		// Deliberately do NOT set Accept-Encoding. Go's transport adds gzip
		// itself and transparently decompresses the result; setting the header
		// by hand disables that and hands callers raw gzip bytes, which fails
		// XML/JSON decoding with misleading syntax errors.

		resp, err := c.http.Do(req)
		if err != nil {
			// Transport-level failure: worth retrying, but count it as a
			// signal to slow down.
			lastErr = fmt.Errorf("%s %s: %w", method, url, err)
			slog.Warn("request failed", "url", url, "attempt", attempt, "err", err)
			continue
		}
		c.Requests.Add(1)

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return resp, nil

		case resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout:
			// The harvesting service publishes a 1-per-10s limit and answers 429
			// when exceeded. Treat any 429 as authoritative: honour Retry-After
			// and never resume at the previous pace.
			c.Throttle.Add(1)
			wait := respRetryAfter(resp, c.cfg.BackoffBase.D())
			drain(resp)
			lastErr = &StatusCodeError{StatusCode: resp.StatusCode, Status: resp.Status, URL: url, RetryAfter: wait}
			slog.Warn("throttled by server", "url", url, "status", resp.StatusCode, "backoff", wait.String(), "attempt", attempt)
			if err := c.sleep(ctx, wait); err != nil {
				return nil, err
			}
			// Push the bucket forward so the very next request is also delayed.
			continue

		case resp.StatusCode >= 500:
			drain(resp)
			lastErr = &StatusCodeError{StatusCode: resp.StatusCode, Status: resp.Status, URL: url}
			slog.Warn("server error", "url", url, "status", resp.StatusCode, "attempt", attempt)
			continue

		default:
			// 4xx other than 429: not retryable by pacing (bad URL, gone, ...).
			snippet := readSnippet(resp, 512)
			return nil, &StatusCodeError{StatusCode: resp.StatusCode, Status: resp.Status, URL: url, Body: snippet}
		}
	}
	return nil, fmt.Errorf("%s %s: giving up after %d attempts: %w", method, url, c.cfg.MaxRetries+1, lastErr)
}

// backoff grows exponentially from BackoffBase to BackoffMax with jitter.
func (c *Client) backoff(attempt int) time.Duration {
	base := c.cfg.BackoffBase.D()
	max := c.cfg.BackoffMax.D()
	d := base
	for i := 1; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	j := c.cfg.BackoffJitter
	if j > 0 {
		// Jitter both ways so concurrent workers do not align their retries.
		d += time.Duration((c.jitter.Float64()*2 - 1) * j * float64(d))
	}
	if d < time.Second {
		d = time.Second
	}
	return d
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// respRetryAfter honours Retry-After in seconds or HTTP-date form, falling back
// to base when absent or unparseable.
func respRetryAfter(resp *http.Response, fallback time.Duration) time.Duration {
	h := resp.Header.Get("Retry-After")
	if h == "" {
		return fallback
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return fallback
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
}

func readSnippet(resp *http.Response, n int64) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, n))
	resp.Body.Close()
	return strings.TrimSpace(string(b))
}
