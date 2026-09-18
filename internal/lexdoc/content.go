package lexdoc

import (
	"fmt"
	"strings"
)

// Content is a document's text in the mirror's own terms.
//
// Two representation choices carry the weight, and both exist because of something the
// old tree representation cost:
//
//   - Text is stored as a flat ordered slice of Segments per provision, and a stykke or
//     item points at the span of that slice it covers. The provision's own text therefore
//     includes its subsections, exactly as the printed page does, and the same characters
//     are never stored twice. Consolidation edits a span in place and every enclosing
//     text follows it, which is what "the amendment changed § 2, stk. 1" has to mean.
//   - Entry-into-force material, appendices, the preamble and the title are separate
//     fields rather than provisions carrying an "appendix" flag. The old flag existed only
//     because the publisher nests that material under an element whose numbering restarts
//     at § 1; with it, "§ 3 ophæves" could delete a historical entry-into-force quotation
//     while the law's real § 3 survived. Here a provision is either in the law's own
//     numbering series or it is not, and the address resolver cannot accidentally choose
//     the wrong one.
type Content struct {
	// Order holds the document's parts and top-level provisions in reading order.
	// Provisions inside a part are reached through it, so the order of the corpus is the
	// order of this list rather than an artifact of a walk.
	Order []Node

	// Preamble is the document's opening prose ("indledningen affattes således:").
	Preamble []Segment

	// Title is the document's own title as addressable text, for "lovens titel ændres".
	Title []Segment

	// Signature is the signing block: the minister, the place and the date as printed.
	Signature []Segment

	// EntryIntoForce holds the effectivity and transitional provisions. They are
	// provisions — they are cited, amended and repealed — but their numbering is its own
	// series, so they are never reachable as "§ 1" of the law.
	EntryIntoForce []Provision

	// Appendices are the annexes, each with its own label and numbering.
	Appendices []Appendix

	// Unmapped is content the model has no concept for yet. It is counted and its text is
	// kept, so an incomplete model is visible in every document it read rather than being
	// inferred from a corpus-wide average.
	Unmapped []Unmapped
}

// Node is one entry in reading order: either a part or a provision. Exactly one is set.
// A pair of optional fields rather than an interface is deliberate: the compiled dataset is
// JSON, and a tagged union encodes into it without type names.
type Node struct {
	Part      *Part
	Provision *Provision
}

// PartKind is what a grouping unit is, in the mirror's terms.
type PartKind string

const (
	PartChapter PartKind = "chapter" // "kapitel"
	PartAfsnit  PartKind = "afsnit"  // a larger division than a chapter, printed in roman
	PartGroup   PartKind = "group"   // a named group of provisions within a chapter
)

// Part groups provisions: a chapter, an afsnit, a named paragraph group.
type Part struct {
	Kind PartKind

	// Number is normalised for addressing: roman where the source printed roman, because
	// "afsnit II" and "afsnit 2" are the same target and an amendment may use either.
	Number string

	// Printed is the number as the document shows it, e.g. "Afsnit II". Kept because it is
	// what a reader sees and what an amendment quotes.
	Printed string

	Heading string

	// Order nests: an afsnit holds chapters, a chapter holds groups and provisions.
	Order []Node
}

// Provision is one numbered provision of the document's own numbering series.
type Provision struct {
	// Number is the normalised sequence number used to match an address, e.g. "267".
	Number string

	// Printed is the element's own number text, e.g. "§ 267.".
	Printed string

	// Litra is the letter suffix: "a" in "§ 8 a", which the corpus prints apart and cites
	// together ("§ 8a").
	Litra string

	// Heading is the rubric printed above the provision, which amendments can target
	// ("overskriften for § 12").
	Heading string

	// Segments is the provision's full text, including its stykker, in reading order.
	Segments []Segment

	// Stykker are the subsections, each covering a span of Segments.
	Stykker []Stykke
}

