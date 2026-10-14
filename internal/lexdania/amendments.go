package lexdania

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/lkarlslund/lovkilde/internal/lexdoc"
)

// EditKind is what an amendment does to the target provision. It is the model's own kind: the
// reader classifies, the model carries out, and one list of kinds keeps the two from drifting.
type EditKind = lexdoc.EditKind

const (
	EditReplaceText       = lexdoc.EditReplaceText       // ændres »X« til: »Y«
	EditDeleteText        = lexdoc.EditDeleteText        // en bestemt passage ophæves
	EditInsertText        = lexdoc.EditInsertText        // indsættes følgende tekst
	EditReplaceProvision  = lexdoc.EditReplaceProvision  // whole § replaced, usually with AendringNyTekst markup
	EditInsertProvision   = lexdoc.EditInsertProvision   // en ny § indsættes
	EditDeleteProvision   = lexdoc.EditDeleteProvision   // § ophæves
	EditReplaceEverywhere = lexdoc.EditReplaceEverywhere // "Overalt i loven ændres »X« til: »Y«"
	EditNonOperative      = lexdoc.EditNonOperative      // "(Sættes ikke i kraft for Færøerne)"
	EditRenumber          = lexdoc.EditRenumber          // "Stk. 2 bliver herefter stk. 3."
	EditUnknown           = lexdoc.EditUnknown
)

// Edit is one numbered change extracted from an amending act.
//
// Extraction keeps the prose (Prose) even when the edit is understood, because a
// consolidation must be able to show its work: when a reader asks why a paragraph
// reads the way it does, the answer is the amending document's own wording.
type Edit struct {
	Source string // accession of the amending document
	Number string // the change number, e.g. "1."
	// Section is the act's own provision the change list sits in; see lexdoc.Edit.Section.
	Section string
	Kind    EditKind
	Target  TargetHint

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

// TargetHint identifies the law an amending paragraph is aimed at; see lexdoc.Target.
type TargetHint = lexdoc.Target

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
	var walk func(n *Node, inQuoted bool, preamble, section string)
	walk = func(n *Node, inQuoted bool, preamble, section string) {
		if !inQuoted && (n.Name == "Paragraf" || n.Name == "AendringCentreretParagraf") {
			if num := lexdoc.FirstNumber(n.Elem("Explicatus").TextOrNull()); num != "" {
				section = num
			}
		}
		switch {
		case n.Name == "AendringCentreretParagraf":
			out = append(out, amendmentBlock{n, amendmentPreamble(n), section})
			preamble = amendmentPreamble(n)
		case n.Name == "AendringsForslag" || n.Name == "AendringNyTekst":
			inQuoted = true
		case n.Name == "Stk" && !inQuoted && len(n.ElemsNamed("AendringsNummer")) > 0:
			own := amendmentPreamble(n)
			if own == "" {
				own = preamble
			}
			out = append(out, amendmentBlock{n, own, section})
		}
		for _, c := range n.Elems {
			walk(c, inQuoted, preamble, section)
		}
	}
	walk(root, false, "", "")
	return out
}

// amendmentBlock is one change list, the text that names the act it changes, and the act's own
// provision it sits in.
type amendmentBlock struct {
	Node     *Node
	Preamble string
	Section  string
}

