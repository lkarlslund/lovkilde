package lexdoc

import (
	"strings"
	"testing"
	"time"
)

func TestAnActsEntryIntoForceIsReadByPart(t *testing.T) {
	signed := date(2008, 6, 17)
	f := ParseInForce([]string{
		"§ 4 Stk. 1. Loven træder i kraft den 1. januar 2009, jf. dog stk. 2-4. Stk. 2. § 1, nr. 13-15, træder i kraft den 1. juli 2008. Stk. 3. § 1, nr. 7, 10-12 og 21-24, træder i kraft den 1. august 2008. Stk. 4. § 1, nr. 2, 3 og 5, træder i kraft dagen efter bekendtgørelsen i Lovtidende.",
		"§ 12 Stk. 1. Lovens § 1, nr. 1, 6, 8, §§ 3-5 og § 7, nr. 3 og 4, træder i kraft den 1. juli 2005. Lovens § 2, § 6 og §§ 8-11 træder i kraft den 1. januar 2007.",
	}, signed)
	for _, c := range []struct {
		section, number string
		want            time.Time
	}{
		{"1", "14.", date(2008, 7, 1)},
		{"1", "11.", date(2008, 8, 1)},
		{"1", "3.", date(2008, 6, 18)},
		{"1", "4.", date(2009, 1, 1)},
		{"1", "6.", date(2005, 7, 1)},
		{"4", "1.", date(2005, 7, 1)},
		{"7", "3.", date(2005, 7, 1)},
		{"7", "1.", date(2009, 1, 1)},
		{"9", "2.", date(2007, 1, 1)},
		{"6", "1.", date(2007, 1, 1)},
	} {
		if got, ok := f.When(c.section, c.number); !ok || !got.Equal(c.want) {
			t.Errorf("§ %s, nr. %s: %v (%v), want %v", c.section, c.number, got, ok, c.want)
		}
	}
}

func TestAProvisionsOwnMarkerIsNotAPartOfTheAct(t *testing.T) {
	f := ParseInForce([]string{"§ 2 Loven træder i kraft den 1. august 2007. Givet på Christiansborg Slot, den 6. juni 2007"}, date(2007, 6, 6))
	if !f.Default.Equal(date(2007, 8, 1)) || len(f.Rules) != 0 {
		t.Errorf("read %+v, want the whole act from 1 August 2007", f)
	}
}

func TestPublicationDaysAreRead(t *testing.T) {
	signed := date(2008, 12, 19)
	for _, text := range []string{
		"§ 8 Stk. 1. Loven træder i kraft dagen efter lovens bekendtgørelse i Lovtidende, jf. dog stk. 2.",
		"§ 2 Stk. 1. Loven træder i kraft ved bekendtgørelsen i Lovtidende.",
		"§ 18 Stk. 2. Loven træder i kraft fra dagen efter bekendtgørelsen i Lovtidende.",
	} {
		if f := ParseInForce([]string{text}, signed); !f.Default.Equal(signed.AddDate(0, 0, 1)) {
			t.Errorf("%q read as %v", text, f.Default)
		}
	}
}

func TestAnOrderSettingAnActInForceIsRead(t *testing.T) {
	text := "I medfør af § 7, stk. 4, i lov nr. 462 af 9. juni 2004 om ændring af lov om kuldioxidafgift af visse energiprodukter bestemmes, at lovens § 1, nr. 21 1) træder i kraft den 1. juni 2005."
	f := ParseInForceOrder([]string{text}, "nr. 462 af 9. juni 2004")
	if d, ok := f.When("1", "21."); !ok || !d.Equal(date(2005, 6, 1)) {
		t.Errorf("§ 1, nr. 21 = %v (%v)", d, ok)
	}
	if _, ok := f.When("1", "20."); ok {
		t.Errorf("a change the order does not name was given a date")
	}
	whole := ParseInForceOrder([]string{"§ 1. Lov nr. 538 af 8. juni 2006 om ændring af retsplejeloven træder i kraft den 1. januar 2007."}, "nr. 538 af 8. juni 2006")
	if !whole.Default.Equal(date(2007, 1, 1)) {
		t.Errorf("the whole act = %v", whole.Default)
	}
	other := ParseInForceOrder([]string{"§ 1. Lov nr. 539 af 8. juni 2006 om noget andet træder i kraft den 1. januar 2007."}, "nr. 538 af 8. juni 2006")
	if !other.Default.IsZero() {
		t.Errorf("another act's date was taken")
	}
}

