package lexdoc

import (
	"strings"
	"testing"
)

func cell(table, row, col int, text string) Segment {
	return Segment{Text: text, Break: true, Table: table, Row: row, Col: col}
}

// The pesticide orders change their limits cell by cell. A substance the group's table lists has
// its cell set (the 2007 order prints Chlormequat's 0,2m for pears where the 2006 order had 0,3m);
// one it does not list gets a row after the one the change names.
func TestATablesCellsAreChangedWhereTheChangeNamesThem(t *testing.T) {
	segs := []Segment{{Text: "Bilag 2", Break: true},
		// The header prints no cell over the substances: its columns line up from the right.
		cell(1, 1, 1, "KERNE- FRUGTER"), cell(1, 1, 2, "Kvæder"), cell(1, 1, 3, "Pærer"), cell(1, 1, 4, "Æbler"), cell(1, 1, 5, "Andre"),
		cell(1, 2, 1, "Chlorfenvinphos"), cell(1, 2, 1, "36)"), cell(1, 2, 2, "0,05"),
		cell(1, 3, 1, "Chlormequat"), cell(1, 3, 4, "0,3m"), cell(1, 3, 6, "0,05*"),
		cell(1, 4, 1, "Methomyl/ Thiodicarb"), cell(1, 4, 3, "0,5"),
		cell(1, 5, 1, "Zineb"), cell(1, 5, 2, "1"),
	}
	c := &Content{Appendices: []Appendix{{Number: "2", Label: "Bilag 2", Segments: segs}}}
	apply := func(def string) {
		t.Helper()
		if out := c.Apply(Edit{Kind: EditUnknown, Address: ParseAddress(def), Definition: def}); !out.Applied {
			t.Fatalf("%q refused: %s", def, out.Reason)
		}
	}
	apply("I bilag 2 indsættes i gruppen »kernefrugter« efter »Chlorfenvinphos«: »Chlormequat« med maksimalgrænseværdien »0,2m« for produktet »Pærer«.")
	if got := cellText(c.Appendices[0].Segments, 1, 3, 4); got != "0,2m" {
		t.Errorf("Chlormequat, pærer = %q", got)
	}
	apply("I bilag 2 indsættes i gruppen »kernefrugter« efter »Thiodicarb«: »Thiophanat-methyl« med maksimalgrænseværdien »1« for produktet »Pærer« og med maksimalgrænseværdien »0,1*« i »Andre«.")
	ap := c.Appendices[0].Segments
	if cellText(ap, 1, 5, 1) != "Thiophanat-methyl" || cellText(ap, 1, 5, 4) != "1" || cellText(ap, 1, 5, 6) != "0,1*" || cellText(ap, 1, 6, 1) != "Zineb" {
		t.Errorf("rows after the insertion: %v", SegmentsText(ap))
	}
	if out := c.Apply(Edit{Kind: EditUnknown, Definition: "I bilag 2 indsættes i gruppen »kernefrugter« efter »Captan«: »Folpet« med maksimalgrænseværdien »3« for produktet »Pærer«."}); out.Applied {
		t.Error("a row placed by a substance the table does not list")
	}
}

