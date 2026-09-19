package lexdoc

import (
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
