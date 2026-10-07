package lexdoc

import (
	"regexp"
	"strings"
	"unicode"
)

// Replacing text where it lives.
//
// Consolidation is why the model stores text as segments a stykke points into rather than as
// strings each level owns. When "§ 5, stk. 2" is amended, the provision's text changes and
// every enclosing text has to show that change without being recomposed: an edit that rewrote
// the subsection and then rebuilt the parent would leave the two halves of a provision able to
// disagree with each other, which for consolidated law is not a display defect but a wrong
// legal text.
//
// The match is located in normalised space and applied through the index map MatchRunes
// returns, because an amendment's quotation differs from its target in spaces, quote style and
// soft hyphens while the answer must be printed in the target's own typography. Replacement
// therefore puts the new text into the first segment the match touches and deletes only the
// parts of later segments that the match actually covers: rewriting the whole provision would
// flatten quotation typography and any footnote marker in the untouched remainder.
//
// Markup inside the replaced span does not survive: the replacement lands in the first segment
// touched and empties the rest of the span, because an amendment's new text is plain prose with
// no markup to put back. Everything outside the span keeps its typography.
//
// Segments keep their Break and Note flags even when an edit empties them. Dropping a segment
// would move a paragraph break or orphan a footnote, and a segment with no text renders as
// nothing anyway.
//
// The search is bounded by the span the edit addressed. The tree reader had this property by
// accident — it could only ever see the one node it was editing — and the model has to keep it
// deliberately, because a flat provision slice would otherwise let an edit meant for stk. 2
// match prose in stk. 5 and report success.

// Replace replaces the first occurrence of old anywhere in the provision's text, and reports
// whether the passage was there. old arrives as the amendment wrote it, quotation marks
// included; new is inserted as the amendment wrote it.
func (p *Provision) Replace(old, new string) bool {
	return p.ReplaceSpan(Span{From: 0, To: len(p.Segments)}, old, new)
}

// ReplaceSpan replaces the first occurrence of old within the text the span covers. The edit
// is applied to the provision's own segment slice, so the enclosing provision text and every
// other span over the same characters change with it.
func (p *Provision) ReplaceSpan(s Span, old, new string) bool {
	_, _, n := replaceIn(p.Segments, s, old, new, false)
	return n > 0
}

// ReplaceEverywhereInSpan replaces every occurrence of old within the span and returns how many
// it wrote. An "Overalt i loven ændres »X« til: »Y«" instruction carries no address, and
// replacing one occurrence at a time in a loop would spin forever whenever the new text contains
// the old — "§ 5" becoming "§ 5, stk. 2" is exactly that shape, and it is common in legal prose.
func (p *Provision) ReplaceEverywhereInSpan(s Span, old, new string) int {
	_, _, n := replaceIn(p.Segments, s, old, new, true)
	return n
}

// ReplaceSegments applies a replacement to a stand-alone piece of the document's text — the
// preamble, the title, an annex's prose — which are addressed by the model the same way a
// provision is and are the same thing here: an ordered slice of segments. It returns how many
// occurrences it wrote.
func ReplaceSegments(segs []Segment, old, new string, all bool) int {
	if len(segs) == 0 {
		return 0
	}
	_, _, n := replaceIn(segs, Span{From: 0, To: len(segs)}, old, new, all)
	return n
}

// replaceIn is the engine, and there is exactly one of it.
//
// It reports whether the passage was found, whether any segment ended up different, and how
// many occurrences were written. found and changed are separate answers on purpose: a passage
// that straddles segments moves into the first one touched and empties the others, so restating
// identical words still drops the markup inside the span. Consolidation has to be able to tell an
// amendment that restates existing law from one that silently achieved nothing.
func replaceIn(segs []Segment, s Span, old, new string, all bool) (found, changed bool, n int) {
	which := pickFirst
	if all {
		which = pickAll
	}
	return replaceSelected(segs, s, old, new, which)
}

// Which occurrences replaceSelected rewrites: all, the first (the last for a lone punctuation
// mark), or the n-th counting from 0.
const (
	pickAll   = -1
	pickFirst = -2
)

// lowerFollows reports text going on after position at with a word in lower case: the sentence
// did not end there.
func lowerFollows(text []rune, at int) bool {
	k := at
	for k < len(text) && (text[k] == ' ' || text[k] == '\u00a0') {
		k++
	}
	return k > at && k < len(text) && unicode.IsLower(text[k])
}

// replaceSelected is replaceIn with a choice of occurrence: a sentence-scoped change rewrites the
// occurrence inside its sentence, in place, so the segments, items and markup around it survive.
func replaceSelected(segs []Segment, s Span, old, new string, which int) (found, changed bool, n int) {
	return replaceCore(segs, s, old, new, which, false)
}

