// Anchors: the addresses a browser can point at.
//
// An HTML page needs an id per thing a citation can name, and a table of contents needs to agree
// with those ids. Both come from the model's own addressing rather than from a slug of the text:
// "§ 267" is the anchor "p-267" because that is what the provision is called, and an id invented
// from a heading's words changes when the heading is reworded while the citation does not.
//
// One walk assigns every id, and both the render and the contents list read what it assigned. That
// is the whole design, and the alternative — a second walk that derives ids again — is what the
// first version of this file did. It produced contents links to "#" on the real mirror within an
// afternoon, because an unnumbered provision has no name to build an id from, and two divisions
// numbered "1" in different parts of the same law both want "kapitel-1". A second opinion about
// where things are is exactly what a table of contents must not be.
package lexdoc

import (
	"strconv"
	"strings"
	"unicode"
)

// anchorSet is every id one render will write, decided in one reading-order walk.
type anchorSet struct {
	provisions map[*Provision]string
	parts      map[*Part]string
	appendices map[*Appendix]string

	// contents is the outline in the same order the walk went, with the ids it assigned.
	contents []Contents

	used map[string]bool
}

// Contents is one line of a table of contents: what to call it, and where the render put it.
type Contents struct {
	ID    string
	Label string
	Level int
}

// anchorSet walks the content and assigns its ids. It is built per render rather than stored on
// the content, because a served document is read by several requests at once and a cache inside a
// shared model would be a data race with a nice name.
func (c *Content) anchorSet() *anchorSet {
	a := &anchorSet{
		provisions: make(map[*Provision]string, len(c.Provisions())),
		parts:      make(map[*Part]string),
		appendices: make(map[*Appendix]string, len(c.Appendices)),
		used:       map[string]bool{},
	}
	// The two headings the mirror adds itself are reserved before anything else is named, so a
	// thing whose own number happens to slug to the same word loses the tie rather than the page.
	a.reserve("ikraft")
	for _, h := range textHeads {
		a.reserve(h.id)
	}
	a.walk(c.Order, 2, "")
	if len(c.EntryIntoForce) > 0 {
		a.contents = append(a.contents, Contents{ID: "ikraft", Label: "Ikrafttrædelsesbestemmelser", Level: 2})
		for i := range c.EntryIntoForce {
			p := &c.EntryIntoForce[i]
			id := a.name("ikraft", p.Address(), strconv.Itoa(i+1))
			a.provisions[p] = id
			a.contents = append(a.contents, Contents{ID: id, Label: provisionLabel(p), Level: 3})
		}
	}
	for i := range c.Appendices {
		ap := &c.Appendices[i]
		id := a.name("bilag", firstNonEmpty(ap.Number, ap.Label), strconv.Itoa(i+1))
		a.appendices[ap] = id
		a.contents = append(a.contents, Contents{ID: id, Label: appendixTitle(ap), Level: 2})
	}
	for _, g := range textGroups(c.Unmapped) {
		h, ok := textHeads[g.concept]
		switch {
		case !ok:
		case g.concept == "summary":
			// printed ahead of the provisions, so first in the contents too
			a.contents = append([]Contents{{ID: h.id, Label: h.da, Level: 2}}, a.contents...)
		default:
			a.contents = append(a.contents, Contents{ID: h.id, Label: h.da, Level: 2})
		}
	}
	return a
}

// walk names the parts and provisions of one level, in the order the document puts them. The path
// is the place of a thing among its ancestors and siblings — "1-2" is the second node of the first
// top-level division — which is the only thing that identifies a provision the publisher left
// unnumbered.
func (a *anchorSet) walk(nodes []Node, level int, prefix string) {
	for i, n := range nodes {
		path := childPath(prefix, i+1)
		switch {
		case n.Part != nil:
			id := a.name(partKindWord(n.Part), n.Part.Number, path)
			a.parts[n.Part] = id
			if strings.TrimSpace(n.Part.Printed) == "" && strings.TrimSpace(n.Part.Heading) == "" {
				// A division the document neither numbers nor heads is no line of its contents:
				// "provisionsgruppe" above every run of provisions told a reader nothing.
				a.walk(n.Part.Order, level, path)
				continue
			}
			a.contents = append(a.contents, Contents{ID: id, Label: partLabel(n.Part), Level: level})
			a.walk(n.Part.Order, level+1, path)
		case n.Provision != nil:
			id := a.name("p", n.Provision.Address(), path)
			a.provisions[n.Provision] = id
			a.contents = append(a.contents, Contents{ID: id, Label: provisionLabel(n.Provision), Level: level})
		}
	}
}

