package lexdania

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Version is one published state of a law.
//
// Every version is its own document in this register: the base act plus one document
// per amending act. Nothing here is a snapshot of consolidated text, because the
// source publishes none — the canonical ELI URI serves the text as promulgated,
// which was verified rather than assumed (of 6,000 sampled lta documents, none cite
// an amendment newer than their own publication year).
type Version struct {
	Accession string
	Effective time.Time // Ref_Af, the date the change takes effect for this law
	Title     string    // Ref_Text, the amending document's title
	Found     bool      // whether the amending document is present in the archive
	Path      string
}

// Versions lists the documents that amended this one, oldest first.
//
// The chain is read from this document's own <Change> anchors. The opposite
// direction is not available: only 1 of 70 sampled amending acts declared a
// Concerns anchor, so a chain cannot be assembled by scanning for documents that
// mention this one.
//
// Sorting is by effective date, never by publication year. The two differ often
// enough to matter: lta/1980/14.xml is signed 1980-01-07 but its StartDate is
// 1997-10-01.
func (d *Document) Versions() []Version {
	var out []Version
	for _, r := range d.Meta.Changes() {
		if r.Accn == "" {
			continue
		}
		v := Version{Accession: r.Accn, Title: r.Text}
		if t, ok := ParseDate(r.Effective); ok {
			v.Effective = t
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Effective.Equal(out[j].Effective) {
			return out[i].Effective.Before(out[j].Effective)
		}
		return out[i].Accession < out[j].Accession
	})
	return out
}

// AppliedEdit is a change the engine located and executed.
type AppliedEdit struct {
	Edit    Edit
	Section string // what it landed on, e.g. "§ 267 stk. 1"
	Note    string // caveats: approximate insertion point, numbering mismatch, …
}

// UnresolvedEdit is a change the engine refused to guess at.
//
// Refusing is the important behaviour. A consolidation that silently skips an edit
// produces text that looks authoritative and is wrong, and the reader has no way to
// tell. Every skipped change appears here with a reason.
type UnresolvedEdit struct {
	Edit   Edit
	Reason string
}

// View is a derived state of a law at a point in time. It is not a document the
// source ever published, and callers must not present it as one.
type View struct {
	Accession string
	Title     string
	AsOf      time.Time

	// Path is where the base document was read from, and is empty for views built
	// from fixtures. It is kept separate from Title so a rendered title stays clean.
	Path string

	// Doc is the derived document: a copy of the base text with the applicable
	// amendments applied. The caller's Document is never mutated.
	Doc *Document

	Chain      []Version // every known version, with Applied set on those used
	Applied    []AppliedEdit
	Unresolved []UnresolvedEdit

	// SkippedForeign counts changes in the chain that were aimed at some other law
	// than this one, which is normal: one amending act often changes several laws.
	SkippedForeign int

	// SkippedBills counts chain members that are parliamentary bills rather than
	// promulgated acts, and whose proposed text was therefore not applied. A bill
	// describes what someone wanted changed; only the enacted act changes the law.
	SkippedBills int
}

// Applied reports how many chain documents were applied.
func (v *View) AppliedCount() int {
	n := 0
	for _, c := range v.Chain {
		if c.Found && !c.Effective.After(v.AsOf) {
			n++
		}
	}
	return n
}

// Trustworthy reports whether this view can be presented as complete: no change was
// refused and no chain document was missing.
func (v *View) Trustworthy() bool {
	for _, c := range v.Chain {
		if !c.Effective.After(v.AsOf) && !c.Found {
			return false
		}
	}
	return len(v.Unresolved) == 0
}

func (v *View) String() string {
	return fmt.Sprintf("view of %s as of %s: %d edits applied, %d unresolved, %d chain documents missing",
		v.Accession, v.AsOf.Format(time.DateOnly), len(v.Applied), len(v.Unresolved), v.missingChain())
}

func (v *View) missingChain() int {
	n := 0
	for _, c := range v.Chain {
		if !c.Found && !c.Effective.After(v.AsOf) {
			n++
		}
	}
	return n
}

// Text renders the derived law as readable text.
func (v *View) Text() string {
	var b strings.Builder
	b.WriteString(v.Title + "\n")
	for _, n := range v.Doc.Body {
		b.WriteString(n.PlainText())
		b.WriteString("\n")
	}
	return collapseBlankLines(b.String())
}

