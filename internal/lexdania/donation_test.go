package lexdania

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

// fixtureRunLaw is the law the run-amender below changes. It is its own fixture rather than
// fixtureLaw because it has to name B20080000405 in its Change anchors, and editing the shared
// fixture would move every other test's chain.
const fixtureRunLaw = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <DocumentType>Lovbekendtgørelse</DocumentType>
    <Rank>A</Rank>
    <AccessionNumber>A20050000129</AccessionNumber>
    <DocumentId>CA000111</DocumentId>
    <UniqueDocumentId>11111</UniqueDocumentId>
    <DocumentTitle>Bekendtgørelse af lov om anpartsselskaber</DocumentTitle>
    <Year>2005</Year>
    <DiesSigni>2005-03-01</DiesSigni>
    <StartDate>2005-04-01</StartDate>
    <Status>Historic</Status>
    <Number>1</Number>
    <Ministry>Justitsministeriet</Ministry>
    <Change id="change_1" />
    <Ref_Accn REFid="change_1">B20080000405</Ref_Accn>
    <Ref_Af REFid="change_1">2008-01-01</Ref_Af>
    <Ref_Text REFid="change_1">Bekendtgørelse om ændring af lov om anpartsselskaber nr 3</Ref_Text>
    <Sign id="sign_1" />
    <Signature REFid="sign_1">HANS HOLGER ARNESEN</Signature>
  </Meta>
  <TitelGruppe>
    <Titel><Linea><Char>Bekendtgørelse af lov om anpartsselskaber</Char></Linea></Titel>
  </TitelGruppe>
  <DokumentIndhold>
    <Paragraf id="p1" localId="1">
      <Explicatus>§ 1.</Explicatus>
      <Stk id="p1s1"><Exitus><Linea><Char>Loven gælder for anpartsselskaber.</Char></Linea></Exitus></Stk>
    </Paragraf>
    <Paragraf id="p2" localId="2">
      <Explicatus>§ 2.</Explicatus>
      <Stk id="p2s1"><Exitus><Linea><Char>Selskabet skal anmode om tilladelse før erhvervelse af egne anparter.</Char></Linea></Exitus></Stk>
      <Stk id="p2s2"><Exitus><Linea><Char>Tilladelsen gives af Erhvervsstyrelsen.</Char></Linea></Exitus></Stk>
    </Paragraf>
    <Paragraf id="p3" localId="3">
      <Explicatus>§ 3.</Explicatus>
      <Stk id="p3s1"><Exitus><Linea><Char>Loven træder i kraft straks.</Char></Linea></Exitus></Stk>
    </Paragraf>
  </DokumentIndhold>
</Dokument>`

// fixtureAmenderRun replaces a provision the way the register really writes it: the new wording
// arrives as a RUN of siblings inside AendringNyTekst — the paragraph holding only its first
// subsection, then the numbered items as a loose Exitus, then the following subsections as Stk
// elements beside the paragraph. This shape is taken from B20210128205's replacement of BR18 § 21.
//
// The engine used to insert the first node and drop the rest, which reported the change as
// applied while writing one sentence of a three-part provision. Everything later in the chain
// that cited § 2, stk. 2 was then refused for citing something that no longer existed.
const fixtureAmenderRun = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <DocumentType>Ændringsbekendtgørelse</DocumentType>
    <Rank>B</Rank>
    <AccessionNumber>B20080000405</AccessionNumber>
    <DocumentId>CM000444</DocumentId>
    <UniqueDocumentId>44444</UniqueDocumentId>
    <DocumentTitle>Bekendtgørelse om ændring af lov om anpartsselskaber nr 3</DocumentTitle>
    <Year>2008</Year>
    <DiesSigni>2007-12-15</DiesSigni>
    <Status>Historic</Status>
    <Number>4</Number>
  </Meta>
  <DokumentIndhold>
    <AendringCentreretParagraf id="a1" localId="1">
      <Explicatus>§ 1</Explicatus>
      <Exitus>
        <Linea><Char>I bekendtgørelse nr. 1 af 1. marts 2005 om lov om anpartsselskaber foretages følgende ændringer:</Char></Linea>
      </Exitus>
      <AendringsNummer id="n1">
        <Explicatus>1.</Explicatus>
        <Aendring id="ae1">
          <AendringDefinition id="ad1">
            <Exitus><Linea><Char formaChar="Italic">§ 2 </Char><Char>affattes således:</Char></Linea></Exitus>
          </AendringDefinition>
          <AendringAktion id="aa1">
            <AendringNyTekst>
              <Paragraf id="np2" localId="2">
                <Explicatus>§ 2.</Explicatus>
                <Stk id="np2s1">
                  <Exitus><Linea><Char>Selskabet skal oplyse om sine adresser:</Char></Linea></Exitus>
                </Stk>
              </Paragraf>
              <Exitus>
                <Index>
                  <Indentatio formaInd="Nummer"><Explicatus>1)</Explicatus><Exitus><Linea><Char>den ved selskabets vedtægter vedtagelse</Char></Linea></Exitus></Indentatio>
                  <Indentatio formaInd="Nummer"><Explicatus>2)</Explicatus><Exitus><Linea><Char>den hvor bestyrelsen har mødested</Char></Linea></Exitus></Indentatio>
                </Index>
              </Exitus>
              <Stk id="np2s2">
                <Exitus><Linea><Char>Oplysningen skal ske til Erhvervsstyrelsen.</Char></Linea></Exitus>
              </Stk>
            </AendringNyTekst>
          </AendringAktion>
        </Aendring>
      </AendringsNummer>
    </AendringCentreretParagraf>
  </DokumentIndhold>
</Dokument>`

