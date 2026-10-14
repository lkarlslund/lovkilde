package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/lkarlslund/lovkilde/internal/config"
	"github.com/lkarlslund/lovkilde/internal/limiter"
	"github.com/lkarlslund/lovkilde/internal/progress"
)

// testTree builds the same flag registration as the real CLI but attaches a probe
// subcommand whose RunE only records what was parsed. Running the real commands
// would open the state store and hit the network.
type parsed struct {
	global map[string]string
	local  map[string]string
	called string
}

func testTree(t *testing.T, p *parsed) *cobra.Command {
	t.Helper()
	cfg := config.Default()
	var logLevel string
	var progressMode string

	root := rootShell()
	registerFlags(root.PersistentFlags(), &cfg, &logLevel, &progressMode)

	probe := &cobra.Command{
		Use:  "probe",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p.called = cmd.Name()
			p.global = map[string]string{}
			cmd.Flags().Visit(func(f *pflag.Flag) { p.global[f.Name] = f.Value.String() })
			p.local = map[string]string{}
			cmd.LocalFlags().Visit(func(f *pflag.Flag) { p.local[f.Name] = f.Value.String() })
			return nil
		},
	}
	var limit int
	probe.Flags().IntVar(&limit, "limit", 0, "stop after N items")

	root.AddCommand(probe)
	return root
}

func runCLI(t *testing.T, root *cobra.Command, args ...string) error {
	t.Helper()
	root.SetArgs(args)
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	return root.Execute()
}

// recorded looks for a flag across the global and local views.
func (p *parsed) recorded(name string) (string, bool) {
	if v, ok := p.global[name]; ok {
		return v, true
	}
	if v, ok := p.local[name]; ok {
		return v, true
	}
	return "", false
}

// Flags must bind identically before and after a subcommand. This is exactly the
// bug class the previous hand-rolled parser had, where "fetch -limit 3" was
// silently dropped and a crawl ran uncapped.
func TestFlagsBindOnEitherSideOfSubcommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		key  string
		want string
	}{
		{"local flag after command", []string{"probe", "--limit", "3"}, "limit", "3"},
		{"local flag before command", []string{"--limit", "3", "probe"}, "limit", "3"},
		{"global flag before command", []string{"--data-dir", "/tmp/x", "probe"}, "data-dir", "/tmp/x"},
		{"global flag after command", []string{"probe", "--data-dir", "/tmp/x"}, "data-dir", "/tmp/x"},
		{"both sides at once", []string{"--data-dir", "/tmp/y", "probe", "--limit", "7"}, "limit", "7"},
		{"duration flag before command", []string{"--request-timeout", "30s", "probe"}, "request-timeout", "30s"},
		{"duration with unit after command", []string{"probe", "--floor", "300ms"}, "floor", "300ms"},
		{"progress mode after command", []string{"probe", "--progress", "never"}, "progress", "never"},
		{"progress mode before command", []string{"--progress", "always", "probe"}, "progress", "always"},
	}
	for _, c := range cases {
		var p parsed
		root := testTree(t, &p)
		if err := runCLI(t, root, c.args...); err != nil {
			t.Errorf("%s: Execute: %v", c.name, err)
			continue
		}
		if p.called != "probe" {
			t.Errorf("%s: subcommand did not run (called %q)", c.name, p.called)
			continue
		}
		got, ok := p.recorded(c.key)
		if !ok {
			t.Errorf("%s: --%s not recorded as set; global=%v local=%v", c.name, c.key, p.global, p.local)
			continue
		}
		if got != c.want {
			t.Errorf("%s: --%s = %q, want %q", c.name, c.key, got, c.want)
		}
	}
}

