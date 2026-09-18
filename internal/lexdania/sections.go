package lexdania

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Section is one addressable unit of law text.
//
// Sections are collected as a flat ordered list by element name rather than by
// walking a presumed structure, because the structure is not stable across document
// types: paragraphs appear under DokumentIndhold, under ParagrafGruppe, and directly
// under Dokument depending on the document. A flat list is also all consolidation
// needs — an amendment addresses "§ 267, stk. 1", not a path.
type Section struct {
	Node *Node

	Kind string // "kapitel", "paragraf", "afsnit", "stk", "nr", "bilag", "overskrift"

	// Number is the normalised sequence number used to match an amendment's
	// address, e.g. "267". It comes from localId when present, otherwise parsed
	// out of Explicatus — localId covers only 3,681 of 4,010 sampled Paragraf
	// elements, so the fallback is load-bearing rather than theoretical.
	Number string

	// RawNumber is the element's own Explicatus text, e.g. "§ 267." — kept for
	// rendering and for reporting which section an edit landed on.
	RawNumber string

	Stk   int    // for stk sections
	Nr    int    // for nr sections
	Litra string // "a" in "§ 8 a"

	Parent *Section

	// Container is the element holding Node. Repealing or inserting a provision has
	// to edit the container, since a Node has no back-pointer to its parent.
	Container *Node

	// Appendix marks a provision that belongs to the entry-into-force and transitional
	// material rather than to the law's own numbering series. Consolidated acts quote
	// the entry-into-force paragraph of every amending act that has touched them, and
	// each of those quotations restarts at § 1, so a consolidated act routinely holds
	// several provisions printed as "§ 3" that are not the law's § 3. The full-corpus
	// scan found 25,680 such provisions: 14,852 <IkraftCentreretParagraf> plus 1,030
	// <Paragraf> inside <Ikraft>, and 9,798 <IkraftCentreretParagraf> directly under
	// <DokumentIndhold>. They stay in the tree — they are law text — but address
	// resolution ignores them, or "§ 3 ophæves" deletes a historical entry-into-force
	// quotation while the real § 3 survives.
	Appendix bool

	// Match is MatchText(Node) — the normalised form edits are located in.
	Match string

	// Runs lists every text run inside this section, in order, so a located match
	// can be spliced back into the tree instead of being rewritten wholesale.
	Runs []textRun
}

type textRun struct {
	node *Node
	idx  int
}

func (s *Section) Display() string { return DisplayText(s.Node) }

// paragraphish are the elements that carry a numbered provision. The list comes
// from the corpus element inventory, not from the XSD: the schema is not mirrored
// locally, and the extra centreret variants are how amending and entry-into-force
// documents frame their provisions.
var paragraphish = []string{"Paragraf", "TilParagraf", "AendringCentreretParagraf", "IkraftCentreretParagraf"}

// isParagraphName reports whether an element carries a numbered provision. The
// payload nesting pass has to agree with Sections() about what a paragraph is --
// absorbing stykker under an element Sections() will not read as one would build
// structure nothing can address -- so both read the same list rather than two
// copies that can drift.
func isParagraphName(name string) bool {
	for _, p := range paragraphish {
		if name == p {
			return true
		}
	}
	return false
}

// Sections returns the document's numbered provisions in order, with their
// subsections attached.
//
// Proposed text is excluded on purpose. An amending act embeds replacement
// provisions inside <AendringNyTekst>, and those <Paragraf> elements carry their
// own localId numbers; counting them as the document's own sections would make an
// amendment appear to contain the law it is amending, which corrupts every
// subsequent address lookup.
func (d *Document) Sections() []*Section {
	return sectionsIn(d.Root)
}

