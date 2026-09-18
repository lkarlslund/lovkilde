package lexdania

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// TestModelEditParityOnCorpus splices the same real passage through both paths and requires
// the same bytes out — including agreement about which passages are not there.
//
// This is the gate the flat text slice has to pass before consolidation moves onto it. The
// tree reader's splice edits runs inside one node, so it can only ever see the provision it was
// given; the model edits one slice that every level reads, so the equivalence is not structural
// and cannot be assumed. Comparing the whole key-to-text map after each single edit is what
// makes the claim specific: an edit to stk. 3 must leave both representations agreeing about
// § 5, about the whole document, and about every provision the edit did not touch.
//
// Agreement about refusal matters as much as agreement about success. A passage the model finds
// when the tree reader did not would apply an amendment the register's own reader declines, and
// that is a fabricated legal text rather than a formatting difference.
func TestModelEditParityOnCorpus(t *testing.T) {
	dir := corpusDir(t)
	files := sampleFiles(t, dir, 120)

	var cases, applied, refusedBoth, textCompared int
	var diffs []string
	report := func(f, key, what string) {
		if len(diffs) < 12 {
			diffs = append(diffs, fmt.Sprintf("%s %s: %s", f, key, what))
		}
	}

	for _, f := range files {
		doc, err := LoadWithBody(f)
		if err != nil {
			t.Logf("parse failed (the crawl may be mid-write): %v", err)
			continue
		}
		provisions := doc.Model().Content
		secs := doc.Sections()

		// Only units that appear exactly once are edited: two provisions sharing a number in
		// one document is a real corpus fact, and which one an edit addresses would then be a
		// question this test has no business answering.
		modelUnits := singleUnits(&provisions)
		if len(modelUnits) == 0 {
			continue
		}
		uniqueOld := map[string]bool{}
		for _, s := range secs {
			k := secKey(s)
			if uniqueOld[k] {
				uniqueOld[k] = false // ambiguous in the flat list; leave it alone
			} else {
				uniqueOld[k] = true
			}
		}

		done := 0
		for _, s := range secs {
			if done >= 3 {
				break
			}
			key := secKey(s)
			unit, inModel := modelUnits[key]
			if !inModel || !uniqueOld[key] || !comparableText(key) {
				continue
			}
			// The passage comes from the unit the edit will address, not from the section
			// object: in the flat list a paragraph section's own text is the whole
			// containing part, so a passage cut from its middle can sit in a different
			// provision entirely, and both readers would then be right about different
			// things. Parity gate one already proves the two representations agree about
			// this unit's text, so taking the passage from the model unit tests the splice
			// rather than the sampling.
			old := middlePassage(unit.p.SpanText(unit.span))
			if old == "" {
				continue
			}
			done++
			cases++

			// Path A: the tree reader, on its own fresh parse.
			a, err := LoadWithBody(f)
			if err != nil {
				t.Logf("reparse failed: %v", err)
				continue
			}
			aSec := findSection(a.Sections(), key)
			if aSec == nil {
				report(f, key, "section vanished on reparse")
				continue
			}
			before := aSec.Display()
			okA, changedA := aSec.splice(old, "udskiftet ordtekst")

			// Path B: the model, on its own fresh parse.
			b, err := LoadWithBody(f)
			if err != nil {
				t.Logf("reparse failed: %v", err)
				continue
			}
			// One Model() per document: it derives the content afresh every call, and an
			// edit applied to one instance is invisible in the next, which is exactly the
			// kind of difference this gate must not confuse with a real one.
			bModel := b.Model()
			bUnits := singleUnits(&bModel.Content)
			bU, ok := bUnits[key]
			if !ok {
				report(f, key, "unit vanished in the model on reparse")
				continue
			}
			okB := bU.p.ReplaceSpan(bU.span, old, "udskiftet ordtekst")

			if okA != okB {
				report(f, key, fmt.Sprintf("the two paths disagree about whether the passage exists: tree %v, model %v; passage %q",
					okA, okB, cut(old, 80)))
				continue
			}
			if !okA {
				refusedBoth++
				continue
			}
			applied++

			// A splice that reports success without changing the text is the failure this
			// whole gate exists to catch: the register's own reader did exactly that for
			// passages inside numbered items, and consolidation recorded the amendment as
			// applied. Checking it here means no future case can do it quietly.
			if after := unitText(a.Sections(), key); after == before {
				// changedA tells the two ways this happens apart: the splice wrote text
				// identical to the old (changed=false), or it wrote something different into
				// a run the display does not read from (changed=true) — which is what
				// duplicated runs did.
				report(f, key, fmt.Sprintf("the tree splice reported success and left the text unchanged (splice reported changed=%v)", changedA))
				continue
			}

			// Same edit applied: every unit in the document must read the same in both, not
			// only the one that was edited.
			_, aTexts, _ := sectionKeys(a.Sections())
			_, bTexts, _, _ := contentKeys(&bModel.Content)
			for k, av := range aTexts {
				if !comparableText(k) {
					continue
				}
				bv := bTexts[k]
				if len(av) != len(bv) {
					report(f, k, fmt.Sprintf("instance count differs after the edit: %d vs %d", len(av), len(bv)))
					continue
				}
				for i := range av {
					textCompared++
					if av[i] != bv[i] {
						report(f, k, fmt.Sprintf("text differs after editing %s at rune %d\n tree: …%s…\nmodel: …%s…",
							key, firstDifference(av[i], bv[i]), around(av[i], firstDifference(av[i], bv[i])), around(bv[i], firstDifference(av[i], bv[i]))))
						break
					}
				}
			}

			// An absent passage must be refused identically too. The suffix cannot occur in
			// legal Danish, so this is a refusal both readers owe.
			a2, err := LoadWithBody(f)
			if err != nil {
				continue
			}
			b2, err := LoadWithBody(f)
			if err != nil {
				continue
			}
			missing := old + " zz ikke-tilstrækkeligt"
			absentA, _ := findSection(a2.Sections(), key).splice(missing, "x")
			b2Model := b2.Model()
			b2U := singleUnits(&b2Model.Content)[key]
			absentB := b2U.p.ReplaceSpan(b2U.span, missing, "x")
			if absentA != absentB {
				report(f, key, fmt.Sprintf("the two paths disagree about refusing an absent passage: tree %v, model %v",
					absentA, absentB))
			} else if absentA {
				report(f, key, "both paths matched a passage that cannot be present")
			}
		}
	}

	if cases < 30 {
		t.Fatalf("only %d edit cases across the corpus sample; the gate is not measuring anything", cases)
	}
	if len(diffs) > 0 {
		t.Errorf("splice parity failed in %d of %d cases (%d applied, %d refused by both, %d unit texts compared):\n%s",
			len(diffs), cases, applied, refusedBoth, textCompared, strings.Join(diffs, "\n"))
	}
	t.Logf("splice parity: %d cases, %d applied, %d refused identically, %d unit texts identical afterwards",
		cases, applied, refusedBoth, textCompared)
}

