package lexdoc

import (
	"regexp"
	"strings"
)

// Structural changes to a document's content: the operations an amendment performs that are
// not text edits. Together with the span edits in edit.go they are everything consolidation
// needs, which is what lets the engine live here instead of in whatever markup arrived.
//
// Two rules run through this file, and both come from what the printed law does rather than
// from convenience:
//
//   - A provision keeps its identity when its content is replaced. "§ 5 ændres til: …" does not
//     repeal § 5 and write a new one; the number the law is cited by survives its own wording.
//   - Stykker are numbered by position. Measured over 400 documents' worth of the corpus, none
//     of 4,058 <Stk> elements carries a number of its own, so "stk. 2" means "the second
//     stykke", and repealing stk. 2 makes the old stk. 3 become stk. 2. Renumbering here is not
//     a fallback for missing data; it is the numbering rule.

// Clone returns an independent copy of the content.
//
// The copy has to be deep rather than a struct copy: every slice in a Content shares a backing
// array with its original when copied by value, and an insert into that copy — append with
// spare capacity — then writes through into the document it was cloned from. A consolidation
// reading a law as it stood on one date must never edit the archived one.
func (c *Content) Clone() *Content {
	if c == nil {
		return nil
	}
	out := &Content{
		Order:          cloneNodes(c.Order),
		Preamble:       cloneSegments(c.Preamble),
		Title:          cloneSegments(c.Title),
		Signature:      cloneSegments(c.Signature),
		EntryIntoForce: cloneProvisions(c.EntryIntoForce),
		Unmapped:       append([]Unmapped(nil), c.Unmapped...),

		EntryIntoForceContinues: c.EntryIntoForceContinues,
	}
	if c.Appendices != nil {
		out.Appendices = make([]Appendix, len(c.Appendices))
		for i := range c.Appendices {
			a := &c.Appendices[i]
			cp := Appendix{Number: a.Number, Label: a.Label, Heading: a.Heading,
				Order:    cloneNodes(a.Order),
				Segments: cloneSegments(a.Segments),
				Unmapped: append([]Unmapped(nil), a.Unmapped...)}
			out.Appendices[i] = cp
		}
	}
	return out
}

func cloneNodes(in []Node) []Node {
	if in == nil {
		return nil
	}
	out := make([]Node, len(in))
	for i, n := range in {
		if n.Part != nil {
			cp := *n.Part
			cp.Order = cloneNodes(n.Part.Order)
			out[i].Part = &cp
		}
		if n.Provision != nil {
			cp := cloneProvision(*n.Provision)
			out[i].Provision = &cp
		}
	}
	return out
}

func cloneProvisions(in []Provision) []Provision {
	if in == nil {
		return nil
	}
	out := make([]Provision, len(in))
	for i, p := range in {
		out[i] = cloneProvision(p)
	}
	return out
}

func cloneProvision(p Provision) Provision {
	cp := p
	cp.Segments = cloneSegments(p.Segments)
	if p.Stykker != nil {
		cp.Stykker = make([]Stykke, len(p.Stykker))
		for i, st := range p.Stykker {
			st.Items = append([]Item(nil), st.Items...)
			cp.Stykker[i] = st
		}
	}
	return cp
}

func cloneSegments(in []Segment) []Segment {
	if in == nil {
		return nil
	}
	return append([]Segment(nil), in...)
}

// pinNumbering fixes the stykke and item numbers Find answers to at what they are now, for the
// duration of one amending act.
//
// An amending act cites the law as it stood before that act, not as its own earlier changes have
// left it: "I § 88, stk. 2, der bliver stk. 5, …" follows a change that inserted three stykker
// ahead of stk. 2, and "I § 14, stk. 1, nr. 4, der bliver nr. 3" follows one that repealed nr. 1.
// The "der bliver" clause is the drafter's courtesy; the rule holds without it. Resolving such a
// change against the renumbered text edits the neighbour of the provision it names, or refuses a
// change that was perfectly placeable.
//
// Provisions are not affected: a § keeps its number when others come and go. Stykker and items
// an act creates have no number before it (Was 0), so the same act cannot address them by one.
func (c *Content) pinNumbering() {
	pin := func(p *Provision) {
		snap := cloneProvision(*p)
		p.preAct = &snap
		for i := range p.Stykker {
			st := &p.Stykker[i]
			st.Was = st.Nr
			for j := range st.Items {
				st.Items[j].Was = st.Items[j].Nr
			}
		}
	}
	c.eachProvision(pin)
	for _, p := range c.Parts() {
		p.was = p.Number
	}
	c.pinned = true
}

// pinDay records the numbering at the start of a day's acts. Acts that take effect on the same
// day are drafted against the text before any of them: the second act's "§ 84, stk. 2" is the
// subsection the first act's new stk. 1 has made stk. 3. Changes are still read against the
// numbering the earlier acts leave first; the day's numbering is what a refused change is retried
// against (see swapDay).
func (c *Content) pinDay() {
	c.eachProvision(func(p *Provision) {
		snap := cloneProvision(*p)
		p.preDay = &snap
		for i := range p.Stykker {
			st := &p.Stykker[i]
			st.WasDay = st.Nr
			for j := range st.Items {
				st.Items[j].WasDay = st.Items[j].Nr
			}
		}
	})
}

// swapDay exchanges the act's numbering with the day's, so Find answers to the day's. Swapping
// twice restores it, whatever was inserted or removed in between: a unit added since has neither.
func (c *Content) swapDay() {
	c.eachProvision(func(p *Provision) {
		p.preAct, p.preDay = p.preDay, p.preAct
		for i := range p.Stykker {
			st := &p.Stykker[i]
			st.Was, st.WasDay = st.WasDay, st.Was
			for j := range st.Items {
				it := &st.Items[j]
				it.Was, it.WasDay = it.WasDay, it.Was
			}
		}
	})
}

