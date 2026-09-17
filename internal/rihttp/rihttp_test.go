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
	cfg.DocumentInterval = config.Duration{Duration: 200 * time.Millisecond}
	cfg.HarvestInterval = config.Duration{Duration: 10 * time.Second}
	cfg.BackoffBase = config.Duration{Duration: 20 * time.Millisecond}
	cfg.BackoffMax = config.Duration{Duration: 80 * time.Millisecond}
	cfg.MaxRetries = 3
	if tune != nil {
		tune(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	lim := limiter.New(map[string]time.Duration{
		limiter.BucketDocument: cfg.DocumentInterval.D(),
		limiter.BucketHarvest:  cfg.HarvestInterval.D(),
		limiter.BucketSitemap:  cfg.DocumentInterval.D(),
	})
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
