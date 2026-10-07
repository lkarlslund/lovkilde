package artifactdir

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/compiled"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// writeArtifact writes a one-document artifact to path and returns its header.
func writeArtifact(t *testing.T, path string) compiled.ArtifactHeader {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := &lexdoc.Document{Identity: lexdoc.Identity{Accession: "A1", Title: "Lov"}}
	h, _, err := compiled.Build(f, "test", []compiled.ArtifactDoc{{Accession: "A1", Document: doc}})
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	return *h
}

// TestArtifactsAreNamedInstalledAndPruned: an artifact is kept under its build time and stamp,
// becomes current only when installed whole, and two are kept — the current and the previous —
// with their indexes, while older ones, their indexes and abandoned partial files go.
func TestArtifactsAreNamedInstalledAndPruned(t *testing.T) {
	data := t.TempDir()
	if _, err := Current(data); !errors.Is(err, ErrNone) {
		t.Fatalf("an empty data directory has a current artifact: %v", err)
	}
	if err := os.MkdirAll(Dir(data), 0o755); err != nil {
		t.Fatal(err)
	}
	var names []string
	for i := 0; i < 3; i++ {
		part := filepath.Join(Dir(data), "incoming"+Ext+PartExt)
		h := writeArtifact(t, part)
		path, err := Install(data, part)
		if err != nil {
			t.Fatal(err)
		}
		if want := Name(h); filepath.Base(path) != want || !strings.Contains(want, h.Stamp[:8]) || !strings.HasSuffix(want, Ext) {
			t.Fatalf("installed as %s, want %s", filepath.Base(path), want)
		}
		if cur, err := Current(data); err != nil || cur != path {
			t.Fatalf("current is %q (%v) after installing %s", cur, err, path)
		}
		// An index beside each, as a server would keep it.
		if err := os.WriteFile(IndexPath(path, h.Stamp), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		names = append(names, filepath.Base(path))
		time.Sleep(10 * time.Millisecond)
	}
	// Builds in the same minute share a time; the names must still be distinct.
	if names[0] == names[1] || names[1] == names[2] {
		t.Fatalf("two builds share a name: %v", names)
	}
	abandoned := filepath.Join(Dir(data), "old"+Ext+PartExt)
	fresh := filepath.Join(Dir(data), "running"+Ext+PartExt)
	for _, p := range []string{abandoned, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * abandonedAfter)
	if err := os.Chtimes(abandoned, old, old); err != nil {
		t.Fatal(err)
	}

	if _, err := Prune(data); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	entries, _ := os.ReadDir(Dir(data))
	for _, e := range entries {
		have[e.Name()] = true
	}
	current, _ := Current(data)
	artifacts := 0
	for n := range have {
		if strings.HasSuffix(n, Ext) {
			artifacts++
		}
	}
	if !have[filepath.Base(current)] || artifacts != 2 {
		t.Errorf("after pruning: %v; want the current artifact and one other", have)
	}
	if have["old"+Ext+PartExt] || !have["running"+Ext+PartExt] {
		t.Errorf("pruning should remove the abandoned partial file and keep the fresh one: %v", have)
	}
	indexes := 0
	for n := range have {
		if strings.HasSuffix(n, ".idx") {
			indexes++
		}
	}
	if indexes != 2 {
		t.Errorf("%d indexes kept, want the two kept artifacts' own: %v", indexes, have)
	}
}

// TestTheSingleFileLayoutMigrates: a data directory from before — data/artifact.bin and
// data/search.idx — is moved into this layout the first time it is asked for its current artifact.
func TestTheSingleFileLayoutMigrates(t *testing.T) {
	data := t.TempDir()
	h := writeArtifact(t, filepath.Join(data, "artifact.bin"))
	if err := os.WriteFile(filepath.Join(data, "search.idx"), []byte("index"), 0o644); err != nil {
		t.Fatal(err)
	}
	cur, err := Current(data)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(cur) != Name(h) {
		t.Errorf("migrated to %s, want %s", filepath.Base(cur), Name(h))
	}
	if _, err := os.Stat(filepath.Join(data, "artifact.bin")); !os.IsNotExist(err) {
		t.Error("the old file is still where it was")
	}
	if b, err := os.ReadFile(IndexPath(cur, h.Stamp)); err != nil || string(b) != "index" {
		t.Errorf("the index did not move with its artifact: %v", err)
	}
}
