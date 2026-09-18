package lexdoc

import (
	"strings"
	"testing"
)

// The rules here are the ones a reader cannot be blamed for getting wrong, because they are
// about what a Danish citation means rather than about how the register encodes it.

func TestSegmentsTextKeepsBlockSeparation(t *testing.T) {
	// A break is what tells two paragraphs apart once whitespace is normalised. Without it
	// "stk. 1stk. 2" would be one run of characters.
	segs := []Segment{
		{Text: "Første stykke.", Break: true},
		{Text: "Andet stykke."},
	}
	if got, want := SegmentsText(segs), "Første stykke. Andet stykke."; got != want {
		t.Errorf("SegmentsText = %q, want %q", got, want)
	}
	// A break with no text of its own still separates: it is how an empty block element
	// contributes the whitespace it printed.
	segs = []Segment{{Text: "A"}, {Break: true}, {Text: "B"}}
	if got, want := SegmentsText(segs), "A B"; got != want {
		t.Errorf("SegmentsText with a bare break = %q, want %q", got, want)
	}
}

func TestSpanTextNestsInsideProvision(t *testing.T) {
	p := &Provision{Number: "5", Segments: []Segment{
		{Text: "§ 5.", Break: true},
		{Text: "Første stykke.", Break: true},
		{Text: "1) Nr een.", Break: true},
		{Text: "2) Nr to.", Break: true},
	}}
	p.Stykker = []Stykke{{Nr: 1, Span: Span{From: 1, To: 4}, Items: []Item{
		{Nr: 1, Span: Span{From: 2, To: 3}},
		{Nr: 2, Span: Span{From: 3, To: 4}},
	}}}
	if got, want := p.Text(), "§ 5. Første stykke. 1) Nr een. 2) Nr to."; got != want {
		t.Errorf("provision text = %q, want %q", got, want)
	}
	st := p.Stykke(1)
	if st == nil {
		t.Fatal("Stykke(1) not found")
	}
	stekst := p.SpanText(st.Span)
	if !strings.Contains(p.Text(), stekst) {
		t.Errorf("stykke text %q does not nest inside the provision text %q", stekst, p.Text())
	}
	it := st.Item(2, "")
	if it == nil {
		t.Fatal("item 2 not found")
	}
	if got := p.SpanText(it.Span); got != "2) Nr to." {
		t.Errorf("item text = %q", got)
	}
	// A span that runs past the end is clamped rather than panicking: spans come from a
	// decoded artifact, and a truncated file must not take the server down.
	if got := p.SpanText(Span{From: 3, To: 99}); got != "2) Nr to." {
		t.Errorf("clamped span = %q", got)
	}
	if got := p.SpanText(Span{From: 99, To: 100}); got != "" {
		t.Errorf("out-of-range span = %q, want empty", got)
	}
}

func TestFindPrefersTheLawOverItsOwnAppendix(t *testing.T) {
	// A consolidated act quotes the entry-into-force paragraph of every amending act that
	// touched it, and each quotation restarts at § 1. "§ 1" must reach the law's own
	// provision; the quotation is unreachable by number, so repealing § 1 cannot quietly
	// delete a historical entry-into-force paragraph while the real § 1 survives.
	c := &Content{
		Preamble: []Segment{{Text: "Loven gælder ikke for Færøerne."}},
		Title:    []Segment{{Text: "Lov om noget"}},
	}
	own := &Provision{Number: "1", Segments: []Segment{{Text: "§ 1. Lovens eget § 1."}}}
	c.Order = []Node{{Provision: own}}
	c.EntryIntoForce = []Provision{{Number: "1", Printed: "§ 1.", Segments: []Segment{{Text: "§ 1. Ikrafttræden."}}}}

	loc, ok := c.Find(ParseAddress("§ 1"))
	if !ok || loc.Provision != own {
		t.Fatalf("§ 1 resolved to %s, want the law's own provision", loc)
	}
	if _, ok := c.Find(ParseAddress("indledningen affattes således:")); !ok {
		t.Error("the preamble did not resolve")
	}
	if _, ok := c.Find(ParseAddress("lovens titel ændres")); !ok {
		t.Error("the document title did not resolve")
	}
	// "I § 1 og i indledningen ændres …" keeps pointing at § 1: the non-provision units are
	// claimed only when the address names nothing else.
	if loc, ok := c.Find(ParseAddress("I § 1 og i indledningen ændres »X« til: »Y«")); !ok || loc.Provision != own {
		t.Errorf("compound address resolved to %s, want § 1", loc)
	}
	// An address naming a provision that does not exist resolves to nothing.
	if _, ok := c.Find(ParseAddress("§ 2")); ok {
		t.Error("§ 2 resolved although the document has no § 2")
	}
	if _, ok := c.Find(ParseAddress("det er bare nogle ord")); ok {
		t.Error("an unrecognised address resolved anyway")
	}
}

