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

	// EntryIntoForceContinues says the entry-into-force provisions continue the law's own
	// numbering, as a short law's do, so that "§ 6" names them. Set from the law as published.
	EntryIntoForceContinues bool `json:",omitempty"`

	// Unmapped is content the model has no concept for yet. It is counted and its text is
	// kept, so an incomplete model is visible in every document it read rather than being
	// inferred from a corpus-wide average.
	Unmapped []Unmapped

	// pinned makes Find resolve stykke and item numbers against Was rather than Nr, while one
	// amending act is applied. See pinNumbering.
	pinned bool

	// carried is set while an act the base text may partly carry is applied: see
	// Amendment.Tentative. A change of such an act whose wording is already as it would leave it
	// is part of what the base carries, not a refusal.
	carried bool

	// renumbered keeps, per provision, how it stood before an act inserted or removed a subsection:
	// a later act may have been drafted against that numbering. See staleNumbering.
	renumbered map[string][]renumbering

	// afterAct holds what an act asks to be done once all its changes are made: relabelling an
	// annex's parts, which the act's later changes still cite by their old labels. relabelled keeps
	// one renumbering from being carried out twice when the act states it twice.
	afterAct   []func()
	relabelled map[string]bool
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
	PartBook    PartKind = "bog"     // the largest division: retsplejelovens "Fjerde bog", holding afsnit
)

// DanishOrdinals are the ordinal words a book or an afsnit is numbered by: "Fjerde bog", "Andet
// afsnit".
var DanishOrdinals = map[string]int{"første": 1, "anden": 2, "andet": 2, "tredje": 3, "fjerde": 4, "femte": 5,
	"sjette": 6, "syvende": 7, "ottende": 8, "niende": 9, "tiende": 10, "ellevte": 11, "tolvte": 12}

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

	// was is the number this part had when the act being applied began, "" for one that act
	// brought. See Content.pinNumbering.
	was string
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

	// preAct is the provision as it stood when the act being applied began, for the sentences that
	// act cites by their numbers from before it.
	preAct *Provision
	// preDay is the provision as it stood when the first act of the day began. See pinDay.
	preDay *Provision
}

// Stykke is a subsection: "stk. 2" of a provision.
//
// Label keeps the number the document prints for this stykke, in the form it prints it
// ("Stk. 2."). The publisher states it in a child <Explicatus> element on every stykke except
// a provision's first — 318 labelled of 897 <Stk> elements in BR18's base document, and the
// 579 unlabelled ones are exactly one per provision, the Danish convention — so the printed
// label is the fact and Nr is a position. Nr is kept positional on purpose: honouring the
// printed number before deletions and insertions renumber costs five real edits on the
// parity corpus, because provisions donated from another act arrive as text with no labels.
// The printed label is what an amendment renumbering subsections ("Nr. 8-10 bliver herefter
// 7-9") has to edit, so keeping it is what makes the renumbering and the switch to it
// possible. See lexdania.printedStykke.
type Stykke struct {
	Nr    int
	Label string `json:"Label,omitempty"` // printed label as published, e.g. "Stk. 2."; "" where the document labels none
	Span  Span
	Items []Item

	// Was is the number this stykke had when the amending act being applied was read, and 0
	// for one that act created. See Content.pinNumbering.
	Was int `json:"-"`
	// WasDay is the number before the first act taking effect on the same day. See pinDay.
	WasDay int `json:"-"`
}

