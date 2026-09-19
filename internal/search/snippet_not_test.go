package search

import (
	"strings"
	"testing"
)

// TestSnippetFromWithoutRefusesAFouledPassage is the reader-visible half of NOT. The index
// excludes the unit it matched; a reader is shown a passage cut out of whatever text the unit
// resolves to, and for a whole-document unit or an address that no longer resolves, that text is
// the document. So a hit can be legitimately kept by the index and still show the word the query
// forbade -- which is how "miljø NOT miljøbeskyttelse" answered with a paragraph about
// miljøbeskyttelse and looked like a broken operator.
func TestSnippetFromWithoutRefusesAFouledPassage(t *testing.T) {
	// One document text: the wanted word appears twice, the first time right beside the
	// forbidden word and the second time far from it. A 320-rune window around the first
	// occurrence is fouled, so only a cutter that tries the next occurrence can answer at all.
	fill := strings.Repeat("x ", 200)
	text := "miljø og miljøbeskyttelse " + fill + " miljø i ro og mag " + fill

	got := SnippetFromWithout(text, []string{"miljø"}, []string{"miljøbeskyttelse"}, 320)
	if got == "" {
		t.Fatal("a clean passage exists further into the text, so one should be shown")
	}
	if strings.Contains(strings.ToLower(got), "miljøbeskyttelse") {
		t.Errorf("the shown passage still carries the forbidden word: %q", got)
	}
	if !strings.Contains(got, "i ro og mag") {
		t.Errorf("the passage is not the clean occurrence of the word but somewhere else: %q", got)
	}

	// Same text, but now every occurrence of the wanted word sits inside a window that contains
	// the forbidden word: the honest answer is no passage at all.
	fouled := "miljø i miljøbeskyttelsessagen er der miljø og miljøbeskyttelse tæt på"
	if piece := SnippetFromWithout(fouled, []string{"miljø"}, []string{"miljøbeskyttelse"}, 320); piece != "" {
		t.Errorf("a text with nowhere clean returned %q, want nothing", piece)
	}

	// A forbidden word inside a longer compound is not the word: the index distinguishes them,
	// and so does the passage.
	compound := "miljø i barnepenge-betingelser " + fill + "miljø"
	if piece := SnippetFromWithout(compound, []string{"miljø"}, []string{"barn"}, 120); piece == "" {
		t.Error("the compound barnepenge was treated as the forbidden word barn")
	}
}

// TestExcludedWordsReadsEveryWayOfForbidding: the operator has four spellings, and the passage
// check has to know all of them or it silently stops applying.
func TestExcludedWordsReadsEveryWayOfForbidding(t *testing.T) {
	for _, query := range []string{
		"miljø NOT miljøbeskyttelse",
		"miljø AND NOT miljøbeskyttelse",
		"miljø -miljøbeskyttelse",
	} {
		got := ExcludedWords(query)
		if len(got) != 1 || got[0] != "miljøbeskyttelse" {
			t.Errorf("%q forbids %v, want [miljøbeskyttelse]", query, got)
		}
	}
	if got := ExcludedWords("miljø miljøbeskyttelse"); len(got) != 0 {
		t.Errorf("a plain query forbids %v, want nothing", got)
	}
}
