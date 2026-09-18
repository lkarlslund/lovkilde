package lexdania

import (
	"sort"
	"strings"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// Text differences between the two consolidation engines are only useful if each one is put in a
// class that says who loses what and why. Counting them all as "unexplained" let a model-side
// text loss - a subsection the model deleted that the tree kept - sit in the same bucket as a
// disagreement about numbering, and the bucket's bound could not tell the two apart.

// debtContext is what is known about one document's amendment chain when a differing unit is
// classified: which words an applied change brought, which of those came from putting new text
// in rather than changing text that was there, and what each engine's provision holds as a whole.
type debtContext struct {
	brought   []broughtFrag
	treeProv  map[string]string
	modelProv map[string]string
}

type broughtFrag struct {
	text     string
	inserted bool
}

func newDebtContext(cons *lexdoc.Consolidation, treeTexts, modelTexts map[string][]string) *debtContext {
	c := &debtContext{treeProv: provisionTexts(treeTexts), modelProv: provisionTexts(modelTexts)}
	add := func(s string, ins bool) {
		if s != "" {
			c.brought = append(c.brought, broughtFrag{text: s, inserted: ins})
		}
	}
	for _, ap := range cons.Applied {
		ins := ap.Edit.Kind == lexdoc.EditInsertProvision || ap.Edit.Kind == lexdoc.EditInsertText
		add(QuoteStrip(ap.Edit.New), ins)
		for _, segs := range ap.Edit.NewStykker {
			add(lexdoc.SegmentsText(segs), ins)
		}
		for _, p := range ap.Edit.NewProvisions {
			add(p.Text(), ins)
		}
		for _, s := range ap.Edit.NewProse {
			add(s, ins)
		}
	}
	sort.Slice(c.brought, func(i, j int) bool { return len(c.brought[i].text) > len(c.brought[j].text) })
	return c
}

// provisionOf is the provision a unit key belongs to: "p:98e,stk3,nr4+" is part of "p:98e".
func provisionOf(key string) string {
	if i := strings.IndexByte(key, ','); i >= 0 {
		return key[:i]
	}
	return key
}

// provisionTexts joins every unit of a provision, so a difference can be checked for whether the
// other engine has those words at all, or only filed under another number.
func provisionTexts(units map[string][]string) map[string]string {
	out := map[string]string{}
	for k, v := range units {
		p := provisionOf(k)
		out[p] = out[p] + "\n" + strings.Join(v, "\n")
	}
	return out
}

// diffTally counts each class of difference. Map classes are keyed by the example so the log can
// name where the loss is; the counts are what the bounds are recorded against.
type diffTally struct {
	treeSideLoss      map[string]int
	insertionPosition map[string]int
	modelSideLoss     map[string]int
	relocated         int
	inlineUnit        map[string]int
	unexplained       int
	examples          []string
}

func newDiffTally() *diffTally {
	return &diffTally{
		treeSideLoss:      map[string]int{},
		insertionPosition: map[string]int{},
		modelSideLoss:     map[string]int{},
		inlineUnit:        map[string]int{},
	}
}

func (d *diffTally) total(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func (d *diffTally) note(example string) {
	if len(d.examples) < 8 {
		d.examples = append(d.examples, example)
	}
}

// classify puts one differing unit in the first class that accounts for it, most specific first,
// and returns the class name so a measurement of the classes themselves can be checked.
//
// For a unit that is part of a provision, the question is simply whether the law is there: is the
// tree's text anywhere in the model's provision, and the model's in the tree's? That decides loss
// without needing to know which change brought the words, so pre-existing law that an engine
// dropped counts as lost too. Only when both engines hold both texts - a disagreement about which
// subsection or condition a passage is - is provenance irrelevant. Whole-provision keys compare
// against themselves and are settled by provenance instead.
func (c *debtContext) classify(d *diffTally, prov string, unit bool, treeText, modelText, describe string) string {
	want, have := lexdoc.MatchText(treeText), lexdoc.MatchText(modelText)
	tp, mp := lexdoc.MatchText(c.treeProv[prov]), lexdoc.MatchText(c.modelProv[prov])
	// A unit one engine has and the other does not have at all - an empty side - is a loss of the
	// clearest kind, and must not fall through to "unexplained" because it has no text to search.
	if unit && (len(want) > 20 || len(have) > 20) {
		inModelProv := len(want) <= 20 || strings.Contains(mp, want)
		inTreeProv := len(have) <= 20 || strings.Contains(tp, have)
		switch {
		case !inModelProv:
			d.modelSideLoss[describe]++
			d.note("the model does not hold this law anywhere in the provision: " + describe)
			return "model-side-loss"
		case !inTreeProv:
			if c.fromInsertion(have) {
				d.insertionPosition[describe]++
				d.note("an applied insertion landed where the tree has nothing: " + describe)
				return "insertion-position"
			}
			d.treeSideLoss[describe]++
			d.note("the tree does not hold this law anywhere in the provision: " + describe)
			return "tree-side-loss"
		case !inModelProv || !inTreeProv:
			// Both engines hold law here that the other does not: a text disagreement, which
			// provenance may still explain.
			break
		case want == "" || have == "":
			// One engine has no unit at this address, but its provision holds the words the other
			// has there. The law is present and not addressable: a numbered condition that became
			// inline text cannot be cited, looked up, or indexed as a condition.
			d.inlineUnit[describe]++
			d.note("the words are in the provision but no unit is addressed here: " + describe)
			return "inline-unit"
		case len(want) > 20 && len(have) > 20:
			d.relocated++
			d.note("the same words filed under another number: " + describe)
			return "relocated"
		}
	}
	for _, f := range c.brought {
		frag := lexdoc.MatchText(f.text)
		if len(frag) < 24 {
			continue
		}
		inTree, inModel := strings.Contains(tp, frag), strings.Contains(mp, frag)
		switch {
		case inModel && !inTree:
			if f.inserted {
				// The model puts inserted text after the provision the change addresses and
				// records that the position within a run of insertions is approximate; the tree
				// splices at whatever position the publisher's file has. Both wrote the words.
				d.insertionPosition[describe]++
				d.note("an applied insertion landed at another position: " + describe)
				return "insertion-position"
			}
			d.treeSideLoss[describe]++
			d.note("the tree wrote none of this change's words: " + describe)
			return "tree-side-loss"
		case inTree && !inModel:
			d.modelSideLoss[describe]++
			d.note("the model wrote none of this change's words: " + describe)
			return "model-side-loss"
		}
	}
	d.unexplained++
	d.note("no change accounts for this: " + describe)
	return "unexplained"
}

// fromInsertion reports whether a stretch of text came from an applied change that put new text
// in rather than altering text that was there.
func (c *debtContext) fromInsertion(text string) bool {
	for _, f := range c.brought {
		if !f.inserted {
			continue
		}
		frag := lexdoc.MatchText(f.text)
		if len(frag) < 24 {
			continue
		}
		if strings.Contains(text, frag) || strings.Contains(frag, text) {
			return true
		}
	}
	return false
}
