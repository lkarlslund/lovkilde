package lexdoc

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Changes to a table's cells.
//
// The pesticide orders print their limits as tables: a header row naming each group of products
// ("KERNE- FRUGTER") and its products ("Kvæder", "Pærer", …), and a row per substance with a limit
// under each product. An amending act changes them cell by cell: "I bilag 2 indsættes i gruppen
// »kernefrugter« efter »Chlorfenvinphos«: »Chlormequat« med maksimalgrænseværdien »0,2m« for
// produktet »Pærer«." Read as words to replace, the quotations named the wrong things; read against
// the table the reader keeps (Segment.Table, Row, Col), they name a row and a column. A substance
// the table already lists has its cell set, as the next order prints it; one it does not list gets a
// row after the one the change names.

var (
	// reCellInsert is "I bilag 2 indsættes i gruppen »G« efter »A«: »S«" and the limits after it.
	reCellInsert = regexp.MustCompile(`(?i)^\s*i\s+bilag\s+([0-9]+)\s+indsættes\s+i\s+gruppen\s*»\s*([^«]+?)\s*«\s*efter\s*»\s*([^«]+?)\s*«\s*:\s*»\s*([^«]+?)\s*«(.*)$`)
	// reCellLimit is "med maksimalgrænseværdien »0,2m« for produktet »Pærer«", the product left out
	// in a table of one column.
	reCellLimit = regexp.MustCompile(`(?i)maksimalgrænseværdien\s*»\s*([^«]+?)\s*«(?:\s*(?:for\s+produktet|i)\s*»\s*([^«]+?)\s*«)?`)
	// reCellRemove is "I Bilag 6 udgår »Boscalid« i produktet »hovedsalat« med maksimalgrænseværdien
	// »3,0« mg/kg."
	reCellRemove = regexp.MustCompile(`(?i)^\s*i\s+bilag\s+([0-9]+)\s+udgår\s*»\s*([^«]+?)\s*«\s*i\s+produktet\s*»\s*([^«]+?)\s*«\s*med\s+maksimalgrænseværdien\s*»\s*([^«]+?)\s*«`)
	// reNoteMark is a footnote marker or date printed after a substance's name: "36)", "(10. januar
	// 2007)".
	reCellAlsoRemove = regexp.MustCompile(`(?i)\.?\s*samtidig\s+ophæves\s*»\s*([^«]+?)\s*«\s*med\s+maksimalgrænseværdien\s*»\s*([^«]+?)\s*«\.?`)
	reNoteMark       = regexp.MustCompile(`\s*(?:[0-9]{1,3}\)|\([^()]*[0-9]{4}\))\s*`)
)

// applyTableCells carries out a change to a table's cells, and reports false when the change is not
// one it reads.
func (c *Content) applyTableCells(e Edit, reject func(string), apply func(where, note string)) bool {
	def := Squeeze(e.Definition)
	if m := reCellInsert.FindStringSubmatch(def); m != nil {
		tail := m[5]
		var replaced []string // "Samtidig ophæves »Atrazin« med maksimalgrænseværdien »0,05*«"
		if r := reCellAlsoRemove.FindStringSubmatch(tail); r != nil {
			replaced = []string{r[1], r[2]}
			tail = strings.Replace(tail, r[0], "", 1)
		}
		var limits [][2]string // value, product
		for _, l := range reCellLimit.FindAllStringSubmatch(tail, -1) {
			limits = append(limits, [2]string{l[1], l[2]})
		}
		rest := reCellLimit.ReplaceAllString(tail, "")
		if len(limits) == 0 || strings.ContainsAny(rest, "»") {
			return false // a further clause this does not read: "Samtidig ophæves …"
		}
		ap, ok := c.annexText(Atoi(m[1]))
		if !ok {
			reject("the law has no bilag " + m[1] + " printed as text")
			return true
		}
		g, why := findGroupTable(ap.Segments, m[2], m[3])
		if why != "" {
			reject("bilag " + m[1] + ": " + why)
			return true
		}
		cols := make([]int, len(limits))
		for i, l := range limits {
			col, why := g.productCol(l[1])
			if why != "" {
				reject("bilag " + m[1] + ": " + why)
				return true
			}
			cols[i] = col
		}
		row := g.rowOf(m[4])
		note := "the substance's row is the table's own; its cells are set"
		if replaced != nil {
			// The row the change puts in replaces the one it repeals: verified by its limit, and
			// renamed when the new name differs only by its note.
			old := g.rowOf(replaced[0])
			if old == 0 || len(cols) != 1 || alnumKey(cellText(ap.Segments, g.table, old, cols[0])) != alnumKey(replaced[1]) {
				reject(fmt.Sprintf("bilag %s: the table does not list %s with the limit %s the change repeals", m[1], replaced[0], replaced[1]))
				return true
			}
			if row != old {
				reject(fmt.Sprintf("bilag %s: %s replaces %s, which this model does not move", m[1], m[4], replaced[0]))
				return true
			}
			ap.Segments = setRowName(ap.Segments, g.table, row, m[4])
			note = "the substance's row is replaced as the change says"
		}
		if row == 0 {
			row = g.anchor + 1
			ap.Segments = g.insertRow(ap.Segments, row, m[4])
			g.rows = nil // the rows have moved
			note = "a row for the substance is inserted after " + m[3]
		}
		for i, l := range limits {
			ap.Segments = setCell(ap.Segments, g.table, row, cols[i], l[0])
		}
		apply("bilag "+m[1]+", "+m[2], note)
		return true
	}
	if m := reCellRemove.FindStringSubmatch(def); m != nil {
		ap, ok := c.annexText(Atoi(m[1]))
		if !ok {
			reject("the law has no bilag " + m[1] + " printed as text")
			return true
		}
		if segs, ok := removeListRow(ap.Segments, m[2], m[3], m[4]); ok {
			// "Boscalid | Hovedsalat | 3,0": a table listing a row per substance and product.
			ap.Segments = segs
			apply("bilag "+m[1], "the row listing the substance, the product and the limit is removed")
			return true
		}
		var hits []tableGroup
		for _, g := range productTables(ap.Segments, m[3]) {
			if g.rowOf(m[2]) != 0 {
				hits = append(hits, g)
			}
		}
		if len(hits) != 1 {
			reject(fmt.Sprintf("bilag %s: %d tables list %s under a product %s", m[1], len(hits), m[2], m[3]))
			return true
		}
		g := hits[0]
		col, why := g.productCol(m[3])
		if why != "" {
			reject("bilag " + m[1] + ": " + why)
			return true
		}
		row := g.rowOf(m[2])
		if got := cellText(ap.Segments, g.table, row, col); alnumKey(got) != alnumKey(m[4]) {
			reject(fmt.Sprintf("bilag %s: %s under %s is %q, not the %s the change removes", m[1], m[2], m[3], got, m[4]))
			return true
		}
		ap.Segments = setCell(ap.Segments, g.table, row, col, "")
		apply("bilag "+m[1], "the limit is removed from the substance's row")
		return true
	}
	return false
}

