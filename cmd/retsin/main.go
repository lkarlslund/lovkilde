// Command retsin mirrors Danish legislation from retsininformation.dk into a
// local archive of canonical ELI XML, keeping itself current via the official
// harvesting service.
//
//	retsin inventory   refresh the catalogue from the ELI sitemap (~22 requests)
//	retsin harvest     consume the official change feed (max 1 call / 10s)
//	retsin fetch       download canonical XML for anything not yet on disk
//	retsin update      inventory + harvest + fetch, i.e. the daily cron entry
//	retsin status      report progress
//
// Every phase is resumable and idempotent: re-running costs no requests for
// content already held.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lkarlslund/retsinformation/internal/archive"
	"github.com/lkarlslund/retsinformation/internal/config"
	"github.com/lkarlslund/retsinformation/internal/crawl"
	"github.com/lkarlslund/retsinformation/internal/limiter"
	"github.com/lkarlslund/retsinformation/internal/rihttp"
	"github.com/lkarlslund/retsinformation/internal/store"
)

func main() {
	if err := run(); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "interrupted; state is saved, re-run to resume")
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type app struct {
	cfg    config.Config
	store  *store.Store
	arch   *archive.Archive
	crawl  *crawl.Crawler
	client *rihttp.Client
}

func run() error {
	// Parse our own flags, then hand the remainder to the subcommand. Using the
	// global flag package for subcommand flags would silently ignore them: Go
	// stops parsing at the first non-flag argument, so "fetch -limit 3" would
	// drop -limit and run uncapped.
	global := flag.NewFlagSet("retsin", flag.ContinueOnError)
	logLevel := global.String("log-level", "info", "log level: debug, info, warn")
	configPath := global.String("config", "", "JSON config file (optional)")
	dataDir := global.String("data-dir", "", "data directory (overrides config)")
	global.Usage = usage

	args, err := parseGlobal(global)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		usage()
		return errors.New("no command given")
	}
	cmd := args[0]
	sub := flag.NewFlagSet(cmd, flag.ContinueOnError)
	limit := sub.Int("limit", 0, "stop after N items")
	batch := sub.Int("batch", 200, "pending rows to process per batch")
	if err := sub.Parse(args[1:]); err != nil {
		return err
	}
	if sub.NArg() > 0 {
		return fmt.Errorf("unexpected arguments after %s: %v", cmd, sub.Args())
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}

	setLogger(*logLevel)

	a, err := open(cfg)
	if err != nil {
		return err
	}
	defer a.close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	slog.Info("retsin starting", "command", cmd, "data_dir", cfg.DataDir,
		"harvest_min_interval", cfg.HarvestInterval.D().String(),
		"document_min_interval", cfg.DocumentInterval.D().String())

	switch cmd {
	case "inventory":
		err = a.crawl.Inventory(ctx)
	case "harvest":
		err = a.crawl.Harvest(ctx)
	case "fetch":
		err = a.crawl.Fetch(ctx, crawl.FetchOptions{Limit: *limit, Batch: *batch})
	case "update":
		err = a.crawl.Update(ctx)
	case "status":
		err = status(ctx, a)
	case "rebuild":
		err = a.crawl.Rebuild(ctx)
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		return err
	}
	if th := a.client.Throttle.Load(); th > 0 {
		slog.Warn("server throttled us during this run; consider a longer interval",
			"throttled_requests", th, "total_requests", a.client.Requests.Load())
	}
	return nil
}

// parseGlobal consumes leading flags and returns the remaining arguments.
// Interleaved forms like "retsin fetch -limit 3" are supported by stripping
// known global flags wherever they appear.
func parseGlobal(fs *flag.FlagSet) ([]string, error) {
	if err := fs.Parse(os.Args[1:]); err != nil {
		return nil, err
	}
	return fs.Args(), nil
}

// open wires configuration into a live application.
func open(cfg config.Config) (*app, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "meta", "kv"))
	if err != nil {
		return nil, err
	}
	ar, err := archive.Open(cfg.XMLDir())
	if err != nil {
		st.Close()
		return nil, err
	}
	// One limiter for the whole process makes the published "once per 10
	// seconds" harvesting limit a structural guarantee rather than a convention.
	lim := limiter.New(map[string]time.Duration{
		limiter.BucketHarvest:  cfg.HarvestInterval.D(),
		limiter.BucketDocument: cfg.DocumentInterval.D(),
		limiter.BucketSitemap:  2 * time.Second,
	})
	client := rihttp.New(cfg, lim)
	return &app{
		cfg:    cfg,
		store:  st,
		arch:   ar,
		client: client,
		crawl:  crawl.New(cfg, st, ar, client),
	}, nil
}

func (a *app) close() {
	if err := a.store.Close(); err != nil {
		slog.Error("closing state store", "err", err)
	}
}

// status reports manifest and archive totals.
func status(ctx context.Context, a *app) error {
	counts, total, err := a.store.CountByState(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("state store: %s\n", filepath.Join(a.cfg.DataDir, "meta", "kv"))
	fmt.Printf("archive:     %s\n", a.arch.Dir())
	fmt.Printf("inventory resources: %d\n", total)
	for _, st := range []store.State{store.StateFetched, store.StatePending, store.StateSkipped, store.StateFailed, store.StateGone} {
		if n, ok := counts[st]; ok || n > 0 {
			fmt.Printf("  %-8s %d\n", st, n)
		}
	}
	files, bytes, err := a.arch.Count()
	if err != nil {
		return err
	}
	fmt.Printf("archive files: %d, %s\n", files, archive.SizeHint(bytes))
	if day, found, err := a.store.LastHarvestedDay(ctx); err != nil {
		return err
	} else if found {
		fmt.Printf("last harvested feed day: %s\n", day.Format(time.DateOnly))
	} else {
		fmt.Printf("last harvested feed day: (never)\n")
	}
	if v, found, err := a.store.GetMeta(ctx, "last_sitemap_inventory"); err != nil {
		return err
	} else if found {
		fmt.Printf("last sitemap inventory:  %s\n", v)
	}
	return nil
}

func setLogger(level string) {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv})
	slog.SetDefault(slog.New(h))
}

func usage() {
	fmt.Fprint(os.Stderr, `retsin - mirror Danish legislation from retsininformation.dk

Usage:
  retsin [flags] <command>

Commands:
  inventory   Refresh the catalogue from the ELI sitemap (~22 requests, ~203k resources)
  harvest     Consume the official change feed (published limit: 1 call per 10 seconds)
  fetch       Download canonical ELI XML for every resource not already on disk
  update      inventory + harvest + fetch (the daily cron entry point)
  status      Report inventory, archive and checkpoint totals
  rebuild     Re-derive the state store from the XML archive (no requests)

Global flags:
`)
	fs := flag.NewFlagSet("retsin", flag.ContinueOnError)
	fs.String("log-level", "info", "log level: debug, info, warn")
	fs.String("config", "", "JSON config file (optional)")
	fs.String("data-dir", "", "data directory (overrides config)")
	fs.PrintDefaults()
	fmt.Fprint(os.Stderr, `
Pacing:
  The harvesting service is limited by its own documentation to one call per 10
  seconds and answers HTTP 429 beyond that; this tool enforces that limit in a
  process-global limiter. Document requests are additionally throttled because
  they are served uncached straight from the origin.

Data layout:
  <data-dir>/xml/<kind>/<year>/<id>.xml   canonical documents (the archive)
  <data-dir>/meta/kv/                     Badger state store (derived, rebuildable)
`)
}
