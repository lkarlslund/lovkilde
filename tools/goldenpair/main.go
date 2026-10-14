// Command goldenpair checks a consolidated law against an external truth: a later document
// the publisher actually promulgated.
//
// The parity tests cannot answer this question. They compare the tree engine against the model
// engine, and both are ours: when both were numbering stykker by position they agreed with each
// other while printing item numbers that cannot exist in 21 BR18 provisions. Agreement between
// two implementations is a migration guard, not ground truth.
//
// Ground truth does exist in the archive, just not as merged text. Measured over all 74,504
// executive-order documents: 16,398 pairs where a newer order repeals an older one whose text it
// then re-enacts, 71 of them re-enacting at least half of the older order's provisions verbatim.
// The ministry copies most of the old law, changes some of it, and promulgates the result, so:
//
//   - a successor provision matching the base as promulgated was untouched;
//   - one matching the base as amended was copied from the consolidated state, which is exactly
//     what our engine has to have produced;
//   - one matching ours except in its digits carries the numbering defect's own signature -- the
//     ministry cites renumbered provisions and we still cite the old ones;
//   - one matching neither is a real change, or a copy of an amended state we failed to build.
//
// Chain edges come from the register's own <Change> anchors, never from our parser reading its
// own output, and the reference is a document the register published in its own right.
//
// The pair filter is the part that took a wrong turn to learn: a text-only census suggested 787
// high-overlap pairs, and all six best of them were same-year corrections re-issuing an
// unamended order -- 0 applied / 0 refused on tools/engines, so a consolidation test on such a
// pair compares one document with itself. A base has to have had a change of its own first.
//
// A law has the same kind of truth in its own next consolidation: `-pairs base=successor` with two
// lovbekendtgørelser of one law consolidates the base as of the day the successor was signed and
// classifies the successor against it, through the chain production uses (earlier consolidations'
// acts included).
//
// Usage:
//
//	go run ./tools/goldenpair [-budget N] [-min-overlap 0.5] [-check N] [-workers N]
//	go run ./tools/goldenpair -pairs A20190098129=A20210024129,…
//
// -budget strides rather than truncates, since archive order starts in 1852; -check caps how
// many of the best pairs are reconstructed and classified end to end.
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

	"github.com/lkarlslund/lovkilde/internal/lexdania"
)

type pair struct {
	base, next string
	baseTitle  string
	earlier    int     // changes taking effect before the successor, so there was a state to consolidate
	overlap    float64 // share of the base's provisions whose text reappears in the successor
	baseProvs  int
}

func main() {
	var (
		budget   = flag.Int("budget", 0, "documents to scan for chain edges (0 = all)")
		minOv    = flag.Float64("min-overlap", 0.5, "minimum share of the base re-enacted verbatim")
		checkN   = flag.Int("check", 10, "pairs to reconstruct and classify end to end")
		workers  = flag.Int("workers", 12, "documents to parse in parallel")
		minProvs = flag.Int("min-provisions", 5, "smallest base worth pairing")
		pairsIn  = flag.String("pairs", "", "check these base=successor pairs and stop, skipping the census")
		root     = flag.String("root", "data/xml", "archive root")
	)
	flag.Parse()

	// A named pair is worth checking whatever its overlap: the census is for finding pairs, and
	// once one is known, the interesting provisions are exactly the ones that did not copy.
	var given []pair
	for _, spec := range strings.Split(*pairsIn, ",") {
		base, next, ok := strings.Cut(strings.TrimSpace(spec), "=")
		if !ok {
			continue
		}
		given = append(given, pair{base: base, next: next})
	}

	start := time.Now()
	a, err := lexdania.Scan(context.Background(), *root)
	if err != nil {
		fmt.Println("scan:", err)
		os.Exit(1)
	}
	if len(given) > 0 {
		checkPairs(a, given, 0, start)
		return
	}

	var laws []lexdania.Entry
	for _, e := range a.Entries() {
		switch e.Kind {
		case "lta", "ltb", "ltc", "mt":
			laws = append(laws, e)
		}
	}
	if *budget > 0 && len(laws) > *budget {
		stride := len(laws) / *budget
		sampled := make([]lexdania.Entry, 0, *budget)
		for i := 0; i < len(laws) && len(sampled) < *budget; i += stride {
			sampled = append(sampled, laws[i])
		}
		laws = sampled
	}
	fmt.Printf("archive: %d documents, %d in the executive-order media, %s to scan\n", a.Len(), len(laws), *root)

	// Phase 1: the register's own chain edges. Each candidate is a pair where the successor took
	// effect after at least one earlier change, so the base had a state worth reconstructing.
	type job struct{ e lexdania.Entry }
	type edge struct {
		base    lexdania.Entry
		version lexdania.Version
		earlier int
	}
	jobs := make(chan job)
	out := make(chan edge, 256)
	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				// a.Versions, not Document.Versions: the archive's variant resolves each edge
				// against the archive and fills Found.
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

	// No cache here, deliberately: holding provision text for every document a candidate touches
	// is gigabytes over this archive, and the first full run was killed for it after printing one
	// line.
	var pairs []pair
	ovHist := map[int]int{}
	scanned, parsed := 0, 0
	for ed := range out {
		scanned++
		if scanned%5000 == 0 {
			fmt.Printf("   ... %d candidate edges, %d at overlap >= %.2f so far\n", scanned, len(pairs), *minOv)
		}
		bp, ok := openProvisions(a, ed.base.Accession)
		if !ok || len(bp) < *minProvs {
			continue
		}
		np, ok := openProvisions(a, ed.version.Accession)
		if !ok || len(np) == 0 {
			continue
		}
		parsed++
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
			earlier: ed.earlier, overlap: ov, baseProvs: len(bp),
		})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].overlap != pairs[j].overlap {
			return pairs[i].overlap > pairs[j].overlap
		}
		return pairs[i].earlier > pairs[j].earlier
	})

	fmt.Printf("phase 1 in %s: %d candidate pairs, %d at overlap >= %.2f\n",
		time.Since(start).Round(time.Second), parsed, len(pairs), *minOv)
	fmt.Printf("   how much of the base the successor re-enacted, every candidate pair:\n")
	cum := 0
	for d := 90; d >= 0; d -= 10 {
		cum += ovHist[d]
		fmt.Printf("   %3d-%3d%%  %5d   (>= %3d%%: %d)\n", d, d+9, ovHist[d], d, cum)
	}
	if len(pairs) == 0 {
		fmt.Println("no usable pairs at this overlap; lower -min-overlap or raise -budget")
		return
	}
	fmt.Printf("\ntop pairs by re-enacted share:\n")
	for i, p := range pairs {
		if i >= 12 {
			break
		}
		fmt.Printf("   %5.1f%%  %4d prov  %2d earlier  %s -> %s  %s\n",
			p.overlap*100, p.baseProvs, p.earlier, p.base, p.next, cut(p.baseTitle, 52))
	}
	checkPairs(a, pairs, *checkN, start)
}

