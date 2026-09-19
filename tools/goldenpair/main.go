// Command goldenpair checks a consolidated law against an external truth: a later document
// the publisher actually promulgated.
//
// The parity tests cannot answer this question. TestModelConsolidationParityOnCorpus compares
// the tree engine against the model engine, which are both ours: when both engines were
// numbering stykker by position, they agreed with each other while printing item numbers that
// cannot exist in 21 BR18 provisions. Agreement between two implementations is a migration
// guard, not ground truth.
//
// Ground truth does exist in the archive, just not as merged text. Measured over data/xml/lta
// before this tool was written: 11,567 document pairs where a newer executive order repeals an
// older one whose text it then re-enacts, 4,481 of them re-enacting at least half the older
// order's provisions verbatim and 787 at least 90%. The ministry copies most of the old law,
// changes some of it, and promulgates the result. So for a pair like that:
//
//   - a provision of the NEW order that matches the old one as promulgated was untouched;
//   - a provision that matches the old one as amended by earlier acts was copied from the
//     consolidated state -- which is exactly what our engine has to reconstruct, so a miss is
//     not a judgement call, it is a defect;
//   - a provision matching neither is a real change, unless the words match and only the
//     digits differ, which is the signature of the numbering defect this family produced.
//
// The chain edges come from the publisher's own <Change> anchors (Document.Versions), never
// from our own parser reading its own output, and the reference text is a document the
// register published in its own right.
//
// Usage:
//
//	go run ./tools/goldenpair [-budget N] [-min-overlap 0.5] [-check N] [-workers N]
//
// -budget caps how many documents are scanned for chain edges (0 = all), -check caps how many
// of the best pairs are reconstructed and classified end to end.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

type pair struct {
	base, next string
	baseTitle  string
	effective  time.Time // when the newer order takes effect for the older one
	earlier    int       // changes taking effect before that, so the base had a life to consolidate
	overlap    float64   // share of the base's provisions whose text reappears in the newer order
	baseProvs  int
}

