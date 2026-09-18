package search

import (
	"strings"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

// DefaultSnippetRunes is how much text a hit shows around the match.
const DefaultSnippetRunes = 320

// Snippet renders the matched passage by reading the document out of the archive.
//
// This costs a document parse per hit, which is the price of the index storing no text: the
// alternative would duplicate 12.6 GB of legal text into a derived file that then needs its
// own invalidation story, and the archive is the product. The cost is bounded by the result
// limit, so a page of ten hits is ten parses of files that are already on disk. Where a
// caller needs the same text twice — to grade proximity and then to render the passage — it
// uses UnitTexts, which parses each document once per search instead of once per hit.
//
// It returns "" rather than an error when the document cannot be read: a hit whose snippet
// failed to render is still a hit, and the caller has the address to open it directly.
// Silence would be the wrong trade here, so the caller is given "" and the command prints the
// address instead of invented text.
func Snippet(meta DocMeta, addr string, terms []string, window int) string {
	return SnippetFrom(UnitText(meta, addr), terms, window)
}

// SnippetFrom centres a window on text that has already been read.
func SnippetFrom(text string, terms []string, window int) string {
	if text == "" {
		return ""
	}
	if window <= 0 {
		window = DefaultSnippetRunes
	}
	return centerWindow(text, terms, window)
}

// UnitText returns the whole provision a hit names, with no window applied.
//
// Proximity has to be measured on the provision rather than on a snippet, because a 320-rune
// window can cut one of the two words off and the grade would then report "could not see"
// about words that are plainly there.
func UnitText(meta DocMeta, addr string) string {
	if meta.Path == "" {
		return ""
	}
	doc, err := lexdania.LoadWithBody(meta.Path)
	if err != nil {
		return ""
	}
	return unitTextOf(doc, meta, addr)
}

// UnitTexts reads provision text for a batch of hits, parsing each document once.
//
// Its lifetime is one search: the cache is deliberately not shared between searches, because
// a document that changed on disk between two queries must not keep answering with the first
// reading, and because a bounded per-request cache needs no eviction policy to reason about.
type UnitTexts struct {
	docs map[string]*lexdania.Document
}

// NewUnitTexts starts a cache for one search.
func NewUnitTexts() *UnitTexts { return &UnitTexts{docs: map[string]*lexdania.Document{}} }

// Unit returns the text of the provision the hit names, or "" if it cannot be read.
func (u *UnitTexts) Unit(meta DocMeta, addr string) string {
	if meta.Path == "" {
		return ""
	}
	doc, ok := u.docs[meta.Path]
	if !ok {
		loaded, err := lexdania.LoadWithBody(meta.Path)
		if err != nil {
			// A document that fails to parse is not retried for every later hit from the
			// same file: the answer is "" and it stays "".
			u.docs[meta.Path] = nil
			return ""
		}
		doc = loaded
		u.docs[meta.Path] = doc
	}
	if doc == nil {
		return ""
	}
	return unitTextOf(doc, meta, addr)
}

// unitTextOf is the one join between an index address and a document: the rendered address is
// matched against the sections the document rebuilds, so snippet, grading and lookup all
// resolve a hit the same way and cannot drift apart.
func unitTextOf(doc *lexdania.Document, meta DocMeta, addr string) string {
	switch addr {
	case "titel":
		return meta.Title
	case "":
		return lexdania.DisplayText(doc.Root)
	}
	for _, s := range doc.Sections() {
		if AddressOf(s) == addr {
			return s.Display()
		}
	}
	// The section moved or the address did not survive the round-trip. Saying so with the
	// document's own text is more useful than an empty hit, but it has to be the document's
	// text and not a passage claimed to be the section.
	return lexdania.DisplayText(doc.Root)
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
