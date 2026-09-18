package lexdania

import (
	"strings"
	"testing"
)

// The nesting pass is the part of the reader whose failure mode is silent: a
// paragraph that keeps its prose flat still renders the right text, but Sections()
// finds no stykker in it, so "§ 1, stk. 3" resolves to nothing and the amendment is
// reported as aimed at a provision that does not exist. Every test below therefore
// asks either what Sections() can address or what order the text lands in -- not
// merely whether the words are present.

// nestedPayload is a modern payload with three stykker, the shape that dominates the
// archive: the paragraph carries its own prose, and each later stykke is a sibling.
const nestedPayload = `<p class="Paragraf" id="P1"><span class="ParagrafNr" id="Par1_Stk1">§ 1.</span> Første stykke.</p>` +
	`<p class="Stk2"><span class="StkNr" id="Par1_Stk2">Stk. 2.</span> Andet stykke.</p>` +
	`<p class="Stk2"><span class="StkNr" id="Par1_Stk3">Stk. 3.</span> Tredje stykke.</p>`

func TestNestingMakesStykkerAddressable(t *testing.T) {
	body := parseBodyFixture(t, nestedPayload)

	secs := sectionsIn(body.Root)
	for _, addr := range []string{"§ 1", "§ 1, stk. 2", "§ 1, stk. 3"} {
		sec := Find(secs, ParseAddress(addr))
		if sec == nil {
			t.Errorf("%s resolved to nothing; sections=%s tree=%s", addr,
				formatAddresses(secs), dumpNode(body.Root, ""))
			continue
		}
		if sec.Kind != "stk" && addr != "§ 1" {
			t.Errorf("%s resolved to kind %q, want stk", addr, sec.Kind)
		}
	}
	// The numbering is positional in the canonical tree too (Stk almost never carries
	// a localId), so the order of the children is the numbering and has to be right.
	stk := Find(secs, ParseAddress("§ 1, stk. 3"))
	if stk != nil && !strings.Contains(stk.Display(), "Tredje stykke") {
		t.Errorf("stk. 3 is %q", stk.Display())
	}
}

func TestNestingKeepsTheNumberOnTheParagraph(t *testing.T) {
	body := parseBodyFixture(t, nestedPayload)

	par := findElem(body.Root, "Paragraf")
	if par == nil {
		t.Fatalf("no Paragraf: %s", dumpNode(body.Root, ""))
	}
	if marker := par.Elem("Explicatus"); marker == nil || !strings.Contains(marker.Text(), "§ 1.") {
		t.Errorf("printed number is not on the paragraph: %s", dumpNode(par, ""))
	}
	// Canonical paragraph elements hold no text of their own -- measured across the
	// paired corpus, 0 of 3,629 do -- so a payload tree that leaves prose directly on
	// the paragraph has not finished being read.
	for _, run := range par.Runs {
		if strings.TrimSpace(run) != "" {
			t.Errorf("paragraph holds loose text %q instead of a stykke: %s", run, dumpNode(par, ""))
		}
	}
	stykker := par.Elems
	if len(stykker) != 4 { // Explicatus + 3 stykker
		t.Fatalf("paragraph has %d children, want 4: %s", len(stykker), dumpNode(par, ""))
	}
	for _, c := range stykker[1:] {
		if c.Name != "Stk" {
			t.Errorf("child of Paragraf is %s, want Stk: %s", c.Name, dumpNode(par, ""))
		}
	}
}

func TestNestingWrapsDespiteAnAnchorBeforeTheNumber(t *testing.T) {
	// The site emits an empty anchor with the paragraph's own id before the number
	// span. Treating only index 0 as the marker meant no first stykke was built for
	// those paragraphs, their prose became unaddressable, and the payload's own
	// p.Stk2 elements became stk. 1 -- shifting every later reference by one.
	body := parseBodyFixture(t, `<p class="Paragraf" id="P1"><span id="P1"></span><span class="ParagrafNr" id="Par1_Stk1">§ 1.</span> Tekst.</p>`+
		`<p class="Stk2"><span class="StkNr">Stk. 2.</span> Mere tekst.</p>`)

	secs := sectionsIn(body.Root)
	sec := Find(secs, ParseAddress("§ 1, stk. 2"))
	if sec == nil {
		t.Fatalf("stk. 2 unresolvable with an anchor before the number: %s", dumpNode(body.Root, ""))
	}
	if !strings.Contains(sec.Display(), "Mere tekst") {
		t.Errorf("stk. 2 is %q, want the payload's second stykke", sec.Display())
	}
	first := Find(secs, ParseAddress("§ 1, stk. 1"))
	if first == nil || !strings.Contains(first.Display(), "Tekst.") {
		t.Errorf("the paragraph's own prose is not reachable as stk. 1: %s", dumpNode(body.Root, ""))
	}
}

