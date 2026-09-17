package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "kv"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestInventoryRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	in := Inventory{
		ELIURL: "https://www.retsinformation.dk/eli/lta/2026/789", LastMod: "2026-09-17",
		Kind: "lta", State: StateFetched, Attempts: 2,
		Accession: "B20260078905", SHA256: "abc123", Bytes: 72366,
	}
	if err := s.PutInventory(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetInventory(ctx, in.ELIURL)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("record not found after write")
	}
	if got != in {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, in)
	}
}

func TestPendingSkipsExhaustedAndFetched(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rows := []Inventory{
		{ELIURL: "https://x/eli/a/1", State: StatePending, Kind: "a"},
		{ELIURL: "https://x/eli/a/2", State: StateFetched, Kind: "a", SHA256: "deadbeef"},
		{ELIURL: "https://x/eli/a/3", State: StateFailed, Attempts: 1, Kind: "a"},
		{ELIURL: "https://x/eli/a/4", State: StateFailed, Attempts: 9, Kind: "a"},
		{ELIURL: "https://x/eli/a/5", State: StateGone, Kind: "a"},
		{ELIURL: "https://x/eli/a/6", State: StateSkipped, Kind: "a"},
	}
	if err := s.PutInventoryBatch(ctx, rows); err != nil {
		t.Fatal(err)
	}
	got, err := s.PendingInventory(ctx, 100, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("PendingInventory returned %d rows, want 2: %+v", len(got), got)
	}
	for _, r := range got {
		if r.ELIURL != "https://x/eli/a/1" && r.ELIURL != "https://x/eli/a/3" {
			t.Errorf("unexpected pending row %s (state %s, attempts %d)", r.ELIURL, r.State, r.Attempts)
		}
	}

	// A limit must be honoured so batches stay small and memory flat.
	limited, err := s.PendingInventory(ctx, 1, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 {
		t.Errorf("limit not honoured: got %d rows", len(limited))
	}
}

func TestPendingByKind(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rows := []Inventory{
		{ELIURL: "https://x/eli/ft/1", State: StatePending, Kind: "ft"},
		{ELIURL: "https://x/eli/retsinfo/1", State: StatePending, Kind: "retsinfo"},
		{ELIURL: "https://x/eli/retsinfo/2", State: StateFailed, Attempts: 1, Kind: "retsinfo"},
		{ELIURL: "https://x/eli/lta/1", State: StateFetched, Kind: "lta", SHA256: "aa"},
		{ELIURL: "https://x/eli/lta/2", State: StateGone, Kind: "lta"},
	}
	if err := s.PutInventoryBatch(ctx, rows); err != nil {
		t.Fatal(err)
	}

	// One kind only, and retryable failures count while fetched/gone do not.
	only, err := s.PendingInventory(ctx, 100, 5, []string{"retsinfo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 2 {
		t.Fatalf("filtered pending = %d rows, want 2: %+v", len(only), only)
	}
	for _, r := range only {
		if r.Kind != "retsinfo" {
			t.Errorf("kind filter leaked %s (%s)", r.ELIURL, r.Kind)
		}
	}

	// Several kinds at once, and a kind nothing matches.
	if many, err := s.PendingInventory(ctx, 100, 5, []string{"ft", "retsinfo"}); err != nil {
		t.Fatal(err)
	} else if len(many) != 3 {
		t.Errorf("two-kind filter = %d rows, want 3", len(many))
	}
	if none, err := s.PendingInventory(ctx, 100, 5, []string{"nosuchkind"}); err != nil {
		t.Fatal(err)
	} else if len(none) != 0 {
		t.Errorf("unknown kind matched %d rows, want 0", len(none))
	}

	counts, err := s.PendingByKind(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["retsinfo"] != 2 || counts["ft"] != 1 || counts["lta"] != 0 {
		t.Errorf("PendingByKind = %v, want retsinfo=2 ft=1 lta=0", counts)
	}
}

func TestCountByState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.PutInventoryBatch(ctx, []Inventory{
		{ELIURL: "u1", State: StateFetched}, {ELIURL: "u2", State: StateFetched}, {ELIURL: "u3", State: StatePending},
	}); err != nil {
		t.Fatal(err)
	}
	counts, total, err := s.CountByState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if counts[StateFetched] != 2 || counts[StatePending] != 1 {
		t.Errorf("counts = %v", counts)
	}
}

func TestAccessionDedupIndex(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, found, err := s.GetAccession(ctx, "B20260078905"); err != nil || found {
		t.Fatalf("unknown accession: found=%v err=%v", found, err)
	}
	if err := s.PutAccession(ctx, Accession{Accession: "B20260078905", ELIURL: "https://x/eli/lta/2026/789", SHA256: "h1", Bytes: 72366, ChangedAt: "2026-09-17T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetAccession(ctx, "B20260078905")
	if err != nil || !found {
		t.Fatalf("accession lookup: found=%v err=%v", found, err)
	}
	if got.SHA256 != "h1" || got.Bytes != 72366 {
		t.Errorf("accession = %+v", got)
	}
}

func TestHarvestDayCheckpoint(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, found, err := s.LastHarvestedDay(ctx); err != nil || found {
		t.Fatalf("fresh store should have no checkpoint: found=%v err=%v", found, err)
	}
	d1 := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	for _, d := range []time.Time{d1, d2} {
		if err := s.MarkDayHarvested(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	got, found, err := s.LastHarvestedDay(ctx)
	if err != nil || !found {
		t.Fatalf("LastHarvestedDay: found=%v err=%v", found, err)
	}
	if !got.Equal(d2) {
		t.Errorf("LastHarvestedDay = %s, want %s", got.Format(time.DateOnly), d2.Format(time.DateOnly))
	}
}

// Re-recording a change must be harmless, since the harvest window is walked
// repeatedly after outages.
func TestPutChangesIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := Change{Date: "2026-09-17", Accession: "A20240039929", Reason: "DocumentChanged", DocType: "LBK H", Href: "https://x/eli/accn/A20240039929/xml", SeenAt: "now"}
	for i := 0; i < 3; i++ {
		if err := s.PutChanges(ctx, []Change{c}); err != nil {
			t.Fatalf("PutChanges iteration %d: %v", i, err)
		}
	}
	got, err := s.UnresolvedAccessions(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("UnresolvedAccessions = %d, want 1 (dedup by key)", len(got))
	}
	if got[0].Accession != c.Accession || got[0].Reason != c.Reason {
		t.Errorf("change = %+v", got[0])
	}

	// Once the body is stored, the change is no longer outstanding.
	if err := s.PutAccession(ctx, Accession{Accession: c.Accession, SHA256: "h"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.UnresolvedAccessions(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("resolved change still reported outstanding: %+v", got)
	}
}

func TestSha256Hex(t *testing.T) {
	// Known-answer test: a wrong hash silently breaks change detection.
	if got := Sha256Hex([]byte("abc")); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("Sha256Hex(abc) = %s", got)
	}
}
