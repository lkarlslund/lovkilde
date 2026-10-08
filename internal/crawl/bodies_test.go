package crawl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/archive"
	"github.com/lkarlslund/retsinformation/internal/config"
	"github.com/lkarlslund/retsinformation/internal/limiter"
	"github.com/lkarlslund/retsinformation/internal/rihttp"
	"github.com/lkarlslund/retsinformation/internal/store"
)

// stubXML is what the canonical endpoint actually serves for older documents: the
// record, no <DokumentIndhold>, and therefore no text anywhere in the mirror.
const stubXML = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <DocumentType>Lov</DocumentType>
    <AccessionNumber>A19950049829</AccessionNumber>
    <Titel>Bekendtgørelse af skattekontrolloven</Titel>
    <Status>Historic</Status>
  </Meta>
</Dokument>
`

// fullXML is the same shape with the body element present.
const fullXML = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta><AccessionNumber>A19630000729</AccessionNumber></Meta>
  <DokumentIndhold id="id1">
    <Paragraf id="id2"><Explicatus>§ 1.</Explicatus></Paragraf>
  </DokumentIndhold>
</Dokument>
`

// bodyPayload builds the site's document payload shape: a one-element array.
func bodyPayload(html string) string {
	p, err := json.Marshal([]map[string]any{{"id": 62968, "documentHtml": html, "isHistorical": true}})
	if err != nil {
		panic(err)
	}
	return string(p)
}

// realBodyHTML is the modern-generation markup the site serves: classes that map
// onto the XML element names, and a paragraph number in its own span.
const realBodyHTML = `<div class="bjelke">Den fulde tekst</div>` +
	`<p class="Titel2">Bekendtgørelse af skattekontrolloven</p>` +
	`<p class="Paragraf" id="id3880dfc9"><span class="ParagrafNr">§ 1.</span> Det er formålet at føre kontrol med skattepligten.</p>`

// bodyHarness is a crawler wired to a temp archive and an httptest origin.
type bodyHarness struct {
	c        *Crawler
	st       *store.Store
	ar       *archive.Archive
	base     string
	requests *atomic.Int64
}

// eli builds the ELI URL the crawler itself derives for a path, because that URL is
// the queue key: hardcoding the production host here would look up a row the
// crawler never wrote and pass by accident.
func (h *bodyHarness) eli(path string) string { return h.base + "/eli/" + path }

