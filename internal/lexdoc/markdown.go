package lexdoc

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Rendering the content as markdown, for anything that shows a document to a reader rather than
// addressing into it.
//
// The tree path renders what its section list happens to hold, which is why a chapter, a provision
// and an annex are told apart by which publisher element they came from. This renders what the
// model *knows*: parts by their kind and printed number, provisions with their number and rubric,
// subsections as paragraphs and items as list entries because that is what they are, and the
// entry-into-force series under its own heading since citing them as "§ 1" of the law would be
// false.
//
// Two things are deliberately kept rather than tidied away. A footnote stays where the document
// put it — separated only by a paragraph break, since its marker is already in the text and the
// identifier the register generated for it is not something a reader needs to see — because a legal
// citation without its note is a different citation. And unmapped content is printed under a heading of its own: content the
// mirror has no concept for belongs in the open, where a reader can see the model is incomplete,
// rather than missing quietly.

// Markdown renders the whole content, in reading order.
func (c *Content) Markdown() string {
	var b strings.Builder
	writeSegments(&b, c.Title, "")
	writeSegments(&b, c.Preamble, "\n")
	writeNodes(&b, c.Order, 0)
	writeSegments(&b, c.Signature, "\n")

	if len(c.EntryIntoForce) > 0 {
		// These are provisions, and they are cited — but their numbering is its own series, so
		// they are not shown as part of the law's sequence.
		b.WriteString("\n## Entry into force and transitional provisions\n\n")
		b.WriteString("*Numbered in their own series: citing them as a section of the law would reach a different provision.*\n\n")
		for i := range c.EntryIntoForce {
			writeProvision(&b, &c.EntryIntoForce[i])
		}
	}

	for i := range c.Appendices {
		ap := &c.Appendices[i]
		fmt.Fprintf(&b, "\n## %s\n\n", appendixLabel(ap))
		writeSegments(&b, ap.Segments, "")
		writeNodes(&b, ap.Order, 0)
	}

	writeUnmapped(&b, c.Unmapped)
	return collapse(b.String())
}

// MarkdownProvisions renders one provision, or the part an address named, without the rest of the
// document: what an answer to "what does § 27 say?" is made of.
func (c *Content) MarkdownProvisions(a Address) (string, bool) {
	loc, ok := c.Find(a)
	if !ok {
		return "", false
	}
	var b strings.Builder
	switch {
	case loc.Part != nil:
		writePart(&b, loc.Part)
	case loc.Appendix != nil:
		fmt.Fprintf(&b, "## %s\n\n", appendixLabel(loc.Appendix))
		writeSegments(&b, loc.Appendix.Segments, "")
		writeNodes(&b, loc.Appendix.Order, 0)
	case loc.Preamble:
		writeSegments(&b, c.Preamble, "")
	case loc.Title:
		writeSegments(&b, c.Title, "")
	case loc.Provision != nil:
		writeProvision(&b, loc.Provision)
	}
	return collapse(b.String()), true
}

// writeNodes emits parts and provisions in the order the document puts them.
func writeNodes(b *strings.Builder, nodes []Node, depth int) {
	for _, n := range nodes {
		switch {
		case n.Part != nil:
			writePart(b, n.Part)
			writeNodes(b, n.Part.Order, depth+1)
		case n.Provision != nil:
			writeProvision(b, n.Provision)
		}
	}
}

// writePart states what a division is. Where the document printed its own label — "Kapitel 1" —
// that is what the reader gets, because it is what the page says and what an amendment quotes.
// Where it printed nothing, the mirror's own word for the kind stands in, so the answer still says
// what the thing is instead of leaving it to be guessed from a heading's font size.
func writePart(b *strings.Builder, p *Part) {
	label := strings.TrimSpace(p.Printed)
	if label == "" {
		kind := "section"
		switch p.Kind {
		case PartChapter:
			kind = "chapter"
		case PartAfsnit:
			kind = "division"
		case PartGroup:
			kind = "group of provisions"
		}
		label = strings.TrimSpace(kind + " " + p.Number)
	}
	fmt.Fprintf(b, "\n## %s\n\n", label)
	if p.Heading != "" && !strings.EqualFold(p.Heading, label) {
		fmt.Fprintf(b, "*%s*\n\n", p.Heading)
	}
}