// Sections returns the derived section list, reflecting every applied edit.
func (v *View) Sections() []*Section { return v.Doc.Sections() }

// Section returns the derived section at an address, or nil.
func (v *View) Section(addr string) *Section {
	return Find(v.Sections(), ParseAddress(addr))
}

// Opener resolves an accession to a parsed document. Archive supplies this; the
// consolidation engine stays free of storage concerns so it can be tested on fixtures.
type Opener func(accession string) (*Document, string, bool)

// LawAsOf reconstructs a law as it stood at a date by applying its amendment chain
// in effective-date order.
//
// Order is not an optimisation, it is correctness: an amendment's "§ 12, stk. 2"
// refers to the numbering that existed when it was written. Once earlier changes have
// inserted or repealed provisions, numbering has moved, so applying edits out of order
// — or skipping one — silently mis-targets every later edit. Sections are therefore
// recomputed after each structural change rather than computed once up front.
func LawAsOf(base *Document, asOf time.Time, open Opener) *View {
	v := &View{
		Accession: base.Meta.Accession,
		Title:     base.Title(),
		AsOf:      asOf,
		Doc:       base.Clone(),
	}
	info := base.Accession()
	for _, ver := range base.Versions() {
		if ver.Effective.After(asOf) {
			v.Chain = append(v.Chain, ver)
			continue
		}
		amender, path, ok := open(ver.Accession)
		ver.Path = path
		ver.Found = ok
		v.Chain = append(v.Chain, ver)
		if !ok {
			v.Unresolved = append(v.Unresolved, UnresolvedEdit{
				Reason: fmt.Sprintf("amending document %s is not in the archive", ver.Accession),
			})
			continue
		}
		if amender.IsBill() {
			// A bill's amendment text is a proposal, not enacted law. This is not
			// hypothetical: bills are archived next to the acts they became and can
			// appear in a chain, and their markup looks identical to the enacted one.
			v.SkippedBills++
			continue
		}
		for _, e := range amender.Amendments() {
			if !targetsLaw(e.Target, base.Meta, info) {
				v.SkippedForeign++
				continue
			}
			v.applyOne(e)
		}
	}
	return v
}

// targetsLaw checks the amending paragraph's preamble against the law we are
// reconstructing. Amendments routinely address several laws, and applying a change
// meant for another law is the worst failure this engine could have.
//
// An unparseable hint is accepted rather than rejected: refusing it would drop real
// amendments whenever the preamble names its target in an unusual way, and the
// address still has to match a provision here for anything to happen.
func targetsLaw(h TargetHint, m Meta, info AccessionInfo) bool {
	if h.Empty() {
		return true
	}
	if h.Number != 0 && h.Number != m.Number {
		return false
	}
	if !h.Date.IsZero() {
		if signed, ok := ParseDate(m.DiesSigni); ok && !signed.IsZero() && !signed.Equal(h.Date) {
			return false
		}
	}
	return true
}

