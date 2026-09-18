package lexdoc

import (
	"fmt"
	"strings"
	"time"
)

// Applying amendments to the law they address.
//
// This is the part of consolidation that is about law rather than about markup, which is why it
// lives here: deciding that "§ 12, stk. 2" means the second subsection of the provision that
// exists *now*, that a repeal of a subsection shifts the numbering of the ones after it, and that
// an instruction which cannot be carried out must be reported rather than guessed at — none of
// that has anything to do with which element name the register happened to use.
//
// What stays outside this package is everything that needs the archive: finding the amending acts
// in the mirror, and turning their markup into the Edits below.

// EditKind is what an amendment does to the provision it addresses.
type EditKind string

const (
	EditReplaceText      EditKind = "replace-text"      // the quoted passage becomes new text
	EditDeleteText       EditKind = "delete-text"       // the quoted passage is repealed
	EditInsertText       EditKind = "insert-text"       // new text is added to a provision
	EditReplaceProvision EditKind = "replace-provision" // a whole provision becomes new text
	EditInsertProvision  EditKind = "insert-provision"  // a new provision is added
	EditDeleteProvision  EditKind = "delete-provision"  // a provision is repealed
	// EditNonOperative is a change that annotates the document rather than its text:
	// "gælder ikke for Færøerne", "træder i kraft den …". Recording these as applied with a
	// note, rather than refusing them, is what keeps the refusal count down to real problems.
	EditNonOperative EditKind = "non-operative"
	// EditReplaceEverywhere is "Overalt i loven ændres »X« til: »Y«": no address, and the §
	// inside the quoted text is content rather than a place to look.
	EditReplaceEverywhere EditKind = "replace-everywhere"
	// EditRenumber is "Det hidtidige stk. 1 bliver stk. 2": a statement about numbering, not
	// about words. Numbering is positional in this model, so it is usually already true.
	EditRenumber EditKind = "renumber"
	EditUnknown  EditKind = "unknown"
)

// Edit is one numbered change from an amending act, as the model understands it.
//
// It deliberately carries no markup. A provision-level change arrives as provisions, stykker or
// segments — whatever the register's markup held, turned into the model's own shapes by whoever
// read it, so nothing downstream needs to know what the markup looked like.
type Edit struct {
	Source string // the amending document's identifier, so an answer can cite its origin
	Number string // the change's own number within that document, e.g. "1."

	Kind    EditKind
	Address Address
	Old     string
	New     string

	// What the change brings with it, in whatever shape the register sent it. An amendment
	// adds a subsection more often than it adds a provision (75 <Stk> against 24 <Paragraf>
	// over 90 sampled amending acts), and sometimes only lines of prose, so the shape is part
	// of the edit rather than something the engine has to guess from an address.
	//
	// A provision's number is ignored when replacing — the target keeps its identity — and is
	// what an inserted provision is addressed by afterwards.
	NewProvisions []*Provision
	NewStykker    [][]Segment
	NewProse      []string

	// Skip carries a reason from whoever read the register, for a change this model cannot
	// carry out. It surfaces as a refusal with that reason rather than as a partial
	// application, because a counted gap is reviewable and a wrong placement is not.
	Skip string

	// Preceding is the address of the previous numbered change in the same block, for the
	// Danish convention where a change list reads as one sentence: change 1 says "I § 5, stk.
	// 1, indsættes …" and change 2 continues "Det hidtidige stk. 1 bliver stk. 2." without
	// naming a § at all. It is kept separate from Address so nothing silently applies a
	// change against a provision the change never named.
	Preceding Address

	// Prose and Definition are the change as written, kept because a refusal is only
	// reviewable if the answer can show the words it was asked in.
	Prose      string
	Definition string
}

// Amendment is one amending act's contribution: when it takes effect and what it changes.
type Amendment struct {
	Source    string
	Effective time.Time
	Edits     []Edit
}

// AppliedEdit and UnresolvedEdit are the engine's books. Every change either appears as applied,
// with the provision it landed on, or as unresolved with the reason — an amendment that
// disappears silently is an unrecorded legal fact.
type AppliedEdit struct {
	Edit  Edit
	Where string
	Note  string
}

