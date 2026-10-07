package lexdoc

import (
	"regexp"
	"strings"
)

// Reading change lists from text.
//
// An amending act's change list has the same shape in every format the register has used since
// the 1950s, because it is a drafting convention rather than markup:
//
//	§ 1
//	I lov om bygnings- og boligregistrering, jf. lovbekendtgørelse nr. 132 af 28. februar 1990,
//	foretages følgende ændringer:
//	1. § 6, stk. 1-3, affattes således:
//	»Offentlige myndigheder … kan rekvirere oplysninger fra bygnings- og boligregistret.
//	Stk. 2. …
//	Stk. 3. …«
//	2. I § 6, stk. 4, ændres »stk. 3« til: »stk. 1«.
//	§ 2
//	Loven træder i kraft den 1. juli 1991.
//
// The register's XML marks those parts up and is read from the markup; the site's HTML, which is
// the only text half the archive's amending acts have, prints them as paragraphs. Given those
// paragraphs in reading order, this file finds the lists and turns each numbered change into an
// Edit through the same classifier the XML path uses, so a change means the same thing whichever
// format carried it.

var (
	// reListOpens is the preamble that opens a change list and names the law it changes.
	// "foretages efter forhandling med Landsarbejdsrådet følgende ændringer" puts words between.
	reListOpens = regexp.MustCompile(`(?i)foretages\b.{0,120}?følgende\s+ændring`)
	// reChangeNumber is a numbered change: "1." at the start of a line, then its sentence; "5 ."
	// typed apart too. "2.1 Apotekere skal …" is a point of new text, not change 2.
	reChangeNumber = regexp.MustCompile(`^([0-9]+)\s?\.(?:\s*([^0-9\s].*))?$`)
	// reBecomesLine is the sentence renumbering what follows an insertion: "Punkt 2.6-2.8 bliver
	// herefter 2.7-2.9", "Stk. 2-4 bliver herefter stk. 3-5."
	reBecomesLine = regexp.MustCompile(`(?i)^(?:punkt(?:erne)?|stk\.|nr\.|litra|afsnit)\s*[0-9][^»«]*\bbliver\s+herefter\b[^»«]*$`)
	// reActProvision is the amending act's own provision header, printed on a line of its own and
	// without the period a provision inside new text carries: "§ 2", not "§ 2.".
	reActProvision = regexp.MustCompile(`^§\s*[0-9]+$`)

	// What new text opens with: a provision, a subsection, a chapter.
	reProvisionLead = regexp.MustCompile(`^§[\s\x{a0}]*([0-9]+)(?:[\s\x{a0}]*([a-zæøåA-ZÆØÅ]{1,2}))?\.`)
	reStykkeLead    = regexp.MustCompile(`(?i)^[\s\x{a0}]*stk\.[\s\x{a0}]*([0-9]+)[\s\x{a0}]*\.`)
	reChapterLead   = regexp.MustCompile(`(?i)^(kapitel|afsnit)\s+([0-9]+(?:\s*[a-zæøå])?|[ivxlcdm]+(?:\s+[a-zæøå])?)\.?$`)
)

