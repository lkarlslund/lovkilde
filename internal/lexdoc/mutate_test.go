package lexdoc

import (
	"strings"
	"testing"
	"time"
)

// A small law built by hand: two provisions at the top, a chapter holding a third, an annex
// holding a fourth, and an entry-into-force provision that is deliberately not addressable as
// "§ 99" of the law.
func sampleLaw() *Content {
	three := &Provision{Number: "3", Printed: "§ 3.", Heading: "Særlige tilfælde",
		Segments: []Segment{{Text: "Særlige tilfælde.", Break: true}, {Text: "§ 3. Der må ikke ske udledning."}}}
	two := &Provision{Number: "2", Printed: "§ 2.",
		Segments: []Segment{{Text: "§ 2.", Break: true}, {Text: "Første stykke.", Break: true},
			{Text: "1) Nr een.", Break: true}, {Text: "2) Nr to.", Break: true},
			{Text: "Andet stykke.", Break: true}, {Text: "Tredje stykke."}}}
	two.Stykker = []Stykke{
		{Nr: 1, Span: Span{From: 1, To: 4}, Items: []Item{{Nr: 1, Span: Span{From: 2, To: 3}}, {Nr: 2, Span: Span{From: 3, To: 4}}}},
		{Nr: 2, Span: Span{From: 4, To: 5}},
		{Nr: 3, Span: Span{From: 5, To: 6}},
	}
	four := &Provision{Number: "4", Segments: []Segment{{Text: "Bilagets eget nummer."}}}
	return &Content{
		Order: []Node{
			{Provision: &Provision{Number: "1", Printed: "§ 1.", Segments: []Segment{{Text: "§ 1. Loven gælder her."}}}},
			{Provision: two},
			{Part: &Part{Kind: PartChapter, Number: "1", Heading: "Hovedregler", Order: []Node{{Provision: three}}}},
		},
		Preamble:       []Segment{{Text: "Indledning."}},
		Title:          []Segment{{Text: "Lov om noget."}},
		EntryIntoForce: []Provision{{Number: "99", Segments: []Segment{{Text: "§ 99. Ikrafttræden."}}}},
		Appendices:     []Appendix{{Number: "1", Label: "Bilag 1", Order: []Node{{Provision: four}}}},
	}
}

func textOf(t *testing.T, c *Content, addr string) string {
	loc, ok := c.Find(ParseAddress(addr))
	if !ok {
		t.Fatalf("%s not found", addr)
	}
	return loc.Provision.Text()
}

func TestCloneIsIndependent(t *testing.T) {
	c := sampleLaw()
	cp := c.Clone()

	// Edit every way the consolidation engine can: text, insertion, deletion, renumbering.
	loc, _ := cp.Find(ParseAddress("§ 1"))
	loc.Provision.Replace("Loven gælder her.", "Loven gælder kun her.")
	cp.DeleteProvision(mustFindProvision(t, cp, "§ 2"))
	mustFindProvision(t, cp, "§ 3").SetNumber("2", "§ 2.")
	cp.Preamble = append(cp.Preamble, Segment{Text: "Mere indledning."})

	if got, want := textOf(t, c, "§ 1"), "§ 1. Loven gælder her."; got != want {
		t.Errorf("cloned edit reached the original: %q, want %q", got, want)
	}
	if _, ok := c.Find(ParseAddress("§ 2")); !ok {
		t.Error("deleting from the clone removed a provision from the original")
	}
	if got := textOf(t, c, "§ 3"); !strings.Contains(got, "Der må ikke ske udledning") {
		t.Errorf("original § 3 = %q", got)
	}
	if len(c.Preamble) != 1 {
		t.Errorf("appending to the clone's preamble wrote through into the original: %d segments", len(c.Preamble))
	}
	// A clone must be able to be cloned again without sharing anything either.
	again := cp.Clone()
	loc, _ = again.Find(ParseAddress("§ 1"))
	loc.Provision.Replace("kun", "også")
	if got := textOf(t, cp, "§ 1"); !strings.Contains(got, "kun") {
		t.Errorf("second clone reached the first: %q", got)
	}
}

func mustFindProvision(t *testing.T, c *Content, addr string) *Provision {
	loc, ok := c.Find(ParseAddress(addr))
	if !ok {
		t.Fatalf("%s not found", addr)
	}
	return loc.Provision
}

