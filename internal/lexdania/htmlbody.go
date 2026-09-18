package lexdania

import (
	"fmt"
	"io"
	"sort"
	"strings"

	stdhtml "golang.org/x/net/html"
)

// The builder turns a documentHtml payload into the same tree shape the canonical
// ELI XML decodes into, so that Sections(), Match() and the consolidation passes
// work on both without knowing which produced which.
//
// Two properties are load-bearing:
//
//   - The mixed-content invariant (len(Runs) == len(Elems)+1, Runs[k] preceding
//     Elems[k]) is maintained here rather than repaired afterwards, because every
//     repair is a guess about where text belonged.
//   - Nothing is dropped silently. An unrecognised class, a skipped subtree, an
//     element closed without an end tag and a footnote that never found its anchor
//     are all counted, so "this document parsed" and "this document parsed
//     completely" stay distinguishable claims.

// HTMLUnmapped is one payload class the mapping table does not know, with how many
// elements carried it.
type HTMLUnmapped struct {
	Tag   string
	Class string
	Count int
}

// HTMLBody is a documentHtml payload read into the canonical tree shape.
type HTMLBody struct {
	// Root is named DokumentIndhold, the element a canonical document's body lives
	// in, so a synthesised tree and a parsed one are interchangeable downstream.
	Root *Node

	unmapped map[string]*HTMLUnmapped
	skips    map[string]int
	// skippedText is the text that was dropped, kept so that conservation can be
	// checked without a second copy of the drop rules: everything in the payload
	// must be either in the tree or accounted for here.
	skippedText    []string
	implicitCloses int
	strayEnds      int
	unmatchedNotes int
	relocatedNotes int
	sgml           bool
	// The nesting pass counts what it moved, so a payload generation that writes
	// paragraphs differently shows up as a changed number rather than a tree that
	// quietly has different sections in it.
	absorbed int
	wrapped  int
}

// Unmapped lists classes outside the table, most frequent first.
func (b *HTMLBody) Unmapped() []HTMLUnmapped {
	out := make([]HTMLUnmapped, 0, len(b.unmapped))
	for _, u := range b.unmapped {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Class < out[j].Class
	})
	return out
}

// UnmappedCount is how many elements carried a class the table does not know.
func (b *HTMLBody) UnmappedCount() int {
	n := 0
	for _, u := range b.unmapped {
		n += u.Count
	}
	return n
}

// Skips counts dropped subtrees by the reason recorded on the mapping row, so a
// text-loss regression names itself.
func (b *HTMLBody) Skips() map[string]int { return b.skips }

// SkippedText is the text that was dropped, verbatim.
func (b *HTMLBody) SkippedText() []string { return b.skippedText }

// ImplicitCloses counts elements that were closed because a later element forced
// it, not because the payload said so. The payloads do leave elements unclosed --
// measured: 47 spans and 15 table rows -- so a nonzero count is normal; a count
// that grows across the corpus means a new unclosed construct appeared.
func (b *HTMLBody) ImplicitCloses() int { return b.implicitCloses }

// StrayEnds counts end tags with no matching open element, which the tokenizer
// cannot be at fault for: they are in the payload.
func (b *HTMLBody) StrayEnds() int { return b.strayEnds }

// UnmatchedNotes counts footnotes whose text never reached an anchor, and
// RelocatedNotes those that did: a footnote left where it was found is a note
// attached to the wrong sentence.
func (b *HTMLBody) UnmatchedNotes() int { return b.unmatchedNotes }
func (b *HTMLBody) RelocatedNotes() int { return b.relocatedNotes }

// SGML reports whether the doubled-quote pre-pass fired, which distinguishes the
// legacy generation of payloads without re-inspecting them.
func (b *HTMLBody) SGML() bool { return b.sgml }

// Absorbed is how many elements the nesting pass moved inside a paragraph, and
// WrappedParagraphs how many numbered paragraphs had their prose moved into a first
// stykke. Both are zero for a payload that has nothing to nest; a payload full of
// sub-stykker that reports zero absorbed elements means the class map stopped
// recognising them.
func (b *HTMLBody) Absorbed() int          { return b.absorbed }
func (b *HTMLBody) WrappedParagraphs() int { return b.wrapped }

