package lexdoc

import (
	"fmt"
	"html"
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

// renderer writes the model's text either as markdown, which is the form an answer to a reader is
// served in, or as plain text, which is what a search snippet is cut from and what proximity is
// measured on. One walker serves both because two walkers drift: the plain mode drops only the
// characters markdown needs, never a word, so a snippet of a provision is the provision's own text
// rather than an approximation of it.
type renderer struct {
	// ids is what this render will write for each thing it meets, assigned by one walk of the
	// content (Content.anchorSet). It is nil for the text modes, which write no ids at all; every
	// caller below tolerates that by asking for an id and getting the empty string.
	ids  *anchorSet
	b    *strings.Builder
	mode renderMode
}

// renderMode selects the markup a rendering carries, never the words.
type renderMode int

const (
	markdown  renderMode = iota // the form an answer to a reader is served in
	plainText                   // what a snippet is cut from and proximity measured on
	htmlText                    // the same text, marked up for a browser and escaped
)

// text writes the document's words. In HTML mode that is the one place they get escaped, so a
// law whose text contains a tag arrives as the tag's own characters rather than as markup. Every
// other write in this file goes through here or through a helper that calls it.
func (r renderer) text(s string) {
	if r.mode == htmlText {
		r.b.WriteString(html.EscapeString(s))
		return
	}
	r.b.WriteString(s)
}

// lineBreak ends a line the way the source broke it.
func (r renderer) lineBreak() {
	if r.mode == htmlText {
		r.b.WriteString("<br>")
		return
	}
	r.b.WriteByte(10)
}

// paragraph writes a block of the document's own text. Markdown and plain text keep the blank
// line; HTML gets an element, which is the only way a browser keeps the same separation.
func (r renderer) paragraph(text string) {
	if r.mode == htmlText {
		r.b.WriteString("<p>")
		// A break the source marked becomes <br>, not a newline: in HTML a newline is
		// whitespace and disappears, so the register's own line break would be lost while the
		// text modes keep it.
		r.b.WriteString(strings.ReplaceAll(html.EscapeString(text), "\n", "<br>"))
		r.b.WriteString("</p>")
		return
	}
	r.text(text)
	r.b.WriteString("\n\n")
}

// section opens and closes one provision in HTML, so an address has something to point at even
// when the provision has no rubric to make a heading. The other modes have no such element: a
// blank line is where a provision starts in text a person reads linearly.
func (r renderer) open(id string) {
	if r.mode == htmlText && id != "" {
		fmt.Fprintf(r.b, "<section id=%q>", id)
	}
}

func (r renderer) close(id string) {
	if r.mode == htmlText && id != "" {
		r.b.WriteString("</section>")
	}
}

// heading states what a block is. Markdown gets a level; plain text gets the words, because
// "## Kapitel 5" quoted into an answer is markup the law does not contain.
func (r renderer) heading(level int, id, text string) {
	switch r.mode {
	case plainText:
		fmt.Fprintf(r.b, "\n")
		r.text(text)
		r.b.WriteString("\n\n")
	case htmlText:
		r.b.WriteString(fmt.Sprintf("\n<h%d", level))
		if id != "" {
			fmt.Fprintf(r.b, " id=%q", id)
		}
		r.b.WriteString(">")
		r.text(text)
		fmt.Fprintf(r.b, "</h%d>\n\n", level)
	default:
		r.b.WriteString(fmt.Sprintf("\n%s ", strings.Repeat("#", level)))
		r.text(text)
		r.b.WriteString("\n\n")
	}
}

// emphasis sets a line apart without giving it weight: a rubric, or a note about the model.
func (r renderer) emphasis(text string) {
	if r.mode == htmlText {
		r.b.WriteString("<p class=emphasis>")
		r.text(text)
		r.b.WriteString("</p>\n\n")
		return
	}
	if r.mode == plainText {
		r.paragraph(text)
		return
	}
	r.b.WriteString("*")
	r.text(text)
	r.b.WriteString("*\n\n")
}

// quote holds content the model has no concept for, so it reads as quoted rather than as the
// law's own words. Plain text cannot mark that, so the block boundary is all it keeps.
func (r renderer) quote(text string) {
	switch r.mode {
	case plainText:
		r.paragraph(text)
	case htmlText:
		r.b.WriteString("<blockquote>")
		r.text(text)
		r.b.WriteString("</blockquote>\n\n")
	default:
		r.b.WriteString("> ")
		r.text(text)
		r.b.WriteString("\n\n")
	}
}

// Markdown renders the whole content, in reading order.
func (c *Content) Markdown() string {
	s, _ := c.render(Address{}, markdown)
	return s
}

// PlainText is the whole content without markup.
func (c *Content) PlainText() string {
	s, _ := c.render(Address{}, plainText)
	return s
}

// HTML renders the whole content as escaped HTML with an id on every citable thing, which is what
// makes a search result's address into a link that lands on the provision it quoted. It walks the
// same renderer as the two text modes, so the words cannot differ from what an answer serves: the
// gate for that claim is TestHTMLSaysWhatPlainTextSays, which strips the markup back out and
// compares the words against PlainText.
func (c *Content) HTML() string {
	s, _ := c.render(Address{}, htmlText)
	return s
}

// HTMLProvisions renders one provision, or whatever an address named, in the same form.
func (c *Content) HTMLProvisions(a Address) (string, bool) { return c.render(a, htmlText) }

// MarkdownProvisions renders one provision, or the part an address named, without the rest of the
// document: what an answer to "what does § 27 say?" is made of.
func (c *Content) MarkdownProvisions(a Address) (string, bool) { return c.render(a, markdown) }

// PlainTextProvisions is the same rendering without markup, for a hit whose snippet has to be the
// text the document holds rather than the text the answer wrapper adds around it.
func (c *Content) PlainTextProvisions(a Address) (string, bool) { return c.render(a, plainText) }

// render is the one walker both forms go through, so the words a search snippet shows and the
// words an answer shows cannot come from different places.
func (c *Content) render(a Address, mode renderMode) (string, bool) {
	var b strings.Builder
	r := renderer{b: &b, mode: mode}
	if mode == htmlText {
		// Built once per render so that the anchors the page links to are the same ones the
		// contents list promised, and so that a narrowed view and the whole document give one
		// provision one id.
		r.ids = c.anchorSet()
	}
	if !a.Recognised {
		renderWhole(r, c)
		return collapse(b.String()), true
	}
	loc, ok := c.Find(a)
	if !ok {
		return "", false
	}
	renderLocated(r, c, loc)
	return collapse(b.String()), true
}

// renderWhole prints everything the document has, in reading order.
func renderWhole(r renderer, c *Content) {
	writeSegments(r, c.Title, "")
	writeSegments(r, c.Preamble, "\n")
	writeNodes(r, c.Order, 0)
	writeSegments(r, c.Signature, "\n")

	if len(c.EntryIntoForce) > 0 {
		// These are provisions, and they are cited — but their numbering is its own series, so
		// they are not shown as part of the law's sequence.
		r.heading(2, "ikraft", "Entry into force and transitional provisions")
		r.emphasis("Numbered in their own series: citing them as a section of the law would reach a different provision.")
		for i := range c.EntryIntoForce {
			writeEntryIntoForce(r, &c.EntryIntoForce[i])
		}
	}

	for i := range c.Appendices {
		ap := &c.Appendices[i]
		r.heading(2, r.ids.appendixID(ap), appendixLabel(ap))
		writeSegments(r, ap.Segments, "")
		writeNodes(r, ap.Order, 0)
	}

	writeUnmapped(r, c.Unmapped)
}

// renderLocated prints the narrowest unit an address names, and nothing else.
func renderLocated(r renderer, c *Content, loc Located) {
	switch {
	case loc.Item != nil:
		// The narrowest answer wins: a caller asking for "§ 5, stk. 2, nr. 3" is quoting that
		// entry, and getting the whole section back is an answer they have to search again.
		// The provision's rubric goes above it anyway, since an entry without its section is
		// not a citation.
		writeProvisionHead(r, loc.Provision)
		r.paragraph(loc.Provision.SpanText(loc.Item.Span))
	case loc.Stykke != nil:
		writeProvisionHead(r, loc.Provision)
		r.paragraph(loc.Provision.SpanText(loc.Stykke.Span))
	case loc.Part != nil:
		writePart(r, loc.Part)
	case loc.Appendix != nil:
		r.heading(2, r.ids.appendixID(loc.Appendix), appendixLabel(loc.Appendix))
		writeSegments(r, loc.Appendix.Segments, "")
		writeNodes(r, loc.Appendix.Order, 0)
	case loc.Preamble:
		writeSegments(r, c.Preamble, "")
	case loc.Title:
		writeSegments(r, c.Title, "")
	case loc.Provision != nil:
		writeProvision(r, loc.Provision)
	}
}

// writeNodes emits parts and provisions in the order the document puts them.
func writeNodes(r renderer, nodes []Node, depth int) {
	for _, n := range nodes {
		switch {
		case n.Part != nil:
			writePart(r, n.Part)
			writeNodes(r, n.Part.Order, depth+1)
		case n.Provision != nil:
			writeProvision(r, n.Provision)
		}
	}
}

// writePart states what a division is. Where the document printed its own label — "Kapitel 1" —
// that is what the reader gets, because it is what the page says and what an amendment quotes.
// Where it printed nothing, the mirror's own word for the kind stands in, so the answer still says
// what the thing is instead of leaving it to be guessed from a heading's font size.
func writePart(r renderer, p *Part) {
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
	r.heading(2, r.ids.partID(p), label)
	if p.Heading != "" && !strings.EqualFold(p.Heading, label) {
		r.emphasis(p.Heading)
	}
}

// writeProvision prints a provision. The text already carries the number the document printed —
// "§ 2." is the first thing on the page — so a rubric is what earns a heading of its own, and a
// provision without one is printed as it stands rather than with a duplicate number above it.
func writeProvision(r renderer, p *Provision) {
	id := r.ids.provisionID(p)
	r.open(id)
	writeProvisionHead(r, p)
	writeProvisionBody(r, p)
	r.close(id)
}

// writeEntryIntoForce prints one provision of the effectivity series. It is the same walk as any
// other provision; only the anchor differs, and it has to: "§ 2" of that series is a different
// provision from "§ 2" of the law, and one anchor for both would send a reader to the wrong one.
func writeEntryIntoForce(r renderer, p *Provision) {
	id := r.ids.provisionID(p)
	r.open(id)
	writeProvisionHead(r, p)
	writeProvisionBody(r, p)
	r.close(id)
}

// writeProvisionHead is the line a provision is cited by, so that a narrower answer can say what
// it is a part of.
func writeProvisionHead(r renderer, p *Provision) {
	if p.Heading != "" {
		head := strings.TrimSpace(p.Printed)
		if head == "" {
			head = "§ " + p.Address()
		}
		r.heading(3, "", head+" "+p.Heading)
	}
}

// writeProvisionBody walks the provision's segments once and puts a paragraph break where the
// model says a subsection or item begins. It invents no markers: the document prints "Stk. 3" and
// "1)" itself, and a renderer that added its own numbering would print two numbers where the page
// has one, and would then disagree with any amendment that quotes the real one. The spans are what
// make the break — nothing here re-reads the text for a "Stk." which would turn one written inside
// a sentence into a subsection.
func writeProvisionBody(r renderer, p *Provision) {
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

	// A footnote is deliberately not marked here. Its text sits where the mark sits, which is
	// often inside a sentence, and the block rules already separate it: anything more would
	// either split that sentence further or insert characters into the provision's own text.
	// What the model carries — the note's identifier — stays out of the answer entirely, since
	// "Par1_Stk1_Not1" and the uuids beside it are the publisher's bookkeeping, and the
	// document's own marker ("1)") is already in the text.
	var out strings.Builder
	started := false
	for i, sg := range p.Segments {
		if boundary[i] && started {
			out.WriteByte(10)
		}
		started = true
		out.WriteString(sg.Text)
		if sg.Break {
			out.WriteByte(10)
		}
	}
	r.paragraph(out.String())
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
func writeUnmapped(r renderer, unmapped []Unmapped) {
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
	r.heading(2, "ovrigt", fmt.Sprintf("Other content the model has no concept for (%d pieces)", len(unmapped)))
	r.emphasis(strings.Join(labels, ", "))
	for _, u := range unmapped {
		if strings.TrimSpace(u.Text) == "" {
			continue
		}
		r.quote(strings.TrimSpace(u.Text))
	}
}

// writeSegments renders a run of segments, optionally separated from what came before.
func writeSegments(r renderer, segs []Segment, sep string) {
	if len(segs) == 0 {
		return
	}
	r.b.WriteString(sep)
	var out strings.Builder
	for _, s := range segs {
		out.WriteString(s.Text)
		if s.Break {
			out.WriteByte('\n')
		}
	}
	r.paragraph(strings.TrimRight(out.String(), "\n"))
}

// collapse keeps the document's paragraphs apart and nothing else, and starts the answer on its
// first line rather than on a blank one. Blocks each end with a blank
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
	return strings.Trim(b.String(), "\n") + "\n"
}