// tableGroup is one table of an annex, read for a group of its columns: the header row, the
// columns the group heads (in the data rows' numbering), and the row the change goes by.
type tableGroup struct {
	table, header int
	end           int // the next group's header row, which ends this group's rows
	shift         int
	from, to      int // the group's product columns, data numbering, to inclusive
	headers       map[int]string
	anchor        int
	group         int // the group's own column, data numbering
	rows          map[string]int
	segs          []Segment
}

// findGroupTable is the one table whose header names group and lists anchor as a substance.
func findGroupTable(segs []Segment, group, anchor string) (tableGroup, string) {
	var found []tableGroup
	for _, t := range tablesOf(segs) {
		g, ok := readGroup(segs, t, group)
		if !ok {
			continue
		}
		if g.anchor = g.rowOf(anchor); g.anchor != 0 {
			found = append(found, g)
		}
	}
	switch len(found) {
	case 0:
		return tableGroup{}, fmt.Sprintf("no table lists %s under the group %s", anchor, group)
	case 1:
		return found[0], ""
	}
	return tableGroup{}, fmt.Sprintf("%d tables list %s under the group %s", len(found), anchor, group)
}

// productTables are the tables with a column headed product.
func productTables(segs []Segment, product string) []tableGroup {
	var out []tableGroup
	for _, t := range tablesOf(segs) {
		g := tableGroup{table: t, segs: segs}
		if !g.readHeader(func(string) bool { return true }) {
			continue
		}
		g.from, g.to = 1, 1<<20
		if _, why := g.productCol(product); why == "" {
			out = append(out, g)
		}
	}
	return out
}

// tablesOf lists the tables segs print, in order.
func tablesOf(segs []Segment) []int {
	seen := map[int]bool{}
	var out []int
	for _, s := range segs {
		if s.Table != 0 && !seen[s.Table] {
			seen[s.Table] = true
			out = append(out, s.Table)
		}
	}
	return out
}

// readGroup reads table t's header for the columns group heads: from the cell naming it to the next
// cell in capitals, which names the next group.
func readGroup(segs []Segment, t int, group string) (tableGroup, bool) {
	g := tableGroup{table: t, segs: segs}
	want := alnumKey(group)
	same := func(text string) bool {
		k := alnumKey(text)
		// "Pesticidrester i og på cerialier", the act's "cerealier": a letter or two in a long name.
		return k == want || len(want) >= 15 && editDistance(k, want) <= 2
	}
	var at int
	if !g.readHeader(same) {
		// A table of one column of limits names its group above itself: "Pesticidrester i og på
		// cerealier".
		return g, false
	}
	for col, text := range g.headers {
		if same(text) {
			at = col
		}
	}
	g.group = at
	g.from, g.to = at+1, 1<<20
	for col, text := range g.headers {
		if col > at && isGroupHeading(text) && col-1 < g.to {
			g.to = col - 1
		}
	}
	return g, true
}

