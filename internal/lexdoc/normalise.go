package lexdoc

import (
	"regexp"
	"strings"
)

// Structure from printed labels.
//
// A provision's subsections and conditions are printed in its text whatever the markup does with
// them: "Stk. 2." opens a subsection, "3)" and "b)" a condition. The register's XML marks them up;
// the site's HTML often does not, and the oldest of it marks up nothing at all, so a provision
// arrived as one run of text that "§ 130, stk. 2" and "nr. 5, litra a" could not be resolved in —
// measured as 132 refused changes in HTML-bodied laws over the 799-law census. Normalise reads the
// labels where the markup gave no structure, so every front end hands the engine the same shape.

// reStkLabelLead is a subsection label at the head of a segment: "Stk. 2.", with whatever space.
var reStkLabelLead = regexp.MustCompile(`(?i)^[\s\x{a0}]*stk\.?[\s\x{a0}]*([0-9]+)[\s\x{a0}]*\.`)

// Normalise completes a provision's structure from its printed labels: subsections where the markup
// gave none, the unlabelled stk. 1 in front of a first subsection printed "Stk. 2.", and conditions
// inside subsections that carry none. Structure the markup did give is kept as it is.
func (p *Provision) Normalise() {
	from := p.bodyStart()
	if from >= len(p.Segments) {
		return
	}
	// Labels inside quotation marks are someone else's: an amending act's provision quotes the
	// "Stk. 6." and "4)" of the law it changes, and reading them as its own made one provision of
	// change list into forty units.
	quoted := quotedSegments(p.Segments)
	for _, seg := range p.Segments {
		if seg.Note == "" && opensList(seg.Text) {
			// A change list: everything after its preamble is another law's text, which the XML
			// does not even put in quotation marks. Its own structure is its numbered changes, and
			// a later act cites them that way: "§ 78, nr. 30, ophæves" repeals change 30.
			p.changeItems(quoted)
			return
		}
	}
	switch {
	case len(p.Stykker) == 0:
		// One subsection per printed label, and the text in front of the first label is stk. 1:
		// Danish prints no "Stk. 1.". With no label at all the provision is one subsection, which
		// is how the XML models every provision.
		starts := []int{}
		want := 2
		for i := from; i < len(p.Segments); i++ {
			m := reStkLabelLead.FindStringSubmatch(p.Segments[i].Text)
			if m == nil || quoted[i] {
				continue
			}
			if i == from && Atoi(m[1]) != 1 && len(starts) == 0 {
				want = Atoi(m[1])
			}
			if Atoi(m[1]) != want {
				// Printed numbers that do not run in order are not this provision's subsections.
				starts = nil
				break
			}
			starts = append(starts, i)
			want++
		}
		if len(starts) == 0 || starts[0] > from {
			starts = append([]int{from}, starts...)
		}
		for k, at := range starts {
			to := len(p.Segments)
			if k+1 < len(starts) {
				to = starts[k+1]
			}
			p.Stykker = append(p.Stykker, Stykke{Nr: k + 1, Span: Span{From: at, To: to}})
		}
	case p.firstPrintedStykke() == 2 && p.Stykker[0].Span.From > from:
		// The opening text was left out of the first subsection the markup made.
		p.Stykker = append([]Stykke{{Span: Span{From: from, To: p.Stykker[0].Span.From}}}, p.Stykker...)
		for i := range p.Stykker {
			p.Stykker[i].Nr = i + 1
		}
	}
	p.splitPrintedStykker(from, quoted)
	// A list a reader left outside the subsection that introduces it belongs to that subsection:
	// "Stk. 2. I følgende tilfælde gives bindende svar: 1) … 2) …" arrived as stk. 2 without its
	// conditions, which then belonged to nothing and could not be cited.
	for i := range p.Stykker {
		end := len(p.Segments)
		if i+1 < len(p.Stykker) {
			end = p.Stykker[i+1].Span.From
		}
		st := &p.Stykker[i]
		if st.Span.To < end && !quoted[st.Span.To] && len(ItemsIn(p.Segments, Span{From: st.Span.To, To: st.Span.To + 1})) == 1 {
			st.Span.To = end
		}
	}
	for i := range p.Stykker {
		if len(p.Stykker[i].Items) == 0 {
			p.Stykker[i].Items = itemsOutsideQuotes(p.Segments, p.Stykker[i].Span, quoted)
		}
		p.Stykker[i].Items = withLetteredSubItems(p.Segments, p.Stykker[i].Items)
	}
}

