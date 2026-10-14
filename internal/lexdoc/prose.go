package lexdoc

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Prose is the text a document prints outside its provisions: the preamble, the signature, and an
// annex that is text and tables rather than numbered provisions. The model keeps it as segments
// with what the register said about each one — where a paragraph ends, what is set in bold, which
// entry of which list a line belongs to, which table cell it was printed in — and this lays it out
// from those facts: a paragraph per paragraph, a paragraph set wholly in bold as the heading it is,
// a list entry with its label hanging, a table as a table. It reads nothing out of the words: a
// line that opens "1." is a list entry because the register printed it as one, not because it
// looks like one.

// proseKind is what a block of prose is.
type proseKind int

const (
	proseParagraph proseKind = iota
	proseHeading             // a paragraph set wholly in bold
	proseItem                // an entry of a list, with its printed label
	proseTable
)

type proseBlock struct {
	kind  proseKind
	flow  bool   // lines run together as prose rather than standing on their own
	depth int    // list depth of an item, or of a paragraph inside a list entry
	label string // an item's printed label
	lines [][]Segment
	table []Segment // a table's segments, placed by Row and Col
}

// proseBlocks groups segments into the blocks they print as.
func proseBlocks(segs []Segment) []proseBlock {
	var out []proseBlock
	var cur *proseBlock
	open := false // cur takes more lines
	newLine := true
	start := func(b proseBlock) {
		out = append(out, b)
		cur, open, newLine = &out[len(out)-1], true, true
	}
	for i := 0; i < len(segs); i++ {
		sg := segs[i]
		if sg.Table > 0 {
			if cur == nil || cur.kind != proseTable || cur.table[0].Table != sg.Table {
				start(proseBlock{kind: proseTable})
			}
			cur.table = append(cur.table, sg)
			open = false
			continue
		}
		if sg.Marker {
			label := strings.TrimSpace(sg.Text)
			for i+1 < len(segs) && segs[i+1].Marker {
				i++
				label = joinWords(label, strings.TrimSpace(segs[i].Text))
			}
			start(proseBlock{kind: proseItem, depth: sg.Indent, label: label})
			continue
		}
		if !open || cur.kind == proseTable {
			start(proseBlock{depth: sg.Indent})
		}
		if strings.TrimSpace(sg.Text) != "" || sg.Note != "" {
			if newLine || len(cur.lines) == 0 {
				cur.lines = append(cur.lines, nil)
			}
			cur.lines[len(cur.lines)-1] = append(cur.lines[len(cur.lines)-1], sg)
			newLine = false
		}
		if sg.Break && !continuesLine(segs, i) {
			newLine = true
		}
		if sg.Para {
			open = false
		}
	}
	// Empty paragraphs (the register's blank <Exitus/> spacers) print nothing; a paragraph set
	// wholly in bold is a heading.
	kept := out[:0]
	for _, b := range out {
		if b.kind == proseParagraph && (len(b.lines) == 0 || placeholder(b.lines)) {
			continue
		}
		b.lines = joinLabelLines(b.lines)
		if b.kind == proseParagraph && allBold(b.lines) {
			b.kind = proseHeading
		}
		kept = append(kept, b)
	}
	return kept
}

// placeholder reports a paragraph that is only the dash the register prints where a heading
// has no words: "-" above an annex's text. It is no word of the document's.
func placeholder(lines [][]Segment) bool {
	for _, l := range lines {
		for _, sg := range l {
			if t := strings.TrimSpace(sg.Text); t != "" && t != "-" && t != "–" {
				return false
			}
		}
	}
	return true
}

// reLabelLine is a line that prints a label and nothing else: "§ 1.", "Stk. 2.", "1.", "a)".
var reLabelLine = regexp.MustCompile(`^(?:§[\s\x{a0}]*[0-9]+(?:[\s\x{a0}]*[a-zæøå])?\.|Stk\.[\s\x{a0}]*[0-9]+(?:[\s\x{a0}]*[a-zæøå])?\.?|[0-9]+\.|\(?[0-9a-zæøå]{1,3}\))$`)

// joinLabelLines sets a label the register printed on a line of its own at the head of the line
// after it, "Stk. 2." and then the subsection's first sentence, the way the law prints it.
func joinLabelLines(lines [][]Segment) [][]Segment {
	out := lines[:0:0]
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if i+1 < len(lines) && reLabelLine.MatchString(lineText(l)) && !reLabelLine.MatchString(lineText(lines[i+1])) {
			l = append(append([]Segment(nil), l...), lines[i+1]...)
			i++
		}
		out = append(out, l)
	}
	return out
}

