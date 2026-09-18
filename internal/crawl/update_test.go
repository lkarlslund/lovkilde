package crawl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/archive"
	"github.com/lkarlslund/retsinformation/internal/config"
	"github.com/lkarlslund/retsinformation/internal/limiter"
	"github.com/lkarlslund/retsinformation/internal/rihttp"
	"github.com/lkarlslund/retsinformation/internal/store"
)

// updateFixture is a mirror with a store, an archive, and a site that answers the
// change feed with nothing and document requests with one fixture law.
type updateFixture struct {
	c           *Crawler
	st          *store.Store
	requests    *int
	feedRequest *int
	failing     *atomic.Bool
	dir         string
}

func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()

	doc, err := os.ReadFile(filepath.Join("..", "docapi", "testdata", "fob-lok53.xml"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// Counted apart: the refill re-reads today's feed entry on every run, because
	// a day is only finished once the day is over, so feed calls are expected and
	// must not be mistaken for document re-requests.
	var requests, feedRequests int
	var failing atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/Documents", func(w http.ResponseWriter, _ *http.Request) {
		feedRequests++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]any{})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if failing.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write(doc)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.SiteBase = srv.URL
	cfg.HarvestBase = srv.URL
	cfg.RequestTimeout = 5 * time.Second

	st, err := store.Open(filepath.Join(dir, "meta", "kv"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ar, err := archive.Open(dir)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Pacing collapsed: this is a correctness test, not a courtesy rehearsal.
	lim := limiter.NewTuned(map[string]limiter.Spec{
		limiter.BucketDocument: {Start: time.Microsecond, Floor: time.Microsecond, Max: time.Millisecond},
		limiter.BucketHarvest:  {Start: time.Microsecond, Floor: time.Microsecond, Max: time.Millisecond},
	}, 1.5, 2, time.Millisecond)

	return &updateFixture{c: New(cfg, st, ar, rihttp.New(cfg, lim)), st: st, requests: &requests,
		feedRequest: &feedRequests, failing: &failing, dir: dir}
}

// The daily refill must refuse a mirror that has never completed a collection
// pass, and refuse it before the first request: harvesting a mirror with an
// unknown baseline produces a feed checkpoint that makes it look baselined.
func TestUpdateRefusesMirrorWithoutSweep(t *testing.T) {
	f := newUpdateFixture(t)

	err := f.c.Update(context.Background(), UpdateOptions{})
	if !errors.Is(err, ErrStaleSweep) {
		t.Fatalf("Update on an unbaselined mirror: err=%v, want ErrStaleSweep", err)
	}
	// The two refusals give different instructions, and an absent baseline parses
	// to the zero time which satisfies the age comparison anyway, so the advice in
	// the message is the only thing proving which case the operator is in.
	if !strings.Contains(err.Error(), "never completed a collection pass") {
		t.Errorf("unbaselined refusal says %q, which does not say the mirror is new", err)
	}
	if *f.requests != 0 {
		t.Errorf("refusal made %d requests; it must make none", *f.requests)
	}
}

// A completed sweep older than the feed window cannot be refilled honestly: the
// days in between are no longer listed anywhere, so the run would collect a
// partial set and leave the mirror looking current.
func TestUpdateRefusesSweepOlderThanFeedWindow(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()

	stale := time.Now().UTC().Add(-(MaxSweepAge + 24*time.Hour)).Format(time.RFC3339)
	if err := f.st.SetMeta(ctx, metaLastSweep, stale); err != nil {
		t.Fatal(err)
	}

	if err := f.c.Update(ctx, UpdateOptions{}); !errors.Is(err, ErrStaleSweep) {
		t.Fatalf("Update after a %d-day gap: err=%v, want ErrStaleSweep", int(MaxSweepAge/(24*time.Hour))+1, err)
	}
	if *f.requests != 0 {
		t.Errorf("stale refusal made %d requests; it must make none", *f.requests)
	}

	// --force is the documented escape hatch, and it is the only way past: the
	// gate has to be a flag, not a warning an operator learns to scroll past.
	if err := f.c.Update(ctx, UpdateOptions{Force: true}); err != nil {
		t.Fatalf("Update --force on a stale mirror: %v", err)
	}
	if _, found, err := f.c.LastSweep(ctx); err != nil || !found {
		t.Errorf("forced run did not record a sweep: found=%v err=%v", found, err)
	}
}

// The timestamp means "nothing is left to collect", so a pass that collected
// everything must record it, and one that could not must not.
func TestUpdateRecordsSweepOnlyAfterDraining(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()

	// A recent baseline so the gate is open, and one document genuinely missing.
	recent := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if err := f.st.SetMeta(ctx, metaLastSweep, recent); err != nil {
		t.Fatal(err)
	}
	pending := "https://www.retsinformation.dk/eli/lta/2026/1234"
	if err := f.st.PutInventory(ctx, store.Inventory{ELIURL: pending, LastMod: "2026-09-18", Kind: "lta", State: store.StatePending}); err != nil {
		t.Fatal(err)
	}

	if err := f.c.Update(ctx, UpdateOptions{}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if *f.requests == 0 {
		t.Fatal("the refill fetched nothing: the pending resource was not collected")
	}
	ts, found, err := f.c.LastSweep(ctx)
	if err != nil || !found {
		t.Fatalf("no sweep timestamp after a drained pass: found=%v err=%v", found, err)
	}
	if time.Since(ts) > time.Minute {
		t.Errorf("sweep timestamp is %.1f h old, expected this run's", time.Since(ts).Hours())
	}
	row, found, err := f.st.GetInventory(ctx, pending)
	if err != nil || !found || row.State != store.StateFetched {
		t.Errorf("pending row after the refill: found=%v row=%+v err=%v", found, row, err)
	}

	// Now leave work the sweep will not do but still owes -- pending at the retry
	// ceiling is stalled, not outstanding, and must not block the timestamp
	// forever -- while an actually-owed row must block it.
	stalled := "https://www.retsinformation.dk/eli/lta/2026/9999"
	if err := f.st.PutInventory(ctx, store.Inventory{ELIURL: stalled, LastMod: "2026-09-18", Kind: "lta",
		State: store.StatePending, Attempts: defaultMaxAttempts, Error: "giving up"}); err != nil {
		t.Fatal(err)
	}
	// The stored timestamp has second granularity, so the run expected to advance
	// it has to begin in a later second than the one it is compared against.
	time.Sleep(1100 * time.Millisecond)
	before := *f.requests
	if err := f.c.Update(ctx, UpdateOptions{}); err != nil {
		t.Fatalf("Update with a stalled row: %v", err)
	}
	if *f.requests != before {
		t.Errorf("the sweep re-fetched a row past the retry ceiling (%d extra document requests)", *f.requests-before)
	}
	ts2, found, err := f.c.LastSweep(ctx)
	if err != nil || !found {
		t.Fatalf("stalled row blocked the timestamp: found=%v err=%v", found, err)
	}
	if !ts2.After(ts) {
		t.Errorf("stalled row left the timestamp where it was: %s then %s", ts.Format(time.RFC3339), ts2.Format(time.RFC3339))
	}
}

// A corrupt timestamp must not parse into "the zero time", which the age
// comparison would read as ancient (harmless) or fresh (a silent lie, depending
// on the direction of the bug). It has to be an error.
func TestLastSweepRejectsCorruptTimestamp(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	if err := f.st.SetMeta(ctx, metaLastSweep, "yesterday-ish"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := f.c.LastSweep(ctx); err == nil || found {
		t.Fatalf("LastSweep accepted a corrupt value: found=%v err=%v", found, err)
	}
}

// A run that came home empty-handed is not a completed collection. The document
// stays owed -- failed but still under the retry ceiling -- and the mirror must
// not be stamped as collected, because that stamp is what authorises every
// subsequent incremental refill.
func TestUpdateWithholdsTimestampWhenFetchFails(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()

	f.failing.Store(true)
	doc := "https://www.retsinformation.dk/eli/lta/2026/4242"
	if err := f.st.PutInventory(ctx, store.Inventory{ELIURL: doc, LastMod: "2026-09-18", Kind: "lta", State: store.StatePending}); err != nil {
		t.Fatal(err)
	}

	// Force opens the staleness gate: this run is about the drain check, not the
	// baseline, and there is deliberately no baseline to advance.
	if err := f.c.Update(ctx, UpdateOptions{Force: true}); err != nil {
		t.Fatalf("Update with a failing document: %v", err)
	}
	if _, found, err := f.c.LastSweep(ctx); err != nil {
		t.Fatal(err)
	} else if found {
		t.Error("a pass that failed to download a document recorded a completed sweep")
	}
	row, found, err := f.st.GetInventory(ctx, doc)
	if err != nil || !found {
		t.Fatalf("document row vanished: found=%v err=%v", found, err)
	}
	if row.State != store.StateFailed || row.Attempts != 1 {
		t.Errorf("failed document stored as %+v, want failed after 1 attempt", row)
	}

	// The same document arriving on the next run must clear both the row and the
	// timestamp: owed is not a life sentence.
	f.failing.Store(false)
	if err := f.c.Update(ctx, UpdateOptions{Force: true}); err != nil {
		t.Fatalf("Update after the document became available: %v", err)
	}
	if _, found, err := f.c.LastSweep(ctx); err != nil || !found {
		t.Errorf("sweep not recorded once the document arrived: found=%v err=%v", found, err)
	}
}
