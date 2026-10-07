package search

import (
	"context"
	"fmt"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
	"github.com/lkarlslund/retsinformation/internal/parallel"
)

// The XML archive, read directly: what the measurements in this package's tests replay. Nothing
// that serves or builds an index reads XML; an index is cut from the compiled artifact.

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
	taken := make([]lexdania.Entry, 0, len(entries))
	for _, i := range Strata(len(entries), func(i int) string { return StratumOf(entries[i].Kind, entries[i].Year) }, perStratum) {
		taken = append(taken, entries[i])
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
	parallel.For(ctx, len(taken), func(i int) {
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

		// The same cut the artifact path makes (stream.go), so a mirror build and an
		// artifact build index the same units.
		s.units = DocUnits(i, meta, doc.Model())
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

// XMLDocuments reads documents through the archive's own reader. It is the building and grading
// path: the artifact does not exist yet while an index is being built, and a snippet graded
// against a document that was just parsed does not need it either way.
type XMLDocuments struct{}

// Document parses the file the index says the document is in.
func (XMLDocuments) Document(meta DocMeta) (*lexdoc.Document, error) {
	if meta.Path == "" {
		return nil, nil
	}
	doc, err := lexdania.LoadWithBody(meta.Path)
	if err != nil {
		return nil, err
	}
	return doc.Model(), nil
}
