// Package library serves the mirror from the compiled artifact alone: the catalogue its build
// stored, each document's model, and each amending act's changes in its model. Lookups, browsing
// and laws as of a date are answered without the XML archive, by the same consolidation code that
// reads the archive (package chain), so the two give the same answers.
package library

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/lkarlslund/lovkilde/internal/chain"
	"github.com/lkarlslund/lovkilde/internal/compiled"
	"github.com/lkarlslund/lovkilde/internal/lexdoc"
)

// Artifact is a library read from a compiled artifact.
type Artifact struct {
	cache   *compiled.Cache
	records map[string]*chain.CatalogueRecord
	chains  *chain.Chains
}

// Open reads the artifact's catalogue and makes a library of it. The documents themselves are read
// through the cache when they are asked for.
func Open(cache *compiled.Cache) (*Artifact, error) {
	raw, err := cache.Artifact().Catalogue()
	if err != nil {
		return nil, fmt.Errorf("%s: catalogue: %w", cache.Artifact().Path(), err)
	}
	var recs []chain.CatalogueRecord
	if err := json.Unmarshal(raw, &recs); err != nil {
		return nil, fmt.Errorf("%s: catalogue: %w", cache.Artifact().Path(), err)
	}
	a := &Artifact{cache: cache, records: make(map[string]*chain.CatalogueRecord, len(recs))}
	entries := make([]chain.Entry, len(recs))
	for i := range recs {
		entries[i] = recs[i].Entry
		a.records[recs[i].Entry.Accession] = &recs[i]
	}
	a.chains = chain.NewChains(entries, a.law, a.act, a.notes)
	return a, nil
}

// law is a document as consolidation reads it: its catalogue metadata and its stored model.
func (a *Artifact) law(accession string) (*chain.Law, bool) {
	rec, ok := a.records[accession]
	if !ok || !rec.Readable {
		path := ""
		if ok {
			path = rec.Entry.Path
		}
		return chain.NewLaw(chain.Meta{}, path, nil), false
	}
	return chain.NewLaw(rec.Meta, rec.Entry.Path, func() *lexdoc.Document {
		doc, err := a.cache.Document(accession)
		if err != nil {
			// The catalogue says the document was read when the artifact was built, and its frame
			// is checked when the artifact is opened: this is a damaged file, and an empty model
			// makes the chain report the act as contributing nothing rather than crash an answer.
			return &lexdoc.Document{}
		}
		return doc
	}), true
}

// act is an amending act's record: whether it is a bill, and the changes its model carries.
func (a *Artifact) act(accession string) (*chain.ActRecord, bool) {
	rec, ok := a.records[accession]
	if !ok {
		return &chain.ActRecord{}, false
	}
	if !rec.Readable {
		return &chain.ActRecord{Path: rec.Entry.Path}, false
	}
	if rec.Bill {
		return &chain.ActRecord{Path: rec.Entry.Path, Bill: true}, true
	}
	doc, err := a.cache.Document(accession)
	if err != nil {
		return &chain.ActRecord{Path: rec.Entry.Path}, false
	}
	out := &chain.ActRecord{Path: rec.Entry.Path}
	if doc.Changes != nil {
		out.Edits, out.InForce = doc.Changes.Edits, doc.Changes.InForce
	}
	return out, true
}

// notes are the changes a document's notes say it does not print yet, as the build read them.
func (a *Artifact) notes(accession string) []chain.WaitingChange {
	if rec, ok := a.records[accession]; ok {
		return rec.Waiting
	}
	return nil
}

// Len is the number of documents in the catalogue.
func (a *Artifact) Len() int { return a.chains.Len() }

// Entries is the catalogue sorted by kind, year and number.
func (a *Artifact) Entries() []chain.Entry { return a.chains.Entries() }

// Find is the catalogue entry for an accession.
func (a *Artifact) Find(accession string) (chain.Entry, bool) { return a.chains.Find(accession) }

// Versions lists a law's amendment chain.
func (a *Artifact) Versions(accession string) ([]chain.Version, error) {
	return a.chains.Versions(accession)
}

// LawAsOf is a law as it stood on a date.
func (a *Artifact) LawAsOf(accession string, asOf time.Time) (*chain.View, error) {
	return a.chains.LawAsOf(accession, asOf)
}

// LawAdoptedAsOf is a law as amended by every act signed by a date.
func (a *Artifact) LawAdoptedAsOf(accession string, asOf time.Time) (*chain.View, error) {
	return a.chains.LawAdoptedAsOf(accession, asOf)
}

// Model is a document's model as the artifact stores it.
func (a *Artifact) Model(accession string) (*lexdoc.Document, string, bool) {
	rec, ok := a.records[accession]
	if !ok || !rec.Readable {
		return nil, "", false
	}
	doc, err := a.cache.Document(accession)
	if err != nil {
		return nil, rec.Entry.Path, false
	}
	return doc, rec.Entry.Path, true
}

// Location is the artifact file.
func (a *Artifact) Location() string { return a.cache.Artifact().Path() }

var _ chain.Library = (*Artifact)(nil)
