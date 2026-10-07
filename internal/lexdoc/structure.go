package lexdoc

import (
	"fmt"
	"regexp"
	"strings"
)

// Changes to the document's grouping: chapters, afsnit and named groups, and the units that are
// text rather than provisions (the preamble, the title, an annex).
//
// One rule decides where a new part goes, and it is read off the printed page rather than off the
// publisher's nesting: a heading governs everything printed after it until the next heading of the
// same rank or higher. "Efter § 66 indsættes: Kapitel 8 a …" therefore puts the new chapter beside
// the chapter § 66 sits in, and whatever followed § 66 in that chapter now prints under the new
// heading, so it moves into the new chapter. Text order is never changed; only which heading the
// text sits under.

// rank orders the grouping units: an afsnit holds chapters, a chapter holds named groups.
func rank(k PartKind) int {
	switch k {
	case PartBook:
		return 4
	case PartAfsnit:
		return 3
	case PartChapter:
		return 2
	case PartGroup:
		return 1
	}
	return 0
}

// pathStep is one level on the way from the document root to a node: the list holding it, its
// index there, and the part that owns the list (nil for a top-level list).
type pathStep struct {
	list  *[]Node
	idx   int
	owner *Part
}

// pathTo returns the levels from the outermost list to the one holding the node match accepts,
// or nil when no node does.
func (c *Content) pathTo(match func(n *Node) bool) []pathStep {
	var path []pathStep
	var walk func(list *[]Node, owner *Part) bool
	walk = func(list *[]Node, owner *Part) bool {
		for i := range *list {
			n := &(*list)[i]
			path = append(path, pathStep{list, i, owner})
			if match(n) {
				return true
			}
			if n.Part != nil && walk(&n.Part.Order, n.Part) {
				return true
			}
			path = path[:len(path)-1]
		}
		return false
	}
	if walk(&c.Order, nil) {
		return path
	}
	for i := range c.Appendices {
		path = path[:0]
		if walk(&c.Appendices[i].Order, nil) {
			return path
		}
	}
	return nil
}

func isProvision(p *Provision) func(*Node) bool {
	return func(n *Node) bool { return n.Provision == p }
}

func isPart(p *Part) func(*Node) bool {
	return func(n *Node) bool { return n.Part == p }
}

// InsertParts places new parts next to the node anchor accepts, before or after it, and moves the
// text that now prints under the last of them into it. See the file comment for the rule.
//
// A later part of lower rank than the first is nested in the part before it: "Kapitel 4 a" followed
// by a group heading is one chapter with a group inside, not two siblings.
func (c *Content) InsertParts(anchor func(*Node) bool, before bool, parts []*Part) bool {
	if len(parts) == 0 {
		return false
	}
	path := c.pathTo(anchor)
	if path == nil {
		return false
	}
	var top []*Part
	for _, p := range parts {
		if n := len(top); n > 0 && rank(p.Kind) < rank(top[n-1].Kind) {
			top[n-1].Order = append(top[n-1].Order, Node{Part: p})
			continue
		}
		top = append(top, p)
	}
	r := rank(top[0].Kind)

	// The level the new part belongs at is the innermost one whose owner outranks it.
	level := 0
	for k := len(path) - 1; k >= 0; k-- {
		if path[k].owner == nil || rank(path[k].owner.Kind) > r {
			level = k
			break
		}
	}

	// Collect what now prints under the new heading, innermost first, which is reading order: the
	// rest of the anchor's own group comes before the rest of the chapter around it. Collection
	// stops at a heading of the same rank or higher, which keeps its own text.
	last := top[len(top)-1]
	var rest []Node
	cutAt := func(k int) int {
		if before && k == len(path)-1 {
			return path[k].idx
		}
		return path[k].idx + 1
	}
	for k := len(path) - 1; k >= level; k-- {
		list := *path[k].list
		from := cutAt(k)
		to := from
		for to < len(list) && (list[to].Part == nil || rank(list[to].Part.Kind) < r) {
			to++
		}
		rest = append(rest, list[from:to]...)
		kept := make([]Node, 0, len(list)-(to-from)+len(top))
		kept = append(kept, list[:from]...)
		if k == level {
			for _, p := range top {
				kept = append(kept, Node{Part: p})
			}
		}
		kept = append(kept, list[to:]...)
		*path[k].list = kept
	}
	last.Order = append(last.Order, rest...)
	return true
}