func TestAChangeNotYetInForceIsPendingNotApplied(t *testing.T) {
	c := &Content{Order: []Node{{Provision: &Provision{Number: "1", Segments: []Segment{{Text: "§ 1. Afgiften er 10 kr."}}}}}}
	def := "I § 1 ændres »10 kr.« til: »12 kr.«"
	chain := []Amendment{{Source: "A", Effective: date(2008, 6, 17), Edits: []Edit{
		{Number: "1.", Section: "1", Kind: EditReplaceText, Address: ParseAddress(def), Definition: def, Old: "10 kr.", New: "12 kr.", InForce: date(2009, 1, 1)},
	}}}
	before, out := c.AsOf(chain, date(2008, 12, 31))
	if got := before.Order[0].Provision.Text(); got != "§ 1. Afgiften er 10 kr." || len(out.Pending) != 1 {
		t.Errorf("before it takes effect: %q, pending %d", got, len(out.Pending))
	}
	after, out := c.AsOf(chain, date(2009, 1, 1))
	if got := after.Order[0].Provision.Text(); got != "§ 1. Afgiften er 12 kr." || len(out.Pending) != 0 {
		t.Errorf("once it takes effect: %q, pending %d", got, len(out.Pending))
	}
}

func TestTitleNoteToleratesMisquotation(t *testing.T) {
	// B20080084305 quotes "Kommissionens direktiv" where the note prints "Kommissions direktiv";
	// B20080108205 places an insertion by "De Cock& Man in't Veld", printed "De Cock & Man in´t Veld".
	note := "Bekendtgørelsen gennemfører dele af direktiv 2000/29/EF som senest ændret ved Kommissions direktiv 2004/102/EF (EUT 2004, L 309, s. 9), – Kommissionsbeslutning 2002/757/EF om Phytophthora ramorum Werres, De Cock & Man in´t Veld sp. nov. (EFT 2002, L 252, s. 37) og"
	c := &Content{Title: []Segment{{Text: "Bekendtgørelse om planter"}, {Text: "1)", Note: "FN501"}, {Text: note, Note: "FN501"}}}
	edits := []Edit{
		{Kind: EditReplaceText, Old: "som senest ændret ved Kommissionens direktiv 2004/102/EF (EUT 2004, L 309, s. 9)", New: "som senest ændret ved Kommissionens direktiv 2008/64/EF (EUT L 168, s. 31)"},
		{Kind: EditInsertText, Definition: "I fodnoten til bekendtgørelsens titel , indsættes efter »Phytophthora ramorum Werres, De Cock& Man in't Veld sp. nov. (EFT 2002, L 252, s. 37)«, »Kommissionsbeslutning 2008/840/EF«", New: ", Kommissionsbeslutning 2008/840/EF"},
	}
	for _, e := range edits {
		c.applyFootnote(e, func(r string) { t.Fatalf("%v refused: %s", e.Kind, r) }, func(string, string) {})
	}
	got := SegmentsText(c.Title)
	for _, want := range []string{"2008/64/EF", "s. 37), Kommissionsbeslutning 2008/840/EF"} {
		if !strings.Contains(got, want) {
			t.Errorf("title note lacks %q: %s", want, got)
		}
	}
}

