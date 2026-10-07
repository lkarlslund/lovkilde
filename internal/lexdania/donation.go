package lexdania

import (
	"regexp"
	"strings"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// donated is what one change brings with it, sorted into the model's shapes.
type donated struct {
	provisions []*lexdoc.Provision
	stykker    [][]lexdoc.Segment
	prose      []string
	parts      []*lexdoc.Part
	text       []lexdoc.Segment // the whole run as one block, for a preamble or a title
	lead       []lexdoc.Segment // loose lines ahead of the first subsection: an unlabelled stk. 1
	leadProse  int              // how many of prose they are
	appendix   *lexdoc.Appendix
	skip       string
}

// donation says what an amending act actually brings with it. The register's "indsættes" and
// "ændres til" instructions carry a run of elements, and the run is rarely what the change's kind
// name suggests: over 90 sampled amending acts, 75 provision-level changes brought a subsection,
// 38 brought loose lines, 24 brought a paragraph, and a few brought a chapter or a rubric. Over
// every fifth law since 2008, the shapes beyond those were a preamble (189), a chapter (~150), a
// rubric on its own or heading a group (~200), a title (26) and a footnote (49).
//
// Every node is classified, not just the first. The tree reader inserted NewNodes[0] and ignored
// the rest, which quietly dropped 89 elements over those 142 changes — law that consolidation
// reported as applied.
//
// The shapes this model cannot place are reported in the model's own words, never the
// publisher's element names: what comes back from the mirror must not need the schema to be
// understood.
func donation(nodes []*Node) donated {
	var d donated
	if len(nodes) == 0 {
		return d
	}
	b := &contentBuilder{
		watch:     map[*Node]bool{},
		spanStart: map[*Node]int{},
		spanEnd:   map[*Node]int{},
	}
	// last is the provision this change has assembled so far. Subsections and loose lines that
	// follow it in the act's own order belong to it, not to a side bucket. open is the chapter or
	// group the change has opened: provisions after a heading print under it.
	var last *lexdoc.Provision
	var open *lexdoc.Part
	stk := false // the last element read was a subsection this change brings on its own
	place := func(p *lexdoc.Provision) {
		if open != nil {
			open.Order = append(open.Order, lexdoc.Node{Provision: p})
			return
		}
		d.provisions = append(d.provisions, p)
	}
	for _, n := range nodes {
		d.text = b.render(d.text, n, true)
		switch {
		case n.Name == "Stk" && strings.HasPrefix(strings.TrimSpace(n.Elem("Explicatus").TextOrNull()), "§"):
			// "§ 56 b." marked up as a subsection: the act's markup, not its meaning.
			p := b.provision(donorClone(n))
			place(&p)
			last = &p
		case n.Name == "Stk":
			segs := b.render(nil, donorClone(n), true)
			if last == nil {
				d.stykker = append(d.stykker, segs)
				stk = true
				continue
			}
			// The subsection follows a paragraph this change already brought, so it belongs to
			// that paragraph's provision. Keeping it in a side bucket is what let a rewritten
			// § 21 arrive as one sentence: the replacement path reads the provision and leaves
			// the bucket where the subsection was born.
			nr := lexdoc.Atoi(lexdoc.FirstNumber(n.Attrs["localId"]))
			last.AppendStykke(segs, nr)
		case isProvisionElement(n):
			p := b.provision(donorClone(n))
			place(&p)
			last = &p
		case concepts[n.Name].kind == conceptPart:
			// A chapter, afsnit or group, built by the same walk that reads a law's own: the
			// provisions inside it are the law's provisions from now on.
			var order []lexdoc.Node
			b.walk(&order, &Node{Elems: []*Node{donorClone(n)}, Runs: []string{"", ""}}, walkCtx{})
			// The walk clears the span maps after each provision it reads; a provision this
			// change brings after the part needs them back.
			b.watch, b.spanStart, b.spanEnd = map[*Node]bool{}, map[*Node]int{}, map[*Node]int{}
			for _, o := range order {
				if o.Part != nil {
					d.parts = append(d.parts, o.Part)
					open = innermostOpen(o.Part)
				}
			}
			last = nil
		case n.Name == "Rubrica" && !(reUnitLabel.MatchString(DisplayText(n)) && len([]rune(Squeeze(DisplayText(n)))) < 20):
			// A rubric on its own heads what follows it: a group heading, unless it labels a
			// chapter this change opened without one.
			text := Squeeze(DisplayText(n))
			if open != nil && open.Heading == "" && len(open.Order) == 0 {
				open.Heading = text
				continue
			}
			g := &lexdoc.Part{Kind: lexdoc.PartGroup, Heading: text}
			d.parts = append(d.parts, g)
			open, last = g, nil
		case (n.Name == "Explicatus" || n.Name == "Rubrica") && reUnitLabel.MatchString(DisplayText(n)) && len([]rune(Squeeze(DisplayText(n)))) < 20:
			// "Kapitel 4 a" printed loose, ahead of the rubric and provisions that belong to it.
			printed := Squeeze(DisplayText(n))
			kind := lexdoc.PartChapter
			if strings.HasPrefix(strings.ToLower(printed), "afsnit") {
				kind = lexdoc.PartAfsnit
			}
			ch := &lexdoc.Part{Kind: kind, Printed: printed, Number: lexdoc.NumberWithLitra(printed)}
			d.parts = append(d.parts, ch)
			open, last = ch, nil
		case n.Name == "Indledning", n.Name == "Titel":
			// The text is the block above; these name what it replaces, which the address does.
		case n.Name == "Bilag":
			ab := &contentBuilder{}
			ab.appendix(donorClone(n))
			if len(ab.c.Appendices) > 0 {
				ap := ab.c.Appendices[0]
				d.appendix = &ap
			}
		case n.Name == "Nota":
			// A footnote's text is the block above; the address says which note it is.
		default:
			if text := Squeeze(DisplayText(n)); text != "" {
				if m := reLooseProvisionLabel.FindStringSubmatch(text); m != nil && open != nil && last == nil && n.Name == "Exitus" {
					// "Efter § 9 a indsættes: Kompensation for CO2-afgift § 9 b. Elproducenter …
					// Stk. 2. …" with no provision element around § 9 b: the rubric opened a
					// group, and the text under it is the provision its label names. It went
					// to loose lines and loose subsections, and the group arrived empty.
					segs := b.render(nil, donorClone(n), true)
					label := Squeeze(m[0])
					if len(segs) > 0 && Squeeze(segs[0].Text) != label {
						first := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(segs[0].Text), strings.TrimSpace(m[0])))
						segs = append([]lexdoc.Segment{{Text: label, Break: true}, {Text: first, Break: true}}, segs[1:]...)
					}
					p := lexdoc.Provision{Number: m[1], Litra: strings.ToLower(m[2]), Printed: label, Segments: segs}
					p.Normalise()
					place(&p)
					last = &p
					continue
				}
				if last != nil {
					last.Append(text)
					continue
				}
				if len(d.stykker) == 0 && len(d.provisions) == 0 && len(d.parts) == 0 {
					d.lead = b.render(d.lead, donorClone(n), true)
					d.leadProse++
				}
				if stk {
					// "Stk. 2. … udgør et månedligt beløb på" followed by its conditions "1) …",
					// "2) …" as elements of their own: they are the subsection's, and a
					// replacement of it without them dropped them from the law.
					k := len(d.stykker) - 1
					d.stykker[k] = b.render(d.stykker[k], donorClone(n), true)
					continue
				}
				d.prose = append(d.prose, text)
			}
		}
		if n.Name != "Stk" {
			stk = false
		}
	}
	d.provisions, d.parts = lexdoc.FlattenGroups(d.provisions, d.parts)
	return d
}

// innermostOpen is the part a loose provision following a donated part belongs to: the last
// group inside the last chapter, or the chapter itself.
func innermostOpen(p *lexdoc.Part) *lexdoc.Part {
	for {
		if len(p.Order) == 0 || p.Order[len(p.Order)-1].Part == nil {
			return p
		}
		p = p.Order[len(p.Order)-1].Part
	}
}

// reLooseProvisionLabel is a provision's label opening a loose block of text: "§ 9 b.".
var reLooseProvisionLabel = regexp.MustCompile(`^§\s*([0-9]+)\s*([a-zæøå])?\s*\.`)

// reUnitLabel is a loose label for a chapter or an afsnit.
var reUnitLabel = regexp.MustCompile(`(?i)^\s*(kapitel|afsnit)\s+[0-9ivxlcdm]`)