// DeletePart repeals a chapter, an afsnit or a group, with everything it holds: "Kapitel 3 e
// ophæves" repeals the provisions printed under it too.
func (c *Content) DeletePart(p *Part) bool {
	path := c.pathTo(isPart(p))
	if path == nil {
		return false
	}
	at := path[len(path)-1]
	list := *at.list
	kept := make([]Node, 0, len(list)-1)
	kept = append(kept, list[:at.idx]...)
	kept = append(kept, list[at.idx+1:]...)
	*at.list = kept
	return true
}

// ReplacePart swaps a part's heading and content for a new one's, keeping its place and number.
func (c *Content) ReplacePart(target, with *Part) bool {
	if target == nil || with == nil || c.pathTo(isPart(target)) == nil {
		return false
	}
	target.Heading = with.Heading
	if with.Printed != "" {
		target.Printed = with.Printed
	}
	target.Order = cloneNodes(with.Order)
	return true
}

// DeleteAppendix repeals one annex.
func (c *Content) DeleteAppendix(ap *Appendix) bool {
	for i := range c.Appendices {
		if &c.Appendices[i] == ap {
			c.Appendices = append(c.Appendices[:i:i], c.Appendices[i+1:]...)
			return true
		}
	}
	return false
}

// reWholeUnit is a change naming a whole chapter or annex and nothing inside it: "Kapitel 3 e
// ophæves", "Bilag 2 affattes således". The address alone cannot say this. "Pkt. 3 i bilag 1-7
// ophæves" resolves to bilag 1, and "Bilag 3, nr. 2, affattes" to bilag 3; repealing or rewriting
// the whole annex for either would be a confident corruption of law the change never touched.
var reWholeUnit = regexp.MustCompile(`(?i)^\s*(?:bilag|kapitel|afsnit)\s+[0-9ivxlcdm]+(?:\s*[a-zæøå](?:[^\p{L}]|$))?\s*(?:ophæves|affattes|udgår)`)

// namesWholeUnit reports whether a change's own wording names a whole chapter, afsnit or annex.
func namesWholeUnit(definition string) bool {
	return reWholeUnit.MatchString(definition) || len(unitList(definition)) > 0
}

// rePlacedBefore is an insertion that goes in front of its address: "Før § 21 indsættes".
// "Efter § 7 c indsættes før overskriften før § 8" starts with its real anchor and is after.
var rePlacedBefore = regexp.MustCompile(`(?i)^\s*(?:i\s+[^,]*,\s*)?før\s`)

// placedBefore reports whether an insertion's wording puts it in front of the provision it names.
func placedBefore(definition string) bool { return rePlacedBefore.MatchString(definition) }