func sectionsIn(root *Node) []*Section {
	var out []*Section
	var walk func(container, n *Node, insideProposed, inAppendix bool)
	walk = func(container, n *Node, insideProposed, inAppendix bool) {
		proposed := insideProposed || n.Name == "AendringNyTekst" || n.Name == "AendringsNummer"
		// Appendix material is recognised by its container, measured against the whole
		// corpus: <Ikraft> holds the entry-into-force quotations and
		// <IkraftCentreretParagraf> is that material's own provision element, which some
		// documents place straight under <DokumentIndhold> without the wrapper.
		appendix := inAppendix || n.Name == "Ikraft" || n.Name == "IkraftCentreretParagraf"
		if !proposed {
			for _, name := range paragraphish {
				if n.Name != name {
					continue
				}
				sec := newSection(n, "paragraf", nil)
				sec.Container = container
				sec.Appendix = appendix
				stkIdx := 0
				for _, ch := range n.Elems {
					if ch.Name != "Stk" {
						continue
					}
					stkIdx++
					stk := newSection(ch, "stk", sec)
					stk.Container = n
					// Position is the numbering: <Stk> elements carry an id= but
					// almost never a localId=, so "stk. 2" can only mean "the second
					// Stk element". An explicit number wins if one is ever present.
					stk.Stk = atoi(firstNumber(ch.Attrs["localId"]))
					if stk.Stk == 0 {
						stk.Stk = stkIdx
					}
					sec.Runs = append(sec.Runs, stk.Runs...)

					// Nr elements are numbered the same way, and the Explicatus of a
					// numbered paragraph often carries it ("2)"), which is why the
					// explicit value is preferred when present.
					nrIdx := 0
					var lastParent *Node
					for _, it := range numberedItems(ch) {
						if it.parent != lastParent {
							nrIdx = 0
							lastParent = it.parent
						}
						nrIdx++
						nr := newSection(it.node, "nr", stk)
						nr.Container = it.parent
						nr.Nr, nr.Litra = itemMarker(it.node.Elem("Explicatus").TextOrNull())
						if nr.Nr == 0 && nr.Litra == "" {
							nr.Nr = nrIdx
						}
						nr.Stk = stk.Stk
						stk.Runs = append(stk.Runs, nr.Runs...)
						sec.Runs = append(sec.Runs, nr.Runs...)
						out = append(out, nr)
					}
					out = append(out, stk)
				}
				out = append(out, sec)
				return // children are represented through the section
			}
			switch n.Name {
			case "Kapitel":
				sec := newSection(n, "kapitel", nil)
				sec.Container = container
				sec.Number = firstNumber(n.Attrs["localId"])
				out = append(out, sec)
			case "Afsnit":
				sec := newSection(n, "afsnit", nil)
				sec.Container = container
				sec.Number = firstNumber(n.Attrs["localId"])
				// Afsnit elements carry id="Afs1" and no localId, and print their number
				// as a roman numeral: <Explicatus>Afsnit I</Explicatus>. firstNumber finds
				// no digit in that, so every one of these sections used to have an empty
				// Number and "afsnit II" could not resolve at all. The printed numeral is
				// the only number the element has, and it wins over localId because it is
				// what the amendment cites.
				if r := printedRoman(sec.RawNumber); r != "" {
					sec.Number = r
				}
				out = append(out, sec)
			case "Indledning":
				// The preamble of a consolidated act is prose at the top of
				// DokumentIndhold, and amending acts rewrite it wholesale ("Indledningen
				// affattes således:"). Without a section for it there was nothing to
				// point at, so 1,142 changes had no target.
				sec := newSection(n, "indledning", nil)
				sec.Container = container
				out = append(out, sec)
			case "Titel":
				// The document's own title, for "Lovens titel affattes således:". The
				// first one in document order is the document title; a later one belongs
				// to an annex.
				sec := newSection(n, "titel", nil)
				sec.Container = container
				out = append(out, sec)
			case "Bilag":
				sec := newSection(n, "bilag", nil)
				sec.Container = container
				out = append(out, sec)
			}
			// These are containers, not leaves, so the walk continues into them.
			// Returning here — which is what this line replaced — made every section
			// inside a chapter or afsnit invisible: a law organised as
			// Afsnit>Kapitel>ParagrafGruppe>Paragraf appeared to have no paragraphs at
			// all, so every amendment to it reported "address not found".
		}
		for _, c := range n.Elems {
			walk(n, c, proposed, appendix)
		}
	}
	walk(nil, root, false, false)
	return out
}

