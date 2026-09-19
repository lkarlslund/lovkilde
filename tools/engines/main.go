// Command engines measures the two consolidation engines against each other on real chains.
//
// Two engines exist because the model path (M012) is meant to retire the tree path, and a
// retirement needs numbers: which engine places more of a chain, and which one leaves less law
// behind than the document it started from. The second question is the one no test asks, because
// an applied edit reports success whether or not its words survived.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: engines <accession> [as-of YYYY-MM-DD]")
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
	base := law.Model()
	before := provisionLens(&base.Content)

	tree := lexdania.LawAsOf(law, asOf, a.Open)
	model := lexdania.LawAsOfModel(law, asOf, a.Open)

	fmt.Printf("%s as of %s\n", acc, asOf.Format(time.DateOnly))
	fmt.Printf("  tree  engine: applied %d, refused %d\n", len(tree.Applied), len(tree.Unresolved))
	fmt.Printf("  model engine: applied %d, refused %d\n", len(model.Consolidation.Applied), len(model.Consolidation.Unresolved))
	treeDoc := tree.Doc.Model()
	report("tree ", before, provisionLens(&treeDoc.Content))
	report("model", before, provisionLens(model.Content))
	// The total count matters as much as the shortened ones: a consolidation that quietly
	// loses whole provisions does not show up in the shorter-text count at all.
	fmt.Printf("  provisions: as promulgated %d, tree %d, model %d\n",
		len(before), len(provisionLens(&treeDoc.Content)), len(provisionLens(model.Content)))

	// A named provision is how the question really arrives — "what does § 21 say today" — and
	// answering it from the compiled dataset would answer with the old build. The address is
	// normalised on both sides because the model writes "§ 5 stk. 2" and the shell argument
	// arrives with a non-breaking space, several spaces, or no § at all.
	if len(os.Args) > 3 {
		want := normaliseAddress(os.Args[3])
		for _, ref := range treeDoc.Content.Provisions() {
			if normaliseAddress(ref.Provision.Address()) == want {
				fmt.Printf("\nTREE says at %s:\n%s\n", os.Args[3], strings.TrimSpace(ref.Provision.Text()))
			}
		}
	}
}

// normaliseAddress puts an address into the form the model prints: "§" with an ordinary space,
// one space before a letter suffix, no trailing period.
func normaliseAddress(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimSuffix(strings.TrimSpace(s), ".")
	s = strings.TrimPrefix(s, "§")
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "abcdefghijklmnopqrstuvwxyzæøå"); i > 0 && i < len(s)-1 {
		s = s[:i] + " " + strings.TrimSpace(s[i:])
	}
	return "§ " + s
}

// report counts provisions whose text got shorter than the text as promulgated. Some of that is
// real law being repealed, so the number is a lead rather than a defect count — but a provision
// that lost its subsections to an applied replacement looks identical from here, and that is the
// failure this measures.
func report(name string, before, after map[string]int) {
	var losses []loss
	lost := 0
	for pr, n := range before {
		if got, ok := after[pr]; ok && got < n {
			losses = append(losses, loss{pr, n, got})
			lost += n - got
		}
	}
	sort.Slice(losses, func(i, j int) bool {
		return losses[i].was-losses[i].now > losses[j].was-losses[j].now
	})
	fmt.Printf("  %s provisions shorter than as promulgated: %d, %d runes lost\n", name, len(losses), lost)
	for _, l := range losses {
		fmt.Printf("      § %-8s %6d -> %6d\n", l.pr, l.was, l.now)
	}
}

func provisionLens(c *lexdoc.Content) map[string]int {
	out := map[string]int{}
	if c == nil {
		return out
	}
	for _, ref := range c.Provisions() {
		out[ref.Provision.Address()] += len([]rune(ref.Provision.Text()))
	}
	return out
}

type loss struct {
	pr       string
	was, now int
}
