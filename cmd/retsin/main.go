// Command retsin mirrors Danish legislation from retsininformation.dk into a
// local archive of canonical ELI XML, keeping itself current via the official
// harvesting service.
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

	"github.com/lkarlslund/retsinformation/internal/config"
	"github.com/lkarlslund/retsinformation/internal/limiter"
	"github.com/lkarlslund/retsinformation/internal/progress"
)

// version is reported by --version and identifies the tool.
const version = "0.1"

// gate serializes progress frames and log records, which share stderr. It is
// package-level because slog's default logger is: every writer in the process,
// including the ones below, has to go through the same gate or the guarantee is
// void.
var gate = progress.NewGate(os.Stderr)

// progressModeValue is the validated --progress setting, resolved when the
// runtime opens. It defaults to auto, which renders only on a terminal.
var progressModeValue = progress.ModeAuto

func main() {
	if err := newRootCmd().Execute(); err != nil {
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
		setLogger(logLevel)
		a, lim, err := newApp(cfg)
		if err != nil {
			return nil, nil, err
		}
		logPacing(cfg, cmd.Name())
		return a, lim, nil
	})

	root.AddCommand(
		inventoryCmd(open),
		harvestCmd(open),
		fetchCmd(open),
		updateCmd(open),
		statusCmd(open),
		rebuildCmd(open),
	)
	return root
}

// rootShell holds only the static description and root-level behaviour.
func rootShell() *cobra.Command {
	return &cobra.Command{
		Use:     "retsin",
		Short:   "Mirror Danish legislation from retsininformation.dk",
		Version: version,
		Long: `Mirror Danish legislation from retsininformation.dk into a local archive of
canonical ELI XML, and keep it current through Civilstyrelsens harvesting service.

Phases (each independent, resumable and idempotent):
  inventory  walk the ELI sitemap: ~22 requests discover all ~203k resources
  harvest    consume the official change feed, which covers a rolling 10 days
  fetch      download canonical XML for every resource not already on disk
  update     all three in order, suitable for a daily cron

Pacing:
  The harvesting service is limited by its own documentation to one call per 10
  seconds and answers HTTP 429 beyond that. Its request bucket is pinned and
  validated at startup, so it can never be configured below the published limit.

  Document requests have no published limit and are served uncached straight from
  the origin, so their interval is learned rather than fixed. It starts at
  --document-interval and steps down towards --document-floor while requests keep
  succeeding, doubling back on any 429 or 5xx and holding that safer interval for
  --adaptive-cooldown before probing faster again. Use --pinned-intervals to
  disable learning and hold every interval exactly as configured.

Data layout:
  <data-dir>/xml/<kind>/<year>/<id>.xml   canonical documents (the archive)
  <data-dir>/meta/kv/                     Badger state store (derived, rebuildable)`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
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

	pf.DurationVar(&cfg.HarvestInterval, "harvest-interval", cfg.HarvestInterval,
		fmt.Sprintf("minimum gap between harvesting-service calls (cannot go below the published %s limit)", config.HarvestLimit))
	pf.DurationVar(&cfg.DocumentStart, "document-interval", cfg.DocumentStart,
		"starting minimum gap between document requests; the learner steps down to --document-floor while requests succeed")
	pf.DurationVar(&cfg.DocumentFloor, "document-floor", cfg.DocumentFloor,
		fmt.Sprintf("fastest document interval the learner may settle at (courtesy minimum %s)", config.DocumentFloor))
	pf.DurationVar(&cfg.DocumentMax, "document-max-interval", cfg.DocumentMax,
		"slowest document interval backoff may reach after throttling")
	pf.DurationVar(&cfg.SitemapInterval, "sitemap-interval", cfg.SitemapInterval, "minimum gap between sitemap requests")
	pf.BoolVar(&cfg.PinnedIntervals, "pinned-intervals", cfg.PinnedIntervals,
		"disable adaptive pacing and hold every interval at its configured value")

	pf.Float64Var(&cfg.AdaptiveStepDown, "adaptive-step-down", cfg.AdaptiveStepDown,
		"divide the interval by this after a clean window (must be > 1)")
	pf.IntVar(&cfg.AdaptiveWindow, "adaptive-window", cfg.AdaptiveWindow,
		"consecutive successful requests required before stepping down")
	pf.DurationVar(&cfg.AdaptiveCooldown, "adaptive-cooldown", cfg.AdaptiveCooldown,
		"hold the current interval this long after being throttled before probing faster")

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
// run is visible in the very first log line.
func logPacing(cfg config.Config, cmd string) {
	slog.Info("retsin starting",
		"command", cmd,
		"data_dir", cfg.DataDir,
		"harvest_interval", cfg.HarvestInterval.String(),
		"harvest_pinned", true,
		"document_interval_start", cfg.DocumentStart.String(),
		"document_floor", cfg.DocumentFloor.String(),
		"adaptive", !cfg.PinnedIntervals,
		"adaptive_window", cfg.AdaptiveWindow,
		"adaptive_step_down", cfg.AdaptiveStepDown)
}

func setLogger(level string) {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	// Through the gate, so a record never lands in the middle of a bar frame.
	slog.SetDefault(slog.New(slog.NewTextHandler(gate.LogWriter(), &slog.HandlerOptions{Level: lv})))
}
