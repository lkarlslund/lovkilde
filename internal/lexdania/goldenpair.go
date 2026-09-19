package lexdania

import (
	"fmt"
	"regexp"
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
	// their numbers. The count is split below, because a differing digit has three causes and only
	// one of them is ours: a citation our chain left pointing at the old numbering, a citation the
	// successor itself rewrote because it renumbered the provision it points at, and a number that
	// is not a citation at all, such as a percentage, a date or a count.
	DigitsOnly int

	// StaleNumbering is the debt: the citation moved, and the provision it cites is identical in
	// the successor and in our reconstruction, so nothing in the successor's own text gives the
	// reference a reason to move.
	StaleNumbering int

	// SuccessorRenumbered is explained: the successor cites a provision whose text it changed in
	// the same document, so the numbering moved on the ministry's side of the pair.
	SuccessorRenumbered int

	// NumberMoved is outside the check: the differing digit sits in no citation.
	NumberMoved int

	// Changed counts successor provisions matching neither, which is usually a real change in
	// the newer order and sometimes a provision the ministry copied from an amended state we
	// failed to reproduce. The two cannot be told apart from text alone, so the count is
	// reported as a bound rather than as a verdict.
	Changed int

	// Suspicious carries the DigitsOnly cases with the numbers that disagree and the explanation
	// the classifier reached, so a reader can check the reasoning against the two documents.
	Suspicious []GoldenPairDiff
}

