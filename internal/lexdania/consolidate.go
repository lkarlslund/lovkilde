package lexdania

import (
	"strings"

	"github.com/lkarlslund/lovkilde/internal/lexdoc"
)

// Consolidation: a law as it stood on a date.
//
// Everything about what an amendment does to a provision lives in lexdoc, on the model. What lives
// here is what needs the archive and the register's markup: finding the amending acts a law names,
// deciding which of their changes are aimed at this law, and turning their markup into the
// model's edits (donation.go).
//
// There used to be a second engine here that edited the parse tree in place. It was retired once
// the model engine placed more of every measured chain, ran an order of magnitude faster, and the
// external checks (tools/goldenpair, tools/pairs) said it lost nothing the tree kept.

// Opener resolves an accession to a parsed document. Archive supplies this; the
// consolidation engine stays free of storage concerns so it can be tested on fixtures.
type Opener func(accession string) (*Document, string, bool)

// ReadAct is what consolidation reads from an amending document: whether it is a bill, and when it
// is not, its changes and entry-into-force provisions. The artifact build stores it in the act's
// model (lexdoc.Document.Changes) so the same record can be had without the XML.
func ReadAct(doc *Document, path string) *ActRecord { return readAct(doc, path) }

func readAct(doc *Document, path string) *ActRecord { return readActModel(doc, path, nil) }

// readActModel is readAct with the document's model already built, when the caller has one: the
// artifact build does, and building it a second time for the act's dates was a fifth of the build.
func readActModel(doc *Document, path string, m *lexdoc.Document) *ActRecord {
	act := &ActRecord{Path: path, Bill: doc.IsBill()}
	if !act.Bill {
		act.Edits = doc.Changes()
		if m == nil {
			m = doc.Model()
		}
		act.InForce = doc.inForceOf(m)
	}
	return act
}

// inForce reads the act's entry-into-force provisions: its own provisions saying "træder i kraft",
// not its change lists, whose quoted text may say the same of the law they amend.
func (d *Document) inForce() lexdoc.InForce { return d.inForceOf(d.Model()) }

func (d *Document) inForceOf(m *lexdoc.Document) lexdoc.InForce {
	c := m.Content
	var texts []string
	read := func(p *lexdoc.Provision) {
		t := p.OwnText()
		if strings.Contains(t, "træder i kraft") && !strings.Contains(t, "»") && !strings.Contains(t, "følgende ændring") {
			texts = append(texts, t)
		}
	}
	for _, r := range c.Provisions() {
		read(r.Provision)
	}
	for i := range c.EntryIntoForce {
		read(&c.EntryIntoForce[i])
	}
	signed, _ := ParseDate(d.Meta.DiesSigni)
	return lexdoc.ParseInForce(texts, signed)
}

// actsFrom reads acts through an Opener, without keeping them: what a fixture-driven test needs.
func actsFrom(open Opener) func(string) (*ActRecord, bool) {
	return func(accession string) (*ActRecord, bool) {
		doc, path, ok := open(accession)
		if !ok {
			return &ActRecord{Path: path}, false
		}
		return readAct(doc, path), true
	}
}

