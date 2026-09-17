// Package config holds runtime settings. There is deliberately no config file
// and no environment-variable layer: every setting is a CLI flag, so what a run
// does is visible in the invocation that started it, and a stale file cannot
// silently change a multi-day crawl.
//
// Pacing is not configurable beyond one number: DocumentFloor. The learner that
// approaches it is tuned by constants in internal/limiter, because every
// measured run behaved the same way and extra knobs only invite a configuration
// that breaches the terms. What the terms do constrain is a floor, so that is
// the only pacing flag.
package config

import (
	"fmt"
	"time"
)

// DefaultUserAgent identifies the client to the server. Keep a contact address
// here: the terms ask users not to burden the service, and an identifiable client
// lets the operator reach us instead of banning our address.
const DefaultUserAgent = "retsin-mirror/0.1 (open-data mirror; contact: https://github.com/lkarlslund/retsinformation)"

// Limits that are obligations or measurements, not options. They are constants
// so they cannot be misconfigured from a shell history.
const (
	// HarvestLimit is the harvesting service's own documented ceiling: it may be
	// called at most once per 10 seconds and answers HTTP 429 beyond that.
	HarvestLimit = 10 * time.Second
	// HarvestInterval is the pace we actually keep: the published limit plus a
	// margin for clock skew. There is no flag for it because there is no
	// configuration in which going faster is acceptable.
	HarvestInterval = 12 * time.Second
	// SitemapInterval paces the ~22 sitemap requests.
	SitemapInterval = 2 * time.Second
	// DocumentFloor is our own courtesy floor for document requests, and the
	// default. They are served uncached straight from the origin, and the service
	// publishes no limit for them, so we decline to probe below this.
	DocumentFloor = 200 * time.Millisecond
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	// DataDir holds the archive and the state store. The archive runs to several
	// gigabytes, so this is the flag that matters most.
	DataDir string

	// SiteBase serves the ELI channels and document API; HarvestBase is the
	// dedicated harvesting service. Both are overridable for tests.
	SiteBase    string
	HarvestBase string

	// UserAgent sent on every request.
	UserAgent string

	// DocumentFloor is the fastest the adaptive document bucket may settle at.
	DocumentFloor time.Duration

	// RequestTimeout bounds a single HTTP request.
	RequestTimeout time.Duration
}

// Default returns production-sane defaults built around the published limits.
func Default() Config {
	return Config{
		DataDir:        "data",
		SiteBase:       "https://www.retsinformation.dk",
		HarvestBase:    "https://api.retsinformation.dk",
		UserAgent:      DefaultUserAgent,
		DocumentFloor:  DocumentFloor,
		RequestTimeout: 60 * time.Second,
	}
}

// XMLDir returns the directory holding downloaded ELI XML documents.
func (c Config) XMLDir() string { return c.DataDir + "/xml" }

// StoreDir returns the directory holding the Badger state store.
func (c Config) StoreDir() string { return c.DataDir + "/meta/kv" }

// Validate refuses the configurations that a flag can still get wrong. The
// harvest obligation is not one of them: HarvestInterval is a constant at or
// above HarvestLimit by construction, asserted in config_test.go.
func (c Config) Validate() error {
	if c.SiteBase == "" || c.HarvestBase == "" {
		return fmt.Errorf("site_base and harvest_base must be set")
	}
	if c.DocumentFloor < DocumentFloor {
		return fmt.Errorf("document floor %s is below the %s courtesy minimum: document responses are served uncached straight from the origin", c.DocumentFloor, DocumentFloor)
	}
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("request timeout must be positive")
	}
	return nil
}
