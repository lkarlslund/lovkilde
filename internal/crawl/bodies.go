// Sidecar bodies: fetching the text the canonical XML does not carry.
//
// Roughly half of the law corpus has no <DokumentIndhold> element in its
// canonical ELI XML: the /xml endpoint answers with metadata only, and the
// document's text exists nowhere in the mirror. The site does have it, in the
// documentHtml of the same JSON payload its own pages are built from
// (POST {site}/api/document/eli/{path}), so a body backfill is possible.
//
// The three parts here are the ones that decision actually costs something:
// detection, which is a property of the files and not of the state store; a
// queue in its own keyspace, because a document can simultaneously be "we hold
// the canonical XML" and "we still need its body"; and the payload write, which
// goes beside the XML with a different extension and never into it.
package crawl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lkarlslund/retsinformation/internal/archive"
	"github.com/lkarlslund/retsinformation/internal/docapi"
	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/rihttp"
	"github.com/lkarlslund/retsinformation/internal/store"
)

// JSONExt is the sidecar representation's extension: data/xml/lta/1995/498.json
// beside data/xml/lta/1995/498.xml. The raw upstream payload is kept rather than
// the extracted documentHtml, because the rest of the payload (isHistorical,
// alternativeMedia, pdfLink) is the triage information that says whether a
// body-less document is prose, a table document, or available only as PDF.
const JSONExt = lexdania.SidecarExt

// minBodyTextChars is the line between "this document has a body" and "the
// answer came back with nothing in it".
//
// It is deliberately low. Measured across the pilot corpus the shortest real
// law text was 341 characters, so anything near zero is not a short law, it is
// a payload with a heading and nothing else. Setting a higher bar would quietly
// discard short bekendtgørelser, and the payloads are on disk anyway.
const minBodyTextChars = 40

// dokumentIndhold is the element that carries an XML document's text. Its
// absence is what makes a document a backfill candidate, and it is searched for
// as bytes rather than parsed because the question is asked of 60k+ files.
var dokumentIndhold = []byte("<DokumentIndhold")

// BodyScanReport is what the detection pass learned, printed so the operator can
// decide scope before spending requests on it.
type BodyScanReport struct {
	// Scanned counts XML files examined.
	Scanned int64
	// WithXMLBody counts files whose canonical XML already carries the text.
	WithXMLBody int64
	// Held counts documents whose sidecar is already on disk.
	Held int64
	// Queued counts rows written to the body queue by this pass.
	Queued int64
	// Existing counts candidates already in the queue, whose state the scan
	// leaves alone -- a document recorded as gone or empty must not be
	// re-queued as pending or the next run requests it again.
	Existing int64
	// Alias counts accn/ files skipped: an accession path is a second route to a
	// document the archive also holds under its own ELI path.
	Alias int64
}