// ReadChangeLists finds the change lists in an act's text, given as its paragraphs in reading
// order, and returns their changes. source is the act's identifier, recorded on every change.
//
// A list opens with a preamble naming its target law and runs until the act's next own provision
// or the next preamble. Changes are numbered from 1 within a list, and a line only starts a change
// when it carries the next number: "2. pkt." inside a change's text cannot start change 2 out of
// turn. New text is the quoted block after a definition ending in a colon; quotation marks are
// counted, so new text quoting a passage of its own does not end early.
func ReadChangeLists(lines []string, source string) []Edit {
	var out []Edit
	var target Target
	var cur *pendingChange
	var preceding Address
	inList, next := false, 1
	act := 0 // the number of the act's own provision the current list sits in

	flush := func() {
		if cur == nil {
			return
		}
		e := cur.edit(source, target)
		if act > 0 {
			e.Section = itoa(act)
		}
		if e.Address.Paragraph == "" {
			e.Preceding = preceding
		} else {
			preceding = e.Address
		}
		out = append(out, e)
		cur = nil
	}
	for _, raw := range lines {
		line := Squeeze(raw)
		if line == "" {
			continue
		}
		if cur != nil && cur.depth > 0 && !(act > 0 && isActProvision(line, act+1)) {
			// Inside quoted new text, until its marks balance. A quotation that never closes still
			// ends at the act's own next provision.
			cur.text = append(cur.text, line)
			cur.depth += quoteBalance(line)
			continue
		}
		if opensList(line) {
			flush()
			if m := reProvisionLead.FindStringSubmatch(line); m != nil && strings.HasPrefix(line, "§") {
				act = Atoi(m[1]) // "§ 2. I lov om … foretages følgende ændringer:" on one line
			}
			target, inList, next, preceding = ParseTarget(line), true, 1, Address{}
			continue
		}
		if !inList {
			if m := reProvisionLead.FindStringSubmatch(line + "."); m != nil && strings.HasPrefix(line, "§") {
				act = Atoi(m[1])
			}
			continue
		}
		if cur != nil && act > 0 && isActProvision(line, act+1) {
			// The act's next provision, whatever the change before it was still collecting. Unquoted
			// new text has no closing mark, and without this "§ 2. Bekendtgørelsen træder i kraft …"
			// was inserted into the law being amended as a provision of its own.
			flush()
			inList = false
			act++
			continue
		}
		if reActProvision.MatchString(line) {
			flush()
			inList = false
			continue
		}
		if m := reChangeNumber.FindStringSubmatch(line); m != nil && Atoi(m[1]) == next {
			flush()
			cur = &pendingChange{number: m[1] + ".", definition: []string{m[2]}}
			next++
			continue
		}
		if cur == nil {
			if next == 1 {
				// "… foretages følgende ændring: I § 3 ændres …": a list of one, which is printed
				// without a number.
				cur = &pendingChange{number: "1.", definition: []string{line}}
				next++
			}
			continue
		}
		switch {
		case len(cur.text) > 0 && !cur.quoted && reBecomesLine.MatchString(line):
			// "Punkt 2.6-2.8 bliver herefter 2.7-2.9" after unquoted new text: the change's own
			// sentence again, not the new point's last line.
			cur.definition = append(cur.definition, line)
		case cur.wantsText() && !cur.quoted:
			// New text, quoted or not: modern acts sometimes print it without quotation marks,
			// and then it runs until the next change.
			if len(cur.text) == 0 && strings.HasPrefix(line, "»") {
				cur.quoted = true
			}
			cur.text = append(cur.text, line)
			cur.depth += quoteBalance(line)
		case cur.quoted && len(cur.text) > 0 && (cur.more || strings.HasPrefix(line, "»")):
			// The new text goes on in a second quotation after the first closed, "»§ 3. …«" and
			// then "»Stk. 2. …", its closing mark never printed: read as the change's sentence,
			// the new § 3 lost its stk. 2-4. It runs to the next change, as unquoted text does.
			if last := len(cur.text) - 1; strings.HasPrefix(line, "»") && strings.HasSuffix(strings.TrimSpace(cur.text[last]), "«") {
				cur.text[last] = strings.TrimSuffix(strings.TrimSpace(cur.text[last]), "«")
				line = strings.TrimPrefix(line, "»")
			}
			cur.text = append(cur.text, line)
			cur.more = true
		case cur.complete() && (reProvisionLead.MatchString(line) || reChapterLead.MatchString(line)):
			// A finished change followed by a provision or a chapter of the act's own: the list has
			// ended, and its last change must not swallow the act's entry-into-force provisions.
			flush()
			inList = false
		default:
			// The sentence ran over a line break, or the change goes on after its new text: "Stk.
			// 2-4 bliver herefter stk. 3-5."
			cur.definition = append(cur.definition, line)
		}
	}
	flush()
	return ContinueAnnex(withAnnexes(out, lines))
}