// replaceLoose is the last resort for a passage the text prints with different spacing: "§§ 45,
// 46og 49" in the promulgated text against »§§ 45, 46 og 49« in the act. Spaces are not counted
// on either side, and the passage has to occur exactly once in the span, so the words decide the
// place and nothing else can.
func replaceLoose(segs []Segment, s Span, old, new string) (found, changed bool) {
	f, ch, n := replaceCore(segs, s, old, new, pickFirst, true)
	return f && n > 0, ch
}

func replaceCore(segs []Segment, s Span, old, new string, which int, loose bool) (found, changed bool, n int) {
	all := which == pickAll
	from, to := s.From, s.To
	if from < 0 {
		from = 0
	}
	if from >= len(segs) {
		return false, false, 0
	}
	if to > len(segs) || to < from {
		to = len(segs)
	}
	if to <= from {
		return false, false, 0
	}

	// Concatenate the span's rendered text once, with the separators SegmentsText puts between
	// segments, and record which segment each rune belongs to. The separators have no owner,
	// and they must be present: an amendment quotes the law as it prints, and Provision.Match
	// is computed from the same rendered text, so a passage quoted across a paragraph break
	// has to be findable here or it is not findable at all.
	texts := make([][]rune, to-from)
	start := make([]int, to-from)
	var text []rune
	var owner []int
	for i := from; i < to; i++ {
		texts[i-from] = []rune(segs[i].Text)
		start[i-from] = len(text)
		text = append(text, texts[i-from]...)
		for range texts[i-from] {
			owner = append(owner, i-from)
		}
		if segs[i].Break && i+1 < to {
			text = append(text, '\n')
			owner = append(owner, -1)
		}
	}

	// A footnote set inside the span is not the span's text: "I § 8, stk. 5, indsættes efter
	// »stk. 1-3«" means the subsection's words, not a note that happens to quote them, and a passage
	// the law prints around a note ("37 a [2) § 37 a … er ikke sat i kraft.] og 38-42") is one
	// passage. The note is left out of what is matched and stays where it is. A span that starts in
	// a note is the note's own, and is edited like any text.
	outerNote := segs[from].Note != ""
	noted := func(k int) bool { return !outerNote && owner[k] >= 0 && segs[from+owner[k]].Note != "" }
	var visible []rune
	var visAt []int
	for k, r := range text {
		if !noted(k) {
			visible = append(visible, r)
			visAt = append(visAt, k)
		}
	}
	norm, vidx := MatchRunes(string(visible))
	idx := make([]int, len(vidx))
	for k, v := range vidx {
		idx[k] = visAt[v]
	}
	needle, _ := MatchRunes(QuoteStrip(old))
	if loose {
		norm, idx = withoutSpaces(norm, idx)
		needle, _ = withoutSpaces(needle, nil)
	}
	if len(needle) == 0 {
		// An empty passage is present everywhere, which makes "replace it" meaningless. The
		// old reader rejected the same edit rather than rewriting the provision.
		return false, false, 0
	}

	// Locate every occurrence in the original text before anything is rewritten, so the
	// positions of the ones still to come stay valid while the earlier ones are replaced.
	//
	// When the instruction quotes a single word — "Overalt i loven ændres »skib« til:
	// »fartøj«" — it means the word, not the letters inside every longer word, so an occurrence
	// with a letter or digit against either side is skipped. Matching is case-sensitive, so this
	// is not about "Skibet" at a sentence start; it is about "… at skibet skal …", which without
	// the filter becomes "… at fartøjet skal …", a change no Danish amending act asks for and
	// which no later check would notice.
	short := wholeWordQuote(old)
	place := make([]bool, len(text))
	skip := make([]bool, len(text))
	newRunes := []rune(new)
	withRunes := map[int][]rune{} // the new words for one occurrence, spaced to fit where it sits
	var matches [][2]int
	for at := 0; at < len(norm); {
		pos := RuneIndex(norm[at:], needle)
		if pos < 0 || len(idx) <= at+pos+len(needle)-1 {
			break
		}
		pos += at
		end := pos + len(needle)
		end += decimalZeros(norm, needle, end)
		if short && gluedToWord(norm, pos, len(needle)) || splitsNumber(norm, needle, pos, end) {
			at = pos + 1
			continue
		}
		matches = append(matches, [2]int{pos, end})
		at = end
	}
	// Which occurrences the change means. A quoted mark and nothing else — "ændres ».« til: »,
	// eller«" — is the mark that ends the unit, which is why an item gets "eller" before the next
	// one is added: the first full stop in "beskæftigelse m.v., jf. …" is inside an abbreviation, and
	// replacing it gave "m, ellerv.".
	switch {
	case loose && len(matches) != 1:
		matches = nil // spacing is not counted, so only a passage that occurs once is placed
	case all:
	case which >= 0 && which < len(matches):
		matches = matches[which : which+1]
	case which >= 0:
		matches = nil
	case len(matches) > 0 && punctuationOnly(old):
		matches = matches[len(matches)-1:]
	case len(matches) > 0:
		matches = matches[:1]
	}
	for _, m := range matches {
		pos, end := m[0], m[1]
		// Mark the runes this occurrence covers. The range is taken through the index map, as
		// the contiguous stretch of the *original* text the match reaches: that is what swallows
		// the soft hyphens and non-breaking spaces normalisation dropped inside the quoted
		// passage, instead of leaving them stranded in the middle of the new wording.
		//
		// The replacement goes at the first covered rune that belongs to a segment — the new
		// text inherits that segment's typography rather than being distributed over segments it
		// has nothing to do with — and a separator, which owns no text, is simply gone once the
		// text around it has been replaced.
		lo, hi := idx[pos], idx[end-1]+1
		// »… indkvarteringen« replaced by »… indkvarteringen.«: the stop the text prints after
		// the passage is the one the new words end with, not a second one.
		if n := len(newRunes); n > 0 && (newRunes[n-1] == '.' || newRunes[n-1] == ',') && hi < len(text) &&
			text[hi] == newRunes[n-1] && text[hi-1] != newRunes[n-1] && owner[hi] >= 0 {
			hi++
		}
		if len(newRunes) == 0 && lo > 0 && hi < len(text) && text[lo-1] == ' ' && owner[hi] >= 0 {
			// "om gensidig anerkendelse" less »gensidig« is "om anerkendelse", and "a X, b" less
			// »X« is "a, b": a deleted word takes a space with it.
			switch {
			case text[hi] == ' ':
				hi++
			case strings.ContainsRune(",.;:)", text[hi]) && owner[lo-1] >= 0:
				lo--
			}
		}
		with := newRunes
		// »… eller § 8, stk. 8.« put where the text goes on ", optjenes …", or »efter stk. 5 og 6.«
		// before " henføres": the act's own full stop slipped inside its quotation, and a citation
		// does not end a sentence mid-way.
		if n := len(with); n > 1 && with[n-1] == '.' && unicode.IsDigit(with[n-2]) && hi < len(text) && (text[hi] == ',' || text[hi] == ';' || lowerFollows(text, hi)) {
			with = with[:n-1]
		}
		first := true
		for k := lo; k < hi; k++ {
			if owner[k] < 0 || noted(k) {
				continue // a note inside the passage stays
			}
			if first {
				place[k] = true
				withRunes[k] = spacedFor(text, lo, hi, with)
				first = false
			}
			skip[k] = true
		}
		n++
	}
	if n == 0 {
		return false, false, 0
	}

	for i := range texts {
		out := make([]rune, 0, len(texts[i])+len(newRunes))
		for at := start[i]; at < start[i]+len(texts[i]); at++ {
			if place[at] {
				out = append(out, withRunes[at]...)
			}
			if !skip[at] {
				out = append(out, text[at])
			}
		}
		if string(out) != string(texts[i]) {
			changed = true
		}
		segs[from+i].Text = string(out)
	}
	return true, changed, n
}