func newSection(n *Node, kind string, parent *Section) *Section {
	s := &Section{Node: n, Kind: kind, Parent: parent, RawNumber: strings.TrimSpace(n.Elem("Explicatus").TextOrNull())}
	s.Number = firstNumber(n.Attrs["localId"])
	if s.Number == "" {
		s.Number = firstNumber(s.RawNumber)
	}
	if kind == "paragraf" {
		s.Litra = paraLitra(s.RawNumber)
	}
	s.Match = MatchText(n.PlainText())
	collectRuns(n, &s.Runs)
	return s
}

// paraLitra extracts the letter suffix of a printed § number: "§ 2 a." yields "a".
//
// The corpus writes the provision "§ 2 a." and amendments cite it as "§ 2a". Find
// already accepts a number plus a Litra, so recording the suffix here is what makes
// the two spellings resolve; without it every "§ 2a" address reported the provision
// as missing from a law that plainly contains it.
func paraLitra(raw string) string {
	i := digitRun.FindStringIndex(raw)
	if i == nil {
		return ""
	}
	if m := reParaLitra.FindStringSubmatch(raw[i[1]:]); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// numberedItem is a numbered or lettered sub-item together with its parent, which is
// the container an edit has to splice into.
type numberedItem struct {
	parent, node *Node
}

// numberedItems returns the "nr. 1" and "litra b" items under a node, in document
// order.
//
// The obvious guess — that numbered items are <Nr> children of <Stk> — is wrong for
// most of the corpus. The shape actually used is
//
//	<Stk><Exitus><Index><Indentatio formaInd="Nummer"><Explicatus>1)</Explicatus>
//
// and lettered items use the same Indentatio with "a)". One real 2026 order contains
// 0 <Nr> elements and 27 <Index> elements, so reading only <Nr> left every
// "stk. 1, nr. 2" address unresolvable.
//
// Descent stops at the first item found: nested items are not addressable in Danish
// citation practice, and flattening them would double-count their text.
func numberedItems(n *Node) []numberedItem {
	var out []numberedItem
	var walk func(p *Node)
	walk = func(p *Node) {
		for _, c := range p.Elems {
			switch {
			case c.Name == "Nr":
				out = append(out, numberedItem{p, c})
			case c.Name == "Indentatio" && numberedIndentation(c):
				out = append(out, numberedItem{p, c})
			default:
				walk(c)
			}
		}
	}
	walk(n)
	return out
}

// numberedIndentation reports whether an Indentatio is a numbered or lettered item
// rather than plain indentation. Indentatio is a general layout element, so treating
// every one as an item would invent provisions.
func numberedIndentation(n *Node) bool {
	kind := strings.ToLower(n.Attrs["formaInd"])
	if strings.Contains(kind, "nummer") || strings.Contains(kind, "litra") {
		return true
	}
	num, litra := itemMarker(n.Elem("Explicatus").TextOrNull())
	return num != 0 || litra != ""
}

// reItemMarker matches the marker a numbered item prints: "1)", "2.", "a)", "b.".
var reItemMarker = regexp.MustCompile(`^([0-9]+|[a-zæøåA-ZÆØÅ])[\).]`)

// itemMarker reads that marker: "1)" gives a number, "a)" gives a litra.
func itemMarker(s string) (num int, litra string) {
	m := reItemMarker.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, ""
	}
	if r, _ := utf8.DecodeRuneInString(m[1]); unicode.IsLetter(r) {
		return 0, strings.ToLower(m[1])
	}
	return atoi(m[1]), ""
}

// collectRuns records every text run in document order.
func collectRuns(n *Node, out *[]textRun) {
	for i := range n.Runs {
		if n.Runs[i] != "" {
			*out = append(*out, textRun{node: n, idx: i})
		}
	}
	for _, c := range n.Elems {
		collectRuns(c, out)
	}
}

// TextOrNull returns "" for a nil node, which keeps Explicatus lookups readable:
// not every section element carries one.
func (n *Node) TextOrNull() string {
	if n == nil {
		return ""
	}
	return n.Text()
}

var digitRun = regexp.MustCompile(`[0-9]+`)

