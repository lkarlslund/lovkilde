// Package crawl implements the mirror's phases. Each phase is independent and
// resumable, so any of them can be interrupted and repeated without wasting a
// single request beyond the one in flight.
//
// The phases, and why they are separate:
//
//	Inventory  sitemap pages only          ~22 requests for the full 203k catalogue
//	Harvest    harvesting service only     10-day rolling window
//	Fetch      document XML only           1 request per not-yet-held resource
//	Update     inventory + harvest + fetch  the daily cron entry point
//
// Nothing here runs concurrently: the binding constraint is courtesy to a
// third-party public service, so extra workers would only increase the chance
// of being rate-limited or blocked without finishing sooner.
package crawl

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lkarlslund/retsinformation/internal/archive"
	"github.com/lkarlslund/retsinformation/internal/config"
	"github.com/lkarlslund/retsinformation/internal/docapi"
	"github.com/lkarlslund/retsinformation/internal/harvest"
	"github.com/lkarlslund/retsinformation/internal/rihttp"
	"github.com/lkarlslund/retsinformation/internal/sitemap"
	"github.com/lkarlslund/retsinformation/internal/store"
)

// Crawler drives the phases against one shared store and archive.
type Crawler struct {
	cfg     config.Config
	store   *store.Store
	archive *archive.Archive
	doc     *docapi.API
	sitemap *sitemap.API
	harvest *harvest.API
	client  *rihttp.Client
}

// New wires a crawler.
func New(cfg config.Config, st *store.Store, ar *archive.Archive, client *rihttp.Client) *Crawler {
	return &Crawler{
		cfg:     cfg,
		store:   st,
		archive: ar,
		doc:     docapi.New(cfg, client),
		sitemap: sitemap.New(cfg, client),
		harvest: harvest.New(cfg, client),
		client:  client,
	}
}

