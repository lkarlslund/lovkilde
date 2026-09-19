package lexdania

import (
	"strconv"
	"strings"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// Address is a parsed reference to a provision, as written in Danish amendment prose. The
// concept lives in lexdoc; this alias keeps every reader of the old name working while the
// code around it moves onto the model.
type Address = lexdoc.Address

// ParseAddress reads a Danish provision reference out of amendment prose.
func ParseAddress(s string) Address { return lexdoc.ParseAddress(s) }

// Section is one addressable unit of law text.
//
// Sections are collected as a flat ordered list by element name rather than by
// walking a presumed structure, because the structure is not stable across document
// types: paragraphs appear under DokumentIndhold, under ParagrafGruppe, and directly
// under Dokument depending on the document. A flat list is also all consolidation
// needs — an amendment addresses "§ 267, stk. 1", not a path.
type Section struct {
	Node *Node

	Kind string // "kapitel", "paragraf", "afsnit", "stk", "nr", "bilag", "overskrift"

	// Number is the normalised sequence number used to match an amendment's
	// address, e.g. "267". It comes from localId when present, otherwise parsed
	// out of Explicatus — localId covers only 3,681 of 4,010 sampled Paragraf
	// elements, so the fallback is load-bearing rather than theoretical.
	Number string

	// RawNumber is the element's own Explicatus text, e.g. "§ 267." — kept for
	// rendering and for reporting which section an edit landed on.
	RawNumber string

	Stk   int    // for stk sections
	Nr    int    // for nr sections
	Litra string // "a" in "§ 8 a"

	Parent *Section

	// Container is the element holding Node. Repealing or inserting a provision has
	// to edit the container, since a Node has no back-pointer to its parent.
	Container *Node

	// Appendix marks a provision that belongs to the entry-into-force and transitional
	// material rather than to the law's own numbering series. Consolidated acts quote
	// the entry-into-force paragraph of every amending act that has touched them, and
	// each of those quotations restarts at § 1, so a consolidated act routinely holds
	// several provisions printed as "§ 3" that are not the law's § 3. The full-corpus
	// scan found 25,680 such provisions: 14,852 <IkraftCentreretParagraf> plus 1,030
	// <Paragraf> inside <Ikraft>, and 9,798 <IkraftCentreretParagraf> directly under
	// <DokumentIndhold>. They stay in the tree — they are law text — but address
	// resolution ignores them, or "§ 3 ophæves" deletes a historical entry-into-force
	// quotation while the real § 3 survives.
	Appendix bool

	// Match is MatchText(Node) — the normalised form edits are located in.
	Match string

	// Runs lists every text run inside this section, in order, so a located match
	// can be spliced back into the tree instead of being rewritten wholesale.
	Runs []TextRun
}

// TextRun locates one text run inside the tree: run Idx of Node. It is exported because
// the compiled dataset has to store it. Unexported fields are invisible to gob and to
// every JSON encoder, so a compiled document built straight off the parse struct would
// lose the exact positions that amendments are spliced back into, and nothing would
// report the loss.
type TextRun struct {
	Node *Node
	Idx  int
}

func (s *Section) Display() string { return DisplayText(s.Node) }

// paragraphish are the elements that carry a numbered provision. The list comes
// from the corpus element inventory, not from the XSD: the schema is not mirrored
// locally, and the extra centreret variants are how amending and entry-into-force
// documents frame their provisions.
var paragraphish = []string{"Paragraf", "TilParagraf", "AendringCentreretParagraf", "IkraftCentreretParagraf"}

// isParagraphName reports whether an element carries a numbered provision. The
// payload nesting pass has to agree with Sections() about what a paragraph is --
// absorbing stykker under an element Sections() will not read as one would build
// structure nothing can address -- so both read the same list rather than two
// copies that can drift.
func isParagraphName(name string) bool {
	for _, p := range paragraphish {
		if name == p {
			return true
		}
	}
	return false
}

// Sections returns the document's numbered provisions in order, with their
// subsections attached.
//
// Proposed text is excluded on purpose. An amending act embeds replacement
// provisions inside <AendringNyTekst>, and those <Paragraf> elements carry their
// own localId numbers; counting them as the document's own sections would make an
// amendment appear to contain the law it is amending, which corrupts every
// subsequent address lookup.
func (d *Document) Sections() []*Section {
	return sectionsIn(d.Root)
}

func sectionsIn(root *Node) []*Section {
	var out []*Section
	var walk func(container, n *Node, insideProposed, inAppendix bool)
	walk = func(container, n *Node, insideProposed, inAppendix bool) {
		proposed := insideProposed || n.Name == "AendringNyTekst" || n.Name == "AendringsNummer"
		// Appendix material is recognised by its container, measured against the whole
		// corpus: <Ikraft> holds the entry-into-force quotations and
		// <IkraftCentreretParagraf> is that material's own provision element, which some
		// documents place straight under <DokumentIndhold> without the wrapper.
		appendix := inAppendix || n.Name == "Ikraft" || n.Name == "IkraftCentreretParagraf"
		if !proposed {
			for _, name := range paragraphish {
				if n.Name != name {
					continue
				}
				sec := newSection(n, "paragraf", nil)
				sec.Container = container
				sec.Appendix = appendix
				stkIdx := 0
				for _, ch := range n.Elems {
					if ch.Name != "Stk" {
						continue
					}
					stkIdx++
					stk := newSection(ch, "stk", sec)
					stk.Container = n
					// Position is the numbering: <Stk> elements carry an id= but
					// almost never a localId=, so "stk. 2" can only mean "the second
					// Stk element". An explicit number wins if one is ever present.
					stk.Stk = atoi(lexdoc.FirstNumber(ch.Attrs["localId"]))
					if stk.Stk == 0 {
						stk.Stk = stkIdx
					}
					// sec.Runs already holds every run in this provision's subtree, in
					// document order, because newSection collected them recursively. The
					// stykke and item lists must not be appended on top of that: doing so
					// repeated each descendant run two or three times, which made the
					// concatenation splice searched a garbled copy of the provision, and its
					// write-back then restored the unedited copy over the edited one. An
					// amendment quoting a passage inside "stk. 1, nr. 2" reported itself
					// applied while the text stayed exactly as it was.

					// Nr elements are numbered the same way, and the Explicatus of a
					// numbered paragraph often carries it ("2)"), which is why the
					// explicit value is preferred when present.
					nrIdx := 0
					var lastParent *Node
					for _, it := range numberedItems(ch) {
						if it.parent != lastParent {
							nrIdx = 0
							lastParent = it.parent
						}
						nrIdx++
						nr := newSection(it.node, "nr", stk)
						nr.Container = it.parent
						nr.Nr, nr.Litra = lexdoc.ItemMarker(it.node.Elem("Explicatus").TextOrNull())
						if nr.Nr == 0 && nr.Litra == "" {
							nr.Nr = nrIdx
						}
						nr.Stk = stk.Stk
						out = append(out, nr)
					}
					out = append(out, stk)
				}
				out = append(out, sec)
				return // children are represented through the section
			}
			switch n.Name {
			case "Kapitel":
				sec := newSection(n, "kapitel", nil)
				sec.Container = container
				sec.Number = lexdoc.FirstNumber(n.Attrs["localId"])
				out = append(out, sec)
			case "Afsnit":
				sec := newSection(n, "afsnit", nil)
				sec.Container = container
				sec.Number = lexdoc.FirstNumber(n.Attrs["localId"])
				// Afsnit elements carry id="Afs1" and no localId, and print their number
				// as a roman numeral: <Explicatus>Afsnit I</Explicatus>. firstNumber finds
				// no digit in that, so every one of these sections used to have an empty
				// Number and "afsnit II" could not resolve at all. The printed numeral is
				// the only number the element has, and it wins over localId because it is
				// what the amendment cites.
				if r := lexdoc.PrintedRoman(sec.RawNumber); r != "" {
					sec.Number = r
				}
				out = append(out, sec)
			case "Indledning":
				// The preamble of a consolidated act is prose at the top of
				// DokumentIndhold, and amending acts rewrite it wholesale ("Indledningen
				// affattes således:"). Without a section for it there was nothing to
				// point at, so 1,142 changes had no target.
				sec := newSection(n, "indledning", nil)
				sec.Container = container
				out = append(out, sec)
			case "Titel":
				// The document's own title, for "Lovens titel affattes således:". The
				// first one in document order is the document title; a later one belongs
				// to an annex.
				sec := newSection(n, "titel", nil)
				sec.Container = container
				out = append(out, sec)
			case "Bilag":
				sec := newSection(n, "bilag", nil)
				sec.Container = container
				out = append(out, sec)
			}
			// These are containers, not leaves, so the walk continues into them.
			// Returning here — which is what this line replaced — made every section
			// inside a chapter or afsnit invisible: a law organised as
			// Afsnit>Kapitel>ParagrafGruppe>Paragraf appeared to have no paragraphs at
			// all, so every amendment to it reported "address not found".
		}
		for _, c := range n.Elems {
			walk(n, c, proposed, appendix)
		}
	}
	walk(nil, root, false, false)
	return out
}

func newSection(n *Node, kind string, parent *Section) *Section {
	s := &Section{Node: n, Kind: kind, Parent: parent, RawNumber: strings.TrimSpace(n.Elem("Explicatus").TextOrNull())}
	s.Number = lexdoc.FirstNumber(n.Attrs["localId"])
	if s.Number == "" {
		s.Number = lexdoc.FirstNumber(s.RawNumber)
	}
	if kind == "paragraf" {
		s.Litra = lexdoc.ParaLitra(s.RawNumber)
	}
	s.Match = MatchText(n.PlainText())
	collectRuns(n, &s.Runs)
	return s
}

// numberedItem is a numbered or lettered sub-item together with its parent, which is
// the container an edit has to splice into.
type numberedItem struct {
	parent, node *Node
}

// numberedItems returns the "nr. 1" and "litra b" items under a node, in document
// order.
//
// The obvious guess — that numbered items are <Nr> children of <Stk> — is wrong for
// most of the corpus. The shape actually used is
//
//	<Stk><Exitus><Index><Indentatio formaInd="Nummer"><Explicatus>1)</Explicatus>
//
// and lettered items use the same Indentatio with "a)". One real 2026 order contains
// 0 <Nr> elements and 27 <Index> elements, so reading only <Nr> left every
// "stk. 1, nr. 2" address unresolvable.
//
// Descent stops at the first item found: nested items are not addressable in Danish
// citation practice, and flattening them would double-count their text.
func numberedItems(n *Node) []numberedItem {
	var out []numberedItem
	var walk func(p *Node)
	walk = func(p *Node) {
		for _, c := range p.Elems {
			switch {
			case c.Name == "Nr":
				out = append(out, numberedItem{p, c})
			case c.Name == "Indentatio" && numberedIndentation(c):
				out = append(out, numberedItem{p, c})
			default:
				walk(c)
			}
		}
	}
	walk(n)
	return out
}

// numberedIndentation reports whether an Indentatio is a numbered or lettered item
// rather than plain indentation. Indentatio is a general layout element, so treating
// every one as an item would invent provisions.
func numberedIndentation(n *Node) bool {
	kind := strings.ToLower(n.Attrs["formaInd"])
	if strings.Contains(kind, "nummer") || strings.Contains(kind, "litra") {
		return true
	}
	num, litra := lexdoc.ItemMarker(n.Elem("Explicatus").TextOrNull())
	return num != 0 || litra != ""
}

// collectRuns records every text run in document order, using the same traversal PlainText
// renders with: Runs[0] precedes Elems[0], and the run after each child follows it.
//
// It has to be the same traversal, not merely a walk. splice turns a located match back into
// offsets in this list, so a list ordered differently from the displayed text points those
// offsets at the wrong characters — and its write-back then overwrites the untouched copy of a
// run with the edited one. Collecting a node's own runs before its children's did exactly that
// for any provision with an inline element in its prose, which is most of them: an inline
// <Char>, <Ref> or <Nota> pushed the marker to the end of the concatenated text instead of
// leaving it where the page puts it.
func collectRuns(n *Node, out *[]TextRun) {
	if n.Runs[0] != "" {
		*out = append(*out, TextRun{Node: n, Idx: 0})
	}
	for i, c := range n.Elems {
		collectRuns(c, out)
		if i+1 < len(n.Runs) && n.Runs[i+1] != "" {
			*out = append(*out, TextRun{Node: n, Idx: i + 1})
		}
	}
}

// TextOrNull returns "" for a nil node, which keeps Explicatus lookups readable:
// not every section element carries one.
func (n *Node) TextOrNull() string {
	if n == nil {
		return ""
	}
	return n.Text()
}

// Find returns the section an address points at, or nil.
//
// Numbering is read from the *current* state of the section list, not from the
// document as originally published. That is the whole reason edits must be applied
// strictly in date order: an amendment's "§ 12, stk. 2" refers to the numbering that
// existed when it was written, and once earlier edits have inserted or repealed
// provisions the numbering has moved. Applying edits out of order, or skipping one,
// silently mis-targets every later edit.
func Find(secs []*Section, a Address) *Section {
	if !a.Recognised {
		return nil
	}
	if a.Bilag != 0 {
		for _, s := range secs {
			if s.Kind == "bilag" && lexdoc.FirstNumber(s.Number+s.RawNumber) == strconv.Itoa(a.Bilag) {
				return s
			}
		}
		return nil
	}
	if a.Chapter != 0 && a.Paragraph == "" {
		for _, s := range secs {
			if s.Kind == "kapitel" && s.Number == strconv.Itoa(a.Chapter) {
				return s
			}
		}
		return nil
	}
	// The non-provision units are only claimed when the address names nothing else:
	// "I § 1 og i indledningen ændres …" has to keep pointing at § 1.
	if a.Paragraph == "" && a.Chapter == 0 && a.Bilag == 0 {
		if a.Afsnit != 0 {
			for _, s := range secs {
				if s.Kind == "afsnit" && s.Number == strconv.Itoa(a.Afsnit) {
					return s
				}
			}
			return nil
		}
		if a.Preamble {
			for _, s := range secs {
				if s.Kind == "indledning" {
					return s
				}
			}
			return nil
		}
		if a.DocTitle {
			for _, s := range secs {
				if s.Kind == "titel" {
					return s
				}
			}
			return nil
		}
	}
	if a.Paragraph == "" {
		return nil
	}

	// Paragraph litra is written two ways in the corpus: "§ 8 a" as one token, and
	// "§ 8, litra a". Accept both.
	want := strings.ToLower(a.Paragraph)
	var par *Section
	for _, s := range secs {
		// Appendix provisions are skipped on purpose. If the law's own § 3 is gone, an
		// address for § 3 resolves to nothing rather than to the entry-into-force
		// quotation of some amending act that happens to be printed as "§ 3": refusing
		// the edit is visible in Unresolved, editing the wrong provision is not.
		if s.Kind != "paragraf" || s.Appendix {
			continue
		}
		if strings.ToLower(s.Number) == want {
			par = s
			break
		}
		if len(want) > 1 && strings.EqualFold(s.Number, want[:len(want)-1]) && s.Litra == want[len(want)-1:] {
			par = s
			break
		}
	}
	if par == nil {
		return nil
	}
	if a.Stk == 0 && a.Nr == 0 && a.Litra == "" {
		return par
	}
	// A lettered paragraph is written two ways: "§ 8 a" glued, and "§ 8, litra a". The second
	// arrives looking like an item litra, so the paragraph claims it when its own litra
	// matches. Returning an item instead would answer with a different provision.
	if a.Stk == 0 && a.Nr == 0 && a.Litra != "" && strings.EqualFold(par.Litra, a.Litra) {
		return par
	}

	// A numbered or lettered item is a child of its stykke, not of the paragraph, so the
	// search has to descend one level. Looking for the item among the paragraph's direct
	// children meant every "stk. 1, nr. 2" address resolved to nothing.
	//
	// Omitting the stykke is not a mistake, and until now it was fatal. An act citing an item
	// in a provision with one stykke writes "§ 92, nr. 2" and never says "stk. 1", which is
	// how 26 of BR18's 109 refusals arrived: the item was found, rendered and unaddressable.
	// The search therefore covers the paragraph's own children and each of its stykker. An
	// item number carried by two stykker is ambiguous and resolves to nothing, because
	// choosing one silently edits the wrong provision and a refusal is visible while that is
	// not.
	var parents []*Section
	if a.Stk != 0 {
		var stk *Section
		for _, s := range secs {
			if s.Parent == par && s.Kind == "stk" && s.Stk == a.Stk {
				stk = s
				break
			}
		}
		if stk == nil {
			return nil
		}
		if a.Nr == 0 && a.Litra == "" {
			return stk
		}
		parents = []*Section{stk}
	} else {
		parents = []*Section{par}
		for _, s := range secs {
			if s.Parent == par && s.Kind == "stk" {
				parents = append(parents, s)
			}
		}
	}

	var found *Section
	for _, s := range secs {
		if !amongParents(parents, s.Parent) {
			continue
		}
		if a.Nr != 0 && s.Nr != a.Nr {
			continue
		}
		if a.Litra != "" && !strings.EqualFold(s.Litra, a.Litra) {
			continue
		}
		if found == nil {
			found = s
			continue
		}
		if s.Parent != found.Parent {
			return nil
		}
	}
	return found
}

// amongParents reports whether parent is one of the sections an item search may look inside.
func amongParents(parents []*Section, parent *Section) bool {
	for _, p := range parents {
		if p == parent {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