// applyOne executes one edit against the current derived state.
func (v *View) applyOne(e Edit) {
	// Two kinds address no provision, and looking for one would reject them before they
	// got as far as their own case: a parenthetical note annotates the document, and a
	// replace-everywhere spans it.
	switch e.Kind {
	case EditNonOperative:
		v.Applied = append(v.Applied, AppliedEdit{
			Edit:    e,
			Section: "document",
			Note:    "read the territorial/validity note; it asks for no change to the text",
		})
		return
	case EditReplaceEverywhere:
		v.replaceEverywhere(e)
		return
	}

	// A renumbering instruction names a subsection, not a provision: "Stk. 2 bliver
	// herefter stk. 3." takes its § from the change before it. Substituting that here
	// keeps the lookup honest — with no § in the change and none in front of it, the
	// instruction is refused rather than applied wherever the numbers happen to fit.
	if e.Kind == EditRenumber && e.Address.Paragraph == "" && e.Preceding.Paragraph != "" {
		e.Address = e.Preceding
	}

	secs := v.Sections()
	sec := Find(secs, e.Address)

	// A range address (§§ 259-266) applies the same change to each paragraph in it.
	targets := []*Section{sec}
	if sec != nil && e.Address.RangeTo != "" {
		targets = rangeSections(secs, e.Address)
	}

	// Insertions are excluded from this check: their anchor provision is very often
	// repealed by the same amending act, and they have their own positional anchoring.
	// Renumbering instructions are excluded for a different reason: they may legitimately
	// name no provision, and applyRenumber reports that specific cause instead of this
	// generic one.
	if e.Kind != EditInsertProvision && e.Kind != EditRenumber {
		for _, t := range targets {
			if t == nil {
				v.reject(e, fmt.Sprintf("address %q not found in the current text", e.Address))
				return
			}
		}
	}

	switch e.Kind {
	case EditReplaceText:
		old := QuoteStrip(e.Old)
		if old == "" {
			v.reject(e, "no quoted passage to replace")
			return
		}
		for _, t := range targets {
			if !t.splice(old, e.New) {
				v.reject(e, fmt.Sprintf("quoted passage not present in %s", describe(t)))
				return
			}
			v.Applied = append(v.Applied, AppliedEdit{Edit: e, Section: describe(t)})
		}
	case EditDeleteText:
		old := QuoteStrip(e.Old)
		for _, t := range targets {
			if !t.splice(old, "") {
				v.reject(e, fmt.Sprintf("passage to delete not present in %s", describe(t)))
				return
			}
			v.Applied = append(v.Applied, AppliedEdit{Edit: e, Section: describe(t)})
		}
	case EditReplaceProvision:
		// A heading rewrite arrives as prose only — there is no <AendringNyTekst> for
		// it — so it has to be handled before the "carries no new text" guard, which
		// otherwise rejected every "overskriften … ændres til: »X«" change outright.
		if e.Address.Heading {
			newText := e.New
			if newText == "" && len(e.NewNodes) > 0 {
				newText = DisplayText(e.NewNodes[0])
			}
			for _, t := range targets {
				h, _ := headingFor(t.Container, t.Node)
				if h == nil || !setHeadingText(h, newText) {
					v.reject(e, fmt.Sprintf("no heading found before %s", describe(t)))
					return
				}
				v.Applied = append(v.Applied, AppliedEdit{Edit: e, Section: describe(t),
					Note: "replaced the heading only; the provision text is untouched"})
			}
			return
		}
		if len(e.NewNodes) == 0 {
			v.reject(e, "provision replacement carries no new text")
			return
		}
		for _, t := range targets {
			note := ""
			if !t.replaceProvision(e.NewNodes[0]) {
				v.reject(e, fmt.Sprintf("could not replace %s", describe(t)))
				return
			}
			if after := firstNumber(t.Node.Elem("Explicatus").TextOrNull()); after != "" && after != t.Number {
				note = fmt.Sprintf("numbering moved: was %s, new text is numbered %s", t.Number, after)
			}
			v.Applied = append(v.Applied, AppliedEdit{Edit: e, Section: describe(t), Note: note})
			secs = v.Sections() // numbering may have moved
			targets = []*Section{Find(secs, e.Address)}
		}
	case EditDeleteProvision:
		for _, t := range targets {
			if e.Address.Heading {
				// "Overskriften før § 3 ophæves" removes the group heading, not the
				// provision underneath it.
				if !deleteHeading(t) {
					v.reject(e, fmt.Sprintf("no heading found before %s", describe(t)))
					return
				}
				v.Applied = append(v.Applied, AppliedEdit{Edit: e, Section: describe(t),
					Note: "removed the heading only; the provision itself is untouched"})
				continue
			}
			if !t.deleteProvision() {
				v.reject(e, fmt.Sprintf("could not remove %s", describe(t)))
				return
			}
			v.Applied = append(v.Applied, AppliedEdit{Edit: e, Section: describe(t)})
			secs = v.Sections()
			targets = []*Section{Find(secs, e.Address)}
		}
	case EditInsertProvision:
		if len(e.NewNodes) == 0 {
			v.reject(e, "insertion carries no content")
			return
		}
		anchor := targets[0]
		anchorAfter := true
		note := "inserted after the addressed provision; exact position within a run of insertions is approximate"
		if anchor == nil {
			// Danish amendatory acts routinely say "Efter § 3 indsættes en ny § 4"
			// while repealing § 3 in the same act, so the anchor provision is often
			// gone by the time the insertion is applied. Refusing here would lose real
			// law, so the insertion is anchored positionally instead: it belongs where
			// the addressed number used to sit.
			anchor, anchorAfter, note = positionalAnchor(v.Sections(), e.Address)
			if anchor == nil {
				v.reject(e, fmt.Sprintf("address %q is absent and no positional anchor could be found", e.Address))
				return
			}
		}
		placed := false
		if anchorAfter {
			placed = insertAfter(anchor, e.NewNodes[0])
		} else {
			placed = insertBefore(anchor, e.NewNodes[0])
		}
		if !placed {
			v.reject(e, "could not insert the new provision")
			return
		}
		v.Applied = append(v.Applied, AppliedEdit{Edit: e, Section: describe(anchor), Note: note})
	case EditInsertText:
		for _, t := range targets {
			if !t.appendText(e.New) {
				v.reject(e, fmt.Sprintf("could not append text to %s", describe(t)))
				return
			}
			v.Applied = append(v.Applied, AppliedEdit{
				Edit: e, Section: describe(t),
				Note: "insertion point within the provision is approximate; the source states it in prose",
			})
		}
	case EditRenumber:
		v.applyRenumber(e)
		return
	default:
		v.reject(e, "change wording not recognised by the pattern set")
	}
}