// unpinNumbering returns Find to the current numbering, which is what every reader of a finished
// consolidation means by "stk. 2".
func (c *Content) unpinNumbering() {
	c.pinned = false
	c.eachProvision(func(p *Provision) { p.preAct = nil })
}

// eachProvision visits every provision the content holds: the law's own series, the
// entry-into-force series, and the provisions inside annexes.
func (c *Content) eachProvision(fn func(p *Provision)) {
	var walk func(nodes []Node)
	walk = func(nodes []Node) {
		for _, n := range nodes {
			switch {
			case n.Provision != nil:
				fn(n.Provision)
			case n.Part != nil:
				walk(n.Part.Order)
			}
		}
	}
	walk(c.Order)
	for i := range c.EntryIntoForce {
		fn(&c.EntryIntoForce[i])
	}
	for i := range c.Appendices {
		walk(c.Appendices[i].Order)
	}
}

// findNodeList returns a pointer to the list that holds the node pointing at p, and where in
// it the node sits.
//
// A pointer to the *field* rather than to a copy of the slice: an insertion or a deletion grows
// or shrinks a list, and only writing through the field that holds it — Content.Order, or a
// Part's, or an appendix's — puts the change where readers of the document will find it. A
// window over the slice would compile but lose the edit.
func findNodeList(list *[]Node, p *Provision) (*[]Node, int) {
	for i := range *list {
		n := &(*list)[i]
		if n.Provision == p {
			return list, i
		}
		if n.Part != nil {
			if inner, idx := findNodeList(&n.Part.Order, p); idx >= 0 {
				return inner, idx
			}
		}
	}
	return nil, -1
}

// containingList is the list a provision belongs to, plus its index there.
func (c *Content) containingList(p *Provision) (*[]Node, int) {
	if p == nil {
		return nil, -1
	}
	if list, idx := findNodeList(&c.Order, p); idx >= 0 {
		return list, idx
	}
	for i := range c.Appendices {
		if list, idx := findNodeList(&c.Appendices[i].Order, p); idx >= 0 {
			return list, idx
		}
	}
	return nil, -1
}

// InsertProvisionBefore puts p in front of the provision anchor points at, and InsertProvisionAfter
// behind it. The anchor must be a provision the content holds; inserting next to something the
// document does not contain is refused rather than turned into an append, because an insertion
// whose position could not be established has to be visible in the unresolved count.
func (c *Content) InsertProvisionBefore(anchor, p *Provision) bool {
	list, idx := c.containingList(anchor)
	if list == nil {
		return false
	}
	in := *list
	// The slice handed back is the window the parent holds, so growing it and writing it back
	// is what puts the new provision in the parent rather than in a local copy.
	grown := make([]Node, 0, len(in)+1)
	grown = append(grown, in[:idx]...)
	grown = append(grown, Node{Provision: p})
	grown = append(grown, in[idx:]...)
	*list = grown
	return true
}

func (c *Content) InsertProvisionAfter(anchor, p *Provision) bool {
	list, idx := c.containingList(anchor)
	if list == nil {
		return false
	}
	in := *list
	grown := make([]Node, 0, len(in)+1)
	grown = append(grown, in[:idx+1]...)
	grown = append(grown, Node{Provision: p})
	grown = append(grown, in[idx+1:]...)
	*list = grown
	return true
}

// DeleteProvision removes a provision from the document: what "§ 7 ophæves" does. Provisions
// elsewhere keep their numbers, since those are their own, not positions — unlike stykker, whose
// numbering is positional and therefore shifts (see DeleteStykke).
func (c *Content) DeleteProvision(p *Provision) bool {
	list, idx := c.containingList(p)
	if list == nil {
		for i := range c.EntryIntoForce {
			if &c.EntryIntoForce[i] == p {
				c.EntryIntoForce = append(c.EntryIntoForce[:i:i], c.EntryIntoForce[i+1:]...)
				return true
			}
		}
		return false
	}
	in := *list
	gone := make([]Node, 0, len(in)-1)
	gone = append(gone, in[:idx]...)
	gone = append(gone, in[idx+1:]...)
	*list = gone
	return true
}

// ReplaceContent swaps one provision's wording, stykker and heading for another's while keeping
// the provision itself. The number and printed marker stay with the target: an amendment that
// rewrites § 5 changes what § 5 says, and § 5 remains citable.
//
// Heading is only taken from the replacement when it carries one. Amendments routinely give the
// new wording without repeating the heading, and dropping a rubric nobody asked to change would
// be a silent deletion.
func (c *Content) ReplaceContent(target *Provision, with Provision) bool {
	if target == nil || c.containingProvision(target) == nil {
		return false
	}
	target.Segments = cloneSegments(with.Segments)
	target.Stykker = nil
	for _, st := range with.Stykker {
		st.Items = append([]Item(nil), st.Items...)
		target.Stykker = append(target.Stykker, st)
	}
	if with.Heading != "" {
		target.Heading = with.Heading
	}
	if with.Printed != "" {
		target.Printed = with.Printed
	}
	return true
}

// containingProvision reports whether p belongs to this content, including the entry-into-force
// series, which lives in its own slice and so is not found by containingList.
func (c *Content) containingProvision(p *Provision) *Provision {
	if p == nil {
		return nil
	}
	if _, idx := c.containingList(p); idx >= 0 {
		return p
	}
	for i := range c.EntryIntoForce {
		if &c.EntryIntoForce[i] == p {
			return p
		}
	}
	return nil
}

