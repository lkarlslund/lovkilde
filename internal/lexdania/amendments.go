package lexdania

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// EditKind is what an amendment does to the target provision.
type EditKind string

const (
	EditReplaceText      EditKind = "replace-text"      // ændres »X« til: »Y«
	EditDeleteText       EditKind = "delete-text"       // en bestemt passage ophæves
	EditInsertText       EditKind = "insert-text"       // indsættes følgende tekst
	EditReplaceProvision EditKind = "replace-provision" // whole § replaced, usually with AendringNyTekst markup
	EditInsertProvision  EditKind = "insert-provision"  // en ny § indsættes
	EditDeleteProvision  EditKind = "delete-provision"  // § ophæves
	// EditReplaceEverywhere is "Overalt i loven ændres »X« til: »Y«": one instruction,
	// no address, every occurrence in the act changes. It is not EditReplaceText,
	// because treating it as one would splice the change into the single provision that
	// the quoted text happened to mention and leave the rest of the act stale.
	EditReplaceEverywhere EditKind = "replace-everywhere"
	// EditNonOperative marks text inside a change list that is not an edit at all —
	// "(Sættes ikke i kraft for Færøerne)", "(Ophævet)". The consolidator records these
	// as handled without touching the text, which is what they ask for; they used to be
	// counted as changes it could not understand.
	EditNonOperative EditKind = "non-operative"
	// EditRenumber is "Stk. 2 bliver herefter stk. 3." — an instruction about
	// numbering, not about words. See applyOne for why the answer is usually "already
	// done", and when it is not.
	EditRenumber EditKind = "renumber"
	EditUnknown  EditKind = "unknown"
)

// Edit is one numbered change extracted from an amending act.
//
// Extraction keeps the prose (Prose) even when the edit is understood, because a
// consolidation must be able to show its work: when a reader asks why a paragraph
// reads the way it does, the answer is the amending document's own wording.
type Edit struct {
	Source string // accession of the amending document
	Number string // the change number, e.g. "1."
	Kind   EditKind
	Target TargetHint

	Address  Address
	Old      string
	New      string
	NewNodes []*Node // AendringNyTekst markup for provision-level replacements

	// Preceding is the address of the previous numbered change in the same block, when
	// this change names no provision of its own.
	//
	// Danish amendment lists read as a running sentence: change 1 says "I § 5, stk. 1,
	// indsættes …" and change 2 continues with "Det hidtidige stk. 1 bliver stk. 2."
	// The second change has no § anywhere in its text — it inherits one from the
	// block. Carrying it here is what makes 1,645 such changes evaluable instead of
	// anonymous; it is kept separate from Address so nothing silently applies a change
	// against a provision the change itself never named.
	Preceding Address

	// Prose is the full text of this numbered change.
	Prose string

	// Definition is the AendringDefinition text alone, which is where the address
	// and the quoted passage live.
	Definition string
}

// TargetHint identifies the law an amending paragraph is aimed at, as written in
// the preamble: "I bekendtgørelse nr. 1399 af 12. december 2019 …".
//
// It is a hint rather than an accession because the preamble quotes the Danish way
// (number plus signing date), not the register identifier. Resolving it needs an
// index of number-and-date to accession, which is what Archive provides. Note the
// asymmetry that makes this necessary at all: only 1 of 70 sampled amending acts
// declared a Concerns anchor, so the target cannot be read from metadata.
type TargetHint struct {
	Raw    string
	Number int
	Date   time.Time
	Title  string
}

func (t TargetHint) Empty() bool { return t.Number == 0 && t.Date.IsZero() }

