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

	Chapter   int
	Afsnit    int    // for "afsnit II", written in roman numerals in the older collections
	Paragraph string // number, possibly with a litra suffix: "267", "8a"
	RangeTo   string // for "§§ 259-266"
	Stk       int
	StkTo     int // for "stk. 1-3"
	Nr        int
	Litra     string
	Bilag     int
	Heading   bool // "overskriften for § 12" / "rubrikaen"

	// Steps is the same reference as the path a resolver walks: every level the sentence
	// names, in the order it names them, with its number exactly as printed. The fields above
	// hold one value per level and cannot hold this: a chapter is an int, so "kap. 1.3.5" —
	// how BR10's annex numbers its own chapters, 126 times in Bilag I alone — has nowhere to
	// go, and "bilag I" is printed in roman while reBilag reads digits. Steps is where M025
	// puts those levels.
	//
	// Nothing reads it yet. The fields are still what decides behaviour, and ParseAddress
	// fills both from the same sentence, so the agreement between them is asserted in the
	// test rather than assumed: the one intended disagreement is a decimal chapter, which the
	// fields cannot carry at all. A range ("stk. 2-5") also still lives in StkTo alone.
	Steps []Step

	// EntryIntoForce names the effectivity series explicitly: "§ 2, ikrafttrædelsesbestemmelse".
	// The series prints its own § 1, § 2, ... and is not the law's numbering, so a plain "§ 2"
	// must never reach it. That refusal is load-bearing for consolidation, and it left the series
	// with no address at all: 25,680 provisions the mirror prints could not be opened by name.
	// Naming the series is what makes them reachable without making them ambiguous.
	EntryIntoForce bool
	Preamble       bool // "indledningen affattes således"
	DocTitle       bool // "lovens titel affattes således"
	Everywhere     bool // "overalt i loven ændres »X« til: »Y«" — no address at all
	Recognised     bool
}

// Step is one level of an address path. Kind is the mirror's own word for the level, Number is
// the level's number exactly as the sentence prints it — "1.3.5", "8.4.2.2", "I", "B.1.1.2",
// "267", "8a" — and both are strings because the corpus puts dots and letters in numbers: an int
// chapter cannot hold "1.3.5" and a digit-only pattern cannot read "bilag I".
type Step struct {
	Kind   string
	Number string
}

// The levels an address can name, in the words a reader and an amendment both use. These are
// deliberately not the publisher's element names: the artifact must not leak the schema, and an
// amendment cites "kapitel 7" whether the target turned out to be an <Exitus> or an <Indentatio>.
const (
	KindBilag    = "bilag"
	KindKapitel  = "kapitel"
	KindAfsnit   = "afsnit"
	KindParagraf = "paragraf"
	KindStykke   = "stykke"
	KindNr       = "nr"
	KindLitra    = "litra"
	KindTabel    = "tabel"
)

