package lexdania

import (
	"strings"
	"testing"
	"time"
)

// fixtureIkraftLaw is a consolidated act that carries an entry-into-force appendix,
// which is how the archive actually stores them: the body holds the law's own §§ 2-3,
// and <Ikraft> afterwards quotes the entry-into-force paragraph of every amending act,
// each restarting at § 1. So this document contains two provisions printed as "§ 2"
// that are not the same provision — a collision the full-corpus scan found 25,680 times.
const fixtureIkraftLaw = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <DocumentType>BEK H#LOKDOK03</DocumentType>
    <Rank>B</Rank>
    <AccessionNumber>B202600029307</AccessionNumber>
    <DocumentId>CM000777</DocumentId>
    <UniqueDocumentId>77777</UniqueDocumentId>
    <DocumentTitle>Bekendtgørelse om havneafgifter</DocumentTitle>
    <Year>2026</Year>
    <DiesSigni>2026-04-10</DiesSigni>
    <Status>Current</Status>
    <Number>293</Number>
    <Ministry>Transportministeriet</Ministry>
    <Change id="change_1" />
    <Ref_Accn REFid="change_1">B202600038907</Ref_Accn>
    <Ref_Af REFid="change_1">2026-06-01</Ref_Af>
    <Ref_Text REFid="change_1">Bekendtgørelse om ændring af bekendtgørelse om havneafgifter</Ref_Text>
  </Meta>
  <DokumentIndhold>
    <ParagrafGruppe>
      <Paragraf localId="2">
        <Explicatus>§ 2.</Explicatus>
        <Stk><Exitus><Linea><Char>Afgten betales af skibets fører.</Char></Linea></Exitus></Stk>
      </Paragraf>
      <Paragraf localId="3">
        <Explicatus>§ 3.</Explicatus>
        <Stk><Exitus><Linea><Char>Afgten fastsættes årligt af havnechefen.</Char></Linea></Exitus></Stk>
      </Paragraf>
    </ParagrafGruppe>
    <Ikraft>
      <IkraftCentreretParagraf>
        <Explicatus>§ 1</Explicatus>
        <Exitus><Linea><Char>Stk. 1. Loven træder i kraft den 1. januar 2020.</Char></Linea></Exitus>
      </IkraftCentreretParagraf>
      <IkraftCentreretParagraf>
        <Explicatus>§ 2</Explicatus>
        <Exitus><Linea><Char>Stk. 1. Tidspunktet for ikrafttræden fastsættes af transportministeren.</Char></Linea></Exitus>
      </IkraftCentreretParagraf>
    </Ikraft>
  </DokumentIndhold>
</Dokument>`

// ikraftAmender repeals the law's §§ 2-3 as a range and then asks for "§ 1" to be
// repealed. The law has no § 1 — only the appendix does — so the second change must be
// refused rather than silently delete the historical entry-into-force quotation.
const ikraftAmender = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <DocumentType>BEK Æ#LOKDOK05</DocumentType>
    <Rank>B</Rank>
    <AccessionNumber>B202600038907</AccessionNumber>
    <DocumentId>CM000778</DocumentId>
    <UniqueDocumentId>77778</UniqueDocumentId>
    <DocumentTitle>Bekendtgørelse om ændring af bekendtgørelse om havneafgifter</DocumentTitle>
    <Year>2026</Year>
    <DiesSigni>2026-05-20</DiesSigni>
    <Status>Current</Status>
    <Number>389</Number>
    <Ministry>Transportministeriet</Ministry>
  </Meta>
  <DokumentIndhold>
    <AendringCentreretParagraf localId="1">
      <Explicatus>§ 1</Explicatus>
      <Exitus><Linea><Char>I bekendtgørelse nr. 293 af 10. april 2026 om havneafgifter foretages følgende ændringer:</Char></Linea></Exitus>
      <AendringsNummer>
        <Explicatus>1.</Explicatus>
        <Aendring>
          <AendringDefinition><Exitus><Linea><Char>§§ 2-3 ophæves.</Char></Linea></Exitus></AendringDefinition>
          <AendringAktion><AendringNyTekst /></AendringAktion>
        </Aendring>
      </AendringsNummer>
      <AendringsNummer>
        <Explicatus>2.</Explicatus>
        <Aendring>
          <AendringDefinition><Exitus><Linea><Char>§ 1 ophæves.</Char></Linea></Exitus></AendringDefinition>
          <AendringAktion><AendringNyTekst /></AendringAktion>
        </Aendring>
      </AendringsNummer>
    </AendringCentreretParagraf>
  </DokumentIndhold>
</Dokument>`

// TestAddressResolutionIgnoresIkraftAppendix guards the collision between the law's own
// §§ and the entry-into-force appendix that renumbers from § 1. Without the Appendix
// flag the range repeal also deletes the appendix's § 2 and "§ 1 ophæves" deletes a
// historical entry-into-force quotation, so the consolidated act silently loses text it
// was never addressed about.
func TestAddressResolutionIgnoresIkraftAppendix(t *testing.T) {
	base := mustParse(t, fixtureIkraftLaw)
	var appendix, body int
	for _, s := range base.Sections() {
		if s.Kind != "paragraf" {
			continue
		}
		if s.Appendix {
			appendix++
		} else {
			body++
		}
	}
	if body != 2 || appendix != 2 {
		t.Fatalf("body/appendix provisions = %d/%d, want 2/2", body, appendix)
	}

	a := writeArchive(t, map[string]string{
		"lta/2026/293.xml": fixtureIkraftLaw,
		"lta/2026/389.xml": ikraftAmender,
	})
	v, err := a.LawAsOf("B202600029307", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Applied) != 2 {
		t.Fatalf("applied = %d, want the two body provisions of the range: %+v", len(v.Applied), v.Applied)
	}
	if len(v.Unresolved) != 1 {
		t.Fatalf("unresolved = %d, want the § 1 refusal: %+v", len(v.Unresolved), v.Unresolved)
	}
	if !strings.Contains(v.Unresolved[0].Reason, "not found") {
		t.Errorf("the § 1 refusal should say the address is absent, got %q", v.Unresolved[0].Reason)
	}

	text := v.Doc.Root.PlainText()
	for _, want := range []string{
		"Loven træder i kraft den 1. januar 2020",
		"Tidspunktet for ikrafttræden fastsættes af transportministeren",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("appendix text %q was edited even though the law has no § 1", want)
		}
	}
	for _, gone := range []string{"Afgten betales af skibets fører", "Afgten fastsættes årligt af havnechefen"} {
		if strings.Contains(text, gone) {
			t.Errorf("%q should have been repealed", gone)
		}
	}
	if err := v.Doc.Root.checkInvariant(""); err != nil {
		t.Errorf("derived tree broken: %v", err)
	}
}

