package search

import (
	"context"
	"testing"
)

// TestSetFiltersTakeAnyOfTheirValues: the reading page's toggles send several types and several
// statuses at once, and a hit passes when its document has any of them. Rank returns every match
// in the order Search would, so a page further down is a slice of the same list.
func TestSetFiltersTakeAnyOfTheirValues(t *testing.T) {
	idx, err := Build(context.Background(),
		[]DocMeta{
			{Accession: "A1", Kind: "lta", Year: 2020, Title: "Lov", Source: "xml", Status: "Valid"},
			{Accession: "A2", Kind: "ft", Year: 2020, Title: "Forslag", Source: "xml", Status: "Historic"},
			{Accession: "A3", Kind: "mt", Year: 2020, Title: "Vejledning", Source: "xml", Status: ""},
		},
		[]Unit{
			{Doc: 0, Addr: "§ 1", Text: "pligt"},
			{Doc: 1, Addr: "§ 1", Text: "pligt"},
			{Doc: 2, Addr: "§ 1", Text: "pligt"},
			{Doc: 2, Addr: "§ 2", Text: "pligt og pligt"},
		},
		Options{})
	if err != nil {
		t.Fatal(err)
	}
	docs := func(q Query) map[string]int {
		q.Text = "pligt"
		out := map[string]int{}
		for _, h := range idx.Rank(q) {
			out[h.Doc.Accession]++
		}
		return out
	}
	if got := docs(Query{Kinds: []string{"lta", "mt"}}); len(got) != 2 || got["A1"] != 1 || got["A3"] != 2 {
		t.Errorf("kinds lta+mt = %v, want A1 once and A3 twice", got)
	}
	if got := docs(Query{Statuses: []string{"gældende", "unknown"}}); len(got) != 2 || got["A2"] != 0 {
		t.Errorf("statuses valid+unknown = %v, want A1 and A3", got)
	}
	if got := docs(Query{Accession: "A3"}); len(got) != 1 || got["A3"] != 2 {
		t.Errorf("accession A3 = %v, want only A3's two units", got)
	}
	all := idx.Rank(Query{Text: "pligt"})
	if len(all) != 4 {
		t.Fatalf("Rank returned %d hits, want all 4", len(all))
	}
	top := idx.Search(Query{Text: "pligt", Limit: 2})
	for i := range top {
		if top[i].Unit != all[i].Unit {
			t.Errorf("Search's hit %d is unit %d, Rank's is %d: a page must be a slice of one order", i, top[i].Unit, all[i].Unit)
		}
	}
}