// A table listing a row per substance and product loses the row a change removes.
func TestAListTablesRowIsRemoved(t *testing.T) {
	segs := []Segment{cell(1, 1, 1, "Boscalid"), cell(1, 1, 2, "Hovedsalat"), cell(1, 1, 3, "3,0"),
		cell(1, 2, 1, "Boscalid"), cell(1, 2, 2, "Hvid- og rødkål"), cell(1, 2, 3, "1,0"), cell(1, 3, 1, "Captan"), cell(1, 3, 2, "Æbler"), cell(1, 3, 3, "3")}
	c := &Content{Appendices: []Appendix{{Number: "6", Label: "Bilag 6", Segments: segs}}}
	def := "I Bilag 6 udgår »Boscalid« i produktet »hovedsalat« med maksimalgrænseværdien »3,0« mg/kg."
	if out := c.Apply(Edit{Kind: EditUnknown, Definition: def}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	got := SegmentsText(c.Appendices[0].Segments)
	if strings.Contains(got, "Hovedsalat") || !strings.Contains(got, "Hvid- og rødkål") || cellText(c.Appendices[0].Segments, 1, 2, 1) != "Captan" {
		t.Errorf("bilag 6 = %q", got)
	}
}

// An act may print its limits in tables of its own and order them set in the law's (the pesticide
// order of 2006, nr. 1162): tables grouping products under a group in capitals, described in a row
// above the header and split over two tables; groups of one column named in the row above; columns
// standing over the data without a group; and a list of substance, product and limit.
func TestAnActsTablesOfLimitsAreSetInTheLaws(t *testing.T) {
	law := []Segment{{Text: "Bilag 2", Break: true},
		cell(1, 1, 1, "Pesticidrester"), cell(1, 1, 2, "Maksimalgrænseværdier i mg/kg for grupper"),
		cell(1, 2, 1, "BÆR OG SMÅFRUGTER I, friske"),
		cell(1, 3, 1, "BÆR OG SMÅ- FRUGTER"), cell(1, 3, 2, "Druer"), cell(1, 3, 3, "Andre"),
		cell(1, 4, 1, "Captan"), cell(1, 4, 3, "0,02*"),
		cell(2, 1, 1, "Pesticidrester"), cell(2, 1, 2, "Maksimalgrænseværdier i mg/kg for grupper"),
		cell(2, 2, 1, "BÆR OG SMÅFRUGTER II, friske"),
		cell(2, 3, 1, "BÆR OG SMÅ- FRUGTER"), cell(2, 3, 2, "Bær, vilde"), cell(2, 3, 3, "Andre"),
		cell(2, 4, 1, "Captan"), cell(2, 4, 3, "0,02*"),
		cell(3, 1, 1, "TE"), cell(3, 1, 2, "HUMLE"), cell(3, 1, 3, "KARTOFLER"),
		cell(3, 2, 1, "TE (tørrede blade)"), cell(3, 2, 2, "HUMLE, tørret"), cell(3, 2, 3, "KARTOFLER"),
		cell(3, 3, 1, "Abamectin"), cell(3, 3, 2, "0,02*"),
		cell(3, 4, 1, "Zineb"), cell(3, 4, 3, "1"),
	}
	act := []Segment{{Text: "Bilag 1", Break: true},
		cell(1, 1, 1, "Pesticidrester"), cell(1, 1, 2, "Maksimalgrænseværdier i mg/kg for grupper"),
		cell(1, 2, 1, "BÆR OG SMÅFRUGTER II, friske"),
		cell(1, 3, 1, "BÆR OG SMÅFRUGTER"), cell(1, 3, 2, "Bær, vilde"), cell(1, 3, 3, "Andre"),
		cell(1, 4, 1, "Captan"), cell(1, 4, 2, "0,1"),
		cell(2, 1, 1, "TE"), cell(2, 1, 2, "HUMLE"), cell(2, 1, 3, "KARTOFLER"),
		cell(2, 2, 1, "Te (tørrede blade)"), cell(2, 2, 2, "HUMLE, tørret"), cell(2, 2, 3, "KARTOFLER"),
		cell(2, 3, 1, "Deltamethrin"), cell(2, 3, 2, "5"), cell(2, 3, 3, "5"),
	}
	c := &Content{Appendices: []Appendix{{Number: "2", Label: "Bilag 2", Segments: law}}}
	def := "I Bilag 2 indsættes maksimalgrænseværdier som angivet i bilag 1 til denne bekendtgørelse."
	if out := c.Apply(Edit{Kind: EditUnknown, Address: ParseAddress(def), Definition: def, NewAppendix: &Appendix{Number: "1", Segments: act}}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	got := c.Appendices[0].Segments
	if cellText(got, 2, 4, 2) != "0,1" || cellText(got, 1, 4, 2) != "" {
		t.Errorf("Captan, vilde bær: II %q, I %q", cellText(got, 2, 4, 2), cellText(got, 1, 4, 2))
	}
	if cellText(got, 3, 4, 1) != "Deltamethrin" || cellText(got, 3, 4, 2) != "5" || cellText(got, 3, 4, 3) != "5" || cellText(got, 3, 5, 1) != "Zineb" {
		t.Errorf("te and humle: %v", SegmentsText(got))
	}

	animal := []Segment{cell(1, 1, 1, "Pesticidrester"), cell(1, 1, 2, "Maksimalgrænseværdier i mg/kg"),
		cell(1, 2, 2, "For fedtindhold i kød (1)(4)"), cell(1, 2, 3, "For rå mælk som anført under pos. 0401 (2)(4)"),
		cell(1, 3, 1, "Aldrin"), cell(1, 3, 2, "0,2"), cell(1, 3, 3, "0,006"),
	}
	list := []Segment{cell(2, 1, 1, "Pesticidrest"), cell(2, 1, 2, "Produkt"), cell(2, 1, 3, "Maksimalgrænse- værdi mg/kg"),
		cell(2, 2, 1, "Boscalid"), cell(2, 2, 2, "Kirsebær"), cell(2, 2, 3, "1"),
		cell(2, 3, 1, "Boscalid"), cell(2, 3, 2, "Blommer"), cell(2, 3, 3, "0,5"),
		cell(2, 4, 1, "Tolylfluanid"), cell(2, 4, 2, "Æbler"), cell(2, 4, 3, "1"),
		cell(2, 5, 1, "Tolylfluanid"), cell(2, 5, 2, "Pærer"), cell(2, 5, 3, "1"),
	}
	c = &Content{Appendices: []Appendix{{Number: "4A", Label: "Bilag 4A", Segments: animal}, {Number: "6", Label: "Bilag 6", Segments: list}}}
	act = []Segment{cell(1, 1, 1, "Pesticidrester"), cell(1, 1, 2, "Maksimalgrænseværdier i mg/kg"),
		cell(1, 2, 2, "For fedtindhold i kød, tilberedt kød (1)(4)"), cell(1, 2, 3, "For rå mælk og sødmælk (2)(4)"),
		cell(1, 3, 1, "Carbaryl"), cell(1, 3, 2, "0,5*"), cell(1, 3, 3, "0,05*"),
	}
	def = "I Bilag 4A indsættes maksimalgrænseværdier som angivet i bilag 3 til denne bekendtgørelse."
	if out := c.Apply(Edit{Kind: EditUnknown, Address: ParseAddress(def), Definition: def, NewAppendix: &Appendix{Number: "3", Segments: act}}); !out.Applied {
		t.Fatalf("4A refused: %s", out.Reason)
	}
	got = c.Appendices[0].Segments
	if cellText(got, 1, 4, 1) != "Carbaryl" || cellText(got, 1, 4, 2) != "0,5*" || cellText(got, 1, 4, 3) != "0,05*" || cellText(got, 1, 3, 1) != "Aldrin" {
		t.Errorf("4A: %v", SegmentsText(got))
	}
	act = []Segment{cell(1, 1, 1, "Pesticidrest"), cell(1, 1, 2, "Produkt"), cell(1, 1, 3, "Maksimalgrænseværdi mg/kg"),
		cell(1, 2, 1, "Boscalid"), cell(1, 2, 2, "Spinat"), cell(1, 2, 3, "0,05*"),
		cell(1, 3, 1, "Boscalid"), cell(1, 3, 2, "Kirsebær"), cell(1, 3, 3, "2"),
		cell(1, 4, 1, "Tolylfluanid"), cell(1, 4, 2, "Æbler, pærer"), cell(1, 4, 3, "2"),
	}
	def = "I Bilag 6 indsættes maksimalgrænseværdier som angivet i bilag 5 til denne bekendtgørelse."
	if out := c.Apply(Edit{Kind: EditUnknown, Address: ParseAddress(def), Definition: def, NewAppendix: &Appendix{Number: "5", Segments: act}}); !out.Applied {
		t.Fatalf("6 refused: %s", out.Reason)
	}
	got = c.Appendices[1].Segments
	want := [][3]string{{"Boscalid", "Kirsebær", "2"}, {"Boscalid", "Blommer", "0,5"}, {"Boscalid", "Spinat", "0,05*"}, {"Tolylfluanid", "Æbler, pærer", "2"}}
	for i, w := range want {
		if row := i + 2; cellText(got, 2, row, 1) != w[0] || cellText(got, 2, row, 2) != w[1] || cellText(got, 2, row, 3) != w[2] {
			t.Errorf("bilag 6 row %d: %v", row, SegmentsText(got))
		}
	}
	if cellText(got, 2, 6, 1) != "" {
		t.Errorf("bilag 6 keeps a row too many: %v", SegmentsText(got))
	}
}

// A change to an annex's table of contents removes a row by its text and puts in a new row after
// another, split as that row is (the ship order's bilag 3, 2021); a heading changed later is the
// rule's own, not the new row's.
func TestATableOfContentsRowIsRemovedAndAdded(t *testing.T) {
	segs := []Segment{
		cell(1, 1, 1, "Regel 3-12"), cell(1, 1, 2, "Beskyttelse mod støj"),
		cell(1, 2, 1, "Regel 3-13"), cell(1, 2, 2, "Konstruktion og installation af hængedæk"),
		cell(1, 3, 1, "Regel 19"), cell(1, 3, 2, "Havarikontrolplaner"),
		cell(1, 4, 1, "Regel 20"), cell(1, 4, 2, "Lastning af passagerskibe"),
		{Text: "Regel 19 Havarikontrolplaner", Break: true},
		{Text: "Regel 19-1 – Havarikontrolplaner for passagerskibe", Break: true},
		{Text: "1 Der skal være en plan.", Break: true},
	}
	c := &Content{Appendices: []Appendix{{Number: "3", Label: "Bilag 3", Segments: segs}}}
	prose := "1. I bilag 3 foretages følgende ændringer i indholdsfortegnelsen: Teksten ”Regel 3-13 Konstruktion og installation af hænge-dæk\" slettes Efter teksten \"Regel 19 Havarikontrolplaner\" indsættes ny række med følgende tekst: Regel 19-1 Øvelser i havarikontrol for passagerskibe"
	if out := c.Apply(Edit{Kind: EditUnknown, Address: ParseAddress("I bilag 3"), Prose: prose, Definition: "I bilag 3 foretages følgende ændringer i indholdsfortegnelsen:"}); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	got := c.Appendices[0].Segments
	rows := [][2]string{{"Regel 3-12", "Beskyttelse mod støj"}, {"Regel 19", "Havarikontrolplaner"}, {"Regel 19-1", "Øvelser i havarikontrol for passagerskibe"}, {"Regel 20", "Lastning af passagerskibe"}}
	for i, r := range rows {
		if cellText(got, 1, i+1, 1) != r[0] || cellText(got, 1, i+1, 2) != r[1] {
			t.Errorf("row %d: %v", i+1, SegmentsText(got))
		}
	}
}

func TestASchemeEntryGoesAfterTheNumberNamed(t *testing.T) {
	ap := &Appendix{Number: "4", Segments: []Segment{
		{Text: "3.3.", Table: 1, Row: 1, Col: 2}, {Text: "Ændring i erstatningshensættelser", Table: 1, Row: 1, Col: 3},
		{Text: "3.4.", Table: 1, Row: 2, Col: 2}, {Text: "Ændring i genforsikringsandel", Table: 1, Row: 2, Col: 3},
		{Text: "3.", Table: 1, Row: 3, Col: 1}, {Text: "Erstatningsudgifter i alt", Table: 1, Row: 3, Col: 2}}}
	if _, ok := insertNumberedRow(ap, "3.3", "3.4. Ændring i risikomargen"); !ok {
		t.Fatal("not placed")
	}
	var got []string
	for _, s := range ap.Segments {
		got = append(got, s.Text)
	}
	want := "3.3.|Ændring i erstatningshensættelser|3.4.|Ændring i risikomargen|3.5.|Ændring i genforsikringsandel|3.|Erstatningsudgifter i alt"
	if strings.Join(got, "|") != want {
		t.Errorf("%s", strings.Join(got, "|"))
	}
}

func TestSentencesAndLettersGoIntoAnAnnexPoint(t *testing.T) {
	ap := &Appendix{Number: "2", Segments: []Segment{
		{Text: "27.", Break: true}, {Text: "Investeringsområdet: Bestyrelsen vedtager en politik.", Break: true},
		{Text: "Investeringsområdet omfatter markedsrisici. Ved markedsrisici forstås kursrisici.", Break: true},
		{Text: "28.", Break: true}, {Text: "For nedskrivninger skal revisionen bestå i:", Break: true},
		{Text: "a.", Break: true}, {Text: "En stikprøve.", Break: true}, {Text: "b.", Break: true}, {Text: "En gennemgang.", Break: true},
		{Text: "29.", Break: true}, {Text: "Andet.", Break: true}}}
	if _, ok := insertAnnexSentence(ap, "I bilag 2, pkt. 27, indsættes efter 2. pkt.:", "Derudover omfatter området livsrisici."); !ok {
		t.Fatal("sentence not placed")
	}
	if got := ap.Segments[2].Text; got != "Investeringsområdet omfatter markedsrisici. Derudover omfatter området livsrisici. Ved markedsrisici forstås kursrisici." {
		t.Errorf("pkt. 27 = %q", got)
	}
	if _, ok := insertAnnexLetter(ap, "I bilag 2, punkt 28, indsættes før litra a som nyt litra:", "a. En gennemgang af forretningsgange."); !ok {
		t.Fatal("letter not placed")
	}
	var got []string
	for _, s := range ap.Segments[4:11] {
		got = append(got, s.Text)
	}
	if want := "For nedskrivninger skal revisionen bestå i:|a.|En gennemgang af forretningsgange.|b.|En stikprøve.|c.|En gennemgang."; strings.Join(got, "|") != want {
		t.Errorf("pkt. 28 = %s", strings.Join(got, "|"))
	}
}

// An order printed as text ends with its endnotes under "Officielle noter", after its last annex:
// they are the order's, not the annex's.
func TestAnActsAnnexEndsAtItsEndnotes(t *testing.T) {
	ap := AnnexFromLines([]string{"1. I bilag 4 indsættes som nr. 11.2.a bilag 2 til denne bekendtgørelse.", "Bilag 2", "11.2.a", "Planter af Castanea Mill.", "Officielle noter", "1) Bekendtgørelsen gennemfører beslutning K2006/2881."}, 2, false)
	if ap == nil || SegmentsText(ap.Segments) != "11.2.a Planter af Castanea Mill." {
		t.Fatalf("annex = %+v", ap)
	}
}

// "I Bilag 1 under punktet »Bogstavhenvisning p« ophæves »Bentazon:11. juni 2006«, »…« og »…«":
// the first quotation says where, the ones after the verb go. Read the other way, the annex lost
// its heading and kept the three dates.
func TestPassagesRepealedUnderANamedPointGoAndThePointStays(t *testing.T) {
	c := &Content{Appendices: []Appendix{{Number: "1", Segments: []Segment{{Text: "Bilag 1", Break: true}, {Text: "Bogstavhenvisning", Break: true},
		{Text: "p", Table: 3, Row: 1, Col: 1, Break: true}, {Text: "Amitrol: 14. juli 2007", Table: 3, Row: 2, Col: 2, Break: true},
		{Text: "Bentazon: 11. juni 2006", Table: 3, Row: 2, Col: 2, Break: true}, {Text: "Metsulfuron-methyl: 27. september 2006", Table: 3, Row: 10, Col: 2, Break: true},
		{Text: "Pyridat: 11. juni 2006", Table: 3, Row: 13, Col: 2, Break: true}}}}}
	edits := ReadChangeLists([]string{"I bekendtgørelse nr. 375 af 2006 foretages følgende ændringer:",
		"1. I Bilag 1 under punktet »Bogstavhenvisning p« ophæves »Bentazon:11. juni 2006«, »Metsulfuron-methyl: 27. september 2006« og »Pyridat: 11. juni 2006«."}, "X")
	if len(edits) != 1 {
		t.Fatalf("edits: %+v", edits)
	}
	if out := c.Apply(edits[0]); !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := SegmentsText(c.Appendices[0].Segments); got != "Bilag 1 Bogstavhenvisning p Amitrol: 14. juli 2007" {
		t.Errorf("bilag 1 = %q", got)
	}
}

// "I bilag 1 indsættes som nyt punkt 2.6:" names the new point, not a place: it goes in front of
// the 2.6 the annex prints, and 2.6-2.7 move up. It used to go at the end of the annex.
func TestANewPointGoesInFrontOfTheOneWithItsNumber(t *testing.T) {
	c := &Content{Appendices: []Appendix{{Number: "1", Segments: []Segment{{Text: "Bilag 1", Break: true}, {Text: "2.5 Femte.", Break: true},
		{Text: "2.6 Sjette.", Break: true}, {Text: "2.7 Syvende.", Break: true}, {Text: "3. Afsnit tre", Break: true}, {Text: "3.1 Første.", Break: true}}}}}
	def := "I bilag 1 indsættes som nyt punkt 2.6:"
	out := c.Apply(Edit{Kind: EditInsertProvision, Address: ParseAddress(def), Definition: def, NewText: []Segment{{Text: "2.6 Ny.", Break: true}}})
	if !out.Applied {
		t.Fatalf("refused: %s", out.Reason)
	}
	if got := SegmentsText(c.Appendices[0].Segments); got != "Bilag 1 2.5 Femte. 2.6 Ny. 2.7 Sjette. 2.8 Syvende. 3. Afsnit tre 3.1 Første." {
		t.Errorf("bilag 1 = %q", got)
	}
}

// An annex quoting a provision prints it with a slip of its own; a long quotation is the one
// passage opening and closing with its words, of about its length.
func TestALongQuotationIsFoundByItsEnds(t *testing.T) {
	text := "Den eksterne revision skal redegøre for kontrollen i henhold til § 9, stk. 2, i bekendtgørelse om registrering af aktiver i direkte tegnende forsikringsselskaber, tværgående pensionskasser og filialer af udenlandsk tegnende forsikringsselskaber. Der skal redegøres for aktiver i direkte tegnende forsikringsselskaber."
	quoted := "§ 9, stk. 2, i bekendtgørelse om registrering af aktiver i direkte tegnende forsikringsselskaber, tværgående pensionskasser og filialer af udenlandske direkte tegnende forsikringsselskaber"
	got, ok := anchoredPrinted(text, quoted)
	if !ok || !strings.HasSuffix(got, "af udenlandsk tegnende forsikringsselskaber") || !strings.HasPrefix(got, "§ 9, stk. 2,") {
		t.Errorf("got %q %v", got, ok)
	}
	if _, ok := anchoredPrinted(text, "§ 9, stk. 2, i bekendtgørelse om registrering"); ok {
		t.Error("a short quotation read by its ends")
	}
}
