package lexdoc

import (
	"strings"
	"testing"
	"time"
)

// The rules here are the ones that decide what a consolidated law says, so each test is about a
// legal outcome rather than about a function's return value.

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestAsOfAppliesInDateOrderAndLeavesTheSourceAlone(t *testing.T) {
	c := sampleLaw()
	before := textOf(t, c, "§ 1")

	ninth := &Provision{Number: "9", Printed: "§ 9.", Segments: []Segment{{Text: "§ 9. Ny regel."}}}
	insert := Amendment{
		Source: "A", Effective: date(2020, 1, 1),
		Edits: []Edit{{Kind: EditInsertProvision,
			Address: ParseAddress("§ 8"), NewProvisions: []*Provision{ninth}}},
	}
	cite := Amendment{
		Source: "B", Effective: date(2024, 1, 1),
		Edits: []Edit{{Kind: EditReplaceText, Address: ParseAddress("§ 9"),
			Old: "Ny regel.", New: "Ændret regel."}},
	}

	// Deliberately out of order: the later act is listed first. Sorting by effective date is
	// what makes the citation find its provision, because § 9 does not exist until A.
	view, out := c.AsOf([]Amendment{cite, insert}, date(2025, 6, 1))
	if len(out.Unresolved) != 0 {
		t.Fatalf("a chain applied in date order should refuse nothing: %+v", out.Unresolved)
	}
	if got := textOf(t, view, "§ 9"); got != "§ 9. Ændret regel." {
		t.Errorf("§ 9 after the chain = %q", got)
	}
	if got := textOf(t, c, "§ 1"); got != before {
		t.Errorf("building an as-of view edited the document it read: %q", got)
	}
	if _, ok := c.Find(ParseAddress("§ 9")); ok {
		t.Error("the inserted provision appeared in the document rather than in the view")
	}

	// A date before the second act must not show its change, and must say so by not applying it.
	_, out2 := c.AsOf([]Amendment{cite, insert}, date(2021, 6, 1))
	if len(out2.Applied) != 1 || len(out2.Unresolved) != 0 {
		t.Errorf("as of 2021: applied=%d unresolved=%d, want the 2020 act only", len(out2.Applied), len(out2.Unresolved))
	}
	view2, _ := c.AsOf([]Amendment{cite, insert}, date(2021, 6, 1))
	if got := textOf(t, view2, "§ 9"); got != "§ 9. Ny regel." {
		t.Errorf("§ 9 as of 2021 = %q", got)
	}
}

func TestStykkeAddressCannotBeSatisfiedInAnotherStykke(t *testing.T) {
	c := sampleLaw()
	// "Første stykke." is in stk. 1. An edit addressed to stk. 2 that quotes it has to be
	// refused: the tree reader searched the whole provision, which is how an edit meant for one
	// subsection could satisfy itself in another and report success.
	_, out := c.AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditReplaceText, Address: ParseAddress("§ 2, stk. 2"), Old: "Første stykke.", New: "Udskiftet."},
	}}}, time.Time{})
	if len(out.Applied) != 0 || len(out.Unresolved) != 1 {
		t.Fatalf("an edit quoted from another subsection was not refused: applied=%+v unresolved=%+v", out.Applied, out.Unresolved)
	}
	if got := textOf(t, c, "§ 2"); strings.Contains(got, "Udskiftet") {
		t.Errorf("the refused edit still changed the text: %q", got)
	}

	// The same edit at the right address goes through, and only there.
	view, out2 := c.AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditReplaceText, Address: ParseAddress("§ 2, stk. 2"), Old: "Andet stykke.", New: "Udskiftet."},
	}}}, time.Time{})
	if len(out2.Applied) != 1 {
		t.Fatalf("the same edit at its own address was refused: %+v", out2.Unresolved)
	}
	if got := textOf(t, view, "§ 2"); !strings.Contains(got, "Udskiftet") || !strings.Contains(got, "Første stykke") {
		t.Errorf("§ 2 after the edit = %q, want the second subsection changed and the first left alone", got)
	}
}