func TestNestingInventsNothingForUnnumberedProse(t *testing.T) {
	// An unnumbered p.Paragraf is preamble prose. In amending acts this is the
	// sentence naming the law being changed, and amendments.go reads a paragraph's
	// own text for it, so wrapping it in a stykke would lose the target hint.
	body := parseBodyFixture(t, `<p class="Paragraf">I lov om landsplanlægning foretages følgende ændring:</p>`)

	par := findElem(body.Root, "Paragraf")
	if par == nil {
		t.Fatalf("no Paragraf: %s", dumpNode(body.Root, ""))
	}
	if len(par.Elems) != 0 {
		t.Errorf("preamble prose was nested into %v: %s", elemNames(par), dumpNode(par, ""))
	}
	if !strings.Contains(docText(par), "følgende ændring") {
		t.Errorf("preamble text lost: %q", docText(par))
	}
}

func TestNestingAbsorbsProseThatArrivesAfterTheNumber(t *testing.T) {
	// A paragraph whose number is empty-handed is common: the prose follows in its
	// own paragraph. Wrapping only on the paragraph's own prose left those items and
	// sentences attached to the paragraph, where Sections() cannot see them.
	body := parseBodyFixture(t, `<p class="Paragraf"><span class="ParagrafNr">§ 2.</span></p>`+
		`<p class="para">Brødteksten hører til paragrafen.</p>`)

	secs := sectionsIn(body.Root)
	sec := Find(secs, ParseAddress("§ 2, stk. 1"))
	if sec == nil {
		t.Fatalf("prose arriving after the number produced no stykke: %s", dumpNode(body.Root, ""))
	}
	if !strings.Contains(sec.Display(), "Brødteksten") {
		t.Errorf("stk. 1 is %q", sec.Display())
	}
}

func TestNestingKeepsItemsAfterTheirOwnSentence(t *testing.T) {
	// The bug this guards: absorbing the list into the stykke and the sentence into
	// the paragraph put the list items ahead of the sentence introducing them,
	// because the stykke precedes that sentence inside the paragraph. Different
	// depths must not be filled out of order.
	body := parseBodyFixture(t, `<p class="Paragraf"><span class="ParagrafNr">§ 30</span></p>`+
		`<p class="para">Bevillingshaveren skal være uberettiget til:</p>`+
		`<ul class="PLAIN-LIST"><li>a) at bortforpagte Banens Drift;</li><li>b) at afhænde grundejendom;</li></ul>`)

	want := "§ 30 Bevillingshaveren skal være uberettiget til: a) at bortforpagte Banens Drift; b) at afhænde grundejendom;"
	if got := treeText(body.Root); got != want {
		t.Errorf("text reordered\ngot:  %q\nwant: %q", got, want)
	}
	// The items also have to sit where a numbered item is looked for: inside a stykke.
	stk := findElem(body.Root, "Stk")
	if stk == nil || len(stk.FindNamed("Indentatio")) != 2 {
		t.Errorf("items are not inside a stykke: %s", dumpNode(body.Root, ""))
	}
}

