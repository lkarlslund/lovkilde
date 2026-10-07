// Package config holds runtime settings. There is deliberately no config file
// and no environment-variable layer: every setting is a CLI flag, so what a run
// does is visible in the invocation that started it, and a stale file cannot
// silently change a multi-day crawl.
//
// Pacing is configurable by two numbers: DocumentFloor, the interval the learner
// starts at and returns to, and JitterPercent, how far each request may drift past
// that interval. The learner that approaches the floor is tuned by constants in
// internal/limiter, because every measured run behaved the same way and extra knobs
// would only offer more ways to misconfigure a crawl. The floor itself is a default
// rather than a limit — the one pace the terms actually impose (one harvest call per
// 10 s) is a constant with no flag at all.
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
	// DefaultJitterPercent is the share of the current interval added at random on
	// top of each paced wait. Kept equal to limiter.JitterPercentDefault, which the
	// limiter applies when nothing overrides it; a test asserts the two agree.
	DefaultJitterPercent = 10
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

	// JitterPercent is how much of the current interval may be added at random on
	// top of each paced wait, so a multi-day crawl does not run on a metronome. It
	// only ever delays a request, so DocumentFloor remains a hard minimum. Zero
	// gives exact spacing, which is the setting to choose when testing whether a
	// service's own limits are the thing being hit.
	JitterPercent float64

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
		JitterPercent:  DefaultJitterPercent,
		RequestTimeout: 60 * time.Second,
	}
}

// XMLDir returns the directory holding downloaded ELI XML documents.
func (c Config) XMLDir() string { return c.DataDir + "/xml" }

// StoreDir returns the directory holding the collection state (internal/store).
func (c Config) StoreDir() string { return c.DataDir + "/state" }

// SweepStampPath returns the file holding the timestamp of the last collection
// sweep that left nothing owed.
//
// The authority is a row in the state store. This file exists because the store is
// locked to one process: a reader cannot open it without locking out the nightly
// crawl, so the collection pass mirrors the one fact readers need into a plain file
// (and the artifact build copies it into the artifact's header). Same value,
// readable by anything -- and nothing else should be derived from it. If it ever
// disagrees with the store, the store is right.
func (c Config) SweepStampPath() string { return c.DataDir + "/sweep.stamp" }

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
	if c.JitterPercent < 0 || c.JitterPercent > 100 {
		// Refused rather than clamped: --jitter 500 is a typo, and silently
		// halving the wait it implies would hide the mistake.
		return fmt.Errorf("jitter percent must be between 0 (exact spacing) and 100 (up to one extra interval), got %g", c.JitterPercent)
	}
	return nil
}
