package lexdania

import (
	"testing"
)

// TestGoldenPairAgainstPromulgatedSuccessors checks our consolidation against documents the
// ministry actually promulgated, which is the one kind of assertion the parity tests cannot
// make. Parity compares the tree path with the model path: both are ours, and two
// implementations sharing one wrong assumption agree perfectly. Both once numbered stykker by
// position, so both printed numbering that cannot exist in 21 BR18 provisions while
// TestModelConsolidationParityOnCorpus stayed green.
//
// A golden pair is external. A newer executive order repealed an older one and re-enacted most
// of its text, so the newer document states, in the publisher's own words, what the older one's
// chain had to have produced. Each successor provision is then either a copy of the base as
// promulgated (outside our control), a copy of the amended state (which is what we must have
// built), a copy that differs only in its digits (stale numbering), or unaccounted for.
//
// These pairs are pinned rather than rediscovered so the test costs seconds instead of the
// 3m38s a full census takes; tools/goldenpair still runs the census over the whole archive. The
// pair list came from that census, and one filter matters: the base must have had a change of
// its own before the successor arrived. Without it the highest-overlap pairs are same-year
// corrections of unamended orders, which compare one document with itself and assert nothing.
//
// The bounds are measured, not guessed. There used to be three digits-only cases, recorded as
// debt: in B19950050705 -> B19950082405 the ministry's own later order cites "§ 2, stk. 5, 7 og
// 8" where our consolidation of the base cited "stk. 5, 6 og 7". They were the base's chain read
// with its stykker misnumbered, and they became copies of our amended state once each act's
// citations were read against the numbering before that act and the act's prose changes were read
// at all. A new digits-only case is a regression found by someone else's text, which is the point.
func TestGoldenPairAgainstPromulgatedSuccessors(t *testing.T) {
	a, err := corpusArchive(t)
	if err != nil {
		t.Fatal(err)
	}

	pairs := []struct {
		base, next, why string
	}{
		{"B19960106705", "B19970063305", "93% re-enacted, the best pair in the archive"},
		{"B19910088105", "B19920113905", "92% re-enacted"},
		{"B19970101205", "B19980024805", "91% re-enacted, one cross-reference the chain renumbers"},
		{"B19910011605", "B19930055505", "88% re-enacted"},
		{"C19910019309", "C19950007609", "88% re-enacted, a circular rather than an order"},
		{"B19950050705", "B19950082405", "87% re-enacted, two cross-references renumbered by a stykke insertion"},
		{"B19920051705", "B19960014405", "86% re-enacted over 63 provisions, the largest pair"},
		{"B19890062605", "B19920056305", "85% re-enacted after two earlier changes"},
	}

	// Measured over these eight pairs: 271 successor provisions, 241 accounted for as copies (3 of
	// them copies of the amended state), 0 differing only in their digits, 30 matching neither. copies is the number a chain must not lose: every provision a successor
	// copied stops counting as a copy the moment our reconstruction corrupts it.
	const (
		copiesBound     = 241
		digitsOnlyBound = 0
		changedBound    = 30
		minProvisions   = 10
	)

	var copies, digits, changed, provisions int
	for _, p := range pairs {
		v, ok := GoldenPair(a, p.base, p.next)
		if !ok {
			t.Fatalf("%s -> %s is not checkable: it must be named in %s's own <Change> anchors, be present in the archive, and consolidate", p.base, p.next, p.base)
		}
		t.Logf("%s (%s)", v, p.why)
		if v.Provisions < minProvisions {
			t.Errorf("%s -> %s prints only %d provisions, so this pair checks almost nothing; the successor's text or the parser changed",
				p.base, p.next, v.Provisions)
		}
		copies += v.CopiedPromulgated + v.CopiedConsolidated
		digits += v.DigitsOnly
		changed += v.Changed
		provisions += v.Provisions
		for _, s := range v.Suspicious {
			t.Logf("     theirs %v\n       %s\n     ours   %v\n       %s",
				s.TheirRuns, cutRunes(s.Successor, 140), s.OurRuns, cutRunes(s.Ours, 140))
		}
	}

	if provisions == 0 {
		t.Fatal("no successor provisions were compared at all")
	}
	switch {
	case copies < copiesBound:
		t.Errorf("only %d of %d successor provisions are accounted for as copies; the recorded floor was %d, so text the ministry copied verbatim no longer matches what we rebuilt",
			copies, provisions, copiesBound)
	case copies > copiesBound:
		t.Logf("copies measured %d against the recorded floor %d: raise the floor to lock the gain in", copies, copiesBound)
	}
	switch {
	case digits > digitsOnlyBound:
		t.Errorf("%d successor provisions match our reconstruction except in their digits; the recorded debt was %d -- stale numbering seen from a published document", digits, digitsOnlyBound)
	case digits < digitsOnlyBound:
		t.Logf("digits-only measured %d against its recorded %d: lower the bound to lock the fix in", digits, digitsOnlyBound)
	}
	switch {
	case changed > changedBound:
		t.Errorf("%d successor provisions match neither state, against a recorded %d: either the ministry changed more than these pairs ever did, or we lost text it copied", changed, changedBound)
	case changed < changedBound:
		t.Logf("unaccounted measured %d against its recorded %d: lower the bound to lock the gain in", changed, changedBound)
	}
}
