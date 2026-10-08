// Package rihttp is the mirror's HTTP client: single-origin, self-identifying,
// rate-paced, and polite about throttling responses.
package rihttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

// Throttled says the server asked to be called less often (429, 503, 504): the request was
// not refused, and the same request will be answered once the pacing has slowed down.
func (e *StatusCodeError) Throttled() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode == http.StatusServiceUnavailable ||
		e.StatusCode == http.StatusGatewayTimeout
}

// Throttled says err is a throttle answer (StatusCodeError.Throttled).
func Throttled(err error) bool {
	var sc *StatusCodeError
	return errors.As(err, &sc) && sc.Throttled()
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
}

// New builds a client. Exactly one Client should be shared per process so the
// limiter is global.
func New(cfg config.Config, lim *limiter.Limiter) *Client {
	return &Client{
		http: &http.Client{
			Timeout: cfg.RequestTimeout,
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

// do issues exactly one paced request. A failure is returned rather than retried
// here: the adaptive learner is the mirror's only throttle response, and a failed
// resource is re-selected on a later sweep of the crawl, bounded by the store's
// attempt limit. Two independent backoff ladders on one request only made the
// effective pace impossible to reason about.
func (c *Client) do(ctx context.Context, bucket, method, url, accept string, body []byte) (*http.Response, error) {
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
		// Transport-level failure says nothing about pacing: the request may
		// never have reached the service. Neutral keeps the learner honest,
		// while still breaking a success streak so learning only happens on
		// clean windows.
		c.limiter.Feedback(bucket, limiter.OutcomeNeutral)
		slog.Warn("request failed", "url", url, "err", err)
		return nil, fmt.Errorf("%s %s: %w", method, url, err)
	}
	c.Requests.Add(1)

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		c.limiter.Feedback(bucket, limiter.OutcomeOK)
		return resp, nil

	case resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode == http.StatusServiceUnavailable ||
		resp.StatusCode == http.StatusGatewayTimeout:
		// Any 429 is authoritative: the adaptive bucket doubles its interval, and
		// a Retry-After pushes the whole bucket out so the next request waits too.
		c.Throttle.Add(1)
		c.limiter.Feedback(bucket, limiter.OutcomeThrottled)
		ra := retryAfter(resp)
		drain(resp)
		if applied := c.limiter.Delay(bucket, ra); ra > 0 && applied < ra {
			slog.Warn("retry-after clamped to the pacing ceiling",
				"url", url, "retry_after", ra.String(), "applied", applied.String())
		}
		slog.Warn("throttled by server", "url", url, "status", resp.StatusCode, "retry_after", ra.String())
		return nil, &StatusCodeError{StatusCode: resp.StatusCode, Status: resp.Status, URL: url, RetryAfter: ra}

	case resp.StatusCode >= 500:
		// Any other server fault is about the request, not the pace: the site answers
		// 500 for the same handful of documents every time, and treating that as
		// pushback doubled the interval per broken document until a run crawled at
		// minutes per request. Neutral breaks the success streak and nothing more.
		c.limiter.Feedback(bucket, limiter.OutcomeNeutral)
		drain(resp)
		slog.Warn("server error", "url", url, "status", resp.StatusCode)
		return nil, &StatusCodeError{StatusCode: resp.StatusCode, Status: resp.Status, URL: url}

	default:
		// Other 4xx are permanent answers, not capacity signals: a 404 means
		// the server handled the request fine, so it counts as success and
		// must not slow the crawl.
		c.limiter.Feedback(bucket, limiter.OutcomeOK)
		snippet := readSnippet(resp, 512)
		return nil, &StatusCodeError{StatusCode: resp.StatusCode, Status: resp.Status, URL: url, Body: snippet}
	}
}

// retryAfter reports a server's Retry-After in seconds or HTTP-date form, or zero
// when it is absent or unparseable. Nothing replaces it: the adaptive bucket
// already doubles on a throttle, which is the fallback.
func retryAfter(resp *http.Response) time.Duration {
	h := resp.Header.Get("Retry-After")
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// Limiter exposes the pacing state so status and logs can report the interval
// actually in force rather than the one requested at startup.
func (c *Client) Limiter() *limiter.Limiter { return c.limiter }

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
}

func readSnippet(resp *http.Response, n int64) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, n))
	resp.Body.Close()
	return strings.TrimSpace(string(b))
}
