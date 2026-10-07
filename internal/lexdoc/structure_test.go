package lexdoc

import (
	"strings"
	"testing"
	"time"
)

// "§ 2, stk. 1, nr. 2, affattes således: 2) …" rewrites one condition. It used to rewrite the
// whole subsection with that one line, deleting the introduction and every other condition while
// reporting the change as applied.
func TestReplacingAnItemKeepsItsSubsection(t *testing.T) {
	c := sampleLaw()
	out := c.Apply(Edit{Kind: EditReplaceProvision, Address: ParseAddress("§ 2, stk. 1, nr. 2"),
		Definition: "§ 2, stk. 1, nr. 2, affattes således:", NewProse: []string{"2) Nr to, ny."}})
	if !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	p := mustFindProvision(t, c, "§ 2")
	got := p.SpanText(p.Stykke(1).Span)
	if !strings.Contains(got, "Første stykke.") || !strings.Contains(got, "1) Nr een.") || !strings.Contains(got, "2) Nr to, ny.") || strings.Contains(got, "2) Nr to.") {
		t.Errorf("stk. 1 after replacing nr. 2 = %q", got)
	}
	st := p.Stykke(1)
	if len(st.Items) != 2 || p.SpanText(st.Items[1].Span) != "2) Nr to, ny." {
		t.Errorf("items after the replacement: %+v", st.Items)
	}
	if got := p.SpanText(p.Stykke(2).Span); got != "Andet stykke." {
		t.Errorf("stk. 2 moved: %q", got)
	}
}

// Within one amending act every address is read against the law as it stood before that act.
func TestOneActCitesTheNumberingBeforeIt(t *testing.T) {
	c := sampleLaw()
	view, cons := c.AsOf([]Amendment{{Edits: []Edit{
		{Kind: EditInsertProvision, Address: ParseAddress("§ 2, stk. 1"), Definition: "I § 2 indsættes efter stk. 1 som nyt stykke:",
			NewStykker: [][]Segment{{{Text: "Nyt stykke.", Break: true}}}},
		// The former stk. 2, which is stk. 3 by now.
		{Kind: EditReplaceText, Address: ParseAddress("§ 2, stk. 2"), Definition: "I § 2, stk. 2, der bliver stk. 3, ændres »Andet« til: »Det andet«.",
			Old: "Andet", New: "Det andet"},
	}}}, time.Time{})
	if len(cons.Unresolved) > 0 {
		t.Fatalf("refused: %+v", cons.Unresolved)
	}
	p := mustFindProvision(t, view, "§ 2")
	if got := p.SpanText(p.Stykke(3).Span); got != "Det andet stykke." {
		t.Errorf("stk. 3 = %q", got)
	}
	if got := p.SpanText(p.Stykke(2).Span); got != "Nyt stykke." {
		t.Errorf("stk. 2 = %q", got)
	}
}

// A heading inserted between provisions takes the provisions printed after it; a chapter inserted
// after a provision goes beside the chapter that provision sits in.
func TestInsertedHeadingTakesWhatFollows(t *testing.T) {
	c := sampleLaw()
	out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress("§ 1"), Definition: "Efter § 1 indsættes:",
		NewParts: []*Part{{Kind: PartGroup, Heading: "Nye regler"}}})
	if !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if len(c.Order) != 3 || c.Order[1].Part == nil || c.Order[1].Part.Heading != "Nye regler" {
		t.Fatalf("order after the insertion: %+v", c.Order)
	}
	// § 2 printed after the new heading, so it sits under it now; chapter 1 outranks a group and
	// keeps its own place.
	if g := c.Order[1].Part; len(g.Order) != 1 || g.Order[0].Provision.Number != "2" {
		t.Errorf("the new group holds %+v", g.Order)
	}
	if c.Order[2].Part == nil || c.Order[2].Part.Kind != PartChapter {
		t.Errorf("chapter 1 moved: %+v", c.Order[2])
	}
}

func TestChapterInsertedAfterAProvisionGoesBesideItsChapter(t *testing.T) {
	c := sampleLaw()
	newP := &Provision{Number: "3a", Segments: []Segment{{Text: "§ 3 a. Ny."}}}
	out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress("§ 3"), Definition: "Efter § 3 indsættes:",
		NewParts: []*Part{{Kind: PartChapter, Number: "1a", Heading: "Nyt kapitel", Order: []Node{{Provision: newP}}}}})
	if !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if len(c.Order) != 4 || c.Order[3].Part == nil || c.Order[3].Part.Number != "1a" {
		t.Fatalf("order: %+v", c.Order)
	}
	if _, ok := c.Find(ParseAddress("§ 3 a")); !ok {
		t.Error("the provision the chapter brought is not addressable")
	}
	if _, ok := c.Find(ParseAddress("kapitel 1 a")); !ok {
		t.Error("kapitel 1 a is not addressable")
	}
}

