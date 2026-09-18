package lexdania

import (
	"strings"
	"testing"
	"time"
)

// Retsinformation's text carries typesetting marks that are not part of the law: a hunt
// across the whole archive found U+00AD soft hyphen in 7,564 documents, U+200D in 3,448,
// U+200C in 2,953, U+200B in 145 and U+FEFF in 30 — for example "Ting­lysningsrettens"
// and "H‌øjesteret" in the acts on procedural and financial law. An amending act quoting
// such a provision does not reproduce the mark, so the comparison form has to drop them:
// soft hyphens already disappear there, and the zero-width run has to follow, or the
// edit is rejected as "not found" and the change silently never happens.
//
// The characters are written as XML entities so the fixtures stay reviewable; the parser
// decodes them into the same bytes the archive carries.
const fixtureZeroWidthLaw = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <DocumentType>BEK H#LOKDOK04</DocumentType>
    <Rank>B</Rank>
    <AccessionNumber>B202600041111</AccessionNumber>
    <DocumentId>CM000921</DocumentId>
    <UniqueDocumentId>92111</UniqueDocumentId>
    <DocumentTitle>Bekendtgørelse om retspleje</DocumentTitle>
    <Year>2026</Year>
    <DiesSigni>2026-02-02</DiesSigni>
    <Status>Current</Status>
    <Number>411</Number>
    <Ministry>Justitsministeriet</Ministry>
    <Change id="change_1" />
    <Ref_Accn REFid="change_1">B202600045511</Ref_Accn>
    <Ref_Af REFid="change_1">2026-09-01</Ref_Af>
    <Ref_Text REFid="change_1">Bekendtgørelse om ændring af bekendtgørelse om retspleje</Ref_Text>
  </Meta>
  <DokumentIndhold>
    <Paragraf>
      <Explicatus>§ 1.</Explicatus>
      <Stk><Exitus><Linea><Char>Sagen indbringes for H&#x200C;øjesteret.</Char></Linea></Exitus></Stk>
    </Paragraf>
    <Paragraf>
      <Explicatus>§ 2.</Explicatus>
      <Stk><Exitus><Linea><Char>Afgørelsen kan pådømmes af Ting&#xad;lysningsretten.</Char></Linea></Exitus></Stk>
    </Paragraf>
  </DokumentIndhold>
</Dokument>`

// fixtureZeroWidthAmender quotes § 1, stk. 1 exactly as a drafter writes it: without the
// zero-width mark the archived text happens to carry.
const fixtureZeroWidthAmender = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <DocumentType>BEK Æ#LOKDOK05</DocumentType>
    <Rank>B</Rank>
    <AccessionNumber>B202600045511</AccessionNumber>
    <DocumentId>CM000922</DocumentId>
    <UniqueDocumentId>92112</UniqueDocumentId>
    <DocumentTitle>Bekendtgørelse om ændring af bekendtgørelse om retspleje</DocumentTitle>
    <Year>2026</Year>
    <DiesSigni>2026-06-10</DiesSigni>
    <Status>Current</Status>
    <Number>455</Number>
    <Ministry>Justitsministeriet</Ministry>
  </Meta>
  <DokumentIndhold>
    <AendringCentreretParagraf localId="1">
      <Explicatus>§ 1</Explicatus>
      <Exitus><Linea><Char>I bekendtgørelse nr. 411 af 2. februar 2026 om retspleje foretages følgende ændring:</Char></Linea></Exitus>
      <AendringsNummer>
        <Explicatus>1.</Explicatus>
        <Aendring>
          <AendringDefinition>
            <Exitus><Linea><Char>I § 1, stk. 1, ændres »for Højesteret« til: »for Landsretten som første instans«.</Char></Linea></Exitus>
          </AendringDefinition>
          <AendringAktion><AendringNyTekst /></AendringAktion>
        </Aendring>
      </AendringsNummer>
    </AendringCentreretParagraf>
  </DokumentIndhold>
</Dokument>`

func TestMatchTextDropsTypesettingMarks(t *testing.T) {
	// Concatenated rather than one escape sequence per case: \u takes exactly four hex
	// digits, and writing "H\u200cejesteret" silently loses the ø from the word.
	for _, tc := range []struct{ marked, plain string }{
		{"H" + "\u200c" + "øjesteret", "Højesteret"},
		{"H" + "\u200d" + "øjesteret", "Højesteret"},
		{"H" + "\u200b" + "øjesteret", "Højesteret"},
		{"H" + "\ufeff" + "øjesteret", "Højesteret"},
		{"Ting" + "\u00ad" + "lysningsretten", "Tinglysningsretten"},
	} {
		if got, want := MatchText(tc.marked), MatchText(tc.plain); got != want {
			t.Errorf("MatchText(%q) = %q, want %q", tc.marked, got, want)
		}
	}
}

func TestZeroWidthProvisionIsStillAmendable(t *testing.T) {
	a := writeArchive(t, map[string]string{
		"lta/2026/411.xml": fixtureZeroWidthLaw,
		"lta/2026/455.xml": fixtureZeroWidthAmender,
	})
	v, err := a.LawAsOf("B202600041111", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Unresolved) != 0 {
		t.Fatalf("a provision carrying a zero-width mark is still the provision an amendment quotes: %+v",
			v.Unresolved)
	}
	text := v.Doc.Root.PlainText()
	if !strings.Contains(text, "for Landsretten som første instans") {
		t.Errorf("the substitution did not land, got:\n%s", text)
	}
	if !strings.Contains(text, "Ting\u00adlysningsretten") {
		t.Error("the soft hyphen in § 2 is part of the archived text and must survive rendering")
	}
}
