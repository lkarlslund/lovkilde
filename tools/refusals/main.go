// Command refusals explains every refusal in one law's chain, in chain order, with the state
// of the text as evidence rather than the reason string alone.
//
// The reason a change was refused is not why it failed. "address not found" is often collateral
// damage from an edit that applied successfully earlier in the same chain — including one that
// deleted, gutted or renumbered the thing a later act cites. So this walks the chain date by
// date and asks, for each refusal: did the target ever exist in the promulgated text, does it
// exist in the state right before this act, are its words still in the text under a different
// number, and which applied edit touched that address.
//
// It also answers the renumbering question directly: fingerprints every provision by its words
// and reports any provision whose printed number differs from the number the same words had as
// promulgated.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: refusals <accession> [as-of YYYY-MM-DD]")
		os.Exit(2)
	}
	acc := os.Args[1]
	asOf := time.Now()
	if len(os.Args) > 2 {
		t, err := time.Parse(time.DateOnly, os.Args[2])
		if err != nil {
			fmt.Println("bad date:", err)
			os.Exit(2)
		}
		asOf = t
	}

	a, err := lexdania.Scan(context.Background(), "data/xml")
	if err != nil {
		fmt.Println("scan:", err)
		os.Exit(1)
	}
	law, _, ok := a.Open(acc)
	if !ok {
		fmt.Println("no such accession")
		os.Exit(1)
	}

	// Found is decided by the chain walker, not by Versions(), so the chain comes from one
	// real view and the dates below are derived from it.
	final := lexdania.LawAsOf(law, asOf, a.Open)
	var chain []lexdania.Version
	for _, v := range final.Chain {
		if v.Found && !v.Effective.After(asOf) {
			chain = append(chain, v)
		}
	}
	if len(chain) == 0 {
		fmt.Println("no found versions")
		os.Exit(1)
	}

	// One view per effective date, plus one before the first of them: the state an act is
	// applied to is what decides whether its target can be found.
	dates := make([]time.Time, 0, len(chain)+1)
	dates = append(dates, chain[0].Effective.AddDate(0, 0, -1))
	seen := map[string]bool{}
	for _, v := range chain {
		d := v.Effective.Format(time.DateOnly)
		if !seen[d] {
			seen[d] = true
			dates = append(dates, v.Effective)
		}
	}
	views := make([]*lexdania.View, len(dates))
	{
		var wg sync.WaitGroup
		sem := make(chan struct{}, 6)
		for i, d := range dates {
			wg.Add(1)
			go func(i int, d time.Time) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				views[i] = lexdania.LawAsOf(law, d, a.Open)
			}(i, d)
		}
		wg.Wait()
	}
	viewByDate := map[string]*lexdania.View{}
	for i, d := range dates {
		viewByDate[d.Format(time.DateOnly)] = views[i]
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })
	// stateBefore is the consolidation as of the latest date strictly earlier than the act's
	// own effective date: the text the act met. Using its own date instead answers a
	// different question and makes the tool report an act's own inserts as "earlier".
	stateBefore := func(t time.Time) *lexdania.View {
		var best time.Time
		for _, d := range dates {
			if d.Before(t) && (best.IsZero() || d.After(best)) {
				best = d
			}
		}
		if best.IsZero() {
			return views[0]
		}
		return viewByDate[best.Format(time.DateOnly)]
	}
	baseView := views[0]

	baseFP := fingerprints(baseView)
	fmt.Printf("%s as of %s: %d found versions, %d refusals total\n\n",
		acc, asOf.Format(time.DateOnly), len(chain), len(final.Unresolved))

	// Renumbering, answered by fingerprints rather than by reading the mutators.
	fmt.Println("RENUMBERING (same words, different printed number)")
	for i, v := range views {
		changed := 0
		var examples []string
		for _, s := range paragraphs(v) {
			fp := fingerprint(s)
			if fp == "" {
				continue
			}
			if was, ok := baseFP[fp]; ok && was != s.printed {
				changed++
				if len(examples) < 3 {
					examples = append(examples, fmt.Sprintf("%s→%s", was, s.printed))
				}
			}
		}
		fmt.Printf("  %-12s provisions %4d, renumbered %3d %s\n",
			dates[i].Format(time.DateOnly), len(paragraphs(v)), changed, strings.Join(examples, " "))
	}
	fmt.Println()

	fmt.Println("REFUSALS, chain order, with the state that made them fail")
	classes := map[string]int{}
	knockOn := map[string]int{}
	sameAct := map[string]int{}
	examples := map[string][]string{}
	var interesting []string
	for _, act := range chain {
		before := stateBefore(act.Effective)
		v := viewByDate[act.Effective.Format(time.DateOnly)]
		if v == nil {
			continue
		}
		beforeText := lawText(before)

		var mine []lexdoc.UnresolvedEdit
		for _, u := range v.Unresolved {
			if u.Edit.Source == act.Accession {
				mine = append(mine, u)
			}
		}
		if len(mine) == 0 {
			continue
		}
		fmt.Printf("%s %s  %d refused, %d applied so far\n", act.Effective.Format(time.DateOnly), act.Accession, len(mine), len(v.Applied))
		for _, u := range mine {
			addr := u.Edit.Address.String()
			if addr == "" {
				addr = u.Edit.Address.Raw
			}
			code := classify(u, before, baseView, beforeText, baseFP)
			classes[code]++
			if len(examples[code]) < 2 {
				examples[code] = append(examples[code], fmt.Sprintf("%s %s", cut(addr, 24), cut(oneLine(u.Edit.Prose), 60)))
			}
			culprit := touched(before, addr)
			switch {
			case culprit != "":
				knockOn[code]++
			case touchedBy(v, addr, act.Accession) != "":
				culprit = "same-act " + touchedBy(v, addr, act.Accession)
				sameAct[code]++
			}
			line := fmt.Sprintf("  %-18s %-22s %-12s %s", u.Edit.Kind, cut(addr, 22), code, culprit)
			fmt.Printf("%s | %s\n", line, cut(oneLine(u.Edit.Prose), 78))
			if code == "GONE" || code == "ORPHAN" {
				interesting = append(interesting, fmt.Sprintf("%s %s %s %s %s", act.Effective.Format(time.DateOnly), act.Accession, u.Edit.Kind, addr, culprit))
			}
		}
		fmt.Println()
	}

	fmt.Println("CLASSES")
	for _, k := range sortedKeys(classes) {
		fmt.Printf("  %-13s %3d   (target touched by an earlier act: %d, by this same act: %d)\n", k, classes[k], knockOn[k], sameAct[k])
		for _, ex := range examples[k] {
			fmt.Printf("        e.g. %s\n", ex)
		}
	}
	if len(interesting) > 0 {
		fmt.Println("\nCOLLATERAL (a target that existed and no longer does)")
		for _, l := range interesting {
			fmt.Println("  " + l)
		}
	}
}

