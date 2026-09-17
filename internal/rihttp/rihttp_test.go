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

	"github.com/lkarlslund/retsinformation/internal/config"
	"github.com/lkarlslund/retsinformation/internal/limiter"
)

func testClient(t *testing.T, h http.Handler, tune func(*config.Config)) (*Client, string) {
	t.Helper()
	cfg := config.Default()
	// Keep tests fast: the pacing under test is verified by its own package test.
	cfg.DocumentStart = 200 * time.Millisecond
	cfg.DocumentFloor = 200 * time.Millisecond
	cfg.DocumentMax = time.Minute
	cfg.HarvestInterval = 10 * time.Second
	cfg.BackoffBase = 20 * time.Millisecond
	cfg.BackoffMax = 80 * time.Millisecond
	cfg.MaxRetries = 3
	if tune != nil {
		tune(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	lim := limiter.New(map[string]limiter.Spec{
		// Pinned so pacing tests measure the client's retry behaviour rather than
		// the adaptive learner moving intervals underneath them.
		limiter.BucketDocument: {Start: cfg.DocumentStart, Floor: cfg.DocumentStart, Max: cfg.DocumentMax, Pinned: true},
		limiter.BucketHarvest:  {Start: cfg.HarvestInterval, Floor: cfg.HarvestInterval, Max: cfg.HarvestInterval, Pinned: true},
		limiter.BucketSitemap:  {Start: cfg.SitemapInterval, Floor: cfg.SitemapInterval, Max: cfg.SitemapInterval, Pinned: true},
	}, cfg.AdaptiveStepDown, cfg.AdaptiveWindow, cfg.AdaptiveCooldown)
	return New(cfg, lim), srv.URL
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
	c, url := testClient(t, h, nil)
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

// The published harvesting limit is one call per 10 seconds; when exceeded the
// service answers 429. The client must back off rather than hammer on, and must
// respect Retry-After.
func TestBacksOffOn429AndHonoursRetryAfter(t *testing.T) {
	var hits atomic.Int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n <= 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "Servicen er kaldt for ofte.")
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	c, url := testClient(t, h, nil)

	start := time.Now()
	got, err := c.GetJSON(context.Background(), limiter.BucketDocument, url+"/x", "text/plain")
	if err != nil {
		t.Fatalf("request should eventually succeed: %v", err)
	}
	if string(got) != "ok" {
		t.Errorf("body = %q", got)
	}
	if c.Throttle.Load() != 2 {
		t.Errorf("Throttle = %d, want 2", c.Throttle.Load())
	}
	// Two Retry-After: 1 responses must have forced at least ~2s of waiting.
	if elapsed := time.Since(start); elapsed < 1500*time.Millisecond {
		t.Errorf("only waited %s for two Retry-After: 1 responses", elapsed)
	}
}

// A persistent 429 must end in an error, not an infinite retry loop that keeps
// the mirror hammering a service that has told it to stop.
func TestGivesUpWhenThrottlingPersists(t *testing.T) {
	var hits atomic.Int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c, url := testClient(t, h, func(cfg *config.Config) { cfg.MaxRetries = 2 })

	_, err := c.GetJSON(context.Background(), limiter.BucketDocument, url+"/x", "text/plain")
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if got := hits.Load(); got != 3 { // initial attempt + MaxRetries
		t.Errorf("server saw %d requests, want 3", got)
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
	c, url := testClient(t, h, nil)

	_, err := c.GetJSON(context.Background(), limiter.BucketDocument, url+"/gone", "application/xml")
	var sc *StatusCodeError
	if !errors.As(err, &sc) {
		t.Fatalf("expected StatusCodeError, got %T: %v", err, err)
	}
	if sc.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d", sc.StatusCode)
	}
	if hits.Load() != 1 {
		t.Errorf("404 retried %d times, want 1", hits.Load())
	}
}

func TestSendsIdentifyingUserAgent(t *testing.T) {
	// An honest User-Agent with a contact address is what lets the operator
	// contact us instead of banning our IP.
	got := make(chan string, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got <- r.Header.Get("User-Agent") })
	c, url := testClient(t, h, nil)
	if _, err := c.GetJSON(context.Background(), limiter.BucketDocument, url+"/x", "text/plain"); err != nil {
		t.Fatal(err)
	}
	ua := <-got
	if !strings.Contains(ua, "contact:") {
		t.Errorf("User-Agent %q lacks a contact address", ua)
	}
}

func TestContextCancellationStopsRetryLoop(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c, url := testClient(t, h, func(cfg *config.Config) { cfg.MaxRetries = 50 })
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := c.GetJSON(ctx, limiter.BucketDocument, url+"/x", "text/plain"); err == nil {
		t.Fatal("expected cancellation error")
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
	cfg := config.Default()
	cfg.DocumentStart = 600 * time.Millisecond
	cfg.DocumentFloor = 200 * time.Millisecond
	cfg.DocumentMax = 30 * time.Second
	cfg.AdaptiveWindow = 3
	cfg.AdaptiveStepDown = 1.5
	cfg.AdaptiveCooldown = 0
	cfg.BackoffBase = 10 * time.Millisecond
	cfg.BackoffMax = 20 * time.Millisecond
	cfg.MaxRetries = 1
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	lim := limiter.New(map[string]limiter.Spec{
		limiter.BucketDocument: {Start: cfg.DocumentStart, Floor: cfg.DocumentFloor, Max: cfg.DocumentMax},
	}, cfg.AdaptiveStepDown, cfg.AdaptiveWindow, cfg.AdaptiveCooldown)
	return New(cfg, lim), srv.URL, lim
}

// A 429 must make the adaptive bucket slower for subsequent requests, not just
// for the current retry chain.
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

	// MaxRetries=1 means the initial attempt plus one retry, both throttled, so
	// the learner should have doubled twice: 600ms -> 1.2s -> 2.4s.
	cur, _ := lim.Current(limiter.BucketDocument)
	if cur != 2400*time.Millisecond {
		t.Errorf("after two throttled attempts: interval = %s, want 2.4s (client did not report the throttle to the learner)", cur)
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