type UnresolvedEdit struct {
	Edit   Edit
	Reason string
}

// Consolidation is the result of applying a chain: the amended content is the Content that was
// edited, and these are the receipts.
type Consolidation struct {
	Applied    []AppliedEdit
	Unresolved []UnresolvedEdit

	// NoOpEdits counts text edits that found their passage and wrote back text identical to
	// what was there — legitimately when an amendment restates existing law, and worth a
	// number because "applied" must never be able to mean "nothing changed" unnoticed.
	NoOpEdits int
}

// Outcome is what one change did: applied where, or refused why.
type Outcome struct {
	Applied bool
	NoOp    bool
	Where   string
	Note    string
	Reason  string
}

// Apply carries out one change against the content as it stands.
//
// It is the engine's unit of work, exposed because "what happens to this change" is a question
// worth asking on its own: a consolidation is a chain applied in order, and anything that reviews
// a refusal has to be able to reach the state the change was offered against.
func (c *Content) Apply(e Edit) Outcome {
	out := &Consolidation{}
	c.applyOne(e, out)
	switch {
	case len(out.Applied) > 0:
		return Outcome{Applied: true, Where: out.Applied[0].Where, Note: out.Applied[0].Note, NoOp: out.NoOpEdits > 0}
	case len(out.Unresolved) > 0:
		return Outcome{Reason: out.Unresolved[0].Reason}
	}
	// An edit that neither landed nor was refused is the outcome this engine must not have: an
	// amendment that vanishes is an unrecorded legal fact. It is reported as a refusal with the
	// only honest reason available.
	return Outcome{Reason: "the change was neither applied nor refused"}
}

// AsOf returns the content as it stood on a date, along with what it took to get there.
//
// The chain is supplied rather than looked up, because knowing which documents exist is the
// archive's business; knowing what they do to a provision is this package's. Amendments are
// applied in effective-date order, because provision numbers are read against the text as it
// stands at each step — applying them out of order silently mis-targets every later edit.
//
// The content is cloned first: an as-of view must never edit the document it was made from.
func (c *Content) AsOf(chain []Amendment, asOf time.Time) (*Content, *Consolidation) {
	view := c.Clone()
	out := &Consolidation{}
	ordered := make([]Amendment, len(chain))
	copy(ordered, chain)
	sortByEffective(ordered)
	for _, am := range ordered {
		if !am.Effective.IsZero() && !asOf.IsZero() && am.Effective.After(asOf) {
			continue
		}
		for _, e := range am.Edits {
			view.applyOne(e, out)
		}
	}
	return view, out
}

// sortByEffective puts amendments in the order their changes take effect, keeping the relative
// order of same-date entries stable, since a chain arrives already ordered by its own source.
func sortByEffective(chain []Amendment) {
	for i := 1; i < len(chain); i++ {
		for j := i; j > 0 && chain[j-1].Effective.After(chain[j].Effective); j-- {
			chain[j-1], chain[j] = chain[j], chain[j-1]
		}
	}
}

