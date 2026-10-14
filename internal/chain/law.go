package chain

import "github.com/lkarlslund/lovkilde/internal/lexdoc"

// Law is a document as consolidation reads it: the register's metadata and the model. It is what
// the chain is resolved against and what it consults in other documents, and it carries nothing of
// the XML: a law read from the archive's files and one read from the compiled artifact are the same
// thing to the engine, which is what lets a server answer from the artifact alone.
type Law struct {
	Meta Meta
	Path string // where it was read from, for receipts; empty when nobody needs to know

	// model builds the document's model. Every call returns a document of its own, as
	// Document.Model does: a consolidated view replaces its copy's content, and a shared one
	// would make one answer depend on another.
	model func() *lexdoc.Document
}

// NewLaw is a law from its metadata and a function returning its model, one per call.
func NewLaw(meta Meta, path string, model func() *lexdoc.Document) *Law {
	return &Law{Meta: meta, Path: path, model: model}
}

// Model is the law's model, a fresh one per call.
func (l *Law) Model() *lexdoc.Document { return l.model() }

// Title is the document title, falling back to the popular title.
func (l *Law) Title() string {
	if l.Meta.Title != "" {
		return l.Meta.Title
	}
	return l.Meta.PopularTitle
}

// Versions lists the documents that amended this one, oldest first; see Document.Versions.
func (l *Law) Versions() []Version { return VersionsOf(&l.Meta) }
