// Command retsin-update mirrors Danish legislation from retsinformation.dk into a
// local archive of canonical ELI XML, keeps it current via the official harvesting
// service, and compiles it into the artifact the server reads (command retsin).
//
// It is the only program that talks to retsinformation.dk or reads the publisher's
// XML. Serving needs none of it: an artifact is everything a server reads.
//
// All settings are flags and there is deliberately no config file or environment
// layer: a run does exactly what its invocation says, and nothing remembered from
// a previous day can silently change a multi-day crawl.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/lkarlslund/retsinformation/internal/cli"
	"github.com/lkarlslund/retsinformation/internal/config"
	"github.com/lkarlslund/retsinformation/internal/limiter"
	"github.com/lkarlslund/retsinformation/internal/progress"
)

// version is reported by --version and identifies the tool.
const version = "0.1"

// gate serializes progress frames and log records, which share stderr (cli.Gate).
var gate = cli.Gate

// progressModeValue is the validated --progress setting, resolved when the
// runtime opens. It defaults to auto, which renders only on a terminal.
var progressModeValue = progress.ModeAuto

// prof is the run's profiler, set by --cpuprofile and --memprofile.
var prof = &cli.Profiler{}

func main() {
	err := newRootCmd().Execute()
	prof.Finish()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(gate.LogWriter(), "interrupted; state is saved, re-run to resume")
			os.Exit(1)
		}
		fmt.Fprintf(gate.LogWriter(), "error: %v\n", err)
		os.Exit(1)
	}
}

// newRootCmd builds the command tree. Flag registration is split out so tests can
// build the same tree and assert on parsed values without opening storage.
func newRootCmd() *cobra.Command {
	cfg := config.Default()
	var logLevel string
	var progressMode string

	root := rootShell()
	registerFlags(root.PersistentFlags(), &cfg, &logLevel, &progressMode)
	prof.Register(root)
	root.PersistentPreRunE = func(*cobra.Command, []string) error { return prof.Start() }

	// The runtime is opened lazily by each command, so --help and flag validation
	// never create directories or open the state store.
	open := openFunc(func(cmd *cobra.Command) (*app, *limiter.Limiter, error) {
		if err := cfg.Validate(); err != nil {
			return nil, nil, err
		}
		mode, err := progress.ParseMode(progressMode)
		if err != nil {
			return nil, nil, err
		}
		progressModeValue = mode
		cli.SetLogger(logLevel)
		a, lim, err := newApp(cfg)
		if err != nil {
			return nil, nil, err
		}
		logPacing(cfg, cmd.Name())
		return a, lim, nil
	})

	// resolve validates the flags and sets up logging without opening any storage.
	// The artifact build takes this instead of the runtime: opening the store would
	// take its lock, and a build that held it would keep the crawl out.
	resolve := func() (config.Config, error) {
		if err := cfg.Validate(); err != nil {
			return cfg, err
		}
		cli.SetLogger(logLevel)
		return cfg, nil
	}

	root.AddCommand(
		inventoryCmd(open),
		harvestCmd(open),
		fetchCmd(open),
		sampleCmd(open),
		artifactCmd(resolve),
		updateCmd(open),
		statusCmd(open),
		changesCmd(open),
		rebuildCmd(open),
	)
	return root
}

// rootShell holds only the static description and root-level behaviour.
func rootShell() *cobra.Command {
	return &cobra.Command{
		Use:     "retsin-update",
		Short:   "Mirror Danish legislation from retsinformation.dk and compile the artifact",
		Version: version,
		Long: `Mirror Danish legislation from retsinformation.dk into a local archive of
canonical ELI XML, and keep it current through Civilstyrelsens harvesting service.

Phases (each independent, resumable and idempotent):
  inventory  walk the ELI sitemap: ~22 requests discover all ~203k resources
  harvest    consume the official change feed, which covers a rolling 10 days
  fetch      download canonical XML for every resource not already on disk
  update     harvest + fetch since the last completed pass; --full adds the sitemap
             pass, which is the only one that can repair an outage over 10 days
  changes    read the change feed back and reconcile it against the archive
  artifact build
             compile the archive into the artifact the server reads (retsin serve)

Pacing:
  The harvesting service is limited by its own documentation to one call per 10
  seconds and answers HTTP 429 beyond that. That pace, and the sitemap's, are
  constants: there is no flag that can lower them.

  Document requests have no published limit and are served uncached straight from
  the origin, so their interval is learned rather than fixed. It starts at the
  floor and stays there while the service is happy: any 429 or 5xx doubles the
  interval and holds it for 10 minutes, after which 20 consecutive successes step
  it back down. Two flags shape it. --document-floor is the interval to start at and
  the fastest one to return to; 200ms is the considerate default rather than a
  limit, and setting it lower is honoured until the service pushes back. --jitter
  adds a random share of the current interval on top of each wait, so a crawl that
  runs for days is not machine-regular; it never shortens a wait, so the floor stays
  a hard minimum, and --jitter 0 gives exact spacing when the question is whether a
  service limit is what you are hitting. Learning starts over on every run.

Data layout:
  <data-dir>/xml/<kind>/<year>/<id>.xml   canonical documents (the archive)
  <data-dir>/state/current.json           what the mirror holds and knows (rebuildable)
  <data-dir>/state/queue.json             retry counters and errors of unfinished rows
  <data-dir>/state/journal.jsonl          changes since those were written`,
		SilenceUsage: true,
		// main prints the error once, through the gate that keeps it off a progress frame.
		SilenceErrors: true,
		Args:          cobra.NoArgs,
	}
}

