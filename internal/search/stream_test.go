package search

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

// TestStreamingBuildIsTheSameIndex: the streaming build exists to hold less, not to index
// differently. The same documents, given one at a time, must come out as the same tables the
// in-memory build makes from all their units at once — documents, units, terms and postings —
// and survive the file, source included.
func TestStreamingBuildIsTheSameIndex(t *testing.T) {
	docs := []DocMeta{
		{Accession: "A1", Kind: "lta", Title: "Lov om jagt"},
		{Accession: "A2", Kind: "ft", Title: "Forslag til lov om jagt"},
		{Accession: "A3", Kind: "mt", Title: "Vejledning"},
	}
	perDoc := [][]Unit{
		{{Addr: "titel", Text: "Lov om jagt"}, {Addr: "§ 1", Text: "Jagttegn kræves. Jagt jagt jagt."}},
		{{Addr: "titel", Text: "Forslag til lov om jagt"}},
		{{Addr: "§ 1", Text: "Om jagttegn og prøven"}, {Addr: "§ 2", Text: "Aalborg og Århus"}},
	}
	// More documents than one batch holds, so the batching itself is exercised.
	for i := 0; i < streamBatch+10; i++ {
		docs = append(docs, DocMeta{Accession: fmt.Sprintf("F%d", i), Kind: "lta"})
		perDoc = append(perDoc, []Unit{{Addr: "§ 1", Text: fmt.Sprintf("fyld nummer %d jagt", i)}})
	}
	var flat []Unit
	for d, us := range perDoc {
		for _, u := range us {
			u.Doc = d
			flat = append(flat, u)
		}
	}
	want, err := Build(context.Background(), docs, flat, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := BuildStream(context.Background(), len(docs), func(i int) (DocMeta, []Unit, bool) {
		return docs[i], perDoc[i], true
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Docs, want.Docs) || !reflect.DeepEqual(got.Units, want.Units) {
		t.Fatal("documents or units differ between the two builds")
	}
	if !reflect.DeepEqual(got.Terms, want.Terms) || !reflect.DeepEqual(got.Postings, want.Postings) || got.avgUnitLen != want.avgUnitLen {
		t.Fatal("dictionary or postings differ between the two builds")
	}

	got.Source = "artifact 2026-10-06T00:00:00Z"
	path := filepath.Join(t.TempDir(), "x.idx")
	if err := got.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Source != got.Source || !reflect.DeepEqual(back.Postings, got.Postings) {
		t.Errorf("the file lost the source or the postings: source %q", back.Source)
	}
}
