package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
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
	got, err := s.PendingInventory(ctx, 100, Filter{MaxAttempts: 5})
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
	limited, err := s.PendingInventory(ctx, 1, Filter{MaxAttempts: 5})
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
	only, err := s.PendingInventory(ctx, 100, Filter{MaxAttempts: 5, Kinds: []string{"retsinfo"}})
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
	if many, err := s.PendingInventory(ctx, 100, Filter{MaxAttempts: 5, Kinds: []string{"ft", "retsinfo"}}); err != nil {
		t.Fatal(err)
	} else if len(many) != 3 {
		t.Errorf("two-kind filter = %d rows, want 3", len(many))
	}
	if none, err := s.PendingInventory(ctx, 100, Filter{MaxAttempts: 5, Kinds: []string{"nosuchkind"}}); err != nil {
		t.Fatal(err)
	} else if len(none) != 0 {
		t.Errorf("unknown kind matched %d rows, want 0", len(none))
	}

	counts, err := s.PendingByKind(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if counts["retsinfo"] != 2 || counts["ft"] != 1 || counts["lta"] != 0 {
		t.Errorf("PendingByKind = %v, want retsinfo=2 ft=1 lta=0", counts)
	}
}

// Naming states is the retry-the-failures path, and it must reach rows the
// automatic sweep has given up on.
func TestInventoryStateSelection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rows := []Inventory{
		{ELIURL: "https://x/eli/ft/1", State: StatePending, Kind: "ft"},
		{ELIURL: "https://x/eli/ft/2", State: StateFailed, Attempts: 1, Kind: "ft"},
		{ELIURL: "https://x/eli/ft/3", State: StateFailed, Attempts: 99, Kind: "ft"},
		{ELIURL: "https://x/eli/lta/1", State: StateGone, Kind: "lta", Error: "404 Not Found"},
		{ELIURL: "https://x/eli/lta/2", State: StateFetched, Kind: "lta", SHA256: "aa"},
		{ELIURL: "https://x/eli/ltb/1", State: StateSkipped, Kind: "ltb"},
	}
	if err := s.PutInventoryBatch(ctx, rows); err != nil {
		t.Fatal(err)
	}

	count := func(f Filter) int {
		got, err := s.PendingInventory(ctx, 100, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}

	// Naming failed must include the row at 99 attempts: the ceiling protects the
	// automatic sweep, not a deliberate retry.
	if n := count(Filter{States: []string{"failed"}, MaxAttempts: 5}); n != 2 {
		t.Errorf("states=failed matched %d rows, want 2 (exhausted failures must be retriable)", n)
	}
	if n := count(Filter{States: []string{"gone"}}); n != 1 {
		t.Errorf("states=gone matched %d rows, want 1", n)
	}
	if n := count(Filter{States: []string{"gone", "skipped"}}); n != 2 {
		t.Errorf("two states matched %d rows, want 2", n)
	}
	// fetched is a legitimate target: it re-checks rows that claim to be on disk.
	if n := count(Filter{States: []string{"fetched"}}); n != 1 {
		t.Errorf("states=fetched matched %d rows, want 1", n)
	}
	// Case and padding are normalised, so a hand-typed state still matches.
	if n := count(Filter{States: []string{" GONE "}}); n != 1 {
		t.Errorf(`states=" GONE " matched %d rows, want 1`, n)
	}
	// States compose with kinds.
	if n := count(Filter{States: []string{"gone", "fetched"}, Kinds: []string{"lta"}}); n != 2 {
		t.Errorf("state+kind filter matched %d rows, want 2", n)
	}

	// A state that cannot exist is an error, not an empty run: silence here would
	// look like a crawl that had nothing to do.
	if _, err := s.PendingInventory(ctx, 100, Filter{States: []string{"borked"}}); err == nil {
		t.Error("unknown state should be an error")
	} else if !strings.Contains(err.Error(), "pending") {
		t.Errorf("error should list the valid states, got %v", err)
	}
	if _, err := s.PendingByKind(ctx, Filter{States: []string{"borked"}}); err == nil {
		t.Error("unknown state should be an error for counts too")
	}

	// Counts honour states too, so the bar's total matches what is fetched.
	counts, err := s.PendingByKind(ctx, Filter{States: []string{"failed"}})
	if err != nil {
		t.Fatal(err)
	}
	if counts["ft"] != 2 {
		t.Errorf("PendingByKind(failed) = %v, want ft=2", counts)
	}
}

