// Package config holds runtime settings. There is deliberately no config file
// and no environment-variable layer: every setting is a CLI flag, so what a run
// does is visible in the invocation that started it, and a stale file cannot
// silently change a multi-day crawl.
//
// Pacing is not configurable beyond one number: DocumentFloor. The learner that
// approaches it is tuned by constants in internal/limiter, because every measured
// run behaved the same way and extra knobs would only offer more ways to
// misconfigure a crawl. The floor itself is a default rather than a limit — the
// one pace the terms actually impose (one harvest call per 10 s) is a constant
// with no flag at all.
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
	// DefaultDocumentFloor is where the document bucket is allowed to settle by
	// default. Document responses are served uncached straight from the origin, so
	// this is a judgement about a considerate pace, not a limit we enforce: an
	// operator who sets --document-floor lower has chosen that themselves, and the
	// adaptive learner will pursue it. The service answers a real 429 if the choice
	// was wrong, and the learner slows down on its own when that happens.
	DefaultDocumentFloor = 200 * time.Millisecond
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
	// It defaults to DefaultDocumentFloor, but any positive value is honoured: the
	// pace is the operator's choice, and the service's own 429 is what says so if
	// the choice was too ambitious.
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
		DocumentFloor:  DefaultDocumentFloor,
		RequestTimeout: 60 * time.Second,
	}
}

// XMLDir returns the directory holding downloaded ELI XML documents.
func (c Config) XMLDir() string { return c.DataDir + "/xml" }

// StoreDir returns the directory holding the Badger state store.
func (c Config) StoreDir() string { return c.DataDir + "/meta/kv" }

// Validate refuses configurations that cannot mean anything. It deliberately does
// not police the document floor: how fast to ask for uncached documents is the
// operator's call, and the service itself answers 429 if the answer was wrong.
func (c Config) Validate() error {
	if c.SiteBase == "" || c.HarvestBase == "" {
		return fmt.Errorf("site_base and harvest_base must be set")
	}
	if c.DocumentFloor <= 0 {
		return fmt.Errorf("document floor must be a positive duration; zero would leave documents unpaced")
	}
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("request timeout must be positive")
	}
	return nil
}