// ReplaceSpanContent puts new segments where a span's were, and moves every span after them by
// the difference. This is what "stk. 1 ændres til: …" means when the new wording is not a quoted
// passage but the whole subsection: there is no old text to search for, only a range to replace.
func (p *Provision) ReplaceSpanContent(s Span, segs []Segment) bool {
	if s.From < 0 || s.To > len(p.Segments) || s.From > s.To {
		return false
	}
	delta := len(segs) - (s.To - s.From)
	kept := make([]Segment, 0, len(p.Segments)+delta)
	kept = append(kept, p.Segments[:s.From]...)
	kept = append(kept, segs...)
	kept = append(kept, p.Segments[s.To:]...)
	p.Segments = kept

	for i := range p.Stykker {
		st := &p.Stykker[i]
		if st.Span == s {
			// The addressed subsection owns the new text, and its items went with the words it
			// used to have. Leaving them would leave them pointing into whichever subsections
			// happen to follow the replacement: a § 27 whose replaced stk. 3 still listed four
			// numbered conditions belonging to stk. 4, 5 and 6. The new text brings its own.
			st.Span = Span{From: s.From, To: s.From + len(segs)}
			st.Items = ItemsIn(p.Segments, st.Span)
			continue
		}
		shiftSpan(&st.Span, s.To, delta)
		kept := st.Items[:0]
		for _, it := range st.Items {
			if it.Span.From >= s.From && it.Span.To <= s.To {
				continue // the condition's words went with the text that was replaced
			}
			shiftSpan(&it.Span, s.To, delta)
			kept = append(kept, it)
		}
		st.Items = kept
	}
	return true
}

// ReplaceItemContent puts new segments where one numbered item's were. The item keeps its place
// and its number; the subsection around it grows or shrinks with it, and everything after it moves.
func (p *Provision) ReplaceItemContent(target *Item, segs []Segment) bool {
	s := target.Span
	if s.From < 0 || s.To > len(p.Segments) || s.From > s.To {
		return false
	}
	delta := len(segs) - (s.To - s.From)
	kept := make([]Segment, 0, len(p.Segments)+delta)
	kept = append(kept, p.Segments[:s.From]...)
	kept = append(kept, segs...)
	kept = append(kept, p.Segments[s.To:]...)
	p.Segments = kept
	// "nr. 2 affattes således: 2) … 3) … 4) …" brings several conditions where there was one; each
	// is a condition of its own from now on, or the next act's "nr. 4" finds nothing.
	brought := ItemsIn(p.Segments, Span{From: s.From, To: s.From + len(segs)})
	for i := range p.Stykker {
		st := &p.Stykker[i]
		shiftSpan(&st.Span, s.To, delta)
		kept := st.Items[:0:0]
		for j := range st.Items {
			it := st.Items[j]
			if it.Span == s && it.Nr == target.Nr && it.Litra == target.Litra {
				if len(brought) > 1 && brought[0].Span.From == s.From {
					kept = append(kept, brought...)
					continue
				}
				it.Span = Span{From: s.From, To: s.From + len(segs)}
				kept = append(kept, it)
				continue
			}
			if it.Span.From >= s.From && it.Span.To <= s.To && it.Span != s {
				continue // a lettered condition inside the replaced one went with its words
			}
			shiftSpan(&it.Span, s.To, delta)
			kept = append(kept, it)
		}
		st.Items = withLetteredSubItems(p.Segments, kept)
	}
	return true
}

// shiftSpan moves the end of a range past a change that happened before it. Spans that reach
// across the change are left alone: they contain it, and their end has to move only if it sits
// after the change.
func shiftSpan(sp *Span, changedTo, delta int) {
	if sp.From >= changedTo {
		sp.From += delta
	}
	if sp.To >= changedTo {
		sp.To += delta
	}
	if sp.From > sp.To {
		sp.From = sp.To
	}
}

// InsertStykkerAfter adds subsections after the stykke numbered nr, or after the last one when nr
// is zero, or in front of the first when nr is negative, and renumbers by position. Zero used to put the text at the end and the subsections'
// entries at the front, which renamed every existing subsection. Each subsection is the segments it prints, marker text
// included: the register supplies a new "stk. 2" as its own element rather than as a provision,
// and measured over 90 amending acts that is the most common donated shape of all — 75 of 142
// provision-level changes brought a subsection, 24 a whole paragraph. Inserting a subsection is the one edit that changes what other
// subsections are *called*: the former stk. 3 becomes stk. 4, which is why an amendment's own
// "Det hidtidige stk. 3 bliver stk. 4" is usually already true by the time it is read.
func (p *Provision) InsertStykkerAfter(nr int, add [][]Segment) bool {
	if len(add) == 0 {
		return false
	}
	labelled := p.stykkeLabelsInOrder()
	firstNew := -1
	defer func() {
		// The new subsections print the numbers the act gave them. When those agree with where
		// they landed, the ones after them move up; when they do not, this text and the act
		// disagree about what came before, and the act's labels are left to say so.
		if labelled && p.stykkeLabelsAgreeFrom(firstNew, len(add)) {
			p.relabelStykker()
		}
	}()
	at := len(p.Segments)
	if nr < 0 {
		// "I § 1 indsættes før stk. 1 som nyt stykke": the old first subsection, which prints
		// no label, becomes stk. 2 and prints one.
		if len(p.Stykker) == 0 || p.Stykker[0].Span.From > len(p.Segments) {
			return false
		}
		f := p.Stykker[0].Span.From
		if f >= len(p.Segments) || reStykkeLabel.FindStringIndex(p.Segments[f].Text) == nil {
			label := "Stk. " + itoa(len(add)+1) + "."
			p.Segments = append(p.Segments[:f], append([]Segment{{Text: label, Break: true}}, p.Segments[f:]...)...)
			for i := range p.Stykker {
				st := &p.Stykker[i]
				if i > 0 {
					st.Span.From++
				}
				st.Span.To++
				for j := range st.Items {
					st.Items[j].Span.From++
					st.Items[j].Span.To++
				}
			}
			p.Stykker[0].Label = label
		}
		at = f
	} else if nr != 0 {
		st := p.Stykke(nr)
		if st == nil {
			return false
		}
		at = st.Span.To
	}

	// Everything after the insertion point moves by the number of segments added, and the new
	// subsections take the ranges they occupy as they are laid down in order.
	delta := 0
	for _, segs := range add {
		delta += len(segs)
	}
	if delta == 0 {
		return false
	}
	// Insertion moves only what starts at or after the point: a subsection ending exactly
	// there still ends there, and counting the new text as part of it would give one stykke
	// the words of its neighbour.
	for i := range p.Stykker {
		st := &p.Stykker[i]
		shiftInserted(&st.Span, at, delta)
		for j := range st.Items {
			shiftInserted(&st.Items[j].Span, at, delta)
		}
	}

	kept := make([]Segment, 0, len(p.Segments)+delta)
	kept = append(kept, p.Segments[:at]...)
	placed := make([]Span, 0, len(add))
	pos := at
	for _, segs := range add {
		kept = append(kept, segs...)
		placed = append(placed, Span{From: pos, To: pos + len(segs)})
		pos += len(segs)
	}
	// The text that was already there behind the insertion point stays there: dropping it is
	// how a subsection insertion would silently repeal the subsections that followed.
	kept = append(kept, p.Segments[at:]...)
	p.Segments = kept

	atIdx := len(p.Stykker)
	if nr < 0 {
		atIdx = 0
	}
	for i := range p.Stykker {
		if nr != 0 && p.Stykker[i].Nr == nr {
			atIdx = i + 1
		}
	}
	room := make([]Stykke, 0, len(p.Stykker)+len(add))
	room = append(room, p.Stykker[:atIdx]...)
	firstNew = atIdx
	for i := range placed {
		room = append(room, Stykke{Span: placed[i], Items: ItemsIn(p.Segments, placed[i])})
	}
	room = append(room, p.Stykker[atIdx:]...)
	p.Stykker = room
	p.renumberStykker()
	return true
}