// modelEdits turns parsed changes into the model's form. The only real translation is new *text*:
// the register supplies a replacement provision as markup, and the model needs a provision, which
// is built by the same code that reads a law's own provisions.
func modelEdits(in []Edit) []lexdoc.Edit {
	out := make([]lexdoc.Edit, 0, len(in))
	for _, e := range in {
		me := lexdoc.Edit{
			Source:     e.Source,
			Number:     e.Number,
			Section:    e.Section,
			Kind:       e.Kind,
			Address:    e.Address,
			Old:        e.Old,
			New:        e.New,
			Preceding:  e.Preceding,
			Prose:      e.Prose,
			Definition: e.Definition,
			Target:     e.Target,
		}
		if e.Address.Heading && e.Kind != EditInsertProvision && e.New == "" && len(e.NewNodes) > 0 {
			// "Overskriften for § 12 ændres til: »X«" carries its new rubric as markup rather
			// than as a quoted string: a bare rubric, or the chapter or group it heads. Only the
			// rubric's words are the heading; "Kapitel 7" printed beside them is the number.
			n := e.NewNodes[0]
			if h, _ := headingText(n); h != "" {
				me.New = Squeeze(h)
			} else {
				me.New = Squeeze(DisplayText(n))
			}
		}
		// An insertion is donated whatever its address says about headings: "Efter § 7 c
		// indsættes før overskriften før § 8" names where the new text goes, not a heading to
		// edit, and skipping the donation refused every such change as carrying no content.
		if e.Kind == EditInsertProvision || e.Kind == EditReplaceProvision && (!e.Address.Heading || lexdoc.NamesHeadingAndProvision(e.Definition)) {
			d := donation(e.NewNodes)
			if (e.Address.Stk == 1 || e.Address.Stk == 0 && e.Address.Paragraph != "") && len(d.stykker) > 0 && len(d.lead) > 0 && len(d.provisions) == 0 &&
				!lexdoc.OpensWithStykkeLabel(d.lead) && d.leadProse <= len(d.prose) {
				// "§ 37, stk. 1-3, affattes således:" bringing the new stk. 1 as loose lines, with no
				// label as a first subsection has none, ahead of "Stk. 2." and "Stk. 3.".
				d.stykker = append([][]lexdoc.Segment{d.lead}, d.stykker...)
				d.prose = d.prose[d.leadProse:]
			}
			me.NewProvisions, me.NewStykker, me.NewProse = d.provisions, d.stykker, d.prose
			me.NewParts, me.NewText, me.NewAppendix, me.Skip = d.parts, d.text, d.appendix, d.skip
		}
		if e.Kind == EditReplaceProvision && e.Address.Heading && !lexdoc.NamesHeadingAndProvision(e.Definition) && len(e.NewNodes) > 0 {
			// "Overskriften til kapitel 6 ophæves, og i stedet indsættes: Kapitel 6 Henstand … § 17.
			// … § 18. …": the new heading comes with the chapter's new provisions, and both are
			// the change's.
			if d := donation(e.NewNodes); len(d.parts) == 1 && len(d.provisions) == 0 && lexdoc.PartHoldsProvisions(d.parts[0]) {
				me.NewParts = d.parts
				me.New = d.parts[0].Heading
			}
		}
		lexdoc.CompleteFromDefinition(&me)
		if me.Kind == EditInsertProvision || me.Kind == EditReplaceProvision {
			lexdoc.PromoteStykker(&me)
		}
		out = append(out, me)
	}
	return out
}

// isProvisionElement reports a top-level element that starts a new paragraph of the law rather
// than continuing the one before it. The register uses four spellings for the same thing,
// depending on where in an amending act the paragraph sits.
func isProvisionElement(n *Node) bool {
	switch n.Name {
	case "Paragraf", "TilParagraf", "AendringCentreretParagraf", "IkraftCentreretParagraf":
		return true
	}
	return false
}

// Clone deep-copies a subtree. Consolidation must not mutate the parsed document:
// the same Document is handed to every view of every date, and a mutated cache would
// make results depend on call order.
func (n *Node) Clone() *Node {
	c := &Node{
		Name:  n.Name,
		Space: n.Space,
		Attrs: make(map[string]string, len(n.Attrs)),
		Runs:  append([]string(nil), n.Runs...),
		Elems: make([]*Node, len(n.Elems)),
	}
	for k, v := range n.Attrs {
		c.Attrs[k] = v
	}
	for i, e := range n.Elems {
		c.Elems[i] = e.Clone()
	}
	return c
}

// Clone deep-copies a document, which is what every view operates on.
func (d *Document) Clone() *Document {
	c := &Document{
		Root:           d.Root.Clone(),
		SchemaLocation: d.SchemaLocation,
		Meta:           d.Meta,
		// Provenance has to survive the copy: every consolidated view is a Clone, and a
		// view that forgot which text was HTML-derived would report recovered text as
		// though it were the register's own.
		Source:   d.Source,
		Sidecar:  d.Sidecar,
		warnings: append([]string(nil), d.warnings...),
	}
	meta := c.Root.Elem("Meta")
	if meta != nil {
		c.Meta = parseMeta(meta)
	}
	for _, n := range c.Root.Elems {
		if n.Name != "Meta" {
			c.Body = append(c.Body, n)
		}
	}
	return c
}

// donorClone copies proposed text from an amending act into the law being
// consolidated, and drops localId on the way.
//
// localId numbers an element *inside the document that carries it*: an act inserting a
// new § 3 writes it as <Paragraf localId="9"> because 9 is where it sits in the act's
// own sequence. Spliced into the law unchanged, newSection reads that 9 as the
// provision's number, so the new § 3 becomes unaddressable and every later act citing
// § 3 fails to find it. The id also collides with the target law's own localIds, which
// is what <Ref> elements resolve against, so keeping it would let a reference in the
// law point into inserted text from some other act.
//
// Numbering therefore comes from the printed Explicatus, which is what the corpus
// agrees with anyway: localId and the printed § number match in 20,410 of the 20,417
// sampled provisions that carry both.
func donorClone(n *Node) *Node {
	if n == nil {
		return nil
	}
	c := n.Clone()
	c.dropLocalIDs()
	return c
}

func (n *Node) dropLocalIDs() {
	delete(n.Attrs, "localId")
	for _, c := range n.Elems {
		c.dropLocalIDs()
	}
}
