package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/search"
)

// An ELI path is not a unique identifier, and this is not hypothetical: the first real
// query over the full mirror returned the same eli/ft/2020/54 twice, because a bill is
// stored once per stage (202012L00054, 202013L00054, 20201BB00054) and they all derive
// that path. Those stages do not have the same text, so whichever one a map collision
// happened to keep would have been presented as "the document".

func twoStagesOfOneBill(t *testing.T) *Server {
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Same year and number as the path, different accessions and different text: the
	// shape the mirror really writes for a folketingssag.
	write("ft/2020/202012L00054.xml", "<Dokument>"+meta("202012L00054", "Forslag til lov", "Forslag til Lov om prøvesager", 2020, 54)+
		"<DokumentIndhold><Paragraf><Explicatus>§ 1.</Explicatus><Stk><Explicatus>Stk. 1.</Explicatus>FØRSTE TRIN</Stk></Paragraf></DokumentIndhold></Dokument>")
	write("ft/2020/202013L00054.xml", "<Dokument>"+meta("202013L00054", "Lovforslag", "Forslag til Lov om prøvesager", 2020, 54)+
		"<DokumentIndhold><Paragraf><Explicatus>§ 1.</Explicatus><Stk><Explicatus>Stk. 1.</Explicatus>ANDET TRIN</Stk></Paragraf></DokumentIndhold></Dokument>")

	a, err := lexdania.Scan(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if a.Len() != 2 {
		t.Fatalf("indexed %d documents, want 2", a.Len())
	}
	return New(a, "https://www.retsinformation.dk/", "test")
}

func TestLookupRefusesAnAmbiguousELIPath(t *testing.T) {
	s := twoStagesOfOneBill(t)

	_, err := s.Lookup(LookupRequest{Document: "eli/ft/2020/54"})
	if err == nil {
		t.Fatal("lookup answered an ambiguous ELI path; it must name its candidates instead of picking one")
	}
	for _, want := range []string{"202012L00054", "202013L00054", "2 documents"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ambiguity error does not mention %q: %v", want, err)
		}
	}

	// The point of listing accessions is that the caller can use them.
	res, err := s.Lookup(LookupRequest{Document: "202013L00054"})
	if err != nil {
		t.Fatalf("lookup by accession after an ambiguity error: %v", err)
	}
	if !strings.Contains(res.Text, "ANDET TRIN") {
		t.Errorf("accession resolved to the wrong stage: %q", res.Text)
	}
}

func TestSearchPointsAtTheUniqueReference(t *testing.T) {
	s := twoStagesOfOneBill(t)

	docs := []search.DocMeta{
		{Accession: "202012L00054", ELI: "eli/ft/2020/54", Kind: "ft", Year: 2020, Number: 54, Title: "Forslag til Lov om prøvesager", Source: "xml"},
		{Accession: "202013L00054", ELI: "eli/ft/2020/54", Kind: "ft", Year: 2020, Number: 54, Title: "Forslag til Lov om prøvesager", Source: "xml"},
	}
	units := []search.Unit{
		{Doc: 0, Addr: "§ 1", Text: "FØRSTE TRIN prøvesager"},
		{Doc: 1, Addr: "§ 1", Text: "ANDET TRIN prøvesager"},
	}
	idx, err := search.Build(context.Background(), docs, units, search.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.UseIndex(idx)

	res, err := s.Search(SearchRequest{Query: "prøvesager", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("hits = %d, want both stages kept: they are different documents", len(res.Hits))
	}
	for _, h := range res.Hits {
		if h.Reference != h.Accession {
			t.Errorf("hit suggests %q, want the accession %q: the ELI path matches two documents",
				h.Reference, h.Accession)
		}
	}
	if strings.Contains(res.String(), `document="eli/ft/2020/54"`) {
		t.Errorf("rendered answer tells the caller to re-ask with an ambiguous path:\n%s", res.String())
	}
}

func TestSearchKeepsTheELIWhenItIsUnique(t *testing.T) {
	s := newTestServer(t)
	docs := []search.DocMeta{
		{Accession: accCanon, ELI: "eli/lta/1998/2", Kind: "lta", Year: 1998, Number: 2, Title: "Testlov", Source: "xml"},
	}
	idx, err := search.Build(context.Background(), docs, []search.Unit{{Doc: 0, Addr: "§ 1", Text: "KANONISK TEKST"}}, search.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.UseIndex(idx)
	res, err := s.Search(SearchRequest{Query: "kanonisk"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("hits = %d", len(res.Hits))
	}
	if res.Hits[0].Reference != "eli/lta/1998/2" {
		t.Errorf("reference = %q, want the readable ELI path when it identifies one document", res.Hits[0].Reference)
	}
}