// AppendStykke adds a subsection at the end of a provision, as the amending act delivered it.
//
// The register writes "§ 5 affattes således:" and then lays the paragraph, its items and its
// subsections out as siblings, so a provision assembled from the paragraph alone arrives missing
// the law that followed it. nr 0 leaves the number to position, which is how the reader numbers
// subsections; an explicit nr is what the publisher's own localId said.
func (p *Provision) AppendStykke(segs []Segment, nr int) bool {
	if len(segs) == 0 {
		return false
	}
	from := len(p.Segments)
	p.Segments = append(p.Segments, segs...)
	if nr == 0 {
		nr = len(p.Stykker) + 1
		for _, st := range p.Stykker {
			// Position is the ordinary rule, but not when the provision already carries
			// subsections the amending act numbered: taking the number of one that exists
			// would make two subsections answer the same citation.
			if st.Nr >= nr {
				nr = st.Nr + 1
			}
		}
	}
	sp := Span{From: from, To: len(p.Segments)}
	p.Stykker = append(p.Stykker, Stykke{Nr: nr, Span: sp, Items: ItemsIn(p.Segments, sp)})
	return true
}

// ItemsIn finds the numbered conditions in a stretch of text: a segment that opens with a marker
// such as "1)" or "b)" starts one, which runs to the next or to the end of the stretch.
//
// Amendments bring subsections and conditions as text, and both readers print a condition's marker
// at the head of its own segment. Text without its items cannot be cited by them: a subsection an
// amendment inserted was unaddressable as "stk. 6, nr. 1" by every later act. "1." is not taken,
// because it opens a sentence as often as a condition.
func ItemsIn(segs []Segment, sp Span) []Item {
	var items []Item
	for i := sp.From; i < sp.To && i < len(segs); i++ {
		f := strings.Fields(segs[i].Text)
		if len(f) == 0 || segs[i].Note != "" {
			continue // a footnote prints "1)" too, and is not a condition
		}
		marker := f[0]
		if !strings.HasSuffix(marker, ")") && len(f) > 1 && len([]rune(f[1])) == 2 && strings.HasSuffix(f[1], ")") {
			marker = f[0] + " " + f[1] // "5 a)"
		}
		if !strings.HasSuffix(marker, ")") {
			continue
		}
		nr, litra, suffix := ItemMarkerSuffix(marker)
		if nr == 0 && litra == "" {
			continue
		}
		if n := len(items); n > 0 {
			items[n-1].Span.To = i
		}
		items = append(items, Item{Nr: nr, Suffix: suffix, Litra: litra, Label: marker, Span: Span{From: i, To: sp.To}})
	}
	return items
}

// InsertLines puts lines into a provision behind the unit l names and inside it: after an item
// within its subsection, at the end of a subsection, or at the end of the provision. Lines that
// open with a condition marker become conditions of the subsection they land in.
//
// "I § 2, stk. 1, indsættes som nr. 4: 4) …" used to append to the end of the whole provision,
// outside the subsection it named, where the new condition could neither be read in place nor
// cited.
func (l Located) InsertLines(segs []Segment) bool {
	p := l.Provision
	if p == nil || len(segs) == 0 {
		return false
	}
	at := len(p.Segments)
	host := l.Stykke
	switch {
	case l.Item != nil:
		at = l.Item.Span.To
	case l.Stykke != nil:
		at = l.Stykke.Span.To
	case len(p.Stykker) > 0 && p.Stykker[len(p.Stykker)-1].Span.To == at:
		host = &p.Stykker[len(p.Stykker)-1]
	}
	return p.insertLinesAt(at, host, segs)
}

// InsertLinesBefore puts lines in front of the condition l names: "indsættes som nr. 2" against a
// subsection that has a nr. 2 makes the new condition nr. 2, and the old one nr. 3.
func (l Located) InsertLinesBefore(segs []Segment) bool {
	if l.Provision == nil || l.Item == nil || l.Stykke == nil || len(segs) == 0 {
		return false
	}
	return l.Provision.insertLinesAt(l.Item.Span.From, l.Stykke, segs)
}

