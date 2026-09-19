// Command pairs audits a chain against the ground truth each amendment carries with it.
//
// The publisher never publishes a merged text — measured again before this tool existed:
// BR18's base document holds 734 <Paragraf> elements while its amending acts hold 2 to 19
// and carry the changes as <AendringNyTekst> blocks. There is therefore no consolidated
// document to diff our work against. But every amendment states, in its own prose, what the
// text looks like on both sides of it: "I § 92, nr. 2, ændres »bygningen« til: »byggeriet«."
// asserts that the day before, § 92's item 2 contained "bygningen", and that the day after it
// contains "byggeriet". Those assertions are the register's own words, they cover most of a
// chain, and a mismatch is not a judgement call — it is one of us being wrong.
//
// Two audits run over one view per effective date:
//
//  1. Quoted pairs. For every change the engine applied, the quoted old wording must be
//     present in the state the act met, and the quoted new wording in the state after it.
//     "old-absent" means we were not holding text the act says was there (an earlier edit
//     lost or overwrote it). "new-absent" means we reported applied and did not write the
//     words, which is the phantom-edit shape a length report cannot see.
//
//  2. Numbering integrity. "§ 359, nr. 7, ophæves. Nr. 8-10 bliver herefter 7-9." carries two
//     instructions; executing one and dropping the other leaves the printed items running
//     1-6, 8-10. A hole in printed numbering is arithmetic, so each state's item numbers are
//     compared against the gapless sequence their own count implies, and holes already in the
//     promulgated text are reported separately as the publisher's rather than ours.
//
// Deliberate limit: stykker are numbered positionally in this model (<Stk> carries no
// Explicatus), so only item numbering is auditable here. Stykke holes are named in the
// refusal report instead.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: pairs <accession> [as-of YYYY-MM-DD]")
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

	dates := []time.Time{chain[0].Effective.AddDate(0, 0, -1)}
	seen := map[string]bool{}
	for _, v := range chain {
		d := v.Effective.Format(time.DateOnly)
		if !seen[d] {
			seen[d] = true
			dates = append(dates, v.Effective)
		}
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })

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
	// The state an act met is the latest date strictly earlier than its own: using the act's
	// own date would blame it for text it had not produced yet.
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

	fmt.Printf("%s as of %s: %d found versions, %d states\n",
		acc, asOf.Format(time.DateOnly), len(chain), len(dates))

	auditPairs(chain, stateBefore, viewByDate)
	auditNumbering(dates, views)
}

// auditPairs checks each applied change against the wording it quotes.
func auditPairs(chain []lexdania.Version, stateBefore func(time.Time) *lexdania.View,
	viewByDate map[string]*lexdania.View) {
	type fail struct{ who, addr, quote string }
	var oldAbsent, newAbsent, okPairs, noPair, refusedWithOld int
	var oldEx, newEx []fail

	for _, v := range chain {
		cur := viewByDate[v.Effective.Format(time.DateOnly)]
		if cur == nil {
			continue
		}
		prevText := norm(stateBefore(v.Effective).Text())
		curText := norm(cur.Text())
		for _, ap := range cur.Applied {
			e := ap.Edit
			if e.Source != v.Accession {
				continue
			}
			if e.Old == "" && e.New == "" {
				noPair++
				continue
			}
			good := true
			if e.Old != "" && !strings.Contains(prevText, norm(e.Old)) {
				oldAbsent++
				good = false
				if len(oldEx) < 6 {
					oldEx = append(oldEx, fail{v.Accession, e.Address.String(), e.Old})
				}
			}
			if e.New != "" && !strings.Contains(curText, norm(e.New)) {
				newAbsent++
				good = false
				if len(newEx) < 6 {
					newEx = append(newEx, fail{v.Accession, e.Address.String(), e.New})
				}
			}
			if good {
				okPairs++
			}
		}
		// A refusal whose quoted old wording was present is a gap in the engine, not a
		// correct refusal: the act described text we were holding and we did not act on it.
		for _, u := range cur.Unresolved {
			if u.Edit.Source != v.Accession || u.Edit.Old == "" {
				continue
			}
			if strings.Contains(prevText, norm(u.Edit.Old)) {
				refusedWithOld++
			}
		}
	}

	fmt.Printf("\nQUOTED PAIRS (the act's own assertions about the text)\n")
	fmt.Printf("  satisfied        %4d  old wording present before, new wording present after\n", okPairs)
	fmt.Printf("  no quote         %4d  the change carries no wording to check (structural edits)\n", noPair)
	fmt.Printf("  old absent       %4d  applied, but the state it met did not hold what the act quoted\n", oldAbsent)
	fmt.Printf("  new absent       %4d  applied, but the new wording is not in the result\n", newAbsent)
	fmt.Printf("  refused+old here %4d  refused although the quoted wording was in front of it\n", refusedWithOld)
	for _, f := range oldEx {
		fmt.Printf("    old-absent %s %s quoted %q\n", f.who, f.addr, clip(f.quote))
	}
	for _, f := range newEx {
		fmt.Printf("    new-absent %s %s wrote %q\n", f.who, f.addr, clip(f.quote))
	}
}