// ContinueAnnex gives a change that names no place the annex the change before it in the same
// list named: "1. Bilag 1, punkt 2.1 affattes således: …", "2. Som nyt punkt 2.6 indsættes: …".
// A change naming a provision, or a new list, ends the run.
func ContinueAnnex(edits []Edit) []Edit {
	annex, section, target := 0, "", Target{}
	for i := range edits {
		e := &edits[i]
		if e.Section != section || e.Target.Raw != target.Raw {
			annex, section, target = 0, e.Section, e.Target
		}
		a := e.Address
		placeless := a.Bilag == 0 && a.Paragraph == "" && a.Chapter == 0 && !a.Preamble && !a.DocTitle && !a.Footnote &&
			!a.Everywhere && !a.EntryIntoForce && !a.Heading && !reNamesAnnex.MatchString(e.Definition)
		switch {
		case placeless && annex != 0 && e.Kind != EditRenumber:
			e.Address.Bilag, e.Address.Recognised, e.Address.Continued = annex, true, true
		case a.Bilag != 0:
			annex = a.Bilag
		case !placeless:
			annex = 0
		}
	}
	return edits
}

// ExpandAnnexChangeList reads the changes an act makes "som anført i bilag 2 til denne
// bekendtgørelse" when that annex is a list of changes in words: "1) Bilag 2, del E, ændres
// således: a) I punkt 1 tilføjes litra g): »…« …", "2) Bilag 4 affattes således: »Bilag 4 …«".
// Each becomes a change of its own, a lettered one read inside the part its number names. It
// reports false when the annex is not such a list.
func ExpandAnnexChangeList(e Edit, annex *Appendix) ([]Edit, bool) {
	if annex == nil || !reByActAnnexList.MatchString(e.Definition) {
		return nil, false
	}
	var lines []string
	for _, sg := range annex.Segments {
		if t := Squeeze(sg.Text); t != "" {
			lines = append(lines, t)
		}
	}
	type change struct {
		def  string
		text []string
	}
	var out []change
	scope, depth := "", 0
	pending := "" // a label printed on a line of its own: its sentence is the next line
	for _, l := range lines {
		if depth > 0 || len(out) > 0 && len(out[len(out)-1].text) > 0 && strings.HasPrefix(l, "»") {
			out[len(out)-1].text = append(out[len(out)-1].text, l)
			depth += quoteBalance(l)
			continue
		}
		if pending != "" {
			l, pending = pending+" "+l, ""
		}
		if m := reAnnexListTop.FindStringSubmatch(l); m != nil {
			if strings.TrimSpace(m[2]) == "" {
				pending = m[1] + ")"
				continue
			}
			def := strings.TrimSpace(m[2])
			if reChangesFollow.MatchString(def) {
				scope = strings.TrimSpace(reChangesFollow.ReplaceAllString(def, ""))
				continue
			}
			scope = ""
			out = append(out, change{def: def})
			continue
		}
		if m := reAnnexListSub.FindStringSubmatch(l); m != nil && scope != "" {
			if strings.TrimSpace(m[2]) == "" {
				pending = m[1] + ")"
				continue
			}
			out = append(out, change{def: "I " + lowerFirst(scope) + ", " + lowerFirst(strings.TrimSpace(m[2]))})
			continue
		}
		if len(out) > 0 && strings.HasPrefix(l, "»") {
			out[len(out)-1].text = append(out[len(out)-1].text, l)
			depth = quoteBalance(l)
			continue
		}
		if len(out) == 0 {
			continue // "I bilagene til … foretages følgende ændringer:"
		}
		return nil, false // a line this does not read: the list is not one of changes in words
	}
	if len(out) == 0 {
		return nil, false
	}
	var edits []Edit
	for _, ch := range out {
		blocks := quotedBlocks(ch.text)
		pairs := annexBlocks(blocks, annexNumbersIn(ch.def))
		if m := reAnnexesRewritten.FindStringSubmatch(ch.def); m != nil && len(pairs) > 1 {
			// "Bilag 9 og 10 affattes således: »Bilag 9 …« »Bilag 10 …«": one change per annex.
			for _, b := range pairs {
				one := e
				one.Definition = "Bilag " + b.number + " affattes således:"
				one.Address = ParseAddress(one.Definition)
				one.Kind = EditReplaceProvision
				one.NewText = b.segs
				one.NewProvisions, one.NewStykker, one.NewProse, one.NewParts, one.NewAppendix = nil, nil, nil, nil, nil
				edits = append(edits, one)
			}
			continue
		}
		one := e
		one.Definition = ch.def
		one.Address = ParseAddress(ch.def)
		var segs []Segment
		for _, b := range blocks {
			segs = append(segs, b...)
		}
		one.NewText = segs
		one.NewProvisions, one.NewStykker, one.NewProse, one.NewParts, one.NewAppendix = nil, nil, nil, nil, nil
		one.Kind, one.Old, one.New = Classify(ch.def, "", one.Address, Brought{Any: len(segs) > 0, First: Squeeze(SegmentsText(segs))})
		edits = append(edits, one)
	}
	return edits, true
}

