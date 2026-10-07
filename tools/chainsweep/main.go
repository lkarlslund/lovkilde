// Command chainsweep consolidates a spread of laws to today and counts what the engine applied and
// refused, grouped by the reason it gave.
//
// tools/refusals explains one law's refusals in depth; this is the census that says which reason
// to work on next, and whether a change to the engine moved the totals. It was the measurement
// behind retiring the tree engine and reading prose change lists: over 799 laws since 2005 the
// model engine went from 7,549 changes placed and 2,376 refused to 9,633 and 1,237, against the
// tree engine's 7,915 and 2,010.
//
// -solvable sorts each refusal by the evidence for placing it, against the law as the act met it,
// and that sorting took the census to 12,566 placed and 367 refused (see the README). -dump writes
// every refusal to a file, one per line, so two runs can be diffed.
//
// A falling refusal count is not on its own evidence of correct placement; run tools/pairs and
// tools/goldenpair beside it, which check the result against the register's own words.
//
// Usage:
//
//	go run ./tools/chainsweep [-n 1000] [-examples 2] [-solvable] [-diagnose] [-dump file]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

func main() {
	n := flag.Int("n", 1000, "laws to consolidate, spread evenly over the archive")
	examples := flag.Int("examples", 2, "examples printed per refusal reason")
	diagnose := flag.Bool("diagnose", false, "explain each refused address against the text the act met")
	solvable := flag.Bool("solvable", false, "sort refusals into fixable, knock-on and unplaceable")
	dump := flag.String("dump", "", "write every refusal to this file, one per line, for diffing two runs")
	flag.Parse()

	a, err := lexdania.Scan(context.Background(), "data/xml")
	if err != nil {
		fmt.Println("scan:", err)
		os.Exit(1)
	}
	var entries []lexdania.Entry
	for _, e := range a.Entries() {
		if (e.Kind == "lta" || e.Kind == "ltc") && e.Year >= 2005 {
			entries = append(entries, e)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Accession < entries[j].Accession })
	step := max(1, len(entries)/(*n*4)) // most laws have no chain; stride wide enough to find n that do

	asOf := time.Now()
	var elapsed time.Duration
	laws, applied, refused, invalid := 0, 0, 0, 0
	reasons := map[string]int{}
	var lines []string
	shown := map[string][]string{}
	for i := 0; i < len(entries) && laws < *n; i += step {
		e := entries[i]
		vs, err := a.Versions(e.Accession)
		if err != nil || len(vs) == 0 {
			continue
		}
		laws++
		start := time.Now()
		v, err := a.LawAsOf(e.Accession, asOf)
		elapsed += time.Since(start)
		if err != nil {
			continue
		}
		if err := v.Content.Validate(); err != nil {
			invalid++
			if invalid <= 5 {
				fmt.Printf("INVALID %s: %v\n", e.Accession, err)
			}
		}
		applied += len(v.Applied)
		for _, ap := range v.Applied {
			lines = append(lines, fmt.Sprintf("A %s ← %s %s %s @%s", e.Accession, ap.Edit.Source, ap.Edit.Number, ap.Edit.Kind, ap.Where))
		}
		refused += len(v.Unresolved)
		var refusedSoFar []lexdoc.Edit
		for _, u := range v.Unresolved {
			lines = append(lines, fmt.Sprintf("%s ← %s %s %s || %s", e.Accession, u.Edit.Source, u.Edit.Number, u.Edit.Address, u.Reason))
			if *solvable {
				class := solvability(a, e.Accession, v, u, refusedSoFar)
				if v.Tentative[u.Edit.Source] {
					class += " [act cites an earlier consolidation]"
				}
				refusedSoFar = append(refusedSoFar, u.Edit)
				key := class + " | " + string(u.Edit.Kind)
				reasons[key]++
				if len(shown[key]) < *examples {
					shown[key] = append(shown[key], fmt.Sprintf("%s ← %s %s %s || %s", e.Accession, u.Edit.Source, u.Edit.Number, cut(oneLine(u.Edit.Prose), 150), cut(u.Reason, 90)))
				}
				continue
			}
			// Quoted addresses are cut off, so "§ 21, stk. 2 not found" and "§ 523 not found"
			// count as the one reason they are.
			r := u.Reason
			if k := strings.IndexByte(r, '"'); k >= 0 {
				r = r[:k] + "…"
			}
			key := string(u.Edit.Kind) + ": " + r
			if *diagnose && strings.Contains(u.Reason, "not found") {
				key = "address not found: " + diagnoseAddress(a, e.Accession, v, u.Edit)
			}
			reasons[key]++
			if len(shown[key]) < *examples {
				shown[key] = append(shown[key], fmt.Sprintf("%s ← %s %s %s", e.Accession, u.Edit.Source, u.Edit.Number, cut(oneLine(u.Edit.Prose), 160)))
			}
		}
	}
	if *dump != "" {
		if err := os.WriteFile(*dump, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			fmt.Println("dump:", err)
		}
	}
	fmt.Printf("%d laws as of %s: applied %d, refused %d, %d with broken structure, %v consolidating\n\n",
		laws, asOf.Format(time.DateOnly), applied, refused, invalid, elapsed.Round(time.Millisecond))
	keys := make([]string, 0, len(reasons))
	for k := range reasons {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return reasons[keys[i]] > reasons[keys[j]] })
	for _, k := range keys {
		fmt.Printf("%5d  %s\n", reasons[k], k)
		for _, ex := range shown[k] {
			fmt.Printf("         %s\n", ex)
		}
	}
}

