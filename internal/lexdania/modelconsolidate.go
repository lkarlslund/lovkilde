package lexdania

import (
	"fmt"
	"time"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// Running the same consolidation on the model instead of on the parse tree.
//
// The two paths share everything that is not the thing being edited: the same chain resolution
// (resolveChain), the same decision to leave bills alone, the same target-law filter. What differs
// is the substrate — lexdoc.Content with its spans, rather than nodes with their runs — which is
// the point: the model is what the compiled dataset stores and what the front ends will read, so
// consolidation has to be able to carry out its work there.
//
// LawAsOf (the tree path) stays in place until the parity gate says the two agree, and then the
// consumers move onto this one and the tree path goes away. Until then the duplication is the
// price of proving the change rather than asserting it.

// chainStep is one amending document's contribution, resolved but not applied.
type chainStep struct {
	Version Version
	Edits   []Edit

	// Missing says why no edits could be read from the version: usually that the amending act
	// is not in the archive, which is a fact a reader is owed rather than a gap to hide.
	Missing string
}

// resolveChain opens every version of a law that takes effect by asOf and collects the changes
// each one contributes against that law.
//
// Two counters come back with the steps, because both describe amendments that were *not*
// applied and neither should be invisible: bills (whose text is a proposal rather than enacted
// law, and whose markup looks identical to the enacted one) and changes whose preamble aims at a
// different law. A chain is walked in effective-date order by the caller; the order is what makes
// "§ 12, stk. 2" mean the right thing, not an optimisation.
func resolveChain(base *Document, asOf time.Time, open Opener) (steps []chainStep, bills, foreign int) {
	info := base.Accession()
	for _, ver := range base.Versions() {
		if ver.Effective.After(asOf) {
			steps = append(steps, chainStep{Version: ver})
			continue
		}
		amender, path, ok := open(ver.Accession)
		ver.Path = path
		ver.Found = ok
		if !ok {
			steps = append(steps, chainStep{
				Version: ver,
				Missing: fmt.Sprintf("amending document %s is not in the archive", ver.Accession),
			})
			continue
		}
		if amender.IsBill() {
			bills++
			steps = append(steps, chainStep{Version: ver})
			continue
		}
		var edits []Edit
		for _, e := range amender.Amendments() {
			if !targetsLaw(e.Target, base.Meta, info) {
				foreign++
				continue
			}
			edits = append(edits, e)
		}
		steps = append(steps, chainStep{Version: ver, Edits: edits})
	}
	return steps, bills, foreign
}

// ModelView is a law reconstructed on the model: the content as it stood, and the receipts.
type ModelView struct {
	Accession      string
	Title          string
	AsOf           time.Time
	Content        *lexdoc.Content
	Chain          []Version
	Consolidation  *lexdoc.Consolidation
	SkippedBills   int
	SkippedForeign int
}

// String reports what was applied, refused, and missing, in the same terms as the tree view.
func (v *ModelView) String() string {
	missing := 0
	for _, c := range v.Chain {
		if !c.Found && !c.Effective.After(v.AsOf) {
			missing++
		}
	}
	return fmt.Sprintf("model view of %s as of %s: %d edits applied, %d unresolved, %d chain documents missing",
		v.Accession, v.AsOf.Format(time.DateOnly), len(v.Consolidation.Applied), len(v.Consolidation.Unresolved), missing)
}

// LawAsOfModel reconstructs a law as it stood at a date by applying its amendment chain to the
// model, in effective-date order.
func LawAsOfModel(base *Document, asOf time.Time, open Opener) *ModelView {
	steps, bills, foreign := resolveChain(base, asOf, open)

	amendments := make([]lexdoc.Amendment, 0, len(steps))
	var chain []Version
	for _, st := range steps {
		chain = append(chain, st.Version)
		if len(st.Edits) == 0 {
			continue
		}
		amendments = append(amendments, lexdoc.Amendment{
			Source:    st.Version.Accession,
			Effective: st.Version.Effective,
			Edits:     modelEdits(st.Edits),
		})
	}

	content := base.Model().Content
	view, cons := content.AsOf(amendments, asOf)
	return &ModelView{
		Accession:      base.Meta.Accession,
		Title:          base.Title(),
		AsOf:           asOf,
		Content:        view,
		Chain:          chain,
		Consolidation:  cons,
		SkippedBills:   bills,
		SkippedForeign: foreign,
	}
}

// modelEdits turns parsed changes into the model's form. The only real translation is new *text*:
// the register supplies a replacement provision as markup, and the model needs a provision, which
// is built by the same code that reads a law's own provisions.
func modelEdits(in []Edit) []lexdoc.Edit {
	out := make([]lexdoc.Edit, 0, len(in))
	for _, e := range in {
		me := lexdoc.Edit{
			Source:     e.Source,
			Number:     e.Number,
			Kind:       lexdoc.EditKind(string(e.Kind)),
			Address:    e.Address,
			Old:        e.Old,
			New:        e.New,
			Preceding:  e.Preceding,
			Prose:      e.Prose,
			Definition: e.Definition,
		}
		if e.Address.Heading && e.New == "" && len(e.NewNodes) > 0 {
			// "Overskriften for § 12 ændres til: »X«" carries its new rubric as markup rather
			// than as a quoted string. It is still just text.
			me.New = Squeeze(DisplayText(e.NewNodes[0]))
		}
		if !e.Address.Heading && (e.Kind == EditReplaceProvision || e.Kind == EditInsertProvision) {
			me.NewProvisions, me.NewStykker, me.NewProse, me.Skip = donation(e.NewNodes)
		}
		out = append(out, me)
	}
	return out
}

// donation says what an amending act actually brings with it. The register's "indsættes" and
// "ændres til" instructions carry a run of elements, and the run is rarely what the change's kind
// name suggests: over 90 sampled amending acts, 75 provision-level changes brought a subsection,
// 38 brought loose lines, 24 brought a paragraph, and a few brought a chapter or a rubric.
//
// Every node is classified, not just the first. The tree reader inserted NewNodes[0] and ignored
// the rest, which quietly dropped 89 elements over those 142 changes — law that consolidation
// reported as applied.
//
// The two shapes this model cannot place are reported in the model's own words, never the
// publisher's element names: what comes back from the mirror must not need the schema to be
// understood.
func donation(nodes []*Node) (provisions []*lexdoc.Provision, stykker [][]lexdoc.Segment, prose []string, skip string) {
	if len(nodes) == 0 {
		return nil, nil, nil, ""
	}
	b := &contentBuilder{
		watch:     map[*Node]bool{},
		spanStart: map[*Node]int{},
		spanEnd:   map[*Node]int{},
	}
	for _, n := range nodes {
		switch n.Name {
		case "Stk":
			stykker = append(stykker, b.render(nil, donorClone(n), true))
		case "Paragraf", "TilParagraf", "AendringCentreretParagraf", "IkraftCentreretParagraf":
			p := b.provision(donorClone(n))
			provisions = append(provisions, &p)
		case "Kapitel", "Afsnit":
			skip = "the change brings a chapter, which this model cannot place yet"
		case "ParagrafGruppe", "Rubrica", "KapitelOverskrift", "ParagrafOverskrift":
			skip = "the change brings a heading of its own, which this model cannot place yet"
		case "Indledning":
			skip = "the change brings the document's own indledning, which this model cannot place yet"
		default:
			if text := Squeeze(DisplayText(n)); text != "" {
				prose = append(prose, text)
			}
		}
	}
	return provisions, stykker, prose, skip
}
