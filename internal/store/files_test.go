package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fill writes one of everything the state holds.
func fill(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for _, err := range []error{
		s.PutInventoryBatch(ctx, []Inventory{
			{ELIURL: "https://x/eli/lta/2020/1", LastMod: "2026-09-01", Kind: "lta", State: StateFetched, Accession: "A1", Hash: "h1", Bytes: 10},
			{ELIURL: "https://x/eli/lta/2020/2", LastMod: "2026-09-02", Kind: "lta", State: StateFailed, TriedAt: "2026-10-01T12:00:00Z", Error: "HTTP 500"},
		}),
		s.PutBody(ctx, Body{ELIURL: "https://x/eli/lta/2020/1", Kind: "lta", State: StateEmpty, Bytes: 5}),
		s.PutAccession(ctx, Accession{Accession: "A1", ELIURL: "https://x/eli/lta/2020/1", Hash: "h1", Bytes: 10, ChangedAt: "2026-09-01T00:00:00Z"}),
		// Dated today: changes past changeRetention are dropped when the state is written.
		s.PutChanges(ctx, []Change{{Date: time.Now().Format(time.DateOnly), Accession: "A1", Reason: "updated", SeenAt: "2026-10-01T10:00:00Z"}}),
		s.MarkDayHarvested(ctx, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)),
		s.SetMeta(ctx, "last_full_sweep", "2026-10-01T11:00:00Z owed=1"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// same reports how the state in b differs from the one fill wrote, or "".
func same(t *testing.T, b *Store) string {
	t.Helper()
	ctx := context.Background()
	var diffs []string
	failed, _, _ := b.GetInventory(ctx, "https://x/eli/lta/2020/2")
	if failed.State != StateFailed || failed.LastMod != "2026-09-02" || failed.TriedAt != "2026-10-01T12:00:00Z" || failed.Error != "HTTP 500" {
		diffs = append(diffs, "failed row "+failed.ELIURL+" "+string(failed.State))
	}
	if in, _, _ := b.GetInventory(ctx, "https://x/eli/lta/2020/1"); in.Hash != "h1" || in.Accession != "A1" || in.State != StateFetched {
		diffs = append(diffs, "fetched row")
	}
	if body, ok, _ := b.GetBody(ctx, "https://x/eli/lta/2020/1"); !ok || body.State != StateEmpty || body.Bytes != 5 {
		diffs = append(diffs, "body row")
	}
	if a, ok, _ := b.GetAccession(ctx, "A1"); !ok || a.ELIURL != "https://x/eli/lta/2020/1" || a.ChangedAt == "" {
		diffs = append(diffs, "accession")
	}
	if cs, _ := b.Changes(ctx, "", 0); len(cs) != 1 || cs[0].SeenAt != "2026-10-01T10:00:00Z" {
		diffs = append(diffs, "changes")
	}
	if d, ok, _ := b.LastHarvestedDay(ctx); !ok || d.Format(time.DateOnly) != "2026-10-01" {
		diffs = append(diffs, "harvested day")
	}
	if v, _, _ := b.GetMeta(ctx, "last_full_sweep"); v != "2026-10-01T11:00:00Z owed=1" {
		diffs = append(diffs, "meta")
	}
	return strings.Join(diffs, ", ")
}

// Closing writes the files and empties the journal; opening them again is the same state. A
// failed document is part of it -- when it was last tried and what the site said -- and a table
// is one row per line.
func TestTheStateSurvivesCloseAndOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fill(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir, JournalFile)); err != nil || info.Size() != 0 {
		t.Errorf("the journal after a close: %v, %v", info, err)
	}
	inv, _ := os.ReadFile(filepath.Join(dir, InventoryFile))
	if !strings.Contains(string(inv), `"tried_at":"2026-10-01T12:00:00Z","error":"HTTP 500"`) {
		t.Errorf("%s does not record the failure:\n%s", InventoryFile, inv)
	}
	if lines := strings.Count(string(inv), "\n"); lines != 2 {
		t.Errorf("%s has %d lines for two rows:\n%s", InventoryFile, lines, inv)
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if d := same(t, b); d != "" {
		t.Errorf("reopened state differs: %s", d)
	}
}

// A process killed before Close loses nothing: the journal holds every change, and a torn
// last line is ignored.
func TestAKilledRunLosesNothing(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fill(t, s)
	// No Close: release the lock as a dying process would, and tear the last write.
	s.journal.f.Write([]byte(`{"inv":{"url":"torn`))
	s.journal.close()
	s.unlock()

	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if d := same(t, b); d != "" {
		t.Errorf("state replayed from the journal differs: %s", d)
	}
	if _, ok, _ := b.GetInventory(context.Background(), "torn"); ok {
		t.Error("a torn journal line was applied")
	}
}

// One collector at a time.
func TestTheStateIsLocked(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("a second Open of the same state succeeded")
	}
	s.Close()
	again, err := Open(dir)
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	again.Close()
}

