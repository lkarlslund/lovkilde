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
	if !p.SetHeading("Særlige tilfælde", "Undtagelser") {
		t.Fatal("heading rewrite refused although the rubric is the provision's own first words")
	}
	if p.Heading != "Undtagelser" {
		t.Errorf("Heading field = %q", p.Heading)
	}
	if !strings.Contains(p.Text(), "Undtagelser") || strings.Contains(p.Text(), "Særlige tilfælde") {
		t.Errorf("heading text in the provision = %q", p.Text())
	}
	if p.SetHeading("rubric the provision never had", "x") {
		t.Error("heading rewrite accepted a rubric that is not there")
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