// Elements is the element count, for the coverage assertions that pin the mapping.
func (b *HTMLBody) Elements() int {
	n := 0
	b.Root.Descendants(func(*Node) { n++ })
	return n
}

// htmlFrame is one open element. node is nil when the element contributes no
// canonical element of its own: a layout wrapper contributes its children to the
// nearest ancestor that does, and a skipped subtree contributes nothing.
type htmlFrame struct {
	htmlTag string
	node    *Node
	skip    bool
	// note marks the footnote container whose text has to be relocated when it
	// closes. The marker inside it is also roleNote, so the role alone cannot say
	// which one is the container, and a single "current note" field got overwritten
	// by the marker.
	note bool
	// pending is the text seen since this element's last child. It is flushed into
	// the node before a child is appended, which is what keeps Runs interleaved
	// with Elems instead of appended after them.
	pending strings.Builder
}

type htmlBuilder struct {
	z     *stdhtml.Tokenizer
	stack []*htmlFrame
	body  *HTMLBody

	// notes holds footnote containers by id. The payload puts a note's text at the
	// bottom of the document and its marker wherever the note is referenced; the
	// canonical tree puts the note's text at the reference. Anchors are therefore
	// created at the marker and filled in when the text turns up later.
	notes map[string]*Node
	// inIkraft is the region state roleCentreret needs: a centred provision line
	// after the entry-into-force rule is appendix material, and prose before it.
	inIkraft bool
}

// ParseHTMLBody reads a documentHtml payload into the canonical tree shape.
func ParseHTMLBody(src string) (*HTMLBody, error) {
	root := &Node{Name: "DokumentIndhold", Attrs: map[string]string{}, Runs: []string{""}}
	b := &htmlBuilder{
		body: &HTMLBody{
			Root:     root,
			unmapped: map[string]*HTMLUnmapped{},
			skips:    map[string]int{},
			sgml:     hasSGMLAttrs(src),
		},
		notes: map[string]*Node{},
		stack: []*htmlFrame{{htmlTag: "#root", node: root}},
	}
	b.z = stdhtml.NewTokenizer(strings.NewReader(unquoteSGML(src)))

	for {
		switch tt := b.z.Next(); tt {
		case stdhtml.ErrorToken:
			if err := b.z.Err(); err != io.EOF {
				return nil, fmt.Errorf("lexdania: reading documentHtml: %w", err)
			}
			b.closeAll()
			// Notes are placed while their containers close, so the paragraph
			// nesting runs last: it moves elements between containers, and a note
			// that had not been relocated yet would be looked for in the wrong place.
			nestStykker(root, b.body)
			return b.body, nil

		case stdhtml.TextToken:
			b.text(string(b.z.Text()))

		case stdhtml.StartTagToken, stdhtml.SelfClosingTagToken:
			tag, attrs := b.tagAttrs()
			b.start(tag, attrs)
			// A self-closing tag contributes nothing that can outlive itself, and
			// the void elements that dominate these (br, hr, img) are handled in
			// start; popping is for XML-style <br/> spellings.
			if tt == stdhtml.SelfClosingTagToken {
				b.closeTo(tag, false)
			}

		case stdhtml.EndTagToken:
			tag, _ := b.z.TagName()
			b.closeTo(string(tag), true)
		}
	}
}

// tagAttrs reads the current start tag's attributes. It has to run before the next
// token, which is why tag and attributes are returned together.
func (b *htmlBuilder) tagAttrs() (string, map[string]string) {
	tag, _ := b.z.TagName()
	attrs := map[string]string{}
	for {
		key, val, more := b.z.TagAttr()
		attrs[strings.ToLower(string(key))] = string(val)
		if !more {
			break
		}
	}
	return string(tag), attrs
}

// cur is the innermost open element that contributes a node: text and children
// belong to it.
func (b *htmlBuilder) cur() *htmlFrame {
	for i := len(b.stack) - 1; i >= 0; i-- {
		if b.stack[i].node != nil {
			return b.stack[i]
		}
	}
	return b.stack[0]
}

