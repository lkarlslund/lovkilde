// Package lexdania maps the canonical Lex Dania Open XML that
// retsinformation.dk serves at /eli/.../xml into Go structs.
//
// # Why the body is a generic tree
//
// The schema is large and only partly understood: a 1,812-document sample of the
// archive contains 131 distinct element names, and document types range from a
// 1.4 KB law to an 88 MB parliamentary folio. Two designs were possible:
//
//   - hand-write a struct per element, or
//   - decode everything into one generic Node and project typed views from it.
//
// The second was chosen deliberately. Struct-per-element silently drops any
// element the author did not know about, and the failure mode is invisible: Go's
// encoding/xml leaves fields empty rather than reporting "I saw something here".
// With a generic tree, nothing can be silently lost, unknown elements stay
// inspectable, and the typed views we do have (Meta, Sections, Amendments) are
// projections that can be extended without touching the parser.
//
// This is why the package also reports ElementStats: it makes "did we map
// everything" a measurable question instead of an aspiration.
package lexdania

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// Node is one XML element, decoded without needing to know the schema.
//
// Mixed content is preserved through an interleave invariant:
//
//	len(Text) == len(Elems) + 1
//
// Runs[0] is the character data before Elems[0], Runs[1] sits between Elems[0]
// and Elems[1], and so on. This matters more than it looks: Danish amendment text
// interleaves formatting inside a sentence, e.g.
//
//	<Linea><Char>I </Char><Char formaChar="Italic">§ 267, stk. 1,</Char><Char> ændres …</Char></Linea>
//
// and quote-matching an amendment requires the sentence to come back out in the
// original order. Element-only or text-only representations both lose that, which
// is why Node is not simply {Name, Attrs, Children, Text}.
type Node struct {
	Name  string
	Space string            // XML namespace prefix, empty in practice for this schema
	Attrs map[string]string // all attributes, including id/localId/formaChar

	Runs  []string // text runs; len == len(Elems)+1, see the interleave invariant
	Elems []*Node
}

// Attr returns an attribute value, or "" when absent. The second result reports
// presence, which callers need because "" is a meaningful value in this schema
// (e.g. <Concerns id=""/> has appeared in the wild).
func (n *Node) Attr(name string) (string, bool) {
	v, ok := n.Attrs[name]
	return v, ok
}

// ID returns the id= attribute, which is how elements in this schema become
// addressable anchors that other elements point at with REFid=.
func (n *Node) ID() string { return n.Attrs["id"] }

// LocalID returns the localId= attribute as text. Paragraphs, chapters and
// sections carry a sequence number in it (e.g. <Paragraf localId="268">), which is
// the join key between an amendment's prose ("§ 267, stk. 1") and the target tree.
func (n *Node) LocalID() string { return n.Attrs["localId"] }