// A part of an act taking effect late rewrites the text, in the order the acts were signed, ahead
// of an act signed after it that changes the words first. That act finds its words on the day it
// takes effect, and from the rewrite on nothing of it remains.
func TestAChangeOvertakenByALaterRewriteIsNotRefused(t *testing.T) {
	c := sampleLaw()
	day := func(y int) time.Time { return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC) }
	chain := []Amendment{
		{Source: "early", Effective: day(2021), Edits: []Edit{{Kind: EditReplaceProvision, Number: "4.", Address: ParseAddress("§ 2, stk. 2"),
			Definition: "§ 2, stk. 2, affattes således:", NewStykker: [][]Segment{{{Text: "Omskrevet stykke.", Break: true}}}, InForce: day(2024)}}},
		{Source: "between", Effective: day(2023), Edits: []Edit{{Kind: EditReplaceText, Address: ParseAddress("§ 2, stk. 2"),
			Definition: "I § 2, stk. 2, ændres »Andet« til: »Det andet«.", Old: "Andet", New: "Det andet"}}},
	}
	view, cons := c.AsOf(chain, day(2025))
	if len(cons.Unresolved) > 0 {
		t.Fatalf("refused: %+v", cons.Unresolved)
	}
	if got := textOf(t, view, "§ 2, stk. 2"); !strings.Contains(got, "Omskrevet") || strings.Contains(got, "Det andet") {
		t.Errorf("stk. 2 = %q, want the rewrite", got)
	}
	// A change whose words were never there is still refused.
	chain[1].Edits[0].Old, chain[1].Edits[0].Definition = "Femte", "I § 2, stk. 2, ændres »Femte« til: »Det andet«."
	if _, cons := c.AsOf(chain, day(2025)); len(cons.Unresolved) != 1 {
		t.Errorf("a change whose words were never there was not refused: %+v", cons.Applied)
	}
}

// An act drafted against the numbering before another act gave the provision a new subsection
// makes its changes where the text it names now stands, when every one of them is made there.
func TestAnActNumberingSubsectionsAsTheyStoodBeforeARenumbering(t *testing.T) {
	p := &Provision{Number: "16c", Printed: "§ 16 c.", Segments: []Segment{
		{Text: "§ 16 c.", Break: true}, {Text: "En debetsaldo forrentes med renten.", Break: true},
		{Text: "Stk. 2.", Break: true}, {Text: "En kreditsaldo forrentes ikke overhovedet.", Break: true},
		{Text: "Stk. 3.", Break: true}, {Text: "Overstiger en debetsaldo 5.000 kr., udsendes et rykkerbrev.", Break: true},
		{Text: "Stk. 4.", Break: true}, {Text: "En kreditsaldo udbetales, når der kan udbetales mindst 100 kr.", Break: true},
	}}
	p.Normalise()
	c := &Content{Order: []Node{{Provision: p}}}
	day := func(m int) time.Time { return time.Date(2016, time.Month(m), 1, 0, 0, 0, 0, time.UTC) }
	chain := []Amendment{
		{Source: "322", Effective: day(4), Edits: []Edit{{Kind: EditInsertProvision, Address: ParseAddress("§ 16 c, stk. 1"),
			Definition: "I § 16 c indsættes efter stk. 1 som nyt stykke:", NewStykker: [][]Segment{{{Text: "Stk. 2. Toldskyld forrentes med EU-renten.", Break: true}}}}}},
		{Source: "428", Effective: day(5), Edits: []Edit{
			{Kind: EditReplaceText, Address: ParseAddress("§ 16 c, stk. 3"), Definition: "I § 16 c, stk. 3, ændres »5.000 kr.« til: »6.000 kr.«", Old: "5.000 kr.", New: "6.000 kr."},
			{Kind: EditReplaceText, Address: ParseAddress("§ 16 c, stk. 4"), Definition: "I § 16 c, stk. 4, ændres »100 kr.« til: »200 kr.«", Old: "100 kr.", New: "200 kr."},
		}},
	}
	view, cons := c.AsOf(chain, day(12))
	if len(cons.Unresolved) > 0 {
		t.Fatalf("refused: %+v", cons.Unresolved)
	}
	if got := textOf(t, view, "§ 16 c, stk. 4"); !strings.Contains(got, "6.000 kr.") {
		t.Errorf("stk. 4 = %q, want the act's stk. 3", got)
	}
	if got := textOf(t, view, "§ 16 c, stk. 5"); !strings.Contains(got, "200 kr.") {
		t.Errorf("stk. 5 = %q, want the act's stk. 4", got)
	}
}