// Amendments extracts the numbered changes from an amending document.
//
// The Lex Dania structure helps a lot here: each change is an <AendringsNummer>
// holding an <AendringDefinition> (where and what) and an <AendringAktion> whose
// <AendringNyTekst> carries the replacement text as real markup. Measured on 318
// sampled numbered changes, 68 % carried a structured AendringNyTekst and about 75 %
// matched one of the four prose patterns implemented below; the rest come back as
// EditUnknown with their prose intact, and AmendmentStats makes that gap a number
// rather than an assumption.
// amendmentBlocks returns the elements that frame a numbered change list.
//
// The publisher has more than one way to write one, and the difference is not
// cosmetic: measured over the 20,812 mirror documents that hold an
// <AendringsNummer>, reading only <AendringCentreretParagraf> claims 238,579 of
// 262,297 changes and silently drops 26,537 (10.1 %). Counted by ancestor chain,
// the dropped ones are four different things:
//
//   - 21,885 sit in bills' committee reports under
//     <AendringsForslagIndhold><TilParagrafGruppe><TilParagraf><AendringsForslag>.
//     Those are proposals, not enacted amendments, and the consolidator refuses
//     bills anyway (SkippedBills), so they stay unclaimed — deliberately, and
//     guarded in amendments_test.go so the number cannot drift into a surprise.
//   - 5,178 sit inside another change's <AendringNyTekst>. That is quoted text:
//     the instruction belongs to the change that carries it, not to a list.
//   - 2,973 sit in an ordinary <Paragraf><Stk> whose <Exitus> introduces the act
//     being changed — the shape lta/2010/648 (Visitdenmark) uses.
//   - 1,146 sit in <IkraftCentreretParagraf><Stk>, where an entry-into-force
//     paragraph carries numbered changes of its own (mt/2009/124).
//
// So a block is either an <AendringCentreretParagraf> anywhere, or a <Stk> with
// direct <AendringsNummer> children that is not inside a proposal or inside quoted
// change text. The preamble text is carried down so a nested block inherits the
// target act its enclosing paragraph names, which is what makes the change
// applicable rather than merely seen.
func amendmentBlocks(root *Node) []amendmentBlock {
	var out []amendmentBlock
	var walk func(n *Node, inQuoted bool, preamble string)
	walk = func(n *Node, inQuoted bool, preamble string) {
		switch {
		case n.Name == "AendringCentreretParagraf":
			out = append(out, amendmentBlock{n, amendmentPreamble(n)})
			preamble = amendmentPreamble(n)
		case n.Name == "AendringsForslag" || n.Name == "AendringNyTekst":
			inQuoted = true
		case n.Name == "Stk" && !inQuoted && len(n.ElemsNamed("AendringsNummer")) > 0:
			own := amendmentPreamble(n)
			if own == "" {
				own = preamble
			}
			out = append(out, amendmentBlock{n, own})
		}
		for _, c := range n.Elems {
			walk(c, inQuoted, preamble)
		}
	}
	walk(root, false, "")
	return out
}

// amendmentBlock is one change list and the text that names the act it changes.
type amendmentBlock struct {
	Node     *Node
	Preamble string
}