// replaceEverywhere applies one "Overalt i loven ændres »X« til: »Y«" instruction across
// the whole document.
//
// It is not routed through Find on purpose: the address form has no provision in it, and
// the § that frequently appears inside the quoted text is the *content* being replaced,
// not a place to look.
func (v *View) replaceEverywhere(e Edit) {
	old := QuoteStrip(e.Old)
	if old == "" {
		v.reject(e, "no quoted passage to replace")
		return
	}
	sec := &Section{Node: v.Doc.Root}
	collectRuns(v.Doc.Root, &sec.Runs)
	n := sec.spliceAll(old, e.New)
	if n == 0 {
		v.reject(e, fmt.Sprintf("%q does not occur anywhere in the text", old))
		return
	}
	v.Applied = append(v.Applied, AppliedEdit{
		Edit:    e,
		Section: fmt.Sprintf("document (%d occurrences)", n),
		Note:    "replaced everywhere in the act",
	})
}

// applyRenumber resolves a "bliver herefter stk. N" instruction.
//
// Numbering in this model is positional — <Stk> elements carry no number, so "stk. 2"
// means the second Stk element — which means an act that inserts a new subsection and
// then says the old one becomes the next has already renumbered the text by inserting,
// because the changes are applied in the act's own order. Carrying the instruction out
// again would be wrong, so it is *checked* rather than executed: if the provision now
// has at least N subsections, the renumbering is a fact and the instruction is recorded
// as satisfied; if it does not, the structural change the instruction depends on is
// missing from this view, and it is refused with that reason rather than quietly doing
// nothing.
func (v *View) applyRenumber(e Edit) {
	// applyOne has already substituted the running subject when there was one, so an
	// empty § here means there genuinely was none. Falling back again would leave two
	// places that inherit, and only one of them would be tested.
	para := e.Address.Paragraph
	if para == "" {
		v.reject(e, "renumbering names no provision, and no earlier change in the same block names one either")
		return
	}
	after := ""
	if i := strings.Index(strings.ToLower(e.Definition), "herefter"); i >= 0 {
		after = e.Definition[i:]
	}
	want := renumberTarget(after)
	if want == 0 {
		v.reject(e, "renumbering does not say which number the provision becomes")
		return
	}
	secs := v.Sections()
	par := Find(secs, ParseAddress("§ "+para))
	if par == nil {
		v.reject(e, fmt.Sprintf("renumbering addresses § %s, which is not in the current text", para))
		return
	}
	// "nr." instructions renumber the lettered or numbered items inside a subsection,
	// "stk." instructions the subsections themselves.
	countNr := strings.Contains(strings.ToLower(after), "nr.")
	kind, counted := "stk", 0
	if countNr {
		kind = "nr"
	}
	for _, s := range secs {
		if s.Kind != kind {
			continue
		}
		if countNr {
			// Items hang under their subsection, so walk up one level.
			if s.Parent == nil || s.Parent.Parent != par {
				continue
			}
		} else if s.Parent != par {
			continue
		}
		counted++
	}
	if counted >= want {
		v.Applied = append(v.Applied, AppliedEdit{
			Edit:    e,
			Section: describe(par),
			Note: fmt.Sprintf("numbering is positional; %s already has %d %s, so the renumbering is in effect",
				describe(par), counted, kind),
		})
		return
	}
	v.reject(e, fmt.Sprintf("%s has %d %s, so %q cannot be satisfied: the structural change it depends on is not in this text",
		describe(par), counted, kind, Squeeze(e.Definition)))
}

