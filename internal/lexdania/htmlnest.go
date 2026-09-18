package lexdania

import "strings"

// The payload writes a paragraph and its sub-stykker as siblings, because a flat
// HTML paragraph list cannot nest, and the canonical tree keeps a paragraph's text
// inside <Stk> children with numbered items inside those. This pass rebuilds the
// nesting.
//
// It is not cosmetic. Sections() only recognises a stykke as a *direct child* of a
// paragraph (sections.go), and numberedItems() is only ever called on such a child,
// so a flat tree has no "stk. 3" to resolve and never even looks for "nr. 2" -- an
// amendment addressing "§ 2, stk. 3" reports the provision as missing from a law
// that contains it.
//
// What may be absorbed is limited to the elements the paired canonical corpus
// actually nests inside a paragraph or a stykke: Stk (10,432 under paragraph
// elements), Exitus (22 directly under a paragraph, 11,161 under a stykke),
// Indentatio (3,457 under a stykke) and AendringsNummer (261 under a paragraph).
// Everything else ends the paragraph. Absorbing a heading or a chapter element
// would move the next section's text into this one, and a stolen heading is
// invisible while an unresolved address is not.
// nestStykker rebuilds paragraph nesting throughout a payload tree.
func nestStykker(root *Node, b *HTMLBody) {
	var walk func(n *Node)
	walk = func(n *Node) {
		nestInContainer(n, b)
		// Children are walked after the container is restructured, so everything
		// that moved is still visited through its new parent.
		for _, c := range n.Elems {
			walk(c)
		}
	}
	walk(root)
}

// nestInContainer absorbs what follows each paragraph of one container.
func nestInContainer(n *Node, b *HTMLBody) {
	for i := 0; i < len(n.Elems); i++ {
		par := n.Elems[i]
		if !isParagraphName(par.Name) {
			continue
		}
		nestParagraph(n, par, i, b)
	}
}

// nestParagraph moves the siblings that belong to par into it, in segments.
//
// Segmentation is what keeps the text in order, and that is not obvious. Absorbing
// one element at a time into "the paragraph, or else its last stykke" reorders
// prose whenever two elements land at different depths: measured on lta_1896_71, §
// 30's list items went into the stykke while the sentence introducing them went
// into the paragraph, and since the stykke precedes that sentence, the items read
// ahead of their own introduction. Attaching everything after a segment head into
// that head -- and only while it is the last thing appended -- makes reordering
// structurally impossible: each head's children are a contiguous slice of the
// payload's own order.
func nestParagraph(container, par *Node, at int, b *HTMLBody) {
	// head is the element later siblings attach to, and it is always the last
	// appended element: a stykke takes its own prose and items, and a proposed
	// change takes the list it introduces -- measured in the paired canonical
	// corpus, where an inserted list lives inside AendringsNummer rather than beside
	// it.
	//
	// Whether a first stykke is created at all depends on whether anything will go
	// into it, which is why the sibling run is inspected first: a paragraph whose
	// prose arrives in the next paragraph over is common enough (1,153 items and
	// their paragraphs in the pilot alone) that wrapping only on its own prose left
	// the items attached to the paragraph, where Sections() cannot see them.
	hasSibling := len(container.Elems) > at+1 && absorbIntoParagraph(container.Elems[at+1].Name)
	head := wrapStykke(par, hasSibling, b)
	for len(container.Elems) > at+1 {
		next := container.Elems[at+1]
		if !absorbIntoParagraph(next.Name) {
			break
		}
		dst := par
		switch {
		case isSegmentHead(next.Name):
			// Becomes a child of the paragraph and the new head.
		case head != nil:
			dst = head
		}
		absorbChild(container, dst, at+1)
		// The absorbed element is removed from at+1, so the sibling after it slides
		// into that index: no index is advanced.
		if isSegmentHead(next.Name) {
			head = next
		}
		b.absorbed++
	}
}

// isSegmentHead reports whether an element begins a segment of its own rather than
// joining the one before it. A stykke is never inside the previous stykke -- stk. 3
// is not a child of stk. 2 -- and a proposed change is separate content that the
// paragraph merely holds.
func isSegmentHead(name string) bool {
	return name == "Stk" || name == "AendringsNummer"
}