// Inventory refreshes the catalogue from the ELI sitemap. This is the cheapest
// full-scope operation in the whole tool: ~22 requests discover every resource,
// and each entry carries a lastmod that later runs use to detect changes.
func (c *Crawler) Inventory(ctx context.Context) error {
	start := time.Now()
	var total int
	err := c.sitemap.Inventory(ctx, func(page int, entries []sitemap.Entry) error {
		rows := make([]store.Inventory, 0, len(entries))
		for _, e := range entries {
			rows = append(rows, store.Inventory{
				ELIURL:  e.Loc,
				LastMod: e.LastMod.Format(time.DateOnly),
				Kind:    eliKind(e.Loc),
				State:   store.StatePending,
			})
		}
		if err := c.store.PutInventoryBatch(ctx, rows); err != nil {
			return fmt.Errorf("sitemap page %d: store: %w", page, err)
		}
		total += len(rows)
		slog.Info("sitemap page ingested", "page", page, "entries", len(entries), "total", total)
		return nil
	})
	if err != nil {
		return err
	}
	if err := c.store.SetMeta(ctx, "last_sitemap_inventory", time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	slog.Info("inventory complete", "resources", total, "elapsed", time.Since(start).Round(time.Second).String())
	return nil
}

// maxThrottled is how many times a harvest day is asked for while the service throttles. The
// harvest bucket doubles its interval on every throttle, so the calls are a minute apart well
// before the attempts run out.
const maxThrottled = 10

// Harvest consumes the official change feed. It walks backwards from today only
// as far as it has not already recorded, because the service answers for a
// rolling 10-day window: entries older than that expire, so an outage longer
// than 10 days can only be repaired via the sitemap.
//
// When the gap is longer than the window it still collects what remains
// available -- throwing away recoverable changes would be silly -- and then
// returns ErrFeedGap, because the run is a partial refill and only the caller
// knows whether a sitemap pass is on this same run to cover the rest. A daily
// cron that ignores this error is reporting success while silently missing days.
func (c *Crawler) Harvest(ctx context.Context) error {
	last, found, err := c.store.LastHarvestedDay(ctx)
	if err != nil {
		return err
	}
	today := time.Now().UTC()
	limit := harvest.MaxWindowDays
	var gapDays int
	if found {
		// Resume the day after the last completed one, capped at the window.
		span := int(today.UTC().Truncate(24*time.Hour).Sub(last.Truncate(24*time.Hour)).Hours()/24) + 1
		if span <= 0 {
			slog.Info("harvest already current", "last_day", last.Format(time.DateOnly))
			return nil
		}
		if span > limit {
			gapDays = span
			slog.Warn("harvest gap exceeds the service window; older changes are no longer available and need a sitemap inventory pass",
				"last_day", last.Format(time.DateOnly), "gap_days", span, "window_days", limit)
			span = limit
		}
		limit = span
	}

	var seen, stored int
	for _, day := range harvest.Days(today, limit) {
		docs, err := c.harvest.ForDay(ctx, day)
		// A throttled day is asked for again: the client has already slowed the harvest bucket
		// down (and honoured any Retry-After), so the next call waits as long as the service
		// asked. Giving up on the first 429 ended the whole update over one busy moment.
		for attempt := 1; rihttp.Throttled(err) && attempt < maxThrottled; attempt++ {
			slog.Info("harvest throttled, asking again once the pacing allows", "day", day.Format(time.DateOnly), "attempt", attempt)
			docs, err = c.harvest.ForDay(ctx, day)
		}
		if err != nil {
			// A 400 means the date fell outside the window: stop walking back
			// rather than retrying days that can never succeed.
			var sc *rihttp.StatusCodeError
			if errors.As(err, &sc) && sc.StatusCode == http.StatusBadRequest {
				slog.Info("reached edge of harvest window", "day", day.Format(time.DateOnly))
				break
			}
			return err
		}
		changes := make([]store.Change, 0, len(docs))
		for _, d := range docs {
			changes = append(changes, store.Change{
				Date:       normalizeDate(d.ChangeDate),
				Accession:  d.Accessionsnummer,
				Reason:     string(d.ReasonForChange),
				DocumentID: d.DocumentID,
				DocType:    d.DocumentType.ShortName,
				Href:       d.Href,
				SeenAt:     time.Now().UTC().Format(time.RFC3339),
			})
		}
		if err := c.store.PutChanges(ctx, changes); err != nil {
			return err
		}
		if err := c.store.MarkDayHarvested(ctx, day); err != nil {
			return err
		}
		if err := c.store.SetMeta(ctx, "last_harvest_day", day.Format(time.DateOnly)); err != nil {
			return err
		}
		seen += len(docs)
		stored++
		slog.Info("harvested day", "day", day.Format(time.DateOnly), "changes", len(docs))
	}
	slog.Info("harvest complete", "days", stored, "changes_seen", seen)
	if gapDays > 0 {
		return fmt.Errorf("%w: last harvested %s, %d days ago; only the newest %d days were collected, and the remaining %d are gone from the service. Run a sitemap inventory pass (retsin-update inventory) to find the changes by lastmod",
			ErrFeedGap, last.Format(time.DateOnly), gapDays, stored, gapDays-stored)
	}
	return nil
}

// FetchOptions tunes the document download phase.
type FetchOptions struct {
	// Limit caps how many resources this call attempts; 0 means run until done.
	Limit int
	// Kinds restricts the pass to specific ELI kinds ("retsinfo", "lta", ...);
	// empty means every kind. It exists so a kind can be sampled or repaired
	// without first crawling everything that sorts before it.
	Kinds []string
	// States restricts the pass to inventory rows in these states ("failed",
	// "gone", ...); empty means the ordinary sweep: pending and stale rows, and
	// failures last tried store.RetryFailedAfter ago. Naming a state also lifts that
	// wait, so failures can be retried on purpose.
	States []string
	// Batch is how many pending rows to load at a time. Small batches keep
	// memory flat and make progress checkpoints frequent.
	Batch int
	// Order is the sequence the selection is walked in: "asc" (default), "desc", or
	// "random". It exists because the three questions people ask of a 200k catalogue
	// need three different orders: finish it chronologically, take the newest law
	// first, or take an unbiased sample of it.
	Order string
	// RandomSeed fixes the shuffle for Order=random; zero seeds from the clock.
	// Recorded at debug level so a sample can be reproduced.
	RandomSeed int64
	// OnProgress, when set, is called after every resource with running
	// totals. It runs on the crawl goroutine, so it must not block: it exists
	// to drive a progress bar, not to do work.
	OnProgress func(FetchStats)
}

// FetchStats are the running totals handed to FetchOptions.OnProgress.
type FetchStats struct {
	Attempted int64
	Stored    int64
	Skipped   int64
	Failed    int64
	Bytes     int64
	// Total is the denominator: resources this run is expected to work
	// through, 0 when it could not be estimated.
	Total int64
}

// PendingCount reports how many resources a fetch pass would take on, honouring
// the same --kind and --limit narrowing the loop does. The CLI needs this
// *before* starting: a progress bar must be built with its denominator already in
// place, so the count is exposed rather than reported mid-run.
//
// It counts pending and retryable-failed rows without re-checking their attempt
// counts, which errs slightly high and only affects the bar's percentage.
// PendingCount reports how many rows a fetch will take on, which is the
// denominator the progress bar needs at construction. The filter is applied by the
// store, so this is one scan rather than a full count narrowed afterwards.
func (c *Crawler) PendingCount(ctx context.Context, limit int, f store.Filter) (int64, error) {
	byKind, err := c.store.PendingByKind(ctx, f)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, n := range byKind {
		total += n
	}
	if limit > 0 && int64(limit) < total {
		total = int64(limit)
	}
	return total, nil
}

// queue is one of the store's work queues specialised for the fetch loops: the
// XML inventory and the body sidecar queue differ in record type and in which
// store methods they call, not in how a batch is handed out.
type queue[T any] struct {
	pending func(context.Context, int, store.Filter) ([]T, error)
	keys    func(context.Context, store.Filter) ([]string, error)
	byKeys  func(context.Context, []string) ([]T, error)
	key     func(T) string
}

// inventoryQueue is the queue the XML fetch loop drains.
func (c *Crawler) inventoryQueue() queue[store.Inventory] {
	return queue[store.Inventory]{
		pending: c.store.PendingInventory,
		keys:    c.store.PendingKeys,
		byKeys:  c.store.Inventories,
		key:     func(in store.Inventory) string { return in.ELIURL },
	}
}

// bodyQueue is the queue the sidecar fetch loop drains.
func (c *Crawler) bodyQueue() queue[store.Body] {
	return queue[store.Body]{
		pending: c.store.PendingBodies,
		keys:    c.store.PendingBodyKeys,
		byKeys:  c.store.Bodies,
		key:     func(b store.Body) string { return b.ELIURL },
	}
}

// batchSource returns the function the fetch loop pulls its next batch from, so the
// per-row work below stays shared whatever order was asked for.
//
// asc and desc page the keyspace by cursor: each batch resumes on the far side of
// the last row handed out. Without that the next batch would re-scan from the start
// and offer again any row whose state still matches the filter -- the normal outcome
// for a retry, since a 404 re-tried under --state gone stays gone -- so an unbounded
// run would loop over the same rows forever and a progress bar would climb past its
// total. It also stops a full crawl re-walking 200k rows once per batch.
//
// random cannot page: the keyspace is stored in ELI URL order, so a shuffled order
// has no cursor to resume from. Materialising the selection once and draining it
// costs a few tens of megabytes of URLs on a run that asked for it, which beats
// re-scanning the whole catalogue per batch to re-derive the same shuffle.
func (c *Crawler) batchSource(ctx context.Context, filter store.Filter, opt FetchOptions) (func(context.Context, int) ([]store.Inventory, error), error) {
	return batchSourceT(c, ctx, filter, opt, c.inventoryQueue())
}

// batchSourceT is batchSource for any queue, so the cursor and shuffle handling
// above is written once rather than once per representation.
func batchSourceT[T any](c *Crawler, ctx context.Context, filter store.Filter, opt FetchOptions, q queue[T]) (func(context.Context, int) ([]T, error), error) {
	if filter.Order != store.OrderRandom {
		return func(ctx context.Context, want int) ([]T, error) {
			rows, err := q.pending(ctx, want, filter)
			if err != nil {
				return nil, err
			}
			if len(rows) > 0 {
				filter.Cursor = q.key(rows[len(rows)-1])
			}
			return rows, nil
		}, nil
	}

	keys, err := q.keys(ctx, filter)
	if err != nil {
		return nil, err
	}
	seed := opt.RandomSeed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	// Logged because a random sample that cannot be named afterwards is not
	// reproducible, and this is the order people use for sampling.
	slog.Debug("random fetch order", "seed", seed, "selected", len(keys))
	rnd := rand.New(rand.NewSource(seed))
	rnd.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })

	pos := 0
	return func(ctx context.Context, want int) ([]T, error) {
		if pos >= len(keys) || want <= 0 {
			return nil, nil
		}
		end := pos + want
		if end > len(keys) {
			end = len(keys)
		}
		page := keys[pos:end]
		pos = end
		return q.byKeys(ctx, page)
	}, nil
}

