package lexdoc

import (
	"strings"
	"testing"
	"time"
)

// The 1991 amendment to lov om bygnings- og boligregistrering (A19910027530), as its HTML payload
// prints it: the only text the mirror has for it.
var legacyChangeList = []string{
	"Lov om ændring af lov om bygnings- og boligregistrering",
	"VI MARGRETHE DEN ANDEN, af Guds Nåde Danmarks Dronning, gør vitterligt:",
	"§ 1",
	"I lov om bygnings- og boligregistrering, jf. lovbekendtgørelse nr. 132 af 28. februar 1990, foretages følgende ændringer:",
	"1. § 6, stk. 1-3, affattes således:",
	"»Offentlige myndigheder kan rekvirere oplysninger fra registret.",
	"Stk. 2. Oplysninger må ikke videregives kommercielt.",
	"Stk. 3. Oplysninger skal opbevares forsvarligt.«",
	"2. I § 6, stk. 4, ændres »stk. 3« til: »stk. 1«.",
	"§ 2",
	"Loven træder i kraft den 1. juli 1991.",
}

func TestReadChangeListsFromALegacyAct(t *testing.T) {
	got := ReadChangeLists(legacyChangeList, "A19910027530")
	if len(got) != 2 {
		t.Fatalf("changes = %d, want 2: %+v", len(got), got)
	}
	one, two := got[0], got[1]
	if one.Target.Number != 132 || one.Target.Date.Format("2006-01-02") != "1990-02-28" {
		t.Errorf("target = %+v, want lovbekendtgørelse nr. 132 af 28. februar 1990", one.Target)
	}
	if one.Kind != EditReplaceProvision || one.Address.Stk != 1 || one.Address.StkTo != 3 {
		t.Errorf("change 1 = %s %s", one.Kind, one.Address)
	}
	// The unlabelled first line is stk. 1, because labelled subsections follow it.
	if len(one.NewStykker) != 3 || !strings.HasPrefix(SegmentsText(one.NewStykker[0]), "Offentlige") ||
		!strings.HasSuffix(SegmentsText(one.NewStykker[2]), "forsvarligt.") {
		t.Errorf("change 1 brings %d stykker: %+v", len(one.NewStykker), one.NewStykker)
	}
	if two.Kind != EditReplaceText || two.Old != "stk. 3" || two.New != "stk. 1" {
		t.Errorf("change 2 = %s %q → %q", two.Kind, two.Old, two.New)
	}
	// § 2 is the act's own, and the list ended before it.
	for _, e := range got {
		if strings.Contains(e.Prose, "træder i kraft") {
			t.Errorf("the act's entry into force was read into change %s", e.Number)
		}
	}
}

// The modern payload: unbalanced lines, a new provision with its heading, and a list of one with no
// number.
func TestReadChangeListsShapes(t *testing.T) {
	got := ReadChangeLists([]string{
		"§ 1",
		"I lov nr. 483 af 31. maj 2000 om Danmarks Pædagogiske Universitet foretages følgende ændringer:",
		"1. Efter § 18 indsættes:",
		"»Elektronisk kommunikation",
		"§ 18 a. Ministeren kan fastsætte regler om elektronisk kommunikation.",
		"Stk. 2. Reglerne kan omfatte digital signatur, jf. § 11, stk. 10.«",
		"2. § 16, stk. 5, nr. 4, affattes således:",
		"»4) I værdipapirer fra et EU-/EØS-medlemsland.«",
		"§ 2",
		"I lov nr. 202 af 18. maj 1982 foretages følgende ændring:",
		"I § 3 ændres »på 8 pct.« til: »på 6 pct.«",
		"§ 3",
		"Loven træder i kraft den 1. januar 2002.",
	}, "A20010024630")
	if len(got) != 3 {
		t.Fatalf("changes = %d, want 3: %+v", len(got), got)
	}
	ins := got[0]
	if ins.Kind != EditInsertProvision || len(ins.NewProvisions) != 1 {
		t.Fatalf("change 1 = %s with %d provisions", ins.Kind, len(ins.NewProvisions))
	}
	p := ins.NewProvisions[0]
	if p.Number != "18" || p.Litra != "a" || p.Heading != "Elektronisk kommunikation" || len(p.Stykker) != 2 {
		t.Errorf("new provision = %s%s heading %q, %d stykker", p.Number, p.Litra, p.Heading, len(p.Stykker))
	}
	if got[1].Address.Nr != 4 || len(got[1].NewProse) != 1 {
		t.Errorf("change 2 = %s, prose %q", got[1].Address, got[1].NewProse)
	}
	if one := got[2]; one.Target.Number != 202 || one.Kind != EditReplaceText || one.New != "på 6 pct." {
		t.Errorf("the unnumbered change = %s %+v %q", one.Kind, one.Target, one.New)
	}
}

