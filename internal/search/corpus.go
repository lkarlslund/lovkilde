// Package search indexes the retsinformation mirror and answers full-text queries
// against it.
//
// The index is derived data: it is cut from the artifact at zero HTTP cost, and it is
// a plain file rather than a database. Two reasons, both measured rather than
// stylistic. The server reads the artifact and nothing else, so anything it reads has
// to be a file it can open read-only. And the store's own rejection of SQLite still
// applies here: a second engine would bring its own dependency weight and its own
// corruption story for data that is a rebuildable view of files we already have.
//
// The unit of retrieval is the section, not the document. An agent that gets a hit
// needs an address it can pass to lookup — "§ 267, stk. 3" — and a document-level
// hit in a 300-page act is a pointer to a haystack.
package search

import (
	"fmt"
	"math/rand"
	"sort"
)

// Unit is one addressable span of text: a section, a title, or a whole document
// that has no sections at all.
type Unit struct {
	Doc  int    // index into Index.Docs
	Addr string // Danish citation form, "" for a whole-document unit
	Text string
}

// DocMeta is what a hit needs to say about the document it came from.
type DocMeta struct {
	Accession string
	ELI       string
	Kind      string
	Year      int
	Number    int
	Title     string
	Source    string // xml | html | none
	Path      string

	// Status is the register's own claim about the document, carried through from the
	// file's Meta block: "Valid", "Historic", or "" when the payload does not say. It is
	// the register's assertion, not a verification, and the UI says so: a reader who
	// filters to "kun gældende" is asking the register what it thinks, not asking us to
	// prove the act has not been repealed by something we have not crawled.
	Status string

	// TypeCode is the publisher's own document-type code ("LOKDOK01", "KEN#LOKDOK23").
	// It is stored untranslated and unlabelled on purpose: the mirror holds the code and
	// no name table, so inventing Danish labels for it would be our invention, not the
	// register's word.
	TypeCode string
}

// Strata picks a balanced subset of n documents: up to perStratum from each stratum, chosen with
// a fixed seed so two runs measure the same corpus. perStratum <= 0 takes everything. It returns
// the chosen indices, stratum by stratum in key order.
//
// Balance matters for a measurement: the mirror is 19,347 folketingssager against a few thousand
// consolidated acts, so a naive first-N sample would index almost nothing but bills and would
// make every conclusion about section coverage an artefact of kind mix. Callers key a document by
// its kind and decade.
func Strata(n int, key func(i int) string, perStratum int) []int {
	byStratum := map[string][]int{}
	for i := 0; i < n; i++ {
		k := key(i)
		byStratum[k] = append(byStratum[k], i)
	}
	keys := make([]string, 0, len(byStratum))
	for k := range byStratum {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rng := rand.New(rand.NewSource(20260917))
	taken := make([]int, 0, n)
	for _, k := range keys {
		bucket := byStratum[k]
		if perStratum > 0 && len(bucket) > perStratum {
			rng.Shuffle(len(bucket), func(i, j int) { bucket[i], bucket[j] = bucket[j], bucket[i] })
			bucket = bucket[:perStratum]
		}
		taken = append(taken, bucket...)
	}
	return taken
}

// StratumOf is the stratum a document of this kind and year falls in.
func StratumOf(kind string, year int) string { return fmt.Sprintf("%s/%d0", kind, year/10) }
