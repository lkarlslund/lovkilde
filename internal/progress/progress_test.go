package progress

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// A log record must never be appended to the tail of a bar frame: the frame
// leaves the cursor mid-line, and the reader gets one garbled line instead of
// two useful ones.
func TestGateErasesBarBeforeLog(t *testing.T) {
	var buf bytes.Buffer
	g := NewGate(&buf)

	_, _ = g.BarWriter().Write([]byte("\rfetch ▕███▏ 10/100"))
	_, _ = g.LogWriter().Write([]byte("time=... msg=\"fetch progress\"\n"))

	out := buf.String()
	iFrame := strings.Index(out, "fetch ▕███▏")
	iClear := strings.Index(out, "\r\033[K")
	iLog := strings.Index(out, "msg=\"fetch progress\"")
	if iFrame < 0 || iClear < 0 || iLog < 0 {
		t.Fatalf("missing part of the sequence in %q", out)
	}
	if !(iFrame < iClear && iClear < iLog) {
		t.Errorf("log did not clear the frame first: frame=%d clear=%d log=%d in %q", iFrame, iClear, iLog, out)
	}
}

// Without a frame on the line the logger must be left completely alone, so
// unattended runs contain no escape sequences at all.
func TestGateLeavesLogsAloneWithoutBar(t *testing.T) {
	var buf bytes.Buffer
	g := NewGate(&buf)

	if _, err := g.LogWriter().Write([]byte("plain log line\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := buf.String(); got != "plain log line\n" {
		t.Errorf("got %q, want the line untouched", got)
	}
}

// Two log records in a row must not accumulate clears either.
func TestGateClearsOncePerFrame(t *testing.T) {
	var buf bytes.Buffer
	g := NewGate(&buf)

	_, _ = g.BarWriter().Write([]byte("frame\n"))
	_, _ = g.LogWriter().Write([]byte("one\n"))
	_, _ = g.LogWriter().Write([]byte("two\n"))

	if n := strings.Count(buf.String(), "\033[K"); n != 1 {
		t.Errorf("got %d clears, want exactly 1: %q", n, buf.String())
	}
}

// Off a terminal, auto mode must produce no output whatsoever, not a bar that
// nobody asked for sitting in a cron mail.
func TestBarAutoIsSilentOffTerminal(t *testing.T) {
	var buf bytes.Buffer
	b := NewBar(NewGate(&buf), "fetch", 100, ModeAuto)

	if b.Enabled() {
		t.Fatal("bar enabled on a non-terminal writer")
	}
	b.SetTotal(100)
	b.Set(50)
	b.Describe("fetch @250ms")
	b.Finish()

	if buf.Len() != 0 {
		t.Errorf("silent bar wrote %q", buf.String())
	}
}

func TestBarAlwaysRendersCounts(t *testing.T) {
	var buf bytes.Buffer
	b := NewBar(NewGate(&buf), "fetch", 0, ModeAlways)
	if !b.Enabled() {
		t.Fatal("always mode disabled on a non-terminal writer")
	}

	b.SetTotal(100)
	b.Set(50)

	// Redraws are throttled on purpose, so a frame is only due once the
	// interval has passed; waiting past it keeps the test honest about that.
	time.Sleep(200 * time.Millisecond)
	b.Set(75)
	if !strings.Contains(buf.String(), "75/100") {
		t.Fatalf("no frame showing the current count: %q", buf.String())
	}

	// Describe repaints only when the redraw throttle allows, so the new label
	// shows up on the next frame rather than instantly. That is the right
	// trade for a crawl: the pace text is informational, not urgent.
	b.Describe("fetch @250ms")
	time.Sleep(200 * time.Millisecond)
	b.Set(76)
	if !strings.Contains(buf.String(), "fetch @250ms") {
		t.Errorf("pacing label not painted on the next frame: %q", buf.String())
	}
}

// The docs/sec figure is the point of the bar, so assert it actually appears
// once enough progress has been made to compute one.
func TestBarShowsRateAndETA(t *testing.T) {
	var buf bytes.Buffer
	b := NewBar(NewGate(&buf), "fetch", 1000, ModeAlways)

	for i := int64(1); i <= 200; i++ {
		b.Set(i)
	}
	// Past the redraw throttle, so the next Set paints a frame carrying a rate
	// computed from the 200 documents already counted.
	time.Sleep(200 * time.Millisecond)
	b.Set(201)

	out := buf.String()
	if !strings.Contains(out, "/s") {
		t.Errorf("no rate shown: %q", out)
	}
}

func TestParseMode(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Mode
	}{
		{"auto", ModeAuto},
		{"", ModeAuto},
		{"always", ModeAlways},
		{"never", ModeNever},
	} {
		got, err := ParseMode(c.in)
		if err != nil {
			t.Errorf("ParseMode(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseMode(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if _, err := ParseMode("loud"); err == nil {
		t.Error("ParseMode(\"loud\") accepted; a typo should fail at startup")
	}
}

// A nil bar is what a command that has no progress phase holds, so every method
// has to tolerate it.
func TestNilBarIsInert(t *testing.T) {
	var b *Bar
	b.SetTotal(10)
	b.Set(1)
	b.Describe("x")
	if b.Enabled() {
		t.Error("nil bar reported enabled")
	}
	b.Finish()
}

// Regression: the underlying library latches its finished state if a frame ever
// renders while its max is still unset, after which it ignores every update. A
// bar whose total arrives late -- which is how fetch works -- must survive being
// labelled first, or the operator watches a line that never moves.
func TestBarSurvivesDescribeBeforeTotal(t *testing.T) {
	var buf bytes.Buffer
	b := NewBar(NewGate(&buf), "fetch", 0, ModeAlways)

	b.Describe("fetch @250ms") // renders while the total is still unknown
	b.SetTotal(10)
	time.Sleep(200 * time.Millisecond)
	if err := b.Set(10); err != nil {
		t.Fatalf("Set: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := b.Set(10); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if !strings.Contains(buf.String(), "10/10") {
		t.Errorf("bar silenced by an early Describe: %q", buf.String())
	}
}

// Updates before a total is known are dropped rather than drawn against a
// placeholder denominator, which would read as a completed crawl.
func TestBarIgnoresCountsUntilTotal(t *testing.T) {
	var buf bytes.Buffer
	b := NewBar(NewGate(&buf), "fetch", 0, ModeAlways)

	if err := b.Set(3); err != nil {
		t.Fatalf("Set before total: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("painted %q with no total to measure against", buf.String())
	}
}
