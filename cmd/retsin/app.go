package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/lkarlslund/retsinformation/internal/archive"
	"github.com/lkarlslund/retsinformation/internal/config"
	"github.com/lkarlslund/retsinformation/internal/crawl"
	"github.com/lkarlslund/retsinformation/internal/limiter"
	"github.com/lkarlslund/retsinformation/internal/progress"
	"github.com/lkarlslund/retsinformation/internal/rihttp"
	"github.com/lkarlslund/retsinformation/internal/store"
)

// app is the wired-up runtime: one state store, one archive, one HTTP client and
// one process-global limiter shared by every phase.
type app struct {
	cfg    config.Config
	store  *store.Store
	arch   *archive.Archive
	crawl  *crawl.Crawler
	client *rihttp.Client
}

// newApp opens storage and builds the pacing schedule. The limiter is returned
// alongside the app so callers such as `status` can report the interval actually in
// force without inventing a partially-opened app.
func newApp(cfg config.Config) (*app, *limiter.Limiter, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create data dir: %w", err)
	}
	st, err := store.Open(cfg.StoreDir())
	if err != nil {
		return nil, nil, err
	}
	ar, err := archive.Open(cfg.XMLDir())
	if err != nil {
		st.Close()
		return nil, nil, err
	}

	// One limiter for the whole process makes the published "once per 10 seconds"
	// harvesting limit a structural guarantee rather than a convention. The harvest
	// bucket is pinned: that limit is an obligation, not an observation, so there
	// is nothing to learn from success and no room to be faster.
	//
	// Document and sitemap buckets are adaptive: the service publishes no limit for
	// them and sends no Retry-After header, so the safe interval can only be found
	// by probing, and must back off the moment it is told to slow down. Only their
	// floors come from the caller; the learner's shape is fixed in the limiter.
	specs := map[string]limiter.Spec{
		limiter.BucketHarvest: {
			Start:  config.HarvestInterval,
			Floor:  config.HarvestInterval,
			Max:    config.HarvestInterval,
			Pinned: true,
		},
		limiter.BucketDocument: {
			Start: limiter.StartInterval,
			Floor: cfg.DocumentFloor,
			Max:   limiter.MaxInterval,
		},
		limiter.BucketSitemap: {
			Start: config.SitemapInterval,
			Floor: config.SitemapInterval,
			Max:   config.SitemapInterval * 8,
		},
	}
	lim := limiter.New(specs)
	client := rihttp.New(cfg, lim)

	return &app{
		cfg:    cfg,
		store:  st,
		arch:   ar,
		client: client,
		crawl:  crawl.New(cfg, st, ar, client),
	}, lim, nil
}

// Close releases the state store.
func (a *app) Close() {
	if err := a.store.Close(); err != nil {
		slog.Error("closing state store", "err", err)
	}
}

// currentDocumentInterval reports the interval the learner has settled on.
func currentDocumentInterval(l *limiter.Limiter) string {
	if d, ok := l.Current(limiter.BucketDocument); ok {
		return d.String()
	}
	return "unknown"
}

// openFunc builds the runtime from validated flags.
type openFunc func(cmd *cobra.Command) (*app, *limiter.Limiter, error)

func inventoryCmd(open openFunc) *cobra.Command {
	return &cobra.Command{
		Use:   "inventory",
		Short: "Refresh the catalogue from the ELI sitemap",
		Long: `Walk the ELI sitemap index and record every resource it lists.

This is the cheapest full-scope operation in the tool: roughly 22 requests discover
all ~203k resources, and each entry carries a lastmod that later runs use to detect
what changed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, lim, err := open(cmd)
			if err != nil {
				return err
			}
			defer a.Close()
			return runWith(a, lim, a.crawl.Inventory)
		},
	}
}

func harvestCmd(open openFunc) *cobra.Command {
	return &cobra.Command{
		Use:   "harvest",
		Short: "Consume the official change feed",
		Long: `Query the harvesting service for changed documents and record what it reports.

The service answers for a rolling 10-day window only, so this resumes from the last
completed day and warns if an outage has exceeded the window, in which case older
changes are gone and only a sitemap inventory pass can repair the gap.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, lim, err := open(cmd)
			if err != nil {
				return err
			}
			defer a.Close()
			return runWith(a, lim, a.crawl.Harvest)
		},
	}
}

