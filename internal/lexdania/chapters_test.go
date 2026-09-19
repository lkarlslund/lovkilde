package lexdania

import (
	"strings"
	"testing"
	"time"
)

// fixtureChapteredLaw is organised the way larger Danish acts are:
// Afsnit > Kapitel > ParagrafGruppe (with a Rubrica heading) > Paragraf. The flat
// fixtures elsewhere never exercise the descent into Afsnit and Kapitel, which is
// exactly how the "no paragraphs at all" bug survived them.
const fixtureChapteredLaw = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <DocumentType>BEK H#LOKDOK04</DocumentType>
    <Rank>B</Rank>
    <AccessionNumber>B202600029305</AccessionNumber>
    <DocumentId>CM000333</DocumentId>
    <UniqueDocumentId>33333</UniqueDocumentId>
    <DocumentTitle>Bekendtgørelse om smittebeskyttelse</DocumentTitle>
    <Year>2026</Year>
    <DiesSigni>2026-02-20</DiesSigni>
    <Status>Current</Status>
    <Number>293</Number>
    <Ministry>Fødevareministeriet</Ministry>
    <Change id="change_1" />
    <Ref_Accn REFid="change_1">B202600038905</Ref_Accn>
    <Ref_Af REFid="change_1">2026-03-19</Ref_Af>
    <Ref_Text REFid="change_1">Bekendtgørelse om ændring af bekendtgørelse om smittebeskyttelse</Ref_Text>
  </Meta>
  <DokumentIndhold>
    <Afsnit localId="1">
      <Kapitel localId="1">
        <ParagrafGruppe>
          <Rubrica><Linea><Char>Definitioner</Char></Linea></Rubrica>
          <Paragraf localId="1">
            <Explicatus>§ 1.</Explicatus>
            <Stk><Exitus><Linea><Char>Formålet er at forebygge smitte.</Char></Linea></Exitus></Stk>
          </Paragraf>
          <Paragraf localId="2">
            <Explicatus>§ 2.</Explicatus>
            <Stk><Exitus><Linea><Char>Beskyttelseszonen har en radius på 3 kilometer.</Char></Linea></Exitus></Stk>
          </Paragraf>
        </ParagrafGruppe>
      </Kapitel>
      <Kapitel localId="2">
        <ParagrafGruppe>
          <Rubrica><Linea><Char>Straffebestemmelser</Char></Linea></Rubrica>
          <Paragraf localId="3">
            <Explicatus>§ 3.</Explicatus>
            <Stk><Exitus><Linea><Char>Straffen kan stige til fængsel i indtil 2 år.</Char></Linea></Exitus></Stk>
            <Stk><Exitus><Linea><Char>Der kan pålægges selskaber strafansvar.</Char></Linea></Exitus></Stk>
          </Paragraf>
        </ParagrafGruppe>
      </Kapitel>
    </Afsnit>
  </DokumentIndhold>