// applyUnit carries out the changes aimed at a unit that is not a provision's text: the preamble,
// the title, an annex or a chapter as a whole, and the insertion of new chapters and headings. It
// reports whether the change was one of those, applied or refused; anything else is left to the
// provision-level cases.
func (c *Content) applyUnit(e Edit, loc Located, found bool, reject func(string), apply func(where, note string)) bool {
	if e.Address.Heading && e.Kind != EditInsertProvision {
		return false // a heading change, which the provision-level cases own
	}
	switch e.Kind {
	case EditReplaceProvision:
		switch {
		case loc.Preamble, loc.Title:
			segs := e.blockText()
			if len(segs) == 0 {
				reject(fmt.Sprintf("the new %s carries no text", loc))
				return true
			}
			if loc.Preamble {
				c.Preamble = segs
			} else {
				c.Title = segs
			}
			apply(loc.String(), "")
			return true
		case loc.Appendix != nil && e.Address.Paragraph == "" && reAnnexText.MatchString(e.Definition):
			// "I bilag 2 affattes bilagsteksten således": the annex's words, not the map or table
			// it prints them with. Placed only where the annex has one paragraph of them.
			segs := e.blockText()
			at := -1
			for i, sg := range loc.Appendix.Segments {
				if len([]rune(Squeeze(sg.Text))) > 40 {
					if at >= 0 {
						reject(fmt.Sprintf("%s prints more than one paragraph of text", loc))
						return true
					}
					at = i
				}
			}
			if at < 0 || len(segs) == 0 {
				reject(fmt.Sprintf("%s prints no paragraph of text to replace", loc))
				return true
			}
			ap := loc.Appendix
			ap.Segments = append(append(append([]Segment(nil), ap.Segments[:at]...), segs...), ap.Segments[at+1:]...)
			apply(loc.String(), "the annex's one paragraph of text")
			return true
		case loc.Appendix != nil && e.Address.Paragraph == "":
			if !namesWholeUnit(e.Definition) {
				reject(fmt.Sprintf("the change names a part of %s, which this model does not address", loc))
				return true
			}
			with := e.NewAppendix
			if with == nil {
				// "Bilag 5 affattes således: »Instrumenter 1) …«" brings the annex as text: its
				// lines are the annex, after the "Bilag 5" header when it prints one.
				segs := e.blockText()
				if len(segs) == 0 {
					reject(fmt.Sprintf("the replacement for %s carries no annex", loc))
					return true
				}
				lines := make([]string, 0, len(segs))
				for _, sg := range segs {
					lines = append(lines, sg.Text)
				}
				if ap := AnnexFromLines(lines, Atoi(loc.Appendix.Number), true); ap != nil {
					with = ap
				} else {
					with = &Appendix{Number: loc.Appendix.Number, Label: loc.Appendix.Label, Segments: segs}
				}
			}
			number := loc.Appendix.Number
			*loc.Appendix = cloneAppendix(*with)
			if loc.Appendix.Number == "" {
				loc.Appendix.Number = number
			}
			apply(loc.String(), "")
			return true
		case loc.Part != nil:
			if m := reChapterRange.FindStringSubmatch(e.Definition); m != nil && len(e.NewParts) > 0 && loc.Part.Kind == PartChapter {
				c.replaceChapterRange(loc.Part, m[1], m[2], e.NewParts, reject, apply)
				return true
			}
			if e.Address.Book != 0 && e.Address.AfsnitTo > e.Address.Afsnit {
				// "Fjerde bog, tredje og fjerde afsnit, ophæves, og i stedet indsættes": the run goes,
				// and the new afsnit takes the first one's place.
				var gone []*Part
				for k := e.Address.Afsnit + 1; k <= e.Address.AfsnitTo; k++ {
					a := e.Address
					a.Afsnit, a.AfsnitTo, a.Heading = k, 0, false
					l, found := c.Find(a)
					if !found || l.Part == nil {
						reject(fmt.Sprintf("the law has no afsnit %d of bog %d", k, e.Address.Book))
						return true
					}
					gone = append(gone, l.Part)
				}
				if len(e.NewParts) == 0 || !c.ReplacePart(loc.Part, e.NewParts[0]) {
					reject(fmt.Sprintf("the replacement for %s carries no afsnit", loc))
					return true
				}
				for _, p := range gone {
					if !c.DeletePart(p) {
						reject("could not repeal afsnit " + p.Number)
						return true
					}
				}
				for _, p := range e.NewParts[1:] {
					if !c.InsertParts(isPart(loc.Part), false, []*Part{clonePart(p)}) {
						reject("could not place the new afsnit")
						return true
					}
				}
				apply(loc.String(), "")
				return true
			}
			if !namesWholeUnit(e.Definition) {
				reject(fmt.Sprintf("the change names a part of %s, which this model does not address", loc))
				return true
			}
			if len(e.NewParts) == 0 && len(e.NewProvisions) > 0 {
				// "Kapitel 9 b affattes således: »Kapitel 9 b § 92. …«" arrives as the chapter's
				// provisions, its label and heading as lines in front of them.
				var order []Node
				for _, p := range e.NewProvisions {
					cp := cloneProvision(*p)
					if reChapterLead.MatchString(strings.TrimSpace(cp.Heading)) {
						cp.Heading = "" // the chapter's own label, read as the provision's rubric
					}
					order = append(order, Node{Provision: &cp})
				}
				loc.Part.Order = order
				if h := donatedChapterHeading(e.NewText); h != "" {
					loc.Part.Heading = h
				}
				apply(loc.String(), "")
				return true
			}
			if len(e.NewParts) == 0 || !c.ReplacePart(loc.Part, e.NewParts[0]) {
				reject(fmt.Sprintf("the replacement for %s carries no chapter", loc))
				return true
			}
			apply(loc.String(), "")
			return true
		}
	case EditDeleteProvision:
		switch {
		case loc.Appendix != nil && e.Address.Paragraph == "", loc.Part != nil:
			if !namesWholeUnit(e.Definition) {
				reject(fmt.Sprintf("the change names a part of %s, which this model does not address", loc))
				return true
			}
			removed := false
			if loc.Part != nil {
				removed = c.DeletePart(loc.Part)
			} else {
				removed = c.DeleteAppendix(loc.Appendix)
			}
			if !removed {
				reject(fmt.Sprintf("could not remove %s", loc))
				return true
			}
			apply(loc.String(), "")
			return true
		}
	case EditInsertProvision:
		if len(e.NewParts) == 0 {
			return false
		}
		var anchor func(*Node) bool
		before := placedBefore(e.Definition)
		note := ""
		switch {
		case found && loc.Provision != nil:
			// A chapter goes beside the provision, whichever part of it the address named.
			anchor = isProvision(loc.Provision)
		case found && loc.Part != nil:
			anchor = isPart(loc.Part)
		default:
			p, after, why := c.insertionAnchor(Located{}, false, e.Address)
			if p == nil {
				reject(fmt.Sprintf("address %q is absent and no positional anchor could be found", e.Address))
				return true
			}
			anchor, before, note = isProvision(p), !after, why
		}
		parts := make([]*Part, len(e.NewParts))
		for i, p := range e.NewParts {
			parts[i] = clonePart(p)
		}
		if !c.InsertParts(anchor, before, parts) {
			reject("could not place the new chapter or heading")
			return true
		}
		where := loc.String()
		if where == "" {
			where = e.Address.String()
		}
		apply(where, note)
		return true
	}
	return false
}