// quotedBlocks are the quotations of new text, each its lines with the outer marks taken off.
func quotedBlocks(lines []string) [][]Segment {
	var blocks [][]Segment
	var cur []Segment
	depth := 0
	for _, l := range lines {
		if depth <= 0 && strings.HasPrefix(l, "»") && len(cur) > 0 {
			blocks, cur = append(blocks, cur), nil
		}
		depth += quoteBalance(l)
		t := l
		if len(cur) == 0 {
			t = strings.TrimPrefix(t, "»")
		}
		if depth <= 0 {
			t = strings.TrimSuffix(t, "«")
		}
		if t = strings.TrimSpace(t); t != "" {
			cur = append(cur, Segment{Text: t, Break: true})
		}
	}
	if len(cur) > 0 {
		blocks = append(blocks, cur)
	}
	return blocks
}

type annexBlock struct {
	number string
	segs   []Segment
}

// annexBlocks are the whole annexes the quotations print, each under its "Bilag N" header; one
// quotation may hold several, "»Bilag 9 … Bilag 10 … «". Only the annexes named are headers.
func annexBlocks(blocks [][]Segment, named map[string]bool) []annexBlock {
	var out []annexBlock
	for _, b := range blocks {
		for _, sg := range b {
			if m := reBareAnnexHeader.FindStringSubmatch(Squeeze(sg.Text)); m != nil && named[m[1]] {
				out = append(out, annexBlock{number: m[1]})
			}
			if len(out) == 0 {
				return nil // text before the first annex's header
			}
			out[len(out)-1].segs = append(out[len(out)-1].segs, sg)
		}
	}
	return out
}

// annexNumbersIn are the annexes a sentence names: "Bilag 9 og 10 affattes således".
func annexNumbersIn(def string) map[string]bool {
	out := map[string]bool{}
	if m := reAnnexList.FindStringSubmatch(def); m != nil {
		for _, n := range reNumbers.FindAllString(m[1], -1) {
			out[n] = true
		}
	}
	return out
}

// reBareAnnexHeader is an annex's header line: "Bilag 10".
var reBareAnnexHeader = regexp.MustCompile(`(?i)^bilag\s+([0-9]+)\.?$`)

var (
	// reByActAnnexList is a change carried out as the act's own annex lists.
	reByActAnnexList = regexp.MustCompile(`(?i)(?:^|\s)ændres\s+som\s+(?:anført|angivet)\s+i\s+bilag\s*[0-9]+\s+til\s+denne\b`)
	// reAnnexListTop and reAnnexListSub are the numbered and lettered changes of such a list.
	reAnnexListTop = regexp.MustCompile(`^([0-9]+)\)\s*(.*)$`)
	reAnnexListSub = regexp.MustCompile(`^([a-zæøå])\)\s*(.*)$`)
	// reChangesFollow is a change whose lettered changes follow: "Bilag 2, del E, ændres således:".
	reChangesFollow = regexp.MustCompile(`(?i),?\s*ændres\s+således\s*:\s*$`)
	// reAnnexesRewritten is "Bilag 9 og 10 affattes således:".
	reAnnexesRewritten = regexp.MustCompile(`(?i)^bilag(?:ene)?\s+[0-9]+(?:\s*(?:,|og)\s*[0-9]+)+\s+affattes\s+således\s*:\s*$`)
)

// reNamesAnnex is a sentence naming an annex.
var reNamesAnnex = regexp.MustCompile(`(?i)\bbilag`)

