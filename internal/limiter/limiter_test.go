package limiter

import (
	"context"
	"testing"
	"time"
)

func specs(start, floor, max time.Duration, pinned bool) map[string]Spec {
	return map[string]Spec{
		BucketDocument: {Start: start, Floor: floor, Max: max, Pinned: pinned},
		BucketHarvest:  {Start: 10 * time.Second, Floor: 10 * time.Second, Max: time.Minute, Pinned: true},
	}
}

// The published harvesting limit is one call per 10 seconds and the service
// answers 429 beyond it, so the pacing guarantee needs a direct test.
func TestWaitEnforcesMinimumInterval(t *testing.T) {
	const interval = 150 * time.Millisecond
	l := NewTuned(map[string]Spec{BucketDocument: {Start: interval, Floor: interval, Max: interval * 4}}, 1.5, 20, time.Minute)
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < 4; i++ {
		if err := l.Wait(ctx, BucketDocument); err != nil {
			t.Fatalf("Wait %d: %v", i, err)
		}
	}
	// Four calls reserve slots at 0, 1x, 2x, 3x intervals. Jitter only delays.
	if elapsed, want := time.Since(start), 3*interval; elapsed < want {
		t.Errorf("4 calls took %s, want >= %s", elapsed, want)
	}
}

// A clean window of successes should make the crawl faster. This is what turns a
// 2.8 day bootstrap into hours when the service tolerates it.
func TestAdaptiveStepsDownAfterCleanWindow(t *testing.T) {
	const start, floor = 1200 * time.Millisecond, 200 * time.Millisecond
	l := NewTuned(specs(start, floor, 30*time.Second, false), 1.5, 5, 0)

	// Fewer successes than the window must not change anything.
	for i := 0; i < 4; i++ {
		l.Feedback(BucketDocument, OutcomeOK)
	}
	if cur, _ := l.Current(BucketDocument); cur != start {
		t.Fatalf("interval changed after %d of 5 successes: %s", 4, cur)
	}

	l.Feedback(BucketDocument, OutcomeOK) // completes the window
	want := time.Duration(float64(start) / 1.5)
	cur, _ := l.Current(BucketDocument)
	if cur != want {
		t.Fatalf("after window: interval = %s, want %s", cur, want)
	}
	if l.Steps() != 1 {
		t.Errorf("Steps = %d, want 1", l.Steps())
	}
}

// Repeated clean windows must converge on the floor and then stop.
func TestAdaptiveConvergesOnFloor(t *testing.T) {
	const floor = 200 * time.Millisecond
	l := NewTuned(specs(3*time.Second, floor, time.Minute, false), 1.5, 2, 0)
	for i := 0; i < 40; i++ {
		l.Feedback(BucketDocument, OutcomeOK)
		l.Feedback(BucketDocument, OutcomeOK)
	}
	cur, _ := l.Current(BucketDocument)
	if cur != floor {
		t.Errorf("interval = %s after long clean run, want floor %s", cur, floor)
	}

	// Further successes must not push below the floor.
	for i := 0; i < 100; i++ {
		l.Feedback(BucketDocument, OutcomeOK)
	}
	if cur, _ := l.Current(BucketDocument); cur != floor {
		t.Errorf("interval fell below floor: %s", cur)
	}
}

// Throttling must produce an immediate multiplicative slowdown.
func TestThrottleDoublesInterval(t *testing.T) {
	l := NewTuned(specs(1200*time.Millisecond, 200*time.Millisecond, 30*time.Second, false), 1.5, 5, 0)
	l.Feedback(BucketDocument, OutcomeThrottled)
	cur, _ := l.Current(BucketDocument)
	if cur != 2400*time.Millisecond {
		t.Errorf("after throttle: interval = %s, want 2.4s", cur)
	}

	l.Feedback(BucketDocument, OutcomeThrottled)
	cur, _ = l.Current(BucketDocument)
	if cur != 4800*time.Millisecond {
		t.Errorf("after second throttle: interval = %s, want 4.8s", cur)
	}
}

