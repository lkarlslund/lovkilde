package rihttp

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lkarlslund/lovkilde/internal/config"
	"github.com/lkarlslund/lovkilde/internal/limiter"
)

// testClient builds a client against h. Buckets are held at millisecond scale
// so these tests assert how the client reacts to a status code, not how fast it
// paces: the learner's arithmetic has its own package test.
func testClient(t *testing.T, h http.Handler) (*Client, string) {
	t.Helper()
	if err := config.Default().Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	// Max equal to the floor keeps a throttled test from slowing itself down.
	ms := limiter.Spec{Start: time.Millisecond, Floor: time.Millisecond, Max: time.Millisecond}
	lim := limiter.New(map[string]limiter.Spec{
		limiter.BucketDocument: ms,
		limiter.BucketHarvest:  ms,
		limiter.BucketSitemap:  ms,
	})
	return New(config.Default(), lim), srv.URL
}

// gzipServing reproduces the real deployment: the origin sits behind a CDN that
// gzips responses. Callers must receive decompressed bytes.
func TestTransparentlyDecodesGzip(t *testing.T) {
	want := strings.Repeat("<?xml version=\"1.0\"?><Dokument/>", 200)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Go only auto-decompresses when IT added Accept-Encoding, so we must not
		// set that header by hand. Assert the server actually compressed.
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			t.Error("client should advertise gzip so the CDN can compress")
		}
		w.Header().Set("Content-Type", "application/xml")
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		_, _ = gz.Write([]byte(want))
		_ = gz.Close()
	})
	c, url := testClient(t, h)
	got, err := c.GetJSON(context.Background(), limiter.BucketSitemap, url+"/eli/sitemap.xml", "application/xml")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("body mismatch: got %d bytes starting %q, want %d bytes",
			len(got), got[:minInt(len(got), 40)], len(want))
	}
	if bytes.Contains(got, []byte{0x1f, 0x8b}) {
		t.Error("body still contains gzip magic bytes")
	}
}

// A throttle is answered by returning, not by retrying inside the call: the
// adaptive learner is the only backoff, and it acts on the whole bucket. A
// Retry-After must therefore delay the *next* request rather than stall this one.
func TestThrottleReturnsAndPacesTheNextRequest(t *testing.T) {
	var hits atomic.Int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "Servicen er kaldt for ofte.")
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	c, url := testClient(t, h)
	ctx := context.Background()

	_, err := c.GetJSON(ctx, limiter.BucketDocument, url+"/x", "text/plain")
	var sc *StatusCodeError
	if !errors.As(err, &sc) {
		t.Fatalf("first call: expected StatusCodeError, got %T: %v", err, err)
	}
	if sc.RetryAfter != time.Second {
		t.Errorf("RetryAfter = %s, want 1s", sc.RetryAfter)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server saw %d requests for one call, want 1: the client must not retry in place", got)
	}
	if c.Throttle.Load() != 1 {
		t.Errorf("Throttle = %d, want 1", c.Throttle.Load())
	}

	start := time.Now()
	got, err := c.GetJSON(ctx, limiter.BucketDocument, url+"/x", "text/plain")
	if err != nil {
		t.Fatalf("second call should succeed once the wait has passed: %v", err)
	}
	if string(got) != "ok" {
		t.Errorf("body = %q", got)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("second call waited %s, want the Retry-After interval to be honoured", elapsed)
	}
}

// An endless 429 must cost one request per call, not a retry chain: the mirror is
// told to stop, and the crawl decides whether to try the resource again later.
func TestPersistentThrottleCostsOneRequestPerCall(t *testing.T) {
	var hits atomic.Int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c, url := testClient(t, h)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := c.GetJSON(ctx, limiter.BucketDocument, url+"/x", "text/plain"); err == nil {
			t.Fatalf("call %d: expected a throttle error", i+1)
		}
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("server saw %d requests for 3 calls, want 3", got)
	}
}

// A server fault is a load signal, so it is reported as throttling, but it is
// still one request per call rather than a retry ladder.
func TestServerFaultIsReportedOnce(t *testing.T) {
	var hits atomic.Int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c, url := testClient(t, h)
	if _, err := c.GetJSON(context.Background(), limiter.BucketDocument, url+"/x", "text/plain"); err == nil {
		t.Fatal("expected error")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server saw %d requests, want 1", got)
	}
}

