package chain

import (
	"time"

	"github.com/lkarlslund/lovkilde/internal/lexdoc"
)

// Library is a collection of documents a server answers from: the XML archive the crawl writes, or
// the compiled artifact. Both give the same answers; the artifact needs nothing else on disk.
type Library interface {
	Len() int
	Entries() []Entry
	Find(accession string) (Entry, bool)
	Versions(accession string) ([]Version, error)
	LawAsOf(accession string, asOf time.Time) (*View, error)
	// Model is a document's model and where it was read from, a fresh one per call.
	Model(accession string) (*lexdoc.Document, string, bool)
	// Location names where the library lives, for status lines.
	Location() string
}

// CatalogueRecord is one document as the artifact's catalogue carries it: the archive entry as the
// scan read it, the metadata consolidation reads, whether it is a bill, and the changes its notes
// say it does not print yet. With the documents' models it is everything an amendment chain needs.
type CatalogueRecord struct {
	Entry   Entry
	Meta    Meta            `json:",omitempty"`
	Bill    bool            `json:",omitempty"`
	Waiting []WaitingChange `json:",omitempty"`
	// Readable is false for an entry whose document could not be read when the artifact was
	// built: it is in the catalogue, as it is in the archive, but cannot be opened.
	Readable bool `json:",omitempty"`
	// XMLHash and BodyHash are what the document was compiled from: the archive's hash
	// (archive.Hash) of its XML and of its body sidecar, "" when it had none. An incremental
	// build compares them with the files to know which documents it can carry over unchanged.
	XMLHash  string `json:",omitempty"`
	BodyHash string `json:",omitempty"`
}

// NewChains is consolidation over a library given by its parts: the catalogue's entries in scan
// order, each document as a Law, each act's record, and each document's waiting notes. The
// archive builds the same thing from its files.
func NewChains(entries []Entry, law func(string) (*Law, bool), act func(string) (*ActRecord, bool), notes func(string) []WaitingChange) *Chains {
	c := NewCatalog()
	for _, e := range entries {
		c.Add(e)
	}
	return NewChainsFor(c, law, act, notes)
}

// Catalog exposes the chains' catalogue: entries sorted, lookup by accession, and its size.
func (a *Chains) Len() int { return a.Catalog.Len() }
