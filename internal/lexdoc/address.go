package lexdoc

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Address is a reference to a part of a document, in the form Danish legal prose writes it:
// "§ 267, stk. 1, nr. 2", "kapitel 4", "afsnit II", "lovens titel", "overalt i loven".
//
// It is a domain concept, not a markup concept: an amendment cites a provision, and the
// citation has to resolve against whatever representation the target happens to be stored
// in. That is why the parsing lives here rather than beside the reader that first needed it.
type Address struct {
	Raw string

	Chapter    int
	Afsnit     int    // for "afsnit II", written in roman numerals in the older collections
	Paragraph  string // number, possibly with a litra suffix: "267", "8a"
	RangeTo    string // for "§§ 259-266"
	Stk        int
	StkTo      int // for "stk. 1-3"
	Nr         int
	Litra      string
	Bilag      int
	Heading    bool // "overskriften for § 12" / "rubrikaen"
	Preamble   bool // "indledningen affattes således"
	DocTitle   bool // "lovens titel affattes således"
	Everywhere bool // "overalt i loven ændres »X« til: »Y«" — no address at all
	Recognised bool
}

func (a Address) String() string {
	var b strings.Builder
	if a.Chapter != 0 {
		fmt.Fprintf(&b, "kapitel %d", a.Chapter)
	}
	if a.Bilag != 0 {
		fmt.Fprintf(&b, "bilag %d", a.Bilag)
	}
	if a.Afsnit != 0 {
		fmt.Fprintf(&b, "afsnit %s", RomanOf(a.Afsnit))
	}
	if a.Preamble {
		b.WriteString("indledning")
	}
	if a.DocTitle {
		b.WriteString("titel")
	}
	if a.Everywhere {
		b.WriteString("overalt i teksten")
	}
	if a.Paragraph != "" {
		b.WriteString("§ " + a.Paragraph)
		if a.RangeTo != "" {
			b.WriteString("–" + a.RangeTo)
		}
	}
	if a.Stk != 0 {
		fmt.Fprintf(&b, ", stk. %d", a.Stk)
		if a.StkTo != 0 {
			fmt.Fprintf(&b, "–%d", a.StkTo)
		}
	}
	if a.Nr != 0 {
		fmt.Fprintf(&b, ", nr. %d", a.Nr)
	}
	if a.Litra != "" {
		fmt.Fprintf(&b, ", litra %s", a.Litra)
	}
	if a.Heading {
		b.WriteString(" (overskrift)")
	}
	return strings.TrimSpace(b.String())
}