// blockText is a change's new text as one block: what it brought as text, or its loose lines.
func (e Edit) blockText() []Segment {
	if len(e.NewText) > 0 {
		return cloneSegments(e.NewText)
	}
	var segs []Segment
	for _, line := range e.NewProse {
		segs = append(segs, Segment{Text: line, Break: true})
	}
	if len(segs) == 0 && strings.TrimSpace(e.New) != "" {
		segs = []Segment{{Text: Squeeze(strings.Trim(e.New, "»« ")), Break: true}}
	}
	return segs
}

func clonePart(p *Part) *Part {
	return cloneNodes([]Node{{Part: p}})[0].Part
}

func cloneAppendix(a Appendix) Appendix {
	a.Order = cloneNodes(a.Order)
	a.Segments = cloneSegments(a.Segments)
	a.Unmapped = append([]Unmapped(nil), a.Unmapped...)
	return a
}

// Validate checks the structure every edit has to preserve: each stykke and item covers a range
// of its provision's own text, stykker follow each other in reading order without overlapping,
// and an item sits inside its stykke. A consolidation that breaks one of these prints the wrong
// words under a citation, which no count of applied and refused changes can show.
func (c *Content) Validate() error {
	var err error
	c.eachProvision(func(p *Provision) {
		if err != nil {
			return
		}
		prevTo := 0
		for _, st := range p.Stykker {
			if st.Span.From < prevTo || st.Span.From > st.Span.To || st.Span.To > len(p.Segments) {
				err = fmt.Errorf("§ %s stk. %d spans %v of %d segments after %d", p.Address(), st.Nr, st.Span, len(p.Segments), prevTo)
				return
			}
			prevTo = st.Span.To
			itemTo, parent := st.Span.From, Span{}
			for _, it := range st.Items {
				nested := it.Litra != "" && it.Nr == 0 && it.Span.From >= parent.From && it.Span.To <= parent.To && parent.To > parent.From
				if !nested && it.Span.From < itemTo || it.Span.From > it.Span.To || it.Span.To > st.Span.To {
					err = fmt.Errorf("§ %s stk. %d nr. %d%s spans %v outside stk. %v", p.Address(), st.Nr, it.Nr, it.Litra, it.Span, st.Span)
					return
				}
				if !nested {
					itemTo, parent = it.Span.To, it.Span
				}
			}
		}
	})
	return err
}