// skipping reports whether an enclosing element's subtree is being dropped.
// insideNote reports whether a footnote paragraph is open, which is what separates
// a note's own marker from a reference to it.
func (b *htmlBuilder) insideNote() bool {
	for _, f := range b.stack {
		if f.note {
			return true
		}
	}
	return false
}

func (b *htmlBuilder) skipping() bool {
	for _, f := range b.stack {
		if f.skip {
			return true
		}
	}
	return false
}

func (b *htmlBuilder) text(s string) {
	if s == "" {
		return
	}
	if b.skipping() {
		b.body.skippedText = append(b.body.skippedText, s)
		return
	}
	b.cur().pending.WriteString(s)
}

// addChild puts child into the innermost open node, closing the text run that
// precedes it.
func (b *htmlBuilder) addChild(child *Node) {
	f := b.cur()
	f.node.Runs[len(f.node.Runs)-1] += f.pending.String()
	f.pending.Reset()
	f.node.Elems = append(f.node.Elems, child)
	f.node.Runs = append(f.node.Runs, "")
}

// start handles one start tag.
func (b *htmlBuilder) start(tag string, attrs map[string]string) {
	class := strings.TrimSpace(attrs["class"])

	// The rule that separates the two readings of a centred provision line has to
	// be read before the element it precedes is classified.
	if class == "ikraftstreg" {
		b.inIkraft = true
	}

	target := classifyHTML(tag, class)
	if target.Role == roleUnknown {
		b.body.recordUnmapped(tag, class)
		// Placement still follows the tag, so an unknown class costs a name, not
		// the text inside it.
		target = htmlTagFallback(tag)
	}
	if target.Role == roleUnknown {
		// Neither the class nor the tag is known, which leaves no name to give. The
		// honest reading is a container: the text stays in the tree and nothing is
		// invented. Falling through instead built a node with an empty name, which is
		// how the first corpus-wide census found 12,371 elements named "" -- and a
		// nameless element is skipped by every walk that looks elements up by name, so
		// the text under it reads as missing even though the tree holds it.
		b.push(htmlFrame{htmlTag: tag})
		return
	}
	if target.Role == roleCentreret {
		target = b.resolveCentreret()
	}
	// Inside a note paragraph, a footnote link is that note's own printed marker: it
	// belongs to the note as an Explicatus and carries the id references point at.
	// Outside one it is a reference, and gets an anchor to fill in later. Both of
	// these context-dependent readings are settled before anything is constructed:
	// deciding afterwards means the element has already been built under the wrong
	// name, which is how a note's marker turned into a second footnote.
	if target.Role == roleNote && target.Attr == "ref" && b.insideNote() {
		target.Name = "Explicatus"
		target.Attr = ""
	}

	// Void elements never appear as an end tag, so leaving them on the stack would
	// nest the rest of the document inside the first <br> of a legacy payload.
	if htmlVoidTags[tag] {
		b.breakElement(tag)
		return
	}

	b.impliedClose(tag)

	switch target.Role {
	case roleSkip:
		b.body.skips[target.Reason]++
		b.push(htmlFrame{htmlTag: tag, skip: true})
		return

	case roleContainer:
		b.push(htmlFrame{htmlTag: tag})
		return
	}

	// A new element has no children, so it opens with exactly one (empty) run:
	// len(Runs) == len(Elems)+1 holds from birth, which is what lets addChild and
	// pop keep it instead of repairing it.
	node := &Node{Name: target.Name, Attrs: map[string]string{}, Runs: []string{""}}
	// The payload's own id is kept: it is the same identifier the canonical XML
	// carries for the paired documents, which is what lets one tree be aligned
	// against the other instead of merely compared by text.
	// name is kept as well: a footnote's identity is the name= on its own marker,
	// and dropping it makes the note unanchorable.
	for _, key := range []string{"id", "localId", "name"} {
		if v := attrs[key]; v != "" {
			node.Attrs[key] = v
		}
	}
	switch target.Role {
	case roleInline:
		if target.Attr != "" {
			node.Attrs["formaChar"] = target.Attr
		}
	case roleText:
		if target.Attr != "" {
			node.Attrs["formaInd"] = target.Attr
		}
	case roleNote:
		if target.Attr == "ref" {
			// The inline marker: open the note here so its text can be moved in
			// from the bottom of the payload when it arrives.
			id := footnoteTargetID(attrs)
			if id == "" {
				// A marker with no target is navigation with no note; keeping the
				// element would fabricate a footnote.
				b.push(htmlFrame{htmlTag: tag, skip: true})
				return
			}
			node.Attrs["id"] = id
			b.notes[id] = node
			b.addChild(node)
			b.push(htmlFrame{htmlTag: tag, node: node})
			return
		}
	}
	if htmlBlockTags[tag] {
		// Paragraphs are separate lines when rendered; without a break they would
		// concatenate into one run in DisplayText, which squeezes whitespace.
		b.text("\n")
	}
	b.addChild(node)
	// Only the container relocates: the marker inside it is roleNote as well, and
	// treating it as a container reports a footnote that never existed.
	b.push(htmlFrame{htmlTag: tag, node: node, note: target.Role == roleNote && target.Name == "Nota"})
}

