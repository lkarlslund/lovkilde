package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/lkarlslund/lovkilde/internal/search"
)

// TestASearchSaysWhenItsIndexIsFromAnotherArtifact: the index is cut from the artifact, and a
// pair from different builds disagrees about which provisions exist. The answer still comes,
// and says so; a matching pair says nothing.
func TestASearchSaysWhenItsIndexIsFromAnotherArtifact(t *testing.T) {
	s := newTestServer(t)
	if err := s.UseArtifact(writeFixtureArtifact(t, testArchive(s)), 1<<20); err != nil {
		t.Fatal(err)
	}
	idx := mustIndex(t)
	s.UseIndex(idx)

	warned := func() bool {
		res, err := s.Search(SearchRequest{Query: "kanonisk"})
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range res.Warnings {
			if strings.Contains(w, "different compiled artifact") {
				return true
			}
		}
		return false
	}
	idx.Source = "artifact 2001-01-01T00:00:00Z"
	if !warned() {
		t.Error("an index from another artifact was not mentioned")
	}
	idx.Source = search.SourceFor(s.cache().Artifact().Identity())
	if warned() {
		t.Error("a matching pair was warned about")
	}
}

// TestAHitWithoutAnELIPathIsReferencedByAccession: a document the register numbered 0 has no
// ELI path, and a hit on it must still be something lookup can open.
func TestAHitWithoutAnELIPathIsReferencedByAccession(t *testing.T) {
	s := newTestServer(t)
	idx, err := search.Build(context.Background(),
		[]search.DocMeta{{Accession: accCanon, Kind: "ft", Title: "Uden nummer", Source: "xml"}},
		[]search.Unit{{Doc: 0, Addr: "", Text: "nummerløs tekst"}}, search.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.UseIndex(idx)
	res, err := s.Search(SearchRequest{Query: "nummerløs"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Reference != accCanon {
		t.Fatalf("hits = %+v, want one referenced by its accession", res.Hits)
	}
}