// insertLinesAt puts lines into a provision at segment at, inside host when there is one.
func (p *Provision) insertLinesAt(at int, host *Stykke, segs []Segment) bool {
	sequential := host != nil && itemLabelsSequential(p.Segments, host.Items)
	delta := len(segs)
	hostEnds := host != nil && host.Span.To == at
	for i := range p.Stykker {
		st := &p.Stykker[i]
		shiftInserted(&st.Span, at, delta)
		for j := range st.Items {
			shiftInserted(&st.Items[j].Span, at, delta)
		}
	}
	kept := make([]Segment, 0, len(p.Segments)+delta)
	kept = append(kept, p.Segments[:at]...)
	kept = append(kept, segs...)
	kept = append(kept, p.Segments[at:]...)
	p.Segments = kept
	if host == nil {
		return true
	}
	if hostEnds {
		host.Span.To += delta
	}
	added := ItemsIn(p.Segments, Span{From: at, To: at + delta})
	if len(added) == 0 {
		return true
	}
	// The last condition before the insertion ends where the new ones begin, and the new ones are
	// filed between the conditions around them, numbered by position like DeleteItem does.
	items := make([]Item, 0, len(host.Items)+len(added))
	placed := false
	for _, it := range host.Items {
		if !placed && it.Span.From >= at+delta {
			items = append(items, added...)
			placed = true
		}
		items = append(items, it)
	}
	if !placed {
		items = append(items, added...)
	}
	lastBefore := 0 // the number printed on the last condition before the new ones
	for _, it := range host.Items {
		if it.Span.To <= at {
			if m := reItemLabel.FindStringSubmatch(strings.TrimSpace(SegmentsText(p.Segments[it.Span.From:min(it.Span.To, it.Span.From+1)]))); m != nil {
				lastBefore = Atoi(m[2])
			}
		}
	}
	renumberItems(items)
	host.Items = items
	if sequential && (placed || !continuesAfter(p.Segments, added, lastBefore)) {
		// Conditions after the new ones move up. Added at the end, nothing moves, and a new
		// condition numbered past the last keeps the number the act prints: "33)" where the
		// mirror's list, short of two conditions, ends at 31).
		p.relabelItems(host.Items)
	}
	return true
}

// continuesAfter reports new conditions whose printed numbers all come after last.
func continuesAfter(segs []Segment, added []Item, last int) bool {
	for _, it := range added {
		m := reItemLabel.FindStringSubmatch(strings.TrimSpace(SegmentsText(segs[it.Span.From:min(it.Span.To, it.Span.From+1)])))
		if m == nil || Atoi(m[2]) <= last {
			return false
		}
	}
	return len(added) > 0
}

// reItemLabel is a condition's printed number at the head of its line: "4)".
var reItemLabel = regexp.MustCompile(`^(\s*)([0-9]+)(\))`)

// itemLabelsSequential reports conditions printed 1), 2), 3) … in order, with no lettered or
// inserted ones among them: the numbering an insertion or a repeal can carry on by position.
func itemLabelsSequential(segs []Segment, items []Item) bool {
	n := 0
	for _, it := range items {
		if it.Litra != "" {
			continue
		}
		if it.Suffix != "" || it.Span.From >= len(segs) {
			return false
		}
		m := reItemLabel.FindStringSubmatch(segs[it.Span.From].Text)
		n++
		if m == nil || Atoi(m[2]) != n {
			return false
		}
	}
	return n > 0
}

// relabelItems prints each condition's position as its number, as the consolidated law does after
// "nr. 4 ophæves. Nr. 5 og 6 bliver herefter nr. 4 og 5", whether or not the act says so.
func (p *Provision) relabelItems(items []Item) {
	n := 0
	for i := range items {
		it := &items[i]
		if it.Litra != "" || it.Suffix != "" || it.Span.From >= len(p.Segments) {
			continue
		}
		n++
		seg := &p.Segments[it.Span.From]
		if m := reItemLabel.FindStringSubmatchIndex(seg.Text); m != nil && Atoi(seg.Text[m[4]:m[5]]) != n {
			seg.Text = seg.Text[:m[4]] + itoa(n) + seg.Text[m[5]:]
			if it.Label != "" {
				it.Label = itoa(n) + ")"
			}
		}
	}
}

// reLetterOpens is a lettered condition's label opening its text: "j)".
var reLetterOpens = regexp.MustCompile(`^(\s*)([a-z])(\))`)

// relabelLetters prints each lettered condition's position as its letter, in each run of letters
// under one condition or none, where an insertion has left a letter printed twice: "efter litra i
// som nyt litra: j) …" makes the old j the k.
func (p *Provision) relabelLetters(st *Stykke) {
	if st == nil {
		return
	}
	for i := 0; i < len(st.Items); {
		if st.Items[i].Litra == "" {
			i++
			continue
		}
		j := i
		for j < len(st.Items) && st.Items[j].Litra != "" {
			j++
		}
		run := st.Items[i:j]
		twice := false
		for k := 1; k < len(run); k++ {
			twice = twice || strings.EqualFold(run[k].Litra, run[k-1].Litra)
		}
		if twice && strings.EqualFold(run[0].Litra, "a") || twice && len(run) > 1 && strings.EqualFold(run[1].Litra, "a") {
			for k := range run {
				it := &run[k]
				if it.Span.From >= len(p.Segments) || k >= 26 {
					continue
				}
				want := string(rune('a' + k))
				seg := &p.Segments[it.Span.From]
				if m := reLetterOpens.FindStringSubmatchIndex(seg.Text); m != nil && seg.Text[m[4]:m[5]] != want {
					seg.Text = seg.Text[:m[4]] + want + seg.Text[m[5]:]
					it.Litra = want
					if it.Label != "" {
						it.Label = want + ")"
					}
				}
			}
		}
		i = j
	}
}

// shiftInserted moves the ends of a range that sit after an insertion point.
func shiftInserted(sp *Span, at, delta int) {
	if sp.From >= at {
		sp.From += delta
	}
	if sp.To > at {
		sp.To += delta
	}
}