</Dokument>`

// TestSectionsDescendIntoChapters guards the bug where the walker recorded Afsnit and
// Kapitel and then returned, so every provision inside a chapter was invisible and
// the document appeared to contain no paragraphs at all.
func TestSectionsDescendIntoChapters(t *testing.T) {
	doc := mustParse(t, fixtureChapteredLaw)
	secs := doc.Sections()

	var paras []string
	for _, s := range secs {
		if s.Kind == "paragraf" && s.Parent == nil {
			paras = append(paras, s.Number)
		}
	}
	if strings.Join(paras, " ") != "1 2 3" {
		t.Fatalf("top-level paragraphs = %v, want [1 2 3]", paras)
	}

	if s := sec(doc, "§ 3"); s == nil {
		t.Fatal("§ 3 inside Kapitel 2 is not addressable")
	} else if s.Stk != 0 {
		t.Errorf("a paragraph itself must not carry a stk number, got %d", s.Stk)
	}
	if s := sec(doc, "§ 3, stk. 2"); s == nil {
		t.Fatal("§ 3, stk. 2 inside Kapitel 2 is not addressable")
	} else if !strings.Contains(s.Display(), "selskaber") {
		t.Errorf("§ 3, stk. 2 = %q", s.Display())
	}

	// The containers are recorded too, since amendments cite kapitels by number.
	kapitels := 0
	for _, s := range secs {
		if s.Kind == "kapitel" {
			kapitels++
		}
	}
	if kapitels != 2 {
		t.Errorf("kapitel sections = %d, want 2", kapitels)
	}
}

// TestHeadingEditsLeaveProvisionAlone guards the conflation of a heading with the
// provision under it. "Overskriften før § 3 ophæves" removes the Rubrica, not § 3,
// and deleting the provision instead made the act's following "§ 3 ophæves" fail.
func TestHeadingEditsLeaveProvisionAlone(t *testing.T) {
	doc := mustParse(t, fixtureChapteredLaw)
	// § 3 is the only provision in the second chapter's group, headed
	// "Straffebestemmelser"; § 1 and § 2 sit under "Definitioner".
	if !deleteHeading(sec(doc, "§ 3")) {
		t.Fatal("the heading before § 3 should be deletable")
	}
	if sec(doc, "§ 3").Display() == "" {
		t.Fatal("deleting a heading must leave the provision's text intact")
	}
	if got := sec(doc, "§ 3").Display(); !strings.Contains(got, "fængsel") {
		t.Errorf("§ 3 text after heading deletion = %q", got)
	}
	if !strings.Contains(doc.Root.PlainText(), "Definitioner") {
		t.Error("the other heading should still be present")
	}
	if strings.Contains(doc.Root.PlainText(), "Straffebestemmelser") {
		t.Error("the deleted heading is still in the text")
	}

	// Replacing a heading leaves the provisions under it alone.
	if !setHeadingText(mustHeading(t, doc, "1"), "Bøder og strafansvar") {
		t.Fatal("heading replacement failed")
	}
	if !strings.Contains(doc.Root.PlainText(), "Bøder og strafansvar") {
		t.Error("new heading text is missing")
	}
	if strings.Contains(doc.Root.PlainText(), "Definitioner") {
		t.Error("the old heading text survived the replacement")
	}
	if got := sec(doc, "§ 1").Display(); !strings.Contains(got, "forebygge smitte") {
		t.Errorf("the provision under a replaced heading must be untouched, got %q", got)
	}
	if err := doc.Root.checkInvariant(""); err != nil {
		t.Errorf("synthesised heading broke the tree: %v", err)
	}
}

// sec resolves an address inside a document, mirroring View.Section for documents
// that were never put through a consolidation.
func sec(doc *Document, addr string) *Section {
	return Find(doc.Sections(), ParseAddress(addr))
}

func mustHeading(t *testing.T, doc *Document, number string) *Node {
	s := sec(doc, "§ "+number)
	if s == nil {
		t.Fatalf("§ %s is not addressable", number)
	}
	h, _ := headingFor(s.Container, s.Node)
	if h == nil {
		t.Fatalf("no heading before § %s", number)
	}
	return h
}

// TestLitraAndNumberedItems covers the two numbering shapes that made most deep
// addresses unresolvable in real laws: the printed "§ 2 a." (cited by amendments as
// "§ 2a") and numbered items written as <Index><Indentatio formaInd="Nummer"> rather
// than <Nr>.
func TestLitraAndNumberedItems(t *testing.T) {
	doc := mustParse(t, fixtureLitraLaw)

	if s := sec(doc, "§ 2a"); s == nil {
		t.Fatal(`"§ 2a" does not resolve, although the law prints "§ 2 a."`)
	} else if s.Litra != "a" {
		t.Errorf("Litra = %q, want %q", s.Litra, "a")
	}
	// The plain number must not accidentally match the litra provision.
	if s := sec(doc, "§ 3"); s == nil {
		t.Error("§ 3 should still resolve on its own number")
	}

	if s := sec(doc, "§ 2a, stk. 1, nr. 2"); s == nil {
		t.Fatal("nr. 2 under a stk is not reachable through Index/Indentatio")
	} else if !strings.Contains(s.Display(), "adresse") {
		t.Errorf("nr. 2 = %q", s.Display())
	}
	if s := sec(doc, "§ 2a, stk. 1, nr. 1"); s == nil || !strings.Contains(s.Display(), "navn") {
		t.Fatalf("nr. 1 = %+v", s)
	}
	if s := sec(doc, "§ 2a, stk. 1, litra b"); s == nil {
		t.Fatal("lettered items should be addressable by litra")
	} else if !strings.Contains(s.Display(), "køretøj") {
		t.Errorf("litra b = %q", s.Display())
	}

	// Unnumbered indentation is layout, not a provision.
	nrs := 0
	for _, s := range doc.Sections() {
		if s.Kind == "nr" {
			nrs++
		}
	}
	if nrs != 4 {
		t.Errorf("nr sections = %d, want 4 (the plain Indentatio must not count)", nrs)
	}
}

// TestItemAddressWithoutStykke covers the citation an act actually writes. A provision with
// one stykke is cited as "§ 92, nr. 2"; nobody says "stk. 1". The item was already modelled
// and rendered, but Find searched the paragraph's own children for it, so 26 of BR18's 109
// refusals were items we could see and could not name — and BR18's 814 numbered items were not
// citable at all.
func TestItemAddressWithoutStykke(t *testing.T) {
	doc := mustParse(t, fixtureLitraLaw)

	if s := sec(doc, "§ 2a, nr. 2"); s == nil {
		t.Fatal(`"§ 2a, nr. 2" does not resolve, although the law prints "2)" under one stykke`)
	} else if !strings.Contains(s.Display(), "adresse") {
		t.Errorf(`"§ 2a, nr. 2" = %q`, s.Display())
	}
	if s := sec(doc, "§ 2a, litra b"); s == nil || !strings.Contains(s.Display(), "køretøj") {
		t.Errorf(`"§ 2a, litra b" = %+v`, s)
	}
	// The stykke still wins when the citation names it.
	if s := sec(doc, "§ 2a, stk. 1, nr. 1"); s == nil || !strings.Contains(s.Display(), "navn") {
		t.Errorf(`"§ 2a, stk. 1, nr. 1" = %+v`, s)
	}
	// A lettered paragraph written the comma way is the paragraph, not one of its items.
	if s := sec(doc, "§ 2, litra a"); s == nil {
		t.Fatal(`"§ 2, litra a" should reach the lettered paragraph`)
	} else if s.Kind != "paragraf" {
		t.Errorf(`"§ 2, litra a" resolved to a %s, want the paragraph`, s.Kind)
	}
	// A number no stykke carries is still nothing.
	if s := sec(doc, "§ 2a, nr. 7"); s != nil {
		t.Errorf("nr. 7 does not exist, got %q", s.Display())
	}

	// Two stykker carrying the same item number is ambiguous, and guessing which one an
	// amendment meant would edit the wrong provision.
	two := mustParse(t, fixtureTwoStykkerSameNr)
	if s := sec(two, "§ 5, nr. 2"); s != nil {
		t.Errorf(`"§ 5, nr. 2" is ambiguous across two stykker, got %q`, s.Display())
	}
	if s := sec(two, "§ 5, stk. 2, nr. 2"); s == nil || !strings.Contains(s.Display(), "fjerde") {
		t.Errorf(`"§ 5, stk. 2, nr. 2" = %+v`, s)
	}
	if s := sec(two, "§ 6, nr. 1"); s == nil || !strings.Contains(s.Display(), "entydig") {
		t.Errorf(`"§ 6, nr. 1" should resolve from the only stykke carrying it, got %+v`, s)
	}
}

const fixtureTwoStykkerSameNr = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <DocumentType>BEK H#LOKDOK04</DocumentType>
    <Rank>B</Rank>
    <AccessionNumber>B202600024215</AccessionNumber>
    <DocumentId>CM000445</DocumentId>
    <UniqueDocumentId>44445</UniqueDocumentId>
    <DocumentTitle>Bekendtgørelse om tvetydighed</DocumentTitle>
    <Year>2026</Year>
    <DiesSigni>2026-04-10</DiesSigni>
    <Status>Current</Status>
    <Number>242</Number>
    <Ministry>Transportministeriet</Ministry>
  </Meta>
  <DokumentIndhold>
    <Paragraf>
      <Explicatus>§ 5.</Explicatus>
      <Stk>
        <Exitus><Linea><Char>Liste over det første:</Char></Linea></Exitus>
        <Exitus><Index>
          <Indentatio formaInd="Nummer"><Explicatus>1)</Explicatus><Exitus><Linea><Char>første</Char></Linea></Exitus></Indentatio>
          <Indentatio formaInd="Nummer"><Explicatus>2)</Explicatus><Exitus><Linea><Char>andet</Char></Linea></Exitus></Indentatio>
        </Index></Exitus>
      </Stk>
      <Stk>
        <Exitus><Linea><Char>Liste over det andet:</Char></Linea></Exitus>
        <Exitus><Index>
          <Indentatio formaInd="Nummer"><Explicatus>1)</Explicatus><Exitus><Linea><Char>tredje</Char></Linea></Exitus></Indentatio>
          <Indentatio formaInd="Nummer"><Explicatus>2)</Explicatus><Exitus><Linea><Char>fjerde</Char></Linea></Exitus></Indentatio>
        </Index></Exitus>
      </Stk>
    </Paragraf>
    <Paragraf>
      <Explicatus>§ 6.</Explicatus>
      <Stk>
        <Exitus><Linea><Char>Kun den første stykke har liste:</Char></Linea></Exitus>
        <Exitus><Index>
          <Indentatio formaInd="Nummer"><Explicatus>1)</Explicatus><Exitus><Linea><Char>entydig</Char></Linea></Exitus></Indentatio>
        </Index></Exitus>
      </Stk>
      <Stk>
        <Exitus><Linea><Char>Det andet stykke har ingen punkter.</Char></Linea></Exitus>
      </Stk>
    </Paragraf>
  </DokumentIndhold>
</Dokument>`