// TestEntryIntoForceNeedsItsOwnName is the other half of the rule the test above states: refusing
// a plain "§ 1" out of the effectivity series is right for an amendment and wrong for a reader,
// because the series holds provisions the mirror prints and a search hit can land on. Naming the
// series reaches them; a plain number still does not.
func TestEntryIntoForceNeedsItsOwnName(t *testing.T) {
	c := &Content{}
	own := &Provision{Number: "1", Segments: []Segment{{Text: "§ 1. Lovens eget § 1."}}}
	c.Order = []Node{{Provision: own}}
	eif := &Provision{Number: "1", Printed: "§ 1.", Segments: []Segment{{Text: "§ 1. Ikrafttræden."}, {Text: "Stk. 2. Overgangsregel."}},
		Stykker: []Stykke{{Nr: 1, Span: Span{From: 0, To: 1}}, {Nr: 2, Span: Span{From: 1, To: 2}}}}
	c.EntryIntoForce = []Provision{*eif}

	named := ParseAddress("§ 1, ikrafttrædelsesbestemmelse")
	if !named.EntryIntoForce {
		t.Fatal("the trailing locator did not name the effectivity series")
	}
	loc, ok := c.Find(named)
	if !ok || loc.Provision == own {
		t.Fatalf("the named address resolved to %s, want the effectivity provision", loc)
	}
	if loc, ok := c.Find(ParseAddress("§ 1, stk. 2, ikrafttrædelsesbestemmelse")); !ok || loc.Stykke == nil || loc.Stykke.Nr != 2 {
		t.Errorf("the named address did not reach stk. 2 of the series: %s ok=%v", loc, ok)
	}
	if _, ok := c.Find(ParseAddress("§ 9, ikrafttrædelsesbestemmelse")); ok {
		t.Error("a named series resolved to a provision it does not hold")
	}
	// An amendment sentence may mention the series while citing some other section, and must not be
	// redirected by it: the marker counts only after the § it qualifies.
	if a := ParseAddress("lovens ikrafttrædelsesbestemmelser ændres, og § 1 ophæves"); a.EntryIntoForce {
		t.Error("an amendment naming the series in passing was aimed at it")
	}
	if a := ParseAddress("§ 1, ikrafttrædelsesbestemmelse"); a.String() != "§ 1, ikrafttrædelsesbestemmelse" {
		t.Errorf("the printed address is %q, which ParseAddress would not read back", a.String())
	}
}

func TestFindRefusesAnItemWithoutItsStykke(t *testing.T) {
	// "§ 5, nr. 2" names no stykke, and items live under a stykke. Searching every stykke for
	// a "nr. 2" would splice text into whichever one happened to have it, which is a guess
	// about which provision the drafter meant, so the address resolves to nothing.
	c := &Content{}
	p := &Provision{Number: "5", Segments: []Segment{{Text: "§ 5."}, {Text: "1) Nr een."}, {Text: "2) Nr to."}}}
	p.Stykker = []Stykke{{Nr: 1, Span: Span{From: 1, To: 3}, Items: []Item{{Nr: 1, Span: Span{From: 1, To: 2}}, {Nr: 2, Span: Span{From: 2, To: 3}}}}}
	c.Order = []Node{{Provision: p}}

	if _, ok := c.Find(ParseAddress("§ 5, stk. 1, nr. 2")); !ok {
		t.Error("a complete item address did not resolve")
	}
	if _, ok := c.Find(ParseAddress("§ 5, nr. 2")); ok {
		t.Error("an item address with no stykke resolved, which is a guess about the target")
	}
	if _, ok := c.Find(ParseAddress("§ 5, stk. 3")); ok {
		t.Error("a stykke that does not exist resolved anyway")
	}
}