func TestRepealOfASubsectionShiftsTheNumbering(t *testing.T) {
	view, out := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditDeleteProvision, Address: ParseAddress("§ 2, stk. 2")},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("repealing a subsection that exists was refused: %+v", out.Unresolved)
	}
	p := mustFindProvision(t, view, "§ 2")
	if len(p.Stykker) != 2 {
		t.Fatalf("stykker after the repeal = %d", len(p.Stykker))
	}
	// The printed law renumbers: what was stk. 3 is cited as stk. 2, not left as a gap.
	if got := p.SpanText(p.Stykke(2).Span); got != "Tredje stykke." {
		t.Errorf("new stk. 2 = %q, want the former stk. 3", got)
	}
}

func TestInsertionAnchorsWhereTheAddressedNumberSat(t *testing.T) {
	// The common Danish shape: the same act repeals § 2 and inserts a new provision addressed
	// to it. Refusing would lose the inserted law; anchoring it at the address would put it
	// nowhere. It goes where the number used to sit, with the reason recorded.
	inserted := &Provision{Number: "2A", Printed: "§ 2 a.", Segments: []Segment{{Text: "§ 2 a. Ny bestemmelse."}}}
	chain := []Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditDeleteProvision, Address: ParseAddress("§ 2")},
		{Kind: EditInsertProvision, Address: ParseAddress("§ 2"), NewProvisions: []*Provision{inserted}},
	}}}
	view, out := sampleLaw().AsOf(chain, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("the insertion was refused: %+v", out.Unresolved)
	}
	applied := out.Applied[1]
	if !strings.Contains(applied.Note, "positionally") {
		t.Errorf("the positional anchoring was not recorded: %q", applied.Note)
	}
	if got := textOf(t, view, "§ 2 a"); !strings.Contains(got, "Ny bestemmelse") {
		t.Errorf("the inserted provision is not addressable as § 2 a: %q", got)
	}
}

func TestRenumberingIsCheckedRatherThanExecuted(t *testing.T) {
	// § 2 has three stykker. "the former stk. 2 becomes stk. 3" is already true once the
	// insertion that prompted it has been applied, so it is recorded as satisfied rather than
	// carried out again — carrying it out again is what would corrupt the numbering.
	_, ok := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditRenumber, Address: ParseAddress("§ 2"),
			Definition: "Det hidtidige stk. 2 bliver herefter stk. 3."},
	}}}, time.Time{})
	if len(ok.Applied) != 1 || len(ok.Unresolved) != 0 {
		t.Fatalf("a renumbering that is already true should be applied: %+v / %+v", ok.Applied, ok.Unresolved)
	}
	if !strings.Contains(ok.Applied[0].Note, "positional") {
		t.Errorf("the reason was not recorded: %q", ok.Applied[0].Note)
	}

	// A number the provision does not have means the structural change behind the instruction
	// is missing from this view. That is a fact about the chain, and it has to surface.
	_, bad := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditRenumber, Address: ParseAddress("§ 2"),
			Definition: "Det hidtidige stk. 2 bliver herefter stk. 7."},
	}}}, time.Time{})
	if len(bad.Unresolved) != 1 || !strings.Contains(bad.Unresolved[0].Reason, "structural") {
		t.Fatalf("an impossible renumbering should be refused with its cause: %+v", bad.Unresolved)
	}
}

func TestRenumberingInheritsItsSubjectFromTheChangeBeforeIt(t *testing.T) {
	// Danish change lists read as one sentence: the second change says "bliver herefter stk. 3"
	// and names no § at all. Without the inheritance it is a refusal; with it, an answer.
	_, out := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditInsertText, Address: ParseAddress("§ 2"), New: "Mere tekst."},
		{Kind: EditRenumber, Preceding: ParseAddress("§ 2"),
			Definition: "Det hidtidige stk. 2 bliver herefter stk. 3."},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("the inherited subject was not used: %+v", out.Unresolved)
	}

	// With nothing to inherit from, it is refused rather than applied wherever the numbers fit.
	_, orphan := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditRenumber, Definition: "Det hidtidige stk. 2 bliver herefter stk. 3."},
	}}}, time.Time{})
	if len(orphan.Unresolved) != 1 || !strings.Contains(orphan.Unresolved[0].Reason, "names no provision") {
		t.Fatalf("an orphan renumbering should be refused by name: %+v", orphan.Unresolved)
	}
}