func main() {
	var (
		budget   = flag.Int("budget", 0, "documents to scan for chain edges (0 = all)")
		minOv    = flag.Float64("min-overlap", 0.5, "minimum share of the base's provisions re-enacted verbatim")
		checkN   = flag.Int("check", 10, "pairs to reconstruct and classify end to end")
		workers  = flag.Int("workers", 12, "documents to parse in parallel")
		minProvs = flag.Int("min-provisions", 5, "smallest base worth pairing")
		root     = flag.String("root", "data/xml", "archive root")
		verbose  = flag.Bool("v", false, "list every pair and every provision verdict")
		showMiss = flag.Int("show", 6, "examples to print per bucket")
	)
	flag.Parse()

	start := time.Now()
	a, err := lexdania.Scan(context.Background(), *root)
	if err != nil {
		fmt.Println("scan:", err)
		os.Exit(1)
	}
	entries := a.Entries()
	var laws []lexdania.Entry
	for _, e := range entries {
		switch e.Kind {
		case "lta", "ltb", "ltc", "mt":
			laws = append(laws, e)
		}
	}
	if *budget > 0 && len(laws) > *budget {
		// Stride, not truncation: entries arrive in archive order, so taking the first N
		// would measure the 1880s and call it the corpus.
		stride := len(laws) / *budget
		sampled := make([]lexdania.Entry, 0, *budget)
		for i := 0; i < len(laws) && len(sampled) < *budget; i += stride {
			sampled = append(sampled, laws[i])
		}
		laws = sampled
	}
	fmt.Printf("archive: %d documents, %d in the executive-order media (%s), %s to scan\n",
		a.Len(), len(laws), *root, strings.Join([]string{"lta", "ltb", "ltc", "mt"}, "/"))

	// Phase 1: chain edges as the register states them. A base is worth pairing only if it
	// had at least one change of its own before the newer order arrived, otherwise the pair
	// says nothing about consolidation. Measured: every one of the six highest-overlap pairs
	// from the first census was this class -- same-year corrections re-issuing an unamended
	// order, 0 applied / 0 refused -- which is why this filter is here.
	type job struct{ e lexdania.Entry }
	type edge struct {
		base    lexdania.Entry
		version lexdania.Version
		earlier int
	}
	jobs := make(chan job)
	out := make(chan edge, 256)
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				// a.Versions rather than Document.Versions: it is the archive's variant,
				// and the one that resolves each edge against the archive and fills Found.
				vs, err := a.Versions(j.e.Accession)
				if err != nil || len(vs) < 2 {
					continue
				}
				for i, v := range vs {
					if !v.Found || v.Effective.IsZero() {
						continue
					}
					earlier := 0
					for k := range vs[:i] {
						if !vs[k].Effective.IsZero() && !vs[k].Effective.After(v.Effective) {
							earlier++
						}
					}
					if earlier == 0 {
						continue
					}
					out <- edge{base: j.e, version: v, earlier: earlier}
				}
			}
		}()
	}
	go func() {
		for _, e := range laws {
			jobs <- job{e}
		}
		close(jobs)
	}()
	go func() {
		wg.Wait()
		close(out)
	}()

	// No cache here, deliberately: holding provision text for every document touched by a
	// supersession candidate is gigabytes over this archive, and the first full run of this
	// tool was killed for it after printing nothing but its header.
	var pairs []pair
	ovHist := map[int]int{}
	scanned, edged := 0, 0
	for ed := range out {
		scanned++
		if scanned%5000 == 0 {
			fmt.Printf("   ... %d candidate edges looked at, %d so far at overlap >= %.2f\n", scanned, len(pairs), *minOv)
		}
		bp, ok := openProvisions(a, ed.base.Accession)
		if !ok || len(bp) < *minProvs {
			continue
		}
		np, ok := openProvisions(a, ed.version.Accession)
		if !ok || len(np) == 0 {
			continue
		}
		edged++
		needle := strings.Join(np, " ")
		hit := 0
		for _, p := range bp {
			if strings.Contains(needle, p) {
				hit++
			}
		}
		ov := float64(hit) / float64(len(bp))
		ovHist[int(ov*10)*10]++
		if ov < *minOv {
			continue
		}
		pairs = append(pairs, pair{
			base: ed.base.Accession, next: ed.version.Accession, baseTitle: ed.base.Title,
			effective: ed.version.Effective, earlier: ed.earlier, overlap: ov, baseProvs: len(bp),
		})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].overlap != pairs[j].overlap {
			return pairs[i].overlap > pairs[j].overlap
		}
		return pairs[i].earlier > pairs[j].earlier
	})

	fmt.Printf("phase 1 in %s: %d candidate pairs (a change arriving after at least one earlier change), %d at overlap >= %.2f\n",
		time.Since(start).Round(time.Second), edged, len(pairs), *minOv)
	fmt.Printf("   how much of the base the successor re-enacted, every candidate pair:\n")
	cum := 0
	for d := 90; d >= 0; d -= 10 {
		cum += ovHist[d]
		fmt.Printf("   %3d-%3d%%  %5d   (>= %3d%%: %d)\n", d, d+9, ovHist[d], d, cum)
	}
	buckets := map[int]int{}
	for _, p := range pairs {
		buckets[int(p.earlier)]++
	}
	var keys []int
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	shown := 0
	for _, k := range keys {
		if shown >= 6 {
			fmt.Printf("   ... %d more with even longer chains\n", sum(buckets, keys[6:]))
			break
		}
		fmt.Printf("   %2d earlier change(s): %d pairs\n", k, buckets[k])
		shown++
	}
	if len(pairs) == 0 {
		fmt.Println("no usable pairs at this overlap; lower -min-overlap or raise -budget")
		return
	}
	fmt.Printf("\ntop pairs by re-enacted share (base provisions, earlier changes):\n")
	for i, p := range pairs {
		if i >= 12 {
			break
		}
		fmt.Printf("   %5.1f%%  %4d prov  %2d earlier  %s -> %s  %s\n",
			p.overlap*100, p.baseProvs, p.earlier, p.base, p.next, cut(p.baseTitle, 52))
	}
	if *checkN == 0 {
		return
	}

	// Phase 2: reconstruct and classify against the publisher's own later document.
	fmt.Printf("\nphase 2: reconstructing the base the day before the newer order takes effect, then\n" +
		"matching every provision of the newer order against it. Buckets:\n" +
		"  promulgated  copied from the old text unchanged (proves nothing about us)\n" +
		"  consolidated copied from the amended state, which is what we must have produced\n" +
		"  digits-only  same words, different numbers: the numbering defect's own signature\n" +
		"  changed      a real change in the newer order\n\n")
	totals := map[string]int{}
	for i, p := range pairs {
		if i >= *checkN {
			break
		}
		reportPair(a, p, totals, *showMiss, *verbose)
	}
	fmt.Printf("\ntotals over %d pairs: promulgated %d, consolidated %d, digits-only %d, changed %d\n",
		min(*checkN, len(pairs)), totals["promulgated"], totals["consolidated"], totals["digits-only"], totals["changed"])
	if t := totals["consolidated"] + totals["digits-only"] + totals["changed"]; t > 0 {
		fmt.Printf("provisions the newer order copied that we did NOT reproduce: %d of %d external assertions (%.1f%%)\n",
			totals["consolidated"]+totals["digits-only"], totals["consolidated"]+totals["digits-only"]+totals["changed"],
			100*float64(totals["consolidated"]+totals["digits-only"])/float64(t))
	}
	fmt.Printf("elapsed %s\n", time.Since(start).Round(time.Second))
}