const fixtureLitraLaw = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <DocumentType>BEK H#LOKDOK04</DocumentType>
    <Rank>B</Rank>
    <AccessionNumber>B202600024205</AccessionNumber>
    <DocumentId>CM000444</DocumentId>
    <UniqueDocumentId>44444</UniqueDocumentId>
    <DocumentTitle>Bekendtgørelse om tilladelser</DocumentTitle>
    <Year>2026</Year>
    <DiesSigni>2026-04-10</DiesSigni>
    <Status>Current</Status>
    <Number>242</Number>
    <Ministry>Transportministeriet</Ministry>
  </Meta>
  <DokumentIndhold>
    <Paragraf>
      <Explicatus>§ 1.</Explicatus>
      <Stk><Exitus><Linea><Char>Tilladelse kan gives ved brev.</Char></Linea></Exitus></Stk>
    </Paragraf>
    <Paragraf>
      <Explicatus>§ 2 a.</Explicatus>
      <Stk>
        <Exitus><Linea><Char>Ansøgningen skal indeholde:</Char></Linea></Exitus>
        <Exitus>
          <Index>
            <Indentatio formaInd="Nummer">
              <Explicatus>1)</Explicatus>
              <Exitus><Linea><Char>navn og cpr-nr.</Char></Linea></Exitus>
            </Indentatio>
            <Indentatio formaInd="Nummer">
              <Explicatus>2)</Explicatus>
              <Exitus><Linea><Char>adresse og telefonnr.</Char></Linea></Exitus>
            </Indentatio>
          </Index>
        </Exitus>
        <Exitus>
          <Index>
            <Indentatio formaInd="Litra">
              <Explicatus>a)</Explicatus>
              <Exitus><Linea><Char>postadresse</Char></Linea></Exitus>
            </Indentatio>
            <Indentatio formaInd="Litra">
              <Explicatus>b)</Explicatus>
              <Exitus><Linea><Char>køretøjets type</Char></Linea></Exitus>
            </Indentatio>
          </Index>
        </Exitus>
        <Exitus><Linea><Char>Ansøgningen kan ikke afvises.</Char></Linea></Exitus>
      </Stk>
    </Paragraf>
    <Paragraf>
      <Explicatus>§ 3.</Explicatus>
      <Stk>
        <Exitus><Linea><Char>Klage skal sendes.</Char></Linea></Exitus>
        <Exitus><Indentatio><Char>Uden for indekset står denne tekst.</Char></Indentatio></Exitus>
      </Stk>
    </Paragraf>
  </DokumentIndhold>
