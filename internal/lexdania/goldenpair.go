package lexdania

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// A golden pair is a document pair the register itself produced: a newer executive order that
// repeals an older one and re-enacts most of its text. The ministry copies the old law, changes
// part of it, and promulgates the result, so the newer document is an external reference for
// what the older one's chain had to reconstruct.
//
// This matters because the parity tests cannot supply it. They compare the tree path with the
// model path, both of them ours, and two implementations sharing one wrong assumption agree
// perfectly: both once numbered stykker by position, so both printed numbering that cannot
// exist while TestModelConsolidationParityOnCorpus stayed green.

// GoldenPairVerdict is the result of checking one successor document against its base.
type GoldenPairVerdict struct {
	Base       string
	Successor  string
	AsOf       time.Time // the day before the successor takes effect for the base
	Applied    int       // changes the base had absorbed by then
	Provisions int       // provisions the successor prints

	// CopiedPromulgated counts successor provisions whose text equals the base's as published.
	// They prove the pair is a copy and nothing about the chain.
	CopiedPromulgated int

	// CopiedConsolidated counts successor provisions whose text equals what we reconstructed.
	// These are the load-bearing ones: the ministry copied them from the amended state, so the
	// state we rebuilt has to contain them character for character.
	CopiedConsolidated int

	// DigitsOnly counts successor provisions that match our reconstruction everywhere except in
	// their numbers, which is what stale numbering looks like from outside: the ministry's own
	// later order cites the renumbered provisions and we still cite the old ones.
	DigitsOnly int

	// Changed counts successor provisions matching neither, which is usually a real change in
	// the newer order and sometimes a provision the ministry copied from an amended state we
	// failed to reproduce. The two cannot be told apart from text alone, so the count is
	// reported as a bound rather than as a verdict.
	Changed int

	// Suspicious carries the DigitsOnly cases with the numbers that disagree, because "their
	// [5 7 8] against our [5 6 7]" is a numbering question while a moved year is the ministry
	// moving a date, and only the first one is ours to fix.
	Suspicious []GoldenPairDiff
}

// GoldenPairDiff names one provision where the ministry's text and ours agree except in digits.
type GoldenPairDiff struct {
	Successor string
	Ours      string
	TheirRuns []string
	OurRuns   []string
}

// Copies is how many successor provisions are copies of the base in one state or the other.
// It is the number worth bounding: when a chain corrupts a provision, a provision the ministry
// copied verbatim stops matching either state and drops into Changed.
func (v *GoldenPairVerdict) Copies() int { return v.CopiedPromulgated + v.CopiedConsolidated }

// Misses is successor provisions we cannot account for as copies: the bound a regression test
// watches, since every one of them is either a real change or a defect of ours.
func (v *GoldenPairVerdict) Misses() int { return v.DigitsOnly + v.Changed }

// GoldenPair reconstructs the base as it stood the day before successor takes effect and
// classifies every provision successor prints against it. The effective date comes from the
// base's own <Change> anchor for successor, so callers name two documents and nothing else.
//
// It returns false when successor is not in the base's chain, when either document is missing
// from the archive, or when the reconstruction failed; an unchecked pair must not read as a
// clean verdict.
func GoldenPair(a *Archive, base, successor string) (*GoldenPairVerdict, bool) {
	vs, err := a.Versions(base)
	if err != nil {
		return nil, false
	}
	var asOf time.Time
	for _, v := range vs {
		if v.Accession == successor && v.Found && !v.Effective.IsZero() {
			asOf = v.Effective.Add(-24 * time.Hour)
			break
		}
	}
	if asOf.IsZero() {
		return nil, false
	}

	baseDoc, _, ok := a.Open(base)
	if !ok {
		return nil, false
	}
	promulgated := map[string]bool{}
	for _, t := range ProvisionTexts(&baseDoc.Model().Content) {
		promulgated[t] = true
	}

	view := LawAsOfModel(baseDoc, asOf, a.Open)
	if view == nil || view.Content == nil {
		return nil, false
	}
	consolidated := map[string]bool{}
	nodigits := map[string]string{}
	for _, t := range ProvisionTexts(view.Content) {
		consolidated[t] = true
		nodigits[stripDigits(t)] = t
	}

	nextDoc, _, ok := a.Open(successor)
	if !ok {
		return nil, false
	}
	theirs := ProvisionTexts(&nextDoc.Model().Content)

	out := &GoldenPairVerdict{Base: base, Successor: successor, AsOf: asOf, Provisions: len(theirs)}
	for _, v := range view.Chain {
		if v.Found && !v.Effective.After(asOf) {
			out.Applied++
		}
	}
	for _, t := range theirs {
		switch {
		case promulgated[t]:
			out.CopiedPromulgated++
		case consolidated[t]:
			out.CopiedConsolidated++
		case nodigits[stripDigits(t)] != "":
			ours := nodigits[stripDigits(t)]
			out.DigitsOnly++
			if len(out.Suspicious) < 6 {
				out.Suspicious = append(out.Suspicious, GoldenPairDiff{
					Successor: t, Ours: ours, TheirRuns: digitRuns(t), OurRuns: digitRuns(ours),
				})
			}
		default:
			out.Changed++
		}
	}
	return out, true
}

// ProvisionTexts returns every provision's full text in reading order, normalised for
// comparison between two publisher documents: whitespace folded, soft hyphens and non-breaking
// spaces gone, the printed number left in place because a copied provision keeps its number and
// a renumbered provision is exactly what we want to notice. Provisions shorter than 40
// characters are skipped: they are cross-reference stubs and transition clauses, and matching
// one of those says nothing.
func ProvisionTexts(c *lexdoc.Content) []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, ref := range c.Provisions() {
		if ref.Provision == nil {
			continue
		}
		t := normProvisionText(ref.Provision.Text())
		if len(t) < 40 {
			continue
		}
		out = append(out, t)
	}
	return out
}

func normProvisionText(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.ReplaceAll(s, "\u00ad", "")
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// stripDigits removes the digits so "Stk. 3." and "Stk. 2." compare equal, which is what makes
// the numbering defect its own bucket: every word the ministry copied is present, only the
// numbers are impossible.
func stripDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// digitRuns lists the numbers in a provision in order, so two texts that agree everywhere except
// in their numbering can be read side by side.
func digitRuns(s string) []string {
	var out []string
	start := -1
	for i := 0; i <= len(s); i++ {
		digit := i < len(s) && s[i] >= '0' && s[i] <= '9'
		if digit && start < 0 {
			start = i
		} else if !digit && start >= 0 {
			out = append(out, s[start:i])
			start = -1
		}
	}
	return out
}

// String renders the verdict on one line, for tool output and test logs.
func (v *GoldenPairVerdict) String() string {
	return fmt.Sprintf("%s -> %s as of %s, %d changes applied, %d provisions: promulgated %d, consolidated %d, digits-only %d, changed %d",
		v.Base, v.Successor, v.AsOf.Format(time.DateOnly), v.Applied, v.Provisions,
		v.CopiedPromulgated, v.CopiedConsolidated, v.DigitsOnly, v.Changed)
}