func bodyTester(t *testing.T, handler http.HandlerFunc) *bodyHarness {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.SiteBase = srv.URL
	cfg.RequestTimeout = 5 * time.Second

	st, err := store.Open(filepath.Join(dir, "meta", "kv"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ar, err := archive.Open(dir)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	lim := limiter.NewTuned(map[string]limiter.Spec{
		limiter.BucketDocument: {Start: time.Microsecond, Floor: time.Microsecond, Max: time.Millisecond},
	}, 1.5, 2, time.Millisecond)
	return &bodyHarness{c: New(cfg, st, ar, rihttp.New(cfg, lim)), st: st, ar: ar, base: srv.URL, requests: &requests}
}

// writeDoc puts a file where the archive's own path mapping says it belongs, so the
// test cannot drift from the mapping the scan relies on.
func writeDoc(t *testing.T, ar *archive.Archive, eliURL, contents string) string {
	dest := ar.Path(eliURL)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dest, []byte(contents), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dest
}

// everyState is what a queue assertion has to ask for: the default sweep offers only
// pending rows, so "nothing in the queue" checked with it would pass on a queue full
// of failures.
var everyState = []string{
	string(store.StatePending), string(store.StateFetched), string(store.StateFailed),
	string(store.StateSkipped), string(store.StateGone), string(store.StateEmpty),
}

// The scan is what turns "we hold no text for this document" into work, so getting
// it wrong in either direction is expensive: too eager and the 33k documents with
// real XML bodies get requested anyway, too strict and the backfill finds nothing.
func TestScanBodiesQueuesOnlyBodylessDocuments(t *testing.T) {
	h := bodyTester(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodyPayload(realBodyHTML)))
	})
	ctx := context.Background()

	bodyless := h.eli("lta/1995/498")
	withBody := h.eli("lta/1963/7")
	held := h.eli("lta/1955/195")
	writeDoc(t, h.ar, bodyless, stubXML)
	writeDoc(t, h.ar, withBody, fullXML)
	writeDoc(t, h.ar, held, stubXML)
	if _, _, _, err := h.ar.PutExt(held, JSONExt, []byte(bodyPayload(realBodyHTML))); err != nil {
		t.Fatalf("sidecar: %v", err)
	}

	rep, err := h.c.ScanBodies(ctx, FetchOptions{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rep.Queued != 1 {
		t.Errorf("queued %d, want 1 (only the body-less document without a sidecar)", rep.Queued)
	}
	if rep.WithXMLBody != 1 {
		t.Errorf("with-xml-body %d, want 1", rep.WithXMLBody)
	}
	if rep.Held != 1 {
		t.Errorf("held %d, want 1 (a document with a sidecar is not work)", rep.Held)
	}
	if h.requests.Load() != 0 {
		t.Errorf("scan made %d requests, want 0: detection is a file walk", h.requests.Load())
	}

	rows, err := h.st.PendingBodies(ctx, 10, store.Filter{})
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(rows) != 1 || rows[0].ELIURL != bodyless {
		t.Fatalf("queue contents %v, want just the stub", rows)
	}
	if rows[0].Kind != "lta" {
		t.Errorf("kind %q, want lta (the --kind filter selects on it)", rows[0].Kind)
	}
}

// An accession path is a second route to a document the archive also stores under
// its own ELI path. Queueing both spends two requests on one payload.
func TestScanBodiesSkipsAccessionAliases(t *testing.T) {
	h := bodyTester(t, func(w http.ResponseWriter, _ *http.Request) {})
	ctx := context.Background()
	writeDoc(t, h.ar, h.eli("accn/A19950049829"), stubXML)

	rep, err := h.c.ScanBodies(ctx, FetchOptions{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rep.Alias != 1 || rep.Queued != 0 {
		t.Errorf("alias %d queued %d, want 1 and 0", rep.Alias, rep.Queued)
	}
	rows, err := h.st.PendingBodies(ctx, 10, store.Filter{States: everyState})
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("queue %v, want empty", rows)
	}
}

// Re-scanning must not turn recorded outcomes back into work. A document that 404s,
// or whose payload has no text, is a fact about the corpus; making it pending again
// on every scan is the mistake the XML path already refuses to make, and across the
// real corpus it is a re-crawl disguised as a census refresh.
func TestScanBodiesPreservesRecordedStates(t *testing.T) {
	h := bodyTester(t, func(w http.ResponseWriter, _ *http.Request) {})
	ctx := context.Background()
	gone := h.eli("lta/1906/158")
	empty := h.eli("lta/1990/110")
	writeDoc(t, h.ar, gone, stubXML)
	writeDoc(t, h.ar, empty, stubXML)
	if err := h.st.PutBody(ctx, store.Body{ELIURL: gone, Kind: "lta", State: store.StateGone, Error: "404 Not Found"}); err != nil {
		t.Fatalf("seed gone: %v", err)
	}
	if err := h.st.PutBody(ctx, store.Body{ELIURL: empty, Kind: "lta", State: store.StateEmpty, TextChars: 12}); err != nil {
		t.Fatalf("seed empty: %v", err)
	}

	rep, err := h.c.ScanBodies(ctx, FetchOptions{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rep.Queued != 0 {
		t.Errorf("queued %d on a re-scan, want 0", rep.Queued)
	}
	if rep.Existing != 2 {
		t.Errorf("existing %d, want 2", rep.Existing)
	}
	for _, tc := range []struct {
		eli  string
		want store.State
	}{{gone, store.StateGone}, {empty, store.StateEmpty}} {
		row, found, err := h.st.GetBody(ctx, tc.eli)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if !found || row.State != tc.want {
			t.Errorf("%s state %q found=%v, want %q", tc.eli, row.State, found, tc.want)
		}
	}
}

// The whole point of the backfill: the payload lands next to the XML it belongs to,
// the XML is not touched, and the row records how much text came with it.
func TestFetchBodiesWritesSidecarBesideXML(t *testing.T) {
	h := bodyTester(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodyPayload(realBodyHTML)))
	})
	ctx := context.Background()
	eli := h.eli("lta/1995/498")
	xmlPath := writeDoc(t, h.ar, eli, stubXML)

	if _, err := h.c.ScanBodies(ctx, FetchOptions{}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	var stats FetchStats
	if err := h.c.FetchBodies(ctx, FetchOptions{OnProgress: func(s FetchStats) { stats = s }}); err != nil {
		t.Fatalf("fetch bodies: %v", err)
	}
	if h.requests.Load() != 1 {
		t.Errorf("requests %d, want 1", h.requests.Load())
	}
	if stats.Stored != 1 {
		t.Errorf("stored %d, want 1", stats.Stored)
	}

	sidecar := strings.TrimSuffix(xmlPath, archive.XMLExt) + JSONExt
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("sidecar not written beside the xml: %v", err)
	}
	if filepath.Dir(sidecar) != filepath.Dir(xmlPath) {
		t.Errorf("sidecar dir %s, want %s", filepath.Dir(sidecar), filepath.Dir(xmlPath))
	}
	if now, err := os.ReadFile(xmlPath); err != nil || string(now) != stubXML {
		t.Errorf("canonical xml changed under the backfill")
	}

	row, found, err := h.st.GetBody(ctx, eli)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found || row.State != store.StateFetched {
		t.Fatalf("found=%v state %q, want fetched", found, row.State)
	}
	if row.TextChars <= minBodyTextChars {
		t.Errorf("recorded %d text chars, want the payload's real text length", row.TextChars)
	}
	if row.Hash == "" || row.Bytes == 0 {
		t.Errorf("row has no content hash or size: %+v", row)
	}
}

// A payload with no text is a result, not a failure: Finanslove are published as
// tables and no number of retries produces prose. Recording it as empty is what
// keeps it out of every future run, and storing the payload anyway is what lets a
// later reader tell "unrecoverable" from "never fetched".
func TestFetchBodiesRecordsPayloadWithoutText(t *testing.T) {
	h := bodyTester(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodyPayload(`<div class="bjelke">Den fulde tekst</div>`)))
	})
	ctx := context.Background()
	eli := h.eli("lta/2021/1361")
	path := writeDoc(t, h.ar, eli, stubXML)

	if _, err := h.c.ScanBodies(ctx, FetchOptions{}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if err := h.c.FetchBodies(ctx, FetchOptions{}); err != nil {
		t.Fatalf("fetch bodies: %v", err)
	}
	row, found, err := h.st.GetBody(ctx, eli)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found || row.State != store.StateEmpty {
		t.Fatalf("found=%v state %q, want empty", found, row.State)
	}
	if _, err := os.Stat(strings.TrimSuffix(path, archive.XMLExt) + JSONExt); err != nil {
		t.Errorf("payload was thrown away; want it stored with the empty verdict: %v", err)
	}
}

