package lexdoc

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
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
	// InForce is the day the change takes effect when the act says it is later than the day the
	// act was signed: "§ 1, nr. 13-15, træder i kraft den 1. juli 2008". The chain is applied in
	// the order the acts were signed, because each is drafted against the law as the acts before
	// it left it, in force or not; InForce only decides whether a view as of a day includes it.
	InForce time.Time

	// Section is the act's own provision the change list sits in, "1" for "§ 1. I lov om …
	// foretages følgende ændringer", which the act's entry into force names: "§ 1, nr. 3, træder i
	// kraft den 1. juli 2008". Empty when the act's provisions are not numbered.
	Section string

	Kind    EditKind
	Address Address
	Old     string
	New     string

	// Anchor is the passage an inserted text goes by, when the operation was read on its own
	// rather than from Definition, and AnchorBefore puts it in front of the passage.
	Anchor       string
	AnchorBefore bool

	// Target is the law the change's preamble names. A change is only applied to the law it is
	// aimed at; one amending act often changes several.
	Target Target

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

	// NewParts are chapters, afsnit and named groups the change brings, each holding the
	// provisions the register put under it. A part holding nothing is a heading inserted between
	// provisions, which takes the text printed after it (see InsertParts).
	NewParts []*Part

	// NewText is the change's text as one block, for the units that are text rather than
	// provisions: "Indledningen affattes således", "Lovens titel affattes således".
	NewText []Segment

	// NewAppendix is a whole annex the change brings: "Bilag 2 affattes således".
	NewAppendix *Appendix

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

	// CitedStykker are the openings of the subsections of the provisions the change names, as the
	// consolidation its act cites printed them, by provision and in order: an act drafted against
	// aktivloven's consolidation of March 2019 calls § 69, stk. 2, what that of September 2019
	// prints as stk. 3, an act in between having put a subsection in front of it.
	CitedStykker map[string][]string
}

// Amendment is one amending act's contribution: when it takes effect and what it changes.
type Amendment struct {
	Source    string
	Effective time.Time
	Edits     []Edit

	// Tentative marks an act the base text may already carry: one that cites an earlier
	// consolidation of the law and dates from before the base. It is tried first, and taken only
	// when the text still prints most of the wording it changes.
	Tentative bool
	// Late marks the changes of a tentative act that took effect after the consolidation the chain
	// starts from was signed. The consolidation cannot have printed them, so a new provision they
	// bring that the text lacks, or a unit they rewrite that the text prints otherwise, says they are
	// still to be made. For an older act the same absence says nothing: a later act may have changed
	// the unit again since.
	Late bool
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

	// InBase lists the changes of tentative acts the base text already carries: the wording they
	// change is gone and the act was not applied again. A receipt, not a refusal.
	InBase []Edit

	// Pending lists the changes of acts signed by the view's day that take effect after it: law
	// passed, not yet in force, and not in the text.
	Pending []Edit
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
	c.pinNumbering()
	c.applyOne(e, out)
	c.runAfterAct()
	c.unpinNumbering()
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
	st := &asOfState{}
	for i := 0; i < len(ordered); {
		// The acts taking effect the day this one does.
		j := i + 1
		for j < len(ordered) && !ordered[i].Effective.IsZero() && ordered[j].Effective.Equal(ordered[i].Effective) {
			j++
		}
		for _, am := range view.dayOrder(ordered[i:j], asOf, st, out) {
			view.step(am, asOf, st, out)
		}
		i = j
	}
	if st.daySources > 1 {
		view.retryAfterDay(out, st.dayFrom)
	}
	view.eachProvision(func(p *Provision) { p.preDay = nil })
	return view, out
}

// asOfState is what a consolidation carries from one act to the next.
type asOfState struct {
	day     time.Time
	sameDay bool
	ahead   []aheadEdit
	// aheadText are wording changes applied ahead of their day, see supersedesAhead.
	aheadText []aheadEdit
	// dayFrom is the day's first refusal, and daySources how many acts the day has.
	dayFrom, daySources int
}

// clone is the state for a trial run on a copy of the text.
func (st *asOfState) clone() *asOfState {
	cp := *st
	cp.ahead = append([]aheadEdit(nil), st.ahead...)
	cp.aheadText = append([]aheadEdit(nil), st.aheadText...)
	return &cp
}

// dayOrder is the order to make the acts taking effect on one day in. Acts passed together are
// drafted on top of one another in an order their numbers do not give: lov nr. 893 and lov nr.
// 915 of 21 June 2022 both amend udlændingeloven from 1 July, and nr. 893 repeals "§ 9, stk. 40"
// as it is numbered once nr. 915 has repealed stk. 21. Made in the order of their numbers, nr.
// 893 repealed the subsection before it, and nr. 915's change to that one was refused. Each order
// of two or three acts is tried on a copy of the text, or of more with each moved after the
// others, and the one whose changes refuse fewest is taken; the order of their numbers when no
// other does better.
func (c *Content) dayOrder(acts []Amendment, asOf time.Time, st *asOfState, out *Consolidation) []Amendment {
	if len(acts) < 2 || len(acts) > 8 {
		return acts
	}
	refusals := func(order []Amendment) int {
		trial := c.Clone()
		tst := st.clone()
		tout := &Consolidation{Applied: append([]AppliedEdit(nil), out.Applied...), Unresolved: append([]UnresolvedEdit(nil), out.Unresolved...)}
		for _, am := range order {
			trial.step(am, asOf, tst, tout)
		}
		if tst.daySources > 1 {
			trial.retryAfterDay(tout, tst.dayFrom)
		}
		return len(tout.Unresolved) - len(out.Unresolved)
	}
	best, fewest := acts, refusals(acts)
	if fewest == 0 {
		return acts
	}
	orders := permutations(acts)[1:]
	if len(acts) > 3 {
		orders = lastOrders(acts)
	}
	for _, order := range orders {
		if n := refusals(order); n < fewest {
			best, fewest = order, n
		}
	}
	return best
}

// lastOrders are the orders of several acts with one of them moved to the end.
func lastOrders(acts []Amendment) [][]Amendment {
	var all [][]Amendment
	for i := range acts[:len(acts)-1] {
		order := make([]Amendment, 0, len(acts))
		order = append(order, acts[:i]...)
		order = append(order, acts[i+1:]...)
		all = append(all, append(order, acts[i]))
	}
	return all
}

// permutations are the orders of a few acts, the given one first.
func permutations(acts []Amendment) [][]Amendment {
	if len(acts) <= 1 {
		return [][]Amendment{acts}
	}
	var all [][]Amendment
	for i := range acts {
		rest := make([]Amendment, 0, len(acts)-1)
		rest = append(rest, acts[:i]...)
		rest = append(rest, acts[i+1:]...)
		for _, p := range permutations(rest) {
			all = append(all, append([]Amendment{acts[i]}, p...))
		}
	}
	return all
}

// step makes one act's changes.
func (c *Content) step(am Amendment, asOf time.Time, st *asOfState, out *Consolidation) {
	if !am.Effective.IsZero() && !asOf.IsZero() && am.Effective.After(asOf) {
		return
	}
	all := am.Edits
	if am.Edits = inForceBy(all, asOf); len(am.Edits) < len(all) {
		for _, e := range all {
			if e.InForce.After(asOf) {
				out.Pending = append(out.Pending, e)
			}
		}
	}
	if len(am.Edits) == 0 {
		return
	}
	st.sameDay = !am.Effective.IsZero() && am.Effective.Equal(st.day)
	if !st.sameDay {
		if st.daySources > 1 {
			c.retryAfterDay(out, st.dayFrom)
		}
		st.dayFrom, st.daySources = len(out.Unresolved), 0
	}
	st.daySources++
	st.day = am.Effective
	if am.Tentative && !c.carries(am, st.day, st.sameDay) {
		out.InBase = append(out.InBase, am.Edits...)
		return
	}
	c.carried = am.Tentative
	c.pinNumbering()
	if !st.sameDay {
		c.pinDay()
	}
	apply := func(e Edit) {
		if !st.sameDay {
			first := &Consolidation{}
			c.applyOne(e, first)
			out.Applied = append(out.Applied, first.Applied...)
			out.NoOpEdits += first.NoOpEdits
			for _, u := range first.Unresolved {
				if !c.retryCited(u, out) {
					out.Unresolved = append(out.Unresolved, u)
				}
			}
			return
		}
		c.applyOneOrDay(e, out)
	}
	// Every change in an act quotes the text as it stood before the act. A change "overalt i
	// loven" is meant for the occurrences the act's specific changes leave, so it runs after
	// them: run first, it rewrote "Investeringsforeninger, specialforeninger og
	// fåmandsforeninger" before the change deleting part of those words could find them.
	var done []Edit
	stale := c.staleNumbering(am, st.sameDay)
	for _, e := range am.Edits {
		if !e.Address.Everywhere {
			refused := len(out.Unresolved)
			before := c.aheadSnapshots(e, am.Source, am.Effective)
			beforeText := c.aheadTextSnapshot(e, am.Source, am.Effective)
			snap := c.renumberSnapshot(e)
			applied := len(out.Applied)
			if overtakenUnplaced(c, e, am.Effective, st.ahead, out) {
				done = append(done, e)
				continue
			}
			if c.countsAfterOwnInsertion(e) {
				// "I § 7 indsættes efter stk. 1 som nyt stykke", then "I § 7, stk. 3, og stk. 4,
				// ændres …": § 7 had three subsections, and the act counts them as its own
				// insertion left them.
				c.pinned = false
				apply(e)
				c.pinned = true
				for k := applied; k < len(out.Applied); k++ {
					out.Applied[k].Note = joinNote(out.Applied[k].Note, "the act numbers the subsections as its own insertion left them")
				}
			} else {
				apply(e)
			}
			if len(out.Unresolved) == refused {
				c.relabelFromProse(e, out)
			}
			if by, ok := stale[e.Address.Paragraph]; ok {
				for k := applied; k < len(out.Applied); k++ {
					out.Applied[k].Note = joinNote(out.Applied[k].Note, "the act numbers the subsections as they stood before "+by)
				}
			}
			c.recordRenumbering(e, snap, am)
			if len(out.Unresolved) == refused {
				st.ahead = append(st.ahead, before...)
			}
			// A wording change made in part counts: "»Har« til: »Når«, og »…« ændres til" made
			// its first operation and not its second.
			st.aheadText = append(st.aheadText, beforeText...)
			if len(out.Unresolved) > refused && c.replayInAct(e, done, out, refused) {
				out.Unresolved = append(out.Unresolved[:refused], out.Unresolved[refused+1:]...)
			}
			if len(out.Unresolved) > refused && overtaken(e, am.Effective, st.ahead, out, refused) {
				out.Unresolved = append(out.Unresolved[:refused], out.Unresolved[refused+1:]...)
			}
			if len(out.Unresolved) > refused && madeAlready(e, out, refused) {
				out.Unresolved = append(out.Unresolved[:refused], out.Unresolved[refused+1:]...)
			}
			if len(out.Unresolved) > refused+1 {
				// "I § 69, stk. 6, og § 69 k, stk. 6, ændres …": each place on its own.
				kept := out.Unresolved[:refused]
				for _, u := range out.Unresolved[refused:] {
					one := &Consolidation{Applied: out.Applied, Unresolved: []UnresolvedEdit{u}}
					if madeAlready(u.Edit, one, 0) {
						out.Applied = one.Applied
						continue
					}
					kept = append(kept, u)
				}
				out.Unresolved = kept
			}
			if len(out.Unresolved) > refused && c.superseded(e, am.Effective, out, refused) {
				out.Unresolved = append(out.Unresolved[:refused], out.Unresolved[refused+1:]...)
			}
			if len(out.Unresolved) > refused && c.supersedesAhead(e, am.Effective, st.aheadText, out, refused) {
				out.Unresolved = out.Unresolved[:refused] // every operation of the change is made
			}
			done = append(done, e)
		}
	}
	for _, e := range am.Edits {
		if e.Address.Everywhere {
			apply(e)
		}
	}
	c.runAfterAct()
	c.unpinNumbering()
	c.carried = false
	// New text an act appended to a subsection may carry the labels of subsections of its
	// own: "Stk. 3. …" printed inside the stk. 2 it followed is stk. 3 from now on.
	c.SplitInlineStykker()
	c.SplitInlineConditions()
	c.dropReusedStubs()
}

// rewritesNextCondition carries out "§ 1, stk. 1, nr. 6, affattes således: 6) …" where the list
// ends at nr. 5: the condition the change writes is the next one, and it goes at the list's end.
// Bekendtgørelse nr. 956 af 2014 never had a nr. 6, and no order gave it one before the order of
// 2021 "rewrote" it.
func (c *Content) rewritesNextCondition(e Edit, out *Consolidation) bool {
	a := e.Address
	if e.Kind != EditReplaceProvision || a.Nr < 2 || a.NrSuffix != "" || a.Litra != "" || a.Sentence != 0 || a.Paragraph == "" {
		return false
	}
	list := a
	list.Nr = 0
	loc, found := c.Find(list)
	if !found || loc.Stykke == nil || len(loc.Stykke.Items) != a.Nr-1 || loc.Stykke.Items[len(loc.Stykke.Items)-1].Nr != a.Nr-1 {
		return false
	}
	text := Squeeze(SegmentsText(withoutOuterQuotes(e.blockText())))
	if m := reItemLabel.FindStringSubmatch(text); m == nil || Atoi(m[2]) != a.Nr {
		return false
	}
	one := e
	one.Kind = EditInsertProvision
	one.Definition = fmt.Sprintf("I %s indsættes som nr. %d:", list, a.Nr)
	one.NewStykker, one.NewProse, one.NewText = nil, []string{text}, nil
	trial := &Consolidation{}
	c.applyOne(one, trial)
	if len(trial.Unresolved) > 0 || len(trial.Applied) == 0 {
		return false
	}
	for _, ap := range trial.Applied {
		ap.Edit = e
		ap.Note = joinNote(ap.Note, fmt.Sprintf("%s ends at nr. %d; the condition the change writes is the next one, added at its end", list, a.Nr-1))
		out.Applied = append(out.Applied, ap)
	}
	return true
}

// rewritesNeighboursSubsection carries out "§ 7, stk. 15, affattes således: Stk. 15. En
// institution, der ansøger om kompensation …" where § 7 has nine subsections and § 6, stk. 15,
// opens with the same words as the new one: the provision next to the one named holds the
// subsection the new text rewrites.
func (c *Content) rewritesNeighboursSubsection(e Edit, out *Consolidation) bool {
	a := e.Address
	if e.Kind != EditReplaceProvision || a.Stk < 2 || a.Nr != 0 || a.Sentence != 0 || a.Paragraph == "" || a.Litra != "" {
		return false
	}
	newWords := strings.Fields(reStkLabelLead.ReplaceAllString(Squeeze(SegmentsText(withoutOuterQuotes(e.blockText()))), ""))
	if len(newWords) < 8 {
		return false
	}
	want := alnumKey(strings.Join(newWords[:6], " "))
	n := Atoi(FirstNumber(a.Paragraph))
	var hit *Address
	for _, other := range []int{n - 1, n + 1} {
		b := Address{Recognised: true, Paragraph: itoa(other), Stk: a.Stk}
		loc, found := c.Find(b)
		if !found || loc.Stykke == nil || loc.Provision == nil {
			continue
		}
		have := strings.Fields(reStkLabelLead.ReplaceAllString(Squeeze(loc.Provision.SpanText(loc.Stykke.Span)), ""))
		if len(have) >= 6 && alnumKey(strings.Join(have[:6], " ")) == want {
			if hit != nil {
				return false
			}
			hit = &b
		}
	}
	if hit == nil {
		return false
	}
	one := e
	one.Address = *hit
	trial := &Consolidation{}
	c.applyAt(one, trial)
	if len(trial.Unresolved) > 0 || len(trial.Applied) == 0 {
		return false
	}
	for _, ap := range trial.Applied {
		ap.Edit = e
		ap.Note = joinNote(ap.Note, fmt.Sprintf("%s has no stk. %d; § %s, stk. %d, opens with the new text's words", a.String(), a.Stk, hit.Paragraph, a.Stk))
		out.Applied = append(out.Applied, ap)
	}
	return true
}

// splitAtStykkeLabels cuts new subsections the reader handed over as one at the labels of the
// ones after the first: "I § 8 indsættes efter stk. 2 som nye stykker: Stk. 3. … Stk. 4. … Stk.
// 5. …" came as one subsection, and the change repealing stk. 3 and 4 two years later took the
// three new ones and the old stk. 3 with them.
func splitAtStykkeLabels(news [][]Segment) [][]Segment {
	var out [][]Segment
	for _, st := range news {
		prev := 0
		if len(st) > 0 {
			if m := reStkLabelLead.FindStringSubmatch(st[0].Text); m != nil {
				prev = Atoi(m[1])
			}
		}
		from := 0
		for i := 1; i < len(st); i++ {
			m := reStkLabelLead.FindStringSubmatch(st[i].Text)
			if m == nil || prev == 0 || Atoi(m[1]) != prev+1 || strings.ContainsAny(st[i].Text, "»«") {
				continue
			}
			out = append(out, st[from:i])
			from, prev = i, prev+1
		}
		out = append(out, st[from:])
	}
	return out
}

// countsAfterOwnInsertion reports a change naming a subsection its provision had no number for
// before the act, and has now because the act inserted one ahead of it: the act counts as its own
// insertion left the provision, in that sentence everywhere. A "der bliver" clause says it counts
// as before.
func (c *Content) countsAfterOwnInsertion(e Edit) bool {
	if !c.pinned || strings.Contains(e.Definition, "bliver") {
		return false
	}
	places := ParseAddresses(e.Definition)
	if len(places) == 0 {
		places = []Address{e.Address}
	}
	for _, a := range places {
		if a.Paragraph == "" || a.Stk == 0 {
			continue
		}
		loc, ok := c.Find(Address{Recognised: true, Paragraph: a.Paragraph, Litra: a.Litra})
		if !ok || loc.Provision == nil {
			continue
		}
		p := loc.Provision
		inserted := false
		for _, st := range p.Stykker {
			inserted = inserted || st.Was == 0 && st.Nr <= a.Stk
		}
		if inserted && p.stykke(a.Stk, true) == nil && p.stykke(a.Stk, false) != nil && !p.createdInAct() {
			return true
		}
	}
	return false
}

// citationOnceInLaw is the subsection next to the one a change names that prints its quoted
// citation, when no other place in the law does: a citation printed once cannot be the
// neighbour's step in a run of changes.
func (c *Content) citationOnceInLaw(l Located, passage string, a Address) (Located, bool) {
	p := l.Provision
	want := MatchText(passage)
	if p == nil || l.Stykke == nil || l.Item != nil || a.Sentence != 0 || want == "" || strings.IndexFunc(want, unicode.IsDigit) < 0 {
		return Located{}, false
	}
	total := 0
	for _, r := range c.Provisions() {
		total += strings.Count(MatchText(r.Provision.Text()), want)
	}
	if total != 1 {
		return Located{}, false
	}
	for i := range p.Stykker {
		st := &p.Stykker[i]
		if (st.Nr == l.Stykke.Nr+1 || st.Nr == l.Stykke.Nr-1) && strings.Contains(MatchText(p.SpanText(st.Span)), want) {
			return Located{Provision: p, Stykke: st, Part: l.Part}, true
		}
	}
	return Located{}, false
}

// labelLineRun is a point an annex opens with its label on a line printed once, "78.", up to the
// line opening with the next number: where its text ends is not otherwise told, a list inside it
// numbering from 1 again.
func labelLineRun(segs []Segment, label string) (Span, bool) {
	at := -1
	for i, sg := range segs {
		if opensWith(Squeeze(sg.Text), label) {
			if at >= 0 {
				return Span{}, false
			}
			at = i
		}
	}
	if at < 0 {
		return Span{}, false
	}
	if next := successorLabel(label); next != "" {
		for i := at + 1; i < len(segs); i++ {
			if opensWith(Squeeze(segs[i].Text), next) {
				return Span{From: at, To: i}, true
			}
		}
	}
	return Span{}, false // where the last point ends is not told
}

// transposedDigits is how a text prints a quotation whose one number of three digits or more has
// its digits in another order, everything else the same, when the text prints that once and not
// the quotation.
func transposedDigits(text, quoted string) (string, bool) {
	text, quoted = Squeeze(text), Squeeze(quoted)
	if strings.Contains(text, quoted) {
		return "", false
	}
	nums := reNumberRun.FindAllStringIndex(quoted, -1)
	var found string
	for _, loc := range nums {
		n := quoted[loc[0]:loc[1]]
		if len(n) < 3 {
			continue
		}
		for _, perm := range digitOrders(n) {
			if perm == n {
				continue
			}
			v := quoted[:loc[0]] + perm + quoted[loc[1]:]
			if strings.Count(text, v) == 1 {
				if found != "" && found != v {
					return "", false
				}
				found = v
			}
		}
	}
	return found, found != ""
}

// reNumberRun is a run of digits.
var reNumberRun = regexp.MustCompile(`[0-9]+`)

// digitOrders are the orders of a short number's digits.
func digitOrders(n string) []string {
	if len(n) > 4 {
		return nil
	}
	if len(n) <= 1 {
		return []string{n}
	}
	var out []string
	for i := range n {
		for _, rest := range digitOrders(n[:i] + n[i+1:]) {
			out = append(out, string(n[i])+rest)
		}
	}
	return out
}

// annexScopeText is the text of the parts of an annex a change names, or of the whole annex.
func annexScopeText(l Located) string {
	if len(l.AnnexSpans) == 0 {
		return Squeeze(SegmentsText(l.Appendix.Segments))
	}
	var b strings.Builder
	for _, sp := range l.AnnexSpans {
		b.WriteString(SegmentsText(l.Appendix.Segments[sp.From:sp.To]))
		b.WriteByte(' ')
	}
	return Squeeze(b.String())
}

// anchoredPrinted is the one passage of a text opening with a long quotation's first six words and
// closing with its last two, of about its length: the quotation printed with a word or two
// otherwise in the middle. Short quotations are not read this way.
func anchoredPrinted(text, quoted string) (string, bool) {
	words := strings.Fields(Squeeze(quoted))
	if len(words) < 12 {
		return "", false
	}
	head := strings.Join(words[:6], " ")
	if printed, ok := anchoredBy(text, head, strings.Join(words[len(words)-2:], " "), len(words)); ok {
		return printed, true
	}
	if last := words[len(words)-1]; len([]rune(last)) >= 6 && len(words) >= 20 {
		return anchoredBy(text, head, last, len(words))
	}
	return "", false
}

// anchoredBy is the one passage opening with head and closing with tail, of about n words.
func anchoredBy(text, head, tail string, n int) (string, bool) {
	var hits []string
	for from := 0; ; {
		i := strings.Index(text[from:], head)
		if i < 0 {
			break
		}
		start := from + i
		rest := text[start:]
		for at := 0; ; {
			j := strings.Index(rest[at:], tail)
			if j < 0 {
				break
			}
			printed := rest[:at+j+len(tail)]
			if k := len(strings.Fields(printed)); k >= n-3 && k <= n+3 {
				hits = append(hits, printed)
			} else if k > n+3 {
				break
			}
			at += j + len(tail)
		}
		from = start + len(head)
	}
	if len(hits) != 1 || strings.Count(text, hits[0]) != 1 {
		return "", false
	}
	return hits[0], true
}

// cellsAre reports words every named part of an annex prints as a line of their own.
func cellsAre(l Located, words string) bool {
	for _, sp := range l.AnnexSpans {
		hit := false
		for i := sp.From; i < sp.To && i < len(l.Appendix.Segments); i++ {
			hit = hit || Squeeze(l.Appendix.Segments[i].Text) == Squeeze(words)
		}
		if !hit {
			return false
		}
	}
	return len(l.AnnexSpans) > 0
}

// annexNamedParts are the parts of an annex a change's place names before its words: "I bilag 1,
// pkt. 5 og 6, udgår …" names 5 and 6.
func annexNamedParts(def string) []string {
	head := def
	if i := strings.Index(head, "»"); i >= 0 {
		head = head[:i]
	}
	m := reAnnexNamedParts.FindStringSubmatch(head)
	if m == nil {
		return nil
	}
	var out []string
	for _, n := range reDottedNumbers.FindAllString(m[1], -1) {
		out = append(out, strings.TrimRight(n, "."))
	}
	return out
}

