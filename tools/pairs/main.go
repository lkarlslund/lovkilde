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
// Both numbers are auditable, and the claim in this file's history that they were not was
// wrong: <Stk> does carry its number, in a child <Explicatus> element ("Stk. 2."). BR18's
// base document has 897 <Stk> elements, 579 unlabelled — exactly one per provision, the
// Danish convention of not numbering a first stykke — and 318 labelled, so every non-first
// stykke states its own number. The tree model reads that element into Section.RawNumber and
// then numbers stykker by position anyway; the model path has no field for it at all.
//
// A third audit runs over the corpus instead of one chain: `pairs -silent N` counts the chain
// documents that were opened, read, and yielded no change at all. Neither audit above can see
// those, because both start from the edits the engine found. Half the amending acts in sampled
// chains are of this kind, and until now their receipt read exactly like a clean run.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/lkarlslund/lovkilde/internal/lexdania"
	"github.com/lkarlslund/lovkilde/internal/lexdoc"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: pairs <accession> [as-of YYYY-MM-DD]")
		fmt.Println("       pairs -silent [laws]   count chain documents read with no result")
		os.Exit(2)
	}
	if os.Args[1] == "-silent" {
		n := 400
		if len(os.Args) > 2 {
			v, err := strconv.Atoi(os.Args[2])
			if err != nil || v <= 0 {
				fmt.Println("bad count:", os.Args[2])
				os.Exit(2)
			}
			n = v
		}
		corpusSilent(n)
		return
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

	final := lexdania.LawAdoptedAsOf(law, asOf, a.Open)
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
				views[i] = lexdania.LawAdoptedAsOf(law, d, a.Open)
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

// corpusSilent answers the third audit, which no single chain can show: how often a chain document
// is opened, read, and yields not one change.
//
// A refusal is a decision, and the receipt says so. Silence is the absence of material, and the
// old receipt described it in the same words as a clean run — applied 0, refused 0, trustworthy.
// This counts the difference across sampled chains, split by the decade of the law being amended,
// because the shape is not evenly spread: documents whose XML carries metadata and chain anchors
// but no body — the body living in the HTML payload beside them — are the norm before 2010.
//
// "no body read" is the diagnostic: it means the document parsed to nothing at all, so the change
// text exists only in a payload the amendment reader never looks at.
func corpusSilent(budget int) {
	a, err := lexdania.Scan(context.Background(), "data/xml")
	if err != nil {
		fmt.Println("scan:", err)
		os.Exit(1)
	}
	var laws []lexdania.Entry
	for _, e := range a.Entries() {
		switch e.Kind {
		case "lta", "ltb", "ltc", "mt":
			laws = append(laws, e)
		}
	}
	stride := max(1, len(laws)/budget)
	// Three ways an act can yield no change, and they want different remedies: the XML holds
	// metadata only and the text is in the HTML payload beside it; the XML has a body but no
	// amendatory markup at all; or the markup is there and the reader still found nothing, which
	// is a parser bug rather than a coverage gap.
	type tally struct {
		acts, silent, metadataOnly, noMarkup, unparsedMarkup int
		examples                                             [3]string
	}
	per := map[int]*tally{}
	sampled, withChain := 0, 0
	for i := 0; i < len(laws) && sampled < budget; i += stride {
		sampled++
		vs, err := a.Versions(laws[i].Accession)
		if err != nil || len(vs) < 2 {
			continue
		}
		withChain++
		dec := laws[i].Year / 10 * 10
		t := per[dec]
		if t == nil {
			t = &tally{}
			per[dec] = t
		}
		for _, v := range vs {
			if !v.Found {
				continue
			}
			doc, _, ok := a.Open(v.Accession)
			if !ok || doc.IsBill() {
				continue
			}
			t.acts++
			// The changes consolidation reads, from whichever format the act arrived in: its XML
			// change markup, or the change list in its HTML payload's prose.
			if len(doc.Changes()) != 0 {
				continue
			}
			t.silent++
			kind := 2
			switch {
			case !bytes.Contains(readRaw(v.Path), []byte("<DokumentIndhold")):
				kind = 0
			case !bytes.Contains(readRaw(v.Path), []byte("AendringsNummer")):
				kind = 1
			}
			switch kind {
			case 0:
				t.metadataOnly++
			case 1:
				t.noMarkup++
			default:
				t.unparsedMarkup++
			}
			if t.examples[kind] == "" {
				t.examples[kind] = fmt.Sprintf("%s amends %s", v.Accession, laws[i].Accession)
			}
		}
	}
	var acts, silent int
	byKind := [3]int{}
	fmt.Printf("\namending documents read from the chains of %d sampled laws (%d have a chain):\n", sampled, withChain)
	kinds := []string{"metadata-only XML", "body without amendatory markup", "markup present, nothing parsed"}
	decades := make([]int, 0, len(per))
	for dec := range per {
		decades = append(decades, dec)
	}
	sort.Ints(decades)
	for _, dec := range decades {
		t := per[dec]
		fmt.Printf("  %d: %5d acts, %5d yielded no change (%.0f%%): metadata-only %d, no markup %d, unparsed %d\n",
			dec, t.acts, t.silent, 100*float64(t.silent)/float64(t.acts),
			t.metadataOnly, t.noMarkup, t.unparsedMarkup)
		acts += t.acts
		silent += t.silent
		byKind[0] += t.metadataOnly
		byKind[1] += t.noMarkup
		byKind[2] += t.unparsedMarkup
		for k, ex := range t.examples {
			if ex != "" {
				fmt.Printf("      first %s: %s\n", kinds[k], ex)
			}
		}
	}
	if acts == 0 {
		fmt.Println("  no chains found in this sample")
		return
	}
	fmt.Printf("  total: %d acts, %d yielded no change (%.1f%%)\n", acts, silent, 100*float64(silent)/float64(acts))
	for k, name := range kinds {
		fmt.Printf("   %-34s %d\n", name, byKind[k])
	}
}

// readRaw returns the file, or nil when it cannot be read. The audit is a diagnostic, and a file
// that will not read is not worth failing a run over.
func readRaw(path string) []byte {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return b
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
		prevText := norm(stateBefore(v.Effective).Content.PlainText())
		curText := norm(cur.Content.PlainText())
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
				if os.Getenv("PAIRS_VERBOSE") != "" {
					fmt.Printf("    refused-old %s %s: %s | %s\n", v.Accession, u.Edit.Address.String(), u.Reason, clip(oneLineOf(u.Edit.Prose)))
				}
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
	report := func(name string, f func(*lexdania.View) []hole) {
		base := f(views[0])
		baseKeys := map[string]bool{}
		for _, h := range base {
			baseKeys[h.key] = true
		}
		worst, worstDate := 0, ""
		for i, d := range dates[1:] {
			n := 0
			for _, h := range f(views[i]) {
				if !baseKeys[h.key] {
					n++
				}
			}
			if n > worst {
				worst, worstDate = n, d.Format(time.DateOnly)
			}
		}
		fmt.Printf("  %s: %d irregular as promulgated, worst state %d new (%s)\n", name, len(base), worst, worstDate)
		for i, d := range dates[1:] {
			var fresh []string
			for _, h := range f(views[i]) {
				if !baseKeys[h.key] {
					fresh = append(fresh, h.key)
				}
			}
			if i == len(dates)-1 || len(fresh) != 0 {
				fmt.Printf("    %s  %2d\n", d.Format(time.DateOnly), len(fresh))
				for _, k := range fresh {
					fmt.Printf("        %s\n", k)
				}
			}
		}
	}
	fmt.Printf("\nNUMBERING INTEGRITY (printed numbers must run 1..n)\n")
	report("items  ", holes)
	report("stykker", stykkehols)
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
	var out []hole
	for _, ref := range v.Content.Provisions() {
		p := ref.Provision
		for _, st := range p.Stykker {
			var nums []int
			for _, it := range st.Items {
				if n, ok := printedNumber(it.Label); ok && it.Litra == "" {
					nums = append(nums, n)
				}
			}
			sort.Ints(nums)
			for k, n := range nums {
				if n != k+1 {
					out = append(out, hole{fmt.Sprintf("%s items print %v (expected 1..%d)", addressOf(p), nums, len(nums))})
					break
				}
			}
		}
	}
	return out
}

// printedNumber reads the number out of a printed label: "2)", "§ 267.", "Stk. 2.".
//
// The first digit run anywhere is the number in all three shapes. Danish practice leaves the
// first stykke of a provision unlabelled and labels every later one, and the label is a
// dedicated <Explicatus> child element, not prose: BR18's base document has 897 <Stk>
// elements, 579 unlabelled (one per provision) and 318 labelled — every non-first stykke
// states its own number.
func printedNumber(raw string) (int, bool) {
	raw = strings.ReplaceAll(raw, "\u00a0", " ")
	start := -1
	for i := 0; i < len(raw); i++ {
		if raw[i] >= '0' && raw[i] <= '9' {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, false
	}
	end := start
	for end < len(raw) && raw[end] >= '0' && raw[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(raw[start:end])
	return n, err == nil
}

// stykkehols reports provisions whose stykke labels are not the sequence 1..n. A provision
// may leave its first stykke unlabelled, which is the Danish convention and counts as 1.
func stykkehols(v *lexdania.View) []hole {
	if v == nil {
		return nil
	}
	var out []hole
	for _, ref := range v.Content.Provisions() {
		p := ref.Provision
		if len(p.Stykker) < 2 {
			continue
		}
		var print []int
		for i, st := range p.Stykker {
			label := st.Label
			if label == "" && st.Span.From < len(p.Segments) && strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.Segments[st.Span.From].Text)), "stk") {
				label = p.Segments[st.Span.From].Text // the HTML reader leaves the label in the text
			}
			n, ok := printedNumber(label)
			if !ok {
				n = i + 1 // unlabelled: the convention is that it is the next in line
			}
			print = append(print, n)
		}
		for i, n := range print {
			if n != i+1 {
				out = append(out, hole{fmt.Sprintf("%s stykker print %v (expected 1..%d)", addressOf(p), print, len(print))})
				break
			}
		}
	}
	return out
}

// addressOf renders a provision's printed number. An empty number is reported as such rather
// than as an empty string: a row that names nothing looks like a formatting bug in this tool, when
// it is really a provision the archive prints no number on.
func addressOf(p *lexdoc.Provision) string {
	if n := strings.TrimSpace(p.Printed); n != "" {
		return n
	}
	return "(provision with no printed number)"
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

func oneLineOf(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string) string {
	r := []rune(s)
	if len(r) > 70 {
		return string(r[:67]) + "..."
	}
	return s
}