// A repeal taking effect late overtakes a change to the provision in the meantime, as a rewrite
// does; and a change another act has made already, word for word, is not refused.
func TestARepealAndARepeatOvertakeAChange(t *testing.T) {
	c := sampleLaw()
	day := func(y, m int) time.Time { return time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC) }
	chain := []Amendment{
		{Source: "early", Effective: day(2021, 1), Edits: []Edit{
			{Source: "early", Kind: EditDeleteProvision, Number: "3.", Address: ParseAddress("§ 1"), Definition: "§ 1 og § 3 ophæves.", InForce: day(2024, 1)},
			{Source: "early", Kind: EditReplaceText, Number: "4.", Address: ParseAddress("§ 2, stk. 2"), Definition: "I § 2, stk. 2, ændres »Andet stykke« til: »Nyt stykke«.", Old: "Andet stykke", New: "Nyt stykke", InForce: day(2023, 7)},
		}},
		{Source: "between", Effective: day(2023, 1), Edits: []Edit{
			{Source: "between", Kind: EditReplaceText, Address: ParseAddress("§ 3"), Definition: "I § 3 ændres »udledning« til: »udslip«.", Old: "udledning", New: "udslip"},
			{Source: "between", Kind: EditReplaceText, Address: ParseAddress("§ 2, stk. 2"), Definition: "I § 2, stk. 2, ændres »Andet stykke« til: »Nyt stykke«.", Old: "Andet stykke", New: "Nyt stykke"},
		}},
	}
	_, cons := c.AsOf(chain, day(2025, 1))
	if len(cons.Unresolved) > 0 {
		t.Fatalf("refused: %+v", cons.Unresolved)
	}
	notes := 0
	for _, a := range cons.Applied {
		if a.Edit.Source == "between" && (strings.Contains(a.Note, "repealed") || strings.Contains(a.Note, "made the same change already")) {
			notes++
		}
	}
	if notes != 2 {
		for _, a := range cons.Applied {
			t.Logf("%s %s @%s: %s", a.Edit.Source, a.Edit.Number, a.Where, a.Note)
		}
		t.Errorf("want both of the later act's changes settled by the earlier act's")
	}
}

// A part an act leaves to a minister is not in force until an order sets it (sømandsloven's § 64 b:
// "Da § 2 i lov nr. 1231 af 27. december 2003 stadig ikke er sat i kraft"). The act's own date is
// not its date; an order's is. A minister setting parts of another law in force is no rule of this
// act's.
func TestAPartLeftToAMinisterWaitsForTheOrder(t *testing.T) {
	act := ParseInForce([]string{"§ 3 Stk. 1. Loven træder i kraft den 1. januar 2004, jf. dog stk. 2. Stk. 2. Økonomi- og erhvervsministeren fastsætter tidspunktet for ikrafttræden af § 2."}, date(2003, 12, 27))
	if got, _ := act.When("2", "1."); !got.Equal(Deferred) {
		t.Errorf("§ 2 takes effect %v, want deferred", got)
	}
	if got, _ := act.When("1", "1."); !got.Equal(date(2004, 1, 1)) {
		t.Errorf("§ 1 takes effect %v, want 1 January 2004", got)
	}
	parts := ParseInForce([]string{"§ 7 Stk. 1. Loven træder i kraft den 1. juli 2019. Stk. 2. Beskæftigelsesministeren fastsætter tidspunktet for ikrafttræden af § 2, nr. 20, 23 og 90."}, date(2019, 5, 7))
	if got, _ := parts.When("2", "23."); !got.Equal(Deferred) {
		t.Errorf("§ 2, nr. 23, takes effect %v, want deferred", got)
	}
	if got, _ := parts.When("2", "21."); !got.Equal(date(2019, 7, 1)) {
		t.Errorf("§ 2, nr. 21, takes effect %v, want 1 July 2019", got)
	}
	ordered := act
	ordered.Rules = append(ordered.Rules, ParseInForceOrder([]string{"I medfør af § 3, stk. 2, i lov nr. 1231 af 27. december 2003 bestemmes, at lovens § 2 træder i kraft den 1. maj 2006."}, "nr. 1231 af 27. december 2003").Rules...)
	if got, _ := ordered.When("2", "1."); !got.Equal(date(2006, 5, 1)) {
		t.Errorf("§ 2 set in force by order takes effect %v, want 1 May 2006", got)
	}
	other := ParseInForce([]string{"§ 9 Stk. 1. Loven træder i kraft den 1. juli 2011. Stk. 3. Skatteministeren fastsætter tidspunktet for ikrafttræden af § 11, stk. 17, § 11 f, bilag 2-8 og bilag 5 i lov om afgift af elektricitet som affattet ved denne lovs § 1."}, date(2010, 12, 21))
	if len(other.Rules) != 0 {
		t.Errorf("another law's provisions read as this act's: %+v", other.Rules)
	}
	// Lov nr. 1337 of 2008: the changes named by what they bring.
	named := ParseInForce([]string{"§ 4 Stk. 1. Loven træder i kraft den 1. januar 2009, jf. dog stk. 2. Stk. 2. Skatteministeren fastsætter ikrafttrædelsestidspunktet for ændringerne i ligningslovens § 16 som affattet ved denne lovs § 1, nr. 6 og 7, ændringerne i vægtafgiftslovens § 4 som affattet ved denne lovs § 2, nr. 1, og ændringerne i vægtafgiftslovens § 23 som affattet ved denne lovs § 2, nr. 4-6."}, date(2008, 12, 19))
	for _, c := range [][2]string{{"1", "7."}, {"2", "1."}, {"2", "5."}} {
		if got, _ := named.When(c[0], c[1]); !got.Equal(Deferred) {
			t.Errorf("§ %s, nr. %s, takes effect %v, want deferred", c[0], c[1], got)
		}
	}
	if got, _ := named.When("2", "3."); !got.Equal(date(2009, 1, 1)) {
		t.Errorf("§ 2, nr. 3, takes effect %v, want 1 January 2009", got)
	}
}