var (
	reAnnexNamedParts = regexp.MustCompile(`(?i)\bbilag\s+[0-9]+\s*,\s*(?:pkt\.|punkt|punkterne|nr\.)\s*([0-9]+(?:\.[0-9]+)*\.?(?:\s*(?:,|og)\s*[0-9]+(?:\.[0-9]+)*\.?)*)\s*,`)
	reDottedNumbers   = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)*`)
)

// partOpenedBy is the innermost part with a heading that opens with the provision, through groups
// that print none.
func (c *Content) partOpenedBy(p *Provision) *Part {
	if p == nil {
		return nil
	}
	var first func(list []Node) *Provision
	first = func(list []Node) *Provision {
		if len(list) == 0 {
			return nil
		}
		switch n := list[0]; {
		case n.Provision != nil:
			return n.Provision
		case n.Part != nil && n.Part.Heading == "":
			return first(n.Part.Order)
		}
		return nil
	}
	var found *Part
	for _, pt := range c.Parts() {
		if pt.Heading != "" && first(pt.Order) == p {
			found = pt // Parts lists outer parts first; the last is the innermost
		}
	}
	return found
}

// reHeadingBefore is a heading named as the one before a provision: "Overskriften før § 145".
var reHeadingBefore = regexp.MustCompile(`(?i)overskriften\s+(?:før|foran)\s+§`)

// labelledScopes reports scopes that are all labels the annex prints, or headings numbered as their
// neighbours are ("FELT08 …" up to "FELT09 …"), not heading words a run of lines is searched for:
// one is found, but where its part ends is not known, and a new last paragraph of it went at the
// end of the annex.
func labelledScopes(scopes []string) bool {
	for _, sc := range scopes {
		if strings.HasPrefix(sc, "~") && wordNumber(strings.TrimPrefix(sc, "~")) == nil {
			return false
		}
	}
	return len(scopes) > 0
}

// lowerFirst is a sentence with its first letter in lower case: "Som nyt punkt" read on from "I
// bilag 1, ".
func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToLower(r)) + s[n:]
}

// retryAfterDay offers again the changes refused on a day several acts took effect, once all of
// them are made. Acts passed together are drafted on top of one another in an order no number
// gives: lov nr. 893 and lov nr. 915 of 21 June 2022 both amend udlændingeloven from 1 July, and
// nr. 893 repeals "§ 11, stk. 13, nr. 1, 2. pkt.", a subsection nr. 915 inserts.
func (c *Content) retryAfterDay(out *Consolidation, from int) {
	if from >= len(out.Unresolved) {
		return
	}
	kept := out.Unresolved[:from:from]
	for _, u := range out.Unresolved[from:] {
		if !strings.Contains(u.Reason, "not found in the current text") {
			kept = append(kept, u)
			continue
		}
		// A change naming several places is offered again at the one it was refused at.
		several := len(ParseAddresses(u.Edit.Definition)) > 1
		try := func(v *Content) *Consolidation {
			trial := &Consolidation{}
			v.pinNumbering()
			if several {
				v.applyAt(u.Edit, trial)
			} else {
				v.applyOne(u.Edit, trial)
			}
			v.runAfterAct()
			v.unpinNumbering()
			return trial
		}
		if trial := try(c.Clone()); len(trial.Unresolved) > 0 || len(trial.Applied) == 0 {
			kept = append(kept, u)
			continue
		}
		trial := try(c)
		for _, a := range trial.Applied {
			a.Note = joinNote(a.Note, "made after the other acts taking effect the same day, which it is drafted on")
			out.Applied = append(out.Applied, a)
		}
		out.NoOpEdits += trial.NoOpEdits
	}
	out.Unresolved = kept
}

// replayInAct carries out a change whose words an earlier change of the same act rewrote first.
// Every change of an act quotes the text as it stood before the act: "I § 43, stk. 1, ændres
// »integrationsydelse efter § 22 …«" after a change replacing »integrationsydelse« throughout the
// subsection found its passage gone. The change is made on the provision as it stood before the act,
// the act's earlier changes to it are made again, and the result takes the provision's place. An
// earlier change may then find nothing only where its words were inside this one's passage.
func (c *Content) replayInAct(e Edit, done []Edit, out *Consolidation, at int) bool {
	if len(out.Unresolved) != at+1 || e.Address.Everywhere {
		return false
	}
	switch e.Kind {
	case EditReplaceText, EditDeleteText, EditInsertText:
	default:
		return false
	}
	loc, ok := c.Find(e.Address)
	if !ok || loc.Provision == nil || loc.Provision.preAct == nil {
		return false
	}
	target := loc.Provision
	scratch := &Content{Order: []Node{{Provision: func() *Provision { p := cloneProvision(*target.preAct); return &p }()}}}
	scratch.pinNumbering()
	trial := &Consolidation{}
	scratch.applyOne(e, trial)
	if len(trial.Unresolved) > 0 || len(trial.Applied) == 0 {
		return false
	}
	para := e.Address.Paragraph
	for _, d := range done {
		names := false
		for _, a := range append(ParseAddresses(d.Definition), d.Address) {
			names = names || a.Paragraph == para
		}
		if !names {
			continue
		}
		again := &Consolidation{}
		scratch.applyOne(d, again)
		for _, u := range again.Unresolved {
			if loc, found := scratch.Find(u.Edit.Address); found && loc.Provision != nil && u.Edit.Address.Paragraph == para &&
				!(d.Old != "" && strings.Contains(MatchText(QuoteStrip(e.Old)), MatchText(QuoteStrip(d.Old)))) {
				return false
			}
		}
	}
	if !c.ReplaceContent(target, *scratch.Order[0].Provision) {
		return false
	}
	target.Stykker = scratch.Order[0].Provision.Stykker
	out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: loc.String(),
		Note: "an earlier change of the same act had rewritten the words; carried out on the text before the act, with the act's earlier changes to it made again"})
	return true
}

// aheadEdit is a change applied before its day: the chain runs in the order the acts were signed,
// and a part of an act taking effect later than the act was signed rewrites the text ahead of acts
// signed after it that take effect sooner. Address is the unit it rewrites or repeals, one of those
// the change names, and Before is the provision holding it as it stood until then.
type aheadEdit struct {
	Edit    Edit
	Address Address
	Source  string
	Before  *Provision
}

// aheadSnapshots are the provisions a change rewrites or repeals whole ahead of its day, as they
// stand before the change, one for each unit the change names; none for any other change.
func (c *Content) aheadSnapshots(e Edit, source string, effective time.Time) []aheadEdit {
	if e.Kind != EditReplaceProvision && e.Kind != EditDeleteProvision || e.InForce.IsZero() || !e.InForce.After(effective) || e.Address.Heading {
		return nil
	}
	addrs := []Address{e.Address}
	if more := ParseAddresses(e.Definition); len(more) > 1 {
		addrs = more
	}
	var out []aheadEdit
	for _, a := range addrs {
		if a.Paragraph == "" || a.Heading {
			continue
		}
		loc, ok := c.Find(Address{Recognised: true, Paragraph: a.Paragraph})
		if !ok || loc.Provision == nil {
			continue
		}
		p := cloneProvision(*loc.Provision)
		out = append(out, aheadEdit{Edit: e, Address: a, Source: source, Before: &p})
	}
	return out
}

// overtaken settles a refused change whose words a change applied ahead of its day had already
// rewritten. Lov nr. 2606 of 2021 put "§ 20, stk. 18." into varmeforsyningsloven § 20 b, stk. 1, 2.
// pkt., from 1 January 2022, and rewrote the whole sentence from 1 January 2024; lov nr. 217 of
// 2023 changed the citation in between. In the order the acts were signed the sentence was already
// rewritten when the 2023 change came, and it was refused, though on the day it took effect it found
// its words, and from 2024 nothing of it remains. It counts as applied when it does find its words
// in the provision as it stood before the rewrite, and the rewrite covers the unit it changes and
// takes effect after it.
func overtaken(e Edit, effective time.Time, ahead []aheadEdit, out *Consolidation, at int) bool {
	if len(out.Unresolved) != at+1 || e.Address.Everywhere || e.Address.Paragraph == "" {
		return false
	}
	day := effective
	if !e.InForce.IsZero() {
		day = e.InForce
	}
	for i := len(ahead) - 1; i >= 0; i-- {
		a := ahead[i]
		if !a.Edit.InForce.After(day) || !covers(a.Address, e.Address) {
			continue
		}
		scratch := &Content{Order: []Node{{Provision: func() *Provision { p := cloneProvision(*a.Before); return &p }()}}}
		trial := &Consolidation{}
		scratch.applyOne(e, trial)
		if len(trial.Unresolved) > 0 || len(trial.Applied) == 0 {
			continue
		}
		verb := "rewrote"
		if a.Edit.Kind == EditDeleteProvision {
			verb = "repealed"
		}
		out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: e.Address.String(),
			Note: fmt.Sprintf("found its words as they stood until %s, when %s %s %s %s; nothing of it remains", a.Edit.InForce.Format("2. 1. 2006"), a.Source, strings.TrimSpace(a.Edit.Number), verb, a.Address)})
		return true
	}
	return false
}

// lacksSentenceHost reports an address for a new sentence whose unit is not there: a condition its
// subsection does not hold, or a subsection the provision neither has nor prints the label of.
func (c *Content) lacksSentenceHost(a Address) bool {
	st := Address{Recognised: true, Paragraph: a.Paragraph, Stk: a.Stk}
	if loc, ok := c.Find(st); ok && loc.Provision != nil {
		return a.Nr > 0 || a.Litra != ""
	}
	return a.Stk > 1 && !c.printsStykke(a)
}

// printsStykke reports a provision whose text prints the label of the subsection an address names,
// "Stk. 3.", whether or not its subsections were read apart: a mangled base prints its "Stk. 3."
// inside one long line.
func (c *Content) printsStykke(a Address) bool {
	loc, ok := c.Find(Address{Recognised: true, Paragraph: a.Paragraph})
	if !ok || loc.Provision == nil {
		return false
	}
	return regexp.MustCompile(`\bStk\.\s*` + itoa(a.Stk) + `\s*\.`).MatchString(loc.Provision.Text())
}

// endsAlready reports a change of how a condition ends, "»plan.« til: »plan, eller«", that the
// unit already reads: the new ending is there and the old one is not.
func (l Located) endsAlready(old, wording string) bool {
	stem := strings.TrimRight(old, ".,; ")
	if stem == old || len([]rune(stem)) < 3 || !strings.HasPrefix(wording, stem) || len(wording) <= len(stem) {
		return false
	}
	text, _, ok := l.unitText()
	if !ok {
		return false
	}
	unit := Squeeze(text)
	return strings.Contains(unit, Squeeze(wording)) && !strings.Contains(unit, Squeeze(old))
}

// readsInsertedAlready reports a unit in which every occurrence of an insertion's anchor already
// has the new words beside it, as the change would put them: a second act making an insertion the
// first had made. New words shorter than twenty letters say too little to tell.
func (l Located) readsInsertedAlready(anchor, add string, before bool) bool {
	text, _, ok := l.unitText()
	if !ok {
		return false
	}
	words := strings.Trim(MatchText(QuoteStrip(add)), " ,.;")
	if len([]rune(words)) < 20 {
		return false
	}
	unit, a := MatchText(text), MatchText(anchor)
	joined := strings.Trim(MatchText(joinInsertion(anchor, QuoteStrip(add), before)), " ,.;")
	n := strings.Count(unit, a)
	return n > 0 && strings.Count(unit, joined) == n
}

// overtakenUnplaced settles an insertion whose place a change applied ahead of its day had already
// rewritten away. Lov nr. 525 of 2020 put a second sentence into each of aktivloven § 74 e, stk. 3,
// nr. 1-3, from 1 May 2020; lov nr. 701 of 2018 rewrote the whole subsection, without numbered
// conditions, from 1 September 2020. In the order the acts were signed the conditions were gone when
// the 2020 act came, and its sentences went to the end of the new subsection, where no
// consolidation prints them. Made on the provision as it stood before the rewrite, the insertion
// finds its place, and from the rewrite on nothing of it remains.
func overtakenUnplaced(c *Content, e Edit, effective time.Time, ahead []aheadEdit, out *Consolidation) bool {
	if e.Kind != EditInsertProvision || e.Address.Everywhere || e.Address.Paragraph == "" || e.Address.Stk == 0 {
		return false
	}
	if _, ok := c.Find(e.Address); ok {
		return false
	}
	day := effective
	if !e.InForce.IsZero() {
		day = e.InForce
	}
	for i := len(ahead) - 1; i >= 0; i-- {
		a := ahead[i]
		if !a.Edit.InForce.After(day) || !covers(a.Address, e.Address) {
			continue
		}
		scratch := &Content{Order: []Node{{Provision: func() *Provision { p := cloneProvision(*a.Before); return &p }()}}}
		if _, ok := scratch.Find(e.Address); !ok {
			continue
		}
		trial := &Consolidation{}
		scratch.applyOne(e, trial)
		if len(trial.Unresolved) > 0 || len(trial.Applied) == 0 || strings.Contains(trial.Applied[0].Note, "approximate") {
			continue
		}
		verb := "rewrote"
		if a.Edit.Kind == EditDeleteProvision {
			verb = "repealed"
		}
		out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: e.Address.String(),
			Note: fmt.Sprintf("found its place as the text stood until %s, when %s %s %s %s; nothing of it remains", a.Edit.InForce.Format("2. 1. 2006"), a.Source, strings.TrimSpace(a.Edit.Number), verb, a.Address)})
		return true
	}
	return false
}

// madeAlready settles a refused change another act has made already, word for word, in the same
// provision: two orders of June 2021 each moved the expiry in § 12, stk. 2, from »5. juli« to »1.
// august«, and the second found its words replaced by the very words it brings.
func madeAlready(e Edit, out *Consolidation, at int) bool {
	if len(out.Unresolved) == at+1 && e.Kind == EditDeleteProvision && e.Address.Paragraph != "" && e.Address.Sentence == 0 {
		// "§ 362, stk. 4, ophæves" by lov nr. 1606 of 2013, when lov nr. 595 of the same year had
		// repealed that subsection already: the unit is gone, and nothing is left to repeal.
		a := e.Address
		for i := len(out.Applied) - 1; i >= 0; i-- {
			d := out.Applied[i].Edit
			if d.Source == e.Source || d.Kind != EditDeleteProvision || d.Address.Sentence != 0 {
				continue
			}
			b := d.Address
			if b.Paragraph == a.Paragraph && b.Stk == a.Stk && b.StkTo == a.StkTo && b.Nr == a.Nr && b.Litra == a.Litra {
				out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: e.Address.String(),
					Note: fmt.Sprintf("%s %s repealed it already", d.Source, strings.TrimSpace(d.Number))})
				return true
			}
		}
		return false
	}
	if len(out.Unresolved) != at+1 || e.Kind != EditReplaceText || e.Address.Paragraph == "" {
		return false
	}
	old, new := MatchText(QuoteStrip(e.Old)), MatchText(QuoteStrip(e.New))
	if old == "" || new == "" || old == new {
		return false
	}
	for i := len(out.Applied) - 1; i >= 0; i-- {
		d := out.Applied[i].Edit
		if d.Source == e.Source || d.Kind != EditReplaceText || d.Address.Paragraph != e.Address.Paragraph || d.Address.Stk != e.Address.Stk {
			continue
		}
		if MatchText(QuoteStrip(d.Old)) == old && MatchText(QuoteStrip(d.New)) == new {
			out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: e.Address.String(),
				Note: fmt.Sprintf("%s %s made the same change already", d.Source, strings.TrimSpace(d.Number))})
			return true
		}
	}
	return false
}

// superseded carries out a refused change whose words another act had already replaced with words
// of its own, by a change that had not taken effect when this act was signed: both acts change the
// same words, and the later one's wording stands. Lov nr. 551 of May 2019 changed aktivloven § 74
// a, stk. 5, »§ 70 a« to »§ 117« from 2020; lov nr. 1555 of December 2019 changed »§ 70 a« to »§
// 118«, and the consolidation of 2021 prints "§ 118".
func (c *Content) superseded(e Edit, signed time.Time, out *Consolidation, at int) bool {
	unit := e.Address.Paragraph != "" || e.Address.Preamble || e.Address.Footnote
	if len(out.Unresolved) != at+1 || e.Kind != EditReplaceText || !unit {
		return false
	}
	old, new := MatchText(QuoteStrip(e.Old)), QuoteStrip(e.New)
	if old == "" || new == "" {
		return false
	}
	for i := len(out.Applied) - 1; i >= 0; i-- {
		d := out.Applied[i].Edit
		if d.Source == e.Source || d.Kind != EditReplaceText || d.Address.Paragraph != e.Address.Paragraph || d.Address.Stk != e.Address.Stk ||
			d.Address.Preamble != e.Address.Preamble || d.Address.Footnote != e.Address.Footnote {
			continue
		}
		if MatchText(QuoteStrip(d.Old)) != old {
			continue
		}
		// "I indledningen ændres »2008« til: »2010«" from an order of 2009, when the order of 2008
		// had made the year 2009: drafted against the text before it, and its wording stands.
		before := d.InForce.IsZero() || !d.InForce.After(signed)
		switch {
		case e.Address.Footnote:
			// "I fodnoten til bekendtgørelsens titel ændres »som senest ændret ved … direktiv
			// 2004/102/EF …« til: »… 2008/109/EF …«" after an order had made it 2008/64/EF.
			sp, has := c.titleNote()
			if !has || countMatches(SegmentsText(c.Title[sp.From:sp.To]), QuoteStrip(d.New)) != 1 {
				return false
			}
			if f, _, k := replaceIn(c.Title, sp, QuoteStrip(d.New), new, false); !f || k == 0 {
				return false
			}
		case e.Address.Preamble:
			if countMatches(SegmentsText(c.Preamble), QuoteStrip(d.New)) != 1 || ReplaceSegments(c.Preamble, QuoteStrip(d.New), new, true) != 1 {
				return false
			}
		default:
			loc, ok := c.Find(e.Address)
			if !ok {
				return false
			}
			segs, sp, ok := loc.looseUnit()
			if !ok || countMatches(SegmentsText(segs[sp.From:sp.To]), QuoteStrip(d.New)) != 1 {
				return false
			}
			if f, _ := loc.replaceSpan(c, QuoteStrip(d.New), new); !f {
				return false
			}
		}
		note := fmt.Sprintf("%s %s had changed the same words from %s, after this act was signed; this act's wording stands", d.Source, strings.TrimSpace(d.Number), d.InForce.Format("2. 1. 2006"))
		if before {
			note = fmt.Sprintf("drafted against the text before %s %s changed the same words; this act's wording stands", d.Source, strings.TrimSpace(d.Number))
		}
		out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: e.Address.String(), Note: note})
		return true
	}
	return false
}

// covers reports an address naming the same unit as another or one containing it.
func covers(outer, inner Address) bool {
	if outer.Paragraph != inner.Paragraph || outer.Heading || inner.Heading {
		return false
	}
	if outer.Stk != 0 && outer.Stk != inner.Stk {
		return false
	}
	if outer.Nr != 0 && (outer.Nr != inner.Nr || outer.NrSuffix != inner.NrSuffix) {
		return false
	}
	if outer.Litra != "" && outer.Litra != inner.Litra {
		return false
	}
	if outer.Sentence != 0 && (outer.Sentence != inner.Sentence || outer.SentenceTo != 0 || inner.SentenceTo != 0) {
		return false
	}
	return true
}

// inForceBy is the changes of an act that have taken effect by asOf.
func inForceBy(edits []Edit, asOf time.Time) []Edit {
	if asOf.IsZero() {
		return edits
	}
	for i, e := range edits {
		if e.InForce.After(asOf) {
			kept := append([]Edit(nil), edits[:i]...)
			for _, e := range edits[i+1:] {
				if !e.InForce.After(asOf) {
					kept = append(kept, e)
				}
			}
			return kept
		}
	}
	return edits
}

// applyOneOrDay carries out a change of an act that takes effect on the same day as an earlier one.
// What it cannot place in the numbering the earlier acts left, it tries in the day's numbering, at
// the one place it was refused: the acts were drafted side by side against the text before either.
func (c *Content) applyOneOrDay(e Edit, out *Consolidation) {
	first := &Consolidation{}
	c.applyOne(e, first)
	out.Applied = append(out.Applied, first.Applied...)
	out.NoOpEdits += first.NoOpEdits
	for _, u := range first.Unresolved {
		if c.retryCited(u, out) {
			continue
		}
		retry := &Consolidation{}
		c.swapDay()
		c.applyAt(u.Edit, retry)
		c.swapDay()
		if len(retry.Applied) > 0 && len(retry.Unresolved) == 0 {
			for _, a := range retry.Applied {
				if a.Note == "" {
					a.Note = "placed by the numbering before the acts taking effect the same day"
				}
				out.Applied = append(out.Applied, a)
			}
			out.NoOpEdits += retry.NoOpEdits
			continue
		}
		out.Unresolved = append(out.Unresolved, u)
	}
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
//
// A change may name several places: "I § 132, § 133, stk. 1, og § 136, stk. 3, udgår »X«". Its
// address used to be read as one, mixing the first § with a subsection belonging to another, so
// the change was refused or, worse, aimed at a unit it never named. It is carried out at each
// place in turn, and each place is applied or refused on its own.
func (c *Content) applyOne(e Edit, out *Consolidation) {
	e.NewStykker = splitAtStykkeLabels(e.NewStykker)
	if e.Kind == EditInsertProvision && len(e.NewStykker) > 0 && len(e.NewProse) == 0 && len(e.NewProvisions) == 0 &&
		reAfterProvisionProse.MatchString(e.Definition) && len(e.NewStykker[0]) > 0 && reProvisionLine.MatchString(Squeeze(SegmentsText(e.NewStykker[0][:min(2, len(e.NewStykker[0]))]))) {
		// "Efter § 3 indsættes i kapitel 1: § 3 a . Arrangører …" donated as a subsection: a
		// provision of its own, which went into § 3 as words.
		var lines []string
		for _, st := range e.NewStykker {
			for _, sg := range st {
				t := Squeeze(sg.Text)
				switch {
				case t == "":
				case len(lines) > 0 && strings.HasPrefix(t, ".") && reProvisionLabelOnly.MatchString(lines[len(lines)-1]):
					lines[len(lines)-1] += "." + strings.TrimPrefix(t, ".") // "§ 3 a" and "." printed apart
				default:
					lines = append(lines, t)
				}
			}
		}
		e.NewStykker, e.NewProse = nil, lines
	}
	if e.Kind == EditInsertProvision && e.Address.Nr > 0 && len(e.NewStykker) == 1 && len(e.NewProse) == 0 {
		// "I § 5, stk. 9, indsættes som nr. 3: 3) udgifterne …" donated as a subsection: it is the
		// condition the address numbers. Inserted as a subsection, it pushed stk. 10 to 11.
		text := Squeeze(SegmentsText(e.NewStykker[0]))
		if m := reItemLabel.FindStringSubmatch(text); m != nil && Atoi(m[2]) == e.Address.Nr {
			e.NewStykker, e.NewProse = nil, []string{text}
		}
	}
	if reLimitMerge.MatchString(Squeeze(e.Definition)) {
		c.mergeLimits(e, func(reason string) {
			out.Unresolved = append(out.Unresolved, UnresolvedEdit{Edit: e, Reason: reason})
		}, func(where, note string) {
			out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: where, Note: note})
		})
		return
	}
	if m := reQuotedStykkeRepeal.FindStringSubmatch(Squeeze(e.Definition)); m != nil {
		where, why := c.repealQuotedStykke(m[1], Atoi(m[2]), m[3], Atoi(m[4]))
		if why != "" {
			out.Unresolved = append(out.Unresolved, UnresolvedEdit{Edit: e, Reason: why})
		} else {
			out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: where})
		}
		return
	}
	if reContentsChange.MatchString(Squeeze(e.Prose)) {
		c.changeContents(e, func(reason string) {
			out.Unresolved = append(out.Unresolved, UnresolvedEdit{Edit: e, Reason: reason})
		}, func(where, note string) {
			out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: where, Note: note})
		})
		return
	}
	if reTableCells.MatchString(e.Definition) {
		handled := c.applyTableCells(e, func(reason string) {
			out.Unresolved = append(out.Unresolved, UnresolvedEdit{Edit: e, Reason: reason})
		}, func(where, note string) {
			out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: where, Note: note})
		})
		if !handled {
			out.Unresolved = append(out.Unresolved, UnresolvedEdit{Edit: e, Reason: unrecognisedReason(e.Definition)})
		}
		return
	}
	if resolved, ok, named := c.lastSentence(e); named {
		if !ok {
			out.Unresolved = append(out.Unresolved, UnresolvedEdit{Edit: e, Reason: fmt.Sprintf("the change names the last sentence of %s, which is not in the current text", e.Address)})
			return
		}
		e = resolved
	}
	if c.applyHeadingAndRepeals(e, out) || c.applyChapterLabel(e, out) || c.applyStykkeLabel(e, out) {
		return
	}
	if e.Kind == EditInsertProvision || e.Kind == EditReplaceProvision || e.Kind == EditDeleteProvision {
		// "I § 2 indsættes i stk. 1 efter »stk. 2«: »og 3«, og som stk. 3 indsættes: …" edits a
		// subsection's words and inserts a new one in one sentence. The words are carried out
		// first, at the place their part of the sentence names; the new text follows.
		if ops := parseOperations(e.Definition); len(ops) > 0 && !e.Address.Heading && !e.Address.Everywhere {
			c.applyOperations(e, ops, out)
			if e.Kind != EditDeleteProvision {
				e.Address = broughtPlace(e.Definition, e.Address)
			}
		}
	}
	switch e.Kind {
	case EditReplaceText, EditDeleteText, EditInsertText:
		// "I § 13 b, stk. 4, ændres »…« til: »…«, og før nr. 1 indsættes som nyt nummer:" is
		// addressed by what it names before its quotation; "nr. 1" is where the new item goes.
		if b := addressBeforeOperations(e.Definition); b.Paragraph != "" && b.Paragraph == e.Address.Paragraph && !e.Address.Heading {
			e.Address = b
		}
	}
	switch e.Kind {
	case EditReplaceText, EditDeleteText, EditInsertText, EditReplaceEverywhere, EditUnknown:
		if ops := parseOperations(e.Definition); len(ops) > 1 || len(ops) == 1 && (e.Kind == EditUnknown || ops[0].whole) {
			c.applyOperations(e, ops, out)
			return
		}
	}
	c.applyPlaces(e, out)
}

// countsPlaces reports a unit, the sentence aside, printing a change's old wording exactly as often
// as the count in lead says: "to steder".
func (c *Content) countsPlaces(e Edit, lead string) bool {
	m := reSeveralPlaces.FindString(lead)
	want := map[string]int{"to": 2, "tre": 3, "fire": 4, "fem": 5, "seks": 6, "syv": 7, "otte": 8, "ni": 9, "ti": 10}[strings.ToLower(strings.Fields(m)[0])]
	if want == 0 {
		want = Atoi(strings.Fields(m)[0])
	}
	a := e.Address
	a.Sentence, a.SentenceTo, a.SentenceList = 0, 0, false
	loc, ok := c.Find(a)
	if !ok || want == 0 {
		return false
	}
	segs, sp, ok := loc.looseUnit()
	return ok && countMatches(ownText(segs, sp), QuoteStrip(e.Old)) == want
}

// broughtPlace is where the new text of a change that also edits words goes: the clause after its
// last quotation names it relative to the change's address. "I § 17, stk. 5, ændres »A« til: »B«,
// og som 2. pkt. indsættes:" inserts the second sentence of stk. 5; "I § 2 indsættes i stk. 1 efter
// »stk. 2«: »og 3«, og som stk. 3 indsættes:" inserts stk. 3, not something in stk. 1.
func broughtPlace(definition string, a Address) Address {
	i := strings.LastIndexAny(definition, "«”")
	if i < 0 || !bringsAfterQuotes(definition) {
		return a
	}
	tail := definition[i:]
	switch {
	case rePunktum.MatchString(tail):
		m := rePunktum.FindStringSubmatch(tail)
		a.Sentence, a.SentenceTo, a.SentenceList, a.SentenceBecomes = Atoi(m[1]), Atoi(m[2]), false, false
	case reStk.MatchString(tail):
		a.Stk, a.StkTo, a.Nr, a.NrSuffix, a.Litra, a.Sentence, a.SentenceTo = Atoi(reStk.FindStringSubmatch(tail)[1]), 0, 0, "", "", 0, 0
	case reNr.MatchString(tail):
		a.Nr, a.NrSuffix, a.Litra, a.Sentence, a.SentenceTo = Atoi(reNr.FindStringSubmatch(tail)[1]), "", "", 0, 0
	}
	return a
}

// applyOperations carries out a change that asks for several edits. Each is carried out at the
// change's address, narrowed by any "i nr. 1" in front of it, and an everywhere-change whose list
// names a form the text does not contain has nothing to do for that form once another form was
// replaced: the drafters list every inflection, and a law need not use them all.
func (c *Content) applyOperations(e Edit, ops []operation, out *Consolidation) {
	base := e.Address
	if !base.Everywhere {
		base = addressBeforeOperations(e.Definition)
	}
	if base.Everywhere {
		// "»den systemansvarlige virksomhed« … til: »Energinet.dk«, og »den systemansvarlige
		// virksomheds« … til: »Energinet.dk’s«": the shorter form is inside the longer, and
		// replaced first it turned the genitive into "Energinet.dks" before the genitive's own
		// pair could find it. The longer forms go first.
		ops = append([]operation(nil), ops...)
		sort.SliceStable(ops, func(i, j int) bool {
			return len([]rune(QuoteStrip(ops[i].old))) > len([]rune(QuoteStrip(ops[j].old)))
		})
	}
	sub := &Consolidation{}
	running, narrowedOnce := base, false
	for _, op := range ops {
		if op.kind == EditDeleteProvision {
			// A repeal names its own units, relative to the change's address: "stk. 4" is a
			// subsection of the change's §, "§ 42 k, stk. 4" carries its own.
			text := op.place
			if !strings.Contains(text, "§") {
				text = Address{Recognised: true, Paragraph: base.Paragraph}.String() + ", " + text
				if base.Stk != 0 && !strings.Contains(strings.ToLower(op.place), "stk") {
					text = Address{Recognised: true, Paragraph: base.Paragraph, Stk: base.Stk}.String() + ", " + op.place
				}
			}
			one := e
			one.Kind, one.Old, one.New = EditDeleteProvision, "", ""
			one.Definition = text + " ophæves." // what the repeal names is its own place, not the sentence's
			places := ParseAddresses(text + " ophæves")
			if len(places) == 0 {
				places = itemRange(ParseAddress(text), text)
			}
			for _, a := range places {
				one.Address = a
				c.applyAt(one, sub)
			}
			continue
		}
		one := op.asEdit(e)
		if a, narrowed := op.narrow(running); narrowed {
			running, narrowedOnce = a, true
		}
		one.Address = running
		refused := len(sub.Unresolved)
		if narrowedOnce {
			c.applyAt(one, sub) // the sentence named this place itself
		} else {
			c.applyPlaces(one, sub)
		}
		if len(sub.Unresolved) > refused && one.Address.Sentence > 0 && reSeveralPlaces.MatchString(op.lead) && c.countsPlaces(one, op.lead) {
			// "I § 107, 2. pkt., ændres »A« til: »B«, og to steder ændres »C« til »D«": the second
			// clause counts its places in the provision, where C is printed exactly that often,
			// not in the sentence the first named.
			sub.Unresolved = sub.Unresolved[:refused]
			one.Address.Sentence, one.Address.SentenceTo, one.Address.SentenceList = 0, 0, false
			c.applyPlaces(one, sub)
		}
	}
	out.NoOpEdits += sub.NoOpEdits
	out.Applied = append(out.Applied, sub.Applied...)
	for _, u := range sub.Unresolved {
		if base.Everywhere && len(sub.Applied) > 0 && strings.Contains(u.Reason, "does not occur anywhere") {
			out.Applied = append(out.Applied, AppliedEdit{Edit: u.Edit, Where: "document",
				Note: "this form does not occur in the text; the change's other forms were replaced"})
			out.NoOpEdits++
			continue
		}
		out.Unresolved = append(out.Unresolved, u)
	}
}

// applyPlaces carries out one operation at every place its change names.
func (c *Content) applyPlaces(e Edit, out *Consolidation) {
	if e.Address.Heading && (e.Kind == EditReplaceText || e.Kind == EditDeleteText || e.Kind == EditInsertText) {
		// "I overskriften til kapitel 5 og i § 14 ændres …" names a heading and a provision; each
		// place is read on its own, so the heading's reading does not stick to § 14.
		if places := clausePlaces(e.Definition); len(places) > 1 {
			for _, a := range places {
				one := e
				one.Address = a
				c.applyAt(one, out)
			}
			return
		}
	}
	if e.Kind == EditDeleteProvision && !e.Address.Heading {
		if places := itemRange(e.Address, e.Definition); len(places) > 1 {
			for _, a := range places {
				one := e
				one.Address = a
				c.applyAt(one, out)
			}
			return
		}
	}
	switch e.Kind {
	case EditReplaceText, EditDeleteText, EditInsertText, EditDeleteProvision:
		if places := ParseAddresses(e.Definition); len(places) > 1 && !e.Address.Heading {
			for _, a := range places {
				one := e
				one.Address = a
				c.applyAt(one, out)
			}
			return
		}
	}
	c.applyAt(e, out)
}

// applyAt carries out a change at the one place its address names.
func (c *Content) applyAt(e Edit, out *Consolidation) {
	reject := func(reason string) {
		out.Unresolved = append(out.Unresolved, UnresolvedEdit{Edit: e, Reason: reason})
	}
	placedNote := ""
	apply := func(where, note string) {
		if placedNote != "" {
			note = strings.TrimSpace(placedNote + "; " + note)
		}
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
			if nw := strings.TrimSpace(QuoteStrip(e.New)); len([]rune(nw)) >= 8 && c.replaceEverywhere(nw, nw) > 0 {
				// "Overalt i loven ændres »økonomiministeren« til: »økonomi- og
				// erhvervsministeren«" in a text that names the minister so already.
				out.NoOpEdits++
				apply("document", "the text already reads as the change has it")
				return
			}
			if m := c.replaceInsideWords(old, QuoteStrip(e.New)); m > 0 {
				// "Overalt i bekendtgørelsen ændres »køkkenredskaber« til: »køkkenartikler«" in a
				// text that prints only "plastkøkkenredskaber" and "køkkenredskaberne".
				apply(fmt.Sprintf("document (%d occurrences)", m), "replaced everywhere in the act, inside the compounds and inflections that hold the word")
				return
			}
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
	if !ok && e.Address.Paragraph == "1" && e.Address.Stk == 0 && e.Address.Nr == 0 && len(c.Order) == 0 && len(c.Preamble) > 0 &&
		(e.Kind == EditReplaceText || e.Kind == EditInsertText || e.Kind == EditDeleteText) {
		// "Bekendtgørelse om ikrafttræden af …" is one unnumbered sentence, which the reader files
		// as the text before any provision; "I § 1 indsættes efter »1. juni 2005«" can mean nothing
		// else.
		loc, ok = Located{Preamble: true}, true
	}
	targets := []Located{}
	switch {
	case ok && e.Address.RangeTo != "":
		targets = c.rangeProvisions(e.Address)
		if len(targets) == 0 {
			reject(fmt.Sprintf("the range %s names no provision of this text", e.Address))
			return
		}
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
	if e.Kind == EditInsertProvision && e.Address.Bilag > 0 && e.Address.Paragraph == "" && reNewAnnexInline.MatchString(Squeeze(e.Definition)) {
		// "Som bilag 1 til loven indsættes: »Bilag 1 Konvention af 10. marts 1995 …«": a new annex,
		// its text in the change.
		n := e.Address.Bilag
		if c.hasAnnex(n, e.Address.BilagLitra) {
			reject("the law already has bilag " + itoa(n) + ", and the change adds one")
			return
		}
		segs := withoutOuterQuotes(e.blockText())
		if len(segs) == 0 {
			reject("the new annex carries no text")
			return
		}
		lines := make([]string, 0, len(segs))
		for _, sg := range segs {
			lines = append(lines, sg.Text)
		}
		ap := Appendix{Number: itoa(n), Label: "Bilag " + itoa(n), Segments: segs}
		if read := AnnexFromLines(lines, n, true); read != nil {
			ap = *read
		}
		c.setAppendix(n, e.Address.BilagLitra, ap, true)
		apply("bilag "+itoa(n), "")
		return
	}
	if e.Kind == EditDeleteProvision && e.Address.Bilag > 0 && reLastParagraphs.MatchString(Squeeze(e.Definition)) {
		c.deleteLastParagraphs(e, reject, apply)
		return
	}
	if e.Kind == EditReplaceProvision && reRulesFor.MatchString(Squeeze(e.Definition)) {
		c.replaceRulesFor(e, reject, apply)
		return
	}
	if e.Address.Continued {
		// The annex was named by the change before; this one's words say where in it. They are
		// read as if the sentence named the annex itself, and nothing is put at the annex's end
		// for want of them.
		def := "I bilag " + itoa(e.Address.Bilag) + ", " + lowerFirst(strings.TrimSpace(e.Definition))
		if part, isPart := ParseAnnexPart(def); isPart && (part.Label != "" || part.NewLabel != "" || labelledScopes(part.Scopes)) &&
			(e.Kind == EditInsertProvision || e.Kind == EditReplaceProvision || e.Kind == EditDeleteProvision) {
			c.applyAnnexPart(part, e, reject, apply)
			return
		}
		if e.Kind != EditReplaceText && e.Kind != EditDeleteText && e.Kind != EditInsertText || e.Kind == EditInsertText && InsertionAnchor(e) == "" {
			reject("the change names no place in bilag " + itoa(e.Address.Bilag) + ", which the change before it named, that this model reads")
			return
		}
	}
	if e.Kind == EditReplaceProvision || e.Kind == EditDeleteProvision || e.Kind == EditInsertProvision || e.Kind == EditUnknown {
		// "indsættes efter nr. 10, nr. 10.a, der affattes som bilag 3 til denne" is an insertion
		// whatever the verb its new part is described with.
		if part, isPart := ParseAnnexPart(e.Definition); isPart && len(AnnexPairs(e.Definition)) == 0 && (!part.Insert || e.Kind == EditInsertProvision || part.Source != 0) {
			c.applyAnnexPart(part, e, reject, apply)
			return
		}
		if part, isPart := ParseAnnexPartRemoving(e.Definition, e.blockText()); isPart {
			c.applyAnnexPart(part, e, reject, apply)
			return
		}
	}
	if e.NewAppendix != nil && e.Address.Bilag > 0 && e.Address.Paragraph == "" &&
		(e.Kind == EditReplaceProvision || e.Kind == EditInsertProvision) && len(AnnexPairs(e.Definition)) > 0 {
		// The act prints the whole new annex, so it can stand in for the law's annex whether or not
		// the mirror holds the old one: annexes are often published outside the register's XML.
		ap := cloneAppendix(*e.NewAppendix)
		if e.Kind == EditInsertProvision {
			for _, m := range reAnnexRenumber.FindAllStringSubmatch(e.Definition+" "+e.Prose, -1) {
				c.renumberAnnex(Atoi(m[1]), Atoi(m[2])) // "Bilag 1 bliver herefter bilag 5", once
			}
			ok = c.hasAnnex(e.Address.Bilag, e.Address.BilagLitra)
		}
		replaced := c.setAppendix(e.Address.Bilag, e.Address.BilagLitra, ap, e.Kind == EditInsertProvision)
		switch {
		case e.Kind == EditInsertProvision && !ok:
			out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: e.Address.String()})
		case e.Kind == EditInsertProvision:
			reject("the law already has bilag " + itoa(e.Address.Bilag) + ", and the change adds one")
		case replaced:
			out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: "bilag " + itoa(e.Address.Bilag)})
		default:
			out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: "bilag " + itoa(e.Address.Bilag),
				Note: "the mirror did not hold the annex this replaces; the act's new annex stands in its place"})
		}
		return
	}
	if !ok && e.Kind == EditDeleteProvision && e.Address.Bilag > 0 && e.Address.Paragraph == "" && namesWholeUnit(e.Definition) {
		out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: "bilag " + itoa(e.Address.Bilag),
			Note: "the mirror never held this annex, so there was nothing to remove"})
		out.NoOpEdits++
		return
	}
	if !ok && (e.Kind == EditReplaceText || e.Kind == EditDeleteText || e.Kind == EditInsertText) && e.Address.Paragraph != "" {
		// The address names a subsection, item or sentence this text does not resolve, but the
		// provision is there and the passage the change quotes occurs in it exactly once: the
		// passage places the change where the address cannot.
		if p, found := c.Find(Address{Recognised: true, Paragraph: e.Address.Paragraph, EntryIntoForce: e.Address.EntryIntoForce}); found && p.Provision != nil {
			passage := QuoteStrip(e.Old)
			if e.Kind == EditInsertText {
				passage, _ = insertionAnchorText(e)
			}
			if passage != "" && strings.Count(MatchText(p.Provision.Text()), MatchText(passage)) == 1 {
				loc, ok, targets = p, true, []Located{p}
				placedNote = fmt.Sprintf("%s is not in this text; the quoted passage occurs once in § %s and placed the change", e.Address, p.Provision.Address())
			}
		}
	}
	if !ok && e.Address.Heading && e.Address.Afsnit != 0 && e.Address.Paragraph == "" && e.Kind == EditReplaceProvision {
		// "Overskriften til afsnit III affattes således: »III. Fælles bestemmelser«" where the text
		// prints the afsnit only as its heading, "III. Administrative bestemmelser …", between two
		// chapters: the one line opening with the numeral is the heading.
		if c.replaceLooseAfsnitHeading(e, apply) {
			return
		}
	}
	if !ok && e.Address.Preamble && e.Kind == EditReplaceProvision && len(c.Preamble) == 0 {
		// "Indledningen affattes således" says what the preamble reads from now on; a text the
		// mirror holds without one gets it.
		if segs := e.blockText(); len(segs) > 0 {
			c.Preamble = segs
			apply("indledning", "the mirror's text carried no preamble; the one the change gives it is set")
			return
		}
	}
	if !ok && e.Kind == EditDeleteProvision && c.carried && e.Address.Paragraph != "" && e.Address.Sentence == 0 {
		// "§ 3, stk. 4, ophæves" by an act the base text may carry, which prints § 3 without a stk.
		// 4: the base made the repeal already.
		if p, found := c.Find(Address{Recognised: true, Paragraph: e.Address.Paragraph}); found && p.Provision != nil {
			out.NoOpEdits++
			apply(e.Address.String(), "the text already reads as the change has it; the unit is not printed")
			return
		}
	}
	if !ok && c.rewritesNextCondition(e, out) {
		return
	}
	if !ok && c.rewritesNeighboursSubsection(e, out) {
		return
	}
	if !ok && e.Kind != EditInsertProvision && e.Kind != EditRenumber {
		out.Unresolved = append(out.Unresolved, UnresolvedEdit{
			Edit:   e,
			Reason: fmt.Sprintf("address %q not found in the current text", e.Address),
		})
		return
	}

	if ok && e.Address.Heading && loc.Provision != nil && reHeadingAfter.MatchString(e.Definition) {
		// "Overskriften efter § 145" is the heading over the provision that follows § 145.
		if next := c.nextProvision(loc.Provision); next != nil {
			loc = Located{Provision: next}
			targets = []Located{loc}
		}
	}
	if ok && loc.Appendix != nil && e.Address.Paragraph != "" && len(loc.Appendix.Order) == 0 {
		// "I bilag 1 affattes § 11, stk. 1, således": statutes printed as an annex's text.
		if c.applyInAnnexProvision(e, loc.Appendix, out, reject) {
			return
		}
	}
	if ok && loc.Footnote {
		c.applyFootnote(e, reject, apply)
		return
	}
	if ok && e.Address.Heading && (e.Kind == EditReplaceText || e.Kind == EditDeleteText || e.Kind == EditInsertText) {
		c.applyHeadingText(e, loc, reject, apply)
		return
	}
	if c.applyUnit(e, loc, ok, reject, apply) {
		return
	}
	if e.Kind == EditInsertText && len(e.NewProse) == 0 && QuoteStrip(e.New) != "" && InsertionAnchor(e) == "" && e.Address.Bilag == 0 && namesNewUnit(e) {
		// "I § 1, stk. 1, indsættes efter 1. pkt. som nyt punktum: »…«" and "indsættes efter nr. 9
		// som nyt nummer: »10) …«": a sentence or a condition given in quotation marks, placed as
		// one given as a line; added to the end of the unit, it was out of place.
		e.Kind, e.NewProse = EditInsertProvision, []string{QuoteStrip(e.New)}
	}

	if ok && e.Address.RangeTo == "" && c.applySentence(e, loc, reject, apply) {
		return
	}

	// "I § 9, stk. 1, affattes skemaet for GRUNDFAG således" names a table inside a subsection, and
	// a sentence that could not be resolved is no better. The address stops at the subsection,
	// so carrying the change out would replace the whole subsection with one sentence of it.
	if (e.Kind == EditReplaceProvision || e.Kind == EditDeleteProvision) && !e.Address.Heading && namesPartOfUnit(e.Definition) &&
		!partNamedElsewhere(e.Definition, e.Address) {
		reject("the change names a sentence or a table inside the provision, which this model does not address")
		return
	}

	if e.Address.Bilag > 0 && (e.Kind == EditReplaceText || e.Kind == EditDeleteText || e.Kind == EditInsertText) {
		// "I bilag 1, pkt. 5 og 6, udgår »I henhold til § 13 …«": the annex prints the words in
		// every one of its eight model texts, and the first was taken, pkt. 1's.
		if labels := annexNamedParts(e.Definition); len(labels) > 0 {
			for i := range targets {
				ap := targets[i].Appendix
				if ap == nil || len(ap.Order) > 0 {
					continue
				}
				var spans []Span
				for _, l := range labels {
					sp, found := findPart(ap.Segments, Span{From: 0, To: len(ap.Segments)}, l)
					if !found {
						sp, found = labelLineRun(ap.Segments, l)
					}
					if !found {
						spans = nil
						break
					}
					spans = append(spans, sp)
				}
				if spans == nil {
					// The parts are not printed as labelled lines: the words place the change only
					// where the annex prints them once.
					passage := QuoteStrip(e.Old)
					if e.Kind == EditInsertText {
						passage, _ = insertionAnchorText(e)
					}
					if passage == "" || strings.Count(MatchText(SegmentsText(ap.Segments)), MatchText(passage)) != 1 {
						reject(fmt.Sprintf("bilag %d does not print pkt. %s, which the change names, at the head of exactly one line, and the words occur more than once", e.Address.Bilag, strings.Join(labels, " og ")))
						return
					}
					continue
				}
				targets[i].AnnexSpans = spans
			}
		}
	}

	switch e.Kind {
	case EditReplaceText:
		old := QuoteStrip(e.Old)
		if old == "" {
			reject("no quoted passage to replace")
			return
		}
		for _, t := range targets {
			found, changed := t.replaceInSentence(c, e.Address, old, e.New, reSeveralPlaces.MatchString(e.Definition))
			note := ""
			if !found && t.endsAlready(old, e.New) {
				// "I § 69, stk. 3, nr. 4, ændres »plan.« til: »plan, eller«" where an earlier act
				// made the very change: read loosely, "plan," became "plan, eller, eller".
				out.NoOpEdits++
				apply(t.String(), "the text already reads as the change has it")
				continue
			}
			if !found && !t.alreadyReads(e.Address, e.New) {
				// "I § 6, stk. 3, og § 8, stk. 2, ændres »Ansøger« til: »Ansøger eller
				// projektoperatør«": the act quotes the word as § 6 begins a sentence with it, and
				// § 8 has it mid-sentence. The words are the same; only the initial differs.
				if f, ch := t.replaceInSentence(c, e.Address, flipInitial(old), flipInitial(e.New), reSeveralPlaces.MatchString(e.Definition)); f {
					found, changed, note = f, ch, "the text prints the passage with "+initialCase(flipInitial(old))+" initial"
				} else if f, ch := t.replaceLoosely(old, e.New); f {
					found, changed, note = f, ch, "the text prints the passage with different spacing or punctuation; it occurs once"
				}
			}
			if !found {
				if text, _, ok := t.unitText(); ok {
					if printed, ok := transposedDigits(text, old); ok {
						// "I § 6, stk. 1, ændres »jf. § 135, stk. 2, i lov om social service«" where
						// stk. 1 prints "§ 153", and the act's next change quotes "§ 153" right.
						if f, ch := t.replaceInSentence(c, e.Address, printed, e.New, false); f {
							found, changed, note = f, ch, fmt.Sprintf("the text prints the passage as %q, the same digits in another order", printed)
						}
					}
				}
			}
			if !found && t.Provision != nil && len(strings.Fields(old)) >= 20 {
				// "»efter §§ 7-9, 9 b-9 e eller 9 m, … efter stk. 2, nr. 1-9 eller 11-14, afvises,«"
				// where the text prints "nr. 1-14": a long passage the change replaces whole, the
				// one in the unit opening and closing with its words.
				if text, _, ok := t.unitText(); ok {
					if printed, ok := anchoredPrinted(text, old); ok {
						if f, ch := t.replaceInSentence(c, e.Address, printed, e.New, false); f {
							found, changed, note = f, ch, "the text prints the passage with a few words otherwise; it is the one passage opening and closing as the quotation does"
						}
					}
				}
			}
			if !found && t.Appendix != nil && t.Provision == nil {
				// An annex quoting a provision prints it with a slip of its own: "af udenlandsk
				// tegnende forsikringsselskaber" for the act's "af udenlandske direkte tegnende
				// forsikringsselskaber". A long quotation is the one passage opening and closing
				// with its words.
				if printed, ok := anchoredPrinted(annexScopeText(t), old); ok {
					if f, ch := t.replaceSpan(c, printed, e.New); f {
						found, changed, note = f, ch, "the annex prints the passage with a few words otherwise; it is the one passage opening and closing as the quotation does"
					}
				}
			}
			if !found {
				if variant, ok := t.numberSpelledOtherwise(old); ok {
					// "I … to steder i § 13, stk. 10, ændres »i sammenlagt to år« til: …" where the
					// text prints "i sammenlagt 2 år": the number written in figures.
					if f, ch := t.replaceInSentence(c, e.Address, variant, e.New, reSeveralPlaces.MatchString(e.Definition)); f {
						found, changed, note = f, ch, fmt.Sprintf("the text prints the passage as %q", variant)
					}
				}
			}
			if !found {
				if numOld, numNew, ok := t.citationInList(old, e.New); ok {
					// "I § 59, stk. 5, 1. og 2. pkt., ændres »§ 57 a« til: »§ 57 b«" where the text
					// cites "§§ 56 c, 56 e og 57 a": the number in the list is the one quoted.
					if f, ch := t.replaceInSentence(c, e.Address, numOld, numNew, true); f {
						found, changed, note = f, ch, "the text cites the provision in a list of §§"
					}
				}
			}
			if !found {
				if printed, ok := c.citationOfMissingStk(t, old); ok {
					// "I § 70, stk. 3, ændres »§ 140, stk. 8«, til: »§ 54, stk. 1«" where § 140 has
					// six subsections and the unit cites "§ 140, stk. 6": a subsection that does not
					// exist, and the one citation of the provision the unit holds.
					if f, ch := t.replaceInSentence(c, e.Address, printed, e.New, false); f {
						found, changed, note = f, ch, fmt.Sprintf("the act cites a subsection the provision does not have; the unit cites it once, as %q", printed)
					}
				}
			}
			if !found && t.Item != nil {
				if f, ch := t.joinListEnding(old, e.New); f {
					// "I § 47 c, stk. 3, nr. 7, ændres »lov om halvlederprodukters udformning
					// (topografi)« til: »… (topografi) og«" for the condition ending "lov om
					// beskyttelse af halvlederprodukters udformning (topografi).": the list's
					// connective goes where the condition ends.
					found, changed, note = f, ch, "the condition ends as the quotation does; the connective is put at its end"
				}
			}
			if !found {
				if printed, ok := t.ministerUnderOtherTitle(old, reSeveralPlaces.MatchString(e.Definition)); ok {
					// "I § 13, stk. 1, nr. 3, ændres »velfærdsministeren« til: »Valgnævnet«": the
					// minister was renamed by royal resolution, which no act in the chain records,
					// and the text still names "indenrigs- og sundhedsministeren".
					if f, ch := t.replaceInSentence(c, e.Address, printed, e.New, reSeveralPlaces.MatchString(e.Definition)); f {
						found, changed, note = f, ch, fmt.Sprintf("the text names the minister by an earlier title, %q", printed)
					}
				}
			}
			if !found && (t.alreadyReads(e.Address, e.New) || (c.carried || longWording(e.New)) && t.provisionReads(e.New, old) || t.extendedAlready(old, e.New)) {
				// The old wording is gone and the new is where the change puts it: the text this
				// chain started from already carries the change, as a consolidated base often does.
				out.NoOpEdits++
				apply(t.String(), "the text already reads as the change has it")
				continue
			}
			if !found {
				if sib, ok := t.siblingHolding(old, e.Address); ok {
					if f, ch := sib.replaceSpan(c, old, e.New); f {
						found, changed, note = f, ch, fmt.Sprintf("the passage is not in %s; it occurs once in the provision, in %s, which the act's numbering names", t, sib)
						t = sib
					}
				} else if sib, ok := c.citationOnceInLaw(t, old, e.Address); ok {
					// "I § 3, stk. 2, ændres »§ 70 f, stk. 5« til: »§ 124«" where stk. 3 prints the
					// citation, the only place in the law that does.
					if f, ch := sib.replaceSpan(c, old, e.New); f {
						found, changed, note = f, ch, fmt.Sprintf("the passage is not in %s; the law prints it once, in %s, next to it", t, sib)
						t = sib
					}
				}
			}
			if !found {
				reject(fmt.Sprintf("quoted passage not present in %s", t))
				return
			}
			if !changed {
				out.NoOpEdits++
			}
			apply(t.String(), note)
		}

	case EditDeleteText:
		old := QuoteStrip(e.Old)
		for _, t := range targets {
			found, changed := t.replaceInSentence(c, e.Address, old, "", reSeveralPlaces.MatchString(e.Definition))
			note := ""
			if !found {
				if f, ch := t.replaceInSentence(c, e.Address, flipInitial(old), "", reSeveralPlaces.MatchString(e.Definition)); f {
					found, changed, note = f, ch, "the text prints the passage with "+initialCase(flipInitial(old))+" initial"
				} else if f, ch := t.replaceLoosely(old, ""); f {
					found, changed, note = f, ch, "the text prints the passage with different spacing or punctuation; it occurs once"
				}
			}
			if !found && t.Appendix != nil && len(t.AnnexSpans) > 0 && strings.TrimRight(old, ",; ") != old && cellsAre(t, strings.TrimRight(old, ",; ")) {
				// »§ 13, nr. 1 (E),« in a list of passages to delete, where the named parts print
				// "§ 13, nr. 1 (E)" as a cell of its own: the comma is the list's.
				if f, ch := t.replaceSpan(c, strings.TrimRight(old, ",; "), ""); f {
					found, changed, note = f, ch, "the annex prints the words as a cell of their own, without the comma the act's list gives them"
				}
			}
			if !found {
				if sib, ok := t.siblingHolding(old, e.Address); ok {
					if f, ch := sib.replaceSpan(c, old, ""); f {
						found, changed, note = f, ch, fmt.Sprintf("the passage is not in %s; it occurs once in the provision, in %s, which the act's numbering names", t, sib)
						t = sib
					}
				}
			}
			if !found && t.Provision != nil && len([]rune(strings.TrimSpace(old))) >= 3 && !strings.Contains(MatchText(t.Provision.Text()), MatchText(old)) {
				// The words a deletion removes are nowhere in the provision: it reads as the change
				// leaves it. An act the base partly carries deleted them already; lov nr. 345 of 2009
				// deletes "m.v." and ", jf. dog § 19, stk. 2, 2. pkt." from garantifondsloven §§ 18
				// and 22, which print neither, and the consolidation after it reads as ours.
				out.NoOpEdits++
				note := "the text already reads as the change has it"
				if !c.carried {
					note = "the words the change removes are not in the provision; it reads as the change leaves it"
				}
				apply(t.String(), note)
				continue
			}
			if !found {
				reject(fmt.Sprintf("passage to delete not present in %s", t))
				return
			}
			if !changed {
				out.NoOpEdits++
			}
			apply(t.String(), note)
		}

	case EditInsertText:
		// "I § 5 indsættes efter »X«: »Y«" names the words the new ones go by. Appending at the
		// end of the provision instead put them in the wrong sentence and reported success.
		anchor, before := insertionAnchorText(e)
		for _, t := range targets {
			if anchor != "" {
				if c.carried && t.Provision != nil && strings.Contains(MatchText(t.Provision.Text()), MatchText(joinInsertion(anchor, QuoteStrip(e.New), before))) {
					// An act the base text carries: the new words stand by the anchor already, and
					// inserting them again would print them twice.
					out.NoOpEdits++
					apply(t.String(), "the text already reads as the change has it")
					continue
				}
				if t.readsInsertedAlready(anchor, e.New, before) {
					// Lov nr. 525 of 2020 put "tilbud efter kapitel 5 i lov om kompensation …" after
					// "tilbud efter lov om en aktiv beskæftigelsesindsats" in aktivloven § 69, stk. 3,
					// nr. 1, where lov nr. 551 of 2019 had put the very words: made again, they
					// printed twice in a row.
					out.NoOpEdits++
					apply(t.String(), "the text already reads as the change has it; an earlier act made the same insertion")
					continue
				}
				found, _ := t.replaceInSentence(c, e.Address, anchor, joinInsertion(anchor, e.New, before), false)
				if !found {
					// "I lovens titel indsættes efter »Lov om danskuddannelse …«" where the title
					// reads "Bekendtgørelse af lov om danskuddannelse …".
					flipped := flipInitial(anchor)
					found, _ = t.replaceInSentence(c, e.Address, flipped, joinInsertion(flipped, e.New, before), false)
				}
				if !found {
					found = t.insertLoosely(anchor, e.New, before)
				}
				if !found && t.Provision != nil && len([]rune(QuoteStrip(e.New))) >= 3 &&
					(c.carried && strings.Contains(MatchText(t.Provision.Text()), MatchText(QuoteStrip(e.New))) ||
						longWording(e.New) && t.unitHolds(e.New)) {
					// The words are there and the passage they go by is not: an act the base text
					// carries, or one of the same day that made the change in other words around it.
					out.NoOpEdits++
					apply(t.String(), "the text already reads as the change has it")
					continue
				}
				if !found {
					reject(fmt.Sprintf("the passage the insertion is placed by is not present in %s", t))
					return
				}
				apply(t.String(), "")
				continue
			}
			if t.Appendix != nil {
				if where, ok := insertAnnexSentence(t.Appendix, e.Definition, QuoteStrip(e.New)); ok {
					apply(t.String()+", "+where, "inserted after the sentence the change names")
					continue
				}
				if where, ok := insertAnnexLetter(t.Appendix, e.Definition, QuoteStrip(e.New)); ok {
					apply(t.String()+", "+where, "inserted by the letter the change names; the letters after it move up")
					continue
				}
				if m := reAnnexAfterNumber.FindStringSubmatch(e.Definition); m != nil {
					if where, ok := insertNumberedRow(t.Appendix, m[1], QuoteStrip(e.New)); ok {
						apply(t.String()+", "+where, "inserted as the row after the number the change names")
						continue
					}
				}
			}
			if !t.append(c, e.New) {
				reject(fmt.Sprintf("could not append text to %s", t))
				return
			}
			apply(t.String(), "insertion point within the provision is approximate; the source states it in prose")
		}

	case EditReplaceProvision:
		if e.Address.Heading && reHeadingAndProvision.MatchString(e.Definition) {
			// "Overskriften før § 7 og § 7 ophæves, og i stedet indsættes: Erstatningstest … § 7.
			// …" replaces the provision and its heading together: the new text brings both.
			e.Address.Heading = false
			c.applyAt(e, out)
			return
		}
		if e.Address.Heading && len(targets) == 1 && targets[0].Part != nil && len(e.NewParts) == 1 && len(e.NewParts[0].provisions(nil)) > 0 {
			c.replaceChapterHeadingWith(e, targets[0], reject, apply)
			return
		}
		// A heading rewrite arrives as prose only, so it has to be taken before the "carries no
		// new text" guard, which would otherwise refuse every "overskriften … ændres til: »X«".
		if e.Address.Heading {
			for _, t := range targets {
				if !c.SetHeadingChange(t, e.New) {
					// "Overskriften før § 42 affattes således: …" says what the heading reads; a
					// provision the mirror holds without one gets it.
					if t.Provision == nil || strings.TrimSpace(e.New) == "" || len([]rune(e.New)) > 200 || strings.Contains(e.New, "§") {
						reject(fmt.Sprintf("no heading found for %s", t))
						return
					}
					t.Provision.SetHeading(e.New)
					apply(t.String(), "the mirror held no heading here; the one the change gives is set")
					continue
				}
				apply(t.String(), "replaced the heading only; the provision text is untouched")
			}
			return
		}
		if whole := c.wholeProvisions(e, targets); len(whole) > 1 || len(whole) == 1 && len(e.NewParts) > 0 {
			c.replaceProvisions(e, whole, reject, apply)
			return
		}
		// A replacement aimed at a subsection changes that subsection, not the provision around
		// it, and the register delivers the new wording as stykker or as lines rather than as a
		// paragraph. The shape decides where the text goes.
		for _, t := range targets {
			if t.Provision == nil && t.Appendix != nil {
				if m := reAnnexSectionPart.FindStringSubmatch(e.Definition); m != nil {
					c.replaceInAnnexSection(e, t, m, reject, apply)
					continue
				}
			}
			if t.Provision == nil {
				reject(fmt.Sprintf("cannot replace the content of %s", t))
				return
			}
			if t.Item != nil {
				// "nr. 4 affattes således: 4) …" rewrites one condition. Replacing the
				// subsection it sits in instead deleted the introduction and every other
				// condition, and reported the change as applied.
				segs := firstDonatedSegments(e)
				if len(segs) == 0 {
					reject("item replacement carries no new text")
					return
				}
				others := c.otherNamedItems(e, t)
				if !t.Provision.ReplaceItemContent(t.Item, segs) {
					reject(fmt.Sprintf("could not replace %s", t))
					return
				}
				// "nr. 2 og 3 ophæves, og i stedet indsættes: 2) … 3) … 4) …" replaces both: the
				// new conditions take the place of the first, and the second goes.
				for _, nr := range others {
					a := e.Address
					a.Nr, a.NrSuffix, a.Litra, a.Sentence, a.SentenceTo = nr, "", "", 0, 0
					if loc, found := c.Find(a); found && loc.Item != nil && loc.Stykke != nil && loc.Item.Was == nr {
						loc.Provision.DeleteItem(loc.Stykke.Nr, loc.Item.Nr)
					}
				}
				apply(t.String(), "")
				continue
			}
			if t.Stykke != nil {
				segs := firstDonatedSegments(e)
				if len(segs) == 0 {
					reject("subsection replacement carries no new text")
					return
				}
				// "§ 6, stk. 1-3, affattes således" replaces the whole range: the subsections after
				// the first one named go, and the new text brings its own. Replacing only stk. 1
				// left the old stk. 2 and 3 standing beside the new ones.
				for n := e.Address.StkTo; n > e.Address.Stk; n-- {
					if st := t.Provision.stykke(n, c.pinned); st == nil || !t.Provision.DeleteStykke(st.Nr) {
						reject(fmt.Sprintf("%s names stk. %d, which is not in the current text", t, n))
						return
					}
				}
				// "§ 3, stk. 3 og 4, ophæves, og i stedet indsættes" replaces both as well. The second
				// new subsection takes the second's place, so it keeps the number the act knows it by,
				// for a later change of the same act naming the pair again.
				extra := restDonatedStykker(e)
				if n := otherNamedStykke(e, t); n > 0 && (len(extra) == 0 || t.Provision.stykke(n, c.pinned) != nil || t.Stykke.Nr != len(t.Provision.Stykker)) {
					// Naming a second subsection the text ends before, with a new text for it, the
					// act's result is the same whether it was there: it is added after the first.
					st := t.Provision.stykke(n, c.pinned)
					if st == nil {
						reject(fmt.Sprintf("%s names stk. %d, which is not in the current text", t, n))
						return
					}
					if len(extra) == 0 {
						if !t.Provision.DeleteStykke(st.Nr) {
							reject(fmt.Sprintf("could not repeal stk. %d of %s", n, t))
							return
						}
					} else {
						if !t.Provision.ReplaceSpanContent(st.Span, extra[0]) {
							reject(fmt.Sprintf("could not replace stk. %d of %s", n, t))
							return
						}
						if len(extra) > 1 && !t.Provision.InsertStykkerAfter(st.Nr, extra[1:]) {
							reject(fmt.Sprintf("could not add the subsections following stk. %d of %s", n, t))
							return
						}
						extra = nil
					}
				}
				segs = keepMarker(t.Provision, t.Stykke, segs)
				if !t.Provision.ReplaceSpanContent(t.Stykke.Span, segs) {
					reject(fmt.Sprintf("could not replace %s", t))
					return
				}
				// "Stk. 1 ændres til" followed by two new subsections is one change: the second
				// belongs where the first one landed, and the numbering after it moves.
				if len(extra) > 0 {
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
			if len(e.NewProvisions) > 1 {
				for _, extra := range e.NewProvisions[1:] {
					d := cloneProvision(*extra)
					if !c.InsertProvisionAfter(prev, &d) {
						break
					}
					prev = &d
				}
			}
			apply(t.String(), note)
		}

	case EditDeleteProvision:
		for _, t := range targets {
			if e.Address.Heading {
				// "Overskriften før § 3 ophæves" removes the heading, not the provision under it.
				if !c.SetHeadingChange(t, "") {
					if pt := c.partOpenedBy(t.Provision); pt != nil && reHeadingBefore.MatchString(e.Definition) {
						// "Overskriften før § 145 ophæves" where § 145 opens kapitel 8, whose title
						// "Udarbejdelse af halvårsrapport" is the heading printed before it: the
						// chapter keeps its number, and the consolidation of 2023 prints it so.
						pt.Heading = ""
						apply(t.String(), "the heading before it is the title of the "+pt.Printed+" it opens; the title is removed, the number stays")
						continue
					}
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
				// "ophæves stk. 2-4": the subsections after the first named go too, last first so
				// the numbers the act cites still find them.
				for n := e.Address.StkTo; n > e.Address.Stk; n-- {
					if st := t.Provision.stykke(n, c.pinned); st == nil || !t.Provision.DeleteStykke(st.Nr) {
						reject(fmt.Sprintf("%s names stk. %d, which is not in the current text", t, n))
						return
					}
				}
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
		if m := reNewHeading.FindStringSubmatch(e.Definition); m != nil && e.Address.Paragraph != "" && len(e.NewProvisions) == 0 && len(e.NewStykker) == 0 && len(e.NewParts) == 0 {
			// "Efter § 75 c indsættes som ny overskrift: Salg i problemramte afdelinger": a heading
			// between two provisions heads the one after it. Appended to § 75 c, it read as the
			// last words of § 75 c.
			c.insertHeading(e, strings.EqualFold(m[1], "efter"), reject, apply)
			return
		}
		// Where inserted text goes depends on the shape it arrives in, not on the word the
		// change list used. Measured over 90 amending acts, most "indsættes" instructions bring
		// a subsection for an existing provision (75 <Stk>), fewer bring a whole paragraph
		// (24 <Paragraf>), and some bring only lines of prose.
		if lines := itemLinesOf(e); lines != nil {
			// "I § 6, stk. 1, indsættes som litra j:" whose "j) …" the register delivers as a
			// subsection: it is a condition of stk. 1, and went in as a stk. 2 of its own.
			e.NewProse, e.NewStykker = lines, nil
		}
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
			if c.carried && host.printsStykkerLike(e.NewStykker) {
				// An act the base may carry, bringing udlændingeloven § 58 g, stk. 3, which the base
				// prints with "i medfør af" where the act wrote "i henhold til", a later act's
				// change: inserted again, the subsection stood twice.
				out.NoOpEdits++
				apply(hostLoc.String(), "the text already prints the subsection, in words a later change made")
				return
			}
			at, note := 0, "numbering after the insertion is positional"
			switch after, named := stykkePosition(e.Definition); {
			case named && after <= len(host.Stykker):
				// "efter stk. 2" and "som stk. 3" both say where the new subsection lands, and the
				// address alone does not: "I § 2 indsættes i stk. 1 …, og som stk. 3 indsættes"
				// names stk. 1 for the other half of the change.
				at = after
				if after == 0 {
					at = -1 // "som stk. 1", "før stk. 1": in front of the first
				}
				if after > 0 {
					at = host.Stykker[after-1].Nr
					// "efter stk. 4, som bliver stk. 3": the act's stk. 4 is the one it found,
					// which an earlier repeal in the same act has moved up.
					if st := host.stykke(after, c.pinned); st != nil && reAfterStykke.MatchString(blankQuoted(e.Definition)) {
						at = st.Nr
					}
				}
			case hostLoc.Stykke != nil:
				at = hostLoc.Stykke.Nr
			case e.Address.Stk > 0:
				at = len(host.Stykker)
				note = fmt.Sprintf("numbered from stk. %d as the change says; subsection numbering is positional", e.Address.Stk)
			}
			if m := reBeforeStykke.FindStringSubmatch(blankQuoted(e.Definition)); m != nil && len(e.NewStykker) > 0 && len(e.NewStykker[0]) > 0 {
				// "I § 17 indsættes før stk. 3 som nye stykker: Stk. 2. … Stk. 3. … Stk. 4. …": the
				// new subsections' labels say they go in front of stk. 2, and the act's later
				// "stk. 3, der bliver til stk. 6" holds either way; an order of 2022 names the old
				// stk. 2 "stk. 5".
				if l := reStkLabelLead.FindStringSubmatch(e.NewStykker[0][0].Text); l != nil {
					if n := Atoi(l[1]); n >= 2 && n < Atoi(m[1]) && n-1 <= len(host.Stykker) {
						at, note = host.Stykker[n-2].Nr, fmt.Sprintf("the new subsections' labels place them in front of stk. %d, not stk. %s", n, m[1])
					}
				}
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
			if ok && loc.Provision != nil && placedBefore(e.Definition) {
				// "Før § 21 indsættes" puts the new provision in front of the one it names.
				after, note = false, "inserted before the addressed provision"
			}
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
			if len(e.NewParts) > 0 {
				// "Efter § 10 indsættes før kapitel 6: § 10 a. … Kapitel 5 a …": the provisions
				// close the chapter they follow, and the chapters they bring open after them.
				parts := make([]*Part, 0, len(e.NewParts))
				for _, p := range e.NewParts {
					parts = append(parts, clonePart(p))
				}
				if !c.InsertParts(isProvision(prev), false, parts) {
					reject("could not place the chapter the change brings after its provisions")
					return
				}
			}
			apply(anchor.Address(), note)
			return
		}
		if m := reAfterProvisionProse.FindStringSubmatch(e.Definition); m != nil && len(e.NewProse) > 0 {
			// "Efter § 24 indsættes: Hastighedsbegrænsere / § 24 a. … / Stk. 2. …" printed as loose
			// lines: provisions of their own, which went into § 24 as words.
			if anchor, ok := c.Find(Address{Recognised: true, Paragraph: m[1] + strings.ToLower(m[2])}); ok && anchor.Provision != nil && anchor.Item == nil {
				if len(e.NewProse) == 1 && looksLikeHeading(e.NewProse[0]) {
					// "Efter § 26 a indsättes: Driftstilskud og deltagerbetaling": a heading over the
					// provision that follows.
					c.insertHeading(e, true, reject, apply)
					return
				}
				if provs := provisionsOfLines(e.NewProse); len(provs) > 0 {
					prev := anchor.Provision
					for _, p := range provs {
						if !c.InsertProvisionAfter(prev, p) {
							reject("could not insert the new provision")
							return
						}
						prev = p
					}
					apply(anchor.String(), "inserted as the provisions the new text numbers")
					return
				}
			}
		}
		if len(e.NewProse) > 0 {
			// Loose lines of new text need somewhere to land. When the address named a part
			// that is not there yet — "I § 2 indsættes som nr. 4" against a § 2 with three items
			// — the host is the provision itself. Without that, this branch had no targets,
			// applied nothing, and recorded nothing: the change vanished from the report.
			proseTargets := targets
			if len(proseTargets) == 0 && e.Address.Sentence > 0 && !e.Address.Heading && e.Address.Stk > 0 && loc.Provision == nil && c.lacksSentenceHost(e.Address) {
				// "I § 142, stk. 5, indsættes som 2. pkt." where § 142 prints no stk. 5: a sentence
				// goes after another sentence, and there is none to follow.
				reject(fmt.Sprintf("the new sentence goes into %s, which is not in the current text", e.Address))
				return
			}
			if len(proseTargets) == 0 {
				if hostLoc, ok := c.insertionHost(e.Address, loc); ok {
					proseTargets = []Located{hostLoc}
				}
			}
			if len(proseTargets) == 0 {
				reject(fmt.Sprintf("inserted text addresses %q, which is not in the current text", e.Address))
				return
			}
			if m := reStykkeProse.FindStringSubmatch(e.Definition); m != nil && len(proseTargets) == 1 && proseTargets[0].Provision != nil && proseTargets[0].Item == nil && e.Address.Nr == 0 {
				// "I § 23 indsættes efter stk. 1 som nyt stykke: Stk. 2 . …" printed as loose lines:
				// a subsection, which went into stk. 1 as words and left the numbers after it.
				t := proseTargets[0]
				after := Atoi(m[2])
				switch {
				case m[2] == "":
					after = Atoi(m[3]) - 1
				case strings.EqualFold(m[1], "før"):
					after--
				}
				if groups := stykkerOfLines(e.NewProse); len(groups) > 0 && (after == 0 || t.Provision.Stykke(after) != nil) && after <= len(t.Provision.Stykker) {
					nr := after
					if after == 0 {
						nr = -1 // in front of stk. 1
					}
					if t.Provision.InsertStykkerAfter(nr, groups) {
						apply(fmt.Sprintf("§ %s%s, stk. %d", t.Provision.Number, t.Provision.Litra, after+1), "inserted as the subsection the change numbers it")
						return
					}
				}
			}
			for _, t := range proseTargets {
				segs := make([]Segment, 0, len(e.NewProse))
				for _, line := range e.NewProse {
					segs = append(segs, splitInlineLetters(Segment{Text: " " + line, Break: true})...)
				}
				if t.Stykke == nil && t.Item == nil && t.Provision != nil && len(t.Provision.Stykker) == 1 && e.Address.Stk == 0 {
					// "I § 2 indsättes som nr. 4:" in a provision of one subsection names its
					// conditions; the new one went to the end whatever its number.
					t.Stykke = &t.Provision.Stykker[0]
				}
				if m := reAfterLitraNew.FindStringSubmatch(defHead(e.Definition)); m != nil && t.Item != nil && strings.EqualFold(t.Item.Litra, m[2]) && t.Provision != nil && t.Stykke != nil {
					// "I § 6, stk. 1, indsættes efter litra i som nyt litra: j) …": after litra i, and
					// the old j is k from now on; it went in after i and left two j's.
					host := t
					ok := false
					if strings.EqualFold(m[1], "før") {
						ok = host.InsertLinesBefore(segs)
					} else {
						ok = host.InsertLines(segs)
					}
					if !ok {
						reject(fmt.Sprintf("could not add text to %s", t))
						return
					}
					t.Provision.relabelLetters(t.Provision.stykkeAt(t.Stykke))
					apply(t.String(), "inserted by the letter the change names; the letters after it move up")
					continue
				}
				if m := reAsLitra.FindStringSubmatch(defHead(e.Definition)); m != nil && t.Item == nil && t.Provision != nil && t.Stykke != nil {
					// "I § 6, stk. 1, indsættes som litra j:" in a subsection of letters alone: after
					// its last letter, when that is i.
					st := t.Provision.stykkeAt(t.Stykke)
					var last *Item
					numbered := false
					for j := range st.Items {
						if st.Items[j].Litra == "" {
							numbered = true
						} else {
							last = &st.Items[j]
						}
					}
					want := strings.ToLower(m[1])
					if !numbered && last != nil && len(last.Litra) == 1 && len(want) == 1 && want[0] == strings.ToLower(last.Litra)[0]+1 {
						host := Located{Provision: t.Provision, Stykke: st, Item: last}
						if !host.InsertLines(segs) {
							reject(fmt.Sprintf("could not add text to %s", t))
							return
						}
						apply(t.String()+", litra "+want, "inserted after the subsection's last letter, as the letter the change gives it")
						continue
					}
				}
				if m := reAsLitra.FindStringSubmatch(defHead(e.Definition)); m != nil && t.Item != nil && t.Item.Litra == "" && t.Provision != nil && t.Stykke != nil {
					// "I § 10, nr. 2, indsættes som litra d:" goes after the condition's last letter;
					// it went to the end of the subsection.
					st := t.Provision.stykkeAt(t.Stykke)
					last, host := "", t
					for j := range st.Items {
						if &st.Items[j] != t.Item {
							continue
						}
						// Its letters are inside its text, or follow it up to the next number.
						for k := j + 1; k < len(st.Items) && st.Items[k].Litra != ""; k++ {
							last, host.Item = strings.ToLower(st.Items[k].Litra), &st.Items[k]
						}
					}
					want := strings.ToLower(m[1])
					if last == "" && want == "a" || last != "" && len(last) == 1 && len(want) == 1 && want[0] == last[0]+1 {
						if !host.InsertLines(segs) {
							reject(fmt.Sprintf("could not add text to %s", t))
							return
						}
						apply(t.String()+", litra "+want, "inserted after the condition's last letter, as the letter the change gives it")
						continue
					}
				}
				if m := reAfterNumberNew.FindStringSubmatch(defHead(e.Definition)); m != nil && t.Stykke != nil && t.Provision != nil {
					// "I § 52 a, stk. 1, indsættes efter nr. 10 som nyt nummer:" goes after nr. 10,
					// and the conditions after it move up; it went to the end of the subsection.
					before, after, n := strings.EqualFold(m[1], "før"), Atoi(m[2]), 0
					if m[3] != "" {
						// "efter nr. 1, der bliver nr. 2": the condition as the act's earlier
						// insertions left its printed number.
						after = Atoi(m[3])
					}
					var at *Item // by its printed number: conditions printed on one line are one item
					st := t.Provision.stykkeAt(t.Stykke)
					for j := range st.Items {
						if it := &st.Items[j]; it.Litra == "" && it.Suffix == "" && FirstNumber(it.Label) == itoa(after) {
							n++
							at = it
						}
					}
					if n == 0 && len(st.Items) == 0 && !before {
						// "Udgifterne omfatter 1) …, 2) …, … 12) …" in one paragraph: the new
						// condition goes in after the text of nr. 10, and the numbers after it move up.
						if where, ok := insertAfterInlineItem(t.Provision, st, after, segs); ok {
							apply(where, "inserted after the condition the change names, in a paragraph that prints its conditions inline; the conditions after it move up")
							continue
						}
					}
					if n != 1 {
						reject(fmt.Sprintf("%s does not print nr. %d once for the new condition to follow", t, after))
						return
					}
					host := Located{Provision: t.Provision, Stykke: st, Item: at}
					if before {
						// "indsættes før nr. 1 som nyt nummer": the new condition is nr. 1, the
						// old one nr. 2; it went in after nr. 1.
						if !host.InsertLinesBefore(segs) {
							reject(fmt.Sprintf("could not add text to %s", t))
							return
						}
						apply(host.String(), "inserted before the condition the change names; the conditions from it move up")
						continue
					}
					if !host.InsertLines(segs) {
						reject(fmt.Sprintf("could not add text to %s", t))
						return
					}
					apply(host.String(), "inserted after the condition the change names; the conditions after it move up")
					continue
				}
				leading := 0 // conditions the text prints already, ahead of the first new one
				if t.Stykke != nil && t.Provision != nil {
					var dropped int
					if segs, dropped, leading = withoutPrintedItems(t.Provision, t.Provision.stykkeAt(t.Stykke), segs); len(segs) == 0 {
						apply(t.String(), "the text already prints the conditions the change brings")
						continue
					} else if dropped > 0 {
						placedNote = joinNote(placedNote, fmt.Sprintf("%d of the conditions the change brings the text already prints", dropped))
					}
				}
				if reAsNewNumber.MatchString(defHead(e.Definition)) && t.Stykke != nil && t.Provision != nil && len(segs) > 0 {
					// "I § 10, stk. 1, indsættes som nyt nummer: 9) …": the number the new line
					// prints, after the last condition when it is the next.
					st := t.Provision.stykkeAt(t.Stykke)
					n := 0
					for _, it := range st.Items {
						if it.Litra == "" && it.Suffix == "" {
							n++
						}
					}
					if lm := reItemLabel.FindStringSubmatch(strings.TrimSpace(segs[0].Text)); lm != nil && n > 0 && Atoi(lm[2]) == n+1 {
						host := Located{Provision: t.Provision, Stykke: st, Item: lastItem(st)}
						if !host.InsertLines(segs) {
							reject(fmt.Sprintf("could not add text to %s", t))
							return
						}
						apply(host.String(), "inserted after the last condition, as the number the new text prints")
						continue
					}
				}
				if m := reAsNumber.FindStringSubmatch(defHead(e.Definition)); m != nil && t.Stykke != nil && t.Provision != nil && !reAfterNumber.MatchString(e.Definition) {
					// "indsættes som nr. 17" names the number the new condition takes once the act's
					// earlier changes are made, not a condition the act found: it goes in front of
					// the condition that holds that number now, or after the last when it is the
					// next number.
					want, n := Atoi(m[1])+leading, 0
					var at *Item
					st := t.Provision.stykkeAt(t.Stykke)
					if moved := listMovedTo(t.Provision, st, want); moved != nil {
						// "I § 69, stk. 2, indsættes som nr. 5" from an act that numbered the
						// subsections before another put a new stk. 2 in front of the list: the list
						// is stk. 3 now, and the condition went to the end of the new stk. 2.
						st = moved
						placedNote = joinNote(placedNote, "the act numbers the subsections as they stood before a subsection was put in front of the conditions")
					}
					for j := range st.Items {
						if it := &st.Items[j]; it.Litra == "" && it.Suffix == "" {
							n++
							if n == want {
								at = it
							}
						}
					}
					host := Located{Provision: t.Provision, Stykke: st}
					switch {
					case at != nil:
						host.Item = at
						if !host.InsertLinesBefore(segs) {
							reject(fmt.Sprintf("could not add text to %s", t))
							return
						}
						apply(host.String(), "inserted as the condition the change numbers it; the conditions after it move up")
						continue
					case want == n+1 && n > 0:
						host.Item = lastItem(st)
						if !host.InsertLines(segs) {
							reject(fmt.Sprintf("could not add text to %s", t))
							return
						}
						apply(host.String(), "inserted after the last condition, as the number the change gives it")
						continue
					}
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
		reject(unrecognisedReason(e.Definition))
	}
}

// stykkeAt is the provision's own subsection a location points at.
func (p *Provision) stykkeAt(st *Stykke) *Stykke {
	for i := range p.Stykker {
		if &p.Stykker[i] == st || p.Stykker[i].Span == st.Span {
			return &p.Stykker[i]
		}
	}
	return st
}

// lastItem is a subsection's last numbered condition, or nil.
func lastItem(st *Stykke) *Item {
	for j := len(st.Items) - 1; j >= 0; j-- {
		if st.Items[j].Litra == "" {
			return &st.Items[j]
		}
	}
	return nil
}

// reNewHeading is a heading inserted between provisions: "Efter § 75 c indsættes som ny
// overskrift:", "Før § 12 indsættes en ny overskrift:".
var reNewHeading = regexp.MustCompile(`(?i)^\s*(efter|før|foran)\s+§\s*[0-9]+(?:\s*[a-zæøå])?\s+indsættes\s+(?:som\s+)?(?:en\s+)?(?:ny\s+)?overskrift\s*:?\s*$`)

// insertHeading puts a new heading over the provision after the one the change names, or over the
// one it names when the heading goes before it.
func (c *Content) insertHeading(e Edit, after bool, reject func(string), apply func(where, note string)) {
	text := Squeeze(QuoteStrip(e.New))
	if text == "" && len(e.NewProse) == 1 {
		text = Squeeze(e.NewProse[0])
	}
	if text == "" || len(e.NewProse) > 1 {
		reject("the new heading's text is not one line")
		return
	}
	a := e.Address
	a.Heading, a.Stk, a.StkTo, a.Nr, a.Sentence, a.SentenceTo = false, 0, 0, 0, 0, 0
	loc, ok := c.Find(a)
	if !ok || loc.Provision == nil {
		reject(fmt.Sprintf("address %q not found in the current text", a))
		return
	}
	target := loc.Provision
	if after {
		target = nil
		refs := c.Provisions()
		for i, r := range refs {
			if r.Provision == loc.Provision && i+1 < len(refs) {
				target = refs[i+1].Provision
			}
		}
		if target == nil {
			reject(fmt.Sprintf("no provision follows %s for the heading to head", loc))
			return
		}
	}
	switch {
	case Squeeze(target.Heading) == text:
		apply("§ "+target.Number+target.Litra, "the text already reads as the change has it")
	case target.Heading != "":
		reject(fmt.Sprintf("§ %s%s already has a heading, and the change puts another before it", target.Number, target.Litra))
	default:
		target.Heading = text
		apply("§ "+target.Number+target.Litra, "the heading heads the provision after the one the change names")
	}
}

// reItemInsertion is an insertion of a numbered or lettered condition: "indsættes som litra j",
// "som nr. 5", "efter nr. 3 som nyt nummer", "efter litra i som nyt litra".
var reItemInsertion = regexp.MustCompile(`(?i)\bindsættes\s+(?:(?:efter|før)\s+(?:nr\.\s*[0-9]+|litra\s+[a-zæøå])\s*,?\s+)?som\s+(?:nyt?e?\s+)?(?:nr\.|nummer|numre|litra|litra[er])\b`)

// reItemLead is a condition's label opening its text: "5)", "5 a)", "j)".
var reItemLead = regexp.MustCompile(`^\s*(?:[0-9]+\s*[a-zæøå]?|[a-zæøå])\)`)

// itemLinesOf is the text of the subsections an insertion of conditions brings, one line each,
// when every one of them opens with a condition's label; nil otherwise.
func itemLinesOf(e Edit) []string {
	if len(e.NewStykker) == 0 || len(e.NewProse) > 0 || !reItemInsertion.MatchString(blankQuoted(e.Definition)) {
		return nil
	}
	lines := make([]string, 0, len(e.NewStykker))
	for _, st := range e.NewStykker {
		var parts []string
		for _, seg := range st {
			if t := strings.TrimSpace(seg.Text); t != "" {
				parts = append(parts, t)
			}
		}
		line := Squeeze(strings.Join(parts, " "))
		if !reItemLead.MatchString(line) {
			return nil
		}
		lines = append(lines, line)
	}
	return lines
}

// reAfterLitraNew is an insertion of a lettered condition by another: "indsættes efter litra i som
// nyt litra", "før litra a som nyt litra".
var reAfterLitraNew = regexp.MustCompile(`(?i)\bindsættes\s+(efter|før)\s+litra\s+([a-zæøå])\s*,?\s+som\s+(?:nyt?e?\s+)?litra(?:er)?\b`)

// reProvisionLabelOnly is a line holding a provision's label alone: "§ 3 a".
var reProvisionLabelOnly = regexp.MustCompile(`^§\s*[0-9]+\s*[a-zæøå]?$`)

// reAfterProvisionProse is an insertion after a provision: "Efter § 24 indsættes:", "Efter § 24
// indsættes i kapitel 4:".
var reAfterProvisionProse = regexp.MustCompile(`(?i)^\s*efter\s+§\s*([0-9]+)\s*([a-zæøå])?\s+indsættes(?:\s+i\s+kapitel\s+[0-9]+\s*[a-zæøå]?)?\s*:?\s*$`)

// reProvisionLine opens a provision's text: "§ 24 a. Installation …".
var reProvisionLine = regexp.MustCompile(`^\s*§\s*([0-9]+)\s*([a-zæøå])?\s*\.\s*(.*)$`)

// looksLikeHeading is a line read as a heading: short, capitalised, and not closed as a sentence.
func looksLikeHeading(line string) bool {
	line = strings.TrimSpace(line)
	r := []rune(line)
	if len(r) == 0 || len(r) > 100 || !unicode.IsUpper(r[0]) || strings.ContainsAny(string(r[len(r)-1]), ".:;,") {
		return false
	}
	return !reStykkeLabel.MatchString(line) && !reProvisionLine.MatchString(line)
}

// provisionsOfLines reads loose lines as the provisions they number: a "§ 24 a." line opens a
// provision, a heading line before one heads it, and "Stk. 2." and "1)" open its subsections and
// conditions. Nil when a line stands outside every provision.
func provisionsOfLines(lines []string) []*Provision {
	var out []*Provision
	heading := ""
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if m := reProvisionLine.FindStringSubmatch(l); m != nil {
			litra := strings.ToLower(m[2])
			printed := "§ " + m[1] + "."
			if litra != "" {
				printed = "§ " + m[1] + " " + litra + "."
			}
			p := &Provision{Number: m[1], Litra: litra, Printed: printed, Heading: heading, Segments: []Segment{{Text: printed, Break: true}}}
			if rest := strings.TrimSpace(m[3]); rest != "" {
				p.Segments = append(p.Segments, Segment{Text: rest, Break: true})
			}
			heading = ""
			out = append(out, p)
			continue
		}
		if looksLikeHeading(l) && i+1 < len(lines) && reProvisionLine.MatchString(lines[i+1]) {
			heading = l
			continue
		}
		if len(out) == 0 {
			return nil
		}
		p := out[len(out)-1]
		if m := reProseStkLabel.FindStringSubmatch(l); m != nil {
			p.Segments = append(p.Segments, Segment{Text: "Stk. " + m[1] + ".", Break: true})
			l = strings.TrimSpace(l[len(m[0]):])
		} else if m := reItemLead.FindString(l); m != "" {
			p.Segments = append(p.Segments, Segment{Text: strings.TrimSpace(m), Break: true})
			l = strings.TrimSpace(l[len(m):])
		}
		if l != "" {
			p.Segments = append(p.Segments, Segment{Text: l, Break: true})
		}
	}
	for _, p := range out {
		p.Normalise()
	}
	return out
}

// defHead is a change's definition up to the new words it quotes.
func defHead(def string) string {
	if i := strings.Index(def, "»"); i >= 0 {
		return def[:i]
	}
	return def
}

// namesNewUnit reports an insertion naming the sentence or condition it makes: "efter 1. pkt. som
// nyt punktum", "som 2. pkt.", "efter nr. 9 som nyt nummer", "som litra d".
func namesNewUnit(e Edit) bool {
	d := defHead(e.Definition)
	return e.Address.Sentence > 0 && reNewSentence.MatchString(d) ||
		reAfterNumberNew.MatchString(d) || reAsNumber.MatchString(d) || reAsLitra.MatchString(d) || reAfterLitraNew.MatchString(d)
}

// reNewSentence is an insertion of a sentence: "indsættes efter 1. pkt. som nyt punktum", "indsættes
// som 2. pkt.".
var reNewSentence = regexp.MustCompile(`(?i)\bindsættes\s+(?:efter\s+[0-9]+\.\s*(?:pkt\.?|punktum)\s*,?\s*)?som\s+(?:nyt?e?\s+)?(?:[0-9]+\.\s*)?(?:pkt\.?|punktum|punktummer)`)

// listMovedTo is the subsection next to st that prints the conditions an insertion "som nr. want"
// follows, when st prints no condition at all: want-1 numbered conditions, in exactly one of its
// neighbours. Nil otherwise.
func listMovedTo(p *Provision, st *Stykke, want int) *Stykke {
	if want < 2 {
		return nil
	}
	for _, it := range st.Items {
		if it.Litra == "" {
			return nil
		}
	}
	if from, to := st.Span.From, min(st.Span.To, len(p.Segments)); from < to {
		for _, seg := range p.Segments[from:to] {
			if strings.Contains(seg.Text, itoa(want-1)+")") {
				return nil // the conditions printed inline
			}
		}
	}
	idx := -1
	for i := range p.Stykker {
		if &p.Stykker[i] == st {
			idx = i
		}
	}
	var found *Stykke
	for _, i := range []int{idx - 1, idx + 1} {
		if idx < 0 || i < 0 || i >= len(p.Stykker) {
			continue
		}
		n := 0
		for _, it := range p.Stykker[i].Items {
			if it.Litra == "" && it.Suffix == "" {
				n++
			}
		}
		if n == want-1 {
			if found != nil {
				return nil
			}
			found = &p.Stykker[i]
		}
	}
	return found
}

// reAsNewNumber is an insertion of a condition that names no number: "indsættes som nyt nummer:".
var reAsNewNumber = regexp.MustCompile(`(?i)\bindsættes\s+som\s+(?:nyt?\s+)?(?:nummer|nr\.)\s*:?\s*$`)

// reAsLitra is an insertion naming the letter its new condition takes, "indsættes som litra d",
// "som litra c og d", "som litra d)".
var reAsLitra = regexp.MustCompile(`(?i)\bindsættes\s+som\s+(?:nyt?e?\s+)?litra\s+([a-zæøå])\)?(?:\s*(?:[-–]|og)\s*[a-zæøå]\)?)?\s*:?\s*$`)

// reAsNumber is an insertion naming the number its new condition takes, "indsættes som nr. 2",
// and reAfterNumber one naming the condition it follows, "indsættes efter nr. 1".
var (
	reAsNumber    = regexp.MustCompile(`(?i)\bindsættes\s+som\s+(?:nyt?e?\s+)?nr\.\s*([0-9]+)(?:\s*(?:[-–]|og)\s*[0-9]+)?\s*(?::|\.?\s*$)`)
	reAfterNumber = regexp.MustCompile(`(?i)\befter\s+nr\.\s*[0-9]+`)
	// reAfterNumberNew is "indsættes efter nr. 10 som nyt nummer:", which names no number of its own.
	reAfterNumberNew = regexp.MustCompile(`(?i)\bindsættes\s+(efter|før)\s+nr\.\s*([0-9]+)(?:\s*,\s*(?:der|som)\s+bliver\s+nr\.\s*([0-9]+)\s*,)?\s*,?\s+som\s+(?:nyt?e?\s+)?(?:nummer|numre|nr\.)(?:\s*[0-9]+(?:\s*(?:[-–]|og)\s*[0-9]+)?)?\s*:?\s*(?:nr\.\s*[0-9]+(?:\s*(?:[-–]|og)\s*[0-9]+)?\s+bliver\s+herefter\s+nr\.\s*[0-9]+(?:\s*(?:[-–]|og)\s*[0-9]+)?\.?\s*)?$`)
)

// reByActAnnex is a change carried out by a table or list the act prints in an annex of its own:
// "I Bilag 2 indsættes maksimalgrænseværdier som angivet i bilag 1 til denne bekendtgørelse",
// "Bilagene 2, 4, 9 og 10 ændres som anført i bilag 2 til denne bekendtgørelse".
var reByActAnnex = regexp.MustCompile(`(?i)\b(?:som\s+(?:angivet|anført|fastsat)\s+i|som\s+i|angivet\s+i)\s+bilag(?:et)?\s*[0-9]*\s+(?:til|i)\s+denne\b`)

// unrecognisedReason says why a change was not read, as precisely as its wording allows.
func unrecognisedReason(def string) string {
	if reTableCells.MatchString(def) {
		return "the change edits a table's rows and values, which this model holds as text rather than as a table"
	}
	if reByActAnnex.MatchString(def) {
		return "the change is made by a table or list in the amending act's own annex, which says what to change cell by cell rather than in words this model can apply"
	}
	return "change wording not recognised by the pattern set"
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
	// The subsection the new text goes into, when the address names one that is there; otherwise
	// the provision.
	host := a
	if a.Nr != 0 && a.Litra != "" {
		// "I § 10, nr. 2, indsættes som litra d": the condition the new letter goes into.
		host.Litra = ""
		if got, found := c.Find(host); found && got.Provision != nil && got.Item != nil {
			return got, true
		}
	}
	host.Nr, host.Litra = 0, ""
	if got, found := c.Find(host); found && got.Provision != nil {
		return got, true
	}
	host.Stk, host.StkTo = 0, 0
	got, found := c.Find(host)
	return got, found && got.Provision != nil
}

// insertionAnchor picks the provision a new § goes beside: the addressed one if it is still
// there, otherwise the neighbour its number used to sit between.
func (c *Content) insertionAnchor(loc Located, found bool, a Address) (anchor *Provision, after bool, note string) {
	if found && loc.Provision != nil {
		return loc.Provision, true, "inserted after the addressed provision; exact position within a run of insertions is approximate"
	}
	want := Atoi(strings.TrimRight(a.Paragraph, "abcdefghijklmnopqrstuvwxyzæøå "))
	if want == 0 {
		return nil, false, ""
	}
	wantLitra := paragraphLitra(a.Paragraph)
	// The anchor has to stay inside the law's own numbering series: entry-into-force material
	// restarts at § 1, so without this an insertion meant for § 20 would land beside an
	// entry-into-force quotation printed as § 3. "Efter § 21 b indsættes" where § 21 b is gone
	// sits after § 21 a and before § 22.
	var last, next *Provision
	for _, ref := range c.Provisions() {
		p := ref.Provision
		n := Atoi(p.Number)
		if n == 0 {
			continue
		}
		if n < want || n == want && strings.ToLower(p.Litra) < wantLitra {
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
	if m := reAnnexRenumber.FindStringSubmatch(Squeeze(e.Definition)); m != nil {
		// "Bilag 3 ophæves. Bilag 4 bliver herefter til bilag 3.": an annex's number is printed in
		// its header, not given by its place, so the renumbering is carried out.
		from, to := Atoi(m[1]), Atoi(m[2])
		if c.renumberAnnex(from, to) {
			apply("bilag "+m[2], "")
			return
		}
		if c.hasAnnex(to, "") && !c.hasAnnex(from, "") {
			apply("bilag "+m[2], "the annex is numbered so already")
			return
		}
		reject(fmt.Sprintf("bilag %d is not in the current text to become bilag %d", from, to))
		return
	}
	para := e.Address.Paragraph
	if !strings.Contains(e.Definition, "§") && c.relabelAnnexParts(e, reject, apply) {
		return
	}
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
	from, to := Atoi(strings.TrimRight(a.Paragraph, "abcdefghijklmnopqrstuvwxyzæøå ")), Atoi(strings.TrimRight(strings.ToLower(a.RangeTo), "abcdefghijklmnopqrstuvwxyzæøå "))
	if from == 0 || to < from {
		return nil
	}
	var out []Located
	for _, ref := range c.Provisions() {
		n := Atoi(ref.Provision.Number)
		if n == 0 {
			continue
		}
		// "§§ 34-36" ends at § 36 itself: § 36 a-36 m come after it, and the act that repeals
		// §§ 34-36 goes on to amend § 36 g. The letters are compared the same way at both ends.
		if !inRange(n, ref.Provision.Litra, from, paragraphLitra(a.Paragraph), to, paragraphLitra(a.RangeTo)) {
			continue
		}
		// Only the law's own series: entry-into-force material restarts its numbering at § 1,
		// and "§§ 1-3" must not sweep up those quotations. Provisions() is the law's own
		// series by construction, which is what makes this the same rule in a different shape.
		out = append(out, Located{Provision: ref.Provision, Part: ref.Part})
	}
	return out
}

// flipInitial swaps the case of a passage's first letter: "Ansøger" and "ansøger".
func flipInitial(s string) string {
	r := []rune(s)
	if len(r) == 0 || !unicode.IsLetter(r[0]) {
		return s
	}
	if unicode.IsUpper(r[0]) {
		r[0] = unicode.ToLower(r[0])
	} else {
		r[0] = unicode.ToUpper(r[0])
	}
	return string(r)
}

// initialCase names the case of a passage's first letter, for the note an applied change carries.
func initialCase(s string) string {
	if r := []rune(s); len(r) > 0 && unicode.IsUpper(r[0]) {
		return "a capital"
	}
	return "a lower-case"
}

// paragraphLitra is the letter of a § number as addresses spell it: "a" in "36a".
func paragraphLitra(p string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimLeft(p, "0123456789")))
}

// inRange reports whether § n litra lies between the two ends of a range, both included; a number
// without a letter comes before its lettered provisions.
func inRange(n int, litra string, from int, fromLitra string, to int, toLitra string) bool {
	litra = strings.ToLower(litra)
	after := n > from || n == from && litra >= fromLitra
	before := n < to || n == to && litra <= toLitra
	return after && before
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
// replaceInsideWords replaces a long word of one piece where the text prints it only as part of
// longer words, keeping their other parts. Nothing below ten letters, nor a word of several.
func (c *Content) replaceInsideWords(old, new string) int {
	old, new = strings.TrimSpace(old), strings.TrimSpace(new)
	if len([]rune(old)) < 10 || strings.ContainsAny(old, " -") || new == "" || strings.ContainsAny(new, " ") {
		return 0
	}
	re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(old))
	n := 0
	fix := func(segs []Segment) {
		for i := range segs {
			segs[i].Text = re.ReplaceAllStringFunc(segs[i].Text, func(m string) string {
				n++
				if r := []rune(m); unicode.IsUpper(r[0]) {
					nr := []rune(new)
					nr[0] = unicode.ToUpper(nr[0])
					return string(nr)
				}
				return new
			})
		}
	}
	fix(c.Preamble)
	fix(c.Title)
	c.eachProvision(func(p *Provision) { fix(p.Segments) })
	for i := range c.EntryIntoForce {
		fix(c.EntryIntoForce[i].Segments)
	}
	for i := range c.Appendices {
		fix(c.Appendices[i].Segments)
	}
	return n
}

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
	case l.Part != nil && l.Appendix == nil:
		// "I kapitel 25 ændres »…«": the first occurrence among the chapter's provisions.
		return replaceInPart(l.Part, old, new, false)
	case l.Appendix != nil && len(l.AnnexSpans) > 0:
		// The parts the change names, each of them.
		for _, sp := range l.AnnexSpans {
			if f, ch, n := replaceIn(l.Appendix.Segments, sp, old, new, false); f && n > 0 {
				found, changed = true, changed || ch
			}
		}
		return found, changed
	case l.Appendix != nil:
		// An annex's text is its own prose, or the provisions it holds: the first occurrence in
		// reading order is the one an edit to the annex means.
		if found, changed := replaceWhole(l.Appendix.Segments, old, new); found {
			return found, changed
		}
		var hit, changed bool
		var walk func(nodes []Node)
		walk = func(nodes []Node) {
			for _, n := range nodes {
				if hit {
					return
				}
				switch {
				case n.Provision != nil:
					hit, changed = replaceSpanOf(n.Provision, Span{From: 0, To: len(n.Provision.Segments)}, old, new)
				case n.Part != nil:
					walk(n.Part.Order)
				}
			}
		}
		walk(l.Appendix.Order)
		return hit, changed
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
	// "§ 28, stk. 1, affattes således: »§ 28. …«" prints the subsection with its provision's number
	// in front. The words are the first subsection's; the number stays where it is.
	if len(e.NewProse) == 0 && len(e.NewProvisions) == 1 && len(e.NewProvisions[0].Stykker) > 0 {
		p := e.NewProvisions[0]
		segs := cloneSegments(p.Segments[p.Stykker[0].Span.From:p.Stykker[0].Span.To])
		if len(segs) > 0 && p.Printed != "" {
			segs[0].Text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(segs[0].Text), p.Printed))
		}
		return segs
	}
	var segs []Segment
	for _, line := range e.NewProse {
		// "§ 99, stk. 2, nr. 1-4, affattes således: 1) … 2) … 3) … 4) …" on one line: a condition
		// to a segment, or the four read as one and the next act's "nr. 2" found the old nr. 5.
		segs = append(segs, conditionLines(Segment{Text: " " + line, Break: true})...)
	}
	return segs
}

// conditionLines is a line of new text cut into its conditions, numbered and lettered, when it
// prints several in a row.
func conditionLines(seg Segment) []Segment {
	var out []Segment
	for _, sg := range splitInlineItems(seg) {
		out = append(out, splitInlineLetters(sg)...)
	}
	return out
}

// keepMarker keeps a provision's printed number when the subsection being replaced carries it.
// Some readers put "§ 4." inside stk. 1's text rather than in front of it, and replacing stk. 1
// with the amendment's wording then took the provision's own number with it.
func keepMarker(p *Provision, st *Stykke, segs []Segment) []Segment {
	marker := strings.TrimSpace(p.Printed)
	if marker == "" || len(segs) == 0 || st.Span.From >= len(p.Segments) || st.Span.From >= st.Span.To {
		return segs
	}
	if !strings.HasPrefix(strings.TrimSpace(p.Segments[st.Span.From].Text), marker) ||
		strings.HasPrefix(strings.TrimSpace(segs[0].Text), marker) {
		return segs
	}
	return append([]Segment{{Text: marker + " "}}, segs...)
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
	// "§ 54 affattes således:" sometimes brings the rubric above § 54 as a group of its own. The
	// provision inside is the replacement, and the rubric is its heading.
	for _, part := range e.NewParts {
		for _, n := range part.Order {
			if n.Provision != nil {
				p := cloneProvision(*n.Provision)
				if p.Heading == "" {
					p.Heading = part.Heading
				}
				return p, true
			}
		}
	}
	p := Provision{}
	pos := 0
	for _, segs := range e.NewStykker {
		p.Segments = append(p.Segments, segs...)
		p.Stykker = append(p.Stykker, Stykke{Nr: len(p.Stykker) + 1, Span: Span{From: pos, To: pos + len(segs)}})
		pos += len(segs)
	}
	for _, line := range e.NewProse {
		for _, sg := range conditionLines(Segment{Text: " " + line, Break: true}) {
			p.Segments = append(p.Segments, sg)
			pos++
		}
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
		return l.InsertLines(segs)
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

var (
	reAfterStykke  = regexp.MustCompile(`(?i)\befter\s+stk\.\s*([0-9]+)\b`)
	reBeforeStykke = regexp.MustCompile(`(?i)\bindsættes\s+før\s+stk\.\s*([0-9]+)\b`)
	reAsStykke     = regexp.MustCompile(`(?i)\bsom\s+(?:nyt?\s+|nye\s+)?stk\.\s*([0-9]+)\b`)
)

// stykkePosition reads where an inserted subsection lands: after the subsection a change names
// with "efter stk. 2", or in front of the place it is numbered into with "som stk. 3". It reports
// the number of the subsection the new one follows, and whether the change said.
func stykkePosition(def string) (after int, named bool) {
	s := blankQuoted(def)
	if m := reAfterStykke.FindStringSubmatch(s); m != nil {
		return Atoi(m[1]), true
	}
	if m := reAsStykke.FindStringSubmatch(s); m != nil && Atoi(m[1]) > 0 {
		return Atoi(m[1]) - 1, true
	}
	if m := reBeforeStykke.FindStringSubmatch(s); m != nil && Atoi(m[1]) > 0 {
		return Atoi(m[1]) - 1, true
	}
	return 0, false
}

// reClauseJoin joins the places a change sentence names: "… kapitel 5 og i § 14 …".
var reClauseJoin = regexp.MustCompile(`(?i)\s*,?\s+og\s+(?:i\s+)?|\s*,\s+(?:i\s+)?(?:§|overskrift)`)

// clausePlaces reads each place of a sentence whose places differ in kind, a heading beside a
// provision, by reading each clause before the verb on its own.
func clausePlaces(def string) []Address {
	head := blankQuoted(Squeeze(def))
	if m := reAddressEnd.FindStringIndex(head); m != nil {
		head = head[:m[0]]
	}
	var out []Address
	prev := 0
	cuts := reClauseJoin.FindAllStringIndex(head, -1)
	cuts = append(cuts, []int{len(head), len(head)})
	for _, c := range cuts {
		part := head[prev:c[0]]
		joint := strings.ToLower(head[c[0]:c[1]])
		if i := strings.Index(joint, "overskrift"); i >= 0 {
			c[1] = c[0] + i // the unit word belongs to the next place
		} else if i := strings.Index(joint, "§"); i >= 0 {
			c[1] = c[0] + i
		}
		prev = c[1]
		a := ParseAddress(part)
		if !a.Recognised {
			return nil
		}
		out = append(out, a)
	}
	return out
}

// reHeadingRepeal is a repeal that names a heading by its words and the provisions under it:
// "Overskriften »Overtagelse af vindmøller« efter § 65 a og §§ 66 og 68 ophæves", "I kapitel 9
// ophæves overskriften »a. Pristillæg m.v.« og §§ 55, 56, 56 a-56 f".
var reHeadingRepeal = regexp.MustCompile(`(?i)overskriften\s*»([^«]+)«(?:\s*(?:efter|før)\s*§\s*[0-9]+(?:\s*[a-zæøå](?:[^\p{L}]|$))?)?\s*,?\s*og\s+(§§?[^»]*?)\s*(?:ophæves|$)`)

// reListSplit separates the entries of a list of provisions: "13-17, 17 a, 17 b og 18".
var reListSplit = regexp.MustCompile(`\s*(?:,|\bog\b)\s*`)

// reParagraphNumber is a provision number as a list prints it: "17", "17a".
var reParagraphNumber = regexp.MustCompile(`(?i)^[0-9]+[a-zæøå]?$`)

// applyHeadingAndRepeals carries out a repeal of a heading named by its words together with the
// provisions under it. Read as a deletion of the quoted words, it was refused at the provision the
// heading follows, and the provisions it repealed stayed in the law.
func (c *Content) applyHeadingAndRepeals(e Edit, out *Consolidation) bool {
	if !strings.Contains(strings.ToLower(e.Definition), "ophæves") {
		return false
	}
	m := reHeadingRepeal.FindStringSubmatch(e.Definition)
	if m == nil {
		return false
	}
	sub := &Consolidation{}
	heading := Edit(e)
	heading.Kind, heading.Old, heading.New = EditDeleteProvision, "", ""
	if loc, ok := c.headingByText(m[1]); ok && c.SetHeadingChange(loc, "") {
		sub.Applied = append(sub.Applied, AppliedEdit{Edit: heading, Where: loc.String() + " (overskrift)",
			Note: "removed the heading the change names by its words"})
	} else {
		sub.Unresolved = append(sub.Unresolved, UnresolvedEdit{Edit: heading,
			Reason: fmt.Sprintf("no heading reads %q", m[1])})
	}
	list := strings.NewReplacer("§§", " ", "§", " ", "–", "-").Replace(strings.TrimRight(m[2], " .,"))
	for _, piece := range reListSplit.Split(list, -1) {
		ends := strings.SplitN(piece, "-", 2)
		from := strings.ReplaceAll(strings.TrimSpace(ends[0]), " ", "")
		if !reParagraphNumber.MatchString(from) {
			continue
		}
		one := heading
		one.Address = Address{Recognised: true, Paragraph: strings.ToLower(from)}
		if len(ends) == 2 {
			one.Address.RangeTo = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(ends[1]), " ", ""))
		}
		one.Definition = one.Address.String() + " ophæves."
		c.applyAt(one, sub)
	}
	out.Applied = append(out.Applied, sub.Applied...)
	out.Unresolved = append(out.Unresolved, sub.Unresolved...)
	out.NoOpEdits += sub.NoOpEdits
	return true
}

// headingByText finds the one heading in the law that reads as quoted: a provision's rubric or a
// chapter's or group's heading.
func (c *Content) headingByText(text string) (Located, bool) {
	want := MatchText(text)
	var found []Located
	var walk func(nodes []Node)
	walk = func(nodes []Node) {
		for _, n := range nodes {
			switch {
			case n.Provision != nil:
				if n.Provision.Heading != "" && MatchText(n.Provision.Heading) == want {
					found = append(found, Located{Provision: n.Provision})
				}
			case n.Part != nil:
				if n.Part.Heading != "" && MatchText(n.Part.Heading) == want {
					found = append(found, Located{Part: n.Part})
				}
				walk(n.Part.Order)
			}
		}
	}
	walk(c.Order)
	if len(found) != 1 {
		return Located{}, false
	}
	return found[0], true
}

// wholeProvisions is every provision a replacement names as a whole: the range it resolved, or the
// list its sentence names ("§§ 101 og 102 ophæves, og i stedet indsættes"). Nil when it names a
// part of one, or a place that is not there.
func (c *Content) wholeProvisions(e Edit, targets []Located) []*Provision {
	var out []*Provision
	for _, t := range targets {
		if t.Provision == nil || t.Stykke != nil || t.Item != nil || t.Part != nil && t.Provision == nil {
			return nil
		}
		out = append(out, t.Provision)
	}
	if len(out) != 1 || e.Address.RangeTo != "" {
		return out
	}
	places := ParseAddresses(e.Definition)
	if len(places) < 2 {
		return out
	}
	out = out[:0]
	seen := map[*Provision]bool{}
	for _, a := range places {
		if a.Stk != 0 || a.Nr != 0 || a.Sentence != 0 || a.Paragraph == "" {
			return nil
		}
		loc, ok := c.Find(a)
		if !ok || loc.Provision == nil {
			return nil
		}
		if !seen[loc.Provision] {
			// "Overskriften før § 7 og § 7" names one provision twice.
			seen[loc.Provision] = true
			out = append(out, loc.Provision)
		}
	}
	return out
}

// replaceProvisions puts the new text in the place of several provisions at once, or of one
// provision that the new text replaces with a chapter: "§§ 28-30 affattes således: § 28. … § 29. …
// § 30. …" is three provisions for three, not each of the three replaced by all of them, and
// "§§ 101 og 102 ophæves, og i stedet indsættes: Kapitel 7 a …" puts a chapter where they stood.
func (c *Content) replaceProvisions(e Edit, old []*Provision, reject func(string), apply func(where, note string)) {
	where := "§ " + old[0].Address()
	if len(old) > 1 {
		where += "–" + old[len(old)-1].Address()
	}
	if len(e.NewParts) > 0 && len(e.NewProvisions) == 0 {
		refs := c.Provisions()
		idx := -1
		for i, r := range refs {
			if r.Provision == old[0] {
				idx = i
			}
		}
		var anchor *Provision
		before := false
		if idx > 0 {
			anchor = refs[idx-1].Provision
		} else {
			last := old[len(old)-1]
			for i, r := range refs {
				if r.Provision == last && i+1 < len(refs) {
					anchor, before = refs[i+1].Provision, true
				}
			}
		}
		if anchor == nil {
			reject("the new chapter has no provision to be placed by")
			return
		}
		for _, p := range old {
			if !c.DeleteProvision(p) {
				reject(fmt.Sprintf("could not remove § %s", p.Address()))
				return
			}
		}
		parts := make([]*Part, 0, len(e.NewParts))
		for _, p := range e.NewParts {
			parts = append(parts, clonePart(p))
		}
		if !c.InsertParts(isProvision(anchor), before, parts) {
			reject("could not place the new chapter")
			return
		}
		apply(where, "replaced by the chapter the change brings")
		return
	}
	if len(e.NewProvisions) == 0 {
		reject("provision replacement carries no new text")
		return
	}
	first := cloneProvision(*e.NewProvisions[0])
	if !c.ReplaceContent(old[0], first) {
		reject(fmt.Sprintf("could not replace § %s", old[0].Address()))
		return
	}
	for _, p := range old[1:] {
		if !c.DeleteProvision(p) {
			reject(fmt.Sprintf("could not remove § %s", p.Address()))
			return
		}
	}
	prev := old[0]
	for _, extra := range e.NewProvisions[1:] {
		d := cloneProvision(*extra)
		if !c.InsertProvisionAfter(prev, &d) {
			reject(fmt.Sprintf("could not place § %s", d.Address()))
			return
		}
		prev = &d
	}
	if len(e.NewParts) > 0 {
		parts := make([]*Part, 0, len(e.NewParts))
		for _, p := range e.NewParts {
			parts = append(parts, clonePart(p))
		}
		if !c.InsertParts(isProvision(prev), false, parts) {
			reject("could not place the chapter the change brings after its provisions")
			return
		}
	}
	apply(where, "")
}

// reChapterLabel is a change to a chapter's printed number: "Kapitelbetegnelsen »Kapitel 5 a«
// ændres til: »Kapitel 5 b«".
var reChapterLabel = regexp.MustCompile(`(?i)^\s*kapitelbetegnelsen\s*»\s*(kapitel\s+[0-9]+(?:\s*[a-zæøå])?)\s*«\s*ændres\s+til\s*:?\s*»\s*(kapitel\s+[0-9]+(?:\s*[a-zæøå])?)\s*«`)

// applyChapterLabel renumbers a chapter the change names by its printed label.
func (c *Content) applyChapterLabel(e Edit, out *Consolidation) bool {
	m := reChapterLabel.FindStringSubmatch(e.Definition)
	if m == nil {
		return false
	}
	p := c.part(PartChapter, NumberWithLitra(m[1]))
	if p == nil {
		out.Unresolved = append(out.Unresolved, UnresolvedEdit{Edit: e, Reason: fmt.Sprintf("the law has no %s", Squeeze(m[1]))})
		return true
	}
	p.Number, p.Printed = NumberWithLitra(m[2]), Squeeze(m[2])
	out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: Squeeze(m[1]), Note: "renumbered the chapter"})
	return true
}

// unitSpan is the span of the provision text a location names: its item, its subsection, or the
// whole provision.
func (l Located) unitSpan() (Span, bool) {
	switch {
	case l.Provision == nil:
		return Span{}, false
	case l.Item != nil:
		return l.Item.Span, true
	case l.Stykke != nil:
		return l.Stykke.Span, true
	}
	return Span{From: 0, To: len(l.Provision.Segments)}, true
}

// replaceLoosely places a replacement by a passage the unit prints with other spacing, or without
// a closing mark the act quoted with it: »… på 6 måneder,« against "… på 6 måneder." The passage
// must occur once in the unit.
func (l Located) replaceLoosely(old, new string) (found, changed bool) {
	segs, sp, ok := l.looseUnit()
	if !ok {
		return false, false
	}
	if f, ch := replaceLoose(segs, sp, old, new); f {
		return f, ch
	}
	if printed, ok := fuzzyPrinted(SegmentsText(segs[sp.From:sp.To]), old); ok && countMatches(SegmentsText(segs[sp.From:sp.To]), printed) == 1 {
		// », 5.30 Ældreboliger og …« replaced by » og 5.30 Ældreboliger.«: the act's quotation
		// kept the space its new words start with, which reading the quotation trimmed. A word
		// is not glued to the one before it.
		text := SegmentsText(segs[sp.From:sp.To])
		if k := strings.Index(text, printed); k > 0 && new != "" {
			prev, _ := utf8.DecodeLastRuneInString(text[:k])
			first, _ := utf8.DecodeRuneInString(printed)
			lead, _ := utf8.DecodeRuneInString(new)
			if (unicode.IsLetter(prev) || unicode.IsDigit(prev)) && !unicode.IsLetter(first) && !unicode.IsDigit(first) &&
				(unicode.IsLetter(lead) || unicode.IsDigit(lead)) {
				new = " " + new
			}
		}
		if f, ch, n := replaceSelected(segs, sp, printed, new, pickFirst); f && n > 0 {
			return f, ch
		}
	}
	if printed, ok := typoPrinted(SegmentsText(segs[sp.From:sp.To]), old); ok && countMatches(SegmentsText(segs[sp.From:sp.To]), printed) == 1 {
		if f, ch, n := replaceSelected(segs, sp, printed, new, pickFirst); f && n > 0 {
			return f, ch
		}
	}
	if l.Provision != nil {
		// "I § 36, stk. 1, 1. pkt., ændres »§ 36, stk. 5« til: »stk. 4«": inside § 36 its own
		// subsection is cited as "stk. 5".
		own := regexp.MustCompile(`^§\s*` + regexp.QuoteMeta(l.Provision.Number) + `\s*` + regexp.QuoteMeta(l.Provision.Litra) + `\s*,\s*(stk\.\s*[0-9].*)$`)
		if m := own.FindStringSubmatch(strings.TrimSpace(old)); m != nil {
			text := SegmentsText(segs[sp.From:sp.To])
			if countMatches(text, m[1]) == 1 {
				if f, ch, n := replaceSelected(segs, sp, m[1], strings.TrimSpace(ownStripped(new, l.Provision)), pickFirst); f && n > 0 {
					return f, ch
				}
			}
		}
	}
	if o, n, ok := bareFigures(old, new); ok && figureOnce(SegmentsText(segs[sp.From:sp.To]), o) {
		// »5,34 kr.« to »5,18 kr.« in a rate table that prints "kr. pr. liter" once at its head
		// and the figures bare beneath it.
		if f, ch, k := replaceSelected(segs, sp, o, n, pickFirst); f && k > 0 {
			return f, ch
		}
	}
	if m := reTrailingCitation.FindStringSubmatch(strings.TrimSpace(QuoteStrip(old))); m != nil && len([]rune(m[1])) >= 20 &&
		countMatches(SegmentsText(segs[sp.From:sp.To]), m[1]) == 1 {
		// »lov om skifte af fællesbo m.v. § 69« where the text names the law alone, as the
		// consolidation of 2012 read it: "reglerne i § 60 i lov om ægtefælleskifte m.v.".
		if f, ch, k := replaceSelected(segs, sp, m[1], new, pickFirst); f && k > 0 {
			return f, ch
		}
	}
	if o, ok := suspendedCompound(SegmentsText(segs[sp.From:sp.To]), old); ok {
		// »studenterkurser og hf-kurser« for the text's "studenter- og hf-kurser".
		if f, ch, k := replaceSelected(segs, sp, o, new, pickFirst); f && k > 0 {
			return f, ch
		}
	}
	if o, n, ok := superscripted(SegmentsText(segs[sp.From:sp.To]), old, new); ok {
		// »(2,0 x hestens stangmål) 2 « for the text's "(2,0 x hestens stangmål)²".
		if f, ch, k := replaceSelected(segs, sp, o, n, pickFirst); f && k > 0 {
			return f, ch
		}
	}
	if printed, replaced, ok := listedSection(SegmentsText(segs[sp.From:sp.To]), old, new); ok {
		if f, ch, k := replaceSelected(segs, sp, printed, replaced, pickFirst); f && k > 0 {
			return f, ch
		}
	}
	trimmed := strings.TrimRight(strings.TrimSpace(old), ",;.")
	if trimmed == strings.TrimSpace(old) || len([]rune(trimmed)) < 12 {
		return false, false
	}
	mark := strings.TrimSpace(old)[len(trimmed):]
	if new != "" {
		if !strings.HasSuffix(strings.TrimSpace(new), mark) {
			return false, false
		}
		new = strings.TrimSuffix(strings.TrimSpace(new), mark)
	}
	return replaceLoose(segs, sp, trimmed, new)
}

// insertLoosely places an insertion by a passage the unit prints with other spacing. The anchor
// keeps the law's own spelling; the new words go beside it.
func (l Located) insertLoosely(anchor, text string, before bool) bool {
	segs, sp, ok := l.looseUnit()
	if !ok {
		return false
	}
	printed, ok := looseMatch(SegmentsText(segs[sp.From:sp.To]), anchor)
	if !ok {
		if printed, ok = fuzzyPrinted(SegmentsText(segs[sp.From:sp.To]), anchor); !ok {
			printed, ok = typoPrinted(SegmentsText(segs[sp.From:sp.To]), anchor)
		}
		if !ok || countMatches(SegmentsText(segs[sp.From:sp.To]), printed) != 1 {
			return false
		}
		// »specialforeninger,« quoted where the text has "specialforeninger eller …": the comma the
		// act counted on is not there, so the new member of the list brings it, in front.
		a, t := strings.TrimSpace(anchor), strings.TrimSpace(text)
		if !before && strings.HasSuffix(a, ",") && !strings.HasSuffix(strings.TrimSpace(printed), ",") && strings.HasSuffix(t, ",") {
			text = ", " + strings.TrimSuffix(t, ",")
			f, _, n := replaceSelected(segs, sp, printed, printed+text, pickFirst)
			return f && n > 0
		}
		f, _, n := replaceSelected(segs, sp, printed, joinInsertion(printed, text, before), pickFirst)
		return f && n > 0
	}
	f, _ := replaceLoose(segs, sp, anchor, joinInsertion(printed, text, before))
	return f
}

// reItemRun is a run of conditions a replacement names: "nr. 2 og 3", "nr. 4-6".
var reItemRun = regexp.MustCompile(`(?i)\bnr\.\s*([0-9]+)\s*(?:(og|-|–)\s*([0-9]+))?`)

// reStykkePair is two subsections a replacement names together: "stk. 3 og 4".
var reStykkePair = regexp.MustCompile(`(?i)\bstk\.\s*([0-9]+)\s+og\s+(?:stk\.\s*)?([0-9]+)\b`)

// otherNamedStykke is the second subsection of "stk. 3 og 4 ophæves, og i stedet indsættes" or
// "stk. 3 og 4 affattes således", in the act's numbering, when the first is the one located; 0
// when the address names one.
func otherNamedStykke(e Edit, t Located) int {
	if e.Address.StkTo != 0 || e.Address.Nr != 0 || e.Address.Sentence != 0 || t.Stykke == nil {
		return 0
	}
	head := blankQuoted(e.Definition)
	if m := reOperativeVerb.FindStringIndex(head); m != nil {
		head = head[:m[0]]
	}
	m := reStykkePair.FindStringSubmatch(head)
	if m == nil || Atoi(m[1]) != e.Address.Stk || Atoi(m[2]) != e.Address.Stk+1 {
		return 0
	}
	return Atoi(m[2])
}

// otherNamedItems is the conditions after the first that a replacement of conditions names in its
// address: 3 in "nr. 2 og 3 ophæves, og i stedet indsættes", 5 and 6 in "nr. 4-6 affattes således".
func (c *Content) otherNamedItems(e Edit, t Located) []int {
	head := blankQuoted(e.Definition)
	if m := reOperativeVerb.FindStringIndex(head); m != nil {
		head = head[:m[0]]
	}
	m := reItemRun.FindStringSubmatch(head)
	if m == nil {
		// "I § 35, stk. 4, ændres »2019-niveau« til: »2024-niveau følgende«, og nr. 1-4 affattes
		// således": the run is named in the clause of its own verb.
		m = reItemRunVerb.FindStringSubmatch(blankQuoted(e.Definition))
	}
	if m == nil || m[3] == "" || Atoi(m[1]) != t.Item.Was && t.Item.Was != 0 {
		return nil
	}
	from, to := Atoi(m[1]), Atoi(m[3])
	if to <= from || to-from > 20 {
		return nil
	}
	var out []int
	if m[2] == "og" {
		return []int{to}
	}
	for n := from + 1; n <= to; n++ {
		out = append(out, n)
	}
	return out
}

// ownStripped is new words citing a subsection of the provision they go into, without the
// provision's own number: "§ 36, stk. 4" inside § 36 reads "stk. 4".
func ownStripped(new string, p *Provision) string {
	own := regexp.MustCompile(`^§\s*` + regexp.QuoteMeta(p.Number) + `\s*` + regexp.QuoteMeta(p.Litra) + `\s*,\s*`)
	return own.ReplaceAllString(strings.TrimSpace(new), "")
}

// numberWords are the numbers drafters write out and texts print in figures, or the other way.
var numberWords = map[string]string{"en": "1", "et": "1", "to": "2", "tre": "3", "fire": "4", "fem": "5", "seks": "6",
	"syv": "7", "otte": "8", "ni": "9", "ti": "10", "elleve": "11", "tolv": "12"}

// reNumberWordOrFigure is a number written out or in figures, as a word of its own.
var reNumberWordOrFigure = regexp.MustCompile(`(?i)(?:^|[^\p{L}0-9])(en|et|to|tre|fire|fem|seks|syv|otte|ni|ti|elleve|tolv|[0-9]{1,2})(?:[^\p{L}0-9]|$)`)

// numberSpelledOtherwise is a quotation with its one number written the other way, when the unit
// prints that and not the quotation: »i sammenlagt to år« for "i sammenlagt 2 år". Only a number
// followed by a unit of time or count keeps "en" and "et" from reading as articles.
func (l Located) numberSpelledOtherwise(old string) (string, bool) {
	text, _, ok := l.unitText()
	if !ok || strings.Contains(Squeeze(text), Squeeze(old)) {
		return "", false
	}
	locs := reNumberWordOrFigure.FindAllStringSubmatchIndex(old, -1)
	if len(locs) != 1 {
		return "", false
	}
	w := old[locs[0][2]:locs[0][3]]
	after := strings.TrimSpace(old[locs[0][3]:])
	if !reCountedUnit.MatchString(after) {
		return "", false
	}
	var other string
	if fig, isWord := numberWords[strings.ToLower(w)]; isWord {
		other = fig
	} else {
		for word, fig := range numberWords {
			if fig == w && word != "et" && word != "en" {
				other = word
			}
		}
	}
	if other == "" {
		return "", false
	}
	variant := old[:locs[0][2]] + other + old[locs[0][3]:]
	if !strings.Contains(Squeeze(text), Squeeze(variant)) {
		return "", false
	}
	return variant, true
}

// reCountedUnit is what a number in a quotation counts: "år", "måneder", "uger", "dage", "gange".
var reCountedUnit = regexp.MustCompile(`(?i)^(?:år|års|måned|måneder|måneders|uge|uger|ugers|dag|dage|dages|gang|gange|timer|timers)\b`)

// reSingleCitation is a quotation that is one provision's number: "§ 57 a".
var reSingleCitation = regexp.MustCompile(`^§\s*([0-9]+(?:\s*[a-zæøå])?)$`)

// citationInList is the number a quotation "§ 57 a" names, when the unit cites it only inside a
// list of §§, "§§ 56 c, 56 e og 57 a", and never as "§ 57 a": the number and its new one.
func (l Located) citationInList(old, new string) (string, string, bool) {
	mo, mn := reSingleCitation.FindStringSubmatch(Squeeze(old)), reSingleCitation.FindStringSubmatch(Squeeze(new))
	if mo == nil || mn == nil {
		return "", "", false
	}
	text, _, ok := l.unitText()
	if !ok {
		return "", "", false
	}
	text = Squeeze(text)
	num := regexp.MustCompile(`(^|[^0-9])` + regexp.QuoteMeta(mo[1]) + `([^0-9\p{L}]|$)`)
	if strings.Contains(text, "§ "+mo[1]) || !num.MatchString(text) {
		return "", "", false
	}
	for _, loc := range num.FindAllStringIndex(text, -1) {
		before := text[:loc[0]]
		k := strings.LastIndex(before, "§§")
		if k < 0 || strings.ContainsAny(before[k:], ".;") && !reListDotsOnly.MatchString(before[k:]) {
			return "", "", false // a number not inside a list of §§
		}
	}
	return mo[1], mn[1], true
}

// reStkCitation is a quotation citing one subsection of a provision: "§ 140, stk. 8".
var reStkCitation = regexp.MustCompile(`^§\s*([0-9]+)\s*([a-zæøå]?)\s*,\s*stk\.\s*([0-9]+)$`)

// citationOfMissingStk is how a unit cites a provision, when a change quotes the citation with a
// subsection the provision does not have: the unit cites the provision once, with another
// subsection, and never as quoted.
func (c *Content) citationOfMissingStk(l Located, old string) (string, bool) {
	m := reStkCitation.FindStringSubmatch(Squeeze(old))
	if m == nil {
		return "", false
	}
	cited, ok := c.Find(Address{Recognised: true, Paragraph: m[1], Litra: m[2]})
	if !ok || cited.Provision == nil || len(cited.Provision.Stykker) == 0 {
		return "", false
	}
	if n, _ := strconv.Atoi(m[3]); n <= len(cited.Provision.Stykker) {
		return "", false
	}
	text, _, ok := l.unitText()
	if !ok {
		return "", false
	}
	sign := `§\s*` + m[1] + `\s*` + m[2]
	if m[2] == "" {
		sign = `§\s*` + m[1]
	}
	re := regexp.MustCompile(sign + `,\s*stk\.\s*[0-9]+\b`)
	all := regexp.MustCompile(sign + `(?:[^0-9\p{L}]|$)`)
	text = Squeeze(text)
	found := re.FindAllString(text, -1)
	if len(found) != 1 || len(all.FindAllString(text, -1)) != 1 {
		return "", false
	}
	return found[0], true
}

// reListDotsOnly is a list whose only stops are those of "stk." or "nr.".
var reListDotsOnly = regexp.MustCompile(`^[^.;]*(?:(?:stk|nr)\.[^.;]*)*$`)

// reListConnective is what a list's next-to-last condition gains when a condition is added after
// it: "og", "eller", ", og".
var reListConnective = regexp.MustCompile(`^\s*,?\s*(?:og|eller)\s*$`)

// joinListEnding puts a list's connective at the end of the condition a change names, when the
// change's new words are its old ones and the connective, and the condition ends with the old
// words' last three, whatever the quotation left out before them.
func (l Located) joinListEnding(old, new string) (bool, bool) {
	old, new = strings.TrimSpace(old), strings.TrimSpace(new)
	if !strings.HasPrefix(new, old) || !reListConnective.MatchString(new[len(old):]) || l.Item == nil || l.Provision == nil {
		return false, false
	}
	words := strings.Fields(old)
	if len(words) < 3 {
		return false, false
	}
	tail := strings.Join(words[len(words)-3:], " ")
	p := l.Provision
	for k := l.Item.Span.To - 1; k >= l.Item.Span.From && k < len(p.Segments); k-- {
		text := strings.TrimRight(p.Segments[k].Text, " \u00a0")
		if strings.TrimSpace(text) == "" {
			continue
		}
		body := strings.TrimRight(text, ".,;")
		if !strings.HasSuffix(Squeeze(body), Squeeze(tail)) {
			return false, false
		}
		p.Segments[k].Text = body + new[len(old):]
		return true, true
	}
	return false, false
}

// reMinisterTitle is a minister named by office: "velfærdsministeren", "indenrigs- og
// sundhedsministeren", "økonomi- og erhvervsministeren".
var reMinisterTitle = regexp.MustCompile(`(?i)\b(?:[a-zæøå]+-(?:,\s*[a-zæøå]+-)*\s+og\s+)?[a-zæøå]+ministeren\b`)

// ministerUnderOtherTitle is the title a unit names its minister by, when a change quotes the
// minister by a title the unit does not print: one title, printed once, or as often as the change
// says when it names several places.
func (l Located) ministerUnderOtherTitle(old string, several bool) (string, bool) {
	old = strings.TrimSpace(old)
	if m := reMinisterTitle.FindString(old); m != old || m == "" {
		return "", false
	}
	text, _, ok := l.unitText()
	if !ok || strings.Contains(strings.ToLower(text), strings.ToLower(old)) {
		return "", false
	}
	titles := reMinisterTitle.FindAllString(Squeeze(text), -1)
	if len(titles) == 0 || len(titles) > 1 && !several {
		return "", false
	}
	for _, t := range titles[1:] {
		if !strings.EqualFold(t, titles[0]) {
			return "", false
		}
	}
	return titles[0], true
}

// printsStykkerLike reports a provision that prints every subsection given, each nearly word for
// word: two words other at most, and one in twenty for a long one.
func (p *Provision) printsStykkerLike(add [][]Segment) bool {
	if len(add) == 0 {
		return false
	}
	for _, segs := range add {
		want := wordsOf(reStkLabelLead.ReplaceAllString(Squeeze(SegmentsText(segs)), ""))
		if len(want) < 12 {
			return false
		}
		found := false
		for _, st := range p.Stykker {
			have := wordsOf(reStkLabelLead.ReplaceAllString(Squeeze(p.SpanText(st.Span)), ""))
			if wordEdits(have, want) <= max(2, len(want)/20) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// reItemRunVerb is a run of conditions named by the clause that rewrites or repeals them: "og nr.
// 1-4 affattes således".
var reItemRunVerb = regexp.MustCompile(`(?i)\bnr\.\s*([0-9]+)\s*(-|–|og)\s*(?:nr\.\s*)?([0-9]+)\s*,?\s+(?:affattes|ophæves)\b`)

// siblingHolding is the other subsection or condition of the same provision that holds a passage
// the addressed one does not, when the passage occurs once in the provision and the place is one
// the act can be read as naming: the subsection prints the number the act cites ("Stk. 9." on the
// eighth, after a repeal the publisher did not renumber), or the passage is long enough to name
// its place by itself. Anything less is refused: a short passage found elsewhere is a coincidence.
func (l Located) siblingHolding(passage string, a Address) (Located, bool) {
	p := l.Provision
	if p == nil || l.Stykke == nil && l.Item == nil || passage == "" {
		return Located{}, false
	}
	want := MatchText(passage)
	if strings.Count(MatchText(p.Text()), want) != 1 {
		return Located{}, false
	}
	// Citations are formulaic: "§ 13, stk. 2, nr. 5" sits in one condition and "nr. 6" in the next,
	// and an act working through them must not have one of its steps land on its neighbour's. Only
	// words carry their place with them.
	long := len([]rune(want)) >= 20 && strings.IndexFunc(want, unicode.IsDigit) < 0
	for i := range p.Stykker {
		st := &p.Stykker[i]
		if !strings.Contains(MatchText(p.SpanText(st.Span)), want) {
			continue
		}
		labelled := false
		if st.Span.From < len(p.Segments) {
			if m := reStykkeLabel.FindStringSubmatch(p.Segments[st.Span.From].Text); m != nil && Atoi(m[2]) == a.Stk {
				labelled = true
			}
		}
		if l.Item != nil {
			inItem := false
			for j := range st.Items {
				it := &st.Items[j]
				if strings.Contains(MatchText(p.SpanText(it.Span)), want) {
					inItem = true
					// A word of its own, printed once in the provision, in the condition next to the
					// one named: "§ 118 a, stk. 1, nr. 1, … ændres »selvvalgt«" for the "6 ugers
					// selvvalgt uddannelse" of nr. 2, which the ministry's consolidation changed.
					word := len([]rune(want)) >= 8 && strings.IndexFunc(want, unicode.IsDigit) < 0 && a.Nr != 0 && (it.Nr == a.Nr+1 || it.Nr == a.Nr-1)
					if long || word || it.Nr != a.Nr && FirstNumber(it.Label) == itoa(a.Nr) {
						return Located{Provision: p, Stykke: st, Item: it, Part: l.Part}, true
					}
				}
			}
			if last := lastItem(st); !inItem && last != nil && l.Stykke != nil && p.stykkeAt(l.Stykke) == st && last.Span == l.Item.Span {
				// "I § 58, stk. 1, nr. 4, ændres »§ 52, stk. 3, nr. 8«": the words closing the
				// subsection after its last condition, which the act reads as that condition's.
				return Located{Provision: p, Stykke: st, Part: l.Part}, true
			}
			return Located{}, false
		}
		if long || labelled {
			return Located{Provision: p, Stykke: st, Part: l.Part}, true
		}
		if a.Sentence > 0 && a.SentenceTo == 0 && l.Item == nil && l.Stykke != nil && (st.Nr == a.Stk+1 || st.Nr == a.Stk-1) {
			// "I § 53 a, stk. 1, 5. pkt., ændres »1-6« til: »1-7«" where stk. 2's fifth sentence
			// prints "nr. 1-6" and stk. 1 has no fifth sentence holding it: the sentence the act
			// counts is the neighbouring subsection's.
			if sent, ok := sentenceText(p.SpanText(st.Span), a.Sentence, 0); ok && strings.Contains(MatchText(sent), want) {
				if own, ok := sentenceText(p.SpanText(p.stykkeAt(l.Stykke).Span), a.Sentence, 0); !ok || !strings.Contains(MatchText(own), want) {
					return Located{Provision: p, Stykke: st, Part: l.Part}, true
				}
			}
		}
	}
	return Located{}, false
}

// reHeadingAfter is a heading named by the provision before it: "Overskriften efter § 145".
var reHeadingAfter = regexp.MustCompile(`(?i)overskrift(?:en)?\s+efter\s+§`)

// nextProvision is the provision after p in the law's own series, or nil.
func (c *Content) nextProvision(p *Provision) *Provision {
	refs := c.Provisions()
	for i, r := range refs {
		if r.Provision == p && i+1 < len(refs) {
			return refs[i+1].Provision
		}
	}
	return nil
}

// reHeadingAndProvision is a change to a heading and the provision under it at once: "Overskriften
// før § 7 og § 7 ophæves, og i stedet indsættes".
var reHeadingAndProvision = regexp.MustCompile(`(?i)overskrift(?:en)?\s+(?:før|til|over)\s+§\s*([0-9]+\s*[a-zæøå]?)\s*,?\s+og\s+§\s*([0-9]+\s*[a-zæøå]?)\s*,?\s+(?:ophæves|affattes)`)

// NamesHeadingAndProvision reports a change to a heading and the provision under it at once, whose
// new text is a provision with its heading.
func NamesHeadingAndProvision(definition string) bool {
	return reHeadingAndProvision.MatchString(definition)
}

// reItemSpan is a run of conditions written as a range: "nr. 1-3".
var reItemSpan = regexp.MustCompile(`(?i)\bnr\.?\s*([0-9]+)\s*[-–]\s*([0-9]+)\b`)

// itemRange is each condition of "nr. 1-3 ophæves", in the act's numbering, or the address alone
// when the sentence names no range of conditions. Read as one address the range repealed nr. 1 and
// left nr. 2 and 3 standing.
func itemRange(a Address, definition string) []Address {
	head := blankQuoted(definition)
	if m := reOperativeVerb.FindStringIndex(head); m != nil && !strings.Contains(strings.ToLower(head[m[0]:m[1]]), "ophæves") {
		head = head[:m[0]]
	}
	m := reItemSpan.FindStringSubmatch(head)
	if m == nil || a.Nr != Atoi(m[1]) {
		return []Address{a}
	}
	from, to := Atoi(m[1]), Atoi(m[2])
	if to <= from || to-from > 40 {
		return []Address{a}
	}
	var out []Address
	for n := from; n <= to; n++ {
		b := a
		b.Nr, b.NrSuffix = n, ""
		out = append(out, b)
	}
	return out
}

// bareFigures takes off the unit a change's old and new wording both end in, »5,34 kr.« and
// »5,18 kr.«, when what is left of the old wording is a figure.
func bareFigures(old, new string) (string, string, bool) {
	o, n := strings.Fields(QuoteStrip(old)), strings.Fields(QuoteStrip(new))
	if len(o) != 2 || len(n) != 2 || o[1] != n[1] || strings.ContainsAny(o[1], "0123456789") {
		return "", "", false
	}
	if !reFigure.MatchString(o[0]) || !reFigure.MatchString(n[0]) {
		return "", "", false
	}
	return o[0], n[0], true
}

var reFigure = regexp.MustCompile(`^[0-9][0-9.,]*[0-9]$`)

// figureOnce reports a figure the text prints exactly once, standing alone rather than as part
// of a longer number.
func figureOnce(text, fig string) bool {
	re := regexp.MustCompile(`(^|[^0-9.,])` + regexp.QuoteMeta(fig) + `($|[^0-9]|[.,][^0-9])`)
	return len(re.FindAllStringIndex(text, -1)) == 1 && countMatches(text, fig) == 1
}

// looseUnit is the text a location names, for the matching that tolerates the act's spelling: a
// provision's unit, or an annex printed as text.
func (l Located) looseUnit() ([]Segment, Span, bool) {
	if sp, ok := l.unitSpan(); ok {
		return l.Provision.Segments, sp, true
	}
	if l.Appendix != nil && len(l.Appendix.Order) == 0 && len(l.Appendix.Segments) > 0 {
		return l.Appendix.Segments, Span{From: 0, To: len(l.Appendix.Segments)}, true
	}
	return nil, Span{}, false
}

// reAnnexParagraphLine is a provision's number printed as a line of an annex: "§ 11.", "§ 14".
var reAnnexParagraphLine = regexp.MustCompile(`^§[\s\x{a0}]*([0-9]+)(?:[\s\x{a0}]*([a-zæøå]))?\.?(?:[\s\x{a0}]|$)`)

// applyInAnnexProvision carries out a change to a provision an annex prints as text: the statutes
// of an institution, set out § by § inside its annex. The provision is read from the annex's lines,
// from its number to the next provision or chapter, the change is carried out on it as on any
// provision, and the result is put back in its place.
func (c *Content) applyInAnnexProvision(e Edit, ap *Appendix, out *Consolidation, reject func(string)) bool {
	want := strings.ToLower(e.Address.Paragraph)
	start := -1
	for i, sg := range ap.Segments {
		m := reAnnexParagraphLine.FindStringSubmatch(Squeeze(sg.Text))
		if m == nil || strings.ToLower(m[1]+m[2]) != want {
			continue
		}
		if start >= 0 {
			return false // printed more than once: not the annex's own provisions
		}
		start = i
	}
	if start < 0 {
		return false // the annex names a § it quotes, not one it prints
	}
	end := len(ap.Segments)
	for i := start + 1; i < len(ap.Segments); i++ {
		t := Squeeze(ap.Segments[i].Text)
		if reAnnexParagraphLine.MatchString(t) || reChapterLead.MatchString(t) {
			end = i
			break
		}
	}
	// Chapter headings printed just before the next provision belong to it, not to this one.
	for end-1 > start && isRubric(Squeeze(ap.Segments[end-1].Text)) && end < len(ap.Segments) &&
		reAnnexParagraphLine.MatchString(Squeeze(ap.Segments[end].Text)) && !reStkLabelLead.MatchString(ap.Segments[end-1].Text) {
		end--
	}
	m := reAnnexParagraphLine.FindStringSubmatch(Squeeze(ap.Segments[start].Text))
	// "Stk. 4" and its stop are printed as two lines; together they are the label.
	var lines []Segment
	for _, sg := range cloneSegments(ap.Segments[start:end]) {
		if n := len(lines); n > 0 && strings.TrimSpace(sg.Text) == "." && (reStkLabel.MatchString(Squeeze(lines[n-1].Text)) || reAnnexParagraphLine.MatchString(Squeeze(lines[n-1].Text))) {
			lines[n-1].Text = strings.TrimRight(lines[n-1].Text, " ") + "."
			continue
		}
		lines = append(lines, sg)
	}
	p := &Provision{Number: m[1], Litra: m[2], Printed: Squeeze(ap.Segments[start].Text), Segments: lines}
	p.Normalise()
	view := &Content{Order: []Node{{Provision: p}}}
	one := e
	one.Address.Bilag = 0
	sub := &Consolidation{}
	view.applyOne(one, sub)
	where := fmt.Sprintf("bilag %s, ", ap.Number)
	for _, a := range sub.Applied {
		a.Where = where + a.Where
		out.Applied = append(out.Applied, a)
	}
	for _, u := range sub.Unresolved {
		u.Reason = where + u.Reason
		out.Unresolved = append(out.Unresolved, u)
	}
	out.NoOpEdits += sub.NoOpEdits
	if len(sub.Applied) == 0 {
		return true
	}
	var segs []Segment
	for _, n := range view.Order {
		if n.Provision != nil {
			segs = append(segs, n.Provision.Segments...)
		}
	}
	kept := make([]Segment, 0, len(ap.Segments)-(end-start)+len(segs))
	kept = append(kept, ap.Segments[:start]...)
	kept = append(kept, segs...)
	kept = append(kept, ap.Segments[end:]...)
	ap.Segments = kept
	return true
}

// reStkLabel is a subsection label without its stop: "Stk. 4".
var reStkLabel = regexp.MustCompile(`(?i)^stk\.[\s\x{a0}]*[0-9]+$`)

// reStykkeRelabel is a correction of a subsection's printed number: "I § 25 ændres nummeret på
// stykket efter stk. 2 til: »3«. Stk. 3 bliver herefter stk. 4."
var reStykkeRelabel = regexp.MustCompile(`(?i)ændres\s+nummeret\s+på\s+stykket\s+efter\s+stk\.\s*([0-9]+)\s+til\s*:?\s*»\s*([0-9]+)\s*«`)

// applyStykkeLabel corrects printed subsection numbers the publisher got wrong: the subsection after
// the named one takes the number the change gives it, and the ones after it follow by position, as
// the change's own "Stk. 3 bliver herefter stk. 4" says.
func (c *Content) applyStykkeLabel(e Edit, out *Consolidation) bool {
	m := reStykkeRelabel.FindStringSubmatch(e.Definition)
	if m == nil {
		return false
	}
	loc, ok := c.Find(Address{Recognised: true, Paragraph: ParseAddress(e.Definition).Paragraph})
	after := Atoi(m[1])
	if !ok || loc.Provision == nil || after < 1 || after >= len(loc.Provision.Stykker) || Atoi(m[2]) != after+1 {
		out.Unresolved = append(out.Unresolved, UnresolvedEdit{Edit: e, Reason: "the subsection the change renumbers is not in the current text"})
		return true
	}
	loc.Provision.relabelStykker()
	out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: loc.String(), Note: "printed subsection numbers corrected to follow their order"})
	return true
}

// carries reports whether a tentative act still has work to do on this text: tried on a copy, most
// of its changes change the text, rather than finding it reads as they have it already or finding
// nothing to change. An act already in the text finds its new wording in place, and applying it
// again would insert its new provisions and words a second time.
func (c *Content) carries(am Amendment, day time.Time, sameDay bool) bool {
	trial := c.Clone()
	trial.carried = true
	trial.pinNumbering()
	if !sameDay {
		trial.pinDay()
	}
	out := &Consolidation{}
	for _, e := range am.Edits {
		trial.applyOne(e, out)
	}
	// Only a change that quotes wording can tell: "§ 8 a ophæves" changes whatever the text holds.
	quoting := map[string]bool{}
	for _, e := range am.Edits {
		if (e.Old != "" || InsertionAnchor(e) != "") && !(citesOnly(e.Old) && citesOnly(e.New)) {
			quoting[e.Number] = true
		}
	}
	changed, read, missed := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, a := range out.Applied {
		switch {
		case strings.Contains(a.Note, "already reads") && quoting[a.Edit.Number]:
			read[a.Edit.Number] = true
		case quoting[a.Edit.Number]:
			changed[a.Edit.Number] = true
		}
	}
	for _, u := range out.Unresolved {
		if quoting[u.Edit.Number] {
			missed[u.Edit.Number] = true
		}
	}
	// A new provision is evidence too: "Efter § 118 indsættes: § 118 a. …" has been made where the
	// text prints a § 118 a, and not where it prints none. So is a unit rewritten in long wording,
	// which a text that carries the rewrite prints. It decides a late part, and an early one only
	// when nothing quoted does.
	structRead, structChanged := map[string]bool{}, map[string]bool{}
	for _, e := range am.Edits {
		if quoting[e.Number] {
			continue
		}
		read, changed := structRead, structChanged
		switch {
		case e.Kind == EditInsertProvision && len(e.NewProvisions)+len(e.NewParts) > 0 && e.Address.Paragraph != "":
			// "Efter § 431 indsættes i afsnit IV: Kapitel 15 a … § 440": the provisions a new
			// chapter holds as much as the provisions brought alone.
			for _, p := range append(slices.Clone(e.NewProvisions), partsProvisions(e.NewParts)...) {
				if p.Number == "" {
					continue
				}
				if _, ok := c.Find(Address{Recognised: true, Paragraph: p.Number + p.Litra}); ok {
					read[e.Number] = true
				} else {
					changed[e.Number] = true
				}
			}
		case e.Kind == EditReplaceProvision && !e.Address.Heading && e.Address.Paragraph != "":
			text := MatchText(stripLeadLabel(e.blockString()))
			if len([]rune(text)) < 60 {
				continue
			}
			loc, ok := c.Find(e.Address)
			if !ok {
				continue
			}
			segs, sp, ok := loc.looseUnit()
			if !ok {
				continue
			}
			// A note may print the wording a change still to come gives the unit: "Det følger af
			// § 3, nr. 1, i lov nr. 1518 af 27. december 2009, at § 52 a, stk. 1, affattes således".
			if strings.Contains(MatchText(ownText(segs, sp)), text) {
				read[e.Number] = true
			} else {
				changed[e.Number] = true
			}
		}
	}
	if am.Late || len(changed)+len(read)+len(missed) == 0 {
		maps.Copy(read, structRead)
		maps.Copy(changed, structChanged)
	}
	for n := range read {
		delete(changed, n)
		delete(missed, n)
	}
	for n := range changed {
		delete(missed, n)
	}
	if len(changed)+len(read)+len(missed) == 0 {
		// Nothing to judge by: whether most of it could be made.
		return len(out.Applied) > len(out.Unresolved)
	}
	if am.Late && len(changed) > 0 {
		// A late part takes effect after the base was signed: an even count is no evidence the
		// base prints it.
		return len(changed) >= len(read)+len(missed)
	}
	return len(changed) > len(read)+len(missed)
}

// unitHolds reports a unit printing the wording, whatever the case of its letters.
func (l Located) unitHolds(wording string) bool {
	segs, sp, ok := l.looseUnit()
	if !ok {
		return false
	}
	return strings.Contains(strings.ToLower(MatchText(SegmentsText(segs[sp.From:sp.To]))), strings.ToLower(MatchText(QuoteStrip(wording))))
}

// longWording is new wording long enough to name its place by itself: a provision printing it,
// and nowhere the words it replaces, has had the change made, as when two acts of the same day
// make the same one.
func longWording(new string) bool {
	return len([]rune(MatchText(QuoteStrip(new)))) >= 30 && !citesOnly(new)
}

// reAmount is a figure with decimals, "47,9" or "0,67", which no reference prints.
var reAmount = regexp.MustCompile(`\d,\d`)

// citesOnly reports wording that is nothing but references, »nr. 5« or »§ 23, stk. 2, nr. 9 eller
// 10«: a text holds such wording in many places whether or not a change reached it.
func citesOnly(s string) bool {
	if reAmount.MatchString(s) {
		// »47,9« to »52,2«: a rate, which a text carrying the change prints in place of the old.
		return false
	}
	for _, w := range strings.FieldsFunc(strings.ToLower(QuoteStrip(s)), func(r rune) bool { return !unicode.IsLetter(r) }) {
		switch w {
		case "nr", "stk", "pkt", "og", "eller", "jf", "litra", "til", "samt":
		default:
			return false
		}
	}
	return true
}

// provisionReads reports a provision that prints a change's new wording and no longer its old: the
// change is in the text already.
func (l Located) provisionReads(new, old string) bool {
	if l.Provision == nil {
		return false
	}
	text := MatchText(l.Provision.Text())
	w := MatchText(QuoteStrip(new))
	return len([]rune(w)) >= 3 && strings.Contains(text, w) && !strings.Contains(text, MatchText(QuoteStrip(old)))
}

// extendedAlready reports a unit that prints the new wording where the new wording only adds to the
// old: »samtale« becoming »jobsamtale«, with "jobsamtale" in the text and "samtale" nowhere on its
// own. Whatever the length, the change has nothing left to do.
func (l Located) extendedAlready(old, new string) bool {
	sp, ok := l.unitSpan()
	if !ok {
		return false
	}
	o, n := MatchText(QuoteStrip(old)), MatchText(QuoteStrip(new))
	if o == "" || len([]rune(n)) <= len([]rune(o)) || !strings.Contains(n, o) {
		return false
	}
	text := l.Provision.SpanText(sp)
	return strings.Contains(MatchText(text), n) && countMatches(text, old) == 0
}

// reRepealedStub is a provision printed only to say it is gone: "§§ 86-87. (Ophævet)", "§ 12.
// (Ophævet)".
var reRepealedStub = regexp.MustCompile(`^§§?[\s\x{a0}]*([0-9]+)(?:[\s\x{a0}]*[-–][\s\x{a0}]*([0-9]+))?[\s\x{a0}]*\.?[\s\x{a0}]*\(Ophævet\)\.?$`)

// dropReusedStubs takes away the mark of a repeal once an act has given its numbers to new
// provisions: "§§ 86-87. (Ophævet)" left beside a new § 86 and § 87 was what "§ 86, stk. 6"
// found, and every later change to the new provisions was refused.
func (c *Content) dropReusedStubs() {
	live := map[string]bool{}
	var stubs []*Provision
	for _, r := range c.Provisions() {
		p := r.Provision
		if reRepealedStub.MatchString(Squeeze(p.Text())) {
			stubs = append(stubs, p)
			continue
		}
		live[strings.ToLower(strings.ReplaceAll(p.Number, " ", ""))] = true
	}
	for _, p := range stubs {
		m := reRepealedStub.FindStringSubmatch(Squeeze(p.Text()))
		from, to := Atoi(m[1]), Atoi(m[1])
		if m[2] != "" {
			to = Atoi(m[2])
		}
		if to < from || to-from > 50 {
			continue
		}
		for from <= to && live[itoa(from)] {
			from++
		}
		for to >= from && live[itoa(to)] {
			to--
		}
		switch {
		case from > to:
			c.DeleteProvision(p)
		case from == Atoi(m[1]) && to == Atoi(m[2]) || m[2] == "" && from == Atoi(m[1]):
			// untouched
		default:
			text := "§ " + itoa(from) + ". (Ophævet)"
			p.Printed = "§ " + itoa(from) + "."
			if to > from {
				text = "§§ " + itoa(from) + "-" + itoa(to) + ". (Ophævet)"
				p.Printed = "§§ " + itoa(from) + "-" + itoa(to) + "."
			}
			p.Number, p.Segments, p.Stykker = itoa(from), []Segment{{Text: text}}, nil
		}
	}
}

// reAnnexSectionPart is a change to text an annex prints under a heading of its own, often the
// wording of a provision it explains: "Bilag 2 i afsnittet Bestemmelsens ordlyd - § 35, stk. 2,
// nr. 4, affattes således", "I bilag 2 i afsnittet Bestemmelsens ordlyd - § 42 affattes stk. 2,
// således". Groups: the heading, a subsection and a condition before the verb, a subsection after.
var reAnnexSectionPart = regexp.MustCompile(`(?i)\bi\s+afsnittet\s+(.+?)\s*(?:,\s*stk\.\s*([0-9]+))?\s*(?:,\s*nr\.\s*([0-9]+))?\s*,?\s+affattes\s+(?:stk\.\s*([0-9]+)\s*,?\s+)?således`)

// reAnnexSectionEnd is where an annex's headed section ends: the next heading of its kind, or a
// numbered point of the annex's own: "73.".
var reAnnexSectionEnd = regexp.MustCompile(`^[0-9]+\.$`)

// replaceInAnnexSection replaces a subsection or a condition of the text an annex prints under a
// heading. The heading is found by its words, the part by the label it prints at the head of a line.
func (c *Content) replaceInAnnexSection(e Edit, t Located, m []string, reject func(string), apply func(where, note string)) {
	ap := t.Appendix
	heading, stk, nr := strings.TrimSpace(m[1]), m[2], m[3]
	if m[4] != "" {
		stk = m[4]
	}
	segs := ap.Segments
	start := -1
	for _, cand := range []string{heading + ", stk. " + stk, heading} {
		if strings.HasSuffix(cand, ", stk. ") {
			continue
		}
		for i, sg := range segs {
			if alnumKey(sg.Text) == alnumKey(cand) {
				if start >= 0 && start != i {
					reject(fmt.Sprintf("%s prints the heading %q more than once", t, cand))
					return
				}
				start = i
			}
		}
		if start >= 0 {
			if cand != heading {
				stk = "" // the heading names the subsection already
			}
			break
		}
	}
	if start < 0 {
		reject(fmt.Sprintf("%s prints no heading %q", t, heading))
		return
	}
	end := len(segs)
	first := strings.Fields(strings.ToLower(Squeeze(segs[start].Text)))[0]
	for i := start + 1; i < len(segs); i++ {
		tx := Squeeze(segs[i].Text)
		if reAnnexSectionEnd.MatchString(tx) || strings.HasPrefix(strings.ToLower(tx), first+" ") && alnumKey(tx) != alnumKey(segs[start].Text) && len([]rune(tx)) < 80 {
			end = i
			break
		}
	}
	from, to := start+1, end
	if stk != "" {
		from, to = -1, end
		for i := start + 1; i < end; i++ {
			if f := reStykkeAt.FindStringSubmatch(Squeeze(segs[i].Text)); f != nil {
				if f[1] == stk {
					from = i
				} else if from >= 0 {
					to = i
					break
				}
			}
		}
		if from < 0 {
			reject(fmt.Sprintf("the section headed %q in %s prints no stk. %s", heading, t, stk))
			return
		}
	}
	if nr != "" {
		at := -1
		for i := from; i < to; i++ {
			if opensWith(segs[i].Text, nr+")") {
				if at >= 0 {
					reject(fmt.Sprintf("the section headed %q in %s prints %s) more than once", heading, t, nr))
					return
				}
				at = i
			}
		}
		if at < 0 {
			reject(fmt.Sprintf("the section headed %q in %s prints no %s)", heading, t, nr))
			return
		}
		next := at + 1
		for next < to && !reItemAt.MatchString(Squeeze(segs[next].Text)) && reStykkeAt.FindStringSubmatch(Squeeze(segs[next].Text)) == nil {
			next++
		}
		from, to = at, next
	}
	repl := withoutOuterQuotes(e.blockText())
	if len(repl) == 0 {
		reject("the change brings no new text")
		return
	}
	out := append(append(append([]Segment(nil), segs[:from]...), repl...), segs[to:]...)
	ap.Segments = out
	apply(t.String(), "the text the annex prints under the heading the change names")
}

var (
	reStykkeAt = regexp.MustCompile(`^[”"»]?Stk\.?\s*([0-9]+)\b`)
	reItemAt   = regexp.MustCompile(`^[0-9]+\)`)
)