func classify(u lexdoc.UnresolvedEdit, before, baseView *lexdania.View, beforeText string, baseFP map[string]string) string {
	e := u.Edit
	addr := e.Address.String()
	if addr == "" {
		addr = e.Address.Raw
	}
	if e.Kind == lexdoc.EditInsertProvision {
		return "INSERT"
	}
	if !e.Address.Recognised {
		return "NOADDR"
	}
	// Annex and table re-issues address a container that exists; what fails is splicing the
	// amending act's table into it, which is a different problem from a missing address.
	if e.Address.Bilag != 0 {
		return "ANNEX"
	}
	if e.Address.Heading {
		return "HEADING"
	}
	if _, ok := before.ProvisionText(addr); ok {
		return "WORDING"
	}

	// The address does not resolve now. Ask whether it ever did, and whether the thing it
	// points at is absent or merely not modelled as a unit.
	if was, ok := baseView.ProvisionText(addr); ok {
		fp := fingerprint(para{text: was})
		if fp != "" && strings.Contains(beforeText, fp) {
			return "ORPHAN"
		}
		return "GONE"
	}
	if e.Address.Paragraph == "" {
		return "NOADDR"
	}
	parentText, ok := before.ProvisionText("§ " + e.Address.Paragraph)
	if !ok {
		return "NO-PARAGRAAF"
	}
	// The paragraph is there. Numbered items and subsections print inline in Danish texts
	// ("1) …", "Stk. 2. …"), so an unresolved item address means one of two quite different
	// things: the words are printed and we never made a node for them, or they are not there.
	body := oneLine(parentText)
	switch {
	case e.Address.Litra != "" && strings.Contains(body, e.Address.Litra+")"):
		return "LITRA-INLINE"
	case e.Address.Nr != 0 && strings.Contains(body, fmt.Sprintf("%d)", e.Address.Nr)):
		return "NR-INLINE"
	case e.Address.Nr != 0:
		return "NR-ABSENT"
	case e.Address.Stk != 0 && strings.Contains(body, fmt.Sprintf("Stk. %d", e.Address.Stk)):
		return "STK-INLINE"
	case e.Address.Stk != 0:
		return "STK-ABSENT"
	default:
		return "PARA-ABSENT"
	}
}