func lineText(l []Segment) string {
	t := ""
	for _, sg := range l {
		t = joinWords(t, strings.TrimSpace(sg.Text))
	}
	return t
}

// continuesLine reports a break the register put between two pieces of one line: "Stk. 2" set in
// italic and the full stop after it set plain are two runs, and the stop belongs on the label's
// line.
func continuesLine(segs []Segment, i int) bool {
	if i+1 >= len(segs) || segs[i].Para || segs[i+1].Marker || segs[i+1].Table != segs[i].Table {
		return false
	}
	r, _ := utf8.DecodeRuneInString(strings.TrimSpace(segs[i+1].Text))
	return strings.ContainsRune(".,;:!?)]»”", r)
}

func allBold(lines [][]Segment) bool {
	bold := false
	for _, l := range lines {
		for _, sg := range l {
			if strings.TrimSpace(sg.Text) == "" {
				continue
			}
			if !strings.Contains(sg.Style, "b") || sg.Note != "" {
				return false
			}
			bold = true
		}
	}
	return bold
}

// joinWords puts a space between two pieces of text where one belongs.
func joinWords(a, b string) string {
	if a == "" || b == "" || !spaceBetween(a, b) {
		return a + b
	}
	return a + " " + b
}

// spaceBetween reports whether two runs the register set apart read with a space between them:
// not before punctuation, and not after an opening quotation mark or bracket — Danish opens a
// quotation with ” as well as closing it, so a ” that follows a space or starts the run opens one.
func spaceBetween(a, b string) bool {
	if r, _ := utf8.DecodeRuneInString(b); strings.ContainsRune(".,;:!?)]»”", r) {
		return false
	}
	last, size := utf8.DecodeLastRuneInString(a)
	switch {
	case strings.ContainsRune("([«„“", last):
		return false
	case last == '”' || last == '"':
		before, _ := utf8.DecodeLastRuneInString(a[:len(a)-size])
		return !(len(a) == size || unicode.IsSpace(before))
	}
	return true
}

// writeProse renders a run of prose, optionally separated from what came before. With flow, the
// lines of a paragraph run together as the sentences they are, which is how the register sets the
// text of a preamble or an annex; without it, as in a title or a signature, each keeps its line.
func writeProse(r renderer, segs []Segment, sep string, flow bool) {
	blocks := proseBlocks(segs)
	if len(blocks) == 0 {
		return
	}
	r.b.WriteString(sep)
	for _, b := range blocks {
		b.flow = flow
		switch b.kind {
		case proseTable:
			writeProseTable(r, b.table)
		case proseHeading:
			writeProseHeading(r, b)
		default:
			writeProseParagraph(r, b)
		}
	}
}

// inline writes one line's segments, each in the style it was set in.
func (r renderer) inline(line []Segment, heading bool) {
	prev := ""
	for _, sg := range line {
		t := strings.TrimSpace(sg.Text)
		if t == "" {
			continue
		}
		if prev != "" && spaceBetween(prev, t) {
			r.b.WriteByte(' ')
		}
		prev = t
		st := sg.Style
		if heading {
			st = strings.ReplaceAll(st, "b", "") // the heading is the emphasis
		}
		r.styled(t, st)
	}
}

// styled writes text in a style: HTML elements, markdown asterisks, plain text as it is.
func (r renderer) styled(t, st string) {
	bold, italic := strings.Contains(st, "b"), strings.Contains(st, "i")
	switch r.mode {
	case htmlText:
		if bold {
			r.b.WriteString("<strong>")
		}
		if italic {
			r.b.WriteString("<em>")
		}
		r.text(t)
		if italic {
			r.b.WriteString("</em>")
		}
		if bold {
			r.b.WriteString("</strong>")
		}
	case markdown:
		mark := ""
		if bold {
			mark += "**"
		}
		if italic {
			mark += "*"
		}
		// Asterisks around space are no emphasis in markdown, so the mark goes inside it.
		if mark != "" && (strings.IndexFunc(t, unicode.IsLetter) < 0 && strings.IndexFunc(t, unicode.IsDigit) < 0) {
			mark = ""
		}
		r.b.WriteString(mark)
		r.text(t)
		r.b.WriteString(reverse(mark))
	default:
		r.text(t)
	}
}

