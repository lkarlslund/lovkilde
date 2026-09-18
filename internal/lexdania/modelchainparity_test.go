package lexdania

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
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
	var outcomeExamples, diffExamples []string
	treeSideLoss := map[string]int{}
	unexplained := 0

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
		brought := broughtWords(model.Consolidation)
		_, treeTexts, _ := sectionKeys(tree.Sections())
		_, modelTexts, _, _ := contentKeys(model.Content)
		for k, tv := range treeTexts {
			if !comparableText(k) {
				continue
			}
			mv := modelTexts[k]
			if len(tv) != len(mv) {
				classifyDiff(treeSideLoss, &unexplained, &diffExamples, brought,
					strings.Join(tv, "\n"), strings.Join(mv, "\n"),
					fmt.Sprintf("%s %s: %d units on the tree, %d on the model", e.Accession, k, len(tv), len(mv)))
				continue
			}
			for i := range tv {
				if tv[i] == mv[i] {
					continue
				}
				classifyDiff(treeSideLoss, &unexplained, &diffExamples, brought, tv[i], mv[i],
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
	t.Logf("consolidation parity over %d documents with chains: tree applied %d / refused %d, model applied %d / refused %d; text debt: %d units the tree wrote none of, %d units unexplained",
		checked, appliedTree, refusedTree, appliedModel, refusedModel, treeLossTotal(treeSideLoss), unexplained)
	if outcomeDiffs > 0 {
		for _, x := range outcomeExamples {
			t.Log(x)
		}
		t.Errorf("%d changes were applied by one engine and refused by the other", outcomeDiffs)
	}
	// Text parity is not reached, and the two remaining classes are recorded here as measured
	// debt with a bound that may only fall. Softening the comparison would hide the debt; a
	// ratchet keeps it visible and stops it growing while it is worked off. Each class says who
	// loses what:
	//
	//   - treeSideLossBound: units whose text is missing words an applied change brought, while
	//     the tree reported that change applied. The model carries the words, so this is the
	//     shipped engine losing law it claims to have written (multi-node donations and
	//     positionally anchored insertions).
	//   - unexplainedBound: units holding different words with no single change to account for
	//     them. Sampling shows these are subsections filed under different numbers after
	//     structural edits, which is the open question this task has not answered yet.
	//
	// Both bounds are the counts measured on 2026-09-18 over this corpus slice.
	const treeSideLossBound, unexplainedBound = 9, 30

	missed, open := treeLossTotal(treeSideLoss), unexplained
	if missed > treeSideLossBound {
		for _, x := range diffExamples {
			t.Log(x)
		}
		t.Errorf("%d units are missing words an applied change brought, while the tree reported that change applied; the recorded debt was %d", missed, treeSideLossBound)
	}
	if open > unexplainedBound {
		for _, x := range diffExamples {
			t.Log(x)
		}
		t.Errorf("%d units hold different words with no change to account for them; the recorded debt was %d", open, unexplainedBound)
	}
	if missed < treeSideLossBound || open < unexplainedBound {
		t.Logf("debt fell below its recorded bound (tree-side loss %d/%d, unexplained %d/%d): lower the bounds to lock the gain in",
			missed, treeSideLossBound, open, unexplainedBound)
	}
}

// broughtWords returns the text the applied changes brought, longest first: the words a provision
// has to contain if the change that named them was carried out.
func broughtWords(cons *lexdoc.Consolidation) []string {
	var out []string
	for _, ap := range cons.Applied {
		if ap.Edit.New != "" {
			out = append(out, QuoteStrip(ap.Edit.New))
		}
		for _, segs := range ap.Edit.NewStykker {
			out = append(out, lexdoc.SegmentsText(segs))
		}
		for _, p := range ap.Edit.NewProvisions {
			out = append(out, p.Text())
		}
		out = append(out, ap.Edit.NewProse...)
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// classifyDiff decides whether a text difference is a change one engine reported and did not
// carry out, or a disagreement no change explains. The second kind is the one that matters, so it
// is counted separately and asserted separately.
func classifyDiff(treeLoss map[string]int, unexplained *int, examples *[]string, brought []string, treeText, modelText, describe string) {
	want := lexdoc.MatchText(treeText)
	have := lexdoc.MatchText(modelText)
	for _, f := range brought {
		frag := lexdoc.MatchText(f)
		if len(frag) < 24 {
			continue
		}
		if strings.Contains(have, frag) && !strings.Contains(want, frag) {
			treeLoss[describe]++
			if len(*examples) < 6 {
				*examples = append(*examples, "the tree wrote none of this change's words: "+describe)
			}
			return
		}
	}
	*unexplained++
	if len(*examples) < 6 {
		*examples = append(*examples, "no change accounts for this: "+describe)
	}
}

func treeLossTotal(m map[string]int) int {
	total := 0
	for _, n := range m {
		total += n
	}
	return total
}