func TestUnknownAddressIsRefusedRatherThanGuessed(t *testing.T) {
	view, out := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditDeleteProvision, Address: ParseAddress("§ 999")},
	}}}, time.Time{})
	if len(out.Applied) != 0 || len(out.Unresolved) != 1 {
		t.Fatalf("an edit with an unfindable address was applied somewhere: %+v", out.Applied)
	}
	if len(view.Provisions()) != len(sampleLaw().Provisions()) {
		t.Error("a refused deletion still removed a provision")
	}
}

func TestReplaceEverywhereChangesWordsNotLettersInsideWords(t *testing.T) {
	c := sampleLaw()
	c.Order = append(c.Order, Node{Provision: &Provision{Number: "7", Segments: []Segment{
		{Text: "§ 7. Skibet skal sejle.", Break: true},
		{Text: "Lasten på det gamle skib tages i beregning.", Break: true},
		{Text: "Et andet skib er undtaget."},
	}}})

	view, out := c.AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditReplaceEverywhere, Old: "»skib«", New: "fartøj"},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("the whole-document replace was refused: %+v", out.Unresolved)
	}
	if !strings.Contains(out.Applied[0].Where, "2 occurrences") {
		t.Errorf("occurrence count = %q, want the two standalone words", out.Applied[0].Where)
	}
	got := textOf(t, view, "§ 7")
	if !strings.Contains(got, "Skibet") {
		t.Errorf("the word inside a longer word was rewritten: %q", got)
	}
	if !strings.Contains(got, "gamle fartøj tages") || !strings.Contains(got, "andet fartøj er") {
		t.Errorf("the standalone words were not replaced: %q", got)
	}
}

func TestNonOperativeChangeIsRecordedRatherThanRefused(t *testing.T) {
	// "gælder ikke for Færøerne" asks for no edit to the text. Refusing it would fill the
	// unresolved list with changes that are not problems, which is how real problems stop being
	// noticed.
	_, out := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditNonOperative, Prose: "Loven gælder ikke for Færøerne."},
	}}}, time.Time{})
	if len(out.Applied) != 1 || !strings.Contains(out.Applied[0].Note, "validity") {
		t.Fatalf("a non-operative change should be applied with a note: %+v / %+v", out.Applied, out.Unresolved)
	}
}

func TestHeadingRewriteLeavesTheTextAlone(t *testing.T) {
	c := sampleLaw()
	before := textOf(t, c, "§ 3")
	view, out := c.AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditReplaceProvision, Address: Address{Paragraph: "3", Heading: true, Recognised: true},
			New: "Undtagelser"},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("the heading rewrite was refused: %+v", out.Unresolved)
	}
	p := mustFindProvision(t, view, "§ 3")
	if p.Heading != "Undtagelser" {
		t.Errorf("heading = %q", p.Heading)
	}
	if !strings.Contains(p.Text(), "Undtagelser") {
		t.Errorf("heading text not updated in the provision: %q", p.Text())
	}
	if !strings.Contains(p.Text(), "Der må ikke ske udledning") {
		t.Errorf("the provision text changed: %q, was %q", p.Text(), before)
	}
}