func TestInsertLandsWhereTheAnchorIs(t *testing.T) {
	c := sampleLaw()

	nine := &Provision{Number: "9", Segments: []Segment{{Text: "§ 9. Ny bestemmelse."}}}
	if !c.InsertProvisionAfter(mustFindProvision(t, c, "§ 1"), nine) {
		t.Fatal("insert after § 1 refused")
	}
	var order []string
	for _, n := range c.Order {
		if n.Provision != nil {
			order = append(order, n.Provision.Number)
		}
	}
	if got := strings.Join(order, ","); got != "1,9,2" {
		t.Errorf("top-level order after insert = %s, want 1,9,2", got)
	}

	// Inside a part: the new provision has to end up in the chapter's own list, not at the
	// document's top level, and the chapter must still be reachable with its provisions.
	inside := &Provision{Number: "4", Segments: []Segment{{Text: "§ 4. I kapitlet."}}}
	if !c.InsertProvisionBefore(mustFindProvision(t, c, "§ 3"), inside) {
		t.Fatal("insert before § 3 refused")
	}
	for _, n := range c.Order {
		if n.Provision == inside {
			t.Error("insertion next to a chaptered provision landed at the top level instead")
		}
	}
	chap, ok := c.Find(ParseAddress("kapitel 1"))
	if !ok {
		t.Fatal("chapter 1 vanished")
	}
	var inChap []string
	for _, n := range chap.Part.Order {
		if n.Provision != nil {
			inChap = append(inChap, n.Provision.Number)
		}
	}
	if got := strings.Join(inChap, ","); got != "4,3" {
		t.Errorf("chapter order after insert = %s, want 4,3", got)
	}

	// An anchor the content does not hold is refused rather than appended somewhere.
	orphan := &Provision{Number: "99", Segments: []Segment{{Text: "x"}}}
	if c.InsertProvisionBefore(orphan, &Provision{Number: "100"}) {
		t.Error("insertion beside a provision the document does not contain was accepted")
	}
	if _, ok := c.Find(ParseAddress("§ 100")); ok {
		t.Error("a refused insertion still put a provision in the document")
	}
}

func TestDeleteProvisionRemovesOnlyIt(t *testing.T) {
	c := sampleLaw()
	if !c.DeleteProvision(mustFindProvision(t, c, "§ 3")) {
		t.Fatal("deleting a provision inside a chapter refused")
	}
	if _, ok := c.Find(ParseAddress("§ 3")); ok {
		t.Error("§ 3 is still there")
	}
	if _, ok := c.Find(ParseAddress("§ 2")); !ok {
		t.Error("deleting § 3 took § 2 with it")
	}
	chap, _ := c.Find(ParseAddress("kapitel 1"))
	if chap.Part == nil || len(chap.Part.Order) != 0 {
		t.Errorf("chapter still holds something: %+v", chap.Part)
	}
	if !c.DeleteProvision(mustFindProvision(t, c, "§ 1")) {
		t.Fatal("deleting a top-level provision refused")
	}
	if c.DeleteProvision(&Provision{Number: "ghost"}) {
		t.Error("deleting a provision the document does not contain was accepted")
	}
}

func TestReplaceContentKeepsTheProvision(t *testing.T) {
	c := sampleLaw()
	with := Provision{Number: "3", Printed: "§ 3.",
		Segments: []Segment{{Text: "§ 3. Ny ordlyd.", Break: true}, {Text: "Stykke eet."}},
		Stykker:  []Stykke{{Nr: 1, Span: Span{From: 1, To: 2}}},
	}
	target := mustFindProvision(t, c, "§ 3")
	if !c.ReplaceContent(target, with) {
		t.Fatal("content replacement refused")
	}
	if got := target.Text(); got != "§ 3. Ny ordlyd. Stykke eet." {
		t.Errorf("replaced text = %q", got)
	}
	if target.Heading != "Særlige tilfælde" {
		t.Errorf("heading disappeared although the amendment said nothing about it: %q", target.Heading)
	}
	st := target.Stykke(1)
	if st == nil || target.SpanText(st.Span) != "Stykke eet." {
		t.Errorf("new stykke not addressable: %+v", target.Stykker)
	}
	// The replacement's own number is ignored: the target keeps being the provision it is.
	with2 := Provision{Number: "77", Printed: "§ 77.", Segments: []Segment{{Text: "§ 77. Igen."}}}
	if !c.ReplaceContent(target, with2) {
		t.Fatal("second replacement refused")
	}
	if _, ok := c.Find(ParseAddress("§ 77")); ok {
		t.Error("replacing content created a provision numbered 77")
	}
	if got := mustFindProvision(t, c, "§ 3").Text(); got != "§ 77. Igen." {
		t.Errorf("§ 3 after second replacement = %q", got)
	}
}

