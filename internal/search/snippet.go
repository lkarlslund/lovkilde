package search

import (
	"strings"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// DefaultSnippetRunes is how much text a hit shows around the match.
const DefaultSnippetRunes = 320

// Documents reads the document a hit names. The index deliberately stores no text, so somebody
// has to: serving passes the compiled artifact (internal/compiled.Cache), which reads one document
// without parsing anything; tests that measure the publisher's files pass
// a reader that parses them.
type Documents interface {
	Document(meta DocMeta) (*lexdoc.Document, error)
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

// SnippetFromWithout cuts a passage that contains none of the forbidden words, and returns ""
// when the text offers no such passage.
//
// It exists because a hit is matched against a unit while a reader is shown a passage, and the two
// are not always the same span: a unit whose address no longer resolves is rendered from the
// document, and a whole-document unit is the document. The index then correctly keeps a unit that
// lacks the forbidden word while the page displays a passage that has it, which is how a search
// for "miljø NOT miljøbeskyttelse" answered with a paragraph about miljøbeskyttelse. The reader
// cannot see which span was excluded, so the only honest fix is to refuse to show the passage.
//
// A forbidden word counts when it begins a word, not when it sits inside a longer one: the index
// itself distinguishes "barn" from "barnepenge", and a reader who excluded the first is not
// being answered with the second, but neither is it what they asked to be rid of.
func SnippetFromWithout(text string, terms, forbidden []string, window int) string {
	if text == "" {
		return ""
	}
	if window <= 0 {
		window = DefaultSnippetRunes
	}
	if len(forbidden) == 0 {
		return centerWindow(text, terms, window)
	}
	original := []rune(text)
	if len(original) <= window {
		if containsAnyWord(text, forbidden) {
			return ""
		}
		return strings.TrimSpace(text)
	}
	for _, start := range candidateStarts([]rune(strings.ToLower(text)), terms, window) {
		end := start + window
		if end > len(original) {
			end = len(original)
		}
		piece := string(original[start:end])
		if containsAnyWord(piece, forbidden) {
			continue
		}
		prefix, suffix := "", ""
		if start > 0 {
			prefix = "…"
		}
		if end < len(original) {
			suffix = "…"
		}
		return prefix + strings.TrimSpace(piece) + suffix
	}
	return ""
}

// candidateStarts lists where a window could begin, nearest a matched word first, so the reader is
// still shown the text they asked for rather than the first clean corner of a document.
//
// Lowercasing Danish text changes no rune counts -- æ, ø and å have no shorter form -- so an offset
// found in the lowered text indexes the original rune slice directly.
func candidateStarts(lowered []rune, terms []string, window int) []int {
	hay := string(lowered)
	out := make([]int, 0, 8)
	seen := map[int]bool{}
	for _, term := range terms {
		needle := strings.ToLower(term)
		if needle == "" {
			continue
		}
		from := 0
		for from <= len(hay) {
			i := strings.Index(hay[from:], needle)
			if i < 0 {
				break
			}
			at := from + i
			start := len([]rune(hay[:at])) - window/3
			if start < 0 {
				start = 0
			}
			if !seen[start] {
				seen[start] = true
				out = append(out, start)
			}
			from = at + len(needle)
			if len(out) >= 8 {
				return out
			}
		}
	}
	if !seen[0] {
		out = append([]int{0}, out...)
	}
	return out
}

// containsAnyWord reports whether any forbidden word begins a word inside the passage.
func containsAnyWord(piece string, forbidden []string) bool {
	lowered := strings.ToLower(piece)
	for _, word := range forbidden {
		needle := strings.ToLower(strings.TrimSpace(word))
		if needle == "" {
			continue
		}
		at := 0
		for at < len(lowered) {
			i := strings.Index(lowered[at:], needle)
			if i < 0 {
				break
			}
			pos := at + i
			// The passage's own start counts as a word boundary; inside the passage the
			// previous rune has to be a non-letter for the word to be a word.
			if pos == 0 || !isWordRune(rune(lowered[pos-1])) {
				return true
			}
			at = pos + len(needle)
		}
	}
	return false
}

func isWordRune(r rune) bool {
	switch r {
	case 'æ', 'ø', 'å':
		return true
	}
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}