// Fetch downloads canonical XML for every resource not already on disk.
//
// Idempotency comes from the filesystem: a resource whose file already exists
// is closed without any request at all, so restarting a multi-day crawl costs
// nothing, and a second full pass after completion issues zero document
// requests.
func (c *Crawler) Fetch(ctx context.Context, opt FetchOptions) error {
	if opt.Batch <= 0 {
		opt.Batch = 200
	}
	var attempted, stored, skippedDup, failed, throttled, bytes int64
	start := time.Now()

	// The denominator comes from the state store rather than the batch loop, so
	// the bar can show a percentage from the first document. For the default sweep
	// it counts failed rows without checking whether they are due, which is an
	// estimate that errs slightly high and only affects the percentage.
	// Hand-picked states are counted exactly.
	filter := store.Filter{Kinds: opt.Kinds, States: opt.States, RetryAfter: store.RetryFailedAfter, Order: opt.Order}
	totalPending, err := c.PendingCount(ctx, opt.Limit, filter)
	if err != nil {
		return err
	}
	nextBatch, err := c.batchSource(ctx, filter, opt)
	if err != nil {
		return err
	}

	report := func() {
		if opt.OnProgress == nil {
			return
		}
		opt.OnProgress(FetchStats{
			Attempted: attempted, Stored: stored, Skipped: skippedDup,
			Failed: failed, Bytes: bytes, Total: totalPending,
		})
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := opt.Batch
		if opt.Limit > 0 {
			remaining := opt.Limit - int(attempted)
			if remaining <= 0 {
				break
			}
			if remaining < want {
				want = remaining
			}
		}

		pending, err := nextBatch(ctx, want)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			break
		}

		for _, row := range pending {
			if err := ctx.Err(); err != nil {
				return err
			}
			attempted++

			// Already on disk from a previous run? Close it without a request --
			// unless the publisher says that copy is out of date, which is the one
			// case where the file on disk is the reason to ask again rather than the
			// reason not to.
			if c.archive.Has(row.ELIURL) && row.State != store.StateStale {
				if err := c.closeWithoutFetch(ctx, row); err != nil {
					return err
				}
				skippedDup++
				report()
				continue
			}

			url := xmlURLFor(c.cfg.SiteBase, row.ELIURL)
			if url == "" {
				if err := c.store.PutInventory(ctx, store.Inventory{ELIURL: row.ELIURL, LastMod: row.LastMod, Kind: row.Kind, State: store.StateSkipped, TriedAt: triedNow(), Error: "not an ELI resource URL"}); err != nil {
					return err
				}
				report()
				continue
			}

			body, err := c.doc.FetchXMLURL(ctx, url)
			if rihttp.Throttled(err) {
				// Not a refusal: the row stays as it was, so the next run asks again rather
				// than the weekly retry of documents the site refused.
				throttled++
				report()
				continue
			}
			if err != nil {
				var sc *rihttp.StatusCodeError
				if errors.As(err, &sc) && (sc.StatusCode == http.StatusNotFound || sc.StatusCode == http.StatusGone) {
					slog.Warn("resource gone", "eli", row.ELIURL, "status", sc.StatusCode)
					if err := c.store.PutInventory(ctx, store.Inventory{ELIURL: row.ELIURL, LastMod: row.LastMod, Kind: row.Kind, State: store.StateGone, TriedAt: triedNow(), Error: sc.Status}); err != nil {
						return err
					}
					report()
					continue
				}
				failed++
				slog.Warn("fetch failed", "eli", row.ELIURL, "err", err)
				if perr := c.store.PutInventory(ctx, store.Inventory{ELIURL: row.ELIURL, LastMod: row.LastMod, Kind: row.Kind, State: store.StateFailed, TriedAt: triedNow(), Error: err.Error()}); perr != nil {
					return perr
				}
				report()
				continue
			}

			meta, err := docapi.ParseXMLMeta(body)
			if err != nil {
				// An HTML answer to an /xml ELI path is definitive: the resource
				// has no XML representation, so another attempt can only repeat
				// the same result. Record it like a 404, otherwise a fixed set of
				// dead representations gets re-requested on every future pass.
				if errors.Is(err, docapi.ErrNotXML) {
					slog.Warn("no xml representation", "eli", row.ELIURL, "bytes", len(body))
					if perr := c.store.PutInventory(ctx, store.Inventory{ELIURL: row.ELIURL, LastMod: row.LastMod, Kind: row.Kind, State: store.StateGone, TriedAt: triedNow(), Error: err.Error()}); perr != nil {
						return perr
					}
					report()
					continue
				}
				failed++
				slog.Warn("unparseable xml", "eli", row.ELIURL, "err", err)
				if perr := c.store.PutInventory(ctx, store.Inventory{ELIURL: row.ELIURL, LastMod: row.LastMod, Kind: row.Kind, State: store.StateFailed, TriedAt: triedNow(), Error: err.Error()}); perr != nil {
					return perr
				}
				report()
				continue
			}

			hash, n, changed, err := c.archive.Put(row.ELIURL, body)
			if err != nil {
				return fmt.Errorf("archive %s: %w", row.ELIURL, err)
			}
			if changed {
				stored++
				bytes += n
			}

			// The accession is read before the row is written: it lives on the row that holds
			// it, so afterwards it would report the new hash and no change would ever show.
			acc, found, err := c.store.GetAccession(ctx, meta.Accession)
			if err != nil {
				return err
			}
			in := store.Inventory{
				ELIURL: row.ELIURL, LastMod: row.LastMod, Kind: row.Kind,
				State:     store.StateFetched,
				Accession: meta.Accession, Hash: hash, Bytes: n,
			}
			if err := c.store.PutInventory(ctx, in); err != nil {
				return err
			}
			// The accession index powers harvest resolution and reporting: it
			// answers "do we already hold the body the feed just told us about?" —
			// and, through FetchedAt, "did we fetch it after the feed said it changed?"
			now := time.Now().UTC().Format(time.RFC3339)
			rec := store.Accession{Accession: meta.Accession, ELIURL: row.ELIURL, Hash: hash, Bytes: n, ChangedAt: acc.ChangedAt, FetchedAt: now}
			if !found || acc.Hash != hash {
				rec.ChangedAt = now
				// New XML for a document we held: its body payload, where it has one,
				// is the older text now, and a held payload is otherwise never asked for.
				if found {
					if err := c.markBodyStale(ctx, row.ELIURL); err != nil {
						return err
					}
				}
			}
			if err := c.store.PutAccession(ctx, rec); err != nil {
				return err
			}

			report()
		}

		if opt.Limit > 0 && int(attempted) >= opt.Limit {
			break
		}
	}

	slog.Info("fetch complete", "attempted", attempted, "stored", stored, "already_held", skippedDup,
		"failed", failed, "throttled", throttled, "bytes", archive.SizeHint(bytes),
		"elapsed", time.Since(start).Round(time.Second).String())
	return nil
}