// A quotation that never closes still ends at the act's own entry into force.
func TestUnclosedQuoteEndsAtTheActsNextProvision(t *testing.T) {
	got := ReadChangeLists([]string{
		"§ 1",
		"I bekendtgørelse nr. 289 af 4. maj 1993 foretages følgende ændringer:",
		"1. I § 9, stk. 1, affattes skemaet således:",
		"»Skoleperioder 2. 3. 4.",
		"§ 2. Bekendtgørelsen træder i kraft den 1. marts 1995.",
	}, "B19950011105")
	if len(got) != 1 || strings.Contains(strings.Join(got[0].NewProse, " "), "træder i kraft") {
		t.Fatalf("changes = %+v", got)
	}
	if !namesPartOfUnit(got[0].Definition) {
		t.Error("a table inside a subsection was not recognised as part of it")
	}
}

// The acts a preamble says amended the law already: the chain the law's own metadata lists can lack
// one, and the act citing it is the only record.
func TestPreambleNamesTheEarlierAmendments(t *testing.T) {
	tg := ParseTarget("I lov nr. 687 af 8. juni 2017 om ændring af registreringsafgiftsloven (Udskydelse …), som ændret ved § 8 i lov nr. 1585 af 27. december 2019, foretages følgende ændring:")
	if tg.Number != 687 || len(tg.Earlier) != 1 {
		t.Fatalf("target = %+v", tg)
	}
	if e := tg.Earlier[0]; e.Number != 1585 || e.Section != "8" || e.Date != time.Date(2019, 12, 27, 0, 0, 0, 0, time.UTC) {
		t.Errorf("earlier = %+v", e)
	}
	tg = ParseTarget("I virksomhedsskatteloven, jf. lovbekendtgørelse nr. 1114 af 18. september 2013, som ændret bl.a. ved § 4 i lov nr. 202 af 27. februar 2015 og § 13 i lov nr. 652 af 8. juni 2016 og senest ved § 15 i lov nr. 688 af 8. juni 2017, foretages følgende ændringer:")
	if len(tg.Earlier) != 3 || tg.Earlier[2].Number != 688 || tg.Earlier[2].Section != "15" {
		t.Errorf("earlier = %+v", tg.Earlier)
	}
	if tg := ParseTarget("I lov om kommunal indkomstskat, jf. lovbekendtgørelse nr. 1 af 2. januar 2020, foretages følgende ændring:"); len(tg.Earlier) != 0 {
		t.Errorf("a preamble naming no earlier act gave %+v", tg.Earlier)
	}
}

// An amending act's own provision is its change list: changes 1-5, whatever the quoted text prints.
func TestAChangeListsNumbersAreItsOwn(t *testing.T) {
	p := Provision{Number: "3", Segments: []Segment{
		{Text: "§ 3"},
		{Text: "I lov om rettens pleje, jf. lovbekendtgørelse nr. 910 af 27. september 2005, foretages følgende ændringer:"},
		{Text: "1."}, {Text: "I § 478, stk. 4, indsættes som"}, {Text: "2. pkt.:"}, {Text: "»Ny sætning.«"},
		{Text: "2."}, {Text: "I § 510 indsættes som stk. 4:"}, {Text: "»"}, {Text: "Stk. 4."}, {Text: "Ny regel.«"},
		{Text: "3."}, {Text: "I § 518, stk. 3, ændres »A« til: »B«."},
	}}
	// The site's HTML made a subsection of the quoted "Stk. 4.".
	p.Stykker = []Stykke{{Nr: 1, Span: Span{From: 8, To: 11}}}
	p.Normalise()
	if len(p.Stykker) != 1 {
		t.Fatalf("stykker = %+v", p.Stykker)
	}
	items := p.Stykker[0].Items
	if len(items) != 3 || items[1].Span.From != 6 || items[2].Span.From != 11 {
		t.Errorf("changes = %+v, want 1., 2. and 3. at segments 2, 6 and 11", items)
	}
}