// applyOne carries out one change, or records why it could not be carried out.
func (c *Content) applyOne(e Edit, out *Consolidation) {
	reject := func(reason string) {
		out.Unresolved = append(out.Unresolved, UnresolvedEdit{Edit: e, Reason: reason})
	}
	apply := func(where, note string) {
		out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: where, Note: note})
	}

	// Two kinds address no provision at all, and looking for one would refuse them before they
	// got as far as their own case: a parenthetical note annotates the document, and a
	// replace-everywhere spans it.
	switch e.Kind {
	case EditNonOperative:
		apply("document", "read the territorial/validity note; it asks for no change to the text")
		return
	case EditReplaceEverywhere:
		old := QuoteStrip(e.Old)
		if old == "" {
			reject("no quoted passage to replace")
			return
		}
		n := c.replaceEverywhere(old, e.New)
		if n == 0 {
			reject(fmt.Sprintf("%q does not occur anywhere in the text", old))
			return
		}
		apply(fmt.Sprintf("document (%d occurrences)", n), "replaced everywhere in the act")
		return
	}

	// A renumbering instruction names a subsection, not a provision: "Stk. 2 bliver herefter
	// stk. 3" takes its § from the change before it. Substituting it here keeps the lookup
	// honest — with no § in the change and none in front of it, the instruction is refused
	// rather than applied wherever the numbers happen to fit.
	if e.Kind == EditRenumber && e.Address.Paragraph == "" && e.Preceding.Paragraph != "" {
		e.Address = e.Preceding
	}

	if e.Skip != "" {
		out.Unresolved = append(out.Unresolved, UnresolvedEdit{Edit: e, Reason: e.Skip})
		return
	}

	loc, ok := c.Find(e.Address)
	targets := []Located{}
	switch {
	case ok && e.Address.RangeTo != "":
		targets = c.rangeProvisions(e.Address)
	case ok:
		targets = []Located{loc}
	}

	// Insertions are excluded from the "address must exist" rule: their anchor provision is
	// very often repealed by the same amending act, and they have positional anchoring of
	// their own. Renumbering is excluded because it may legitimately name no provision, and
	// reports that specific cause itself.
	//
	// Everything else that cannot be located is refused here, at the point where the answer is
	// known. Leaving it to the cases below would mean an empty target list, and an edit that is
	// applied to nothing and recorded nowhere is the one outcome worse than a refusal — it is
	// an unrecorded legal fact that no later report can show.
	if !ok && e.Kind != EditInsertProvision && e.Kind != EditRenumber {
		out.Unresolved = append(out.Unresolved, UnresolvedEdit{
			Edit:   e,
			Reason: fmt.Sprintf("address %q not found in the current text", e.Address),
		})
		return
	}

	switch e.Kind {
	case EditReplaceText:
		old := QuoteStrip(e.Old)
		if old == "" {
			reject("no quoted passage to replace")
			return
		}
		for _, t := range targets {
			found, changed := t.replaceSpan(c, old, e.New)
			if !found {
				reject(fmt.Sprintf("quoted passage not present in %s", t))
				return
			}
			if !changed {
				out.NoOpEdits++
			}
			apply(t.String(), "")
		}

	case EditDeleteText:
		old := QuoteStrip(e.Old)
		for _, t := range targets {
			found, changed := t.replaceSpan(c, old, "")
			if !found {
				reject(fmt.Sprintf("passage to delete not present in %s", t))
				return
			}
			if !changed {
				out.NoOpEdits++
			}
			apply(t.String(), "")
		}

	case EditInsertText:
		for _, t := range targets {
			if !t.append(c, e.New) {
				reject(fmt.Sprintf("could not append text to %s", t))
				return
			}
			apply(t.String(), "insertion point within the provision is approximate; the source states it in prose")
		}

	case EditReplaceProvision:
		// A heading rewrite arrives as prose only, so it has to be taken before the "carries no
		// new text" guard, which would otherwise refuse every "overskriften … ændres til: »X«".
		if e.Address.Heading {
			for _, t := range targets {
				if !c.SetHeadingChange(t, e.New) {
					reject(fmt.Sprintf("no heading found for %s", t))
					return
				}
				apply(t.String(), "replaced the heading only; the provision text is untouched")
			}
			return
		}
		// A replacement aimed at a subsection changes that subsection, not the provision around
		// it, and the register delivers the new wording as stykker or as lines rather than as a
		// paragraph. The shape decides where the text goes.
		for _, t := range targets {
			if t.Provision == nil {
				reject(fmt.Sprintf("cannot replace the content of %s", t))
				return
			}
			if t.Stykke != nil {
				segs := firstDonatedSegments(e)
				if len(segs) == 0 {
					reject("subsection replacement carries no new text")
					return
				}
				if !t.Provision.ReplaceSpanContent(t.Stykke.Span, segs) {
					reject(fmt.Sprintf("could not replace %s", t))
					return
				}
				// "Stk. 1 ændres til" followed by two new subsections is one change: the second
				// belongs where the first one landed, and the numbering after it moves.
				if extra := restDonatedStykker(e); len(extra) > 0 {
					if !t.Provision.InsertStykkerAfter(t.Stykke.Nr, extra) {
						reject(fmt.Sprintf("could not add the subsections following %s", t))
						return
					}
				}
				apply(t.String(), "")
				continue
			}
			with, okWith := donatedProvision(e)
			if !okWith {
				reject("provision replacement carries no new text")
				return
			}
			before := t.Provision.Number
			if !c.ReplaceContent(t.Provision, with) {
				reject(fmt.Sprintf("could not replace %s", t))
				return
			}
			note := ""
			if after := with.Number; after != "" && after != before {
				note = fmt.Sprintf("numbering moved: was %s, new text is numbered %s", before, after)
			}
			// A replacement that delivers a run of paragraphs replaces the addressed one with
			// the first and follows it with the rest. The tree reader stopped at the first.
			prev := t.Provision
			for _, extra := range e.NewProvisions[1:] {
				d := cloneProvision(*extra)
				if !c.InsertProvisionAfter(prev, &d) {
					break
				}
				prev = &d
			}
			apply(t.String(), note)
		}

	case EditDeleteProvision:
		for _, t := range targets {
			if e.Address.Heading {
				// "Overskriften før § 3 ophæves" removes the heading, not the provision under it.
				if !c.SetHeadingChange(t, "") {
					reject(fmt.Sprintf("no heading found for %s", t))
					return
				}
				apply(t.String(), "removed the heading only; the provision itself is untouched")
				continue
			}
			if t.Item != nil && t.Stykke != nil {
				if t.Provision == nil || !t.Provision.DeleteItem(t.Stykke.Nr, t.Item.Nr) {
					reject(fmt.Sprintf("could not remove %s", t))
					return
				}
				apply(t.String(), "removed the condition only; the rest renumbered by position")
				continue
			}
			if t.Stykke != nil {
				if t.Provision == nil || !t.Provision.DeleteStykke(t.Stykke.Nr) {
					reject(fmt.Sprintf("could not remove %s", t))
					return
				}
				apply(t.String(), "")
				continue
			}
			if t.Provision == nil || !c.DeleteProvision(t.Provision) {
				reject(fmt.Sprintf("could not remove %s", t))
				return
			}
			apply(t.String(), "")
		}

	case EditInsertProvision:
		// Where inserted text goes depends on the shape it arrives in, not on the word the
		// change list used. Measured over 90 amending acts, most "indsættes" instructions bring
		// a subsection for an existing provision (75 <Stk>), fewer bring a whole paragraph
		// (24 <Paragraf>), and some bring only lines of prose.
		if len(e.NewStykker) > 0 {
			// The number in an insertion address is the number the new subsection will carry,
			// not a part that exists: "I § 115 indsættes som stk. 12-14" arrives against a § 115
			// with eleven stykker, and looking up "stk. 12" there refuses the change against a
			// provision that is sitting right there. So the host is addressed by paragraph, and
			// a named subsection is only used as an anchor when it is already present.
			hostLoc, hostOK := c.insertionHost(e.Address, loc)
			if !hostOK {
				reject("inserted subsection addresses no provision")
				return
			}
			host := hostLoc.Provision
			at, note := 0, "numbering after the insertion is positional"
			if hostLoc.Stykke != nil {
				at = hostLoc.Stykke.Nr
			} else if e.Address.Stk > 0 {
				at = len(host.Stykker)
				note = fmt.Sprintf("numbered from stk. %d as the change says; subsection numbering is positional", e.Address.Stk)
			}
			if !host.InsertStykkerAfter(at, e.NewStykker) {
				reject("could not insert the new subsection")
				return
			}
			apply(hostLoc.String(), note)
			return
		}
		if len(e.NewProvisions) > 0 {
			// Danish amendatory acts routinely say "Efter § 3 indsættes en ny § 4" while
			// repealing § 3 in the same act, so the anchor is often gone by the time the
			// insertion is applied. Refusing there would lose real law, so the insertion is
			// anchored where the addressed number used to sit.
			anchor, after, note := c.insertionAnchor(loc, ok, e.Address)
			if anchor == nil {
				reject(fmt.Sprintf("address %q is absent and no positional anchor could be found", e.Address))
				return
			}
			prev, placeAfter := anchor, after
			for _, donor := range e.NewProvisions {
				// Each donated provision is copied into the content rather than adopted. One
				// chain is read by every as-of view of a law, and a view that edits an inserted
				// provision must not edit the object the chain holds for the next view.
				d := cloneProvision(*donor)
				placed := false
				if placeAfter {
					placed = c.InsertProvisionAfter(prev, &d)
				} else {
					placed = c.InsertProvisionBefore(prev, &d)
				}
				if !placed {
					reject("could not insert the new provision")
					return
				}
				prev, placeAfter = &d, true
			}
			apply(anchor.Address(), note)
			return
		}
		if len(e.NewProse) > 0 {
			// Loose lines of new text need somewhere to land. When the address named a part
			// that is not there yet — "I § 2 indsættes som nr. 4" against a § 2 with three items
			// — the host is the provision itself. Without that, this branch had no targets,
			// applied nothing, and recorded nothing: the change vanished from the report.
			proseTargets := targets
			if len(proseTargets) == 0 {
				if hostLoc, ok := c.insertionHost(e.Address, loc); ok {
					proseTargets = []Located{hostLoc}
				}
			}
			if len(proseTargets) == 0 {
				reject(fmt.Sprintf("inserted text addresses %q, which is not in the current text", e.Address))
				return
			}
			for _, t := range proseTargets {
				segs := make([]Segment, 0, len(e.NewProse))
				for _, line := range e.NewProse {
					segs = append(segs, Segment{Text: " " + line, Break: true})
				}
				if !t.appendSegments(c, segs) {
					reject(fmt.Sprintf("could not add text to %s", t))
					return
				}
				apply(t.String(), "insertion point within the provision is approximate; the source states it in prose")
			}
			return
		}
		reject("insertion carries no content")

	case EditRenumber:
		c.applyRenumber(e, out, reject, apply)

	default:
		reject("change wording not recognised by the pattern set")
	}
}