// Growth must be capped so a bad patch cannot park a multi-day crawl.
func TestThrottleGrowthIsCapped(t *testing.T) {
	const max = 5 * time.Second
	l := NewTuned(specs(1200*time.Millisecond, 200*time.Millisecond, max, false), 1.5, 5, 0)
	for i := 0; i < 10; i++ {
		l.Feedback(BucketDocument, OutcomeThrottled)
	}
	if cur, _ := l.Current(BucketDocument); cur != max {
		t.Errorf("interval = %s, want capped at %s", cur, max)
	}
}

// A pinned bucket represents the published 10s obligation. Observations, good or
// bad, must never move it.
func TestPinnedBucketNeverAdapts(t *testing.T) {
	l := NewTuned(specs(time.Second, 200*time.Millisecond, time.Hour, false), 1.5, 2, 0)
	for i := 0; i < 50; i++ {
		l.Feedback(BucketHarvest, OutcomeOK)
		l.Feedback(BucketHarvest, OutcomeThrottled)
	}
	cur, _ := l.Current(BucketHarvest)
	if cur != 10*time.Second {
		t.Errorf("pinned harvest interval moved to %s; must stay at 10s", cur)
	}
	if l.Steps() != 0 {
		t.Errorf("pinned bucket caused %d interval changes", l.Steps())
	}
}

// After being throttled, the learner must hold the safe interval for a cooldown
// period rather than immediately probing faster and provoking another 429.
func TestCooldownSuppressesStepDownAfterThrottle(t *testing.T) {
	l := NewTuned(specs(1200*time.Millisecond, 200*time.Millisecond, 30*time.Second, false), 1.5, 3, time.Hour)
	l.Feedback(BucketDocument, OutcomeThrottled) // 1.2s -> 2.4s, marks throttledAt

	for i := 0; i < 20; i++ {
		l.Feedback(BucketDocument, OutcomeOK)
	}
	cur, _ := l.Current(BucketDocument)
	if cur != 2400*time.Millisecond {
		t.Errorf("stepped down during cooldown: interval = %s, want 2.4s", cur)
	}
}

// Neutral outcomes (transport errors, 5xx) must break the streak so learning only
// happens across genuinely clean windows. Without the reset, OK OK would complete
// the window of 3 that the two pre-neutral successes had already started.
func TestNeutralOutcomeBreaksStreak(t *testing.T) {
	l := NewTuned(specs(1200*time.Millisecond, 200*time.Millisecond, 30*time.Second, false), 1.5, 3, 0)
	l.Feedback(BucketDocument, OutcomeOK)
	l.Feedback(BucketDocument, OutcomeOK) // streak is 2 of 3
	l.Feedback(BucketDocument, OutcomeNeutral)
	l.Feedback(BucketDocument, OutcomeOK)
	l.Feedback(BucketDocument, OutcomeOK) // streak restarts: only 2 of 3 now
	if cur, _ := l.Current(BucketDocument); cur != 1200*time.Millisecond {
		t.Errorf("interval = %s, want unchanged 1.2s (neutral should have reset the streak)", cur)
	}

	// One more success completes the *post-neutral* window and may step down.
	l.Feedback(BucketDocument, OutcomeOK)
	if cur, _ := l.Current(BucketDocument); cur == 1200*time.Millisecond {
		t.Error("interval never stepped down after a full clean window")
	}
}

func TestBucketsAreIndependent(t *testing.T) {
	l := NewTuned(map[string]Spec{
		BucketDocument: {Start: 10 * time.Millisecond, Floor: 10 * time.Millisecond, Max: time.Second},
		BucketHarvest:  {Start: 10 * time.Second, Floor: 10 * time.Second, Max: time.Minute, Pinned: true},
	}, 1.5, 20, time.Minute)
	ctx := context.Background()
	if err := l.Wait(ctx, BucketHarvest); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := l.Wait(ctx, BucketDocument); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("document bucket took %s; it is being held hostage by harvest", elapsed)
	}
}

func TestUnknownBucketFails(t *testing.T) {
	l := NewTuned(map[string]Spec{BucketHarvest: {Start: time.Millisecond, Floor: time.Millisecond, Max: time.Second}}, 1.5, 20, time.Minute)
	if err := l.Wait(context.Background(), "nonsense"); err == nil {
		t.Error("unknown bucket should error rather than run unpaced")
	}
	// Feedback on an unknown bucket must be ignored rather than panic.
	l.Feedback("nonsense", OutcomeOK)
}