// Item is a numbered or lettered sub-item: "nr. 3" or "litra b" inside a stykke. Label holds
// its printed marker ("3)", "b)") for the same reason Stykke keeps its label.
type Item struct {
	Nr     int
	Suffix string `json:",omitempty"` // "5 a)": item 5 a, inserted between 5 and 6
	Litra  string
	Label  string `json:"Label,omitempty"`
	Span   Span

	Was    int `json:"-"` // see Stykke.Was
	WasDay int `json:"-"` // see Stykke.WasDay
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

	// Para records that a paragraph ends here, where the source says so (the register's
	// <Exitus>): a paragraph may print as several lines, and "de to sidste afsnit" counts
	// paragraphs, not lines.
	Para bool `json:",omitempty"`

	// Style is how the document set the text: "b" bold, "i" italic, "bi" both, "" plain. The
	// register sets a heading inside prose — "Generelle bemærkninger", "§ 1." in an annex that
	// prints its own provisions as text — in bold and nothing else, so without it a renderer
	// cannot tell a heading from the sentence under it.
	Style string `json:",omitempty"`

	// Indent is how deep in the document's own lists the text sits: 1 for an entry of a list,
	// 2 for an entry of a list inside that one, 0 outside any. Marker marks the entry's printed
	// label ("1.", "a)"), which the document prints apart from the entry's text.
	Indent int  `json:",omitempty"`
	Marker bool `json:",omitempty"`

	// Table, Row and Col place text printed in a table cell: the document's Table-th table, its
	// Row-th row and the grid column the cell starts in (a cell spanning columns counts them).
	// Zero Table is text outside any table. A change to a table's cells — "indsættes i gruppen
	// »kernefrugter« efter »Chlorfenvinphos«: »Chlormequat« med maksimalgrænseværdien »0,2m« for
	// produktet »Pærer«" — is placed by them.
	Table int `json:",omitempty"`
	Row   int `json:",omitempty"`
	Col   int `json:",omitempty"`

	// Note, when set, marks a footnote's text and carries the footnote's identifier, so the
	// reference and its note stay joined without a second index. Every segment of the note
	// carries it; the provision's own words are the segments without it.
	Note string
}

// Unmapped is content that arrived with no concept for it.
type Unmapped struct {
	// Concept is what the mirror called the thing, from its own vocabulary: "table",
	// "signature", "remarks", "presentation", "image", "unclassified". It is never the
	// publisher's element name, so the artifact cannot leak the schema by accident.
	Concept string

	// Text is the piece's words, kept for content read from markup with no layout to keep. Where
	// the reader kept the layout — the paragraphs, headings, lists and tables of a ruling's
	// body, a bill's remarks, a summary — it is in Segments instead and Text is empty, so the
	// words are stored once. Words reads either.
	Text     string    `json:",omitempty"`
	Segments []Segment `json:",omitempty"`
}

// Paragraphs is the piece's text paragraph by paragraph, whichever form it was kept in.
func (u Unmapped) Paragraphs() []string {
	if len(u.Segments) == 0 {
		if strings.TrimSpace(u.Text) == "" {
			return nil
		}
		return []string{u.Text}
	}
	var out []string
	from := 0
	for i, sg := range u.Segments {
		if sg.Para || i == len(u.Segments)-1 {
			if t := SegmentsText(u.Segments[from : i+1]); strings.TrimSpace(t) != "" {
				out = append(out, t)
			}
			from = i + 1
		}
	}
	return out
}