// replaceChapterHeadingWith carries out "Overskriften til kapitel 6 ophæves, og i stedet indsættes:
// Kapitel 6 Henstand ved ophør af skattepligt § 17. … § 18. …": the chapter gets the new heading,
// and the provisions printed under it take the places of the repealed ones whose numbers they
// reuse. Read as a heading alone, virksomhedsskatteloven's new §§ 17 and 18 became one long
// rubric, and every later change to them was refused. A number still in force is not taken
// over: what the act means by it is not something this reading can tell.
func (c *Content) replaceChapterHeadingWith(e Edit, t Located, reject func(string), apply func(where, note string)) {
	refs := e.NewParts[0].provisions(nil)
	stubs := make([]*Provision, len(refs))
	for i, r := range refs {
		loc, ok := c.Find(Address{Recognised: true, Paragraph: r.Provision.Number + r.Provision.Litra})
		if !ok || loc.Provision == nil || !reRepealedStub.MatchString(Squeeze(loc.Provision.Text())) {
			reject(fmt.Sprintf("the new chapter heading brings § %s%s, which the law holds other than as a repealed provision", r.Provision.Number, r.Provision.Litra))
			return
		}
		stubs[i] = loc.Provision
	}
	for i, r := range refs {
		with := cloneProvision(*r.Provision)
		if reChapterLead.MatchString(strings.TrimSpace(with.Heading)) {
			with.Heading = ""
		}
		if !c.ReplaceContent(stubs[i], with) {
			reject(fmt.Sprintf("could not set § %s", r.Provision.Number))
			return
		}
	}
	if h := e.NewParts[0].Heading; h != "" {
		t.Part.Heading = h
	}
	apply(t.String(), "the new heading brings provisions, which take the places of the repealed ones they renumber")
}

