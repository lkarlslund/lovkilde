package lexdania

import (
	"strings"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// This file is the XML front end for lexdoc.Content: the one place that knows which
// publisher element means which mirror concept. Nothing here derives a provision number,
// resolves an address or renders text — those are the model's methods — and nothing it
// produces carries an element name. The mapping is a lookup table, which is what lets the
// HTML front end bring its own vocabulary to the same concepts instead of forging XML to be
// understood.

// proposedText is the one unmapped word whose content is never walked through: provisions
// inside an amending act's replacement text carry their own localId numbers, and emitting
// them as this document's provisions would make an amendment appear to contain the law it is
// amending, which corrupts every later address lookup.
const proposedText = "proposed text"

type conceptKind int

const (
	// conceptContainer is zero so that a name absent from the table means "walk through
	// it", which is the safe default: an unknown element that turns out to hold provisions
	// still reaches the model.
	conceptContainer conceptKind = iota
	conceptUnmapped
	conceptProvision
	conceptPart
	conceptPreamble
	conceptTitle
	conceptAppendix
	conceptEntryIntoForce
	conceptEntryIntoForceContainer
	conceptSignature
)

type concept struct {
	kind conceptKind
	part lexdoc.PartKind
	// word is the mirror's name for the content: for unmapped leaves, what to count and
	// keep; for containers, what to call the prose that sits directly in them.
	word string

	// noText records a count without the text, for content the model already holds
	// elsewhere. Metadata is the case: identity fields carry it, and copying the whole
	// header into the unmapped pile would double the artifact to report what is known.
	noText bool
}

// concepts maps the publisher's element names onto mirror concepts. A name outside this map
// is a container to walk through, or content to count — never something to drop.
var concepts = map[string]concept{
	// Provisions in the document's own numbering series. The centreret variants are how
	// amending and entry-into-force documents frame their provisions.
	"Paragraf":                  {kind: conceptProvision},
	"TilParagraf":               {kind: conceptProvision},
	"AendringCentreretParagraf": {kind: conceptProvision},
	"IkraftCentreretParagraf":   {kind: conceptEntryIntoForce},

	// Grouping units. The old flat section list dropped named groups, which is why a
	// renderer could not print them.
	"Kapitel":                  {kind: conceptPart, part: lexdoc.PartChapter},
	"KapitelOverskrift":        {kind: conceptPart, part: lexdoc.PartChapter},
	"Afsnit":                   {kind: conceptPart, part: lexdoc.PartAfsnit},
	"ParagrafGruppe":           {kind: conceptPart, part: lexdoc.PartGroup},
	"TilParagrafGruppe":        {kind: conceptPart, part: lexdoc.PartGroup},
	"ParagrafGruppeOverskrift": {kind: conceptPart, part: lexdoc.PartGroup},

	// Non-provision parts of an act that amendments nevertheless target.
	"Indledning": {kind: conceptPreamble},
	"Titel":      {kind: conceptTitle},
	// Bilag is the annex; BilagIndhold is the wrapper around its content and appears at the
	// top level of bills on its own, where it is a section of text rather than a numbered
	// annex. Turning it into an annex invented unnumbered ones.
	"Bilag":             {kind: conceptAppendix},
	"BilagIndhold":      {kind: conceptContainer, word: "annex text"},
	"Ikraft":            {kind: conceptEntryIntoForceContainer},
	"UnderskriftGruppe": {kind: conceptSignature},

	// Proposed text: replacement provisions an amending act embeds. They carry their own
	// localId numbers, so reading them as this document's provisions would make an
	// amendment appear to contain the law it amends. Counted, and kept as text until the
	// amendment stage gives them a concept of their own.
	"AendringNyTekst":      {kind: conceptUnmapped, word: "proposed text"},
	"AendringsNummer":      {kind: conceptUnmapped, word: "proposed text"},
	"Aendring":             {kind: conceptUnmapped, word: "proposed text"},
	"AendringsBeskrivelse": {kind: conceptUnmapped, word: "proposed text"},
	"AendringsForslag":     {kind: conceptUnmapped, word: "proposed text"},
	"AendringAktion":       {kind: conceptUnmapped, word: "proposed text"},
	"AendringDefinition":   {kind: conceptUnmapped, word: "proposed text"},

	// Content the model has no concept for yet, in the mirror's own words.
	//
	// Meta is the identity block in the publisher's own files, and counting it without its text is
	// not tidiness: the whole header is already held as identity, and once the answer started
	// showing what the model has no concept for, every whole-document answer printed the accession,
	// the title and the status again as content — measured in 200 of 200 sampled documents.
	"Meta":                     {kind: conceptUnmapped, word: "metadata", noText: true},
	"Table":                    {kind: conceptUnmapped, word: "table"},
	"DokumentHoved":            {kind: conceptUnmapped, word: "metadata", noText: true},
	"Bemaerkninger":            {kind: conceptUnmapped, word: "remarks"},
	"BemaerkningIndhold":       {kind: conceptUnmapped, word: "remarks"},
	"BilagTilBemaerkninger":    {kind: conceptUnmapped, word: "remarks"},
	"BemaerkningerTilParagraf": {kind: conceptUnmapped, word: "remarks"},
	"AlmindeligeBemaerkninger": {kind: conceptUnmapped, word: "remarks"},
	"FremsaettelsesIndledning": {kind: conceptUnmapped, word: "presentation"},
	"FremsaettelsesTekst":      {kind: conceptUnmapped, word: "presentation"},
	"SkriftligFremsaettelse":   {kind: conceptUnmapped, word: "presentation"},
	"BeretningsTekst":          {kind: conceptUnmapped, word: "presentation"},
	"BetaenkningsTekst":        {kind: conceptUnmapped, word: "presentation"},
	"Resume":                   {kind: conceptUnmapped, word: "summary"},
	"Udkast":                   {kind: conceptUnmapped, word: "draft"},
	"Traktat":                  {kind: conceptUnmapped, word: "treaty"},
	"Segl":                     {kind: conceptUnmapped, word: "seal"},
	"Hymne":                    {kind: conceptUnmapped, word: "hymn"},
	"Rykningsklausul":          {kind: conceptUnmapped, word: "promulgation clause"},
	"EditorialNote":            {kind: conceptUnmapped, word: "editorial note"},
	"Afslutning":               {kind: conceptUnmapped, word: "closing"},
}

// contentOf builds the model's content from a parsed tree.
func contentOf(root *Node) lexdoc.Content {
	b := &contentBuilder{}
	b.walk(&b.c.Order, root, walkCtx{})
	b.c.Unmapped = b.unmapped
	return b.c
}

type contentBuilder struct {
	c        lexdoc.Content
	unmapped []lexdoc.Unmapped

	// titleSeen guards the document's own title: the first one in document order is the
	// document title, and a later one belongs to an annex.
	titleSeen bool

	// Span bookkeeping. A provision renders its text once; a stykke or item registers the
	// node whose text it wants a range for, and the renderer reports where it landed in the
	// shared slice. Storing item text twice would have been simpler and would have doubled
	// the artifact.
	watch     map[*Node]bool
	spanStart map[*Node]int
	spanEnd   map[*Node]int

	// labelled records the child element whose words became a parent's heading, so that the
	// same words are not then walked again as content. A chapter that prints its own heading
	// element was both a second chapter and a paragraph of prose; a provision's rubric was
	// printed inside its own text as well as above it.
	labelled map[*Node]*Node
}

// walk classifies the children of n, recursing through containers. Structure is not stable
// across document types — provisions appear under DokumentIndhold, under ParagrafGruppe and
// directly under Dokument depending on the document — so the walk goes all the way down
// rather than trusting a shape, which is what the section list always did.
// walkCtx carries what the walk already knows about where it is, because two of the
// publisher's containers change what their children mean: a <Paragraf> inside <Ikraft> is
// entry-into-force material, and a <BilagIndhold> inside a <Bilag> is that annex's content
// rather than a second annex.
type walkCtx struct {
	inForce bool
	inAnnex bool
}

func (b *contentBuilder) walk(order *[]lexdoc.Node, n *Node, ctx walkCtx) {
	for _, c := range n.Elems {
		if b.labelled[n] == c {
			continue // its words are this element's heading, not a piece of content
		}
		con, known := concepts[c.Name]
		if !known {
			con = concept{kind: conceptContainer}
		}
		switch con.kind {
		case conceptProvision, conceptEntryIntoForce:
			b.watch = map[*Node]bool{}
			b.spanStart = map[*Node]int{}
			b.spanEnd = map[*Node]int{}
			p := b.provision(c)
			b.watch, b.spanStart, b.spanEnd = nil, nil, nil
			if ctx.inForce || con.kind == conceptEntryIntoForce {
				b.c.EntryIntoForce = append(b.c.EntryIntoForce, p)
				continue
			}
			*order = append(*order, lexdoc.Node{Provision: &p})

		case conceptEntryIntoForceContainer:
			// The wrapper is not content. Its provisions are, and they are <Paragraf>
			// elements like any other — 1,030 of them in the corpus — which is exactly why
			// the context has to travel down the walk rather than be read off the element
			// name. Their numbering restarts at one, so they must never be addressable as
			// the law's own § 1.
			b.walk(order, c, walkCtx{inForce: true})

		case conceptPart:
			part := &lexdoc.Part{Kind: con.part}
			part.Number = lexdoc.FirstNumber(c.Attrs["localId"])
			part.Printed = strings.TrimSpace(c.Elem("Explicatus").TextOrNull())
			if con.part == lexdoc.PartAfsnit {
				// An afsnit carries id="Afs1" and no localId, and prints "Afsnit I": the
				// printed roman numeral is the only number the element has, and it is what
				// an amendment cites. Chapters are not given the same treatment, because
				// reading a number out of a heading is a different claim and belongs with
				// the HTML payload, where it can be measured against paired XML.
				if part.Number == "" {
					part.Number = lexdoc.FirstNumber(part.Printed)
				}
				if r := lexdoc.PrintedRoman(part.Printed); r != "" {
					part.Number = r
				}
			}
			part.Heading = b.label(c)
			b.walk(&part.Order, c, ctx)
			*order = append(*order, lexdoc.Node{Part: part})

		case conceptPreamble:
			b.c.Preamble = b.render(b.c.Preamble, c, true)
		case conceptSignature:
			b.c.Signature = b.render(b.c.Signature, c, true)
		case conceptTitle:
			if b.titleSeen {
				b.record(lexdoc.Unmapped{Concept: "annex title", Text: DisplayText(c)})
				continue
			}
			b.titleSeen = true
			b.c.Title = b.render(b.c.Title, c, true)
		case conceptAppendix:
			if ctx.inAnnex {
				// A Bilag nested inside a Bilag is the annex's own content, not a second
				// annex.
				b.walk(order, c, ctx)
				continue
			}
			b.appendix(c)

		case conceptUnmapped:
			// A leaf's text is kept whole. A container that merely happens to be named here
			// is still walked, or the provisions inside it would disappear: this is how a
			// committee report whose amendments sit several levels deep still reaches the
			// model. Proposed replacement text is the exception that earns its name.
			if con.word == proposedText || !knownConceptBelow(c) {
				b.recordUnmapped(con, c)
				continue
			}
			b.captureProse(c, con.word)
			b.walk(order, c, ctx)

		default: // container
			b.captureProse(c, con.word)
			b.walk(order, c, ctx)
		}
	}
}

// appendix records an annex together with whatever structure it carries: annexes hold
// numbered provisions as often as they hold tables and prose. They are stored apart from the
// reading order because that is how they are addressed ("bilag 3") and how they print: after
// the act, each with its own numbering.
func (b *contentBuilder) appendix(n *Node) {
	ap := &lexdoc.Appendix{}
	ap.Number = lexdoc.FirstNumber(n.Attrs["localId"])
	ap.Heading = b.label(n)
	ap.Label = strings.TrimSpace(n.Elem("Explicatus").TextOrNull())
	if ap.Label == "" {
		ap.Label = strings.TrimSpace(n.Elem("Rubrica").TextOrNull())
	}
	if ap.Number == "" {
		ap.Number = lexdoc.FirstNumber(ap.Label)
	}
	b.walk(&ap.Order, n, walkCtx{inAnnex: true})
	// Whatever the annex holds that became no concept is the annex's own text: an annex with
	// no provisions inside it is prose and tables, and that text has to live somewhere.
	if len(ap.Order) == 0 {
		ap.Segments = b.render(nil, n, true)
	}
	b.c.Appendices = append(b.c.Appendices, *ap)
}

// provision builds one provision: its identity, its text, and the spans of its subsections.
func (b *contentBuilder) provision(n *Node) lexdoc.Provision {
	p := lexdoc.Provision{
		Printed: strings.TrimSpace(n.Elem("Explicatus").TextOrNull()),
		Heading: b.label(n),
	}
	p.Number = lexdoc.FirstNumber(n.Attrs["localId"])
	if p.Number == "" {
		p.Number = lexdoc.FirstNumber(p.Printed)
	}
	p.Litra = lexdoc.ParaLitra(p.Printed)

	var segs []lexdoc.Segment
	segs = appendRun(segs, n.Runs[0])
	for i, c := range n.Elems {
		if b.labelled[n] == c {
			segs = appendRun(segs, n.Runs[i+1])
			continue // the rubric is this provision's heading, not the first line of its text
		}
		if c.Name == "Stk" {
			items := numberedItems(c)
			for _, it := range items {
				b.watch[it.node] = true
			}
			b.watch[c] = true
			segs = b.render(segs, c, true)
			st := lexdoc.Stykke{Span: lexdoc.Span{From: b.spanStart[c], To: b.spanEnd[c]}}
			st.Nr = lexdoc.Atoi(lexdoc.FirstNumber(c.Attrs["localId"]))
			if st.Nr == 0 {
				// Position is the numbering: Stk elements carry an id but almost never a
				// localId, so "stk. 2" can only mean "the second Stk element".
				st.Nr = len(p.Stykker) + 1
			}
			nrIdx := 0
			var lastParent *Node
			for _, it := range items {
				if it.parent != lastParent {
					nrIdx = 0
					lastParent = it.parent
				}
				nrIdx++
				end, ok := b.spanEnd[it.node]
				if !ok {
					continue
				}
				item := lexdoc.Item{Span: lexdoc.Span{From: b.spanStart[it.node], To: end}}
				item.Nr, item.Litra = lexdoc.ItemMarker(it.node.Elem("Explicatus").TextOrNull())
				if item.Nr == 0 && item.Litra == "" {
					// The Explicatus of a numbered paragraph often carries the marker
					// ("2)"), so an explicit one wins over position.
					item.Nr = nrIdx
				}
				st.Items = append(st.Items, item)
			}
			p.Stykker = append(p.Stykker, st)
			segs = appendRun(segs, n.Runs[i+1])
			continue
		}
		segs = b.render(segs, c, true)
		segs = appendRun(segs, n.Runs[i+1])
	}
	p.Segments = segs
	return p
}

// render appends the subtree's text to out, marking block boundaries and reporting where any
// watched node landed.
//
// The rules mirror the reader's own text rendering exactly, which is what lets the model
// show the same characters a reader sees today: every run is trimmed, whitespace-only runs
// contribute nothing, and a block boundary supplies the separation instead. Whitespace-only
// runs are only droppable because every element below a block element is treated as a block,
// so a dropped run always sits beside a recorded break — measured over the corpus, no inline
// element ever abuts a letter (0 occurrences in 1,500 documents), so no word is split either
// way.
func (b *contentBuilder) render(out []lexdoc.Segment, n *Node, block bool) []lexdoc.Segment {
	watched := b.watch != nil && b.watch[n]
	if watched {
		b.spanStart[n] = len(out)
	}
	out = appendRun(out, n.Runs[0])
	for i, c := range n.Elems {
		if b.labelled[n] == c {
			out = appendRun(out, n.Runs[i+1])
			continue // the provision's rubric is its heading, not the first line of its text
		}
		if c.Name == "Nota" {
			// A footnote's text stays where it sits and the segment that begins it carries
			// the note's id, so the reference and its note cannot drift apart.
			from := len(out)
			out = b.render(out, c, block)
			markNote(&out, from, c.ID())
		} else {
			out = b.render(out, c, block)
		}
		out = appendRun(out, n.Runs[i+1])
	}
	if block || blockElements[n.Name] {
		markBreak(&out)
	}
	if watched {
		b.spanEnd[n] = len(out)
	}
	return out
}

// captureProse keeps the prose that sits directly inside a container, between its children.
//
// A container's own text runs belong to no provision, preamble or part, so walking past them
// would drop them: a committee report's body text, or a presentation's paragraphs, are the
// document's content even though nothing in the model addresses them. They are counted under
// the container's concept so the count says what was not understood, and the text is kept so
// that a later stage can promote it to a concept without re-reading the archive.
func (b *contentBuilder) captureProse(n *Node, word string) {
	if word == "" {
		word = "prose"
	}
	text := Squeeze(strings.Join(n.Runs, " "))
	if text == "" {
		return
	}
	b.record(lexdoc.Unmapped{Concept: word, Text: text})
}

// recordUnmapped keeps content the model cannot express yet, counted and with its text.
func (b *contentBuilder) recordUnmapped(con concept, n *Node) {
	word := con.word
	if word == "" {
		word = "unclassified"
	}
	if con.noText {
		b.record(lexdoc.Unmapped{Concept: word})
		return
	}
	if knownConceptBelow(n) {
		// A subtree holding provisions belongs to them, not to the pile. Proposed text is
		// the exception it is named for: its provisions are somebody else's.
		if word != "proposed text" {
			return
		}
	}
	text := DisplayText(n)
	if text == "" {
		b.record(lexdoc.Unmapped{Concept: word})
		return
	}
	b.record(lexdoc.Unmapped{Concept: word, Text: text})
}

func (b *contentBuilder) record(u lexdoc.Unmapped) {
	b.unmapped = append(b.unmapped, u)
}

// appendRun appends a source run if it carries text.
func appendRun(out []lexdoc.Segment, run string) []lexdoc.Segment {
	if t := strings.TrimSpace(run); t != "" {
		return append(out, lexdoc.Segment{Text: t})
	}
	return out
}

// markBreak records that a block of text ends here. Marking the last segment is equivalent
// to emitting a newline of its own: display text collapses any whitespace run to one space,
// and a rendering that respects breaks gets the paragraph separation it needs either way.
func markBreak(out *[]lexdoc.Segment) {
	if n := len(*out); n > 0 {
		(*out)[n-1].Break = true
		return
	}
	*out = append(*out, lexdoc.Segment{Break: true})
}

// markNote tags the segment that begins a footnote's text.
func markNote(out *[]lexdoc.Segment, from int, id string) {
	if id == "" {
		return
	}
	if from < len(*out) {
		(*out)[from].Note = id
		return
	}
	*out = append(*out, lexdoc.Segment{Note: id})
}

// headingText reads the rubric printed above an element, which is what an amendment means by
// "overskriften for § 12".
// label reads the rubric printed above n and records which element it came from, so the walk can
// leave that element alone: its words are the parent's heading now, not content of their own.
func (b *contentBuilder) label(n *Node) string {
	t, h := headingText(n)
	if b.labelled == nil {
		b.labelled = map[*Node]*Node{}
	}
	b.labelled[n] = h
	return t
}

// headingText reads the rubric and reports the element it read it from.
func headingText(n *Node) (string, *Node) {
	// DisplayText, not TextOrNull: the register puts a heading's words in Rubrica>Linea>Char, so
	// the element's own text is only the whitespace between its children. Reading direct text
	// made every rubric come back empty, which cost the model the text itself and left
	// "Overskriften før § 3 ophæves" with nothing to remove.
	for _, name := range []string{"Rubrica", "ParagrafOverskrift", "KapitelOverskrift"} {
		h := n.Elem(name)
		if h == nil {
			continue
		}
		if t := strings.TrimSpace(DisplayText(h)); t != "" {
			return t, h
		}
	}
	return "", nil
}

// knownConceptBelow reports whether a subtree contains anything the model addresses. It is
// what lets the walk be structure-agnostic without double-counting the same text: a container
// holding provisions belongs to them, and one holding nothing does not belong to the pile.
func knownConceptBelow(n *Node) bool {
	found := false
	n.Descendants(func(c *Node) {
		if found {
			return
		}
		switch concepts[c.Name].kind {
		case conceptProvision, conceptPart, conceptPreamble, conceptTitle,
			conceptAppendix, conceptEntryIntoForce, conceptEntryIntoForceContainer,
			conceptSignature:
			found = true
		}
	})
	return found
}
