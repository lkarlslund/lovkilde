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
			{ELIURL: "https://x/eli/lta/2020/1", LastMod: "2026-09-01", Kind: "lta", State: StateFetched, Accession: "A1", SHA256: "h1", Bytes: 10},
			{ELIURL: "https://x/eli/lta/2020/2", LastMod: "2026-09-02", Kind: "lta", State: StateFailed, Attempts: 3, Error: "HTTP 500"},
		}),
		s.PutBody(ctx, Body{ELIURL: "https://x/eli/lta/2020/1", Kind: "lta", State: StateEmpty, Attempts: 1, Bytes: 5}),
		s.PutAccession(ctx, Accession{Accession: "A1", ELIURL: "https://x/eli/lta/2020/1", SHA256: "h1", Bytes: 10, ChangedAt: "2026-09-01T00:00:00Z"}),
		s.PutChanges(ctx, []Change{{Date: "2026-10-01", Accession: "A1", Reason: "updated", SeenAt: "2026-10-01T10:00:00Z"}}),
		s.MarkDayHarvested(ctx, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)),
		s.SetMeta(ctx, "last_full_sweep", "2026-10-01T11:00:00Z owed=1"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// same reports how the state in b differs from the one fill wrote, or "".
func same(t *testing.T, b *Store, withRetries bool) string {
	t.Helper()
	ctx := context.Background()
	var diffs []string
	failed, _, _ := b.GetInventory(ctx, "https://x/eli/lta/2020/2")
	wantAttempts := 0
	if withRetries {
		wantAttempts = 3
	}
	if failed.State != StateFailed || failed.LastMod != "2026-09-02" || failed.Attempts != wantAttempts {
		diffs = append(diffs, "failed row "+failed.ELIURL+" "+string(failed.State))
	}
	if in, _, _ := b.GetInventory(ctx, "https://x/eli/lta/2020/1"); in.SHA256 != "h1" || in.Accession != "A1" || in.State != StateFetched {
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

// Closing writes the two files and empties the journal; opening them again is the same state.
// Attempts and errors are in queue.json and nowhere in current.json.
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
	cur, _ := os.ReadFile(filepath.Join(dir, CurrentFile))
	if strings.Contains(string(cur), "HTTP 500") || strings.Contains(string(cur), "attempts") {
		t.Errorf("current.json carries retry bookkeeping:\n%s", cur)
	}
	if q, _ := os.ReadFile(filepath.Join(dir, QueueFile)); !strings.Contains(string(q), "HTTP 500") {
		t.Errorf("queue.json lacks the error:\n%s", q)
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if d := same(t, b, true); d != "" {
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
	if d := same(t, b, true); d != "" {
		t.Errorf("state replayed from the journal differs: %s", d)
	}
	if _, ok, _ := b.GetInventory(context.Background(), "torn"); ok {
		t.Error("a torn journal line was applied")
	}
}

// A fresh machine gets current.json alone: everything but the retry counters is there, and
// the rows still owed are still owed.
func TestCurrentStateAloneIsEnoughToCarryOn(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fill(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, QueueFile)); err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if d := same(t, b, false); d != "" {
		t.Errorf("state from current.json alone differs: %s", d)
	}
	if owed, _ := b.PendingInventory(context.Background(), 0, Filter{MaxAttempts: 5}); len(owed) != 1 {
		t.Errorf("rows owed after losing the queue: %+v", owed)
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