// Stykke is a subsection: "stk. 2" of a provision. Its number is usually its position,
// because the publisher's elements carry an id but almost never a number.
type Stykke struct {
	Nr    int
	Span  Span
	Items []Item
}

// Item is a numbered or lettered sub-item: "nr. 3" or "litra b" inside a stykke.
type Item struct {
	Nr    int
	Litra string
	Span  Span
}

// Span is a half-open range over the Segments of the provision that owns it.
type Span struct {
	From, To int
}

// Segment is one piece of a provision's text.
type Segment struct {
	Text string

	// Break records that a block of text ends here, which is how the model keeps the
	// whitespace a printed page puts between paragraphs. Rendering emits a newline where
	// this is set; display text collapses it to a space, which is exactly what the
	// reader saw before the model existed.
	Break bool

	// Note, when set, marks the start of a footnote's text and carries the footnote's
	// identifier, so the reference and its note stay joined without a second index.
	Note string
}

// Unmapped is content that arrived with no concept for it.
type Unmapped struct {
	// Concept is what the mirror called the thing, from its own vocabulary: "table",
	// "signature", "remarks", "presentation", "image", "unclassified". It is never the
	// publisher's element name, so the artifact cannot leak the schema by accident.
	Concept string
	Text    string
}

// Appendix is an annex to the document, with its own label ("bilag 1", "Bilag A").
type Appendix struct {
	Number  string
	Label   string
	Heading string

	// Order holds whatever the annex contains, which is often a numbered series of its
	// own provisions and often only tables and prose.
	Order    []Node
	Segments []Segment
	Unmapped []Unmapped
}

// SegmentsText renders segments the way a reader sees them: whitespace-normalised, with a
// space where a block boundary fell.
func SegmentsText(segs []Segment) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.Text)
		if s.Break {
			b.WriteByte('\n')
		}
	}
	return Squeeze(b.String())
}

// Text is the provision's readable text, including its subsections.
func (p *Provision) Text() string { return SegmentsText(p.Segments) }

// Match is the normalised form quoted passages are located in. It is computed rather than
// stored: consolidation rewrites text in place, and a cached normalisation is how an engine
// starts matching against text that is no longer there.
func (p *Provision) Match() string { return MatchText(p.Text()) }

// SpanText renders the text of one stykke or item.
func (p *Provision) SpanText(s Span) string {
	if s.From > len(p.Segments) {
		return ""
	}
	if s.To > len(p.Segments) || s.To < s.From {
		s.To = len(p.Segments)
	}
	return SegmentsText(p.Segments[s.From:s.To])
}

// Stykke returns the subsection by number, or nil.
func (p *Provision) Stykke(nr int) *Stykke {
	for i := range p.Stykker {
		if p.Stykker[i].Nr == nr {
			return &p.Stykker[i]
		}
	}
	return nil
}

// Item returns the numbered or lettered item inside a stykke, or nil.
func (st *Stykke) Item(nr int, litra string) *Item {
	for i := range st.Items {
		it := &st.Items[i]
		if nr != 0 && it.Nr != nr {
			continue
		}
		if litra != "" && !strings.EqualFold(it.Litra, litra) {
			continue
		}
		if nr == 0 && litra == "" {
			continue
		}
		return it
	}
	return nil
}

// provisions appends the part's provisions, descending through nested parts. The innermost
// part is the one recorded, because that is the one a citation needs: "kapitel 4, § 12" names
// the chapter, not the afsnit the chapter sits in.
func (p *Part) provisions(dst []ProvisionRef) []ProvisionRef {
	for _, n := range p.Order {
		switch {
		case n.Provision != nil:
			dst = append(dst, ProvisionRef{Provision: n.Provision, Part: p})
		case n.Part != nil:
			dst = n.Part.provisions(dst)
		}
	}
	return dst
}

// ProvisionRef is a provision together with the part it sits in, which is what a renderer
// needs to print a chapter heading before its provisions and what an address needs to
// answer "kapitel 4, § 12".
type ProvisionRef struct {
	Provision *Provision
	Part      *Part
}