// BR18 § 344 prints "Stk. 2", without its full stop, inside the markup of stk. 1.
func TestASubsectionLabelInsideAnotherIsSplitOff(t *testing.T) {
	p := Provision{Number: "344", Segments: []Segment{
		{Text: "§ 344."}, {Text: "Konstruktioner skal projekteres."}, {Text: "Stk. 2"}, {Text: "Projektering skal ske efter:"},
		{Text: "1)"}, {Text: "DS/EN 1990."}, {Text: "Stk. 3."}, {Text: "For væksthuse gælder andet."},
	}, Stykker: []Stykke{{Nr: 1, Span: Span{From: 1, To: 6}}, {Nr: 2, Span: Span{From: 6, To: 8}}}}
	p.Normalise()
	if len(p.Stykker) != 3 || p.Stykker[1].Span.From != 2 || p.Stykker[2].Span.From != 6 {
		t.Fatalf("stykker = %+v", p.Stykker)
	}
	if len(p.Stykker[1].Items) != 1 {
		t.Errorf("stk. 2 items = %+v", p.Stykker[1].Items)
	}
	// A "Stk. 2" whose numbers do not then run in order is left where it is.
	q := Provision{Number: "1", Segments: []Segment{
		{Text: "§ 1."}, {Text: "Tekst."}, {Text: "Stk. 2"}, {Text: "Mere."}, {Text: "Stk. 2."}, {Text: "Andet."},
	}, Stykker: []Stykke{{Nr: 1, Span: Span{From: 1, To: 4}}, {Nr: 2, Span: Span{From: 4, To: 6}}}}
	q.Normalise()
	if len(q.Stykker) != 2 {
		t.Errorf("stykker = %+v", q.Stykker)
	}
}

// "Efter afsnit X indsættes som afsnit X a: »Afsnit X a Investeringsrådgivere Kapitel 20 a …«": the
// new chapter stands in the new afsnit, not beside it.
func TestANewAfsnitHoldsItsChapters(t *testing.T) {
	got := ReadChangeLists([]string{
		"§ 1",
		"I lov om finansiel virksomhed, jf. lovbekendtgørelse nr. 286 af 4. april 2006, foretages følgende ændring:",
		"1. Efter afsnit X indsættes som afsnit X a:",
		"»Afsnit X a",
		"Investeringsrådgivere",
		"Kapitel 20 a",
		"Investeringsrådgivere",
		"§ 343 a. Virksomheder må ikke udøve investeringsrådgivning uden registrering.«",
		"§ 2",
		"Loven træder i kraft den 1. januar 2008.",
	}, "A20070010830")
	if len(got) != 1 || len(got[0].NewParts) != 1 {
		t.Fatalf("changes %+v", got)
	}
	a := got[0].NewParts[0]
	if a.Kind != PartAfsnit || a.Number != "10a" || a.Heading != "Investeringsrådgivere" || len(a.Order) != 1 || a.Order[0].Part == nil || a.Order[0].Part.Number != "20a" {
		t.Errorf("new afsnit = %+v", a)
	}
}

// A change list names its law before the consolidation it cites, and that name stands when the
// consolidation is not archived or printed the law's older title.
func TestAPreambleNamesItsLaw(t *testing.T) {
	tg := ParseTarget("I lov om almene boliger m.v., jf. lovbekendtgørelse nr. 106 af 21. februar 2005, som ændret ved § 1 i lov nr. 328 af 18. maj 2005, foretages følgende ændringer:")
	if tg.Title != "lov om almene boliger m.v." || tg.Number != 106 {
		t.Errorf("target = %+v", tg)
	}
	if tg := ParseTarget("I lov nr. 687 af 8. juni 2017 om ændring af registreringsafgiftsloven foretages følgende ændring:"); tg.Title != "" {
		t.Errorf("an act cited by number has no name to go by: %q", tg.Title)
	}
}

