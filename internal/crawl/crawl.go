// Package crawl implements the mirror's phases. Each phase is independent and
// resumable, so any of them can be interrupted and repeated without wasting a
// single request beyond the one in flight.
//
// The phases, and why they are separate:
//
//	Inventory  sitemap pages only          ~22 requests for the full 203k catalogue
//	Harvest    harvesting service only     1 call per 10s, 10-day rolling window
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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lkarlslund/retsinformation/internal/archive"
	"github.com/lkarlslund/retsinformation/internal/config"
	"github.com/lkarlslund/retsinformation/internal/docapi"
	"github.com/lkarlslund/retsinformation/internal/harvest"
	"github.com/lkarlslund/retsinformation/internal/limiter"
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

// Harvest consumes the official change feed. It walks backwards from today only
// as far as it has not already recorded, because the service answers for a
// rolling 10-day window: entries older than that expire, so an outage longer
// than 10 days can only be repaired via the sitemap.
func (c *Crawler) Harvest(ctx context.Context) error {
	last, found, err := c.store.LastHarvestedDay(ctx)
	if err != nil {
		return err
	}
	today := time.Now().UTC()
	limit := harvest.MaxWindowDays
	if found {
		// Resume the day after the last completed one, capped at the window.
		span := int(today.UTC().Truncate(24*time.Hour).Sub(last.Truncate(24*time.Hour)).Hours()/24) + 1
		if span <= 0 {
			slog.Info("harvest already current", "last_day", last.Format(time.DateOnly))
			return nil
		}
		if span > limit {
			slog.Warn("harvest gap exceeds the service window; older changes are no longer available and need a sitemap inventory pass",
				"last_day", last.Format(time.DateOnly), "gap_days", span, "window_days", limit)
			span = limit
		}
		limit = span
	}

	var seen, stored int
	for _, day := range harvest.Days(today, limit) {
		docs, err := c.harvest.ForDay(ctx, day)
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
	return nil
}

// FetchOptions tunes the document download phase.
type FetchOptions struct {
	// Limit caps how many resources this call attempts; 0 means run until done.
	Limit int
	// Batch is how many pending rows to load at a time. Small batches keep
	// memory flat and make progress checkpoints frequent.
	Batch int
	// MaxAttempts bounds retries for a single resource.
	MaxAttempts int
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
	if opt.MaxAttempts <= 0 {
		opt.MaxAttempts = 5
	}
	var attempted, stored, skippedDup, failed, bytes int64
	start := time.Now()

	// The denominator comes from the state store rather than the batch loop, so
	// the bar can show a percentage from the first document. It counts pending
	// and retryable-failed rows without re-checking their attempt counts, which
	// is an estimate that errs slightly high and only affects the percentage.
	counts, _, err := c.store.CountByState(ctx)
	if err != nil {
		return err
	}
	totalPending := counts[store.StatePending] + counts[store.StateFailed]
	if opt.Limit > 0 && int64(opt.Limit) < totalPending {
		totalPending = int64(opt.Limit)
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

		pending, err := c.store.PendingInventory(ctx, want, opt.MaxAttempts)
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

			// Already on disk from a previous run? Close it without a request.
			if c.archive.Has(row.ELIURL) {
				if err := c.closeWithoutFetch(ctx, row); err != nil {
					return err
				}
				skippedDup++
				report()
				continue
			}

			url := xmlURLFor(c.cfg.SiteBase, row.ELIURL)
			if url == "" {
				if err := c.store.PutInventory(ctx, store.Inventory{ELIURL: row.ELIURL, LastMod: row.LastMod, Kind: row.Kind, State: store.StateSkipped, Attempts: row.Attempts + 1, Error: "not an ELI resource URL"}); err != nil {
					return err
				}
				report()
				continue
			}

			body, err := c.doc.FetchXMLURL(ctx, url)
			if err != nil {
				var sc *rihttp.StatusCodeError
				if errors.As(err, &sc) && (sc.StatusCode == http.StatusNotFound || sc.StatusCode == http.StatusGone) {
					slog.Warn("resource gone", "eli", row.ELIURL, "status", sc.StatusCode)
					if err := c.store.PutInventory(ctx, store.Inventory{ELIURL: row.ELIURL, LastMod: row.LastMod, Kind: row.Kind, State: store.StateGone, Attempts: row.Attempts + 1, Error: sc.Status}); err != nil {
						return err
					}
					report()
					continue
				}
				failed++
				slog.Warn("fetch failed", "eli", row.ELIURL, "err", err)
				if perr := c.store.PutInventory(ctx, store.Inventory{ELIURL: row.ELIURL, LastMod: row.LastMod, Kind: row.Kind, State: store.StateFailed, Attempts: row.Attempts + 1, Error: err.Error()}); perr != nil {
					return perr
				}
				report()
				continue
			}

			meta, err := docapi.ParseXMLMeta(body)
			if err != nil {
				failed++
				slog.Warn("unparseable xml", "eli", row.ELIURL, "err", err)
				if perr := c.store.PutInventory(ctx, store.Inventory{ELIURL: row.ELIURL, LastMod: row.LastMod, Kind: row.Kind, State: store.StateFailed, Attempts: row.Attempts + 1, Error: err.Error()}); perr != nil {
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

			in := store.Inventory{
				ELIURL: row.ELIURL, LastMod: row.LastMod, Kind: row.Kind,
				State: store.StateFetched, Attempts: row.Attempts + 1,
				Accession: meta.Accession, SHA256: hash, Bytes: n,
			}
			if err := c.store.PutInventory(ctx, in); err != nil {
				return err
			}
			// The accession index powers harvest resolution and reporting: it
			// answers "do we already hold the body the feed just told us about?"
			if acc, found, err := c.store.GetAccession(ctx, meta.Accession); err != nil {
				return err
			} else if !found || acc.SHA256 != hash {
				if err := c.store.PutAccession(ctx, store.Accession{
					Accession: meta.Accession, ELIURL: row.ELIURL, SHA256: hash, Bytes: n,
					ChangedAt: time.Now().UTC().Format(time.RFC3339),
				}); err != nil {
					return err
				}
			}

			// Every 50th document, report the interval actually in force: on a
			// multi-day crawl this is how you see the learner tightening or easing
			// the schedule rather than guessing from throughput.
			if attempted%50 == 0 {
				el := time.Since(start)
				interval, _ := c.client.Limiter().Current(limiter.BucketDocument)
				slog.Info("fetch progress", "attempted", attempted, "stored", stored, "already_held", skippedDup,
					"bytes", archive.SizeHint(bytes), "elapsed", el.Round(time.Second).String(),
					"rate", fmt.Sprintf("%.1f req/s", float64(attempted)/el.Seconds()),
					"document_interval", interval.String(),
					"interval_changes", c.client.Limiter().Steps())
			}
			report()
		}

		if opt.Limit > 0 && int(attempted) >= opt.Limit {
			break
		}
	}

	slog.Info("fetch complete", "attempted", attempted, "stored", stored, "already_held", skippedDup,
		"failed", failed, "bytes", archive.SizeHint(bytes),
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
		State: store.StateFetched, Attempts: row.Attempts,
		Accession: acc, SHA256: hash, Bytes: n,
	})
}

// Update is the intended recurring entry point: refresh the catalogue, consume
// the official change feed, then download whatever is new or changed.
func (c *Crawler) Update(ctx context.Context) error {
	if err := c.Inventory(ctx); err != nil {
		return fmt.Errorf("inventory: %w", err)
	}
	if err := c.Harvest(ctx); err != nil {
		return fmt.Errorf("harvest: %w", err)
	}
	if err := c.Fetch(ctx, FetchOptions{}); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	return nil
}

// Rebuild re-derives manifest state from the XML already on disk. Needed only
// after state loss or suspicion: it makes zero HTTP requests, which is the whole
// payoff of keeping the archive outside the database.
//
// It walks every stored document, re-extracts its accession and hash, and marks
// the corresponding inventory row fetched. Rows with no file on disk are left
// pending, so a rebuild followed by fetch is always safe.
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
		in.State = store.StateFetched
		in.Accession = acc
		in.SHA256 = store.Sha256Hex(raw)
		in.Bytes = int64(len(raw))
		if err := c.store.PutInventory(ctx, in); err != nil {
			return err
		}
		if acc != "" {
			if _, found, err := c.store.GetAccession(ctx, acc); err != nil {
				return err
			} else if !found {
				if err := c.store.PutAccession(ctx, store.Accession{
					Accession: acc, ELIURL: eliURL, SHA256: in.SHA256, Bytes: in.Bytes,
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
