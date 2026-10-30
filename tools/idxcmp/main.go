// Throwaway check for the parallel build work: are two index files the same index?
//
// The header carries a build timestamp, so a plain sha256 of the files always differs.
// What has to be equal is everything else — the document table, the unit table, the
// dictionary, the postings — plus the answers, because ranking depends on a value the
// file does not expose twice (average unit length) and a byte-identical dictionary with
// different scores would still be a different search service.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log"
	"os"

	"github.com/lkarlslund/lovkilde/internal/search"
)

var queries = []string{
	"miljøbeskyttelsesloven",
	"ubetinget råderet AND tinglyst",
	"pligt AND virksomhed -lejlighed",
	"»præjudicielle spørgsmål«",
	"lov nr. 1399",
	"hvornår træder loven i kraft",
}

func fingerprint(idx *search.Index) string {
	h := sha256.New()
	var buf [8]byte
	put := func(s string) {
		binary.BigEndian.PutUint64(buf[:], uint64(len(s)))
		h.Write(buf[:8])
		h.Write([]byte(s))
	}
	put(fmt.Sprintf("B=%v", idx.B))
	for _, d := range idx.Docs {
		put(d.Accession)
		put(d.ELI)
		put(d.Kind)
		put(fmt.Sprintf("%d %d %s %s %s", d.Year, d.Number, d.Source, d.Status, d.TypeCode))
		put(d.Title)
		put(d.Path)
	}
	for i, u := range idx.Units {
		put(fmt.Sprintf("%d|%d|%s|%d", i, u.Doc, u.Addr, u.Len))
	}
	for i, t := range idx.Terms {
		put(fmt.Sprintf("%d|%d|%s", i, idx.Stream[i], t))
	}
	for i := range idx.Terms {
		for one := range idx.Postings(i).All() {
			binary.BigEndian.PutUint64(buf[:], uint64(one.Unit))
			h.Write(buf[:8])
			h.Write([]byte{one.Tf})
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func answers(idx *search.Index) string {
	h := sha256.New()
	for _, q := range queries {
		hits := idx.Search(search.Query{Text: q, Limit: 20})
		for _, hit := range hits {
			fmt.Fprintf(h, "%s|%d|%s|%.12f\n", q, hit.Unit, hit.Addr, hit.Score)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: idxcmp <a.idx> <b.idx>")
	}
	var out [2]*search.Index
	for i, p := range os.Args[1:] {
		idx, err := search.Load(p)
		if err != nil {
			log.Fatalf("%s: %v", p, err)
		}
		out[i] = idx
		fmt.Printf("%-24s %d docs, %d units, %d terms, built %d\n", p, len(idx.Docs), len(idx.Units), len(idx.Terms), idx.Built)
	}
	fa, fb := fingerprint(out[0]), fingerprint(out[1])
	qa, qb := answers(out[0]), answers(out[1])
	fmt.Printf("structure  %s vs %s\n", fa, fb)
	fmt.Printf("answers    %s vs %s\n", qa, qb)
	if fa == fb && qa == qb {
		fmt.Println("IDENTICAL index, identical answers")
		return
	}
	fmt.Println("DIFFERENT")
	if os.Getenv("IDXCMPDUMP") != "" {
		for _, q := range queries {
			ha := out[0].Search(search.Query{Text: q, Limit: 20})
			hb := out[1].Search(search.Query{Text: q, Limit: 20})
			fmt.Printf("\nQUERY %q  (%d vs %d hits)\n", q, len(ha), len(hb))
			for i := 0; i < len(ha) || i < len(hb); i++ {
				var a, b string
				if i < len(ha) {
					a = fmt.Sprintf("%d %s %.9f cov%d", ha[i].Unit, ha[i].Addr, ha[i].Score, ha[i].Covered)
				}
				if i < len(hb) {
					b = fmt.Sprintf("%d %s %.9f cov%d", hb[i].Unit, hb[i].Addr, hb[i].Score, hb[i].Covered)
				}
				mark := "  "
				if a != b {
					mark = "**"
				}
				fmt.Printf("%s %-2d | %-42s | %-42s\n", mark, i, a, b)
			}
		}
	}
	// Name the first divergence rather than leaving the reader to diff two hashes.
	for i := range out[0].Docs {
		if i >= len(out[1].Docs) || out[0].Docs[i] != out[1].Docs[i] {
			fmt.Printf("first document difference at %d\n", i)
			break
		}
	}
	os.Exit(1)
}