// solvability sorts a refusal by the evidence for placing it. Fixable: the text the act met holds
// the passage it quotes, or prints the unit it names. Knock-on: an earlier refused change in the
// same chain brought that passage or unit. Unplaceable: neither, so nothing in the data can place it.
func solvability(a *lexdania.Archive, law string, v *lexdania.View, u lexdoc.UnresolvedEdit, earlier []lexdoc.Edit) string {
	e := u.Edit
	before, err := a.LawBefore(law, e.Source)
	if err != nil {
		return "unknown"
	}
	if len(before.Content.Provisions()) == 0 && e.Address.Paragraph != "" {
		return "READER: the law was read with no provisions at all"
	}
	text := lexdoc.MatchText(before.Content.PlainText())
	for _, ap := range before.Content.Appendices {
		text += " " + lexdoc.MatchText(lexdoc.SegmentsText(ap.Segments))
	}
	text += " " + lexdoc.MatchText(lexdoc.SegmentsText(before.Content.Title)) + " " + lexdoc.MatchText(lexdoc.SegmentsText(before.Content.Preamble))
	passage := lexdoc.QuoteStrip(e.Old)
	if passage == "" {
		passage = lexdoc.InsertionAnchor(e)
	}
	brought := func(needle string) bool {
		needle = lexdoc.MatchText(needle)
		if needle == "" {
			return false
		}
		for _, x := range earlier {
			body := x.New + " " + x.Prose
			for _, s := range x.NewText {
				body += " " + s.Text
			}
			if strings.Contains(lexdoc.MatchText(body), needle) {
				return true
			}
		}
		return false
	}
	if passage != "" {
		inProvision := false
		if e.Address.Paragraph != "" {
			if loc, ok := before.Content.Find(lexdoc.Address{Recognised: true, Paragraph: e.Address.Paragraph}); ok && loc.Provision != nil {
				inProvision = strings.Contains(lexdoc.MatchText(loc.Provision.Text()), lexdoc.MatchText(passage))
			}
		}
		inUnit := false
		if loc, ok := before.Content.Find(e.Address); ok && loc.Provision != nil {
			sp := lexdoc.Span{From: 0, To: len(loc.Provision.Segments)}
			switch {
			case loc.Item != nil:
				sp = loc.Item.Span
			case loc.Stykke != nil:
				sp = loc.Stykke.Span
			}
			inUnit = strings.Contains(lexdoc.MatchText(loc.Provision.SpanText(sp)), lexdoc.MatchText(passage))
		}
		if !inUnit && e.Address.Paragraph != "" && e.Kind == lexdoc.EditReplaceText {
			if loc, ok := before.Content.Find(lexdoc.Address{Recognised: true, Paragraph: e.Address.Paragraph}); ok && loc.Provision != nil {
				if nw := lexdoc.MatchText(lexdoc.QuoteStrip(e.New)); nw != "" && strings.Contains(lexdoc.MatchText(loc.Provision.Text()), nw) && !strings.Contains(nw, lexdoc.MatchText(passage)) {
					return fmt.Sprintf("ALREADY: the provision already prints the new wording (%d letters)", len([]rune(nw)))
				}
			}
		}
		if !inUnit && e.Address.Paragraph != "" {
			if loc, ok := before.Content.Find(lexdoc.Address{Recognised: true, Paragraph: e.Address.Paragraph}); ok && loc.Provision != nil {
				if d, w := closest(loc.Provision.Text(), passage); d >= 0 && d*5 <= len([]rune(squash(passage))) && d > 0 {
					if digitsOf(w) != digitsOf(squash(passage)) {
						return "VERSIONS: the provision prints the passage with other numbers; the act and this text disagree on a figure or a citation"
					}
					return fmt.Sprintf("WORDING: the provision prints the passage in other words (%d letters apart): %q", d, w)
				}
			}
		}
		switch {
		case inUnit:
			if out := before.Content.Clone().Apply(e); out.Applied {
				return "IN-ACT: applies alone; an earlier change in the same act took its words"
			}
			return "BUG: the quoted passage is in the addressed unit, and the change fails even alone"
		case inProvision:
			return "MISMATCH: the passage is in the addressed provision, not in the unit the address names"
		case strings.Contains(text, lexdoc.MatchText(passage)):
			return "ELSEWHERE: the quoted passage is in the law, not in the addressed provision" + chainGap(v, e.Source)
		case brought(passage):
			return "KNOCK-ON: an earlier refused change brought the passage"
		case strings.Contains(squash(text), squash(passage)):
			return "NEAR: the passage is in the law once spacing, punctuation and case are ignored"
		}
		return "UNPLACEABLE: the quoted passage is nowhere in the law" + chainGap(v, e.Source)
	}
	if strings.Contains(u.Reason, "not found") {
		d := diagnoseAddress(a, law, v, e)
		switch {
		case strings.Contains(d, "printed in the text"):
			return "FIXABLE: the unit is printed in the law (" + d + ")"
		case e.Address.Paragraph != "" && brought("§ "+e.Address.Paragraph):
			return "KNOCK-ON: an earlier refused change brought the provision"
		}
		return "UNPLACEABLE: the unit is not in the law (" + d + ")" + chainGap(v, e.Source)
	}
	return "REVIEW: " + u.Reason
}