// withAnnexes attaches the act's own annexes to the changes that name them, one change per annex
// when a sentence names several.
func withAnnexes(edits []Edit, lines []string) []Edit {
	var out []Edit
	for _, e := range edits {
		pairs := AnnexPairs(e.Definition)
		if len(pairs) == 0 {
			if part, ok := ParseAnnexPart(e.Definition); ok && part.Source != 0 {
				e.Kind = EditReplaceProvision
				e.NewAppendix = AnnexFromLines(lines, part.Source, false)
			}
			out = append(out, e)
			continue
		}
		for _, p := range pairs {
			one := e
			one.Address = ParseAddress("bilag " + itoa(p.Target))
			one.Address.BilagLitra = p.Litra
			one.Kind = EditReplaceProvision
			if p.Insert {
				one.Kind = EditInsertProvision
			}
			one.NewAppendix = AnnexFromLines(lines, p.Source, p.Only)
			out = append(out, one)
		}
	}
	return out
}

// reEntryIntoForce is the sentence an act's closing provision opens with.
var reEntryIntoForce = regexp.MustCompile(`(?i)træder\s+i\s+kraft|har\s+virkning\s+fra|ikrafttræden`)

// isActProvision reports a line that is the act's own provision number n: a bare header, or one
// whose text is the act's entry into force. New text quoting a "§ 2." of the law being amended is
// neither, which is what keeps the test from ending a list in the middle of its new text.
func isActProvision(line string, n int) bool {
	if reActProvision.MatchString(line) {
		return Atoi(FirstNumber(line)) == n
	}
	m := reProvisionLead.FindStringSubmatch(line)
	return m != nil && m[2] == "" && Atoi(m[1]) == n && reEntryIntoForce.MatchString(line)
}

// pendingChange is one numbered change while its lines are being collected.
type pendingChange struct {
	number     string
	definition []string
	text       []string // the new text, outer quotation marks still on
	depth      int      // open » not yet closed by «
	quoted     bool     // the new text opened with a quotation mark, so its closing one ends it
	more       bool     // a further quotation of new text followed the first
}

// complete reports a change whose sentence has ended.
func (p *pendingChange) complete() bool {
	def := strings.TrimSpace(strings.Join(p.definition, " "))
	return strings.HasSuffix(def, ".") || strings.HasSuffix(def, ".«") || p.quoted
}

// wantsText reports a definition that announces new text: "affattes således:", "indsættes:".
func (p *pendingChange) wantsText() bool {
	return strings.HasSuffix(strings.TrimSpace(strings.Join(p.definition, " ")), ":")
}

// quoteBalance counts the quotation marks a line opens and does not close.
func quoteBalance(line string) int {
	return strings.Count(line, "»") - strings.Count(line, "«")
}

// edit turns the collected lines into a change, through the same classifier the XML path uses.
func (p *pendingChange) edit(source string, target Target) Edit {
	def := RepairQuotes(strings.Join(p.definition, " "))
	text := unquoteBlock(p.text)
	e := Edit{
		Source:     source,
		Number:     p.number,
		Address:    ParseAddress(def),
		Definition: def,
		Prose:      strings.TrimSpace(p.number + " " + def + " " + strings.Join(p.text, " ")),
		Target:     target,
	}
	if t := ParseTarget(def); e.Target.Empty() && !t.Empty() {
		// A list whose preamble named nothing recognisable can still name its law per change.
		e.Target = t
	}
	b := Brought{Any: len(text) > 0}
	if b.Any {
		b.First = text[0]
	}
	for _, line := range text {
		if reProvisionLead.MatchString(line) || reStykkeLead.MatchString(line) || reChapterLead.MatchString(line) {
			b.Structure = true
		}
	}
	e.Kind, e.Old, e.New = Classify(def, e.Prose, e.Address, b)
	switch {
	case e.Address.Heading && e.Kind != EditInsertProvision:
		// A heading's new wording is the text itself; the classifier already took its first line.
	case e.Kind == EditInsertProvision, e.Kind == EditReplaceProvision:
		e.donateLines(text)
	}
	CompleteFromDefinition(&e)
	return e
}