func reverse(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

func writeProseHeading(r renderer, b proseBlock) {
	switch r.mode {
	case htmlText:
		r.b.WriteString(`<p class="prose-head">`)
		for i, l := range b.lines {
			if i > 0 {
				r.b.WriteByte(' ')
			}
			r.inline(l, true)
		}
		r.b.WriteString("</p>\n")
	case markdown:
		r.b.WriteString("\n#### ")
		for i, l := range b.lines {
			if i > 0 {
				r.b.WriteByte(' ')
			}
			r.inline(l, true)
		}
		r.b.WriteString("\n\n")
	default:
		writeProseParagraph(r, b)
	}
}

func writeProseParagraph(r renderer, b proseBlock) {
	depth := min(b.depth, 3)
	switch r.mode {
	case htmlText:
		switch {
		case b.kind == proseItem:
			fmt.Fprintf(r.b, `<p class="pitem d%d"><span class="lbl">`, max(depth, 1))
			r.text(b.label)
			r.b.WriteString("</span>")
		case depth > 0:
			fmt.Fprintf(r.b, `<p class="pitem d%d"><span class="lbl"></span>`, depth)
		default:
			r.b.WriteString("<p>")
		}
		for i, l := range b.lines {
			if i > 0 && b.flow {
				r.b.WriteByte(' ')
			} else if i > 0 {
				r.b.WriteString("<br>")
			}
			r.inline(l, false)
		}
		r.b.WriteString("</p>\n")
	default:
		// Markdown and plain text: an entry is its label and its text on one line, indented by
		// its depth, and the entries of one list stand on consecutive lines.
		indent := ""
		if depth > 1 {
			indent = strings.Repeat("   ", depth-1)
		}
		r.b.WriteString(indent)
		if b.kind == proseItem {
			r.text(b.label)
			if len(b.lines) > 0 {
				r.b.WriteByte(' ')
			}
		}
		for i, l := range b.lines {
			if i > 0 {
				if b.kind == proseItem || depth > 0 || b.flow {
					r.b.WriteByte(' ')
				} else {
					r.b.WriteByte('\n')
				}
			}
			r.inline(l, false)
		}
		if b.kind == proseItem || depth > 0 {
			r.b.WriteString("\n")
			return
		}
		r.b.WriteString("\n\n")
	}
}

// writeProseTable sets a table's cells in its rows and columns: an HTML table, a markdown pipe
// table, and in plain text one line per row with the cells apart.
func writeProseTable(r renderer, segs []Segment) {
	type cell struct{ row, col int }
	texts := map[cell][]Segment{}
	rows, cols := 0, 0
	for _, sg := range segs {
		k := cell{sg.Row, sg.Col}
		texts[k] = append(texts[k], sg)
		rows, cols = max(rows, sg.Row), max(cols, sg.Col)
	}
	if r.mode == htmlText {
		r.b.WriteString(`<div class="table"><table>`)
		for row := 1; row <= rows; row++ {
			r.b.WriteString("<tr>")
			for col := 1; col <= cols; col++ {
				r.b.WriteString("<td>")
				for i, sg := range texts[cell{row, col}] {
					if i > 0 && !strings.ContainsRune(".,;:!?)]»”", firstRune(sg.Text)) {
						r.b.WriteString("<br>")
					}
					r.styled(strings.TrimSpace(sg.Text), sg.Style)
				}
				r.b.WriteString("</td>")
			}
			r.b.WriteString("</tr>\n")
		}
		r.b.WriteString("</table></div>\n")
		return
	}
	for row := 1; row <= rows; row++ {
		if r.mode == markdown {
			r.b.WriteString("| ")
		}
		for col := 1; col <= cols; col++ {
			if col > 1 {
				if r.mode == markdown {
					r.b.WriteString(" | ")
				} else {
					r.b.WriteString("\t")
				}
			}
			for i, sg := range texts[cell{row, col}] {
				if i > 0 {
					r.b.WriteByte(' ')
				}
				t := strings.TrimSpace(sg.Text)
				if r.mode == markdown {
					t = strings.ReplaceAll(t, "|", `\|`)
				}
				r.text(t)
			}
		}
		if r.mode == markdown {
			r.b.WriteString(" |")
		}
		r.b.WriteString("\n")
		if r.mode == markdown && row == 1 {
			r.b.WriteString("|" + strings.Repeat(" --- |", cols) + "\n")
		}
	}
	r.b.WriteString("\n")
}

func firstRune(s string) rune {
	c, _ := utf8.DecodeRuneInString(strings.TrimSpace(s))
	return c
}

// appendixTitle is what an annex is called in a list of contents: its label and its heading.
func appendixTitle(ap *Appendix) string {
	label, head := appendixHeadingText(ap)
	if head == "" {
		return label
	}
	return label + " – " + head
}

// appendixHeadingText is an annex's label and the heading printed under it. A label that names
// nothing — the register prints "-" above an annex it does not number — gives way to the heading.
func appendixHeadingText(ap *Appendix) (label, head string) {
	label = appendixLabel(ap)
	head = strings.TrimSpace(ap.Heading)
	if strings.IndexFunc(label, func(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) }) < 0 && head != "" {
		return head, ""
	}
	if strings.EqualFold(head, label) {
		head = ""
	}
	return label, head
}