func TestReplaceProvisionKeepsItsNumber(t *testing.T) {
	replacement := &Provision{Number: "3", Segments: []Segment{{Text: "§ 3. Helt ny ordlyd."}}}
	view, out := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditReplaceProvision, Address: ParseAddress("§ 3"), NewProvisions: []*Provision{replacement}},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("the replacement was refused: %+v", out.Unresolved)
	}
	if got := textOf(t, view, "§ 3"); got != "§ 3. Helt ny ordlyd." {
		t.Errorf("§ 3 after the replacement = %q", got)
	}
	if _, ok := view.Find(ParseAddress("§ 3")); !ok {
		t.Error("the provision stopped being addressable as § 3")
	}
}

func TestRangeAddressReachesEachParagraph(t *testing.T) {
	c := sampleLaw()
	for i := 6; i <= 8; i++ {
		c.Order = append(c.Order, Node{Provision: &Provision{Number: itoa(i),
			Segments: []Segment{{Text: "Tekst med ordet gebyr."}}}})
	}
	view, out := c.AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditReplaceText, Address: Address{Paragraph: "6", RangeTo: "8", Recognised: true},
			Old: "gebyr", New: "afgift"},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("the range edit was refused: %+v", out.Unresolved)
	}
	if len(out.Applied) != 3 {
		t.Fatalf("applied = %d, want one per paragraph in the range", len(out.Applied))
	}
	for _, n := range []string{"§ 6", "§ 7", "§ 8"} {
		if got := textOf(t, view, n); !strings.Contains(got, "afgift") {
			t.Errorf("%s after the range edit = %q", n, got)
		}
	}
}

func TestInsertedSubsectionsNumberByPosition(t *testing.T) {
	// The common Danish case: "Efter stk. 1 indsættes følgende stk." The new subsection lands
	// after the addressed one, and everything behind it changes name — which is why the act's
	// own "det hidtidige stk. 2 bliver stk. 3" is usually already true when it is read.
	_, out := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditInsertProvision, Address: ParseAddress("§ 2, stk. 1"),
			NewStykker: [][]Segment{{{Text: " Stk. 2. Mellemværende regel.", Break: true}}}},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("inserting a subsection was refused: %+v", out.Unresolved)
	}
	view, _ := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditInsertProvision, Address: ParseAddress("§ 2, stk. 1"),
			NewStykker: [][]Segment{{{Text: " Stk. 2. Mellemværende regel.", Break: true}}}},
	}}}, time.Time{})
	p := mustFindProvision(t, view, "§ 2")
	if len(p.Stykker) != 4 {
		t.Fatalf("stykker = %d, want the original three plus one", len(p.Stykker))
	}
	if got := p.SpanText(p.Stykke(2).Span); !strings.Contains(got, "Mellemværende") {
		t.Errorf("new stk. 2 = %q", got)
	}
	if got := p.SpanText(p.Stykke(3).Span); !strings.Contains(got, "Andet stykke") {
		t.Errorf("the former stk. 2 should now be stk. 3, got %q", got)
	}
	if got := p.SpanText(p.Stykke(4).Span); !strings.Contains(got, "Tredje stykke") {
		t.Errorf("the former stk. 3 should now be stk. 4, got %q", got)
	}
	// The provision's own text shows the insertion in place, in reading order.
	if got := p.Text(); !strings.Contains(got, "Første stykke.") || !strings.Contains(got, "Mellemværende") || !strings.Contains(got, "Andet stykke") {
		t.Errorf("provision text after the insertion = %q", got)
	}
}

