package lexdania

import "testing"

// Fixtures are hand-built from the structures observed in the archive, not invented:
// the Meta anchor/REFid shape comes from lta/2005/433.xml, the amendment markup from
// lta/2025/271.xml (the BR18 amendment act).
//
// fixtureLaw is the base act: rank A, year 2005, number 1, signed 2005-03-01, with
// two later amending documents recorded as Change anchors.

const fixtureLaw = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument SchemaLocation="http://www.retsinformation.dk/offentlig/xml/schemas/20110301/dl.xsd">
  <Meta>
    <DocumentType>Lovbekendtgørelse</DocumentType>
    <Rank>A</Rank>
    <AccessionNumber>A20050000129</AccessionNumber>
    <DocumentId>CA000111</DocumentId>
    <UniqueDocumentId>11111</UniqueDocumentId>
    <DocumentTitle>Bekendtgørelse af lov om anpartsselskaber</DocumentTitle>
    <Year>2005</Year>
    <DiesSigni>2005-03-01</DiesSigni>
    <DateOfSubmit id="submit_1" />
    <StartDate REFid="submit_1">2005-04-01</StartDate>
    <EndDate REFid="submit_1">2015-01-01</EndDate>
    <Status>Historic</Status>
    <Number>1</Number>
    <Ministry>Justitsministeriet</Ministry>
    <PopularTitle>Anpartsselskabsloven</PopularTitle>
    <AnnouncedIn>Lovtidende A</AnnouncedIn>
    <Subject>selskabsret</Subject>
    <Subject>anpartsselskaber</Subject>
    <EuReferences>32017L1132</EuReferences>
    <Concerns id="concerns_1" />
    <Ref_Accn REFid="concerns_1">A20040099929</Ref_Accn>
    <Ref_Af REFid="concerns_1">2004-06-05</Ref_Af>
    <Ref_Text REFid="concerns_1">Bekendtgørelse af lov om erhvervsdrivende fonde</Ref_Text>
    <Change id="change_1" />
    <Ref_Accn REFid="change_1">B20060000205</Ref_Accn>
    <Ref_Af REFid="change_1">2006-07-01</Ref_Af>
    <Ref_Text REFid="change_1">Bekendtgørelse om ændring af anpartsselskabsloven</Ref_Text>
    <Change id="change_2" />
    <Ref_Accn REFid="change_2">B20070000305</Ref_Accn>
    <Ref_Af REFid="change_2">2007-01-01</Ref_Af>
    <Ref_Text REFid="change_2">Bekendtgørelse om ændring af anpartsselskabsloven nr 2</Ref_Text>
    <Sign id="sign_1" />
    <Signature REFid="sign_1">HANS HOLGER ARNESEN</Signature>
  </Meta>
  <TitelGruppe>
    <Titel>
      <Linea><Char>Bekendtgørelse af lov om anpartsselskaber</Char></Linea>
    </Titel>
  </TitelGruppe>
  <DokumentIndhold>
    <Kapitel id="k1" localId="1">
      <Rubrica>Kapitel 1</Rubrica>
    </Kapitel>
    <Paragraf id="p1" localId="1">
      <Explicatus>§ 1.</Explicatus>
      <Stk id="p1s1">
        <Exitus><Linea><Char>Loven gælder for anpartsselskaber.</Char></Linea></Exitus>
      </Stk>
    </Paragraf>
    <Paragraf id="p2" localId="2">
      <Explicatus>§ 2.</Explicatus>
      <Stk id="p2s1">
        <Exitus>
          <Linea>
            <Char>Selskabet skal anmode om </Char>
            <Char formaChar="Italic">tilladelse</Char>
            <Char> før erhvervelse af egne anparter.</Char>
          </Linea>
        </Exitus>
      </Stk>
      <Stk id="p2s2">
        <Exitus><Linea><Char>Tilladelsen gives af Erhvervsstyrelsen.</Char></Linea></Exitus>
      </Stk>
    </Paragraf>
    <Paragraf id="p3" localId="3">
      <Explicatus>§ 3.</Explicatus>
      <Stk id="p3s1">
        <Exitus><Linea><Char>Loven træder i kraft straks.</Char></Linea></Exitus>
      </Stk>
    </Paragraf>
  </DokumentIndhold>
  <UnderskriftGruppe>
    <UnderskriftStedDato><Linea><Char>Slotsholmen, den 1. marts 2005.</Char></Linea></UnderskriftStedDato>
    <Underskriver><Linea><Char>HANS HOLGER ARNESEN</Char></Linea></Underskriver>
  </UnderskriftGruppe>
  <Bilag id="b1" localId="1">
    <Rubrica>Bilag 1</Rubrica>
    <BilagIndhold><Exitus><Linea><Char>Skema over krav.</Char></Linea></Exitus></BilagIndhold>
  </Bilag>
