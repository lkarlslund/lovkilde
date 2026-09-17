package limiter

import (
	"context"
	"testing"
	"time"
)

// The published harvesting limit (one call per 10 seconds) is enforced here, so
// its pacing guarantee needs a direct test.
func TestWaitEnforcesMinimumInterval(t *testing.T) {
	const interval = 120 * time.Millisecond
	l := New(map[string]time.Duration{BucketHarvest: interval})
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < 4; i++ {
		if err := l.Wait(ctx, BucketHarvest); err != nil {
			t.Fatalf("Wait %d: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	// Four calls reserve slots at 0, 1x, 2x, 3x intervals, so the last one lands
	// at >= 3*interval. Jitter only ever delays, never advances, a slot.
	if min := 3 * interval; elapsed < min {
		t.Errorf("4 calls took %s, want >= %s", elapsed, min)
	}
}

func TestBucketsAreIndependent(t *testing.T) {
	l := New(map[string]time.Duration{
		BucketHarvest:  time.Second,
		BucketDocument: 5 * time.Millisecond,
	})
	ctx := context.Background()
	if err := l.Wait(ctx, BucketHarvest); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	// The document bucket must not be held hostage by the harvest bucket.
	if err := l.Wait(ctx, BucketDocument); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("independent bucket took %s; buckets are not isolated", elapsed)
	}
}

func TestUnknownBucketFails(t *testing.T) {
	l := New(map[string]time.Duration{BucketHarvest: time.Millisecond})
	if err := l.Wait(context.Background(), "nonsense"); err == nil {
		t.Error("unknown bucket should return an error rather than run unpaced")
	}
}

func TestWaitHonoursContextCancellation(t *testing.T) {
	l := New(map[string]time.Duration{BucketHarvest: 10 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	// First call consumes the free slot, so the second must actually wait.
	if err := l.Wait(ctx, BucketHarvest); err != nil {
		t.Fatal(err)
	}
	err := l.Wait(ctx, BucketHarvest)
	if err == nil {
		t.Fatal("Wait should have been cancelled")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
}