// The value of a value-taking flag must not be mistaken for the command name: in
// "--data-dir /tmp/ri probe" the path is not a command even though it does not
// start with a dash.
func TestFlagValueIsNotTreatedAsCommand(t *testing.T) {
	var p parsed
	root := testTree(t, &p)
	if err := runCLI(t, root, "--data-dir", "/tmp/ri", "probe"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if p.called != "probe" {
		t.Fatalf("called %q, want probe (the path was taken as the command)", p.called)
	}
	if v, _ := p.recorded("data-dir"); v != "/tmp/ri" {
		t.Errorf("data-dir = %q, want /tmp/ri", v)
	}
}

// A value-taking flag placed before the command must not swallow the command
// token. (The bool variant of this test is retired: after the pacing cleanup the
// CLI has no bool flags, and testing pflag's own parsing is not our business.)
func TestDurationFlagBeforeCommandDoesNotConsumeCommand(t *testing.T) {
	var p parsed
	root := testTree(t, &p)
	if err := runCLI(t, root, "--request-timeout", "30s", "probe"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if p.called != "probe" {
		t.Fatalf("called %q, want probe", p.called)
	}
	if v, _ := p.recorded("request-timeout"); v != "30s" {
		t.Errorf("request-timeout = %q, want 30s", v)
	}
}

func TestUnknownFlagFails(t *testing.T) {
	var p parsed
	root := testTree(t, &p)
	if err := runCLI(t, root, "--not-a-real-flag", "probe"); err == nil {
		t.Error("unknown flag accepted")
	}
}

func TestExtraArgumentsFail(t *testing.T) {
	var p parsed
	root := testTree(t, &p)
	if err := runCLI(t, root, "probe", "stray"); err == nil {
		t.Error("stray argument accepted")
	}
}

// A fetch-only flag must not be accepted by another command, where it would have
// been silently ignored.
func TestFetchOnlyFlagsRejectedElsewhere(t *testing.T) {
	root := newRootCmd()
	if err := runCLI(t, root, "--limit", "5", "status"); err == nil {
		t.Error("--limit accepted by status; it would have been silently ignored")
	}
}

// The real command tree must expose the documented commands, so a rename cannot
// silently break existing cron invocations.
func TestRealCommandTree(t *testing.T) {
	root := newRootCmd()
	have := map[string]bool{}
	for _, c := range root.Commands() {
		have[c.Name()] = true
	}
	for _, name := range []string{"inventory", "harvest", "fetch", "sample", "update", "status", "changes", "rebuild", "artifact"} {
		if !have[name] {
			t.Errorf("command %q missing from the tree", name)
		}
	}
}

// fetch must expose --limit, --batch, --kind, --state and --order: --limit is the
// safety valve for trial runs, --kind samples one series out of URL order, --state is
// how failures get a second chance, and --order decides which end of 200k rows to
// start from.
func TestFetchHasLimitBatchAndKindFlags(t *testing.T) {
	root := newRootCmd()
	var fetch *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "fetch" {
			fetch = c
		}
	}
	if fetch == nil {
		t.Fatal("fetch command missing")
	}
	for _, name := range []string{"limit", "batch", "kind", "state", "order", "random-seed"} {
		if fetch.Flags().Lookup(name) == nil {
			t.Errorf("fetch is missing --%s", name)
		}
	}
	// The default has to stay chronological: a mirror aiming to finish should finish
	// in order, and a random default would turn a partial crawl into a shallow skim.
	if got := fetch.Flags().Lookup("order").DefValue; got != "asc" {
		t.Errorf("--order defaults to %q, want asc", got)
	}
}

// A bad --order must be refused rather than quietly crawling in some other order.
// This is the store's validation reached through the flag; silence here would look
// like a crawl that had simply made no progress.
func TestFetchRejectsUnknownOrder(t *testing.T) {
	root := newRootCmd()
	err := runCLI(t, root, "--data-dir", t.TempDir(), "fetch", "--order", "sideways")
	if err == nil {
		t.Fatal("--order sideways was accepted")
	}
	if !strings.Contains(err.Error(), "order") {
		t.Errorf("error %q does not mention the order", err)
	}
}

// The pacing flags must reach the limiter, not just the config struct: a flag that
// parses and is then dropped is worse than no flag, because it looks like a setting
// that works. This is the guard for that failure.
func TestLimiterTakesThePacingFlags(t *testing.T) {
	cfg := config.Default()
	cfg.Floor = 45 * time.Millisecond
	cfg.JitterPercent = 33
	lim := newLimiter(cfg)

	// Every bucket starts at the floor the flag gave; each adapts on its own from there.
	for _, b := range []string{limiter.BucketDocument, limiter.BucketHarvest, limiter.BucketSitemap} {
		if d, ok := lim.Current(b); !ok || d != 45*time.Millisecond {
			t.Errorf("%s bucket = %s (present %v), want the 45ms floor from the flag", b, d, ok)
		}
	}
	if got := lim.JitterPercent(); got != 33 {
		t.Errorf("--jitter 33 reached the limiter as %g", got)
	}
}

// Jitter is a percentage of the current interval and may only ever delay a
// request, so the floor stays a hard minimum.
func TestJitterDelaysButNeverAdvances(t *testing.T) {
	cfg := config.Default()
	cfg.Floor = 10 * time.Millisecond
	cfg.JitterPercent = 100
	lim := newLimiter(cfg)
	ctx := context.Background()
	if err := lim.Wait(ctx, limiter.BucketDocument); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < 8; i++ {
		if err := lim.Wait(ctx, limiter.BucketDocument); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	// Eight slots of 10ms plus up to one extra interval each: never faster than
	// 80ms, never slower than 160ms.
	if elapsed < 80*time.Millisecond {
		t.Errorf("8 paced waits took %s, want at least 80ms (jitter must not shorten the floor)", elapsed)
	}
	if elapsed > 160*time.Millisecond+40*time.Millisecond {
		t.Errorf("8 paced waits took %s, want at most ~160ms (jitter is capped at 100%% of the interval)", elapsed)
	}
}

// The defaults must be valid or every run fails at startup, and the pacing surface
// must stay two flags wide: a learned value cannot have a flag.
func TestConfigSafety(t *testing.T) {
	if err := config.Default().Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}

	root := newRootCmd()
	for _, name := range []string{
		"harvest-interval", "sitemap-interval", "document-interval", "document-max-interval",
		"document-floor", "adaptive-window", "adaptive-step-down", "adaptive-cooldown",
		"jitter-interval", "jitter-max",
	} {
		if root.PersistentFlags().Lookup(name) != nil {
			t.Errorf("--%s exists: that pace is a learned value, not an option", name)
		}
	}
	for _, name := range []string{"floor", "jitter"} {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Errorf("--%s is a pacing knob and must exist", name)
		}
	}

	// The floor itself is not policed — only a value that would leave requests
	// unpaced, which cannot be what anyone meant.
	unpaced := config.Default()
	unpaced.Floor = 0
	if err := unpaced.Validate(); err == nil {
		t.Error("a zero floor was accepted, which would disable pacing")
	}

	// Jitter is a percentage, so nonsense outside 0-100 is refused rather than
	// quietly clamped: --jitter 500 is a typo, not a request to double every wait.
	for _, j := range []float64{-1, 101, 500} {
		bad := config.Default()
		bad.JitterPercent = j
		if err := bad.Validate(); err == nil {
			t.Errorf("jitter %g accepted, want refusal", j)
		}
	}
	exact := config.Default()
	exact.JitterPercent = 0
	if err := exact.Validate(); err != nil {
		t.Errorf("--jitter 0 (exact spacing) rejected: %v", err)
	}
	// The two defaults must agree, or the flag's help text and the limiter's
	// built-in default drift apart.
	if config.Default().JitterPercent != float64(limiter.JitterPercentDefault) {
		t.Errorf("default jitter %g does not match limiter.JitterPercentDefault %d",
			config.Default().JitterPercent, limiter.JitterPercentDefault)
	}
}