// touchedBy is the same question inside the state this act produced, which answers whether the
// act inserted something and then asked for an edit inside it.
func touchedBy(v *lexdania.View, addr, accession string) string {
	for _, ap := range v.Applied {
		if ap.Edit.Source != accession {
			continue
		}
		a := ap.Edit.Address.String()
		if a == "" {
			a = ap.Edit.Address.Raw
		}
		if a == addr {
			return string(ap.Edit.Kind)
		}
	}
	return ""
}

// touched reports an applied edit in this state that acted on the same address, which is the
// usual reason a later act cannot find it.
func touched(v *lexdania.View, addr string) string {
	for _, ap := range v.Applied {
		a := ap.Edit.Address.String()
		if a == "" {
			a = ap.Edit.Address.Raw
		}
		if a == addr {
			return fmt.Sprintf("earlier %s by %s", ap.Edit.Kind, ap.Edit.Source)
		}
	}
	return ""
}

// para is one provision of the law's own series: the number it prints and its text.
type para struct{ printed, text string }

func paragraphs(v *lexdania.View) []para {
	var out []para
	for _, ref := range v.Content.Provisions() {
		out = append(out, para{strings.TrimSpace(ref.Provision.Printed), oneLine(ref.Provision.Text())})
	}
	return out
}

// lawText is the whole derived law as one line, for asking whether some words survive anywhere.
func lawText(v *lexdania.View) string {
	var b strings.Builder
	for _, p := range paragraphs(v) {
		b.WriteString(p.text)
		b.WriteByte(' ')
	}
	return b.String()
}

func fingerprints(v *lexdania.View) map[string]string {
	counts := map[string]int{}
	nums := map[string]string{}
	for _, s := range paragraphs(v) {
		fp := fingerprint(s)
		if fp == "" {
			continue
		}
		counts[fp]++
		nums[fp] = s.printed
	}
	out := map[string]string{}
	for fp, n := range counts {
		// Words repeated across provisions cannot identify one. BR18 prints many short
		// cross-referencing paragraphs with identical openings, and a truncated fingerprint
		// claimed 40 of them had been renumbered in a view where no act had run yet.
		if n == 1 {
			out[fp] = nums[fp]
		}
	}
	return out
}

// fingerprint is a provision's whole text minus its own number, so the same words printed
// under another number are recognised as the same provision.
func fingerprint(s para) string {
	t := s.text
	if n := strings.TrimSpace(s.printed); n != "" {
		if i := strings.Index(t, n); i >= 0 {
			t = strings.TrimSpace(t[i+len(n):])
		}
	}
	if len(t) < 20 {
		return ""
	}
	return t
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func sortedKeys(m map[string]int) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