// Paging must advance strictly past the cursor, including off the end of the
// prefix. This is what stops a retry sweep re-offering rows that stay selected.
func TestPendingInventoryPagesAfterCursor(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var rows []Inventory
	for _, id := range []string{"a/1", "a/2", "a/3", "a/4", "a/5"} {
		rows = append(rows, Inventory{ELIURL: "https://x/eli/" + id, State: StatePending, Kind: "a"})
	}
	if err := s.PutInventoryBatch(ctx, rows); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	var after string
	for page := 0; page < 10; page++ {
		got, err := s.PendingInventory(ctx, 2, Filter{MaxAttempts: 5, Cursor: after})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 {
			break
		}
		for _, in := range got {
			if seen[in.ELIURL] {
				t.Fatalf("cursor paging offered %s twice", in.ELIURL)
			}
			seen[in.ELIURL] = true
		}
		after = got[len(got)-1].ELIURL
	}
	if len(seen) != 5 {
		t.Errorf("paged %d distinct rows, want 5", len(seen))
	}

	// A cursor at or past the last key must return nothing rather than wrapping.
	if got, err := s.PendingInventory(ctx, 10, Filter{MaxAttempts: 5, Cursor: "https://x/eli/a/5"}); err != nil {
		t.Fatal(err)
	} else if len(got) != 0 {
		t.Errorf("cursor at the last key returned %d rows, want 0: %v", len(got), got)
	}
	if got, err := s.PendingInventory(ctx, 10, Filter{MaxAttempts: 5, Cursor: "zzz-beyond-the-end"}); err != nil {
		t.Fatal(err)
	} else if len(got) != 0 {
		t.Errorf("cursor past the prefix returned %d rows, want 0", len(got))
	}
}