// wholeWordQuote reports whether an amendment's quotation is a single bare word, which is the
// one case where "replace this" has to mean the word rather than the letters inside longer words.
func wholeWordQuote(old string) bool {
	return old == Squeeze(old) && !strings.ContainsAny(old, " »") &&
		strings.IndexFunc(old, unicode.IsLetter) >= 0 &&
		strings.IndexFunc(old, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) < 0
}

// gluedToWord reports whether the occurrence at pos sits inside a longer word, which is how a
// single-word replace told to change »skib« avoids rewriting "skibet".
// splitsNumber reports an occurrence that cuts a number in two: »stk. 1« in "stk. 12", »14« in
// "114". The act quotes a whole number; replaced, part of a longer one left "stk. 22". A number
// that opens a range, »10« in "kapitel 10-12", is whole.
func splitsNumber(norm, needle []rune, pos, end int) bool {
	if len(needle) == 0 {
		return false
	}
	if unicode.IsDigit(needle[len(needle)-1]) && end < len(norm) && unicode.IsDigit(norm[end]) {
		// "stk. 31)": a footnote reference printed against the number ends it.
		j := end
		for j < len(norm) && unicode.IsDigit(norm[j]) {
			j++
		}
		return !(j < len(norm) && norm[j] == ')' && j-end <= 2)
	}
	return unicode.IsDigit(needle[0]) && pos > 0 && unicode.IsDigit(norm[pos-1])
}