func TestFindMatchesLitraBothWays(t *testing.T) {
	// The corpus prints "§ 2 a." and cites it as "§ 2a"; both must reach the same provision,
	// and "§ 8 og 9" must not be read as a litra at all.
	c := &Content{}
	a := &Provision{Number: "2", Litra: "a"}
	b := &Provision{Number: "8"}
	c.Order = []Node{{Provision: a}, {Provision: b}}

	for _, q := range []string{"§ 2 a", "§ 2a"} {
		loc, ok := c.Find(ParseAddress(q))
		if !ok || loc.Provision != a {
			t.Errorf("%q resolved to %s, want § 2 a", q, loc)
		}
	}
	if loc, ok := c.Find(ParseAddress("§ 8")); !ok || loc.Provision != b {
		t.Errorf("§ 8 resolved to %s", loc)
	}
	if loc, ok := c.Find(ParseAddress("§ 8 og 9 ændres")); !ok || loc.Provision != b {
		t.Errorf("§ 8 og 9 resolved to %q, want § 8 rather than a litra read out of the conjunction", loc)
	}
}

func TestRomanAndAfsnitNumbers(t *testing.T) {
	// "afsnit II" and "afsnit 2" are the same target, and Danish words spelled with roman
	// symbols must not be read as numbers: "vid" would otherwise number a section.
	if got := RomanToNumber("IV"); got != "4" {
		t.Errorf("RomanToNumber(IV) = %q", got)
	}
	if got := RomanToNumber("IIII"); got != "" {
		t.Errorf("RomanToNumber(IIII) = %q, want rejected", got)
	}
	if got := RomanToNumber("vid"); got != "" {
		t.Errorf("RomanToNumber(vid) = %q, want rejected", got)
	}
	if got := PrintedRoman("Afsnit XXIV"); got != "24" {
		t.Errorf("PrintedRoman = %q, want 24", got)
	}
	c := &Content{}
	p := &Part{Kind: PartAfsnit, Number: "2"}
	c.Order = []Node{{Part: p}}
	if loc, ok := c.Find(ParseAddress("afsnit II")); !ok || loc.Part != p {
		t.Errorf("afsnit II resolved to %s", loc)
	}
	if loc, ok := c.Find(ParseAddress("afsnit 2")); !ok || loc.Part != p {
		t.Errorf("afsnit 2 resolved to %s", loc)
	}
}

func TestEverywhereAddressNamesNoProvision(t *testing.T) {
	// "overalt i loven ændres »X« til: »Y«" has no address, and the § inside the quoted
	// passage is content. Resolving it would splice the change into one provision and leave
	// every other occurrence of the old wording untouched.
	c := &Content{}
	p := &Provision{Number: "5", Segments: []Segment{{Text: "§ 5."}}}
	c.Order = []Node{{Provision: p}}

	a := ParseAddress("Der indskydes et nummer som § 5, stk. 3, overalt i loven ændres »X« til: »Y«")
	if !a.Everywhere || a.Recognised != true {
		t.Fatalf("Everywhere/Recognised = %v/%v", a.Everywhere, a.Recognised)
	}
	if _, ok := c.Find(a); ok {
		t.Error("a whole-act replace resolved to a provision")
	}
}

func TestDatesAndIdentityStayFacts(t *testing.T) {
	// A shelf nobody stated must not become a citation, and the register's own type string
	// has to survive the derivation that reads a family and a class out of it.
	id := Identity{Type: "BEK Æ#LOKDOK05", Family: "BEK", Class: "Æ", Medium: "lta", PublishedYear: 2020, PublishedNumber: 1234}
	if got, want := id.ELIPath(), "eli/lta/2020/1234"; got != want {
		t.Errorf("ELIPath = %q, want %q", got, want)
	}
	unknown := Identity{Medium: "lta"}
	if unknown.ELIPath() != "" {
		t.Errorf("ELIPath = %q for a document whose year and number are unknown", unknown.ELIPath())
	}
}