// readHeader finds table t's header row, the first holding a cell match accepts, and how far its
// columns stand from the data rows': the header prints no cell over the substances, so its columns
// line up with the data from the right.
func (g *tableGroup) readHeader(match func(string) bool) bool {
	cells := map[int]map[int]string{}
	maxCol := map[int]int{}
	for _, s := range g.segs {
		if s.Table != g.table || s.Row == 0 {
			continue
		}
		if cells[s.Row] == nil {
			cells[s.Row] = map[int]string{}
		}
		cells[s.Row][s.Col] = strings.TrimSpace(cells[s.Row][s.Col] + " " + Squeeze(s.Text))
		if s.Col > maxCol[s.Row] {
			maxCol[s.Row] = s.Col
		}
	}
	var rows []int
	for r := range cells {
		rows = append(rows, r)
	}
	sort.Ints(rows)
	matches := func(r int) bool {
		for _, text := range cells[r] {
			if match(text) && len(cells[r]) > 1 {
				return true
			}
		}
		return false
	}
	for i, r := range rows {
		if !matches(r) {
			continue
		}
		// "TE | HUMLE | KARTOFLER" over "TE (tørrede blade …) | HUMLE, (tørret) … | KARTOFLER | Nye
		// kartofler", or "BÆLGFRUGTER, tørrede" describing the group over its header: the header
		// is the last row before the limits, a group of one column named in the row above it.
		named := r
		for _, r2 := range rows[i+1:] {
			if holdsLimit(cells[r2]) {
				break
			}
			if len(cells[r2]) >= len(cells[r]) && len(cells[r2]) > 1 {
				merged := map[int]string{}
				for col, text := range cells[r2] {
					merged[col] = text
					if above := cells[r][col]; isGroupHeading(above) && !isGroupHeading(text) {
						merged[col] = above
					}
				}
				cells[r2] = merged
				r = r2
			}
		}
		if !matches(r) {
			r = named
		}
		for _, text := range cells[r] {
			if match(text) && len(cells[r]) > 1 {
				g.header = r
				g.headers = map[int]string{}
				dataWidth := 0
				for _, r2 := range rows {
					if r2 > r && maxCol[r2] > dataWidth {
						dataWidth = maxCol[r2]
					}
				}
				g.shift = 0
				if dataWidth > maxCol[r] {
					g.shift = dataWidth - maxCol[r]
				}
				if first := minKey(cells[r]); isGroupHeading(cells[r][first]) {
					// A header opening with a group's name stands over the group's own column: the
					// data rows print their substance first, so the header is one column to the left.
					g.shift = 1
				}
				g.end = 1 << 30
				for _, r2 := range rows {
					if r2 <= r {
						continue
					}
					for _, t2 := range cells[r2] {
						if isGroupHeading(t2) && len(cells[r2]) > 1 && cells[r2][minKey(cells[r2])] == t2 && !holdsLimit(cells[r2]) {
							g.end = r2
						}
					}
					if g.end != 1<<30 {
						break
					}
				}
				for col, t := range cells[r] {
					g.headers[col+g.shift] = t
				}
				return true
			}
		}
	}
	return false
}

// isGroupHeading reports a header cell naming a group of products, printed in capitals.
func isGroupHeading(text string) bool {
	letters, upper := 0, 0
	for _, r := range text {
		if r >= 'a' && r <= 'z' || r == 'æ' || r == 'ø' || r == 'å' {
			letters++
		}
		if r >= 'A' && r <= 'Z' || r == 'Æ' || r == 'Ø' || r == 'Å' {
			letters++
			upper++
		}
	}
	return letters >= 2 && upper*10 >= letters*9
}

// productCol is the column of the group headed product; the group's only column when no product is
// named.
func (g *tableGroup) productCol(product string) (int, string) {
	if product == "" {
		var cols []int
		for col := range g.headers {
			if col >= g.from && col <= g.to {
				cols = append(cols, col)
			}
		}
		if len(cols) == 1 {
			return cols[0], ""
		}
		return 0, "the change names no product, and the group has several"
	}
	want := alnumKey(product)
	var hits []int
	for col, text := range g.headers {
		if col >= g.from && col <= g.to && alnumKey(text) == want {
			hits = append(hits, col)
		}
	}
	if len(hits) != 1 {
		return 0, fmt.Sprintf("the group has %d columns headed %s", len(hits), product)
	}
	return hits[0], ""
}

// rowOf is the data row listing substance, or 0.
func (g *tableGroup) rowOf(substance string) int {
	if g.rows == nil {
		g.rows = map[string]int{}
		names := map[int]string{}
		for _, s := range g.segs {
			if s.Table == g.table && s.Row > g.header && s.Row < g.end && s.Col == 1 {
				names[s.Row] += " " + Squeeze(s.Text)
			}
		}
		index := func(k string, row int) {
			if k == "" {
				return
			}
			if prev, dup := g.rows[k]; dup && prev != row {
				g.rows[k] = -1 // printed twice: no row to go by
				return
			}
			g.rows[k] = row
		}
		for row, name := range names {
			clean := reNoteMark.ReplaceAllString(name, " ")
			index(alnumKey(clean), row)
			if parts := strings.Split(clean, "/"); len(parts) > 1 {
				// "Methomyl/ Thiodicarb": one row for both, found by either.
				for _, p := range parts {
					index(alnumKey(p), row)
				}
			}
		}
	}
	r := g.rows[alnumKey(reNoteMark.ReplaceAllString(substance, " "))]
	if r < 0 {
		return 0
	}
	return r
}

// insertRow puts a row for substance at row, moving the table's later rows down.
func (g *tableGroup) insertRow(segs []Segment, row int, substance string) []Segment {
	at := len(segs)
	for i, s := range segs {
		if s.Table == g.table && s.Row >= row {
			at = i
			break
		}
	}
	if at == len(segs) {
		for i := len(segs) - 1; i >= 0; i-- {
			if segs[i].Table == g.table {
				at = i + 1
				break
			}
		}
	}
	out := make([]Segment, 0, len(segs)+1)
	out = append(out, segs[:at]...)
	out = append(out, Segment{Text: substance, Break: true, Table: g.table, Row: row, Col: 1})
	for _, s := range segs[at:] {
		if s.Table == g.table && s.Row >= row {
			s.Row++
		}
		out = append(out, s)
	}
	return out
}

