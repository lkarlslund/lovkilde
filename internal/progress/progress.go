// Package progress renders live crawl progress.
//
// The bar itself is github.com/schollz/progressbar, which is listed in
// awesome-go and already does counts, docs/sec, ETA and throttled redraws.
// This package contributes only what that library does not cover, which is the
// thing that actually matters here: the bar and the structured log share one
// stream, and neither may begin writing in the middle of the other's line.
package progress

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/schollz/progressbar/v3"
)

// Gate serializes a progress bar and a logger writing to the same stream.
//
// A bar frame ends without a newline and leaves the cursor on its own line;
// a log record assumes it starts a fresh line. Without this, a log line is
// appended to the tail of a bar frame and both become unreadable.
type Gate struct {
	mu      sync.Mutex
	w       io.Writer
	barLive bool // a bar frame currently owns the line
}

// NewGate returns a gate over w, normally os.Stderr.
func NewGate(w io.Writer) *Gate {
	if w == nil {
		w = os.Stderr
	}
	return &Gate{w: w}
}

// Writer returns a writer for one source. Give the bar BarWriter() and the
// logger LogWriter(); sharing a single writer would lose the distinction this
// exists to enforce.
func (g *Gate) Writer(isLog bool) io.Writer { return gateWriter{g: g, isLog: isLog} }

// BarWriter is the sink for progress frames.
func (g *Gate) BarWriter() io.Writer { return g.Writer(false) }

// LogWriter is the sink for log records.
func (g *Gate) LogWriter() io.Writer { return g.Writer(true) }

// IsTerminal reports whether g writes to an interactive terminal. The bar uses
// this to switch itself off, so cron jobs and redirected runs get plain logs
// with no escape sequences and no redraw cost.
func (g *Gate) IsTerminal() bool {
	f, ok := g.w.(*os.File)
	if !ok {
		return false
	}
	if term := os.Getenv("TERM"); term == "" || term == "dumb" {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

type gateWriter struct {
	g     *Gate
	isLog bool
}

func (w gateWriter) Write(p []byte) (int, error) {
	g := w.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if w.isLog {
		if g.barLive {
			// Erase the frame the bar left on this line before logging. The
			// bar redraws on its next tick, one line down.
			io.WriteString(g.w, "\r\033[K")
			g.barLive = false
		}
	} else {
		g.barLive = true
	}
	return g.w.Write(p)
}

// Mode selects when the bar renders.
type Mode int

const (
	// ModeAuto renders only on a terminal, so cron and redirected runs get
	// plain logs with no escape sequences.
	ModeAuto Mode = iota
	// ModeAlways renders even when the destination is not a terminal.
	ModeAlways
	// ModeNever disables the bar entirely.
	ModeNever
)

// ParseMode maps a --progress value onto a Mode.
func ParseMode(s string) (Mode, error) {
	switch s {
	case "auto", "":
		return ModeAuto, nil
	case "always":
		return ModeAlways, nil
	case "never":
		return ModeNever, nil
	default:
		return ModeAuto, fmt.Errorf("progress mode must be auto, always or never, not %q", s)
	}
}

// Bar is the fetch progress line: documents done, docs/sec, and ETA.
type Bar struct {
	pb      *progressbar.ProgressBar
	w       io.Writer
	enabled bool

	// totalUnknown guards a trap in the underlying library: rendering while its
	// max is still 0 evaluates "current >= max" as true, latches its finished
	// state, and silently ignores every later update -- a progress bar that
	// looks frozen. The library offers no way to un-latch short of Reset(), so
	// the wrapper simply never lets a frame render before a total is known.
	totalUnknown bool
}

// NewBar builds a bar over gate. total may be 0 and set later with SetTotal.
func NewBar(g *Gate, label string, total int64, mode Mode) *Bar {
	enabled := mode == ModeAlways || (mode == ModeAuto && g.IsTerminal())
	b := &Bar{enabled: enabled}
	// A zero max is what triggers the finish latch described on Bar, so an
	// unknown total is represented by a placeholder the first SetTotal replaces.
	construct := total
	if construct <= 0 {
		construct = 1
		b.totalUnknown = true
	}
	b.w = g.BarWriter()
	b.pb = progressbar.NewOptions64(construct,
		// Off unless we are on a terminal: "invisible" short-circuits before
		// any formatting, so an unattended run pays nothing for the bar.
		progressbar.OptionSetVisibility(enabled),
		progressbar.OptionSetWriter(g.BarWriter()),
		// The crawl emits one event per document, which at sub-second pacing
		// would flood the terminal and dominate the request budget.
		progressbar.OptionThrottle(150*time.Millisecond),
		progressbar.OptionSetDescription(label),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionSetItsString("docs"),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionSetElapsedTime(true),
		progressbar.OptionSetWidth(30),
	)
	return b
}

// Enabled reports whether the bar renders anything. A nil bar is inert, which is
// what the phases without a progress line hold.
func (b *Bar) Enabled() bool { return b != nil && b.enabled }

// SetTotal sets the denominator, e.g. the number of pending documents.
func (b *Bar) SetTotal(total int64) {
	if b == nil || b.pb == nil || total <= 0 {
		return
	}
	b.pb.ChangeMax64(total)
	b.totalUnknown = false
}

// Total reports the denominator currently in force, for diagnostics.
func (b *Bar) Total() int64 {
	if b == nil || b.pb == nil {
		return 0
	}
	return b.pb.GetMax64()
}

// Set moves the bar to an absolute count. The error is returned rather than
// swallowed so a caller can log it: the library rejects updates when its max is
// unset, which is exactly the kind of silent no-op that wastes an hour.
func (b *Bar) Set(n int64) error {
	if b == nil || b.pb == nil {
		return nil
	}
	if b.totalUnknown {
		// Nothing meaningful to draw, and painting now risks the latch above.
		return nil
	}
	return b.pb.Set64(n)
}

// Describe updates the leading label, used to surface the current pacing.
func (b *Bar) Describe(s string) {
	if b == nil || b.pb == nil || b.totalUnknown {
		// Describe repaints immediately, and painting against the placeholder
		// denominator would show a crawl that reads as finished at 0/1.
		return
	}
	b.pb.Describe(s)
}

// Finish ends the bar by committing its last frame. Erasing would be wrong: the
// crawl logs its summary the moment a run ends, and the tally the bar built up
// should stay above it rather than vanish.
func (b *Bar) Finish() {
	if b == nil || b.pb == nil || !b.enabled {
		return
	}
	_, _ = io.WriteString(b.w, "\n")
}
