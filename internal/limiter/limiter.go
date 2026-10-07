// Package limiter paces outbound HTTP requests per bucket, and optionally
// learns the interval the remote service will actually tolerate.
//
// Two bucket flavours exist, and the difference matters:
//
//   - Pinned buckets never change. The harvesting service publishes a hard limit
//     ("once per 10 seconds") and answers 429 beyond it, so there is nothing to
//     learn and no room to be more aggressive. A pinned bucket encodes an
//     obligation, not an observation.
//   - Adaptive buckets self-tune. retsinformation.dk sends no Retry-After and no
//     rate-limit header on 429 (measured), so the only way to discover a safe
//     document interval is to probe. Adaptive buckets start at their floor -- the
//     pace believed safe is the pace used -- double on pushback, and step back
//     down once a whole window succeeds again.
//
// A process-global limiter is used so the published 10s limit is a structural
// property rather than something each caller has to remember.
package limiter

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"
)

// Bucket names used by the mirror.
const (
	BucketHarvest  = "harvest"
	BucketDocument = "document"
	BucketSitemap  = "sitemap"
)

// Outcome classifies how a request finished. It is the learner's only input.
type Outcome int

const (
	// OutcomeOK means the server processed the request. Both successful 2xx and
	// permanent 4xx (404, 410) count: the server was responsive, and a missing
	// document is not a capacity signal.
	OutcomeOK Outcome = iota
	// OutcomeThrottled means the server pushed back (429/503/504). This is the
	// only signal that makes an adaptive bucket slow down.
	OutcomeThrottled
	// OutcomeNeutral covers transport failures and server-side 5xx faults. They
	// say nothing reliable about pacing, so they neither speed us up nor slow us
	// down, but they break a success streak so learning happens only on clean
	// windows.
	OutcomeNeutral
)

// Spec configures one bucket.
type Spec struct {
	// Start is the interval a cold bucket begins with. Zero means start at the
	// floor, which is what production uses; tests set a higher value to exercise
	// the step-down path on millisecond scales.
	Start time.Duration
	// Floor is the fastest interval an adaptive bucket may use, and the one it
	// starts at unless Start says otherwise. Ignored by pinned buckets.
	Floor time.Duration
	// Max caps growth after throttling, so one bad patch cannot park a multi-day
	// crawl for hours.
	Max time.Duration
	// Pinned freezes the interval at Start.
	Pinned bool
}

// bucket is one bucket's adaptive state. Guarded by Limiter.mu.
type bucket struct {
	spec        Spec
	cur         time.Duration
	consec      int // consecutive OK outcomes at the current interval
	scheduled   time.Time
	throttledAt time.Time
}

// Limiter paces calls across a set of buckets.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rand    *rand.Rand

	stepDown float64 // interval *= 1/stepDown once a window succeeds
	window   int     // consecutive OK requests required to step down
	cooldown time.Duration
	jitter   float64 // fraction of the interval added at random to a reserved slot

	steps int // interval changes made, for logging and tests
}

// Pacing constants for the adaptive learner. Only the floor is user-settable:
// these shape how the learner behaves, so they are observations rather than
// options.
const (
	// MaxInterval caps growth after throttling, so one bad patch cannot park a
	// multi-day crawl for hours.
	MaxInterval = 5 * time.Minute
	// Window is consecutive successful requests required to step back down
	// towards the floor after a throttle.
	Window = 20
	// StepDown is the factor each recovery step divides by. Measured behaviour:
	// after a throttle at the floor, 400ms returns to 200ms in two steps.
	StepDown = 1.5
	// Cooldown suppresses probing for this long after a throttle.
	Cooldown = 10 * time.Minute
	// JitterPercentDefault is the share of the current interval added at random on
	// top of each reserved slot, so a multi-day crawl does not present a
	// machine-regular request signature. It only ever delays a slot, never
	// advances one, so the minimum interval stays a guarantee.
	JitterPercentDefault = 10
	// MaxDelay caps how far a server's Retry-After may push a bucket forward.
	// Longer guidance is logged and clamped so one header cannot stall a crawl.
	MaxDelay = time.Hour
)

// defaultFloor is used only when a caller supplies neither a floor nor a start.
// A zero interval would mean firing requests with no gap at all, which is not a
// pacing configuration anyone means.
const defaultFloor = time.Second

// New builds a limiter with the production pacing constants.
func New(specs map[string]Spec) *Limiter {
	return NewTuned(specs, StepDown, Window, Cooldown)
}