// resolveCentreret answers the question the classifier deliberately leaves open.
func (b *htmlBuilder) resolveCentreret() htmlTarget {
	if b.inIkraft {
		return htmlTarget{Role: roleSection, Name: "IkraftCentreretParagraf", Basis: basisLayout}
	}
	return htmlTarget{Role: roleText, Name: "Exitus", Basis: basisLayout}
}

// breakElement handles the elements that carry no content: a line break is a
// newline in canonical text, and an image contributes nothing.
func (b *htmlBuilder) breakElement(tag string) {
	switch tag {
	case "br", "hr":
		b.text("\n")
	}
}

// impliedClose closes what HTML itself would have closed. Only constructs measured
// in the payloads get a rule: the corpus leaves spans and table rows unclosed
// (47 and 15), and inventing a broader rule risks re-nesting content that is
// currently in the right place.
func (b *htmlBuilder) impliedClose(newTag string) {
	var closes []string
	switch {
	case newTag == "td" || newTag == "th":
		// A new cell stays in the open row, so the row is not a candidate. This case
		// has to come first: a combined arm below would also match td and move the
		// cell to a row of its own.
		closes = []string{"td", "th"}
	case newTag == "tr" || newTag == "tbody" ||
		newTag == "tfoot" || newTag == "thead" || newTag == "table" || newTag == "caption":
		// A row ends the previous cell wherever the parser currently is: the
		// payload's cells hold prose wrappers, and HTML closes those with the cell.
		closes = []string{"td", "th", "tr"}
	case newTag == "p":
		// A paragraph cannot contain a paragraph.
		closes = []string{"p", "span"}
	case htmlBlockTags[newTag]:
		closes = []string{"span"}
	default:
		return
	}

	// The outermost match is the element being ended, and everything opened inside
	// it goes with it. The innermost match is wrong twice over: prose wrappers sit
	// inside cells, so a new row has to reach past them, and a new cell must stop at
	// the cell -- reaching the row would move it to a row of its own.
	for i := 1; i < len(b.stack); i++ {
		if !containsHTMLTag(closes, b.stack[i].htmlTag) {
			continue
		}
		for len(b.stack)-1 > i {
			b.pop(true)
		}
		b.pop(false)
		return
	}
}

func containsHTMLTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// closeTo ends the innermost element with this tag, closing anything opened inside
// it. Stray end tags -- those with no open element -- are counted and ignored,
// since failing here would discard the rest of the document over one typo.
func (b *htmlBuilder) closeTo(tag string, fromEndTag bool) {
	for i := len(b.stack) - 1; i > 0; i-- {
		if b.stack[i].htmlTag != tag {
			continue
		}
		for len(b.stack)-1 > i {
			b.pop(true)
		}
		b.pop(false)
		return
	}
	if fromEndTag {
		b.body.strayEnds++
	}
}

// pop closes the innermost frame, flushing its open text run.
func (b *htmlBuilder) pop(implicit bool) {
	f := b.stack[len(b.stack)-1]
	if implicit && f.node != nil {
		b.body.implicitCloses++
	}
	if f.node != nil {
		f.node.Runs[len(f.node.Runs)-1] += f.pending.String()
		if f.note {
			b.placeNote(f.node)
		}
	}
	b.stack = b.stack[:len(b.stack)-1]
}