// name is the one place an id is chosen. A thing with a number gets an id from it, because that is
// what a citation names; anything else — unnumbered, or already taken by an earlier thing with the
// same number — gets an id from its place, which nothing else can claim.
//
// The positional form carries a double dash, which a slug can never produce: slug leaves only
// letters, digits, and single dashes between them. That is what makes "the second one falls back to
// its position" a guarantee rather than a hope, and it is why ids like p--u1-2 look odd on purpose.
func (a *anchorSet) name(kind, printedNumber, path string) string {
	if n := slug(strings.TrimSpace(printedNumber)); n != "" {
		if candidate := kind + "-" + n; !a.used[candidate] {
			a.used[candidate] = true
			return candidate
		}
	}
	id := kind + "--u" + path
	a.used[id] = true
	return id
}

func (a *anchorSet) reserve(ids ...string) {
	for _, id := range ids {
		a.used[id] = true
	}
}

// provisionID, partID, and appendixID answer what the render wrote for one object. An object the
// walk never reached has no id, and "" says so: an id that was never written is a link to the top
// of the page, which reads exactly like a working link.
func (a *anchorSet) provisionID(p *Provision) string {
	if a == nil || p == nil {
		return ""
	}
	return a.provisions[p]
}

func (a *anchorSet) partID(p *Part) string {
	if a == nil || p == nil {
		return ""
	}
	return a.parts[p]
}

func (a *anchorSet) appendixID(ap *Appendix) string {
	if a == nil || ap == nil {
		return ""
	}
	return a.appendices[ap]
}

// AnchorFor is the id the HTML render gives to whatever an address names in this content, or the
// empty string when the address names nothing. Returning nothing rather than a guess is the point,
// and resolving through the same walk the render uses is why a typed address and a clicked link
// cannot end up at different provisions.
func (c *Content) AnchorFor(a Address) string {
	if !a.Recognised {
		return ""
	}
	loc, ok := c.Find(a)
	if !ok {
		return ""
	}
	ids := c.anchorSet()
	switch {
	case loc.Provision != nil:
		return ids.provisionID(loc.Provision)
	case loc.Part != nil:
		return ids.partID(loc.Part)
	case loc.Appendix != nil:
		return ids.appendixID(loc.Appendix)
	}
	return ""
}

// Contents lists what the document has, in reading order, with the same ids the HTML render
// writes. A page that built its own table of contents by reading the rendered markup would be
// parsing our own output to recover facts the model already holds; a page that guessed the ids the
// same way twice would be doing the same thing worse.
func (c *Content) Contents() []Contents { return c.anchorSet().contents }

// childPath is the place of the n-th node under prefix, or of the n-th top-level node when there is
// no prefix.
func childPath(prefix string, n int) string {
	if prefix == "" {
		return strconv.Itoa(n)
	}
	return prefix + "-" + strconv.Itoa(n)
}

// partKindWord is the prefix a division's anchor carries, taken from what it is rather than from
// what the page happens to call it: a heading can be reworded, an anchor cannot.
func partKindWord(p *Part) string {
	switch p.Kind {
	case PartChapter:
		return "kapitel"
	case PartGroup:
		return "gruppe"
	case PartBook:
		return "bog"
	default:
		return "afsnit"
	}
}

// partLabel and provisionLabel are the words the render uses, taken from the same rule: what the
// document printed, and the mirror's own word for the kind only where the document printed none.
func partLabel(p *Part) string {
	if l := strings.TrimSpace(p.Printed); l != "" {
		return l
	}
	if h := strings.TrimSpace(p.Heading); h != "" {
		return h
	}
	kind := "afsnit"
	switch p.Kind {
	case PartChapter:
		kind = "kapitel"
	case PartGroup:
		kind = "provisionsgruppe"
	}
	return strings.TrimSpace(kind + " " + p.Number)
}

func provisionLabel(p *Provision) string {
	head := strings.TrimSpace(p.Printed)
	if head == "" && strings.TrimSpace(p.Address()) != "" {
		head = "§ " + p.Address()
	}
	switch {
	case head != "" && p.Heading != "":
		return head + " " + p.Heading
	case head != "":
		return head
	case p.Heading != "":
		return p.Heading
	default:
		// A provision the publisher gave neither a number nor a rubric is still text in the law, so
		// it earns a contents line; the mirror's own words say what it is rather than printing a
		// lone section sign and leaving the reader to guess which one.
		return "bestemmelse uden nummer"
	}
}

// slug keeps an id out of the trouble that punctuation makes in a URL fragment: §, periods and
// spaces all appear in what a document prints, and every one of them needs escaping in an href.
// Letters and digits survive, everything else becomes a single dash.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		default:
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
