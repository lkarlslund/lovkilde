package lexdoc

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Tests for the readings the refusal census taught: each case is a shape a real amending act
// printed, which the engine used to refuse or, worse, carry out somewhere else.

func addressStrings(as []Address) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.String())
	}
	return out
}

func TestBecomesClausesAreReadPast(t *testing.T) {
	for def, want := range map[string][]string{
		// The new numbers are the act's own bookkeeping; the places are the old numbers.
		"I § 20, stk. 3, der bliver stk. 2, stk. 5, nr. 1, der bliver stk. 4, nr. 1, og stk. 6, der bliver stk. 5, ændres »a« til: »b«.": {
			"§ 20, stk. 3", "§ 20, stk. 5, nr. 1", "§ 20, stk. 6"},
		// Two units renamed at once: the second new number is not a third place.
		"I § 118, stk. 6, 1. pkt., og stk. 7, der bliver stk. 3, 1. pkt., og stk. 4, ændres »a« til: »b«.": {
			"§ 118, stk. 6, 1.", "§ 118, stk. 7"},
		"I § 9, stk. 3 og 6, der bliver stk. 2 og 5, ændres: »a« til: »b«": {"§ 9, stk. 3", "§ 9, stk. 6"},
		// A bare number with a letter is the next §, printed without its sign.
		"I § 15, stk. 2 og 3, § 15 a, stk. 1, og 15 c, stk. 1, ændres »a« til: »b«.": {
			"§ 15, stk. 2", "§ 15, stk. 3", "§ 15a, stk. 1", "§ 15c, stk. 1"},
	} {
		got := addressStrings(ParseAddresses(def))
		for i := range got {
			got[i] = strings.TrimSuffix(got[i], " pkt.")
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s\n  places = %q\n  want     %q", def, got, want)
		}
	}
}

func TestSeveralWholeUnitsAreRepealedEach(t *testing.T) {
	for def, want := range map[string][]string{
		"Bilag 4, 5 og 7 ophæves.":      {"bilag 4", "bilag 5", "bilag 7"},
		"Bilag 13 og bilag 14 ophæves.": {"bilag 13", "bilag 14"},
		"Kapitel 2 B og 2 C ophæves.":   {"kapitel 2 b", "kapitel 2 c"},
	} {
		if got := addressStrings(ParseAddresses(def)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: places = %q, want %q", def, got, want)
		}
		if !namesWholeUnit(def) {
			t.Errorf("%s does not read as naming whole units", def)
		}
	}
	// One part of an annex is not the annex.
	if got := ParseAddresses("Bilag 3, del C og D, ophæves."); len(got) != 0 {
		t.Errorf("parts of an annex read as whole annexes: %q", addressStrings(got))
	}
}

func TestMistypedQuotationMarksAreRepaired(t *testing.T) {
	for in, want := range map[string]string{
		"I § 84 ændres »§ 25, stk. 1 og 3» til: »§ 25, stk. 1 og 5«.": "I § 84 ændres »§ 25, stk. 1 og 3« til: »§ 25, stk. 1 og 5«.",
		"I § 2 ændres «nr. 1-4« til: »nr. 1-5«.":                      "I § 2 ændres »nr. 1-4« til: »nr. 1-5«.",
		"I § 58 ændres «A» til: «B«.":                                 "I § 58 ændres »A« til: »B«.",
		// Nested and well-formed quotations are read as written.
		"I § 86 ændres »»mor/far«« til: »»mor/far/medfar««.": "I § 86 ændres »»mor/far«« til: »»mor/far/medfar««.",
	} {
		if got := RepairQuotes(in); got != want {
			t.Errorf("RepairQuotes(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListsOfWordingsAreEachReplaced(t *testing.T) {
	ops := parseOperations("Overalt i bekendtgørelsen ændres »fødevareregionen« og »fødevareregionens« til henholdsvis »Fødevarestyrelsen« og »Fødevarestyrelsens«.")
	if len(ops) != 2 || ops[0].old != "fødevareregionen" || ops[0].new != "Fødevarestyrelsen" ||
		ops[1].old != "fødevareregionens" || ops[1].new != "Fødevarestyrelsens" {
		t.Errorf("paired lists read as %+v", ops)
	}
	ops = parseOperations("Overalt i loven ændres »jobplan«, »Jobplan« og »jobplaner« til: »»Min Plan««.")
	if len(ops) != 3 || ops[2].old != "jobplaner" || ops[2].new != "»Min Plan«" {
		t.Errorf("one wording for a list read as %+v", ops)
	}
	// Two replacements joined by "og" are not a list.
	ops = parseOperations("I § 5 ændres »a« til: »b« og »c« til: »d«.")
	if len(ops) != 2 || ops[1].old != "c" || ops[1].new != "d" {
		t.Errorf("two replacements read as %+v", ops)
	}
}

func TestSentencesOpeningWithANumberAreSentences(t *testing.T) {
	for text, want := range map[string]int{
		"Perioden kan højst udgøre 2 måneder. 1. og 2. pkt. finder tilsvarende anvendelse.":     2,
		"Bekendtgørelsen om karakterskala anvendes. 13-skalaen anvendes ved alle prøver.":       2,
		"Afgiften betales efter lov om vægtafgift af motorkøretøjer m.v. Er afgiften betalt, …": 2,
		"En virksomhed, der d. 31. juli 2025 råder over arealer, skal anvende dem.":             1,
		"Beløbet er 2.000 kr. pr. år, jf. stk. 2. Ministeren kan ændre det.":                    2,
	} {
		if got := len(SentencesOf(text)); got != want {
			t.Errorf("%q has %d sentences, want %d: %q", text, got, want, SentencesOf(text))
		}
	}
}

func TestRangeEndsAtTheBareNumber(t *testing.T) {
	c := &Content{}
	for _, n := range []string{"34", "35", "36", "36a"} {
		num, litra := strings.TrimRight(n, "a"), strings.TrimLeft(n, "0123456789")
		c.Order = append(c.Order, Node{Provision: &Provision{Number: num, Litra: litra,
			Segments: []Segment{{Text: "Tekst."}}, Stykker: []Stykke{{Nr: 1, Span: Span{From: 0, To: 1}}}}})
	}
	view, out := c.AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditDeleteProvision, Address: Address{Recognised: true, Paragraph: "34", RangeTo: "36"}, Definition: "§§ 34-36 ophæves."},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("refused: %+v", out.Unresolved)
	}
	if _, ok := view.Find(ParseAddress("§ 36 a")); !ok {
		t.Errorf("§§ 34-36 repealed § 36 a too")
	}
	if _, ok := view.Find(ParseAddress("§ 35")); ok {
		t.Errorf("§ 35 survived the repeal of §§ 34-36")
	}
}

func TestARangeIsReplacedOnceNotOncePerProvision(t *testing.T) {
	c := &Content{}
	for _, n := range []string{"27", "28", "29", "30", "31"} {
		c.Order = append(c.Order, Node{Provision: &Provision{Number: n, Printed: "§ " + n + ".",
			Segments: []Segment{{Text: "§ " + n + ". Gammel."}}, Stykker: []Stykke{{Nr: 1, Span: Span{From: 0, To: 1}}}}})
	}
	var donated []*Provision
	for _, n := range []string{"28", "29", "30"} {
		donated = append(donated, &Provision{Number: n, Printed: "§ " + n + ".",
			Segments: []Segment{{Text: "§ " + n + ". Ny."}}, Stykker: []Stykke{{Nr: 1, Span: Span{From: 0, To: 1}}}})
	}
	view, out := c.AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditReplaceProvision, Address: Address{Recognised: true, Paragraph: "28", RangeTo: "30"},
			Definition: "§§ 28-30 affattes således:", NewProvisions: donated},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("refused: %+v", out.Unresolved)
	}
	var got []string
	for _, r := range view.Provisions() {
		got = append(got, r.Provision.Text())
	}
	want := []string{"§ 27. Gammel.", "§ 28. Ny.", "§ 29. Ny.", "§ 30. Ny.", "§ 31. Gammel."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("provisions after the replacement:\n  %q\nwant\n  %q", got, want)
	}
}

func TestSubsectionLabelsFollowARepeal(t *testing.T) {
	p := &Provision{Number: "36", Segments: []Segment{
		{Text: "Første.", Break: true}, {Text: "Stk. 2. Andet.", Break: true}, {Text: "Stk. 3. Tredje.", Break: true}, {Text: "Stk. 4. Fjerde."},
	}, Stykker: []Stykke{
		{Nr: 1, Span: Span{From: 0, To: 1}}, {Nr: 2, Span: Span{From: 1, To: 2}},
		{Nr: 3, Span: Span{From: 2, To: 3}}, {Nr: 4, Span: Span{From: 3, To: 4}},
	}}
	if !p.DeleteStykke(2) {
		t.Fatal("repeal of stk. 2 failed")
	}
	if got := p.Text(); got != "Første. Stk. 2. Tredje. Stk. 3. Fjerde." {
		t.Errorf("after repealing stk. 2: %q", got)
	}
	// A provision the publisher labelled out of order keeps its labels.
	q := &Provision{Number: "37", Segments: []Segment{{Text: "Første.", Break: true}, {Text: "Stk. 3. Andet.", Break: true}, {Text: "Stk. 4. Tredje."}},
		Stykker: []Stykke{{Nr: 1, Span: Span{From: 0, To: 1}}, {Nr: 2, Span: Span{From: 1, To: 2}}, {Nr: 3, Span: Span{From: 2, To: 3}}}}
	q.DeleteStykke(2)
	if got := q.Text(); got != "Første. Stk. 4. Tredje." {
		t.Errorf("labels the publisher printed were rewritten: %q", got)
	}
}

