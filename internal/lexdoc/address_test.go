package lexdoc

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestQuotedPassageIsNotAnAddress guards a contamination found by the corpus parity gate: the
// passage a change reproduces talks about numbering ("ændres »nr. 8« til: »nr. 9«"), and reading
// that as the address looked the change up somewhere other than where it said it was — often
// nowhere at all, so the change was refused against a provision that was right there.
func TestQuotedPassageIsNotAnAddress(t *testing.T) {
	got := ParseAddress("I § 119 a, stk. 1, ændres »nr. 8« til: »nr. 9«")
	if got.Paragraph != "119a" || got.Stk != 1 || got.Nr != 0 {
		t.Errorf("address = %+v, want § 119a stk. 1 with no item", got)
	}

	// A § inside the quoted passage is content too, and reading it as a target would edit the
	// wrong provision entirely.
	got = ParseAddress("I § 15, stk. 2, ændres »§ 120, stk. 3, nr. 4« til: »§ 120, stk. 4«")
	if got.Paragraph != "15" || got.Stk != 2 || got.Nr != 0 {
		t.Errorf("address = %+v, want § 15 stk. 2", got)
	}

	// An anchor outside the quotes is still an anchor: this one says where the new item goes.
	got = ParseAddress("I § 5, stk. 1, indsættes efter nr. 8 som nyt nummer")
	if got.Paragraph != "5" || got.Stk != 1 || got.Nr != 8 {
		t.Errorf("anchor address = %+v, want § 5 stk. 1 nr. 8", got)
	}

	// "Overalt i loven" must keep winning even with a paragraph inside the quoted text.
	got = ParseAddress("Overalt i loven ændres »§ 5, stk. 2« til: »§ 6«")
	if !got.Everywhere || got.Paragraph != "" {
		t.Errorf("everywhere address = %+v, want Everywhere with no paragraph", got)
	}
}

// TestOpenEndedQuoteAfterColonIsContent covers the footnote re-issue, whose quote never closes.
// B20230167305 change 1 reads "Fodnoten til bekendtgørelses titel affattes således: »1)
// Bekendtgørelsen gennemfører dele af ... Rådets forordning (EU) nr. 623/2012 ...", and the
// footnote runs past 1,300 characters without a closing mark. Leaving an unmatched quote alone is
// right for prose and wrong here: the change was looked up as this law's item 623, a provision
// nobody named, while the thing the change actually addressed — the footnote under the title —
// went unmentioned.
func TestOpenEndedQuoteAfterColonIsContent(t *testing.T) {
	prose := "Fodnoten til bekendtgørelses titel affattes således: »1) Bekendtgørelsen gennemfører " +
		"dele af Europa-Parlamentets og Rådets direktiv 2018/844/EU af 30. maj 2018, EU-Tidende " +
		"2018, nr. L 156, side 75, og Rådets forordning (EU) nr. 623/2012 af 11. juli 2012"
	if got := ParseAddress(prose); got.Nr != 0 || got.Paragraph != "" || got.Stk != 0 {
		t.Errorf("a footnote quoted after a colon was read as an address: %+v", got)
	}

	// The blanking runs to the end only when a colon introduces the mark. Without that guard a
	// stray quote in prose would blank the rest of the instruction, address included.
	if got := blankQuoted("Bemærk » og derefter § 9, stk. 2, nr. 3"); !strings.Contains(got, "§ 9") {
		t.Errorf("an unmatched quote mid-sentence blanked the rest of the text: %q", got)
	}
	if got := blankQuoted("affattes således: »1) jf. forordning nr. 623/2012"); strings.Contains(got, "623") {
		t.Errorf("an open quote after a colon left its content addressable: %q", got)
	}
}

// TestAddressSteps records the path an address names, beside the fields that decide behaviour
// today.
//
// The citation forms below are the ones tools/refusals reports as refused, copied out of the
// amending acts' own prose. BR10 refuses 145 of 153 changes on this shape ("Bilag I, kap. 1.3.5,
// stk. 7"), and the reason is the representation: a chapter is an int, and reBilag reads digits
// while the annex is printed "Bilag I". Steps carries what the fields cannot. It is not yet what
// resolves an edit — the annex has no nodes to walk into, which is M025T004 — so recording a path
// here changes nothing about which edits apply, and this test is what keeps the two
// representations honest while both exist.
func TestAddressSteps(t *testing.T) {
	for _, tc := range []struct {
		name, prose string
		want        []Step
	}{
		{
			name:  "roman annex, decimal chapter, subsection",
			prose: "Bilag I, kap. 1.12, stk. 3, affattes således: Stk. 3. Beslutter kommunalbestyrelsen …",
			want:  []Step{{KindBilag, "I"}, {KindKapitel, "1.12"}, {KindStykke, "3"}},
		},
		{
			name:  "chapter four levels deep",
			prose: "Bilag I, kap. 8.5.1.4, stk. 7, affattes således: Stk. 7. Kedler til fyring …",
			want:  []Step{{KindBilag, "I"}, {KindKapitel, "8.5.1.4"}, {KindStykke, "7"}},
		},
		{
			name:  "annex chapter without a subsection",
			prose: "Bilag I, kap. 7.4.3 ophæves.",
			want:  []Step{{KindBilag, "I"}, {KindKapitel, "7.4.3"}},
		},
		{
			// Bilag 1 numbers its sections with a letter in front, and cites them bare.
			name:  "lettered annex heading, cited by number alone",
			prose: "I bilag 1, B.1.1.2, indsættes efter stk. 1 som nyt stykke: Stk. 2. Vejarealet …",
			want:  []Step{{KindBilag, "1"}, {KindKapitel, "B.1.1.2"}, {KindStykke, "1"}},
		},
		{
			// BR18's ANNEX class: the container resolves, and the table inside it is the target.
			name:  "a table inside an annex",
			prose: "Bilag 1, tabel 2, affattes som bilag 1 til denne bekendtgørelse.",
			want:  []Step{{KindBilag, "1"}, {KindTabel, "2"}},
		},
		{
			name:  "annex holding provisions with a range",
			prose: "Bilag 3, § 5, stk. 2-5, affattes således: Stk. 2. Midlertidig og lejlighedsbestemt …",
			want:  []Step{{KindBilag, "3"}, {KindParagraf, "5"}, {KindStykke, "2"}},
		},
		{
			name:  "the plain form the fields already carry",
			prose: "I § 119 a, stk. 1, ændres »nr. 8« til: »nr. 9«",
			want:  []Step{{KindParagraf, "119a"}, {KindStykke, "1"}},
		},
		{
			name:  "chapter, afsnit, item and litra",
			prose: "Kapitel 7, afsnit II, § 12, stk. 3, nr. 4, litra b, ophæves.",
			want: []Step{{KindKapitel, "7"}, {KindAfsnit, "II"}, {KindParagraf, "12"},
				{KindStykke, "3"}, {KindNr, "4"}, {KindLitra, "b"}},
		},
	} {
		got := ParseAddress(tc.prose)
		if !reflect.DeepEqual(got.Steps, tc.want) {
			t.Errorf("%s:\n ParseAddress(%q).Steps = %v\n want %v", tc.name, tc.prose, got.Steps, tc.want)
			continue
		}
		if path := got.Path(); strings.Count(path, "/") != len(tc.want)-1 {
			t.Errorf("%s: Path() = %q, want %d levels", tc.name, path, len(tc.want))
		}
	}
}