// TestInsertionAnchorsOutsideAppendix covers positional anchoring: the new § 3 is
// inserted after § 2, which the same act has just repealed, so the anchor is found by
// number alone. The appendix's § 2 is numerically closer in the section list than the
// body's § 2 was, so without the guard the insertion lands inside <Ikraft>.
const ikraftInsertAmender = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <DocumentType>BEK Æ#LOKDOK05</DocumentType>
    <Rank>B</Rank>
    <AccessionNumber>B202600038907</AccessionNumber>
    <DocumentId>CM000779</DocumentId>
    <UniqueDocumentId>77779</UniqueDocumentId>
    <DocumentTitle>Bekendtgørelse om ændring af bekendtgørelse om havneafgifter</DocumentTitle>
    <Year>2026</Year>
    <DiesSigni>2026-05-21</DiesSigni>
    <Status>Current</Status>
    <Number>390</Number>
    <Ministry>Transportministeriet</Ministry>
  </Meta>
  <DokumentIndhold>
    <AendringCentreretParagraf localId="1">
      <Explicatus>§ 1</Explicatus>
      <Exitus><Linea><Char>I bekendtgørelse nr. 293 af 10. april 2026 om havneafgifter foretages følgende ændringer:</Char></Linea></Exitus>
      <AendringsNummer>
        <Explicatus>1.</Explicatus>
        <Aendring>
          <AendringDefinition><Exitus><Linea><Char>§ 3 ophæves.</Char></Linea></Exitus></AendringDefinition>
          <AendringAktion><AendringNyTekst /></AendringAktion>
        </Aendring>
      </AendringsNummer>
      <AendringsNummer>
        <Explicatus>2.</Explicatus>
        <Aendring>
          <AendringDefinition><Exitus><Linea><Char>Efter § 3 indsættes en ny § 3 a, der affattes således:</Char></Linea></Exitus></AendringDefinition>
          <AendringAktion>
            <AendringNyTekst>
              <Paragraf localId="9">
                <Explicatus>§ 3 a.</Explicatus>
                <Stk><Exitus><Linea><Char>Afgten kan udskydes for skibe i fast rutefart.</Char></Linea></Exitus></Stk>
              </Paragraf>
            </AendringNyTekst>
          </AendringAktion>
        </Aendring>
      </AendringsNummer>
    </AendringCentreretParagraf>
  </DokumentIndhold>
</Dokument>`

// TestInsertionAnchorsOutsideAppendix covers positional anchoring: the act repeals § 3
// and inserts a new § 3 a "after § 3", so the anchor has to be found by number alone.
// The appendix is later in document order than the body and also holds a § 2, so
// "the last provision numbered below 3" is the appendix's § 2 without the guard, and the
// new § 3 a becomes part of a historical entry-into-force quotation.
//
// It also guards donorClone: the inserted § arrives with localId="9" from the amender's
// own numbering, and if that survives the splice the provision is numbered 9 instead of
// 3 a, so no later act citing § 3 a can find it.
func TestInsertionAnchorsOutsideAppendix(t *testing.T) {
	a := writeArchive(t, map[string]string{
		"lta/2026/293.xml": fixtureIkraftLaw,
		"lta/2026/390.xml": ikraftInsertAmender,
	})
	v, err := a.LawAsOf("B202600029307", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Unresolved) != 0 {
		t.Fatalf("both changes should apply: %+v", v.Unresolved)
	}
	if len(v.Applied) != 2 {
		t.Fatalf("applied = %d, want 2", len(v.Applied))
	}
	newSec := v.Section("§ 3a")
	if newSec == nil {
		t.Fatal("the new § 3 a is missing: the inserted provision kept the amender's localId")
	}
	if newSec.Appendix || containerName(newSec.Container) == "Ikraft" {
		t.Errorf("the new § 3 a was inserted into the entry-into-force appendix (appendix=%v, container=%s)",
			newSec.Appendix, containerName(newSec.Container))
	}
	if !strings.Contains(newSec.Display(), "fast rutefart") {
		t.Errorf("the new § 3 a has the wrong content: %q", newSec.Display())
	}
	text := v.Doc.Root.PlainText()
	if !strings.Contains(text, "Tidspunktet for ikrafttræden fastsættes af transportministeren") {
		t.Error("the appendix § 2 the anchor was *not* meant to find has gone")
	}
	if err := v.Doc.Root.checkInvariant(""); err != nil {
		t.Errorf("derived tree broken: %v", err)
	}
}

// containerName is the element name of a section's container, for assertions about
// where an edit physically landed.
func containerName(n *Node) string {
	if n == nil {
		return ""
	}
	return n.Name
}
