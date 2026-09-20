// Command artifactcmp answers one question about two artifact files: is the payload the same,
// and if the file sizes differ, where.
//
// It hashes the compressed frames in document order, which is the only part a build produces
// from the corpus, and then reports the header and the document tables separately. Two builds
// of the same corpus are expected to differ in the header's build stamp and in the absolute
// offsets the tables carry, and in nothing else; a difference in the frame hash means the
// parallel build changed the payload.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lkarlslund/retsinformation/internal/compiled"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: artifactcmp A.bin B.bin")
		os.Exit(2)
	}
	a, err := compiled.OpenArtifact(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer a.Close()
	b, err := compiled.OpenArtifact(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer b.Close()

	fmt.Printf("A %s: %d documents\nB %s: %d documents\n", os.Args[1], len(a.Documents()), os.Args[2], len(b.Documents()))
	reportHeaders(a.Header, b.Header)

	orderA, orderB := a.Documents(), b.Documents()
	if len(orderA) != len(orderB) {
		fmt.Printf("DOCUMENT ORDER DIFFERS: %d against %d\n", len(orderA), len(orderB))
		os.Exit(1)
	}
	for i := range orderA {
		if orderA[i] != orderB[i] {
			fmt.Printf("DOCUMENT ORDER DIFFERS at %d: %s against %s\n", i, orderA[i], orderB[i])
			os.Exit(1)
		}
	}
	fmt.Println("document order identical")

	// The frame bytes are the payload: reading them from the file rather than decoding them
	// compares what the build wrote, at the cost of a pass over the file rather than a pass
	// over the model.
	hashA, err := frames(a)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	hashB, err := frames(b)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("compressed frames  A %s\n                   B %s", hashA, hashB)
	if hashA == hashB {
		fmt.Println("  -> payload identical")
	} else {
		fmt.Println("  -> PAYLOAD DIFFERS")
	}

	// Tables are compared as deltas from the first frame, because the header's build stamp
	// shifts every absolute offset in the file by the length of its own text.
	firstA, _ := a.Ref(orderA[0])
	firstB, _ := b.Ref(orderB[0])
	var lengthDiff, deltaDiff int
	var firstLength, firstDelta string
	for _, acc := range orderA {
		ra, _ := a.Ref(acc)
		rb, _ := b.Ref(acc)
		if ra.Length != rb.Length || ra.Raw != rb.Raw {
			lengthDiff++
			if firstLength == "" {
				firstLength = fmt.Sprintf("%s: %d/%d against %d/%d", acc, ra.Length, ra.Raw, rb.Length, rb.Raw)
			}
		}
		if ra.Offset-firstA.Offset != rb.Offset-firstB.Offset {
			deltaDiff++
			if firstDelta == "" {
				firstDelta = fmt.Sprintf("%s: +%d against +%d", acc, ra.Offset-firstA.Offset, rb.Offset-firstB.Offset)
			}
		}
	}
	fmt.Printf("table: %d documents with different frame lengths (%s), %d with different relative offsets (%s)\n",
		lengthDiff, orNone(firstLength), deltaDiff, orNone(firstDelta))
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func reportHeaders(ha, hb compiled.ArtifactHeader) {
	fields := []struct {
		name string
		a, b string
	}{
		{"version", fmt.Sprint(ha.Version), fmt.Sprint(hb.Version)},
		{"codec", ha.Codec, hb.Codec},
		{"compression", ha.Compression, hb.Compression},
		{"source", ha.Source, hb.Source},
		{"build", ha.Build, hb.Build},
		{"modified", fmt.Sprint(ha.Modified), fmt.Sprint(hb.Modified)},
		{"created", ha.Created.Format(time.RFC3339), hb.Created.Format(time.RFC3339)},
	}
	for _, f := range fields {
		mark := "same"
		if f.a != f.b {
			mark = "DIFFERS"
		}
		fmt.Printf("header %s: %s / %s  (%s)\n", f.name, f.a, f.b, mark)
	}
}

// frames hashes the stored bytes of every document frame in order.
func frames(a *compiled.Artifact) (string, error) {
	file, err := os.Open(a.Path())
	if err != nil {
		return "", err
	}
	defer file.Close()
	sum := sha256.New()
	buf := make([]byte, 1<<20)
	for _, acc := range a.Documents() {
		ref, ok := a.Ref(acc)
		if !ok {
			return "", fmt.Errorf("%s: not in the table", acc)
		}
		for written := uint64(0); written < ref.Length; {
			want := ref.Length - written
			if want > uint64(len(buf)) {
				want = uint64(len(buf))
			}
			n, err := file.ReadAt(buf[:want], int64(ref.Offset+written))
			if err != nil && err != io.EOF {
				return "", err
			}
			sum.Write(buf[:n])
			written += uint64(n)
			if n == 0 {
				return "", fmt.Errorf("%s: stopped at %d of %d bytes", acc, written, ref.Length)
			}
		}
	}
	return hex.EncodeToString(sum.Sum(nil))[:16], nil
}