</Dokument>`

// TestUdgaarIsRecognised covers "I § 6, stk. 1, udgår »X«.", the ordinary Danish
// deletion wording, which the pattern set previously reported as unrecognised on real
// 2026 amendments.
func TestUdgaarIsRecognised(t *testing.T) {
	amender := strings.Replace(fixtureAmender1,
		"<Char> ændres »anmode om tilladelse før« til: »søge om tilladelse før«.</Char>",
		"<Char> udgår »anmode om tilladelse før«.</Char>", 1)
	doc := mustParse(t, amender)
	eds := doc.Amendments()
	if len(eds) == 0 {
		t.Fatal("no amendments parsed")
	}
	if eds[0].Kind != EditDeleteText {
		t.Fatalf("kind = %s, want %s", eds[0].Kind, EditDeleteText)
	}
	if eds[0].Old != "anmode om tilladelse før" {
		t.Errorf("Old = %q", eds[0].Old)
	}

	a := writeArchive(t, map[string]string{
		"lta/2005/1.xml":   fixtureLaw,
		"lta/2006/205.xml": amender,
	})
	v, err := a.LawAsOf("A20050000129", time.Date(2008, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(v.Doc.Root.PlainText(), "anmode om tilladelse før") {
		t.Error("the udgår passage is still in the consolidated text")
	}
}

// fixtureChapterAmender is the real shape found in lta/2026/389.xml: a heading repeal
// and a provision repeal for the same §, then a passage deletion, in one act.
const fixtureChapterAmender = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <DocumentType>BEK Æ#LOKDOK05</DocumentType>
    <Rank>B</Rank>
    <AccessionNumber>B202600038905</AccessionNumber>
    <DocumentId>CM000555</DocumentId>
    <UniqueDocumentId>55555</UniqueDocumentId>
    <DocumentTitle>Bekendtgørelse om ændring af bekendtgørelse om smittebeskyttelse</DocumentTitle>
    <Year>2026</Year>
    <DiesSigni>2026-03-19</DiesSigni>
    <Status>Current</Status>
    <Number>389</Number>
    <Ministry>Fødevareministeriet</Ministry>
  </Meta>
  <DokumentIndhold>
    <AendringCentreretParagraf localId="1">
      <Explicatus>§ 1</Explicatus>
      <Exitus><Linea><Char>I bekendtgørelse nr. 293 af 20. februar 2026 om smittebeskyttelse foretages følgende ændringer:</Char></Linea></Exitus>
      <AendringsNummer>
        <Explicatus>1.</Explicatus>
        <Aendring>
          <AendringDefinition><Exitus><Linea><Char>Overskriften før § 3 ophæves.</Char></Linea></Exitus></AendringDefinition>
          <AendringAktion><AendringNyTekst /></AendringAktion>
        </Aendring>
      </AendringsNummer>
      <AendringsNummer>
        <Explicatus>2.</Explicatus>
        <Aendring>
          <AendringDefinition><Exitus><Linea><Char>§ 3 ophæves.</Char></Linea></Exitus></AendringDefinition>
          <AendringAktion><AendringNyTekst /></AendringAktion>
        </Aendring>
      </AendringsNummer>
      <AendringsNummer>
        <Explicatus>3.</Explicatus>
        <Aendring>
          <AendringDefinition><Exitus><Linea><Char>I § 1, stk. 1, udgår »forebygge smitte«.</Char></Linea></Exitus></AendringDefinition>
          <AendringAktion><AendringNyTekst /></AendringAktion>
        </Aendring>
      </AendringsNummer>
    </AendringCentreretParagraf>
  </DokumentIndhold>
</Dokument>`