// An act names its changes by the provisions of the law they bring ("§ 52 a, stk. 1, i lov om
// vandforsyning m.v. som affattet ved denne lovs § 3, nr. 1"), its own beside another law's, and
// spells the minister's part "ikrafttrædelsen".
func TestAnActsChangesNamedByTheLawTheyAmend(t *testing.T) {
	f := ParseInForce([]string{"§ 7 Stk. 1. Loven træder i kraft den 1. januar 2010, jf. dog stk. 2. Stk. 2. § 52 a, stk. 1, i lov om vandforsyning m.v. som affattet ved denne lovs § 3, nr. 1, træder i kraft den 1. januar 2011."}, date(2009, 12, 27))
	if got, _ := f.When("3", "1."); !got.Equal(date(2011, 1, 1)) {
		t.Errorf("§ 3, nr. 1 = %v", got)
	}
	g := ParseInForce([]string{"§ 7 Stk. 3. Lovens § 3, nr. 2 og 3, og § 5, nr. 3-5, og §§ 3 b og 3 c i lov om elektroniske cigaretter m.v. som affattet ved denne lovs § 3, nr. 4, træder i kraft den 31. juli 2021. Stk. 4. Erhvervsministeren fastsætter tidspunktet for ikrafttrædelsen af lovens § 3, nr. 16."}, date(2020, 12, 21))
	for _, c := range []struct {
		section, number string
		want            time.Time
	}{{"3", "2.", date(2021, 7, 31)}, {"5", "4.", date(2021, 7, 31)}, {"3", "4.", date(2021, 7, 31)}, {"3", "16.", Deferred}} {
		if got, _ := g.When(c.section, c.number); !got.Equal(c.want) {
			t.Errorf("§ %s, nr. %s = %v, want %v", c.section, c.number, got, c.want)
		}
	}
}

func TestAnOrderSettingTheActButForAProvision(t *testing.T) {
	order := ParseInForceOrder([]string{"I medfør af § 13, stk. 1 og 2, i lov nr. 539 af 8. juni 2006 om ændring af lov om tinglysning og forskellige andre love (Digital tinglysning) bestemmes, at loven med undtagelse af § 12 træder i kraft den 8. september 2009 kl. 9.00 for så vidt angår tingbogen."}, "nr. 539 af 8. juni 2006")
	if !order.Default.Equal(date(2009, 9, 8)) {
		t.Errorf("the act = %v", order.Default)
	}
}
