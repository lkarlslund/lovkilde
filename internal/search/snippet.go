package search

import (
	"strings"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

// DefaultSnippetRunes is how much text a hit shows around the match.
const DefaultSnippetRunes = 320

// Snippet renders the matched passage by reading the document out of the archive.
//
// This costs a document parse per hit, which is the price of the index storing no
// text: the alternative would duplicate 12.6 GB of legal text into a derived file that
// then needs its own invalidation story, and the archive is the product. The cost is
// bounded by the result limit, so a page of ten hits is ten parses of files that are
// already on disk.
//
// It returns "" rather than an error when the document cannot be read: a hit whose
// snippet failed to render is still a hit, and the caller has the address to open it
// directly. Silence would be the wrong trade here, so the caller is given "" and the
// command prints the address instead of invented text.
func Snippet(meta DocMeta, addr string, terms []string, window int) string {
	if meta.Path == "" {
		return ""
	}
	if window <= 0 {
		window = DefaultSnippetRunes
	}
	doc, err := lexdania.LoadWithBody(meta.Path)
	if err != nil {
		return ""
	}
	text := ""
	switch addr {
	case "titel":
		text = meta.Title
	case "":
		text = lexdania.DisplayText(doc.Root)
	default:
		// Match on the rendered address rather than on an index of sections: the
		// section list is rebuilt from the document every time, and keeping the
		// address as the only join key means snippet and lookup cannot drift apart.
		for _, s := range doc.Sections() {
			if AddressOf(s) == addr {
				text = s.Display()
				break
			}
		}
		if text == "" {
			// The section moved or the address did not survive the round-trip. Saying
			// so with the document's own text is more useful than an empty hit, but it
			// has to be the document's text and not a passage claimed to be the section.
			text = lexdania.DisplayText(doc.Root)
		}
	}
	return centerWindow(text, terms, window)
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