// Path is the address as one canonical string, "bilag I/kap. 1.12/stk. 3", derived from Steps and
// from nothing else. String is the citation a reader sees; Path is the identity a machine wants —
// a unit's address, an anchor, the key a consolidated view is cached under — and it is derived in
// one place because a second derivation is how a table of contents starts linking to "#".
func (a Address) Path() string {
	var b strings.Builder
	for _, s := range a.Steps {
		if b.Len() > 0 {
			b.WriteByte('/')
		}
		b.WriteString(s.Kind)
		b.WriteByte(' ')
		b.WriteString(s.Number)
	}
	return b.String()
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
	if a.EntryIntoForce {
		// Printed after the number, because that is the only position ParseAddress accepts: an
		// amendment sentence may mention the series in passing while citing some other §, and a
		// marker that could appear anywhere would then move the edit.
		b.WriteString(", ikrafttrædelsesbestemmelse")
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

// reStep reads an address sentence once, left to right, so the levels come out in the order the
// prose puts them. Each branch is one level; the alternation order is part of the behaviour: "kap.
// 1.12" has to be read by the kapitel branch, which starts further left, rather than as a bare
// decimal. Go picks the leftmost start and then the first branch that matches there.
//
// The number branches accept what the corpus prints: "1.3.5" and "8.5.1.4" for a chapter inside an
// annex, "B.1.1.2" for the lettered series bilag 1 uses, "I" for a roman annex, "8a" for a
// provision with a litra.
var reStep = regexp.MustCompile(`(?i)` +
	`\bbilag(?:et)?\s*([0-9]+|[a-z]+)` + // 1  "bilag 1", "bilag I", "bilag A"
	`|\b(?:kapitel|kapitlet|kap\.)\s*([0-9]+(?:\.[0-9]+)*|[a-z]\.\s*[0-9]+(?:\.[0-9]+)*)` + // 2
	`|([0-9]+(?:\.[0-9]+)+|[a-z]\.\s*[0-9]+(?:\.[0-9]+)*)` + // 3  a bare annex heading number
	`|\bafsnit(?:et)?\s+([0-9]+|[ivxlcdm]+)` + // 4
	`|§+\s*([0-9]+)` + // 5
	`|\bstk\.?\s*([0-9]+)` + // 6
	`|\bnr\.?\s*([0-9]+)` + // 7
	`|\blitra\s*([a-z])` + // 8
	`|\btabel\s*([0-9]+)`) // 9

// parseSteps records the path an address sentence names. It is not a second authority on where an
// edit goes — nothing consumes it until the resolver does — but while both representations exist
// they have to agree, and address_test.go asserts that on every citation in the golden table.
func parseSteps(s string) []Step {
	var steps []Step
	seen := map[string]bool{}
	add := func(kind, number string) {
		// One number per level, the first one printed. The replacement text a change carries
		// repeats the number it is replacing — "kap. 1.12, stk. 3, affattes således: Stk. 3. …" —
		// and reading the second one as a second level is how a path ends up longer than the
		// address. The fields already take the first match of each kind; the path has to agree.
		if seen[kind] {
			return
		}
		seen[kind] = true
		steps = append(steps, Step{kind, strings.ReplaceAll(number, " ", "")})
	}
	inAnnex := false
	for _, m := range reStep.FindAllStringSubmatchIndex(s, -1) {
		branch := -1
		for g := 1; g*2 < len(m); g++ {
			if m[g*2] >= 0 {
				branch = g
				break
			}
		}
		text := func(g int) string { return s[m[g*2]:m[g*2+1]] }
		switch branch {
		case 1:
			add(KindBilag, text(1))
			inAnnex = true
		case 2:
			add(KindKapitel, text(2))
		case 3:
			// A dotted number is how an annex numbers its own chapters, and only an annex does
			// it: outside one, a bare decimal is a figure in prose ("1.3" inside a quotation
			// about kWh/m2). So it names a chapter only once the address has entered a bilag.
			if !inAnnex {
				continue
			}
			add(KindKapitel, text(3))
		case 4:
			add(KindAfsnit, text(4))
		case 5:
			number := text(5)
			// A litra hugs the number ("§ 8a") or sits beside it ("§ 8 a"). The same rule the
			// fields use, so the two representations cannot disagree about which provision is
			// meant: a lone letter counts only when a non-letter follows, which is what keeps
			// "§ 8 og 9" from reading as "§ 8o".
			if lm := reParaLitra.FindStringSubmatch(s[m[11]:]); lm != nil {
				number += lm[1]
			}
			add(KindParagraf, number)
		case 6:
			add(KindStykke, text(6))
		case 7:
			add(KindNr, text(7))
		case 8:
			add(KindLitra, text(8))
		case 9:
			add(KindTabel, text(9))
		}
	}
	return steps
}

// ParseAddress reads a provision reference out of Danish amendment prose.
//
// It is deliberately conservative: it recognises the § / stk / nr / kapitel / bilag forms,
// including ranges ("§§ 259-266", "stk. 1-3") and paragraph litra ("§ 8 a"), and sets
// Recognised=false rather than guessing when it cannot find a number. Unrecognised
// addresses are counted and reported, so a missing pattern shows up as a coverage number
// instead of a wrong consolidation.
//
// Noise it tolerates: "i lovens § 12", "jf. § 12", "bekendtgørelsens § 5".
// blankQuoted replaces the runs between Danish quotation marks with spaces, keeping every other
// character where it is so that positions still mean what they did. Unmatched marks are left
// alone: half a quote is more likely prose than a passage.
func blankQuoted(s string) string {
	out := []rune(s)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		var closer rune
		switch runes[i] {
		case '»':
			closer = '«'
		case '“':
			closer = '”'
		default:
			continue
		}
		end := i
		for j := i + 1; j < len(runes); j++ {
			if runes[j] == closer {
				end = j
				break
			}
		}
		if end == i {
			// An unmatched mark is usually half a quote in prose, which is why this is not
			// unconditional. But when the mark follows a colon the rest of the text *is* the
			// passage being written: "Fodnoten til bekendtgørelses titel affattes således: »1)
			// Bekendtgørelsen gennemfører dele af ... forordning (EU) nr. 623/2012 ...", whose
			// footnote runs to 1,300 characters and never closes. Left unblanked, its "nr. 623"
			// read as this law's item 623 and the change was looked up against a provision that
			// was not the one its own prose named. An unclosed quote after a colon is content,
			// and content is not an address.
			if prevIsColon(runes, i) {
				for j := i + 1; j < len(runes); j++ {
					out[j] = ' '
				}
				return string(out)
			}
			continue
		}
		for j := i + 1; j < end; j++ {
			out[j] = ' '
		}
		i = end
	}
	return string(out)
}

// prevIsColon reports whether the last non-space character before runes[i] is a colon, which is
// the shape that says a quoted passage is being introduced rather than mentioned mid-sentence.
func prevIsColon(runes []rune, i int) bool {
	for j := i - 1; j >= 0; j-- {
		switch runes[j] {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return runes[j] == ':'
	}
	return false
}

func ParseAddress(s string) Address {
	a := Address{Raw: Squeeze(s)}
	// The quoted passage a change reproduces is not an address. "I § 119 a, stk. 1, ændres
	// »nr. 8« til: »nr. 9«" addresses § 119 a, stk. 1 and talks about the words "nr. 8"; if the
	// quote is read as an address, the change is looked up as item 8 — which is the thing being
	// rewritten, not where it is — and the change is refused against a provision that is sitting
	// right there. Blanking quoted runs before scanning is what separates the two.
	s = blankQuoted(a.Raw)

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
	if m := reParagraph.FindStringSubmatchIndex(s); m != nil && reIkraftMarker.MatchString(s[m[3]:]) {
		// Only a marker printed after the § names the series. Amendments say things like
		// "lovens ikrafttrædelsesbestemmelser affattes således" and quote a § in passing, and
		// letting the word anywhere in the sentence redirect the edit would be the exact
		// confusion the refusal in Find exists to prevent. The unitiser prints the marker as a
		// trailing locator, which is the only place it is accepted.
		a.EntryIntoForce = true
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
	a.Steps = parseSteps(s)
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

// reIkraftMarker matches the words that name the effectivity series in an address. Both halves
// of the printed heading are accepted, since the corpus writes "ikrafttrædelsesbestemmelse" and
// "overgangsbestemmelse" for provisions in the same series.
var reIkraftMarker = regexp.MustCompile(`(?i)ikrafttr[æa]delsesbestemmelse|overgangsbestemmelse`)

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
