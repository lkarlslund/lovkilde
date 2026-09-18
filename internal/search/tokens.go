package search

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Token kinds the index stores.
//
// Two streams per token, not one. The raw stream answers exact-term and prefix
// queries ("miljøbeskyttelse" finds "miljøbeskyttelsesloven" only if the query is
// allowed to extend right, which it must for Danish compounds). The folded stream
// strips inflectional endings so a query for "pligt" finds "pligtige" without
// pulling in a stemmer built for English, which would fold Danish pairs that carry
// meaning apart. A token whose folded form equals its raw form is indexed once.
//
// Folding is deliberately conservative: only endings measured to help are listed,
// in internal/search/testdata/, and the fold is never used alone — a fold-only
// match ranks below a raw match so precision loss shows up as ranking, not as
// wrong answers.
// Two token streams per term, not one.
//
// The raw stream serves exact-term and right-extension queries: Danish builds almost
// everything as compounds, so a query for "miljøbeskyttelse" has to reach
// "miljøbeskyttelsesloven", and a prefix scan over a sorted term dictionary does
// that without indexing anything extra.
//
// The folded stream strips inflectional endings so "pligt" reaches "pligtige". It is
// an extra stream rather than a replacement because folding loses distinctions legal
// text cares about, and because the stemmer libraries reach for — Porter, and the one
// bundled with most search engines — is trained on English. A fold-only match ranks
// below a raw match, so precision loss shows up as worse ranking instead of wrong
// answers.
const (
	StreamRaw    = 0
	StreamFolded = 1
)

// aaFoldLower maps a word-initial "aa" to "å" in lower-case text.
//
// Danish was spelled with "Aa" for the letter Å until the 1948 reform, and the
// corpus runs back that far, so "Aa-sag" and "Å-sag", "Aalborg" and "Ålborg" are
// the same words indexed under two spellings. Folding on both sides of the query
// is what makes a modern-spelling query find pre-reform text. It is applied only
// at word start, where the ambiguity does not exist: no Danish word other than the
// letter itself alternates medially.
const aaFoldLower = "aa"

// FoldSuffixes are Danish inflectional endings, longest first so the first match
// wins. This is the candidate set, not a settled list: the measurement in
// measure_test.go reports how many distinct corpus tokens each ending actually
// merges, and what it merges into what. Endings that buy little or that merge two
// different legal concepts get pruned with their number in the report.
var FoldSuffixes = []string{
	"ernes", "erede", "endes", "ernes", "ende", "else",
	"ere", "ede", "et", "en", "er", "es", "te", "s",
}

// Tokenize splits text into indexable terms in lower case.
//
// Letters and digits are kept, everything else is a separator. Digits survive
// because Danish legal text cites amounts, dates and case numbers, and a query
// for "lov nr. 1399" is a citation, not prose.
func Tokenize(text string) []string {
	out := make([]string, 0, 32)
	start := -1
	for i, r := range text {
		keep := unicode.IsLetter(r) || unicode.IsDigit(r)
		if keep {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if tok := normalize(text[start:i]); tok != "" {
				out = append(out, tok)
			}
			start = -1
		}
	}
	if start >= 0 {
		if tok := normalize(text[start:]); tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

// normalize lower-cases a token and folds the pre-1948 "Aa" spelling to "å".
//
// It does not fold æ or ø, and it does not strip diacritics: those three are
// separate letters in the Danish alphabet with separate words riding on them
// ("tanke" and "tænke", "a" and "å" being the clearest), so a diacritic-insensitive
// index would return the wrong statute rather than a helpful extra one.
func normalize(token string) string {
	lowered := strings.ToLower(token)
	// "Aa" is one letter written twice, so the fold consumes both letters: "Aa" becomes
	// "å" and "Aalborg" becomes "Ålborg". Hyphenated pre-reform forms make the bare
	// case load-bearing — "Aa-sag" tokenises to "aa" and "sag", and leaving that "aa"
	// unfolded strands it from the modern "å" spelling.
	if strings.HasPrefix(lowered, aaFoldLower) {
		lowered = "å" + lowered[2:]
	}
	if minTokenLength(lowered) {
		return lowered
	}
	return ""
}

// minTokenLength keeps single-character tokens out of the index.
//
// A one-letter term in legal text is a list marker ("a)", "b)") or a section
// sub-part, and it accounts for a surprising share of postings for no retrieval
// value: a query for it matches nearly every document.
func minTokenLength(token string) bool {
	return utf8.RuneCountInString(token) >= 2
}

// Fold returns the stem used for the folded stream, or "" when no ending applies.
func Fold(token string) string {
	for _, ending := range FoldSuffixes {
		if len(token) > len(ending)+2 && strings.HasSuffix(token, ending) {
			return token[:len(token)-len(ending)]
		}
	}
	return ""
}