func TestNestingDoesNotStealAHeading(t *testing.T) {
	// Only the elements measured inside a paragraph may be absorbed. A heading
	// belongs to whatever follows it, and a heading moved into the paragraph before
	// it is invisible in the text while being wrong in the structure.
	body := parseBodyFixture(t, `<p class="Paragraf"><span class="ParagrafNr">§ 1.</span> Tekst.</p>`+
		`<p class="KapitelOverskrift2">Mellemliggende overskrift</p>`+
		`<p class="Stk2"><span class="StkNr">Stk. 2.</span> Senere tekst.</p>`)

	par := findElem(body.Root, "Paragraf")
	if got := treeText(par); strings.Contains(got, "Mellemliggende") {
		t.Errorf("heading absorbed into the paragraph: %q", got)
	}
	if !strings.Contains(treeText(body.Root), "Mellemliggende overskrift Stk. 2. Senere tekst") {
		t.Errorf("content after the barrier was reordered or lost: %q", treeText(body.Root))
	}
}

func TestNestingAttachesAnInsertedListToItsChange(t *testing.T) {
	// In an amending act the list is the text being inserted, and canonical keeps it
	// inside the change block rather than beside it.
	body := parseBodyFixture(t, `<p class="Paragraf">I lov X foretages følgende ændring:</p>`+
		`<p class="AendringMedNummer"><span class="AendringNr">1.</span> I § 7 indsættes:</p>`+
		`<p class="Liste1"><span class="Liste1Nr">7)</span> nyt nummer</p>`)

	par := findElem(body.Root, "Paragraf")
	change := par.Elem("AendringsNummer")
	if change == nil {
		t.Fatalf("change block not attached to the paragraph: %s", dumpNode(body.Root, ""))
	}
	if items := change.FindNamed("Indentatio"); len(items) != 1 {
		t.Errorf("inserted list not inside the change block: %s", dumpNode(change, ""))
	}
	if got, want := treeText(body.Root), "I lov X foretages følgende ændring: 1. I § 7 indsættes: 7) nyt nummer"; got != want {
		t.Errorf("text reordered\ngot:  %q\nwant: %q", got, want)
	}
}

func TestNestingAdoptsTheStykkeIDWithoutDuplicatingIt(t *testing.T) {
	body := parseBodyFixture(t, nestedPayload)

	par := findElem(body.Root, "Paragraf")
	stk := par.FindNamed("Stk")
	if len(stk) != 3 {
		t.Fatalf("got %d stykker, want 3", len(stk))
	}
	if got := stk[0].ID(); got != "Par1_Stk1" {
		t.Errorf("first stykke id = %q, want the canonical id the payload put on the number span", got)
	}
	// One id, one node: leaving it on the marker as well makes id lookups ambiguous.
	if marker := par.Elem("Explicatus"); marker != nil && marker.ID() == "Par1_Stk1" {
		t.Errorf("the id stayed on the marker too, so it names two nodes")
	}
}

func TestNestingCountsWhatItMoved(t *testing.T) {
	body := parseBodyFixture(t, nestedPayload)
	if body.WrappedParagraphs() != 1 {
		t.Errorf("WrappedParagraphs = %d, want 1", body.WrappedParagraphs())
	}
	if body.Absorbed() != 2 {
		t.Errorf("Absorbed = %d, want the two payload stykker that moved", body.Absorbed())
	}

	quiet := parseBodyFixture(t, `<p class="para">Prosa uden paragrafer.</p>`)
	if quiet.Absorbed() != 0 || quiet.WrappedParagraphs() != 0 {
		t.Errorf("a payload with nothing to nest reported %d absorbed, %d wrapped",
			quiet.Absorbed(), quiet.WrappedParagraphs())
	}
}

func TestNestingLeavesAParagraphWithOnlyANumberAlone(t *testing.T) {
	// A number with nothing around it must not gain an empty stykke: the section list
	// would then contain a provision with no text, which consolidation could edit.
	body := parseBodyFixture(t, `<p class="Paragraf"><span class="ParagrafNr">§ 5.</span></p>`)

	if got := len(findElem(body.Root, "Paragraf").FindNamed("Stk")); got != 0 {
		t.Errorf("empty paragraph gained %d stykker: %s", got, dumpNode(body.Root, ""))
	}
	if body.Absorbed() != 0 {
		t.Errorf("nothing was absorbable, counted %d", body.Absorbed())
	}
}

func formatAddresses(secs []*Section) string {
	parts := make([]string, 0, len(secs))
	for _, s := range secs {
		parts = append(parts, s.Kind+":"+sectionAddress(s))
	}
	return strings.Join(parts, " ")
}