// firstNumber extracts the first integer from text, e.g. "§ 267." -> "267".
func firstNumber(s string) string {
	if m := digitRun.FindString(s); m != "" {
		return m
	}
	return ""
}

// reAfsnitRoman matches the printed number of an afsnit, which the older collections
// and the large acts write in roman numerals: "Afsnit I", "Afsnit XXIV".
var reAfsnitRoman = regexp.MustCompile(`(?i)\bafsnit\s+([ivxlcdm]+)\b`)

// printedRoman returns the arabic form of a roman afsnit number, or "".
func printedRoman(raw string) string {
	if m := reAfsnitRoman.FindStringSubmatch(raw); m != nil {
		return romanToNumber(m[1])
	}
	return ""
}

// romanValues are the symbols a Danish legal number may use. "afsnit" numbers run into
// the low hundreds at most, so this is deliberately small.
var romanValues = map[string]int{"i": 1, "v": 5, "x": 10, "l": 50, "c": 100, "d": 500, "m": 1000}

// romanToNumber converts a roman numeral, rejecting anything that is not written the
// canonical way.
//
// The rejection matters more than the arithmetic: "vid", "id" and other Danish letter
// runs are made of roman symbols, and a pattern that simply added symbol values would
// happily number a section from an ordinary word. Round-tripping the value back
// through the encoder rejects "IIII", "ic" and every word that is not a numeral.
func romanToNumber(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	value := 0
	for i := 0; i < len(s); {
		cur, ok := romanValues[s[i:i+1]]
		if !ok {
			return ""
		}
		// Subtractive notation: a smaller symbol in front of a larger one counts
		// against it ("iv" is 5-1, not 1 and 5 side by side). The symbols are single
		// characters, so the pair has to be read as a comparison rather than looked up
		// as a two-letter word — looking it up silently returned 0 and rejected every
		// "IV" and "IX" in the archive.
		if i+1 < len(s) {
			if nxt, okNext := romanValues[s[i+1:i+2]]; okNext && nxt > cur {
				value += nxt - cur
				i += 2
				continue
			}
		}
		value += cur
		i++
	}
	if value == 0 || romanOf(value) != s {
		return ""
	}
	return strconv.Itoa(value)
}