func fetchCmd(open openFunc) *cobra.Command {
	var limit, batch int
	cmd := &cobra.Command{
		Use:   "fetch",
		Short: "Download canonical ELI XML for anything not on disk",
		Long: `Download the canonical XML for every catalogued resource whose file is missing.

Requests are paced by the adaptive document bucket. Resources already on disk are
closed without any request at all, so a multi-day crawl can be interrupted at any
point and a second full pass after completion costs nothing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, lim, err := open(cmd)
			if err != nil {
				return err
			}
			defer a.Close()
			return runWith(a, lim, func(ctx context.Context) error {
				// Count first, then build the bar: it needs its denominator at
				// construction, and a run with nothing pending gets no bar at all.
				pending, err := a.crawl.PendingCount(ctx, limit)
				if err != nil {
					return err
				}
				bar := newFetchBar(lim, progressModeValue, pending)
				defer bar.Finish()
				var barWarned bool
				return a.crawl.Fetch(ctx, crawl.FetchOptions{
					Limit: limit, Batch: batch,
					OnProgress: func(st crawl.FetchStats) {
						if err := bar.Set(st.Attempted); err != nil && !barWarned {
							// Once is enough: a bar that cannot render is not
							// worth 200k warnings, but it is worth knowing about.
							barWarned = true
							slog.Warn("progress bar not updating", "err", err)
						}
						if bar.Enabled() {
							// Show the pace actually in force: it is what tells
							// the operator the learner is working, not idling.
							bar.Describe(pacingLabel(lim))
						}
					},
				})
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "stop after N items (useful for a trial run)")
	cmd.Flags().IntVar(&batch, "batch", 200, "pending rows to process per batch")
	return cmd
}

// newFetchBar builds the live progress line for a fetch run, honouring
// --progress. total must be the number of items the run will take on: a bar with
// nothing to count is not built. The label carries the interval currently in
// force, which is the one number worth watching on a multi-day crawl: it shows
// the learner acting on what the origin is actually accepting.
func newFetchBar(lim *limiter.Limiter, mode progress.Mode, total int64) *progress.Bar {
	bar := progress.NewBar(gate, "fetch", total, mode)
	if d, ok := lim.Current(limiter.BucketDocument); ok {
		bar.Describe("fetch @" + d.Round(time.Millisecond).String())
	}
	return bar
}

// pacingLabel is the bar's leading text, refreshed as the learner retunes.
func pacingLabel(lim *limiter.Limiter) string {
	d, ok := lim.Current(limiter.BucketDocument)
	if !ok {
		return "fetch"
	}
	return "fetch @" + d.Round(time.Millisecond).String()
}

func updateCmd(open openFunc) *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "inventory + harvest + fetch (the daily cron entry point)",
		Long: `Run all three phases in order. Safe on any schedule: unchanged documents are
never re-requested, so a daily run costs one or two harvest calls plus whatever is
genuinely new.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, lim, err := open(cmd)
			if err != nil {
				return err
			}
			defer a.Close()
			return runWith(a, lim, a.crawl.Update)
		},
	}
}

func statusCmd(open openFunc) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report inventory, archive, checkpoints and the pacing in force",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, lim, err := open(cmd)
			if err != nil {
				return err
			}
			defer a.Close()
			return status(cmd.Context(), a, lim)
		},
	}
}

func rebuildCmd(open openFunc) *cobra.Command {
	return &cobra.Command{
		Use:   "rebuild",
		Short: "Re-derive the state store from the XML archive (no requests)",
		Long: `Relink every XML file on disk back into the state store, at zero HTTP cost.

This is the payoff of keeping the archive outside the database: the store is derived
state, so losing it costs an offline rescan rather than a re-download.

After state loss run, in this order:
  1. rebuild    relink what is already on disk
  2. inventory  re-discover the catalogue (~22 requests)
  3. fetch      resume; already-held documents are skipped`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, lim, err := open(cmd)
			if err != nil {
				return err
			}
			defer a.Close()
			return runWith(a, lim, a.crawl.Rebuild)
		},
	}
}

// status prints manifest and archive totals plus the live pacing.
func status(ctx context.Context, a *app, lim *limiter.Limiter) error {
	counts, total, err := a.store.CountByState(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("state store: %s\n", a.cfg.StoreDir())
	fmt.Printf("archive:     %s\n", a.arch.Dir())
	fmt.Printf("inventory resources: %d\n", total)
	for _, st := range []store.State{store.StateFetched, store.StatePending, store.StateSkipped, store.StateFailed, store.StateGone} {
		if n := counts[st]; n > 0 {
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

	fmt.Printf("pacing now:  ")
	for _, s := range lim.States() {
		kind := "adaptive"
		if s.Pinned {
			kind = "pinned"
		}
		fmt.Printf("%s=%s(%s) ", s.Name, s.Interval, kind)
	}
	fmt.Println()
	return nil
}