func TestAChapterIsRenumberedByItsLabel(t *testing.T) {
	ch := &Part{Kind: PartChapter, Number: "5a", Printed: "Kapitel 5 a", Heading: "Dødsbobehandling"}
	c := &Content{Order: []Node{{Part: ch}}}
	view, out := c.AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditReplaceText, Definition: "Kapitelbetegnelsen »Kapitel 5 a« ændres til: »Kapitel 5 b«."},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("refused: %+v", out.Unresolved)
	}
	if p := view.part(PartChapter, "5b"); p == nil || p.Heading != "Dødsbobehandling" {
		t.Errorf("kapitel 5 a was not renumbered 5 b: %+v", view.Parts())
	}
	if ch.Number != "5a" {
		t.Errorf("AsOf edited the source")
	}
}

func TestAnAnnexSectionIsFoundByItsHeadingAndEndsAtTheNext(t *testing.T) {
	lines := []string{"Bilag 12", "1.", "Formål", "1.1.", "Tekst.", "1.", "Punkt et.", "2.", "Punkt to.",
		"2.", "Uddannelsens varighed og struktur m.v.", "2.1.", "Gammel varighed.",
		"3.", "Kompetencemål", "1.", "Mål et."}
	var segs []Segment
	for _, l := range lines {
		segs = append(segs, Segment{Text: l, Break: true})
	}
	repl := []Segment{{Text: "2. Uddannelsens varighed og struktur m.v. 2.1. Ny varighed.", Break: true}}
	sp, ok := headedSection(segs, Span{From: 0, To: len(segs)}, "2", repl)
	if !ok || sp.From != 9 || sp.To != 13 {
		t.Fatalf("section 2 = %+v (%v), want lines 9-13: the list item \"2.\" is not the section, and section 3 is not part of it", sp, ok)
	}
	// Without a heading after the next number the extent is unknown, and nothing is replaced.
	segs = segs[:13]
	segs = append(segs, Segment{Text: "3.", Break: true}, Segment{Text: "Et punkt i en liste, der fortsætter her.", Break: true},
		Segment{Text: "4.", Break: true}, Segment{Text: "Endnu et afsnit", Break: true})
	if sp, ok := headedSection(segs, Span{From: 0, To: len(segs)}, "2", repl); ok && sp.To == len(segs) {
		t.Errorf("section 2 ran to the end of the annex past a later section: %+v", sp)
	}
}

func fourStykker() *Provision {
	return &Provision{Number: "3", Segments: []Segment{
		{Text: "Første.", Break: true}, {Text: "Stk. 2. Andet.", Break: true}, {Text: "Stk. 3. Tredje.", Break: true}, {Text: "Stk. 4. Fjerde."},
	}, Stykker: []Stykke{
		{Nr: 1, Span: Span{From: 0, To: 1}}, {Nr: 2, Span: Span{From: 1, To: 2}},
		{Nr: 3, Span: Span{From: 2, To: 3}}, {Nr: 4, Span: Span{From: 3, To: 4}},
	}}
}

func TestAPairOfSubsectionsIsReplacedTogether(t *testing.T) {
	def := "§ 3, stk. 2 og 3, ophæves, og i stedet indsættes:"
	pair := func() Edit {
		return Edit{Kind: EditReplaceProvision, Address: ParseAddress(def), Definition: def,
			NewStykker: [][]Segment{{{Text: "Stk. 2. Ny to.", Break: true}}, {{Text: "Stk. 3. Ny tre.", Break: true}}}}
	}
	c := &Content{Order: []Node{{Provision: fourStykker()}}}
	// The act names the pair twice, as an act with two dates of entry into force does.
	view, out := c.AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{pair(), pair()}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("refused: %+v", out.Unresolved)
	}
	p := mustFindProvision(t, view, "§ 3")
	if got := Squeeze(p.Text()); got != "Første. Stk. 2. Ny to. Stk. 3. Ny tre. Stk. 4. Fjerde." {
		t.Errorf("after replacing stk. 2 and 3: %q", got)
	}
}

func TestARateIsFoundBesideTheUnitItsTablePrintsOnce(t *testing.T) {
	o, n, ok := bareFigures("5,34 kr.", "5,18 kr.")
	if !ok || o != "5,34" || n != "5,18" {
		t.Fatalf("bareFigures = %q %q %v", o, n, ok)
	}
	if _, _, ok := bareFigures("50.000 euro", "100.000 kroner"); ok {
		t.Errorf("a change of unit was read as a change of figure")
	}
	if !figureOnce("kr. pr. liter 1) over 1,2 pct. 5,34 2) over 6 pct. 11,61", "5,34") {
		t.Errorf("5,34 is printed once")
	}
	if figureOnce("5,34 og 5,34", "5,34") || figureOnce("5,345", "5,34") || !figureOnce("15,34 og 5,34", "5,34") {
		t.Errorf("a figure inside a longer number counted, or one printed twice was taken")
	}
}

func TestATentativeActTheTextCarriesIsNotAppliedAgain(t *testing.T) {
	text := "Behovet kan opfyldes ved integrationsydelse eller kontanthjælp. Ydelsen udbetales af kommunen."
	c := &Content{Order: []Node{{Provision: &Provision{Number: "8", Segments: []Segment{{Text: text}}}}}}
	ins := "I § 8 indsættes efter »opfyldes ved«: »integrationsydelse eller«."
	rep := "I § 8 ændres »udbetales af staten« til: »udbetales af kommunen«."
	view, out := c.AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Tentative: true, Edits: []Edit{
		{Number: "1.", Kind: EditInsertText, Address: ParseAddress(ins), Definition: ins, Anchor: "opfyldes ved", New: "integrationsydelse eller"},
		{Number: "2.", Kind: EditReplaceText, Address: ParseAddress(rep), Definition: rep, Old: "udbetales af staten", New: "udbetales af kommunen"},
	}}}, time.Time{})
	if len(out.InBase) != 2 {
		t.Errorf("the act was not recognised as carried: in base %d, applied %d, refused %d", len(out.InBase), len(out.Applied), len(out.Unresolved))
	}
	if got := mustFindProvision(t, view, "§ 8").Text(); got != text {
		t.Errorf("the carried act was applied again: %q", got)
	}
}

// A rate is wording too. Lov nr. 1564 of 2010 changed the coal duty's rates and rewrote its § 7 b;
// the consolidation of 2015 prints its rates and later ones, and a later act's rates where the
// 2010 act quotes its own. Read as references, the rates told nothing, and the act was applied
// again, its 2010 § 7 b printed over the 2015 one.
func TestATentativeActChangingRatesIsKnownByThem(t *testing.T) {
	text := "Afgiften udgør 31,8 kr. pr. gigajoule og 54,5 kr. for kul."
	rewritten := "Varmeproducenter kan få tilbagebetalt afgiften efter de regler, der gjaldt i 2015 og senere."
	c := &Content{Order: []Node{
		{Provision: &Provision{Number: "1", Segments: []Segment{{Text: text}}}},
		{Provision: &Provision{Number: "7", Litra: "b", Segments: []Segment{{Text: rewritten}}}},
	}}
	one := "I § 1 ændres »33,125« til: »31,8«."
	two := "I § 1 ændres »47,9« til: »52,2«."
	three := "§ 7 b affattes således:"
	view, out := c.AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Tentative: true, Edits: []Edit{
		{Number: "1.", Kind: EditReplaceText, Address: ParseAddress(one), Definition: one, Old: "33,125", New: "31,8"},
		{Number: "2.", Kind: EditReplaceText, Address: ParseAddress(two), Definition: two, Old: "47,9", New: "52,2"},
		{Number: "3.", Kind: EditReplaceProvision, Address: ParseAddress(three), Definition: three,
			NewProvisions: []*Provision{{Number: "7", Litra: "b", Segments: []Segment{{Text: "Varmeproducenter kan få tilbagebetalt afgiften efter reglerne i 2010."}}}}},
	}}}, time.Time{})
	if len(out.InBase) != 3 {
		t.Errorf("the act was not recognised as carried: in base %d, applied %d, refused %d", len(out.InBase), len(out.Applied), len(out.Unresolved))
	}
	if got := mustFindProvision(t, view, "§ 7 b").Text(); got != rewritten {
		t.Errorf("the 2010 § 7 b was printed over the 2015 one: %q", got)
	}
}

func TestARepealedAnnexSectionIsNotAListItemThatLooksLikeIt(t *testing.T) {
	lines := []string{"Bilag 2", "1.", "Formål", "1.1.", "Specialer:", "3.", "Fisk og vildt, niveau 4.",
		"2.", "Varighed", "2.1.", "To år.", "3.", "Særlige kompetencemål", "3.1.", "Eleven kan:", "4.", "Kompetencemål", "4.1.", "Mål."}
	var segs []Segment
	for _, l := range lines {
		segs = append(segs, Segment{Text: l, Break: true})
	}
	sp, ok := headedSection(segs, Span{From: 0, To: len(segs)}, "3", nil)
	if !ok || sp.From != 11 || sp.To != 15 {
		t.Fatalf("section 3 = %+v (%v), want lines 11-15", sp, ok)
	}
}