// closeWithoutFetch marks a resource fetched using bytes already on disk, which
// is the resume path: no request, no rewrite.
func (c *Crawler) closeWithoutFetch(ctx context.Context, row store.Inventory) error {
	body, err := c.archive.Get(row.ELIURL)
	if err != nil {
		return err
	}
	hash, n, _, err := c.archive.Put(row.ELIURL, body) // re-verifies and reports size
	if err != nil {
		return err
	}
	acc := row.Accession
	if acc == "" {
		if meta, err := docapi.ParseXMLMeta(body); err == nil {
			acc = meta.Accession
		}
	}
	return c.store.PutInventory(ctx, store.Inventory{
		ELIURL: row.ELIURL, LastMod: row.LastMod, Kind: row.Kind,
		State:     store.StateFetched,
		Accession: acc, Hash: hash, Bytes: n,
	})
}

// metaLastSweep records the moment a collection pass finished with *nothing left
// to collect*. A timestamp set when the inventory merely completed would be a
// lie after a run that died with 40,000 documents pending: the next day's refill
// would look current while the mirror was days behind.
const metaLastSweep = "last_full_sweep"

// MaxSweepAge is how long a completed sweep keeps an incremental refill honest.
// It is the change feed's own window, not a taste: past it, the days in between
// are no longer retrievable at all, so continuing to "refill" would produce a
// mirror that looks up to date and silently omits every change in the gap.
const MaxSweepAge = harvest.MaxWindowDays * 24 * time.Hour