// insertionHost finds the provision inserted subsections or prose belong to.
//
// The number in an insertion address names the new text rather than an existing part: "indsættes
// som stk. 12-14" and "indsættes som nr. 4" both describe what the act is adding. Addressing the
// host by paragraph alone is what lets those changes land; a named subsection or item is used as
// the anchor only while it is actually present.
func (c *Content) insertionHost(a Address, loc Located) (Located, bool) {
	if loc.Provision != nil {
		return loc, true
	}
	host := a
	host.Stk, host.StkTo, host.Nr = 0, 0, 0
	got, found := c.Find(host)
	return got, found && got.Provision != nil
}

// insertionAnchor picks the provision a new § goes beside: the addressed one if it is still
// there, otherwise the neighbour its number used to sit between.
func (c *Content) insertionAnchor(loc Located, found bool, a Address) (anchor *Provision, after bool, note string) {
	if found && loc.Provision != nil {
		return loc.Provision, true, "inserted after the addressed provision; exact position within a run of insertions is approximate"
	}
	want := Atoi(a.Paragraph)
	if want == 0 {
		return nil, false, ""
	}
	// The anchor has to stay inside the law's own numbering series: entry-into-force material
	// restarts at § 1, so without this an insertion meant for § 20 would land beside an
	// entry-into-force quotation printed as § 3.
	var last, next *Provision
	for _, ref := range c.Provisions() {
		p := ref.Provision
		n := Atoi(p.Number)
		if n == 0 {
			continue
		}
		if n < want {
			last = p
			continue
		}
		if next == nil {
			next = p
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

// applyRenumber resolves a "bliver herefter stk. N" instruction.
//
// Numbering in this model is positional — a stykke carries no number of its own, measured on the
// corpus as 0 of 4,058 — which means an act that inserts a new subsection and then says the old
// one becomes the next has already renumbered the text by inserting, because the changes are
// applied in the act's own order. Carrying the instruction out again would be wrong, so it is
// *checked* rather than executed: if the provision now has at least N subsections the
// renumbering is a fact and the instruction is recorded as satisfied; if it does not, the
// structural change it depends on is missing from this view, and that is refused rather than
// quietly doing nothing.
func (c *Content) applyRenumber(e Edit, out *Consolidation, reject func(string), apply func(where, note string)) {
	para := e.Address.Paragraph
	if para == "" {
		reject("renumbering names no provision, and no earlier change in the same block names one either")
		return
	}
	after := ""
	if i := strings.Index(strings.ToLower(e.Definition), "herefter"); i >= 0 {
		after = e.Definition[i:]
	}
	want := renumberTarget(after)
	if want == 0 {
		reject("renumbering does not say which number the provision becomes")
		return
	}
	loc, ok := c.Find(ParseAddress("§ " + para))
	if !ok || loc.Provision == nil {
		reject(fmt.Sprintf("renumbering addresses § %s, which is not in the current text", para))
		return
	}
	countNr := strings.Contains(strings.ToLower(after), "nr.")
	kind, counted := "stk", 0
	if countNr {
		kind = "nr"
		for _, st := range loc.Provision.Stykker {
			counted += len(st.Items)
		}
	} else {
		counted = len(loc.Provision.Stykker)
	}
	where := loc.String()
	if counted >= want {
		apply(where, fmt.Sprintf("numbering is positional; %s already has %d %s, so the renumbering is in effect", where, counted, kind))
		return
	}
	reject(fmt.Sprintf("%s has %d %s, so %q cannot be satisfied: the structural change it depends on is not in this text",
		where, counted, kind, Squeeze(e.Definition)))
}

// renumberTarget returns the number the provision is told to become, asked of the same parser
// every other citation reads rather than a private pattern that could disagree about what
// "stk. 3" means.
func renumberTarget(after string) int {
	a := ParseAddress(after)
	switch {
	case a.Stk != 0:
		return a.Stk
	case a.Nr != 0:
		return a.Nr
	}
	return Atoi(FirstNumber(after))
}

// rangeProvisions expands "§§ 259-266" into the paragraphs it covers, stopping at the end of the
// series rather than running past it.
func (c *Content) rangeProvisions(a Address) []Located {
	from, to := Atoi(a.Paragraph), Atoi(a.RangeTo)
	if from == 0 || to < from {
		return nil
	}
	var out []Located
	for _, ref := range c.Provisions() {
		n := Atoi(ref.Provision.Number)
		if n == 0 {
			continue
		}
		if n < from || n > to {
			continue
		}
		// Only the law's own series: entry-into-force material restarts its numbering at § 1,
		// and "§§ 1-3" must not sweep up those quotations. Provisions() is the law's own
		// series by construction, which is what makes this the same rule in a different shape.
		out = append(out, Located{Provision: ref.Provision, Part: ref.Part})
	}
	return out
}

// replaceEverywhere applies one "Overalt i loven ændres »X« til: »Y«" instruction across the
// document.
//
// It is not routed through Find on purpose: the address form has no provision in it, and the §
// that so often sits inside the quoted text is the *content* being replaced, not a place to look.
//
// The difference from the tree reader, named rather than hidden: the tree spliced the whole
// document as one text, so it could match a passage that ran across a provision boundary. Here
// every unit is replaced within itself, which cannot. A quoted passage spanning two provisions
// would be reported as absent rather than rewritten across a boundary that the printed law does
// not have.
func (c *Content) replaceEverywhere(old, new string) int {
	n := ReplaceSegments(c.Preamble, old, new, true)
	n += ReplaceSegments(c.Title, old, new, true)
	n += ReplaceSegments(c.Signature, old, new, true)
	for _, ref := range c.Provisions() {
		n += ref.Provision.ReplaceEverywhereInSpan(Span{From: 0, To: len(ref.Provision.Segments)}, old, new)
	}
	for i := range c.EntryIntoForce {
		p := &c.EntryIntoForce[i]
		n += p.ReplaceEverywhereInSpan(Span{From: 0, To: len(p.Segments)}, old, new)
	}
	for i := range c.Appendices {
		ap := &c.Appendices[i]
		n += ReplaceSegments(ap.Segments, old, new, true)
		var walk func(nodes []Node)
		walk = func(nodes []Node) {
			for _, node := range nodes {
				switch {
				case node.Provision != nil:
					p := node.Provision
					n += p.ReplaceEverywhereInSpan(Span{From: 0, To: len(p.Segments)}, old, new)
				case node.Part != nil:
					walk(node.Part.Order)
				}
			}
		}
		walk(ap.Order)
	}
	return n
}

// replaceSpan edits the text of the unit an address resolved to, which is where the model's
// span-bounded search earns its keep: an edit addressed to stk. 2 cannot satisfy itself in
// stk. 5, because it is not looking at stk. 5.
func (l Located) replaceSpan(c *Content, old, new string) (found, changed bool) {
	switch {
	case l.Item != nil && l.Provision != nil:
		return replaceSpanOf(l.Provision, l.Item.Span, old, new)
	case l.Stykke != nil && l.Provision != nil:
		return replaceSpanOf(l.Provision, l.Stykke.Span, old, new)
	case l.Provision != nil:
		return replaceSpanOf(l.Provision, Span{From: 0, To: len(l.Provision.Segments)}, old, new)
	case l.Preamble:
		return replaceWhole(c.Preamble, old, new)
	case l.Title:
		return replaceWhole(c.Title, old, new)
	case l.Appendix != nil:
		return replaceWhole(l.Appendix.Segments, old, new)
	}
	return false, false
}

func replaceSpanOf(p *Provision, s Span, old, new string) (found, changed bool) {
	f, ch, n := replaceIn(p.Segments, s, old, new, false)
	return f && n > 0, ch
}

// replaceWhole replaces the first occurrence in a stand-alone piece of text and reports both
// answers, which is what an edit addressed at the preamble or the title needs: a count alone
// cannot say whether anything actually changed.
func replaceWhole(segs []Segment, old, new string) (found, changed bool) {
	if len(segs) == 0 {
		return false, false
	}
	f, ch, n := replaceIn(segs, Span{From: 0, To: len(segs)}, old, new, false)
	return f && n > 0, ch
}

// firstDonatedSegments is the text a replacement puts where a subsection was: the first
// subsection the change brings, marker and wording, or its lines of prose when it brings none.
func firstDonatedSegments(e Edit) []Segment {
	if len(e.NewStykker) > 0 {
		return e.NewStykker[0]
	}
	var segs []Segment
	for _, line := range e.NewProse {
		segs = append(segs, Segment{Text: " " + line, Break: true})
	}
	return segs
}

// restDonatedStykker are the further subsections one change brings. "Stk. 1 ændres til" followed
// by two new subsections is a single change in the register's own numbering, and the second one
// is law too: the tree reader inserted the first node and dropped the rest.
func restDonatedStykker(e Edit) [][]Segment {
	if len(e.NewStykker) < 2 {
		return nil
	}
	return e.NewStykker[1:]
}

// donatedProvision assembles the provision a replacement puts in the addressed one's place, from
// whichever shape the change arrived in. Assembling is cheap and it keeps the identity rule in
// one place: the target keeps its number, and what it keeps being is decided here.
func donatedProvision(e Edit) (Provision, bool) {
	if len(e.NewProvisions) > 0 {
		return cloneProvision(*e.NewProvisions[0]), true
	}
	p := Provision{}
	pos := 0
	for _, segs := range e.NewStykker {
		p.Segments = append(p.Segments, segs...)
		p.Stykker = append(p.Stykker, Stykke{Nr: len(p.Stykker) + 1, Span: Span{From: pos, To: pos + len(segs)}})
		pos += len(segs)
	}
	for _, line := range e.NewProse {
		p.Segments = append(p.Segments, Segment{Text: " " + line, Break: true})
		pos++
	}
	if len(p.Segments) == 0 {
		return Provision{}, false
	}
	return p, true
}

// append adds text to the end of whatever the address named.
func (l Located) append(c *Content, text string) bool {
	return l.appendSegments(c, []Segment{{Text: " " + text}})
}

// appendSegments adds a block of text to the end of the addressed unit. A prose insertion arrives
// as several lines, and each keeps its own segment so the paragraph breaks the source printed
// survive into the text a reader sees.
func (l Located) appendSegments(c *Content, segs []Segment) bool {
	switch {
	case l.Provision != nil:
		l.Provision.Segments = append(l.Provision.Segments, segs...)
		return true
	case l.Preamble:
		c.Preamble = append(c.Preamble, segs...)
		return true
	case l.Title:
		c.Title = append(c.Title, segs...)
		return true
	case l.Appendix != nil:
		l.Appendix.Segments = append(l.Appendix.Segments, segs...)
		return true
	}
	return false
}