func TestWaitHonoursContextCancellation(t *testing.T) {
	l := NewTuned(map[string]Spec{BucketHarvest: {Start: 10 * time.Second, Floor: 10 * time.Second, Max: time.Minute, Pinned: true}}, 1.5, 20, time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := l.Wait(ctx, BucketHarvest); err != nil { // consumes the free slot
		t.Fatal(err)
	}
	if err := l.Wait(ctx, BucketHarvest); err != context.DeadlineExceeded {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
}

func TestStatesSnapshot(t *testing.T) {
	l := NewTuned(specs(1200*time.Millisecond, 200*time.Millisecond, 30*time.Second, false), 1.5, 20, time.Minute)
	states := l.States()
	if len(states) != 2 {
		t.Fatalf("States() returned %d entries, want 2", len(states))
	}
	for _, s := range states {
		if s.Name == BucketHarvest && !s.Pinned {
			t.Error("harvest bucket should be pinned")
		}
		if s.Name == BucketDocument && s.Pinned {
			t.Error("document bucket should be adaptive")
		}
	}
}

// Defaults must protect against nonsense configuration.
func TestColdBucketStartsAtTheFloor(t *testing.T) {
	// The production shape: no Start, so the learner opens at the pace it believes
	// is safe rather than crawling up to it.
	l := New(map[string]Spec{
		BucketDocument: {Floor: 200 * time.Millisecond, Max: MaxInterval},
	})
	cur, ok := l.Current(BucketDocument)
	if !ok {
		t.Fatal("bucket missing")
	}
	if cur != 200*time.Millisecond {
		t.Errorf("cold interval = %s, want the 200ms floor", cur)
	}

	// Recovery still works: one throttle backs off.
	l.Feedback(BucketDocument, OutcomeThrottled)
	if cur, _ := l.Current(BucketDocument); cur != 400*time.Millisecond {
		t.Fatalf("after throttle: interval = %s, want 400ms", cur)
	}
}

func TestNewNormalizesSaneSpecs(t *testing.T) {
	l := NewTuned(map[string]Spec{
		// Floor above start, and max below start: both must be clamped, not
		// inverted, or the limiter would pace backwards.
		BucketDocument: {Start: time.Second, Floor: time.Hour, Max: 0},
		// Neither floor nor start: it must not default to no gap at all.
		BucketSitemap: {},
	}, 0, 0, 0)
	cur, ok := l.Current(BucketDocument)
	if !ok {
		t.Fatal("bucket missing")
	}
	if cur <= 0 {
		t.Errorf("interval = %s, want a positive default", cur)
	}
	if cur > time.Hour {
		t.Errorf("interval = %s, want clamped start", cur)
	}
	if cur, _ := l.Current(BucketSitemap); cur <= 0 {
		t.Errorf("unset bucket paces at %s, want a positive interval", cur)
	}
}

// Retry-After is honoured as pacing rather than a sleeping request, so the wait
// protects the whole bucket instead of one call.
func TestDelayPacesTheWholeBucket(t *testing.T) {
	l := NewTuned(map[string]Spec{BucketDocument: {Start: time.Millisecond, Floor: time.Millisecond, Max: time.Minute}}, 1.5, 20, time.Minute)
	if err := l.Wait(context.Background(), BucketDocument); err != nil {
		t.Fatal(err)
	}
	applied := l.Delay(BucketDocument, 150*time.Millisecond)
	start := time.Now()
	if err := l.Wait(context.Background(), BucketDocument); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("second Wait returned after %s, want roughly the %s delay", elapsed, applied)
	}
}

func TestDelayBounds(t *testing.T) {
	l := NewTuned(map[string]Spec{BucketDocument: {Start: time.Millisecond, Floor: time.Millisecond, Max: time.Minute}}, 1.5, 20, time.Minute)
	if got := l.Delay(BucketDocument, 0); got != 0 {
		t.Errorf("zero delay applied %s, want 0", got)
	}
	// A server asking for a day off must not stall a multi-day crawl indefinitely.
	if got := l.Delay(BucketDocument, 24*time.Hour); got != MaxDelay {
		t.Errorf("applied = %s, want the %s ceiling", got, MaxDelay)
	}
}
