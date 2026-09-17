package crawl

import (
	"context"
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

func TestELIKind(t *testing.T) {
	cases := map[string]string{
		"https://www.retsinformation.dk/eli/retsinfo/2026/10019": "retsinfo",
		"https://www.retsinformation.dk/eli/lta/2026/789":        "lta",
		"https://www.retsinformation.dk/eli/ft/202523L00005":     "ft",
		"https://www.retsinformation.dk/eli/fob/2024/1234":       "fob",
		"https://www.retsinformation.dk/eli/mt/2024/1":           "mt",
		"https://www.retsinformation.dk/eli/accn/A20240039929":   "accn",
		"https://www.retsinformation.dk/eli/lta/2026/789.rdfa":   "lta",
		"https://example.com/not-an-eli-url":                     "",
	}
	for url, want := range cases {
		if got := eliKind(url); got != want {
			t.Errorf("eliKind(%s) = %q, want %q", url, got, want)
		}
	}
}

// The fetch phase relies entirely on /xml being served for every ELI kind; these
// forms are the ones actually produced by the sitemap and the harvest feed.
func TestXMLURLFor(t *testing.T) {
	const base = "https://www.retsinformation.dk"
	cases := map[string]string{
		"https://www.retsinformation.dk/eli/retsinfo/2026/10019": base + "/eli/retsinfo/2026/10019/xml",
		"https://www.retsinformation.dk/eli/lta/2026/789":        base + "/eli/lta/2026/789/xml",
		"https://www.retsinformation.dk/eli/ft/202523L00005":     base + "/eli/ft/202523L00005/xml",
		"https://www.retsinformation.dk/eli/accn/A20240039929":   base + "/eli/accn/A20240039929/xml",
		"https://www.retsinformation.dk/eli/lta/2026/789/":       base + "/eli/lta/2026/789/xml",
		"https://www.retsinformation.dk/eli/lta/2026/789.rdfa":   base + "/eli/lta/2026/789/xml",
		"https://example.com/nope":                               "",
	}
	for url, want := range cases {
		if got := xmlURLFor(base, url); got != want {
			t.Errorf("xmlURLFor(%s)\n got %s\nwant %s", url, got, want)
		}
	}
}

func TestNormalizeDate(t *testing.T) {
	cases := map[string]string{
		"2026-09-17":           "2026-09-17",
		"2026-09-17T00:00:00Z": "2026-09-17",
		"17-09-2026":           "2026-09-17",
		"2026-09":              "2026-09",
		"":                     "",
	}
	for in, want := range cases {
		if got := normalizeDate(in); got != want {
			t.Errorf("normalizeDate(%q) = %q, want %q", in, got, want)
		}
	}
}

// Fetch reports progress once per resource, including the resources that fail:
// a bar that silently stops advancing is worse than no bar, because it looks
// like the crawl stalled. Every exit path in the loop has to report.
func TestFetchReportsProgressEveryResource(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "docapi", "testdata", "fob-lok53.xml"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// One deliberately missing resource, to cover the "gone" path.
		if strings.Contains(r.URL.Path, "/2023/300") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
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
	// Pacing collapsed to microseconds: this is a correctness test, and the
	// limiter has its own suite for pacing behaviour.
	lim := limiter.NewTuned(map[string]limiter.Spec{
		limiter.BucketDocument: {Start: time.Microsecond, Floor: time.Microsecond, Max: time.Millisecond},
	}, 1.5, 2, time.Millisecond)
	c := New(cfg, st, ar, rihttp.New(cfg, lim))

	ctx := context.Background()
	eli := []string{
		"https://www.retsinformation.dk/eli/lta/2021/100",
		"https://www.retsinformation.dk/eli/lta/2022/200",
		"https://www.retsinformation.dk/eli/lta/2023/300", // served as 404
		"https://www.retsinformation.dk/eli/lta/2024/400",
	}
	for _, u := range eli {
		if err := st.PutInventory(ctx, store.Inventory{ELIURL: u, LastMod: "2024-01-01", Kind: "lta", State: store.StatePending}); err != nil {
			t.Fatalf("seed %s: %v", u, err)
		}
	}

	var calls int
	var last FetchStats
	if err := c.Fetch(ctx, FetchOptions{Batch: 10, MaxAttempts: 2,
		OnProgress: func(s FetchStats) { calls++; last = s }}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if calls != len(eli) {
		t.Errorf("progress called %d times, want %d (one per resource)", calls, len(eli))
	}
	if last.Attempted != int64(len(eli)) {
		t.Errorf("attempted %d, want %d", last.Attempted, len(eli))
	}
	if last.Stored != 3 {
		t.Errorf("stored %d, want 3", last.Stored)
	}
	if last.Failed != 0 {
		t.Errorf("failed %d, want 0: a 404 is a definitive answer, not a failure", last.Failed)
	}
	if last.Total != int64(len(eli)) {
		t.Errorf("total %d, want %d", last.Total, len(eli))
	}
	if last.Bytes <= 0 {
		t.Errorf("bytes %d, want > 0", last.Bytes)
	}
}

// --kind must narrow the work, not just the report: its purpose is sampling or
// repairing one series without crawling everything that sorts before it. The
// denominator has to follow the same filter, or the bar is built with the wrong
// total.
func TestFetchKindFilter(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte("<Dokument><Meta><AccessionNumber>W20261001925</AccessionNumber></Meta></Dokument>"))
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
	seed := []store.Inventory{
		{ELIURL: "https://www.retsinformation.dk/eli/ft/2021/1", Kind: "ft", State: store.StatePending},
		{ELIURL: "https://www.retsinformation.dk/eli/retsinfo/2021/10", Kind: "retsinfo", State: store.StatePending},
		{ELIURL: "https://www.retsinformation.dk/eli/retsinfo/2021/11", Kind: "retsinfo", State: store.StatePending},
	}
	if err := st.PutInventoryBatch(ctx, seed); err != nil {
		t.Fatal(err)
	}

	total, err := c.PendingCount(ctx, 0, store.Filter{Kinds: []string{"retsinfo"}})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("PendingCount = %d, want 2", total)
	}

	var last FetchStats
	if err := c.Fetch(ctx, FetchOptions{Batch: 10, MaxAttempts: 2, Kinds: []string{"retsinfo"},
		OnProgress: func(s FetchStats) { last = s }}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if last.Attempted != 2 || requests != 2 {
		t.Errorf("attempted %d over %d requests, want 2 and 2: rows of other kinds were fetched", last.Attempted, requests)
	}

	// The excluded kind must still be waiting, untouched.
	rest, err := c.PendingCount(ctx, 0, store.Filter{Kinds: []string{"ft"}})
	if err != nil {
		t.Fatal(err)
	}
	if rest != 1 {
		t.Errorf("pending ft after a filtered run = %d, want 1", rest)
	}
}

// --state failed is the retry path for rows the ordinary sweep has given up on, so
// the attempt ceiling must not veto it -- and must still veto it for a default run,
// which is what keeps a dead URL from being re-requested forever.
func TestFetchStateFilterRetriesExhaustedFailures(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte("<Dokument><Meta><AccessionNumber>W20261001926</AccessionNumber></Meta></Dokument>"))
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
	seed := []store.Inventory{
		{ELIURL: "https://www.retsinformation.dk/eli/ft/2021/2", Kind: "ft", State: store.StateFailed, Attempts: 99, Error: "transport"},
		{ELIURL: "https://www.retsinformation.dk/eli/ft/2021/3", Kind: "ft", State: store.StateGone, Attempts: 1, Error: "404 Not Found"},
	}
	if err := st.PutInventoryBatch(ctx, seed); err != nil {
		t.Fatal(err)
	}

	// The ordinary sweep: both rows are out of scope, so nothing is requested.
	if err := c.Fetch(ctx, FetchOptions{Batch: 10, MaxAttempts: 5}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if requests != 0 {
		t.Fatalf("default sweep made %d requests, want 0: exhausted failures and 404s must stay put", requests)
	}

	// Named states: the ceiling lifts and the failed row is retried.
	if err := c.Fetch(ctx, FetchOptions{Batch: 10, MaxAttempts: 5, States: []string{"failed"}}); err != nil {
		t.Fatalf("Fetch --state failed: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests after --state failed = %d, want 1", requests)
	}
	got, found, err := st.GetInventory(ctx, seed[0].ELIURL)
	if err != nil || !found {
		t.Fatalf("GetInventory: %v found=%v", err, found)
	}
	if got.State != store.StateFetched {
		t.Errorf("state after retry = %s, want fetched", got.State)
	}

	// The count the progress bar uses must follow the same states, or the bar is
	// built with a total of 189k for a run that has one row to do.
	n, err := c.PendingCount(ctx, 0, store.Filter{States: []string{"gone"}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("PendingCount(gone) = %d, want 1", n)
	}
}

// A retry sweep must visit each selected row once per invocation. A 404 retried
// under --state gone is written back as gone, so it still matches the filter, and the
// batch loop re-scanned from the start of the keyspace: the run looped over the same
// rows forever and the progress bar climbed past its total.
func TestFetchStateRetryVisitsEachRowOnce(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusNotFound)
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
	seed := []store.Inventory{
		{ELIURL: "https://www.retsinformation.dk/eli/ft/2021/5", Kind: "ft", State: store.StateGone, Attempts: 1, Error: "404 Not Found"},
		{ELIURL: "https://www.retsinformation.dk/eli/ft/2021/6", Kind: "ft", State: store.StateGone, Attempts: 1, Error: "404 Not Found"},
		{ELIURL: "https://www.retsinformation.dk/eli/ft/2021/7", Kind: "ft", State: store.StateGone, Attempts: 1, Error: "404 Not Found"},
	}
	if err := st.PutInventoryBatch(ctx, seed); err != nil {
		t.Fatal(err)
	}

	// Batch smaller than the seed so the loop has to page: that is where the
	// wrap-around happened.
	runOnce := func() {
		done := make(chan error, 1)
		go func() {
			done <- c.Fetch(ctx, FetchOptions{Batch: 2, MaxAttempts: 5, States: []string{"gone"}})
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Fetch --state gone: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Fetch did not terminate: a retry sweep kept re-selecting rows that stayed gone")
		}
	}

	runOnce()
	if requests != 3 {
		t.Fatalf("first sweep made %d requests, want 3 (each gone row exactly once)", requests)
	}
	for _, row := range seed {
		got, found, err := st.GetInventory(ctx, row.ELIURL)
		if err != nil || !found {
			t.Fatalf("GetInventory(%s): %v found=%v", row.ELIURL, err, found)
		}
		if got.State != store.StateGone {
			t.Errorf("state after a retried 404 = %s, want gone", got.State)
		}
	}

	// A second sweep re-tests them: that is the point of the flag, and it must
	// still terminate after one pass.
	runOnce()
	if requests != 6 {
		t.Errorf("two sweeps made %d requests, want 6", requests)
	}
}

// A second pass must report progress while closing every resource from disk,
// which is the resume path a multi-day crawl takes most often.
func TestFetchReportsProgressOnResumeWithoutRequests(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte("<Dokument/>"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.SiteBase = srv.URL
	cfg.RequestTimeout = 5 * time.Second
	u := "https://www.retsinformation.dk/eli/lta/2021/100"

	st, err := store.Open(filepath.Join(dir, "meta", "kv"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()
	ar, err := archive.Open(dir)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, _, _, err := ar.Put(u, []byte("<Dokument/>")); err != nil {
		t.Fatalf("put: %v", err)
	}
	ctx := context.Background()
	if err := st.PutInventory(ctx, store.Inventory{ELIURL: u, LastMod: "2024-01-01", Kind: "lta", State: store.StatePending}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	lim := limiter.NewTuned(map[string]limiter.Spec{
		limiter.BucketDocument: {Start: time.Microsecond, Floor: time.Microsecond, Max: time.Millisecond},
	}, 1.5, 2, time.Millisecond)
	c := New(cfg, st, ar, rihttp.New(cfg, lim))

	var last FetchStats
	if err := c.Fetch(ctx, FetchOptions{Batch: 10, MaxAttempts: 2,
		OnProgress: func(s FetchStats) { last = s }}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if requests != 0 {
		t.Errorf("issued %d requests for a document already on disk", requests)
	}
	if last.Skipped != 1 {
		t.Errorf("skipped %d, want 1", last.Skipped)
	}
}

// A resource the site serves as an HTML page must be closed definitively:
// retried on the next pass it can only fail the same way, and a daily-update
// crawl would keep paying for it forever.
func TestFetchClosesHTMLResponseWithoutRetrying(t *testing.T) {
	var requests int
	page := "<!DOCTYPE html><html><body>Den fulde tekst</body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.SiteBase = srv.URL
	cfg.RequestTimeout = 5 * time.Second
	u := "https://www.retsinformation.dk/eli/fob/2009/1-1"

	st, err := store.Open(filepath.Join(dir, "meta", "kv"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()
	ar, err := archive.Open(dir)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	ctx := context.Background()
	if err := st.PutInventory(ctx, store.Inventory{ELIURL: u, LastMod: "2024-01-01", Kind: "fob", State: store.StatePending}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	lim := limiter.NewTuned(map[string]limiter.Spec{
		limiter.BucketDocument: {Start: time.Microsecond, Floor: time.Microsecond, Max: time.Millisecond},
	}, 1.5, 2, time.Millisecond)
	c := New(cfg, st, ar, rihttp.New(cfg, lim))

	opt := FetchOptions{Batch: 10, MaxAttempts: 5}
	var first FetchStats
	if err := c.Fetch(ctx, FetchOptions{Batch: 10, MaxAttempts: 5,
		OnProgress: func(s FetchStats) { first = s }}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if first.Failed != 0 {
		t.Errorf("failed %d, want 0: an HTML answer is definitive, not a fault", first.Failed)
	}
	if requests != 1 {
		t.Errorf("requests %d on first pass, want 1", requests)
	}

	row, found, err := st.GetInventory(ctx, u)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found || row.State != store.StateGone {
		t.Errorf("state %q, want %q", row.State, store.StateGone)
	}

	// Second pass must leave it alone entirely.
	if err := c.Fetch(ctx, opt); err != nil {
		t.Fatalf("Fetch second pass: %v", err)
	}
	if requests != 1 {
		t.Errorf("requests %d after a second pass, want still 1", requests)
	}
}
