package lexdania_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/compiled"
	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/library"
)

// TestTheArtifactAloneConsolidatesAsTheArchiveDoes: a law with two amending acts, read through the
// XML archive and through a library made of nothing but the artifact built from it — its catalogue,
// the documents' models and the acts' changes stored in them — gives the same law on every date,
// with the same receipts. The artifact's library is then served with the XML gone.
func TestTheArtifactAloneConsolidatesAsTheArchiveDoes(t *testing.T) {
	root := t.TempDir()
	for rel, body := range map[string]string{
		"lta/2005/1.xml": lexdania.FixtureLaw,
		"lta/2006/2.xml": lexdania.FixtureAmender1,
		"lta/2007/3.xml": lexdania.FixtureAmender2,
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	arch, err := lexdania.Scan(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}

	// The artifact, as the build writes it.
	artPath := filepath.Join(t.TempDir(), "a.lkz")
	f, err := os.Create(artPath)
	if err != nil {
		t.Fatal(err)
	}
	wr, err := compiled.NewWriter(f, root)
	if err != nil {
		t.Fatal(err)
	}
	var catalogue []lexdania.CatalogueRecord
	for _, e := range arch.ScanOrder() {
		doc, path, ok := arch.Open(e.Accession)
		if !ok {
			t.Fatalf("opening %s", e.Accession)
		}
		m, rec := lexdania.ArtifactRecords(e, doc, path)
		if err := wr.Add(e.Accession, m); err != nil {
			t.Fatal(err)
		}
		catalogue = append(catalogue, rec)
	}
	payload, _ := json.Marshal(catalogue)
	wr.SetCatalogue(payload)
	if err := wr.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	// The archive's answers, read while the XML is still there.
	dates := []time.Time{
		time.Date(2005, 6, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2006, 6, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2009, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	want := map[time.Time]viewSummary{}
	applied := 0
	for _, d := range dates {
		v, err := arch.LawAsOf("A20050000129", d)
		if err != nil {
			t.Fatal(err)
		}
		want[d] = viewSummary{markdown: v.Content.Markdown(), applied: len(v.Applied), chain: pathless(v.Chain)}
		applied += len(v.Applied)
	}
	if applied == 0 {
		t.Fatal("the fixture's chain applied nothing, so the comparison would prove nothing")
	}
	// The XML is gone: what follows can only have come from the artifact.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	cache, err := compiled.OpenCached(artPath, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	lib, err := library.Open(cache)
	if err != nil {
		t.Fatal(err)
	}
	if lib.Len() != 3 {
		t.Fatalf("the artifact's catalogue holds %d documents, want 3", lib.Len())
	}

	for _, d := range dates {
		w := want[d]
		got, err := lib.LawAsOf("A20050000129", d)
		if err != nil {
			t.Fatalf("as of %s: %v", d.Format("2006-01-02"), err)
		}
		if got.Content.Markdown() != w.markdown || len(got.Applied) != w.applied {
			t.Errorf("as of %s: the artifact applied %d changes, the archive %d, or the text differs:\n%s\n---\n%s",
				d.Format("2006-01-02"), len(got.Applied), w.applied, got.Content.Markdown(), w.markdown)
		}
		if !reflect.DeepEqual(pathless(got.Chain), w.chain) {
			t.Errorf("as of %s: chains differ:\n%+v\n%+v", d.Format("2006-01-02"), pathless(got.Chain), w.chain)
		}
	}
}

type viewSummary struct {
	markdown string
	applied  int
	chain    []lexdania.Version
}

func pathless(vs []lexdania.Version) []lexdania.Version {
	out := append([]lexdania.Version(nil), vs...)
	for i := range out {
		out[i].Path = ""
	}
	return out
}