// A change to an amending act may reach into a provision one of its changes brings: lov nr. 1587
// of 2006 repeals "§ 1, nr. 14, … § 11 i, stk. 2" of lov nr. 571 of 2005. That act's change 11
// opens a quotation twice ("»Kapitel 2 c", then "»§ 5 h.") and closes it once, and its changes
// from 12 on read as quoted text: the change list ended at 11.
func TestASubsectionAChangeBringsIsRepealed(t *testing.T) {
	lines := []string{"§ 1.", "I lov om planlægning foretages følgende ændringer:",
		"1. Efter § 5 f indsættes:", "»Kapitel 2 c", "Planlægning i hovedstadsområdet", "»§ 5 h. Hovedstadsområdet omfatter kommunerne.", "§ 5 k. Planerne gælder.«",
		"2. Efter § 11 h indsættes:", "»§ 11 i. Miljøministeren kan fastsætte regler.", "Stk. 2. For anlæg til husdyrbrug kan ministeren fastsætte regler om,",
		"1) at afgørelser kræver samtykke, og", "2) at tilladelser indsendes.«",
		"3. I § 12 udgår »på Bornholm«."}
	var segs []Segment
	for _, l := range lines {
		segs = append(segs, Segment{Text: l, Break: true})
	}
	p := &Provision{Number: "1", Printed: "§ 1.", Segments: segs}
	p.Normalise()
	if n := len(p.Stykker[0].Items); n != 3 {
		t.Fatalf("the change list has %d changes, want 3", n)
	}
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 1, nr. 2, ophæves § 11 i, stk. 2."
	if out := c.Apply(Edit{Kind: EditDeleteProvision, Address: ParseAddress(def), Definition: def}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	got := SegmentsText(p.Segments)
	if strings.Contains(got, "husdyrbrug") || !strings.Contains(got, "fastsætte regler.«") || !strings.Contains(got, "3. I § 12") {
		t.Errorf("§ 1 = %q", got)
	}
}

// "§ 3 affattes således:" brings the new stk. 1 in one quotation and stk. 2-4 in a second whose
// closing mark is never printed (bekendtgørelse nr. 818 af 2005). Read as the change's sentence,
// the new § 3 lost three subsections; read to the end of the quotation, it swallowed the list.
func TestASecondQuotationOfNewTextRunsToTheNextChange(t *testing.T) {
	lines := []string{
		"I bekendtgørelse nr. 633 af 27. juni 2005 om indretning af olietanke foretages følgende ændringer:",
		"1. § 3 affattes således:",
		"»§ 3. Bekendtgørelsen finder ikke anvendelse på nedgravede anlæg.«",
		"»Stk. 2. Bekendtgørelsen finder ikke anvendelse på listevirksomheder, jf. dog stk. 3.",
		"Stk. 3. Ved afgørelser skal",
		"1) vilkårene omfatte §§ 29-34.",
		"2. I § 37 ændres »nr. 6« til: »nr. 5«.",
	}
	edits := ReadChangeLists(lines, "X")
	if len(edits) != 2 || len(edits[0].NewProvisions) != 1 {
		t.Fatalf("edits: %+v", edits)
	}
	p := edits[0].NewProvisions[0]
	if len(p.Stykker) != 3 || p.Text() != "§ 3. Bekendtgørelsen finder ikke anvendelse på nedgravede anlæg. Stk. 2. Bekendtgørelsen finder ikke anvendelse på listevirksomheder, jf. dog stk. 3. Stk. 3. Ved afgørelser skal 1) vilkårene omfatte §§ 29-34." {
		t.Errorf("new § 3 (%d stk): %q", len(p.Stykker), p.Text())
	}
	if edits[0].Definition != "§ 3 affattes således:" || edits[1].Old != "nr. 6" {
		t.Errorf("definitions: %q, %+v", edits[0].Definition, edits[1])
	}
}

// Bekendtgørelse nr. 381 af 2009 names its annex in change 1 only; changes 2-5 say where in it by
// a new point's number, a field's code, a quoted run of table rows and a point to add to. Each
// used to be refused or put at the end of the annex, and "2.1 Apotekere …" in change 1's new text
// was read as change 2.
func TestChangesGoOnInTheAnnexTheFirstNamed(t *testing.T) {
	lines := []string{"§ 1", "I bekendtgørelse nr. 269 af 21. marts 2007 om indberetning foretages følgende ændringer:",
		"1.", "Bilag 1, punkt 2.1 affattes således:", "2.1 Apotekere indberetter elektronisk.",
		"2.", "Som nyt punkt 2.6 indsættes:", "2.6 Virksomheder anvender filnavn H.", "Punkt 2.6-2.7 bliver herefter 2.7-2.8",
		"3.", "I FELT03 Ekspeditionstype, indsættes efter", "» Receptursalg, erhverv", "DI«:", "»Diacetylmorphin", "HE«",
		"4. I FELT08 Identifikation af receptudsteder , indsættes som nyt sidste afsnit:", "»Ved leverancer udfyldes feltet.«",
		"5 . I punkt 4.3 tilføjes:", "»12. Virksomheder (HE).«",
		"§ 2", "Bekendtgørelsen træder i kraft den 1. april 2009."}
	edits := ReadChangeLists(lines, "X")
	if len(edits) != 5 {
		t.Fatalf("%d changes read: %+v", len(edits), edits)
	}
	c := &Content{Appendices: []Appendix{{Number: "1", Segments: []Segment{{Text: "Bilag 1", Break: true},
		{Text: "2.1 Apotekere indberetter.", Break: true}, {Text: "2.5 Femte.", Break: true}, {Text: "2.6 Sjette.", Break: true}, {Text: "2.7 Syvende.", Break: true},
		{Text: "3.3", Break: true}, {Text: "FELT03 Ekspeditionstype", Break: true}, {Text: "Receptursalg, erhverv", Break: true}, {Text: "DI", Break: true},
		{Text: "3.8", Break: true}, {Text: "FELT08 Identifikation af receptudsteder", Break: true}, {Text: "Lægens ydernummer.", Break: true},
		{Text: "3.9", Break: true}, {Text: "FELT09 Lægens CPR-nummer", Break: true},
		{Text: "4.3", Break: true}, {Text: "11. Forhandlere.", Break: true}, {Text: "4.4 Feltudfyldelse", Break: true}}}}}
	for _, e := range edits {
		if out := c.Apply(e); !out.Applied {
			t.Fatalf("change %s refused: %s", e.Number, out.Reason)
		}
	}
	want := "Bilag 1 2.1 Apotekere indberetter elektronisk. 2.5 Femte. 2.6 Virksomheder anvender filnavn H. 2.7 Sjette. 2.8 Syvende. " +
		"3.3 FELT03 Ekspeditionstype Receptursalg, erhverv DI Diacetylmorphin HE " +
		"3.8 FELT08 Identifikation af receptudsteder Lægens ydernummer. Ved leverancer udfyldes feltet. 3.9 FELT09 Lægens CPR-nummer " +
		"4.3 11. Forhandlere. 12. Virksomheder (HE). 4.4 Feltudfyldelse"
	if got := SegmentsText(c.Appendices[0].Segments); got != want {
		t.Errorf("bilag 1:\n got %q\nwant %q", got, want)
	}
}

// "Bilagene 2, 4, 9 og 10 ændres som anført i bilag 2 til denne bekendtgørelse" (bekendtgørelse
// nr. 454 af 2017): the act's annex lists the changes in words, lettered ones inside the part the
// numbered one names, and one quotation printing two whole annexes.
func TestAnAnnexListingChangesIsReadAsChanges(t *testing.T) {
	ap := &Appendix{Number: "2"}
	for _, l := range []string{"I bilagene til bekendtgørelse om humane væv og celler foretages følgende ændringer:",
		"1)", "Bilag 2, del E, ændres således:", "a)", "I punkt 1 tilføjes litra g):", "»g) Den fælles europæiske kode.«",
		"b)", "Punkt 1, andet afsnit, affattes således:", "»Hvis der ikke er plads.", "Følgesedlen pakkes med.«",
		"2) Bilag 9 og 10 affattes således:", "»Bilag 9", "Bivirkninger", "1) Dato", "Bilag 10", "Hændelser", "«"} {
		ap.Segments = append(ap.Segments, Segment{Text: l, Break: true})
	}
	edits, ok := ExpandAnnexChangeList(Edit{Source: "X", Definition: "Bilagene 2, 4, 9 og 10 ændres som anført i bilag 2 til denne bekendtgørelse."}, ap)
	if !ok || len(edits) != 4 {
		t.Fatalf("ok=%v edits=%+v", ok, edits)
	}
	want := []string{"I bilag 2, del E, i punkt 1 tilføjes litra g):", "I bilag 2, del E, punkt 1, andet afsnit, affattes således:", "Bilag 9 affattes således:", "Bilag 10 affattes således:"}
	texts := []string{"g) Den fælles europæiske kode.", "Hvis der ikke er plads. Følgesedlen pakkes med.", "Bilag 9 Bivirkninger 1) Dato", "Bilag 10 Hændelser"}
	for i, e := range edits {
		if e.Definition != want[i] || SegmentsText(e.NewText) != texts[i] {
			t.Errorf("edit %d: %q %q", i, e.Definition, SegmentsText(e.NewText))
		}
	}
}
