package search

import (
	"strconv"
	"strings"

	"github.com/lkarlslund/lovkilde/internal/lexdoc"
)

// The unit of retrieval, cut from the model.
//
// The section list was never a claim about what a document *is*: it is whatever the
// publisher's elements happened to produce, one level deeper where a payload was recovered
// from HTML and one shallower where the register's XML nested its own headings. It indexed
// a paragraph as itself, as part of its section, and as part of its chapter, while the
// words outside that nesting stayed unfindable — 45.6% of a document's words on average and
// 93.0 MB of the 174.9 MB the server renders, measured over the 9,237-document sample
// (testdata/index-measurement-20260918-sections.txt, with the new side in
// …-model.txt). These units are cut from what the model knows instead, and every word goes
// in exactly once:
//
//   - a section is a unit whole when it has no numbered conditions; otherwise it carries its
//     rubric plus the text outside every condition span — the section's own number, a lead-in
//     line, a closing sentence. 918 of 919 sections with numbered conditions have such text,
//     holding 15.7% of their words, and the register prints it outside the conditions, so the
//     unit is cited as the section rather than as an invented address;
//   - each numbered condition is a unit: that is where a reader is looking, and it is the
//     address lookup resolves;
//   - a document's title, preamble, divisions, annexes, signature and whatever the model has
//     no concept for are units as well, so the anonymous whole-document blob is the last
//     resort rather than the default. An annex's own heading and prose form the annex unit;
//     the provisions and divisions inside it are cited as themselves.
//
// maxUnitRunes caps an indexed run of content the model cannot name. A unit bigger than a
// few paragraphs matches almost any pair of words and tells the reader nothing about where.
const maxUnitRunes = 2000

// One rule holds all of it together: a unit keeps its address only if the model's own lookup opens
// that address to the content the unit holds. The register does print the same number twice — an
// amendment act prints "§ 2" once per law it amends, ten times over — and lookup answers such an
// address with the first of them. Citing the rest by it would point a reader at the wrong provision,
// so their words stay findable and are cited as the document: imprecise, rather than wrong.

// Units builds the index units for one document. Exported so the serving gate asks the same
// question the index does: an address the shipped unitiser cites, not one a test invents.
func Units(docID int, meta DocMeta, m *lexdoc.Document) []Unit {
	u := &unitBuilder{docID: docID, m: m}
	u.add("titel", meta.Title)
	if text := lexdoc.SegmentsText(m.Content.Title); text != "" && strings.TrimSpace(text) != strings.TrimSpace(meta.Title) {
		u.add("titel", text)
	}
	u.add("indledningen", lexdoc.SegmentsText(m.Content.Preamble))

	for _, ref := range m.Content.Provisions() {
		u.provision(ref.Provision, false)
	}
	for i := range m.Content.EntryIntoForce {
		u.provision(&m.Content.EntryIntoForce[i], true)
	}
	for _, p := range m.Content.Parts() {
		u.part(p)
	}
	for i := range m.Content.Appendices {
		u.appendix(&m.Content.Appendices[i])
	}
	u.add("", lexdoc.SegmentsText(m.Content.Signature))
	// The content the model has no concept for is indexed, because the section list dropped
	// it and half of some documents' words were consequently unfindable. It goes in as runs
	// rather than one unit per element: per element it was ninety-one percent of all units
	// (103,492 of 113,103 in a diagnostic sample), and joined into one blob per document it
	// makes a unit so large that a hit in it points at a whole document. A run is capped at
	// a passage's size and carries no address, which is honest: the model cannot say where
	// in the document it sits.
	var run strings.Builder
	flush := func() {
		if text := strings.TrimSpace(run.String()); text != "" {
			u.add("", text)
		}
		run.Reset()
	}
	for _, un := range m.Content.Unmapped {
		// A piece kept with its layout — a ruling's whole body — is cut at its paragraphs, so a
		// run stays a passage.
		for _, para := range un.Paragraphs() {
			run.WriteString(para)
			run.WriteString("\n")
			if run.Len() >= maxUnitRunes {
				flush()
			}
		}
	}
	flush()
	return u.units
}

// unitBuilder accumulates units and refuses to cite an address twice: see the rule above.
type unitBuilder struct {
	docID int
	m     *lexdoc.Document
	units []Unit
}

func (u *unitBuilder) add(addr, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	u.units = append(u.units, Unit{Doc: u.docID, Addr: addr, Text: text})
}

// cite adds a unit with a location attached, and keeps the location only if the model opens
// it to this same content. That is a stronger test than "no two units may share an address":
// an amendment act that prints "§ 2" ten times — one per law it amends — has nine units that
// each genuinely are § 2 of something, and lookup answers the address with the first of them.
// Citing those units by that address would point a reader at the wrong provision, so they
// keep their words and are cited as the document, which is imprecise rather than wrong.
func (u *unitBuilder) cite(addr, text string, want func(lexdoc.Located) bool) {
	if addr != "" {
		a := lexdoc.ParseAddress(addr)
		if loc, ok := u.m.Content.Find(a); !ok || !want(loc) {
			addr = ""
		}
	}
	u.add(addr, text)
}

