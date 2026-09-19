package mcpserver

import (
	"testing"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

// TestStatusFilterNamesTheBucketsTheUIOffers: the page offers "Valid", "Historic" and "unknown"
// and Danish spellings typed by hand, so the words have to mean the same thing whichever way they
// arrive. An unrecognised word is "no opinion", which is the safe direction: a filter nobody
// understood should widen a result, not narrow it to nothing.
func TestStatusFilterNamesTheBucketsTheUIOffers(t *testing.T) {
	cases := []struct {
		want     string
		valid    bool
		historic bool
		blank    bool
	}{
		{want: "valid", valid: true},
		{want: "Valid", valid: true},
		{want: "gældende", valid: true},
		{want: "historisk", historic: true},
		{want: "ophævet", historic: true},
		{want: "unknown", blank: true},
		{want: "ukendt", blank: true},
		{want: "kjendte-begivenheder"}, // not a status: no opinion, everything passes
	}
	for _, c := range cases {
		pred := statusFilter(c.want)
		if c.want == "" || c.want == "kjendte-begivenheder" {
			if pred != nil {
				t.Errorf("statusFilter(%q) filtered when it should not have an opinion", c.want)
			}
			continue
		}
		if pred(lexdania.StatusValid) != c.valid ||
			pred(lexdania.StatusHistoric) != c.historic ||
			pred(lexdania.Status("")) != c.blank {
			t.Errorf("statusFilter(%q) picked the wrong bucket", c.want)
		}
	}
}

// TestStatusFacetsOrderKeepsTheUngroundedBucketLast: the count of documents the register says
// nothing about is the number that needs explaining, so it is not the first one a reader compares.
func TestStatusFacetsOrderKeepsTheUnknownBucketLast(t *testing.T) {
	got := statusFacets(map[string]int{"unknown": 90, "Historic": 5, "Valid": 8})
	if len(got) != 3 || got[0].Name != "Valid" || got[2].Name != "unknown" {
		t.Fatalf("facets came back as %v", got)
	}
	if got[2].Count != 90 {
		t.Errorf("unknown bucket lost its count: %v", got[2])
	}
}

func TestStatusFacetNameNeverEmitsAnEmptyName(t *testing.T) {
	// An empty facet name would collide with the "no filter" option in a <select> and make the
	// unlabelled documents impossible to ask for.
	if name := statusFacetName(lexdania.Status("")); name != "unknown" {
		t.Errorf("empty status became %q, want the named bucket", name)
	}
}