// reChangeNumber at the head of a segment: "30." standing alone or opening its change. "2. pkt.:",
// the end of "indsættes som 2. pkt.:" on a line of its own, is not one: a change opens with a
// capital or a §, never a lowercase word.
var reChangeNumberLead = regexp.MustCompile(`^\s*([0-9]+)\.(?:\s*$|\s+[^\p{Ll}])`)

// changeItems makes a change list's numbered changes its items, in the subsection that holds them.
// Only numbers outside quotation that run in sequence from 1 count: new text the list quotes may
// print numbers of its own.
func (p *Provision) changeItems(quoted []bool) {
	// A subsection opening at a quoted "»Stk. 4." is the amended law's, not this provision's: the
	// site's HTML made one of § 3 in lov nr. 526 of 2006, and its changes 1-5 belonged to nothing.
	own := p.Stykker[:0:0]
	for _, st := range p.Stykker {
		if st.Span.From < len(p.Segments) && (quoted[st.Span.From] || strings.HasPrefix(strings.TrimSpace(p.Segments[st.Span.From].Text), "»")) {
			continue
		}
		own = append(own, st)
	}
	if len(own) < len(p.Stykker) {
		p.Stykker = nil
		for i, st := range own {
			if i+1 < len(own) {
				st.Span.To = own[i+1].Span.From
			} else {
				st.Span.To = len(p.Segments)
			}
			st.Nr = i + 1
			p.Stykker = append(p.Stykker, st)
		}
		if len(p.Stykker) > 0 && p.Stykker[0].Span.From > p.bodyStart() {
			p.Stykker[0].Span.From = p.bodyStart()
		}
	}
	if len(p.Stykker) == 0 {
		from := p.bodyStart()
		if from >= len(p.Segments) {
			return
		}
		p.Stykker = []Stykke{{Nr: 1, Span: Span{From: from, To: len(p.Segments)}}}
	}
	for si := range p.Stykker {
		st := &p.Stykker[si]
		var items []Item
		next := 1
		for i := st.Span.From; i < st.Span.To; i++ {
			if quoted[i] {
				continue
			}
			m := reChangeNumberLead.FindStringSubmatch(p.Segments[i].Text)
			if m == nil || Atoi(m[1]) != next {
				continue
			}
			if n := len(items); n > 0 {
				items[n-1].Span.To = i
			}
			items = append(items, Item{Nr: next, Label: m[1] + ".", Span: Span{From: i, To: st.Span.To}})
			next++
		}
		if len(items) > 0 {
			st.Items = items
		}
	}
}

// withLetteredSubItems adds the lettered conditions printed inside a numbered one — "3) … a) …
// b) …" — after it in the list, where "nr. 3, litra e" looks for them. The XML marks only the first
// level up, and "§ 1, stk. 2, nr. 3, litra e" was refused against a condition that prints its e).
func withLetteredSubItems(segs []Segment, items []Item) []Item {
	var out []Item
	for i, it := range items {
		out = append(out, it)
		if it.Litra != "" || i+1 < len(items) && items[i+1].Litra != "" {
			continue
		}
		inner := Span{From: it.Span.From + 1, To: it.Span.To}
		for _, sub := range ItemsIn(segs, inner) {
			if sub.Litra != "" && sub.Nr == 0 {
				out = append(out, sub)
			}
		}
	}
	return out
}

// quotedSegments marks the segments that begin inside a quotation.
func quotedSegments(segs []Segment) []bool {
	out := make([]bool, len(segs))
	depth := 0
	for i, s := range segs {
		if s.Note != "" {
			continue // a footnote's quotation marks are its own
		}
		out[i] = depth > 0
		if depth > 0 && strings.HasPrefix(strings.TrimSpace(s.Text), "»") {
			// "»Kapitel 2 c" then "»§ 5 h. …": the heading's quotation never closed, and a line
			// opening one starts a quotation rather than nesting in it. Otherwise the act's later
			// changes all read as quoted text, and the change list ended at change 11.
			depth = 0
		}
		depth += strings.Count(s.Text, "»") - strings.Count(s.Text, "«")
		if depth < 0 {
			depth = 0
		}
	}
	return out
}