// "Pkt. 3 i bilag 1-7 ophæves" resolves to bilag 1, and "Kapitel 1 a ophæves" must not resolve
// to kapitel 1. Neither may take the whole unit with it.
func TestPartOfAUnitNeverRemovesTheUnit(t *testing.T) {
	c := sampleLaw()
	if out := c.Apply(Edit{Kind: EditDeleteProvision, Address: ParseAddress("Pkt.3 i bilag 1-7 ophæves."), Definition: "Pkt.3 i bilag 1-7 ophæves."}); out.Applied {
		t.Error("a point inside an annex removed the annex")
	}
	if len(c.Appendices) != 1 {
		t.Fatal("bilag 1 is gone")
	}
	if out := c.Apply(Edit{Kind: EditDeleteProvision, Address: ParseAddress("Kapitel 1 a ophæves."), Definition: "Kapitel 1 a ophæves."}); out.Applied {
		t.Error("kapitel 1 a removed kapitel 1")
	}
	if out := c.Apply(Edit{Kind: EditDeleteProvision, Address: ParseAddress("Bilag 1 ophæves."), Definition: "Bilag 1 ophæves."}); !out.Applied || len(c.Appendices) != 0 {
		t.Errorf("a whole annex was not repealed: %+v", out)
	}
	if out := c.Apply(Edit{Kind: EditDeleteProvision, Address: ParseAddress("Kapitel 1 ophæves."), Definition: "Kapitel 1 ophæves."}); !out.Applied {
		t.Errorf("a whole chapter was not repealed: %+v", out)
	}
	if _, ok := c.Find(ParseAddress("§ 3")); ok {
		t.Error("the repealed chapter's provision is still there")
	}
}

func TestBeforeMeansBefore(t *testing.T) {
	c := sampleLaw()
	newP := &Provision{Number: "1a", Segments: []Segment{{Text: "§ 1 a. Ny."}}}
	out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress("§ 2"), Definition: "Før § 2 indsættes:", NewProvisions: []*Provision{newP}})
	if !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if c.Order[1].Provision == nil || c.Order[1].Provision.Number != "1a" {
		t.Errorf("§ 1 a is not in front of § 2: %+v", c.Order)
	}
}

func TestPreambleAndTitleAreReplaced(t *testing.T) {
	c := sampleLaw()
	c.Apply(Edit{Kind: EditReplaceProvision, Address: ParseAddress("Indledningen affattes således:"), NewText: []Segment{{Text: "Ny indledning."}}})
	c.Apply(Edit{Kind: EditReplaceProvision, Address: ParseAddress("Lovens titel affattes således:"), NewProse: []string{"Lov om noget andet"}})
	if SegmentsText(c.Preamble) != "Ny indledning." || SegmentsText(c.Title) != "Lov om noget andet" {
		t.Errorf("preamble %q, title %q", SegmentsText(c.Preamble), SegmentsText(c.Title))
	}
}

func TestSentencesAreCountedPastLabelsAndAbbreviations(t *testing.T) {
	text := "Stk. 2. Ministeren kan efter forhandling med kommunerne, jf. Rådets direktiv nr. 5, fastsætte regler. Reglerne gælder også for § 3. Dette gælder ikke for Grønland."
	got := []string{}
	for _, sp := range sentenceSpans(text) {
		got = append(got, text[sp[0]:sp[1]])
	}
	want := []string{
		"Ministeren kan efter forhandling med kommunerne, jf. Rådets direktiv nr. 5, fastsætte regler.",
		"Reglerne gælder også for § 3.",
		"Dette gælder ikke for Grønland.",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("sentences:\n got %q\nwant %q", got, want)
	}
}

// "§ 2, stk. 1, 2. pkt., affattes således" rewrites one sentence. It used to replace the whole
// subsection with that one sentence, reported as applied.
func TestReplacingASentenceKeepsTheOthers(t *testing.T) {
	c := &Content{Order: []Node{{Provision: &Provision{Number: "2", Printed: "§ 2.",
		Segments: []Segment{{Text: "§ 2. Første sætning gælder. Anden sætning gælder. Tredje sætning gælder."}},
		Stykker:  []Stykke{{Nr: 1, Span: Span{From: 0, To: 1}}}}}}}
	out := c.Apply(Edit{Kind: EditReplaceProvision, Address: ParseAddress("§ 2, stk. 1, 2. pkt., affattes således:"),
		Definition: "§ 2, stk. 1, 2. pkt., affattes således:", NewText: []Segment{{Text: "»Ny anden sætning.«"}}})
	if !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := mustFindProvision(t, c, "§ 2").Text(); got != "§ 2. Første sætning gælder. Ny anden sætning. Tredje sætning gælder." {
		t.Errorf("text = %q", got)
	}
	out = c.Apply(Edit{Kind: EditDeleteProvision, Address: ParseAddress("§ 2, 3. pkt., ophæves."), Definition: "§ 2, 3. pkt., ophæves."})
	if !out.Applied {
		t.Fatalf("repeal refused: %s", out.Reason)
	}
	out = c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress("I § 2 indsættes som 2. pkt.:"), Definition: "I § 2 indsættes som 2. pkt.:",
		NewProse: []string{"Indsat sætning."}})
	if !out.Applied {
		t.Fatalf("insertion refused: %s", out.Reason)
	}
	if got := mustFindProvision(t, c, "§ 2").Text(); got != "§ 2. Første sætning gælder. Indsat sætning. Ny anden sætning." {
		t.Errorf("text = %q", got)
	}
	if out := c.Apply(Edit{Kind: EditDeleteProvision, Address: ParseAddress("§ 2, 7. pkt., ophæves."), Definition: "§ 2, 7. pkt., ophæves."}); out.Applied {
		t.Error("a sentence the provision does not have was repealed")
	}
}