// ErrFeedGap means the change feed could not cover the time since the last
// harvested day: some changed documents are no longer listed by the service and
// must be found by a sitemap inventory pass instead.
var ErrFeedGap = errors.New("change feed gap")

// ErrStaleSweep means the mirror has not completed a collection pass recently
// enough for an incremental refill to be trustworthy, so the caller must run a
// full pass rather than accept a partial picture.
var ErrStaleSweep = errors.New("stale collection sweep")

// UpdateOptions tunes the daily entry point.
type UpdateOptions struct {
	// Full runs the sitemap inventory first, which is the only pass with full
	// scope. Incremental refills skip it because the feed plus the stored
	// lastmod values cover a day's change, and a cold mirror needs it once.
	Full bool
	// Force runs the incremental refill regardless of the staleness gate. It
	// exists for repairing a mirror deliberately, not for cron.
	Force bool
}

func (c *Crawler) Update(ctx context.Context, opt UpdateOptions) error {
	last, found, err := c.LastSweep(ctx)
	if err != nil {
		return err
	}
	if !opt.Full && !opt.Force {
		if !found {
			return fmt.Errorf("%w: this mirror has never completed a collection pass, so there is nothing to refill incrementally. Run the full pass once: retsin-update update --full", ErrStaleSweep)
		}
		if age := time.Since(last); age > MaxSweepAge {
			return fmt.Errorf("%w: the last completed pass was %s (%.1f days ago), older than the %d-day change feed. The changes in between are no longer listed by the service, so a refill would only collect part of them and leave the mirror looking current. Run retsin-update update --full, which finds every change by sitemap lastmod",
				ErrStaleSweep, last.Format(time.RFC3339), age.Hours()/24, harvest.MaxWindowDays)
		}
	}

	if opt.Full {
		if err := c.Inventory(ctx); err != nil {
			return fmt.Errorf("inventory: %w", err)
		}
	}

	if err := c.Harvest(ctx); err != nil {
		// A full pass already detected changes by lastmod, so a feed gap there
		// costs metadata detail but not documents. An incremental refill has no
		// such fallback, so the gap is fatal there.
		if !errors.Is(err, ErrFeedGap) || !opt.Full {
			return fmt.Errorf("harvest: %w", err)
		}
		slog.Warn("continuing past the feed gap: this run re-inventoried the sitemap, which covers the missing days")
	}

	if err := c.QueueFeedChanges(ctx); err != nil {
		return fmt.Errorf("queue feed changes: %w", err)
	}
	if err := c.Fetch(ctx, FetchOptions{}); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if err := c.FetchBodies(ctx, FetchOptions{}); err != nil {
		return fmt.Errorf("fetch bodies: %w", err)
	}

	// Whatever the pass managed, it records the attempt: when it pulled, and a
	// fingerprint of what it could not get. A mirror can only hold what the site serves,
	// so a run that came home with holes is still the record of a run, and the holes are
	// data rather than a reason to hide the timestamp. "pulled 3 hours ago, 14
	// unresolved" is a usable fact; silence because one document answers HTTP 500 is not.
	now := time.Now().UTC()
	issues, err := c.Issues(ctx)
	if err != nil {
		return err
	}
	rec := SweepRecord{PulledAt: now, Issues: issues, IssuesKnown: true}
	if err := c.store.SetMeta(ctx, metaLastSweep, rec.String()); err != nil {
		return err
	}
	if err := PublishSweepRecord(c.cfg.SweepStampPath(), rec); err != nil {
		slog.Warn("could not publish the sweep record", "path", c.cfg.SweepStampPath(), "err", err)
	}
	if issues.Total() > 0 {
		slog.Warn("collection pass recorded with unresolved work",
			"at", now.Format(time.RFC3339),
			"owed", issues.Owed, "failed", issues.Failed, "issues", issues.Digest,
			"remedy", "owed rows are fetched by the next update; failed ones are retried weekly, or now with retsin-update fetch --state failed (and --bodies --state failed)")
	} else {
		slog.Info("collection pass recorded", "at", now.Format(time.RFC3339))
	}
	return nil
}