// wrapStykke moves a paragraph's own text into its first stykke, which is where the
// canonical tree keeps it: measured across the paired corpus, not one of 3,629
// paragraph elements holds text of its own.
//
// It only fires for a paragraph carrying a printed number. An unnumbered
// p.Paragraf is preamble prose -- "I lov om ..., foretages følgende ændring:" --
// whose canonical home is an <Exitus> inside the *preceding* centred paragraph,
// which is a merge in the opposite direction and deliberately not attempted here.
func wrapStykke(par *Node, hasSibling bool, b *HTMLBody) *Node {
	// The printed number is not always the paragraph's first child: the site puts an
	// empty anchor span in front of it (<span id="P1"/><span class="ParagrafNr">§ 1.</span>)
	// for linking. Requiring index 0 meant no first stykke was built for those
	// paragraphs, their own prose stayed unaddressable, and the payload's own
	// p.Stk2 elements became the paragraph's first stykke -- so every "stk. N" read
	// one place early. Measured: that off-by-one is the single largest residual in
	// the paired corpus.
	marker := -1
	for i, c := range par.Elems {
		if c.Name == "Explicatus" {
			marker = i
			break
		}
		// Skip only elements that carry nothing: an anchor is a link target, while
		// any text at all is content that belongs after the number, not before it.
		if len(c.Elems) > 0 || !blankHTMLRuns(c.Runs) {
			break
		}
	}
	if marker < 0 {
		// No printed number: this is preamble prose, not a provision. Measured
		// across the paired corpus every paragraph element has an Explicatus, so an
		// unnumbered one is legacy or amending preamble material, and inventing a
		// stykke around it would hide the sentence that names the law an amendment
		// changes (amendments.go reads a paragraph's own prose for that).
		return nil
	}
	// Everything after the number is the paragraph's prose; the number itself stays
	// on the paragraph, as in canonical. With nothing of its own and nothing
	// following it, the stykke would be empty, so no stykke is made.
	if !hasSibling && len(par.Elems) == marker+1 && blankHTMLRuns(par.Runs[marker+1:]) {
		return nil
	}
	split := marker + 1
	stk := &Node{
		Name:  "Stk",
		Attrs: map[string]string{},
		// par.Runs holds one more run than par.Elems, so the tail taken from the
		// number onwards is exactly one more than the elements moved with it: the
		// invariant holds on both sides without a synthetic run.
		Runs:  append([]string{}, par.Runs[split:]...),
		Elems: append([]*Node{}, par.Elems[split:]...),
	}
	// The payload puts the first stykke's canonical id on the number span
	// (<span class="ParagrafNr" id="Par2_Stk1">§ 2.</span>), which is the same
	// identifier the paired XML carries for that stykke. Moving it rather than
	// copying keeps one id on one node.
	if id := par.Elems[marker].Attrs["id"]; strings.Contains(id, "_Stk") {
		stk.Attrs["id"] = id
		delete(par.Elems[marker].Attrs, "id")
	}
	keptElems := append([]*Node{}, par.Elems[:split]...)
	keptRuns := append([]string{}, par.Runs[:split]...)
	// The kept children keep their own runs plus the two that bracket the new
	// stykke -- the text that followed the number went into the stykke with the
	// prose -- so len(Runs) == len(Elems)+1 holds on this side as well.
	par.Elems = append(keptElems, stk)
	par.Runs = append(keptRuns, "", "")
	b.wrapped++
	return stk
}

// absorbChild moves p.Elems[i] into dst as its last child.
//
// Both runs around the element survive: the whitespace that separated it from its
// old sibling travels with it, and the whitespace behind it stays in the old
// container. removeElement in consolidate.go deliberately drops one of them, which
// is right for deleting a provision and wrong for a move.
func absorbChild(p, dst *Node, i int) {
	child, lead, trail := p.Elems[i], p.Runs[i], p.Runs[i+1]
	p.Elems = append(p.Elems[:i], p.Elems[i+1:]...)
	p.Runs[i] = trail
	p.Runs = append(p.Runs[:i+1], p.Runs[i+2:]...)
	dst.Runs[len(dst.Runs)-1] += lead
	dst.Elems = append(dst.Elems, child)
	dst.Runs = append(dst.Runs, "")
}

// absorbIntoParagraph reports whether an element may be pulled into the paragraph
// before it. See the note above the measurement that bounds this list.
func absorbIntoParagraph(name string) bool {
	switch name {
	case "Stk", "Exitus", "Indentatio", "AendringsNummer":
		return true
	}
	return false
}

// blankHTMLRuns reports whether runs carry nothing but whitespace.
func blankHTMLRuns(runs []string) bool {
	for _, r := range runs {
		if strings.TrimSpace(r) != "" {
			return false
		}
	}
	return true
}