// Provisions returns every provision of the law's own numbering series, in reading order.
func (c *Content) Provisions() []ProvisionRef {
	var out []ProvisionRef
	for _, n := range c.Order {
		if n.Part != nil {
			out = n.Part.provisions(out)
			continue
		}
		if n.Provision != nil {
			out = append(out, ProvisionRef{Provision: n.Provision})
		}
	}
	return out
}

// Parts returns every part, outermost first, with nested parts after their parent.
func (c *Content) Parts() []*Part {
	var out []*Part
	var walk func(nodes []Node)
	walk = func(nodes []Node) {
		for _, n := range nodes {
			if n.Part != nil {
				out = append(out, n.Part)
				walk(n.Part.Order)
			}
		}
	}
	walk(c.Order)
	return out
}

// Located is what an Address resolved to. Exactly one pointer is set, except for an item,
// which names its stykke and provision as well so a renderer can print the whole citation.
type Located struct {
	Provision *Provision
	Stykke    *Stykke
	Item      *Item
	Part      *Part
	Appendix  *Appendix
	Preamble  bool
	Title     bool
}

func (l Located) Found() bool {
	return l.Provision != nil || l.Part != nil || l.Appendix != nil || l.Preamble || l.Title
}

// String renders the resolution in the citation form a reader would use.
func (l Located) String() string {
	switch {
	case l.Item != nil && l.Stykke != nil && l.Provision != nil:
		if l.Item.Litra != "" {
			return "§ " + l.Provision.Address() + ", stk. " + itoa(l.Stykke.Nr) + ", litra " + l.Item.Litra
		}
		return "§ " + l.Provision.Address() + ", stk. " + itoa(l.Stykke.Nr) + ", nr. " + itoa(l.Item.Nr)
	case l.Stykke != nil && l.Provision != nil:
		return "§ " + l.Provision.Address() + ", stk. " + itoa(l.Stykke.Nr)
	case l.Provision != nil:
		return "§ " + l.Provision.Address()
	case l.Part != nil:
		return string(l.Part.Kind) + " " + l.Part.Number
	case l.Appendix != nil:
		return "bilag " + l.Appendix.Number
	case l.Preamble:
		return "indledning"
	case l.Title:
		return "titel"
	}
	return ""
}

// Address is the number an address resolves against: the provision's number with its litra.
func (p *Provision) Address() string { return p.Number + p.Litra }

// itoa is here rather than imported from strconv so this file stays free of a second
// concern; the standard library call is one line either way.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Find resolves an address against the content, or reports not found.
//
// Resolution reads the *current* state of the content, not the document as published: an
// amendment's "§ 12, stk. 2" refers to the numbering that existed when it was written, so
// applying edits out of order silently mis-targets every later edit. That rule belongs to
// the model rather than to a reader, because it is about what a provision number means.
// Occurrences returns where a passage actually sits: "§ 56, stk. 3", "§ 5, stk. 2, nr. 3", or
// "§ 56" for text outside its numbered parts.
//
// This is the question a refused change raises. A refusal that says "the passage is not there" is
// only honest if the passage is nowhere in the act; when it is present in a different subsection,
// the amendment pointed at the wrong place and the reader deserves to be told where it points.
func (c *Content) Occurrences(passage string) []string {
	needle := MatchText(passage)
	if needle == "" {
		return nil
	}
	var found []string
	for _, ref := range c.Provisions() {
		p := ref.Provision
		where := p.Number
		inNumberedPart := false
		for _, st := range p.Stykker {
			at := where + fmt.Sprintf(", stk. %d", st.Nr)
			if strings.Contains(MatchText(p.SpanText(st.Span)), needle) {
				found = append(found, at)
				inNumberedPart = true
			}
			for _, it := range st.Items {
				if strings.Contains(MatchText(p.SpanText(it.Span)), needle) {
					found = append(found, fmt.Sprintf("%s, nr. %d", at, it.Nr))
					inNumberedPart = true
				}
			}
		}
		if !inNumberedPart && strings.Contains(p.Match(), needle) {
			found = append(found, where)
		}
	}
	return found
}

