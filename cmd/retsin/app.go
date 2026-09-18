package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
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
	var bodies, bodiesScan bool
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
  retsin fetch --order random --limit 200  an unbiased sample

--bodies works on the other half of the corpus: documents whose canonical XML carries
metadata but no text at all. It scans the archive for those, queues the ones with no
.json sidecar yet, and fetches the payload the site's own pages are built from, storing
it beside the XML. Same pacing and same narrowing flags, but a separate queue, so an
XML row that says "fetched" keeps meaning "we hold the canonical XML":

  retsin fetch --bodies-scan                    the census, zero requests
  retsin fetch --bodies --kind lta --limit 50   fifty sidecars, lta only
  retsin fetch --bodies --state empty           re-test documents whose payload had no text`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, lim, err := open(cmd)
			if err != nil {
				return err
			}
			defer a.Close()
			kinds = cleanList(kinds)
			states = cleanList(states)
			if bodies || bodiesScan {
				opt := crawl.FetchOptions{
					Limit: limit, Batch: batch, Kinds: kinds, States: states,
					Order: order, RandomSeed: randomSeed,
				}
				return runWith(a, lim, func(ctx context.Context) error {
					return a.bodies(ctx, lim, bodiesScan, opt)
				})
			}
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
				bar := newFetchBar(lim, progressModeValue, pending, "fetch")
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
							bar.Describe(pacingLabel(lim, "fetch"))
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
	cmd.Flags().BoolVar(&bodies, "bodies", false,
		"operate on document bodies instead of XML: scan the archive for documents whose canonical XML has no "+
			"<DokumentIndhold>, queue those without a .json sidecar, then fetch the site JSON payload that carries "+
			"their text and write it beside the XML. Same pacing, same --kind/--limit/--state/--order narrowing")
	cmd.Flags().BoolVar(&bodiesScan, "bodies-scan", false,
		"detect and queue body candidates only, requesting nothing: this is how to get the census "+
			"(how many documents have no XML body) before deciding what to spend requests on")
	return cmd
}

// bodies runs the sidecar backfill: detection over the archive, then the paced
// fetch of whatever the queue holds.
//
// The scan runs first even on a plain --bodies run, because the queue has to
// reflect the archive rather than whatever was scanned last: XML arriving from the
// ordinary crawl creates new candidates. It walks the tree once (a couple of
// minutes over 60k files) and leaves existing rows alone, so it neither re-queues
// a document recorded as gone nor re-requests one whose payload had no text.
func (a *app) bodies(ctx context.Context, lim *limiter.Limiter, scanOnly bool, opt crawl.FetchOptions) error {
	rep, err := a.crawl.ScanBodies(ctx, opt)
	if err != nil {
		return err
	}
	slog.Info("body scan", "scanned", rep.Scanned, "xml_has_body", rep.WithXMLBody,
		"sidecar_held", rep.Held, "queued", rep.Queued, "already_queued", rep.Existing, "aliases_skipped", rep.Alias)
	if scanOnly {
		return nil
	}

	filter := store.Filter{Kinds: opt.Kinds, States: opt.States, Order: opt.Order}
	total, err := a.crawl.PendingBodyCount(ctx, opt.Limit, filter)
	if err != nil {
		return err
	}
	if len(opt.Kinds) > 0 || len(opt.States) > 0 {
		attrs := []any{"kinds", strings.Join(opt.Kinds, ","), "states", strings.Join(opt.States, ","), "total", total}
		if total == 0 {
			slog.Warn("no body candidates match these filters", attrs...)
		} else {
			slog.Info("body fetch filtered", attrs...)
		}
	}
	bar := newFetchBar(lim, progressModeValue, total, "bodies")
	defer bar.Finish()
	var barWarned bool
	return a.crawl.FetchBodies(ctx, crawl.FetchOptions{
		Limit: opt.Limit, Batch: opt.Batch, Kinds: opt.Kinds, States: opt.States,
		Order: opt.Order, RandomSeed: opt.RandomSeed,
		OnProgress: func(st crawl.FetchStats) {
			if err := bar.Set(st.Attempted); err != nil && !barWarned {
				barWarned = true
				slog.Warn("progress bar not updating", "err", err)
			}
			if bar.Enabled() {
				bar.Describe(pacingLabel(lim, "bodies"))
			}
		},
	})
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
func newFetchBar(lim *limiter.Limiter, mode progress.Mode, total int64, name string) *progress.Bar {
	bar := progress.NewBar(gate, name, total, mode)
	bar.Describe(pacingLabel(lim, name))
	return bar
}

// pacingLabel is the bar's leading text, refreshed as the learner retunes. The
// name distinguishes the two loops that share the document bucket: "fetch" is the
// canonical XML, "bodies" the sidecar payloads.
func pacingLabel(lim *limiter.Limiter, name string) string {
	d, ok := lim.Current(limiter.BucketDocument)
	if !ok {
		return name
	}
	label := name + " @" + d.Round(time.Millisecond).String()
	if j := lim.JitterPercent(); j > 0 {
		// Showing the jitter is what makes a --jitter change verifiable from the
		// terminal instead of from the source.
		label += fmt.Sprintf(" ±%.0f%%", j)
	}
	return label
}

func updateCmd(open openFunc) *cobra.Command {
	var full, force bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Harvest and fetch what changed since the last completed pass (the daily cron entry point)",
		Long: `Daily refill: consume the official change feed, download whatever is new or moved,