// renumberTarget returns the first number after "herefter", i.e. the number the
// provision is told to become.
func renumberTarget(after string) int {
	if m := reStk.FindStringSubmatch(after); len(m) > 0 {
		return atoi(m[1])
	}
	if m := reNr.FindStringSubmatch(after); len(m) > 0 {
		return atoi(m[1])
	}
	return atoi(digitRun.FindString(after))
}

// spliceAll replaces every occurrence of old, right to left so that the positions of the
// occurrences still to come stay valid while the later ones are rewritten. splice()
// replaces the first occurrence only, and calling it in a loop would loop forever
// whenever the new text contains the old — "§ 5" becoming "§ 5, stk. 2" is exactly that
// shape, and it is common in legal prose.
func (s *Section) spliceAll(old, new string) int {
	if len(s.Runs) == 0 {
		return 0
	}
	var text []rune
	runes := make([][]rune, len(s.Runs))
	start := make([]int, len(s.Runs))
	for i, r := range s.Runs {
		runes[i] = []rune(r.Node.Runs[r.Idx])
		start[i] = len(text)
		text = append(text, runes[i]...)
	}

	norm, idx := matchRunes(string(text))
	needle, _ := matchRunes(QuoteStrip(old))
	if len(needle) == 0 {
		return 0
	}
	// Collect every non-overlapping occurrence in the original text.
	//
	// When the instruction quotes a single word — "Overalt i loven ændres »skib« til:
	// »fartøj«" — it means the word, not the letters inside every longer word, so an
	// occurrence with a letter on either side is skipped. Matching is case-sensitive, so
	// this is not about "Skibet" at a sentence start; it is about "… at skibet skal …",
	// which without the filter becomes "… at fartøjet skal …", a change no Danish amending
	// act asks for and which no later check would notice.
	wholeWord := old == Squeeze(old) && !strings.ContainsAny(old, " »") &&
		strings.IndexFunc(old, unicode.IsLetter) >= 0 &&
		strings.IndexFunc(old, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) < 0
	var positions []int
	for off := 0; off < len(norm); {
		pos := runeIndex(norm[off:], needle)
		if pos < 0 {
			break
		}
		if wholeWord && gluedToWord(norm, off+pos, len(needle)) {
			off += pos + 1
			continue
		}
		positions = append(positions, off+pos)
		off += pos + len(needle)
	}
	if len(positions) == 0 {
		return 0
	}

	newRunes := []rune(new)
	for k := len(positions) - 1; k >= 0; k-- {
		pos := positions[k]
		if len(idx) <= pos+len(needle)-1 {
			continue
		}
		lo, hi := idx[pos], idx[pos+len(needle)-1]+1 // rune range in text
		first := true
		for i := range runes {
			from, to := start[i], start[i]+len(runes[i])
			ovFrom, ovTo := max(lo, from), min(hi, to)
			if ovFrom >= ovTo {
				continue
			}
			a, b := ovFrom-from, ovTo-from
			repl := []rune(nil)
			if first {
				repl = newRunes
				first = false
			}
			out := make([]rune, 0, len(runes[i])-(b-a)+len(repl))
			out = append(out, runes[i][:a]...)
			out = append(out, repl...)
			out = append(out, runes[i][b:]...)
			runes[i] = out
		}
	}
	for i, r := range s.Runs {
		r.Node.Runs[r.Idx] = string(runes[i])
	}
	return len(positions)
}

