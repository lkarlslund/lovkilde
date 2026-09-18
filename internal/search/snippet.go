package search

import (
	"strings"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// DefaultSnippetRunes is how much text a hit shows around the match.
const DefaultSnippetRunes = 320

// Documents reads the document a hit names. The index deliberately stores no text, so somebody
// has to: serving passes the compiled artifact (internal/compiled.Cache), which reads one document
// without parsing anything, and the offline paths pass XMLDocuments, which reads the publisher's
// file because there the parse is the point.
type Documents interface {
	Document(meta DocMeta) (*lexdoc.Document, error)
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

// SnippetFrom centres a window on text that has already been read.// SnippetFrom centres a window on text that has already been read.
func SnippetFrom(text string, terms []string, window int) string {
	if text == "" {
		return ""
	}
	if window <= 0 {
		window = DefaultSnippetRunes
	}
	return centerWindow(text, terms, window)
}

// UnitTexts reads document text for a batch of hits, reading each document once.
//
// Its lifetime is one search: the cache is deliberately not shared between searches, because a
// document that changed since the last query must not keep answering with the first reading, and
// because a bounded per-request cache needs no eviction policy to reason about. The document it
// caches is the model, so a hit's snippet, its proximity grade and the answer that opens it are
// all resolutions of the same address.
type UnitTexts struct {
	src  Documents
	docs map[string]*lexdoc.Document
}

// NewUnitTexts starts a cache for one search over the documents src can read.
func NewUnitTexts(src Documents) *UnitTexts {
	return &UnitTexts{src: src, docs: map[string]*lexdoc.Document{}}
}

// Unit returns the text of the unit the hit names, or "" if it cannot be read.
func (u *UnitTexts) Unit(meta DocMeta, addr string) string {
	key := meta.Accession
	if key == "" {
		key = meta.Path
	}
	if key == "" {
		return ""
	}
	doc, ok := u.docs[key]
	if !ok {
		loaded, err := u.src.Document(meta)
		if err != nil {
			// A document that cannot be read is not retried for every later hit from the
			// same file: the answer is "" and it stays "". A hit whose snippet failed is still
			// a hit, and the caller has the address to open it directly, so the failure is
			// reported as empty text rather than as an error that would drop the hit.
			u.docs[key] = nil
			return ""
		}
		doc = loaded
		u.docs[key] = doc
	}
	if doc == nil {
		return ""
	}
	return unitTextOf(doc, meta, addr)
}

// unitTextOf is the one join between an index address and a document. The address is resolved
// through the model rather than matched against a rebuilt section list, which is what makes a hit
// and the answer it opens agree: the same Find, the same scope, the same words, minus the markup
// an answer needs and a snippet does not.
func unitTextOf(doc *lexdoc.Document, meta DocMeta, addr string) string {
	switch addr {
	case "titel":
		return meta.Title
	case "":
		return doc.Content.PlainText()
	}
	if text, ok := doc.Content.PlainTextProvisions(lexdoc.ParseAddress(addr)); ok {
		return text
	}
	// The unit moved or the address did not survive the round-trip. Saying so with the
	// document's own text is more useful than an empty hit, but it has to be the document's
	// text and not a passage claimed to be the unit.
	return doc.Content.PlainText()
}

// centerWindow returns a window of text around the first term that appears in it.
func centerWindow(text string, terms []string, window int) string {
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= window {
		return strings.TrimSpace(text)
	}
	lowered := strings.ToLower(text)
	at := -1
	for _, t := range terms {
		if t == "" {
			continue
		}
		if i := strings.Index(lowered, strings.ToLower(t)); i >= 0 {
			at = len([]rune(lowered[:i]))
			break
		}
	}
	if at < 0 {
		at = 0
	}
	start := at - window/3
	if start < 0 {
		start = 0
	}
	end := start + window
	if end > len(runes) {
		end = len(runes)
	}
	prefix, suffix := "", ""
	if start > 0 {
		prefix = "…"
	}
	if end < len(runes) {
		suffix = "…"
	}
	return prefix + strings.TrimSpace(string(runes[start:end])) + suffix
}
