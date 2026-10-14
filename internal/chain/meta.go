// Package chain is how a law stood on a date: its model, the amending acts its metadata names, the
// changes each of them makes, and the rules for when they take effect, applied in order. It reads
// nothing but models and their metadata — the XML reader (lexdania) and the compiled artifact
// (library) are two ways of supplying them, and give the same answers.
package chain

import (
	"time"

	"github.com/lkarlslund/lovkilde/internal/lexdoc"
)

// TargetHint is the law a change list names: "lov nr. 1399 af 19. december 2019".
type TargetHint = lexdoc.Target

// Meta is the part of a document's register metadata consolidation reads.
type Meta struct {
	DocumentType string        `json:",omitempty"`
	Accession    string        `json:",omitempty"`
	Title        string        `json:",omitempty"`
	PopularTitle string        `json:",omitempty"`
	Year         int           `json:",omitempty"`
	Number       int           `json:",omitempty"`
	Status       lexdoc.Status `json:",omitempty"`
	DiesSigni    string        `json:",omitempty"`
	References   []Reference   `json:",omitempty"`
}

// Reference is one anchor of the register's reference layer: a document this one names, and what
// it says of it.
type Reference struct {
	Kind string // anchor element name: Concerns, Change, DateOfSubmit, Sign, Sub-Sign, …
	ID   string // the id= value

	Accn      string // Ref_Accn: the accession of the referenced document
	Effective string // Ref_Af: the date the referenced document takes effect for this one
	Text      string // Ref_Text: the referenced document's title

	StartDate string // StartDate for this anchor (period of validity)
	EndDate   string // EndDate for this anchor

	Signature string // Signature for this anchor (signing authority)

	// Values holds any other value element attached to this anchor, keyed by
	// element name, so a value we have no field for is still reachable.
	Values map[string]string
}

// Changes returns the later documents that amended this one, each with the date it takes effect.
// This is the authoritative forward edge of the version chain.
func (m *Meta) Changes() []Reference {
	var out []Reference
	for _, r := range m.References {
		if r.Kind == "Change" {
			out = append(out, r)
		}
	}
	return out
}

// ParseDate parses the register's dates; see lexdoc.ParseDate.
func ParseDate(s string) (time.Time, bool) { return lexdoc.ParseDate(s) }

// Squeeze collapses whitespace; see lexdoc.Squeeze.
func Squeeze(s string) string { return lexdoc.Squeeze(s) }