// GoldenPairDiff names one provision where the ministry's text and ours agree except in digits.
type GoldenPairDiff struct {
	Successor string
	Ours      string
	TheirRuns []string
	OurRuns   []string
	Kind      string // stale, successor, or number
	Target    string // the provision the moved citation points at, when the citation names one
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
	ourTexts := map[string]string{}
	for _, t := range ProvisionTexts(view.Content) {
		consolidated[t] = true
		nodigits[stripDigits(t)] = t
		ourTexts[provisionHeading(t)] = t
	}

	nextDoc, _, ok := a.Open(successor)
	if !ok {
		return nil, false
	}
	theirs := ProvisionTexts(&nextDoc.Model().Content)
	theirTexts := map[string]string{}
	for _, t := range theirs {
		theirTexts[provisionHeading(t)] = t
	}

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
			theirRuns, ourRuns := locatedDigitRuns(t), locatedDigitRuns(ours)
			kind, target := explainDigits(t, ours, theirRuns, ourRuns, theirTexts, ourTexts)
			out.DigitsOnly++
			switch kind {
			case "successor":
				out.SuccessorRenumbered++
			case "number", "external":
				out.NumberMoved++
			default: // stale, or a citation whose target is missing: ours until proven otherwise
				out.StaleNumbering++
			}
			if len(out.Suspicious) < 12 {
				out.Suspicious = append(out.Suspicious, GoldenPairDiff{
					Successor: t, Ours: ours,
					TheirRuns: runValues(theirRuns), OurRuns: runValues(ourRuns),
					Kind: kind, Target: target,
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

// citationWindow is wide enough to see "stk." or a "§ N" around a differing digit, and narrow
// enough that a window containing neither is really a number rather than a citation.
const (
	citationWindow = 32
	lawWindow      = 120 // far enough to reach "i lov om ..." after the address, or "lovens" before it
)

var (
	otherLevelRe = regexp.MustCompile(`(?i)\b(stk|nr|pkt|litra)\.`)
	// A citation names another document when an act, order or directive qualifies it, whether the
	// name comes before ("lægemiddellovens § 22") or after ("§ 3 ... 2. led i lov om taxikørsel").
	otherLawRe = regexp.MustCompile(`(?i)\b(lov|lovs|lovens|love|bekendtgørelse|bekendtgørelsen|bekendtgørelseslov|forordning|forordningen|direktiv|cirkulære|cirkulæret|cirkulæreskrivelse)\w*`)
)

// digitRun is a run of digits and where it sits, because the position is what lets the classifier
// look at the words around it.
type digitRun struct {
	at  string
	pos int
}

func runValues(runs []digitRun) []string {
	out := make([]string, len(runs))
	for i, r := range runs {
		out[i] = r.at
	}
	return out
}

// locatedDigitRuns returns every run of digits in the text with its byte offset, in order.
func locatedDigitRuns(s string) []digitRun {
	var out []digitRun
	start := -1
	for i := 0; i <= len(s); i++ {
		isDigit := i < len(s) && s[i] >= '0' && s[i] <= '9'
		if isDigit && start < 0 {
			start = i
		} else if !isDigit && start >= 0 {
			out = append(out, digitRun{at: s[start:i], pos: start})
			start = -1
		}
	}
	return out
}

// provisionHeading returns the address a provision text is filed under, or "" when it does not
// start with one. ProvisionTexts always puts the heading first, so this is how the two texts find
// each other's provisions; "" keys nothing, so an unaddressed piece of text cannot be mistaken for
// a citation target.
func provisionHeading(text string) string {
	h, _ := readAddress(text, 0)
	return h
}

// nearestCitedHeading returns the provision address nearest to the digit run at s[at:at+n], or ""
// when no address lies within citationWindow of it.
func nearestCitedHeading(s string, at, n int) string {
	// The test has to be a prefix test, not s[i] == '§': both signs below start with the byte 0xC2
	// in UTF-8, so comparing one byte against the rune compares it against 0xA7, the sign's second
	// byte, and finds nothing wherever a citation actually is.
	best, bestGap := "", citationWindow+1
	for i := range s {
		if !strings.HasPrefix(s[i:], "§") && !strings.HasPrefix(s[i:], "¶") {
			continue
		}
		h, end := readAddress(s, i)
		if h == "" {
			continue
		}
		// The distance to the differing digits, not the position in the text: "§ 1 og § 2, stk. 5,
		// 6 og 7" cites two provisions, and only the second one governs the stykke numbers.
		gap := 0
		switch {
		case end < at:
			gap = at - end
		case i > at+n:
			gap = i - (at + n)
		}
		if gap < bestGap {
			best, bestGap = h, gap
		}
	}
	return best
}

// readAddress parses a provision address at s[at:], such as "§ 2", "§ 298 a", "Bilag 3", and
// reports the normalised address and where it ended.
//
// The trailing letter belongs to the address only when the next character is not a letter: "§ 1 a"
// is a provision of its own while "§ 1 og" cites § 1 and carries on with a word. A regular
// expression cannot say that, because Go's regexp has no lookahead, and matching the letter
// wrongly is what once made "§ 1 og" read as an address no document has.
func readAddress(s string, at int) (string, int) {
	i := at
	for i < len(s) && s[i] == ' ' {
		i++
	}
	word := "\u00a7 "
	switch {
	case strings.HasPrefix(s[i:], "\u00a7\u00a7"):
		// §§ cites several provisions at once; reading the first is enough for this check.
		i += len("\u00a7\u00a7")
	case strings.HasPrefix(s[i:], "\u00a7"):
		i += len("\u00a7")
	case strings.HasPrefix(s[i:], "\u00b6"):
		i += len("\u00b6")
	case strings.HasPrefix(s[i:], "Bilag"):
		word, i = "Bilag ", i+len("Bilag")
	default:
		return "", at
	}
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	digits := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == digits {
		return "", at
	}
	num := s[digits:i]
	letter := ""
	if i+1 < len(s) && s[i] == ' ' && isASCIILower(s[i+1]) && (i+2 == len(s) || !isASCIILower(s[i+2])) {
		letter, i = " "+string(s[i+1]), i+2
	}
	return word + num + letter, i
}

func isASCIILower(b byte) bool { return b >= 'a' && b <= 'z' }

// explainDigits says who moved. A provision differing from ours only in its digits has three
// causes, and the text around the differing digit tells them apart:
//
//	number    the digit sits in no citation, so the successor changed a percentage, a date or a
//	          count. Outside this check, since the ministry was free to change it.
//	successor the citation names a provision the successor itself rewrote in the same document,
//	          so the numbering moved on the ministry's side and nothing of ours is missing.
//	stale     the citation names a provision identical in the successor and in our
//	          reconstruction, so nothing gives the reference a reason to move and ours is the one
//	          still pointing at the old numbering. That is the debt this check exists to find.
//
// B19950050705 -> B19950082405 is the second kind and was first read as the third: the
// successor's § 6 cites "§ 2, stk. 5, 7 og 8" while our § 6 cites "stk. 5, 6 og 7", which looks
// exactly like our chain having missed a renumbering. It had not: the successor's own § 2 prints
// eight stykker where the base prints seven, because the successor inserted a new stk. 6 about
// multipakninger, and it rewrote § 6's reference to match its own insertion.
func explainDigits(theirs, ours string, theirRuns, ourRuns []digitRun, theirTexts, ourTexts map[string]string) (kind, target string) {
	pos, n := -1, 0
	for i := range theirRuns {
		if i >= len(ourRuns) || theirRuns[i].at != ourRuns[i].at {
			pos, n = theirRuns[i].pos, len(theirRuns[i].at)
			break
		}
	}
	if pos < 0 {
		return "number", "" // equal digits in unequal counts: the runs themselves moved
	}
	window := theirs[max(0, pos-citationWindow):min(len(theirs), pos+n+citationWindow)]
	cited := nearestCitedHeading(theirs, pos, n)
	if !otherLevelRe.MatchString(window) && cited == "" {
		return "number", ""
	}
	if wide := theirs[max(0, pos-lawWindow):min(len(theirs), pos+n+lawWindow)]; otherLawRe.MatchString(wide) {
		return "external", "" // the numbering that moved belongs to a different document
	}
	// A citation naming another provision is checked against that provision; one that only
	// names a level ("stk. 6") refers inside the provision itself.
	target = provisionHeading(theirs)
	if cited != "" && cited != target {
		target = cited
	} else if cited == "" {
		// The reference is to this provision's own subsections, and this provision is the one
		// being compared, so the pair cannot say who renumbered them: the successor may have
		// inserted a subsection here, or our chain may have failed to. Counted as debt.
		return "self", target
	}
	theirTarget, theirOK := theirTexts[target]
	ourTarget, ourOK := ourTexts[target]
	if !theirOK || !ourOK {
		return "reference", target // the cited provision is missing from one of the two texts
	}
	if theirTarget != ourTarget {
		return "successor", target
	}
	return "stale", target
}

// String renders the verdict on one line, for tool output and test logs.
func (v *GoldenPairVerdict) String() string {
	return fmt.Sprintf("%s -> %s as of %s, %d changes applied, %d provisions: promulgated %d, consolidated %d, digits-only %d (stale %d, successor %d, other numbers %d), changed %d",
		v.Base, v.Successor, v.AsOf.Format(time.DateOnly), v.Applied, v.Provisions,
		v.CopiedPromulgated, v.CopiedConsolidated, v.DigitsOnly,
		v.StaleNumbering, v.SuccessorRenumbered, v.NumberMoved, v.Changed)
}