// stripLeadLabel takes the printed label off new text: "Stk. 4. Rådighedsbeløbet …" is compared by
// its words, which a text printing the subsection under another number holds the same.
func stripLeadLabel(s string) string {
	s = strings.TrimSpace(s)
	if m := reStkLabelLead.FindStringIndex(s); m != nil {
		return s[m[1]:]
	}
	if m := reProvisionLead.FindStringIndex(s); m != nil {
		return s[m[1]:]
	}
	return s
}

// renumbering is a provision as it stood before an act inserted or removed one of its subsections.
type renumbering struct {
	Source    string
	Effective time.Time
	Before    *Provision
}

// renumberSnapshot is the provision a change inserting or removing a whole subsection is about to
// renumber, as it stands, or nil.
func (c *Content) renumberSnapshot(e Edit) *Provision {
	a := e.Address
	if (e.Kind != EditInsertProvision && e.Kind != EditDeleteProvision) || a.Paragraph == "" || a.Stk == 0 || a.Sentence != 0 || a.Nr != 0 || a.Heading {
		return nil
	}
	loc, ok := c.Find(Address{Recognised: true, Paragraph: a.Paragraph})
	if !ok || loc.Provision == nil {
		return nil
	}
	p := cloneProvision(*loc.Provision)
	return &p
}

