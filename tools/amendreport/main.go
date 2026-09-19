// Throwaway diagnostic: prints the consolidation receipts for one law, grouped by
// refusal reason. Run with `go run ./tools/amendreport [accession] [as-of]`.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

func cut(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func main() {
	acc := "B20190139905"
	if len(os.Args) > 1 {
		acc = os.Args[1]
	}
	asOfStr := time.Now().Format("2006-01-02")
	if len(os.Args) > 2 {
		asOfStr = os.Args[2]
	}
	asOf, ok := lexdania.ParseDate(asOfStr)
	if !ok {
		fmt.Println("bad date", asOfStr)
		return
	}

	a, err := lexdania.Scan(context.Background(), "data/xml")
	if err != nil {
		fmt.Println("scan:", err)
		return
	}
	v, err := a.LawAsOf(acc, asOf)
	if err != nil {
		fmt.Println("lawasof:", err)
		return
	}

	fmt.Printf("%s as of %s: chain %d versions, applied edits %d, applied versions %d, unresolved %d, foreign %d, bills %d, trustworthy=%v\n\n",
		acc, asOfStr, len(v.Chain), len(v.Applied), v.AppliedCount(), len(v.Unresolved), v.SkippedForeign, v.SkippedBills, v.Trustworthy())

	fmt.Println("CHAIN")
	for _, c := range v.Chain {
		flag := "found"
		if !c.Found {
			flag = "MISSING"
		}
		fmt.Printf("  %-14s eff %-10s %-7s %s\n", c.Accession, c.Effective.Format("2006-01-02"), flag, cut(c.Title, 62))
	}

	fmt.Println("\nAPPLIED (where each landed)")
	for _, ap := range v.Applied {
		fmt.Printf("  %-14s %-6s %-22s %-28s %s\n", ap.Edit.Source, ap.Edit.Kind, ap.Edit.Address.Raw, ap.Section, cut(ap.Note, 40))
	}

	by := map[string][]lexdania.UnresolvedEdit{}
	for _, u := range v.Unresolved {
		key := u.Reason
		if u.Reason == "" {
			key = "(empty reason)"
		}
		by[key] = append(by[key], u)
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(by[keys[i]]) > len(by[keys[j]]) })

	fmt.Println("\nREFUSED, grouped by reason")
	for _, k := range keys {
		fmt.Printf("\n  [%d] %s\n", len(by[k]), cut(k, 100))
		for i, u := range by[k] {
			if i >= 3 {
				fmt.Printf("      … %d more of this reason\n", len(by[k])-i)
				break
			}
			fmt.Printf("      %-14s ch %-4s %-8s %-24s old=%q new=%q\n",
				u.Edit.Source, u.Edit.Number, u.Edit.Kind, u.Edit.Address.Raw, cut(u.Edit.Old, 40), cut(u.Edit.New, 40))
		}
	}

	// Does the refused target resolve in the base text at all? That is the difference between
	// "the engine could not place it" and "this edit belongs to another version of the law".
	// The question is asked the way a reader asks it: resolve the address through the model.
	base, _, okBase := a.Open(acc)
	if !okBase {
		fmt.Println("\nbase document could not be reopened")
		return
	}
	m := base.Model()
	present, missing := 0, 0
	var absentAddrs []string
	for _, u := range v.Unresolved {
		raw := strings.TrimSpace(u.Edit.Address.Raw)
		if raw == "" {
			continue
		}
		if _, ok := m.Content.PlainTextProvisions(u.Edit.Address); ok {
			present++
			continue
		}
		missing++
		if len(absentAddrs) < 12 {
			absentAddrs = append(absentAddrs, raw)
		}
	}
	fmt.Printf("\nrefused edits whose address resolves in the base text: %d\n", present)
	fmt.Printf("refused edits whose address does NOT resolve there:      %d\n", missing)
	fmt.Println("examples that do not resolve:", strings.Join(absentAddrs, ", "))

	// How much law did the chain lose? A consolidation must never print less text than
	// the document it started from, yet a replacement that lands with only its first
	// paragraph does exactly that, silently. Compare every provision against the text as
	// promulgated: shorter means law went missing on the way here.
	if m != nil {
		before := map[string]int{}
		for _, ref := range m.Content.Provisions() {
			before[ref.Provision.Address()] += len([]rune(ref.Provision.Text()))
		}
		after := map[string]int{}
		for _, ref := range v.Doc.Model().Content.Provisions() {
			after[ref.Provision.Address()] += len([]rune(ref.Provision.Text()))
		}
		var losses []loss
		for pr, n := range before {
			if got, ok := after[pr]; ok && got < n {
				losses = append(losses, loss{pr, n, got})
			}
		}
		sort.Slice(losses, func(i, j int) bool {
			return losses[i].was-losses[i].now > losses[j].was-losses[j].now
		})
		lost := 0
		for _, l := range losses {
			lost += l.was - l.now
		}
		fmt.Printf("\nPROVISIONS SHORTER THAN THE TEXT AS PROMULGATED: %d, %d runes of law lost\n", len(losses), lost)
		// Which edit took the text? The last applied edit naming the provision.
		type touch struct{ kind, src, where, note string }
		byPr := map[string]touch{}
		for _, a := range v.Applied {
			k := strings.ToLower(a.Section)
			k = strings.NewReplacer("§", "", " ", "", ".", "").Replace(k)
			byPr[k] = touch{string(a.Edit.Kind), a.Edit.Source, a.Section, a.Note}
		}
		replaced := 0
		for _, l := range losses {
			if t, ok := byPr[strings.ToLower(l.pr)]; ok && t.kind == "replace-provision" {
				replaced++
			}
		}
		fmt.Printf("      of these, %d were the target of an applied whole-provision replacement\n", replaced)
		for _, l := range losses {
			t := byPr[strings.ToLower(l.pr)]
			fmt.Printf("      %-8s %6d -> %6d  %-18s %-14s %-18s %s\n", l.pr, l.was, l.now, t.kind, t.src, t.where, t.note)
		}
	}
}

type loss struct {
	pr       string
	was, now int
}
