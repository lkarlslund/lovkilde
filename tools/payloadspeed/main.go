// Throwaway measurement for the payload (artifact) build: how much does compiling
// documents on workers buy, and does it write the same file?
//
// A subset is the right instrument here. The two variants are one code path over N
// documents — open, model, marshal, compress one zstd frame — so the ratio they differ
// by is measurable on 4,000 documents in about a minute, and the full corpus is the
// same work 48 times over. What a subset cannot show is memory growth, and this build
// does not have any: it streams one chunk at a time.
//
// Both variants run in one process against the same documents, warm (the first pass
// over the mirror is discarded), so the comparison is the goroutines and not the page
// cache. GOMAXPROCS is what makes the serial variant: the same code, one worker.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/lkarlslund/retsinformation/internal/compiled"
	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/parallel"
)

func main() {
	root := "data/xml"
	docs := 4000
	if len(os.Args) > 1 {
		docs = atoi(os.Args[1])
	}

	a, err := lexdania.Scan(context.Background(), root)
	if err != nil {
		log.Fatal(err)
	}
	entries := a.Entries()
	// Spread over the archive by taking every stride-th entry, with the stride derived
	// from how many documents were asked for: 4,000 documents then covers the whole
	// mirror at stride 47, and 60,000 covers it at stride 3. Both cross kinds and decades
	// in the archive's own order rather than clustering in one medium.
	stride := len(entries) / docs
	if stride < 1 {
		stride = 1
	}
	var want []string
	seen := map[string]bool{}
	for i := 0; i < len(entries) && len(want) < docs; i += stride {
		e := entries[i]
		if e.Accession == "" || seen[e.Accession] {
			continue
		}
		seen[e.Accession] = true
		want = append(want, e.Accession)
	}
	fmt.Printf("mirror %d entries, measuring %d documents (stride %d)\n", len(entries), len(want), stride)

	dir, err := os.MkdirTemp("", "payloadspeed")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Warm: the first pass pays for reading 4,000 XML files and their sidecars once.
	tWarm := run(a, want, filepath.Join(dir, "warm.bin"), 1)
	fmt.Printf("warm-up      %6.2fs (discarded, page cache now warm)\n", tWarm.Seconds())

	cpus := runtime.GOMAXPROCS(0)
	runtime.GOMAXPROCS(1)
	serialPath := filepath.Join(dir, "serial.bin")
	tSerial := run(a, want, serialPath, 1)
	runtime.GOMAXPROCS(cpus)
	parPath := filepath.Join(dir, "parallel.bin")
	tPar := run(a, want, parPath, 512)

	fmt.Printf("serial       %6.2fs  %6.0f docs/s  (GOMAXPROCS=1)\n", tSerial.Seconds(), float64(len(want))/tSerial.Seconds())
	fmt.Printf("workers      %6.2fs  %6.0f docs/s  (GOMAXPROCS=%d)\n", tPar.Seconds(), float64(len(want))/tPar.Seconds(), cpus)
	fmt.Printf("speedup      %.2fx\n", tSerial.Seconds()/tPar.Seconds())

	if sameLayout(serialPath, parPath, want) {
		fmt.Println("same layout: identical frame sizes at identical relative offsets")
	} else {
		fmt.Println("DIFFERENT LAYOUT")
		os.Exit(1)
	}
}

// run writes the documents to path and returns how long the writing took. chunk == 1 is
// the serial path (compile and write in one step, one goroutine); chunk > 1 is the
// pipeline the build command uses.
func run(a *lexdania.Archive, want []string, path string, chunk int) time.Duration {
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	wr, err := compiled.NewWriter(f, "payloadspeed")
	if err != nil {
		f.Close()
		log.Fatal(err)
	}
	start := time.Now()
	if chunk <= 1 {
		for _, acc := range want {
			doc, _, ok := a.Open(acc)
			if !ok {
				continue
			}
			if err := wr.Add(acc, doc.Model()); err != nil {
				f.Close()
				log.Fatal(err)
			}
		}
	} else {
		for lo := 0; lo < len(want); lo += chunk {
			hi := min(lo+chunk, len(want))
			frames := make([]compiled.CompiledFrame, hi-lo)
			parallel.For(context.Background(), hi-lo, func(i int) {
				doc, _, ok := a.Open(want[lo+i])
				if !ok {
					return
				}
				var err error
				frames[i], err = wr.Compile(want[lo+i], doc.Model())
				if err != nil {
					log.Fatal(err)
				}
			})
			for _, fr := range frames {
				if fr.Accession == "" {
					continue
				}
				if err := wr.WriteFrame(fr); err != nil {
					f.Close()
					log.Fatal(err)
				}
			}
		}
	}
	wr.SetCatalogue([]byte("[]")) // no archive: an empty catalogue
	if err := wr.Close(); err != nil {
		f.Close()
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
	return time.Since(start)
}

// sameLayout compares where each document landed. Absolute offsets are not comparable
// across two files — the header carries the time it was written, and its compressed
// length shifts everything after it — so this compares frame lengths and distances.
func sameLayout(aPath, bPath string, want []string) bool {
	x, err := compiled.OpenArtifact(aPath)
	if err != nil {
		log.Fatal(err)
	}
	defer x.Close()
	y, err := compiled.OpenArtifact(bPath)
	if err != nil {
		log.Fatal(err)
	}
	defer y.Close()

	x0, ok := x.Ref(want[0])
	y0, ok2 := y.Ref(want[0])
	if !ok || !ok2 {
		return false
	}
	for _, acc := range want {
		ra, ok := x.Ref(acc)
		rb, ok2 := y.Ref(acc)
		if !ok || !ok2 {
			fmt.Printf("%s missing from one of them (%v %v)\n", acc, ok, ok2)
			return false
		}
		if ra.Length != rb.Length || ra.Raw != rb.Raw || ra.Offset-x0.Offset != rb.Offset-y0.Offset {
			fmt.Printf("%s: {off %d len %d raw %d} vs {off %d len %d raw %d}\n",
				acc, ra.Offset, ra.Length, ra.Raw, rb.Offset, rb.Length, rb.Raw)
			return false
		}
	}
	return true
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}