// romanOf encodes a positive integer as a canonical roman numeral.
func romanOf(v int) string {
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

// Address is a parsed reference to a provision, as written in Danish amendment
// prose.
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
		fmt.Fprintf(&b, "afsnit %s", romanOf(a.Afsnit))
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

// Address patterns, each named for the Danish phrasing it matches. These were
// written against real amendment text in the archive, and the coverage number that
// Amendments() reports is what tells us whether the inventory is adequate.
var (
	// reParagraph matches only the digits. A paragraph litra is resolved afterwards,
	// in code: Go's regexp is RE2 and has no lookahead, so "§ 8 a" cannot be
	// distinguished from "§ 8 og" inside the pattern — a lookahead there would also
	// panic at package init.
	reParagraph = regexp.MustCompile(`(?i)§+\s*([0-9]+)(?:\s*-\s*([0-9]+))?`)
	// reParaLitra accepts a litra only when the single letter is followed by a
	// non-letter or the end of the text, so "§ 8 og 9" does not read as "§ 8o".
	reParaLitra = regexp.MustCompile(`^\s*([a-z])(?:[^a-z]|$)`)
	reKapitel   = regexp.MustCompile(`(?i)\bkapitlet\s*([0-9]+)|\bkapitel\s*([0-9]+)`)
	reBilag     = regexp.MustCompile(`(?i)\bbilag(?:et)?\s*([0-9]+)`)
	reStk       = regexp.MustCompile(`(?i)\bstk\.?\s*([0-9]+)\s*(?:-\s*([0-9]+))?`)
	reNr        = regexp.MustCompile(`(?i)\bnr\.?\s*([0-9]+)`)
	reLitra     = regexp.MustCompile(`(?i)\blitra\s*([a-z])`)
	reHeading   = regexp.MustCompile(`(?i)\b(overskrift|rubrik|rubrika|hovedoverskrift)`)
	// reAfsnit accepts both spellings of the number: "afsnit 2" and "afsnit XXIV".
	reAfsnit = regexp.MustCompile(`(?i)\bafsnit(?:et)?\s+([0-9]+|[ivxlcdm]+)\b`)
	// reEverywhere is a replace that spans the whole act. It deliberately has no
	// provision in it, and the § that often appears inside the quoted passage must not
	// be read as its address.
	reEverywhere = regexp.MustCompile(`(?i)\boveralt i\s+(?:hele\s+)?(?:loven|bekendtgørelsen|cirkulæret|denne)\b`)
	// rePreamble and reDocTitle name the two parts of a document that are not
	// provisions: the preamble and the document's own title.
	rePreamble = regexp.MustCompile(`(?i)\bindledning(?:en|s)?\b`)
	reDocTitle = regexp.MustCompile(`(?i)\btitel(?:en)?\b`)
	// reChangeVerb is the verbs that mean "this text is being rewritten", and its
	// boundary is written by hand: Go's \b is ASCII-only, so `\bændres` can never match
	// — "æ" is not an ASCII word character — and the whole document-title branch behind
	// it sat dead while the corpus kept feeding it "I lovens titel ændres »X« til: »Y«".
	// [^\p{L}] is Unicode-aware, which is what a Danish corpus needs, and it still
	// refuses to read "forændres" as the verb.
	reChangeVerb = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(affattes|ændres|ophæves|indsættes|tilføjes|udgår|erstattes)(?:$|[^\p{L}])`)
)

// ParseAddress reads a Danish provision reference out of amendment prose.
//
// It is deliberately conservative: it recognises the § / stk / nr / kapitel / bilag
// forms, including ranges ("§§ 259-266", "stk. 1-3") and paragraph litra
// ("§ 8 a"), and sets Recognised=false rather than guessing when it cannot find a
// number. Unrecognised addresses are counted by Amendments() and reported, so a
// missing pattern shows up as a coverage number instead of a wrong consolidation.
//
// Noise it tolerates: "i lovens § 12", "jf. § 12", "bekendtgørelsens § 5".
func ParseAddress(s string) Address {
	a := Address{Raw: Squeeze(s)}
	s = a.Raw

	if m := reHeading.FindString(s); m != "" {
		a.Heading = true
	}
	// A replace that spans the whole act has no address, and the § that so often sits
	// inside its quoted passage is content rather than a target: reading it as one
	// would splice the change into a single provision and leave every other occurrence
	// of the old wording untouched. Returning here is what keeps that from happening.
	if reEverywhere.MatchString(s) {
		a.Everywhere = true
		a.Recognised = true
		return a
	}
	if m := reAfsnit.FindStringSubmatch(s); m != nil {
		if n := atoi(m[1]); n > 0 {
			a.Afsnit = n
		} else if r := romanToNumber(m[1]); r != "" {
			a.Afsnit = atoi(r)
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
	// title. There is no footnote model yet, and rewriting the title in its place would
	// be a confident corruption of the wrong text, so the title is only claimed when no
	// footnote is mentioned.
	if reDocTitle.MatchString(s) && reChangeVerb.MatchString(s) &&
		!strings.Contains(strings.ToLower(s), "fodnote") {
		a.DocTitle = true
		a.Recognised = true
	}
	if m := reBilag.FindStringSubmatch(s); len(m) > 0 {
		a.Bilag = atoi(m[1])
		a.Recognised = true
	}
	if m := reKapitel.FindStringSubmatch(s); len(m) > 0 {
		a.Chapter = atoi(firstNonEmpty(m[1], m[2]))
		a.Recognised = true
	}
	if m := reParagraph.FindStringSubmatchIndex(s); m != nil {
		a.Paragraph = s[m[2]:m[3]]
		if m[4] >= 0 {
			a.RangeTo = s[m[4]:m[5]]
		}
		// A litra hugs the number ("§ 8a") or sits beside it ("§ 8 a"), and both
		// spellings occur in the corpus. Resolving it here rather than in the pattern
		// is what keeps "§ 8 og 9" from reading as "§ 8o".
		if lm := reParaLitra.FindStringSubmatch(s[m[3]:]); lm != nil {
			a.Paragraph += lm[1]
		}
		a.Recognised = true
	}
	if m := reStk.FindStringSubmatch(s); len(m) > 0 {
		a.Stk = atoi(m[1])
		a.StkTo = atoi(m[2])
	}
	if m := reNr.FindStringSubmatch(s); len(m) > 0 {
		a.Nr = atoi(m[1])
	}
	if m := reLitra.FindStringSubmatch(s); len(m) > 0 {
		a.Litra = m[1]
	}
	return a
}

// Find returns the section an address points at, or nil.
//
// Numbering is read from the *current* state of the section list, not from the
// document as originally published. That is the whole reason edits must be applied
// strictly in date order: an amendment's "§ 12, stk. 2" refers to the numbering that
// existed when it was written, and once earlier edits have inserted or repealed
// provisions the numbering has moved. Applying edits out of order, or skipping one,
// silently mis-targets every later edit.
func Find(secs []*Section, a Address) *Section {
	if !a.Recognised {
		return nil
	}
	if a.Bilag != 0 {
		for _, s := range secs {
			if s.Kind == "bilag" && firstNumber(s.Number+s.RawNumber) == strconv.Itoa(a.Bilag) {
				return s
			}
		}
		return nil
	}
	if a.Chapter != 0 && a.Paragraph == "" {
		for _, s := range secs {
			if s.Kind == "kapitel" && s.Number == strconv.Itoa(a.Chapter) {
				return s
			}
		}
		return nil
	}
	// The non-provision units are only claimed when the address names nothing else:
	// "I § 1 og i indledningen ændres …" has to keep pointing at § 1.
	if a.Paragraph == "" && a.Chapter == 0 && a.Bilag == 0 {
		if a.Afsnit != 0 {
			for _, s := range secs {
				if s.Kind == "afsnit" && s.Number == strconv.Itoa(a.Afsnit) {
					return s
				}
			}
			return nil
		}
		if a.Preamble {
			for _, s := range secs {
				if s.Kind == "indledning" {
					return s
				}
			}
			return nil
		}
		if a.DocTitle {
			for _, s := range secs {
				if s.Kind == "titel" {
					return s
				}
			}
			return nil
		}
	}
	if a.Paragraph == "" {
		return nil
	}

	// Paragraph litra is written two ways in the corpus: "§ 8 a" as one token, and
	// "§ 8, litra a". Accept both.
	want := strings.ToLower(a.Paragraph)
	var par *Section
	for _, s := range secs {
		// Appendix provisions are skipped on purpose. If the law's own § 3 is gone, an
		// address for § 3 resolves to nothing rather than to the entry-into-force
		// quotation of some amending act that happens to be printed as "§ 3": refusing
		// the edit is visible in Unresolved, editing the wrong provision is not.
		if s.Kind != "paragraf" || s.Appendix {
			continue
		}
		if strings.ToLower(s.Number) == want {
			par = s
			break
		}
		if len(want) > 1 && strings.EqualFold(s.Number, want[:len(want)-1]) && s.Litra == want[len(want)-1:] {
			par = s
			break
		}
	}
	if par == nil {
		return nil
	}
	if a.Stk == 0 && a.Nr == 0 && a.Litra == "" {
		return par
	}

	// A numbered or lettered item is a child of its stk, not of the paragraph, so the
	// search has to descend one level. Looking for the item among the paragraph's
	// direct children meant every "stk. 1, nr. 2" address resolved to nothing.
	parent := par
	if a.Stk != 0 {
		parent = nil
		for _, s := range secs {
			if s.Parent == par && s.Kind == "stk" && s.Stk == a.Stk {
				parent = s
				break
			}
		}
		if parent == nil {
			return nil
		}
		if a.Nr == 0 && a.Litra == "" {
			return parent
		}
	}
	for _, s := range secs {
		if s.Parent != parent {
			continue
		}
		if a.Nr != 0 && s.Nr != a.Nr {
			continue
		}
		if a.Litra != "" && !strings.EqualFold(s.Litra, a.Litra) {
			continue
		}
		return s
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