// TestStepsAgreeWithFields is the guard against two authorities about where an edit goes. Steps is
// new and nothing resolves through it yet, so every level the old fields can express has to come
// out of the scanner the same way, or the day the resolver switches there will be a surprise
// rather than a measurement.
func TestStepsAgreeWithFields(t *testing.T) {
	for _, prose := range []string{
		"I § 267, stk. 2, nr. 3, ændres »X« til: »Y«",
		"I § 8 a ændres »X« til: »Y«",
		"Kapitel 4 ophæves.",
		"I afsnit XXIV, § 3, stk. 1, ændres »X« til: »Y«",
		"Bilag 9, § 1, stk. 2, affattes således",
		"I § 5, stk. 1, indsættes efter nr. 8 som nyt nummer",
	} {
		a := ParseAddress(prose)
		for _, s := range a.Steps {
			// A step keeps the number as printed, so an afsnit the collection printed in roman
			// has to be read back as one before the two representations can be compared.
			number := s.Number
			if s.Kind == KindAfsnit {
				number = RomanToNumber(s.Number)
			}
			want := map[string]string{
				KindKapitel:  strconv.Itoa(a.Chapter),
				KindBilag:    strconv.Itoa(a.Bilag),
				KindAfsnit:   strconv.Itoa(a.Afsnit),
				KindParagraf: a.Paragraph,
				KindStykke:   strconv.Itoa(a.Stk),
				KindNr:       strconv.Itoa(a.Nr),
				KindLitra:    a.Litra,
			}[s.Kind]
			if want != number {
				t.Errorf("%q: step %+v disagrees with the fields, which hold %q", prose, s, want)
			}
		}
	}
}

// TestBareDecimalNeedsAnAnnex holds the one rule parseSteps invents. A dotted number is how an
// annex heads its chapters, and only an annex does that: outside one, "1.3" is a figure in prose,
// as in a guidance note about energy frames. Reading it as an address would aim an edit at a
// chapter that was never named.
func TestBareDecimalNeedsAnAnnex(t *testing.T) {
	if got := ParseAddress("I bilag 6, i afsnittet »Tilbygninger«, ændres »145 kWh/m2« til: »135 kWh/m2«"); len(got.Steps) != 1 {
		t.Errorf("a figure outside an annex became a level: %+v", got.Steps)
	}
	got := ParseAddress("I bilag 1, B. 1.1.5, stk. 3, ændres »vejskel« til: »skel mod vej«")
	if len(got.Steps) != 3 || got.Steps[1].Number != "B.1.1.5" {
		t.Errorf("a chapter inside an annex was not recorded: %+v", got.Steps)
	}
}

// TestNoStepsWhereThereIsNoPath covers the two shapes with no address at all: a change spanning
// the whole act, and the quoted-passage contamination the scanner would otherwise walk straight
// into, since it reads the same blanked text the fields do.
func TestNoStepsWhereThereIsNoPath(t *testing.T) {
	if got := ParseAddress("Overalt i loven ændres »§ 5, stk. 2« til: »§ 6«"); len(got.Steps) != 0 {
		t.Errorf("a whole-act change got a path: %+v", got.Steps)
	}
	if got := ParseAddress("I § 15, stk. 2, ændres »§ 120, stk. 3, nr. 4« til: »§ 120, stk. 4«"); len(got.Steps) != 2 {
		t.Errorf("words inside the quotation became levels: %+v", got.Steps)
	}
	// "§ 8 og 9" is two provisions named in prose, not § 8o. The scanner defers to the same
	// rule the fields use, so it must not invent a litra either.
	if got := ParseAddress("I § 8 og 9 ændres »X« til: »Y«"); len(got.Steps) != 1 || got.Steps[0].Number != "8" {
		t.Errorf("a conjunction was read as a litra: %+v", got.Steps)
	}
}