func TestDeleteStykkeRenumbersByPosition(t *testing.T) {
	c := sampleLaw()
	p := mustFindProvision(t, c, "§ 2")
	if !p.DeleteStykke(2) {
		t.Fatal("deleting stk. 2 refused")
	}
	if len(p.Stykker) != 2 {
		t.Fatalf("stykker after delete = %d, want 2", len(p.Stykker))
	}
	// The former stk. 3 is now cited as stk. 2, and its span still points at its own words.
	if got := p.SpanText(p.Stykke(2).Span); got != "Tredje stykke." {
		t.Errorf("new stk. 2 text = %q, want the former stk. 3", got)
	}
	if got := p.SpanText(p.Stykke(1).Span); got != "Første stykke. 1) Nr een. 2) Nr to." {
		t.Errorf("stk. 1 text after the delete = %q", got)
	}
	if got := p.SpanText(p.Stykke(1).Items[1].Span); got != "2) Nr to." {
		t.Errorf("item text after the delete = %q", got)
	}
	if p.DeleteStykke(7) {
		t.Error("deleting a stykke that does not exist was accepted")
	}
}

func TestSetHeadingChangesTextAndField(t *testing.T) {
	c := sampleLaw()
	p := mustFindProvision(t, c, "§ 3")
	p.SetHeading("Undtagelser")
	if p.Heading != "Undtagelser" {
		t.Errorf("Heading field = %q", p.Heading)
	}
	if !strings.Contains(p.Text(), "Undtagelser") || strings.Contains(p.Text(), "Særlige tilfælde") {
		t.Errorf("heading text in the provision = %q", p.Text())
	}
}

// The XML reader keeps a provision's rubric out of its text, so a heading rewrite cannot depend
// on finding the old words there. It used to, and refused every one read from the XML.
func TestSetHeadingWhenTheRubricIsNotInTheText(t *testing.T) {
	p := &Provision{Number: "12", Heading: "Gammel", Segments: []Segment{{Text: "§ 12. Tekst."}}}
	c := &Content{Order: []Node{{Provision: p}}}
	if !c.SetHeadingChange(Located{Provision: p}, "Ny") {
		t.Fatal("heading rewrite refused")
	}
	if p.Heading != "Ny" || p.Text() != "§ 12. Tekst." {
		t.Errorf("heading %q, text %q", p.Heading, p.Text())
	}
}

func TestSetNumberMovesBothForms(t *testing.T) {
	c := sampleLaw()
	p := mustFindProvision(t, c, "§ 3")
	p.SetNumber("4", "§ 4.")
	if _, ok := c.Find(ParseAddress("§ 4")); !ok {
		t.Fatal("§ 4 not reachable after renumbering")
	}
	if _, ok := c.Find(ParseAddress("§ 3")); ok {
		t.Error("the old number still resolves")
	}
	if got := mustFindProvision(t, c, "§ 4").Printed; got != "§ 4." {
		t.Errorf("printed marker after renumbering = %q", got)
	}
	p.SetNumber("5", "")
	if got := mustFindProvision(t, c, "§ 5").Printed; got != "§ 4." {
		t.Errorf("printed marker = %q; an empty replacement should leave it alone", got)
	}
}