// decimalZeros is how many zeros the text prints after a decimal the act quotes without them:
// »0,8« against "0,80". They are the same number, and the zeros go with it.
func decimalZeros(norm, needle []rune, end int) int {
	k := len(needle) - 1
	for k >= 0 && unicode.IsDigit(needle[k]) {
		k--
	}
	if k < 1 || k == len(needle)-1 || needle[k] != ',' || !unicode.IsDigit(needle[k-1]) {
		return 0
	}
	z := 0
	for end+z < len(norm) && norm[end+z] == '0' {
		z++
	}
	if end+z < len(norm) && unicode.IsDigit(norm[end+z]) {
		return 0
	}
	return z
}

func gluedToWord(norm []rune, pos, n int) bool {
	if pos > 0 && (unicode.IsLetter(norm[pos-1]) || unicode.IsDigit(norm[pos-1])) {
		return true
	}
	end := pos + n
	if end < len(norm) && (unicode.IsLetter(norm[end]) || unicode.IsDigit(norm[end])) {
		// "skiftelovens1)": a footnote reference printed against the word ends it.
		j := end
		for j < len(norm) && unicode.IsDigit(norm[j]) {
			j++
		}
		return !(j > end && j < len(norm) && norm[j] == ')')
	}
	return false
}

// Append adds text to the end of the provision, as a segment of its own.
//
// Insertions the source expresses in prose have no exact position to land on — the register
// says "indsættes efter stk. 3" and the new provision arrives without an anchor of its own —
// so this deliberately does not pretend otherwise. Callers record the approximation; the
// model only undertakes not to lose the text or the ordering.
func (p *Provision) Append(text string) bool {
	if text == "" {
		return false
	}
	p.Segments = append(p.Segments, Segment{Text: " " + text})
	return true
}

// punctuationOnly reports a quoted passage that is only marks: ".", ";", ",".
func punctuationOnly(s string) bool {
	s = strings.TrimSpace(QuoteStrip(s))
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsPunct(r) {
			return false
		}
	}
	return true
}

// countMatches counts the occurrences of a passage in a text the way replaceSelected finds them,
// single-word passages as whole words.
func countMatches(text, old string) int {
	norm, _ := MatchRunes(text)
	needle, _ := MatchRunes(QuoteStrip(old))
	if len(needle) == 0 {
		return 0
	}
	short := wholeWordQuote(old)
	n := 0
	for at := 0; at < len(norm); {
		pos := RuneIndex(norm[at:], needle)
		if pos < 0 {
			break
		}
		pos += at
		end := pos + len(needle)
		end += decimalZeros(norm, needle, end)
		if short && gluedToWord(norm, pos, len(needle)) || splitsNumber(norm, needle, pos, end) {
			at = pos + 1
			continue
		}
		n++
		at = end
	}
	return n
}

// withoutSpaces drops the spaces from a normalised text and its index map.
func withoutSpaces(norm []rune, idx []int) ([]rune, []int) {
	out := make([]rune, 0, len(norm))
	var outIdx []int
	for i, r := range norm {
		if r == ' ' {
			continue
		}
		out = append(out, r)
		if idx != nil {
			outIdx = append(outIdx, idx[i])
		}
	}
	return out, outIdx
}

// looseMatch finds the one place a passage occurs in a text when spaces are not counted, and
// returns it as the text prints it.
func looseMatch(text, passage string) (string, bool) {
	norm, idx := MatchRunes(text)
	norm, idx = withoutSpaces(norm, idx)
	needle, _ := MatchRunes(QuoteStrip(passage))
	needle, _ = withoutSpaces(needle, nil)
	if len(needle) == 0 {
		return "", false
	}
	pos := RuneIndex(norm, needle)
	if pos < 0 || RuneIndex(norm[pos+1:], needle) >= 0 {
		return "", false
	}
	src := []rune(text)
	return Squeeze(string(src[idx[pos] : idx[pos+len(needle)-1]+1])), true
}

// fuzzyPrinted finds the one place a passage occurs in a text when only its letters and digits are
// compared, case aside, and returns it as the text prints it. Acts quote citations with their own
// punctuation — »jf. stk 1 og 12« for "jf. stk. 1 og 12", »§ 1, stk. 1, nr. 1-2 a, og 3 a-5 b« for
// "… nr. 1-2 a og 3 a-5 b" — and the words are the same. The match has to stand on word boundaries
// on both sides, so "6stk4" is not found inside "§ 16, stk. 4", and the passage's own leading and
// trailing marks are taken along where the text prints them.
func fuzzyPrinted(text, passage string) (string, bool) {
	src := []rune(text)
	var norm []rune
	var idx []int
	for i, r := range src {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			norm = append(norm, unicode.ToLower(r))
			idx = append(idx, i)
		}
	}
	var needle []rune
	for _, r := range QuoteStrip(passage) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			needle = append(needle, unicode.ToLower(r))
		}
	}
	if len(needle) < 4 {
		return "", false
	}
	alnum := func(i int) bool {
		return i >= 0 && i < len(src) && (unicode.IsLetter(src[i]) || unicode.IsDigit(src[i]))
	}
	found := -1
	for at := 0; at < len(norm); {
		pos := RuneIndex(norm[at:], needle)
		if pos < 0 {
			break
		}
		pos += at
		from, to := idx[pos], idx[pos+len(needle)-1]+1
		// A boundary in the passage is a boundary in the text: "6stk4" inside "16, stk. 4" is
		// not the passage, and neither is a word it only begins.
		if !alnum(from-1) && !alnum(to) {
			if found >= 0 {
				return "", false
			}
			found = pos
		}
		at = pos + 1
	}
	if found < 0 {
		return "", false
	}
	from, to := idx[found], idx[found+len(needle)-1]+1
	from, to = withMarks(src, from, to, passage)
	return string(src[from:to]), true
}

