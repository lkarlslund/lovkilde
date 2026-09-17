// Package limiter provides fixed-interval pacing for outbound HTTP requests.
package limiter

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"
)

// Limiter paces calls per bucket so that no two calls in a bucket happen
// closer than that bucket's interval. A single shared limiter is used for the
// whole process, which makes the published "once per 10 seconds" harvesting
// limit a structural property rather than something each caller must remember.
type Limiter struct {
	mu       sync.Mutex
	interval map[string]time.Duration
	next     map[string]time.Time
	// rand is only read under mu.
	rand *rand.Rand
}

// New returns a limiter with the given per-bucket minimum intervals.
func New(intervals map[string]time.Duration) *Limiter {
	l := &Limiter{
		interval: make(map[string]time.Duration, len(intervals)),
		next:     make(map[string]time.Time, len(intervals)),
		rand:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	for name, d := range intervals {
		l.interval[name] = d
	}
	return l
}

// Bucket names used by the mirror.
const (
	BucketHarvest  = "harvest"
	BucketDocument = "document"
	BucketSitemap  = "sitemap"
)

// Wait blocks until a call in bucket may proceed, then reserves the slot.
// Jitter is added to the reservation so repeated runs do not produce a
// machine-regular request pattern.
func (l *Limiter) Wait(ctx context.Context, bucket string) error {
	l.mu.Lock()
	interval, ok := l.interval[bucket]
	if !ok {
		l.mu.Unlock()
		return errors.New("limiter: unknown bucket " + bucket)
	}
	now := time.Now()
	earliest := l.next[bucket]
	if earliest.Before(now) {
		earliest = now
	} else {
		// Add a little jitter to the tail of the wait, up to 20% of the interval.
		jitter := time.Duration(l.rand.Float64() * 0.2 * float64(interval))
		earliest = earliest.Add(jitter)
	}
	l.next[bucket] = earliest.Add(interval)
	l.mu.Unlock()

	timer := time.NewTimer(time.Until(earliest))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
