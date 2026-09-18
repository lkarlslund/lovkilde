package lexdoc

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
	if len(p.Segments) == 0 {
		return false
	}
	from, to := s.From, s.To
	if from < 0 {
		from = 0
	}
	if from >= len(p.Segments) {
		return false
	}
	if to > len(p.Segments) || to < from {
		to = len(p.Segments)
	}
	if to <= from {
		return false
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
		texts[i-from] = []rune(p.Segments[i].Text)
		start[i-from] = len(text)
		text = append(text, texts[i-from]...)
		for range texts[i-from] {
			owner = append(owner, i-from)
		}
		if p.Segments[i].Break && i+1 < to {
			text = append(text, '\n')
			owner = append(owner, -1)
		}
	}

	norm, idx := MatchRunes(string(text))
	needle, _ := MatchRunes(QuoteStrip(old))
	if len(needle) == 0 {
		// An empty passage is present everywhere, which makes "replace it" meaningless. The
		// old reader rejected the same edit rather than rewriting the provision.
		return false
	}
	pos := RuneIndex(norm, needle)
	if pos < 0 || len(idx) <= pos+len(needle)-1 {
		return false
	}
	lo, hi := idx[pos], idx[pos+len(needle)-1]+1 // rune range within the rendered span text

	// The covered range is contiguous, so within any one segment it is a single interval: the
	// first and last owned rune the match reaches. Separators belong to no segment and are
	// simply gone once the text around them has been replaced.
	coverFrom := make([]int, len(texts))
	coverTo := make([]int, len(texts))
	for i := range coverFrom {
		coverFrom[i], coverTo[i] = -1, -1
	}
	for at := lo; at < hi; at++ {
		seg := owner[at]
		if seg < 0 {
			continue
		}
		off := at - start[seg]
		if coverFrom[seg] < 0 {
			coverFrom[seg] = off
		}
		coverTo[seg] = off + 1
	}

	newRunes := []rune(new)
	first := true
	for i := range texts {
		if coverFrom[i] < 0 {
			continue
		}
		out := make([]rune, 0, len(texts[i])-(coverTo[i]-coverFrom[i])+len(newRunes))
		out = append(out, texts[i][:coverFrom[i]]...)
		if first {
			// The whole replacement goes into the first touched segment, the way the tree
			// reader did it: the new text inherits that run's formatting rather than being
			// distributed over runs it has nothing to do with.
			out = append(out, newRunes...)
			first = false
		}
		out = append(out, texts[i][coverTo[i]:]...)
		p.Segments[from+i].Text = string(out)
	}
	return true
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