func (b *htmlBuilder) closeAll() {
	for len(b.stack) > 1 {
		b.pop(true)
	}
	b.pop(false)
}

// push opens an element. Frames are held by pointer: their pending run is written
// to from addChild long after the frame was pushed.
func (b *htmlBuilder) push(f htmlFrame) { b.stack = append(b.stack, &f) }

// placeNote puts a footnote's text at its reference, as the canonical tree does.
// Anchoring happens at the marker, so the note text found at the bottom of the
// payload has to travel: its children move into the node created there and the
// now-empty container is removed from where it was found.
func (b *htmlBuilder) placeNote(note *Node) {
	id := note.Attrs["id"]
	if id == "" {
		// The note's identity is the name= on its own marker, which was recorded as
		// an Explicatus child. Reaching it here means scanning children, which is
		// cheap and exact.
		for _, c := range note.Elems {
			if c.Name == "Explicatus" {
				if v := c.Attrs["name"]; v != "" {
					id = v
					break
				}
			}
		}
	}
	anchor := b.notes[id]
	if anchor == nil || anchor == note {
		// Counted whatever the reason: a note that cannot be anchored is text about
		// one sentence sitting at the bottom of the document, which is a finding, not
		// a detail.
		b.body.unmatchedNotes++
		return
	}
	anchor.Runs[0] += note.Runs[0]
	anchor.Elems = append(anchor.Elems, note.Elems...)
	anchor.Runs = append(anchor.Runs, note.Runs[1:]...)
	// The container element is only removed once its content has moved, and
	// removeElement maintains the invariant on the way out.
	if parent := b.noteParent(note); parent != nil {
		removeElement(parent, note)
	}
	b.body.relocatedNotes++
}

// noteParent finds the open node holding n. Only open elements are candidates: a
// note's container is closed no later than the note itself.
func (b *htmlBuilder) noteParent(n *Node) *Node {
	for _, f := range b.stack {
		if f.node == nil {
			continue
		}
		for _, c := range f.node.Elems {
			if c == n {
				return f.node
			}
		}
	}
	return nil
}

// footnoteTargetID resolves a footnote marker to the note it points at. The site
// links by fragment (href="#Not1"); older payloads identify the note through the
// marker's own name, which is "Henvisning_" plus the note id.
func footnoteTargetID(attrs map[string]string) string {
	if href := attrs["href"]; href != "" {
		if id := strings.TrimPrefix(href, "#"); id != "" && id != href {
			return id
		}
		if id := strings.TrimPrefix(href, "%23"); id != "" && id != href {
			return id
		}
	}
	// A bare name is the marker's own identity, not a target, so only the
	// "Henvisning_" convention yields an anchor.
	if name := attrs["name"]; strings.HasPrefix(name, "Henvisning_") {
		return strings.TrimPrefix(name, "Henvisning_")
	}
	return ""
}

func (b *HTMLBody) recordUnmapped(tag, class string) {
	key := tag + "." + class
	if u, ok := b.unmapped[key]; ok {
		u.Count++
		return
	}
	b.unmapped[key] = &HTMLUnmapped{Tag: tag, Class: class, Count: 1}
}

// htmlVoidTags are the elements with no end tag in any syntax. Leaving one open
// would nest everything after it inside the element.
var htmlVoidTags = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true, "keygen": true,
	"basefont": true, "bgsound": true, "frame": true, "isindex": true,
}

// htmlBlockTags is used for one purpose: a block starting is where rendered text
// gets a line break, and it is what makes an unclosed inline element get closed.
// Layout wrappers are deliberately absent -- a div inside a table cell is common in
// these payloads (div.TabelTekst is the single most frequent class) and must not
// end the cell.
var htmlBlockTags = map[string]bool{
	"address": true, "blockquote": true, "center": true, "dd": true, "div": true,
	"dl": true, "dt": true, "fieldset": true, "form": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "li": true, "ol": true, "p": true,
	"table": true, "tbody": true, "td": true, "tfoot": true, "th": true,
	"thead": true, "tr": true, "ul": true,
}
