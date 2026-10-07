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

	Chapter int
	// ChapterLitra is the letter of an inserted chapter: "kapitel 3 e" is not kapitel 3, and
	// reading it as one aimed "Kapitel 3 e ophæves" at the wrong chapter.
	ChapterLitra string
	Afsnit       int    // for "afsnit II", written in roman numerals in the older collections
	AfsnitTo     int    // "tredje og fjerde afsnit": the last of a run
	Book         int    // "fjerde bog": retsplejelovens books, which hold its afsnit
	Paragraph    string // number, possibly with a litra suffix: "267", "8a"
	RangeTo      string // for "§§ 259-266"
	Stk          int
	StkTo        int // for "stk. 1-3"
	Nr           int
	NrSuffix     string // "nr. 5 a": an item inserted after nr. 5
	Litra        string
	Bilag        int
	BilagLitra   string // "bilag 2 a": an annex inserted after bilag 2
	Heading      bool   // "overskriften for § 12" / "rubrikaen"

	// Sentence is the punktum a change names inside its unit: "§ 7, stk. 8, 3. pkt.". SentenceTo
	// closes a run: "1. og 2. pkt.", "2.-4. pkt.". Zero when the address names a whole unit.
	Sentence, SentenceTo int
	// SentenceList says Sentence and SentenceTo are two sentences ("1. og 4. pkt."), not a range.
	SentenceList bool
	// SentenceBecomes says Sentence is the number in the text as the act has left it ("der bliver
	// 2. pkt."), not the number from before the act.
	SentenceBecomes bool

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
	// Footnote is "fodnoten til lovens titel": the note printed under the title, which is where
	// Danish law names the EU directives it implements. "Fodnoten" on its own means the same note.
	Footnote   bool
	Everywhere bool // "overalt i loven ændres »X« til: »Y«" — no address at all
	// Continued says Bilag was named by the change before, not by this one: "1. Bilag 1, punkt
	// 2.1 affattes således: …", "2. Som nyt punkt 2.6 indsættes: …". The change's own words say
	// where in the annex, and nothing is placed at its end for want of them.
	Continued  bool
	Recognised bool
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
		if a.ChapterLitra != "" {
			b.WriteString(" " + a.ChapterLitra)
		}
	}
	if a.Bilag != 0 {
		fmt.Fprintf(&b, "bilag %d", a.Bilag)
		if a.BilagLitra != "" {
			b.WriteString(" " + a.BilagLitra)
		}
	}
	if a.Book != 0 {
		fmt.Fprintf(&b, "bog %d, ", a.Book)
	}
	if a.Afsnit != 0 {
		fmt.Fprintf(&b, "afsnit %s", RomanOf(a.Afsnit))
	}
	if a.Preamble {
		b.WriteString("indledning")
	}
	if a.Footnote {
		b.WriteString("fodnoten til ")
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
		if a.NrSuffix != "" {
			b.WriteString(" " + a.NrSuffix)
		}
	}
	if a.Litra != "" {
		fmt.Fprintf(&b, ", litra %s", a.Litra)
	}
	if a.Sentence != 0 {
		fmt.Fprintf(&b, ", %d.", a.Sentence)
		if a.SentenceTo != 0 {
			fmt.Fprintf(&b, "-%d.", a.SentenceTo)
		}
		b.WriteString(" pkt.")
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
	// "§ 30 mm" is the provision put between § 30 m and § 30 n: two letters, the same one twice.
	reParaLitra = regexp.MustCompile(`^[\s\x{a0}]*([a-zæøåA-ZÆØÅ]{1,2})(?:[^\p{L}]|$)`)
	// reInLaw is "§ 5 i lov om …": the i is a preposition, not § 5 i.
	reInLaw    = regexp.MustCompile(`(?i)^[\s\x{a0}]*i[\s\x{a0}]+(?:lov|bekendtgørelse|forordning|anordning|cirkulære|regulativ|reglement|vedtægt|kapitel|bilag|afsnit|denne|dette|den\b|det\b|de\b|samme|nævnte|stk|nr|litra|tekst|overskrift|indledning)`)
	reKapitel  = regexp.MustCompile(`(?i)\bkapitlet\s*([0-9]+)|\bkapitel\s*([0-9]+)|\b([0-9]+)\.\s*kapitel\b`)
	reBilag    = regexp.MustCompile(`(?i)\bbilag(?:et)?\s*([0-9]+)`)
	reTheAnnex = regexp.MustCompile(`(?i)\bbilag(?:et|ets)\b`)
	// reStkComma is "stk, 1": a comma typed for the full stop, which the register prints.
	reStkComma = regexp.MustCompile(`(?i)\bstk\.?,\s*([0-9])`)
	// reMissingSign is "I 9 h, stk. 1": the § left out in front of a provision number that a
	// subsection follows.
	reMissingSign = regexp.MustCompile(`(?i)^(\s*i\s+)([0-9]+(?:\s*[a-zæøå])?\s*,\s*stk\.)`)
	// reGluedStk is "§ 4stk. 1": the comma and space before the subsection left out.
	reGluedStk = regexp.MustCompile(`(?i)([0-9])(stk\.)`)
	// rePktSpace is "2. pkt . ,": a space typed before the abbreviation's stop.
	rePktSpace = regexp.MustCompile(`(?i)\bpkt\s+\.`)
	// rePktComma is "stk. 5, 1, pkt.": a comma typed for the full stop after a sentence number.
	rePktComma = regexp.MustCompile(`(?i)(\b(?:stk|nr)\.\s*[0-9]+\s*,\s*[0-9]+),\s*(pkt\.|punktum)`)
	reStk      = regexp.MustCompile(`(?i)\bstk\.?\s*([0-9]+)\s*(?:-\s*([0-9]+))?`)
	reNr       = regexp.MustCompile(`(?i)\bnr\.?\s*([0-9]+)`)
	reLitra    = regexp.MustCompile(`(?i)\blitra\s*([a-z])`)
	// rePunktum reads "3. pkt.", "1. og 2. pkt.", "2.-4. pkt." and the spelled-out "punktum".
	reSentenceBecomes = regexp.MustCompile(`(?i)der\s+bliver\s+([0-9]+)\.\s*(?:pkt|punktum)`)
	rePunktum         = regexp.MustCompile(`(?i)(?:^|[^0-9])([0-9]+)\.\s*(?:(?:,\s*[0-9]+\.\s*)*(?:og|-|–)\s*([0-9]+)\.\s*)?(?:pkt|punktum)`)
	reHeading         = regexp.MustCompile(`(?i)\b(overskrift|rubrik|rubrika|hovedoverskrift)|\btit(?:el|len)\s+(?:til|for)\s+(?:kapitel|afsnit|bilag)`)
	// reAfsnit accepts both spellings of the number: "afsnit 2" and "afsnit XXIV".
	reAfsnit = regexp.MustCompile(`(?i)\bafsnit(?:et)?\s+([0-9]+|[ivxlcdm]+)\b`)
	// reEverywhere is a replace that spans the whole act. It deliberately has no provision
	// in it, and the § that often appears inside the quoted passage must not be read as
	// its address.
	reEverywhere = regexp.MustCompile(`(?i)\boveralt\s+i\s+(?:hele\s+)?(?:loven|lovbekendtgørelsen|bekendtgørelsen|cirkulæret|forordningen|anordningen|regulativet|reglementet|vedtægten|vedtægterne|reglerne|denne|dette)\b`)
	// rePreamble and reDocTitle name the two parts of a document that are not provisions:
	// the preamble and the document's own title.
	rePreamble = regexp.MustCompile(`(?i)\bindledning(?:en|s)?\b`)
	reDocTitle = regexp.MustCompile(`(?i)\b(?:titel(?:en)?|titlen)\b`)
	reFootnote = regexp.MustCompile(`(?i)\bfodnote(?:n)?\b|\btitelnote(?:n)?\b|^\s*(?:i\s+)?noten\b|^\s*note\s+1\b`)
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
			number := text(2)
			// The same litra rule as a § and as the Chapter field: "kapitel 3 e" is its own
			// chapter.
			number += litraAt(s[m[5]:])
			add(KindKapitel, number)
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
			number += litraAt(s[m[11]:])
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

// TheAnnex is Address.Bilag for "bilaget": the document's only annex, whatever its number.
const TheAnnex = -1

func ParseAddress(s string) Address {
	s = fixTypos(s)
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
	if m := reBookAfsnit.FindStringSubmatch(s); m != nil {
		// "Fjerde bog, tredje og fjerde afsnit": a book, and the afsnit of it, numbered in words.
		a.Book = DanishOrdinals[strings.ToLower(m[1])]
		if m[2] != "" {
			a.Afsnit = DanishOrdinals[strings.ToLower(m[2])]
		}
		if m[3] != "" {
			a.AfsnitTo = DanishOrdinals[strings.ToLower(m[3])]
		}
		a.Recognised = a.Book != 0
	} else if m := reAfsnit.FindStringSubmatch(s); m != nil {
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
	if reDocTitle.MatchString(s) && reChangeVerb.MatchString(s) && !a.Heading &&
		!strings.Contains(strings.ToLower(s), "fodnote") {
		a.DocTitle = true
		a.Recognised = true
	}
	if reFootnote.MatchString(s) && !reParagraph.MatchString(s) && !reBilag.MatchString(s) {
		// The footnote, not the title it hangs from: rewriting the title in its place would be a
		// confident corruption of the wrong text.
		a.Footnote, a.DocTitle, a.Recognised = true, true, true
	}
	if m := reBilag.FindStringSubmatch(s); len(m) > 0 {
		a.Bilag = Atoi(m[1])
		a.Recognised = true
	} else if reTheAnnex.MatchString(s) && !reParagraph.MatchString(s) {
		// "I bilaget ændres …": the definite form names the document's one annex.
		a.Bilag = TheAnnex
		a.Recognised = true
	}
	if m := reKapitel.FindStringSubmatchIndex(s); m != nil {
		g := 2 // "kapitlet 3", "kapitel 3" or the older "7. kapitel": whichever group matched
		for g < len(m) && m[g] < 0 {
			g += 2
		}
		a.Chapter = Atoi(s[m[g]:m[g+1]])
		if g < 6 {
			a.ChapterLitra = litraAt(s[m[g+1]:])
		}
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
		a.Paragraph += litraAt(s[m[3]:])
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
	if m := reNr.FindStringSubmatchIndex(s); m != nil {
		a.Nr = Atoi(s[m[2]:m[3]])
		a.NrSuffix = litraAt(s[m[3]:])
	}
	// A sentence named after a quoted passage belongs to a later operation of the same change: in
	// "I § 17, stk. 5, ændres »A« til: »B«, og som 2. pkt. indsættes:" the replacement is aimed at
	// the subsection, and reading it as aimed at a second sentence that does not exist yet refused it.
	sentenceScope := s
	if q := strings.IndexAny(s, "»“"); q > 0 && reAddressEnd.MatchString(s[:q]) {
		sentenceScope = s[:q]
	}
	if m := rePunktum.FindStringSubmatch(sentenceScope); m != nil {
		a.Sentence, a.SentenceTo = Atoi(m[1]), Atoi(m[2])
		a.SentenceList = a.SentenceTo != 0 && strings.Contains(strings.ToLower(m[0]), "og") && a.SentenceTo != a.Sentence+1
		if ns := reSentenceNumbers.FindAllString(m[0], -1); len(ns) > 2 {
			// "2., 3. og 4. pkt.": the sentences from the first to the last, when they follow on.
			a.SentenceList = false
			for i := 1; i < len(ns); i++ {
				if Atoi(ns[i]) != Atoi(ns[i-1])+1 {
					a.SentenceList = true
				}
			}
		}
		// "1. pkt., der bliver 2. pkt.": the act has already put a sentence in front, and the
		// sentence is the second one in the text the change meets.
		if b := reSentenceBecomes.FindStringSubmatch(sentenceScope); b != nil {
			a.Sentence, a.SentenceTo, a.SentenceBecomes = Atoi(b[1]), 0, true
		}
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
	num, litra, _ = ItemMarkerSuffix(s)
	return num, litra
}

// ItemMarkerSuffix also reads a numbered item's letter: "5 a)" is item 5 a, inserted between 5
// and 6. Read as "not a marker", it was numbered by position, and every item after it answered to
// the number of the one before: "nr. 6" resolved to "5 a)".
func ItemMarkerSuffix(s string) (num int, litra, suffix string) {
	t := strings.TrimSpace(s)
	if m := reItemSuffixMarker.FindStringSubmatch(t); m != nil {
		return Atoi(m[1]), "", strings.ToLower(m[2])
	}
	m := reItemMarker.FindStringSubmatch(t)
	if m == nil {
		return 0, "", ""
	}
	if r, _ := utf8.DecodeRuneInString(m[1]); unicode.IsLetter(r) {
		return 0, strings.ToLower(m[1]), ""
	}
	return Atoi(m[1]), "", ""
}

// reItemSuffixMarker is a numbered item with a letter: "5 a)", "5a)", "12 b.".
var reItemSuffixMarker = regexp.MustCompile(`^([0-9]+)[\s\x{a0}]*([a-zæøå])[\).]`)

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
	return litraAt(Squeeze(raw[i[1]:]))
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
// NumberWithLitra reads a unit number together with a letter glued or spaced onto it: "7a" and
// "7 a" both give "7a". Chapters are numbered this way when inserted between two others, and
// keeping only the digits gave chapter 7 and chapter 7 a the same number.
func NumberWithLitra(s string) string {
	loc := digitRun.FindStringIndex(s)
	if loc == nil {
		return ""
	}
	n := s[loc[0]:loc[1]]
	n += litraAt(Squeeze(s[loc[1]:]))
	return n
}

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

// reAddressToken reads the parts of an address list in order: a §, a subsection, an item, a
// litra, a sentence, or a bare number continuing the level before it ("stk. 2 og 3").
var reAddressToken = regexp.MustCompile(`(?i)(§+)\s*([0-9]+(?:\s*[a-zæøå](?:[^\p{L}]|$))?)|\b(stk|nr|litra)\.?\s*([0-9]+|[a-zæøå](?:[^\p{L}]|$))|([0-9]+)\.\s*(?:og\s+[0-9]+\.\s*)?(?:pkt|punktum)|(?:,|\bog\b|\bsamt\b)\s*([0-9]+)(?:[^0-9.)]|$)`)

// reBecomesClause is the new number a change gives the unit it names: ", der bliver stk. 2",
// ", som bliver 6. pkt.", ", der bliver til stk. 3, nr. 1".
var reBecomesClause = regexp.MustCompile(`(?i),?\s*(?:der|som)\s+bliver\s+(?:til\s+)?(?:(?:stk\.?|nr\.?)\s*[0-9]+(?:\s*[a-zæøå](?:[^\p{L}]|$))?(?:\s*(?:,|og|-|–)\s*[0-9]+(?:[^0-9.\p{L}]|$))*|[0-9]+\.\s*(?:og\s+[0-9]+\.\s*)?pkt\.?)(?:\s*,\s*(?:nr\.?\s*[0-9]+|[0-9]+\.\s*(?:og\s+[0-9]+\.\s*)?pkt\.?))*`)

// fixTypos repairs the slips of punctuation change sentences print in their addresses, which a
// reader of the act passes over without noticing.
func fixTypos(s string) string {
	s = strings.NewReplacer("\u200b", "", "\u200c", "", "\u200d", "", "\ufeff", "", "§ §", "§§").Replace(s)
	s = reStkComma.ReplaceAllString(s, "stk. $1")
	s = rePktSpace.ReplaceAllString(s, "pkt.")
	s = reGluedStk.ReplaceAllString(s, "$1, $2")
	s = reMissingSign.ReplaceAllString(s, "$1§ $2")
	s = rePktNoStop.ReplaceAllString(s, "$1. og $2. pkt")
	s = rePktListComma.ReplaceAllString(s, "$1$2.$3")
	s = reCommaLetter.ReplaceAllString(s, "§ $1 $2$3")
	s = reSplitBilag.ReplaceAllString(s, "${1}ilag")
	s = rePktNoOrdinal.ReplaceAllString(s, "som $1. $2")
	return rePktComma.ReplaceAllString(s, "$1. $2")
}

// reSentenceNumbers are the numbers a sentence list prints.
var reSentenceNumbers = regexp.MustCompile(`[0-9]+`)

// reTrailingPkt is the abbreviation ending a sentence reference.
var reTrailingPkt = regexp.MustCompile(`(?i)\s*(?:pkt|punktum)\.?\s*$`)

// reLitraList is a list of letters after one "litra": "litra a og b", "litra a, b og c".
var reLitraList = regexp.MustCompile(`(?i)\blitra\s+([a-zæøå])(\s*(?:,|og)\s*)([a-zæøå])\b`)

// rePktListComma is "stk. 5, 2, og 5. pkt.": a comma typed for the stop after the first sentence of
// a list, which read "2" as stk. 2.
var rePktListComma = regexp.MustCompile(`(?i)(\bstk\.\s*[0-9]+\s*,\s*)([0-9]+),(\s*og\s+[0-9]+\.\s*pkt)`)

// rePktNoOrdinal is "indsættes som 4 pkt.": the stop after the sentence number left out.
var rePktNoOrdinal = regexp.MustCompile(`(?i)\bsom\s+([0-9]+)\s+(pkt|punktum)\b`)

// reSplitBilag is "I b ilag 1": the word broken by a space.
var reSplitBilag = regexp.MustCompile(`(?i)\b(b)\s+ilag\b`)

// reCommaLetter is "§ 25, A, stk. 2": a comma typed between a provision's number and its capital
// letter, which read the letter as nothing and the change as one for § 25.
var reCommaLetter = regexp.MustCompile(`§\s*([0-9]+)\s*,\s*([A-ZÆØÅ])(\s*,)`)

// rePktNoStop is "2. og 3 pkt.": the stop after the second sentence number left out, which read
// "3" as a subsection continuing the list.
var rePktNoStop = regexp.MustCompile(`(?i)\b([0-9]+)\.\s*og\s+([0-9]+)\s+pkt`)

// reBecomesTail is the second half of a becomes-clause that renames two units at once: "stk. 6
// og stk. 7, der bliver stk. 3 og stk. 4".
var reBecomesTail = regexp.MustCompile(`(?i)^\s*,?\s*og\s+(?:stk\.?|nr\.?)\s*[0-9]+(?:\s*,\s*[0-9]+\.\s*pkt\.?)?`)

// dropBecomes removes the becomes-clauses from an address list. A clause after two units joined
// by "og" renames both, so its own "og stk. 4" is the second new number; after one unit, an "og"
// that follows names the next place.
func dropBecomes(s string) string {
	var b strings.Builder
	prev := 0
	for _, m := range reBecomesClause.FindAllStringIndex(s, -1) {
		if m[0] < prev {
			continue
		}
		b.WriteString(s[prev:m[0]])
		end := m[1]
		before := s[prev:m[0]]
		if k := strings.LastIndex(before, "§"); k >= 0 {
			before = before[k:]
		}
		if strings.Contains(before, " og ") {
			if t := reBecomesTail.FindStringIndex(s[end:]); t != nil {
				end += t[1]
			}
		}
		prev = end
	}
	b.WriteString(s[prev:])
	return b.String()
}

// reCountAfter is a count of places following a number: "14 steder", "1 sted".
var reCountAfter = regexp.MustCompile(`(?i)^\W*\s*sted`)

// reAddressEnd is where the address part of a change sentence ends: its verb, or the first
// quotation.
var reAddressEnd = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(affattes|ændres|ophæves|indsættes|tilføjes|udgår|erstattes|bliver)(?:$|[^\p{L}])|»`)

// ParseAddresses reads every place a change sentence names, in order: "I § 132, § 133, stk. 1,
// § 136, stk. 3, og stk. 4, 1. pkt., … udgår »X«" names six. Each later part inherits the levels
// above it from the part before ("og stk. 4" is still § 136), and a bare number continues the
// deepest level written ("stk. 2 og 3"). A sentence naming one place returns that one address;
// ranges stay ranges, because ParseAddress already reads them.
func ParseAddresses(definition string) []Address {
	if units := unitList(definition); len(units) > 0 {
		return units
	}
	if m := reAnnexPair.FindStringSubmatch(Squeeze(definition)); m != nil {
		// "I bilag 4 og bilag 5 indsættes efter nr. 3.3. …": the same change in both annexes.
		return []Address{{Recognised: true, Bilag: Atoi(m[1])}, {Recognised: true, Bilag: Atoi(m[2])}}
	}
	s := blankQuoted(Squeeze(fixTypos(definition)))
	for {
		// "nr. 2, litra a og b" names both letters: each is written out as a letter of its own.
		t := reLitraList.ReplaceAllString(s, "litra $1${2}litra $3")
		if t == s {
			break
		}
		s = t
	}
	// "stk. 3, der bliver stk. 2, stk. 5, nr. 1, der bliver stk. 4, nr. 1": the act's numbers are
	// the ones before it, which is what the engine resolves, so the new numbers are read past
	// rather than taken as the end of the address.
	s = dropBecomes(s)
	if m := reAddressEnd.FindStringIndex(s); m != nil {
		s = s[:m[0]]
	}
	if strings.Contains(s, "-") || strings.Contains(s, "–") {
		return nil // a range is one address of its own
	}
	type part struct{ para, stk, nr, litra, pkt string }
	var parts []part
	var cur part
	deepest := ""
	have := false
	emit := func() {
		if have {
			parts = append(parts, cur)
		}
	}
	for _, m := range reAddressToken.FindAllStringSubmatch(s, -1) {
		switch {
		case m[1] != "":
			emit()
			cur, have, deepest = part{para: strings.ReplaceAll(strings.TrimRight(m[2], ",.;: "), " ", "")}, true, "para"
		case m[3] != "":
			level := strings.ToLower(m[3])
			n := strings.TrimRight(m[4], ",.;: ")
			if !have {
				return nil // a subsection with no § in front of it is the running subject's business
			}
			next := cur
			switch level {
			case "stk":
				if cur.stk != "" {
					emit()
				}
				next = part{para: cur.para, stk: n}
			case "nr":
				if cur.nr != "" {
					emit()
				}
				next = part{para: cur.para, stk: cur.stk, nr: n}
			case "litra":
				if cur.litra != "" {
					emit()
				}
				next = part{para: cur.para, stk: cur.stk, nr: cur.nr, litra: n}
			}
			cur, deepest = next, level
		case m[5] != "":
			if !have {
				return nil
			}
			if cur.pkt != "" {
				emit()
				cur = part{para: cur.para, stk: cur.stk, nr: cur.nr, litra: cur.litra}
			}
			// "2. og 3. pkt." names both sentences.
			cur.pkt = strings.TrimSuffix(strings.TrimSpace(reTrailingPkt.ReplaceAllString(m[0], "")), ".")
		case m[6] != "":
			if !have || reCountAfter.MatchString(s[strings.Index(s, m[0])+len(m[0])-1:]) {
				continue // "og 14 steder i bilag 1" counts places, it is not stk. 14
			}
			emit()
			if at := strings.Index(s, m[0]); deepest != "para" && strings.Contains(s, "§§") && reCommaOg.MatchString(s[:at+len(m[0])-len(m[6])]) {
				// "§§ 121, 513, nr. 6, og 514": in a list of provisions, ", og" closes the
				// conditions of § 513 and names the next provision.
				cur, deepest = part{para: m[6] + litraAt(s[at+len(m[0])-1:])}, "para"
				continue
			}
			if l := litraAt(s[strings.Index(s, m[0])+len(m[0])-1:]); l != "" && deepest != "nr" {
				// "§ 15 a, stk. 1, og 15 c, stk. 1": a subsection has no letter, so a bare
				// number with one is the next §, printed without its sign.
				cur, deepest = part{para: m[6] + l}, "para"
				continue
			}
			next := part{para: cur.para}
			switch deepest {
			case "para":
				next = part{para: m[6]}
			case "stk":
				next.stk = m[6]
			case "nr":
				next.stk, next.nr = cur.stk, m[6]
			default:
				continue
			}
			cur = next
		}
	}
	emit()
	if len(parts) < 2 {
		return nil
	}
	out := make([]Address, 0, len(parts))
	for _, p := range parts {
		text := "§ " + p.para
		if p.stk != "" {
			text += ", stk. " + p.stk
		}
		if p.nr != "" {
			text += ", nr. " + p.nr
		}
		if p.litra != "" {
			text += ", litra " + p.litra
		}
		if p.pkt != "" {
			text += ", " + p.pkt + ". pkt."
		}
		out = append(out, ParseAddress(text))
	}
	return out
}

// reCommaOg is a list going back up a level: "…, nr. 6, og".
var reCommaOg = regexp.MustCompile(`,\s*og\s*$`)

// litraAt reads the letter printed right after a number: "§ 8 a", "§ 34 A", "§\u00a029\u00a0a.".
// The letter has to stand alone, which keeps "§ 8 og 9" from reading as "§ 8o", and "i" followed
// by "lov om …" is the preposition: "§ 5 i lov om noget" is § 5 of that law, not § 5 i.
func litraAt(rest string) string {
	m := reParaLitra.FindStringSubmatch(rest)
	if m == nil || reInLaw.MatchString(rest) {
		return ""
	}
	if r := []rune(strings.ToLower(m[1])); len(r) == 2 && r[0] != r[1] {
		return "" // "§ 8 og 9": a word, not a letter
	}
	return strings.ToLower(m[1])
}

// reUnitList is a repeal of several whole annexes or chapters: "Bilag 4, 5 og 7 ophæves",
// "Bilag 13 og bilag 14 ophæves", "Kapitel 2 B og 2 C ophæves".
var (
	reUnitList  = regexp.MustCompile(`(?i)^\s*(bilag(?:ene)?|kapit(?:el|lerne))\s+([0-9][0-9a-zæøå ,]*?)\s*,?\s*(?:ophæves|udgår)(?:[^\p{L}]|$)`)
	reAnnexPair = regexp.MustCompile(`(?i)^\s*I\s+bilag\s+([0-9]+)\s+og\s+bilag\s+([0-9]+)\s*,?\s+(?:indsættes|ændres)\b`)
	reUnitSplit = regexp.MustCompile(`(?i)\s*(?:,|\bog\b)\s*(?:bilag\s+|kapitel\s+)?`)
	reUnitOne   = regexp.MustCompile(`(?i)^([0-9]+)(?:\s*([a-zæøå]))?$`)
)

// unitList reads a repeal of several whole annexes or chapters as one address for each. It reads
// nothing unless every listed unit is a plain number, with a letter at most.
func unitList(definition string) []Address {
	m := reUnitList.FindStringSubmatch(Squeeze(definition))
	if m == nil {
		return nil
	}
	pieces := reUnitSplit.Split(" "+m[2], -1)
	var out []Address
	for _, p := range pieces {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		u := reUnitOne.FindStringSubmatch(p)
		if u == nil {
			return nil
		}
		a := Address{Recognised: true}
		if strings.HasPrefix(strings.ToLower(m[1]), "bilag") {
			if u[2] != "" {
				return nil
			}
			a.Bilag = Atoi(u[1])
		} else {
			a.Chapter, a.ChapterLitra = Atoi(u[1]), strings.ToLower(u[2])
		}
		out = append(out, a)
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// reBookAfsnit is a book of a law, and the afsnit of it, numbered in words: "fjerde bog, andet
// afsnit", "Fjerde bog, tredje og fjerde afsnit".
var reBookAfsnit = regexp.MustCompile(`(?i)\b(første|anden|tredje|fjerde|femte|sjette|syvende|ottende|niende|tiende)\s+bog\b(?:\s*,\s*(første|andet|tredje|fjerde|femte|sjette|syvende|ottende|niende|tiende|ellevte|tolvte)(?:\s+og\s+(andet|tredje|fjerde|femte|sjette|syvende|ottende|niende|tiende|ellevte|tolvte))?\s+afsnit\b)?`)