// recordRenumbering keeps the snapshot when the change did change the number of subsections.
func (c *Content) recordRenumbering(e Edit, snap *Provision, am Amendment) {
	if snap == nil {
		return
	}
	loc, ok := c.Find(Address{Recognised: true, Paragraph: e.Address.Paragraph})
	if !ok || loc.Provision == nil || len(loc.Provision.Stykker) == len(snap.Stykker) {
		return
	}
	if c.renumbered == nil {
		c.renumbered = map[string][]renumbering{}
	}
	c.renumbered[e.Address.Paragraph] = append(c.renumbered[e.Address.Paragraph], renumbering{Source: am.Source, Effective: am.Effective, Before: snap})
}

// staleNumbering finds the provisions an act numbers as they stood before another act renumbered
// their subsections, and pins them to that numbering for the act. Opkrævningsloven § 16 c had a new
// stk. 2 from lov nr. 322 of 5 April 2016; lov nr. 428 of 18 May 2016 still called stk. 4 "stk. 3"
// and stk. 5 "stk. 4", and the ministry's next consolidation made its changes there. Read in the
// numbering after lov nr. 322, two of them were refused and the third put a rule on credit balances
// into the subsection on overdue debts. An act renumbering the provision within the year before is
// tried: the act's changes to the provision are made on a copy in the numbering before it, and that
// numbering is taken when every one of them is made there and not every one is made in the
// numbering as it stands. It reports the provisions pinned, and the act each one was numbered
// before.
func (c *Content) staleNumbering(am Amendment, sameDay bool) map[string]string {
	out := map[string]string{}
	if len(c.renumbered) == 0 {
		return out
	}
	byPara := map[string][]int{}
	for i, e := range am.Edits {
		a := e.Address
		if a.Everywhere || a.Paragraph == "" || a.Stk == 0 || len(c.renumbered[a.Paragraph]) == 0 {
			continue
		}
		byPara[a.Paragraph] = append(byPara[a.Paragraph], i)
	}
	for para, idx := range byPara {
		loc, ok := c.Find(Address{Recognised: true, Paragraph: para})
		if !ok || loc.Provision == nil {
			continue
		}
		snaps := c.renumbered[para]
		for k := len(snaps) - 1; k >= 0; k-- {
			r := snaps[k]
			if r.Source == am.Source || !r.Effective.Before(am.Effective) || am.Effective.Sub(r.Effective) > 400*24*time.Hour {
				continue
			}
			was, ok := oldNumbers(r.Before, loc.Provision)
			if !ok {
				continue
			}
			if c.trialRefusals(am, idx, para, nil, sameDay) == 0 || c.trialRefusals(am, idx, para, was, sameDay) != 0 {
				continue
			}
			setWas(loc.Provision, was)
			out[para] = r.Source
			break
		}
	}
	return out
}