// registerFlags declares every setting as a flag. Flags write straight into cfg,
// which the caller pre-filled with defaults, so --help shows real defaults and
// there is exactly one source of truth.
func registerFlags(pf *pflag.FlagSet, cfg *config.Config, logLevel, progress *string) {
	pf.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "data directory holding the archive and the state store")
	pf.StringVar(&cfg.UserAgent, "user-agent", cfg.UserAgent, "User-Agent sent on every request; keep a contact address so the operator can reach us instead of blocking us")
	pf.StringVar(&cfg.SiteBase, "site", cfg.SiteBase, "origin serving /eli and the document API")
	pf.StringVar(&cfg.HarvestBase, "harvest-api", cfg.HarvestBase, "origin of the harvesting service")

	// Pacing has two knobs: how fast the learner is allowed to settle, and how far
	// each request may drift past that interval. The rest of the schedule is either
	// a published obligation (harvest), a measurement (sitemap), or the learner's
	// own shape, all of them constants.
	pf.DurationVar(&cfg.DocumentFloor, "document-floor", cfg.DocumentFloor,
		fmt.Sprintf("fastest document interval the learner may settle at; %s is the considerate default, and anything lower is honoured at the risk of a 429", config.DefaultDocumentFloor))

	pf.Float64Var(&cfg.JitterPercent, "jitter", cfg.JitterPercent,
		"random extra wait as a percentage of the current interval, so a long crawl is not metronome-paced; 0 gives exact spacing, which is what to use when testing whether a service limit is what you are hitting")

	pf.DurationVar(&cfg.RequestTimeout, "request-timeout", cfg.RequestTimeout, "timeout for a single HTTP request")
	pf.StringVar(logLevel, "log-level", "info", "log level: debug, info, warn, error")
	pf.StringVar(progress, "progress", "auto",
		"show a live progress bar with docs/sec and ETA: auto (terminals only), always, or never")
}

// runWith is the shared body for commands needing the runtime and a cancellable
// context, so Ctrl-C always leaves state consistent.
func runWith(a *app, lim *limiter.Limiter, fn func(ctx context.Context) error) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := fn(ctx); err != nil {
		return err
	}
	reportPacingOutcome(a, lim)
	return nil
}

// reportPacingOutcome tells the operator what the learner concluded. A run that got
// throttled has already slowed itself down; a run that never was may tolerate a
// lower floor next time.
func reportPacingOutcome(a *app, lim *limiter.Limiter) {
	if th := a.client.Throttle.Load(); th > 0 {
		slog.Warn("server throttled us during this run; the learner already slowed down, consider a higher --document-floor",
			"throttled_requests", th,
			"total_requests", a.client.Requests.Load(),
			"interval_changes", lim.Steps(),
			"document_interval_now", currentDocumentInterval(lim))
		return
	}
	if steps := lim.Steps(); steps > 0 {
		slog.Info("adaptive pacing tightened the schedule during this run",
			"interval_changes", steps,
			"document_interval_now", currentDocumentInterval(lim))
	}
}

// logPacing states the schedule up front, so an unexpectedly aggressive or timid
// run is visible in the very first log line. Only the document floor is
// configurable; the other two buckets are constants.
func logPacing(cfg config.Config, cmd string) {
	slog.Info("retsin starting",
		"command", cmd,
		"data_dir", cfg.DataDir,
		"document_floor", cfg.DocumentFloor.String(),
		"jitter_percent", cfg.JitterPercent,
		"harvest_interval", config.HarvestInterval.String(),
		"sitemap_interval", config.SitemapInterval.String())
}