// reInsideUnit is wording that names something inside a subsection or an item that addresses do
// not resolve: a sentence ("3. pkt."), a table or a schedule.
var reInsideUnit = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(?:[0-9]+\.\s*(?:og\s+[0-9]+\.\s*)?(?:pkt|punktum)|skema|tabel)`)

// namesPartOfUnit reports a change whose own words name a part of the unit its address resolves
// to, before or after its verb: "§ 7, stk. 8, 3. pkt., affattes", "affattes skemaet for GRUNDFAG
// således". Only the sentence up to its colon or first quotation is read: a sentence the change
// brings may mention "pkt." freely.
func namesPartOfUnit(definition string) bool {
	head := definition
	if i := strings.IndexAny(head, "»:"); i >= 0 {
		head = head[:i]
	}
	return reInsideUnit.MatchString(head)
}

// partNamedElsewhere reports a change listing several places whose sentence belongs to another of
// them: in "§ 62 a, § 62 b og § 62 c, stk. 1, 2. pkt., ophæves" the "2. pkt." is § 62 c's, and § 62 a
// is repealed whole. The address was read with its sentence wherever the sentence was its own.
func partNamedElsewhere(definition string, a Address) bool {
	if a.Sentence != 0 || strings.Contains(strings.ToLower(definition), "skema") || strings.Contains(strings.ToLower(definition), "tabel") {
		return false
	}
	places := ParseAddresses(definition)
	self, other := false, false
	for _, p := range places {
		switch {
		case p.Sentence != 0:
			other = true
		case p.String() == a.String():
			self = true
		}
	}
	return self && other
}

// donatedChapterHeading is the heading printed under a chapter's label in new text: the line after
// "Kapitel 9 b" when it is not already a provision.
func donatedChapterHeading(lines []Segment) string {
	for i := 0; i+1 < len(lines); i++ {
		if reChapterLead.MatchString(strings.TrimSpace(strings.Trim(lines[i].Text, "»« "))) {
			next := strings.TrimSpace(strings.Trim(lines[i+1].Text, "»« "))
			if next != "" && !reProvisionLead.MatchString(next) {
				return next
			}
			return ""
		}
	}
	return ""
}

// reChapterRange is a run of chapters replaced together: "Kapitel 82-84 ophæves, og i stedet
// indsættes:", "Kapitel 3-5 affattes således:".
var reChapterRange = regexp.MustCompile(`(?i)^\s*kapitel\s+([0-9]+(?:\s*[a-zæøå])?)\s*[-–]\s*([0-9]+(?:\s*[a-zæøå])?)\s*,?\s+(?:ophæves\s*,\s*og\s+i\s+stedet\s+indsættes|affattes\s+således)`)

// replaceChapterRange puts the chapters a change brings in place of the run of chapters it names:
// the first takes the first's place, the others of the run go, and the rest of the new ones follow.
func (c *Content) replaceChapterRange(first *Part, from, to string, with []*Part, reject func(string), apply func(where, note string)) {
	lo, hi := Atoi(FirstNumber(from)), Atoi(FirstNumber(to))
	if hi <= lo || hi-lo > 30 || strings.ContainsAny(from+to, "abcdefghijklmnopqrstuvwxyzæøå") {
		reject("the chapters named run " + from + "-" + to + ", which this model does not read as a range")
		return
	}
	var gone []*Part
	for n := lo + 1; n <= hi; n++ {
		p := c.part(PartChapter, itoa(n))
		if p == nil {
			reject(fmt.Sprintf("the law has no chapter %d of the run %s-%s", n, from, to))
			return
		}
		gone = append(gone, p)
	}
	for _, p := range gone {
		if !c.DeletePart(p) {
			reject(fmt.Sprintf("could not repeal chapter %s", p.Number))
			return
		}
	}
	if !c.ReplacePart(first, with[0]) {
		reject("could not replace chapter " + from)
		return
	}
	if len(with) > 1 {
		rest := make([]*Part, 0, len(with)-1)
		for _, p := range with[1:] {
			rest = append(rest, clonePart(p))
		}
		if !c.InsertParts(isPart(first), false, rest) {
			reject("could not place the chapters after chapter " + from)
			return
		}
	}
	apply("kapitel "+from+"-"+to, "")
}

// reAnnexText is a change to an annex's words as a whole: "affattes bilagsteksten således".
var reAnnexText = regexp.MustCompile(`(?i)\baffattes\s+bilagsteksten\b`)

// PartHoldsProvisions reports whether a part prints provisions, at any depth.
func PartHoldsProvisions(p *Part) bool {
	return p != nil && len(p.provisions(nil)) > 0
}