// Budget guard: whatever a first run concluded, a later run must spend zero requests
// on it. This is the property the request budget depends on.
func TestFetchBodiesSecondPassRequestsNothing(t *testing.T) {
	var gone atomic.Bool
	gone.Store(true)
	h := bodyTester(t, func(w http.ResponseWriter, _ *http.Request) {
		if gone.Load() {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodyPayload(realBodyHTML)))
	})
	ctx := context.Background()
	writeDoc(t, h.ar, h.eli("lta/1955/195"), stubXML)
	writeDoc(t, h.ar, h.eli("lta/1906/158"), stubXML)

	if _, err := h.c.ScanBodies(ctx, FetchOptions{}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if err := h.c.FetchBodies(ctx, FetchOptions{}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if h.requests.Load() != 2 {
		t.Fatalf("first pass made %d requests, want 2", h.requests.Load())
	}
	// Both are recorded as gone; the payload becoming available again must not
	// matter, because nothing is pending.
	gone.Store(false)
	if err := h.c.FetchBodies(ctx, FetchOptions{}); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if h.requests.Load() != 2 {
		t.Errorf("second pass made %d requests, want 0 more", h.requests.Load()-2)
	}
	// Naming the state is how an operator retries them deliberately.
	if err := h.c.FetchBodies(ctx, FetchOptions{States: []string{string(store.StateGone)}}); err != nil {
		t.Fatalf("retry by state: %v", err)
	}
	if h.requests.Load() != 4 {
		t.Errorf("--state gone made %d requests, want 2", h.requests.Load()-2)
	}
}

// Resume path: a sidecar already on disk is closed from the file, not re-requested,
// even while the queue row still says pending -- which is exactly the state left
// behind by a run that died between the write and the row.
func TestFetchBodiesClosesSidecarOnDiskWithoutRequest(t *testing.T) {
	h := bodyTester(t, func(w http.ResponseWriter, _ *http.Request) {})
	ctx := context.Background()
	eli := h.eli("lta/1932/41")
	writeDoc(t, h.ar, eli, stubXML)
	if err := h.st.PutBody(ctx, store.Body{ELIURL: eli, Kind: "lta", State: store.StatePending}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, _, err := h.ar.PutExt(eli, JSONExt, []byte(bodyPayload(realBodyHTML))); err != nil {
		t.Fatalf("sidecar: %v", err)
	}

	var stats FetchStats
	if err := h.c.FetchBodies(ctx, FetchOptions{OnProgress: func(s FetchStats) { stats = s }}); err != nil {
		t.Fatalf("fetch bodies: %v", err)
	}
	if h.requests.Load() != 0 {
		t.Errorf("requests %d, want 0: the bytes are already here", h.requests.Load())
	}
	if stats.Skipped != 1 {
		t.Errorf("skipped %d, want 1", stats.Skipped)
	}
	row, found, err := h.st.GetBody(ctx, eli)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found || row.State != store.StateFetched || row.TextChars <= minBodyTextChars {
		t.Fatalf("row %+v, want fetched with the text counted from the file", row)
	}
}

// A transient fault must stay retryable. Treating an unparseable payload as terminal
// would retire documents whose next response is fine -- and a challenge page arrives
// with HTTP 200, so status alone cannot make this call.
func TestFetchBodiesKeepsBadPayloadRetryable(t *testing.T) {
	var serving atomic.Bool
	h := bodyTester(t, func(w http.ResponseWriter, _ *http.Request) {
		if !serving.Load() {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<!DOCTYPE html><html><body>Attention Required</body></html>"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodyPayload(realBodyHTML)))
	})
	ctx := context.Background()
	eli := h.eli("lta/1946/368")
	writeDoc(t, h.ar, eli, stubXML)
	if _, err := h.c.ScanBodies(ctx, FetchOptions{}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if err := h.c.FetchBodies(ctx, FetchOptions{}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	row, _, err := h.st.GetBody(ctx, eli)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.State != store.StateFailed {
		t.Fatalf("state %q, want failed (a challenge page is not a property of the document)", row.State)
	}
	if _, err := h.ar.GetExt(eli, JSONExt); err == nil {
		t.Errorf("challenge page stored as a body payload")
	}

	// A week on, the failure is due again and the site is serving.
	row.TriedAt = time.Now().Add(-store.RetryFailedAfter - time.Hour).UTC().Format(time.RFC3339)
	if err := h.st.PutBody(ctx, row); err != nil {
		t.Fatal(err)
	}
	serving.Store(true)
	if err := h.c.FetchBodies(ctx, FetchOptions{}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	row, _, err = h.st.GetBody(ctx, eli)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.State != store.StateFetched {
		t.Fatalf("state %q after a clean retry, want fetched", row.State)
	}
}

// --kind has to narrow the backfill too: the whole-corpus run is ~100k documents
// across every kind, and the laws are one kind of it.
func TestFetchBodiesHonoursKindFilter(t *testing.T) {
	var (
		seenMut sync.Mutex
		seen    []string
	)
	h := bodyTester(t, func(w http.ResponseWriter, r *http.Request) {
		// The handler runs on the server goroutine while the crawler runs on the
		// test goroutine, so the record of what was asked for needs a lock.
		seenMut.Lock()
		seen = append(seen, r.URL.Path)
		seenMut.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodyPayload(realBodyHTML)))
	})
	ctx := context.Background()
	writeDoc(t, h.ar, h.eli("lta/1955/195"), stubXML)
	writeDoc(t, h.ar, h.eli("retsinfo/1980/12345"), stubXML)

	if _, err := h.c.ScanBodies(ctx, FetchOptions{Kinds: []string{"lta"}}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if err := h.c.FetchBodies(ctx, FetchOptions{Kinds: []string{"lta"}}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	seenMut.Lock()
	got := strings.Join(seen, " ")
	seenMut.Unlock()
	if h.requests.Load() != 1 {
		t.Fatalf("requests %d (%s), want 1 for the lta document only", h.requests.Load(), got)
	}
	rows, err := h.st.PendingBodies(ctx, 10, store.Filter{States: []string{string(store.StateFetched)}})
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(rows) != 1 || rows[0].Kind != "lta" {
		t.Fatalf("fetched rows %v, want one lta", rows)
	}
}

// The limit is the trial-run knob, and a run that overshoots it spends real
// requests: exactly the failure --limit exists to prevent.
func TestFetchBodiesStopsAtLimit(t *testing.T) {
	h := bodyTester(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodyPayload(realBodyHTML)))
	})
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		writeDoc(t, h.ar, fmt.Sprintf("%s/eli/lta/19%02d/7", h.base, 50+i), stubXML)
	}
	if _, err := h.c.ScanBodies(ctx, FetchOptions{}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	// Batch is deliberately larger than the limit: the clamp that matters has to
	// cut a queue batch down to what the limit still allows, not merely stop
	// between batches.
	if err := h.c.FetchBodies(ctx, FetchOptions{Limit: 2, Batch: 5}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if h.requests.Load() != 2 {
		t.Errorf("requests %d with --limit 2, want 2", h.requests.Load())
	}
}