// Issues counts what both queues left out: documents not fetched yet, and documents the
// site refused.
func (c *Crawler) Issues(ctx context.Context) (store.Issues, error) {
	xml, err := c.store.InventoryIssues(ctx)
	if err != nil {
		return store.Issues{}, err
	}
	body, err := c.store.BodyIssues(ctx)
	if err != nil {
		return store.Issues{}, err
	}
	return xml.Merge(body), nil
}

// triedNow is the time a request that did not end in content is recorded under.
func triedNow() string { return time.Now().UTC().Format(time.RFC3339) }

// LastSweep reports when a collection pass last recorded itself. It is a convenience
// over LastSweepRecord for the callers that only need the date.
func (c *Crawler) LastSweep(ctx context.Context) (time.Time, bool, error) {
	rec, found, err := c.LastSweepRecord(ctx)
	if err != nil || !found {
		return time.Time{}, found, err
	}
	return rec.PulledAt, true, nil
}

// LastSweepRecord reports the account the last collection pass left behind: when it
// pulled, and a fingerprint of what it could not get.
func (c *Crawler) LastSweepRecord(ctx context.Context) (SweepRecord, bool, error) {
	v, found, err := c.store.GetMeta(ctx, metaLastSweep)
	if err != nil || !found {
		return SweepRecord{}, found, err
	}
	rec, err := ParseSweepRecord(v)
	if err != nil {
		// A corrupt record must not be read as "recently pulled", which is what parsing
		// to the zero time would mean in the age comparison. Fail loudly.
		return SweepRecord{}, false, fmt.Errorf("reading %s: %w", metaLastSweep, err)
	}
	return rec, true, nil
}