// auditNumbering reports printed item numbers that are not the gapless sequence their count
// implies, separating holes the promulgated text already had from ones a state introduced.
func auditNumbering(dates []time.Time, views []*lexdania.View) {
	base := holes(views[0])
	baseKeys := map[string]bool{}
	for _, h := range base {
		baseKeys[h.key] = true
	}
	fmt.Printf("\nNUMBERING INTEGRITY (printed item numbers must run 1..n)\n")
	fmt.Printf("  as promulgated: %d provisions already irregular in the published text\n", len(base))
	for i, d := range dates[1:] {
		var fresh []string
		for _, h := range holes(views[i]) {
			if !baseKeys[h.key] {
				fresh = append(fresh, h.key)
			}
		}
		fmt.Printf("  %s  %2d new hole(s)\n", d.Format(time.DateOnly), len(fresh))
		for _, k := range fresh {
			fmt.Printf("      %s\n", k)
		}
	}
}

type hole struct {
	key string
}

// holes lists provisions whose printed item numbers skip or repeat, per parent.
//
// The number is read out of the section's own Explicatus text, not Section.Nr: Nr is derived
// positionally on some parse paths, which made this check agree with itself while disagreeing
// with what the page prints. The printed digit is the only thing a reader can cite, so it is
// the only thing worth auditing.
func holes(v *lexdania.View) []hole {
	if v == nil {
		return nil
	}
	byParent := map[*lexdania.Section][]int{}
	order := []*lexdania.Section{}
	seen := map[*lexdania.Section]bool{}
	for _, s := range v.Sections() {
		if s.Kind != "nr" || s.Parent == nil {
			continue
		}
		n, ok := printedNumber(s.RawNumber)
		if !ok {
			continue
		}
		if !seen[s.Parent] {
			seen[s.Parent] = true
			order = append(order, s.Parent)
		}
		byParent[s.Parent] = append(byParent[s.Parent], n)
	}
	var out []hole
	for _, p := range order {
		nums := byParent[p]
		sort.Ints(nums)
		want := 1
		for _, n := range nums {
			if n == want {
				want++
				continue
			}
			out = append(out, hole{fmt.Sprintf("%s items print %v (expected 1..%d)", addressOf(p), nums, len(nums))})
			break
		}
	}
	return out
}

// printedNumber reads the leading digits of a printed number like "2)" or "12 )".
func printedNumber(raw string) (int, bool) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\u00a0", " "))
	i := 0
	for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(raw[:i])
	return n, err == nil
}

// addressOf walks up to the provision and renders its printed number.
func addressOf(s *lexdania.Section) string {
	for s != nil {
		if s.Kind == "paragraf" {
			return strings.TrimSpace(s.RawNumber)
		}
		s = s.Parent
	}
	return "(outside a provision)"
}

// norm folds the typography that differs between an amendment's quotation and the archive's
// own spacing: non-breaking spaces, runs of whitespace, and the soft hyphen.
func norm(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.ReplaceAll(s, "\u00ad", "")
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

func clip(s string) string {
	r := []rune(s)
	if len(r) > 70 {
		return string(r[:67]) + "..."
	}
	return s
}