// itemsOutsideQuotes is ItemsIn over the segments that are this provision's own text.
func itemsOutsideQuotes(segs []Segment, sp Span, quoted []bool) []Item {
	for i := sp.From; i < sp.To && i < len(segs); i++ {
		if segs[i].Note != "" {
			continue // a footnote quoting an amendment does not make the subsection quote one
		}
		if quoted[i] || strings.Contains(segs[i].Text, "»") && !closedQuote(segs[i].Text) {
			return nil // a subsection that quotes is a change list, not conditions
		}
	}
	return ItemsIn(segs, sp)
}

// closedQuote reports a line whose quotation marks open and close on it: a term quoted inside a
// definition, "OTC (»Over The Counter«):", which is not new text a change list brings.
func closedQuote(s string) bool {
	return strings.Count(s, "»") == strings.Count(s, "«") && !reQuotedChange.MatchString(s)
}

// reQuotedChange is a line that changes words it quotes.
var reQuotedChange = regexp.MustCompile(`(?i)\b(ændres|udgår|indsættes|affattes|erstattes)\b`)

// bodyStart is the first segment after the provision's own printed number.
func (p *Provision) bodyStart() int {
	from := 0
	for from < len(p.Segments) {
		t := strings.TrimSpace(p.Segments[from].Text)
		if t != "" && t != strings.TrimSpace(p.Printed) {
			break
		}
		from++
	}
	return from
}

// firstPrintedStykke is the number the first subsection prints, from its label or its first words.
func (p *Provision) firstPrintedStykke() int {
	first := p.Stykker[0]
	printed := first.Label
	if printed == "" && first.Span.From < len(p.Segments) {
		printed = p.Segments[first.Span.From].Text
	}
	if m := reStkLabelLead.FindStringSubmatch(printed); m != nil {
		return Atoi(m[1])
	}
	return 0
}

// renumberItems numbers a subsection's numbered conditions by position. Lettered ones keep their
// letter: "a)" under "5)" is not the sixth condition.
func renumberItems(items []Item) {
	n := 0
	for j := range items {
		switch {
		case items[j].Litra != "":
		case items[j].Suffix != "":
			items[j].Nr = n // "5 a)" belongs to the number before it
		default:
			n++
			items[j].Nr = n
		}
	}
}

// SplitRunawayProvisions recovers provisions a reader left inside the one before them.
//
// Broken markup can nest the rest of a law inside one provision: toldloven's HTML (A20050086729)
// leaves a <DIV> open before every paragraph, and its § 3 arrived holding 565 segments — every
// later provision, chapter heading and all. The printed labels are still there, so they are read
// again: a segment opening with the next provision's number starts that provision, and a line that
// is only "Kapitel N" starts a chapter whose next line is its heading. Only numbers that run
// forward are taken, so a provision quoting an earlier one is not cut, and text inside quotation
// marks or in a change list is never cut at all.
func (c *Content) SplitRunawayProvisions() {
	var walk func(list *[]Node)
	walk = func(list *[]Node) {
		for i := 0; i < len(*list); i++ {
			n := (*list)[i]
			if n.Part != nil {
				walk(&n.Part.Order)
				continue
			}
			if n.Provision != nil {
				c.splitProvision(n.Provision)
			}
		}
	}
	walk(&c.Order)
}