func (c *Crawler) Rebuild(ctx context.Context) error {
	var seen, matched, orphans int64
	start := time.Now()

	err := filepath.WalkDir(c.archive.Dir(), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".tmp-") || !strings.HasSuffix(d.Name(), ".xml") {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		seen++
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(c.archive.Dir(), path)
		if err != nil {
			return err
		}
		eliURL := c.eliURLForPath(rel)
		if eliURL == "" {
			orphans++
			return nil
		}
		in, found, err := c.store.GetInventory(ctx, eliURL)
		if err != nil {
			return err
		}
		if !found {
			// A file whose ELI resource is no longer listed by the sitemap: keep
			// it, and record it so the archive remains explainable.
			in = store.Inventory{ELIURL: eliURL, Kind: eliKind(eliURL)}
		}
		meta, err := docapi.ParseXMLMeta(raw)
		acc := ""
		if err == nil {
			acc = meta.Accession
		}
		// A row the publisher has moved on from stays stale: a rebuild re-derives
		// what is on disk, and what is on disk is exactly the older version.
		// Downgrading it here would erase the one signal that the copy is behind.
		if in.State != store.StateStale {
			in.State = store.StateFetched
		}
		in.Accession = acc
		in.Hash = archive.Hash(raw)
		in.Bytes = int64(len(raw))
		if err := c.store.PutInventory(ctx, in); err != nil {
			return err
		}
		if acc != "" {
			if _, found, err := c.store.GetAccession(ctx, acc); err != nil {
				return err
			} else if !found {
				if err := c.store.PutAccession(ctx, store.Accession{
					Accession: acc, ELIURL: eliURL, Hash: in.Hash, Bytes: in.Bytes,
					ChangedAt: time.Now().UTC().Format(time.RFC3339),
				}); err != nil {
					return err
				}
			}
		}
		matched++
		if matched%5000 == 0 {
			slog.Info("rebuild progress", "scanned", seen, "linked", matched, "orphans", orphans,
				"elapsed", time.Since(start).Round(time.Second).String())
		}
		return nil
	})
	if err != nil {
		return err
	}
	slog.Info("rebuild complete", "scanned", seen, "linked", matched, "orphans", orphans,
		"elapsed", time.Since(start).Round(time.Second).String())
	return nil
}

// eliURLForPath inverts the archive's path mapping for the subset of shapes it
// produces, so a rebuilt store still keys on canonical ELI URLs.
func (c *Crawler) eliURLForPath(rel string) string {
	rel = filepath.ToSlash(rel)
	rel = strings.TrimSuffix(rel, ".xml")
	segs := strings.Split(rel, "/")
	if len(segs) < 2 {
		return ""
	}
	kind := segs[0]
	base := strings.TrimSuffix(c.cfg.SiteBase, "/")
	switch kind {
	case "accn":
		// accn/<rank>/<year>/<accession>
		if len(segs) == 4 {
			return base + "/eli/accn/" + segs[3]
		}
		return ""
	case "ft":
		// ft/<year>/<full-id> where the id embeds its own year
		if len(segs) == 3 {
			return base + "/eli/ft/" + segs[2]
		}
		return ""
	default:
		// <kind>/<year>/<leaf> where leaf is the final ELI path segment
		if len(segs) == 3 {
			return base + "/eli/" + kind + "/" + segs[1] + "/" + segs[2]
		}
		return base + "/eli/" + strings.Join(segs, "/")
	}
}