// checkPairs reconstructs each pair and prints the verdict the library reached. limit 0 means
// every pair given.
func checkPairs(a *lexdania.Archive, pairs []pair, limit int, start time.Time) {
	if len(pairs) == 0 {
		return
	}
	// Phase 2: reconstruct and classify, through the same code the regression test uses.
	fmt.Printf("\nphase 2: the base as it stood the day before the successor takes effect, then every\n" +
		"provision of the successor classified. promulgated and changed are outside our control;\n" +
		"consolidated is what we must have produced; digits-only is the numbering signature.\n\n")
	totals := map[string]int{}
	for i, p := range pairs {
		if limit > 0 && i >= limit {
			break
		}
		v, ok := lexdania.GoldenPair(a, p.base, p.next)
		if !ok {
			fmt.Printf("%s -> %s: not checkable (chain edge, document, or reconstruction)\n", p.base, p.next)
			continue
		}
		fmt.Printf("%s\n", v)
		totals["promulgated"] += v.CopiedPromulgated
		totals["consolidated"] += v.CopiedConsolidated
		totals["digits-only"] += v.DigitsOnly
		totals["changed"] += v.Changed
		for _, s := range v.Suspicious {
			fmt.Printf("     %s via %s\n     theirs [%s]\n       %s\n     ours   [%s]\n       %s\n",
				s.Kind, s.Target,
				strings.Join(s.TheirRuns, " "), cut(s.Successor, 140),
				strings.Join(s.OurRuns, " "), cut(s.Ours, 140))
		}
	}
	n := len(pairs)
	if limit > 0 {
		n = min(limit, n)
	}
	fmt.Printf("\ntotals over %d pairs: promulgated %d, consolidated %d, digits-only %d, changed %d\n",
		n, totals["promulgated"], totals["consolidated"], totals["digits-only"], totals["changed"])
	if t := totals["consolidated"] + totals["digits-only"] + totals["changed"]; t > 0 {
		fmt.Printf("successor provisions we could not account for as copies: %d of %d external assertions (%.1f%%)\n",
			t, t+totals["promulgated"], 100*float64(t)/float64(t+totals["promulgated"]))
	}
	fmt.Printf("elapsed %s\n", time.Since(start).Round(time.Second))
}

// openProvisions reads one document's provisions, or reports that it is not in the archive.
func openProvisions(a *lexdania.Archive, acc string) ([]string, bool) {
	doc, _, ok := a.Open(acc)
	if !ok {
		return nil, false
	}
	return lexdania.ProvisionTexts(&doc.Model().Content), true
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
