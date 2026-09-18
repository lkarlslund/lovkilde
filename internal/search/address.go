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
	// Every form below is a word, or a section sign, plus a number. The publisher does not
	// always give the element one: a bilag whose letter is printed only in the heading beside
	// it has Number "". Citing the bare word then produces an address that cannot be opened and
	// still looks like it points somewhere, which is worse than no address, because a search hit
	// carries it as its location. Over 1,000 mirror documents these two shapes were 179 of the
	// 250 addresses lookup could not open. The section keeps being indexed by its text and keeps
	// its heading; it is only no longer cited by a number the document never gave it.
	if s.Number == "" && (s.Kind == "kapitel" || s.Kind == "afsnit" || s.Kind == "bilag" || s.Kind == "paragraf") {
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
		if p, _ := parentParagraph(s); p != "" {
			b.WriteString("overskriften for § " + p)
		}
	case "paragraf":
		b.WriteString("§ " + s.Number + s.Litra)
		b.WriteString(ikraftMark(s.Appendix))
	case "stk":
		p, ikraft := parentParagraph(s)
		if p == "" {
			// Same rule as above: ", stk. 1" is not an address, it is half of one. The paragraph
			// above it is unnumbered, so the document has no name for where this text is.
			return ""
		}
		b.WriteString("§ " + p)
		b.WriteString(", stk. " + strconv.Itoa(s.Stk))
		b.WriteString(ikraftMark(ikraft))
	case "nr":
		p, ikraft := parentParagraph(s)
		if p == "" {
			return ""
		}
		b.WriteString("§ " + p)
		if s.Stk > 0 {
			b.WriteString(", stk. " + strconv.Itoa(s.Stk))
		}
		if s.Litra != "" {
			b.WriteString(" " + s.Litra)
		} else {
			b.WriteString(" nr. " + strconv.Itoa(s.Nr))
		}
		b.WriteString(ikraftMark(ikraft))
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
func parentParagraph(s *lexdania.Section) (number string, ikraft bool) {
	for p := s.Parent; p != nil; p = p.Parent {
		if p.Kind == "paragraf" && p.Number != "" {
			return p.Number + p.Litra, p.Appendix
		}
	}
	return "", false
}

// ikraftMark names the effectivity series when the paragraph cited belongs to it. The publisher
// prints those provisions with their own § 1, § 2, ... under "Ikrafttrædelses- og
// overgangsbestemmelser", and the model keeps them out of the law's numbering on purpose, so an
// unqualified "§ 2" for one of them names a provision that is not there. Over 1,000 mirror
// documents that was 71 of 261 cited addresses, every one an entry-into-force or transitional
// paragraph of a parliamentary stage document. Naming the series lets the address open, and
// ParseAddress accepts the marker only as a trailing locator, so amendment prose that mentions the
// series in passing still cannot aim an edit at it.
func ikraftMark(ikraft bool) string {
	if !ikraft {
		return ""
	}
	return ", ikrafttrædelsesbestemmelse"
}
