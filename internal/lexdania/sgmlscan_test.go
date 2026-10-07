package lexdania

import (
	"math/rand"
	"strings"
	"testing"
)

// TestSGMLScanIsThePattern: hasSGMLAttrs and unquoteSGML replaced the pattern's MatchString and
// ReplaceAllString for speed, and must answer and write the same for every payload, including the near misses: a doubled quote in prose, a name
// of digits only, spaces around the equals sign, a value that runs to the end.
func TestSGMLScanIsThePattern(t *testing.T) {
	cases := []string{``, `""`, `a=""b""`, `NAME=""P1""`, `a=""b""c=""d""`, `5ab=""x""`, `<a NAME=""P1"" CLASS=""x"">`, `name = ""P1""`, `5=""x""`, `x5=""y""`,
		`-a=""b""`, `a.b=""c""`, `say ""hello"" there`, `a=""b"`, `a="" b=""c""`, `a=""""`,
		`<p CLASS=""CentreretLille"">`, `alt="" class="x"`, `a=\n""v""`, "a=\t\"\"v\"\"", `=""v""`}
	alphabet := []string{"a", "Z", "_", ":", "5", "-", ".", "=", `"`, `""`, " ", "\n", "\t", "<", ">", "æ"}
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 50000; i++ {
		var b strings.Builder
		for n := rng.Intn(14); n > 0; n-- {
			b.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		cases = append(cases, b.String())
	}
	for _, s := range cases {
		if got, want := hasSGMLAttrs(s), sgmlQuotedAttr.MatchString(s); got != want {
			t.Fatalf("hasSGMLAttrs(%q) = %v, the pattern says %v", s, got, want)
		}
		if got, want := unquoteSGML(s), sgmlQuotedAttr.ReplaceAllString(s, `$1="$2"`); got != want {
			t.Fatalf("unquoteSGML(%q) = %q, the pattern writes %q", s, got, want)
		}
	}
}

// TestLooseDivisionPrefilterLosesNothing: the first-letter check in front of the pattern must
// pass every text the pattern accepts, whatever its case or how its runs are split.
func TestLooseDivisionPrefilterLosesNothing(t *testing.T) {
	for _, line := range []string{"Første bog", "ANDEN AFSNIT. Om skat", "  tredje afsnit", "Tolvte bog.", "ſjette afsnit", "Ellevte afsnit Titel"} {
		for _, n := range []*Node{
			{Name: "Linea", Runs: []string{line}},
			{Name: "Linea", Runs: []string{"  ", ""}, Elems: []*Node{{Name: "Char", Runs: []string{line}}}},
		} {
			if reLooseDivision.MatchString(DisplayText(n)) && looseDivision(n) == nil {
				t.Errorf("%q in %d runs: the pattern matches but the prefilter rejected it", line, len(n.Runs))
			}
		}
	}
	if looseDivision(&Node{Name: "Linea", Runs: []string{"§ 1. Loven gælder"}}) != nil {
		t.Error("an ordinary line was read as a division")
	}
}
