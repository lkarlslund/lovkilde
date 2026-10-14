// Command libparity checks that a library read from the compiled artifact alone answers laws as of
// a date exactly as the XML archive does: the same consolidated text, the same changes applied and
// refused, the same chain. It is the gate for serving from the artifact.
//
//	go run ./tools/libparity -artifact data/artifacts/<file>.lkz -n 2000
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"reflect"
	"time"

	"github.com/lkarlslund/retsinformation/internal/compiled"
	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/library"
)

func main() {
	artifact := flag.String("artifact", "", "the artifact to compare against the archive")
	xmlDir := flag.String("xml", "data/xml", "the XML archive")
	n := flag.Int("n", 2000, "laws with an amendment chain to compare, spread over the catalogue")
	show := flag.Int("show", 5, "differences to print")
	flag.Parse()
	if *artifact == "" {
		log.Fatal("-artifact is required")
	}
	arch, err := lexdania.Scan(context.Background(), *xmlDir)
	if err != nil {
		log.Fatal(err)
	}
	cache, err := compiled.OpenCached(*artifact, 256<<20)
	if err != nil {
		log.Fatal(err)
	}
	lib, err := library.Open(cache)
	if err != nil {
		log.Fatal(err)
	}
	if arch.Len() != lib.Len() {
		fmt.Printf("catalogue sizes differ: archive %d, artifact %d\n", arch.Len(), lib.Len())
	}
	// Laws with a chain, from the catalogue.
	raw, err := cache.Artifact().Catalogue()
	if err != nil {
		log.Fatal(err)
	}
	var recs []lexdania.CatalogueRecord
	if err := json.Unmarshal(raw, &recs); err != nil {
		log.Fatal(err)
	}
	var chained []string
	for _, r := range recs {
		for _, ref := range r.Meta.References {
			if ref.Kind == "Change" && ref.Accn != "" {
				chained = append(chained, r.Entry.Accession)
				break
			}
		}
	}
	step := max(1, len(chained) / *n)
	dates := []time.Time{time.Now().UTC().Truncate(24 * time.Hour), time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC)}
	same, differ, applied, refused := 0, 0, 0, 0
	t0 := time.Now()
	for i := 0; i < len(chained); i += step {
		acc := chained[i]
		for _, d := range dates {
			a, errA := arch.LawAsOf(acc, d)
			b, errB := lib.LawAsOf(acc, d)
			why := compare(a, errA, b, errB)
			if why == "" {
				same++
				if a != nil {
					applied += len(a.Applied)
					refused += len(a.Unresolved)
				}
				continue
			}
			differ++
			if differ <= *show {
				fmt.Printf("DIFFERS %s as of %s: %s\n", acc, d.Format("2006-01-02"), why)
			}
		}
	}
	fmt.Printf("%d laws with chains in the catalogue; compared %d answers in %s: %d identical (%d changes applied, %d refused), %d different\n",
		len(chained), same+differ, time.Since(t0).Round(time.Second), same, applied, refused, differ)
	if differ > 0 {
		os.Exit(1)
	}
}

func compare(a *lexdania.View, errA error, b *lexdania.View, errB error) string {
	switch {
	case (errA == nil) != (errB == nil):
		return fmt.Sprintf("archive error %v, artifact error %v", errA, errB)
	case errA != nil:
		return ""
	}
	if ma, mb := a.Content.Markdown(), b.Content.Markdown(); ma != mb {
		return fmt.Sprintf("text differs (%d vs %d bytes)", len(ma), len(mb))
	}
	if len(a.Applied) != len(b.Applied) || len(a.Unresolved) != len(b.Unresolved) {
		return fmt.Sprintf("applied %d/%d, refused %d/%d", len(a.Applied), len(b.Applied), len(a.Unresolved), len(b.Unresolved))
	}
	for i := range a.Unresolved {
		if a.Unresolved[i].Reason != b.Unresolved[i].Reason {
			return fmt.Sprintf("refusal %d: %q vs %q", i, a.Unresolved[i].Reason, b.Unresolved[i].Reason)
		}
	}
	strip := func(vs []lexdania.Version) []lexdania.Version {
		out := append([]lexdania.Version(nil), vs...)
		for i := range out {
			out[i].Path = "" // the artifact keeps the build machine's paths
		}
		return out
	}
	if !reflect.DeepEqual(strip(a.Chain), strip(b.Chain)) {
		return "chain differs"
	}
	if !reflect.DeepEqual(a.InBase, b.InBase) || !reflect.DeepEqual(strip(a.Silent), strip(b.Silent)) {
		return "receipts differ"
	}
	return ""
}