func TestHeadingOfAGroupIsChangeable(t *testing.T) {
	// The register's "Overskriften før § 3 ophæves" usually points at the rubric above the group
	// the paragraph stands in, not at a rubric belonging to the paragraph. Removing it must not
	// touch the provision, and must not be refused because the provision has no heading field.
	c := &Content{Order: []Node{
		{Part: &Part{Kind: PartGroup, Heading: "Beskyttelseszone", Order: []Node{
			{Provision: &Provision{Number: "3", Printed: "§ 3.", Segments: []Segment{{Text: "§ 3. Zonen er afgrænset."}}}},
		}}},
	}}
	view, out := c.AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditDeleteProvision, Address: ParseAddress("Overskriften før § 3 ophæves.")},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("removing the group's rubric was refused: %+v", out.Unresolved)
	}
	if got := view.Order[0].Part.Heading; got != "" {
		t.Errorf("group rubric after removal = %q, want it gone", got)
	}
	if got := c.Order[0].Part.Heading; got == "" {
		t.Error("the view changed the content it was given: an as-of view is a copy")
	}
	p, ok := view.Find(ParseAddress("§ 3"))
	if !ok || !strings.Contains(p.Provision.Text(), "Zonen er afgrænset") {
		t.Errorf("the provision itself changed: %+v", p)
	}
}

// An address that resolves to a numbered condition must delete that condition and nothing
// else: the location carries its subsection too, and the old code acted on the subsection.
func TestDeleteItemKeepsTheSubsection(t *testing.T) {
	c := sampleLaw()
	p := mustFindProvision(t, c, "§ 2")
	st := p.Stykke(1)
	if len(st.Items) != 2 {
		t.Fatalf("fixture items = %d, want 2", len(st.Items))
	}
	before := p.SpanText(st.Span)
	if !p.DeleteItem(1, 1) {
		t.Fatal("deleting nr. 1 refused")
	}
	st = p.Stykke(1)
	if len(st.Items) != 1 {
		t.Fatalf("items after delete = %d, want 1", len(st.Items))
	}
	if st.Items[0].Nr != 1 {
		t.Errorf("surviving condition numbered %d, want position 1", st.Items[0].Nr)
	}
	got := p.SpanText(st.Span)
	if !strings.Contains(got, "Nr to.") {
		t.Errorf("surviving condition lost its words: %q", got)
	}
	if strings.Contains(got, "Nr een.") {
		t.Errorf("repealed condition still present: %q", got)
	}
	if len(before) <= len(got) {
		t.Errorf("text did not shrink: %q -> %q", before, got)
	}
	// Other subsections keep their own words and positions.
	if p.SpanText(p.Stykke(2).Span) != "Andet stykke." {
		t.Errorf("stk. 2 after the item delete = %q", p.SpanText(p.Stykke(2).Span))
	}
	if p.DeleteItem(1, 9) {
		t.Error("deleting a condition that does not exist was accepted")
	}
}

// "I § 5, stk. 2, indsættes som nr. 3:" whose new text the reader took for a subsection: it is the
// condition the address numbers, at the end of stk. 2's list, and stk. 3 keeps its number.
func TestANewConditionDonatedAsASubsectionGoesInTheList(t *testing.T) {
	p := &Provision{Number: "5", Printed: "§ 5.", Segments: []Segment{
		{Text: "§ 5."}, {Text: "Første."}, {Text: "Stk. 2."}, {Text: "Udgifter kan medtages, hvis:"}, {Text: "1)"}, {Text: "udgifterne sker som led i noget, eller"},
		{Text: "2)"}, {Text: "udgifterne er nødvendige."}, {Text: "Stk. 3."}, {Text: "Udgifter til konsulenter kompenseres:"}, {Text: "1)"}, {Text: "Aftale A."}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 5, stk. 2 , indsættes som nr. 3 :"
	out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def,
		NewStykker: [][]Segment{{{Text: "3) udgifterne er direkte relateret."}}}, NewText: []Segment{{Text: "3) udgifterne er direkte relateret."}}})
	if !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if len(p.Stykker) != 3 || len(p.Stykker[1].Items) != 3 || p.Stykker[2].Nr != 3 {
		t.Errorf("stykker %+v\n%q", p.Stykker, p.Text())
	}
}

