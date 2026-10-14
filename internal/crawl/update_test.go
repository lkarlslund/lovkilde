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

	"github.com/lkarlslund/lovkilde/internal/archive"
	"github.com/lkarlslund/lovkilde/internal/config"
	"github.com/lkarlslund/lovkilde/internal/limiter"
	"github.com/lkarlslund/lovkilde/internal/rihttp"
	"github.com/lkarlslund/lovkilde/internal/store"
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
func TestUpdateRecordsEveryPassAndItsProblems(t *testing.T) {
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
		t.Fatalf("no pull record after a drained pass: found=%v err=%v", found, err)
	}
	if time.Since(ts) > time.Minute {
		t.Errorf("pull record is %.1f h old, expected this run's", time.Since(ts).Hours())
	}
	rec, _, err := f.c.LastSweepRecord(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.IssuesKnown || rec.Total() != 0 {
		t.Errorf("a pass that drained both queues recorded %q, want clean", rec.Issues)
	}
	row, found, err := f.st.GetInventory(ctx, pending)
	if err != nil || !found || row.State != store.StateFetched {
		t.Errorf("pending row after the refill: found=%v row=%+v err=%v", found, row, err)
	}

	// Now leave a document the site refused a moment ago: it waits for its weekly
	// retry, is reported as failed, and must not block the timestamp.
	refused := "https://www.retsinformation.dk/eli/lta/2026/9999"
	if err := f.st.PutInventory(ctx, store.Inventory{ELIURL: refused, LastMod: "2026-09-18", Kind: "lta",
		State: store.StateFailed, TriedAt: time.Now().UTC().Format(time.RFC3339), Error: "500"}); err != nil {
		t.Fatal(err)
	}
	// The stored timestamp has second granularity, so the run expected to advance
	// it has to begin in a later second than the one it is compared against.
	time.Sleep(1100 * time.Millisecond)
	before := *f.requests
	if err := f.c.Update(ctx, UpdateOptions{}); err != nil {
		t.Fatalf("Update with a refused row: %v", err)
	}
	if *f.requests != before {
		t.Errorf("the sweep re-fetched a failure before its week was up (%d extra document requests)", *f.requests-before)
	}
	ts2, found, err := f.c.LastSweep(ctx)
	if err != nil || !found {
		t.Fatalf("a refused row blocked the record: found=%v err=%v", found, err)
	}
	if !ts2.After(ts) {
		t.Errorf("a refused row left the record where it was: %s then %s", ts.Format(time.RFC3339), ts2.Format(time.RFC3339))
	}
	// The refused document is reported rather than held against the mirror: the counts
	// and fingerprint are how a reader learns the mirror has a hole in it.
	rec2, _, err := f.c.LastSweepRecord(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rec2.Failed != 1 || rec2.Digest == "" {
		t.Errorf("the refused document is not carried in the record: %q", rec2)
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

// A run that came home empty-handed still records the pull, and the document it could
// not get: failed, with when it was tried and what the site said.
func TestFailingPassRecordsThePullAndTheProblem(t *testing.T) {
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
	rec, found, err := f.c.LastSweepRecord(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The pass records that it pulled and what it could not get. Hiding the record
	// until everything worked would discard the true statement "we tried today and this
	// one would not come", which is the statement an operator needs.
	if !found {
		t.Fatal("a pass that pulled recorded nothing")
	}
	if !rec.IssuesKnown || rec.Failed != 1 {
		t.Errorf("the document the site refused is not in the record: %q", rec)
	}
	if rec.Digest == "" {
		t.Error("unresolved work recorded without a fingerprint, so the next run cannot tell it is the same work")
	}
	row, found, err := f.st.GetInventory(ctx, doc)
	if err != nil || !found {
		t.Fatalf("document row vanished: found=%v err=%v", found, err)
	}
	if row.State != store.StateFailed || row.TriedAt == "" || row.Error == "" {
		t.Errorf("failed document stored as %+v, want failed with when it was tried and why", row)
	}

	// The same document arriving when it is next due must clear both the row and the
	// record: failed is not a life sentence. A week on, it is due again.
	row.TriedAt = time.Now().Add(-store.RetryFailedAfter - time.Hour).UTC().Format(time.RFC3339)
	if err := f.st.PutInventory(ctx, row); err != nil {
		t.Fatal(err)
	}
	f.failing.Store(false)
	if err := f.c.Update(ctx, UpdateOptions{Force: true}); err != nil {
		t.Fatalf("Update after the document became available: %v", err)
	}
	rec2, found, err := f.c.LastSweepRecord(ctx)
	if err != nil || !found {
		t.Fatalf("nothing recorded once the document arrived: found=%v err=%v", found, err)
	}
	if rec2.IssuesKnown && rec2.Total() != 0 {
		t.Errorf("the document arrived but the record still reports %q", rec2.Issues)
	}
	if rec2.Digest != "" {
		t.Errorf("a clean pass kept the previous run's fingerprint: %q", rec2)
	}
}

// The store row is the authority on when a collection pass pulled, but the store is
// locked to one process, so the same fact has to reach readers that cannot open it:
// the artifact build copies it from the published file into the artifact's header. The two must not be allowed to drift apart.
func TestUpdatePublishesTheRecord(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
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
	row, found, err := f.c.LastSweepRecord(ctx)
	if err != nil || !found {
		t.Fatalf("no pull record: found=%v err=%v", found, err)
	}
	file, fileFound, err := ReadSweepRecord(f.c.cfg.SweepStampPath())
	if err != nil {
		t.Fatalf("reading the published record: %v", err)
	}
	if !fileFound {
		t.Fatalf("nothing published at %s: a reader that cannot open the store has no horizon", f.c.cfg.SweepStampPath())
	}
	if !file.PulledAt.Equal(row.PulledAt) || file.Digest != row.Digest || file.Owed != row.Owed || file.Failed != row.Failed {
		t.Errorf("published %q disagrees with the store row %q", file, row)
	}
}

func TestSweepRecordRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "sweep.stamp")
	want := SweepRecord{
		PulledAt:    time.Date(2026, 9, 18, 9, 16, 52, 0, time.UTC),
		Issues:      store.Issues{Owed: 2, Failed: 14, Digest: "6f2c1a9d"},
		IssuesKnown: true,
	}
	if err := PublishSweepRecord(path, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := ReadSweepRecord(path)
	if err != nil || !found {
		t.Fatalf("round trip: found=%v err=%v", found, err)
	}
	if !got.PulledAt.Equal(want.PulledAt) || got.Issues != want.Issues {
		t.Errorf("round trip returned %q, want %q", got, want)
	}

	// A record written before the problems were counted must not read as "clean": the
	// honest rendering is that nobody knows what was outstanding.
	legacy, err := ParseSweepRecord("2026-09-18T09:16:52Z")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.IssuesKnown || legacy.PulledAt.IsZero() {
		t.Errorf("a bare timestamp was read as %q, which claims more than it says", legacy)
	}
	if legacy.String() != "2026-09-18T09:16:52Z" {
		t.Errorf("legacy record re-serialised as %q: an unknown problem count must not gain a verdict", legacy.String())
	}

	_, found, err = ReadSweepRecord(filepath.Join(t.TempDir(), "sweep.stamp"))
	if found || err != nil {
		t.Errorf("absent record read as found=%v err=%v", found, err)
	}

	// A record that will not parse must fail loudly rather than read as the zero time,
	// which every age comparison would then place somewhere it never was.
	bad := filepath.Join(t.TempDir(), "sweep.stamp")
	if err := os.WriteFile(bad, []byte("last tuesday\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, found, err := ReadSweepRecord(bad); found || err == nil || !got.PulledAt.IsZero() {
		t.Errorf("unparsable record: got=%v found=%v err=%v", got, found, err)
	}
	if _, err := ParseSweepRecord("2026-09-18T09:16:52Z owed=notanumber"); err == nil {
		t.Error("a malformed count parsed silently")
	}
}