func (d *Document) Amendments() []Edit {
	var out []Edit
	for _, block := range amendmentBlocks(d.Root) {
		par := block.Node
		target := parseTarget(block.Preamble)
		// The § a change list is talking about, carried from change to change: the
		// list reads as one sentence, and later changes say only "det hidtidige stk.
		// 1". Reset for every framing paragraph, which starts a new subject.
		var preceding Address
		for _, num := range par.ElemsNamed("AendringsNummer") {
			// AendringsNummer wraps Aendring, and the definition and the action live
			// inside Aendring — one level deeper than the number itself. Reading them
			// as direct children yields empty strings for every field that matters,
			// which looks like "amendment with no text" rather than a bug.
			actions := num.ElemsNamed("Aendring")
			if len(actions) == 0 {
				// Tolerate the flatter shape rather than losing the change entirely.
				actions = []*Node{num}
			}
			for _, act := range actions {
				e := Edit{
					Source: d.Meta.Accession,
					Number: strings.TrimSpace(num.Elem("Explicatus").TextOrNull()),
					Target: target,
					Prose:  DisplayText(num),
				}
				if def := act.Elem("AendringDefinition"); def != nil {
					e.Definition = DisplayText(def)
					e.Address = ParseAddress(e.Definition)
					// An entry-into-force paragraph names no act at all — its Exitus
					// says when the act applies — so the change's own definition is the
					// only place its target can come from: "I bekendtgørelse nr. 1399 af
					// 12. december 2019 … ændres »X« til: »Y«". Only used when the
					// framing preamble named nothing, because a preamble is the
					// publisher's own statement of what the list changes and a
					// definition may instead quote an intermediate act.
					if e.Target.Empty() {
						e.Target = parseTarget(e.Definition)
					}
				}
				var nyTekst []*Node
				if aktion := act.Elem("AendringAktion"); aktion != nil {
					if nt := aktion.Elem("AendringNyTekst"); nt != nil {
						nyTekst = nt.Elems
					}
				}
				e.NewNodes = nyTekst
				e.Kind = classifyEdit(e.Definition, e.Prose, nyTekst, &e)
				// Keep the running subject so a change with no § of its own can still
				// be evaluated. It is recorded rather than substituted: an edit whose
				// own text names nothing is a different thing from one that names a §,
				// and applyOne decides what to do with the difference.
				if e.Address.Paragraph == "" {
					e.Preceding = preceding
				} else {
					preceding = e.Address
				}
				out = append(out, e)
			}
		}
	}
	return out
}

// amendmentPreamble returns the introductory sentence of an amending paragraph,
// which names the law being changed: the <Exitus> directly under the paragraph, not
// the ones nested inside individual changes.
func amendmentPreamble(par *Node) string {
	if ex := par.Elem("Exitus"); ex != nil {
		return DisplayText(ex)
	}
	return ""
}

var (
	// reTarget matches the preamble form "bekendtgørelse nr. 1399 af 12. december
	// 2019". Two things cost more than they look:
	//
	//   - The month is a NAME in Danish practice, not a number: matching only a numeric
	//     month silently yields an empty hint, and an empty hint means "target unknown",
	//     which makes the engine apply a change aimed at a different law to the law it is
	//     reconstructing.
	//   - The instrument is often a compound: "jf. lovbekendtgørelse nr. 295 af 7. marts
	//     2022". Requiring a word boundary in front of "bekendtgørelse" fails to match
	//     inside "lovbekendtgørelse", and an amending act that changes several laws then
	//     has its election-law changes applied to the municipal government act. Measured:
	//     of 117 changes kept for lov om kommunernes styrelse, the ones naming § 96 and
	//     § 100 — paragraphs that act does not have — came from exactly this.
	//
	// Letters are therefore allowed on both sides of the instrument word, and the run up
	// to "nr." may not cross a comma, so the reference to the instrument being amended is
	// the one that is read rather than an earlier citation in the same sentence.
	reTarget = regexp.MustCompile(`(?i)(?:^|[^\p{L}])[a-zæøå]*(?:lov|bekendtgørelse|cirkulære|anordning|forordning|bekg)[a-zæøå]*[^,.]{0,60}?nr\.?\s*([0-9]+)\s*af\s*([0-9]{1,2})[./]\s*([0-9]{1,2}|[a-zæøå]+)\s*([0-9]{4})`)
)

// parseTarget reads "bekendtgørelse nr. 1399 af 12. december 2019" out of a preamble.
func parseTarget(preamble string) TargetHint {
	m := reTarget.FindStringSubmatch(preamble)
	if m == nil {
		return TargetHint{Raw: preamble}
	}
	t := TargetHint{Raw: m[0], Number: atoi(m[1])}
	day, year := atoi(m[2]), atoi(m[4])
	month := monthNumber(m[3])
	if day > 0 && month > 0 && year > 0 {
		t.Date = time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	}
	return t
}

// monthNumber resolves a Danish month name, tolerating the numeric form too.
func monthNumber(s string) int {
	s = strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "."))
	if n := atoi(s); n > 0 {
		return n
	}
	return months[s]
}