// NewTuned builds a limiter with an explicit learner shape. Tests use it to run
// the same state machine on millisecond scales; production uses New.
func NewTuned(specs map[string]Spec, stepDown float64, window int, cooldown time.Duration) *Limiter {
	if stepDown <= 1 {
		stepDown = 1.5
	}
	if window < 2 {
		window = 20
	}
	l := &Limiter{
		buckets:  make(map[string]*bucket, len(specs)),
		rand:     rand.New(rand.NewSource(time.Now().UnixNano())),
		stepDown: stepDown,
		window:   window,
		cooldown: cooldown,
		jitter:   float64(JitterPercentDefault) / 100,
	}
	for name, spec := range specs {
		if spec.Floor <= 0 {
			spec.Floor = defaultFloor
		}
		if spec.Start <= 0 {
			// Start at the floor: the assumed-safe pace is the target pace, and
			// the learner's job is to back off when that turns out to be wrong.
			spec.Start = spec.Floor
		}
		if spec.Max < spec.Start {
			spec.Max = spec.Start
		}
		if spec.Floor > spec.Start {
			spec.Floor = spec.Start
		}
		l.buckets[name] = &bucket{spec: spec, cur: spec.Start}
	}
	return l
}

// SetJitterPercent sets how much of the current interval may be added at random
// on top of each reserved slot. Zero makes requests perfectly regular: kinder to
// a rate-limited service, but it gives a multi-day crawl a machine-regular
// signature. Above 100 it is clamped, because a wait past twice the interval is a
// pacing choice better made with a larger floor. Call it at startup, before the
// first request.
func (l *Limiter) SetJitterPercent(percent float64) {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	l.mu.Lock()
	l.jitter = percent / 100
	l.mu.Unlock()
}

// JitterPercent reports the jitter in force, as a percentage of the interval.
func (l *Limiter) JitterPercent() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.jitter * 100
}

// Wait blocks until a call in the named bucket may proceed, then reserves the
// slot. Jitter is added so a long crawl does not present a machine-regular
// request signature.
func (l *Limiter) Wait(ctx context.Context, name string) error {
	l.mu.Lock()
	b, ok := l.buckets[name]
	if !ok {
		l.mu.Unlock()
		return errors.New("limiter: unknown bucket " + name)
	}
	now := time.Now()
	reserved := now
	if b.scheduled.After(now) {
		// The reservation is respected whether or not jitter is on: dropping it
		// here would let an exact-spacing run ignore both the interval and any
		// Retry-After already applied to the bucket.
		reserved = b.scheduled
		if l.jitter > 0 {
			// Jitter only ever delays a reserved slot, never advances it, so the
			// minimum interval remains a hard guarantee.
			reserved = reserved.Add(time.Duration(l.rand.Float64() * l.jitter * float64(b.cur)))
		}
	}
	b.scheduled = reserved.Add(b.cur)
	l.mu.Unlock()

	timer := time.NewTimer(time.Until(reserved))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Delay pushes a bucket's next slot out by d without sleeping here. It is how a
// server's Retry-After is honoured: the wait becomes pacing for the whole bucket
// instead of a stalled request holding a crawl open. It returns the delay
// actually applied, which is less than d when MaxDelay clamped it.
func (l *Limiter) Delay(name string, d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d > MaxDelay {
		d = MaxDelay
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.buckets[name]; ok {
		if until := time.Now().Add(d); b.scheduled.Before(until) {
			b.scheduled = until
		}
	}
	return d
}

// Feedback reports how a request finished and adapts the bucket. Throttled
// requests double the interval; a clean window after the cooldown divides it by
// StepDown, working back towards the floor. Pinned buckets ignore feedback.
func (l *Limiter) Feedback(name string, out Outcome) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[name]
	if !ok {
		return
	}
	switch out {
	case OutcomeThrottled:
		b.consec = 0
		b.throttledAt = time.Now()
		if b.spec.Pinned {
			return // an obligation is not adjusted by observation
		}
		grown := b.cur * 2
		if grown > b.spec.Max {
			grown = b.spec.Max
		}
		if grown != b.cur {
			b.cur = grown
			l.steps++
		}

	case OutcomeNeutral:
		b.consec = 0

	case OutcomeOK:
		if b.spec.Pinned || b.cur <= b.spec.Floor {
			b.consec = 0
			return
		}
		b.consec++
		if b.consec < l.window {
			return
		}
		// Hold the interval that just worked for a while before risking a faster
		// one again.
		if !b.throttledAt.IsZero() && time.Since(b.throttledAt) < l.cooldown {
			return
		}
		next := time.Duration(float64(b.cur) / l.stepDown)
		if next < b.spec.Floor {
			next = b.spec.Floor
		}
		if next < b.cur {
			b.cur = next
			l.steps++
		}
		b.consec = 0
	}
}

// Current reports the interval a bucket is currently enforcing.
func (l *Limiter) Current(name string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[name]
	if !ok {
		return 0, false
	}
	return b.cur, true
}

// Steps is the number of interval changes the learner has made.
func (l *Limiter) Steps() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.steps
}

// States snapshots every bucket, for run-start logging and the status command.
func (l *Limiter) States() []State {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]State, 0, len(l.buckets))
	for name, b := range l.buckets {
		out = append(out, State{Name: name, Interval: b.cur, Floor: b.spec.Floor, Pinned: b.spec.Pinned})
	}
	return out
}

// State is a snapshot of one bucket, for logging and status output.
type State struct {
	Name     string
	Interval time.Duration
	Floor    time.Duration
	Pinned   bool
}