// TestHeadingReplaceWiring drives the replace-provision branch of applyOne directly:
// the chain-level fixture has no realistic single-quote heading rewrite, so without
// this the wiring between Address.Heading and setHeadingText would be unguarded.
func TestHeadingReplaceWiring(t *testing.T) {
	base := mustParse(t, fixtureChapteredLaw)
	v := &View{Doc: base.Clone()}
	v.Doc.Meta = base.Meta
	e := Edit{
		Number:  "1.",
		Kind:    EditReplaceProvision,
		Address: ParseAddress("Overskriften for § 3 ændres til: »Bøder og strafansvar«."),
		New:     "Bøder og strafansvar",
	}
	if !e.Address.Heading {
		t.Fatal("the address should be flagged as a heading edit")
	}
	v.applyOne(e)
	if len(v.Unresolved) != 0 {
		t.Fatalf("heading replacement should apply: %+v", v.Unresolved)
	}
	text := v.Doc.Root.PlainText()
	if !strings.Contains(text, "Bøder og strafansvar") {
		t.Error("the new heading is missing")
	}
	if strings.Contains(text, "Straffebestemmelser") {
		t.Error("the old heading survived the replacement")
	}
	if v.Section("§ 3") == nil || !strings.Contains(v.Section("§ 3").Display(), "fængsel") {
		t.Error("the provision under the heading must be untouched")
	}
	if err := v.Doc.Root.checkInvariant(""); err != nil {
		t.Errorf("derived tree broken: %v", err)
	}
}