// ElemsNamed returns the direct children with the given element name, in order.
func (n *Node) ElemsNamed(name string) []*Node {
	var out []*Node
	for _, c := range n.Elems {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// Elem returns the first direct child named name, or nil.
func (n *Node) Elem(name string) *Node {
	for _, c := range n.Elems {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Descendants walks the subtree in document order, including n itself.
func (n *Node) Descendants(visit func(*Node)) {
	visit(n)
	for _, c := range n.Elems {
		c.Descendants(visit)
	}
}

// FindNamed returns every descendant with the given name, in document order.
func (n *Node) FindNamed(name string) []*Node {
	var out []*Node
	n.Descendants(func(c *Node) {
		if c.Name == name {
			out = append(out, c)
		}
	})
	return out
}

// Text joins the node's own text runs, in order, so prose split across formatting
// elements comes back as one readable string. It does not include text inside
// child elements — use PlainText for that.
func (n *Node) Text() string { return strings.Join(n.Runs, "") }

// PlainText renders the subtree as readable text: text runs in order, with a line
// break after each block-level element and cell separators inside tables.
//
// The block list was derived from the element inventory rather than guessed:
// these are the elements the schema uses to hold a run of prose or a structural
// unit, as opposed to inline formatting (Char) or table cells.
func (n *Node) PlainText() string {
	if n == nil {
		return "" // an element the document does not have, as TextOrNull reads it
	}
	var b strings.Builder
	var render func(cur *Node, block bool)
	render = func(cur *Node, block bool) {
		isBlock := block || blockElements[cur.Name]
		// Runs[0] precedes Elems[0]; emit it, then each child followed by the
		// text run that came after it.
		b.WriteString(strings.TrimSpace(cur.Runs[0]))
		for i, c := range cur.Elems {
			render(c, isBlock)
			b.WriteString(strings.TrimSpace(cur.Runs[i+1]))
		}
		if isBlock {
			b.WriteString("\n")
		}
	}
	render(n, false)
	return collapseBlankLines(b.String())
}

// blockElements are rendered with a trailing newline in PlainText.
var blockElements = map[string]bool{
	"Linea": true, "Exitus": true, "Indentatio": true, "Explicatus": true,
	"Rubrica": true, "Nota": true, "Index": true,

	"Paragraf": true, "Stk": true, "Kapitel": true, "Afsnit": true,
	"ParagrafGruppe": true, "TilParagraf": true, "TilParagrafGruppe": true,
	"AendringsNummer": true, "Aendring": true, "AendringDefinition": true,
	"AendringAktion": true, "AendringNyTekst": true,
	"AendringCentreretParagraf": true, "IkraftCentreretParagraf": true,
	"AendringsForslag": true, "AendringsBeskrivelse": true,

	"Titel": true, "TitelGruppe": true, "TitelGrupper": true, "UnderTitel": true,
	"UnderskriftGruppe": true, "UnderskriftStedDato": true, "Underskriver": true,
	"UnderskriverTekst": true, "Bilag": true, "BilagIndhold": true,
	"BilagTilBemaerkninger": true, "Bemaerkninger": true,
	"BemaerkningIndhold": true, "BemaerkningerTilParagraf": true,
	"BemaerkningerTilAendringsNummer":               true,
	"BemaerkningerTilForslagetsEnkelteBestemmelser": true,
	"AlmindeligeBemaerkninger":                      true, "BetaenkningsTekst": true,
	"BeretningsTekst": true, "Afslutning": true, "Indledning": true,
	"DokumentIndhold": true, "DokumentHoved": true, "TekstGruppe": true,
	"FremsaettelsesIndledning": true, "FremsaettelsesTekst": true,
	"SkriftligFremsaettelse": true, "Hymne": true, "Segl": true, "Traktat": true,
	"Udkast": true, "Rykningsklausul": true, "Resume": true, "EditorialNote": true,

	"Tr": true, "Table": true,
}

// Parse decodes canonical Lex Dania XML. It does not validate against the XSD;
// it fails only on malformed XML or a missing Dokument root.
func Parse(data []byte) (*Document, error) {
	// The shortcut reads what the register serves; anything else, and every error, is read
	// by encoding/xml below. See fastxml.go.
	if root, ok := fastTree(data); ok {
		if root.Name != "Dokument" {
			return nil, fmt.Errorf("lexdania: root element is %q, expected \"Dokument\"", root.Name)
		}
		return newDocument(root)
	}
	return parseSlow(data)
}

// parseSlow is Parse through encoding/xml alone.
func parseSlow(data []byte) (*Document, error) {
	root, err := slowTree(data)
	if err != nil {
		return nil, err
	}
	if root.Name != "Dokument" {
		return nil, fmt.Errorf("lexdania: root element is %q, expected \"Dokument\"", root.Name)
	}
	return newDocument(root)
}

// slowTree reads the root element's tree with encoding/xml.
func slowTree(data []byte) (*Node, error) {
	dec := xml.NewDecoder(bytesReader(data))
	// The schema is served with a declaration and occasional DOCTYPE; unknown
	// entities would otherwise abort decoding, and losing a document to an
	// entity is worse than mis-decoding one character.
	dec.Entity = xml.HTMLEntity
	dec.Strict = true

	t, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("lexdania: no XML token: %w", err)
	}
	for err == nil {
		if se, ok := t.(xml.StartElement); ok {
			return decodeElement(dec, se)
		}
		t, err = dec.Token()
	}
	if err == io.EOF {
		return nil, fmt.Errorf("lexdania: no root element found")
	}
	return nil, fmt.Errorf("lexdania: decode: %w", err)
}

// decodeElement consumes tokens until the matching end element, preserving mixed
// content and every attribute.
func decodeElement(dec *xml.Decoder, start xml.StartElement) (*Node, error) {
	n := &Node{
		Name:  start.Name.Local,
		Space: start.Name.Space,
		Attrs: make(map[string]string, len(start.Attr)),
	}
	for _, a := range start.Attr {
		key := a.Name.Local
		if a.Name.Space != "" && a.Name.Space != "xmlns" {
			key = a.Name.Space + ":" + key
		}
		n.Attrs[key] = a.Value
	}

	// Text runs accumulate into buf; a run is closed when a child element starts.
	var buf strings.Builder
	closeRun := func() {
		n.Runs = append(n.Runs, stripZeroWidth(buf.String()))
		buf.Reset()
	}

	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("lexdania: reading inside <%s>: %w", n.Name, err)
		}
		switch t := tok.(type) {
		case xml.CharData:
			buf.WriteString(string(t))
		case xml.StartElement:
			child, err := decodeElement(dec, t)
			if err != nil {
				return nil, err
			}
			closeRun()
			n.Elems = append(n.Elems, child)
		case xml.EndElement:
			// Whatever text run is open is the final one, so the interleave
			// invariant len(Runs)==len(Elems)+1 holds here and nowhere else.
			closeRun()
			return n, nil
		case xml.Comment, xml.ProcInst:
			// Not representable in Node. Dropped deliberately: the archive's
			// source of truth stays the .xml file on disk, and no Lex Dania
			// document in the corpus carries meaningful comments.
		}
	}
}

// checkInvariant verifies the mixed-content invariant on the whole subtree. It is
// used by tests, not by Parse, because a violation is a parser bug rather than a
// data problem, and the archive must stay readable either way.
func (n *Node) checkInvariant(path string) error {
	want := len(n.Elems) + 1
	if len(n.Runs) != want {
		return fmt.Errorf("%s/%s: len(Runs)=%d, want %d (len(Elems)=%d)", path, n.Name, len(n.Runs), want, len(n.Elems))
	}
	for _, c := range n.Elems {
		if err := c.checkInvariant(path + "/" + n.Name); err != nil {
			return err
		}
	}
	return nil
}

// ElementCount tallies element names and attribute names across a subtree, which
// is how ElementStats answers "have we mapped everything we are actually fed".
type ElementCount struct {
	Name     string
	Seen     int
	Attrs    map[string]int
	Children map[string]int // parent -> child pairs, for schema discovery
}

// Stats walks the subtree and returns per-element tallies.
func (n *Node) Stats() map[string]*ElementCount {
	out := map[string]*ElementCount{}
	get := func(name string) *ElementCount {
		c := out[name]
		if c == nil {
			c = &ElementCount{Name: name, Attrs: map[string]int{}, Children: map[string]int{}}
			out[name] = c
		}
		return c
	}
	n.Descendants(func(c *Node) {
		self := get(c.Name)
		self.Seen++
		for k := range c.Attrs {
			self.Attrs[k]++
		}
		for _, ch := range c.Elems {
			self.Children[ch.Name]++
		}
	})
	return out
}

// zeroWidth removes the invisible characters an act's typesetting leaves inside words,
// "integrati\u200donslovens": the law reads the same without them, and with them a quoted
// word does not match the text that prints it, nor a search for it.
var zeroWidth = strings.NewReplacer("\u200b", "", "\u200c", "", "\u200d", "", "\ufeff", "", "\u2060", "")

// stripZeroWidth applies zeroWidth only to text that can hold one of its characters. All five
// encode with a lead byte of 0xE2 or 0xEF, which no Danish letter has, so almost every run is
// returned as it is; running the replacer on every run was a twentieth of an artifact build.
func stripZeroWidth(s string) string {
	if strings.IndexByte(s, 0xE2) < 0 && strings.IndexByte(s, 0xEF) < 0 {
		return s
	}
	return zeroWidth.Replace(s)
}
