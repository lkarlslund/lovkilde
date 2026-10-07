package lexdoc

import (
	"math/rand"
	"strings"
	"testing"
)

// TestSqueezeIsJoinFields: Squeeze replaced strings.Join(strings.Fields(s), " ") for speed and
// must answer the same for every input, Unicode spaces (no-break, next-line, ideographic)
// included — and hand back the input itself when it is already squeezed.
func TestSqueezeIsJoinFields(t *testing.T) {
	cases := []string{"", " ", "a", " a", "a ", "a  b", "a\tb", "a b", " ", "a\u0085b",
		"a b", "a　b", "æ ø å", "  §  1.  Stk. 2 ", "a\vb", "a\fb", "a\r\nb", "ab cd ef"}
	alphabet := []string{"a", "æ", "§", " ", "  ", "\t", "\n", " ", " ", "\u0085", "1", "."}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		var b strings.Builder
		for n := rng.Intn(12); n > 0; n-- {
			b.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		cases = append(cases, b.String())
	}
	for _, s := range cases {
		if got, want := Squeeze(s), strings.Join(strings.Fields(s), " "); got != want {
			t.Fatalf("Squeeze(%q) = %q, want %q", s, got, want)
		}
	}
	if s := "already squeezed"; Squeeze(s) != s {
		t.Error("a squeezed string did not come back as itself")
	}
}

// TestListOpeningPrefilterLosesNothing: the cheap check in front of reListOpens must pass every
// text the pattern matches, however it is cased.
func TestListOpeningPrefilterLosesNothing(t *testing.T) {
	for _, s := range []string{
		"I lov om leje foretages følgende ændringer:",
		"I LOV OM LEJE FORETAGES FØLGENDE ÆNDRINGER:",
		"I loven Foretages følgende ændring:",
		"fOrEtAgEs i § 3 følgende ændring",
		"Der foretages ikke noget",
	} {
		if reListOpens.MatchString(s) != opensList(s) {
			t.Errorf("%q: pattern %v, prefiltered %v", s, reListOpens.MatchString(s), opensList(s))
		}
	}
}
