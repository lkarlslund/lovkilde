package crawl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/archive"
	"github.com/lkarlslund/retsinformation/internal/config"
	"github.com/lkarlslund/retsinformation/internal/limiter"
	"github.com/lkarlslund/retsinformation/internal/rihttp"
	"github.com/lkarlslund/retsinformation/internal/store"
)

// TestStaleContentCensus answers the question the fetch shortcut raises: when the
// sitemap says a resource was republished, do the bytes actually differ from what
// the archive holds?
//
// The fetch loop closes any row whose file is already on disk without asking the
// site -- which is what makes a re-pended catalogue cheap -- so a document amended
// after it was first downloaded is never re-requested. Whether that is a courtesy
// to the server or a correctness hole depends entirely on how often a moved lastmod
// means changed bytes, and nothing had measured that.
//
// Gated on RETSIN_STALE_CENSUS: it reads the live state store, which is locked
// while a crawl runs, and it makes real requests at the polite pace.
//
// Reproduce:
//
//	RETSIN_STALE_CENSUS=1 RETSIN_STALE_SAMPLE=25 RETSIN_STALE_SINCE=2026-09-17 \
//	  go test ./internal/crawl/ -run TestStaleContentCensus -count=1 -v
func TestStaleContentCensus(t *testing.T) {
	if os.Getenv("RETSIN_STALE_CENSUS") == "" {
		t.Skip("set RETSIN_STALE_CENSUS=1 to ask the live mirror whether a moved lastmod means new text")
	}
	dataDir := os.Getenv("RETSIN_DATA_DIR")
	if dataDir == "" {
		for _, cand := range []string{"data", "../../data"} {
			if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
				dataDir = cand
				break
			}
		}
	}
	if dataDir == "" {
		t.Skip("no data directory found")
	}
	sample := 25
	if v := os.Getenv("RETSIN_STALE_SAMPLE"); v != "" {
		if n, err := fmt.Sscanf(v, "%d", &sample); n != 1 || err != nil || sample < 1 {
			t.Fatalf("RETSIN_STALE_SAMPLE=%q is not a number", v)
		}
	}
	since := os.Getenv("RETSIN_STALE_SINCE")
	if since == "" {
		since = time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	}

	cfg := config.Default()
	cfg.DataDir = dataDir
	st, err := store.Open(cfg.StoreDir())
	if err != nil {
		t.Fatalf("store: %v (a crawl or another reader holds the lock)", err)
	}
	defer st.Close()
	ar, err := archive.Open(cfg.XMLDir())
	if err != nil {
		t.Fatalf("archive: %v", err)
	}

	ctx := context.Background()
	rows, err := st.HeldSince(ctx, since, 0)
	if err != nil {
		t.Fatalf("HeldSince: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("no held row claims a lastmod at or after %s", since)
	}
	totalHeldSince := len(rows)

	// The population that can be stale is narrower than "the publisher republished
	// this": a document downloaded after its own lastmod is current by definition,
	// and the sitemap is full of those -- newly published case law carries its
	// publication date as its lastmod, which is why a sample taken without this
	// filter came back 25 for 25 identical and told us nothing.
	//
	// The comparison needs to know when WE fetched, and file mtimes cannot say it:
	// this corpus was written in one sweep over two days, so every file looks newer
	// than almost every lastmod. The accession record's ChangedAt is the moment the
	// bytes arrived, which is the right side of the comparison.
	trap := make([]store.Inventory, 0, len(rows))
	var missing, unknownFetch int
	for _, row := range rows {
		weStored := ""
		if acc, found, err := st.GetAccession(ctx, row.Accession); err != nil {
			t.Fatalf("GetAccession %s: %v", row.Accession, err)
		} else if found {
			if ts, err := time.Parse(time.RFC3339, acc.ChangedAt); err == nil {
				weStored = ts.UTC().Format(time.DateOnly)
			}
		}
		if weStored == "" {
			// No accession row: fall back to the file's mtime and admit the answer
			// is weaker. Rows this pass merely closed locally keep no fetch stamp.
			if fi, err := os.Stat(ar.Path(row.ELIURL)); err == nil {
				weStored = fi.ModTime().UTC().Format(time.DateOnly)
			} else {
				missing++
				continue
			}
		}
		if weStored < row.LastMod {
			trap = append(trap, row)
		}
	}
	// A null result is the finding, not a reason to withhold the report: "nothing
	// in the archive is demonstrably behind the sitemap" is what a later reader
	// needs to see, and a test that exits early leaves no evidence behind.
	sampled := trap
	if len(trap) > sample {
		// Stride across the trap set rather than taking the first N, so the sample
		// is not one medium's alphabetical neighbourhood.
		stride := len(trap) / sample
		sampled = make([]store.Inventory, 0, sample)
		for i := 0; i < len(trap) && len(sampled) < sample; i += stride {
			sampled = append(sampled, trap[i])
		}
	}

	lim := limiter.NewTuned(map[string]limiter.Spec{
		limiter.BucketDocument: {Start: 200 * time.Millisecond, Floor: 200 * time.Millisecond, Max: 5 * time.Second},
	}, 1.5, 20, time.Minute)
	lim.SetJitterPercent(cfg.JitterPercent)
	c := New(cfg, st, ar, rihttp.New(cfg, lim))

	var same, different, errored int
	var differentBytes, heldBytes int64
	type entry struct {
		eli, lastMod, note string
	}
	var entries []entry
	start := time.Now()

	for _, row := range sampled {
		url := xmlURLFor(cfg.SiteBase, row.ELIURL)
		if url == "" {
			errored++
			entries = append(entries, entry{eli: row.ELIURL, lastMod: row.LastMod, note: "not an ELI resource URL"})
			continue
		}
		body, err := c.doc.FetchXMLURL(ctx, url)
		if err != nil {
			errored++
			entries = append(entries, entry{eli: row.ELIURL, lastMod: row.LastMod, note: err.Error()})
			continue
		}
		sum := sha256.Sum256(body)
		got := hex.EncodeToString(sum[:])
		// The archive records the hash of the bytes it stored, so an unchanged
		// document must reproduce it exactly. Anything else is text we do not have.
		if got == row.Hash {
			same++
			entries = append(entries, entry{eli: row.ELIURL, lastMod: row.LastMod, note: "identical"})
			continue
		}
		different++
		heldBytes += row.Bytes
		differentBytes += int64(len(body))
		entries = append(entries, entry{
			eli: row.ELIURL, lastMod: row.LastMod,
			note: fmt.Sprintf("CHANGED held=%s (%d B) now=%s (%d B)", shortHash(row.Hash), row.Bytes, shortHash(got), len(body)),
		})
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# stale content census\n")
	fmt.Fprintf(&b, "# reproduce: RETSIN_STALE_CENSUS=1 RETSIN_STALE_SAMPLE=%d RETSIN_STALE_SINCE=%s go test ./internal/crawl/ -run TestStaleContentCensus -count=1 -v\n", sample, since)
	fmt.Fprintf(&b, "# ran %s, elapsed %s\n\n", time.Now().UTC().Format(time.RFC3339), time.Since(start).Round(time.Second))
	if len(trap) == 0 {
		fmt.Fprintf(&b, "NOTHING BEHIND: no held row was stored before its own lastmod since %s\n", since)
		fmt.Fprintf(&b, "(checked %d rows; %d had no fetch stamp, %d had no file)\n\n", len(rows), unknownFetch, missing)
	}
	fmt.Fprintf(&b, "held rows with lastmod at or after %s: %d\n", since, totalHeldSince)
	fmt.Fprintf(&b, "  whose stored file predates that lastmod (the trap set): %d\n", len(trap))
	fmt.Fprintf(&b, "  with no file on disk at all:                            %d\n", missing)
	fmt.Fprintf(&b, "  sampled with a request (strided across the trap set):   %d\n", len(sampled))
	fmt.Fprintf(&b, "  bytes identical to what we hold:   %d\n", same)
	fmt.Fprintf(&b, "  bytes DIFFERENT from what we hold: %d  (held %.1f MiB, current %.1f MiB)\n", different, float64(heldBytes)/1e6, float64(differentBytes)/1e6)
	fmt.Fprintf(&b, "  request failed:                    %d\n\n", errored)
	for _, e := range entries {
		fmt.Fprintf(&b, "%-9s %s lastmod=%s %s\n", verdict(e.note), e.eli, e.lastMod, e.note)
	}
	if checked := same + different; checked > 0 {
		fmt.Fprintf(&b, "\nOf documents the sitemap reported as republished, %.0f%% really have different bytes.\n",
			float64(different)/float64(checked)*100)
		fmt.Fprintf(&b, "A refill that closes already-held rows without asking never learns about those.\n")
	}
	t.Log("\n" + b.String())
	if want := os.Getenv("RETSIN_STALE_REPORT"); want != "" {
		// go test runs in the package directory, so a path that works from the
		// command line is not the one that resolves here. Try both, and refuse to
		// lose a measurement that cost real requests.
		dest := want
		if !filepath.IsAbs(dest) {
			for _, cand := range []string{dest, filepath.Join("..", "..", dest), filepath.Join("..", dest)} {
				if fi, err := os.Stat(filepath.Dir(cand)); err == nil && fi.IsDir() {
					dest = cand
					break
				}
			}
		}
		if err := os.WriteFile(dest, []byte(b.String()), 0o644); err != nil {
			t.Fatalf("report %s: %v", dest, err)
		}
		t.Logf("report written to %s", dest)
	}
}

func verdict(note string) string {
	switch {
	case strings.HasPrefix(note, "CHANGED"):
		return "CHANGED"
	case note == "identical":
		return "identical"
	default:
		return "error"
	}
}

func shortHash(h string) string {
	if len(h) > 10 {
		return h[:10]
	}
	return h
}

// TestHeldSinceOrdering pins the query the census and status depend on: newest
// lastmod first, held rows only.
func TestHeldSinceOrdering(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "kv"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.PutInventoryBatch(ctx, []store.Inventory{
		{ELIURL: "https://x/eli/lta/1", LastMod: "2026-09-16", Kind: "lta", State: store.StateFetched, Hash: "a"},
		{ELIURL: "https://x/eli/lta/2", LastMod: "2026-09-18", Kind: "lta", State: store.StateFetched, Hash: "b"},
		{ELIURL: "https://x/eli/lta/3", LastMod: "2026-09-19", Kind: "lta", State: store.StatePending, Hash: "c"},
		{ELIURL: "https://x/eli/lta/4", LastMod: "2026-09-17", Kind: "lta", State: store.StateGone, Hash: "d"},
		// Stale rows are the interesting population for this query, so they count
		// as held: the publisher republished them and we hold an older copy.
		{ELIURL: "https://x/eli/lta/5", LastMod: "2026-09-19", Kind: "lta", State: store.StateStale, Hash: "e"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.HeldSince(ctx, "2026-09-16", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("HeldSince returned %d rows, want the 2 fetched ones and the stale one: %+v", len(got), got)
	}
	if got[0].ELIURL != "https://x/eli/lta/5" {
		t.Errorf("not newest-first: %+v", got)
	}
}

// TestFetchReRequestsStaleRows is the point of StateStale.
//
// The fetch loop closes any row whose file is already on disk without asking the
// site, which is what makes re-running an inventory pass affordable. It also means
// a law amended after its first download can never be updated: the row gets
// re-pended, the file is found, the row is closed, and the mirror keeps serving
// the superseded text while reporting itself current. A stale row therefore has to
// be the one row the on-disk shortcut does not apply to.
func TestFetchReRequestsStaleRows(t *testing.T) {
	current, err := os.ReadFile(filepath.Join("..", "docapi", "testdata", "fob-lok53.xml"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	// What the archive holds is the same document from before the amendment.
	superseded := append(append([]byte(nil), current...), '\n')

	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write(current)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.SiteBase = srv.URL
	cfg.RequestTimeout = 5 * time.Second
	st, err := store.Open(filepath.Join(dir, "meta", "kv"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()
	ar, err := archive.Open(dir)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	lim := limiter.NewTuned(map[string]limiter.Spec{
		limiter.BucketDocument: {Start: time.Microsecond, Floor: time.Microsecond, Max: time.Millisecond},
	}, 1.5, 2, time.Millisecond)
	c := New(cfg, st, ar, rihttp.New(cfg, lim))

	ctx := context.Background()
	eli := "https://www.retsinformation.dk/eli/lta/2026/1234"
	if _, _, _, err := ar.Put(eli, superseded); err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if err := st.PutInventory(ctx, store.Inventory{
		ELIURL: eli, LastMod: "2026-09-18", Kind: "lta", State: store.StateStale,
		Accession: "B202600123405", Hash: archive.Hash(superseded), Bytes: int64(len(superseded)),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var stats FetchStats
	if err := c.Fetch(ctx, FetchOptions{Batch: 10, OnProgress: func(s FetchStats) { stats = s }}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if requests != 1 {
		t.Errorf("stale row made %d requests, want 1: the on-disk shortcut swallowed an amended document", requests)
	}
	if stats.Skipped != 0 {
		t.Errorf("stale row was skipped as a duplicate (%d), which is the bug", stats.Skipped)
	}
	if stats.Stored != 1 {
		t.Errorf("stored %d, want 1: the new text did not replace the old", stats.Stored)
	}

	held, err := ar.Get(eli)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archive.Hash(held) != archive.Hash(current) {
		t.Error("the archive still holds the superseded bytes")
	}
	row, found, err := st.GetInventory(ctx, eli)
	if err != nil || !found {
		t.Fatalf("row vanished: found=%v err=%v", found, err)
	}
	if row.State != store.StateFetched || row.Hash != archive.Hash(current) {
		t.Errorf("stale row after the refill: %+v, want fetched with the new hash", row)
	}
	if row.LastMod != "2026-09-18" {
		t.Errorf("lastmod %q, want the publisher's 2026-09-18 preserved", row.LastMod)
	}

	// The cheap path must survive the fix: now that the row is fetched and its
	// lastmod has not moved again, a further pass must not re-request it.
	if err := c.Fetch(ctx, FetchOptions{Batch: 10}); err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if requests != 1 {
		t.Errorf("second pass made %d requests, want none: a healthy document must stay free", requests)
	}
}

// TestInventoryPassMarksHeldRowsStale covers the half that detects the change at
// all: an inventory pass sees a moved lastmod on a row whose bytes it holds, and
// must record that as stale rather than pending. Pending is the state the fetch
// loop answers from disk, so pending would silently discard the signal.
func TestInventoryPassMarksHeldRowsStale(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "kv"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	held := store.Inventory{
		ELIURL: "https://x/eli/lta/7", LastMod: "2026-01-01", Kind: "lta", State: store.StateFetched,
		Accession: "B20260000705", Hash: "oldhash", Bytes: 4000,
	}
	if err := s.PutInventory(ctx, held); err != nil {
		t.Fatal(err)
	}
	// What a sitemap page hands the store: identity plus lastmod, pending.
	if err := s.PutInventoryBatch(ctx, []store.Inventory{
		{ELIURL: held.ELIURL, LastMod: "2026-09-18", Kind: "lta", State: store.StatePending},
	}); err != nil {
		t.Fatal(err)
	}

	got, found, err := s.GetInventory(ctx, held.ELIURL)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got.State != store.StateStale {
		t.Errorf("state %q after a moved lastmod, want %q: pending is closed from disk and the change would be lost",
			got.State, store.StateStale)
	}
	if got.Hash != "oldhash" || got.Bytes != 4000 || got.Accession != "B20260000705" {
		t.Errorf("marking stale lost what we hold: %+v", got)
	}
}

// TestRebuildKeepsRowsStale covers the repair path: rebuild re-derives state from
// the bytes on disk, and for a stale row those bytes are precisely the older
// version. Trusting disk here would erase the signal with no trace.
func TestRebuildKeepsRowsStale(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "docapi", "testdata", "fob-lok53.xml"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	st, err := store.Open(filepath.Join(dir, "meta", "kv"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()
	ar, err := archive.Open(dir)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	c := New(cfg, st, ar, rihttp.New(cfg, nil))
	ctx := context.Background()

	eli := "https://www.retsinformation.dk/eli/lta/2026/777"
	if _, _, _, err := ar.Put(eli, doc); err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if err := st.PutInventory(ctx, store.Inventory{
		ELIURL: eli, LastMod: "2026-09-18", Kind: "lta", State: store.StateStale,
		Hash: "whatever", Bytes: 1,
	}); err != nil {
		t.Fatal(err)
	}

	if err := c.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	row, found, err := st.GetInventory(ctx, eli)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if row.State != store.StateStale {
		t.Errorf("rebuild rewrote a stale row as %q: the file on disk is the older copy, which is the whole reason the row is stale", row.State)
	}
	if row.Hash != archive.Hash(doc) {
		t.Error("rebuild did not refresh the hash of what is on disk")
	}
}