// oldNumbers maps each subsection of now to the number it had in before, found by its words, and 0
// for one before did not have. False when a subsection of before is not in exactly one of now.
func oldNumbers(before, now *Provision) (map[int]int, bool) {
	was := map[int]int{}
	for _, st := range before.Stykker {
		key := []rune(MatchText(stripLeadLabel(before.SpanText(st.Span))))
		if len(key) > 60 {
			key = key[:60]
		}
		if len(key) < 12 {
			return nil, false
		}
		found, n := 0, 0
		for _, cur := range now.Stykker {
			if strings.HasPrefix(MatchText(stripLeadLabel(now.SpanText(cur.Span))), string(key)) {
				found++
				n = cur.Nr
			}
		}
		if found != 1 {
			return nil, false
		}
		was[n] = st.Nr
	}
	return was, true
}

// setWas pins a provision's subsections to the numbers in was, and the ones it lacks to none.
func setWas(p *Provision, was map[int]int) {
	for i := range p.Stykker {
		p.Stykker[i].Was = was[p.Stykker[i].Nr]
	}
}

// trialRefusals is how many of an act's changes, those at idx, a copy of the text refuses, with the
// provision para pinned to the numbering was when it is given.
func (c *Content) trialRefusals(am Amendment, idx []int, para string, was map[int]int, sameDay bool) int {
	trial := c.Clone()
	trial.pinNumbering()
	if !sameDay {
		trial.pinDay()
	}
	if was != nil {
		loc, ok := trial.Find(Address{Recognised: true, Paragraph: para})
		if !ok || loc.Provision == nil {
			return len(idx)
		}
		setWas(loc.Provision, was)
	}
	out := &Consolidation{}
	for _, i := range idx {
		trial.applyOne(am.Edits[i], out)
	}
	return len(out.Unresolved)
}

