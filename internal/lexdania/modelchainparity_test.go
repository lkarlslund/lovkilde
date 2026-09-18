package lexdania

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Consolidation on the model has to produce the same law as consolidation on the tree, on real
// amendment chains. The two paths already share the chain resolution, so what this measures is
// the part that moved: locating a quoted passage, editing it in place, inserting and repealing
// provisions, and refusing what cannot be done.
//
// The comparison is deliberately not "both produce text". It is: the same change is applied or
// refused by both, the refusal is about the same thing, and every provision's text afterwards is
// the same string.

func TestModelConsolidationParityOnCorpus(t *testing.T) {
	a, err := Scan(t.Context(), corpusDir(t))
	if err != nil {
		t.Fatal(err)
	}

	budget := corpusEntryBudget(t)
	checked, chainsWithEdits := 0, 0
	var appliedTree, appliedModel, refusedTree, refusedModel int
	var outcomeDiffs int
	// Examples are kept per kind of difference, so one noisy class cannot crowd the others out
	// of the log.
	var outcomeExamples []string
	debt := newDiffTally()

	for _, e := range recentLawEntries(a.Entries(), budget) {
		vs, err := a.Versions(e.Accession)
		if err != nil || len(vs) == 0 {
			continue
		}
		doc, _, ok := a.Open(e.Accession)
		if !ok {
			continue
		}
		asOf := time.Now()
		tree := LawAsOf(doc, asOf, a.Open)
		model := LawAsOfModel(doc, asOf, a.Open)
		if len(tree.Applied)+len(tree.Unresolved)+len(model.Consolidation.Applied)+len(model.Consolidation.Unresolved) == 0 {
			continue
		}
		checked++
		if len(tree.Applied)+len(tree.Unresolved) > 0 {
			chainsWithEdits++
		}

		// Which changes each engine carried out, keyed by the amending document and the
		// change's own number — the citation a reader would check it against.
		key := func(source, number, prose string) string { return changeIdentity(source, number, prose) }
		treeApplied := map[string]int{}
		for _, ap := range tree.Applied {
			treeApplied[key(ap.Edit.Source, ap.Edit.Number, ap.Edit.Prose)]++
		}
		modelApplied := map[string]int{}
		for _, ap := range model.Consolidation.Applied {
			modelApplied[key(ap.Edit.Source, ap.Edit.Number, ap.Edit.Prose)]++
		}
		treeRefused := map[string]string{}
		for _, un := range tree.Unresolved {
			if un.Edit.Number == "" {
				continue // a missing chain document, reported the same way by both paths
			}
			treeRefused[key(un.Edit.Source, un.Edit.Number, un.Edit.Prose)] = un.Reason
		}
		modelRefused := map[string]string{}
		for _, un := range model.Consolidation.Unresolved {
			modelRefused[key(un.Edit.Source, un.Edit.Number, un.Edit.Prose)] = un.Reason
		}
		appliedTree += len(treeApplied)
		appliedModel += len(modelApplied)
		refusedTree += len(treeRefused)
		refusedModel += len(modelRefused)

		for k := range treeApplied {
			if modelApplied[k] == 0 {
				outcomeDiffs++
				if len(outcomeExamples) < 8 {
					outcomeExamples = append(outcomeExamples, fmt.Sprintf("tree applied, model refused: %s: change %s (%s)",
						e.Accession, k, Squeeze(modelRefused[k])))
				}
			}
		}
		for k := range modelApplied {
			if treeApplied[k] == 0 {
				outcomeDiffs++
				if len(outcomeExamples) < 8 {
					outcomeExamples = append(outcomeExamples, fmt.Sprintf("model applied, tree refused: %s: change %s (%s)",
						e.Accession, k, Squeeze(treeRefused[k])))
				}
			}
		}

		// The law both engines produced, provision by provision. A difference is only worth
		// calling a difference once it has been traced to a change: the words an amendment
		// brought are known, so a provision that holds them in one engine and not the other is
		// a reported-applied change that produced no text, not an argument about formatting.
		_, treeTexts, _ := sectionKeys(tree.Sections())
		_, modelTexts, _, _ := contentKeys(model.Content)
		ctx := newDebtContext(model.Consolidation, treeTexts, modelTexts)
		for k, tv := range treeTexts {
			if !comparableText(k) {
				continue
			}
			mv := modelTexts[k]
			prov := provisionOf(k)
			if len(tv) != len(mv) {
				ctx.classify(debt, prov, prov != k, strings.Join(tv, "\n"), strings.Join(mv, "\n"),
					fmt.Sprintf("%s %s: %d units on the tree, %d on the model", e.Accession, k, len(tv), len(mv)))
				continue
			}
			for i := range tv {
				if tv[i] == mv[i] {
					continue
				}
				ctx.classify(debt, prov, prov != k, tv[i], mv[i],
					fmt.Sprintf("%s %s:\n  tree: %q\n model: %q", e.Accession, k, cut(tv[i], 140), cut(mv[i], 140)))
			}
		}
		if checked >= 40 {
			break
		}
	}

	if checked < 5 {
		t.Skipf("only %d chained documents with amendments in this archive slice", checked)
	}
	t.Logf("consolidation parity over %d documents with chains: tree applied %d / refused %d, model applied %d / refused %d; text debt: %d units the tree lacks, %d units an insertion put elsewhere, %d units the model lacks, %d units with words but no addressable unit, %d units filed under another number, %d units unexplained",
		checked, appliedTree, refusedTree, appliedModel, refusedModel,
		debt.total(debt.treeSideLoss), debt.total(debt.insertionPosition), debt.total(debt.modelSideLoss),
		debt.total(debt.inlineUnit), debt.relocated, debt.unexplained)
	for _, x := range debt.examples {
		t.Log(x)
	}
	if outcomeDiffs > 0 {
		for _, x := range outcomeExamples {
			t.Log(x)
		}
		t.Errorf("%d changes were applied by one engine and refused by the other", outcomeDiffs)
	}
	// Text parity is not reached yet, and what remains is recorded as measured debt in classes
	// that each say who loses what. Softening the comparison would hide the debt; a per-class
	// bound keeps it visible and stops any class growing while it is worked off. A bound may only
	// fall. Measuring the classes rather than one "unexplained" bucket is what found that the
	// bucket held a deleted subsection, and that the class previously called tree-side loss was
	// an artifact of comparing unit texts in isolation: measured against the whole provision, the
	// tree's engine loses no law at all here.
	//
	//   - treeSideLossBound: law the model holds that the tree's provision does not hold anywhere.
	//   - insertionPositionBound: the same, where the words came from an applied insertion. The
	//     model puts inserted text after the addressed provision and records that the position
	//     inside a run of insertions is approximate; the tree splices at whatever position the
	//     publisher's file has. This is the measured cost of that choice.
	//   - modelSideLossBound: law the tree holds that the model's provision does not hold anywhere.
	//     Sampling: a numbered condition brought by a multi-node donation.
	//   - inlineUnitBound: the words are in the provision but no unit is addressed there - a
	//     condition that became inline text and so cannot be cited, looked up or indexed. Feeds
	//     the unit-recall measurement in the task that indexes units from the model.
	//   - relocatedBound: both engines have a unit here and hold each other's words elsewhere in
	//     the provision: the same law filed under another subsection or condition number.
	//   - unexplainedBound: what none of those checks accounts for.
	//
	// The bounds are the counts measured on 2026-09-18 over this corpus slice: 21 documents with
	// amendment chains, 25 differing units, all six classes accounted for.
	const (
		treeSideLossBound      = 0
		insertionPositionBound = 9
		modelSideLossBound     = 2
		inlineUnitBound        = 6
		relocatedBound         = 6
		unexplainedBound       = 2
	)
	for _, c := range []struct {
		name  string
		count int
		bound int
	}{
		{"the tree's provision does not hold this law", debt.total(debt.treeSideLoss), treeSideLossBound},
		{"an applied insertion landed where the tree has nothing", debt.total(debt.insertionPosition), insertionPositionBound},
		{"the model's provision does not hold this law", debt.total(debt.modelSideLoss), modelSideLossBound},
		{"the words are present but no unit is addressed there", debt.total(debt.inlineUnit), inlineUnitBound},
		{"the same words are filed under another number", debt.relocated, relocatedBound},
		{"no change accounts for the difference", debt.unexplained, unexplainedBound},
	} {
		switch {
		case c.count > c.bound:
			for _, x := range debt.examples {
				t.Log(x)
			}
			t.Errorf("%d units differ: %s; the recorded debt was %d", c.count, c.name, c.bound)
		case c.count < c.bound:
			t.Logf("debt class %q measured %d against its recorded %d: lower the bound to lock the gain in",
				c.name, c.count, c.bound)
		}
	}
}