// splitProvision cuts p at the provisions and chapters printed inside it and places them after it.
func (c *Content) splitProvision(p *Provision) {
	for _, seg := range p.Segments {
		if opensList(seg.Text) {
			return
		}
	}
	quoted := quotedSegments(p.Segments)
	curNum, curLitra := Atoi(p.Number), p.Litra
	type piece struct {
		at      int
		prov    *Provision
		chapter *Part
	}
	var pieces []piece
	for k := p.bodyStart() + 1; k < len(p.Segments); k++ {
		if quoted[k] {
			continue
		}
		text := strings.TrimSpace(p.Segments[k].Text)
		if m := reChapterLead.FindStringSubmatch(text); m != nil && strings.EqualFold(m[1], "kapitel") {
			pieces = append(pieces, piece{at: k, chapter: &Part{Kind: PartChapter, Printed: text, Number: NumberWithLitra(text)}})
			continue
		}
		m := reProvisionLead.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		num, litra := Atoi(m[1]), strings.ToLower(m[2])
		if num < curNum || num == curNum && litra <= curLitra || num > curNum+50 {
			continue // not the next provision: a citation, or a number that would skip half the law
		}
		curNum, curLitra = num, litra
		pieces = append(pieces, piece{at: k, prov: &Provision{Number: m[1], Litra: litra, Printed: reProvisionLead.FindString(text)}})
	}
	if len(pieces) == 0 {
		return
	}
	segs := p.Segments
	for k, pc := range pieces {
		to := len(segs)
		if k+1 < len(pieces) {
			to = pieces[k+1].at
		}
		switch {
		case pc.prov != nil:
			pc.prov.Segments = cloneSegments(segs[pc.at:to])
			pc.prov.Normalise()
		case pc.chapter != nil && pc.at+1 < to && !reProvisionLead.MatchString(strings.TrimSpace(segs[pc.at+1].Text)):
			pc.chapter.Heading = Squeeze(segs[pc.at+1].Text)
		}
	}
	// The provision keeps what came before the first cut; its own structure is read again.
	cut := pieces[0].at
	p.Segments = segs[:cut:cut]
	p.Stykker = nil
	p.Normalise()

	prev := p
	var open *Part
	for _, pc := range pieces {
		switch {
		case pc.chapter != nil:
			if !c.InsertParts(isProvision(prev), false, []*Part{pc.chapter}) {
				return
			}
			open = pc.chapter
		case open != nil:
			open.Order = append(open.Order, Node{Provision: pc.prov})
			prev = pc.prov
		default:
			if !c.InsertProvisionAfter(prev, pc.prov) {
				return
			}
			prev = pc.prov
		}
	}
}

// reInlineStykke is a subsection label printed in the middle of a segment, after the sentence
// before it ends: "… i forbindelse med projektet. Stk. 3. Afsluttende udbetaling …".
var reInlineStykke = regexp.MustCompile(`[.:;][\s\x{a0}]+(Stk\.[\s\x{a0}]*([0-9]+)[\s\x{a0}]*\.)[\s\x{a0}]`)

// SplitInlineStykker gives a subsection its own place when the source printed its label inside the
// text of the one before it: § 21 read as two subsections whose second ran on through "Stk. 3." and
// "Stk. 4.", so "§ 21, stk. 4, affattes således" had nothing to replace. Only labels that continue
// the provision's numbering are cut at, outside quotation and footnotes.
func (c *Content) SplitInlineStykker() {
	var walk func(list []Node)
	walk = func(list []Node) {
		for _, n := range list {
			switch {
			case n.Part != nil:
				walk(n.Part.Order)
			case n.Provision != nil:
				n.Provision.splitInlineStykker()
			}
		}
	}
	walk(c.Order)
}

// SplitInlineConditions cuts a line of new text printing several conditions, "1) … 2) … 3) …",
// into a line for each, and reads the subsection's conditions again. A provision a change brought
// as a whole printed aktivloven § 72, stk. 1, nr. 1-6 on one line, and lov nr. 2218 of 2020's
// change to nr. 2 found no nr. 2. No base prints conditions that way; only new text does.
func (c *Content) SplitInlineConditions() {
	c.eachProvision(func(p *Provision) { p.splitInlineConditions() })
}