// reStykkeLabel is the label a subsection prints in front of its text: "Stk. 3.".
var reStykkeLabel = regexp.MustCompile(`^(\s*Stk\.[\s\x{a0}]*)([0-9]+)(\s*\.)`)

// stykkeLabelsInOrder reports a provision whose subsections print the labels their positions give
// them: "Stk. 2." on the second, "Stk. 3." on the third. Only such a provision is relabelled after
// a subsection is repealed or inserted; one the publisher printed otherwise keeps its labels.
func (p *Provision) stykkeLabelsInOrder() bool {
	if len(p.Stykker) < 2 {
		return false
	}
	for i, st := range p.Stykker[1:] {
		if st.Span.From >= len(p.Segments) {
			return false
		}
		m := reStykkeLabel.FindStringSubmatch(p.Segments[st.Span.From].Text)
		if m == nil || Atoi(m[2]) != i+2 {
			return false
		}
	}
	return true
}

// stykkeLabelsAgreeFrom reports whether the n subsections from index from print labels matching
// their positions, or no label at all.
func (p *Provision) stykkeLabelsAgreeFrom(from, n int) bool {
	for i := from; i >= 0 && i < from+n && i < len(p.Stykker); i++ {
		st := p.Stykker[i]
		if st.Span.From >= len(p.Segments) {
			continue
		}
		if m := reStykkeLabel.FindStringSubmatch(p.Segments[st.Span.From].Text); m != nil && Atoi(m[2]) != i+1 {
			return false
		}
	}
	return true
}

// relabelStykker prints each subsection's position as its label, as the consolidated law does once
// "stk. 3 ophæves" has made the former stk. 4 the third: left alone, the text read "Stk. 4." where
// every citation of it now says stk. 3.
func (p *Provision) relabelStykker() {
	if len(p.Stykker) > 0 {
		// The first subsection prints no label: after "§ 80, stk. 1, ophæves" the former stk. 2
		// opens the provision as its unlabelled first.
		st := p.Stykker[0]
		for k := st.Span.From; k < st.Span.To && k < len(p.Segments) && k < st.Span.From+2; k++ {
			seg := &p.Segments[k]
			if m := reStykkeLabel.FindStringIndex(seg.Text); m != nil {
				rest := strings.TrimLeft(seg.Text[m[1]:], " \u00a0")
				if rest == "" && k+1 < st.Span.To {
					seg.Text = ""
				} else {
					seg.Text = rest
				}
				p.Stykker[0].Label = ""
				break
			}
			if strings.TrimSpace(seg.Text) != "" && !reParagraphMarker.MatchString(strings.TrimSpace(seg.Text)) {
				break // text before any label: the first subsection is unlabelled already
			}
		}
	}
	for i := 1; i < len(p.Stykker); i++ {
		st := &p.Stykker[i]
		if st.Span.From >= len(p.Segments) {
			continue
		}
		seg := &p.Segments[st.Span.From]
		if m := reStykkeLabel.FindStringSubmatchIndex(seg.Text); m != nil {
			seg.Text = seg.Text[:m[4]] + itoa(i+1) + seg.Text[m[5]:]
			if st.Label != "" {
				st.Label = reStykkeLabel.ReplaceAllString(st.Label, "${1}"+itoa(i+1)+"${3}")
			}
		}
	}
}

// reParagraphMarker is a provision's printed number on a line of its own: "§ 80.".
var reParagraphMarker = regexp.MustCompile(`^§[\s\x{a0}]*[0-9]+(?:[\s\x{a0}]*[a-zæøå])?\.$`)

// renumberStykker applies the positional numbering: stykker carry no number of their own, so
// their position is their name.
func (p *Provision) renumberStykker() {
	for i := range p.Stykker {
		p.Stykker[i].Nr = i + 1
	}
}

// DeleteStykke repeals one stykke and renumbers the ones after it, which is what the printed law
// does: repealing stk. 2 leaves no gap, and the former stk. 3 is cited as stk. 2 from then on.
//
// Removing the segments shifts every span after them, and the item spans live inside the removed
// range, so both have to move in the same step.
// DeleteItem removes one numbered condition and renumbers the rest by position, which is
// what "nr. 5 ophæves. Nr. 6 og 7 bliver herefter nr. 5 og 6" asks for. Deleting the
// containing subsection instead - which is what an address that resolves to an item used to
// do, because the location also carries its subsection - took seven conditions and the
// sentence introducing them with the one that was repealed: measured on Bekendtgørelse
// 2026-0037929, where "§ 2, stk. 1, nr. 5, ophæves" emptied § 2's first subsection outright.
func (p *Provision) DeleteItem(stykkeNr, itemNr int) bool {
	at, itemAt := -1, -1
	for i := range p.Stykker {
		if p.Stykker[i].Nr != stykkeNr {
			continue
		}
		at = i
		for j := range p.Stykker[i].Items {
			if p.Stykker[i].Items[j].Nr == itemNr {
				itemAt = j
			}
		}
	}
	if at < 0 || itemAt < 0 {
		return false
	}
	sequential := itemLabelsSequential(p.Segments, p.Stykker[at].Items)
	sp := p.Stykker[at].Items[itemAt].Span
	if sp.From > len(p.Segments) || sp.To > len(p.Segments) || sp.From > sp.To {
		return false
	}
	removed := sp.To - sp.From
	// The removed item takes the lettered conditions printed inside it.
	keptItems := p.Stykker[at].Items[:0]
	for j, it := range p.Stykker[at].Items {
		if j == itemAt || j > itemAt && it.Span.From >= sp.From && it.Span.To <= sp.To {
			continue
		}
		keptItems = append(keptItems, it)
	}
	p.Stykker[at].Items = keptItems
	kept := make([]Segment, 0, len(p.Segments)-removed)
	kept = append(kept, p.Segments[:sp.From]...)
	kept = append(kept, p.Segments[sp.To:]...)
	p.Segments = kept
	for i := range p.Stykker {
		st := &p.Stykker[i]
		if st.Span.From >= sp.To {
			st.Span.From -= removed
		}
		if st.Span.To >= sp.To {
			st.Span.To -= removed
		}
		if st.Span.From > st.Span.To {
			st.Span.From = st.Span.To
		}
		for j := range st.Items {
			it := &st.Items[j]
			if it.Span.From >= sp.To {
				it.Span.From -= removed
			}
			if it.Span.To >= sp.To {
				it.Span.To -= removed
			}
			if it.Span.From > it.Span.To {
				it.Span.From = it.Span.To
			}
		}
		renumberItems(st.Items)
	}
	if sequential {
		// Printed in order before, the conditions after the repealed one move up a number.
		p.relabelItems(p.Stykker[at].Items)
	}
	return true
}