// firstDifference is the rune offset where two texts part company, which is where a splice
// went wrong; reporting the whole provision would hide it in a paragraph of identical text.
func firstDifference(a, b string) int {
	ar, br := []rune(a), []rune(b)
	for i := 0; i < len(ar) && i < len(br); i++ {
		if ar[i] != br[i] {
			return i
		}
	}
	return min(len(ar), len(br))
}

func around(s string, at int) string {
	r := []rune(s)
	from, to := max(0, at-40), min(len(r), at+40)
	return strings.ReplaceAll(string(r[from:to]), "\n", " ")
}

// unitText reads a unit's text back out of a freshly derived section list.
func unitText(secs []*Section, key string) string {
	s := findSection(secs, key)
	if s == nil {
		return ""
	}
	return s.Display()
}

// TestSectionRunsAreTheSectionText pins the property splice depends on: a section's run list
// must be each run in its subtree exactly once, in the order the display renders them, and must
// concatenate (under the display's own trimming rule) to the text the section shows.
//
// It is not a stylistic preference. The run list is what a located match is turned back into
// offsets in, so a list that repeats a run makes the offsets point into a text that is not the
// provision, and the write-back then overwrites the edited copy with the untouched one — the
// old reader reported amendments applied while leaving the law exactly as it was. A list ordered
// differently from the display does the same thing more quietly, by finding the passage at the
// wrong offset.
func TestSectionRunsAreTheSectionText(t *testing.T) {
	dir := corpusDir(t)
	files := sampleFiles(t, dir, 120)
	var checked, duplicated, lost, outOrder int
	var examples []string
	for _, f := range files {
		doc, err := LoadWithBody(f)
		if err != nil {
			continue
		}
		for _, s := range doc.Sections() {
			seen := map[TextRun]bool{}
			dup := false
			displ := lexdoc.Squeeze(s.Display())
			at, missing, outOfOrder := 0, false, false
			for _, r := range s.Runs {
				if seen[r] {
					dup = true
				}
				seen[r] = true
				piece := lexdoc.Squeeze(r.Node.Runs[r.Idx])
				if piece == "" {
					continue
				}
				found := strings.Index(displ[at:], piece)
				if found < 0 {
					// The run's text is not in the displayed text at or after where the
					// previous run landed. Either it is absent, or it is there and the list
					// put it in the wrong order — which is what a walk that visits a node's
					// own runs before its children's does to an inline <Char> or <Nota>.
					if !strings.Contains(displ, piece) {
						missing = true
					} else {
						outOfOrder = true
					}
					break
				}
				at += found + len(piece)
			}
			checked++
			switch {
			case dup:
				duplicated++
				if len(examples) < 5 {
					examples = append(examples, fmt.Sprintf("%s %s: a run appears more than once in the list", f, secKey(s)))
				}
			case missing:
				lost++
				if len(examples) < 5 {
					examples = append(examples, fmt.Sprintf("%s %s: run text absent from the section's own text", f, secKey(s)))
				}
			case outOfOrder:
				outOrder++
				if len(examples) < 5 {
					examples = append(examples, fmt.Sprintf("%s %s: runs are not in the order the section displays them", f, secKey(s)))
				}
			}
		}
	}
	if checked < 500 {
		t.Fatalf("only %d sections checked; the invariant is not being measured", checked)
	}
	if duplicated > 0 || lost > 0 || outOrder > 0 {
		t.Errorf("%d of %d sections have a duplicated run, %d have run text missing from their own text, %d have runs out of display order: %s",
			duplicated, checked, lost, outOrder, strings.Join(examples, "; "))
	}
	t.Logf("run-list invariant holds for %d sections over %d documents", checked, len(files))
}

