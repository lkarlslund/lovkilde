// Command dictsamples writes training samples for the artifact's zstd dictionary: the model JSON
// of every Nth document published up to a cutoff year, one file each, from an existing artifact.
//
// Retraining is deliberate, because a new dictionary re-uploads the whole published artifact once
// (see internal/compiled/dictionary). The steps, as used for retsin-1.zdict:
//
//	go run ./tools/dictsamples -out /tmp/samples -until 2024 -every 20
//	zstd --train-fastcover -T0 --maxdict=1048576 /tmp/samples/* -o internal/compiled/dictionary/retsin-2.zdict
//
// then point dictionary.Current at the new file, and measure it on the documents after the cutoff
// before adopting it: a dictionary is only worth its re-upload if it compresses documents it has
// not seen.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/lkarlslund/lovkilde/internal/artifactdir"
	"github.com/lkarlslund/lovkilde/internal/compiled"
)

func main() {
	path := flag.String("artifact", "", "artifact to read documents from (default: the current one in data/)")
	out := flag.String("out", "", "directory to write samples into")
	until := flag.Int("until", 2024, "last publication year to sample; later documents are left for measuring")
	every := flag.Int("every", 20, "take every Nth document of the period")
	flag.Parse()
	if *out == "" {
		log.Fatal("-out is required")
	}
	if *path == "" {
		cur, err := artifactdir.Current("data")
		if err != nil {
			log.Fatal(err)
		}
		*path = cur
	}
	a, err := compiled.OpenArtifact(*path)
	if err != nil {
		log.Fatal(err)
	}
	defer a.Close()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	seen, written, bytes := 0, 0, 0
	for _, acc := range a.Documents() {
		raw, err := a.Payload(acc)
		if err != nil {
			log.Fatal(err)
		}
		var head struct {
			Identity struct{ PublishedYear int }
		}
		if err := json.Unmarshal(raw, &head); err != nil || head.Identity.PublishedYear == 0 || head.Identity.PublishedYear > *until {
			continue
		}
		seen++
		if seen%*every != 0 {
			continue
		}
		if err := os.WriteFile(filepath.Join(*out, acc+".json"), raw, 0o644); err != nil {
			log.Fatal(err)
		}
		written++
		bytes += len(raw)
	}
	fmt.Printf("%d samples, %.0f MB, from %d documents published up to %d\n", written, float64(bytes)/1e6, seen, *until)
}