// An accession lives on the row that holds it. Two rows may name the same accession (the live
// mirror has one: lta/1963/7 and ltc/1963/7); recording it on the second moves it, and the
// first stops holding it, across a reopen too. Rewriting the holding row keeps its times.
func TestAnAccessionMovesBetweenTheRowsThatNameIt(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const lta, ltc = "https://x/eli/lta/1963/7", "https://x/eli/ltc/1963/7"
	for _, u := range []string{lta, ltc} {
		if err := s.PutInventory(ctx, Inventory{ELIURL: u, State: StateFetched, Accession: "B1", Hash: "h-" + u}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutAccession(ctx, Accession{Accession: "B1", ELIURL: lta, Hash: "h-" + lta, ChangedAt: "2026-09-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	// The fetch loop rewrites the holding row; the accession's times stay with it.
	if err := s.PutInventory(ctx, Inventory{ELIURL: lta, State: StateFetched, Accession: "B1", Hash: "h2"}); err != nil {
		t.Fatal(err)
	}
	if a, _, _ := s.GetAccession(ctx, "B1"); a.ELIURL != lta || a.Hash != "h2" || a.ChangedAt != "2026-09-01T00:00:00Z" {
		t.Fatalf("after rewriting the holding row: %+v", a)
	}
	if err := s.PutAccession(ctx, Accession{Accession: "B1", ELIURL: ltc, Hash: "h-" + ltc, ChangedAt: "2026-10-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if a, ok, _ := b.GetAccession(ctx, "B1"); !ok || a.ELIURL != ltc || a.ChangedAt != "2026-10-01T00:00:00Z" {
		t.Errorf("after moving and reopening: %+v (found %v)", a, ok)
	}
	if left := b.inventory.rows[lta]; left.holds() || left.Accession != "B1" {
		t.Errorf("the row the accession left: %+v", left)
	}
}

// Feed changes older than changeRetention are dropped when the state is written.
func TestOldFeedChangesAreDropped(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	old := time.Now().Add(-changeRetention - 48*time.Hour).Format(time.DateOnly)
	recent := time.Now().Format(time.DateOnly)
	if err := s.PutChanges(ctx, []Change{{Date: old, Accession: "A1", Reason: "x"}, {Date: recent, Accession: "A2", Reason: "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if got, _ := b.Changes(ctx, "", 0); len(got) != 1 || got[0].Date != recent {
		t.Errorf("changes kept: %+v", got)
	}
}

// A save rewrites only the tables that changed, so a fetch run does not rewrite the body table.
func TestASaveLeavesUnchangedTablesAlone(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fill(t, s)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	bodies := filepath.Join(dir, BodiesFile)
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(bodies, past, past); err != nil {
		t.Fatal(err)
	}
	if err := s.PutInventory(context.Background(), Inventory{ELIURL: "https://x/eli/lta/2020/3", State: StatePending}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(bodies); !info.ModTime().Equal(past) {
		t.Errorf("%s was rewritten though no body row changed", BodiesFile)
	}
}
