package lexdoc

import (
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

	norm, idx := MatchRunes(string(text))
	needle, _ := MatchRunes(QuoteStrip(old))
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
	for at := 0; ; {
		pos := RuneIndex(norm[at:], needle)
		if pos < 0 || len(idx) <= at+pos+len(needle)-1 {
			break
		}
		pos += at
		end := pos + len(needle)
		if short && gluedToWord(norm, pos, len(needle)) {
			at = pos + 1
			continue
		}
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
		first := true
		for k := lo; k < hi; k++ {
			if owner[k] < 0 {
				continue
			}
			if first {
				place[k] = true
				first = false
			}
			skip[k] = true
		}
		n++
		at = end
		if !all || at >= len(norm) {
			break
		}
	}
	if n == 0 {
		return false, false, 0
	}

	for i := range texts {
		out := make([]rune, 0, len(texts[i])+len(newRunes))
		for at := start[i]; at < start[i]+len(texts[i]); at++ {
			if place[at] {
				out = append(out, newRunes...)
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
func gluedToWord(norm []rune, pos, n int) bool {
	if pos > 0 && (unicode.IsLetter(norm[pos-1]) || unicode.IsDigit(norm[pos-1])) {
		return true
	}
	end := pos + n
	if end < len(norm) && (unicode.IsLetter(norm[end]) || unicode.IsDigit(norm[end])) {
		return true
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