// withMarks widens a match of a passage's words to the marks the passage quotes around them, where
// the text prints them: the "§§ " in front of »§§ 56-57 e …«, the "," after »… stk. 4,«.
func withMarks(src []rune, from, to int, passage string) (int, int) {
	q := []rune(strings.TrimSpace(QuoteStrip(passage)))
	lead, tail := 0, 0
	for lead < len(q) && !unicode.IsLetter(q[lead]) && !unicode.IsDigit(q[lead]) {
		lead++
	}
	for tail < len(q)-lead && !unicode.IsLetter(q[len(q)-1-tail]) && !unicode.IsDigit(q[len(q)-1-tail]) {
		tail++
	}
	if l := strings.TrimSpace(string(q[:lead])); l != "" {
		before := strings.TrimRight(string(src[:from]), " \u00a0")
		if strings.HasSuffix(before, l) {
			from = len([]rune(before)) - len([]rune(l))
		}
	}
	if t := strings.TrimSpace(string(q[len(q)-tail:])); t != "" && strings.HasPrefix(string(src[to:]), t) {
		to += len([]rune(t))
	}
	return from, to
}

// spacedFor keeps a replacement from gluing to the words around it. A quotation that starts with
// its separator — »,  5.30 Ældreboliger og …« — is replaced by words the act also quoted with a
// leading space, » og 5.30 Ældreboliger.«, and reading the quotation trims that space: written
// as read, "Jernbanedrift" and "og" ran together.
func spacedFor(text []rune, lo, hi int, new []rune) []rune {
	if len(new) == 0 {
		return new
	}
	word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	out := new
	if lo > 0 && word(text[lo-1]) && !word(text[lo]) && word(new[0]) {
		out = append([]rune{' '}, out...)
	}
	if hi < len(text) && word(text[hi]) && !word(text[hi-1]) && word(new[len(new)-1]) {
		out = append(append([]rune(nil), out...), ' ')
	}
	return out
}

