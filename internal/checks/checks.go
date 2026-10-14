// Package checks runs the server's scheduled checks — for a newer published dataset and for a
// newer published build of the software — at the interval the operator chose on the settings
// page, and runs one at once when asked. The intervals and each check's last outcome are kept in
// one file in the data directory, so a restart neither forgets the choice nor repeats a check.
package checks

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The checks a server runs.
const (
	Dataset  = "dataset"  // a newer published artifact, installed when found
	Software = "software" // a newer published build, only reported
)

// File is where the intervals and outcomes are kept, in the data directory.
const File = "checks.json"

// Interval is how often a check runs.
type Interval string

const (
	Daily   Interval = "dagligt"
	Weekly  Interval = "ugentligt"
	Monthly Interval = "maanedligt"
	Off     Interval = "fra"
)

// Intervals are the choices, in the order a page offers them.
var Intervals = []Interval{Daily, Weekly, Monthly, Off}

// Every is the time between checks; 0 for Off.
func (i Interval) Every() time.Duration {
	switch i {
	case Daily:
		return 24 * time.Hour
	case Weekly:
		return 7 * 24 * time.Hour
	case Monthly:
		return 30 * 24 * time.Hour
	}
	return 0
}

// Label is the interval as a page says it.
func (i Interval) Label() string {
	switch i {
	case Daily:
		return "Dagligt"
	case Weekly:
		return "Ugentligt"
	case Monthly:
		return "Månedligt"
	}
	return "Fra"
}

// Valid says whether i is one of Intervals.
func (i Interval) Valid() bool { return i.Every() > 0 || i == Off }

// IntervalFor is the interval closest to a duration given on the command line.
func IntervalFor(d time.Duration) Interval {
	switch {
	case d <= 0:
		return Off
	case d <= 24*time.Hour:
		return Daily
	case d <= 7*24*time.Hour:
		return Weekly
	}
	return Monthly
}

// Outcome is what one run of a check found.
type Outcome struct {
	At      time.Time `json:"at"`
	Summary string    `json:"summary,omitempty"`
	Err     string    `json:"error,omitempty"`
	// Newer is how many changes a newer published version holds that this one does not, for a
	// check that only reports one rather than installing it.
	Newer int `json:"newer,omitempty"`
}

// State is a check as a page shows it.
type State struct {
	Name    string
	Every   Interval
	Last    Outcome
	Running bool
}

type job struct {
	do      func(context.Context) Outcome
	every   Interval
	last    Outcome
	running bool
	now     bool // asked to run at once
	kick    chan struct{}
}

// Checks is the server's checks. Add them, then Run.
type Checks struct {
	path string
	mu   sync.Mutex
	jobs map[string]*job
}

type saved struct {
	Every Interval `json:"every"`
	Last  Outcome  `json:"last"`
}

// Open reads the saved intervals and outcomes from dataDir; a missing or unreadable file is a
// fresh start.
func Open(dataDir string) *Checks {
	return &Checks{path: filepath.Join(dataDir, File), jobs: map[string]*job{}}
}

func (c *Checks) load() map[string]saved {
	var s map[string]saved
	raw, err := os.ReadFile(c.path)
	if err == nil {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

// Add registers a check under name, running every def unless an interval was saved for it.
// lastAt is when it last ran by another record, used when none was saved.
func (c *Checks) Add(name string, def Interval, lastAt time.Time, do func(context.Context) Outcome) {
	j := &job{do: do, every: def, kick: make(chan struct{}, 1), last: Outcome{At: lastAt}}
	if s, ok := c.load()[name]; ok {
		if s.Every.Valid() {
			j.every = s.Every
		}
		if !s.Last.At.IsZero() {
			j.last = s.Last
		}
	}
	c.mu.Lock()
	c.jobs[name] = j
	c.mu.Unlock()
}

// Has says whether a check is registered under name.
func (c *Checks) Has(name string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.jobs[name] != nil
}

// State is the named check now, and false when there is none.
func (c *Checks) State(name string) (State, bool) {
	if c == nil {
		return State{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	j := c.jobs[name]
	if j == nil {
		return State{}, false
	}
	return State{Name: name, Every: j.every, Last: j.last, Running: j.running}, true
}

// Set changes how often the named check runs, and saves it.
func (c *Checks) Set(name string, every Interval) error {
	if !every.Valid() {
		return errors.New("unknown interval " + string(every))
	}
	c.mu.Lock()
	j := c.jobs[name]
	if j == nil {
		c.mu.Unlock()
		return fs.ErrNotExist
	}
	j.every = every
	c.mu.Unlock()
	c.kickOne(j) // re-evaluate: a shorter interval can make it due now
	return c.save()
}

// Now runs the named check at once, whatever its interval, unless it is running already.
func (c *Checks) Now(name string) {
	c.mu.Lock()
	j := c.jobs[name]
	if j != nil {
		j.now = true
	}
	c.mu.Unlock()
	if j != nil {
		c.kickOne(j)
	}
}

func (c *Checks) kickOne(j *job) {
	select {
	case j.kick <- struct{}{}:
	default:
	}
}

func (c *Checks) save() error {
	c.mu.Lock()
	s := make(map[string]saved, len(c.jobs))
	for name, j := range c.jobs {
		s[name] = saved{Every: j.every, Last: j.last}
	}
	c.mu.Unlock()
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// poll is how often a check asks itself whether it is due, besides when it is kicked.
var poll = time.Hour

// Run runs every check when it is due, until ctx ends.
func (c *Checks) Run(ctx context.Context) {
	c.mu.Lock()
	var wg sync.WaitGroup
	for _, j := range c.jobs {
		wg.Go(func() { c.loop(ctx, j) })
	}
	c.mu.Unlock()
	wg.Wait()
}

func (c *Checks) loop(ctx context.Context, j *job) {
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		c.mu.Lock()
		every := j.every.Every()
		due := j.now || every > 0 && time.Since(j.last.At) >= every
		if due {
			j.now, j.running = false, true
		}
		c.mu.Unlock()
		if due {
			out := j.do(ctx)
			if ctx.Err() != nil {
				return
			}
			out.At = time.Now()
			c.mu.Lock()
			j.last, j.running = out, false
			c.mu.Unlock()
			_ = c.save()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-j.kick:
		}
	}
}
