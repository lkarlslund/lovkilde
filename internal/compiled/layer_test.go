package compiled

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A layer holds the documents that changed and reads the rest from the whole artifact beside it,
// in the order of the build that wrote it; a reader sees one artifact. What it must refuse is a
// base that is not the one it was written over.
func TestALayerReadsUnchangedDocumentsFromItsBase(t *testing.T) {
	dir := t.TempDir()
	changed := fixtureDocument()
	changed.Identity.Title = "Ændret titel"
	basePath := filepath.Join(dir, "base.retsin")
	if err := writeArtifactFile(t, basePath, "/mirror", []ArtifactDoc{
		{Accession: "A1", Document: fixtureDocument()},
		{Accession: "A2", Document: fixtureDocument()},
	}); err != nil {
		t.Fatal(err)
	}
	base, err := OpenArtifact(basePath)
	if err != nil {
		t.Fatal(err)
	}
	stamp := base.Header.Stamp
	carried, _ := base.Ref("A1")
	base.Close()

	write := func(name string, ref BaseRef) string {
		t.Helper()
		path := filepath.Join(dir, name)
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		wr, err := NewWriterAfter(f, "/mirror", Provenance{Base: &ref})
		if err != nil {
			t.Fatal(err)
		}
		if err := wr.WriteBaseRef("A1", carried.Raw); err != nil {
			t.Fatal(err)
		}
		for _, d := range []ArtifactDoc{{"A2", changed}, {"A3", changed}} {
			if err := wr.Add(d.Accession, d.Document); err != nil {
				t.Fatal(err)
			}
		}
		wr.SetCatalogue([]byte("[]"))
		if err := wr.Close(); err != nil {
			t.Fatal(err)
		}
		return path
	}

	layer, err := OpenArtifact(write("layer.retsin", BaseRef{File: "base.retsin", Stamp: stamp}))
	if err != nil {
		t.Fatal(err)
	}
	defer layer.Close()
	if got := layer.Documents(); !slices.Equal(got, []string{"A1", "A2", "A3"}) {
		t.Errorf("the layer lists %v, want A1 A2 A3 in the order it was written", got)
	}
	for acc, title := range map[string]string{"A1": fixtureDocument().Identity.Title, "A2": "Ændret titel", "A3": "Ændret titel"} {
		doc, err := layer.Document(acc)
		if err != nil {
			t.Fatalf("%s: %v", acc, err)
		}
		if doc.Identity.Title != title {
			t.Errorf("%s reads as %q, want %q", acc, doc.Identity.Title, title)
		}
	}
	if fr, inBase, err := layer.Frame("A1"); err != nil || !inBase || fr.Raw != int(carried.Raw) {
		t.Errorf("A1's frame: in base %v, raw %d, %v; want it from the base, raw %d", inBase, fr.Raw, err, carried.Raw)
	}
	if _, inBase, err := layer.Frame("A2"); err != nil || inBase {
		t.Errorf("A2's frame is in the base (%v, %v); the layer replaced it", inBase, err)
	}

	for name, ref := range map[string]BaseRef{
		"another build": {File: "base.retsin", Stamp: "0000"},
		"missing base":  {File: "gone.retsin", Stamp: stamp},
		"not beside it": {File: "../base.retsin", Stamp: stamp},
	} {
		path := write(strings.ReplaceAll(name, " ", "-")+".retsin", ref)
		if a, err := OpenArtifact(path); err == nil {
			a.Close()
			t.Errorf("%s: a layer over the wrong base opened", name)
		}
	}
}