func normalizeDate(s string) string {
	s = s[:min(len(s), 10)]
	// The feed mixes YYYY-MM-DD with DD-MM-YYYY; store one canonical form.
	if len(s) == 10 && s[4] == '-' {
		return s
	}
	if len(s) == 10 && s[2] == '-' && s[5] == '-' {
		return s[6:10] + "-" + s[3:5] + "-" + s[0:2]
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// eliKind extracts the ELI family from a resource URL, i.e. the path segment
// directly after /eli/: retsinfo, lta, ft, fob, mt, accn.
func eliKind(eliURL string) string {
	i := strings.Index(eliURL, "/eli/")
	if i < 0 {
		return ""
	}
	rest := eliURL[i+len("/eli/"):]
	if j := strings.IndexAny(rest, "/?."); j >= 0 {
		return rest[:j]
	}
	return rest
}

// xmlURLFor appends the /xml representation suffix to an ELI resource URL.
//
// Verified against production for every kind present in the sitemap: the site
// serves the canonical XSD-validated document for any ELI resource path plus
// /xml, so no intermediate lookup request is needed to find an accession first.
func xmlURLFor(siteBase, eliURL string) string {
	i := strings.Index(eliURL, "/eli/")
	if i < 0 {
		return ""
	}
	path := strings.TrimSuffix(eliURL[i:], "/")
	if j := strings.IndexByte(path[len("/eli/"):], '?'); j >= 0 {
		path = path[:len("/eli/")+j]
	}
	// Drop a representation suffix such as ".rdfa" if one is present.
	if slash := strings.LastIndexByte(path, '/'); slash >= 0 {
		if dot := strings.IndexByte(path[slash+1:], '.'); dot >= 0 {
			path = path[:slash+1+dot]
		}
	}
	return strings.TrimSuffix(siteBase, "/") + path + "/xml"
}

// QueueFeedChanges marks for download every held document the change feed reported changed
// after we last fetched it.
//
// The sitemap's lastmod is what marks a held document stale, and it does not move for every
// change the feed reports: on 2026-10-06, 77 documents the feed listed as changed since
// 2026-10-02 still carried lastmods from years earlier, and neither pass would have fetched
// them. A metadata change is not cosmetic — it is how the register marks a law historic — so a
// feed entry first seen after our last fetch makes the copy stale, and the body payload with
// it when the document has one. A copy fetched after the entry was first seen already includes
// the change.
func (c *Crawler) QueueFeedChanges(ctx context.Context) error {
	since := time.Now().UTC().AddDate(0, 0, -harvest.MaxWindowDays).Format(time.DateOnly)
	changes, err := c.store.Changes(ctx, since, 0)
	if err != nil {
		return err
	}
	queued, bodies := 0, 0
	seen := map[string]bool{}
	for _, ch := range changes {
		acc, found, err := c.store.GetAccession(ctx, ch.Accession)
		if err != nil {
			return err
		}
		if !found || seen[acc.ELIURL] || !HeldBefore(acc, ch) {
			continue
		}
		row, ok, err := c.store.GetInventory(ctx, acc.ELIURL)
		if err != nil {
			return err
		}
		if !ok || row.State != store.StateFetched {
			continue // pending, stale, failed or gone: already someone else's business
		}
		seen[acc.ELIURL] = true
		row.State = store.StateStale
		if err := c.store.PutInventory(ctx, row); err != nil {
			return err
		}
		queued++
		marked, err := c.markBodyStaleCount(ctx, acc.ELIURL)
		if err != nil {
			return err
		}
		bodies += marked
	}
	slog.Info("feed changes queued", "documents", queued, "bodies", bodies, "window_since", since)
	return nil
}

// HeldBefore says whether the stored copy of a document predates a change the feed reported:
// it was last fetched before the change was first seen.
func HeldBefore(acc store.Accession, ch store.Change) bool {
	held := acc.HeldSince()
	return held == "" || ch.SeenAt == "" || held < ch.SeenAt
}

// bodyKey is the body queue's key for a document. The queue is seeded from archive paths under
// the configured site origin (eliURLForPath), while the inventory and the accession index keep
// the sitemap's spelling, which has no "www": the same document, two URLs.
func (c *Crawler) bodyKey(eliURL string) string {
	path := docapi.PathFromELIURL(eliURL)
	if path == "" {
		return eliURL
	}
	return strings.TrimSuffix(c.cfg.SiteBase, "/") + "/" + path
}

// markBodyStale queues a held body payload for download again.
func (c *Crawler) markBodyStale(ctx context.Context, eliURL string) error {
	_, err := c.markBodyStaleCount(ctx, eliURL)
	return err
}

func (c *Crawler) markBodyStaleCount(ctx context.Context, eliURL string) (int, error) {
	b, ok, err := c.store.GetBody(ctx, c.bodyKey(eliURL))
	if err != nil || !ok || b.State != store.StateFetched {
		return 0, err
	}
	b.State = store.StateStale
	return 1, c.store.PutBody(ctx, b)
}
