package lexdania

import (
	"github.com/lkarlslund/retsinformation/internal/chain"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// Location is the archive's root directory.
func (a *Archive) Location() string { return a.Root }

// ScanOrder is the archive's entries in the order the scan found them, which the consolidation
// indexes inherit; Entries sorts. The artifact's catalogue keeps this order so a library read
// from it consolidates exactly as the archive does.
func (a *Archive) ScanOrder() []Entry { return append([]Entry(nil), a.Catalog.ScanOrder()...) }

// CatalogueMeta is the part of a document's metadata consolidation reads.
type CatalogueMeta = chain.Meta

// CatalogueMetaOf is the part of a document's metadata consolidation reads.
func CatalogueMetaOf(m Meta) CatalogueMeta {
	return CatalogueMeta{DocumentType: m.DocumentType, Accession: m.Accession, Title: m.Title, PopularTitle: m.PopularTitle,
		Year: m.Year, Number: m.Number, Status: m.Status, DiesSigni: m.DiesSigni, References: m.References}
}

// CatalogueRecordOf is what the artifact's catalogue carries for a document read from the archive.
func CatalogueRecordOf(e Entry, doc *Document) CatalogueRecord {
	return CatalogueRecord{Entry: e, Meta: CatalogueMetaOf(doc.Meta), Bill: doc.IsBill(),
		Waiting: WaitingNotes(doc.textLines()), Readable: true}
}

// ArtifactRecords is what the artifact stores for a document read from the archive: its model —
// with the shelf and sequence the entry gives it, and its Changes when it amends anything or says
// when its parts take effect — and its catalogue record. One function, so the build and every test
// that writes an artifact store the same thing.
func ArtifactRecords(e Entry, doc *Document, path string) (*lexdoc.Document, CatalogueRecord) {
	m := doc.Model()
	// The shelf and its sequence belong to whoever located the document (see Document.Model),
	// and that is the archive entry: filling them here is what lets a reader of the artifact
	// alone cite the document by its ELI path.
	m.Identity.Medium, m.Identity.PublishedYear, m.Identity.PublishedNumber = e.Kind, e.Year, e.Number
	rec := CatalogueRecordOf(e, doc)
	if !rec.Bill {
		act := readActModel(doc, path, m)
		if len(act.Edits) > 0 || !act.InForce.Default.IsZero() || len(act.InForce.Rules) > 0 {
			m.Changes = &lexdoc.Changes{Edits: act.Edits, InForce: act.InForce}
		}
	}
	return m, rec
}