func TestAnnexPartsNamedByLetterLitraAndList(t *testing.T) {
	for _, c := range []struct {
		def    string
		scopes []string
		label  string
		also   []string
	}{
		{"Bilag 1, A, nr. 4, affattes således:", []string{"A"}, "4", nil},
		// Read without its litra, the whole of punkt 22 was repealed.
		{"Bilag 2, kapitel 6, punkt 22, litra e, ophæves.", []string{"Kapitel 6", "22"}, "e)", nil},
		{"Bilag 2, afsnit I, litra C, affattes således:", []string{"I"}, "C", nil},
		{"I bilag 1 affattes nr. 5.5., 6.5., 7.5., 20.5. og 21.5. således:", nil, "5.5", []string{"6.5", "7.5", "20.5", "21.5"}},
	} {
		ch, ok := ParseAnnexPart(c.def)
		if !ok || !reflect.DeepEqual(ch.Scopes, c.scopes) && len(ch.Scopes)+len(c.scopes) > 0 || ch.Label != c.label || !reflect.DeepEqual(ch.Also, c.also) {
			t.Errorf("%q = %+v (%v), want scopes %q, label %q, also %q", c.def, ch, ok, c.scopes, c.label, c.also)
		}
	}
	// A section letter is a capital with its mark: "a)" is a litra, and "I denne lov" a word.
	if opensWith("a) Modtagelse", "A") || opensWith("I denne lov", "I") || !opensWith("A.", "A") || !opensWith("d. En gennemgang", "d)") {
		t.Errorf("section and litra labels matched the wrong lines")
	}
}

func TestAnAnnexPartIsFoundUnderHeadingWordsAcrossLines(t *testing.T) {
	lines := []string{"Bilag 1", "Kriterium 3.", "Udbud", "5)", "Andet.", "Kriterium 4.", "Bedømmelse af modulet", "1)", "Et.", "5)", "Fem.", "Kriterium 5.", "5)", "Igen."}
	var segs []Segment
	for _, l := range lines {
		segs = append(segs, Segment{Text: l, Break: true})
	}
	ch, ok := ParseAnnexPart("I Bilag 1, i Kriterium 4 Bedømmelse af modulet affattes nr. 1-5 således:")
	if !ok || ch.Label != "1" || ch.LabelTo != "5" {
		t.Fatalf("parsed %+v (%v): the range nr. 1-5 was not read", ch, ok)
	}
	sc, ok := wordsScope(segs, Span{From: 0, To: len(segs)}, "Kriterium 4 Bedømmelse af modulet")
	if !ok || sc.From != 5 || sc.To != 11 {
		t.Fatalf("scope = %+v (%v), want lines 5-11: Kriterium 4 up to Kriterium 5", sc, ok)
	}
	if sp, ok := partSpan(segs, sc, "5"); !ok || sp.From != 9 {
		t.Errorf("nr. 5 = %+v (%v), want line 9", sp, ok)
	}
}

func TestTheLongerFormOfAnEverywhereChangeGoesFirst(t *testing.T) {
	c := &Content{Order: []Node{{Provision: &Provision{Number: "27", Segments: []Segment{
		{Text: "§ 27. Den systemansvarlige virksomheds omkostninger afholdes af den systemansvarlige virksomhed."},
	}}}}}
	def := "Overalt i loven ændres »Den systemansvarlige virksomhed« og »den systemansvarlige virksomhed« til: »Energinet.dk«, og »Den systemansvarlige virksomheds« og »den systemansvarlige virksomheds« ændres til: »Energinet.dk’s«."
	view, out := c.AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{
		{Kind: EditReplaceEverywhere, Address: ParseAddress(def), Definition: def},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("refused: %+v", out.Unresolved)
	}
	if got := mustFindProvision(t, view, "§ 27").Text(); got != "§ 27. Energinet.dk’s omkostninger afholdes af Energinet.dk." {
		t.Errorf("got %q", got)
	}
}

func TestAFullStopInsideTheQuotationDoesNotEndTheSentence(t *testing.T) {
	for _, c := range []struct{ text, old, new, want string }{
		{"efter § 8, stk. 6, 1. pkt., optjenes der ret", "§ 8, stk. 6, 1. pkt.", "§ 8, stk. 6, 1. pkt., eller § 8, stk. 8.", "efter § 8, stk. 6, 1. pkt., eller § 8, stk. 8, optjenes der ret"},
		{"efter stk. 5 henføres gevinst", "efter stk. 5", "efter stk. 5 og 6.", "efter stk. 5 og 6 henføres gevinst"},
		{"jf. stk. 5. Stk. 2. Tekst", "stk. 5", "stk. 6.", "jf. stk. 6. Stk. 2. Tekst"},
	} {
		segs := []Segment{{Text: c.text}}
		if _, _, n := replaceSelected(segs, Span{From: 0, To: 1}, c.old, c.new, pickFirst); n != 1 || segs[0].Text != c.want {
			t.Errorf("%q: got %q, want %q", c.text, segs[0].Text, c.want)
		}
	}
}