// writeProvision prints a provision. The text already carries the number the document printed —
// "§ 2." is the first thing on the page — so a rubric is what earns a heading of its own, and a
// provision without one is printed as it stands rather than with a duplicate number above it.
func writeProvision(b *strings.Builder, p *Provision) {
	if p.Heading != "" {
		head := strings.TrimSpace(p.Printed)
		if head == "" {
			head = "§ " + p.Address()
		}
		fmt.Fprintf(b, "\n### %s %s\n\n", head, p.Heading)
	}
	writeProvisionBody(b, p)
}

// writeProvisionBody walks the provision's segments once and puts a paragraph break where the
// model says a subsection or item begins. It invents no markers: the document prints "Stk. 3" and
// "1)" itself, and a renderer that added its own numbering would print two numbers where the page
// has one, and would then disagree with any amendment that quotes the real one. The spans are what
// make the break — nothing here re-reads the text for a "Stk." which would turn one written inside
// a sentence into a subsection.
func writeProvisionBody(b *strings.Builder, p *Provision) {
	boundary := map[int]bool{}
	for i := range p.Stykker {
		st := &p.Stykker[i]
		if st.Span.From != 0 {
			boundary[st.Span.From] = true
		}
		for j := range st.Items {
			boundary[st.Items[j].Span.From] = true
		}
	}

	for i, s := range p.Segments {
		if boundary[i] {
			b.WriteByte('\n')
		}
		if s.Note != "" {
			// A footnote begins here. The document's own marker ("1)") is already the segment's
			// text, so the only thing the renderer adds is a paragraph break: the note then reads
			// as the separate block it is, while the identifier the model carries — "Par1_Stk1_Not1"
			// or a uuid the publisher generated — stays out of what a reader is shown.
			b.WriteString("\n\n")
		}
		b.WriteString(s.Text)
		if s.Break {
			b.WriteByte('\n')
		}
	}
	b.WriteString("\n\n")
}

// appendixLabel names an annex the way the document shows it.
func appendixLabel(ap *Appendix) string {
	if ap.Label != "" {
		return ap.Label
	}
	return "Annex " + ap.Number
}

// writeUnmapped prints the content the model has no concept for, counted by what the mirror called
// it. Leaving it out would make an incomplete model look complete.
func writeUnmapped(b *strings.Builder, unmapped []Unmapped) {
	if len(unmapped) == 0 {
		return
	}
	counts := map[string]int{}
	for _, u := range unmapped {
		counts[u.Concept]++
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	labels := make([]string, 0, len(kinds))
	for _, k := range kinds {
		labels = append(labels, fmt.Sprintf("%s ×%s", k, strconv.Itoa(counts[k])))
	}
	fmt.Fprintf(b, "\n## Other content the model has no concept for (%d pieces)\n\n", len(unmapped))
	fmt.Fprintf(b, "*%s*\n\n", strings.Join(labels, ", "))
	for _, u := range unmapped {
		if strings.TrimSpace(u.Text) == "" {
			continue
		}
		fmt.Fprintf(b, "> %s\n\n", strings.TrimSpace(u.Text))
	}
}

// writeSegments renders a run of segments, optionally separated from what came before.
func writeSegments(b *strings.Builder, segs []Segment, sep string) {
	if len(segs) == 0 {
		return
	}
	b.WriteString(sep)
	for _, s := range segs {
		b.WriteString(s.Text)
		if s.Break {
			b.WriteByte('\n')
		}
	}
	b.WriteString("\n\n")
}

// collapse keeps the document's paragraphs apart and nothing else. Blocks each end with a blank
// line, so runs of breaks pile up wherever two blocks meet; markdown reads three newlines the same
// way as two, but a reader comparing this text against the register should not have to.
func collapse(rendered string) string {
	var b strings.Builder
	for i := 0; i < len(rendered); {
		if rendered[i] == '\n' {
			run := 0
			for i+run < len(rendered) && rendered[i+run] == '\n' {
				run++
			}
			n := run
			if n > 2 {
				n = 2
			}
			b.WriteString(strings.Repeat("\n", n))
			i += run
			continue
		}
		b.WriteByte(rendered[i])
		i++
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}