// joinNote adds a sentence to an applied change's note.
func joinNote(note, add string) string {
	if note == "" {
		return add
	}
	return note + "; " + add
}

// reLastSentence is a sentence counted from the end of its unit: "sidste pkt.", "næstsidste pkt.",
// "de to sidste pkt.".
var reLastSentence = regexp.MustCompile(`(?i)\b(de\s+to\s+sidste|næstsidste|sidste)\s+(?:pkt\.?|punktum)`)

// lastSentence reads a sentence counted from the end of its unit as the number it has there, in the
// address and in the wording that is read again later. "§ 2, stk. 1, sidste pkt., ophæves" read
// without it named all of stk. 1, and the subsection went with its last sentence. named reports a
// change that counts from the end at all; ok, that the unit it names is there to count in.
func (c *Content) lastSentence(e Edit) (resolved Edit, ok, named bool) {
	head := e.Definition
	if i := strings.Index(head, "»"); i >= 0 {
		head = head[:i]
	}
	m := reLastSentence.FindStringSubmatchIndex(head)
	if m == nil || e.Address.Sentence != 0 || e.Address.Paragraph == "" {
		return e, false, false
	}
	loc, found := c.Find(e.Address)
	if !found {
		return e, false, true
	}
	text, _, has := loc.unitText()
	n := len(sentenceSpans(text))
	if !has || n == 0 {
		return e, false, true
	}
	from, to := n, 0
	switch strings.ToLower(strings.Join(strings.Fields(head[m[2]:m[3]]), " ")) {
	case "næstsidste":
		from = n - 1
	case "de to sidste":
		from, to = n-1, n
	}
	if from < 1 {
		return e, false, true
	}
	number := itoa(from) + ". pkt."
	if to != 0 {
		number = itoa(from) + ". og " + itoa(to) + ". pkt."
	}
	e.Definition = e.Definition[:m[0]] + number + e.Definition[m[1]:]
	e.Address.Sentence, e.Address.SentenceTo = from, to
	return e, true, true
}

// reLabelRenumber is a run of lettered or numbered parts renumbered: "Litra b–c bliver herefter
// litra a–b", "Nr. 4 og 5 bliver herefter nr. 3 og 4".
var reLabelRenumber = regexp.MustCompile(`(?i)\b(litra|nr\.|pkt\.|punkt(?:erne)?)\s*([0-9]+|[a-zæøå])(?:\s*(?:-|–|og)\s*([0-9]+|[a-zæøå]))?\s+bliver\s+herefter\s+(?:til\s+)?(?:litra|nr\.|pkt\.|punkt(?:erne)?)\s*([0-9]+|[a-zæøå])(?:\s*(?:-|–|og)\s*([0-9]+|[a-zæøå]))?\b`)

// relabelAnnexParts carries out a renumbering of an annex's parts, which print their labels: "Bilag
// 1, nr. 18, litra a, ophæves. Litra b–c bliver herefter litra a–b.", "I bilag 1, A, indsættes efter
// nr. 4 som nyt nummer: … Nr. 5 og 6 bliver herefter nr. 6 og 7." The renumbering names no place of
// its own, so it is where the change's first sentence is, whatever § the block lent it. It is carried
// out only where the change it follows has been: moving up, the first label it moves is printed
// twice, by the new part and the old; moving down, the label the first one takes is free. It reports
// false when the change is not one.
func (c *Content) relabelAnnexParts(e Edit, reject func(string), apply func(where, note string)) bool {
	m := reLabelRenumber.FindStringSubmatch(Squeeze(e.Definition))
	if m == nil {
		return false
	}
	// The place is what the change says before the renumbering's own sentence.
	first := reLeadNumber.ReplaceAllString(Squeeze(e.Prose), "")
	if k := strings.Index(first, Squeeze(e.Definition)); k > 0 {
		first = strings.TrimSpace(first[:k])
	} else if loc := reSentenceEnd.FindStringIndex(first); loc != nil {
		first = first[:loc[0]+1]
	}
	ch, ok := ParseAnnexPart(first)
	if !ok || len(ch.Targets) != 1 {
		return false
	}
	from, to := labelRun(m[2], m[3]), labelRun(m[4], m[5])
	if len(from) == 0 || len(from) != len(to) || from[0] == to[0] {
		return false
	}
	n := ch.Targets[0]
	rl := annexRelabel{annex: n, scopes: ch.Scopes, from: from, to: to, letters: strings.HasPrefix(strings.ToLower(m[1]), "litra")}
	if _, found := c.annexText(n); !found {
		return false
	}
	if reason := rl.run(c, false); reason != "" {
		reject(reason)
		return true
	}
	key := e.Source + "|" + e.Number + "|" + Squeeze(m[0])
	if c.relabelled == nil {
		c.relabelled = map[string]bool{}
	}
	if c.relabelled[key] {
		apply("bilag "+itoa(n), "the act states this renumbering twice; it is made once")
		return true
	}
	c.relabelled[key] = true
	// The labels change once the act's other changes, which cite the parts by their old labels,
	// are made.
	c.afterAct = append(c.afterAct, func() { rl.run(c, true) })
	apply("bilag "+itoa(n), "the parts are relabelled as the renumbering says, once the act's other changes are made")
	return true
}

// annexRelabel is a renumbering of an annex's parts: the labels from become those in to, inside the
// part the scopes name.
type annexRelabel struct {
	annex   int
	scopes  []string
	from    []int
	to      []int
	letters bool
}

// annexText is the annex numbered n when it is printed as text.
func (c *Content) annexText(n int) (*Appendix, bool) {
	for i := range c.Appendices {
		if FirstNumber(c.Appendices[i].Number+c.Appendices[i].Label) == itoa(n) {
			ap := &c.Appendices[i]
			return ap, len(ap.Order) == 0
		}
	}
	return nil, false
}

// run checks the renumbering against the annex, and with apply carries it out. It returns why it
// cannot be. Moving up, the first label it moves is printed twice, by the new part and the old, and
// the old is the later one; moving down, the label the first one takes is free.
func (r annexRelabel) run(c *Content, apply bool) string {
	ap, ok := c.annexText(r.annex)
	if !ok {
		return "bilag " + itoa(r.annex) + " is not printed as text"
	}
	within := Span{From: 0, To: len(ap.Segments)}
	for _, sc := range r.scopes {
		var sp Span
		var found bool
		if strings.HasPrefix(sc, "~") {
			sp, found = wordsScope(ap.Segments, within, strings.TrimPrefix(sc, "~"))
		} else {
			sp, found = findPart(ap.Segments, within, sc)
		}
		if !found {
			return "bilag " + itoa(r.annex) + " does not print the part the renumbering is in"
		}
		within = sp
	}
	up := r.from[0] < r.to[0]
	heads := func(k int) []int { return labelHeads(ap.Segments, within, k, r.letters) }
	switch {
	case up && len(heads(r.from[0])) < 2:
		return "bilag " + itoa(r.annex) + " does not print the new part the renumbering makes room for"
	case !up && len(heads(r.to[0])) > 0:
		return "bilag " + itoa(r.annex) + " still prints the part the renumbering moves into"
	}
	order := make([]int, len(r.from))
	for i := range order {
		order[i] = i
		if up {
			order[i] = len(r.from) - 1 - i
		}
	}
	for _, i := range order {
		at := heads(r.from[i])
		if len(at) == 0 || len(at) > 1 && !up {
			return "bilag " + itoa(r.annex) + " does not print the part the renumbering moves once"
		}
		if !apply {
			continue
		}
		k := at[len(at)-1]
		ap.Segments[k].Text = reLabelHead.ReplaceAllStringFunc(ap.Segments[k].Text, func(h string) string {
			sm := reLabelHead.FindStringSubmatch(h)
			return sm[1] + labelPrint(r.to[i], r.letters) + sm[3]
		})
	}
	return ""
}

// runAfterAct does what the act's changes left for its end.
func (c *Content) runAfterAct() {
	for _, f := range c.afterAct {
		f()
	}
	c.afterAct = nil
}

// relabelFromProse carries out the renumbering a change states after its own sentence, when the
// register gives that sentence no change of its own: "Bilag 2, pkt. 29, ophæves. Pkt. 30-76 bliver
// herefter pkt. 29-75." Left undone, a later act's "punkt 48" met the text of punkt 49.
func (c *Content) relabelFromProse(e Edit, out *Consolidation) {
	if e.Kind == EditRenumber || e.Address.Bilag == 0 || e.Prose == "" {
		return
	}
	prose := Squeeze(e.Prose)
	m := reLabelRenumber.FindString(prose)
	if m == "" || strings.Contains(Squeeze(e.Definition), m) {
		return
	}
	r := Edit{Source: e.Source, Number: e.Number, Kind: EditRenumber, Definition: m, Prose: e.Prose, Address: Address{Bilag: e.Address.Bilag, Recognised: true}}
	c.relabelAnnexParts(r, func(reason string) {
		out.Unresolved = append(out.Unresolved, UnresolvedEdit{Edit: r, Reason: reason})
	}, func(where, note string) {
		out.Applied = append(out.Applied, AppliedEdit{Edit: r, Where: where, Note: note})
	})
}

// reLabelHead is a part's label at the head of its line: "5.", "b)".
var reLabelHead = regexp.MustCompile(`^(\s*)([0-9]+|[a-zæøå])([).])`)

// labelHeads are the lines within that open with label k.
func labelHeads(segs []Segment, within Span, k int, letters bool) []int {
	var out []int
	for i := within.From; i < within.To && i < len(segs); i++ {
		sm := reLabelHead.FindStringSubmatch(segs[i].Text)
		if sm == nil {
			continue
		}
		if rest := strings.TrimSpace(segs[i].Text[len(sm[0]):]); len(rest) > 0 && unicode.IsDigit([]rune(rest)[0]) {
			continue // "5.1" is a part of 5, not 5
		}
		if sm[2] == labelPrint(k, letters) {
			out = append(out, i)
		}
	}
	return out
}

// labelPrint is ordinal k as a label prints it: "b", "5".
func labelPrint(k int, letters bool) string {
	if letters {
		return string(rune('a' + k - 1))
	}
	return itoa(k)
}

// labelRun is the labels from a to b: "b", "c" for "b–c"; 4, 5 for "4 og 5". Letters count as
// their place in the alphabet.
func labelRun(a, b string) []int {
	if b == "" {
		b = a
	}
	x, y := labelOrdinal(a), labelOrdinal(b)
	if x == 0 || y < x || y-x > 300 {
		return nil
	}
	var out []int
	for i := x; i <= y; i++ {
		out = append(out, i)
	}
	return out
}

func labelOrdinal(s string) int {
	if n := Atoi(s); n > 0 {
		return n
	}
	if r := []rune(strings.ToLower(s)); len(r) == 1 && r[0] >= 'a' && r[0] <= 'z' {
		return int(r[0]-'a') + 1
	}
	return 0
}

// reLeadNumber is a numbered change's own number in front of its text: "1. ".
var reLeadNumber = regexp.MustCompile(`^\s*[0-9]+\.\s+`)

// reNewAnnexInline is a new annex whose text the change itself brings: "Som bilag 1 til loven
// indsættes:", "Som nyt bilag 3 indsættes:".
var reNewAnnexInline = regexp.MustCompile(`(?i)^\s*som\s+(?:nyt\s+)?bilag\s+[0-9]+(?:\s+[a-z])?\s+(?:til\s+(?:loven|bekendtgørelsen|lovbekendtgørelsen)\s+)?indsættes\s*:?\s*$`)

// reRulesFor is the rules under a heading of one entry in an annex's section: "I bilag 1, i afsnit B.
// Det økonomisk-merkantile område, Erhvervsakademiuddannelsen inden for handel (Handelsøkonom AK)
// affattes reglerne for praktikken således:".
var reRulesFor = regexp.MustCompile(`(?i)\bbilag\s+([0-9]+)\s*,\s*i\s+afsnit\s+([A-Z])\.\s*([^,]+?)\s*,\s*(.+?\(([^()]+)\))\s+affattes\s+reglerne\s+for\s+(\p{L}+)\s+således`)

// replaceRulesFor carries out reRulesFor. The section is the one printing the entry, past the
// annex's table of contents; the entry is the line printing its name in parentheses, "(Handelsøkonom
// AK)", and runs to the next entry's "Uddannelsens navn:"; the rules are the lines under the heading
// the noun names, "Praktik:" for "praktikken", to the next heading or the entry's end.
func (c *Content) replaceRulesFor(e Edit, reject func(string), apply func(where, note string)) {
	m := reRulesFor.FindStringSubmatch(Squeeze(e.Definition))
	n := Atoi(m[1])
	ap, ok := c.annexText(n)
	if !ok {
		reject("the law has no bilag " + m[1] + " printed as text")
		return
	}
	segs := ap.Segments
	repl := e.blockText()
	if len(repl) == 0 {
		reject("the new rules are empty")
		return
	}
	// The entry: the one line printing its name in parentheses with "Uddannelsens navn:" under it.
	name := "(" + Squeeze(m[5]) + ")"
	entry := -1
	for i, sg := range segs {
		if strings.Contains(Squeeze(sg.Text), name) && i+1 < len(segs) && strings.EqualFold(strings.TrimSpace(segs[i+1].Text), "Uddannelsens navn:") {
			if entry >= 0 {
				reject("bilag " + m[1] + " prints " + name + " more than once")
				return
			}
			entry = i
		}
	}
	if entry < 0 {
		reject("bilag " + m[1] + " does not print " + name)
		return
	}
	// The entry sits in the section named: the nearest "B." above it is followed by the heading.
	sec := -1
	for i := entry - 1; i >= 0; i-- {
		if t := Squeeze(segs[i].Text); t == m[2]+"." || strings.HasPrefix(t, m[2]+". ") {
			sec = i
			break
		}
	}
	if sec < 0 || !strings.Contains(alnumKey(SegmentsText(segs[sec:min(sec+3, len(segs))])), alnumKey(m[3])) {
		reject("bilag " + m[1] + " does not print " + name + " in afsnit " + m[2])
		return
	}
	end := len(segs)
	for i := entry + 1; i < len(segs); i++ {
		if strings.EqualFold(strings.TrimSpace(segs[i].Text), "Uddannelsens navn:") && i-1 > entry {
			end = i - 1 // the next entry's own name line
			break
		}
	}
	noun := strings.ToLower(m[6])
	head := -1
	for i := entry + 1; i < end; i++ {
		t := strings.TrimSpace(segs[i].Text)
		if strings.HasSuffix(t, ":") && len(t) > 2 && strings.HasPrefix(noun, strings.ToLower(strings.TrimSuffix(t, ":"))) {
			if head >= 0 {
				reject("the entry prints more than one heading for " + noun)
				return
			}
			head = i
		}
	}
	if head < 0 {
		reject("the entry prints no heading for " + noun)
		return
	}
	stop := end
	for i := head + 1; i < end; i++ {
		if t := strings.TrimSpace(segs[i].Text); strings.HasSuffix(t, ":") && len([]rune(t)) < 60 && unicode.IsUpper([]rune(t)[0]) && !strings.Contains(t, " ") || reEntryHeading.MatchString(t) {
			stop = i
			break
		}
	}
	out := append([]Segment(nil), segs[:head+1]...)
	out = append(out, cloneSegments(withoutOuterQuotes(repl))...)
	ap.Segments = append(out, segs[stop:]...)
	apply("bilag "+m[1]+", afsnit "+m[2], "the rules under the heading "+strings.TrimSpace(segs[head].Text)+" of "+name)
}

