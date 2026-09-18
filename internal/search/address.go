package search

import (
	"strconv"
	"strings"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

// AddressOf renders a section as the citation a caller can hand back to lookup.
//
// It is written to be the inverse of lexdania.ParseAddress rather than a prettier
// string: a hit whose address does not parse back to the same section is a hit the
// user cannot open, and that is the failure mode this whole file exists to avoid.
// The guard for that claim is round-trip over real corpus addresses in
// address_test.go, not a hand-written example.
//
// Sections carry their own fields rather than an address, because the register
// prints addresses in prose ("§ 267.") and the numbers live in localId/Explicatus.
// Hierarchy comes through Parent: "§ 5, stk. 2" needs the paragraph the stykke sits
// in, and a bilag subsection needs to say which bilag.
func AddressOf(s *lexdania.Section) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	switch s.Kind {
	case "kapitel":
		b.WriteString("kapitel " + s.Number)
	case "afsnit":
		b.WriteString("afsnit " + s.Number)
	case "bilag":
		b.WriteString("bilag " + s.Number)
	case "overskrift":
		// A rubrik heading is addressable as "overskriften for § N" only when it sits
		// above a paragraph; a stray heading gets its text as a locator instead, so a
		// hit still says something true rather than inventing an address.
		if p := parentParagraph(s); p != "" {
			b.WriteString("overskriften for § " + p)
		}
	case "paragraf":
		b.WriteString("§ " + s.Number + s.Litra)
	case "stk":
		if p := parentParagraph(s); p != "" {
			b.WriteString("§ " + p)
		}
		b.WriteString(", stk. " + strconv.Itoa(s.Stk))
	case "nr":
		if p := parentParagraph(s); p != "" {
			b.WriteString("§ " + p)
		}
		if s.Stk > 0 {
			b.WriteString(", stk. " + strconv.Itoa(s.Stk))
		}
		if s.Litra != "" {
			b.WriteString(" " + s.Litra)
		} else {
			b.WriteString(" nr. " + strconv.Itoa(s.Nr))
		}
	default:
		if s.RawNumber != "" {
			b.WriteString(strings.TrimRight(s.RawNumber, ". "))
		}
	}
	return b.String()
}

// parentParagraph walks up to the enclosing paragraph's number, "" when there is none.
//
// Only "paragraf" counts: climbing further would turn a stykke under a bilag into a
// citation against a section that does not exist.
func parentParagraph(s *lexdania.Section) string {
	for p := s.Parent; p != nil; p = p.Parent {
		if p.Kind == "paragraf" && p.Number != "" {
			return p.Number + p.Litra
		}
	}
	return ""
}
