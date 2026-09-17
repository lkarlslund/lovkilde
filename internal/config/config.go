// Package config holds runtime settings. There is deliberately no config file
// and no environment-variable layer: every setting is a CLI flag, so what a run
// does is visible in the invocation that started it, and a stale file cannot
// silently change a multi-day crawl.
//
// The important part is not the fields, it is Validate: it encodes the limits
// Civilstyrelsen publishes as machine checks, so a mistyped interval fails at
// startup instead of hammering a government service for three days and getting
// the operator's IP blocked by Cloudflare.
package config

import (
	"fmt"
	"time"
)

// DefaultUserAgent identifies the client to the server. Keep a contact address
// here: the terms ask users not to burden the service, and an identifiable client
// lets the operator reach us instead of banning our address.
const DefaultUserAgent = "retsin-mirror/0.1 (open-data mirror; contact: https://github.com/lkarlslund/retsinformation)"

// Published and enforced limits.
const (
	// HarvestLimit is the harvesting service's own documented ceiling: it may be
	// called at most once per 10 seconds and answers HTTP 429 beyond that.
	HarvestLimit = 10 * time.Second
	// DocumentFloor is our own courtesy floor for document requests. They are
	// served uncached straight from the origin, and the service publishes no
	// limit for them, so we decline to probe below this.
	DocumentFloor = 200 * time.Millisecond
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	// DataDir holds the archive and the state store. 22 GB will not fit on a
	// home partition, so this is the flag that matters most.
	DataDir string

	// SiteBase serves the ELI channels and document API; HarvestBase is the
	// dedicated harvesting service. Both are overridable for tests.
	SiteBase    string
	HarvestBase string

	// UserAgent sent on every request.
	UserAgent string

	// HarvestInterval must never be below HarvestLimit.
	HarvestInterval time.Duration

	// DocumentStart is where the adaptive document bucket begins. It steps down
	// towards DocumentFloor as long as requests keep succeeding.
	DocumentStart time.Duration
	// DocumentFloor is the fastest the adaptive bucket may settle at.
	DocumentFloor time.Duration
	// DocumentMax caps how far the bucket grows after being throttled.
	DocumentMax time.Duration

	// SitemapInterval paces the ~22 sitemap requests.
	SitemapInterval time.Duration

	// PinnedIntervals disables adaptive learning entirely, holding every bucket
	// at its configured value. Useful for reproducing a known-safe pace.
	PinnedIntervals bool

	// AdaptiveStepDown divides the interval after a clean window (e.g. 1.5).
	AdaptiveStepDown float64
	// AdaptiveWindow is consecutive successful requests required to step down.
	AdaptiveWindow int
	// AdaptiveCooldown suppresses step-down attempts this long after a throttle.
	AdaptiveCooldown time.Duration

	// MaxRetries bounds attempts per request after throttling or failure.
	MaxRetries int
	// BackoffBase doubles from here on each retry, capped at BackoffMax.
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// BackoffJitter randomises retry delays so retries do not synchronise.
	BackoffJitter float64

	// RequestTimeout bounds a single HTTP request.
	RequestTimeout time.Duration
}

// Default returns production-sane defaults built around the published limits.
func Default() Config {
	return Config{
		DataDir:          "data",
		SiteBase:         "https://www.retsinformation.dk",
		HarvestBase:      "https://api.retsinformation.dk",
		UserAgent:        DefaultUserAgent,
		HarvestInterval:  12 * time.Second, // margin over the 10s published limit
		DocumentStart:    1200 * time.Millisecond,
		DocumentFloor:    DocumentFloor,
		DocumentMax:      5 * time.Minute,
		SitemapInterval:  2 * time.Second,
		PinnedIntervals:  false,
		AdaptiveStepDown: 1.5,
		AdaptiveWindow:   20,
		AdaptiveCooldown: 10 * time.Minute,
		MaxRetries:       6,
		BackoffBase:      10 * time.Second,
		BackoffMax:       15 * time.Minute,
		BackoffJitter:    0.3,
		RequestTimeout:   60 * time.Second,
	}
}

// XMLDir returns the directory holding downloaded ELI XML documents.
func (c Config) XMLDir() string { return c.DataDir + "/xml" }

// StoreDir returns the directory holding the Badger state store.
func (c Config) StoreDir() string { return c.DataDir + "/meta/kv" }

// Validate refuses configurations that would breach the published terms, or that
// are internally inconsistent enough to pace backwards.
func (c Config) Validate() error {
	if c.SiteBase == "" || c.HarvestBase == "" {
		return fmt.Errorf("site_base and harvest_base must be set")
	}
	if c.HarvestInterval < HarvestLimit {
		return fmt.Errorf("harvest interval %s breaches the published limit: the harvesting service allows at most one call per %s and answers HTTP 429 beyond that", c.HarvestInterval, HarvestLimit)
	}
	if c.DocumentFloor < DocumentFloor {
		return fmt.Errorf("document floor %s is below the %s courtesy minimum: document responses are served uncached straight from the origin", c.DocumentFloor, DocumentFloor)
	}
	if c.DocumentStart < c.DocumentFloor {
		return fmt.Errorf("document start %s is below its own floor %s", c.DocumentStart, c.DocumentFloor)
	}
	if c.DocumentMax < c.DocumentStart {
		return fmt.Errorf("document max %s is below its start %s, which would make backoff speed the crawl up", c.DocumentMax, c.DocumentStart)
	}
	if c.SitemapInterval <= 0 {
		return fmt.Errorf("sitemap interval must be positive")
	}
	if c.AdaptiveStepDown <= 1 {
		return fmt.Errorf("adaptive step-down factor must be greater than 1 (e.g. 1.5)")
	}
	if c.AdaptiveWindow < 2 {
		return fmt.Errorf("adaptive window must be at least 2 requests")
	}
	if c.BackoffJitter < 0 || c.BackoffJitter > 1 {
		return fmt.Errorf("backoff jitter must be between 0 and 1")
	}
	if c.MaxRetries < 0 {
		return fmt.Errorf("max retries must not be negative")
	}
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("request timeout must be positive")
	}
	return nil
}
