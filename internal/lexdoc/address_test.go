package lexdoc

import "testing"

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
