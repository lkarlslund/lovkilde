package lexdania

import (
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// fixtureHandledLaw exercises the shapes the whole-archive scan reported as
// unappliable: a document title, a preamble, afsnit numbered in roman numerals, a word
// that an "overalt i loven" instruction will target, and a subsection count a
// renumbering instruction can be checked against.
const fixtureHandledLaw = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <DocumentType>BEK H#LOKDOK04</DocumentType>
    <Rank>B</Rank>
    <AccessionNumber>B202600029311</AccessionNumber>
    <DocumentId>CM000911</DocumentId>
    <UniqueDocumentId>91111</UniqueDocumentId>
    <DocumentTitle>Bekendtgørelse om fiskeri</DocumentTitle>
    <Year>2026</Year>
    <DiesSigni>2026-03-05</DiesSigni>
    <Status>Current</Status>
    <Number>293</Number>
    <Ministry>Fødevareministeriet</Ministry>
    <Change id="change_1" />
    <Ref_Accn REFid="change_1">B202600038911</Ref_Accn>
    <Ref_Af REFid="change_1">2026-07-01</Ref_Af>
    <Ref_Text REFid="change_1">Bekendtgørelse om ændring af bekendtgørelse om fiskeri</Ref_Text>
  </Meta>
  <TitelGruppe>
    <Titel><Linea><Char>Bekendtgørelse om fiskeri efter torsk</Char></Linea></Titel>
  </TitelGruppe>
  <DokumentIndhold>
    <Indledning>
      <Linea><Char>Bekendtgørelsen er en følge af forordning (EF) nr. 1234/2007.</Char></Linea>
    </Indledning>
    <Afsnit>
      <Explicatus>Afsnit I</Explicatus>
      <Rubrica><Linea><Char>Almindelige regler</Char></Linea></Rubrica>
      <Paragraf>
        <Explicatus>§ 1.</Explicatus>
        <Stk><Exitus><Linea><Char>Fiskeri fra skib er tilladt fra 1. marts.</Char></Linea></Exitus></Stk>
        <Stk><Explicatus>Stk. 2.</Explicatus><Exitus><Linea><Char>Skibet skal være mærket med fiskerinummer.</Char></Linea></Exitus></Stk>
        <Stk><Explicatus>Stk. 3.</Explicatus><Exitus><Linea><Char>Fiskeri skal anmeldes, når skibet bruges i kystfarvand.</Char></Linea></Exitus></Stk>
      </Paragraf>
    </Afsnit>
    <Afsnit>
      <Explicatus>Afsnit II</Explicatus>
      <Rubrica><Linea><Char>Særlige regler</Char></Linea></Rubrica>
      <Paragraf>
        <Explicatus>§ 2.</Explicatus>
        <Stk><Exitus><Linea><Char>Fiskeri i gydetiden er forbudt.</Char></Linea></Exitus></Stk>
      </Paragraf>
    </Afsnit>
  </DokumentIndhold>
</Dokument>`

// fixtureHandledAmender carries one change of each newly handled kind, including the
// parenthetical note that is not an edit and the global replace whose § -free wording
// used to make it look like one.
const fixtureHandledAmender = `<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta>
    <DocumentType>BEK Æ#LOKDOK05</DocumentType>
    <Rank>B</Rank>
    <AccessionNumber>B202600038911</AccessionNumber>
    <DocumentId>CM000912</DocumentId>
    <UniqueDocumentId>91112</UniqueDocumentId>
    <DocumentTitle>Bekendtgørelse om ændring af bekendtgørelse om fiskeri</DocumentTitle>
    <Year>2026</Year>
    <DiesSigni>2026-06-10</DiesSigni>
    <Status>Current</Status>
    <Number>389</Number>
    <Ministry>Fødevareministeriet</Ministry>
  </Meta>
  <DokumentIndhold>
    <AendringCentreretParagraf localId="1">
      <Explicatus>§ 1</Explicatus>
      <Exitus><Linea><Char>I bekendtgørelse nr. 293 af 5. marts 2026 om fiskeri foretages følgende ændringer:</Char></Linea></Exitus>
      <AendringsNummer>
        <Explicatus>1.</Explicatus>
        <Aendring>
          <AendringDefinition><Exitus><Linea><Char>Lovens titel affattes således:</Char></Linea></Exitus></AendringDefinition>
          <AendringAktion>
            <AendringNyTekst>
              <Titel><Linea><Char>Bekendtgørelse om fiskeri i Østersøen</Char></Linea></Titel>
            </AendringNyTekst>
          </AendringAktion>
        </Aendring>
      </AendringsNummer>
      <AendringsNummer>
        <Explicatus>2.</Explicatus>
        <Aendring>
          <AendringDefinition><Exitus><Linea><Char>Indledningen affattes således:</Char></Linea></Exitus></AendringDefinition>
          <AendringAktion>
            <AendringNyTekst>
              <Indledning><Linea><Char>Bekendtgørelsen er en følge af forordning (EU) 2024/1770.</Char></Linea></Indledning>
            </AendringNyTekst>
          </AendringAktion>
        </Aendring>
      </AendringsNummer>
      <AendringsNummer>
        <Explicatus>3.</Explicatus>
        <Aendring>
          <AendringDefinition><Exitus><Linea><Char>Afsnit II ophæves.</Char></Linea></Exitus></AendringDefinition>
          <AendringAktion><AendringNyTekst /></AendringAktion>
        </Aendring>
      </AendringsNummer>
      <AendringsNummer>
        <Explicatus>4.</Explicatus>
        <Aendring>
          <AendringDefinition><Exitus><Linea><Char>Overalt i loven ændres »skib« til: »fartøj«.</Char></Linea></Exitus></AendringDefinition>
          <AendringAktion><AendringNyTekst /></AendringAktion>
        </Aendring>
      </AendringsNummer>
      <AendringsNummer>
        <Explicatus>5.</Explicatus>
        <Aendring>
          <AendringDefinition><Exitus><Linea><Char>(Sættes ikke i kraft for Færøerne)</Char></Linea></Exitus></AendringDefinition>
          <AendringAktion><AendringNyTekst /></AendringAktion>
        </Aendring>
      </AendringsNummer>
      <AendringsNummer>
        <Explicatus>6.</Explicatus>
        <Aendring>
          <AendringDefinition><Exitus><Linea><Char>I § 1, stk. 1, indsættes et nyt stk. 1:</Char></Linea></Exitus></AendringDefinition>
          <AendringAktion>
            <AendringNyTekst>
              <Stk><Exitus><Linea><Char>Fiskeri efter forbudte arter er ulovligt.</Char></Linea></Exitus></Stk>
            </AendringNyTekst>
          </AendringAktion>
        </Aendring>
      </AendringsNummer>
      <!-- The renumbering that follows names no §: 1,645 archive changes are this shape,
           and they are only evaluable because the block they sit in has a running subject. -->
      <AendringsNummer>
        <Explicatus>7.</Explicatus>
        <Aendring>
          <AendringDefinition><Exitus><Linea><Char>Stk. 3 bliver herefter stk. 4.</Char></Linea></Exitus></AendringDefinition>
          <AendringAktion><AendringNyTekst /></AendringAktion>
        </Aendring>
      </AendringsNummer>
      <!-- A word edit inside the title, the shape whose verb used to be unreadable. -->
      <AendringsNummer>
        <Explicatus>8.</Explicatus>
        <Aendring>
          <AendringDefinition><Exitus><Linea><Char>I lovens titel ændres »fiskeri« til: »fiskeri og havbrug«.</Char></Linea></Exitus></AendringDefinition>
          <AendringAktion><AendringNyTekst /></AendringAktion>
        </Aendring>
      </AendringsNummer>
    </AendringCentreretParagraf>
  </DokumentIndhold>
</Dokument>`

// TestNewAddressUnitsApply drives every newly handled shape through the real path: title,
// preamble, a roman-numeral afsnit repeal, a global replace, a note that asks for no text
// change, and a renumbering that inherits the § of the change before it.
func TestNewAddressUnitsApply(t *testing.T) {
	a := writeArchive(t, map[string]string{
		"lta/2026/293.xml": fixtureHandledLaw,
		"lta/2026/389.xml": fixtureHandledAmender,
	})
	v, err := a.LawAsOf("B202600029311", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Unresolved) != 0 {
		t.Fatalf("all eight changes should apply: %+v", v.Unresolved)
	}
	if len(v.Applied) != 8 {
		t.Fatalf("applied = %d, want 8: %+v", len(v.Applied), v.Applied)
	}
	text := v.Doc.Root.PlainText()
	wants := []string{
		"i Østersøen", // document title rewritten
		"2024/1770",   // preamble rewritten
		// The global replace hit the standalone word and stopped there: matching is
		// case-sensitive, so the case that matters is a lowercase longer word.
		"fra fartøj",
		"når skibet bruges",
		"Almindelige", // afsnit I untouched
	}
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("expected %q in the consolidated text", w)
		}
	}
	for _, gone := range []string{
		"fartøjet",       // what a word-blind global replace produces
		"efter torsk",    // old title
		"1234/2007",      // old preamble
		"gydetiden",      // afsnit II repealed
		"Særlige regler", // its heading goes with it
	} {
		if strings.Contains(text, gone) {
			t.Errorf("%q should be gone", gone)
		}
	}
	if err := v.Doc.Root.checkInvariant(""); err != nil {
		t.Errorf("derived tree broken: %v", err)
	}
}

// TestRomanAfsnitNumbering guards the numbering itself: the elements carry id="Afs1" and
// no localId, and print "Afsnit I", so nothing produced a number before and every
// reference to "afsnit II" had nothing to match.
func TestRomanAfsnitNumbering(t *testing.T) {
	law := mustParse(t, fixtureHandledLaw)
	var nums []string
	for _, s := range law.Sections() {
		if s.Kind == "afsnit" {
			nums = append(nums, s.Number)
		}
	}
	if strings.Join(nums, ",") != "1,2" {
		t.Errorf("afsnit numbers = %v, want [1 2]", nums)
	}
	for def, want := range map[string]int{
		"Afsnit IX ophæves.":                           9,
		"Efter afsnit VIII indsættes et nyt afsnit.":   8,
		"Overskriften til afsnit II affattes således:": 2,
	} {
		if got := ParseAddress(def).Afsnit; got != want {
			t.Errorf("ParseAddress(%q).Afsnit = %d, want %d", def, got, want)
		}
	}
	// The rejection matters as much as the conversion: Danish words are made of roman
	// letters, and a naive sum would number a section from the word "id".
	for token, want := range map[string]string{
		"i": "1", "iv": "4", "ix": "9", "viii": "8", "xxiv": "24",
		"iiii": "", "id": "", "ic": "", "vid": "", "": "",
	} {
		if got := lexdoc.RomanToNumber(token); got != want {
			t.Errorf("lexdoc.RomanToNumber(%q) = %q, want %q", token, got, want)
		}
	}
}

// TestRenumberIsCheckedNotExecuted is the whole point of EditRenumber: subsection
// numbering is positional, so the instruction is normally already true and executing it
// again would be wrong. When it is not true, the structural change it depended on is
// missing and the instruction has to say so.
func TestRenumberIsCheckedNotExecuted(t *testing.T) {
	law := mustParse(t, fixtureHandledLaw)
	v := &View{Doc: law.Clone()}
	v.Doc.Meta = law.Meta

	satisfied := Edit{
		Number:     "6.",
		Kind:       EditRenumber,
		Definition: "Stk. 2 bliver herefter stk. 3.",
		Preceding:  ParseAddress("§ 1, stk. 2"),
	}
	v.applyOne(satisfied)
	if len(v.Applied) != 1 {
		t.Fatalf("a renumbering the insertion already achieved should be recorded: %+v", v.Unresolved)
	}
	if !strings.Contains(v.Applied[0].Note, "already has 3 stk") {
		t.Errorf("the note should say what it checked, got %q", v.Applied[0].Note)
	}
	// The printed number is still there: this is the assertion that catches an
	// implementation that splices "stk. 2" to "stk. 3" in the text instead of checking
	// that the numbering already says so.
	if text := v.Doc.Root.PlainText(); !strings.Contains(text, "Stk. 2.") || !strings.Contains(text, "Fiskeri fra skib er tilladt") {
		t.Errorf("a renumbering must not change any words:\n%s", text)
	}

	impossible := Edit{
		Number:     "7.",
		Kind:       EditRenumber,
		Definition: "Stk. 2 bliver herefter stk. 5.",
		Preceding:  ParseAddress("§ 1"),
	}
	v.applyOne(impossible)
	if len(v.Unresolved) != 1 {
		t.Fatalf("renumbering to a number the text cannot hold must be refused: %+v", v.Applied)
	}
	if !strings.Contains(v.Unresolved[0].Reason, "has 3 stk") {
		t.Errorf("the refusal should report the count it found, got %q", v.Unresolved[0].Reason)
	}

	orphan := Edit{Number: "8.", Kind: EditRenumber, Definition: "Stk. 2 bliver herefter stk. 3."}
	v.applyOne(orphan)
	if len(v.Unresolved) != 2 {
		t.Fatal("a renumbering with no § of its own and none before it cannot be placed")
	}
	if !strings.Contains(v.Unresolved[1].Reason, "no earlier change") {
		t.Errorf("the refusal should name the missing context, got %q", v.Unresolved[1].Reason)
	}
}

// TestClassifiesTheNewKinds pins the classifier decisions, including the one that must
// not happen: a change that inserts something *and* mentions the renumbering is an
// insertion, because the renumbering is the consequence of the insertion.
func TestClassifiesTheNewKinds(t *testing.T) {
	for def, want := range map[string]EditKind{
		"(Sættes ikke i kraft for Færøerne)":           EditNonOperative,
		"(Ophævet)":                                    EditNonOperative,
		"Overalt i loven ændres »skib« til: »fartøj«.": EditReplaceEverywhere,
		"Stk. 2 bliver herefter stk. 3.":               EditRenumber,
		"Stk. 2 og 3 bliver herefter nr. 3 og 4.":      EditRenumber,
		"§ 5 ophæves.":                                 EditDeleteProvision,
	} {
		e := Edit{Definition: def}
		e.Address = ParseAddress(def)
		if got := classifyEdit(def, def, nil, &e); got != want {
			t.Errorf("classifyEdit(%q) = %q, want %q", def, got, want)
		}
	}
	e := Edit{Definition: "I § 5 indsættes et nyt stk. 1, hvorefter det hidtidige stk. 1 bliver stk. 2."}
	e.Address = ParseAddress(e.Definition)
	if got := classifyEdit(e.Definition, e.Definition, nil, &e); got == EditRenumber {
		t.Errorf("an insertion that mentions the renumbering it causes must stay an insertion, got %q", got)
	}
	// A real edit with a parenthetical afterthought is still an edit.
	e2 := Edit{Definition: "§ 7 ophæves (Sættes ikke i kraft for Grønland)."}
	e2.Address = ParseAddress(e2.Definition)
	if got := classifyEdit(e2.Definition, e2.Definition, nil, &e2); got == EditNonOperative {
		t.Error("a repeal with a parenthetical note is still a repeal")
	}
}

// TestTitleEditsSeePastASCIIWordBoundaries pins a boundary bug: Go's \b is ASCII-only,
// so \bændres cannot match anything, and every "I lovens titel ændres »X« til: »Y«" —
// the commonest way a Danish act edits its own title — silently produced an
// unrecognised address while the regex looked correct. The negative case matters just as
// much: "forændres" is a different verb and must not be read as this one.
func TestTitleEditsSeePastASCIIWordBoundaries(t *testing.T) {
	for def, want := range map[string]bool{
		"I lovens titel ændres »fiskeri« til: »fiskeri og havbrug«.": true,
		"I lovens titel udgår »privat«.":                             true,
		"Bekendtgørelsens titel affattes således:":                   true,
		"I lovens titel erstattes »X« af »Y«.":                       true,
		"Beskrivelsen forændres af vind og vejr.":                    false,
		"§ 5, stk. 1, ændres således:":                               false,
	} {
		if got := ParseAddress(def).DocTitle; got != want {
			t.Errorf("ParseAddress(%q).DocTitle = %v, want %v", def, got, want)
		}
	}

	// End to end, because the branch also has to reach the right text: the title was
	// already rewritten wholesale by change 1, so this one edits a word inside the title
	// that change 1 produced, and nothing else in the act may move.
	a := writeArchive(t, map[string]string{
		"lta/2026/293.xml": fixtureHandledLaw,
		"lta/2026/389.xml": fixtureHandledAmender,
	})
	v, err := a.LawAsOf("B202600029311", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Unresolved) != 0 {
		t.Fatalf("the title word edit should apply: %+v", v.Unresolved)
	}
	text := v.Doc.Root.PlainText()
	if !strings.Contains(text, "fiskeri og havbrug i Østersøen") {
		t.Errorf("title should carry both edits in order, got:\n%s", text)
	}
	if !strings.Contains(text, "Fiskeri fra fartøj er tilladt") {
		t.Error("the body must be untouched by a title edit")
	}
}

// changes ask for a footnote *to* the title, and rewriting the title instead would be a
// confident corruption of the wrong text while the footnote model does not exist.
func TestFootnoteToTitleIsNotTheTitle(t *testing.T) {
	if a := ParseAddress("Fodnoten til lovens titel affattes således:"); a.DocTitle {
		t.Error("a footnote attached to the title is not the title")
	}
	if a := ParseAddress("Lovens titel affattes således:"); !a.DocTitle {
		t.Error("the document title should be addressable")
	}
}

// TestEverywhereReplaceDoesNotTakeASectionFromItsOwnQuotation: the § inside the quoted
// passage is the content being replaced, and resolving it would splice the change into
// one provision and leave every other occurrence stale.
func TestEverywhereReplaceDoesNotTakeASectionFromItsOwnQuotation(t *testing.T) {
	a := ParseAddress("Overalt i loven ændres »§ 5, stk. 2« til: »§ 5, stk. 3«.")
	if !a.Everywhere || !a.Recognised {
		t.Fatalf("the instruction should be recognised as a global replace: %+v", a)
	}
	if a.Paragraph != "" {
		t.Errorf("a global replace must not adopt a § from its own quoted text, got %q", a.Paragraph)
	}
}
