package search

import (
	"context"
	"fmt"
	"testing"
)

// rankFixture is a small register: one law about a subject, the paperwork around it that
// repeats its words, an order named for a narrower subject, and filler that makes "af" common.
func rankFixture(t *testing.T) *Index {
	t.Helper()
	docs := []DocMeta{
		{Accession: "LAW", Kind: "lta", Title: "Bekendtgørelse af lov om jagt", TypeCode: "LBK H#LOKDOK03", Status: "Valid"},
		{Accession: "AMEND", Kind: "lta", Title: "Lov om ændring af lov om jagt", TypeCode: "LOV H#LOKDOK01", Status: "Valid"},
		{Accession: "BILL", Kind: "ft", Title: "Forslag til lov om jagt", Status: "Valid"},
		{Accession: "FO", Kind: "lta", Title: "Anordning om ikrafttræden for Færøerne af lov om jagt", TypeCode: "ANG I#LOKDOK17", Status: "Valid"},
		{Accession: "ORDER", Kind: "lta", Title: "Bekendtgørelse om jagttegn", TypeCode: "BEK H#LOKDOK04", Status: "Valid"},
		{Accession: "FILL", Kind: "lta", Title: "Fyld", TypeCode: "BEK H#LOKDOK04", Status: "Valid"},
	}
	var units []Unit
	for i := 1; i <= 6; i++ {
		units = append(units, Unit{Doc: 0, Addr: fmt.Sprintf("§ %d", i), Text: "jagttegn kræves af den som driver jagt"})
	}
	units = append(units,
		Unit{Doc: 1, Addr: "§ 1", Text: "jagttegn jagttegn jagttegn af"},
		Unit{Doc: 2, Addr: "", Text: "jagttegn jagttegn jagttegn af"},
		Unit{Doc: 3, Addr: "§ 1", Text: "jagttegn jagttegn jagttegn af"},
		Unit{Doc: 4, Addr: "§ 1", Text: "prøve til jagttegn af"},
	)
	// Enough filler that "jagttegn" is rare and "af" is common, as on the real mirror.
	for i := 0; i < 1000; i++ {
		text := "noget andet"
		if i < 200 {
			text = "noget af det hele"
		}
		units = append(units, Unit{Doc: 5, Addr: fmt.Sprintf("§ %d", i+1), Text: text})
	}
	idx, err := Build(context.Background(), docs, units, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

// TestTheLawOutranksItsPaperwork: an amending act, a bill and a Faroese commencement order
// repeat the law's words more densely than the law does, and without document priors each of
// them came before the law itself — on the real mirror, for 48 of 49 everyday questions.
func TestTheLawOutranksItsPaperwork(t *testing.T) {
	idx := rankFixture(t)
	hits := idx.Search(Query{Text: "jagttegn", Limit: 3})
	if len(hits) == 0 || hits[0].Doc.Accession != "LAW" {
		t.Fatalf("first hit = %v, want the law", accessions(hits))
	}
}

// TestNamingADocumentFindsIt: "bekendtgørelse om jagttegn" is a document's name, and the
// order with that title wins over the law that mentions jagttegn in every section.
func TestNamingADocumentFindsIt(t *testing.T) {
	idx := rankFixture(t)
	hits := idx.Search(Query{Text: "bekendtgørelse om jagttegn", Limit: 3})
	if len(hits) == 0 || hits[0].Doc.Accession != "ORDER" {
		t.Fatalf("first hit = %v, want the order named in the query", accessions(hits))
	}
}

// TestACommonWordOnlyAddsToWhatRarerWordsFound: "af" is in nearly every unit here, and a
// question that also asks for "jagttegn" is answered by the units that have jagttegn — not
// by two hundred units that merely have "af".
func TestACommonWordOnlyAddsToWhatRarerWordsFound(t *testing.T) {
	idx := rankFixture(t)
	all := idx.Rank(Query{Text: "jagttegn af"})
	for _, h := range all {
		if h.Doc.Accession == "FILL" {
			t.Fatalf("a unit with only the common word was ranked: %v", accessions(all))
		}
	}
	if only := idx.Rank(Query{Text: "af"}); len(only) < 200 {
		t.Errorf("a question of only common words found %d units, want all of them", len(only))
	}
}

func accessions(hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.Doc.Accession+" "+h.Addr)
	}
	return out
}
