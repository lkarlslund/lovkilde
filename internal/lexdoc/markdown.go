package lexdoc

import (
	"fmt"
	"html"
	"regexp"
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
// citation without its note is a different citation. And text outside the model's concepts is printed
// as the document's own text — a ruling's body, a bill's remarks under their heading — never left
// out, since a reader cannot tell missing text from text that was never there (see prose.go).

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
	writeProse(r, c.Title, "", false)
	writeProse(r, c.Preamble, "\n", true)
	groups := textGroups(c.Unmapped)
	writeTextGroups(r, groups, true, "summary")
	writeNodes(r, c.Order, 0)
	writeTextGroups(r, groups, false, "")
	writeProse(r, c.Signature, "\n", false)

	if len(c.EntryIntoForce) > 0 {
		// These are provisions, and they are cited — but their numbering is its own series, so
		// they are not shown as part of the law's sequence.
		if r.mode == htmlText {
			// The reading page is Danish; the text forms an agent reads stay in English.
			r.heading(2, "ikraft", "Ikrafttrædelses- og overgangsbestemmelser")
			r.emphasis("Nummereret i deres egen række: at citere dem som en paragraf i loven ville ramme en anden bestemmelse.")
		} else {
			r.heading(2, "ikraft", "Entry into force and transitional provisions")
			r.emphasis("Numbered in their own series: citing them as a section of the law would reach a different provision.")
		}
		for i := range c.EntryIntoForce {
			writeEntryIntoForce(r, &c.EntryIntoForce[i])
		}
	}

	for i := range c.Appendices {
		ap := &c.Appendices[i]
		writeAppendixHead(r, ap)
		writeProse(r, ap.Segments, "", true)
		writeNodes(r, ap.Order, 0)
		writeTextGroups(r, textGroups(ap.Unmapped), false, "")
	}

	writeTextGroups(r, groups, true, "")
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
		writeAppendixHead(r, loc.Appendix)
		writeProse(r, loc.Appendix.Segments, "", true)
		writeNodes(r, loc.Appendix.Order, 0)
		writeTextGroups(r, textGroups(loc.Appendix.Unmapped), false, "")
	case loc.Preamble:
		writeProse(r, c.Preamble, "", true)
	case loc.Title:
		writeProse(r, c.Title, "", false)
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
	if r.mode == htmlText && strings.TrimSpace(p.Printed) == "" {
		// A page shows what the document prints. A division it neither numbers nor heads keeps
		// its anchor and nothing else; one with only a heading shows the heading. The text forms
		// still name the kind, so an agent can tell a chapter from a group.
		id := r.ids.partID(p)
		if strings.TrimSpace(p.Heading) == "" {
			if id != "" {
				fmt.Fprintf(r.b, "<div id=%q></div>\n", id)
			}
			return
		}
		r.heading(2, id, p.Heading)
		return
	}
	label := strings.TrimSpace(p.Printed)
	if label == "" {
		kind := "section"
		switch p.Kind {
		case PartChapter:
			kind = "chapter"
		case PartAfsnit:
			kind = "division"
		case PartBook:
			kind = "book"
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
	if r.mode == htmlText && len(p.Stykker) > 0 {
		writeProvisionBodyHTML(r, p)
		return
	}
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

// writeProvisionBodyHTML lays a provision out as a reader expects a law to look: each subsection a
// paragraph opening with its label, each condition its own line indented under it with the label
// hanging, lettered conditions inside numbered ones indented once more, and the sentences of a
// paragraph run together as prose. The words are the plain text's, in its order
// (TestHTMLSaysWhatPlainTextSays); only where a block starts and how it is set differ.
func writeProvisionBodyHTML(r renderer, p *Provision) {
	// Blocks start at the provision's start, at every subsection and condition, and where a
	// condition ends inside its subsection, so text after a list is not set as the last condition.
	type block struct {
		depth int
		stk   bool
	}
	starts := map[int]block{0: {stk: true}}
	var items []Span
	for _, st := range p.Stykker {
		if st.Span.From > 0 {
			starts[st.Span.From] = block{stk: true}
		}
		for _, it := range st.Items {
			items = append(items, it.Span)
		}
	}
	depthAt := func(i int) int {
		d := 0
		for _, sp := range items {
			if sp.From <= i && i < sp.To {
				d++
			}
		}
		return d
	}
	if first := p.Stykker[0].Span.From; first > 0 && labelsOnly(p.Segments[:first]) {
		delete(starts, first) // "§ 9 a." printed apart: the first subsection's text follows it
	}
	for _, sp := range items {
		starts[sp.From] = block{depth: depthAt(sp.From)}
	}
	for _, st := range p.Stykker {
		numbered := false
		for _, it := range st.Items {
			numbered = numbered || it.Litra == "" && it.Nr > 0
		}
		for _, it := range st.Items {
			// "a)" listed beside "1)" rather than inside it: in a list of numbers, letters are
			// the number's conditions.
			if b := starts[it.Span.From]; numbered && it.Litra != "" && b.depth == 1 {
				starts[it.Span.From] = block{depth: 2}
			}
		}
	}
	for _, sp := range items {
		if _, taken := starts[sp.To]; !taken && sp.To < len(p.Segments) && !isStykkeStart(p, sp.To) {
			starts[sp.To] = block{depth: depthAt(sp.To)}
		}
	}

	open := false
	labelNext := false // the block's first segment is checked for a printed label
	cur := ""          // the class of the block being written
	for i, sg := range p.Segments {
		if b, ok := starts[i]; ok {
			if open {
				r.b.WriteString("</p>\n")
			}
			class := "stk"
			if !b.stk {
				class = "item d" + strconv.Itoa(min(b.depth, 3))
				if b.depth == 0 {
					class = "cont"
				}
			}
			fmt.Fprintf(r.b, "<p class=%q>", class)
			open, labelNext, cur = true, true, class
		}
		text := sg.Text
		if labelNext {
			labelNext = false
			lbl, rest, kind := printedLabel(text)
			if lbl == "" && cur != "cont" {
				// No label printed: an empty cell keeps the text in its column.
				r.b.WriteString(`<span class="lbl"></span>`)
			}
			if lbl != "" {
				fmt.Fprintf(r.b, "<span class=%q>", "lbl "+kind)
				r.text(lbl)
				r.b.WriteString("</span>")
				if strings.TrimSpace(rest) == "" {
					// a label printed on a line of its own: the text follows on the same line
					r.b.WriteString(" ")
					labelNext = kind == "par" && i+1 < len(p.Segments) && !isStartAt(starts, i+1) && isStykkeLabelOnly(p.Segments[i+1].Text)
					continue
				}
				text = rest
			}
		}
		r.text(text)
		if sg.Break {
			r.b.WriteString(" ")
		}
	}
	if open {
		r.b.WriteString("</p>\n")
	}
}

// labelsOnly reports lines that print labels and nothing else.
func labelsOnly(segs []Segment) bool {
	for _, sg := range segs {
		if lbl, rest, _ := printedLabel(sg.Text); lbl == "" || strings.TrimSpace(rest) != "" {
			return false
		}
	}
	return true
}

// isStartAt reports a block starting at segment i.
func isStartAt[T any](starts map[int]T, i int) bool {
	_, ok := starts[i]
	return ok
}

// isStykkeStart reports a segment where one of the provision's subsections begins.
func isStykkeStart(p *Provision, i int) bool {
	for _, st := range p.Stykker {
		if st.Span.From == i {
			return true
		}
	}
	return false
}

// isStykkeLabelOnly reports a line holding a subsection's label alone: "Stk. 1." printed after
// the provision's own number.
func isStykkeLabelOnly(text string) bool {
	m := reStkLabelLead.FindStringIndex(text)
	return m != nil && strings.TrimSpace(text[m[1]:]) == ""
}

// rePrintedLabel is the label a block of a provision opens with: the provision's "§ 9 a.", a
// subsection's "Stk. 2.", a condition's "1)", "5 a)" or "b)".
var rePrintedLabel = regexp.MustCompile(`^([\s\x{a0}]*)(§[\s\x{a0}]*[0-9]+(?:[\s\x{a0}]*[a-zæøå])?\.|Stk\.[\s\x{a0}]*[0-9]+[\s\x{a0}]*[a-zæøå]?\.|\(?[0-9]+(?:[\s\x{a0}]*[a-zæøå])?\)|[a-zæøå]\))([\s\x{a0}]|$)`)

// printedLabel splits a block's first line into the label it opens with and the rest, and says
// which kind of label it is: "par", "stk" or "nr".
func printedLabel(text string) (label, rest, kind string) {
	m := rePrintedLabel.FindStringSubmatchIndex(text)
	if m == nil {
		return "", text, ""
	}
	label = text[m[4]:m[5]]
	rest = text[m[5]:]
	switch {
	case strings.HasPrefix(label, "§"):
		kind = "par"
	case strings.HasPrefix(label, "Stk"):
		kind = "stk"
	default:
		kind = "nr"
	}
	return label, rest, kind
}

// appendixLabel names an annex the way the document shows it.
func appendixLabel(ap *Appendix) string {
	if ap.Label != "" {
		return ap.Label
	}
	return "Bilag " + ap.Number // the document's own word for it
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
