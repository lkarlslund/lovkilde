package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lkarlslund/lovkilde/internal/artifactdir"
	"github.com/lkarlslund/lovkilde/internal/compiled"
	"github.com/lkarlslund/lovkilde/internal/search"
)

// TestIndexIsBuiltFromTheArtifactAlone: the artifact is what gets published, so whoever holds
// it must be able to build the index that goes with it, without the mirror it came from. The
// documents must keep their shelf and sequence (or every filter and citation in the index is
// empty), and the index must name the artifact it was cut from.
func TestIndexIsBuiltFromTheArtifactAlone(t *testing.T) {
	dir := t.TempDir()
	for into, from := range map[string]string{
		"xml/lta/1942/542.xml": "testdata/lta-1942-542.xml",
		"xml/fob/1989/104.xml": "testdata/fob-1989-104.xml",
	} {
		raw, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, into)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	buildArtifact(t, dir)
	// Without the mirror: the index must not need it.
	if err := os.RemoveAll(filepath.Join(dir, "xml")); err != nil {
		t.Fatal(err)
	}
	if err := runCLI(t, newRootCmd(), "--data-dir", dir, "index", "build"); err != nil {
		t.Fatalf("index build from the artifact: %v", err)
	}

	artifact, err := artifactdir.Current(dir)
	if err != nil {
		t.Fatal(err)
	}
	indexPath, err := artifactdir.IndexFor(artifact)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := search.Load(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	a, err := compiled.OpenArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if idx.Source != search.SourceFor(a.Identity()) {
		t.Errorf("index source = %q, want the artifact's %q", idx.Source, search.SourceFor(a.Identity()))
	}
	want := map[string]string{"lta": "eli/lta/1942/542", "fob": "eli/fob/1989/104"}
	if len(idx.Docs) != len(want) {
		t.Fatalf("index holds %d documents, want %d", len(idx.Docs), len(want))
	}
	for _, d := range idx.Docs {
		if d.ELI != want[d.Kind] {
			t.Errorf("%s: kind %q, ELI %q; want the shelf and path the mirror filed it under", d.Accession, d.Kind, d.ELI)
		}
	}
	if len(idx.Units) == 0 || len(idx.Terms) == 0 {
		t.Errorf("index has %d units and %d terms", len(idx.Units), len(idx.Terms))
	}
}