// typoPrinted finds the one place in a text that prints a passage with a slip in its letters —
// "Epidemikommisionen" for »Epidemikommissionen«, "kropsscannere" for »kropscannere« — and returns
// it as printed. The slip may be in the law or in the act; either way the words name one place.
// Only letters may differ: a number that differs is a different citation or amount, and a passage
// shorter than ten letters, or one that two places come equally close to, is not placed.
func typoPrinted(text, passage string) (string, bool) {
	src := []rune(text)
	var norm []rune
	var idx []int
	for i, r := range src {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			norm = append(norm, unicode.ToLower(r))
			idx = append(idx, i)
		}
	}
	var needle []rune
	for _, r := range QuoteStrip(passage) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			needle = append(needle, unicode.ToLower(r))
		}
	}
	if w, ok := inflectedWord(src, passage); ok {
		return w, true
	}
	if w, ok := connectiveSwapped(text, passage); ok {
		return w, true
	}
	if w, ok := oneWordOther(src, passage); ok {
		return w, true
	}
	if w, ok := repeatedNumberDropped(src, passage); ok {
		return w, true
	}
	if len(needle) < 10 || len(norm) < len(needle)/2 {
		return "", false
	}
	limit := max(1, len(needle)/10)
	switch {
	case len(needle) >= 20:
		limit = max(3, limit) // room for a filler word more: "til den 1. september 2015"
	case len(needle) >= 15:
		limit = max(2, limit) // room for a short word more: "inden den 1. januar år 2000"
	}
	// A long citation has room for two words more, "digitalt via Jobnet selv"; a place that close is
	// taken only when no place within the usual limit is.
	strict := limit
	if len(needle) >= 50 {
		limit = max(limit, len(needle)/6)
	}
	// Semi-global alignment: the whole passage against any stretch of the text.
	prev := make([]int, len(needle)+1)
	cur := make([]int, len(needle)+1)
	pstart := make([]int, len(needle)+1)
	cstart := make([]int, len(needle)+1)
	for j := range prev {
		prev[j] = j
	}
	type hit struct{ d, from, to int }
	var hits []hit
	for i := 1; i <= len(norm); i++ {
		cur[0], cstart[0] = 0, i
		for j := 1; j <= len(needle); j++ {
			c := 1
			if norm[i-1] == needle[j-1] {
				c = 0
			}
			v, st := prev[j-1]+c, pstart[j-1]
			if j == 1 {
				st = i - 1
			}
			if prev[j]+1 < v {
				v, st = prev[j]+1, pstart[j]
			}
			if cur[j-1]+1 < v {
				v, st = cur[j-1]+1, cstart[j-1]
			}
			cur[j], cstart[j] = v, st
		}
		if d := cur[len(needle)]; d <= limit {
			hits = append(hits, hit{d, cstart[len(needle)], i})
		}
		prev, cur = cur, prev
		pstart, cstart = cstart, pstart
	}
	if len(hits) == 0 {
		return "", false
	}
	digits := func(rs []rune) string {
		var b strings.Builder
		for _, r := range rs {
			if unicode.IsDigit(r) {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	// The cheapest place keeps every number the passage cites: »nr. 5 og 6,« against the text's
	// "nr. 5 eller 6," aligned cheapest by leaving the 6 out.
	// A window starting or ending on a number the quotation does not start or end with took it from
	// beside the passage: "side 40), dele af …" aligned as cheaply from the 40 as from "dele".
	for i := range hits {
		h := &hits[i]
		for h.from < h.to && unicode.IsDigit(norm[h.from]) && !unicode.IsDigit(needle[0]) {
			h.from++
		}
		for h.to > h.from && unicode.IsDigit(norm[h.to-1]) && !unicode.IsDigit(needle[len(needle)-1]) {
			h.to--
		}
	}
	best, have := hits[0], false
	for _, h := range hits {
		if digits(norm[h.from:h.to]) == digits(needle) && (!have || h.d < best.d) {
			best, have = h, true
		}
	}
	if !have {
		return "", false
	}
	if best.d == 0 {
		return "", false // an exact match is the other readers' business
	}
	if best.d <= strict {
		kept := hits[:0]
		for _, h := range hits {
			if h.d <= strict {
				kept = append(kept, h)
			}
		}
		hits = kept
	}
	// One place: every hit within the limit overlaps the best one.
	for _, h := range hits {
		if h.to <= best.from || h.from >= best.to {
			return "", false
		}
	}

	from, to := idx[best.from], idx[best.to-1]+1
	word := func(i int) bool {
		return i >= 0 && i < len(src) && (unicode.IsLetter(src[i]) || unicode.IsDigit(src[i]))
	}
	for word(from - 1) {
		from--
	}
	for word(to) {
		to++
	}
	// A slip is inside words: the passage and the place print the same words, each spelt nearly
	// alike. A word more or less ("lov om beskyttelse af halvlederprodukters …" against »lov om
	// halvlederprodukters …«) is different wording, and is not placed.
	got, want := withoutFiller(wordsOf(string(src[from:to]))), withoutFiller(wordsOf(QuoteStrip(passage)))
	if len(want) >= 10 && wordEdits(got, want) <= 2 && adjacentEdits(got, want) {
		// Ten words or more, two of them added or dropped together: »af jobcenteret fået mulighed for
		// digitalt selv at booke jobsamtaler« for the text's "… digitalt via Jobnet selv …", as the
		// consolidation of 2021 reads it.
		from, to = withMarks(src, from, to, passage)
		return string(src[from:to]), true
	}
	if len(want) >= 8 && wordEdits(got, want) <= 1 {
		// A citation of eight words or more names its place by itself: "bekendtgørelse om
		// offentliggørelse af redegørelse om samfundsansvar …" against the text's "redegørelse for
		// samfundsansvar". One word more, less or other is the drafter's, not another place.
		from, to = withMarks(src, from, to, passage)
		return string(src[from:to]), true
	}
	if len(got) != len(want) {
		// One short word more or less — "i de i stk. 1 nævnte" against »i de stk. 1 nævnte« — is a
		// slip too; anything longer is wording.
		short := func(ws []string) []string {
			var out []string
			for _, w := range ws {
				if len([]rune(w)) > 2 || unicode.IsDigit([]rune(w)[0]) {
					out = append(out, w)
				}
			}
			return out
		}
		if d := len(got) - len(want); d > 1 || d < -1 {
			return "", false
		}
		got, want = short(got), short(want)
		if len(got) != len(want) {
			return "", false
		}
	}
	other := 0 // words further apart than a slip, which one may be in a passage of four or more
	for i := range got {
		if d := editDistance(got[i], want[i]); d > max(1, len([]rune(want[i]))/4) && !(d <= 3 && commonPrefix(got[i], want[i]) >= 5) {
			// A word may end differently on a stem they share, "medvirken" and "medvirkning";
			// anything more is another word — but for one word in a passage of four words or
			// more, every other word as quoted: »genanvendt eller særskilt håndteret« for the
			// text's "særligt", as the consolidation of 2010 reads it.
			if d > 3 || commonPrefix(got[i], want[i]) < 3 || len(want) < 4 || unicode.IsDigit([]rune(want[i])[0]) {
				return "", false
			}
			other++
		}
	}
	if other > 1 {
		return "", false
	}
	if other == 1 {
		for i := range got {
			if got[i] != want[i] && editDistance(got[i], want[i]) <= max(1, len([]rune(want[i]))/4) {
				return "", false // the other words exactly as quoted
			}
		}
	}
	from, to = withMarks(src, from, to, passage)
	return string(src[from:to]), true
}

// wordsOf is a text's words, lower-cased: its runs of letters and digits.
// withoutFiller drops the words a date or a citation is printed with or without, "den 3. august
// 2015 til den 1. september 2015" against »3. august 2015 til 1. september 2015«, "1. januar år
// 2000".
func withoutFiller(ws []string) []string {
	out := ws[:0:0]
	for _, w := range ws {
		switch w {
		case "den", "det", "de", "år":
		default:
			out = append(out, w)
		}
	}
	return out
}

// wordEdits is how many words must be added, dropped or exchanged to turn one wording into the
// other, words spelt nearly alike counting as the same.
func wordEdits(a, b []string) int {
	same := func(x, y string) bool {
		d := editDistance(x, y)
		return d <= max(1, len([]rune(y))/4) && (d == 0 || !unicode.IsDigit([]rune(y)[0]))
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if same(a[i-1], b[j-1]) {
				c = 0
			}
			cur[j] = min(prev[j-1]+c, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// commonPrefix is how many letters two words begin with alike.
func commonPrefix(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	n := 0
	for n < len(ra) && n < len(rb) && ra[n] == rb[n] {
		n++
	}
	return n
}

func wordsOf(s string) []string {
	// A soft hyphen breaks a word only on paper: "fu\u00adsio\u00adner" is "fusioner".
	s = strings.ReplaceAll(s, "\u00ad", "")
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// editDistance is the Levenshtein distance between two words.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			c := 1
			if ra[i-1] == rb[j-1] {
				c = 0
			}
			cur[j] = min(prev[j-1]+c, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// inflectedWord is the one word of text that a single quoted word names with another ending:
// »genanvendelse« for the text's "genanvende", which the ministry's next consolidation of
// miljøbeskyttelsesloven reads the same way ("lader sig genbruge eller genanvende"). One word must
// begin with the whole of the other, at least seven letters, and differ by at most four letters
// of ending.
// connectiveSwapped is a citation quoted with "og" where the text prints "eller", or the other way
// round: »kapitel 10 og 11« for the text's "kapitel 10 eller 11". Only a passage citing two numbers
// or more, and only when the text prints the swapped wording once.
func connectiveSwapped(text, passage string) (string, bool) {
	words := strings.Fields(Squeeze(QuoteStrip(passage)))
	if len(reNumberWord.FindAllString(strings.Join(words, " "), -1)) < 2 {
		return "", false
	}
	found, n := "", 0
	for i, w := range words {
		alt := ""
		switch strings.ToLower(w) {
		case "og":
			alt = "eller"
		case "eller":
			alt = "og"
		default:
			continue
		}
		swapped := append(append(append([]string{}, words[:i]...), alt), words[i+1:]...)
		cand := strings.Join(swapped, " ")
		if c := strings.Count(text, cand); c > 0 {
			found, n = cand, n+c
		}
	}
	if n == 0 {
		// »§ 71, stk. 1-3,« for the text's "§ 71, stk. 1 og 3,": a range typed for a pair, or the
		// other way.
		q := Squeeze(QuoteStrip(passage))
		for _, alt := range []string{reDashPair.ReplaceAllString(q, "$1 og $2"), reOgPair.ReplaceAllString(q, "$1-$2")} {
			if alt != q {
				if c := strings.Count(text, alt); c > 0 {
					found, n = alt, n+c
				}
			}
		}
	}
	return found, n == 1
}

// oneWordOther is a quotation of eight words or more that the text prints once with one word other,
// numbers aside: »Bestemmelserne om ydelser m.v. efter § 38 og § 45« for the text's "… m.v. i § 38 og
// § 45". A short word in a short quotation is more than the letters' measure allows.
func oneWordOther(src []rune, passage string) (string, bool) {
	want := wordsOf(QuoteStrip(passage))
	if len(want) < 8 {
		return "", false
	}
	type tok struct {
		w        string
		from, to int
	}
	var toks []tok
	for i := 0; i < len(src); {
		if !unicode.IsLetter(src[i]) && !unicode.IsDigit(src[i]) {
			i++
			continue
		}
		j := i
		for j < len(src) && (unicode.IsLetter(src[j]) || unicode.IsDigit(src[j])) {
			j++
		}
		toks = append(toks, tok{strings.ToLower(string(src[i:j])), i, j})
		i = j
	}
	hasDigit := func(w string) bool { return strings.IndexFunc(w, unicode.IsDigit) >= 0 }
	at, n := -1, 0
	for i := 0; i+len(want) <= len(toks); i++ {
		diff, ok := 0, true
		for k, w := range want {
			if toks[i+k].w != w {
				diff++
				if diff > 1 || hasDigit(w) || hasDigit(toks[i+k].w) || k == 0 || k == len(want)-1 {
					ok = false
					break
				}
			}
		}
		if ok && diff == 1 {
			at, n = i, n+1
		}
	}
	if n != 1 {
		return "", false
	}
	from, to := withMarks(src, toks[at].from, toks[at+len(want)-1].to, passage)
	return string(src[from:to]), true
}

// repeatedNumberDropped is a quotation the text prints once, word for word, but for a number the
// quotation prints twice and the text once: »Rådets direktiv 93/22/EØF af 10. maj 1993, EF-Tidende
// 1993 nr. L 141« for the note's "… af 10. maj 1993, EF-Tidende nr. L 141". The citation is the
// same; a number the quotation does not repeat is another citation, and is not placed.
func repeatedNumberDropped(src []rune, passage string) (string, bool) {
	want := wordsOf(QuoteStrip(passage))
	if len(want) < 8 {
		return "", false
	}
	type tok struct {
		w        string
		from, to int
	}
	var toks []tok
	for i := 0; i < len(src); {
		if !unicode.IsLetter(src[i]) && !unicode.IsDigit(src[i]) {
			i++
			continue
		}
		j := i
		for j < len(src) && (unicode.IsLetter(src[j]) || unicode.IsDigit(src[j])) {
			j++
		}
		toks = append(toks, tok{strings.ToLower(string(src[i:j])), i, j})
		i = j
	}
	count := map[string]int{}
	for _, w := range want {
		count[w]++
	}
	found, n := "", 0
	for k, w := range want {
		if count[w] < 2 || strings.IndexFunc(w, unicode.IsDigit) < 0 || k == 0 || k == len(want)-1 {
			continue
		}
		short := append(append([]string{}, want[:k]...), want[k+1:]...)
		for i := 0; i+len(short) <= len(toks); i++ {
			same := true
			for j, sw := range short {
				if toks[i+j].w != sw {
					same = false
					break
				}
			}
			if same {
				from, to := withMarks(src, toks[i].from, toks[i+len(short)-1].to, passage)
				if t := string(src[from:to]); t != found {
					found, n = t, n+1
				}
			}
		}
	}
	return found, n == 1
}

var (
	reDashPair = regexp.MustCompile(`\b([0-9]+)\s*[-–]\s*([0-9]+)\b`)
	reOgPair   = regexp.MustCompile(`\b([0-9]+)\s+og\s+([0-9]+)\b`)
)

// reNumberWord is a number a citation prints.
var reNumberWord = regexp.MustCompile(`\b[0-9]+\b`)

func inflectedWord(src []rune, passage string) (string, bool) {
	want := []rune(strings.ToLower(strings.TrimSpace(QuoteStrip(passage))))
	if len(want) < 8 {
		return "", false
	}
	for _, r := range want {
		if !unicode.IsLetter(r) {
			return "", false
		}
	}
	found, hits := "", 0
	for i := 0; i < len(src); {
		if !unicode.IsLetter(src[i]) {
			i++
			continue
		}
		j := i
		for j < len(src) && unicode.IsLetter(src[j]) {
			j++
		}
		w := []rune(strings.ToLower(string(src[i:j])))
		short, long := w, want
		if len(short) > len(long) {
			short, long = long, short
		}
		if len(short) >= 7 && len(long)-len(short) <= 4 && string(long[:len(short)]) == string(short) {
			found, hits = string(src[i:j]), hits+1
		}
		i = j
	}
	return found, hits == 1
}

// adjacentEdits reports two wordings that differ by words added or dropped in one run: the one is
// the other with a stretch of words cut out.
func adjacentEdits(a, b []string) bool {
	if len(a) < len(b) {
		a, b = b, a
	}
	if len(a) == len(b) {
		return wordEdits(a, b) <= 1
	}
	i := 0
	for i < len(b) && a[i] == b[i] {
		i++
	}
	j := 0
	for j < len(b)-i && a[len(a)-1-j] == b[len(b)-1-j] {
		j++
	}
	return i+j == len(b)
}
