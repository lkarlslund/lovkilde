// Command annexshape counts what actually arrives inside an annex, in the publisher's element
// tree and in the model, for a document read the way the server reads it.
//
// M025 needs node kinds for annex interiors: 47 of BR18's 83 refusals address "Bilag 1, tabel 2",
// and there is no table node to splice an amending act's re-issue into. Choosing the kinds from
// the citation forms alone would invent them, so this measures what is really there instead:
//
//   - the element names below each <Bilag>, counted, so the shapes are the schema's and not ours;
//   - printed numbers that are decimal paths ("1.3.5", "8.4.2.2"), which is what BR10 cites and
//     what Address cannot store today because a chapter is an int;
//   - what the model ended up with per annex: how many children it gave the annex, and the text
//     it had no concept for, grouped by the mirror's own concept name.
//
// usage: annexshape <accession> [accession ...]
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/lkarlslund/lovkilde/internal/lexdania"
	"github.com/lkarlslund/lovkilde/internal/lexdoc"
)

// decimalNumber matches a structural number at the head of an element's own text: "1.3.5 Bygningers
// ...". It is a prefix match on purpose — the heading's words follow the number, and anchoring the
// end found exactly one of the 210 such headings in BR10.
var decimalNumber = regexp.MustCompile(`^([0-9]+(\.[0-9]+)+)`)

// tableish names the elements that hold a table or a piece of one, if the schema names them at
// all. It is a guess worth printing even when it matches nothing: a zero here is the finding.
var tableish = regexp.MustCompile(`(?i)^(tr|td|th|r|c|rc|rader|celler|celle|kolonner|kolonne|rad|raekke|row|table|tabel|tabellag|billoptagelse)$`)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-addresses" {
		countPaths()
		return
	}
	if len(os.Args) < 2 {
		fmt.Println("usage: annexshape <accession> [accession ...]")
		fmt.Println("       annexshape -addresses   count paths in amendment prose read from stdin")
		os.Exit(2)
	}
	a, err := lexdania.Scan(context.Background(), "data/xml")
	if err != nil {
		fmt.Println("scan:", err)
		os.Exit(1)
	}
	for _, acc := range os.Args[1:] {
		census(a, acc)
	}
}

// A second question, asked with -addresses: given amendment prose on stdin, one change per
// line, how many of them now have a path? This is how the citation forms that BR10 refuses were
// counted, and it is the number to watch when the resolver starts walking steps: a line with no
// path is an address the mirror still cannot name.
func countPaths() {
	byKind := map[string]int{}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	lines, withPath := 0, 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lines++
		a := lexdoc.ParseAddress(line)
		if len(a.Steps) == 0 {
			fmt.Printf("no path   %s\n", truncate(line, 100))
			continue
		}
		withPath++
		for _, s := range a.Steps {
			byKind[s.Kind]++
		}
		fmt.Printf("%-24s %s\n", a.Path(), truncate(line, 76))
	}
	fmt.Printf("\n%d changes, %d with a path (%.1f%%)\n", lines, withPath, 100*float64(withPath)/float64(lines))
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Printf("  %-9s %d\n", k, byKind[k])
	}
}

func census(a *lexdania.Archive, acc string) {
	law, _, ok := a.Open(acc)
	if !ok {
		fmt.Printf("%s: no such accession\n", acc)
		return
	}
	fmt.Printf("\n%s  %s\n  source=%v sidecar=%q\n", acc, truncate(law.Title(), 70), law.Source, law.Sidecar)

	annexes := law.Root.FindNamed("Bilag")
	fmt.Printf("  tree: %d <Bilag> element(s)\n", len(annexes))
	for i, n := range annexes {
		names := map[string]int{}
		decimals, decimalExamples := 0, []string{}
		n.Descendants(func(c *lexdania.Node) {
			names[c.Name]++
			// A number printed at the head of an element's own text is how the schema marks a
			// structural number: an Explicatus only sometimes, and in annexes usually not at all.
			own := strings.TrimSpace(c.Runs[0])
			if decimalNumber.MatchString(own) {
				decimals++
				if len(decimalExamples) < 3 {
					decimalExamples = append(decimalExamples, c.Name+" "+truncate(own, 24))
				}
			}
		})
		tables := 0
		for name, count := range names {
			if tableish.MatchString(name) {
				tables += count
			}
		}
		fmt.Printf("    annex %d  label=%-18q  runes=%-7d  element kinds=%d  table-ish elements=%d  decimal numbers=%d %v\n",
			i+1, firstNonEmpty(labelOf(n), "(none)"), len([]rune(n.PlainText())), len(names), tables, decimals, decimalExamples)
		fmt.Printf("      %s\n", topNames(names, 10))
	}

	m := law.Model()
	fmt.Printf("  model: %d annex(es)\n", len(m.Content.Appendices))
	for _, ap := range m.Content.Appendices {
		parts, provisions := 0, 0
		for _, c := range ap.Order {
			if c.Provision != nil {
				provisions++
			}
			if c.Part != nil {
				parts++
			}
		}
		concepts := map[string]int{}
		for _, u := range ap.Unmapped {
			concepts[u.Concept] += len([]rune(u.Words()))
		}
		concepts["(segments)"] = 0
		for _, s := range ap.Segments {
			concepts["(segments)"] += len([]rune(s.Text))
		}
		fmt.Printf("    annex %-3s label=%-18q children=%d (provisions %d, parts %d)  unmapped/other runes: %s\n",
			ap.Number, truncate(ap.Label, 18), len(ap.Order), provisions, parts, runeSummary(concepts))
	}
}

// labelOf is how the annex reader names an annex, repeated here rather than imported so the tool
// reports what the tree says and not what the model decided to keep.
func labelOf(n *lexdania.Node) string {
	for _, name := range []string{"Explicatus", "Rubrica"} {
		if e := n.Elem(name); e != nil {
			if t := strings.TrimSpace(e.Text()); t != "" {
				return t
			}
		}
	}
	return ""
}

func topNames(names map[string]int, limit int) string {
	type pair struct {
		name  string
		count int
	}
	list := make([]pair, 0, len(names))
	for name, count := range names {
		list = append(list, pair{name, count})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].count != list[j].count {
			return list[i].count > list[j].count
		}
		return list[i].name < list[j].name
	})
	var b strings.Builder
	for i, p := range list {
		if i == limit {
			fmt.Fprintf(&b, "… +%d more kinds", len(list)-limit)
			break
		}
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%s=%d", p.name, p.count)
	}
	return b.String()
}

func runeSummary(concepts map[string]int) string {
	names := make([]string, 0, len(concepts))
	for name := range concepts {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		if concepts[name] == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("  ")
		}
		fmt.Fprintf(&b, "%s=%d", name, concepts[name])
	}
	if b.Len() == 0 {
		return "none"
	}
	return b.String()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
