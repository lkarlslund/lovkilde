package lexdania

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// TestModelRefusalClassesOnCorpus explains every change the tree engine applied and the model
// engine refused, at the moment the change was offered.
//
// A refusal is only worth as much as its explanation, and "the passage is not there" is a claim
// about the whole act, not about the part the amendment addressed. So the chain is replayed change
// by change against the state the change actually met, and each refusal is asked where the quoted
// passage was at that moment.
//
// The classes are what a reader and the refusal report need:
//
//   - absent from the act: the passage is in no provision. Whatever the tree edited, it was not
//     this passage.
//   - present elsewhere: the passage is in the act but not where the amendment said. Refusing is
//     correct, and a tree that applied it edited a different part than the act pointed at.
//   - other: insertions and changes that quote nothing, classified by what they tried to do.
func TestModelRefusalClassesOnCorpus(t *testing.T) {
	a, err := Scan(t.Context(), corpusDir(t))
	if err != nil {
		t.Fatal(err)
	}
	asOf := time.Now()
	classes := map[string]int{}
	examples := map[string]string{}
	considered, refusedBoth, diverging, treeNoOps := 0, 0, 0, 0

	for _, e := range recentLawEntries(a.Entries(), corpusEntryBudget(t)) {
		doc, _, ok := a.Open(e.Accession)
		if !ok {
			continue
		}
		steps, _, _ := resolveChain(doc, asOf, a.Open)
		if len(steps) == 0 {
			continue
		}
		tree := LawAsOf(doc, asOf, a.Open)
		treeNoOps += tree.NoOpEdits
		// The same document with no amendments applied: the text the tree started from. A
		// passage that exists here but not in the model is a part of the document the model
		// does not carry, which is a named gap rather than a contradiction.
		baseTree := LawAsOf(doc, time.Time{}, a.Open).Text()
		mod := doc.Model()
		c := &mod.Content
		for _, st := range steps {
			for _, ed := range modelEdits(st.Edits) {
				considered++
				out := c.Apply(ed)
				if out.Applied {
					continue
				}
				if !treeApplied(tree, ed) {
					refusedBoth++
					continue // Both engines refuse it: nothing to explain.
				}
				diverging++
				class := classifyRefusal(c, ed, baseTree, tree.Text(), a, doc, asOf)
				if _, seen := examples[class]; !seen {
					examples[class] = fmt.Sprintf("%s %s %s: %s", e.Accession, ed.Source, ed.Number, cut(out.Reason, 70))
				}
				_ = class
				classes[class]++
				if _, seen := examples[class]; !seen {
					examples[class] = fmt.Sprintf("%s %s %s: %s", e.Accession, ed.Source, ed.Number, cut(out.Reason, 70))
				}
				_ = class
			}
		}
	}

	if n := classes["target present as addressed"]; n != 0 {
		t.Errorf("%d refusals were wrong: the passage sat exactly where the amendment addressed it", n)
	}
	if n := classes["other"]; n != 0 {
		t.Errorf("%d refusals could not be classified, so the engine cannot explain them", n)
	}

	names := make([]string, 0, len(classes))
	for k := range classes {
		names = append(names, k)
	}
	sort.Strings(names)
	t.Logf("replayed %d model-level changes: %d refused by both, %d applied by the tree and refused by the model",
		considered, refusedBoth, diverging)
	t.Logf("the tree reported %d edits applied without changing any text", treeNoOps)
	for _, k := range names {
		t.Logf("%-22s %3d  e.g. %s", k, classes[k], examples[k])
	}
}