func TestSubsectionReplacedByTwoSubsectionsKeepsBoth(t *testing.T) {
	// The register delivers one change whose new wording is two subsections. Keeping only the
	// first is what the tree reader did, and it loses law without saying so.
	_, out := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditReplaceProvision, Address: ParseAddress("§ 2, stk. 2"),
			NewStykker: [][]Segment{
				{{Text: " Stk. 2. Første led.", Break: true}},
				{{Text: " Stk. 3. Andet led.", Break: true}},
			}},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("the replacement was refused: %+v", out.Unresolved)
	}
	view, _ := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditReplaceProvision, Address: ParseAddress("§ 2, stk. 2"),
			NewStykker: [][]Segment{
				{{Text: " Stk. 2. Første led.", Break: true}},
				{{Text: " Stk. 3. Andet led.", Break: true}},
			}},
	}}}, time.Time{})
	p := mustFindProvision(t, view, "§ 2")
	if len(p.Stykker) != 4 {
		t.Fatalf("stykker = %d, want 3 originals - 1 replaced + 2 new + 1 remaining", len(p.Stykker))
	}
	if got := p.SpanText(p.Stykke(2).Span); !strings.Contains(got, "Første led") {
		t.Errorf("stk. 2 = %q", got)
	}
	if got := p.SpanText(p.Stykke(3).Span); !strings.Contains(got, "Andet led") {
		t.Errorf("the second donated subsection is missing: stk. 3 = %q", got)
	}
	if got := p.SpanText(p.Stykke(4).Span); !strings.Contains(got, "Tredje stykke") {
		t.Errorf("the original last subsection moved wrongly: stk. 4 = %q", got)
	}
}

func TestProseOnlyInsertionLandsOnTheAddressedUnit(t *testing.T) {
	view, out := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditInsertProvision, Address: ParseAddress("§ 1"),
			NewProse: []string{"En ny linje.", "Endnu en linje."}},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("a prose insertion was refused: %+v", out.Unresolved)
	}
	got := textOf(t, view, "§ 1")
	if !strings.Contains(got, "En ny linje. Endnu en linje.") {
		t.Errorf("§ 1 after the prose insertion = %q", got)
	}
}

func TestAChangeTheModelCannotCarryOutIsRefusedByName(t *testing.T) {
	view, out := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditInsertProvision, Address: ParseAddress("§ 1"),
			Skip: "the change brings a chapter, which this model does not insert"},
	}}}, time.Time{})
	if len(out.Applied) != 0 || len(out.Unresolved) != 1 {
		t.Fatalf("an unsupported change should be refused: applied=%d unresolved=%d", len(out.Applied), len(out.Unresolved))
	}
	if !strings.Contains(out.Unresolved[0].Reason, "chapter") {
		t.Errorf("the reason was lost: %q", out.Unresolved[0].Reason)
	}
	if got := textOf(t, view, "§ 1"); got != textOf(t, sampleLaw(), "§ 1") {
		t.Errorf("a refused change still edited the text: %q", got)
	}
}

func TestInsertedSubsectionNumberedBeyondTheEndLandsLast(t *testing.T) {
	// The number in an insertion address belongs to the new text, not to an existing part:
	// "I § 115 indsættes som stk. 12-14" is offered against a § 115 with eleven subsections.
	// Looking "stk. 12" up there and refusing when it is absent lost real insertions on corpus
	// chains, reported as a provision that could not be found while sitting in the content.
	_, out := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditInsertProvision, Address: ParseAddress("I § 2 indsættes som stk. 4: Stk. 4. Ny regel."),
			NewStykker: [][]Segment{{{Text: " Stk. 4. Ny regel.", Break: true}}}},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("inserting at the end was refused: %+v", out.Unresolved)
	}
	view, _ := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditInsertProvision, Address: ParseAddress("I § 2 indsættes som stk. 4: Stk. 4. Ny regel."),
			NewStykker: [][]Segment{{{Text: " Stk. 4. Ny regel.", Break: true}}}},
	}}}, time.Time{})
	p := mustFindProvision(t, view, "§ 2")
	if len(p.Stykker) != 4 {
		t.Fatalf("stykker = %d, want three plus the appended one", len(p.Stykker))
	}
	if got := p.SpanText(p.Stykke(4).Span); !strings.Contains(got, "Ny regel") {
		t.Errorf("the new subsection should be last, got %q", got)
	}
	if got := p.SpanText(p.Stykke(3).Span); !strings.Contains(got, "Tredje stykke") {
		t.Errorf("the former last subsection moved, got %q", got)
	}
}