// ScanBodies walks the archive and queues every document that has no XML body and
// no sidecar yet.
//
// Detection comes from the files, not from the state store, because the archive is the
// product and the store is derived: after a store loss the rebuild path has to be
// able to re-derive the queue from disk alone.
func (c *Crawler) ScanBodies(ctx context.Context, opt FetchOptions) (BodyScanReport, error) {
	if opt.Batch <= 0 {
		opt.Batch = 500
	}
	var rep BodyScanReport
	root := c.archive.Dir()
	var batch []store.Body

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		keys := make([]string, len(batch))
		for i, b := range batch {
			keys[i] = b.ELIURL
		}
		// Only documents with no row at all get a pending row. Reading the
		// existing ones is what keeps a re-scan from turning every 404 and every
		// table-only document back into work, which on 29k documents is the
		// difference between a census and a re-crawl.
		got, err := c.store.Bodies(ctx, keys)
		if err != nil {
			return err
		}
		have := make(map[string]bool, len(got))
		for _, b := range got {
			have[b.ELIURL] = true
		}
		fresh := slices.DeleteFunc(batch, func(b store.Body) bool { return have[b.ELIURL] })
		if err := c.store.PutBodyBatch(ctx, fresh); err != nil {
			return err
		}
		rep.Queued += int64(len(fresh))
		rep.Existing += int64(len(batch) - len(fresh))
		batch = batch[:0]
		return nil
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name := d.Name()
		if !strings.HasSuffix(name, archive.XMLExt) || strings.HasPrefix(name, ".tmp-") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		eliURL := c.eliURLForPath(rel)
		if eliURL == "" {
			return nil
		}
		rep.Scanned++
		kind := eliKind(eliURL)
		if kind == "accn" || kind == "unmapped" {
			// An accession path aliases a document the archive also stores under
			// its own ELI path, so queueing it would spend a second request on the
			// same payload.
			rep.Alias++
			return nil
		}
		if len(opt.Kinds) > 0 && !slices.Contains(opt.Kinds, kind) {
			return nil
		}
		if c.archive.HasExt(eliURL, JSONExt) {
			rep.Held++
			return nil
		}
		xmlBody, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(xmlBody, dokumentIndhold) {
			rep.WithXMLBody++
			return nil
		}
		batch = append(batch, store.Body{ELIURL: eliURL, Kind: kind, State: store.StatePending})
		if len(batch) >= opt.Batch {
			return flush()
		}
		return nil
	})
	if err != nil {
		return rep, err
	}
	if err := flush(); err != nil {
		return rep, err
	}
	return rep, nil
}

