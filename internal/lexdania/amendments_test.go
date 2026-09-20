package lexdania

import (
	"testing"
)

// The publisher writes a numbered change list in more than one place. These four
// fixtures are the shapes measured across the 20,812 mirror documents that hold an
// <AendringsNummer>; the numbers are in amendmentBlocks' comment. Each shape needs
// its own assertion, because two of them must be claimed and two must not.

const fixtureStkAmender = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <AccessionNumber>A20100064830</AccessionNumber>
    <DocumentTitle>Bekendtgørelse om ændring af lov om erhvervsfremme</DocumentTitle>
  </Meta>
  <DokumentIndhold>
    <Paragraf id="Par14">
      <Explicatus>§ 14.</Explicatus>
      <Stk id="Par14_Stk1">
        <Exitus><Linea><Char>I lov nr. 602 af 24. juni 2005 om erhvervsfremme, som ændret ved § 3 i lov nr. 375 af 27. maj 2008, ophæves:</Char></Linea></Exitus>
        <AendringsNummer id="Par14_Stk1_Anu1">
          <Explicatus>1.</Explicatus>
          <Aendring>
            <AendringDefinition><Exitus><Linea><Char>Efter § 9 indsættes i </Char><Char formaChar="Italic">kapitel 4:</Char></Linea></Exitus></AendringDefinition>
            <AendringAktion><AendringNyTekst><Paragraf><Explicatus>§ 9 a.</Explicatus><Stk><Exitus><Linea><Char>Regioner skal koordinere.</Char></Linea></Exitus></Stk></Paragraf></AendringNyTekst></AendringAktion>
          </Aendring>
        </AendringsNummer>
        <AendringsNummer id="Par14_Stk1_Anu2">
          <Explicatus>2.</Explicatus>
          <Aendring>
            <AendringDefinition><Exitus><Linea><Char>I § 12, stk. 2, ændres </Char><Char formaChar="Italic">turismeudviklingsselskaber</Char><Char> til: </Char><Char formaChar="Italic">regionale rådgivere</Char></Linea></Exitus></AendringDefinition>
          </Aendring>
        </AendringsNummer>
      </Stk>
    </Paragraf>
  </DokumentIndhold>
</Dokument>`

const fixtureIkraftAmender = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta><AccessionNumber>B20090012405</AccessionNumber></Meta>
  <DokumentIndhold>
    <IkraftCentreretParagraf>
      <Explicatus>§ 2</Explicatus>
      <Stk>
        <Exitus><Linea><Char>Loven træder i kraft dagen efter bekendtgørelsen i Lovtidende.</Char></Linea></Exitus>
        <AendringsNummer>
          <Explicatus>1.</Explicatus>
          <Aendring>
            <AendringDefinition><Exitus><Linea><Char>I bekendtgørelse nr. 1399 af 12. december 2019 om lov om miljøbeskyttelse ændres </Char><Char formaChar="Italic">»X«</Char><Char> til: </Char><Char formaChar="Italic">»Y«</Char></Linea></Exitus></AendringDefinition>
          </Aendring>
        </AendringsNummer>
      </Stk>
    </IkraftCentreretParagraf>
  </DokumentIndhold>
</Dokument>`

// A bill's committee report lists what the members *proposed*. Those are not enacted
// amendments, and the consolidator declines bills on principle, so the walker must
// not claim them. If this ever stops holding, the loss census gains 22,311 changes
// that were proposals and the number in amendmentBlocks is a lie.
const fixtureBillProposals = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta><AccessionNumber>L20230002784</AccessionNumber></Meta>
  <DokumentIndhold>
    <BetaenkningsTekst>
      <AendringsForslagIndhold>
        <TilParagrafGruppe>
          <TilParagraf>
            <AendringsForslag>
              <AendringsNummer>
                <Explicatus>1.</Explicatus>
                <Aendring>
                  <AendringDefinition><Exitus><Linea><Char>I § 5 ændres »A« til: »B«</Char></Linea></Exitus></AendringDefinition>
                </Aendring>
              </AendringsNummer>
            </AendringsForslag>
          </TilParagraf>
        </TilParagrafGruppe>
      </AendringsForslagIndhold>
    </BetaenkningsTekst>
  </DokumentIndhold>