// diagnoseAddress says which level of a refused address the text the act met lacks, and whether
// the unit's printed marker is in the text anyway: a marker in the text is a reader gap, its absence
// is a fact about the law.
func diagnoseAddress(a *lexdania.Archive, law string, v *lexdania.View, e lexdoc.Edit) string {
	var eff time.Time
	for _, c := range v.Chain {
		if c.Accession == e.Source {
			eff = c.Effective
		}
	}
	before, err := a.LawAdoptedAsOf(law, eff.AddDate(0, 0, -1))
	if err != nil {
		return "unknown"
	}
	src := string(before.Doc.Provenance.Source)
	ad := e.Address
	c := before.Content
	switch {
	case ad.Chapter != 0 && ad.Paragraph == "":
		return src + " chapter " + inline(strings.Contains(c.PlainText(), fmt.Sprintf("Kapitel %d", ad.Chapter)))
	case ad.Bilag != 0:
		return src + " annex"
	case ad.Paragraph == "":
		return src + " no §"
	}
	loc, ok := c.Find(lexdoc.Address{Recognised: true, Paragraph: ad.Paragraph})
	if !ok {
		return src + " § " + inline(strings.Contains(c.PlainText(), "§ "+ad.Paragraph+".") || strings.Contains(c.PlainText(), "§\u00a0"+ad.Paragraph+"."))
	}
	p := loc.Provision
	text := p.OwnText() // a "Stk. 4." inside a footnote is the note's, not the provision's
	if ad.Stk != 0 {
		if p.Stykke(ad.Stk) == nil {
			return src + fmt.Sprintf(" stk (has %d) ", len(p.Stykker)) + inline(strings.Contains(strings.ReplaceAll(text, "\u00a0", " "), fmt.Sprintf("Stk. %d.", ad.Stk)))
		}
		text = p.SpanText(p.Stykke(ad.Stk).Span)
	}
	if ad.Nr != 0 {
		return src + " nr " + inline(strings.Contains(text, fmt.Sprintf("%d)", ad.Nr)))
	}
	if ad.Litra != "" {
		return src + " litra " + inline(strings.Contains(text, ad.Litra+")"))
	}
	return src + " other"
}

