package main

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/lkarlslund/retsinformation/internal/config"
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

	root := rootShell()
	registerFlags(root.PersistentFlags(), &cfg, &logLevel)

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
		{"adaptive flag before command", []string{"--adaptive-window", "4", "probe"}, "adaptive-window", "4"},
		{"duration with unit after command", []string{"probe", "--document-floor", "300ms"}, "document-floor", "300ms"},
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

// Bool flags consume no following argument, so the next token is the command.
func TestBoolFlagDoesNotConsumeCommand(t *testing.T) {
	var p parsed
	root := testTree(t, &p)
	if err := runCLI(t, root, "--pinned-intervals", "probe"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if p.called != "probe" {
		t.Fatalf("called %q, want probe", p.called)
	}
	if v, _ := p.recorded("pinned-intervals"); v != "true" {
		t.Errorf("pinned-intervals = %q, want true", v)
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
	for _, name := range []string{"inventory", "harvest", "fetch", "update", "status", "rebuild"} {
		if !have[name] {
			t.Errorf("command %q missing from the tree", name)
		}
	}
}

// fetch must expose --limit and --batch; --limit is the safety valve for trial runs.
func TestFetchHasLimitAndBatchFlags(t *testing.T) {
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
	for _, name := range []string{"limit", "batch"} {
		if fetch.Flags().Lookup(name) == nil {
			t.Errorf("fetch is missing --%s", name)
		}
	}
}

// The published harvesting limit must not be configurable away, and the defaults
// themselves must be valid or every run fails at startup.
func TestConfigSafety(t *testing.T) {
	if err := config.Default().Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}

	breach := config.Default()
	breach.HarvestInterval = 5 * time.Second
	if err := breach.Validate(); err == nil {
		t.Error("harvest interval below the published 10s limit was accepted")
	} else if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should state the consequence, got: %v", err)
	}

	tooFast := config.Default()
	tooFast.DocumentFloor = 10 * time.Millisecond
	if err := tooFast.Validate(); err == nil {
		t.Error("document floor below the courtesy minimum was accepted")
	}

	// Backoff must never be able to speed the crawl up.
	inverted := config.Default()
	inverted.DocumentMax = inverted.DocumentStart / 2
	if err := inverted.Validate(); err == nil {
		t.Error("document max below start was accepted, which would make backoff accelerate")
	}
}