// TestReplacementKeepsTheRunItArrivesWith is the whole-provision case: the paragraph, its items
// and its subsections all have to land inside the replaced provision.
func TestReplacementKeepsTheRunItArrivesWith(t *testing.T) {
	a := writeArchive(t, map[string]string{
		"lta/2005/1.xml": fixtureRunLaw,
		"lta/2008/4.xml": fixtureAmenderRun,
	})
	v, err := a.LawAsOf("A20050000129", time.Date(2008, time.January, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Unresolved) > 0 {
		t.Fatalf("the replacement was refused: %v", v.Unresolved[0].Reason)
	}

	text, ok := v.ProvisionText("§ 2")
	if !ok {
		t.Fatal("§ 2 is gone after being replaced")
	}
	for _, want := range []string{
		"Selskabet skal oplyse om sine adresser:",     // the paragraph's own subsection
		"den hvor bestyrelsen har mødested",           // a numbered item that arrived loose
		"Oplysningen skal ske til Erhvervsstyrelsen.", // a subsection that arrived beside it
	} {
		if !strings.Contains(text, want) {
			t.Errorf("§ 2 lost part of the run it was replaced with; missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "anmode om tilladelse") {
		t.Errorf("§ 2 still shows the wording it was replaced away:\n%s", text)
	}

	// The reason the loss matters: the subsection has to stay citable, or the next act's change
	// to "§ 2, stk. 2" is refused against text that is quietly shorter than the law.
	stk2, ok := v.ProvisionText("§ 2, stk. 2")
	if !ok {
		t.Fatalf("§ 2, stk. 2 does not resolve after the replacement; § 2 now reads:\n%s", text)
	}
	if !strings.Contains(stk2, "Erhvervsstyrelsen") {
		t.Errorf("§ 2, stk. 2 resolves to the wrong text: %q", stk2)
	}
}

// TestReplacementPlacesFurtherParagraphsAfter covers the other shape hiding in the same run:
// "§§ 2-3 affattes således" delivers two paragraphs, and the second is a new provision, not a
// subsection of the first.
func TestReplacementPlacesFurtherParagraphsAfter(t *testing.T) {
	both := strings.Replace(fixtureAmenderRun,
		`              <Stk id="np2s2">
                <Exitus><Linea><Char>Oplysningen skal ske til Erhvervsstyrelsen.</Char></Linea></Exitus>
              </Stk>`,
		`              <Stk id="np2s2">
                <Exitus><Linea><Char>Oplysningen skal ske til Erhvervsstyrelsen.</Char></Linea></Exitus>
              </Stk>
              <Paragraf id="np2b" localId="2 a">
                <Explicatus>§ 2 a.</Explicatus>
                <Stk id="np2bs1">
                  <Exitus><Linea><Char>Oplysningen for § 2 a's vedkommende er offentlig.</Char></Linea></Exitus>
                </Stk>
              </Paragraf>`, 1)
	a := writeArchive(t, map[string]string{
		"lta/2005/1.xml": fixtureRunLaw,
		"lta/2008/4.xml": both,
	})
	v, err := a.LawAsOf("A20050000129", time.Date(2008, time.January, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	extra, ok := v.ProvisionText("§ 2 a")
	if !ok {
		secs := []string{}
		for _, ref := range v.Content.Provisions() {
			secs = append(secs, ref.Provision.Address())
		}
		t.Fatalf("the second paragraph of the run was not placed; provisions are %v", secs)
	}
	if !strings.Contains(extra, "er offentlig") {
		t.Errorf("§ 2 a placed with the wrong text: %q", extra)
	}
	if s, ok := v.ProvisionText("§ 2, stk. 2"); !ok || !strings.Contains(s, "Erhvervsstyrelsen") {
		t.Error("placing the second paragraph disturbed § 2, stk. 2")
	}
}

// TestAProvisionPrintedLooseUnderAGroupHeadingIsKept is lov nr. 528 of 2008's "Efter § 9 a
// indsættes: Kompensation for CO2-afgift § 9 b. …": the rubric, then § 9 b as loose lines and
// subsections with no provision element around them. The group arrived empty.
func TestAProvisionPrintedLooseUnderAGroupHeadingIsKept(t *testing.T) {
	src := `<AendringNyTekst overskriftAfType="ParagrafGruppe">
  <Rubrica><Linea><Char>Kompensation for CO</Char><Char formaChar="Subscript">2</Char><Char>-afgift</Char></Linea></Rubrica>
  <Exitus><Linea><Char formaChar="Bold">§ 9 b.</Char></Linea><Linea><Char>Elproducenter har ret til en kompensation.</Char></Linea></Exitus>
  <Stk><Explicatus>Stk. 2.</Explicatus><Exitus><Linea><Char>Kompensationen udbetales månedligt.</Char></Linea></Exitus></Stk>
</AendringNyTekst>`
	dec := xml.NewDecoder(strings.NewReader(src))
	var root *Node
	for root == nil {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			if root, err = decodeElement(dec, se); err != nil {
				t.Fatal(err)
			}
		}
	}
	d := donation(root.Elems)
	if len(d.parts) != 1 || len(d.parts[0].Order) != 1 || d.parts[0].Order[0].Provision == nil {
		t.Fatalf("parts %+v, prose %q, stykker %d", d.parts, d.prose, len(d.stykker))
	}
	p := d.parts[0].Order[0].Provision
	if p.Number != "9" || p.Litra != "b" || len(p.Stykker) != 2 {
		t.Errorf("§ %s%s with %d subsections: %q", p.Number, p.Litra, len(p.Stykker), p.Text())
	}
}