// 404 and 410 are permanent: retrying them wastes the polite request budget.
func TestNonRetryableClientErrors(t *testing.T) {
	var hits atomic.Int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "not found")
	})
	c, url := testClient(t, h)

	_, err := c.GetJSON(context.Background(), limiter.BucketDocument, url+"/gone", "application/xml")
	var sc *StatusCodeError
	if !errors.As(err, &sc) {
		t.Fatalf("expected StatusCodeError, got %T: %v", err, err)
	}
	if sc.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d", sc.StatusCode)
	}
	if hits.Load() != 1 {
		t.Errorf("404 requested %d times, want 1", hits.Load())
	}
}

func TestSendsIdentifyingUserAgent(t *testing.T) {
	// An honest User-Agent with a contact address is what lets the operator
	// contact us instead of banning our IP.
	got := make(chan string, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got <- r.Header.Get("User-Agent") })
	c, url := testClient(t, h)
	if _, err := c.GetJSON(context.Background(), limiter.BucketDocument, url+"/x", "text/plain"); err != nil {
		t.Fatal(err)
	}
	ua := <-got
	if !strings.Contains(ua, "contact:") {
		t.Errorf("User-Agent %q lacks a contact address", ua)
	}
}

// A Retry-After wait must stay cancellable, so Ctrl-C during a long throttle does
// not strand the crawl until the server's patience expires.
func TestThrottleWaitIsCancellable(t *testing.T) {
	var hits atomic.Int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "30")
		}
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c, url := testClient(t, h)
	ctx := context.Background()
	if _, err := c.GetJSON(ctx, limiter.BucketDocument, url+"/x", "text/plain"); err == nil {
		t.Fatal("expected throttle error")
	}

	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.GetJSON(short, limiter.BucketDocument, url+"/x", "text/plain"); err == nil {
		t.Fatal("expected cancellation while the bucket is held")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("cancelled wait took %s, want prompt return", elapsed)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server saw %d requests, want 1", got)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// adaptiveClient builds a client whose document bucket is adaptive, to prove the
// client actually reports outcomes to the learner rather than only pacing from a
// fixed schedule.
func adaptiveClient(t *testing.T, h http.Handler) (*Client, string, *limiter.Limiter) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	lim := limiter.NewTuned(map[string]limiter.Spec{
		limiter.BucketDocument: {Start: 600 * time.Millisecond, Floor: 200 * time.Millisecond, Max: 30 * time.Second},
	}, 1.5, 3, 0)
	return New(config.Default(), lim), srv.URL, lim
}

// A 429 must make the adaptive bucket slower for subsequent requests. One request
// is one observation: the doubling happens once, not once per retry hop.
func TestClientLearnsToSlowDownFrom429(t *testing.T) {
	var throttle atomic.Bool
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if throttle.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	c, url, lim := adaptiveClient(t, h)
	ctx := context.Background()

	if _, err := c.GetJSON(ctx, limiter.BucketDocument, url+"/x", "text/plain"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := lim.Current(limiter.BucketDocument); cur != 600*time.Millisecond {
		t.Fatalf("clean start: interval = %s, want 600ms", cur)
	}

	throttle.Store(true)
	// Retry-After is absent from the real service, so the client must learn the
	// slowdown itself.
	_, _ = c.GetJSON(ctx, limiter.BucketDocument, url+"/x", "text/plain")

	cur, _ := lim.Current(limiter.BucketDocument)
	if cur != 1200*time.Millisecond {
		t.Errorf("after one throttled request: interval = %s, want 1.2s (client did not report the throttle to the learner)", cur)
	}
}

// A window of clean successes must make the next requests faster. 404 counts as a
// clean success: a missing document is not a capacity signal.
func TestClientLearnsToSpeedUpFromCleanWindow(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	c, url, lim := adaptiveClient(t, h)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, _ = c.GetJSON(ctx, limiter.BucketDocument, url+"/missing", "text/plain")
	}
	cur, _ := lim.Current(limiter.BucketDocument)
	if cur >= 600*time.Millisecond {
		t.Errorf("after a clean window of 3 (window=3): interval = %s, want it to have stepped down", cur)
	}
}