// classifyRefusal says what a refused change was actually up against, by looking at the content at
// that moment rather than by repeating the engine's own words back.
//
// The distinction that matters is between "this part of the act does not exist" and "this part
// exists but does not contain those words": they read alike in a refusal count and mean entirely
// different things to anyone checking a citation.
func classifyRefusal(c *lexdoc.Content, ed lexdoc.Edit, original, amended string, a *Archive, doc *Document, asOf time.Time) string {
	switch {
	case len(ed.NewProvisions) > 0 || len(ed.NewStykker) > 0:
		return "insertion refused"
	case ed.Address.Heading:
		return "heading refused"
	}
	loc, ok := c.Find(ed.Address)
	if !ok {
		if ed.Address.Paragraph == "" {
			return "address not recognised"
		}
		// A missing § 17 and a missing stk. 5 of § 17 are different facts: the first says the
		// provision is not in the act (often repealed, or an entry-into-force paragraph the
		// tree mistook for the real one), the second says the act has the provision but not
		// that part of it.
		for _, ref := range c.Provisions() {
			if ref.Provision.Number == ed.Address.Paragraph {
				return "addressed part absent, provision present"
			}
		}
		return "addressed provision absent"
	}
	passage := ed.Old
	if passage == "" {
		passage = ed.New
	}
	if passage == "" {
		return "nothing quoted"
	}
	// The words a change quotes are only expected to be present beforehand when the change is
	// about words that are already there. Insertions and whole-provision replacements quote text
	// that is absent until they are carried out, so asking them "where was it?" is a category
	// error; what was asked of them is whether the address held.
	switch ed.Kind {
	case lexdoc.EditInsertText, lexdoc.EditInsertProvision:
		return "words to insert, address held"
	case lexdoc.EditReplaceProvision, lexdoc.EditDeleteProvision:
		return "whole-provision change, address held"
	}
	needle := QuoteStrip(passage)
	where := loc.String()
	occ := c.Occurrences(needle)
	for _, o := range occ {
		if o == where {
			if ed.Kind == lexdoc.EditInsertText && strings.Contains(needle, ed.New) {
				return "insertion already present"
			}
			return "target present as addressed"
		}
	}
	if len(occ) > 0 {
		return "passage present in another part"
	}
	if ed.Kind == lexdoc.EditInsertText {
		return "insertion target present, words absent"
	}
	// Three readings of "those words are not here", told apart by which text they are missing
	// from. Only the last is a contradiction: the tree edited words that no version of the
	// document contains.
	if strings.Contains(MatchText(original), MatchText(needle)) {
		return "outside the model's content"
	}
	if strings.Contains(MatchText(amended), MatchText(needle)) {
		return "words a refused step would have created"
	}
	if looseIn(original, needle) {
		return "same words, other normalisation"
	}
	// The endpoints are not the whole history: an earlier change can have written these words
	// and a later one taken them away again, so a passage missing from both can still have been
	// there at the moment in question. The tree is addressable by date, which makes the
	// question answerable rather than speculative.
	if inAnyTreeView(a, doc, asOf, needle) {
		return "words a refused step would have created"
	}
	return "passage absent from the act"
}

// looseIn reports whether two texts share a passage once every character that carries no legal
// meaning is dropped: spacing, dashes, quotes, soft hyphens. A refusal that survives MatchText but
// not this means the two engines disagree about how much typographic difference is allowed between
// a quoted passage and the text it matches, which is a decision, not a fact.
func looseIn(text, passage string) bool {
	return strings.Contains(loose(text), loose(passage))
}

func loose(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 0x7f && unicode.IsLetter(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// inAnyTreeView reports whether the tree engine ever held these words while bringing the law up
// to date, by rebuilding the view at each effective date in the chain.
func inAnyTreeView(a *Archive, doc *Document, asOf time.Time, needle string) bool {
	needle = MatchText(needle)
	seen := map[time.Time]bool{}
	for _, ver := range doc.Versions() {
		if ver.Effective.After(asOf) || seen[ver.Effective] {
			continue
		}
		seen[ver.Effective] = true
		if strings.Contains(MatchText(LawAsOf(doc, ver.Effective, a.Open).Text()), needle) {
			return true
		}
	}
	return false
}

// changeIdentity identifies one numbered change. The number alone does not: an act that amends
// several laws has one change list per law, so "5." appears again in each block, and keying on it
// alone compares a change against a different change that happens to share its number.
func changeIdentity(source, number, prose string) string {
	return source + " " + number + " " + MatchText(prose)
}

// treeApplied reports whether the tree engine carried out this change.
func treeApplied(v *View, ed lexdoc.Edit) bool {
	want := changeIdentity(ed.Source, ed.Number, ed.Prose)
	for _, ap := range v.Applied {
		if changeIdentity(ap.Edit.Source, ap.Edit.Number, ap.Edit.Prose) == want {
			return true
		}
	}
	return false
}