// TestPendingInventoryDescending covers the direction the live mirror wants: the
// newest document is the last key, so a descending scan has to start there and page
// downwards by cursor. This is also the only proof that scanInventory's hand-rolled
// reverse bound works -- the 0xff seek target has to land inside the inventory
// prefix rather than on the manifest keys that sort after it, and the walk has to
// stop at the bottom of the prefix rather than running into the accession keys.
func TestPendingInventoryDescending(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var rows []Inventory
	for _, id := range []string{"a/1", "a/2", "a/3", "a/4", "a/5"} {
		rows = append(rows, Inventory{ELIURL: "https://x/eli/" + id, State: StatePending, Kind: "a"})
	}
	// A manifest key sorts after every inventory key, and an accession key sorts
	// before them, so both bound the walk in the wrong place if the scan is sloppy.
	if err := s.PutInventoryBatch(ctx, rows); err != nil {
		t.Fatal(err)
	}

	want := []string{"a/5", "a/4", "a/3", "a/2", "a/1"}
	got, err := s.PendingInventory(ctx, 0, Filter{MaxAttempts: 5, Order: OrderDesc})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("descending returned %d rows, want %d", len(got), len(want))
	}
	for i, row := range got {
		if row.ELIURL != "https://x/eli/"+want[i] {
			t.Errorf("position %d = %s, want %s", i, row.ELIURL, want[i])
		}
	}

	// Paging must resume strictly below the cursor, not re-walk from the top.
	seen := map[string]bool{}
	var cursor string
	for page := 0; page < 10; page++ {
		pageRows, err := s.PendingInventory(ctx, 2, Filter{MaxAttempts: 5, Order: OrderDesc, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(pageRows) == 0 {
			break
		}
		for _, row := range pageRows {
			if seen[row.ELIURL] {
				t.Fatalf("descending cursor offered %s twice", row.ELIURL)
			}
			seen[row.ELIURL] = true
		}
		cursor = pageRows[len(pageRows)-1].ELIURL
	}
	if len(seen) != 5 {
		t.Errorf("paged %d distinct rows descending, want 5", len(seen))
	}

	// A cursor in the middle resumes on its near side, exclusive.
	below, err := s.PendingInventory(ctx, 10, Filter{MaxAttempts: 5, Order: OrderDesc, Cursor: "https://x/eli/a/3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(below) != 2 || below[0].ELIURL != "https://x/eli/a/2" || below[1].ELIURL != "https://x/eli/a/1" {
		t.Errorf("cursor a/3 descending returned %v, want a/2 then a/1", below)
	}
}

func TestPendingInventoryRejectsUnknownOrder(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.PendingInventory(context.Background(), 10, Filter{Order: "sideways"}); err == nil {
		t.Fatal("unknown order was accepted; it would silently crawl in some other order")
	}
}

// TestPendingKeysAndInventories is the Order=random path: keys only out, rows back
// in the same order, with rows that vanished between the two calls skipped rather
// than turning the batch into an error.
func TestPendingKeysAndInventories(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var rows []Inventory
	for _, id := range []string{"a/1", "a/2", "a/3"} {
		rows = append(rows, Inventory{ELIURL: "https://x/eli/" + id, State: StatePending, Kind: "a", LastMod: "2024-01-0" + id[2:]})
	}
	if err := s.PutInventoryBatch(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := s.PutInventory(ctx, Inventory{ELIURL: "https://x/eli/a/9", State: StateFetched, Kind: "a"}); err != nil {
		t.Fatal(err)
	}

	keys, err := s.PendingKeys(ctx, Filter{MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 {
		t.Fatalf("PendingKeys returned %d keys, want 3 (fetched rows must not be selected)", len(keys))
	}

	// Reversed on purpose: the rows must come back in the order asked for, since
	// that order is the shuffle.
	shuffled := []string{keys[2], "https://x/eli/gone/away", keys[0], keys[1]}
	got, err := s.Inventories(ctx, shuffled)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("Inventories returned %d rows, want 3 with the missing one skipped", len(got))
	}
	for i, want := range []string{keys[2], keys[0], keys[1]} {
		if got[i].ELIURL != want {
			t.Errorf("position %d = %s, want %s", i, got[i].ELIURL, want)
		}
	}
	if got[0].LastMod == "" {
		t.Error("Inventories returned a row that was not decoded from the store")
	}
}

// TestScanStart proves a descending batch resumes on its own cursor. Getting this
// wrong is not visible in the rows a scan returns -- pastCursor drops everything on
// the covered side either way -- so the seek target itself is the thing under test:
// restarting at the top of the prefix would re-walk every row above the cursor once
// per batch, which is the quadratic scan that already bit the ascending path.
func TestScanStartFollowsDirectionAndCursor(t *testing.T) {
	cases := []struct {
		name          string
		sel           selector
		wantLastBytes string
		wantCeiling   bool
	}{
		{"ascending, nothing covered", selector{}, "", false},
		{"ascending, cursor", selector{cursor: "https://x/eli/ft/1"}, "https://x/eli/ft/1", false},
		{"descending, nothing covered", selector{reverse: true}, "", true},
		{"descending, cursor", selector{reverse: true, cursor: "https://x/eli/ft/1"}, "https://x/eli/ft/1", false},
	}
	for _, tc := range cases {
		got := string(tc.sel.scanStart())
		if !strings.HasPrefix(got, prefixInventory) {
			t.Errorf("%s: start %q is outside the inventory prefix", tc.name, got)
			continue
		}
		rest := strings.TrimPrefix(got, prefixInventory)
		switch {
		case tc.wantCeiling:
			// The start of an uncovered descending walk must sit above every ELI URL
			// yet below the next key prefix, or the scan begins on manifest rows.
			if rest != "\xff\xff\xff\xff" {
				t.Errorf("%s: start tail = %q, want the 0xff ceiling above every inventory key", tc.name, rest)
			}
		default:
			if rest != tc.wantLastBytes {
				t.Errorf("%s: start tail = %q, want %q", tc.name, rest, tc.wantLastBytes)
			}
		}
	}
}

// TestBadgerPrefixAndReverseDoNotMix records why scanInventory bounds a reverse walk
// by hand: this Badger version returns no keys at all when ops.Prefix and Reverse are
// combined, which would otherwise look like an empty catalogue. If this test starts
// failing, Badger has fixed the pairing and the hand-rolled bound can be deleted.
func TestBadgerPrefixAndReverseDoNotMix(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.PutInventory(ctx, Inventory{ELIURL: "https://x/eli/a/1", State: StatePending, Kind: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.View(func(tx *badger.Txn) error {
		ops := badger.DefaultIteratorOptions
		ops.PrefetchValues = false
		ops.Reverse = true
		ops.Prefix = []byte(prefixInventory)
		it := tx.NewIterator(ops)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			t.Errorf("Prefix+Reverse yielded %q, so scanInventory's hand-rolled reverse bound is now unnecessary", it.Item().Key())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
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