func (p *Provision) DeleteStykke(nr int) bool {
	labelled := p.stykkeLabelsInOrder()
	defer func() {
		if labelled {
			p.relabelStykker()
		}
	}()
	at := -1
	for i := range p.Stykker {
		if p.Stykker[i].Nr == nr {
			at = i
			break
		}
	}
	if at < 0 {
		return false
	}
	sp := p.Stykker[at].Span
	if sp.From > len(p.Segments) || sp.To > len(p.Segments) || sp.From > sp.To {
		return false
	}
	kept := make([]Segment, 0, len(p.Segments)-(sp.To-sp.From))
	kept = append(kept, p.Segments[:sp.From]...)
	kept = append(kept, p.Segments[sp.To:]...)
	p.Segments = kept

	p.Stykker = append(p.Stykker[:at], p.Stykker[at+1:]...)
	for i := range p.Stykker {
		st := &p.Stykker[i]
		if st.Span.From >= sp.To {
			st.Span.From -= sp.To - sp.From
		}
		if st.Span.To >= sp.To {
			st.Span.To -= sp.To - sp.From
		}
		if st.Span.From > st.Span.To {
			st.Span.From = st.Span.To
		}
		st.Nr = i + 1
		for j := range st.Items {
			it := &st.Items[j]
			if it.Span.From >= sp.To {
				it.Span.From -= sp.To - sp.From
			}
			if it.Span.To >= sp.To {
				it.Span.To -= sp.To - sp.From
			}
			if it.Span.From > it.Span.To {
				it.Span.From = it.Span.To
			}
		}
	}
	return true
}

// SetHeading rewrites the rubric above a provision, which is what "overskriften for § 12 ændres
// til: »X«" asks for.
//
// The heading is a field, not text: the XML reader keeps the rubric out of the provision's
// segments so it is not printed twice. This used to edit the segments and report failure when the
// words were not there, which refused every provision-heading rewrite read from the XML.
func (p *Provision) SetHeading(new string) {
	if p.Heading != "" {
		p.Replace(p.Heading, new) // only where a reader printed it into the text as well
	}
	p.Heading = new
}

// containingPart returns the grouping element a provision stands inside, if any.
func (c *Content) containingPart(p *Provision) *Part {
	var walk func(nodes []Node, here *Part) *Part
	walk = func(nodes []Node, here *Part) *Part {
		for i := range nodes {
			n := &nodes[i]
			if n.Provision == p {
				return here
			}
			if n.Part != nil {
				if got := walk(n.Part.Order, n.Part); got != nil {
					return got
				}
			}
		}
		return nil
	}
	if got := walk(c.Order, nil); got != nil {
		return got
	}
	for i := range c.Appendices {
		if got := walk(c.Appendices[i].Order, nil); got != nil {
			return got
		}
	}
	return nil
}

// SetHeadingChange carries out a change to "the heading of § N", wherever that heading lives.
//
// A provision's own rubric is printed inside its text, so changing it means rewriting the text as
// well as the field. A paragraph group's rubric — what the register means by "overskriften før
// § 3" when the rubric stands above the group rather than above the paragraph — is a heading in its
// own right and exists only as one. Refusing the second shape because the provision has no heading
// field of its own is what made the model lose real heading changes.
func (c *Content) SetHeadingChange(l Located, new string) bool {
	switch {
	case l.Provision != nil && l.Provision.Heading != "":
		l.Provision.SetHeading(new)
		return true
	case l.Provision != nil:
		group := c.containingPart(l.Provision)
		if group == nil || group.Heading == "" {
			return false
		}
		group.Heading = new
		return true
	case l.Part != nil:
		// A chapter is addressed by its own number, so it needs no heading beforehand for one
		// to be given to it. "Andet afsnit. Efterforskning …" prints the part's label before its
		// heading; the label is the part's own.
		if p := strings.TrimSpace(l.Part.Printed); p != "" && len(new) > len(p) && strings.EqualFold(new[:len(p)], p) {
			new = strings.TrimSpace(strings.TrimLeft(new[len(p):], ".: "))
		}
		l.Part.Heading = new
		return true
	case l.Appendix != nil:
		// "Overskriften til bilag 2 affattes således": an annex is addressed by its number too.
		// An annex read as text prints its heading as the line under "Bilag 2"; that line is the
		// heading, and setting one beside it would print both.
		ap := l.Appendix
		if ap.Heading == "" && len(ap.Segments) > 1 && reAnnexHeader.MatchString(Squeeze(ap.Segments[0].Text)) {
			if h := Squeeze(ap.Segments[1].Text); h != "" && len([]rune(h)) < 300 && ItemsIn(ap.Segments, Span{From: 1, To: 2}) == nil {
				if new == "" {
					ap.Segments = append(ap.Segments[:1:1], ap.Segments[2:]...)
				} else {
					ap.Segments[1].Text = new
				}
				return true
			}
		}
		if new == "" && ap.Heading == "" {
			return false
		}
		ap.Heading = new
		return true
	}
	return false
}

// SetNumber renumbers a provision: "Det hidtidige stk. 1 bliver stk. 2" at stykke level, and
// "§ 5 bliver § 6" here. Printed changes with Number, because leaving the old marker above a new
// number would show the reader two answers at once.
func (p *Provision) SetNumber(number, printed string) {
	p.Number = number
	if printed != "" {
		p.Printed = printed
	}
}