// months is the Danish month vocabulary used in dates throughout the corpus.
var months = map[string]int{
	"januar": 1, "februar": 2, "marts": 3, "april": 4, "maj": 5, "juni": 6,
	"juli": 7, "august": 8, "september": 9, "oktober": 10, "november": 11, "december": 12,
}

var (
	reOldNew = regexp.MustCompile(`(?is)ændres\s*(.*?)\s*til\s*:\s*(.*)$`)
	reQuoted = regexp.MustCompile(`(?s)»([^»«]*)«|\x{201c}([^\x{201d}]*)\x{201d}|\x{201e}([^\x{201f}]*)\x{201f}|"([^"]*)"`)
	reNewPar = regexp.MustCompile(`(?i)indsættes\s+(?:en ny|et nyt|nye)\s+(?:paragraf|§| stk|nummer)`)
	// nonOperativeMarker matches the parenthetical notes a Danish change list uses in
	// place of an instruction. They describe the provision's fate or its territorial
	// scope; none of them asks for a word to change.
	nonOperativeMarker = regexp.MustCompile(`(?i)(sættes ikke i kraft|træder ikke i kraft|ophævet|gælder ikke for|fastsættes ikke)`)
)

// classifyEdit decides what a numbered change does, preferring structured evidence
// over prose inference and saying so when neither is sufficient.
func classifyEdit(definition, prose string, nyTekst []*Node, e *Edit) EditKind {
	def := definition
	if def == "" {
		def = prose
	}
	low := strings.ToLower(def)
	trimmed := strings.TrimSpace(def)

	// Text in parentheses in a change list is a note about the provision, not an
	// instruction to change it: "(Sættes ikke i kraft for Færøerne)", "(Ophævet)".
	// Only a definition that is *entirely* parenthetical counts, so a real edit with a
	// parenthetical afterthought stays an edit.
	if strings.HasPrefix(trimmed, "(") && strings.HasSuffix(trimmed, ")") &&
		nonOperativeMarker.MatchString(trimmed) {
		return EditNonOperative
	}

	// "Overalt i loven ændres »X« til: »Y«" carries an old/new pair but no address, and
	// its § is inside the quoted text — see Address.Everywhere.
	if e.Address.Everywhere {
		if m := reOldNew.FindStringSubmatch(def); m != nil {
			e.Old = firstQuoted(m[1])
			e.New = firstQuoted(m[2])
			if e.Old != "" && e.New != "" {
				return EditReplaceEverywhere
			}
		}
		return EditUnknown
	}

	// "Stk. 2 bliver herefter stk. 3." on its own. A change that both inserts something
	// and says the old subsection becomes the new one is an insertion — the renumbering
	// is the consequence, not a separate instruction — so the operative verbs rule this
	// out before it is classified.
	if strings.Contains(low, "bliver herefter") &&
		!strings.Contains(low, "indsættes") && !strings.Contains(low, "ophæves") &&
		!strings.Contains(low, "udgår") && !strings.Contains(low, "ændres") &&
		!strings.Contains(low, "affattes") && !strings.Contains(low, "erstattes") {
		return EditRenumber
	}

	// The quoted-pair form is the most reliable prose pattern: both the old and the
	// new wording are present verbatim, so it can be applied and verified.
	if m := reOldNew.FindStringSubmatch(def); m != nil {
		e.Old = firstQuoted(m[1])
		e.New = firstQuoted(m[2])
		if e.Old != "" && e.New != "" {
			return EditReplaceText
		}
	}

	if len(nyTekst) > 0 {
		hasProvision := false
		for _, n := range nyTekst {
			if n.Name == "Paragraf" || n.Name == "Stk" || n.Name == "Kapitel" || n.Name == "TilParagraf" {
				hasProvision = true
			}
		}
		switch {
		case reNewPar.MatchString(def), strings.Contains(low, "indsættes"):
			e.Kind = EditInsertProvision
			e.New = DisplayText(nyTekst[0])
			return EditInsertProvision
		case strings.Contains(low, "ophæves"), strings.Contains(low, "affattes"), strings.Contains(low, "i stedet for"):
			e.New = DisplayText(nyTekst[0])
			return EditReplaceProvision
		case hasProvision:
			e.New = DisplayText(nyTekst[0])
			return EditReplaceProvision
		}
	}

	// No structured new text: fall back to prose with quoted passages.
	quotes := quotedAll(def)
	switch {
	case strings.Contains(low, "ophæves") && len(quotes) == 0:
		// Repealing a passage with no quote quoted means the whole provision goes.
		return EditDeleteProvision
	case strings.Contains(low, "ophæves") && len(quotes) > 0:
		e.Old = quotes[0]
		return EditDeleteText
	// "I § 6, stk. 1, udgår »§ 3, stk. 2-3,«." is the ordinary Danish way to delete
	// a passage, and it appeared in real 2026 amendments that this classifier
	// previously reported as unrecognised.
	case strings.Contains(low, "udgår") && len(quotes) > 0:
		e.Old = quotes[0]
		return EditDeleteText
	case strings.Contains(low, "udgår"):
		return EditDeleteProvision
	case strings.Contains(low, "indsættes") && len(quotes) > 0:
		e.New = quotes[0]
		if i := strings.Index(low, "indsættes"); i >= 0 && len(quotes) > 1 {
			e.New = quotes[1]
		}
		return EditInsertText
	}
	return EditUnknown
}