// TestHeadingRepealThenProvisionRepeal walks the whole path on the chaptered law: the
// heading goes, § 3 goes, a passage in § 1 is deleted, and the heading repeal must not
// have eaten § 3 on the way.
func TestHeadingRepealThenProvisionRepeal(t *testing.T) {
	a := writeArchive(t, map[string]string{
		"lta/2026/293.xml": fixtureChapteredLaw,
		"lta/2026/389.xml": fixtureChapterAmender,
	})
	v, err := a.LawAsOf("B202600029305", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Unresolved) != 0 {
		t.Fatalf("all three changes should apply: %+v", v.Unresolved)
	}
	if len(v.Applied) != 3 {
		t.Fatalf("applied = %d, want 3", len(v.Applied))
	}
	if v.Section("§ 3") != nil {
		t.Error("§ 3 should be repealed")
	}
	if v.Section("§ 1") == nil {
		t.Fatal("§ 1 must survive")
	}
	text := v.Doc.Root.PlainText()
	if strings.Contains(text, "Straffebestemmelser") {
		t.Error("the repealed heading is still present")
	}
	if !strings.Contains(text, "Definitioner") {
		t.Error("the untouched heading should remain")
	}
	if strings.Contains(text, "forebygge smitte") {
		t.Error("the udgår passage is still present")
	}
	if err := v.Doc.Root.checkInvariant(""); err != nil {
		t.Errorf("derived tree broken: %v", err)
	}
}