// provision adds a section at the size a reader cites it at — "§ 267, stk. 2" — with exactly
// one copy of every word and no unit quoting words the location it cites does not show.
//
// The register's condition spans cover only about five sixths of a split section's text: 918 of
// 919 splittable sections in a measured sample had segments outside every span, 15.7% of their
// words — the section's own number, a lead-in line, a closing sentence. Three shapes were
// measured against that fact:
//
//   - indexing the section whole *and* its conditions, which says every word two or three
//     times (151,502 units, 33.1 MB of index, and no query class measured better);
//   - absorbing the text between conditions into the condition after it, which keeps one copy
//     but leaves 21.2% of citations quoting words the opened location does not contain;
//   - this one: each condition holds its own span, and the text outside every span becomes a
//     unit of its own with the rubric folded in. It carries no address, because the model
//     cannot say where in the section it sits, and an unaddressed unit is imprecise rather
//     than wrong.
//
// Conditions with no number of their own are not cited, and a section with a single condition
// is cited as the section: the register prints no "Stk. 1" beside it, so citing one would
// invent a location the document does not have.
func (u *unitBuilder) provision(p *lexdoc.Provision, ikraft bool) {
	head := provisionAddress(p, ikraft)
	if head == "" || len(p.Stykker) < 2 || !allNumbered(p.Stykker) {
		u.cite(head, provisionText(p), func(l lexdoc.Located) bool { return l.Provision == p })
		return
	}
	covered := make([]bool, len(p.Segments))
	for _, st := range p.Stykker {
		for i := st.Span.From; i < st.Span.To && i < len(covered); i++ {
			if i >= 0 {
				covered[i] = true
			}
		}
	}
	for i, st := range p.Stykker {
		u.cite(head+", stk. "+strconv.Itoa(st.Nr), p.SpanText(st.Span), func(l lexdoc.Located) bool {
			return l.Provision == p && l.Stykke == &p.Stykker[i]
		})
	}
	var outside strings.Builder
	if h := strings.TrimSpace(p.Heading); h != "" {
		outside.WriteString(h + "\n")
	}
	for i, seg := range p.Segments {
		if !covered[i] {
			outside.WriteString(seg.Text)
			if seg.Break {
				outside.WriteByte('\n')
			}
		}
	}
	// The section's own words are cited as the section: opening "§ 267" renders the whole
	// section, which contains them. This is what keeps a split section reachable at its own
	// number as well as by condition — the section list cited 1,319 more such addresses over
	// a measured sample, and this recovers them without indexing any word twice.
	u.cite(head, outside.String(), func(l lexdoc.Located) bool { return l.Provision == p })
}

// allNumbered reports whether every condition carries the number that makes "stk. n" an
// address lookup can open.
func allNumbered(stykker []lexdoc.Stykke) bool {
	for _, st := range stykker {
		if st.Nr == 0 {
			return false
		}
	}
	return true
}

// part adds a division: its own label and heading. A division's provisions are units of their
// own, so the part carries what belongs to it and nothing else; the loose prose a printed chapter
// puts between its sections reaches the index through the model's unmapped content, where it is
// counted rather than silently attributed to the chapter.
func (u *unitBuilder) part(p *lexdoc.Part) {
	u.cite(partAddress(p), strings.TrimSpace(p.Printed+" "+p.Heading), func(l lexdoc.Located) bool { return l.Part == p })
}

// appendix adds an annex whole, with whatever it contains. Annexes are short and are read as
// units, and the model cannot express a provision *inside* an annex as an address, so citing
// smaller than the annex would name a location lookup cannot open.
func (u *unitBuilder) appendix(ap *lexdoc.Appendix) {
	// Only a numbered annex has an address: ParseAddress reaches a bilag by number, so a
	// lettered annex ("Bilag A") would be cited as "bilag " — the bare word with no number, the
	// shape that produces a hit pointing nowhere. It is indexed by its text and cited as the
	// document instead.
	addr := ""
	if n := lexdoc.FirstNumber(strings.TrimSpace(ap.Number + ap.Label)); n != "" {
		addr = "bilag " + n
	}
	// The annex unit holds the annex's own heading and prose. What it *contains* — its
	// sections and divisions — is indexed alongside it rather than folded into its text,
	// because the address for an annex renders the annex, and a unit quoting text that the
	// opened annex does not show is a citation that does not lead where it says.
	text := ap.Label + " " + ap.Heading + "\n" + lexdoc.SegmentsText(ap.Segments)
	for _, un := range ap.Unmapped {
		// the annex's prose between its provisions, which the opened annex shows
		text += "\n" + un.Words()
	}
	u.cite(addr, text, func(l lexdoc.Located) bool { return l.Appendix == ap })
	for _, n := range ap.Order {
		if n.Provision != nil {
			u.provision(n.Provision, false)
			continue
		}
		if n.Part != nil {
			u.part(n.Part)
		}
	}
}

// provisionAddress is the citation a hit carries: the number the document prints, with the
// effectivity series named when the provision belongs to it. No number is no address.
func provisionAddress(p *lexdoc.Provision, ikraft bool) string {
	number := strings.TrimSpace(p.Number + p.Litra)
	if number == "" {
		return ""
	}
	addr := "\u00a7 " + number
	if ikraft {
		addr += ", ikrafttrædelsesbestemmelse"
	}
	return addr
}

// provisionText is a section as a reader and a grep both see it: rubric first, then the body with
// its printed markers, which is what makes "§ 267" findable as text as well as as an address.
func provisionText(p *lexdoc.Provision) string {
	if strings.TrimSpace(p.Heading) == "" {
		return p.Text()
	}
	return p.Heading + "\n" + p.Text()
}

// partAddress is the word the document would be cited with, or nothing for a grouping the mirror
// has no printed word for.
func partAddress(p *lexdoc.Part) string {
	if strings.TrimSpace(p.Number) == "" {
		return ""
	}
	switch p.Kind {
	case lexdoc.PartChapter:
		return "kapitel " + p.Number
	case lexdoc.PartAfsnit:
		return "afsnit " + p.Number
	}
	return ""
}
