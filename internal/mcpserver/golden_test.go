package mcpserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

// A golden answer: one document written the way the register writes one, and the exact text the
// server is required to produce from it. The other tests in this package check that an answer
// identifies itself; this one checks the part a client actually quotes, on a document shaped to
// have the things the model knows and the flat section list did not — a chapter with a rubric, a
// provision with a rubric, a footnote inside a subsection, entry-into-force material whose
// numbering starts again at 1, and a table the model has no concept for.
//
// The golden is the text alone, not the whole answer: the header carries the archive path and the
// date the mirror was asked, and those are checked where they are claimed.

const accGolden = "A19960007X"

func newGoldenServer(t *testing.T) *Server {
	root := t.TempDir()
	doc := "<Dokument>" + meta(accGolden, "Lov", "Testlov med kapitleopbygning", 1996, 7) +
		"<DokumentIndhold>" +
		"<Indledning><Linie>Loven gaelder for testformal.</Linie></Indledning>" +
		"<Kapitel localId=\"1\"><KapitelOverskrift><Rubrica><Linea><Char>Almindelige regler</Char></Linea></Rubrica></KapitelOverskrift>" +
		"<Paragraf localId=\"1\"><Rubrica><Linea><Char>Anvendelsesomraade</Char></Linea></Rubrica><Explicatus>§ 1.</Explicatus>" +
		"<Stk><Explicatus>Stk. 1.</Explicatus>Loven gaelder kun her.</Stk>" +
		// The space before <Nota> is in the source and does not survive: every element below a
		// block element is rendered as a block, whose run is trimmed, so the footnote's marker ends
		// up against the word before it ("findes i1)"). The flat text path glues it the same way,
		// which is why the corpus text gate does not see it either. It is pinned below rather than
		// fixed here, so that whoever changes the block rule sees what an answer costs.
		"<Stk><Explicatus>Stk. 2.</Explicatus>Undtagelserne findes i <Nota id=\"Par1_Stk2_Not1\">1) Fastsat af ministeren.</Nota> bilaget.</Stk>" +
		"</Paragraf>" +
		"<Paragraf localId=\"2\"><Explicatus>§ 2.</Explicatus><Stk>Bemaerkninger fastsaettes af ministeren.</Stk></Paragraf>" +
		"</Kapitel>" +
		"<Ikraft><IkraftCentreretParagraf localId=\"1\"><Explicatus>§ 1.</Explicatus><Stk>Loven traeder i kraft den 1. januar 1997.</Stk></IkraftCentreretParagraf></Ikraft>" +
		"<Table><R><C>en tabelcelle</C></R></Table>" +
		"</DokumentIndhold></Dokument>"
	path := filepath.Join(root, "lta", "1996", "7.xml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := lexdania.Scan(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if a.Len() != 1 {
		t.Fatalf("indexed %d documents, want 1 (skipped %d)", a.Len(), a.Skipped)
	}
	return New(a, "https://www.retsinformation.dk/", "test")
}

func TestGoldenAnswerWholeDocument(t *testing.T) {
	s := newGoldenServer(t)
	res, err := s.Lookup(LookupRequest{Document: accGolden})
	if err != nil {
		t.Fatal(err)
	}
	const want = `Loven gaelder for testformal.

## chapter 1

*Almindelige regler*

### § 1. Anvendelsesomraade

§ 1.

Stk. 1.
Loven gaelder kun her.

Stk. 2.
Undtagelserne findes i1) Fastsat af ministeren.
bilaget.

§ 2.

Bemaerkninger fastsaettes af ministeren.

## Entry into force and transitional provisions

*Numbered in their own series: citing them as a section of the law would reach a different provision.*

§ 1.

Loven traeder i kraft den 1. januar 1997.

## Other content the model has no concept for (2 pieces)

*metadata ×1, table ×1*

> en tabelcelle
`
	if res.Text != want {
		t.Errorf("the whole-document answer changed shape:\n got:\n%s\nwant:\n%s", res.Text, want)
	}
}

func TestGoldenAnswerOneSubsection(t *testing.T) {
	s := newGoldenServer(t)
	res, err := s.Lookup(LookupRequest{Document: accGolden, Address: "§ 1, stk. 2"})
	if err != nil {
		t.Fatal(err)
	}
	// The section's rubric comes along with the subsection: an answer quoting "stk. 2" without
	// saying which section it is in is not a citation.
	const want = `### § 1. Anvendelsesomraade

Stk. 2. Undtagelserne findes i1) Fastsat af ministeren. bilaget.
`
	if res.Text != want {
		t.Errorf("the addressed answer changed shape:\n got:\n%s\nwant:\n%s", res.Text, want)
	}
}