// firstQuoted returns the first quoted passage in s, or the trimmed string when
// nothing is quoted. Amendments delimit passages with » « but curly quotes appear
// too, and both spellings show up in the same document.
func firstQuoted(s string) string {
	if q := quotedAll(s); len(q) > 0 {
		return q[0]
	}
	return Squeeze(QuoteStrip(s))
}

// quotedAll returns every quoted passage in order.
func quotedAll(s string) []string {
	var out []string
	for _, m := range reQuoted.FindAllStringSubmatch(s, -1) {
		for _, g := range m[1:] {
			if g = Squeeze(g); g != "" {
				out = append(out, g)
				break
			}
		}
	}
	return out
}

// AmendmentStats summarises extraction coverage, which is how "did we understand
// this amendment" becomes measurable instead of assumed.
type AmendmentStats struct {
	Total               int
	ByKind              map[EditKind]int
	UnrecognisedAddress int
	WithoutNewText      int
}

// Coverage reports extraction statistics for a set of edits.
func Coverage(edits []Edit) AmendmentStats {
	s := AmendmentStats{Total: len(edits), ByKind: map[EditKind]int{}}
	for _, e := range edits {
		s.ByKind[e.Kind]++
		if !e.Address.Recognised {
			s.UnrecognisedAddress++
		}
		if len(e.NewNodes) == 0 && e.New == "" {
			s.WithoutNewText++
		}
	}
	return s
}

// String renders a one-line summary for logs and CLI output.
func (s AmendmentStats) String() string {
	return fmt.Sprintf("%d changes: %d provision-replace, %d provision-insert, %d text-replace, "+
		"%d text-delete, %d text-insert, %d unknown; %d with an unrecognised address",
		s.Total, s.ByKind[EditReplaceProvision], s.ByKind[EditInsertProvision],
		s.ByKind[EditReplaceText], s.ByKind[EditDeleteText], s.ByKind[EditInsertText],
		s.ByKind[EditUnknown], s.UnrecognisedAddress)
}

// ParseDate parses the date formats this schema uses: ISO first, then the Danish
// "24. oktober 2005" form that appears in prose.
func ParseDate(s string) (time.Time, bool) {
	s = Squeeze(strings.TrimRight(strings.TrimSpace(s), "."))
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, true
	}
	fields := strings.Fields(s)
	if len(fields) != 3 {
		return time.Time{}, false
	}
	d := atoi(strings.TrimRight(fields[0], "."))
	mo := monthNumber(fields[1])
	y := atoi(fields[2])
	if d == 0 || mo == 0 || y == 0 {
		return time.Time{}, false
	}
	return time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC), true
}