// unquoteBlock removes the quotation marks that open and close a block of new text, leaving any
// the text quotes itself.
func unquoteBlock(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	out := append([]string(nil), lines...)
	out[0] = strings.TrimSpace(strings.TrimPrefix(out[0], "»"))
	last := len(out) - 1
	out[last] = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(out[last]), "«"))
	// "…«." and "…«;" close the block with the sentence's own punctuation outside the quote.
	for _, tail := range []string{"«.", "«;", "«,"} {
		if strings.HasSuffix(out[last], tail) {
			out[last] = strings.TrimSuffix(out[last], tail)
		}
	}
	return out
}

// donateLines sorts new text into the model's shapes, the way XML donation sorts markup: a line
// opening "§ 6 a." starts a provision, "Stk. 2." a subsection, "Kapitel 4 a" a chapter whose next
// line is its heading, and anything else continues what came before it. Lines in front of the
// first subsection label are stk. 1 when labelled subsections follow, because Danish prints no
// "Stk. 1.", and loose lines otherwise.
func (e *Edit) donateLines(lines []string) {
	e.NewText = nil
	for _, l := range lines {
		e.NewText = append(e.NewText, Segment{Text: l, Break: true})
	}
	var open *Part      // the chapter new provisions print under
	var division *Part  // the afsnit new chapters print under: "Afsnit X a … Kapitel 20 a …"
	var last *Provision // the provision subsections and lines belong to
	var loose []Segment // lines before any provision or subsection label
	headingNext := false
	pendingHeading := ""

	place := func(p *Provision) {
		if open != nil {
			open.Order = append(open.Order, Node{Provision: p})
			return
		}
		e.NewProvisions = append(e.NewProvisions, p)
	}
	for i, line := range lines {
		seg := Segment{Text: line, Break: true}
		if m := reOrdinalDivision.FindStringSubmatch(strings.TrimLeft(line, "»")); m != nil {
			// "Tredje afsnit. Tiltale og hovedforhandling i 1. instans": a division numbered in
			// words, holding the chapters printed after it.
			kind := PartAfsnit
			if strings.EqualFold(m[2], "bog") {
				kind = PartBook
			}
			division = &Part{Kind: kind, Printed: m[1] + " " + strings.ToLower(m[2]), Number: itoa(DanishOrdinals[strings.ToLower(m[1])]), Heading: strings.TrimSpace(m[3])}
			e.NewParts = append(e.NewParts, division)
			open, last = nil, nil
			continue
		}
		if headingNext && open != nil && !reProvisionLead.MatchString(line) && !reChapterLead.MatchString(line) {
			// "Kapitel 76" then "Tilståelsessager" then "§ 831.": the line under the chapter's
			// number is the chapter's heading, not the provision's.
			headingNext = false
			open.Heading = line
			continue
		}
		if i+1 < len(lines) && isRubric(line) && !reChapterLead.MatchString(line) && reProvisionLead.MatchString(lines[i+1]) {
			// "Overgang til hovedforløb" printed above "§ 5 a." is that provision's heading. Filed
			// as loose text it was dropped when the provisions were placed.
			pendingHeading = line
			continue
		}
		if headingNext {
			headingNext = false
			if !reProvisionLead.MatchString(line) {
				open.Heading = line
				continue
			}
		}
		if m := reChapterLead.FindStringSubmatch(line); m != nil {
			kind := PartChapter
			if strings.EqualFold(m[1], "afsnit") {
				kind = PartAfsnit
			}
			open = &Part{Kind: kind, Printed: line, Number: NumberWithLitra(line)}
			if kind == PartAfsnit {
				if r := PrintedRoman(line); r != "" {
					open.Number = r
					if l := reRomanLitra.FindStringSubmatch(line); l != nil {
						open.Number += strings.ToLower(l[1]) // "Afsnit X a": 10a
					}
				}
			}
			switch {
			case kind == PartChapter && division != nil:
				division.Order = append(division.Order, Node{Part: open})
			default:
				e.NewParts = append(e.NewParts, open)
			}
			if kind == PartAfsnit {
				division = open
			}
			last, headingNext = nil, true
			continue
		}
		if m := reProvisionLead.FindStringSubmatch(line); m != nil {
			p := &Provision{Number: m[1], Litra: strings.ToLower(m[2]), Printed: reProvisionLead.FindString(line), Heading: pendingHeading,
				Segments: []Segment{seg}, Stykker: []Stykke{{Nr: 1, Span: Span{From: 0, To: 1}}}}
			pendingHeading = ""
			place(p)
			last = p
			continue
		}
		if m := reStykkeLead.FindStringSubmatch(line); m != nil {
			if last != nil {
				last.AppendStykke([]Segment{seg}, 0)
				continue
			}
			if len(loose) > 0 && Atoi(m[1]) > 1 && len(e.NewStykker) == 0 {
				e.NewStykker = append(e.NewStykker, loose) // the unlabelled stk. 1 in front of it
				loose = nil
			}
			e.NewStykker = append(e.NewStykker, []Segment{seg})
			continue
		}
		switch {
		case last != nil:
			last.Segments = append(last.Segments, seg)
			st := &last.Stykker[len(last.Stykker)-1]
			st.Span.To = len(last.Segments)
		case len(e.NewStykker) > 0:
			n := len(e.NewStykker) - 1
			e.NewStykker[n] = append(e.NewStykker[n], seg)
		default:
			loose = append(loose, seg)
		}
	}
	for _, l := range loose {
		e.NewProse = append(e.NewProse, l.Text)
	}
	finishProvisions := func(ps []*Provision) {
		for _, p := range ps {
			for i := range p.Stykker {
				p.Stykker[i].Items = ItemsIn(p.Segments, p.Stykker[i].Span)
			}
		}
	}
	finishProvisions(e.NewProvisions)
	for _, part := range e.NewParts {
		var ps []*Provision
		for _, n := range part.Order {
			if n.Provision != nil {
				ps = append(ps, n.Provision)
			}
		}
		finishProvisions(ps)
	}
	e.NewProvisions, e.NewParts = FlattenGroups(e.NewProvisions, e.NewParts)
}

