package lexdania

import (
	"os"
	"regexp"
	"strconv"
	"strings"

	stdhtml "golang.org/x/net/html"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// Changes is what this document changes in other laws, in the model's form: the one structure
// consolidation reconciles from, whatever format the act arrived in.
//
// The register's change markup is read where it exists, because it carries the new text as
// structure. Everywhere else the act's text is handed over as paragraphs in reading order and
// the model reads the change lists out of the prose: that is how the half of the archive whose
// amending acts have no XML body contributes changes at all. Before, those acts were opened, read
// and reported as having changed nothing.
func (d *Document) Changes() []lexdoc.Edit {
	if xml := d.Amendments(); len(xml) > 0 {
		edits := lexdoc.ContinueAnnex(modelEdits(xml))
		if swallowsNext(edits) {
			// The markup holds the next changes inside the last one's sentence: bekendtgørelse
			// nr. 381 of 2009 prints changes 4-7 in the cells of a table that change 3 quotes.
			// Its text read as paragraphs finds them; it is taken when it finds more.
			if text := lexdoc.ReadChangeLists(d.textLines(), d.Meta.Accession); len(changeNumbers(text)) > len(changeNumbers(edits)) {
				return d.withAnnexChangeLists(d.withLimitTables(text))
			}
		}
		return d.withAnnexChangeLists(d.withLimitTables(edits))
	}
	lines := d.textLines()
	if len(lines) == 0 {
		return nil
	}
	return d.withAnnexChangeLists(d.withLimitTables(lexdoc.ReadChangeLists(lines, d.Meta.Accession)))
}

// withAnnexChangeLists replaces a change made "som anført i bilag 2 til denne bekendtgørelse" by
// the changes that annex lists in words, when it is such a list.
func (d *Document) withAnnexChangeLists(edits []lexdoc.Edit) []lexdoc.Edit {
	var lines []string
	var out []lexdoc.Edit
	for _, e := range edits {
		m := reListedInOwnAnnex.FindStringSubmatch(e.Definition)
		if m == nil {
			out = append(out, e)
			continue
		}
		if lines == nil {
			lines = d.textLines()
		}
		if more, ok := lexdoc.ExpandAnnexChangeList(e, lexdoc.AnnexFromLines(lines, lexdoc.Atoi(m[1]), false)); ok {
			out = append(out, more...)
			continue
		}
		out = append(out, e)
	}
	return out
}

// reListedInOwnAnnex is "ændres som anført i bilag 2 til denne".
var reListedInOwnAnnex = regexp.MustCompile(`(?i)(?:^|\s)ændres\s+som\s+(?:anført|angivet)\s+i\s+bilag\s*([0-9]+)\s+til\s+denne\b`)

// swallowsNext reports a change whose sentence holds the start of the change numbered after the
// last one read: "… HE« 4. I FELT08 …" in change 3.
func swallowsNext(edits []lexdoc.Edit) bool {
	last := 0
	for _, e := range edits {
		last = max(last, lexdoc.Atoi(strings.TrimRight(strings.TrimSpace(e.Number), ".")))
	}
	if last == 0 {
		return false
	}
	next := regexp.MustCompile(`[«\s]` + strconv.Itoa(last+1) + `\s?\.\s+(?:I|Som|Efter|Før|Skemaet|Bilag|§)\s`)
	for _, e := range edits {
		if next.MatchString(e.Definition) {
			return true
		}
	}
	return false
}

// changeNumbers are the distinct numbered changes read, by provision of the act.
func changeNumbers(edits []lexdoc.Edit) map[string]bool {
	out := map[string]bool{}
	for _, e := range edits {
		out[e.Section+"/"+strings.TrimSpace(e.Number)] = true
	}
	return out
}

// withLimitTables gives a change made by a table of limits in the act's own annex that annex as
// the model holds it, its cells in their rows and columns: read as lines, a table is a list of
// numbers.
func (d *Document) withLimitTables(edits []lexdoc.Edit) []lexdoc.Edit {
	var annexes []lexdoc.Appendix
	for i := range edits {
		n := lexdoc.LimitTableFrom(edits[i].Definition)
		if n == 0 {
			continue
		}
		if annexes == nil {
			annexes = d.Model().Content.Appendices
		}
		for k := range annexes {
			if annexes[k].Number == strconv.Itoa(n) {
				ap := annexes[k]
				edits[i].NewAppendix = &ap
			}
		}
	}
	return edits
}

// textLines is the document's text as paragraphs in reading order.
//
// For a body recovered from the site's HTML the payload is read again rather than the tree the
// HTML reader built: that tree is shaped for display, and for the oldest markup it holds a
// paragraph's inline pieces apart from the sentence they belong to ("I ændres »stk. 3« til …",
// then "2.", then "§ 6, stk. 4,"), which no change list can be read from.
func (d *Document) textLines() []string {
	if !d.linesRead {
		d.lines, d.linesRead = d.readTextLines(), true
	}
	return d.lines
}

func (d *Document) readTextLines() []string {
	if d.Source == TextFromHTML && d.Sidecar != "" {
		raw, err := os.ReadFile(d.Sidecar)
		if err != nil {
			return nil
		}
		markup, err := PayloadMarkup(raw)
		if err != nil {
			return nil
		}
		return htmlLines(markup)
	}
	var lines []string
	var walk func(n *Node)
	walk = func(n *Node) {
		switch n.Name {
		case "Meta":
			return
		case "Linea", "Explicatus", "Rubrica":
			if t := Squeeze(DisplayText(n)); t != "" {
				lines = append(lines, t)
			}
			return
		}
		for _, c := range n.Elems {
			walk(c)
		}
	}
	walk(d.Root)
	return lines
}

// htmlBlocks end a line of text: what the site's markup, in all three of its generations, uses to
// separate paragraphs.
var htmlBlocks = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "table": true, "td": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"center": true, "blockquote": true, "dd": true, "dt": true, "hr": true,
}

// htmlLines reads payload markup as paragraphs of text in reading order, inline formatting
// joined into the sentence it sits in.
func htmlLines(markup string) []string {
	z := stdhtml.NewTokenizer(strings.NewReader(unquoteSGML(markup)))
	var lines []string
	var cur strings.Builder
	flush := func() {
		if t := Squeeze(cur.String()); t != "" {
			lines = append(lines, t)
		}
		cur.Reset()
	}
	skip := 0
	for {
		switch z.Next() {
		case stdhtml.ErrorToken:
			flush()
			return lines
		case stdhtml.TextToken:
			if skip == 0 {
				cur.WriteString(" ")
				cur.Write(z.Text())
			}
		case stdhtml.StartTagToken, stdhtml.SelfClosingTagToken:
			name, _ := z.TagName()
			switch tag := string(name); {
			case tag == "script" || tag == "style":
				skip++
			case htmlBlocks[tag]:
				flush()
			}
		case stdhtml.EndTagToken:
			name, _ := z.TagName()
			switch tag := string(name); {
			case (tag == "script" || tag == "style") && skip > 0:
				skip--
			case htmlBlocks[tag]:
				flush()
			}
		}
	}
}