</Dokument>`

// A change's new text can itself contain an <AendringsNummer>, because the inserted
// provision quotes another act's change list. That text belongs to the change carrying
// it; treating it as an instruction of its own applies quoted text to live provisions.
const fixtureQuotedChangeText = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta><AccessionNumber>B20080020705</AccessionNumber></Meta>
  <DokumentIndhold>
    <AendringCentreretParagraf>
      <Explicatus>§ 1</Explicatus>
      <Exitus><Linea><Char>I lov nr. 602 af 24. juni 2005 om erhvervsfremme ændres:</Char></Linea></Exitus>
      <AendringsNummer>
        <Explicatus>1.</Explicatus>
        <Aendring>
          <AendringDefinition><Exitus><Linea><Char>§ 10</Char></Linea></Exitus></AendringDefinition>
          <AendringAktion><AendringNyTekst><Paragraf><Explicatus>§ 10.</Explicatus><Stk><Exitus><Linea><Char>Følgende change-liste gælder herefter:</Char></Linea></Exitus><AendringsNummer><Explicatus>2.</Explicatus><Aendring><AendringDefinition><Exitus><Linea><Char>Overalt ændres »old« til: »new«</Char></Linea></Exitus></AendringDefinition></Aendring></AendringsNummer></Stk></Paragraf></AendringNyTekst></AendringAktion>
        </Aendring>
      </AendringsNummer>
    </AendringCentreretParagraf>
  </DokumentIndhold>
</Dokument>`

func TestAmendmentShapesTheWalkerMustClaim(t *testing.T) {
	d, err := Parse([]byte(fixtureStkAmender))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	edits := d.Amendments()
	if len(edits) != 2 {
		t.Fatalf("a change list inside § 14, stk. 1 claimed %d changes, want 2", len(edits))
	}
	for i, e := range edits {
		if e.Target.Number != 602 || e.Target.Date.Year() != 2005 {
			t.Errorf("edit %d lost the act its own paragraph names: %d %v (raw %q)",
				i, e.Target.Number, e.Target.Date, e.Target.Raw)
		}
	}
	if edits[1].Kind != EditReplaceText {
		t.Errorf("quoted replace inside a stk list classified as %s, prose %q", edits[1].Kind, edits[1].Prose)
	}
	// The inserted § 9 a. is an instruction to insert, not a provision to replace.
	if edits[0].Kind != EditInsertProvision && edits[0].Kind != EditInsertText {
		t.Errorf("insert change classified as %s, definition %q", edits[0].Kind, edits[0].Definition)
	}
}

func TestIkraftParagraphChangesAreClaimed(t *testing.T) {
	d, err := Parse([]byte(fixtureIkraftAmender))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	edits := d.Amendments()
	if len(edits) != 1 {
		t.Fatalf("changes under IkraftCentreretParagraf claimed %d, want 1", len(edits))
	}
	if edits[0].Target.Number != 1399 {
		t.Errorf("target parsed as %d, want 1399 (raw %q)", edits[0].Target.Number, edits[0].Target.Raw)
	}
}

func TestBillProposalsAreNotAmendments(t *testing.T) {
	d, err := Parse([]byte(fixtureBillProposals))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := d.Amendments(); len(got) != 0 {
		t.Fatalf("a bill's proposal list yielded %d edits: %v", len(got), got[0].Prose)
	}
	if n := len(d.Root.FindNamed("AendringsNummer")); n == 0 {
		t.Fatal("fixture lost the markup the census counts")
	}
}

func TestQuotedChangeTextIsNotAnInstruction(t *testing.T) {
	d, err := Parse([]byte(fixtureQuotedChangeText))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	edits := d.Amendments()
	if len(edits) != 1 {
		t.Fatalf("quoted change text claimed %d edits, want only the change that carries it", len(edits))
	}
	if edits[0].Number != "1." {
		t.Errorf("claimed change %q instead of the carrying change", edits[0].Number)
	}
}
