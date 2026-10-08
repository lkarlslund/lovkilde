package store

import (
	"context"
	"path/filepath"
	"strings"
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
		Kind: "lta", State: StateFetched,
		Accession: "B20260078905", Hash: "abc123", Bytes: 72366,
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

func TestPendingSkipsRecentFailuresAndFetched(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rows := []Inventory{
		{ELIURL: "https://x/eli/a/1", State: StatePending, Kind: "a"},
		{ELIURL: "https://x/eli/a/2", State: StateFetched, Kind: "a", Hash: "deadbeef"},
		{ELIURL: "https://x/eli/a/3", State: StateFailed, TriedAt: longAgo, Kind: "a"},
		{ELIURL: "https://x/eli/a/4", State: StateFailed, TriedAt: justNow, Kind: "a"},
		{ELIURL: "https://x/eli/a/5", State: StateGone, Kind: "a"},
		{ELIURL: "https://x/eli/a/6", State: StateSkipped, Kind: "a"},
	}
	if err := s.PutInventoryBatch(ctx, rows); err != nil {
		t.Fatal(err)
	}
	got, err := s.PendingInventory(ctx, 100, Filter{RetryAfter: RetryFailedAfter})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("PendingInventory returned %d rows, want 2: %+v", len(got), got)
	}
	for _, r := range got {
		if r.ELIURL != "https://x/eli/a/1" && r.ELIURL != "https://x/eli/a/3" {
			t.Errorf("unexpected pending row %s (state %s, tried %s)", r.ELIURL, r.State, r.TriedAt)
		}
	}

	// A limit must be honoured so batches stay small and memory flat.
	limited, err := s.PendingInventory(ctx, 1, Filter{RetryAfter: RetryFailedAfter})
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
		{ELIURL: "https://x/eli/retsinfo/2", State: StateFailed, TriedAt: longAgo, Kind: "retsinfo"},
		{ELIURL: "https://x/eli/lta/1", State: StateFetched, Kind: "lta", Hash: "aa"},
		{ELIURL: "https://x/eli/lta/2", State: StateGone, Kind: "lta"},
	}
	if err := s.PutInventoryBatch(ctx, rows); err != nil {
		t.Fatal(err)
	}

	// One kind only, and retryable failures count while fetched/gone do not.
	only, err := s.PendingInventory(ctx, 100, Filter{RetryAfter: RetryFailedAfter, Kinds: []string{"retsinfo"}})
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
	if many, err := s.PendingInventory(ctx, 100, Filter{RetryAfter: RetryFailedAfter, Kinds: []string{"ft", "retsinfo"}}); err != nil {
		t.Fatal(err)
	} else if len(many) != 3 {
		t.Errorf("two-kind filter = %d rows, want 3", len(many))
	}
	if none, err := s.PendingInventory(ctx, 100, Filter{RetryAfter: RetryFailedAfter, Kinds: []string{"nosuchkind"}}); err != nil {
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
		{ELIURL: "https://x/eli/ft/2", State: StateFailed, TriedAt: longAgo, Kind: "ft"},
		{ELIURL: "https://x/eli/ft/3", State: StateFailed, TriedAt: justNow, Kind: "ft"},
		{ELIURL: "https://x/eli/lta/1", State: StateGone, Kind: "lta", Error: "404 Not Found"},
		{ELIURL: "https://x/eli/lta/2", State: StateFetched, Kind: "lta", Hash: "aa"},
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
	if n := count(Filter{States: []string{"failed"}, RetryAfter: RetryFailedAfter}); n != 2 {
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
		got, err := s.PendingInventory(ctx, 2, Filter{RetryAfter: RetryFailedAfter, Cursor: after})
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
	if got, err := s.PendingInventory(ctx, 10, Filter{RetryAfter: RetryFailedAfter, Cursor: "https://x/eli/a/5"}); err != nil {
		t.Fatal(err)
	} else if len(got) != 0 {
		t.Errorf("cursor at the last key returned %d rows, want 0: %v", len(got), got)
	}
	if got, err := s.PendingInventory(ctx, 10, Filter{RetryAfter: RetryFailedAfter, Cursor: "zzz-beyond-the-end"}); err != nil {
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
	got, err := s.PendingInventory(ctx, 0, Filter{RetryAfter: RetryFailedAfter, Order: OrderDesc})
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
		pageRows, err := s.PendingInventory(ctx, 2, Filter{RetryAfter: RetryFailedAfter, Order: OrderDesc, Cursor: cursor})
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
	below, err := s.PendingInventory(ctx, 10, Filter{RetryAfter: RetryFailedAfter, Order: OrderDesc, Cursor: "https://x/eli/a/3"})
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

	keys, err := s.PendingKeys(ctx, Filter{RetryAfter: RetryFailedAfter})
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
	// An accession is held by a catalogue row, so one for a URL without a row is refused.
	if err := s.PutAccession(ctx, Accession{Accession: "B20260078905", ELIURL: "https://x/eli/lta/2026/789", ChangedAt: "2026-09-17T00:00:00Z"}); err == nil {
		t.Fatal("an accession for a URL with no catalogue row was recorded")
	}
	if err := s.PutInventory(ctx, Inventory{ELIURL: "https://x/eli/lta/2026/789", Kind: "lta", State: StateFetched}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAccession(ctx, Accession{Accession: "B20260078905", ELIURL: "https://x/eli/lta/2026/789", Hash: "h1", Bytes: 72366, ChangedAt: "2026-09-17T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetAccession(ctx, "B20260078905")
	if err != nil || !found {
		t.Fatalf("accession lookup: found=%v err=%v", found, err)
	}
	if got.Hash != "h1" || got.Bytes != 72366 {
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
	if err := s.PutInventory(ctx, Inventory{ELIURL: "https://x/eli/accn/" + c.Accession, State: StateFetched}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAccession(ctx, Accession{Accession: c.Accession, ELIURL: "https://x/eli/accn/" + c.Accession, Hash: "h", ChangedAt: "2026-09-17T00:00:00Z"}); err != nil {
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

func TestBodyRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	in := Body{
		ELIURL: "https://www.retsinformation.dk/eli/lta/1995/498", Kind: "lta",
		State: StateEmpty, Hash: "deadbeef", Bytes: 12611,
		TextChars: 12, Error: "payload has no text",
	}
	if err := s.PutBody(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetBody(ctx, in.ELIURL)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("body record not found after write")
	}
	if got != in {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, in)
	}
}

// The body queue shares the store with the live mirror and keys rows by the same
// ELI URL. If the two keyspaces ever leak into each other, the XML crawler starts
// re-requesting 29k documents it already holds -- or the backfill starts refetching
// canonical XML as if it were a body -- and neither shows up as an error, just as a
// crawl that never converges. So the same URL is used on both sides here.
func TestBodyRowsDoNotMixWithInventory(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	url := "https://www.retsinformation.dk/eli/lta/1995/498"
	if err := s.PutInventory(ctx, Inventory{ELIURL: url, Kind: "lta", State: StatePending, Hash: "xmlhash", Bytes: 700}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutBody(ctx, Body{ELIURL: url, Kind: "lta", State: StatePending, Hash: "jsonhash", Bytes: 12611}); err != nil {
		t.Fatal(err)
	}

	inv, err := s.PendingInventory(ctx, 10, Filter{RetryAfter: RetryFailedAfter})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv) != 1 || inv[0].Hash != "xmlhash" {
		t.Fatalf("inventory queue = %+v, want only the XML row", inv)
	}
	bodies, err := s.PendingBodies(ctx, 10, Filter{RetryAfter: RetryFailedAfter})
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || bodies[0].Hash != "jsonhash" {
		t.Fatalf("body queue = %+v, want only the body row", bodies)
	}

	invCount, invTotal, err := s.CountByState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if invTotal != 1 || invCount[StatePending] != 1 {
		t.Errorf("inventory census = %v total %d, want one pending row only", invCount, invTotal)
	}
	bodyCount, bodyTotal, err := s.CountBodiesByState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bodyTotal != 1 || bodyCount[StatePending] != 1 {
		t.Errorf("body census = %v total %d, want one pending row only", bodyCount, bodyTotal)
	}

	// The accession index is a third keyspace; seeding it proves the body prefix
	// does not sweep up anything that merely starts with a similar byte.
	if err := s.PutAccession(ctx, Accession{Accession: "A19950049829", ELIURL: url, Hash: "xmlhash", Bytes: 700, ChangedAt: "2026-09-17"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PendingBodies(ctx, 10, Filter{RetryAfter: RetryFailedAfter}); err != nil || len(got) != 1 {
		t.Errorf("body queue after accession seed = %+v (err %v), want still one row", got, err)
	}
}

// The automatic sweep must only offer rows that are genuinely still work. empty and
// gone are conclusions about the corpus -- Finanslove have no prose to fetch -- so a
// sweep that handed them back would re-request ~10k documents on every pass and
// never converge. Only an explicit --state selection may retry them.
func TestPendingBodiesSweepsOnlyRetryableStates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seed := []Body{
		{ELIURL: "https://x/eli/lta/1", Kind: "lta", State: StatePending},
		{ELIURL: "https://x/eli/lta/2", Kind: "lta", State: StateFetched},
		{ELIURL: "https://x/eli/lta/3", Kind: "lta", State: StateFailed, TriedAt: longAgo},
		{ELIURL: "https://x/eli/lta/4", Kind: "lta", State: StateGone},
		{ELIURL: "https://x/eli/lta/5", Kind: "lta", State: StateEmpty},
		{ELIURL: "https://x/eli/lta/6", Kind: "lta", State: StateSkipped},
	}
	if err := s.PutBodyBatch(ctx, seed); err != nil {
		t.Fatal(err)
	}

	got, err := s.PendingBodies(ctx, 10, Filter{RetryAfter: RetryFailedAfter})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("default sweep offered %d rows (%v), want pending and failed only", len(got), got)
	}
	if err := s.PutBody(ctx, Body{ELIURL: "https://x/eli/lta/7", Kind: "lta", State: StateFailed, TriedAt: justNow}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PendingBodies(ctx, 10, Filter{RetryAfter: RetryFailedAfter}); err != nil || len(got) != 2 {
		t.Errorf("default sweep with a failure tried just now = %+v (err %v), want it left until its week is up", got, err)
	}

	for _, st := range []State{StateGone, StateEmpty, StateFetched, StateSkipped} {
		got, err := s.PendingBodies(ctx, 10, Filter{States: []string{string(st)}})
		if err != nil {
			t.Fatalf("state %s: %v", st, err)
		}
		if len(got) != 1 {
			t.Errorf("explicit --state %s offered %d rows, want 1", st, len(got))
		}
	}
	if _, err := s.PendingBodies(ctx, 10, Filter{States: []string{"bogus"}}); err == nil {
		t.Error("an unknown state was accepted, so a typo in --state silently fetches nothing")
	}
}

// Body rows page the same way inventory rows do, and the shared scan is only proven
// prefix-generic if it also works for the body keyspace: a cursor that escaped the
// prefix would either re-offer rows or walk into the inventory keys.
func TestPendingBodiesPagesAfterCursor(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var rows []Body
	for _, id := range []string{"a/1", "a/2", "a/3", "a/4", "a/5"} {
		rows = append(rows, Body{ELIURL: "https://x/eli/" + id, Kind: "a", State: StatePending})
	}
	// An inventory row sorts after every body key, so the walk has to stop at the
	// top of the body prefix instead of spilling into it.
	if err := s.PutInventory(ctx, Inventory{ELIURL: "https://x/eli/z/9", Kind: "z", State: StatePending}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutBodyBatch(ctx, rows); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	var after string
	for page := 0; page < 10; page++ {
		got, err := s.PendingBodies(ctx, 2, Filter{RetryAfter: RetryFailedAfter, Cursor: after})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 {
			break
		}
		for _, b := range got {
			if seen[b.ELIURL] {
				t.Fatalf("cursor paging offered %s twice", b.ELIURL)
			}
			seen[b.ELIURL] = true
		}
		after = got[len(got)-1].ELIURL
	}
	if len(seen) != 5 {
		t.Errorf("paged %d distinct body rows, want 5", len(seen))
	}
	if got, err := s.PendingBodies(ctx, 10, Filter{RetryAfter: RetryFailedAfter, Cursor: "zzz-beyond-the-end"}); err != nil || len(got) != 0 {
		t.Errorf("cursor past the prefix returned %+v (err %v), want nothing", got, err)
	}

	// Descending is what the live backfill wants once it starts from the newest law.
	got, err := s.PendingBodies(ctx, 2, Filter{RetryAfter: RetryFailedAfter, Order: "desc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ELIURL != "https://x/eli/a/5" {
		t.Errorf("descending first page = %+v, want the newest body rows", got)
	}
}

// The census is also the deliverable: "how many laws have no text anywhere" is a
// question about the corpus, and empty must not be folded into failed.
func TestCountBodiesByStateSeparatesEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.PutBodyBatch(ctx, []Body{
		{ELIURL: "https://x/eli/lta/1", Kind: "lta", State: StateFetched, TextChars: 5000},
		{ELIURL: "https://x/eli/lta/2", Kind: "lta", State: StateEmpty, TextChars: 12},
		{ELIURL: "https://x/eli/lta/3", Kind: "lta", State: StateFailed, Error: "timeout"},
	}); err != nil {
		t.Fatal(err)
	}
	counts, total, err := s.CountBodiesByState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Errorf("total %d, want 3", total)
	}
	if counts[StateFetched] != 1 || counts[StateEmpty] != 1 || counts[StateFailed] != 1 {
		t.Errorf("counts %v, want one each of fetched, empty, failed", counts)
	}
}

// TestPutInventoryBatchPreservesState is the guard for the nightly refill.
//
// A sitemap pass reports every resource it has ever advertised, so the rows it
// hands over carry only what the sitemap knows: URL, lastmod, kind, and pending.
// Overwriting on upsert throws away the hash, the accession, the attempt counter,
// and the fact that a resource was already fetched -- turning a daily run into a
// full re-crawl, including re-requests for resources long since recorded as gone.
// Only a moved lastmod may re-pend a row.
func TestPutInventoryBatchPreservesState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	have := []Inventory{
		{ELIURL: "https://x/eli/lta/1", LastMod: "2026-01-01", Kind: "lta", State: StateFetched,
			Accession: "B20260000105", Hash: "hash1", Bytes: 5000},
		{ELIURL: "https://x/eli/lta/2", LastMod: "2026-01-02", Kind: "lta", State: StateGone,
			TriedAt: longAgo, Error: "404 Not Found"},
		// A document we hold, so a moved lastmod has to re-pend it while keeping the
		// hash: that pairing is what lets the next fetch tell an amended text from a
		// republication of the same one.
		{ELIURL: "https://x/eli/lta/3", LastMod: "2026-01-03", Kind: "lta", State: StateFetched,
			Accession: "B20260000305", Hash: "hash3", Bytes: 1200},
	}
	if err := s.PutInventoryBatch(ctx, have); err != nil {
		t.Fatal(err)
	}

	// The next sitemap pass: same lastmod for rows 1 and 2, a newer one for row 1's
	// neighbour below, and one resource the store has never seen.
	observed := []Inventory{
		{ELIURL: "https://x/eli/lta/1", LastMod: "2026-01-01", Kind: "lta", State: StatePending},
		{ELIURL: "https://x/eli/lta/2", LastMod: "2026-01-02", Kind: "lta", State: StatePending},
		{ELIURL: "https://x/eli/lta/3", LastMod: "2026-09-18", Kind: "lta", State: StatePending},
		{ELIURL: "https://x/eli/lta/4", LastMod: "2026-09-18", Kind: "lta", State: StatePending},
	}
	if err := s.PutInventoryBatch(ctx, observed); err != nil {
		t.Fatal(err)
	}

	// Unchanged lastmod: the conclusion survives untouched, its last try included, so
	// a dead resource is not retried forever and a fetched one is not re-fetched.
	for _, want := range []Inventory{
		{ELIURL: "https://x/eli/lta/1", State: StateFetched, Accession: "B20260000105", Hash: "hash1", Bytes: 5000},
		{ELIURL: "https://x/eli/lta/2", State: StateGone, TriedAt: longAgo, Error: "404 Not Found"},
	} {
		got, found, err := s.GetInventory(ctx, want.ELIURL)
		if err != nil || !found {
			t.Fatalf("GetInventory(%s): found=%v err=%v", want.ELIURL, found, err)
		}
		if got.State != want.State || got.TriedAt != want.TriedAt || got.Hash != want.Hash ||
			got.Accession != want.Accession || got.Bytes != want.Bytes || got.Error != want.Error {
			t.Errorf("%s unchanged lastmod rewrote the row: got %+v, want state %s tried %s accession %s hash %s bytes %d error %q",
				want.ELIURL, got, want.State, want.TriedAt, want.Accession, want.Hash, want.Bytes, want.Error)
		}
	}

	// Moved lastmod: stale, not pending -- pending is the state the fetch loop
	// answers from disk, which would discard the change signal. The identity fields
	// must survive so the fetch can tell an amended text from a republication.
	got3, found, err := s.GetInventory(ctx, "https://x/eli/lta/3")
	if err != nil || !found {
		t.Fatalf("GetInventory row 3: found=%v err=%v", found, err)
	}
	if got3.State != StateStale || got3.LastMod != "2026-09-18" {
		t.Errorf("moved lastmod did not mark the held row stale: %+v", got3)
	}
	if got3.Hash != "hash3" || got3.Accession != "B20260000305" || got3.Bytes != 1200 {
		t.Errorf("re-pending threw away the identity of a held document: %+v", got3)
	}

	// A resource the sitemap newly advertises is written as observed.
	got4, found, err := s.GetInventory(ctx, "https://x/eli/lta/4")
	if err != nil || !found {
		t.Fatalf("new resource missing after batch: found=%v err=%v", found, err)
	}
	if got4.State != StatePending || got4.LastMod != "2026-09-18" {
		t.Errorf("new resource stored as %+v", got4)
	}
}

// TestChangesRange pins the read-back path for the change feed: a date filter
// that silently drops the boundary day, or a limit that stops early, both make
// "we harvested N changes and collected them" unverifiable.
func TestChangesRange(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.PutChanges(ctx, []Change{
		{Date: "2026-09-16", Accession: "B20260000001", Reason: "BEKGJOR", DocumentID: "1", DocType: "LBK H", Href: "/a", SeenAt: "t"},
		{Date: "2026-09-17", Accession: "B20260000002", Reason: "AENDRING", DocumentID: "2", DocType: "LBK H", Href: "/b", SeenAt: "t"},
		{Date: "2026-09-18", Accession: "B20260000002", Reason: "AENDRING", DocumentID: "2", DocType: "LBK H", Href: "/b", SeenAt: "t"},
	}); err != nil {
		t.Fatal(err)
	}

	all, err := s.Changes(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("unfiltered changes returned %d, want 3: %+v", len(all), all)
	}
	if all[0].Date != "2026-09-16" || all[2].Date != "2026-09-18" {
		t.Errorf("changes are not oldest-first: %s .. %s", all[0].Date, all[2].Date)
	}
	if all[1].Accession != "B20260000002" || all[1].Reason != "AENDRING" || all[1].DocType != "LBK H" || all[1].Href != "/b" {
		t.Errorf("key or value fields lost in the round trip: %+v", all[1])
	}

	fromDay, err := s.Changes(ctx, "2026-09-17", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fromDay) != 2 {
		t.Errorf("--days filter returned %d rows, want the 2 on and after 2026-09-17: %+v", len(fromDay), fromDay)
	}

	limited, err := s.Changes(ctx, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 {
		t.Errorf("limit 1 returned %d rows", len(limited))
	}
}

// TestIssuesCountsStale pins what the collection record counts once a publisher has
// moved on from a document we hold: a stale row is work the sweep owes, a failed one is
// a document the site refused, and a fetched one is neither.
func TestIssuesCountsStale(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.PutInventoryBatch(ctx, []Inventory{
		{ELIURL: "https://x/eli/lta/1", LastMod: "2026-09-18", Kind: "lta", State: StateStale, Hash: "h1"},
		{ELIURL: "https://x/eli/lta/2", LastMod: "2026-09-18", Kind: "lta", State: StateFailed, TriedAt: justNow, Error: "500"},
		{ELIURL: "https://x/eli/lta/3", LastMod: "2026-09-18", Kind: "lta", State: StateFetched, Hash: "h3"},
	}); err != nil {
		t.Fatal(err)
	}

	issues, err := s.InventoryIssues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if issues.Owed != 1 || issues.Failed != 1 {
		t.Errorf("issues %s, want owed 1 and failed 1: the stale row is owed work, the refused one failed, and the fetched row is neither", issues)
	}
	// The fingerprint is what tells two runs apart when the counts are the same, so it
	// has to depend on which rows are outstanding, not merely how many.
	if issues.Digest == "" {
		t.Fatal("outstanding rows produced no fingerprint")
	}
	before := issues.Digest
	if again, err := s.InventoryIssues(ctx); err != nil || again.Digest != before {
		t.Errorf("the same outstanding set fingerprinted differently: %q then %q", before, again.Digest)
	}
	if err := s.PutInventory(ctx, Inventory{ELIURL: "https://x/eli/lta/4", LastMod: "2026-09-18", Kind: "lta", State: StatePending, Hash: "h4"}); err != nil {
		t.Fatal(err)
	}
	moved, err := s.InventoryIssues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Digest == before {
		t.Errorf("a different outstanding set produced the same fingerprint %q", moved.Digest)
	}

	// Nothing outstanding is reported as clean, with no fingerprint to compare.
	// Draining the queue goes through the single-row write the fetch path uses. The
	// batch path is an inventory pass, and an inventory pass reporting a later date is
	// the publisher saying the text changed -- which re-queues the row rather than
	// settling it -- so it cannot be used to record a completed download here.
	for _, u := range []string{"1", "2", "3", "4"} {
		if err := s.PutInventory(ctx, Inventory{ELIURL: "https://x/eli/lta/" + u, LastMod: "2026-09-18", Kind: "lta", State: StateFetched, Hash: "h" + u}); err != nil {
			t.Fatal(err)
		}
	}
	clean, err := s.InventoryIssues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if clean.Total() != 0 || clean.Digest != "" || clean.String() != "clean" {
		t.Errorf("a drained queue reads as %q", clean)
	}
}

// TestPutInventoryBatchIgnoresOlderLastmod is the duplicate-entry guard.
//
// The sitemap advertises some resources twice per pass and the two entries do not
// always carry the same date. Treating any difference as a change made the row
// stale on the pass that saw the older date, so two passes an hour apart marked
// 872 documents as amended and queued them for re-download. A lastmod that moves
// backwards, or merely disagrees, is not the publisher saying the text changed.
func TestPutInventoryBatchIgnoresOlderLastmod(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.PutInventory(ctx, Inventory{
		ELIURL: "https://x/eli/lta/9", LastMod: "2026-09-10", Kind: "lta", State: StateFetched,
		Accession: "B20260000905", Hash: "hash9", Bytes: 2500,
	}); err != nil {
		t.Fatal(err)
	}
	// Two pages, same resource, disagreeing dates -- the older one arriving second.
	if err := s.PutInventoryBatch(ctx, []Inventory{
		{ELIURL: "https://x/eli/lta/9", LastMod: "2026-09-10", Kind: "lta", State: StatePending},
		{ELIURL: "https://x/eli/lta/9", LastMod: "2026-05-01", Kind: "lta", State: StatePending},
	}); err != nil {
		t.Fatal(err)
	}

	got, found, err := s.GetInventory(ctx, "https://x/eli/lta/9")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got.State != StateFetched || got.LastMod != "2026-09-10" {
		t.Errorf("a disagreeing duplicate entry rewrote the row: %+v, want fetched with the newest date kept", got)
	}

	// A genuinely later date still counts, which is the point of the comparison.
	if err := s.PutInventoryBatch(ctx, []Inventory{
		{ELIURL: "https://x/eli/lta/9", LastMod: "2026-09-18", Kind: "lta", State: StatePending},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := s.GetInventory(ctx, "https://x/eli/lta/9"); err != nil {
		t.Fatal(err)
	} else if got.State != StateStale || got.LastMod != "2026-09-18" {
		t.Errorf("a later lastmod did not mark the held row stale: %+v", got)
	}
}

// longAgo and justNow are last-tried times either side of RetryFailedAfter.
var (
	longAgo = "2020-01-01T00:00:00Z"
	justNow = time.Now().UTC().Format(time.RFC3339)
)