func (p *Provision) splitInlineConditions() {
	if len(p.Stykker) == 0 {
		return
	}
	quoted := quotedSegments(p.Segments)
	noItems := map[int]bool{} // segments of subsections that read no conditions
	for _, st := range p.Stykker {
		if len(st.Items) == 0 {
			for i := st.Span.From; i < st.Span.To; i++ {
				noItems[i] = true
			}
		}
	}
	var segs []Segment
	at := make([]int, len(p.Segments)+1) // where each old segment starts now
	split := false
	for i, seg := range p.Segments {
		at[i] = len(segs)
		if seg.Note == "" && !quoted[i] && !strings.ContainsAny(seg.Text, "»«") {
			parts := conditionLines(seg)
			if len(parts) <= 1 && noItems[i] {
				// "… som nævnt i nr. 1 og 2: 1) Elektricitet … 2) …": the conditions after the
				// subsection's opening words, in a subsection that reads none.
				if m := reColonList.FindStringIndex(seg.Text); m != nil {
					head, rest := seg, seg
					head.Text = strings.TrimRight(seg.Text[:m[0]+1], " ")
					rest.Text = " " + strings.TrimLeft(seg.Text[m[0]+1:], " ")
					if more := conditionLines(rest); len(more) > 1 {
						parts = append([]Segment{head}, more...)
					}
				}
			}
			if len(parts) > 1 {
				segs = append(segs, parts...)
				split = true
				continue
			}
		}
		segs = append(segs, seg)
	}
	if !split {
		return
	}
	at[len(p.Segments)] = len(segs)
	p.Segments = segs
	for i := range p.Stykker {
		st := &p.Stykker[i]
		st.Span = Span{From: at[st.Span.From], To: at[st.Span.To]}
	}
	for i := range p.Stykker {
		// The line of conditions a donated provision printed after its first subsection's words
		// fell between that subsection and the next: it is the first's.
		st := &p.Stykker[i]
		next := len(p.Segments)
		if i+1 < len(p.Stykker) {
			next = p.Stykker[i+1].Span.From
		}
		if st.Span.To < next {
			st.Span.To = next
		}
		st.Items = withLetteredSubItems(p.Segments, ItemsIn(p.Segments, st.Span))
	}
	renumberAllItems(p)
}

// reColonList is a run of conditions opening after a colon: ": 1) ".
var reColonList = regexp.MustCompile(`:\s+1\)\s`)

// renumberAllItems numbers every subsection's conditions by position.
func renumberAllItems(p *Provision) {
	for i := range p.Stykker {
		renumberItems(p.Stykker[i].Items)
	}
}

func (p *Provision) splitInlineStykker() {
	if len(p.Stykker) == 0 {
		return
	}
	for _, seg := range p.Segments {
		if seg.Note == "" && opensList(seg.Text) {
			return // a change list prints another law's subsections, unquoted
		}
	}
	quoted := quotedSegments(p.Segments)
	for si := 0; si < len(p.Stykker); si++ {
		st := p.Stykker[si]
		want := len(p.Stykker) + 1 // only the next label after the last subsection
		if si != len(p.Stykker)-1 {
			continue
		}
		for i := st.Span.From; i < st.Span.To; i++ {
			seg := p.Segments[i]
			if seg.Note != "" || quoted[i] || strings.ContainsAny(seg.Text, "»«") {
				continue
			}
			at := -1 // the segment the new subsection starts at
			if i > st.Span.From {
				if m := reStkLabelLead.FindStringSubmatch(seg.Text); m != nil && Atoi(m[1]) == want {
					at = i // its label is a segment of its own
				}
			}
			if at < 0 {
				var m []int
				// The pattern needs a literal "Stk."; most segments have none.
				if strings.Contains(seg.Text, "Stk.") {
					m = reInlineStykke.FindStringSubmatchIndex(seg.Text)
				}
				if m == nil || Atoi(seg.Text[m[4]:m[5]]) != want {
					continue
				}
				// Cut the segment where the label starts; the label opens the new subsection.
				head := Segment{Text: strings.TrimRight(seg.Text[:m[2]], " \u00a0"), Break: true}
				tail := Segment{Text: seg.Text[m[2]:], Break: seg.Break, Note: seg.Note}
				segs := make([]Segment, 0, len(p.Segments)+1)
				segs = append(segs, p.Segments[:i]...)
				segs = append(segs, head, tail)
				segs = append(segs, p.Segments[i+1:]...)
				p.Segments = segs
				for k := range p.Stykker {
					shiftInserted(&p.Stykker[k].Span, i+1, 1)
					for j := range p.Stykker[k].Items {
						shiftInserted(&p.Stykker[k].Items[j].Span, i+1, 1)
					}
				}
				at = i + 1
			}
			last := &p.Stykker[len(p.Stykker)-1]
			newSt := Stykke{Nr: want, Span: Span{From: at, To: last.Span.To}}
			last.Span.To = at
			var keep, moved []Item
			for _, it := range last.Items {
				if it.Span.From >= at {
					moved = append(moved, it)
					continue
				}
				if it.Span.To > at {
					it.Span.To = at // the condition ends where the next subsection's label begins
				}
				keep = append(keep, it)
			}
			last.Items, newSt.Items = keep, moved
			p.Stykker = append(p.Stykker, newSt)
			quoted = quotedSegments(p.Segments)
			si = len(p.Stykker) - 2 // look again in the new last subsection
			break
		}
	}
}

