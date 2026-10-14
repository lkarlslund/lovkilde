package crawl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lkarlslund/lovkilde/internal/archive"
	"github.com/lkarlslund/lovkilde/internal/config"
	"github.com/lkarlslund/lovkilde/internal/limiter"
	"github.com/lkarlslund/lovkilde/internal/rihttp"
	"github.com/lkarlslund/lovkilde/internal/store"
)

// Being throttled is not being refused. A throttled feed day is asked for again once the pacing
// has slowed, rather than ending the update; a throttled document stays as it was, so the next
// run asks again, rather than waiting the week a document the site refused waits.
func TestThrottlingSlowsTheRunAndFailsNothing(t *testing.T) {
	var feedCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/Documents", func(w http.ResponseWriter, _ *http.Request) {
		if feedCalls++; feedCalls <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir, cfg.SiteBase, cfg.HarvestBase, cfg.RequestTimeout = dir, srv.URL, srv.URL, 5*time.Second
	st, err := store.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ar, err := archive.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	lim := limiter.NewTuned(map[string]limiter.Spec{
		limiter.BucketDocument: {Start: time.Microsecond, Floor: time.Microsecond, Max: time.Millisecond},
		limiter.BucketHarvest:  {Start: time.Microsecond, Floor: time.Microsecond, Max: time.Millisecond},
	}, 1.5, 2, time.Millisecond)
	c := New(cfg, st, ar, rihttp.New(cfg, lim))
	ctx := context.Background()

	if err := c.Harvest(ctx); err != nil {
		t.Fatalf("a harvest throttled twice ended with %v", err)
	}

	u := "https://www.retsinformation.dk/eli/fob/2009/1"
	if err := st.PutInventory(ctx, store.Inventory{ELIURL: u, LastMod: "2024-01-01", Kind: "fob", State: store.StatePending}); err != nil {
		t.Fatal(err)
	}
	var stats FetchStats
	if err := c.Fetch(ctx, FetchOptions{Batch: 10, OnProgress: func(s FetchStats) { stats = s }}); err != nil {
		t.Fatal(err)
	}
	row, _, err := st.GetInventory(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != store.StatePending || row.TriedAt != "" || stats.Failed != 0 {
		t.Errorf("a throttled document is %q, tried %q, %d failed; want it pending as it was", row.State, row.TriedAt, stats.Failed)
	}
}