// writeAppendixHead is an annex's heading: its label, and the heading the document prints under it.
func writeAppendixHead(r renderer, ap *Appendix) {
	label, head := appendixHeadingText(ap)
	r.heading(2, r.ids.appendixID(ap), label)
	if head != "" {
		switch r.mode {
		case htmlText:
			r.b.WriteString(`<p class="annex-title">`)
			r.text(head)
			r.b.WriteString("</p>\n\n")
		case markdown:
			r.b.WriteString("**")
			r.text(head)
			r.b.WriteString("**\n\n")
		default:
			r.paragraph(head)
		}
	}
}

// Text outside the model's concepts. A ruling's body, a paragraph between a bill's provisions, a
// table: the document's own text, printed where the provisions end, with no heading, because it is
// simply what the document says. What the register marks as a kind of text of its own — a summary,
// a bill's remarks, proposed wording — prints under the heading that says which, the summary ahead
// of the provisions as the register puts it, the rest after the annexes.

// textHeads are the kinds of text that print under a heading of their own: the id the page
// anchors it at, and the heading in Danish (the reading page) and English (the text an agent reads).
var textHeads = map[string]struct{ id, da, en string }{
	"summary":             {"resume", "Resumé", "Summary"},
	"remarks":             {"bemaerkninger", "Bemærkninger", "Remarks"},
	"presentation":        {"fremsaettelse", "Fremsættelse", "Presentation"},
	"proposed text":       {"foreslaaet-tekst", "Foreslået tekst", "Proposed text"},
	"draft":               {"udkast", "Udkast", "Draft"},
	"treaty":              {"traktat", "Traktat", "Treaty"},
	"seal":                {"segl", "Segl", "Seal"},
	"hymn":                {"hymne", "Hymne", "Hymn"},
	"promulgation clause": {"rykningsklausul", "Rykningsklausul", "Promulgation clause"},
	"editorial note":      {"redaktionel-note", "Redaktionel note", "Editorial note"},
	"closing":             {"afslutning", "Afslutning", "Closing"},
	"annex title":         {"bilagstitel", "Bilagstitel", "Annex title"},
}

// textGroup is the text of one kind, in document order; concept "" is the body text.
type textGroup struct {
	concept string
	pieces  []Unmapped
}

func textGroups(unmapped []Unmapped) []textGroup {
	var out []textGroup
	at := map[string]int{}
	for _, u := range unmapped {
		if len(u.Segments) == 0 && strings.TrimSpace(u.Text) == "" {
			continue // counted, with no words: the register's metadata block
		}
		concept := u.Concept
		if _, headed := textHeads[concept]; !headed {
			concept = ""
		}
		i, ok := at[concept]
		if !ok {
			i = len(out)
			at[concept] = i
			out = append(out, textGroup{concept: concept})
		}
		out[i].pieces = append(out[i].pieces, u)
	}
	return out
}

// writeTextGroups writes the headed groups (headed) or the body text (!headed); only, when set,
// limits it to one kind and otherwise excludes the summary, which prints ahead of the provisions.
func writeTextGroups(r renderer, groups []textGroup, headed bool, only string) {
	for _, g := range groups {
		if (g.concept != "") != headed || only != "" && g.concept != only || only == "" && g.concept == "summary" {
			continue
		}
		if h, ok := textHeads[g.concept]; ok {
			if r.mode == htmlText {
				r.heading(2, h.id, h.da)
			} else {
				r.heading(2, h.id, h.en)
			}
		}
		for _, u := range g.pieces {
			if len(u.Segments) > 0 {
				writeProse(r, u.Segments, "", true)
				continue
			}
			r.paragraph(strings.TrimSpace(u.Text))
		}
	}
}
