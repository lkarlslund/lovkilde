package checks

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for range 200 {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// A check runs when due, not when off, runs at once when asked whatever its interval, and its
// interval and outcome survive a restart.
func TestChecksRunWhenDueOrAskedAndRemember(t *testing.T) {
	dir := t.TempDir()
	var due, off atomic.Int32
	c := Open(dir)
	c.Add("due", Daily, time.Time{}, func(context.Context) Outcome { due.Add(1); return Outcome{Summary: "ok", Newer: 2} })
	c.Add("off", Off, time.Time{}, func(context.Context) Outcome { off.Add(1); return Outcome{} })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	waitFor(t, func() bool { s, _ := c.State("due"); return !s.Last.At.IsZero() })
	if due.Load() != 1 || off.Load() != 0 {
		t.Fatalf("ran due %d, off %d times", due.Load(), off.Load())
	}
	c.Now("off")
	waitFor(t, func() bool { return off.Load() == 1 })
	if err := c.Set("due", Weekly); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("due", "hourly"); err == nil {
		t.Error("an unknown interval was accepted")
	}

	again := Open(dir)
	again.Add("due", Daily, time.Time{}, func(context.Context) Outcome { t.Error("ran again after a restart"); return Outcome{} })
	if s, _ := again.State("due"); s.Every != Weekly || s.Last.Summary != "ok" || s.Last.Newer != 2 {
		t.Errorf("after a restart: %+v", s)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	go again.Run(ctx2)
	time.Sleep(20 * time.Millisecond)
	cancel2()
}