// FlattenGroups turns headed groups that follow new provisions into those provisions' headings:
// "§ 10 a. … Kontrol § 10 b. …" is two provisions, the second with a heading, and placing them
// is placing provisions. Chapters stay chapters: where they begin is not the provisions' place.
func FlattenGroups(provs []*Provision, parts []*Part) ([]*Provision, []*Part) {
	if len(provs) == 0 || len(parts) == 0 {
		return provs, parts
	}
	for _, p := range parts {
		if p.Kind != PartGroup {
			return provs, parts
		}
		for _, n := range p.Order {
			if n.Provision == nil {
				return provs, parts
			}
		}
	}
	out := append([]*Provision(nil), provs...)
	for _, p := range parts {
		for i, n := range p.Order {
			if i == 0 && n.Provision.Heading == "" {
				n.Provision.Heading = p.Heading
			}
			out = append(out, n.Provision)
		}
	}
	return out, nil
}

// isRubric reports a line shaped like a heading: short, and not a sentence.
func isRubric(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" || len([]rune(line)) > 120 || reStykkeLead.MatchString(line) || reProvisionLead.MatchString(line) {
		return false
	}
	return !strings.ContainsAny(line[len(line)-1:], ".:;,")
}

// reInlineText is new text a change sentence carries after its own colon: "§ 7, stk. 2, affattes
// således: Stk. 2. Børn …", "Indledningen affattes således: »I medfør af …«".
var reInlineText = regexp.MustCompile(`(?is)(?:affattes\s+således|indsættes(?:\s+som\s+[^:»]{0,60})?)\s*[:.]\s*(.+)$`)

// CompleteFromDefinition gives a change the new text its own sentence carries, when whoever read it
// found none elsewhere. The register's XML usually puts new text in markup of its own, but not
// always, and a payload's change line may run on into it; either way the words are the act's.
func CompleteFromDefinition(e *Edit) {
	if e.Kind != EditReplaceProvision && e.Kind != EditInsertProvision && e.Kind != EditUnknown {
		return
	}
	if len(e.NewProvisions)+len(e.NewStykker)+len(e.NewProse)+len(e.NewParts) > 0 || e.NewAppendix != nil || e.Address.Heading {
		return
	}
	m := reInlineText.FindStringSubmatch(e.Definition)
	if m == nil {
		return
	}
	text := strings.TrimSpace(m[1])
	if text == "" || strings.HasPrefix(strings.ToLower(text), "som bilag") {
		return
	}
	if e.Kind == EditUnknown {
		e.Kind = EditReplaceProvision
		if strings.Contains(strings.ToLower(e.Definition[:len(e.Definition)-len(m[1])]), "indsættes") {
			e.Kind = EditInsertProvision
		}
	}
	e.donateLines(unquoteBlock(splitInline(text)))
	if e.New == "" {
		e.New = Squeeze(strings.Trim(text, "»« "))
	}
}

