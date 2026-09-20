package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/search"
)

// A tool the server cannot honour is worse than no tool: the client only finds out
// mid-conversation, and by then it has already trusted the tool list.

func TestSearchRefusedWithoutAnIndex(t *testing.T) {
	s := newTestServer(t)
	if s.HasSearch() {
		t.Fatal("HasSearch is true with no index loaded")
	}
	_, err := s.Search(SearchRequest{Query: "miljøbeskyttelse"})
	if err == nil {
		t.Fatal("Search answered without an index")
	}
	if !strings.Contains(err.Error(), "index build") {
		t.Errorf("refusal does not say how to fix it: %v", err)
	}

	s.UseIndex(mustIndex(t))
	if !s.HasSearch() {
		t.Error("HasSearch is false after an index was attached")
	}
}

func mustIndex(t *testing.T) *search.Index {
	idx, err := search.Build(context.Background(),
		[]search.DocMeta{{Accession: "A19980002X", ELI: "eli/lta/1998/2", Kind: "lta", Year: 1998, Number: 2, Title: "Testlov", Source: "xml"}},
		[]search.Unit{{Doc: 0, Addr: "§ 1", Text: "KANONISK TEKST"}},
		search.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestSearchResultCarriesTheFreshnessCaveat(t *testing.T) {
	s := newTestServer(t)
	s.UseIndex(mustIndex(t))

	res, err := s.Search(SearchRequest{Query: "kanonisk"})
	if err != nil {
		t.Fatal(err)
	}
	// Every search answer has to say which dates it speaks for and what the index covers, so
	// that a partial result cannot be read as a complete one.
	if !strings.Contains(res.Mirror, "covering 1 documents") || !strings.Contains(res.Mirror, "where the mirror holds 3") {
		t.Errorf("horizon line does not state both coverage counts: %q", res.Mirror)
	}
	joined := strings.Join(res.Warnings, " ")
	if !strings.Contains(joined, "a hit here proves presence") {
		t.Errorf("warnings do not state what the counts mean: %q", joined)
	}
	// Saying it once is the other half of saying it. The horizon line used to be assigned to
	// Mirror *and* appended to Warnings, so the identical sentence reached the reader twice in
	// the answer and twice on the page — once as a fact, once as a problem.
	if got := strings.Count(res.String(), "asked on"); got != 1 {
		t.Errorf("the answer states the horizon line %d times, want 1", got)
	}
	if !strings.Contains(res.String(), "**Note:**") {
		t.Errorf("rendered result drops the caveat:\n%s", res.String())
	}

	empty, err := s.Search(SearchRequest{Query: "qwertyuiop"})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Hits) != 0 {
		t.Fatal("a term not in the index returned hits")
	}
	if !strings.Contains(empty.String(), "no provision in the indexed corpus matches") {
		t.Errorf("empty result does not say what it searched:\n%s", empty.String())
	}
	if !strings.Contains(empty.String(), "No matches") {
		t.Errorf("empty result does not say so:\n%s", empty.String())
	}
}

func TestSearchRejectsAnEmptyQueryAndCapsTheLimit(t *testing.T) {
	s := newTestServer(t)
	s.UseIndex(mustIndex(t))
	if _, err := s.Search(SearchRequest{Query: "   "}); err == nil {
		t.Error("empty query accepted")
	}
	// The cap matters because snippets re-read documents from the mirror: an
	// unbounded limit is an unbounded amount of parsing per query.
	res, err := s.Search(SearchRequest{Query: "kanonisk", Limit: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) > 50 {
		t.Errorf("limit 5000 produced %d hits; the cap is what bounds per-query document reads", len(res.Hits))
	}
}

func TestSearchHitNamesItsProvenance(t *testing.T) {
	s := newTestServer(t)
	s.UseIndex(mustIndex(t))
	res, err := s.Search(SearchRequest{Query: "kanonisk"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("no hits")
	}
	out := res.String()
	if !strings.Contains(out, "text: xml") {
		t.Errorf("rendered hit does not name its text source:\n%s", out)
	}
	if !strings.Contains(out, "A19980002X") {
		t.Errorf("rendered hit does not name the accession:\n%s", out)
	}
}