// reportPair consolidates one base to the day before its successor takes effect and classifies
// every provision of that successor against it.
func reportPair(a *lexdania.Archive, p pair, totals map[string]int, show int, verbose bool) {
	base, _, ok := a.Open(p.base)
	if !ok {
		fmt.Printf("%s: base does not open\n", p.base)
		return
	}
	dayBefore := p.effective.Add(-24 * time.Hour)
	view := lexdania.LawAsOfModel(base, dayBefore, a.Open)
	if view == nil || view.Content == nil {
		fmt.Printf("%s -> %s: no consolidated view at %s\n", p.base, p.next, dayBefore.Format(time.DateOnly))
		return
	}
	promulgated := map[string]bool{}
	for _, t := range provisionTexts(&base.Model().Content) {
		promulgated[t] = true
	}
	consolidated := map[string]string{}
	nodigits := map[string]string{}
	for _, t := range provisionTexts(view.Content) {
		consolidated[t] = t
		nodigits[stripDigits(t)] = t
	}
	applied := 0
	for _, v := range view.Chain {
		if v.Found && !v.Effective.After(dayBefore) {
			applied++
		}
	}
	next, _, ok := a.Open(p.next)
	if !ok {
		fmt.Printf("%s: successor %s does not open\n", p.base, p.next)
		return
	}
	counts := map[string]int{}
	misses := map[string][]string{}
	theirs := provisionTexts(&next.Model().Content)
	for _, t := range theirs {
		bucket := "changed"
		example := ""
		switch {
		case promulgated[t]:
			bucket = "promulgated"
		case consolidated[t] != "":
			bucket = "consolidated"
		case nodigits[stripDigits(t)] != "":
			bucket = "digits-only"
			// Say which numbers disagree, because that is what tells a defect apart
			// from a legitimate change: "§ 15" against our "§ 16" is numbering, while
			// "1. januar 1997" against "1. januar 1998" is the ministry moving a date.
			ours := nodigits[stripDigits(t)]
			example = fmt.Sprintf("their numbers [%s] vs ours [%s]\n        %s\n        ours: %s",
				strings.Join(digitRuns(t), " "), strings.Join(digitRuns(ours), " "),
				cut(t, 130), cut(ours, 130))
		}
		counts[bucket]++
		totals[bucket]++
		if example != "" && len(misses[bucket]) < show {
			misses[bucket] = append(misses[bucket], example)
		}
		if verbose {
			fmt.Printf("    %-12s %s\n", bucket, cut(t, 90))
		}
	}
	fmt.Printf("%s -> %s  as of %s, %d changes applied, base had %d provisions, successor %d:\n"+
		"     promulgated %d, consolidated %d, digits-only %d, changed %d\n",
		p.base, p.next, dayBefore.Format(time.DateOnly), applied, p.baseProvs,
		len(theirs),
		counts["promulgated"], counts["consolidated"], counts["digits-only"], counts["changed"])
	for _, b := range []string{"consolidated", "digits-only"} {
		for _, m := range misses[b] {
			fmt.Printf("     %s: %s\n", b, m)
		}
	}
}

// digitRuns lists the numbers in a provision, in order, so two texts that agree everywhere
// except in their numbering can be read side by side.
func digitRuns(s string) []string {
	var out []string
	start := -1
	for i := 0; i <= len(s); i++ {
		digit := i < len(s) && s[i] >= '0' && s[i] <= '9'
		if digit && start < 0 {
			start = i
		} else if !digit && start >= 0 {
			out = append(out, s[start:i])
			start = -1
		}
	}
	return out
}

// provisionTexts returns every provision's full text, normalised for comparison: the printed
// number stays in, because a copied provision keeps its number, and the typography that differs
// between two publisher documents does not.
func provisionTexts(c *lexdoc.Content) []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, ref := range c.Provisions() {
		if ref.Provision == nil {
			continue
		}
		t := normText(ref.Provision.Text())
		if len(t) < 40 {
			continue
		}
		out = append(out, t)
	}
	return out
}

func normText(s string) string {
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

// stripDigits removes digits and the punctuation that carries them, so "Stk. 3." and "Stk. 2."
// compare equal. That is what makes the numbering defect visible as its own bucket: the words
// the ministry copied are all there, only the numbers are impossible.
func stripDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// openProvisions reads one document's provisions, or reports that it is not there.
func openProvisions(a *lexdania.Archive, acc string) ([]string, bool) {
	doc, _, ok := a.Open(acc)
	if !ok {
		return nil, false
	}
	return provisionTexts(&doc.Model().Content), true
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func sum(m map[int]int, keys []int) int {
	out := 0
	for _, k := range keys {
		out += m[k]
	}
	return out
}
