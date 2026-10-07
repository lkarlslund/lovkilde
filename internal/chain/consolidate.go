package chain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
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

// VersionsOf lists the documents that amended a document, oldest first, from its metadata.
func VersionsOf(m *Meta) []Version {
	var out []Version
	for _, r := range m.Changes() {
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

// chainStep is one amending document's contribution, resolved but not applied.
type chainStep struct {
	Version Version
	Edits   []lexdoc.Edit

	// Tentative marks an act whose changes were aimed at an earlier consolidation of this law and
	// that dates from before this one: the base text may already carry it. See lexdoc.Amendment.
	Tentative bool

	// InForce says when the act's parts take effect.
	InForce lexdoc.InForce

	// waits reports a part the act leaves to a minister that a later consolidation says still
	// waits; see chainStep.when.
	waits func(e lexdoc.Edit, from time.Time) bool
	// effectBy is when a change a consolidation named as waiting is known to be in the law; see
	// Archive.takesEffectBy.
	effectBy func(e lexdoc.Edit, from time.Time) time.Time

	// noted reports a change the base's notes list as not printed, which is the act's late part
	// whatever its day; see chainStep.splitAtBase.
	noted func(lexdoc.Edit) bool

	// Late marks the part of a tentative act taking effect after the base was signed; see
	// lexdoc.Amendment.Late.
	Late bool

	// Skipped names a reason the version contributes no edits that is not a gap in our reading:
	// it is a bill, or every change in it aims at a different law. Empty Skipped with no Edits
	// means the document was opened, read, and yielded nothing, which is a gap: see View.Silent.
	Skipped string
}

// ActRecord is what consolidation needs from one amending document: whether it is a bill, and
// its changes in the model's form, each naming the law it is aimed at. It is the same for every law
// the act amends and every date it is asked about, so it is read once.
type ActRecord struct {
	Path    string
	Bill    bool
	Edits   []lexdoc.Edit
	InForce lexdoc.InForce // when the act's parts take effect, as its own provisions say
}

// actSource reads an amending act by accession. The path is reported even when the act could
// not be read, so a reader can tell "not archived" from "archived and unreadable".
type actSource func(accession string) (*ActRecord, bool)

// resolveChain reads every version of a law that takes effect by asOf and collects the changes
// each one contributes against that law.
//
// Two counters come back with the steps, because both describe amendments that were *not*
// applied and neither should be invisible: bills (whose text is a proposal rather than enacted
// law, and whose markup looks identical to the enacted one) and changes whose preamble aims at a
// different law. The order of the chain is what makes "§ 12, stk. 2" mean the right thing, not an
// optimisation; the engine applies it by effective date.
func resolveChain(base *Law, asOf time.Time, acts actSource, help ...chainHelp) (steps []chainStep, bills, foreign int) {
	var h chainHelp
	if len(help) > 0 {
		h = help[0]
	}
	known := map[string]bool{base.Meta.Accession: true}
	queue := base.Versions()
	for _, ver := range queue {
		known[ver.Accession] = true
	}
	sections := map[string]string{}            // a cited act's provision that amends this law
	noted := map[string][]lexdoc.InForceRule{} // the changes of an act a note names as to come
	var repeals []actRepeal                    // changes of other acts the chain's acts repeal
	if h.cited != nil {
		// "Det følger af § 3, nr. 1, i lov nr. 1518 af 27. december 2009, at § 52 a, stk. 1,
		// affattes således": a change the base prints in a note as still to come, by an act
		// older than the base that its change list does not name. The act is read as one citing
		// an earlier consolidation, tentatively, and its changes the base carries are left.
		for _, n := range notedActs(base) {
			if v, ok := h.cited(n.act); ok && !known[v.Accession] && !afterBase(v, base) {
				known[v.Accession] = true
				noted[v.Accession] = n.changes
				queue = append(queue, v)
			}
		}
	}
	for len(queue) > 0 {
		ver := queue[0]
		queue = queue[1:]
		if ver.Effective.After(asOf) {
			steps = append(steps, chainStep{Version: ver})
			continue
		}
		act, ok := acts(ver.Accession)
		ver.Path, ver.Found = act.Path, ok
		switch {
		case !ok:
			steps = append(steps, chainStep{Version: ver})
			continue
		case act.Bill:
			bills++
			steps = append(steps, chainStep{Version: ver, Skipped: "bill"})
			continue
		}
		var edits []lexdoc.Edit
		tentative := false
		section, cited := sections[ver.Accession]
		// The sections of the act that name this law. When one does, a section naming nothing this
		// reader recognised is another law's: lov nr. 710 of 2017's "I registreringsafgiftsloven,
		// jf. lovbekendtgørelse 377 af 18. april 2017" was read as no target, and its change went
		// to the law on borgerservicecentre that the act's § 5 amends.
		named := map[string]bool{}
		for _, e := range act.Edits {
			if !e.Target.Empty() && (TargetsLaw(e.Target, base.Meta) || h.sameLaw != nil && h.sameLaw(e.Target)) {
				named[e.Section] = true
			}
		}
		for _, e := range act.Edits {
			if cited && section != "" && e.Section != section {
				continue
			}
			if e.Target.Empty() && e.Target.Kind == "" && e.Section != "" && len(named) > 0 && !named[e.Section] {
				continue
			}
			if rules, ok := noted[ver.Accession]; ok && !lexdoc.RulesCover(rules, e.Section, e.Number) {
				continue // the base prints the act's other changes, by its note's own account
			}
			if !TargetsLaw(e.Target, base.Meta) {
				if h.sameLaw == nil || !h.sameLaw(e.Target) {
					if h.cited != nil && e.Kind == lexdoc.EditDeleteProvision && !e.Target.Date.IsZero() {
						// "I lov nr. 743 af 8. juni 2018 … foretages følgende ændring: § 1 og § 2
						// ophæves": a repeal of another act's changes, which may be this law's.
						if v, ok := h.cited(e.Target); ok && reActRepeal.MatchString(lexdoc.Squeeze(e.Definition)) &&
							strings.HasPrefix(strings.ToLower(v.Title), "lov om ændring") {
							d := ver.Effective
							if w, has := act.InForce.When(e.Section, e.Number); has && w.After(d) {
								d = w
							}
							repeals = append(repeals, actRepeal{act: v.Accession, rules: lexdoc.InForceRulesOf(reRepealVerb.ReplaceAllString(lexdoc.Squeeze(e.Definition), "")), from: d, by: ver})
						}
					}
					foreign++
					continue
				}
				// Aimed at an earlier consolidation of this law: the act is this law's, and if it
				// dates from before this consolidation the text may print it already.
				tentative = tentative || !afterBase(ver, base)
				if h.openings != nil {
					e.CitedStykker = citedStykker(e, h.openings)
				}
			}
			edits = append(edits, e)
			if h.cited == nil {
				continue
			}
			// "I lov nr. 687 af 8. juni 2017 …, som ændret ved § 8 i lov nr. 1585 af 27. december
			// 2019, foretages følgende ændring": an act the law's metadata does not list as one of
			// its amendments, named by an act that does.
			for _, earlier := range e.Target.Earlier {
				// One dated before this consolidation is left alone: the consolidation printed what of
				// it had taken effect, and the acts it cites in turn reach back through the law's
				// whole history.
				if v, ok := h.cited(earlier); ok && !known[v.Accession] && afterBase(v, base) {
					known[v.Accession] = true
					sections[v.Accession] = earlier.Section
					queue = append(queue, v)
				}
			}
		}
		skipped := ""
		if len(edits) == 0 && len(act.Edits) > 0 {
			skipped = "every change aims at another law"
		}
		st := chainStep{Version: ver, Edits: edits, Skipped: skipped, Tentative: tentative, InForce: act.InForce}
		if h.waiting != nil {
			v := ver
			st.waits = func(e lexdoc.Edit, from time.Time) bool { return h.waiting(v, e, from) }
			if h.effectBy != nil {
				st.effectBy = func(e lexdoc.Edit, from time.Time) time.Time { return h.effectBy(v, e, from) }
			}
		}
		if h.notedInBase != nil {
			v := ver
			st.noted = func(e lexdoc.Edit) bool { return h.notedInBase(v, e) }
		}
		steps = append(steps, st)
	}
	undo := printedRepealed(base, steps, repeals)
	for i := range steps {
		steps[i].Edits = withoutRepealed(steps[i], repeals)
	}
	steps = append(steps, undo...)
	sort.SliceStable(steps, func(i, j int) bool {
		a, b := steps[i].Version, steps[j].Version
		if !a.Effective.Equal(b.Effective) {
			return a.Effective.Before(b.Effective)
		}
		return a.Accession < b.Accession
	})
	return steps, bills, foreign
}

// chainHelp is what an archive knows beyond one law's metadata: sameLaw recognises a change list
// aimed at an earlier consolidation of the law, and cited resolves an act a preamble names as an
// earlier amendment of it.
type chainHelp struct {
	sameLaw  func(TargetHint) bool
	cited    func(TargetHint) (Version, bool)
	waiting  func(act Version, e lexdoc.Edit, from time.Time) bool
	effectBy func(act Version, e lexdoc.Edit, from time.Time) time.Time
	// notedInBase reports a change of the act the base's own notes list as not printed.
	notedInBase func(act Version, e lexdoc.Edit) bool
	// openings are a provision's subsections as a consolidation of the law the change cites
	// printed them; see lexdoc.Edit.CitedStykker.
	openings func(cited TargetHint, paragraph string) []string
}

// amendment is the act's changes as one step of the chain, each carrying the day it takes effect
// when the act says it is later than the day it was signed.
func (st chainStep) amendment(adopted bool) lexdoc.Amendment {
	edits := make([]lexdoc.Edit, len(st.Edits))
	for i, e := range st.Edits {
		if d, ok := st.when(e); ok && d.After(st.Version.Effective) && !adopted {
			e.InForce = d
		}
		edits[i] = e
	}
	return lexdoc.Amendment{Source: st.Version.Accession, Effective: st.Version.Effective, Edits: edits, Tentative: st.Tentative, Late: st.Late}
}

// when is the day a change takes effect. A part the act leaves to a minister waits for the order
// that sets it, and the archive holds too few of the orders to tell when that was: aktivloven's
// consolidation of 2021 prints the parts of lov nr. 1555 of 2019 set in force by an order the
// archive lacks. The newest consolidation of the law signed after the act tells whether it still
// waits — sømandsloven's of 2024 that § 2 of lov nr. 1231 of 2003 "stadig ikke er sat i kraft" —
// and otherwise the part goes with the act.
func (st chainStep) when(e lexdoc.Edit) (time.Time, bool) {
	d, ok := st.InForce.When(e.Section, e.Number)
	deferred := ok && d.Equal(lexdoc.Deferred)
	if (deferred || st.noted != nil && st.noted(e)) && st.waits != nil {
		from := st.Version.Effective
		if ok && !deferred && d.After(from) {
			from = d
		}
		if st.waits(e, from) {
			return lexdoc.Deferred, true
		}
		if st.effectBy != nil {
			if by := st.effectBy(e, from); !by.IsZero() && (!ok || deferred || by.After(d)) {
				return by, true
			}
		}
	}
	if deferred {
		return st.InForce.Default, !st.InForce.Default.IsZero()
	}
	return d, ok
}

// splitAtBase separates a tentative act's changes that took effect after the consolidation was
// signed from the rest, or reports false when the act's changes all fall on one side.
func (st chainStep) splitAtBase(base *Law) (early, late chainStep, ok bool) {
	signed, found := ParseDate(base.Meta.DiesSigni)
	if !found || signed.IsZero() {
		return st, st, false
	}
	early, late = st, st
	early.Edits, late.Edits = nil, nil
	for _, e := range st.Edits {
		if d, has := st.when(e); has && d.After(signed) || st.noted != nil && st.noted(e) {
			late.Edits = append(late.Edits, e)
		} else {
			early.Edits = append(early.Edits, e)
		}
	}
	late.Late = true
	return early, late, len(early.Edits) > 0 && len(late.Edits) > 0
}

// silent reports a version that was opened and read but contributed nothing, which is different
// from being absent (Missing), a proposal rather than law, or aimed at another act (Skipped).
func (st chainStep) silent() bool {
	return st.Version.Found && st.Skipped == "" && len(st.Edits) == 0 &&
		!st.Version.Effective.IsZero()
}

// View is a law as it stood on a date: the base text with its amendment chain applied, and the
// receipts. It is not a document the source ever published, and callers must not present it as
// one: the register publishes no merged text at any layer, so this is ours.
type View struct {
	Accession string
	Title     string
	AsOf      time.Time

	// Path is where the base document was read from, and is empty for views built from fixtures.
	Path string

	// Doc is the base document in the model, carrying the derived law as its Content: identity
	// and provenance are the base's, the text is ours. Content points at Doc.Content. The base
	// document's own content is never edited.
	Doc     *lexdoc.Document
	Content *lexdoc.Content

	Chain      []Version // every known version; Found is set on those the archive holds
	Applied    []lexdoc.AppliedEdit
	Unresolved []lexdoc.UnresolvedEdit

	// InBase lists the changes of acts the base text already carries: acts that cite an earlier
	// consolidation of this law, date from before this one, and whose wording the text no longer
	// prints. They were not applied a second time.
	InBase []lexdoc.Edit

	// Pending lists changes of acts already signed that take effect after the view's day; see
	// lexdoc.Consolidation.Pending.
	Pending []lexdoc.Edit

	// Tentative names the acts that cite an earlier consolidation of this law and date from
	// before this one; see lexdoc.Amendment.Tentative.
	Tentative map[string]bool

	// NoOpEdits counts text edits that found their passage and wrote back identical text. They
	// are still applied — an amendment may restate existing law — but "applied" must never be
	// able to mean "nothing changed" unnoticed.
	NoOpEdits int

	// SkippedBills counts chain members that are parliamentary bills, whose proposed text was not
	// applied: only the enacted act changes the law. SkippedForeign counts changes aimed at another
	// law, which is normal: one amending act often changes several.
	SkippedBills   int
	SkippedForeign int

	// Silent lists chain documents that take effect by AsOf, are present in the mirror, are not
	// bills, are aimed at this law, and yielded no change at all. A refusal says we would not
	// guess; silence says we had nothing to work from, and the two look identical in a receipt
	// that counts only applied and refused.
	//
	// B20000072305 is the shape this exists for: eight amending acts, all eight found, applied 0,
	// refused 0, and 44 provisions standing unchanged from promulgation to 2010 — reported as a
	// trustworthy consolidation the whole time. Each of those acts is metadata-only XML with its
	// body in the HTML payload, which is how most of the pre-2010 archive arrives.
	Silent []Version
}

// AppliedCount reports how many chain documents in effect by AsOf the archive holds.
func (v *View) AppliedCount() int {
	n := 0
	for _, c := range v.Chain {
		if c.Found && !c.Effective.After(v.AsOf) {
			n++
		}
	}
	return n
}

// MissingChain are the amending acts that take effect on or before the asked date and are named
// by this law's own metadata, but whose text the mirror does not hold.
//
// A document missing from the mirror and an edit the engine refused to guess at are different
// problems with different remedies, and only one of them is a gap in what we hold.
func (v *View) MissingChain() []Version {
	var out []Version
	for _, c := range v.Chain {
		if !c.Found && !c.Effective.After(v.AsOf) {
			out = append(out, c)
		}
	}
	return out
}

// Trustworthy reports whether this view can be presented as complete: no chain document missing,
// no change refused, and none read without result.
func (v *View) Trustworthy() bool {
	return len(v.MissingChain()) == 0 && len(v.Unresolved) == 0 && len(v.Silent) == 0
}

func (v *View) String() string {
	return fmt.Sprintf("view of %s as of %s: %d edits applied, %d unresolved, %d chain documents missing, %d read with no result",
		v.Accession, v.AsOf.Format(time.DateOnly), len(v.Applied), len(v.Unresolved), len(v.MissingChain()), len(v.Silent))
}

// Text is the derived law as plain text.
func (v *View) Text() string { return v.Content.PlainText() }

// ProvisionText is the derived text of one provision, subsection or item, or false when the
// address names nothing in the derived law.
func (v *View) ProvisionText(addr string) (string, bool) {
	loc, ok := v.Content.Find(lexdoc.ParseAddress(addr))
	if !ok || loc.Provision == nil {
		return "", false
	}
	switch {
	case loc.Item != nil:
		return Squeeze(loc.Provision.SpanText(loc.Item.Span)), true
	case loc.Stykke != nil:
		return Squeeze(loc.Provision.SpanText(loc.Stykke.Span)), true
	}
	return Squeeze(loc.Provision.Text()), true
}

func lawAsOf(base *Law, asOf time.Time, acts actSource, help ...chainHelp) *View {
	return lawAsOfMode(base, asOf, acts, false, help...)
}

// lawAsOfMode is lawAsOf with a choice: adopted reads every change of an act signed by asOf,
// whether or not it has taken effect, as a consolidation prints the law as amended by every act
// passed when it was signed.
func lawAsOfMode(base *Law, asOf time.Time, acts actSource, adopted bool, help ...chainHelp) *View {
	steps, bills, foreign := resolveChain(base, asOf, acts, help...)

	amendments := make([]lexdoc.Amendment, 0, len(steps))
	v := &View{
		Accession:      base.Meta.Accession,
		Title:          base.Title(),
		AsOf:           asOf,
		SkippedBills:   bills,
		SkippedForeign: foreign,
	}
	for _, st := range steps {
		v.Chain = append(v.Chain, st.Version)
		if st.silent() && !st.Version.Effective.After(asOf) {
			v.Silent = append(v.Silent, st.Version)
		}
		if len(st.Edits) == 0 {
			continue
		}
		if st.Tentative {
			if v.Tentative == nil {
				v.Tentative = map[string]bool{}
			}
			v.Tentative[st.Version.Accession] = true
		}
		if st.Tentative {
			if early, late, ok := st.splitAtBase(base); ok {
				// Lov nr. 720 of 2014 dates from before aktivlovens consolidation of September 2014,
				// which printed most of it; its § 2, nr. 41-43, took effect on 1 January 2015, and
				// the consolidation could not print them. Each part is judged on its own.
				amendments = append(amendments, early.amendment(adopted), late.amendment(adopted))
				continue
			}
		}
		amendments = append(amendments, st.amendment(adopted))
	}
	v.Doc = base.Model()
	view, cons := v.Doc.Content.AsOf(amendments, asOf)
	v.Doc.Content = *view
	v.Content = &v.Doc.Content
	v.Applied, v.Unresolved, v.NoOpEdits, v.InBase, v.Pending = cons.Applied, cons.Unresolved, cons.NoOpEdits, cons.InBase, cons.Pending
	return v
}

// TargetsLaw checks the amending paragraph's preamble against the law we are
// reconstructing. Amendments routinely address several laws, and applying a change
// meant for another law is the worst failure this engine could have.
//
// An unparseable hint is accepted rather than rejected: refusing it would drop real
// amendments whenever the preamble names its target in an unusual way, and the
// address still has to match a provision here for anything to happen.
func TargetsLaw(h TargetHint, m Meta) bool {
	if h.Empty() && h.Kind != "" {
		// A change list for "forordning af 2. december 1825" is for that ordinance: a law of
		// another kind is not what it amends.
		return strings.Contains(strings.ToLower(m.DocumentType+" "+m.Title), h.Kind)
	}
	if h.Empty() {
		return true
	}
	if h.Number != 0 && h.Number != m.Number {
		return false
	}
	if !h.Date.IsZero() {
		if signed, ok := ParseDate(m.DiesSigni); ok && !signed.IsZero() && !signed.Equal(h.Date) {
			// A number is the document's within its year of Lovtidende; the date an act cites with
			// it is now and then the day of publication, or a slip: "bekendtgørelse nr. 1398 af 1.
			// december 2013" for the order signed on the 12th. Read as another document, four
			// orders' amendments were lost.
			if h.Number == 0 || h.Date.Year() != signed.Year() || absDays(h.Date.Sub(signed)) > 45 {
				return false
			}
		}
	}
	return true
}

// absDays is a duration in whole days, whichever way it runs.
func absDays(d time.Duration) int {
	if d < 0 {
		d = -d
	}
	return int(d.Hours() / 24)
}

// afterBase reports an act that takes effect after the base text was promulgated. A consolidation
// prints what was in force when it was signed, so an act in force before it is already in it,
// whichever earlier consolidation the act cited.
func afterBase(v Version, base *Law) bool {
	signed, ok := ParseDate(base.Meta.DiesSigni)
	return !ok || v.Effective.After(signed)
}

// reNotedChange is a note naming a change by an act that the text it annotates does not print yet:
// "Det følger af § 3, nr. 1, i lov nr. 1518 af 27. december 2009, at …", "De ændringer, der følger
// af § 2, nr. 2, i lov nr. 1338 af 19. december 2008, er ikke indarbejdet".
var reNotedChange = regexp.MustCompile(`(?i)følger\s+af\s+(§\s*[0-9][^»§]{0,200}?)\s*,?\s*\bi\s+(lov\s+nr\.\s*[0-9]+\s+af\s+[0-9]{1,2}\.\s*\p{L}+\s+[0-9]{4})`)

// reNotedPending is the wording of a note on a change the text does not print yet.
var reNotedPending = regexp.MustCompile(`(?i)^\s*(?:[0-9]+\)\s*)?det\s+følger\s+af\b|\bikke\s+(?:er\s+)?(?:indarbejdet|medtaget|trådt\s+i\s+kraft|sat\s+i\s+kraft)`)

// notedAct is an act a note names, with the changes of it the note says are still to come.
type notedAct struct {
	act     TargetHint
	changes []lexdoc.InForceRule
}

// notedActs are the acts the base's notes name as bringing changes it does not print.
func notedActs(base *Law) []notedAct {
	c := base.Model().Content
	var texts []string
	for _, r := range c.Provisions() {
		for _, sg := range r.Provision.Segments {
			if sg.Note != "" {
				texts = append(texts, sg.Text)
			}
		}
	}
	for _, sg := range c.Title {
		if sg.Note != "" {
			texts = append(texts, sg.Text)
		}
	}
	var out []notedAct
	seen := map[string]bool{}
	for _, t := range texts {
		t = lexdoc.Squeeze(t)
		if !reNotedPending.MatchString(t) {
			// "Herved bekendtgøres …, med de ændringer, der følger af § 3 i lov nr. 427 …" names
			// changes the text prints.
			continue
		}
		for _, m := range reNotedChange.FindAllStringSubmatch(t, -1) {
			tg := lexdoc.ParseTarget("I " + m[2] + " foretages følgende ændringer:")
			rules := lexdoc.InForceRulesOf(waitingRef(m[1]))
			if tg.Number == 0 || tg.Date.IsZero() || len(rules) == 0 || seen[m[1]+m[2]] {
				continue
			}
			seen[m[1]+m[2]] = true
			out = append(out, notedAct{act: tg, changes: rules})
		}
	}
	return out
}

// citedStykker are the subsections of the provisions a change names a subsection of, as the
// consolidation it cites printed them.
func citedStykker(e lexdoc.Edit, openings func(TargetHint, string) []string) map[string][]string {
	out := map[string][]string{}
	addrs := append([]lexdoc.Address{e.Address}, lexdoc.ParseAddresses(e.Definition)...)
	for _, a := range addrs {
		if a.Paragraph == "" || a.Stk == 0 {
			continue
		}
		if _, done := out[a.Paragraph]; done {
			continue
		}
		if o := openings(e.Target, a.Paragraph); len(o) > 0 {
			out[a.Paragraph] = o
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// actRepeal is a repeal of an act's changes by a later act: the changes, by the act's provisions
// and numbers, and the day the repeal takes effect.
type actRepeal struct {
	act   string
	rules []lexdoc.InForceRule
	from  time.Time
	by    Version // the repealing act
}

// reActRepeal is a repeal of an amending act's own provisions or numbered changes, whole: "§ 1 og §
// 2 ophæves", "§ 2, nr. 2, 6, 8 og 10, ophæves" — not a subsection or a lettered provision, which
// an act amending a law repeals in that law.
var reActRepeal = regexp.MustCompile(`(?i)^§\s*[0-9]+(?:\s*,\s*nr\.\s*[0-9]+(?:\s*[-–]\s*[0-9]+)?(?:\s*(?:,|og)\s*[0-9]+(?:\s*[-–]\s*[0-9]+)?)*)?(?:\s*,?\s*(?:og\s+)?§\s*[0-9]+(?:\s*,\s*nr\.\s*[0-9]+(?:\s*[-–]\s*[0-9]+)?(?:\s*(?:,|og)\s*[0-9]+(?:\s*[-–]\s*[0-9]+)?)*)?)*\s*,?\s*ophæves\.?\s*$`)

// reRepealVerb is the verb ending a repeal, cut before its provisions are read.
var reRepealVerb = regexp.MustCompile(`(?i),?\s*ophæves\.?\s*$`)

// withoutRepealed leaves out the changes of a step that a later act repealed before they took
// effect: lov nr. 743 of June 2018 changed aktivloven § 11 from 1 January 2019, and lov nr. 1525
// of December 2018 repealed its § 1 and § 2 from that day, re-enacting the rules in other words;
// made both, § 11 printed the new subsections twice.
func withoutRepealed(st chainStep, repeals []actRepeal) []lexdoc.Edit {
	var mine []actRepeal
	for _, r := range repeals {
		if r.act == st.Version.Accession {
			mine = append(mine, r)
		}
	}
	if len(mine) == 0 {
		return st.Edits
	}
	var out []lexdoc.Edit
	for _, e := range st.Edits {
		gone := false
		for _, r := range mine {
			if !lexdoc.RulesCover(r.rules, e.Section, e.Number) {
				continue
			}
			when := st.Version.Effective
			if d, ok := st.InForce.When(e.Section, e.Number); ok {
				when = d
			}
			if !when.Before(r.from) {
				gone = true
			}
		}
		if !gone {
			out = append(out, e)
		}
	}
	return out
}

// printedRepealed are removals of subsections the base prints although the change that brought
// them never took effect and was repealed: aktivloven's consolidations of 2016-2019 print § 13 a,
// stk. 3, from lov nr. 1000 of 2015, § 1, nr. 25, with a note that a minister had yet to set it in
// force; lov nr. 551 of 2019 repealed that change from 2020 and numbers § 13 a without it, and the
// consolidation of 2021 no longer prints it. Each removal is a step of its own just before the
// repealing act, and only where the base prints the subsection's words once in the provision.
func printedRepealed(base *Law, steps []chainStep, repeals []actRepeal) []chainStep {
	var out []chainStep
	content := base.Model().Content
	for _, r := range repeals {
		for _, st := range steps {
			if st.Version.Accession != r.act || afterBase(st.Version, base) {
				continue // a base older than the act cannot print what it brought
			}
			for _, e := range st.Edits {
				if e.Kind != lexdoc.EditInsertProvision || e.Address.Paragraph == "" || !lexdoc.RulesCover(r.rules, e.Section, e.Number) {
					continue
				}
				wording := []rune(e.NewWording())
				if len(wording) < 40 {
					continue
				}
				loc, ok := content.Find(lexdoc.Address{Recognised: true, Paragraph: e.Address.Paragraph})
				if !ok || loc.Provision == nil {
					continue
				}
				hit := 0
				for i, open := range lexdoc.StykkeOpenings(loc.Provision) {
					if o := []rune(open); len(o) >= 40 && string(o[:40]) == string(wording[:40]) {
						if hit != 0 {
							hit = -1
							break
						}
						hit = i + 1
					}
				}
				if hit <= 0 {
					continue
				}
				def := fmt.Sprintf("§ %s, stk. %d, ophæves.", e.Address.Paragraph, hit)
				undo := lexdoc.Edit{Source: r.by.Accession, Number: "(" + strings.TrimSpace(st.Version.Accession+" "+e.Number) + ")",
					Kind: lexdoc.EditDeleteProvision, Address: lexdoc.ParseAddress(def), Definition: def,
					Prose: fmt.Sprintf("%s repeals %s %s before it took effect; the base prints it", r.by.Accession, st.Version.Accession, e.Number)}
				if r.from.After(r.by.Effective) {
					undo.InForce = r.from
				}
				out = append(out, chainStep{Version: Version{Accession: r.by.Accession, Effective: r.by.Effective.Add(-time.Second), Found: true}, Edits: []lexdoc.Edit{undo}})
			}
		}
	}
	return out
}

// ResolveAsOf is a law on a date with the acts given by acts, without a library's help: what a
// test drives from fixtures. adopted counts every change of the acts signed by asOf.
func ResolveAsOf(base *Law, asOf time.Time, acts func(string) (*ActRecord, bool), adopted bool) *View {
	return lawAsOfMode(base, asOf, acts, adopted)
}
