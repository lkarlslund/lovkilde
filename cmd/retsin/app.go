package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
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

// newLimiter builds the process-wide pacing schedule.
//
// One limiter for the whole process makes the published "once per 10 seconds"
// harvesting limit a structural guarantee rather than a convention. The harvest
// bucket is pinned: that limit is an obligation, not an observation, so there is
// nothing to learn from success and no room to be faster.
//
// Document and sitemap buckets are adaptive: the service publishes no limit for
// them and sends no Retry-After header, so the safe interval can only be found by
// probing, and must back off the moment it is told to slow down. Only their floors
// and the jitter come from the caller; the learner's shape is fixed in the limiter.
//
// This is a function rather than inline code in newApp so a test can assert the
// flags actually reach the limiter: a flag that is parsed and then dropped is worse
// than no flag at all.
func newLimiter(cfg config.Config) *limiter.Limiter {
	lim := limiter.New(map[string]limiter.Spec{
		limiter.BucketHarvest: {
			Start:  config.HarvestInterval,
			Floor:  config.HarvestInterval,
			Max:    config.HarvestInterval,
			Pinned: true,
		},
		limiter.BucketDocument: {
			Floor: cfg.DocumentFloor,
			Max:   limiter.MaxInterval,
		},
		limiter.BucketSitemap: {
			Floor: config.SitemapInterval,
			Max:   config.SitemapInterval * 8,
		},
	})
	lim.SetJitterPercent(cfg.JitterPercent)
	return lim
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

	lim := newLimiter(cfg)
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
	var kinds, states []string
	var order string
	var randomSeed int64
	cmd := &cobra.Command{
		Use:   "fetch",
		Short: "Download canonical ELI XML for anything not on disk",
		Long: `Download the canonical XML for every catalogued resource whose file is missing.

Requests are paced by the adaptive document bucket. Resources already on disk are
closed without any request at all, so a multi-day crawl can be interrupted at any
point and a second full pass after completion costs nothing.

By default resources are visited in URL order, so --kind is how a single series is sampled
or repaired without crawling everything that sorts before it, and --order picks the other
end of the keyspace instead:

  retsin fetch --kind retsinfo --limit 50

--state selects rows by what happened to them last, which is how failures get a
second chance. A default run takes pending rows plus failures still under the retry
limit; naming a state lifts that limit, so exhausted failures and 404s can be
retried on purpose:

  retsin fetch --state failed              retry everything that failed
  retsin fetch --state gone --kind retsinfo  re-test 404s in one series
  retsin fetch --state fetched             re-check rows that claim to be on disk

--order changes which end of the catalogue is reached first. The newest law is at the
top of the keyspace, so desc gets current legislation quickly where asc would spend
days on 1873 first; random samples the catalogue evenly, which is the honest order
for a trial run that should not be biased toward the old end:

  retsin fetch --order desc --limit 20     the twenty most recent documents
  retsin fetch --order random --limit 200  an unbiased sample`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, lim, err := open(cmd)
			if err != nil {
				return err
			}
			defer a.Close()
			kinds = cleanList(kinds)
			states = cleanList(states)
			return runWith(a, lim, func(ctx context.Context) error {
				filter := store.Filter{Kinds: kinds, States: states, Order: order}
				// Count first, then build the bar: it needs its denominator at
				// construction, and a run with nothing pending gets no bar at all.
				pending, err := a.crawl.PendingCount(ctx, limit, filter)
				if err != nil {
					return err
				}
				if len(kinds) > 0 || len(states) > 0 {
					// A filter that matches nothing is usually a typo, and silence
					// would look like a crawl that had nothing to do.
					attrs := []any{}
					if len(kinds) > 0 {
						attrs = append(attrs, "kinds", strings.Join(kinds, ","))
					}
					if len(states) > 0 {
						attrs = append(attrs, "states", strings.Join(states, ","))
					}
					if pending == 0 {
						slog.Warn("no resources match these filters", append(attrs, "total", pending)...)
					} else {
						slog.Info("fetch filtered", append(attrs, "total", pending)...)
					}
				}
				bar := newFetchBar(lim, progressModeValue, pending)
				defer bar.Finish()
				var barWarned bool
				return a.crawl.Fetch(ctx, crawl.FetchOptions{
					Limit: limit, Batch: batch, Kinds: kinds, States: states,
					Order: order, RandomSeed: randomSeed,
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
	cmd.Flags().StringSliceVar(&kinds, "kind", nil, "only fetch these ELI kinds, e.g. retsinfo or lta,ltb (repeatable)")
	cmd.Flags().StringSliceVar(&states, "state", nil,
		"only fetch rows in these states: pending, failed, gone, skipped, fetched (repeatable). "+
			"Default is pending plus failed rows under the retry limit; naming a state retries it however often it has been tried")
	cmd.Flags().StringVar(&order, "order", store.OrderAsc,
		"which end of the catalogue to visit first: asc, desc, random. "+
			"random shuffles the whole selection once, so the sample is not biased toward the old end of the keyspace")
	cmd.Flags().Int64Var(&randomSeed, "random-seed", 0,
		"seed for --order random; 0 picks one and logs it at debug level, so a sample can be repeated later")
	return cmd
}

// cleanList normalises a repeated or comma-separated list flag: trimmed,
// lower-cased, de-duplicated, empty entries dropped. Comparisons against stored
// values are exact, so shell history should not be able to miss by case or a stray
// space.
func cleanList(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// newFetchBar builds the live progress line for a fetch run, honouring
// --progress. total must be the number of items the run will take on: a bar with
// nothing to count is not built. The label carries the interval currently in
// force, which is the one number worth watching on a multi-day crawl: it shows
// the learner acting on what the origin is actually accepting.
func newFetchBar(lim *limiter.Limiter, mode progress.Mode, total int64) *progress.Bar {
	bar := progress.NewBar(gate, "fetch", total, mode)
	bar.Describe(pacingLabel(lim))
	return bar
}

// pacingLabel is the bar's leading text, refreshed as the learner retunes.
func pacingLabel(lim *limiter.Limiter) string {
	d, ok := lim.Current(limiter.BucketDocument)
	if !ok {
		return "fetch"
	}
	label := "fetch @" + d.Round(time.Millisecond).String()
	if j := lim.JitterPercent(); j > 0 {
		// Showing the jitter is what makes a --jitter change verifiable from the
		// terminal instead of from the source.
		label += fmt.Sprintf(" ±%.0f%%", j)
	}
	return label
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
