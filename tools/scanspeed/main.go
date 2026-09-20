// Throwaway measurement for the index/payload speed work: does Scan scale, and does
// the parallel path produce the same archive as the serial one?
//
// GOMAXPROCS is set inside the run rather than by the environment so both paths
// execute in one process against the same page cache, in the same order, with a
// warm-up pass first: comparing a cold first scan against a warm second one would
// measure the kernel's cache, not the goroutines.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"runtime"
	"time"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

func scanOnce(root string) (*lexdania.Archive, time.Duration, string) {
	start := time.Now()
	a, err := lexdania.Scan(context.Background(), root)
	if err != nil {
		log.Fatal(err)
	}
	d := time.Since(start)
	h := sha256.New()
	var buf [8]byte
	for _, e := range a.Entries() {
		binary.BigEndian.PutUint64(buf[:], uint64(len(e.Accession)))
		h.Write(buf[:])
		h.Write([]byte(e.Accession))
		h.Write([]byte{0})
		h.Write([]byte(e.Path))
		h.Write([]byte{0})
		binary.BigEndian.PutUint64(buf[:], uint64(e.Year))
		h.Write(buf[:])
		h.Write([]byte(e.Status))
		h.Write([]byte{0})
		binary.BigEndian.PutUint64(buf[:], uint64(e.Number))
		h.Write(buf[:])
	}
	return a, d, fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func main() {
	root := "data/xml"
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	cpus := runtime.NumCPU()

	a0, d0, _ := scanOnce(root)
	fmt.Printf("warm-up     %6.1fs  %d entries (page cache now warm, ignored)\n", d0.Seconds(), a0.Len())

	procs := runtime.GOMAXPROCS(1)
	a1, d1, fp1 := scanOnce(root)
	aP, dP, fpP := scanOnce(root)
	runtime.GOMAXPROCS(cpus)
	aN, dN, fpN := scanOnce(root)

	fmt.Printf("GOMAXPROCS=1  %6.1fs  %d entries  fingerprint %s\n", d1.Seconds(), a1.Len(), fp1)
	fmt.Printf("GOMAXPROCS=1 again %5.1fs  %d entries  fingerprint %s\n", dP.Seconds(), aP.Len(), fpP)
	fmt.Printf("GOMAXPROCS=%d  %6.1fs  %d entries  fingerprint %s\n", cpus, dN.Seconds(), aN.Len(), fpN)
	fmt.Printf("skipped serial=%d parallel=%d\n", a1.Skipped, aN.Skipped)

	if fp1 != fpP || fp1 != fpN || a1.Len() != aN.Len() {
		log.Fatalf("the parallel scan disagrees with the serial one")
	}
	fmt.Printf("identical archives; speedup %.2fx at %d procs (procs before=%d)\n", d1.Seconds()/dN.Seconds(), cpus, procs)
}