// applyHeadingText carries out a quoted edit inside a heading: "I overskriften før § 6 i ændres
// »midlertidig« til: »midlertidige«". The heading is the provision's own rubric or, when it has
// none, the rubric of the group it stands in; the words are looked for there, not in the text below.
func (c *Content) applyHeadingText(e Edit, loc Located, reject func(string), apply func(where, note string)) {
	var heading *string
	switch {
	case loc.Provision != nil && loc.Provision.Heading != "":
		heading = &loc.Provision.Heading
	case loc.Provision != nil:
		if g := c.containingPart(loc.Provision); g != nil && g.Heading != "" {
			heading = &g.Heading
		}
	case loc.Part != nil:
		heading = &loc.Part.Heading
	case loc.Appendix != nil && loc.Appendix.Heading != "":
		heading = &loc.Appendix.Heading
	case loc.Appendix != nil:
		// An annex read as text prints its heading as the line under "Bilag 1", as
		// SetHeadingChange reads it.
		ap := loc.Appendix
		if len(ap.Segments) > 1 && reAnnexHeader.MatchString(Squeeze(ap.Segments[0].Text)) {
			if h := Squeeze(ap.Segments[1].Text); h != "" && len([]rune(h)) < 300 && ItemsIn(ap.Segments, Span{From: 1, To: 2}) == nil {
				heading = &ap.Segments[1].Text
			}
		}
	}
	where := loc.String() + " (overskrift)"
	passage := QuoteStrip(e.Old)
	if e.Kind == EditInsertText {
		passage, _ = insertionAnchorText(e)
	}
	if passage != "" && e.Address.Bilag == 0 && (heading == nil || !strings.Contains(MatchText(*heading), MatchText(passage))) {
		// "Efter § 75 h udgår overskriften »Salgets gennemførelse«" names the heading over § 75 i
		// by the provision before it. The words name it exactly, when one heading holds them.
		if h, at, ok := c.headingHolding(passage); ok {
			heading, where = h, at+" (overskrift)"
		}
	}
	if heading == nil {
		reject("no heading found for " + loc.String())
		return
	}
	segs := []Segment{{Text: *heading}}
	var found bool
	var n int
	switch e.Kind {
	case EditInsertText:
		anchor, before := insertionAnchorText(e)
		if anchor == "" {
			segs[0].Text = strings.TrimSpace(*heading) + " " + e.New
			found, n = true, 1
		} else {
			found, _, n = replaceIn(segs, Span{From: 0, To: 1}, anchor, joinInsertion(anchor, e.New, before), false)
		}
	default:
		found, _, n = replaceIn(segs, Span{From: 0, To: 1}, QuoteStrip(e.Old), e.New, false)
	}
	if !found || n == 0 {
		// "VIS- forordningen" in a heading the act quotes as »VIS-forordningen«: the line break
		// the heading was typeset with. Placed only where the words occur once.
		old, with := QuoteStrip(e.Old), e.New
		if e.Kind == EditInsertText {
			anchor, before := insertionAnchorText(e)
			old, with = anchor, joinInsertion(anchor, e.New, before)
		}
		if old != "" {
			if f, _ := replaceLoose(segs, Span{From: 0, To: 1}, old, with); f {
				found, n = true, 1
			}
		}
	}
	if (!found || n == 0) && loc.Appendix != nil && e.Kind == EditReplaceText && QuoteStrip(e.Old) != "" {
		// "I Bilag 1 i indholdsfortegnelsen ændres overskriften »Registrering som
		// arbejdssøgende« til: …": a heading the annex prints in its table of contents, which
		// comes first; named without it, the heading's words must occur once in the annex.
		ap, old := loc.Appendix, QuoteStrip(e.Old)
		text := SegmentsText(ap.Segments)
		if strings.Contains(strings.ToLower(e.Definition), "indholdsfortegnelse") || countMatches(text, old) == 1 {
			if f, _, k := replaceSelected(ap.Segments, Span{From: 0, To: len(ap.Segments)}, old, e.New, pickFirst); f && k > 0 {
				apply(loc.String(), "the heading is printed in the annex's own text")
				return
			}
		}
	}
	if !found || n == 0 {
		// "I overskriften til § 3, stk. 1, tabel A og B, ændres …": a table's caption is printed
		// in the subsection's own text, not as a heading of the provision.
		if loc.Provision != nil && (e.Kind == EditReplaceText || e.Kind == EditDeleteText) && loc.Stykke != nil {
			if f, _ := loc.replaceSpan(c, QuoteStrip(e.Old), e.New); f {
				apply(loc.String(), "the heading the change names is printed in the subsection's text")
				return
			}
		}
		reject("quoted passage not present in " + where)
		return
	}
	*heading = Squeeze(segs[0].Text)
	apply(where, "")
}

// headingHolding finds the one heading in the law whose words contain a passage, and says where it
// is.
func (c *Content) headingHolding(passage string) (*string, string, bool) {
	want := MatchText(passage)
	var found []*string
	var at []string
	var walk func(nodes []Node)
	walk = func(nodes []Node) {
		for _, n := range nodes {
			switch {
			case n.Provision != nil:
				if n.Provision.Heading != "" && strings.Contains(MatchText(n.Provision.Heading), want) {
					found, at = append(found, &n.Provision.Heading), append(at, Located{Provision: n.Provision}.String())
				}
			case n.Part != nil:
				if n.Part.Heading != "" && strings.Contains(MatchText(n.Part.Heading), want) {
					found, at = append(found, &n.Part.Heading), append(at, Located{Part: n.Part}.String())
				}
				walk(n.Part.Order)
			}
		}
	}
	walk(c.Order)
	if len(found) != 1 {
		return nil, "", false
	}
	return found[0], at[0], true
}