// PendingBodyCount is PendingCount for the body queue: the progress bar needs its
// denominator before the run starts.
func (c *Crawler) PendingBodyCount(ctx context.Context, limit int, f store.Filter) (int64, error) {
	byKind, err := c.store.PendingBodiesByKind(ctx, f)
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

// FetchBodies downloads the site JSON payload for every queued document that has
// no sidecar yet, paced by the same adaptive document bucket as the XML fetch.
//
// Idempotency is the filesystem's, exactly as in Fetch: a sidecar that already
// exists is closed without a request, so an interrupted backfill resumes free.
// What the filesystem cannot answer is which documents have nothing to fetch, and
// that is what the queue rows are for.
func (c *Crawler) FetchBodies(ctx context.Context, opt FetchOptions) error {
	if opt.Batch <= 0 {
		opt.Batch = 200
	}
	var attempted, stored, held, empty, failed, throttled, bytes int64
	start := time.Now()

	filter := store.Filter{Kinds: opt.Kinds, States: opt.States, RetryAfter: store.RetryFailedAfter, Order: opt.Order}
	total, err := c.PendingBodyCount(ctx, opt.Limit, filter)
	if err != nil {
		return err
	}
	nextBatch, err := batchSourceT(c, ctx, filter, opt, c.bodyQueue())
	if err != nil {
		return err
	}

	report := func() {
		if opt.OnProgress == nil {
			return
		}
		opt.OnProgress(FetchStats{
			Attempted: attempted, Stored: stored, Skipped: held,
			Failed: failed, Bytes: bytes, Total: total,
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

			// A held payload closes without a request unless the document was reported
			// changed since: then the payload on disk is the older text.
			if c.archive.HasExt(row.ELIURL, JSONExt) && row.State != store.StateStale {
				if err := c.closeBodyWithoutFetch(ctx, row); err != nil {
					return err
				}
				held++
				report()
				continue
			}

			set := func(b store.Body) error {
				if b.State != store.StateFetched && b.State != store.StateEmpty {
					b.TriedAt = triedNow()
				}
				return c.store.PutBody(ctx, b)
			}
			eliPath := docapi.PathFromELIURL(row.ELIURL)
			if eliPath == "" {
				if err := set(store.Body{ELIURL: row.ELIURL, Kind: row.Kind, State: store.StateSkipped, Error: "not an ELI resource URL"}); err != nil {
					return err
				}
				report()
				continue
			}

			payload, err := c.doc.FetchJSON(ctx, eliPath)
			if rihttp.Throttled(err) {
				throttled++ // the row stays as it was: the next run asks again
				report()
				continue
			}
			if err != nil {
				var sc *rihttp.StatusCodeError
				if errors.As(err, &sc) && (sc.StatusCode == http.StatusNotFound || sc.StatusCode == http.StatusGone) {
					slog.Warn("no body representation", "eli", row.ELIURL, "status", sc.StatusCode)
					if perr := set(store.Body{ELIURL: row.ELIURL, Kind: row.Kind, State: store.StateGone, Error: sc.Status}); perr != nil {
						return perr
					}
					report()
					continue
				}
				failed++
				slog.Warn("body fetch failed", "eli", row.ELIURL, "err", err)
				if perr := set(store.Body{ELIURL: row.ELIURL, Kind: row.Kind, State: store.StateFailed, Error: err.Error()}); perr != nil {
					return perr
				}
				report()
				continue
			}

			doc, err := docapi.ParseJSON(payload)
			if err != nil {
				// A payload that is not the expected shape is not a property of the
				// document: it is a challenge page, a truncated response or an API
				// change. Failed, not gone -- treating this as terminal would retire
				// a document whose next response is fine.
				failed++
				slog.Warn("unparseable body payload", "eli", row.ELIURL, "err", err)
				if perr := set(store.Body{ELIURL: row.ELIURL, Kind: row.Kind, State: store.StateFailed, Error: err.Error()}); perr != nil {
					return perr
				}
				report()
				continue
			}

			hash, n, changed, err := c.archive.PutExt(row.ELIURL, JSONExt, payload)
			if err != nil {
				return fmt.Errorf("archive body %s: %w", row.ELIURL, err)
			}
			if changed {
				stored++
				bytes += n
			}

			chars := docapi.TextLen(doc.DocumentHTML)
			state := store.StateFetched
			if chars < minBodyTextChars {
				// Terminal, and the payload is still kept: "the site has no text for
				// this either" is a result, and the documents it applies to
				// (Finanslove and their tillægsbevillinger, published as tables) are
				// exactly the ones a future reader needs to know are unrecoverable
				// rather than unfetched.
				state = store.StateEmpty
				empty++
				slog.Debug("body payload has no text", "eli", row.ELIURL, "chars", chars)
			}
			if err := set(store.Body{
				ELIURL: row.ELIURL, Kind: row.Kind, State: state,
				Hash: hash, Bytes: n, TextChars: int64(chars),
			}); err != nil {
				return err
			}
			report()
		}

		if opt.Limit > 0 && int(attempted) >= opt.Limit {
			break
		}
	}

	slog.Info("body fetch complete", "attempted", attempted, "stored", stored, "already_held", held,
		"without_text", empty, "failed", failed, "throttled", throttled, "bytes", archive.SizeHint(bytes),
		"elapsed", time.Since(start).Round(time.Second).String())
	return nil
}

// closeBodyWithoutFetch records a sidecar already on disk without a request, the
// backfill's resume path.
func (c *Crawler) closeBodyWithoutFetch(ctx context.Context, row store.Body) error {
	payload, err := c.archive.GetExt(row.ELIURL, JSONExt)
	if err != nil {
		return err
	}
	hash, n, _, err := c.archive.PutExt(row.ELIURL, JSONExt, payload)
	if err != nil {
		return err
	}
	state := store.StateFetched
	var chars int64
	if doc, err := docapi.ParseJSON(payload); err == nil {
		chars = int64(docapi.TextLen(doc.DocumentHTML))
		if chars < minBodyTextChars {
			state = store.StateEmpty
		}
	}
	return c.store.PutBody(ctx, store.Body{
		ELIURL: row.ELIURL, Kind: row.Kind, State: state,
		Hash: hash, Bytes: n, TextChars: chars,
	})
}