</Dokument>`

// fixtureAmender1 amends the base law in one place and, in a second paragraph,
// amends a DIFFERENT law (nr. 999 af 5. juni 2004). The second paragraph must not be
// applied to the base law — this fixture exists to prove that it is not.
//
// The quoted passage spans three <Char> runs with an italic run in the middle, which
// is exactly the shape that breaks naive quote matching.
const fixtureAmender1 = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <DocumentType>BEK Æ#LOKDOK05</DocumentType>
    <Rank>B</Rank>
    <AccessionNumber>B20060000205</AccessionNumber>
    <DocumentId>CM000222</DocumentId>
    <UniqueDocumentId>22222</UniqueDocumentId>
    <DocumentTitle>Bekendtgørelse om ændring af lov om anpartsselskaber</DocumentTitle>
    <Year>2006</Year>
    <DiesSigni>2006-06-20</DiesSigni>
    <Status>Historic</Status>
    <Number>2</Number>
    <Ministry>Justitsministeriet</Ministry>
  </Meta>
  <DokumentIndhold>
    <AendringCentreretParagraf id="a1" localId="1">
      <Explicatus>§ 1</Explicatus>
      <Exitus>
        <Linea><Char>I bekendtgørelse nr. 1 af 1. marts 2005 om lov om anpartsselskaber, som senest ændret ved bekendtgørelse nr. 55 af 10. februar 2006, foretages følgende ændringer:</Char></Linea>
      </Exitus>
      <AendringsNummer id="n1">
        <Explicatus>1.</Explicatus>
        <Aendring id="ae1">
          <AendringDefinition id="ad1">
            <Exitus>
              <Linea>
                <Char>I </Char>
                <Char formaChar="Italic">§ 2, stk. 1,</Char>
                <Char> ændres »anmode om tilladelse før« til: »søge om tilladelse før«.</Char>
              </Linea>
            </Exitus>
          </AendringDefinition>
          <AendringAktion id="aa1"><AendringNyTekst /></AendringAktion>
        </Aendring>
      </AendringsNummer>
    </AendringCentreretParagraf>
    <AendringCentreretParagraf id="a2" localId="2">
      <Explicatus>§ 2</Explicatus>
      <Exitus>
        <Linea><Char>I bekendtgørelse nr. 999 af 5. juni 2004 om erhvervsdrivende fonde foretages følgende ændringer:</Char></Linea>
      </Exitus>
      <AendringsNummer id="n2">
        <Explicatus>1.</Explicatus>
        <Aendring id="ae2">
          <AendringDefinition id="ad2">
            <Exitus><Linea><Char>§ 7 ophæves.</Char></Linea></Exitus>
          </AendringDefinition>
          <AendringAktion id="aa2"><AendringNyTekst /></AendringAktion>
        </Aendring>
      </AendringsNummer>
    </AendringCentreretParagraf>
  </DokumentIndhold>
</Dokument>`

// fixtureAmender2 repeals § 3 and inserts a new § 4 with structured new text, which
// is the shape that carries <AendringNyTekst> markup rather than a quoted passage.
const fixtureAmender2 = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <DocumentType>Ændringsbekendtgørelse</DocumentType>
    <Rank>B</Rank>
    <AccessionNumber>B20070000305</AccessionNumber>
    <DocumentId>CM000333</DocumentId>
    <UniqueDocumentId>33333</UniqueDocumentId>
    <DocumentTitle>Bekendtgørelse om ændring af lov om anpartsselskaber nr 2</DocumentTitle>
    <Year>2007</Year>
    <DiesSigni>2006-12-15</DiesSigni>
    <Status>Historic</Status>
    <Number>3</Number>
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
            <Exitus><Linea><Char>§ 3 ophæves.</Char></Linea></Exitus>
          </AendringDefinition>
          <AendringAktion id="aa1"><AendringNyTekst /></AendringAktion>
        </Aendring>
      </AendringsNummer>
      <AendringsNummer id="n2">
        <Explicatus>2.</Explicatus>
        <Aendring id="ae2">
          <AendringDefinition id="ad2">
            <Exitus><Linea><Char>Efter § 3 indsættes en ny § 4, således:</Char></Linea></Exitus>
          </AendringDefinition>
          <AendringAktion id="aa2">
            <AendringNyTekst>
              <Paragraf id="newp4" localId="4">
                <Explicatus>§ 4.</Explicatus>
                <Stk id="newp4s1">
                  <Exitus><Linea><Char>Selskabet skal optage en henvisning i erhvervsregistret.</Char></Linea></Exitus>
                </Stk>
              </Paragraf>
            </AendringNyTekst>
          </AendringAktion>
        </Aendring>
      </AendringsNummer>
    </AendringCentreretParagraf>
  </DokumentIndhold>
</Dokument>`

func mustParse(t *testing.T, src string) *Document {
	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return doc
}
