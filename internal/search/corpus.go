// Package search indexes the retsinformation mirror and answers full-text queries
// against it.
//
// The index is derived data, like data/meta/kv: it is rebuilt from the mirror at
// zero HTTP cost, and it is a plain file rather than a second database. Two reasons,
// both measured rather than stylistic. Badger takes an exclusive lock per process,
// and `retsin mcp` deliberately never opens the state store so the nightly crawl is
// never blocked by a server; anything the server reads has to be openable
// read-only alongside the crawl. And the store's own rejection of SQLite still
// applies here: a second engine would bring its own dependency weight and its own
// corruption story for data that is a rebuildable view of files we already have.
//
// The unit of retrieval is the section, not the document. An agent that gets a hit
// needs an address it can pass to lookup — "§ 267, stk. 3" — and a document-level
// hit in a 300-page act is a pointer to a haystack.
package search

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
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

// Sample reads a balanced subset of the mirror and turns each document into units.
//
// Balance matters for a measurement: the mirror is 19,347 folketingssager against a
// few thousand consolidated acts, so a naive first-N sample would index almost
// nothing but bills and would make every conclusion about section coverage an
// artefact of kind mix. Strata are (kind, decade, text source), and each contributes
// up to perStratum documents, chosen with a fixed seed so two runs measure the same
// corpus.
func Sample(ctx context.Context, root string, perStratum int) (docs []DocMeta, units []Unit, err error) {
	archive, err := lexdania.Scan(ctx, root)
	if err != nil {
		return nil, nil, err
	}
	if archive.Len() == 0 {
		// An empty mirror is not a small sample, it is a wrong path or a crawl that
		// stored nothing. Reporting zero documents as a measurement would be the
		// quiet version of that failure, so it is an error here regardless of what
		// Scan chose to report.
		return nil, nil, fmt.Errorf("no documents found under %s (files that could not be read: %d)", root, archive.Skipped)
	}
	entries := archive.Entries()
	byStratum := map[string][]lexdania.Entry{}
	for _, e := range entries {
		byStratum[fmt.Sprintf("%s/%d0", e.Kind, e.Year/10)] = append(byStratum[fmt.Sprintf("%s/%d0", e.Kind, e.Year/10)], e)
	}
	keys := make([]string, 0, len(byStratum))
	for k := range byStratum {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rng := rand.New(rand.NewSource(20260917))
	taken := make([]lexdania.Entry, 0, len(keys)*perStratum)
	for _, k := range keys {
		bucket := byStratum[k]
		// perStratum <= 0 means "everything", which is what `retsin index build` asks
		// for. Keeping it in the same function means the measured sample and the
		// shipped index go through identical code, so a sample result that did not
		// survive into the real index is a bug in this function rather than a surprise.
		if perStratum > 0 && len(bucket) > perStratum {
			rng.Shuffle(len(bucket), func(i, j int) { bucket[i], bucket[j] = bucket[j], bucket[i] })
			bucket = bucket[:perStratum]
		}
		taken = append(taken, bucket...)
	}

	// Open, model and cut into units, from workers. Each document is independent here:
	// LoadWithBody reads one file and its sidecar and touches no shared state, and the
	// model and the unit cut are pure functions of that document. This is also where a
	// build's time actually goes — measured on a 12,797 document sample, 20 s of the
	// 21.75 s the phase takes, against 1.4 s for scanning the heads of all 191,429.
	//
	// Results are written back into the slot each document owns, and the units carry
	// the slot index as their document id until the flatten below rewrites it. That is
	// what keeps the file byte-identical at any worker count: document ids are still
	// assigned in walk order, and a document that fails to open still consumes no id.
	type slot struct {
		meta  DocMeta
		units []Unit
		ok    bool
	}
	slots := make([]slot, len(taken))
	lexdania.Parallel(ctx, len(taken), func(i int) {
		e := taken[i]
		doc, path, ok := archive.Open(e.Accession)
		if !ok {
			return
		}
		meta := DocMeta{
			Accession: e.Accession,
			ELI:       e.ELIPath(),
			Kind:      e.Kind,
			Year:      e.Year,
			Number:    e.Number,
			Title:     e.Title,
			Source:    string(doc.Source),
			Path:      path,
			Status:    string(e.Status),
			TypeCode:  e.DocumentType,
		}
		s := &slots[i]
		s.meta, s.ok = meta, true

		// A title unit exists because searching for a law by name — "miljøbeskyttelsesloven",
		// "ejendomsvurderingsafgiftsloven" — is the single most common legal query, and
		// without this the index can only find a statute whose title it never indexed.
		if strings.TrimSpace(e.Title) != "" {
			s.units = append(s.units, Unit{Doc: i, Addr: "titel", Text: e.Title})
		}

		// The model, not the section list: see modelunits.go for what each kind of unit is and
		// why numbered conditions are not among them. Empty text is still skipped by the unit
		// builder, which is where the 288,323 empty containers belong — a kapitel that only
		// groups children has no text to index whatever representation it came from.
		s.units = append(s.units, Units(i, meta, doc.Model())...)
	})

	for i, s := range slots {
		if !s.ok {
			continue
		}
		docID := len(docs)
		docs = append(docs, s.meta)
		for _, u := range s.units {
			u.Doc = docID
			units = append(units, u)
		}
		// Release as we go: the unit slices here are a second copy of every Unit header
		// in the corpus, and holding them until the end showed up as heap the build no
		// longer needed. The text they point at is shared with the index either way.
		slots[i].units = nil
	}
	return docs, units, nil
}
