package crawl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if err := c.Fetch(ctx, FetchOptions{Batch: 10,
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
	if err := c.Fetch(ctx, FetchOptions{Batch: 10, Kinds: []string{"retsinfo"},
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

// --state failed is the retry path for failures the ordinary sweep is leaving for their
// weekly retry, so the wait must not veto it -- and must still veto it for a default
// run, which is what keeps a dead URL from being re-requested every run.
func TestFetchStateFilterRetriesRecentFailures(t *testing.T) {
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
		{ELIURL: "https://www.retsinformation.dk/eli/ft/2021/2", Kind: "ft", State: store.StateFailed, TriedAt: time.Now().UTC().Format(time.RFC3339), Error: "transport"},
		{ELIURL: "https://www.retsinformation.dk/eli/ft/2021/3", Kind: "ft", State: store.StateGone, Error: "404 Not Found"},
	}
	if err := st.PutInventoryBatch(ctx, seed); err != nil {
		t.Fatal(err)
	}

	// The ordinary sweep: both rows are out of scope, so nothing is requested.
	if err := c.Fetch(ctx, FetchOptions{Batch: 10}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if requests != 0 {
		t.Fatalf("default sweep made %d requests, want 0: a failure tried just now and a 404 must stay put", requests)
	}

	// Named states: the wait lifts and the failed row is retried.
	if err := c.Fetch(ctx, FetchOptions{Batch: 10, States: []string{"failed"}}); err != nil {
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
		{ELIURL: "https://www.retsinformation.dk/eli/ft/2021/5", Kind: "ft", State: store.StateGone, Error: "404 Not Found"},
		{ELIURL: "https://www.retsinformation.dk/eli/ft/2021/6", Kind: "ft", State: store.StateGone, Error: "404 Not Found"},
		{ELIURL: "https://www.retsinformation.dk/eli/ft/2021/7", Kind: "ft", State: store.StateGone, Error: "404 Not Found"},
	}
	if err := st.PutInventoryBatch(ctx, seed); err != nil {
		t.Fatal(err)
	}

	// Batch smaller than the seed so the loop has to page: that is where the
	// wrap-around happened.
	runOnce := func() {
		done := make(chan error, 1)
		go func() {
			done <- c.Fetch(ctx, FetchOptions{Batch: 2, States: []string{"gone"}})
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
	if err := c.Fetch(ctx, FetchOptions{Batch: 10,
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

	opt := FetchOptions{Batch: 10}
	var first FetchStats
	if err := c.Fetch(ctx, FetchOptions{Batch: 10,
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

// orderRecorder collects the order requests reached the server in. The fetch loop is
// sequential, but the handler runs on httptest's goroutines, so the mutex keeps
// -race honest.
type orderRecorder struct {
	mu   sync.Mutex
	seen []string
}

func (o *orderRecorder) record(path string) {
	o.mu.Lock()
	o.seen = append(o.seen, path)
	o.mu.Unlock()
}

func (o *orderRecorder) order() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.seen...)
}

// newOrderTester wires a crawler to a throwaway archive whose server answers 404 to
// everything, so every row stays in the selection and the only thing under test is
// which row is asked for next. Pacing drops to microseconds.
func newOrderTester(t *testing.T, ids ...string) (*Crawler, *store.Store, *orderRecorder) {
	rec := &orderRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
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
	t.Cleanup(func() { st.Close() })
	ar, err := archive.Open(dir)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	lim := limiter.NewTuned(map[string]limiter.Spec{
		limiter.BucketDocument: {Start: time.Microsecond, Floor: time.Microsecond, Max: time.Millisecond},
	}, 1.5, 2, time.Millisecond)

	ctx := context.Background()
	var rows []store.Inventory
	for _, id := range ids {
		rows = append(rows, store.Inventory{
			ELIURL: "https://www.retsinformation.dk/eli/lta/2024/" + id,
			Kind:   "lta", LastMod: "2024-01-01", State: store.StatePending,
		})
	}
	if err := st.PutInventoryBatch(ctx, rows); err != nil {
		t.Fatal(err)
	}
	return New(cfg, st, ar, rihttp.New(cfg, lim)), st, rec
}

// fetchToCompletion fails the test rather than letting a broken cursor hang the run.
func fetchToCompletion(t *testing.T, c *Crawler, opt FetchOptions) {
	done := make(chan error, 1)
	go func() { done <- c.Fetch(context.Background(), opt) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Fetch did not terminate")
	}
}

// eliID extracts the document number a request path refers to. Requests carry the
// canonical /xml representation suffix, so comparing raw paths against ELI ids would
// silently never match.
func eliID(path string) string {
	path = strings.TrimSuffix(path, "/xml")
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// --order desc is the live-mirror case: the newest law is the last key, so it has to
// be the first request. Batch 2 forces the paging path.
func TestFetchOrderDescendingAsksNewestFirst(t *testing.T) {
	c, _, rec := newOrderTester(t, "1", "2", "3", "4", "5")
	fetchToCompletion(t, c, FetchOptions{Batch: 2, Order: store.OrderDesc})

	want := []string{"5", "4", "3", "2", "1"}
	var got []string
	for _, path := range rec.order() {
		got = append(got, eliID(path))
	}
	if len(got) != len(want) {
		t.Fatalf("made %d requests, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d = %s, want document %s", i, got[i], want[i])
		}
	}
}

// --order random must still visit every row exactly once and stop. The keys are
// materialised once and drained, so a bug in the drain shows up as a repeat or a
// hang rather than as a wrong order nobody notices.
func TestFetchOrderRandomVisitsEachRowOnce(t *testing.T) {
	ids := []string{"1", "2", "3", "4", "5", "6", "7"}
	c, _, rec := newOrderTester(t, ids...)
	fetchToCompletion(t, c, FetchOptions{Batch: 2, Order: store.OrderRandom, RandomSeed: 7})

	got := rec.order()
	if len(got) != len(ids) {
		t.Fatalf("made %d requests, want %d: %v", len(got), len(ids), got)
	}
	seen := map[string]bool{}
	for _, path := range got {
		if seen[path] {
			t.Fatalf("random order requested %s twice", path)
		}
		seen[path] = true
	}
	// A shuffle that quietly became a no-op would keep the ascending order, which
	// is exactly what --order random exists to avoid.
	shuffled := false
	for i := range got {
		if eliID(got[i]) != ids[i] {
			shuffled = true
			break
		}
	}
	if !shuffled {
		t.Errorf("random order came out as the ascending order: %v", got)
	}
}

// The seed is logged so a sample can be repeated later, which only means something if
// the same seed gives the same order.
func TestFetchOrderRandomIsReproducible(t *testing.T) {
	ids := []string{"1", "2", "3", "4", "5", "6", "7", "8"}
	c1, _, rec1 := newOrderTester(t, ids...)
	fetchToCompletion(t, c1, FetchOptions{Batch: 3, Order: store.OrderRandom, RandomSeed: 1234})
	c2, _, rec2 := newOrderTester(t, ids...)
	fetchToCompletion(t, c2, FetchOptions{Batch: 3, Order: store.OrderRandom, RandomSeed: 1234})

	a, b := rec1.order(), rec2.order()
	if len(a) != len(b) {
		t.Fatalf("same seed produced %d and %d requests", len(a), len(b))
	}
	for i := range a {
		if eliID(a[i]) != eliID(b[i]) {
			t.Fatalf("same seed produced a different order at %d: %s vs %s", i, a[i], b[i])
		}
	}
}