func TestInsertedItemTextLandsOnItsProvision(t *testing.T) {
	// "I § 2 indsættes som nr. 4" names an item that does not exist yet, so the address does not
	// resolve. The change still has a host — § 2 — and the prose it brings has to go there. This
	// path used to have no target, apply nothing, and record nothing.
	_, out := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditInsertProvision, Address: ParseAddress("I § 2 indsættes som nr. 4:"),
			NewProse: []string{"4) Ny oplysning."}},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("the insertion was refused: %+v", out.Unresolved)
	}
	if len(out.Applied) != 1 {
		t.Fatalf("applied = %d, want the change reported as applied exactly once", len(out.Applied))
	}
	view, _ := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditInsertProvision, Address: ParseAddress("I § 2 indsættes som nr. 4:"),
			NewProse: []string{"4) Ny oplysning."}},
	}}}, time.Time{})
	if got := textOf(t, view, "§ 2"); !strings.Contains(got, "Ny oplysning") {
		t.Errorf("inserted item text is not in the provision: %q", got)
	}
}

func TestReplacedSubsectionDoesNotKeepTheOldItems(t *testing.T) {
	// Replacing a subsection replaces its words, and the numbered items were words. Their spans
	// are positions in the provision's text, so keeping them after a replacement of different
	// length leaves them pointing into whatever follows — which is how a real consolidation ended
	// up showing § 27's replaced stk. 3 with four conditions belonging to stk. 4, 5 and 6.
	view, out := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditReplaceProvision, Address: ParseAddress("§ 2, stk. 1"),
			NewStykker: [][]Segment{{{Text: " Stk. 1. Kun én regel.", Break: true}}}},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("the replacement was refused: %+v", out.Unresolved)
	}
	p := mustFindProvision(t, view, "§ 2")
	st := p.Stykke(1)
	if st == nil {
		t.Fatal("§ 2 lost its first subsection")
	}
	if len(st.Items) != 0 {
		t.Errorf("the replaced subsection still lists %d items from the text it no longer has", len(st.Items))
	}
	if got := p.SpanText(st.Span); !strings.Contains(got, "Kun én regel") {
		t.Errorf("replaced subsection text = %q", got)
	}
	// The subsections behind it moved with the text, not merely in number.
	if got := p.SpanText(p.Stykke(2).Span); !strings.Contains(got, "Andet stykke") {
		t.Errorf("the following subsection no longer holds its own text: %q", got)
	}
	if got := p.SpanText(p.Stykke(3).Span); !strings.Contains(got, "Tredje stykke") {
		t.Errorf("the last subsection no longer holds its own text: %q", got)
	}
}

// TestWholeProvisionReplacementFromStykkerOnly survives instead of crashing. BR18 delivered one
// of its replacements as subsections and loose lines rather than as a paragraph, and the code
// that follows the addressed provision with the rest of the run sliced NewProvisions[1:] on that
// empty run. A consolidation that dies at a change produces no answer and no report, which is
// worse than the refusal this guard now allows.
func TestWholeProvisionReplacementFromStykkerOnlySurvives(t *testing.T) {
	edits := []Edit{{Kind: EditReplaceProvision, Address: ParseAddress("§ 3"),
		NewStykker: [][]Segment{{{Text: " Stk. 1. Hele bestemmelsen er ny.", Break: true}}},
		NewProse:   []string{"Og den løse linje, der hører til."}}}
	view, out := sampleLaw().AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: edits}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("replacement from subsections alone was refused: %+v", out.Unresolved)
	}
	got := textOf(t, view, "§ 3")
	if !strings.Contains(got, "Hele bestemmelsen er ny") {
		t.Errorf("§ 3 = %q, want the subsection the change brought", got)
	}
	if !strings.Contains(got, "den løse linje") {
		t.Errorf("§ 3 = %q, want the loose line the change brought as well", got)
	}
	if p := mustFindProvision(t, view, "§ 3"); p.Stykke(1) == nil {
		t.Error("the new subsection is not addressable as § 3, stk. 1")
	}
}