// "Overskriften til kapitel 6 ophæves, og i stedet indsættes: Kapitel 6 Henstand … § 17. …" brings
// the chapter's new provisions with its heading. Read as the heading alone, virksomhedsskatteloven's
// new § 17 became a long rubric and later changes to it were refused.
func TestChapterHeadingBringsProvisionsInPlaceOfRepealedOnes(t *testing.T) {
	stub := &Provision{Number: "4", Segments: []Segment{{Text: "§ 4. (Ophævet)."}}}
	c := &Content{Order: []Node{{Part: &Part{Kind: PartChapter, Number: "2", Heading: "Gammel", Order: []Node{{Provision: stub}}}}}}
	newP := &Provision{Number: "4", Segments: []Segment{{Text: "§ 4. Ny regel om henstand."}}}
	out := c.Apply(Edit{Kind: EditReplaceProvision, Address: ParseAddress("Overskriften til kapitel 2"),
		Definition: "Overskriften til kapitel 2 ophæves, og i stedet indsættes:", New: "Henstand",
		NewParts: []*Part{{Kind: PartChapter, Number: "2", Heading: "Henstand", Order: []Node{{Provision: newP}}}}})
	if !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if h := c.Order[0].Part.Heading; h != "Henstand" {
		t.Errorf("heading = %q", h)
	}
	if got := Squeeze(mustFindProvision(t, c, "§ 4").Text()); !strings.Contains(got, "Ny regel om henstand") {
		t.Errorf("§ 4 = %q", got)
	}
	// A number still in force is not taken over.
	newP.Number = "4"
	stub.Segments = []Segment{{Text: "§ 4. Gældende regel."}}
	if out := c.Apply(Edit{Kind: EditReplaceProvision, Address: ParseAddress("Overskriften til kapitel 2"),
		Definition: "Overskriften til kapitel 2 ophæves, og i stedet indsættes:", New: "Andet",
		NewParts: []*Part{{Kind: PartChapter, Number: "2", Heading: "Andet", Order: []Node{{Provision: newP}}}}}); out.Applied {
		t.Error("a provision in force was overwritten")
	}
}