// The fetch bar must actually paint, not just be configured. This mirrors the
// CLI path exactly -- gate swap, newFetchBar, then the same call order the
// OnProgress callback uses -- because a bar that silently renders nothing is
// indistinguishable from a stalled crawl.
func TestFetchBarPaints(t *testing.T) {
	var buf bytes.Buffer
	orig := gate
	gate = progress.NewGate(&buf)
	defer func() { gate = orig }()

	lim := limiter.NewTuned(map[string]limiter.Spec{
		limiter.BucketDocument: {Start: 300 * time.Millisecond, Floor: 200 * time.Millisecond, Max: time.Minute},
	}, 1.5, 4, time.Minute)

	// The same order the command uses: count, build the bar, then paint counts,
	// spaced further apart than the redraw throttle.
	bar := newFetchBar(lim, progress.ModeAlways, 8, "fetch")
	if !bar.Enabled() {
		t.Fatal("bar disabled in always mode")
	}

	for i := int64(1); i <= 8; i++ {
		if err := bar.Set(i); err != nil {
			t.Fatalf("Set(%d): %v", i, err)
		}
		bar.Describe(pacingLabel(lim, "fetch"))
		time.Sleep(200 * time.Millisecond)
	}
	bar.Finish()

	out := buf.String()
	if !strings.Contains(out, "8/8") {
		t.Fatalf("bar never reached its final count: %q", out)
	}
	if !strings.Contains(out, "fetch @") {
		t.Errorf("pacing label missing from the bar: %q", out)
	}
	if !strings.Contains(out, "±") {
		t.Errorf("jitter not visible in the label, so --jitter cannot be verified from the terminal: %q", out)
	}
}

// List flags are compared against stored values exactly, so what --kind and
// --state collect has to be normalised first: shell history should not miss by case
// or a stray space, and repeated flags must not ask for the same value twice.
func TestCleanListValues(t *testing.T) {
	got := cleanList([]string{" RETSINFO ", "retsinfo", "", "lta"})
	if len(got) != 2 || got[0] != "retsinfo" || got[1] != "lta" {
		t.Errorf("cleanList = %q, want [retsinfo lta]", got)
	}
	if len(cleanList(nil)) != 0 || len(cleanList([]string{" ", ""})) != 0 {
		t.Error("empty input must clean to nothing")
	}
	if got := cleanList([]string{" GONE ", "gone", "Failed"}); len(got) != 2 || got[0] != "gone" || got[1] != "failed" {
		t.Errorf("cleanList(states) = %q, want [gone failed]", got)
	}
}