// "I § 8 indsættes efter stk. 2 som nye stykker:" handed over as one subsection printing three
// labels: three subsections, and the old stk. 3 becomes stk. 6.
func TestNewSubsectionsHandedOverAsOneAreThree(t *testing.T) {
	p := &Provision{Number: "8", Printed: "§ 8.", Segments: []Segment{{Text: "§ 8."}, {Text: "Første."}, {Text: "Stk. 2."}, {Text: "Anden."}, {Text: "Stk. 3."}, {Text: "Chefen fastsætter retningslinjer."}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 8 indsættes efter stk. 2 som nye stykker:"
	out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewStykker: [][]Segment{{
		{Text: "Stk. 3."}, {Text: "Terminaladgang."}, {Text: "Stk. 4."}, {Text: "Typer af oplysninger."}, {Text: "Stk. 5."}, {Text: "Flypassagerer."}}}})
	if !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if sp := p.Stykker[len(p.Stykker)-1].Span; len(p.Stykker) != 6 || !strings.HasPrefix(SegmentsText(p.Segments[sp.From:sp.To]), "Stk. 6.") || !strings.Contains(SegmentsText(p.Segments[sp.From:sp.To]), "Chefen") {
		t.Errorf("%d stykker: %q", len(p.Stykker), p.Text())
	}
}

// "§ 1, stk. 1, nr. 6, affattes således: 6) …" where the list ends at nr. 5: the next condition.
func TestRewritingTheConditionAfterTheLastAddsIt(t *testing.T) {
	p := &Provision{Number: "1", Printed: "§ 1.", Segments: []Segment{{Text: "§ 1."}, {Text: "Følgende stoffer må ikke anvendes:"},
		{Text: "1)"}, {Text: "Svovlsyre."}, {Text: "2)"}, {Text: "Phosphor."}, {Text: "Stk. 2."}, {Text: "Forbuddet gælder ikke fyrværkeri."}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "§ 1, stk. 1, nr. 3, affattes således:"
	out := c.Apply(Edit{Kind: EditReplaceProvision, Address: ParseAddress(def), Definition: def, NewText: []Segment{{Text: "»3) Urea-hydrogenperoxid.«"}}})
	if !out.Applied || len(p.Stykker[0].Items) != 3 || len(p.Stykker) != 2 {
		t.Fatalf("outcome %+v; %q", out, p.Text())
	}
	def = "§ 1, stk. 1, nr. 5, affattes således:"
	if out := c.Apply(Edit{Kind: EditReplaceProvision, Address: ParseAddress(def), Definition: def, NewText: []Segment{{Text: "»5) Noget.«"}}}); out.Applied {
		t.Errorf("nr. 5 after a list ending at nr. 3 was added: %q", p.Text())
	}
}

// "I § 361, stk. 1, indsættes som nr. 33: »33) …«" where the mirror's list, short of conditions
// the official text has, ends at 31): nothing moves, and the new condition keeps the act's number.
func TestAConditionAddedAtTheEndKeepsTheNumberTheActGivesIt(t *testing.T) {
	p := &Provision{Number: "361", Printed: "§ 361.", Segments: []Segment{{Text: "§ 361."}, {Text: "Følgende betaler:"},
		{Text: "1)"}, {Text: "Forvaltere."}, {Text: "2)"}, {Text: "Andre forvaltere."}, {Text: "Stk. 2."}, {Text: "Andet."}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 361, stk. 1, indsættes som nr. 4:"
	out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewProse: []string{"4) Rådgivere betaler 12.000 kr."}})
	if !out.Applied || !strings.Contains(p.Text(), "4) Rådgivere") {
		t.Errorf("outcome %+v; %q", out, p.Text())
	}
}

// "I § 17 indsættes før stk. 3 som nye stykker: Stk. 2. … Stk. 3. …": the labels place the new
// subsections in front of stk. 2, which becomes stk. 4.
func TestNewSubsectionsLabelledLowerThanTheirPlaceGoByTheirLabels(t *testing.T) {
	p := &Provision{Number: "17", Printed: "§ 17.", Segments: []Segment{{Text: "§ 17."}, {Text: "Tilskud."}, {Text: "Stk. 2."}, {Text: "Midlerne anvendes."}, {Text: "Stk. 3."}, {Text: "Placeres i fonden."}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 17 indsættes før stk. 3 som nye stykker:"
	out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewStykker: [][]Segment{{{Text: "Stk. 2."}, {Text: "Loft."}}, {{Text: "Stk. 3."}, {Text: "Koncern."}}}})
	if !out.Applied || len(p.Stykker) != 5 {
		t.Fatalf("outcome %+v; %q", out, p.Text())
	}
	if sp := p.Stykker[3].Span; !strings.Contains(SegmentsText(p.Segments[sp.From:sp.To]), "Midlerne") {
		t.Errorf("stk. 4 = %q", SegmentsText(p.Segments[sp.From:sp.To]))
	}
}