// "§ 2, stk. 1, sidste pkt., ophæves" repeals the subsection's last sentence. Read without it, the
// address named all of stk. 1, and the subsection went.
func TestTheLastSentenceIsCountedFromTheEnd(t *testing.T) {
	p := &Provision{Number: "2", Printed: "§ 2.", Segments: []Segment{
		{Text: "§ 2.", Break: true}, {Text: "Avlere skal registreres. Registreringen sker som led i projektet. Den slutter i 2008.", Break: true},
		{Text: "Stk. 2.", Break: true}, {Text: "Andet stykke.", Break: true}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	if out := c.Apply(Edit{Kind: EditDeleteProvision, Address: ParseAddress("§ 2, stk. 1, sidste pkt., ophæves"), Definition: "§ 2, stk. 1, sidste pkt., ophæves."}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := textOf(t, c, "§ 2, stk. 1"); !strings.Contains(got, "som led i projektet") || strings.Contains(got, "slutter") {
		t.Errorf("stk. 1 = %q, want the last sentence gone and the rest kept", got)
	}
	if out := c.Apply(Edit{Kind: EditReplaceText, Address: ParseAddress("I § 2, stk. 1, næstsidste pkt., ændres »Avlere« til: »Biavlere«."),
		Definition: "I § 2, stk. 1, næstsidste pkt., ændres »Avlere« til: »Biavlere«.", Old: "Avlere", New: "Biavlere"}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := textOf(t, c, "§ 2, stk. 1"); !strings.Contains(got, "Biavlere skal registreres") {
		t.Errorf("stk. 1 = %q", got)
	}
}

// An annex prints its number and its parts' labels, so a renumbering of them is carried out.
func TestAnAnnexRenumberingRelabels(t *testing.T) {
	c := &Content{Appendices: []Appendix{
		{Number: "1", Label: "Bilag 1", Segments: []Segment{{Text: "Bilag 1"}, {Text: "18."}, {Text: "For registrering betales:"},
			{Text: "a)"}, {Text: "1.000 kr."}, {Text: "b)"}, {Text: "600 kr."}, {Text: "c)"}, {Text: "720 kr."}, {Text: "19."}, {Text: "Andet."}}},
		{Number: "4", Label: "Bilag 4", Segments: []Segment{{Text: "Bilag 4"}, {Text: "Skema."}}},
	}}
	e := Edit{Kind: EditDeleteProvision, Address: ParseAddress("Bilag 1, nr. 18, litra a, ophæves."), Definition: "Bilag 1, nr. 18, litra a, ophæves.",
		Prose: "1. Bilag 1, nr. 18, litra a, ophæves. Litra b–c bliver herefter litra a–b."}
	if out := c.Apply(e); !out.Applied {
		t.Fatalf("repeal refused: %s", out.Reason)
	}
	r := Edit{Kind: EditRenumber, Definition: "Litra b–c bliver herefter litra a–b.", Prose: e.Prose}
	if out := c.Apply(r); !out.Applied {
		t.Fatalf("renumbering refused: %s", out.Reason)
	}
	got := SegmentsText(c.Appendices[0].Segments)
	if !strings.Contains(got, "a)600 kr.") || !strings.Contains(got, "b)720 kr.") || strings.Contains(got, "c)") {
		t.Errorf("bilag 1 = %q", got)
	}
	if out := c.Apply(Edit{Kind: EditRenumber, Definition: "Bilag 4 bliver herefter til bilag 3."}); !out.Applied || c.Appendices[1].Number != "3" {
		t.Errorf("bilag 4 not renumbered: %+v %s", out, c.Appendices[1].Number)
	}
}

// A balance schema in an annex is scoped by headings: "1. Skema for balance i kontoform
// (regnskabsklasse B, C og D), PASSIVER, EGENKAPITAL, IV. Andre reserver". Each ends at the next
// heading of its shape, and the renumbering the insertion brings relabels the parts after it.
func TestAnAnnexSchemaIsScopedByItsHeadings(t *testing.T) {
	lines := []string{"Bilag 2", "1. Skema for balance i kontoform (regnskabsklasse B, C og D)", "PASSIVER", "EGENKAPITAL",
		"IV. Andre reserver", "1.", "Reserve for opskrivning", "2.", "Reserve for egne kapitalandele", "3.", "Øvrige lovpligtige reserver",
		"V. Overført overskud", "HENSATTE FORPLIGTELSER", "1.", "Hensættelser til pensioner", "2.", "Hensættelser til skat",
		"2. Skema for balance i beretningsform (regnskabsklasse B, C og D)", "EGENKAPITAL", "IV. Andre reserver", "1.", "Reserve", "2.", "Andet", "3.", "Tredje"}
	var segs []Segment
	for _, l := range lines {
		segs = append(segs, Segment{Text: l, Break: true})
	}
	c := &Content{Appendices: []Appendix{{Number: "2", Label: "Bilag 2", Segments: segs}}}
	def := "I Bilag 2, 1. Skema for balance i kontoform (regnskabsklasse B, C og D), PASSIVER, EGENKAPITAL, IV. Andre reserver, indsættes efter nr. 2 som nyt nummer:"
	e := Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewProse: []string{"3.", "Reserve for udlån"},
		Prose: "37. " + def + " »3. Reserve for udlån« Nr. 3 bliver herefter nr. 4."}
	if out := c.Apply(e); !out.Applied {
		t.Fatalf("insertion refused: %s", out.Reason)
	}
	if out := c.Apply(Edit{Kind: EditRenumber, Definition: "Nr. 3 bliver herefter nr. 4.", Prose: e.Prose}); !out.Applied {
		t.Fatalf("renumbering refused: %s", out.Reason)
	}
	got := SegmentsText(c.Appendices[0].Segments)
	for _, want := range []string{"2. Reserve for egne kapitalandele 3. Reserve for udlån 4. Øvrige lovpligtige reserver", "2. Andet 3. Tredje"} {
		if !strings.Contains(Squeeze(got), want) {
			t.Errorf("bilag 2 lacks %q: %s", want, Squeeze(got))
		}
	}
}

// "I bilag 3, nr. 1, indsættes efter litra a som nyt litra: b) … Litra b-e bliver herefter litra
// c-f" goes after a), not at the end of nr. 1; and "punkt 67, 2. pkt., ophæves" takes one sentence.
func TestAnAnnexPartIsPlacedByItsLetterAndCutBySentence(t *testing.T) {
	lines := []string{"Bilag 3", "1) Kunderisikofaktorer:", "a)", "Usædvanlige omstændigheder.", "b)", "Formueforvaltningsselskaber.",
		"c)", "Kontantbaserede virksomheder.", "2) Produkter:", "a)", "Private banking", "67.", "Første sætning. Anden sætning. Tredje sætning.", "68.", "Andet."}
	var segs []Segment
	for _, l := range lines {
		segs = append(segs, Segment{Text: l, Break: true})
	}
	c := &Content{Appendices: []Appendix{{Number: "3", Label: "Bilag 3", Segments: segs}}}
	def := "I bilag 3, nr. 1, indsættes efter litra a som nyt litra:"
	prose := "103. " + def + " b) Kunder i højrisikoområder. Litra b-c bliver herefter litra c-d."
	if out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewProse: []string{"b)", "Kunder i højrisikoområder."}, Prose: prose}); !out.Applied {
		t.Fatalf("insertion refused: %s", out.Reason)
	}
	if out := c.Apply(Edit{Kind: EditRenumber, Definition: "Litra b-c bliver herefter litra c-d.", Prose: prose}); !out.Applied {
		t.Fatalf("renumbering refused: %s", out.Reason)
	}
	got := Squeeze(SegmentsText(c.Appendices[0].Segments))
	if !strings.Contains(got, "a) Usædvanlige omstændigheder. b) Kunder i højrisikoområder. c) Formueforvaltningsselskaber. d) Kontantbaserede") {
		t.Errorf("nr. 1 = %s", got)
	}
	cut := "Bilag 3, punkt 67, 2. pkt., ophæves."
	if out := c.Apply(Edit{Kind: EditDeleteProvision, Address: ParseAddress(cut), Definition: cut}); !out.Applied {
		t.Fatalf("sentence repeal refused: %s", out.Reason)
	}
	got = Squeeze(SegmentsText(c.Appendices[0].Segments))
	if !strings.Contains(got, "67. Første sætning. Tredje sætning. 68. Andet.") {
		t.Errorf("punkt 67 = %s", got)
	}
}

// "I bilag 1, i afsnit B. Det økonomisk-merkantile område, Erhvervsakademiuddannelsen inden for
// handel (Handelsøkonom AK) affattes reglerne for praktikken således:" rewrites the lines under one
// entry's "Praktik:" and nothing of the next entry.
func TestTheRulesUnderAnEntrysHeadingAreRewritten(t *testing.T) {
	lines := []string{"Bilag 1", "B.", "Det økonomisk-merkantile område", "Erhvervsakademiuddannelser",
		"B.", "Det økonomisk-merkantile område", "Erhvervsakademiuddannelser",
		"Erhvervsakademiuddannelse inden for handel (Handelsøkonom AK)", "Uddannelsens navn:", "Handel", "Praktik:", "Gammel regel.", "Gammel regel to.",
		"Erhvervsakademiuddannelse inden for logistik (Logistikøkonom AK)", "Uddannelsens navn:", "Logistik", "Praktik:", "Uberørt regel."}
	var segs []Segment
	for _, l := range lines {
		segs = append(segs, Segment{Text: l, Break: true})
	}
	c := &Content{Appendices: []Appendix{{Number: "1", Label: "Bilag 1", Segments: segs}}}
	def := "I bilag 1 , i afsnit B. Det økonomisk-merkantile område , Erhvervsakademiuddannelsen inden for handel (Handelsøkonom AK) affattes reglerne for praktikken således:"
	if out := c.Apply(Edit{Kind: EditReplaceProvision, Address: ParseAddress(def), Definition: def, NewProse: []string{"Ny regel."}}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	got := Squeeze(SegmentsText(c.Appendices[0].Segments))
	if !strings.Contains(got, "Praktik: Ny regel. Erhvervsakademiuddannelse inden for logistik") || !strings.Contains(got, "Uberørt regel.") || strings.Contains(got, "Gammel") {
		t.Errorf("bilag 1 = %s", got)
	}
}

// "I bilag 10 ophæves de to sidste afsnit" counts the paragraphs the source marks, not its lines.
func TestTheLastParagraphsOfAnAnnexAreCountedAsParagraphs(t *testing.T) {
	segs := []Segment{{Text: "Bilag 10", Break: true, Para: true}, {Text: "6.", Break: true}, {Text: "Egenkapitalforrentning.", Break: true, Para: true},
		{Text: "Nøgletallet opgøres så.", Para: true}, {Text: "Kravet er det indberettede.", Break: true}, {Text: "Nøgletallet er undtaget.", Para: true}}
	c := &Content{Appendices: []Appendix{{Number: "10", Label: "Bilag 10", Segments: segs}}}
	def := "I bilag 10 ophæves de to sidste afsnit."
	if out := c.Apply(Edit{Kind: EditDeleteProvision, Address: ParseAddress(def), Definition: def}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := Squeeze(SegmentsText(c.Appendices[0].Segments)); got != "Bilag 10 6. Egenkapitalforrentning." {
		t.Errorf("bilag 10 = %q", got)
	}
	// Without the marks there is nothing to count by.
	c = &Content{Appendices: []Appendix{{Number: "10", Label: "Bilag 10", Segments: []Segment{{Text: "Bilag 10"}, {Text: "Et."}, {Text: "To."}}}}}
	if out := c.Apply(Edit{Kind: EditDeleteProvision, Address: ParseAddress(def), Definition: def}); out.Applied {
		t.Error("paragraphs counted in an annex that does not mark them")
	}
}

// A pest list scopes its rows by section, lettered group and the group's heading, whose own commas
// are no separators; a lettered group runs to the next letter, past an abbreviation like "f. sp.".
func TestAPestListIsScopedBySectionAndGroup(t *testing.T) {
	ch, ok := ParseAnnexPart("I bilag 2, I. a. Insekter, mider og nematoder på alle udviklingsstadier indsættes efter nr. 10 som nr. 10 a, bilag 1 til denne bekendtgørelse.")
	if !ok || len(ch.Scopes) != 2 || ch.Scopes[0] != "I" || ch.Scopes[1] != "a)" || ch.Label != "10" || ch.Source != 1 {
		t.Errorf("parsed %+v", ch)
	}
	if ch, ok := ParseAnnexPart("I bilag 4, Særlige krav indsættes efter nr. 11.2 som nr. 11.2.a bilag 2 til denne bekendtgørelse."); !ok || ch.Source != 2 {
		t.Errorf("act's annex not read without its comma: %+v", ch)
	}
	lines := []string{"Bilag 2", "II. Planteskadegørere", "c) Svampe", "1.", "Ceratocystis fimbriata", "f. sp.", "platani",
		"2.", "Colletotrichum acutatum", "3.", "Cryphonectria parasitica", "d) Virus", "1.", "Andet"}
	var segs []Segment
	for _, l := range lines {
		segs = append(segs, Segment{Text: l, Break: true})
	}
	c := &Content{Appendices: []Appendix{{Number: "2", Label: "Bilag 2", Segments: segs}}}
	def := "Bilag 2, afsnit II, litra c), nr. 2, udgår."
	if out := c.Apply(Edit{Kind: EditDeleteProvision, Address: ParseAddress(def), Definition: def}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := Squeeze(SegmentsText(c.Appendices[0].Segments)); strings.Contains(got, "Colletotrichum") || !strings.Contains(got, "platani 3. Cryphonectria") {
		t.Errorf("bilag 2 = %s", got)
	}
}

// A heading's own commas leave lower-case pieces that continue it, not headings of their own; and
// "indsættes i afsnit II., Beskrivelser, nr. 2, et nyt litra g" names where the new part goes.
func TestAnAnnexInsertionsPlaceIsReadWhereverItIsNamed(t *testing.T) {
	ch, ok := ParseAnnexPart("I bilag 1, afsnit II. Planteskadegørere, der vides at forekomme i Det Europæiske Fællesskab, litra a) Insekter, mider og nematoder på alle udviklingsstadier , indsættes efter nr. 8.1 som nr. 8 a:")
	if !ok || len(ch.Scopes) != 2 || ch.Scopes[0] != "II" || ch.Scopes[1] != "a)" || ch.Label != "8.1" {
		t.Errorf("parsed %+v", ch)
	}
	ch, ok = ParseAnnexPart("I bilag 5, Planter m.m., der ved indførsel skal være ledsaget af et plantesundhedscertifikat , indsættes i afsnit II., Beskrivelser, nr. 2 , et nyt litra g , der affattes på følgende måde:")
	if !ok || len(ch.Scopes) < 3 || ch.Scopes[len(ch.Scopes)-1] != "2" {
		t.Errorf("parsed %+v", ch)
	}
	if _, ok := ParseAnnexPart("I bilag 5 indsættes i afsnit II noget uden plads:"); ok {
		t.Error("a place named after the verb but not read was taken as the end of the annex")
	}
}

// A text that prints an afsnit only as a heading line, "III. Administrative bestemmelser …", has
// that line rewritten by "Overskriften til afsnit III affattes således".
func TestAnAfsnitPrintedOnlyAsAHeadingLine(t *testing.T) {
	p := &Provision{Number: "49a", Printed: "§ 49 a.", Segments: []Segment{{Text: "§ 49 a. Ved udsletning finder § 20 anvendelse.", Break: true},
		{Text: "III. Administrative bestemmelser m.v.", Break: true}}}
	c := &Content{Order: []Node{{Provision: p}}}
	def := "Overskriften til afsnit III affattes således:"
	if out := c.Apply(Edit{Kind: EditReplaceProvision, Address: ParseAddress(def), Definition: def, New: "III. Fælles bestemmelser"}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := Squeeze(SegmentsText(p.Segments)); !strings.HasSuffix(got, "III. Fælles bestemmelser") {
		t.Errorf("§ 49 a = %q", got)
	}
}

// SOLAS annexes print chapters "Afsnit B-4" and rules "Regel 19-1", listed first in a table of
// contents, and a rule's paragraphs as numbers on lines of their own.
func TestARulesHeadingAndParagraphAreItsOwn(t *testing.T) {
	lines := []string{"Bilag 3", "Afsnit B-4", "Regel 19", "Regel 20",
		"Afsnit B-4 Stabilitetsoplysninger", "Regel 19 Havarikontrolplaner", "1", "Planer skal findes.",
		"Regel 19-1 – Havarikontrolplaner for passagerskibe", "1", "Denne regel gælder for passagerskibe.", "2", "Andet stykke.",
		"Afdeling III – Lastskibe", "Regel 20 Lastning", "1", "Lastning sker forsvarligt."}
	var segs []Segment
	for _, l := range lines {
		segs = append(segs, Segment{Text: l, Break: true})
	}
	c := &Content{Appendices: []Appendix{{Number: "3", Label: "Bilag 3", Segments: segs}}}
	heading := "I Bilag 3, afsnit B-4, regel 19-1 affattes overskriften således:"
	if out := c.Apply(Edit{Kind: EditReplaceProvision, Address: ParseAddress(heading), Definition: heading, NewProse: []string{"Regel 19-1 - Øvelser i havarikontrol"}}); !out.Applied {
		t.Fatalf("heading refused: %s", out.Reason)
	}
	para := "Bilag 3, afsnit B-4, regel 19-1, stk. 2 affattes således:"
	if out := c.Apply(Edit{Kind: EditReplaceProvision, Address: ParseAddress(para), Definition: para, NewProse: []string{"2", "Nyt stykke."}}); !out.Applied {
		t.Fatalf("paragraph refused: %s", out.Reason)
	}
	got := Squeeze(SegmentsText(c.Appendices[0].Segments))
	if !strings.Contains(got, "Regel 19-1 - Øvelser i havarikontrol 1 Denne regel gælder for passagerskibe. 2 Nyt stykke. Afdeling III – Lastskibe") {
		t.Errorf("bilag 3 = %s", got)
	}
	if _, ok := ParseAnnexPart("Bilag 3, afsnit B-4, regel 19-1, stk. 1, nr. 2 affattes således:"); ok {
		t.Error("an unread part inside a rule's paragraph was taken for the paragraph")
	}
}

// Retsplejeloven's books hold afsnit numbered in words, and an act names them so: "Fjerde bog,
// tredje og fjerde afsnit, ophæves, og i stedet indsættes", "Overskriften til fjerde bog, andet
// afsnit, affattes således".
func TestABookAndItsAfsnitAreAddressedByOrdinalWords(t *testing.T) {
	a := ParseAddress("Fjerde bog, tredje og fjerde afsnit , ophæves, og i stedet indsættes:")
	if a.Book != 4 || a.Afsnit != 3 || a.AfsnitTo != 4 {
		t.Fatalf("address = %+v", a)
	}
	chapter := func(n string) Node {
		return Node{Part: &Part{Kind: PartChapter, Number: n, Order: []Node{{Provision: &Provision{Number: n + "0", Segments: []Segment{{Text: "§ " + n + "0. Tekst."}}}}}}}
	}
	afsnit := func(n, printed, heading string, chs ...Node) Node {
		return Node{Part: &Part{Kind: PartAfsnit, Number: n, Printed: printed, Heading: heading, Order: chs}}
	}
	book := &Part{Kind: PartBook, Number: "4", Printed: "Fjerde bog", Order: []Node{
		afsnit("2", "Andet afsnit", "Gammel", chapter("1")), afsnit("3", "Tredje afsnit", "Tre", chapter("2")),
		afsnit("4", "Fjerde afsnit", "Fire", chapter("3")), afsnit("5", "Femte afsnit", "Fem", chapter("4"))}}
	c := &Content{Order: []Node{{Part: book}}}
	h := "Overskriften til fjerde bog, andet afsnit , affattes således:"
	if out := c.Apply(Edit{Kind: EditReplaceProvision, Address: ParseAddress(h), Definition: h, New: "Andet afsnit. Efterforskning, tvangsindgreb m.v."}); !out.Applied {
		t.Fatalf("heading refused: %s", out.Reason)
	}
	if book.Order[0].Part.Heading != "Efterforskning, tvangsindgreb m.v." {
		t.Errorf("heading = %q", book.Order[0].Part.Heading)
	}
	r := "Fjerde bog, tredje og fjerde afsnit , ophæves, og i stedet indsættes:"
	newPart := &Part{Kind: PartAfsnit, Number: "3", Printed: "Tredje afsnit", Heading: "Ny", Order: []Node{chapter("76")}}
	if out := c.Apply(Edit{Kind: EditReplaceProvision, Address: ParseAddress(r), Definition: r, NewParts: []*Part{newPart}}); !out.Applied {
		t.Fatalf("replacement refused: %s", out.Reason)
	}
	var got []string
	for _, n := range book.Order {
		got = append(got, n.Part.Number+":"+n.Part.Heading)
	}
	if strings.Join(got, " ") != "2:Efterforskning, tvangsindgreb m.v. 3:Ny 5:Fem" {
		t.Errorf("fjerde bog = %v", got)
	}
}

// A change list's new afsnit numbered in words holds its chapters, each with the heading printed
// under its number.
func TestANewAfsnitInWordsHoldsItsHeadedChapters(t *testing.T) {
	got := ReadChangeLists([]string{
		"§ 1",
		"I retsplejeloven, jf. lovbekendtgørelse nr. 910 af 27. september 2005, foretages følgende ændring:",
		"1. Fjerde bog, tredje og fjerde afsnit, ophæves, og i stedet indsættes:",
		"»Tredje afsnit. Tiltale og hovedforhandling i 1. instans",
		"Kapitel 76",
		"Tilståelsessager",
		"§ 831. Afgiver sigtede en tilståelse, kan sagen fremmes.«",
		"§ 2",
		"Loven træder i kraft den 1. januar 2007.",
	}, "A20060053830")
	if len(got) != 1 || len(got[0].NewParts) != 1 {
		t.Fatalf("changes %+v", got)
	}
	a := got[0].NewParts[0]
	if a.Kind != PartAfsnit || a.Number != "3" || len(a.Order) != 1 || a.Order[0].Part.Heading != "Tilståelsessager" {
		t.Errorf("new afsnit = %+v", a)
	}
}

// "Overskriften før § 145 ophæves" where § 145 opens kapitel 8 and the heading printed before it
// is the chapter's title: the title goes, the chapter and § 145 stay.
func TestTheHeadingBeforeAChaptersFirstProvisionIsTheChaptersTitle(t *testing.T) {
	p := &Provision{Number: "145", Printed: "§ 145.", Segments: []Segment{{Text: "§ 145. Halvårsrapporten."}}, Stykker: []Stykke{{Nr: 1, Span: Span{From: 0, To: 1}}}}
	ch := &Part{Kind: PartChapter, Number: "8", Printed: "Kapitel 8", Heading: "Udarbejdelse af halvårsrapport", Order: []Node{{Part: &Part{Kind: PartGroup, Order: []Node{{Provision: p}}}}}}
	c := &Content{Order: []Node{{Part: ch}}}
	def := "Overskriften før § 145 ophæves."
	if out := c.Apply(Edit{Kind: EditDeleteProvision, Address: ParseAddress(def), Definition: def}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if ch.Heading != "" || textOf(t, c, "§ 145") != "§ 145. Halvårsrapporten." {
		t.Errorf("chapter heading %q, § 145 %q", ch.Heading, textOf(t, c, "§ 145"))
	}
}

// "Efter § 3 indsættes i kapitel 1: »§ 3 a. Arrangører …«" donated as a subsection, its label and
// full stop printed apart: a provision of its own, not words of § 3.
func TestAProvisionDonatedAsASubsectionIsAProvision(t *testing.T) {
	c := sampleLaw()
	def := "Efter § 1 indsættes i kapitel 1 :"
	out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def,
		NewStykker: [][]Segment{{{Text: "§ 1 a"}, {Text: "."}, {Text: "Arrangører er ikke omfattet."}}}})
	if !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := textOf(t, c, "§ 1a"); !strings.Contains(got, "Arrangører er ikke omfattet.") {
		t.Errorf("§ 1 a = %q", got)
	}
	if strings.Contains(textOf(t, c, "§ 1"), "Arrangører") {
		t.Errorf("§ 1 holds the new text: %q", textOf(t, c, "§ 1"))
	}
}

// "§ 7, stk. 3, affattes således: Stk. 3. En institution, der ansøger om kompensation …" where § 7
// has two subsections and § 6, stk. 3, opens with the new text's words.
func TestASubsectionRewrittenUnderTheNeighboursNumber(t *testing.T) {
	mk := func(n string, third string) *Provision {
		p := &Provision{Number: n, Printed: "§ " + n + ".", Segments: []Segment{{Text: "§ " + n + "."}, {Text: "Første."}, {Text: "Stk. 2."}, {Text: "Andet."}}}
		if third != "" {
			p.Segments = append(p.Segments, Segment{Text: "Stk. 3."}, Segment{Text: third})
		}
		p.Normalise()
		return p
	}
	six, seven := mk("6", "En institution, der ansøger om kompensation for perioden fra den 30. august, må ikke have omsætning."), mk("7", "")
	c := &Content{Order: []Node{{Provision: six}, {Provision: seven}}}
	def := "§ 7, stk. 3, affattes således:"
	out := c.Apply(Edit{Kind: EditReplaceProvision, Address: ParseAddress(def), Definition: def,
		NewStykker: [][]Segment{{{Text: "Stk. 3."}, {Text: "En institution, der ansøger om kompensation inden for perioden, må ikke have omsætning."}}},
		NewText:    []Segment{{Text: "Stk. 3. En institution, der ansøger om kompensation inden for perioden, må ikke have omsætning."}}})
	if !out.Applied || !strings.Contains(six.Text(), "inden for perioden") || strings.Contains(seven.Text(), "institution") {
		t.Errorf("outcome %+v\n§ 6 %q\n§ 7 %q", out, six.Text(), seven.Text())
	}
}