// Address patterns, each named for the Danish phrasing it matches. These were written
// against real amendment text in the archive, and the coverage number the amendment
// inventory reports is what tells us whether the inventory is adequate.
var (
	// reParagraph matches only the digits. A paragraph litra is resolved afterwards, in
	// code: Go's regexp is RE2 and has no lookahead, so "§ 8 a" cannot be distinguished
	// from "§ 8 og" inside the pattern — a lookahead there would also panic at init.
	reParagraph = regexp.MustCompile(`(?i)§+\s*([0-9]+)(?:\s*-\s*([0-9]+))?`)
	// reParaLitra accepts a litra only when the single letter is followed by a non-letter
	// or the end of the text, so "§ 8 og 9" does not read as "§ 8o".
	reParaLitra = regexp.MustCompile(`^\s*([a-z])(?:[^a-z]|$)`)
	reKapitel   = regexp.MustCompile(`(?i)\bkapitlet\s*([0-9]+)|\bkapitel\s*([0-9]+)`)
	reBilag     = regexp.MustCompile(`(?i)\bbilag(?:et)?\s*([0-9]+)`)
	reStk       = regexp.MustCompile(`(?i)\bstk\.?\s*([0-9]+)\s*(?:-\s*([0-9]+))?`)
	reNr        = regexp.MustCompile(`(?i)\bnr\.?\s*([0-9]+)`)
	reLitra     = regexp.MustCompile(`(?i)\blitra\s*([a-z])`)
	reHeading   = regexp.MustCompile(`(?i)\b(overskrift|rubrik|rubrika|hovedoverskrift)`)
	// reAfsnit accepts both spellings of the number: "afsnit 2" and "afsnit XXIV".
	reAfsnit = regexp.MustCompile(`(?i)\bafsnit(?:et)?\s+([0-9]+|[ivxlcdm]+)\b`)
	// reEverywhere is a replace that spans the whole act. It deliberately has no provision
	// in it, and the § that often appears inside the quoted passage must not be read as
	// its address.
	reEverywhere = regexp.MustCompile(`(?i)\boveralt i\s+(?:hele\s+)?(?:loven|bekendtgørelsen|cirkulæret|denne)\b`)
	// rePreamble and reDocTitle name the two parts of a document that are not provisions:
	// the preamble and the document's own title.
	rePreamble = regexp.MustCompile(`(?i)\bindledning(?:en|s)?\b`)
	reDocTitle = regexp.MustCompile(`(?i)\btitel(?:en)?\b`)
	// reChangeVerb is the verbs that mean "this text is being rewritten", and its boundary
	// is written by hand: Go's \b is ASCII-only, so `\bændres` can never match — "æ" is not
	// an ASCII word character — and the whole document-title branch behind it sat dead
	// while the corpus kept feeding it "I lovens titel ændres »X« til: »Y«". [^\p{L}] is
	// Unicode-aware, which is what a Danish corpus needs, and it still refuses to read
	// "forændres" as the verb.
	reChangeVerb = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(affattes|ændres|ophæves|indsættes|tilføjes|udgår|erstattes)(?:$|[^\p{L}])`)
)

// ParseAddress reads a provision reference out of Danish amendment prose.
//
// It is deliberately conservative: it recognises the § / stk / nr / kapitel / bilag forms,
// including ranges ("§§ 259-266", "stk. 1-3") and paragraph litra ("§ 8 a"), and sets
// Recognised=false rather than guessing when it cannot find a number. Unrecognised
// addresses are counted and reported, so a missing pattern shows up as a coverage number
// instead of a wrong consolidation.
//
// Noise it tolerates: "i lovens § 12", "jf. § 12", "bekendtgørelsens § 5".
func ParseAddress(s string) Address {
	a := Address{Raw: Squeeze(s)}
	s = a.Raw

	if m := reHeading.FindString(s); m != "" {
		a.Heading = true
	}
	// A replace that spans the whole act has no address, and the § that so often sits
	// inside its quoted passage is content rather than a target: reading it as one would
	// splice the change into a single provision and leave every other occurrence of the old
	// wording untouched. Returning here is what keeps that from happening.
	if reEverywhere.MatchString(s) {
		a.Everywhere = true
		a.Recognised = true
		return a
	}
	if m := reAfsnit.FindStringSubmatch(s); m != nil {
		if n := Atoi(m[1]); n > 0 {
			a.Afsnit = n
		} else if r := RomanToNumber(m[1]); r != "" {
			a.Afsnit = Atoi(r)
		}
		if a.Afsnit != 0 {
			a.Recognised = true
		}
	}
	if rePreamble.MatchString(s) {
		a.Preamble = true
		a.Recognised = true
	}
	// "Fodnoten til lovens titel affattes således" is about a footnote, not about the
	// title. Rewriting the title in its place would be a confident corruption of the wrong
	// text, so the title is only claimed when no footnote is mentioned.
	if reDocTitle.MatchString(s) && reChangeVerb.MatchString(s) &&
		!strings.Contains(strings.ToLower(s), "fodnote") {
		a.DocTitle = true
		a.Recognised = true
	}
	if m := reBilag.FindStringSubmatch(s); len(m) > 0 {
		a.Bilag = Atoi(m[1])
		a.Recognised = true
	}
	if m := reKapitel.FindStringSubmatch(s); len(m) > 0 {
		a.Chapter = Atoi(firstNonEmpty(m[1], m[2]))
		a.Recognised = true
	}
	if m := reParagraph.FindStringSubmatchIndex(s); m != nil {
		a.Paragraph = s[m[2]:m[3]]
		if m[4] >= 0 {
			a.RangeTo = s[m[4]:m[5]]
		}
		// A litra hugs the number ("§ 8a") or sits beside it ("§ 8 a"), and both spellings
		// occur in the corpus. Resolving it here rather than in the pattern is what keeps
		// "§ 8 og 9" from reading as "§ 8o".
		if lm := reParaLitra.FindStringSubmatch(s[m[3]:]); lm != nil {
			a.Paragraph += lm[1]
		}
		a.Recognised = true
	}
	if m := reStk.FindStringSubmatch(s); len(m) > 0 {
		a.Stk = Atoi(m[1])
		a.StkTo = Atoi(m[2])
	}
	if m := reNr.FindStringSubmatch(s); len(m) > 0 {
		a.Nr = Atoi(m[1])
	}
	if m := reLitra.FindStringSubmatch(s); len(m) > 0 {
		a.Litra = m[1]
	}
	return a
}

// ItemMarker reads the marker a numbered sub-item prints: "1)" gives a number, "a)" gives
// a litra.
func ItemMarker(s string) (num int, litra string) {
	m := reItemMarker.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, ""
	}
	if r, _ := utf8.DecodeRuneInString(m[1]); unicode.IsLetter(r) {
		return 0, strings.ToLower(m[1])
	}
	return Atoi(m[1]), ""
}

// reItemMarker matches the marker a numbered item prints: "1)", "2.", "a)", "b.".
var reItemMarker = regexp.MustCompile(`^([0-9]+|[a-zæøåA-ZÆØÅ])[\).]`)

// ParaLitra extracts the letter suffix of a printed § number: "§ 2 a." yields "a".
//
// The corpus writes the provision "§ 2 a." and amendments cite it as "§ 2a". Find already
// accepts a number plus a Litra, so recording the suffix is what makes the two spellings
// resolve; without it every "§ 2a" address reports the provision as missing from a law that
// plainly contains it.
func ParaLitra(raw string) string {
	i := digitRun.FindStringIndex(raw)
	if i == nil {
		return ""
	}
	if m := reParaLitra.FindStringSubmatch(raw[i[1]:]); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// PrintedRoman returns the arabic form of a roman number printed beside a heading, or "".
// The older collections and the large acts write "Afsnit I", "Afsnit XXIV".
func PrintedRoman(raw string) string {
	if m := reAfsnitRoman.FindStringSubmatch(raw); m != nil {
		return RomanToNumber(m[1])
	}
	return ""
}

// reAfsnitRoman matches the printed number of an afsnit, which the older collections and
// the large acts write in roman numerals: "Afsnit I", "Afsnit XXIV".
var reAfsnitRoman = regexp.MustCompile(`(?i)\bafsnit\s+([ivxlcdm]+)\b`)

// romanValues are the symbols a Danish legal number may use. "afsnit" numbers run into the
// low hundreds at most, so this is deliberately small.
var romanValues = map[byte]int{'i': 1, 'v': 5, 'x': 10, 'l': 50, 'c': 100, 'd': 500, 'm': 1000}

// RomanToNumber converts a roman numeral, rejecting anything not written the canonical way.
//
// The rejection matters more than the arithmetic: "vid", "id" and other Danish letter runs
// are made of roman symbols, and a pattern that simply added symbol values would happily
// number a section from an ordinary word. Round-tripping the value back through the encoder
// rejects "IIII", "ic" and every word that is not a numeral.
func RomanToNumber(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	value := 0
	for i := 0; i < len(s); {
		cur, ok := romanValues[s[i]]
		if !ok {
			return ""
		}
		// Subtractive notation: a smaller symbol in front of a larger one counts against it
		// ("iv" is 5-1, not 1 and 5 side by side). The symbols are single characters, so
		// the pair has to be read as a comparison rather than looked up as a two-letter
		// word — looking it up silently returned 0 and rejected every "IV" and "IX" in the
		// archive.
		if i+1 < len(s) {
			if nxt, okNext := romanValues[s[i+1]]; okNext && nxt > cur {
				value += nxt - cur
				i += 2
				continue
			}
		}
		value += cur
		i++
	}
	if value == 0 || RomanOf(value) != s {
		return ""
	}
	return strconv.Itoa(value)
}

// RomanOf encodes a positive integer as a canonical roman numeral.
func RomanOf(v int) string {
	if v <= 0 || v > 3999 {
		return ""
	}
	table := []struct {
		n     int
		roman string
	}{
		{1000, "m"}, {900, "cm"}, {500, "d"}, {400, "cd"}, {100, "c"}, {90, "xc"},
		{50, "l"}, {40, "xl"}, {10, "x"}, {9, "ix"}, {5, "v"}, {4, "iv"}, {1, "i"},
	}
	var b strings.Builder
	for _, t := range table {
		for v >= t.n {
			b.WriteString(t.roman)
			v -= t.n
		}
	}
	return b.String()
}

var digitRun = regexp.MustCompile(`[0-9]+`)

// FirstNumber extracts the first integer from text, e.g. "§ 267." -> "267". Empty when
// there is none, which is how callers distinguish "no number" from "number zero".
func FirstNumber(s string) string {
	if m := digitRun.FindString(s); m != "" {
		return m
	}
	return ""
}

// Atoi parses a leading integer, returning 0 for anything that is not one.
func Atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
