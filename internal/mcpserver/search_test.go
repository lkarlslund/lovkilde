package mcpserver

import (
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
	idx, err := search.Build(
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
	joined := strings.Join(res.Warnings, " ")
	// The mirror is only as current as its last crawl, and the feed harvest has never
	// run here, so every search answer has to say what the index does not cover.
	if !strings.Contains(joined, "1 documents") || !strings.Contains(joined, "does not include anything published since") {
		t.Errorf("warnings do not bound the claim: %q", joined)
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