func (c *Content) Find(a Address) (Located, bool) {
	if !a.Recognised {
		return Located{}, false
	}
	if a.Bilag != 0 {
		want := itoa(a.Bilag)
		for i := range c.Appendices {
			ap := &c.Appendices[i]
			if FirstNumber(ap.Number+ap.Label) == want {
				return Located{Appendix: ap}, true
			}
		}
		return Located{}, false
	}
	if a.Chapter != 0 && a.Paragraph == "" {
		if p := c.part(PartChapter, itoa(a.Chapter)); p != nil {
			return Located{Part: p}, true
		}
		return Located{}, false
	}
	// The non-provision units are only claimed when the address names nothing else:
	// "I § 1 og i indledningen ændres …" has to keep pointing at § 1.
	if a.Paragraph == "" && a.Chapter == 0 && a.Bilag == 0 {
		if a.Afsnit != 0 {
			p := c.part(PartAfsnit, itoa(a.Afsnit))
			if p == nil {
				return Located{}, false
			}
			return Located{Part: p}, true
		}
		if a.Preamble {
			if len(c.Preamble) == 0 {
				return Located{}, false
			}
			return Located{Preamble: true}, true
		}
		if a.DocTitle {
			if len(c.Title) == 0 {
				return Located{}, false
			}
			return Located{Title: true}, true
		}
	}
	if a.Paragraph == "" {
		return Located{}, false
	}

	// Paragraph litra is written two ways in the corpus: "§ 8 a" as one token, and
	// "§ 8, litra a". Accept both. Entry-into-force provisions are not searched at all: if
	// the law's own § 3 is gone, an address for § 3 resolves to nothing rather than to the
	// entry-into-force quotation of some amending act printed as "§ 3". Refusing the edit
	// is visible in the unresolved count; editing the wrong provision is not.
	want := strings.ToLower(a.Paragraph)
	refs := c.Provisions()
	for i := range refs {
		p := refs[i].Provision
		// The number is compared on its own, not on the printed "1b": an address for "§ 1"
		// means the provision numbered 1, whether or not the corpus prints it as "§ 1 b.",
		// and the second branch is the other spelling, where the litra arrived glued on.
		if strings.ToLower(p.Number) == want {
			return c.descend(p, a)
		}
		if len(want) > 1 && strings.EqualFold(p.Number, want[:len(want)-1]) && p.Litra == want[len(want)-1:] {
			return c.descend(p, a)
		}
	}
	return Located{}, false
}

// part returns the first part of the given kind and number.
func (c *Content) part(kind PartKind, number string) *Part {
	for _, p := range c.Parts() {
		if p.Kind == kind && p.Number == number {
			return p
		}
	}
	return nil
}

// descend narrows a provision match to the requested stykke or item. It reports not-found
// rather than an empty Located: a caller that resolved the provision but not the part of it it
// asked for must not go on to edit the provision as a whole, which is how "stk. 3" would
// replace all of § 5 instead of the third stykke.
func (c *Content) descend(p *Provision, a Address) (Located, bool) {
	loc := Located{Provision: p}
	if a.Stk == 0 && a.Nr == 0 && a.Litra == "" {
		return loc, true
	}
	if a.Stk == 0 {
		// "§ 5, nr. 2" with no stykke named names nothing that exists: items live under a
		// stykke. Refusing is right rather than merely conservative — searching every
		// stykke for a matching number would splice text into whichever one happened to
		// have a "nr. 2", which is a guess about which provision the drafter meant.
		return Located{}, false
	}
	st := p.Stykke(a.Stk)
	if st == nil {
		return Located{}, false
	}
	loc.Stykke = st
	if a.Nr == 0 && a.Litra == "" {
		return loc, true
	}
	it := st.Item(a.Nr, a.Litra)
	if it == nil {
		return Located{}, false
	}
	loc.Item = it
	return loc, true
}