// OpensWithStykkeLabel reports text whose first line is a subsection's label, "Stk. 2.".
func OpensWithStykkeLabel(segs []Segment) bool {
	for _, sg := range segs {
		if t := strings.TrimSpace(sg.Text); t != "" {
			return reStkLabelLead.MatchString(t)
		}
	}
	return false
}

// reStkLabelAlone is a subsection label printed as a line of its own, its full stop left off:
// "Stk. 2".
var reStkLabelAlone = regexp.MustCompile(`(?i)^[\s\x{a0}]*stk\.?[\s\x{a0}]*([0-9]+)[\s\x{a0}]*\.?[\s\x{a0}]*$`)

// splitPrintedStykker splits a subsection at a label the markup left inside it. BR18 § 344 prints
// "Stk. 2" in the same <Stk> as stk. 1; the next <Stk> prints "Stk. 3.", and read by the markup it
// was stk. 2, so "I § 344, stk. 2, indsættes som nr. 9" put a ninth standard into the paragraph on
// greenhouses. The label is taken only where the printed numbers then run in order.
func (p *Provision) splitPrintedStykker(from int, quoted []bool) {
	for k := 0; k < len(p.Stykker); k++ {
		st := p.Stykker[k]
		for i := st.Span.From + 1; i < st.Span.To; i++ {
			if quoted[i] || p.Segments[i].Note != "" {
				continue
			}
			m := reStkLabelLead.FindStringSubmatch(p.Segments[i].Text)
			if m == nil {
				m = reStkLabelAlone.FindStringSubmatch(p.Segments[i].Text)
			}
			if m == nil || Atoi(m[1]) != k+2 {
				continue
			}
			if k+1 < len(p.Stykker) {
				next := reStkLabelLead.FindStringSubmatch(p.Segments[p.Stykker[k+1].Span.From].Text)
				if next == nil || Atoi(next[1]) != k+3 {
					continue
				}
			}
			head := Stykke{Nr: st.Nr, Span: Span{From: st.Span.From, To: i}}
			tail := Stykke{Span: Span{From: i, To: st.Span.To}}
			p.Stykker = append(p.Stykker[:k], append([]Stykke{head, tail}, p.Stykker[k+1:]...)...)
			for j := range p.Stykker {
				p.Stykker[j].Nr = j + 1
			}
			break
		}
	}
}

// JoinLabelOnlyProvision gives a provision that prints only its label the text the markup put after
// it as an unlabelled entry-into-force provision: lov nr. 479 of 2008 prints "§ 5" alone, and its
// "Stk. 1. Bestemmelserne i straffelovens kapitel 14 …" in the next element, with no label, where it
// was read as the act's first entry-into-force provision; a later act's change to § 5, stk. 2, found
// an empty § 5.
func (c *Content) JoinLabelOnlyProvision() {
	if len(c.EntryIntoForce) == 0 || len(c.Order) == 0 {
		return
	}
	last := c.Order[len(c.Order)-1].Provision
	next := &c.EntryIntoForce[0]
	if last == nil || len(last.Stykker) > 0 || strings.TrimSpace(next.Printed) != "" || len(next.Segments) == 0 {
		return
	}
	for _, seg := range last.Segments {
		if t := strings.TrimSpace(seg.Text); t != "" && t != strings.TrimSpace(last.Printed) {
			return // it prints more than its label
		}
	}
	if !reStkLabelLead.MatchString(strings.TrimSpace(next.Segments[0].Text)) {
		return
	}
	if len(next.Stykker) > 0 && reTakesEffect.MatchString(next.SpanText(next.Stykker[0].Span)) {
		return // "§ 2 Stk. 1. Loven træder i kraft …": the label is the entry-into-force provision's
	}
	last.Segments = append([]Segment{{Text: strings.TrimSpace(last.Printed), Break: true}}, cloneSegments(next.Segments)...)
	last.Stykker = nil
	last.Normalise()
	c.EntryIntoForce = c.EntryIntoForce[1:]
}

// reTakesEffect is a subsection saying when something takes effect.
var reTakesEffect = regexp.MustCompile(`(?i)\btræder\s+i\s+kraft\b|\bikrafttræden\b|\bhar\s+virkning\s+fra\b`)
