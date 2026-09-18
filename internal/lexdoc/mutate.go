package lexdoc

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
			// numbered conditions belonging to stk. 4, 5 and 6.
			st.Span = Span{From: s.From, To: s.From + len(segs)}
			st.Items = nil
			continue
		}
		shiftSpan(&st.Span, s.To, delta)
		for j := range st.Items {
			shiftSpan(&st.Items[j].Span, s.To, delta)
		}
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

// InsertStykkerAfter adds subsections after the stykke numbered nr, or at the front when nr is
// zero, and renumbers by position. Each subsection is the segments it prints, marker text
// included: the register supplies a new "stk. 2" as its own element rather than as a provision,
// and measured over 90 amending acts that is the most common donated shape of all — 75 of 142
// provision-level changes brought a subsection, 24 a whole paragraph. Inserting a subsection is the one edit that changes what other
// subsections are *called*: the former stk. 3 becomes stk. 4, which is why an amendment's own
// "Det hidtidige stk. 3 bliver stk. 4" is usually already true by the time it is read.
func (p *Provision) InsertStykkerAfter(nr int, add [][]Segment) bool {
	if len(add) == 0 {
		return false
	}
	at := len(p.Segments)
	if nr != 0 {
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

	atIdx := 0
	for i := range p.Stykker {
		if p.Stykker[i].Nr == nr {
			atIdx = i + 1
		}
	}
	if nr == 0 {
		atIdx = 0
	}
	room := make([]Stykke, 0, len(p.Stykker)+len(add))
	room = append(room, p.Stykker[:atIdx]...)
	for i := range placed {
		room = append(room, Stykke{Span: placed[i]})
	}
	room = append(room, p.Stykker[atIdx:]...)
	p.Stykker = room
	p.renumberStykker()
	return true
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
func (p *Provision) DeleteStykke(nr int) bool {
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
// The heading is text inside the provision's own segments — the printed page prints it as part of
// the provision — so the edit is a span edit, and the Heading field is the same change written
// twice on purpose. It is the one place the field is written after parsing, and doing it here
// means there is no way to change a heading that leaves the two halves disagreeing.
func (p *Provision) SetHeading(old, new string) bool {
	if !p.Replace(old, new) {
		return false
	}
	p.Heading = new
	return true
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
		return l.Provision.SetHeading(l.Provision.Heading, new)
	case l.Provision != nil:
		group := c.containingPart(l.Provision)
		if group == nil || group.Heading == "" {
			return false
		}
		group.Heading = new
		return true
	case l.Part != nil && l.Part.Heading != "":
		l.Part.Heading = new
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
