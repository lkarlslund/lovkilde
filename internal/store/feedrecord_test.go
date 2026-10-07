package store

import (
	"context"
	"path/filepath"
	"testing"

	badger "github.com/dgraph-io/badger/v4"
)

// TestAChangeKeepsItsFirstSighting: the current day is harvested again on every run, and a
// held copy is compared against when the change was first seen; re-recording must not move it.
func TestAChangeKeepsItsFirstSighting(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	c := Change{Date: "2026-10-03", Accession: "A1", Reason: "DocumentMetadataChanged", SeenAt: "2026-10-03T10:00:00Z"}
	if err := s.PutChanges(ctx, []Change{c}); err != nil {
		t.Fatal(err)
	}
	c.SeenAt = "2026-10-06T10:00:00Z"
	if err := s.PutChanges(ctx, []Change{c}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Changes(ctx, "2026-10-01", 0)
	if err != nil || len(got) != 1 || got[0].SeenAt != "2026-10-03T10:00:00Z" {
		t.Fatalf("changes = %+v (%v), want one, first seen 2026-10-03T10:00:00Z", got, err)
	}
}

// TestAnAccessionRecordsWhenItWasFetched: FetchedAt survives the store, and a record written
// before the field existed still reads, with ChangedAt as its HeldSince.
func TestAnAccessionRecordsWhenItWasFetched(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	a := Accession{Accession: "A1", ELIURL: "u", SHA256: "h", Bytes: 3, ChangedAt: "2026-09-01T00:00:00Z", FetchedAt: "2026-10-06T00:00:00Z"}
	if err := s.PutAccession(ctx, a); err != nil {
		t.Fatal(err)
	}
	back, _, err := s.GetAccession(ctx, "A1")
	if err != nil || back.FetchedAt != a.FetchedAt || back.HeldSince() != a.FetchedAt {
		t.Fatalf("read back %+v (%v)", back, err)
	}
	old := "u\th\t3\t2026-09-01T00:00:00Z\tB2" // the five-field form
	if err := s.db.Update(func(tx *badger.Txn) error { return tx.Set([]byte(prefixAccession+"B2"), []byte(old)) }); err != nil {
		t.Fatal(err)
	}
	b, _, err := s.GetAccession(ctx, "B2")
	if err != nil || b.FetchedAt != "" || b.HeldSince() != "2026-09-01T00:00:00Z" {
		t.Fatalf("old record read as %+v (%v)", b, err)
	}
}
