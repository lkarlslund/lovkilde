package lexdania

import (
	"time"

	"github.com/lkarlslund/retsinformation/internal/chain"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// Consolidation lives in package chain and reads nothing but models and metadata. What is here
// supplies it from the XML archive: documents as chain.Law, acts' records read from their markup,
// and the archive as a chain.Library. The names below keep this package's callers as they were.
type (
	Entry           = chain.Entry
	Version         = chain.Version
	View            = chain.View
	ActRecord       = chain.ActRecord
	WaitingChange   = chain.WaitingChange
	Law             = chain.Law
	Chains          = chain.Chains
	Library         = chain.Library
	CatalogueRecord = chain.CatalogueRecord
)

var _ chain.Library = (*Archive)(nil)

// Versions lists the documents that amended this one, oldest first; see chain.VersionsOf.
func (d *Document) Versions() []Version {
	m := CatalogueMetaOf(d.Meta)
	return chain.VersionsOf(&m)
}

// Law is the document as consolidation reads it.
func (d *Document) Law(path string) *Law { return chain.NewLaw(CatalogueMetaOf(d.Meta), path, d.Model) }

// LawAsOf reconstructs a law as it stood at a date from a parsed document and an Opener for its
// amending acts, without the archive's help: what a test drives from fixtures.
func LawAsOf(base *Document, asOf time.Time, open Opener) *View {
	return chain.ResolveAsOf(base.Law(""), asOf, actsFrom(open), false)
}

// LawAdoptedAsOf is LawAsOf counting every change of the acts signed by asOf, whether or not it
// has taken effect.
func LawAdoptedAsOf(base *Document, asOf time.Time, open Opener) *View {
	return chain.ResolveAsOf(base.Law(""), asOf, actsFrom(open), true)
}

// WaitingNotes reads the changes a document's notes say it does not print yet; see
// chain.WaitingNotes.
func WaitingNotes(lines []string) []WaitingChange { return chain.WaitingNotes(lines) }

// Model is a document's model read from the archive's files, for chain.Library.
func (a *Archive) Model(accession string) (*lexdoc.Document, string, bool) {
	doc, path, ok := a.Open(accession)
	if !ok {
		return nil, path, false
	}
	return doc.Model(), path, true
}