including the JSON body payloads, and record the sweep timestamp only if nothing
is left outstanding. Unchanged documents are never re-requested.

The feed answers for a rolling 10 days, so a mirror whose last completed pass is
older than that cannot be refilled honestly: the changes in the gap are no longer
listed anywhere. That run fails instead of collecting a partial set and leaving
the mirror looking current. Use --full after such an outage, or on a cold mirror:
it re-reads the ELI sitemap, which detects every change by lastmod and has full
scope for about 22 requests.

A full pass also tolerates a feed gap, because the sitemap pass covers the days
the feed has already forgotten.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, lim, err := open(cmd)
			if err != nil {
				return err
			}
			defer a.Close()
			opt := crawl.UpdateOptions{Full: full, Force: force}
			return runWith(a, lim, func(ctx context.Context) error {
				return a.crawl.Update(ctx, opt)
			})
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "re-read the ELI sitemap first: the only pass with full scope, needed on a cold mirror or after an outage longer than the feed window")
	cmd.Flags().BoolVar(&force, "force", false, "refill even though the last completed pass is older than the feed window; the gap will not be closed")
	return cmd
}

// changesCmd reports what the change feed said and what the mirror did about it.
//
// The harvest phase has always recorded every change, but nothing read them back,
// so "we harvested 2,195 changes" was unfalsifiable. Resolving each accession
// against the archive turns the feed into a check on the collection pipeline: it
// answers whether the sitemap's lastmod sees what the feed sees, which is the
// question that decides whether the feed has to drive downloads at all.
func changesCmd(open openFunc) *cobra.Command {
	var days, list int
	cmd := &cobra.Command{
		Use:   "changes",
		Short: "Report change-feed entries and whether the mirror collected them",
		Long: `Read back the harvested change feed and reconcile it against the archive.

Each feed entry names a document by accession. Those already in the archive resolve
to an ELI resource, whose inventory row then says what happened: still queued,
already re-fetched, and -- from the stored content timestamp -- whether new bytes
actually arrived. Entries whose accession is unknown are documents this mirror has
never seen, which the sitemap finds by lastmod but the feed alone could not fetch,
because the feed gives no ELI URL.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, _, err := open(cmd)
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := cmd.Context()

			since := time.Now().UTC().AddDate(0, 0, -days).Format(time.DateOnly)
			changes, err := a.store.Changes(ctx, since, 0)
			if err != nil {
				return err
			}
			if len(changes) == 0 {
				fmt.Printf("no harvested changes recorded since %s. Run: retsin harvest\n", since)
				return nil
			}

			var unknown, queued, behind, refetched, freshContent, missingRow int
			byDate := map[string]int{}
			type unresolved struct{ date, accession, reason, docType string }
			var unresolvedRows []unresolved

			for _, c := range changes {
				byDate[c.Date]++
				acc, found, err := a.store.GetAccession(ctx, c.Accession)
				if err != nil {
					return err
				}
				if !found {
					unknown++
					if list > 0 {
						unresolvedRows = append(unresolvedRows, unresolved{c.Date, c.Accession, c.Reason, c.DocType})
					}
					continue
				}
				row, rowFound, err := a.store.GetInventory(ctx, acc.ELIURL)
				if err != nil {
					return err
				}
				if !rowFound {
					// The accession index outlived the catalogue row: a resource the
					// sitemap no longer advertises, which no refill will revisit.
					missingRow++
					continue
				}
				switch row.State {
				case store.StatePending:
					queued++
				case store.StateStale:
					// The publisher says this changed and we hold the previous text:
					// the feed and the sitemap both did their job, and the next fetch
					// is what has to.
					behind++
				case store.StateGone, store.StateFailed, store.StateSkipped:
					missingRow++
				default:
					refetched++
					if t, err := time.Parse(time.RFC3339, acc.ChangedAt); err == nil && !t.Before(changeWindowStart(days)) {
						freshContent++
					}
				}
			}

			fmt.Printf("feed changes since %s: %d\n", since, len(changes))
			for _, d := range sortedKeys(byDate) {
				fmt.Printf("  %s  %d\n", d, byDate[d])
			}
			fmt.Printf("  resolved to a held resource:   %d\n", len(changes)-unknown)
			fmt.Printf("  accession unknown to mirror:   %d  (new documents; the feed gives no ELI URL, so only a sitemap pass can find them)\n", unknown)
			fmt.Printf("  queued now:                    %d\n", queued)
			fmt.Printf("  known changed, old text held:  %d  (the publisher republished it; the next fetch replaces it)\n", behind)
			fmt.Printf("  already collected:             %d  (%d of those got new bytes within the window)\n", refetched, freshContent)
			if missingRow > 0 {
				fmt.Printf("  no usable inventory row:       %d  (gone, failed, or no longer advertised)\n", missingRow)
			}
			fmt.Printf("note: \"new bytes\" comes from the stored content timestamp, which a rebuild also rewrites; it measures bytes that arrived, not that the publisher amended the text.\n")

			if list > 0 && len(unresolvedRows) > 0 {
				fmt.Printf("\nfirst %d unknown accessions (new to this mirror):\n", min(list, len(unresolvedRows)))
				for _, u := range unresolvedRows[:min(list, len(unresolvedRows))] {
					fmt.Printf("  %s  %-14s %-6s %s\n", u.date, u.accession, u.docType, u.reason)
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 10, "how far back to report, capped by the feed's own 10-day window")
	cmd.Flags().IntVar(&list, "list", 0, "also list up to N unknown accessions")
	return cmd
}

// changeWindowStart is the moment from which a freshly stored hash counts as
// "new bytes arrived" for a --days window, with a day of slack: the feed reports
// changes by date, not by hour, and the collection runs on either side of it.
func changeWindowStart(days int) time.Time {
	return time.Now().UTC().AddDate(0, 0, -days-1).Truncate(24 * time.Hour)
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
	for _, st := range []store.State{store.StateFetched, store.StateStale, store.StatePending, store.StateSkipped, store.StateFailed, store.StateGone} {
		if n := counts[st]; n > 0 {
			fmt.Printf("  %-8s %d\n", st, n)
		}
	}
	files, bytes, err := a.arch.Count()
	if err != nil {
		return err
	}
	fmt.Printf("archive files: %d, %s\n", files, archive.SizeHint(bytes))

	// The two representations are reported separately: a backfill adds files, and
	// a total that quietly doubles makes the XML corpus look like it grew.
	jsonFiles, jsonBytes, err := a.arch.CountExt(".json")
	if err != nil {
		return err
	}
	if jsonFiles > 0 {
		fmt.Printf("  canonical xml: %d, %s\n", files-jsonFiles, archive.SizeHint(bytes-jsonBytes))
		fmt.Printf("  body payloads: %d, %s\n", jsonFiles, archive.SizeHint(jsonBytes))
		bodyCounts, bodyTotal, err := a.store.CountBodiesByState(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("body queue: %d documents\n", bodyTotal)
		for _, st := range []store.State{store.StateFetched, store.StateEmpty, store.StatePending, store.StateGone, store.StateFailed, store.StateSkipped} {
			if n := bodyCounts[st]; n > 0 {
				fmt.Printf("  %-8s %d\n", st, n)
			}
		}
	}

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

	// The timestamp that decides whether the next daily refill can be trusted.
	// Reported as an age and a verdict rather than a bare date, because the date
	// means nothing without the 10-day feed window next to it.
	if ts, found, err := a.crawl.LastSweep(ctx); err != nil {
		return err
	} else if found {
		age := time.Since(ts)
		verdict := "incremental refill OK"
		if age > crawl.MaxSweepAge {
			verdict = fmt.Sprintf("STALE: beyond the %d-day feed window, run update --full", int(crawl.MaxSweepAge/(24*time.Hour)))
		}
		fmt.Printf("last completed sweep:    %s (%.1f h ago): %s\n", ts.Format(time.RFC3339), age.Hours(), verdict)
	} else {
		fmt.Printf("last completed sweep:    (never): incremental refill will fail until update --full finishes once\n")
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