func inline(present bool) string {
	if present {
		return "printed in the text (reader gap)"
	}
	return "absent"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// squash keeps only letters and digits, lower-cased: what is left of a passage when its spacing,
// hyphenation and punctuation are not counted.
func squash(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// chainGap says whether the chain before an act has a hole the refusal may fall through: an
// earlier amending act the mirror does not hold, or holds and read no change from.
func chainGap(v *lexdania.View, act string) string {
	var eff time.Time
	for _, c := range v.Chain {
		if c.Accession == act {
			eff = c.Effective
		}
	}
	silent := map[string]bool{}
	for _, s := range v.Silent {
		silent[s.Accession] = true
	}
	missing, quiet := 0, 0
	for _, c := range v.Chain {
		if c.Accession == act || c.Effective.After(eff) {
			continue
		}
		switch {
		case !c.Found:
			missing++
		case silent[c.Accession]:
			quiet++
		}
	}
	switch {
	case missing > 0:
		return " [chain gap: an earlier act is not in the mirror]"
	case quiet > 0:
		return " [chain gap: an earlier act yielded no changes]"
	}
	return ""
}

// closest is the edit distance from a passage to its nearest window of the text, on letters and
// digits only, and that window.
func closest(text, passage string) (int, string) {
	t := []rune(squash(text))
	p := []rune(squash(passage))
	if len(p) < 6 || len(t) < len(p)/2 {
		return -1, ""
	}
	best, at, end := -1, 0, 0
	// Semi-global alignment: the passage must be matched whole, anywhere in the text.
	prev := make([]int, len(p)+1)
	cur := make([]int, len(p)+1)
	start := make([]int, len(p)+1)
	pstart := make([]int, len(p)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(t); i++ {
		cur[0] = 0
		start[0] = i
		for j := 1; j <= len(p); j++ {
			c := 1
			if t[i-1] == p[j-1] {
				c = 0
			}
			v, s := prev[j-1]+c, pstart[j-1]
			if prev[j]+1 < v {
				v, s = prev[j]+1, pstart[j]
			}
			if cur[j-1]+1 < v {
				v, s = cur[j-1]+1, start[j-1]
			}
			cur[j], start[j] = v, s
		}
		if best < 0 || cur[len(p)] < best {
			best, at, end = cur[len(p)], start[len(p)], i
		}
		prev, cur = cur, prev
		pstart, start = start, pstart
	}
	if at < 0 || at > end {
		at = end
	}
	if at > 0 {
		at--
	}
	return best, string(t[at:end])
}

func digitsOf(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