// reEntryHeading is a heading of an annex entry's rules: "Praktik:", "Fagområder:", "Nationale og
// lokale uddannelseselementer:".
var reEntryHeading = regexp.MustCompile(`^\p{Lu}[\p{Ll}\- ]{2,60}:$`)

// reLastParagraphs is a repeal of an annex's closing paragraphs: "I bilag 10 ophæves de to sidste
// afsnit", "det sidste afsnit".
var reLastParagraphs = regexp.MustCompile(`(?i)^\s*i\s+bilag\s+([0-9]+)\s+ophæves\s+(det|de\s+(to|tre|[0-9]+))\s+sidste\s+afsnit\.?\s*$`)

// deleteLastParagraphs carries out reLastParagraphs where the annex says where its paragraphs end:
// bilag 10's last two are a sentence and a paragraph of two sentences, three lines in all.
func (c *Content) deleteLastParagraphs(e Edit, reject func(string), apply func(where, note string)) {
	m := reLastParagraphs.FindStringSubmatch(Squeeze(e.Definition))
	n := Atoi(m[1])
	k := 1
	switch strings.ToLower(m[3]) {
	case "":
	case "to":
		k = 2
	case "tre":
		k = 3
	default:
		k = Atoi(m[3])
	}
	ap, ok := c.annexText(n)
	if !ok {
		reject("the law has no bilag " + m[1] + " printed as text")
		return
	}
	var ends []int
	for i, sg := range ap.Segments {
		if sg.Para {
			ends = append(ends, i)
		}
	}
	if len(ends) == 0 || ends[len(ends)-1] != len(ap.Segments)-1 {
		reject("bilag " + m[1] + " does not say where its paragraphs end")
		return
	}
	if len(ends) <= k {
		reject("bilag " + m[1] + " has no more than " + itoa(len(ends)) + " paragraphs")
		return
	}
	ap.Segments = ap.Segments[:ends[len(ends)-1-k]+1]
	apply("bilag "+m[1], "")
}

// replaceLooseAfsnitHeading rewrites the one heading line in the law that opens with the afsnit's
// roman numeral and a capital: an afsnit the text never made a part of.
func (c *Content) replaceLooseAfsnitHeading(e Edit, apply func(where, note string)) bool {
	text := strings.TrimSpace(e.blockString())
	if text == "" {
		text = strings.TrimSpace(QuoteStrip(e.New))
	}
	if text == "" {
		return false
	}
	numeral := strings.ToUpper(RomanOf(e.Address.Afsnit))
	re := regexp.MustCompile(`^\s*` + numeral + `\.\s+\p{Lu}`)
	var hits []*string
	visit := func(segs []Segment) {
		for i := range segs {
			if segs[i].Note == "" && re.MatchString(segs[i].Text) && len([]rune(segs[i].Text)) < 200 {
				hits = append(hits, &segs[i].Text)
			}
		}
	}
	c.eachProvision(func(p *Provision) {
		if re.MatchString(p.Heading) {
			hits = append(hits, &p.Heading)
		}
		visit(p.Segments)
	})
	var parts func(nodes []Node)
	parts = func(nodes []Node) {
		for _, n := range nodes {
			if n.Part != nil {
				if re.MatchString(n.Part.Heading) {
					hits = append(hits, &n.Part.Heading)
				}
				parts(n.Part.Order)
			}
		}
	}
	parts(c.Order)
	if len(hits) != 1 {
		return false
	}
	if !re.MatchString(text) {
		text = numeral + ". " + text // the numeral the line prints stays
	}
	*hits[0] = text
	apply(e.Address.String(), "the text prints the afsnit only as a heading line; that line is rewritten")
	return true
}

// partsProvisions are the provisions parts hold, at any depth.
func partsProvisions(parts []*Part) []*Provision {
	var out []*Provision
	var walk func(nodes []Node)
	walk = func(nodes []Node) {
		for _, n := range nodes {
			if n.Provision != nil {
				out = append(out, n.Provision)
			}
			if n.Part != nil {
				walk(n.Part.Order)
			}
		}
	}
	for _, p := range parts {
		walk(p.Order)
	}
	return out
}

// reQuotedStykkeRepeal is a change to an amending act that repeals a subsection of a provision one
// of its own changes brings: "I § 1, nr. 14, ophæves § 11 i, stk. 2."
var reQuotedStykkeRepeal = regexp.MustCompile(`(?i)^\s*i\s+§\s*([0-9]+(?:\s?[a-zæøå])?),\s*nr\.\s*([0-9]+),\s*ophæves\s+§\s*([0-9]+(?:\s?[a-zæøå])?),\s*stk\.\s*([0-9]+)\s*\.?\s*$`)

// repealQuotedStykke removes subsection stk of the provision quoted in change nr of the act's
// provision section: from the line opening "Stk. 2." after the line opening "§ 11 i." to the next
// subsection, provision or the end of the change, keeping the quotation's closing mark.
func (c *Content) repealQuotedStykke(section string, nr int, quoted string, stk int) (string, string) {
	loc, ok := c.Find(Address{Recognised: true, Paragraph: section})
	if !ok || loc.Provision == nil {
		return "", "§ " + section + " is not in the current text"
	}
	p := loc.Provision
	var item *Item
	for i := range p.Stykker {
		for j := range p.Stykker[i].Items {
			if p.Stykker[i].Items[j].Nr == nr {
				if item != nil {
					return "", fmt.Sprintf("§ %s prints change %d twice", section, nr)
				}
				item = &p.Stykker[i].Items[j]
			}
		}
	}
	if item == nil {
		return "", fmt.Sprintf("§ %s has no change %d", section, nr)
	}
	head := regexp.MustCompile(`^»?§\s*` + regexp.QuoteMeta(strings.ReplaceAll(quoted, " ", "")) + `\.`)
	at := -1
	for i := item.Span.From; i < item.Span.To; i++ {
		if head.MatchString(strings.ReplaceAll(Squeeze(p.Segments[i].Text), " ", "")) {
			at = i
		}
	}
	if at < 0 {
		return "", fmt.Sprintf("change %d of § %s brings no § %s", nr, section, quoted)
	}
	from, to := -1, item.Span.To
	for i := at + 1; i < item.Span.To; i++ {
		t := Squeeze(p.Segments[i].Text)
		if n, ok := stkLabel(t); ok {
			if n == stk && from < 0 {
				from = i
				continue
			}
			if from >= 0 {
				to = i
				break
			}
		}
		if from >= 0 && strings.HasPrefix(t, "§") {
			to = i
			break
		}
	}
	if from < 0 {
		return "", fmt.Sprintf("the § %s change %d brings has no stk. %d", quoted, nr, stk)
	}
	closing := to == item.Span.To && strings.HasSuffix(strings.TrimSpace(p.Segments[to-1].Text), "«")
	if !p.ReplaceSpanContent(Span{From: from, To: to}, nil) {
		return "", "the subsection could not be removed"
	}
	if closing {
		p.Segments[from-1].Text = strings.TrimRight(p.Segments[from-1].Text, " ") + "«"
	}
	return fmt.Sprintf("§ %s, nr. %d, § %s, stk. %d", section, nr, quoted, stk), ""
}

// stkLabel is the subsection a line opens with, "Stk. 2." giving 2.
func stkLabel(text string) (int, bool) {
	m := reStkOpen.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	return Atoi(m[1]), true
}

var reStkOpen = regexp.MustCompile(`^»?Stk\.\s*([0-9]+)\.`)

// listedSection places a change of »§ 15« to »§ 15, stk. 1, 1. pkt., § 15, stk. 2,« in a text that
// cites the provision inside a list, "§§ 14 og 15": the number in the list takes the new wording
// without its sign, as the ministry's consolidation of hvidvaskloven printed it ("§§ 14 og 15,
// stk. 1, 1. pkt., § 15, stk. 2"). The new wording must open with the same provision, and the list
// must cite it once in the unit.
func listedSection(text, old, new string) (printed, replaced string, ok bool) {
	m := reBareSection.FindStringSubmatch(strings.TrimSpace(QuoteStrip(old)))
	if m == nil {
		return "", "", false
	}
	num := strings.ReplaceAll(m[1], " ", "")
	n := reBareSectionLead.FindStringSubmatch(strings.TrimSpace(QuoteStrip(new)))
	if n == nil || strings.ReplaceAll(n[1], " ", "") != num {
		return "", "", false
	}
	re := regexp.MustCompile(`§§[^§]*?(?:,|\bog|\beller)\s+` + regexp.QuoteMeta(m[1]) + `(?:[^0-9\p{L}]|$)`)
	hits := re.FindAllString(text, -1)
	if len(hits) != 1 {
		return "", "", false
	}
	printed = strings.TrimRightFunc(hits[0], func(r rune) bool { return !unicode.IsDigit(r) && !unicode.IsLetter(r) })
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(QuoteStrip(new)), n[0]))
	return printed, strings.TrimSpace(printed + rest), true
}

var (
	reBareSection     = regexp.MustCompile(`^§\s*([0-9]+(?:\s?[a-zæøå])?)$`)
	reBareSectionLead = regexp.MustCompile(`^§\s*([0-9]+(?:\s?[a-zæøå])?)\b`)
)

// insertAfterInlineItem puts new conditions after condition after of a subsection that prints its
// conditions inside one paragraph: the paragraph is split before the next condition's number, and
// the numbers printed after the insertion move up by the conditions inserted. after must be printed
// once, and the next number after it.
func insertAfterInlineItem(p *Provision, st *Stykke, after int, segs []Segment) (string, bool) {
	mark := func(n int) *regexp.Regexp {
		return regexp.MustCompile(`(^|\s)` + itoa(n) + `\)\s`)
	}
	at, hits := -1, 0
	for i := st.Span.From; i < st.Span.To; i++ {
		if p.Segments[i].Note != "" {
			continue
		}
		if k := len(mark(after).FindAllStringIndex(p.Segments[i].Text, -1)); k > 0 {
			at, hits = i, hits+k
		}
	}
	if hits != 1 {
		return "", false
	}
	text := p.Segments[at].Text
	from := mark(after).FindStringIndex(text)[1]
	next := mark(after + 1).FindStringIndex(text[from:])
	if next == nil {
		return "", false
	}
	cut := from + next[0]
	added := 0
	for _, sg := range segs {
		if reItemLabel.MatchString(strings.TrimSpace(sg.Text)) {
			added++
		}
	}
	if added == 0 {
		return "", false
	}
	tail := strings.TrimSpace(text[cut:])
	// The numbers printed after the insertion move up, from the last down.
	for n := 40; n > after; n-- {
		tail = mark(n).ReplaceAllString(tail, "${1}"+itoa(n+added)+") ")
	}
	head := Segment{Text: strings.TrimRight(text[:cut], " "), Break: true}
	rest := Segment{Text: tail, Break: true}
	repl := append(append([]Segment{head}, segs...), rest)
	if !p.ReplaceSpanContent(Span{From: at, To: at + 1}, repl) {
		return "", false
	}
	return fmt.Sprintf("§ %s%s, nr. %d", p.Number, p.Litra, after), true
}

// replaceInPart replaces a passage in the provisions a part holds, at any depth: the first
// occurrence, or every one ("Overalt i kapitel 25 ændres »bygningsklasse 2020« til:
// »lavenergiklasse«").
func replaceInPart(part *Part, old, new string, all bool) (found, changed bool) {
	var walk func(nodes []Node) bool
	walk = func(nodes []Node) bool {
		for _, n := range nodes {
			switch {
			case n.Provision != nil:
				p := n.Provision
				if all {
					if k := p.ReplaceEverywhereInSpan(Span{From: 0, To: len(p.Segments)}, old, new); k > 0 {
						found, changed = true, true
					}
					continue
				}
				if f, ch := replaceSpanOf(p, Span{From: 0, To: len(p.Segments)}, old, new); f {
					found, changed = true, ch
					return true
				}
			case n.Part != nil:
				if walk(n.Part.Order) {
					return true
				}
			}
		}
		return false
	}
	walk(part.Order)
	return found, changed
}

// superscripted is a quotation with the exponents the act typed as plain digits, "(…) 2 " and "m 2",
// printed as the text prints them, "²" and "³", when it then occurs once in the text.
func superscripted(text, old, new string) (string, string, bool) {
	if !strings.ContainsAny(text, "²³") {
		return "", "", false
	}
	up := func(s string) string {
		return strings.TrimSpace(reTypedExponent.ReplaceAllStringFunc(s, func(m string) string {
			sub := reTypedExponent.FindStringSubmatch(m)
			sup := map[string]string{"2": "²", "3": "³"}[sub[2]]
			return sub[1] + sup + sub[3]
		}))
	}
	o, n := up(QuoteStrip(old)), up(QuoteStrip(new))
	if o == strings.TrimSpace(QuoteStrip(old)) || countMatches(text, o) != 1 {
		return "", "", false
	}
	return o, n, true
}

// reTypedExponent is an exponent typed as a digit after a closing parenthesis or a unit: ") 2 ", "m 2".
var reTypedExponent = regexp.MustCompile(`(\)|\bm)\s*([23])(\s|$|[,.;])`)

// reTrailingCitation is a quotation ending in a provision it cites: "lov om skifte af fællesbo m.v. § 69".
var reTrailingCitation = regexp.MustCompile(`^(.*?\S)\s*§\s*[0-9]+(?:\s?[a-zæøå])?$`)

// retryCited carries out a refused change at the subsection the consolidation its act cites
// numbered as it does (Edit.CitedStykker), found by its opening words in the text as it stands, and
// reports whether that placed it.
func (c *Content) retryCited(u UnresolvedEdit, out *Consolidation) bool {
	cited := u.Edit.CitedStykker[u.Edit.Address.Paragraph]
	k := u.Edit.Address.Stk
	if k == 0 || k > len(cited) || len([]rune(cited[k-1])) < 40 {
		return false
	}
	loc, ok := c.Find(Address{Recognised: true, Paragraph: u.Edit.Address.Paragraph})
	if !ok || loc.Provision == nil {
		return false
	}
	n := 0
	want := []rune(cited[k-1])
	for i, open := range StykkeOpenings(loc.Provision) {
		// Forty letters: a later change may have rewritten the end of a subsection's first line.
		if o := []rune(open); len(o) >= 40 && string(o[:40]) == string(want[:40]) {
			if n != 0 {
				return false
			}
			n = i + 1
		}
	}
	if n == 0 || n == k {
		return false
	}
	alt := u.Edit
	alt.Address.Stk = n
	retry := &Consolidation{}
	c.applyAt(alt, retry)
	if len(retry.Applied) == 0 || len(retry.Unresolved) > 0 {
		return false
	}
	for _, a := range retry.Applied {
		if !strings.Contains(a.Where, fmt.Sprintf("stk. %d", n)) {
			return false // placed elsewhere than the subsection the citation names
		}
	}
	for _, a := range retry.Applied {
		a.Note = joinNote(a.Note, fmt.Sprintf("stk. %d in the consolidation the act cites is stk. %d here", k, n))
		out.Applied = append(out.Applied, a)
	}
	out.NoOpEdits += retry.NoOpEdits
	return true
}

// StykkeOpenings are a provision's subsections by their first sixty letters, without their label.
func StykkeOpenings(p *Provision) []string {
	var out []string
	for _, st := range p.Stykker {
		t := []rune(MatchText(reStykkeHeadLabel.ReplaceAllString(Squeeze(ownText(p.Segments, st.Span)), "")))
		out = append(out, string(t[:min(len(t), 60)]))
	}
	return out
}

// reStykkeHeadLabel is a subsection's printed label at its head, and its provision's.
var reStykkeHeadLabel = regexp.MustCompile(`^(?:§\s*[0-9]+\s*[a-zæøå]?\.\s*)?(?:Stk\.\s*[0-9]+\.\s*)?`)

// suspendedCompound is a quotation with a compound written out where the text suspends it, "X-Z og
// Y-Z" printed "X- og Y-Z": »studenterkurser og hf-kurser« for "studenter- og hf-kurser", when it
// then occurs once in the text.
func suspendedCompound(text, old string) (string, bool) {
	o := strings.TrimSpace(QuoteStrip(old))
	for _, m := range reWrittenOutCompound.FindAllStringSubmatchIndex(o, -1) {
		word, other := o[m[2]:m[3]], o[m[6]:m[7]]
		tail := o[m[8]:m[9]]
		if !strings.HasSuffix(word, tail) || len(word) <= len(tail) {
			continue
		}
		v := o[:m[2]] + strings.TrimSuffix(word, tail) + "- " + o[m[4]:m[5]] + " " + other + "-" + tail + o[m[1]:]
		if countMatches(text, v) == 1 {
			return v, true
		}
	}
	return "", false
}

// reWrittenOutCompound is "studenterkurser og hf-kurser": a word, "og" or "eller", and a hyphenated
// compound sharing its end.
var reWrittenOutCompound = regexp.MustCompile(`(\p{L}+)\s+(og|eller)\s+(\p{L}+)-(\p{L}+)`)

// withoutPrintedItems leaves out of new lines the numbered conditions the subsection prints word
// for word already, whatever their number: lov nr. 1609 of 2010 inserted "6) … meddelelse …" and
// "7) … bopæl …" in a § 2 that printed the first as nr. 6, and the next consolidation prints it once.
func withoutPrintedItems(p *Provision, st *Stykke, segs []Segment) ([]Segment, int, int) {
	if st == nil {
		return segs, 0, 0
	}
	// Conditions are remembered by number and letter, not by pointer: replacing one's content
	// rebuilds the subsection's list and moves every later span, so a pointer kept from before
	// names a condition where it no longer is (it cut past the end of the provision once).
	type itemKey struct {
		nr            int
		suffix, litra string
	}
	have := map[string]itemKey{}
	for k := range st.Items {
		it := &st.Items[k]
		if body := itemBody(SegmentsText(p.Segments[it.Span.From:it.Span.To])); len(body) >= 30 {
			have[body] = itemKey{it.Nr, it.Suffix, it.Litra}
		}
	}
	current := func(k itemKey) *Item {
		for i := range st.Items {
			if it := &st.Items[i]; it.Nr == k.nr && it.Suffix == k.suffix && it.Litra == k.litra &&
				it.Span.From >= 0 && it.Span.To <= len(p.Segments) && it.Span.From <= it.Span.To {
				return it
			}
		}
		return nil
	}
	if len(have) == 0 {
		return segs, 0, 0
	}
	// "6) … standses, og 7) at mindst …" printed as one line: a condition to a line.
	var split []Segment
	for _, sg := range segs {
		split = append(split, splitInlineItems(sg)...)
	}
	segs = split
	// Group the new lines into conditions by their markers.
	var out []Segment
	dropped, leading := 0, 0
	for i := 0; i < len(segs); {
		j := i + 1
		for j < len(segs) && !reItemLabel.MatchString(strings.TrimSpace(segs[j].Text)) {
			j++
		}
		var it *Item
		if k, ok := have[itemBody(SegmentsText(segs[i:j]))]; ok {
			it = current(k)
		}
		if reItemLabel.MatchString(strings.TrimSpace(segs[i].Text)) && it != nil {
			// The condition printed already takes the change's wording, which ends as its new
			// place in the list has it: "… skal standses, og" before the condition the change
			// adds after it.
			marker := reItemLabel.FindString(strings.TrimSpace(SegmentsText(p.Segments[it.Span.From:it.Span.To])))
			body := strings.TrimSpace(reItemLabel.ReplaceAllString(strings.TrimSpace(SegmentsText(segs[i:j])), ""))
			p.ReplaceItemContent(it, []Segment{{Text: marker + " " + body, Break: true}})
			dropped++
			if len(out) == 0 {
				leading++
			}
		} else {
			out = append(out, segs[i:j]...)
		}
		i = j
	}
	return out, dropped, leading
}

// itemBody is a condition's words without its marker and its closing "og", "eller" or punctuation.
// Commas inside do not count: "den frist, som jobcenteret har fastsat, for selv at booke" is the
// condition an earlier act printed without the second comma.
func itemBody(s string) string {
	s = strings.TrimSpace(reItemLabel.ReplaceAllString(strings.TrimSpace(Squeeze(s)), ""))
	s = strings.TrimSpace(reItemTail.ReplaceAllString(s, ""))
	return strings.Join(strings.Fields(strings.NewReplacer(",", " ", ";", " ").Replace(MatchText(s))), " ")
}

// reItemTail is how a condition ends, which depends on where in the list it stands.
var reItemTail = regexp.MustCompile(`(?i)[,.;]?\s*(?:og|eller)?\s*[,.;]?\s*$`)

// splitInlineItems splits a line printing several numbered conditions in sequence, "6) … og 7) …",
// into a line for each.
func splitInlineItems(sg Segment) []Segment {
	text := sg.Text
	m := reItemLabel.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return []Segment{sg}
	}
	next := Atoi(m[2]) + 1
	var out []Segment
	for {
		loc := regexp.MustCompile(`\s` + itoa(next) + `\)\s`).FindStringIndex(text)
		if loc == nil {
			break
		}
		head := sg
		head.Text = strings.TrimRight(text[:loc[0]], " ")
		out = append(out, head)
		text = " " + text[loc[0]+1:] // the next condition, set apart as the first was
		next++
	}
	last := sg
	last.Text = text
	return append(out, last)
}

// splitInlineLetters splits a condition printed with its lettered conditions on the same line,
// "2) er omfattet af § 2, nr. 2, og a) personen er under 30 år …, eller b) personen er fyldt 30 år
// …", into the condition and a line for each letter, as the text prints them. Only a run from a)
// through at least b) counts.
func splitInlineLetters(sg Segment) []Segment {
	text := sg.Text
	if !reItemLabel.MatchString(strings.TrimSpace(text)) {
		return []Segment{sg}
	}
	var cuts []int
	from := 0
	for _, letter := range "abcdefghijklmnopqrstuvwxyz" {
		loc := regexp.MustCompile(`\s` + string(letter) + `\)\s`).FindStringIndex(text[from:])
		if loc == nil {
			break
		}
		cuts = append(cuts, from+loc[0])
		from += loc[1]
	}
	if len(cuts) < 2 {
		return []Segment{sg}
	}
	var out []Segment
	prev := 0
	for _, c := range cuts {
		head := sg
		head.Text = strings.TrimRight(text[prev:c], " ")
		if prev > 0 {
			head.Text = " " + head.Text
		}
		out = append(out, head)
		prev = c + 1
	}
	last := sg
	last.Text = " " + text[prev:]
	return append(out, last)
}

// aheadTextSnapshot is the provision a wording change taking effect later than its act was signed
// changes, as it stood before the change.
func (c *Content) aheadTextSnapshot(e Edit, source string, effective time.Time) []aheadEdit {
	if e.Kind == EditReplaceEverywhere || e.InForce.IsZero() || !e.InForce.After(effective) || e.Address.Paragraph == "" || e.Address.Heading {
		return nil
	}
	loc, ok := c.Find(Address{Recognised: true, Paragraph: e.Address.Paragraph})
	if !ok || loc.Provision == nil {
		return nil
	}
	p := cloneProvision(*loc.Provision)
	return []aheadEdit{{Edit: e, Address: e.Address, Source: source, Before: &p}}
}

// supersedesAhead carries out a refused change on the provision as it stood before a wording change
// of an earlier act, signed before it but taking effect after it was signed, rewrote some of the
// words it quotes: the later act drafted its change against the words the earlier one was still to
// change, and its wording stands. Lov nr. 1555 of December 2019 rewrote aktivloven § 37, stk. 3,
// »åbenlyst uddannelsesparat integrationsydelses- eller uddannelseshjælpsmodtager«, whose
// "integrationsydelses-" lov nr. 174 of February 2019 changed from 2020; the consolidation of 2021
// prints lov nr. 1555's wording. Only where the earlier change is all that stands between the two.
func (c *Content) supersedesAhead(e Edit, signed time.Time, ahead []aheadEdit, out *Consolidation, at int) bool {
	if len(out.Unresolved) <= at || e.Kind != EditReplaceText && e.Kind != EditDeleteText && e.Kind != EditInsertText || e.Address.Paragraph == "" {
		return false
	}
	for i := range ahead {
		a := ahead[i]
		if a.Edit.Source == e.Source || !a.Edit.InForce.After(signed) || a.Address.Paragraph != e.Address.Paragraph {
			continue
		}
		// The earlier acts' changes to the provision from this one on, in order, that had not
		// taken effect when this act was signed.
		var group []Edit
		for _, g := range ahead[i:] {
			if g.Source != e.Source && g.Edit.InForce.After(signed) && g.Address.Paragraph == a.Address.Paragraph {
				group = append(group, g.Edit)
			}
		}
		// The subsection is the snapshot's with those changes made, and nothing else since.
		unit := Address{Recognised: true, Paragraph: e.Address.Paragraph, Stk: e.Address.Stk}
		text := func(c *Content) (string, *Located) {
			l, ok := c.Find(unit)
			if !ok || l.Provision == nil {
				return "", nil
			}
			if l.Stykke == nil && len(l.Provision.Stykker) == 1 {
				l.Stykke = &l.Provision.Stykker[0]
			}
			if l.Stykke == nil {
				// A provision printed without subsections.
				if len(l.Provision.Stykker) > 0 {
					return "", nil
				}
				return MatchText(l.Provision.Text()), &l
			}
			return MatchText(l.Provision.SpanText(l.Stykke.Span)), &l
		}
		redo := &Content{Order: []Node{{Provision: func() *Provision { p := cloneProvision(*a.Before); return &p }()}}}
		for _, g := range group {
			redo.applyOne(g, &Consolidation{})
		}
		was, _ := text(redo)
		now, cur := text(c)
		if was == "" || was != now {
			continue
		}
		clone := func(c *Content) *Content {
			p := cloneProvision(*c.Order[0].Provision)
			return &Content{Order: []Node{{Provision: &p}}}
		}
		works := func(c *Content) bool {
			trial := &Consolidation{}
			clone(c).applyOne(e, trial)
			return len(trial.Unresolved) == 0 && len(trial.Applied) > 0
		}
		// On the snapshot this act's change is made, then the earlier changes that still find
		// their words: aktivloven § 75, stk. 1, "nr. 6" as it stood before lov nr. 551 put a
		// condition in front of it from 2020, and that condition after it.
		mine := &Content{Order: []Node{{Provision: func() *Provision { p := cloneProvision(*a.Before); return &p }()}}}
		if works(mine) {
			mine.applyOne(e, &Consolidation{})
			for _, g := range group {
				mine.applyOne(g, &Consolidation{})
			}
		} else {
			// Or the earlier changes this act's change can follow first, those whose words it
			// rewrites not at all: § 37, stk. 3, quoted after lov nr. 551 made "Har" "Når".
			for _, g := range group {
				next := clone(mine)
				next.applyOne(g, &Consolidation{})
				if works(next) {
					mine = next
				}
			}
			if !works(mine) {
				continue
			}
			mine.applyOne(e, &Consolidation{})
		}
		_, made := text(mine)
		switch {
		case made == nil:
			continue
		case cur.Stykke == nil:
			*cur.Provision = *made.Provision
		case made.Stykke == nil || !cur.Provision.ReplaceSpanContent(cur.Stykke.Span, cloneSegments(made.Provision.Segments[made.Stykke.Span.From:made.Stykke.Span.To])):
			continue
		}
		out.Applied = append(out.Applied, AppliedEdit{Edit: e, Where: e.Address.String(),
			Note: fmt.Sprintf("made on the words %s %s was to change from %s, after this act was signed; this act's wording stands", a.Source, strings.TrimSpace(a.Edit.Number), a.Edit.InForce.Format("2. 1. 2006"))})
		return true
	}
	return false
}

// reStykkeProse is an insertion of subsections: "indsættes efter stk. 1 som nyt stykke", "før stk. 1
// som nyt stykke", "indsættes
// som stk. 3", "som stk. 3-4", "som stk. 2 og 3".
var reStykkeProse = regexp.MustCompile(`(?i)\bindsættes\s+(?:(efter|før)\s+stk\.\s*([0-9]+)\s*,?\s+som\s+(?:nyt?e?\s+)?(?:stykke|stykker|stk\.\s*[0-9]+(?:\s*(?:-|–|og)\s*[0-9]+)?)|som\s+(?:nyt?e?\s+)?stk\.\s*([0-9]+)(?:\s*(?:-|–|og)\s*[0-9]+)?)\s*:?\s*(?:stk\.\s*[0-9]+(?:\s*-\s*[0-9]+)?\s+bliver\s+herefter\s+stk\.\s*[0-9]+(?:\s*-\s*[0-9]+)?\.?\s*)?$`)

// reProseStkLabel is a subsection's label opening a line, "Stk. 2." or "Stk. 2 .".
var reProseStkLabel = regexp.MustCompile(`^\s*Stk\.\s*([0-9]+)\s*\.\s*`)

// stykkerOfLines groups lines of new text into the subsections their labels open, each led by its
// label as the text prints one; lines before any label form one subsection.
func stykkerOfLines(lines []string) [][]Segment {
	var out [][]Segment
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if m := reProseStkLabel.FindStringSubmatch(l); m != nil {
			grp := []Segment{{Text: "Stk. " + m[1] + ".", Break: true}}
			if rest := strings.TrimSpace(l[len(m[0]):]); rest != "" {
				grp = append(grp, Segment{Text: rest, Break: true})
			}
			out = append(out, grp)
			continue
		}
		if len(out) == 0 {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], Segment{Text: l, Break: true})
	}
	return out
}