// splitInline breaks inline new text at its own labels, which a sentence run together: "Stk. 2.",
// "2)" and "§ 4." each start a line.
func splitInline(text string) []string {
	idx := reInlineLabel.FindAllStringIndex(text, -1)
	var out []string
	prev := 0
	for _, loc := range idx {
		if loc[0] <= prev {
			continue
		}
		out = append(out, strings.TrimSpace(text[prev:loc[0]]))
		prev = loc[0]
	}
	out = append(out, strings.TrimSpace(text[prev:]))
	var kept []string
	for _, l := range out {
		if l != "" {
			kept = append(kept, l)
		}
	}
	return kept
}

// reInlineLabel is a label inside run-together text: a space, then "Stk. N.", "N)" or "§ N.".
var reInlineLabel = regexp.MustCompile(`(?i)\s(?:stk\.\s*[0-9]+\.|[0-9]+\)|§\s*[0-9]+(?:\s*[a-zæøå])?\.)\s`)

// PromoteStykker reads new text delivered as subsections, the first of which opens "§ 45 d.", as
// the provision it is: the act's markup put the paragraph in a subsection element, and as
// subsections the insertion looked for a provision to put them in.
func PromoteStykker(e *Edit) {
	if len(e.NewStykker) == 0 || len(e.NewStykker[0]) == 0 {
		return
	}
	first := Squeeze(e.NewStykker[0][0].Text)
	m := reProvisionLead.FindStringSubmatch(first)
	if m == nil {
		return
	}
	p := &Provision{Number: m[1], Litra: strings.ToLower(m[2]), Printed: reProvisionLead.FindString(first)}
	for _, segs := range e.NewStykker {
		from := len(p.Segments)
		p.Segments = append(p.Segments, cloneSegments(segs)...)
		p.Stykker = append(p.Stykker, Stykke{Nr: len(p.Stykker) + 1, Span: Span{From: from, To: len(p.Segments)}})
	}
	for i := range p.Stykker {
		p.Stykker[i].Items = ItemsIn(p.Segments, p.Stykker[i].Span)
	}
	// Subsections a change brings come ahead of its provisions: "§ 46 a." marked up as
	// subsections, then "§ 46 b." as a provision, is § 46 a followed by § 46 b.
	e.NewProvisions, e.NewStykker = append([]*Provision{p}, e.NewProvisions...), nil
}

// reRomanLitra is the letter after an afsnit's roman number: "Afsnit X a".
var reRomanLitra = regexp.MustCompile(`(?i)\bafsnit\s+[ivxlcdm]+\s+([a-zæøå])\.?$`)

// reOrdinalDivision is a book or an afsnit numbered in words: "Tredje afsnit. Tiltale …".
var reOrdinalDivision = regexp.MustCompile(`(?i)^(første|anden|andet|tredje|fjerde|femte|sjette|syvende|ottende|niende|tiende|ellevte|tolvte)\s+(bog|afsnit)\.\s*(.*)$`)

// opensList is reListOpens.MatchString with a prefilter. Every match contains "oretage", whose
// letters have no case-insensitive equivalents outside ASCII (unlike s and k), so text without
// it cannot match; the pattern ran on every segment of every provision, and almost none of them
// is a change list's opening.
func opensList(s string) bool {
	return containsFoldASCII(s, "oretage") && reListOpens.MatchString(s)
}

// containsFoldASCII reports whether s contains sub, a lower-case ASCII word, ignoring ASCII case.
func containsFoldASCII(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		j := 0
		for ; j < len(sub); j++ {
			c := s[i+j]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != sub[j] {
				break
			}
		}
		if j == len(sub) {
			return true
		}
	}
	return false
}