// Words is the piece's text, whichever form it was kept in.
func (u Unmapped) Words() string {
	if len(u.Segments) > 0 {
		return SegmentsText(u.Segments)
	}
	return u.Text
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
func (p *Provision) Stykke(nr int) *Stykke { return p.stykke(nr, false) }

// stykke looks a subsection up by its current number, or by the number it had before the act
// being applied when pinned.
func (p *Provision) stykke(nr int, pinned bool) *Stykke {
	for i := range p.Stykker {
		st := &p.Stykker[i]
		if st.Nr == nr && !pinned || st.Was == nr && pinned {
			return st
		}
	}
	if pinned {
		// A provision the act itself created has no numbers from before it, and the act cites
		// it by the ones it gave it: "Efter § 36 f indsættes: § 36 g …", then "I § 36 g, stk. 1".
		for i := range p.Stykker {
			if st := &p.Stykker[i]; st.Was == 0 && st.Nr == nr && p.createdInAct() {
				return st
			}
		}
	}
	return nil
}

// createdInAct reports a provision none of whose subsections existed before the act being applied.
func (p *Provision) createdInAct() bool {
	for _, st := range p.Stykker {
		if st.Was != 0 {
			return false
		}
	}
	return true
}

// Item returns the numbered or lettered item inside a stykke, or nil.
func (st *Stykke) Item(nr int, litra string) *Item { return st.item(nr, litra, false) }

func (st *Stykke) item(nr int, litra string, pinned bool) *Item {
	return st.itemSuffix(nr, "", litra, pinned)
}

// itemSuffix finds "nr. 5 a" by its number and letter; a plain "nr. 5" never answers with 5 a.
func (st *Stykke) itemSuffix(nr int, suffix, litra string, pinned bool) *Item {
	if nr != 0 && litra != "" {
		// "nr. 3, litra e": the lettered conditions follow their number in the list, up to the
		// next number.
		for i := range st.Items {
			have := st.Items[i].Nr
			if pinned {
				have = st.Items[i].Was
			}
			if have != nr || st.Items[i].Litra != "" {
				continue
			}
			for j := i + 1; j < len(st.Items) && st.Items[j].Litra != ""; j++ {
				if strings.EqualFold(st.Items[j].Litra, litra) {
					return &st.Items[j]
				}
			}
			return nil
		}
		return nil
	}
	if pinned && nr != 0 {
		// The number from before the act first; an item the act itself created answers to the
		// number it was given only when no item had that number before.
		if it := st.itemSuffix(nr, suffix, litra, false); it != nil && it.Was == 0 && !st.hasWas(nr, suffix) {
			return it
		}
	}
	for i := range st.Items {
		it := &st.Items[i]
		have := it.Nr
		if pinned {
			have = it.Was
		}
		if nr != 0 && have != nr || nr != 0 && it.Suffix != suffix {
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
	Footnote  bool // with Title: the note under the title, not the title
	// AnnexSpans are the parts of Appendix a change names, "bilag 1, pkt. 5 og 6": its words are
	// changed there, and not at their first occurrence in the annex.
	AnnexSpans []Span
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
	case l.Title && l.Footnote:
		return "fodnoten til titlen"
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

// Find resolves an address against the content, or reports not found.
//
// Resolution reads the *current* state of the content, not the document as published: an
// amendment's "§ 12, stk. 2" refers to the numbering that existed when its act was written, so
// applying acts out of order silently mis-targets every later edit. While an act is being
// applied the stykke and item numbers are the ones from before that act (see pinNumbering).
func (c *Content) Find(a Address) (Located, bool) {
	if !a.Recognised {
		return Located{}, false
	}
	if a.Bilag == TheAnnex {
		if len(c.Appendices) != 1 {
			return Located{}, false // "bilaget" names one annex, and there is not exactly one
		}
		return Located{Appendix: &c.Appendices[0]}, true
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
		if p := c.part(PartChapter, itoa(a.Chapter)+a.ChapterLitra); p != nil {
			return Located{Part: p}, true
		}
		return Located{}, false
	}
	// The non-provision units are only claimed when the address names nothing else:
	// "I § 1 og i indledningen ændres …" has to keep pointing at § 1.
	if a.Paragraph == "" && a.Chapter == 0 && a.Bilag == 0 && a.Book != 0 {
		book := c.part(PartBook, itoa(a.Book))
		if book == nil {
			return Located{}, false
		}
		if a.Afsnit == 0 {
			return Located{Part: book}, true
		}
		for _, n := range book.Order {
			if n.Part != nil && n.Part.Kind == PartAfsnit && n.Part.Number == itoa(a.Afsnit) {
				return Located{Part: n.Part}, true
			}
		}
		return Located{}, false
	}
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
			return Located{Title: true, Footnote: a.Footnote}, true
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
	if a.EntryIntoForce {
		// The named series is searched and nothing else is: "§ 2, ikrafttrædelsesbestemmelse"
		// cannot satisfy itself against the law's § 2, which is the mirror image of the rule
		// above that keeps a plain "§ 2" out of this series.
		for i := range c.EntryIntoForce {
			p := &c.EntryIntoForce[i]
			if paragraphNumberMatches(p, want) {
				return c.descend(p, a)
			}
		}
		return Located{}, false
	}
	refs := c.Provisions()
	for i := range refs {
		p := refs[i].Provision
		if paragraphNumberMatches(p, want) {
			return c.descend(p, a)
		}
	}
	// A short law's entry-into-force provisions continue its own numbering — §§ 1-5, then "§ 6.
	// Loven træder i kraft …" — and an amendment cites them as the law's own. Whether they do is
	// decided on the law as published (EntryIntoForceContinues), never on a consolidation that
	// has repealed provisions since: with the law's own §§ 2-3 repealed, a restarted series "§ 1,
	// § 2" would otherwise look like it continued from nothing.
	if c.EntryIntoForceContinues {
		for i := range c.EntryIntoForce {
			p := &c.EntryIntoForce[i]
			if paragraphNumberMatches(p, want) {
				return c.descend(p, a)
			}
		}
	}
	return Located{}, false
}

// paragraphNumberMatches compares a § number on its own, not on the printed "1b": an address for
// "§ 1" means the provision numbered 1, whether or not the corpus prints it as "§ 1 b.", and the
// second branch is the other spelling, where the litra arrived glued on.
func paragraphNumberMatches(p *Provision, want string) bool {
	if strings.ToLower(p.Number) == want || p.Litra != "" && strings.ToLower(p.Number+p.Litra) == want {
		return true // "30mm": § 30 mm, its letter doubled
	}
	// The letter is the last rune, not the last byte: "9ø" is § 9 ø, and cut by bytes the letters
	// æ, ø and å never matched, so every change to § 9 ø of miljøbeskyttelsesloven was refused.
	r := []rune(want)
	return len(r) > 1 && strings.EqualFold(p.Number, string(r[:len(r)-1])) && p.Litra == string(r[len(r)-1:])
}

// part returns the first part of the given kind and number.
func (c *Content) part(kind PartKind, number string) *Part {
	if c.pinned {
		// An act cites the chapters it found: its "kapitel 5 a" is the one that was there, not
		// the new kapitel 5 a it has just put in front of it.
		for _, p := range c.Parts() {
			if p.Kind == kind && p.was == number {
				return p
			}
		}
	}
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
	// A lettered paragraph is written two ways, and the comma way arrives looking like an
	// item litra. The paragraph claims it when its own litra matches; answering with one of
	// its items would edit a different provision than the one named.
	if a.Stk == 0 && a.Nr == 0 && strings.EqualFold(p.Litra, a.Litra) && a.Litra != "" {
		return loc, true
	}
	if a.Stk == 0 {
		// "§ 92, nr. 2" names a real item: Danish acts omit "stk. 1" whenever the provision
		// has one stykke, which is how they write almost every item citation. Refusing it
		// cost 26 of BR18's 109 refusals and left its 814 numbered items uncitable. The descent
		// covers the provision's own items and every stykke's, and still refuses rather than
		// guess: when two stykker carry the same item number, nothing decides which one the
		// drafter meant, so an ambiguous number resolves to nothing and shows up in the
		// unresolved count instead of splicing text into the wrong stykke.
		var found *Item
		var st *Stykke
		for i := range p.Stykker {
			it := p.Stykker[i].itemSuffix(a.Nr, a.NrSuffix, a.Litra, c.pinned)
			if it == nil {
				continue
			}
			if found != nil && found != it {
				return Located{}, false
			}
			found, st = it, &p.Stykker[i]
		}
		if found == nil {
			return Located{}, false
		}
		loc.Stykke = st
		loc.Item = found
		return loc, true
	}
	st := p.stykke(a.Stk, c.pinned)
	if st == nil {
		return Located{}, false
	}
	loc.Stykke = st
	if a.Nr == 0 && a.Litra == "" {
		return loc, true
	}
	it := st.itemSuffix(a.Nr, a.NrSuffix, a.Litra, c.pinned)
	if it == nil {
		return Located{}, false
	}
	loc.Item = it
	return loc, true
}

// MarkContinuingEntryIntoForce records whether the entry-into-force series continues the law's
// own numbering: it starts right after the law's last own provision. A consolidated law's quoted
// series restarts at § 1 and does not.
func (c *Content) MarkContinuingEntryIntoForce() {
	maxOwn := 0
	for _, ref := range c.Provisions() {
		maxOwn = max(maxOwn, Atoi(ref.Provision.Number))
	}
	c.EntryIntoForceContinues = maxOwn > 0 && len(c.EntryIntoForce) > 0 && Atoi(c.EntryIntoForce[0].Number) == maxOwn+1
}

// hasWas reports an item that was numbered nr (with suffix) before the act being applied.
func (st *Stykke) hasWas(nr int, suffix string) bool {
	for _, it := range st.Items {
		if it.Was == nr && it.Suffix == suffix {
			return true
		}
	}
	return false
}