func TestEveryFormInAListGetsTheWordingAfterTil(t *testing.T) {
	ops := parseOperations("Overalt i loven ændres »dommeren« og »tinglysningsdommeren« til: »retten«, »dommerens« og »tinglysningsdommerens« til: »rettens« og »Dommeren« og »Tinglysningsdommeren« til: »Retten«.")
	var got []string
	for _, op := range ops {
		got = append(got, op.old+"→"+op.new)
	}
	want := []string{"dommeren→retten", "tinglysningsdommeren→retten", "dommerens→rettens", "tinglysningsdommerens→rettens", "Dommeren→Retten", "Tinglysningsdommeren→Retten"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestAnInsertedConditionTakesItsNumberAndTheRestMoveUp(t *testing.T) {
	items := func() *Provision {
		segs := []Segment{{Text: "§ 3. Ministeren kan fastsætte regler om", Break: true},
			{Text: "1) et,", Break: true}, {Text: "2) to og", Break: true}, {Text: "3) tre.", Break: true},
			{Text: "Stk. 2. Andet.", Break: true}}
		return &Provision{Number: "3", Segments: segs, Stykker: []Stykke{
			{Nr: 1, Span: Span{From: 0, To: 4}, Items: ItemsIn(segs, Span{From: 0, To: 4})},
			{Nr: 2, Span: Span{From: 4, To: 5}},
		}}
	}
	for _, c := range []struct{ def, want string }{
		{"I § 3, stk. 1, indsættes som nr. 2:", "§ 3. Ministeren kan fastsætte regler om 1) et, 2) nyt, 3) to og 4) tre. Stk. 2. Andet."},
		{"I § 3, stk. 1, indsættes efter nr. 1 som nyt nummer:", "§ 3. Ministeren kan fastsætte regler om 1) et, 2) nyt, 3) to og 4) tre. Stk. 2. Andet."},
		{"I § 3, stk. 1, indsættes som nr. 4:", "§ 3. Ministeren kan fastsætte regler om 1) et, 2) to og 3) tre. 4) nyt, Stk. 2. Andet."},
	} {
		c0 := &Content{Order: []Node{{Provision: items()}}}
		e := Edit{Kind: EditInsertProvision, Address: ParseAddress(c.def), Definition: c.def, NewProse: []string{"2) nyt,"}}
		view, _ := c0.AsOf([]Amendment{{Source: "A", Effective: date(2000, 1, 1), Edits: []Edit{e}}}, time.Time{})
		if got := view.Order[0].Provision.Text(); got != c.want {
			t.Errorf("%s\n got %q\nwant %q", c.def, got, c.want)
		}
	}
	// A repealed condition takes its number with it.
	p := items()
	p.DeleteItem(1, 2)
	if got := p.Text(); got != "§ 3. Ministeren kan fastsætte regler om 1) et, 2) tre. Stk. 2. Andet." {
		t.Errorf("after repealing nr. 2: %q", got)
	}
}

func TestAQuotedNumberIsWhole(t *testing.T) {
	for _, c := range []struct{ text, old, new, want string }{
		{"jf. stk. 12 og stk. 1.", "stk. 1", "stk. 2", "jf. stk. 12 og stk. 2."},
		{"efter kapitel 10-12.", "10", "9 b", "efter kapitel 9 b-12."},
		{"2) 0,80 for fjernvarme.", "0,8", "0,85", "2) 0,85 for fjernvarme."},
	} {
		segs := []Segment{{Text: c.text}}
		if _, _, n := replaceSelected(segs, Span{From: 0, To: 1}, c.old, c.new, pickFirst); n != 1 || segs[0].Text != c.want {
			t.Errorf("%q: got %q, want %q", c.text, segs[0].Text, c.want)
		}
	}
}

func TestAChangeQuotesTheTextBeforeItsAct(t *testing.T) {
	c := &Content{Order: []Node{{Provision: &Provision{Number: "43", Segments: []Segment{
		{Text: "§ 43. En person, som modtager integrationsydelse efter § 22 eller integrationsydelse og danskbonus, skal tilbagebetale hjælpen."},
	}}}}}
	first := "I § 43 ændres »integrationsydelse« til: »overgangsydelse«."
	second := "I § 43 ændres »integrationsydelse efter § 22 eller integrationsydelse og danskbonus« til: »selvforsørgelsesydelse efter § 22«."
	view, out := c.AsOf([]Amendment{{Source: "A", Effective: date(2019, 1, 1), Edits: []Edit{
		{Number: "3.", Kind: EditReplaceText, Address: ParseAddress(first), Definition: first, Old: "integrationsydelse", New: "overgangsydelse"},
		{Number: "25.", Kind: EditReplaceText, Address: ParseAddress(second), Definition: second,
			Old: "integrationsydelse efter § 22 eller integrationsydelse og danskbonus", New: "selvforsørgelsesydelse efter § 22"},
	}}}, time.Time{})
	if len(out.Unresolved) != 0 {
		t.Fatalf("refused: %+v", out.Unresolved)
	}
	if got := mustFindProvision(t, view, "§ 43").Text(); got != "§ 43. En person, som modtager selvforsørgelsesydelse efter § 22, skal tilbagebetale hjælpen." {
		t.Errorf("got %q", got)
	}
}

func TestAPassageIsFoundAroundAFootnoteThatStays(t *testing.T) {
	segs := []Segment{
		{Text: "ydelser efter §§ 35 a, 35 b, 36, 37, 37 a", Break: true},
		{Text: "2)", Note: "n2", Break: true},
		{Text: "§ 37 a er ikke sat i kraft.", Note: "n2", Break: true},
		{Text: "og 38-42."},
	}
	if _, _, n := replaceSelected(segs, Span{From: 0, To: 4}, ", 36, 37, 37 a og 38-42", " og 36-42", pickFirst); n != 1 {
		t.Fatalf("the passage around the note was not found")
	}
	if got := SegmentsText(segs); !strings.Contains(got, "§ 37 a er ikke sat i kraft.") || !strings.Contains(got, "35 b og 36-42") {
		t.Errorf("got %q", got)
	}
	// Words inside the note are not the provision's.
	segs = []Segment{{Text: "Teksten", Break: true}, {Text: "1) Se stk. 1-3.", Note: "n1"}}
	if f, _, _ := replaceSelected(segs, Span{From: 0, To: 2}, "stk. 1-3", "stk. 1-4", pickFirst); f {
		t.Errorf("a note's words were edited as the provision's")
	}
}

func TestNewAnnexesGoInFrontOfOneTheyRenumber(t *testing.T) {
	pairs := AnnexPairs("Før bilag 1 indsættes som bilag 1, 2, 2 a, 3 og 4 bilag 1, 2, 3, 4 og 5 til denne lov.")
	if len(pairs) != 5 || pairs[2].Target != 2 || pairs[2].Litra != "a" || pairs[2].Source != 3 {
		t.Fatalf("pairs = %+v", pairs)
	}
	c := &Content{Appendices: []Appendix{{Number: "1", Label: "Bilag 1", Segments: []Segment{{Text: "Bilag 1", Break: true}, {Text: "Statsveje"}}}}}
	if !c.renumberAnnex(1, 5) || c.renumberAnnex(1, 5) {
		t.Fatalf("the renumbering ran other than once")
	}
	for _, p := range pairs {
		if c.hasAnnex(p.Target, p.Litra) || c.setAppendix(p.Target, p.Litra, Appendix{Segments: []Segment{{Text: "Ny"}}}, true) {
			t.Fatalf("bilag %d %s was taken already", p.Target, p.Litra)
		}
	}
	var got []string
	for _, ap := range c.Appendices {
		got = append(got, ap.Label)
	}
	if want := []string{"Bilag 1", "Bilag 2", "Bilag 2 a", "Bilag 3", "Bilag 4", "Bilag 5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("annexes %q, want %q", got, want)
	}
}

func TestADottedPartEndsWhereItsSectionDoes(t *testing.T) {
	lines := []string{"Bilag 4", "16.", "Særlige krav", "16.5", "Træ af løvtræer.", "17.", "Planter af Citrus.", "17.1", "Krav."}
	var segs []Segment
	for _, l := range lines {
		segs = append(segs, Segment{Text: l, Break: true})
	}
	if sp, ok := partSpan(segs, Span{From: 0, To: len(segs)}, "16.5"); !ok || sp.To != 5 {
		t.Errorf("16.5 = %+v (%v), want it to end at 17.", sp, ok)
	}
	// A numbered list inside the part prints "4." too; that is not the next section.
	lines = []string{"3.1", "Målene er:", "1.", "Et.", "4.", "Fire.", "5.", "Fem.", "3.2", "Andet."}
	segs = nil
	for _, l := range lines {
		segs = append(segs, Segment{Text: l, Break: true})
	}
	if sp, ok := partSpan(segs, Span{From: 0, To: len(segs)}, "3.1"); !ok || sp.To != 8 {
		t.Errorf("3.1 = %+v (%v), want it to run to 3.2", sp, ok)
	}
}

// Slips the ministry's consolidations read through, each checked against the next consolidation:
// »genanvendelse« for the text's "genanvende" (miljøbeskyttelsesloven 2010), »selvvalgt« in nr. 1
// for the "selvvalgt" of nr. 2 (beskæftigelsesloven 2015), and »§ 15« for the "15" of "§§ 14 og 15"
// (hvidvaskloven 2009).
func TestSlipsTheConsolidationsReadThrough(t *testing.T) {
	if w, ok := inflectedWord([]rune("i videst muligt omfang lader sig genanvende og 3) ved"), "genanvendelse"); !ok || w != "genanvende" {
		t.Errorf("inflectedWord = %q, %v", w, ok)
	}
	if _, ok := inflectedWord([]rune("genanvende og atter genanvende"), "genanvendelse"); ok {
		t.Error("a word printed twice in forms was placed")
	}
	printed, replaced, ok := listedSection("overtrædelse af § 13, stk. 2, §§ 14 og 15, § 16, stk. 1", "§ 15", "§ 15, stk. 1, 1. pkt., § 15, stk. 2,")
	if !ok || printed != "§§ 14 og 15" || replaced != "§§ 14 og 15, stk. 1, 1. pkt., § 15, stk. 2," {
		t.Errorf("listedSection = %q, %q, %v", printed, replaced, ok)
	}
	p := &Provision{Number: "118", Litra: "a", Segments: []Segment{{Text: "§ 118 a.", Break: true},
		{Text: "Staten refunderer udgiften, når den angår", Break: true},
		{Text: "1) tilbud efter kapitel 10,", Break: true}, {Text: "2) 6 ugers selvvalgt uddannelse,", Break: true}, {Text: "3) undervisningsmaterialer.", Break: true}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 118 a, stk. 1, nr. 1, ændres »selvvalgt« til: »jobrettet«."
	if out := c.Apply(Edit{Kind: EditReplaceText, Address: ParseAddress(def), Definition: def, Old: "selvvalgt", New: "jobrettet"}); !out.Applied || !strings.Contains(p.Text(), "6 ugers jobrettet") {
		t.Errorf("selvvalgt: %+v, %q", out, p.Text())
	}
}

// "2. og 3 pkt." lacks a stop after the second number, and "3" read as a subsection; "2., 3. og 4.
// pkt." names the sentences from the first to the last.
func TestSentenceListsAreRead(t *testing.T) {
	a := ParseAddresses("I § 219 a, stk. 5, 2. og 3 pkt., og § 731, stk. 1, ændres »§ 847« til: »§ 855«.")
	if len(a) != 2 || a[0].Stk != 5 || a[0].Sentence != 2 || a[0].SentenceTo != 3 {
		t.Errorf("2. og 3 pkt. = %+v", a)
		for _, x := range a {
			t.Logf("stk %d sentence %d to %d list %v", x.Stk, x.Sentence, x.SentenceTo, x.SentenceList)
		}
	}
	b := ParseAddress("I § 15 a, stk. 1, indsættes som 2., 3. og 4. pkt.:")
	if b.Sentence != 2 || b.SentenceTo != 4 || b.SentenceList {
		t.Errorf("2., 3. og 4. pkt. = %+v", b)
	}
}

// "I § 52 a, stk. 1, indsættes efter nr. 10 som nyt nummer:" in a subsection printing its conditions
// inside one paragraph goes after nr. 10, and the numbers after it move up; it went to the end.
func TestAConditionGoesAfterTheOneNamed(t *testing.T) {
	p := &Provision{Number: "52", Litra: "a", Segments: []Segment{{Text: "§ 52 a.", Break: true},
		{Text: "Udgifterne omfatter 1) indvinding, 2) løn, 10) kortlægning, 11) afgifter og 12) bidrag.", Break: true}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 52 a, stk. 1, indsættes efter nr. 10 som nyt nummer:"
	out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewProse: []string{"11) godtgørelse,"}})
	if !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := Squeeze(p.Text()); !strings.Contains(got, "10) kortlægning, 11) godtgørelse, 12) afgifter og 13) bidrag.") {
		t.Errorf("§ 52 a = %q", got)
	}
}

// Two acts change the same words, the second signed before the first takes effect: the second's
// wording stands (aktivloven § 74 a, stk. 5, »§ 70 a« to »§ 117« from 2020 by lov nr. 551 of May
// 2019, and to »§ 118« by lov nr. 1555 of December 2019; the consolidation of 2021 prints § 118).
func TestTheLaterActsWordingStands(t *testing.T) {
	c := &Content{Order: []Node{{Provision: &Provision{Number: "74", Litra: "a", Segments: []Segment{{Text: "fleksjob efter § 70 a i lov om en aktiv beskæftigelsesindsats."}}}}}}
	def1 := "I § 74 a ændres »§ 70 a« til: »§ 117«."
	def2 := "I § 74 a ændres »§ 70 a« til: »§ 118«."
	view, out := c.AsOf([]Amendment{
		{Source: "A", Effective: date(2019, 5, 7), Edits: []Edit{{Source: "A", Number: "1.", Kind: EditReplaceText, Address: ParseAddress(def1), Definition: def1, Old: "§ 70 a", New: "§ 117", InForce: date(2020, 1, 1)}}},
		{Source: "B", Effective: date(2019, 12, 27), Edits: []Edit{{Source: "B", Number: "1.", Kind: EditReplaceText, Address: ParseAddress(def2), Definition: def2, Old: "§ 70 a", New: "§ 118", InForce: date(2020, 1, 1)}}},
	}, date(2021, 1, 1))
	if len(out.Unresolved) != 0 {
		t.Fatalf("refused: %+v", out.Unresolved)
	}
	if got := mustFindProvision(t, view, "§ 74 a").Text(); !strings.Contains(got, "efter § 118 i") {
		t.Errorf("§ 74 a = %q", got)
	}
}

// An exponent the act typed as a digit, »(2,0 x hestens stangmål) 2 «, is the text's "²"; a
// quotation ending in a citation the text does not print, »lov om skifte af fællesbo m.v. § 69«,
// names the words before it (ægteskabsloven § 25, stk. 2, as the consolidation of 2012 reads it).
func TestQuotationsTypedOtherwise(t *testing.T) {
	if o, n, ok := superscripted("mindst (2,0 x hestens stangmål)², og", "(2,0 x hestens stangmål) 2 ", "(1,7 x hestens stangmål) 2 "); !ok || o != "(2,0 x hestens stangmål)²" || n != "(1,7 x hestens stangmål)²" {
		t.Errorf("superscripted = %q, %q, %v", o, n, ok)
	}
	c := &Content{Order: []Node{{Provision: &Provision{Number: "25", Segments: []Segment{{Text: "Om formuedelingen gælder reglerne i lov om skifte af fællesbo m.v."}}}}}}
	def := "I § 25 ændres »lov om skifte af fællesbo m.v. § 69« til: »§ 60 i lov om ægtefælleskifte m.v.«."
	if out := c.Apply(Edit{Kind: EditReplaceText, Address: ParseAddress(def), Definition: def, Old: "lov om skifte af fællesbo m.v. § 69", New: "§ 60 i lov om ægtefælleskifte m.v."}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := mustFindProvision(t, c, "§ 25").Text(); got != "Om formuedelingen gælder reglerne i § 60 i lov om ægtefælleskifte m.v." {
		t.Errorf("§ 25 = %q", got)
	}
}

// Conditions placed as the change says: "før nr. 1 som nyt nummer" (fødevareloven § 60, which the
// consolidation of 2011 prints with the new condition first), "som nr. 6 og 7" in a provision of one
// subsection, and a condition the text prints already, which takes the change's wording instead of
// being printed twice (lov nr. 1609 of 2010, børne- og ungeydelsesloven § 2).
func TestConditionsArePlacedAsTheChangeNumbersThem(t *testing.T) {
	mk := func(lines ...string) (*Content, *Provision) {
		segs := []Segment{{Text: "§ 2.", Break: true}, {Text: "Det er en betingelse,", Break: true}}
		for _, l := range lines {
			segs = append(segs, Segment{Text: l, Break: true})
		}
		p := &Provision{Number: "2", Printed: "§ 2.", Segments: segs}
		p.Normalise()
		return &Content{Order: []Node{{Provision: p}}}, p
	}
	c, p := mk("1) at barnet bor her i landet, og", "2) at barnet ikke er gift.")
	def := "I § 2 indsættes før nr. 1 som nyt nummer:"
	if out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewProse: []string{"1) at ansøgeren har forældremyndigheden,"}}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := Squeeze(p.Text()); !strings.Contains(got, "1) at ansøgeren har forældremyndigheden, 2) at barnet bor her i landet, og 3) at barnet") {
		t.Errorf("før nr. 1: %q", got)
	}
	c, p = mk("5) at kommunen ikke har truffet afgørelse om standsning, og",
		"6) at kommunen ikke for den pågældende måned har givet meddelelse om, at ungeydelsen skal standses.")
	def = "I § 2 indsættes som nr. 6 og 7:"
	out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewProse: []string{
		"6) at kommunen ikke for den pågældende måned har givet meddelelse om, at ungeydelsen skal standses, og 7) at mindst en af forældrene har haft bopæl i Danmark i 2 år."}})
	if !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	got := Squeeze(p.Text())
	if strings.Count(got, "meddelelse") != 1 || !strings.Contains(got, "skal standses, og 7) at mindst en af forældrene") {
		t.Errorf("som nr. 6 og 7: %q", got)
	}
}

// More of what the consolidations read through: one word of a longer quotation printed otherwise
// (»… særskilt håndteret« for "særligt", miljøbeskyttelsesloven 2010), a compound the text suspends
// (»studenterkurser og hf-kurser« for "studenter- og hf-kurser"), and a minister renamed everywhere
// in a text that names the new one already.
func TestMoreSlipsTheConsolidationsReadThrough(t *testing.T) {
	if w, ok := typoPrinted("skal tilbagetages og genanvendt eller særligt håndteret.", "genanvendt eller særskilt håndteret"); !ok || w != "genanvendt eller særligt håndteret" {
		t.Errorf("typoPrinted = %q, %v", w, ok)
	}
	if _, ok := typoPrinted("de offentlige eller private skoler", "de offentlige eller statslige skoler"); ok {
		t.Error("a word unlike the quoted one was taken for it")
	}
	if v, ok := suspendedCompound("Private gymnasieskoler og private studenter- og hf-kurser er ikke omfattet", "gymnasieskoler og private studenterkurser og hf-kurser"); !ok || v != "gymnasieskoler og private studenter- og hf-kurser" {
		t.Errorf("suspendedCompound = %q, %v", v, ok)
	}
	c := &Content{Order: []Node{{Provision: &Provision{Number: "3", Segments: []Segment{{Text: "Økonomi- og erhvervsministeren fastsætter regler."}}}}}}
	def := "Overalt i loven ændres »Økonomiministeren« til: »Økonomi- og erhvervsministeren«."
	if out := c.Apply(Edit{Kind: EditReplaceEverywhere, Address: ParseAddress(def), Definition: def, Old: "Økonomiministeren", New: "Økonomi- og erhvervsministeren"}); !out.Applied {
		t.Errorf("refused: %s", out.Reason)
	}
}

// An act signed while an earlier act's wording change still waited to take effect drafted its own
// change against the words before it; its wording stands (aktivloven § 37, stk. 3: lov nr. 174 of
// February 2019 changed "integrationsydelses-" from 2020, lov nr. 1555 of December 2019 rewrote the
// phrase holding it, and the consolidation of 2021 prints lov nr. 1555's wording).
func TestALaterActsChangeToWordsStillToChangeStands(t *testing.T) {
	text := "Har en åbenlyst uddannelsesparat integrationsydelses- eller uddannelseshjælpsmodtager booket en samtale, sidestilles det."
	c := &Content{Order: []Node{{Provision: &Provision{Number: "37", Segments: []Segment{{Text: text}}}}}}
	d1 := "I § 37 ændres »integrationsydelses-« til: »selvforsørgelsesydelses-«."
	d2 := "I § 37 ændres »åbenlyst uddannelsesparat integrationsydelses- eller uddannelseshjælpsmodtager« til: »åbenlyst uddannelsesparat modtager af overgangsydelse eller uddannelseshjælp«."
	view, out := c.AsOf([]Amendment{
		{Source: "A", Effective: date(2019, 2, 27), Edits: []Edit{{Source: "A", Number: "22.", Kind: EditReplaceText, Address: ParseAddress(d1), Definition: d1, Old: "integrationsydelses-", New: "selvforsørgelsesydelses-", InForce: date(2020, 1, 1)}}},
		{Source: "B", Effective: date(2019, 12, 27), Edits: []Edit{{Source: "B", Number: "17.", Kind: EditReplaceText, Address: ParseAddress(d2), Definition: d2,
			Old: "åbenlyst uddannelsesparat integrationsydelses- eller uddannelseshjælpsmodtager", New: "åbenlyst uddannelsesparat modtager af overgangsydelse eller uddannelseshjælp", InForce: date(2020, 1, 1)}}},
	}, date(2021, 1, 1))
	if len(out.Unresolved) != 0 {
		t.Fatalf("refused: %+v", out.Unresolved)
	}
	if got := mustFindProvision(t, view, "§ 37").Text(); !strings.Contains(got, "uddannelsesparat modtager af overgangsydelse eller uddannelseshjælp booket") {
		t.Errorf("§ 37 = %q", got)
	}
}

// "indsættes efter 1. pkt. som nye punktummer" names the sentence the new ones follow. Read as the
// number the new text takes, sentences went in front of the one they follow — about ninety over the
// census, silently.
func TestSentencesGoAfterTheOneNamed(t *testing.T) {
	p := &Provision{Number: "13", Litra: "a", Printed: "§ 13 a.", Segments: []Segment{
		{Text: "§ 13 a.", Break: true}, {Text: "For en person gælder noget.", Break: true},
		{Text: "Stk. 2.", Break: true}, {Text: "En person skal være aktivt jobsøgende. Personen skal registrere.", Break: true}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 13 a, stk. 2, indsættes efter 1. pkt. som nye punktummer:"
	if out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewProse: []string{"Ny sætning et. Ny sætning to."}}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := Squeeze(p.Text()); !strings.Contains(got, "aktivt jobsøgende. Ny sætning et. Ny sætning to. Personen skal registrere.") {
		t.Errorf("§ 13 a = %q", got)
	}
}

func TestSubsectionsPrintedAsLinesBecomeSubsections(t *testing.T) {
	p := &Provision{Number: "23", Printed: "§ 23.", Segments: []Segment{
		{Text: "§ 23.", Break: true}, {Text: "Loven træder i kraft den 1. juli.", Break: true},
		{Text: "Stk. 2.", Break: true}, {Text: "Ministeren fastsætter regler.", Break: true}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 23 indsættes efter stk. 1 som nyt stykke:"
	if out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewProse: []string{"Stk. 2 . § 6 har virkning fra 1. januar."}}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if len(p.Stykker) != 3 {
		t.Fatalf("§ 23 has %d subsections: %q", len(p.Stykker), p.Text())
	}
	if got := Squeeze(p.Text()); !strings.Contains(got, "Stk. 2. § 6 har virkning fra 1. januar. Stk. 3. Ministeren fastsætter regler.") {
		t.Errorf("§ 23 = %q", got)
	}
}

func TestASubsectionGoesInFrontOfTheFirst(t *testing.T) {
	p := &Provision{Number: "1", Printed: "§ 1.", Segments: []Segment{
		{Text: "§ 1.", Break: true}, {Text: "Frihedsberøvelse må kun finde sted efter loven.", Break: true},
		{Text: "Stk. 2.", Break: true}, {Text: "Loven gælder også børn.", Break: true}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 1 indsættes før stk. 1 som nyt stykke:"
	if out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewProse: []string{"Indlæggelse sker på baggrund af informeret samtykke."}}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := Squeeze(p.Text()); got != "§ 1. Indlæggelse sker på baggrund af informeret samtykke. Stk. 2. Frihedsberøvelse må kun finde sted efter loven. Stk. 3. Loven gælder også børn." {
		t.Errorf("§ 1 = %q", got)
	}
	if len(p.Stykker) != 3 {
		t.Errorf("§ 1 has %d subsections", len(p.Stykker))
	}
}

// ferieProvision is aktivloven § 74 e as it stood in 2019, in short: a subsection of numbered
// conditions after a first.
func ferieProvision() *Provision {
	p := &Provision{Number: "74", Litra: "e", Printed: "§ 74 e.", Segments: []Segment{
		{Text: "§ 74 e.", Break: true}, {Text: "Personer har ret til ferie.", Break: true},
		{Text: "Stk. 2.", Break: true}, {Text: "Retten gælder for følgende perioder:", Break: true},
		{Text: "1)", Break: true}, {Text: "Første periode giver 15 dage,", Break: true},
		{Text: "2)", Break: true}, {Text: "Anden periode giver 10 dage,", Break: true},
		{Text: "3)", Break: true}, {Text: "Tredje periode giver 5 dage.", Break: true}}}
	p.Normalise()
	return p
}

func TestASentenceForAConditionAnEarlierActRepealsIsOvertaken(t *testing.T) {
	// Lov nr. 701 of 2018 rewrote aktivloven § 74 e, stk. 3, from 1 September 2020, without its
	// conditions; lov nr. 525 of 2020 put a second sentence into nr. 1-3 from 1 May 2020. In the
	// order the acts were signed the conditions were gone, and the sentences went to the end of the
	// subsection.
	p := ferieProvision()
	c := &Content{Order: []Node{{Provision: p}}}
	rep := "§ 74 e, stk. 2, ophæves."
	ins := "I § 74 e, stk. 2, nr. 1, indsættes som 2. pkt.:"
	chain := []Amendment{
		{Source: "A", Effective: date(2018, 6, 8), Edits: []Edit{{Number: "1.", Kind: EditDeleteProvision, Address: ParseAddress(rep), Definition: rep, InForce: date(2020, 9, 1)}}},
		{Source: "B", Effective: date(2020, 4, 28), Edits: []Edit{{Number: "20.", Kind: EditInsertProvision, Address: ParseAddress(ins), Definition: ins, NewProse: []string{"I 2020 kan der højst afholdes 17 dage."}, InForce: date(2020, 5, 1)}}},
	}
	view, out := c.AsOf(chain, date(2021, 1, 1))
	if len(out.Unresolved) != 0 {
		t.Fatalf("refused: %+v", out.Unresolved)
	}
	if got := Squeeze(view.Order[0].Provision.Text()); strings.Contains(got, "17 dage") {
		t.Errorf("the sentence stands in %q", got)
	}
	found := false
	for _, a := range out.Applied {
		found = found || a.Edit.Number == "20." && strings.Contains(a.Note, "nothing of it remains")
	}
	if !found {
		t.Errorf("the overtaken sentence is not accounted for: %+v", out.Applied)
	}
}

func TestLettersGoInTheirConditionAndMoveUp(t *testing.T) {
	p := &Provision{Number: "10", Printed: "§ 10.", Segments: []Segment{
		{Text: "§ 10.", Break: true}, {Text: "Pligterne gælder, når", Break: true},
		{Text: "1)", Break: true}, {Text: "virksomheden er etableret,", Break: true},
		{Text: "2)", Break: true}, {Text: "der er tale om", Break: true},
		{Text: "a)", Break: true}, {Text: "en pengeoverførsel,", Break: true},
		{Text: "b)", Break: true}, {Text: "en veksling, eller", Break: true},
		{Text: "3)", Break: true}, {Text: "der er mistanke om hvidvask.", Break: true}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 10, nr. 2, indsættes som litra c:"
	if out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewProse: []string{"c) en overførsel af virtuel valuta,"}}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := Squeeze(p.Text()); !strings.Contains(got, "b) en veksling, eller c) en overførsel af virtuel valuta, 3) der er mistanke") {
		t.Errorf("§ 10 = %q", got)
	}
	def = "I § 10, nr. 2, indsættes efter litra a som nyt litra:"
	if out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewProse: []string{"b) en betaling i kontanter,"}}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := Squeeze(p.Text()); !strings.Contains(got, "a) en pengeoverførsel, b) en betaling i kontanter, c) en veksling, eller d) en overførsel") {
		t.Errorf("§ 10 = %q", got)
	}
}

func TestAConditionNumberedBeforeASubsectionWasPutInFront(t *testing.T) {
	// Lov nr. 551 of 2019 put nr. 5 into aktivloven § 69, stk. 2, numbering the subsections as
	// they stood before another act put a new stk. 2 in front of the conditions.
	p := &Provision{Number: "69", Printed: "§ 69.", Segments: []Segment{
		{Text: "§ 69.", Break: true}, {Text: "Det er en betingelse at deltage.", Break: true},
		{Text: "Stk. 2.", Break: true}, {Text: "Afviser en person behandling, ophører ydelsen.", Break: true},
		{Text: "Stk. 3.", Break: true}, {Text: "Kommunen vurderer betingelserne, hvis personen", Break: true},
		{Text: "1)", Break: true}, {Text: "ikke deltager i tilbud,", Break: true},
		{Text: "2)", Break: true}, {Text: "afslår et tilbud om arbejde, eller", Break: true}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 69, stk. 2, indsættes som nr. 3:"
	if out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewProse: []string{"3) undlader at booke en jobsamtale."}}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := Squeeze(p.Text()); !strings.Contains(got, "eller 3) undlader at booke en jobsamtale.") || strings.Contains(got, "ophører ydelsen. 3)") {
		t.Errorf("§ 69 = %q", got)
	}
}

func TestAnInsertionAnEarlierActMadeIsNotMadeTwice(t *testing.T) {
	p := &Provision{Number: "69", Printed: "§ 69.", Segments: []Segment{
		{Text: "§ 69.", Break: true}, {Text: "Personen deltager ikke i tilbud efter lov om en aktiv beskæftigelsesindsats, tilbud efter kapitel 5 i lov om kompensation til handicappede i erhverv m.v. eller anden lovgivning og har en plan, eller", Break: true}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 69 indsættes efter »tilbud efter lov om en aktiv beskæftigelsesindsats«: », tilbud efter kapitel 5 i lov om kompensation til handicappede i erhverv m.v.«."
	if out := c.Apply(Edit{Kind: EditInsertText, Address: ParseAddress(def), Definition: def, New: ", tilbud efter kapitel 5 i lov om kompensation til handicappede i erhverv m.v."}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	def = "I § 69 ændres »plan.« til: »plan, eller«."
	if out := c.Apply(Edit{Kind: EditReplaceText, Address: ParseAddress(def), Definition: def, Old: "plan.", New: "plan, eller"}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := Squeeze(p.Text()); strings.Count(got, "kapitel 5") != 1 || strings.Contains(got, "eller, eller") {
		t.Errorf("§ 69 = %q", got)
	}
}

func TestLinesNumberingProvisionsBecomeProvisions(t *testing.T) {
	p := &Provision{Number: "24", Printed: "§ 24.", Segments: []Segment{{Text: "§ 24.", Break: true}, {Text: "Ildslukkere skal være godkendt.", Break: true}}}
	p.Normalise()
	next := &Provision{Number: "25", Printed: "§ 25.", Segments: []Segment{{Text: "§ 25.", Break: true}, {Text: "Styrelsen kan dispensere.", Break: true}}}
	next.Normalise()
	c := &Content{Order: []Node{{Provision: p}, {Provision: next}}}
	def := "Efter § 24 indsættes i kapitel 4:"
	lines := []string{"Hastighedsbegrænsere", "§ 24 a. Installation må kun foretages af et værksted.", "Stk. 2. Mærkatet skal indeholde oplysninger om", "1) værkstedets nummer, og", "2) datoen."}
	if out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewProse: lines}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	loc, ok := c.Find(ParseAddress("§ 24 a, stk. 2, nr. 2"))
	if !ok || loc.Provision == nil || loc.Provision.Heading != "Hastighedsbegrænsere" || loc.Item == nil {
		t.Fatalf("§ 24 a, stk. 2, nr. 2 not found: %+v", loc)
	}
	if got := Squeeze(p.Text()); got != "§ 24. Ildslukkere skal være godkendt." {
		t.Errorf("§ 24 = %q", got)
	}
	def = "Efter § 24 a indsættes:"
	if out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewProse: []string{"Dispensation"}}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if next.Heading != "Dispensation" {
		t.Errorf("§ 25 heading = %q", next.Heading)
	}
}

func TestAnchorsTypedOtherwise(t *testing.T) {
	for _, tc := range []struct{ def, anchor string }{
		{"I § 71, stk. 4, nr. 4, indsættes efter § »3, stk. 1-3,«: »§ 3 a, stk. 1,«.", "§ 3, stk. 1-3,"},
		{"I § 71 a, stk. 1, indsættes efter: »produkter,«: »stoffer, blandinger,«.", "produkter,"},
		{"I § 1, stk. 1, nr. 13 , indsættes efter m.v.: »,«.", "m.v."},
		{"I indledningen indsættes »efter bemyndigelse«: »i henhold til § 5«.", "bemyndigelse"},
		{"I Bilag 1 indsættes i afsnittet »Kriterium 2: Tilbuddet styrker borgerne« efter »Indikator 2.b: Borgerne indgår i fællesskaber.«: »Indikator 2.c: …«", "Indikator 2.b: Borgerne indgår i fællesskaber."},
		{"I bilag 2, 2. afsnit, indsættes efter 1. pkt.: »Andre virksomheder kan vælge det.«", ""},
	} {
		if got := InsertionAnchor(Edit{Definition: tc.def}); got != tc.anchor {
			t.Errorf("%q: anchor %q, want %q", tc.def, got, tc.anchor)
		}
	}
}

func TestAnInsertionInANamedSectionBringsItsLastQuotation(t *testing.T) {
	def := "I Bilag 1 indsættes i afsnittet »Kriterium 14: Tilbuddets fysiske rammer« efter: »Gælder ikke ambulante stofmisbrugsbehandlingstilbud,«: »ambulante alkoholbehandlingstilbud,«."
	kind, _, nw := Classify(def, "", ParseAddress(def), Brought{})
	if kind != EditInsertText || nw != "ambulante alkoholbehandlingstilbud," {
		t.Errorf("%v %q", kind, nw)
	}
	if got := InsertionAnchor(Edit{Definition: def}); got != "Gælder ikke ambulante stofmisbrugsbehandlingstilbud," {
		t.Errorf("anchor %q", got)
	}
}

func TestConditionsPrintedOnOneLineAreReadApart(t *testing.T) {
	// A chapter lov nr. 551 of 2019 brought printed aktivloven § 72, stk. 1's conditions on one
	// line, outside the subsection; lov nr. 2218 of 2020's change to nr. 2 found no nr. 2.
	p := &Provision{Number: "72", Printed: "§ 72.", Segments: []Segment{
		{Text: "§ 72.", Break: true}, {Text: "Indtægter opgøres som summen af", Break: true},
		{Text: " 1) lønindkomst, 2) ydelser under barsel, og 3) andre ydelser, og a) renter, b) udbytter.", Break: true},
		{Text: "Stk. 2.", Break: true}, {Text: "Andre indtægter kan medregnes.", Break: true}}}
	p.Stykker = []Stykke{{Nr: 1, Span: Span{From: 1, To: 2}}, {Nr: 2, Span: Span{From: 3, To: 5}}}
	c := &Content{Order: []Node{{Provision: p}}}
	c.SplitInlineConditions()
	def := "I § 72, stk. 1, nr. 2, indsættes efter »barsel«: »eller sorgorlov«."
	if out := c.Apply(Edit{Kind: EditInsertText, Address: ParseAddress(def), Definition: def, New: "eller sorgorlov"}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := Squeeze(p.Text()); !strings.Contains(got, "lønindkomst, 2) ydelser under barsel eller sorgorlov, og 3) andre ydelser, og a) renter, b) udbytter.") {
		t.Errorf("§ 72 = %q", got)
	}
	if loc, ok := c.Find(ParseAddress("§ 72, stk. 1, nr. 3, litra b")); !ok || loc.Item == nil {
		t.Errorf("nr. 3, litra b is not read apart")
	}
}

func TestALetterListNamesEachLetter(t *testing.T) {
	got := ParseAddresses("I § 98 b, stk. 1, nr. 2, og § 98 e, stk. 2, nr. 2, litra a og b, indsættes efter »dagpengeret,«: »ydelse,«.")
	var names []string
	for _, a := range got {
		names = append(names, a.String())
	}
	want := []string{"§ 98b, stk. 1, nr. 2", "§ 98e, stk. 2, nr. 2, litra a", "§ 98e, stk. 2, nr. 2, litra b"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("%q", names)
	}
}

func TestARunOfConditionsRewrittenInItsOwnClauseGoesWhole(t *testing.T) {
	// Lov nr. 1655 of 2024 changed aktivloven § 35, stk. 4: "ændres »2019-niveau« til: »2024-niveau
	// følgende«, og nr. 1-4 affattes således". The old nr. 2-4 stood behind the new four.
	p := &Provision{Number: "35", Printed: "§ 35.", Segments: []Segment{
		{Text: "§ 35.", Break: true}, {Text: "Første.", Break: true},
		{Text: "Stk. 2.", Break: true}, {Text: "Satsen udgør i 2019-niveau:", Break: true},
		{Text: "1)", Break: true}, {Text: "100 kr. a,", Break: true},
		{Text: "2)", Break: true}, {Text: "400 kr. b,", Break: true},
		{Text: "3)", Break: true}, {Text: "600 kr. c,", Break: true},
		{Text: "4)", Break: true}, {Text: "700 kr. d.", Break: true}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 35, stk. 2, ændres »2019-niveau« til: »2024-niveau følgende«, og nr. 1-4 affattes således:"
	lines := []string{"1) 200 kr. A.", "2) 450 kr. B.", "3) 600 kr. C.", "4) 700 kr. D."}
	if out := c.Apply(Edit{Kind: EditReplaceProvision, Address: ParseAddress(def), Definition: def, NewProse: lines}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := Squeeze(p.Text()); !strings.HasSuffix(got, "1) 200 kr. A. 2) 450 kr. B. 3) 600 kr. C. 4) 700 kr. D.") {
		t.Errorf("§ 35 = %q", got)
	}
}

func TestAQuotationWithAnotherConnectiveKeepsItsNumbers(t *testing.T) {
	// Lov nr. 1655 of 2024 quoted aktivloven § 73 i, stk. 1, as "nr. 5 og 6," where it prints
	// "nr. 5 eller 6,"; the cheapest alignment left the 6 out and the passage was refused.
	text := "som har modtaget uddannelseshjælp efter § 23, stk. 2, nr. 8 eller 9, og aktivitetstillæg efter § 24, stk. 3, nr. 5 eller 6, i sammenlagt 6 måneder."
	got, ok := typoPrinted(text, "uddannelseshjælp efter § 23, stk. 2, nr. 8 eller 9, og aktivitetstillæg efter § 24, stk. 3, nr. 5 og 6,")
	if !ok || !strings.HasSuffix(strings.TrimSpace(got), "nr. 5 eller 6,") {
		t.Errorf("%q %v", got, ok)
	}
}

func TestACitationQuotedWithOgForEller(t *testing.T) {
	text := "Det kan ske ved at anvende tilbud efter kapitel 10 eller 11 i lov om en aktiv beskæftigelsesindsats."
	if got, ok := typoPrinted(text, "kapitel 10 og 11"); !ok || got != "kapitel 10 eller 11" {
		t.Errorf("%q %v", got, ok)
	}
	if _, ok := typoPrinted("Efter kapitel 10 eller 11, og kapitel 10 eller 11 igen.", "kapitel 10 og 11"); ok {
		t.Errorf("placed where the text prints the citation twice")
	}
}

func TestAProvisionCitesItsOwnSubsectionWithoutItsNumber(t *testing.T) {
	p := &Provision{Number: "36", Printed: "§ 36.", Segments: []Segment{
		{Text: "§ 36.", Break: true}, {Text: "Politiet kan bestemme frihedsberøvelse, hvis foranstaltningerne ikke er tilstrækkelige, jf. stk. 5.", Break: true}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 36, stk. 1, 1. pkt., ændres »§ 36, stk. 5« til: »stk. 4«."
	if out := c.Apply(Edit{Kind: EditReplaceText, Address: ParseAddress(def), Definition: def, Old: "§ 36, stk. 5", New: "stk. 4"}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := Squeeze(p.Text()); !strings.HasSuffix(got, "jf. stk. 4.") {
		t.Errorf("§ 36 = %q", got)
	}
}

func TestTheWordArtEndsASentence(t *testing.T) {
	text := "Boligerne anvises til søgende med behov for boliger af den pågældende art. Boligorganisationen opkræver gebyrer. Se art. 5 i forordningen."
	if s, ok := sentenceText(text, 2, 0); !ok || !strings.HasPrefix(s, "Boligorganisationen") {
		t.Errorf("2. pkt. = %q %v", s, ok)
	}
	if s, ok := sentenceText(text, 3, 0); !ok || s != "Se art. 5 i forordningen." {
		t.Errorf("3. pkt. = %q %v", s, ok)
	}
}

func TestAQuotationWithOneShortWordOther(t *testing.T) {
	text := "Bestemmelserne om ydelser m.v. i § 38 og § 45 finder ikke anvendelse for personer omfattet af § 2, nr. 10."
	if got, ok := typoPrinted(text, "Bestemmelserne om ydelser m.v. efter § 38 og § 45"); !ok || got != "Bestemmelserne om ydelser m.v. i § 38 og § 45" {
		t.Errorf("%q %v", got, ok)
	}
	if _, ok := typoPrinted(text, "Bestemmelserne om ydelser m.v. i § 39 og § 45"); ok {
		t.Errorf("a number other was placed")
	}
}

func TestAMinisterRenamedByResolutionIsTheOneTheTextNames(t *testing.T) {
	// Lov nr. 222 of 2008 quoted "velfærdsministeren", the indenrigs- og sundhedsminister's title
	// after a royal resolution no act in the chain records.
	p := &Provision{Number: "13", Printed: "§ 13.", Segments: []Segment{
		{Text: "§ 13.", Break: true}, {Text: "Et navn er godkendt af indenrigs- og sundhedsministeren for et nyt parti.", Break: true}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 13 ændres »velfærdsministeren« til: »Valgnævnet«."
	if out := c.Apply(Edit{Kind: EditReplaceText, Address: ParseAddress(def), Definition: def, Old: "velfærdsministeren", New: "Valgnævnet"}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := Squeeze(p.Text()); !strings.Contains(got, "godkendt af Valgnævnet for") {
		t.Errorf("§ 13 = %q", got)
	}
	q := &Provision{Number: "14", Printed: "§ 14.", Segments: []Segment{
		{Text: "§ 14.", Break: true}, {Text: "Justitsministeren og skatteministeren fastsætter regler.", Break: true}}}
	q.Normalise()
	c = &Content{Order: []Node{{Provision: q}}}
	def = "I § 14 ændres »velfærdsministeren« til: »Valgnævnet«."
	if out := c.Apply(Edit{Kind: EditReplaceText, Address: ParseAddress(def), Definition: def, Old: "velfærdsministeren", New: "Valgnævnet"}); out.Applied {
		t.Errorf("placed among two ministers: %q", Squeeze(q.Text()))
	}
}

func TestASubsectionTheBasePrintsInLaterWordsIsNotInsertedAgain(t *testing.T) {
	p := &Provision{Number: "58", Litra: "g", Printed: "§ 58 g.", Segments: []Segment{
		{Text: "§ 58 g.", Break: true}, {Text: "Myndighederne indberetter udlændinge.", Break: true},
		{Text: "Stk. 2.", Break: true}, {Text: "Udlændingemyndighederne indberetter i medfør af SIS-tilbagesendelsesforordningen en udlænding, der ikke er statsborger i et Schengenland, når der er truffet en afgørelse om tilbagesendelse.", Break: true}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	c.carried = true
	def := "I § 58 g indsættes som stk. 2:"
	add := [][]Segment{{{Text: "Stk. 2."}, {Text: "Udlændingemyndighederne indberetter i henhold til SIS-tilbagesendelsesforordningen en udlænding, der ikke er statsborger i et Schengenland, når der er truffet en afgørelse om tilbagesendelse."}}}
	c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewStykker: add})
	if len(p.Stykker) != 2 {
		t.Errorf("§ 58 g has %d subsections: %q", len(p.Stykker), p.Text())
	}
}

func TestATargetCitedWithoutNr(t *testing.T) {
	got := ParseTarget("I registreringsafgiftsloven, jf. lovbekendtgørelse 377 af 18. april 2017, foretages følgende ændring:")
	if got.Number != 377 || got.Date.Year() != 2017 {
		t.Errorf("%+v", got)
	}
}

func TestAProvisionLetteredTwice(t *testing.T) {
	if a := ParseAddress("§ 30 mm, stk. 3, affattes således:"); a.Paragraph != "30mm" || a.Stk != 3 {
		t.Errorf("%+v", a)
	}
	if a := ParseAddress("I § 8 og 9 ændres »x« til: »y«."); a.Paragraph != "8" {
		t.Errorf("%+v", a)
	}
	m := &Provision{Number: "30", Litra: "m", Segments: []Segment{{Text: "§ 30 m.", Break: true}, {Text: "Tilskud udbetales.", Break: true}}}
	mm := &Provision{Number: "30", Litra: "mm", Segments: []Segment{{Text: "§ 30 mm. Ministeren kan indhente oplysninger.", Break: true}}}
	m.Normalise()
	mm.Normalise()
	c := &Content{Order: []Node{{Provision: m}, {Provision: mm}}}
	if loc, ok := c.Find(ParseAddress("§ 30 mm")); !ok || loc.Provision != mm {
		t.Errorf("§ 30 mm found as %+v", loc.Provision)
	}
}

func TestARepealNamedBeforeItsNewNumber(t *testing.T) {
	d := "I § 50 ophæves stk. 1, og i stk. 3, 1. pkt., der bliver stk. 2, 1. pkt., ændres »Dagbogen, tingbogen« til: »Tingbogen« og »kan« udgår, og stk. 3, 2. pkt., der bliver stk. 2, 2. pkt., ophæves."
	var kinds, places []string
	for _, o := range parseOperations(d) {
		kinds = append(kinds, string(o.kind))
		if o.kind == EditDeleteProvision {
			places = append(places, Squeeze(o.place))
		}
	}
	if strings.Join(kinds, " ") != "delete-provision replace-text delete-text delete-provision" || strings.ReplaceAll(places[1], " ", "") != "stk.3,2.pkt.," {
		t.Errorf("%v %q", kinds, places)
	}
}

func TestQuotationsTheTextPrintsInOtherWords(t *testing.T) {
	note := "Rådets direktiv 93/6/EØF af 15. marts 1993, EF-Tidende nr. L 141, s. 1 (kapitalkravsdirektivet), Rådets direktiv 93/22/EØF af 10. maj 1993, EF-Tidende nr. L 141, s. 27 (investeringsservicedirektivet), Europa-Parlamentets og Rådets direktiv 95/26/EF"
	if got, ok := typoPrinted(note, "Rådets direktiv 93/22/EØF af 10. maj 1993, EF-Tidende 1993 nr. L 141, s. 27 (investeringsservicedirektivet),"); !ok || !strings.HasPrefix(got, "Rådets direktiv 93/22/EØF") {
		t.Errorf("a repeated year: %q %v", got, ok)
	}
	note = "(EF-Tidende 1989 nr. L 395, side 40), dele af Europa-Parlamentets og Rådets direktiv 2004/25/EF af 21. april 2004 om overtagelsestilbud, (EU-Tidende 2004 nr. L 142, side 12), Europa-Parlamentet og Rådets direktiv 2005/56/EF af 26. oktober 2005 om grænseoverskridende fusioner af selskaber med begrænset ansvar, (EU-Tidende 2005 nr. L 310, side 1), dele af"
	if _, ok := typoPrinted(note, ", og dele af Europa-Parlamentets og Rådets direktiv 2004/25/EF af 21. april 2004 om overtagelsestilbud, (EU-Tidende 2004 nr. L 142, side 12), Europa-Parlamentets og Rådets direktiv 2005/56/EF af 26. oktober 2005 om grænseoverskridende fu­sio­ner af selskaber med begrænset ansvar, (EU-Tidende 2005 nr. L 310, side 1)"); !ok {
		t.Errorf("soft hyphens and a number beside the passage")
	}
}

func TestACitationInAListAndAListsConnective(t *testing.T) {
	p := &Provision{Number: "59", Printed: "§ 59.", Segments: []Segment{
		{Text: "§ 59.", Break: true}, {Text: "Første.", Break: true},
		{Text: "Stk. 2.", Break: true}, {Text: "Overstiger prisen for anlæg som nævnt i §§ 56 c, 56 e og 57 a beløbet, beregnes et negativt tillæg. Tillægget for anlæg som nævnt i §§ 56 c og 57 a kan ikke overstige det udbetalte.", Break: true},
		{Text: "Stk. 3.", Break: true}, {Text: "Virksomhedspant kan omfatte", Break: true},
		{Text: "1)", Break: true}, {Text: "besætning, og", Break: true},
		{Text: "2)", Break: true}, {Text: "rettigheder efter lov om beskyttelse af halvlederprodukters udformning (topografi).", Break: true}}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	def := "I § 59, stk. 2, 1. og 2. pkt., ændres »§ 57 a« til: »§ 57 b«."
	if out := c.Apply(Edit{Kind: EditReplaceText, Address: ParseAddress(def), Definition: def, Old: "§ 57 a", New: "§ 57 b"}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	def = "I § 59, stk. 3, nr. 2, ændres »lov om halvlederprodukters udformning (topografi)« til: »lov om halvlederprodukters udformning (topografi) og«."
	if out := c.Apply(Edit{Kind: EditReplaceText, Address: ParseAddress(def), Definition: def, Old: "lov om halvlederprodukters udformning (topografi)", New: "lov om halvlederprodukters udformning (topografi) og"}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	got := Squeeze(p.Text())
	if strings.Contains(got, "57 a") || strings.Count(got, "57 b") != 2 || !strings.HasSuffix(got, "udformning (topografi) og") {
		t.Errorf("§ 59 = %q", got)
	}
}

func TestAQuotedNumberWithItsDigitsTransposed(t *testing.T) {
	text := "Skolens leder underretter kommunalbestyrelsen, jf. § 153, stk. 2, i lov om social service."
	if got, ok := transposedDigits(text, "jf. § 135, stk. 2, i lov om social service"); !ok || got != "jf. § 153, stk. 2, i lov om social service" {
		t.Errorf("got %q %v", got, ok)
	}
	if _, ok := transposedDigits(text, "jf. § 154, stk. 2, i lov om social service"); ok {
		t.Error("another number read as transposed")
	}
}