// gluedToWord reports whether the occurrence at pos sits inside a longer word, which is
// how a single-word replace told to change »skib« avoids rewriting "Skibet".
func gluedToWord(norm []rune, pos, n int) bool {
	if pos > 0 && (unicode.IsLetter(norm[pos-1]) || unicode.IsDigit(norm[pos-1])) {
		return true
	}
	end := pos + n
	if end < len(norm) && (unicode.IsLetter(norm[end]) || unicode.IsDigit(norm[end])) {
		return true
	}
	return false
}

func (v *View) reject(e Edit, reason string) {
	v.Unresolved = append(v.Unresolved, UnresolvedEdit{
		Edit:   e,
		Reason: reason,
	})
}

// rangeSections expands "§§ 259-266" into the paragraphs it covers, stopping at the
// end of the list rather than running past it.
func rangeSections(secs []*Section, a Address) []*Section {
	var out []*Section
	from, to := atoi(a.Paragraph), atoi(a.RangeTo)
	for _, s := range secs {
		// Appendix provisions restart their own numbering, so "§§ 1-3" must not sweep up
		// the entry-into-force quotations printed as § 1, § 2 and § 3.
		if s.Kind != "paragraf" || s.Appendix {
			continue
		}
		n := atoi(s.Number)
		if n >= from && n <= to {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return []*Section{Find(secs, a)}
	}
	return out
}

func describe(s *Section) string {
	if s == nil {
		return "<none>"
	}
	label := s.Kind
	if s.RawNumber != "" {
		label = s.RawNumber
	}
	if s.Parent != nil && s.Parent.RawNumber != "" {
		label = s.Parent.RawNumber + " " + label
	}
	return label
}

// Clone deep-copies a subtree. Consolidation must not mutate the parsed document:
// the same Document is handed to every view of every date, and a mutated cache would
// make results depend on call order.
func (n *Node) Clone() *Node {
	c := &Node{
		Name:  n.Name,
		Space: n.Space,
		Attrs: make(map[string]string, len(n.Attrs)),
		Runs:  append([]string(nil), n.Runs...),
		Elems: make([]*Node, len(n.Elems)),
	}
	for k, v := range n.Attrs {
		c.Attrs[k] = v
	}
	for i, e := range n.Elems {
		c.Elems[i] = e.Clone()
	}
	return c
}

// Clone deep-copies a document, which is what every view operates on.
func (d *Document) Clone() *Document {
	c := &Document{
		Root:           d.Root.Clone(),
		SchemaLocation: d.SchemaLocation,
		Meta:           d.Meta,
		// Provenance has to survive the copy: every consolidated view is a Clone, and a
		// view that forgot which text was HTML-derived would report recovered text as
		// though it were the register's own.
		Source:   d.Source,
		Sidecar:  d.Sidecar,
		warnings: append([]string(nil), d.warnings...),
	}
	meta := c.Root.Elem("Meta")
	if meta != nil {
		c.Meta = parseMeta(meta)
	}
	for _, n := range c.Root.Elems {
		if n.Name != "Meta" {
			c.Body = append(c.Body, n)
		}
	}
	return c
}

// splice replaces one located passage with new text, editing the text runs in place
// so formatting outside the replaced span survives.
//
// Replacement happens through the index map from matchRunes: the match is located in
// normalised space, and the map turns those offsets back into positions in the
// original runs. Rewriting the whole provision instead would flatten italic runs and
// quotation typography, which is a visible corruption of legal text.
func (s *Section) splice(old, new string) bool {
	if len(s.Runs) == 0 {
		return false
	}
	var text []rune
	owner := make([]int, 0, 64)
	runes := make([][]rune, len(s.Runs))
	start := make([]int, len(s.Runs))
	for i, r := range s.Runs {
		runes[i] = []rune(r.Node.Runs[r.Idx])
		start[i] = len(text)
		text = append(text, runes[i]...)
		for range runes[i] {
			owner = append(owner, i)
		}
	}

	norm, idx := matchRunes(string(text))
	needle, _ := matchRunes(QuoteStrip(old))
	pos := runeIndex(norm, needle)
	if pos < 0 || len(idx) <= pos+len(needle)-1 {
		return false
	}
	lo, hi := idx[pos], idx[pos+len(needle)-1]+1 // rune range in text

	newRunes := []rune(new)
	first := true
	for i := range runes {
		from, to := start[i], start[i]+len(runes[i])
		ovFrom, ovTo := max(lo, from), min(hi, to)
		if ovFrom >= ovTo {
			continue
		}
		a, b := ovFrom-from, ovTo-from
		repl := []rune(nil)
		if first {
			repl = newRunes
			first = false
		}
		out := make([]rune, 0, len(runes[i])-(b-a)+len(repl))
		out = append(out, runes[i][:a]...)
		out = append(out, repl...)
		out = append(out, runes[i][b:]...)
		runes[i] = out
	}
	for i, r := range s.Runs {
		r.Node.Runs[r.Idx] = string(runes[i])
	}
	return true
}

// appendText adds text to the end of a provision. Used only for insertions the
// source expresses in prose, which is why the caller records an approximate-position
// note rather than presenting the result as exact.
func (s *Section) appendText(text string) bool {
	if text == "" {
		return false
	}
	if len(s.Runs) > 0 {
		last := s.Runs[len(s.Runs)-1]
		last.Node.Runs[last.Idx] = strings.TrimRight(last.Node.Runs[last.Idx], " ") + " " + text
		return true
	}
	s.Node.Runs = append(s.Node.Runs, " "+text)
	return true
}

// replaceProvision swaps a provision's content for the amendment's new markup.
func (s *Section) replaceProvision(src *Node) bool {
	if src == nil || s.Node == nil {
		return false
	}
	kids := make([]*Node, 0, len(src.Elems)+1)
	if src.Name != "Explicatus" {
		for _, c := range src.Elems {
			kids = append(kids, donorClone(c))
		}
	}
	s.Node.Elems = kids
	s.Node.Runs = make([]string, len(kids)+1)
	return true
}

// deleteProvision removes a provision from its container.
//
// Removing the element also has to remove one adjacent text run, or the
// mixed-content invariant len(Runs)==len(Elems)+1 breaks and every later index
// arithmetic reads past the slice.
func (s *Section) deleteProvision() bool {
	return removeElement(s.Container, s.Node)
}

// headingElements render the title of a group of provisions. Danish amendatory acts
// cite them as "overskriften før § 3", "rubrikaen for § 12" or "hovedoverskriften".
var headingElements = map[string]bool{
	"Rubrica": true, "Overskrift": true, "CenterOverskrift": true, "HovedOverskrift": true,
}

// headingFor returns the heading belonging to a provision — the nearest heading
// element preceding it in the same container — together with that container.
//
// Deleting a heading is a different edit from deleting the provision, and conflating
// them destroyed law: "Overskriften før § 3 ophæves" removed § 3 outright, after
// which the same act's next change, "§ 3 ophæves", reported the provision as
// missing. Both were observed in a real 2026 amendment pair.
func headingFor(container, target *Node) (*Node, *Node) {
	if container == nil || target == nil {
		return nil, nil
	}
	var last *Node
	for _, c := range container.Elems {
		if c == target {
			return last, container
		}
		if headingElements[c.Name] {
			last = c
		}
	}
	return nil, nil
}

// removeElement drops one child from its container while preserving the rule that a
// container holds one more run than it holds elements.
func removeElement(p, n *Node) bool {
	if p == nil || n == nil {
		return false
	}
	i := -1
	for k, c := range p.Elems {
		if c == n {
			i = k
			break
		}
	}
	if i < 0 {
		return false
	}
	p.Elems = append(p.Elems[:i], p.Elems[i+1:]...)
	drop := i + 1
	if drop >= len(p.Runs) {
		drop = i
	}
	p.Runs = append(p.Runs[:drop], p.Runs[drop+1:]...)
	return true
}

// deleteHeading removes a provision group's heading and nothing else.
func deleteHeading(sec *Section) bool {
	h, p := headingFor(sec.Container, sec.Node)
	if h == nil {
		return false
	}
	return removeElement(p, h)
}

// setHeadingText replaces a heading's content. It rebuilds the Rubrica>Linea>Char
// shape used throughout the corpus so derived documents still look like their
// sources rather than like synthetic output.
func setHeadingText(h *Node, text string) bool {
	if h == nil {
		return false
	}
	h.Runs = []string{"", ""}
	h.Elems = []*Node{{
		Name:  "Linea",
		Runs:  []string{"", ""},
		Elems: []*Node{{Name: "Char", Runs: []string{text}}},
	}}
	return true
}

// positionalAnchor finds where an insertion belongs when the provision it names is
// gone. The addressed number is treated as a position: the insertion goes before the
// first surviving provision with a higher number, or after the last one.
// positionalAnchor finds where an insertion belongs when the provision it names is
// gone. It returns the anchor, whether the new provision goes after it (true) or
// before it (false), and a note explaining the fallback.
//
// The new provision belongs strictly between the last surviving provision numbered
// below the addressed one and the first surviving provision numbered above it. Which
// side the anchor is on matters: inserting before the lower neighbour rather than
// after it silently files the new law in the wrong place.
func positionalAnchor(secs []*Section, a Address) (anchor *Section, after bool, note string) {
	want := atoi(a.Paragraph)
	if want == 0 {
		return nil, false, ""
	}
	var last, next *Section
	for _, s := range secs {
		// The positional anchor has to stay inside the law's own numbering series: the
		// entry-into-force appendix restarts at § 1, so without this an insertion meant
		// for § 20 would be anchored next to the appendix's § 3.
		if s.Kind != "paragraf" || s.Parent != nil || s.Appendix {
			continue
		}
		n := atoi(s.Number)
		if n == 0 {
			continue
		}
		if n < want {
			last = s
			continue
		}
		if next == nil {
			next = s
		}
	}
	note = "anchored positionally: the addressed provision no longer exists, usually because the same amending act repealed it"
	if next != nil {
		return next, false, note
	}
	if last != nil {
		return last, true, note
	}
	return nil, false, ""
}

// donorClone copies proposed text from an amending act into the law being
// consolidated, and drops localId on the way.
//
// localId numbers an element *inside the document that carries it*: an act inserting a
// new § 3 writes it as <Paragraf localId="9"> because 9 is where it sits in the act's
// own sequence. Spliced into the law unchanged, newSection reads that 9 as the
// provision's number, so the new § 3 becomes unaddressable and every later act citing
// § 3 fails to find it. The id also collides with the target law's own localIds, which
// is what <Ref> elements resolve against, so keeping it would let a reference in the
// law point into inserted text from some other act.
//
// Numbering therefore comes from the printed Explicatus, which is what the corpus
// agrees with anyway: localId and the printed § number match in 20,410 of the 20,417
// sampled provisions that carry both.
func donorClone(n *Node) *Node {
	if n == nil {
		return nil
	}
	c := n.Clone()
	c.dropLocalIDs()
	return c
}

func (n *Node) dropLocalIDs() {
	delete(n.Attrs, "localId")
	for _, c := range n.Elems {
		c.dropLocalIDs()
	}
}

// insertBefore adds a provision immediately before an existing one.
func insertBefore(before *Section, src *Node) bool {
	if before == nil || before.Container == nil || src == nil {
		return false
	}
	p := before.Container
	i := -1
	for k, c := range p.Elems {
		if c == before.Node {
			i = k
			break
		}
	}
	if i < 0 {
		return false
	}
	p.Elems = append(p.Elems, nil)
	copy(p.Elems[i+1:], p.Elems[i:])
	p.Elems[i] = donorClone(src)

	// Runs[k] precedes Elems[k], so the new element needs a run at index i: the text
	// that used to precede the anchor becomes the text that follows it.
	p.Runs = append(p.Runs, "")
	copy(p.Runs[i+1:], p.Runs[i:])
	p.Runs[i] = ""
	return true
}

// insertAfter adds a provision immediately after an existing one.
func insertAfter(after *Section, src *Node) bool {
	if after == nil || after.Container == nil || src == nil {
		return false
	}
	p := after.Container
	i := -1
	for k, c := range p.Elems {
		if c == after.Node {
			i = k
			break
		}
	}
	if i < 0 {
		return false
	}
	p.Elems = append(p.Elems, nil)
	copy(p.Elems[i+2:], p.Elems[i+1:])
	p.Elems[i+1] = donorClone(src)

	p.Runs = append(p.Runs, "")
	copy(p.Runs[i+2:], p.Runs[i+1:])
	p.Runs[i+1] = ""
	return true
}