func (d *Document) Amendments() []Edit {
	var out []Edit
	var last Edit // the change before, for one whose list's opening was not read
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
					Source:  d.Meta.Accession,
					Number:  strings.TrimSpace(num.Elem("Explicatus").TextOrNull()),
					Section: block.Section,
					Target:  target,
					Prose:   DisplayText(num),
				}
				if def := act.Elem("AendringDefinition"); def != nil {
					e.Definition = lexdoc.RepairQuotes(DisplayText(def))
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
				if e.Target.Empty() && e.Target.Kind == "" && e.Section == "" && !last.Target.Empty() {
					// Lov nr. 517 of 2008's changes 5-7 to lov om forsikringsformidling follow its
					// changes 1-4 with the list's opening lost to the markup: no act and no § named.
					// The numbering going on says whose they are; read as aimed at nothing, they
					// went to every law the act amends.
					if n, prev := lexdoc.Atoi(strings.TrimRight(e.Number, ".")), lexdoc.Atoi(strings.TrimRight(last.Number, ".")); n > 0 && (n == prev || n == prev+1) {
						e.Target, e.Section = last.Target, last.Section
					}
				}
				// A change can carry more than one action, and the first may be empty: the new
				// subsection of B20240118105's change 3 sits in the second, and reading only the
				// first left the change with no text to apply.
				var nyTekst []*Node
				for _, aktion := range act.ElemsNamed("AendringAktion") {
					if nt := aktion.Elem("AendringNyTekst"); nt != nil {
						nyTekst = append(nyTekst, nt.Elems...)
					}
				}
				if pairs := lexdoc.AnnexPairs(e.Definition); len(nyTekst) == 0 && len(pairs) > 0 {
					// "Bilag 1 affattes som bilag 2 til denne bekendtgørelse": the new annex is
					// one this act prints, so its own annex is the new text, one change per annex.
					for _, p := range pairs {
						one := e
						one.Address = ParseAddress("bilag " + fmt.Sprint(p.Target))
						one.Address.BilagLitra = p.Litra
						one.Kind = EditReplaceProvision
						if p.Insert {
							one.Kind = EditInsertProvision
						}
						if ap := annexNumbered(d.Root, p.Source, p.Only); ap != nil {
							one.NewNodes = []*Node{ap}
						}
						out = append(out, one)
					}
					continue
				}
				if part, ok := lexdoc.ParseAnnexPart(e.Definition); ok && part.Source != 0 && len(nyTekst) == 0 {
					if ap := annexNumbered(d.Root, part.Source, false); ap != nil {
						nyTekst = []*Node{ap}
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
				last = e
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

// parseTarget reads the law a preamble names.
func parseTarget(preamble string) TargetHint { return lexdoc.ParseTarget(preamble) }

// annexNumbered returns the document's own annex with the given number, or its only annex.
func annexNumbered(root *Node, number int, only bool) *Node {
	var found []*Node
	root.Descendants(func(n *Node) {
		if n.Name != "Bilag" {
			return
		}
		label := firstNonBlank(n.Attrs["localId"], DisplayText(n.Elem("Explicatus")))
		if lexdoc.FirstNumber(label) == "" {
			// The site prints the number in the annex's first line, "Bilag 1 består af 2 afsnit:",
			// and the label element holds only the word.
			head := Squeeze(DisplayText(n))
			if r := []rune(head); len(r) > 60 {
				head = string(r[:60])
			}
			if m := reAnnexNumberNear.FindStringSubmatch(head); m != nil {
				label = m[1]
			} else if m := reAnnexID.FindStringSubmatch(n.Attrs["id"]); m != nil {
				label = m[1]
			}
		}
		if only || lexdoc.Atoi(lexdoc.FirstNumber(label)) == number {
			found = append(found, n)
		}
	})
	if len(found) == 0 || only && len(found) != 1 {
		return nil
	}
	return found[0]
}

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// classifyEdit reads a change's wording through the model's classifier, telling it what the XML
// markup brought with the change.
func classifyEdit(definition, prose string, nyTekst []*Node, e *Edit) EditKind {
	b := lexdoc.Brought{Any: len(nyTekst) > 0}
	if b.Any {
		b.First = DisplayText(nyTekst[0])
	}
	for _, n := range nyTekst {
		switch n.Name {
		case "Paragraf", "Stk", "Kapitel", "TilParagraf":
			b.Structure = true
		}
	}
	kind, old, new := lexdoc.Classify(definition, prose, e.Address, b)
	if old != "" {
		e.Old = old
	}
	if new != "" {
		e.New = new
	}
	return kind
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

// ParseDate parses the date formats this schema uses; see lexdoc.ParseDate.
func ParseDate(s string) (time.Time, bool) { return lexdoc.ParseDate(s) }

// reAnnexNumberNear is an annex's number in its opening words: "Bilag »Bilag 1 …".
var reAnnexNumberNear = regexp.MustCompile(`(?i)\bbilag[\s»]+([0-9]+)\b`)