// unitRef points at one addressable unit in the model: a provision, or a span inside one.
type unitRef struct {
	p    *lexdoc.Provision
	span lexdoc.Span
}

// singleUnits names every unit the model can edit that appears exactly once in the document,
// keyed the way the flat section list keys it.
func singleUnits(c *lexdoc.Content) map[string]unitRef {
	all := map[string][]unitRef{}
	var walk func(nodes []lexdoc.Node)
	walk = func(nodes []lexdoc.Node) {
		for _, n := range nodes {
			switch {
			case n.Part != nil:
				walk(n.Part.Order)
			case n.Provision != nil:
				addUnits(&all, n.Provision)
			}
		}
	}
	walk(c.Order)
	for i := range c.Appendices {
		walk(c.Appendices[i].Order)
	}
	out := map[string]unitRef{}
	for k, v := range all {
		if len(v) == 1 {
			out[k] = v[0]
		}
	}
	return out
}

func addUnits(all *map[string][]unitRef, p *lexdoc.Provision) {
	key := "p:" + p.Address()
	(*all)[key] = append((*all)[key], unitRef{p: p, span: lexdoc.Span{From: 0, To: len(p.Segments)}})
	for i := range p.Stykker {
		st := &p.Stykker[i]
		skey := fmt.Sprintf("%s,stk%d", key, st.Nr)
		(*all)[skey] = append((*all)[skey], unitRef{p: p, span: st.Span})
		for _, it := range st.Items {
			ikey := fmt.Sprintf("%s,nr%d+%s", skey, it.Nr, it.Litra)
			(*all)[ikey] = append((*all)[ikey], unitRef{p: p, span: it.Span})
		}
	}
}

// findSection returns the section the given key names, or nil. It matches the same way the
// parity gate matches units, so a mismatch here is a mismatch there.
func findSection(secs []*Section, key string) *Section {
	for _, s := range secs {
		if secKey(s) == key {
			return s
		}
	}
	return nil
}

// middlePassage cuts a few words out of the middle of a text, because the interesting splice is
// not the one at the start of a provision: prefixes match too easily to prove anything about
// offset arithmetic, and Danish provisions run to hundreds of runes.
func middlePassage(text string) string {
	words := strings.Fields(text)
	if len(words) < 6 {
		return ""
	}
	at := len(words) / 2
	end := at + 4
	if end > len(words) {
		end = len(words)
	}
	passage := strings.Join(words[at:end], " ")
	if len([]rune(passage)) < 12 {
		return ""
	}
	return passage
}