// cellText is the text of a cell.
func cellText(segs []Segment, table, row, col int) string {
	var b strings.Builder
	for _, s := range segs {
		if s.Table == table && s.Row == row && s.Col == col {
			b.WriteString(Squeeze(s.Text))
		}
	}
	return b.String()
}

// setCell gives a cell its text, in the reading order of the row's cells; empty text clears it.
func setCell(segs []Segment, table, row, col int, text string) []Segment {
	out := make([]Segment, 0, len(segs)+1)
	placed := text == ""
	at := -1
	for i, s := range segs {
		if s.Table == table && s.Row == row {
			if s.Col == col {
				if !placed {
					s.Text = text
					out = append(out, s)
					placed = true
				}
				continue
			}
			if s.Col > col && !placed {
				out = append(out, Segment{Text: text, Break: true, Table: table, Row: row, Col: col})
				placed = true
			}
			at = len(out)
		}
		out = append(out, s)
		_ = i
	}
	if !placed {
		cell := Segment{Text: text, Break: true, Table: table, Row: row, Col: col}
		if at < 0 {
			return append(out, cell)
		}
		out = append(out[:at+1], append([]Segment{cell}, out[at+1:]...)...)
	}
	return out
}

// setRowName gives a row's first cell its text.
func setRowName(segs []Segment, table, row int, name string) []Segment {
	out := make([]Segment, 0, len(segs))
	done := false
	for _, s := range segs {
		if s.Table == table && s.Row == row && s.Col == 1 {
			if !done {
				s.Text = name
				out = append(out, s)
				done = true
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// removeListRow removes the one table row printing substance first and product and limit among its
// other cells, and moves the table's later rows up.
func removeListRow(segs []Segment, substance, product, limit string) ([]Segment, bool) {
	type key struct{ table, row int }
	cells := map[key][]string{}
	var order []key
	for _, s := range segs {
		if s.Table == 0 {
			continue
		}
		k := key{s.Table, s.Row}
		if _, seen := cells[k]; !seen {
			order = append(order, k)
		}
		for len(cells[k]) < s.Col {
			cells[k] = append(cells[k], "")
		}
		cells[k][s.Col-1] += " " + Squeeze(s.Text)
	}
	var hits []key
	for _, k := range order {
		c := cells[k]
		if len(c) < 3 || alnumKey(reNoteMark.ReplaceAllString(c[0], " ")) != alnumKey(substance) {
			continue
		}
		hasProduct, hasLimit := false, false
		for _, v := range c[1:] {
			hasProduct = hasProduct || alnumKey(v) == alnumKey(product)
			hasLimit = hasLimit || alnumKey(v) == alnumKey(limit)
		}
		if hasProduct && hasLimit {
			hits = append(hits, k)
		}
	}
	if len(hits) != 1 {
		return segs, false
	}
	h := hits[0]
	out := make([]Segment, 0, len(segs))
	for _, s := range segs {
		if s.Table == h.table && s.Row == h.row {
			continue
		}
		if s.Table == h.table && s.Row > h.row {
			s.Row--
		}
		out = append(out, s)
	}
	return out, true
}

// minKey is the smallest key of a row's cells.
func minKey(m map[int]string) int {
	first := 1 << 30
	for k := range m {
		if k < first {
			first = k
		}
	}
	return first
}

// reLimitMerge is a change made by a table of limits the act prints in its own annex: "I Bilag 2
// indsættes maksimalgrænseværdier som angivet i bilag 1 til denne bekendtgørelse. I tilfælde, hvor
// der allerede eksisterer maksimalgrænseværdier …, ændres disse …".
var reLimitMerge = regexp.MustCompile(`(?i)^\s*i\s+bilag\s+([0-9]+\s?[a-z]?)\s+indsættes\s+maksimalgrænseværdier\s+som\s+angivet\s+i\s+bilag\s+([0-9]+)\s+til\s+denne\b`)

// LimitTableFrom is the act's own annex a change takes a table of limits from, or 0.
func LimitTableFrom(definition string) int {
	if m := reLimitMerge.FindStringSubmatch(Squeeze(definition)); m != nil {
		return Atoi(m[2])
	}
	return 0
}

// mergeLimits carries out reLimitMerge: every limit the act's table prints is set in the law's
// table for its group, under its product (or the group's own column), in its substance's row — a
// row the law's table lacks is put in where the substance falls alphabetically. Every limit is placed,
// or none is.
func (c *Content) mergeLimits(e Edit, reject func(string), apply func(where, note string)) bool {
	m := reLimitMerge.FindStringSubmatch(Squeeze(e.Definition))
	if m == nil {
		return false
	}
	if e.NewAppendix == nil || len(e.NewAppendix.Segments) == 0 {
		reject("the act's own bilag " + m[2] + ", which holds the limits, is not in the mirror")
		return true
	}
	ap, ok := c.annexNamed(m[1])
	if !ok {
		reject("the law has no bilag " + m[1] + " printed as text")
		return true
	}
	segs := cloneSegments(ap.Segments)
	src := e.NewAppendix.Segments
	type limit struct {
		group, product, substance, value string
		part                             string          // "II" of "BÆR OG SMÅFRUGTER II", when the group is split
		single                           bool            // a table of one column of limits
		heads                            map[string]bool // the act table's header, by headKey
		columns                          string          // the header of a table of plain columns
		col                              int             // the column of a table of plain columns
		list                             bool            // a list of substance, product and limit
	}
	var limits []limit
	for _, t := range tablesOf(src) {
		rows := map[int]map[int]string{}
		var order []int
		for _, s := range src {
			if s.Table != t || s.Row == 0 {
				continue
			}
			if rows[s.Row] == nil {
				rows[s.Row] = map[int]string{}
				order = append(order, s.Row)
			}
			rows[s.Row][s.Col] = strings.TrimSpace(rows[s.Row][s.Col] + " " + Squeeze(s.Text))
		}
		sort.Ints(order)
		var header map[int]string // header columns, data numbering
		parts := map[int]string{} // the part a description row gives the columns from its cell on
		single := false
		columns, list := "", false
		for _, r := range order {
			cells := rows[r]
			if strings.TrimSpace(cells[1]) == "" && len(cells) > 1 && !holdsLimit(cells) {
				// "| For fedtindhold i kød … | For rå mælk … | For friske æg …": columns standing over
				// the data's own, not naming a group.
				columns, header, single = columnsKey(cells), nil, false
				continue
			}
			if isListHeader(cells) {
				// "Pesticidrest | Produkt | Maksimalgrænseværdi mg/kg": a list, a row per limit.
				list, columns, header, single = true, "", nil, false
				continue
			}
			if columns != "" && cells[1] != "" && holdsLimit(cells) {
				for col, value := range cells {
					if col > 1 && value != "" {
						limits = append(limits, limit{substance: cells[1], value: value, columns: columns, col: col})
					}
				}
				continue
			}
			if list && cells[1] != "" && holdsLimit(cells) {
				limits = append(limits, limit{substance: cells[1], product: cells[2], value: cells[3], list: true})
				continue
			}
			if len(cells) == 2 && cells[1] != "" && reLimitColumn.MatchString(cells[2]) {
				// "Pesticidrester i og på cerealier | Maksimalgrænseværdier i mg/kg": one column.
				header, single = map[int]string{1: cells[1], 2: cells[1]}, true
				continue
			}
			if len(cells) > 1 && !holdsLimit(cells) && namesGroup(cells) {
				// "TE | HUMLE | KARTOFLER" over "Te (tørrede blade …) | HUMLE, tørret … | KARTOFLER |
				// Nye kartofler": a group of one column is named in the row above its header.
				above := map[int]string{}
				if !single {
					above = header
				}
				single = false
				header = map[int]string{}
				for col, text := range cells {
					header[col+1] = text
					if !isGroupHeading(text) && isGroupHeading(above[col+1]) {
						header[col+1] = above[col+1]
					}
				}
				continue
			}
			if !holdsLimit(cells) {
				for col, text := range cells {
					if p := reGroupPart.FindStringSubmatch(text); p != nil {
						parts[col+1] = p[1]
					}
				}
			}
			if header == nil || cells[1] == "" || !holdsLimit(cells) {
				// "KERNEFRUGTER Friske, tørrede … | STENFRUGTER …" describes the groups below.
				continue
			}
			for col, value := range cells {
				if col == 1 || value == "" {
					continue
				}
				h, ok := header[col]
				if !ok {
					reject(fmt.Sprintf("bilag %s of the act prints %s for %s under no heading (table %d, row %d)", m[2], value, cells[1], t, r))
					return true
				}
				if single {
					limits = append(limits, limit{group: header[1], substance: cells[1], value: value, single: true})
					continue
				}
				group := ""
				for gc := col; gc >= 2; gc-- {
					if isGroupHeading(header[gc]) {
						group = header[gc]
						break
					}
				}
				product := h
				if h == group {
					product = ""
				}
				part, from := "", 0
				for pc, p := range parts {
					if pc <= col && pc > from {
						part, from = p, pc
					}
				}
				heads := map[string]bool{}
				for _, h := range header {
					heads[headKey(h)] = true
				}
				limits = append(limits, limit{group: group, product: product, substance: cells[1], value: value, part: part, heads: heads})
			}
		}
	}
	if len(limits) == 0 {
		reject("bilag " + m[2] + " of the act prints no table of limits this model reads")
		return true
	}
	for _, l := range limits {
		if l.columns != "" || l.list {
			var why string
			if segs, why = placeLimit(segs, l.substance, l.product, l.value, l.columns, l.col); why != "" {
				reject("bilag " + m[1] + ": " + why)
				return true
			}
			continue
		}
		var found []tableGroup
		for _, t := range tablesOf(segs) {
			g, ok := readGroup(segs, t, l.group)
			if !ok {
				continue
			}
			// A table of contents names the groups too, with their products in one cell.
			if l.product != "" {
				if _, why := g.productCol(l.product); why != "" {
					continue
				}
			} else if _, why := g.productCol(""); l.single && why != "" {
				continue
			} else if !l.single && len(g.headers) < 3 {
				continue
			}
			found = append(found, g)
		}
		if len(found) > 1 && l.part != "" {
			var named []tableGroup
			for _, g := range found {
				if slices.Contains(g.parts(), l.part) {
					named = append(named, g)
				}
			}
			found = named
		}
		if len(found) > 1 && l.heads != nil {
			// A group printed over two tables repeats its heading: the act's table is the one
			// heading the same products.
			best, score, tie := tableGroup{}, -1, false
			for _, g := range found {
				n := 0
				for _, h := range g.headers {
					if l.heads[headKey(h)] {
						n++
					}
				}
				switch {
				case n > score:
					best, score, tie = g, n, false
				case n == score:
					tie = true
				}
			}
			if !tie {
				found = []tableGroup{best}
			}
		}
		if len(found) != 1 {
			reject(fmt.Sprintf("bilag %s: %d tables hold the group %s", m[1], len(found), l.group))
			return true
		}
		g := found[0]
		col := g.group
		if l.single {
			col, _ = g.productCol("")
		}
		if l.product != "" {
			c, why := g.productCol(l.product)
			if why != "" {
				reject("bilag " + m[1] + ": " + why)
				return true
			}
			col = c
		}
		row := g.rowOf(l.substance)
		if row == 0 {
			row = g.alphabeticalRow(l.substance)
			segs = g.insertRow(segs, row, l.substance)
		}
		segs = setCell(segs, g.table, row, col, l.value)
	}
	ap.Segments = segs
	apply("bilag "+m[1], fmt.Sprintf("%d limits from bilag %s of the act set in the tables of their groups", len(limits), m[2]))
	return true
}

// alphabeticalRow is the row a substance the table lacks takes: before the first it sorts before.
func (g *tableGroup) alphabeticalRow(substance string) int {
	g.rowOf("") // read the rows
	want := strings.ToLower(reNoteMark.ReplaceAllString(substance, " "))
	names := map[int]string{}
	last := g.header
	for _, s := range g.segs {
		if s.Table == g.table && s.Row > g.header && s.Row < g.end && s.Col == 1 {
			names[s.Row] += " " + Squeeze(s.Text)
			if s.Row > last {
				last = s.Row
			}
		}
	}
	best := last + 1
	for row, name := range names {
		if strings.TrimSpace(strings.ToLower(name)) > strings.TrimSpace(want) && row < best {
			best = row
		}
	}
	return best
}

// annexNamed is the annex numbered as label prints it, "4A" or "2", when it is printed as text.
func (c *Content) annexNamed(label string) (*Appendix, bool) {
	want := strings.ToLower(strings.ReplaceAll(label, " ", ""))
	for i := range c.Appendices {
		if strings.ToLower(strings.ReplaceAll(c.Appendices[i].Number, " ", "")) == want {
			ap := &c.Appendices[i]
			return ap, len(ap.Order) == 0
		}
	}
	return nil, false
}

// reLimitColumn heads a table's one column of limits: "Maksimalgrænseværdier i mg/kg".
var reLimitColumn = regexp.MustCompile(`(?i)^maksimalgrænseværdi`)

// reLimitValue is a limit: "0,01*", "10p", "5", or one among words, "Okselever … 0,02. Andre: 0,01*".
// A header's "(1)(4)" and "pos. 0401, 0402" are not.
var reLimitValue = regexp.MustCompile(`^\s*<?\d|\d,\d`)

// holdsLimit reports whether a row prints a limit outside its first column.
func holdsLimit(cells map[int]string) bool {
	for col, text := range cells {
		if col > 1 && reLimitValue.MatchString(text) {
			return true
		}
	}
	return false
}

// namesGroup reports a header row naming a group of products: its first cell or one of its others in
// capitals.
func namesGroup(cells map[int]string) bool {
	for _, text := range cells {
		if isGroupHeading(text) {
			return true
		}
	}
	return false
}

// reGroupPart is the part a group split over two tables is in: "BÆR OG SMÅFRUGTER II, friske …".
var reGroupPart = regexp.MustCompile(`^[A-ZÆØÅ][A-ZÆØÅ -]+\s(I{1,3}|IV),?(?:\s|$)`)

// parts are the parts of split groups the rows above the table's header name.
func (g *tableGroup) parts() []string {
	var out []string
	for _, s := range g.segs {
		if s.Table == g.table && s.Row > 0 && s.Row < g.header {
			for _, m := range reGroupPartAny.FindAllStringSubmatch(Squeeze(s.Text), -1) {
				out = append(out, m[1])
			}
		}
	}
	return out
}

var reGroupPartAny = regexp.MustCompile(`[A-ZÆØÅ]{2,}\s(I{1,3}|IV)(?:,|\s|$)`)

// headKey is a header cell as the hyphenation and spacing of the print leave it the same.
func headKey(text string) string {
	return strings.ToLower(strings.NewReplacer("-", "", " ", "", "\u00ad", "").Replace(Squeeze(text)))
}

// columnsKey is a header row of plain columns as their texts and places give it.
func columnsKey(cells map[int]string) string {
	var cols []int
	for col, text := range cells {
		if strings.TrimSpace(text) == "" {
			continue
		}
		cols = append(cols, col)
	}
	sort.Ints(cols)
	var b strings.Builder
	for _, col := range cols {
		// "For fedtindhold i kød, tilberedt kød …": the act and the law word the heading apart past
		// its opening.
		fmt.Fprintf(&b, "%d:%s|", col, strings.Join(strings.Fields(strings.ToLower(cells[col]))[:min(2, len(strings.Fields(cells[col])))], " "))
	}
	return b.String()
}

// placeLimit sets substance's limit in the one table of segs that has the header columns gives, in
// column col, or — columns empty — in the list of substance, product and limit, putting in a row the
// table lacks where it falls alphabetically.
func placeLimit(segs []Segment, substance, product, value, columns string, col int) ([]Segment, string) {
	type head struct{ table, row int }
	var found []head
	for _, t := range tablesOf(segs) {
		cells := map[int]map[int]string{}
		for _, s := range segs {
			if s.Table == t && s.Row > 0 {
				if cells[s.Row] == nil {
					cells[s.Row] = map[int]string{}
				}
				cells[s.Row][s.Col] = strings.TrimSpace(cells[s.Row][s.Col] + " " + Squeeze(s.Text))
			}
		}
		for r, c := range cells {
			if columns != "" && strings.TrimSpace(c[1]) == "" && !holdsLimit(c) && columnsKey(c) == columns ||
				columns == "" && isListHeader(c) {
				found = append(found, head{t, r})
			}
		}
	}
	if len(found) != 1 {
		return segs, fmt.Sprintf("%d tables have the columns the act's table prints", len(found))
	}
	g := tableGroup{table: found[0].table, header: found[0].row, end: 1 << 30, segs: segs}
	if columns != "" {
		row := g.rowOf(substance)
		if row == 0 {
			row = g.alphabeticalRow(substance)
			segs = g.insertRow(segs, row, substance)
		}
		return setCell(segs, g.table, row, col, value), ""
	}
	// A list prints a substance once per product, in the order of substance and product.
	names, products := map[int]string{}, map[int]string{}
	last := g.header
	for _, s := range segs {
		if s.Table == g.table && s.Row > g.header {
			switch s.Col {
			case 1:
				names[s.Row] += " " + Squeeze(s.Text)
			case 2:
				products[s.Row] += " " + Squeeze(s.Text)
			}
			last = max(last, s.Row)
		}
	}
	// The list orders its substances, not each substance's products: a new product follows the
	// substance's last.
	subst := func(name string) string { return alnumKey(reNoteMark.ReplaceAllString(name, " ")) }
	want := subst(substance)
	row, hits, after := last+1, 0, 0
	for r, name := range names {
		k := subst(name)
		switch {
		case k == want && headKey(products[r]) == headKey(product):
			row = r
			hits++
		case k == want:
			after = max(after, r)
		case k > want && r < row && hits == 0 && after == 0:
			row = r
		}
	}
	if hits == 0 && after != 0 {
		row = after + 1
	}
	if hits == 0 {
		// "Tolylfluanid | Æbler, pærer | 2" over the rows "Æbler | 1" and "Pærer | 1": one row for
		// the products the list printed apiece.
		var apiece []int
		for _, p := range reProductList.Split(product, -1) {
			for r, name := range names {
				if subst(name) == want && headKey(products[r]) == headKey(p) && !slices.Contains(apiece, r) {
					apiece = append(apiece, r)
				}
			}
		}
		if parts := len(reProductList.Split(product, -1)); parts > 1 && len(apiece) == parts {
			slices.Sort(apiece)
			for i := len(apiece) - 1; i > 0; i-- {
				segs = removeRow(segs, g.table, apiece[i])
			}
			segs = setCell(segs, g.table, apiece[0], 2, product)
			return setCell(segs, g.table, apiece[0], 3, value), ""
		}
	}
	switch {
	case hits > 1:
		return segs, fmt.Sprintf("the list prints %s for %s %d times", substance, product, hits)
	case hits == 0:
		segs = g.insertRow(segs, row, substance)
		segs = setCell(segs, g.table, row, 2, product)
	}
	return setCell(segs, g.table, row, 3, value), ""
}

// isListHeader reports the header of a list of limits: "Pesticidrest | Produkt | Maksimalgrænseværdi
// mg/kg".
func isListHeader(cells map[int]string) bool {
	return len(cells) == 3 && headKey(cells[2]) == "produkt" && strings.HasPrefix(headKey(cells[3]), "maksimalgrænseværdi")
}

// reProductList separates the products a list's cell names together: "Æbler, pærer og kvæder".
var reProductList = regexp.MustCompile(`\s*,\s*|\s+og\s+`)

// removeRow removes a table's row, moving its later rows up.
func removeRow(segs []Segment, table, row int) []Segment {
	out := make([]Segment, 0, len(segs))
	for _, s := range segs {
		if s.Table == table && s.Row == row {
			continue
		}
		if s.Table == table && s.Row > row {
			s.Row--
		}
		out = append(out, s)
	}
	return out
}

// reContentsChange is a change to the table of contents an annex opens with, its operations
// following: "I bilag 3 foretages følgende ændringer i indholdsfortegnelsen: Teksten ”Regel 3-13
// Konstruktion …” slettes Efter teksten ”Regel 19 Havarikontrolplaner” indsættes ny række med
// følgende tekst: Regel 19-1 Øvelser i havarikontrol for passagerskibe".
var (
	reContentsChange = regexp.MustCompile(`(?i)^(?:\d+\.\s*)?i\s+bilag\s+(\d+\s?[a-z]?)\s+foretages\s+følgende\s+ændringer\s+i\s+indholdsfortegnelsen:\s*(.+)$`)
	reContentsOp     = regexp.MustCompile(`(?i)(?:^|\s)(?:teksten\s+[”"“»]([^”"“«]+)[”"“«]\s+slettes|efter\s+teksten\s+[”"“»]([^”"“«]+)[”"“«]\s+indsættes\s+(?:en\s+)?ny\s+række\s+med\s+følgende\s+tekst:)`)
)

// changeContents carries out reContentsChange on the table the rows are printed in: a row printing
// the quoted text, its cells read together, is removed, and a new row goes after the one printing
// the anchor, its cells split as the anchor's are. Every operation is carried out, or none is.
func (c *Content) changeContents(e Edit, reject func(string), apply func(where, note string)) {
	m := reContentsChange.FindStringSubmatch(Squeeze(e.Prose))
	ap, ok := c.annexNamed(m[1])
	if !ok {
		reject("the law has no bilag " + m[1] + " printed as text")
		return
	}
	body := m[2]
	ops := reContentsOp.FindAllStringSubmatchIndex(body, -1)
	if len(ops) == 0 {
		reject("the change to the table of contents of bilag " + m[1] + " says nothing this model reads")
		return
	}
	if strings.TrimSpace(body[:ops[0][0]]) != "" {
		reject("the change to the table of contents of bilag " + m[1] + " opens with words this model does not read")
		return
	}
	segs := cloneSegments(ap.Segments)
	rowOf := func(text string) (int, int, string) {
		type key struct{ table, row int }
		rows := map[key]string{}
		for _, s := range segs {
			if s.Table != 0 {
				rows[key{s.Table, s.Row}] += " " + s.Text
			}
		}
		var hits []key
		for k, t := range rows {
			if alnumKey(t) == alnumKey(text) {
				hits = append(hits, k)
			}
		}
		if len(hits) != 1 {
			return 0, 0, fmt.Sprintf("bilag %s has %d rows printing %q", m[1], len(hits), text)
		}
		return hits[0].table, hits[0].row, ""
	}
	for i, op := range ops {
		end := len(body)
		if i+1 < len(ops) {
			end = ops[i+1][0]
		}
		if op[2] >= 0 {
			if strings.TrimSpace(body[op[1]:end]) != "" {
				reject("the change to the table of contents of bilag " + m[1] + " says more after a removal than this model reads")
				return
			}
			t, r, why := rowOf(body[op[2]:op[3]])
			if why != "" {
				reject(why)
				return
			}
			segs = removeRow(segs, t, r)
			continue
		}
		text := strings.TrimSpace(body[op[1]:end])
		t, r, why := rowOf(body[op[4]:op[5]])
		if why != "" || text == "" {
			reject(why + "; or the new row prints nothing")
			return
		}
		// "Regel 19 | Havarikontrolplaner": the new row's first cell takes as many words as the
		// anchor's.
		first := len(strings.Fields(cellText(segs, t, r, 1)))
		words := strings.Fields(text)
		if first == 0 || first >= len(words) {
			reject(fmt.Sprintf("bilag %s: the new row %q does not split as the row it follows", m[1], text))
			return
		}
		g := tableGroup{table: t, segs: segs}
		segs = g.insertRow(segs, r+1, strings.Join(words[:first], " "))
		segs = setCell(segs, t, r+1, 2, strings.Join(words[first:], " "))
	}
	ap.Segments = segs
	apply("bilag "+m[1]+", indholdsfortegnelsen", fmt.Sprintf("%d rows of the table of contents changed", len(ops)))
}

// reAnnexAfterNumber is an insertion into a numbered scheme: "I bilag 4 og bilag 5 indsættes efter
// nr. 3.3. som nyt nummer: »3.4. Ændring i risikomargen«".
var reAnnexAfterNumber = regexp.MustCompile(`(?i)\bindsættes\s+efter\s+nr\.\s*([0-9]+(?:\.[0-9]+)+)\.?\s+som\s+nyt\s+nummer\b`)

// reSchemeEntry is a scheme's entry: its number and its name, "3.4. Ændring i risikomargen".
var reSchemeEntry = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)+\.)\s+(.+?)\.?$`)

// insertNumberedRow puts a new entry of an annex's numbered table after the one row printing the
// number named, number and name in the cells that row prints them in. It went to the end of the
// annex as a line of its own.
func insertNumberedRow(ap *Appendix, after, entry string) (string, bool) {
	m := reSchemeEntry.FindStringSubmatch(Squeeze(entry))
	if m == nil {
		return "", false
	}
	want := after + "."
	table, row, col, hits := 0, 0, 0, 0
	for _, s := range ap.Segments {
		if s.Table != 0 && Squeeze(s.Text) == want {
			table, row, col, hits = s.Table, s.Row, s.Col, hits+1
		}
	}
	if hits != 1 {
		return "", false
	}
	segs := cloneSegments(ap.Segments)
	g := tableGroup{table: table, segs: segs}
	segs = g.insertRow(segs, row+1, "")
	segs = setCell(segs, table, row+1, col, m[1])
	segs = setCell(segs, table, row+1, col+1, m[2])
	if col != 1 {
		segs = setCell(segs, table, row+1, 1, "")
	}
	// "som nyt nummer": the entries after it under the same heading move up, as conditions do, or
	// the scheme printed 3.4. twice.
	prefix := m[1][:strings.LastIndex(strings.TrimSuffix(m[1], "."), ".")+1]
	for i := range segs {
		s := &segs[i]
		if s.Table != table || s.Row <= row+1 || s.Col != col {
			continue
		}
		t := Squeeze(s.Text)
		if !strings.HasPrefix(t, prefix) || !reSchemeNumber.MatchString(t) {
			break
		}
		n := Atoi(strings.TrimSuffix(strings.TrimPrefix(t, prefix), "."))
		s.Text = prefix + itoa(n+1) + "."
	}
	ap.Segments = segs
	return "nr. " + after, true
}

// reSchemeNumber is a scheme entry's number alone: "3.4.".
var reSchemeNumber = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)+\.$`)
