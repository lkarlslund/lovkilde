package lexdoc

import (
	"strings"
	"testing"
	"unicode"
)

// The shape an answer is made of, pinned as a whole so a change to the renderer is a decision
// rather than a surprise. What matters here is that the parts are distinguishable, that the entry-
// into-force series is not filed among the law's own sections, and that no numbering is invented.
func TestMarkdownShape(t *testing.T) {
	got := sampleLaw().Markdown()
	want := `Lov om noget.

Indledning.

§ 1. Loven gælder her.

§ 2.

Første stykke.

1) Nr een.

2) Nr to.

Andet stykke.

Tredje stykke.

## chapter 1

*Hovedregler*

### § 3. Særlige tilfælde

Særlige tilfælde.
§ 3. Der må ikke ske udledning.

## Entry into force and transitional provisions

*Numbered in their own series: citing them as a section of the law would reach a different provision.*

§ 99. Ikrafttræden.

## Bilag 1

Bilagets eget nummer.
`
	if got != want {
		t.Errorf("rendered content differs:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// A provision answer carries only what was asked for.
func TestMarkdownProvisionsScopes(t *testing.T) {
	c := sampleLaw()

	got, ok := c.MarkdownProvisions(ParseAddress("§ 2"))
	if !ok {
		t.Fatal("§ 2 not rendered")
	}
	if !strings.Contains(got, "1) Nr een.") || strings.Contains(got, "Hovedregler") {
		t.Errorf("provision answer carries the wrong text:\n%s", got)
	}

	if _, ok := c.MarkdownProvisions(ParseAddress("§ 99")); ok {
		t.Error("an entry-into-force provision was found among the law's sections, which is the mistake this renderer exists to stop")
	}
	if got, ok := c.MarkdownProvisions(ParseAddress("bilag 1")); !ok || !strings.Contains(got, "Bilagets eget nummer.") {
		t.Errorf("annex not rendered: ok=%v\n%s", ok, got)
	}
}

// Content the model has no concept for stays visible, counted.
func TestMarkdownShowsUnmapped(t *testing.T) {
	c := sampleLaw()
	c.Unmapped = []Unmapped{{Concept: "henvisning til andet dokument", Text: "Jf. lov nr. 100."}}

	got := c.Markdown()
	if !strings.Contains(got, "Other content the model has no concept for (1 pieces)") ||
		!strings.Contains(got, "henvisning til andet dokument ×1") ||
		!strings.Contains(got, "Jf. lov nr. 100.") {
		t.Errorf("unmapped content is not shown:\n%s", got)
	}
}

// A footnote stays where the document put it, and the identifier the register invented for it
// stays out of the answer. The anchor ids in the archive are things like "Par1_Stk1_Not1" and uuids;
// showing them would put publisher data in front of a reader for no benefit, and the document's own
// marker ("1)") is already in the text.
func TestMarkdownKeepsFootnoteInPlace(t *testing.T) {
	c := sampleLaw()
	p := mustFindProvision(t, c, "§ 1")
	p.Segments = []Segment{{Text: "§ 1. Loven gælder her"}, {Text: "1) Jf. lovbekendtgørelsens § 4.", Note: "Par1_Stk1_Not1"}}

	got := c.Markdown()
	if strings.Contains(got, "Par1_Stk1_Not1") {
		t.Errorf("the rendering shows the register's internal anchor id:\n%s", got)
	}
	if !strings.Contains(got, "Loven gælder her1) Jf. lovbekendtgørelsens § 4.") {
		t.Errorf("the note is not where the document put it:\n%s", got)
	}
}

// An answer for a narrower address carries only that piece, with the section it belongs to above
// it: a caller quoting "§ 2, stk. 2" is not asking to re-search the section.
func TestMarkdownScopesToTheAddress(t *testing.T) {
	c := sampleLaw()
	two := mustFindProvision(t, c, "§ 2")
	two.Heading = "Bemærkninger"
	two.Printed = "§ 2."

	got, ok := c.MarkdownProvisions(ParseAddress("§ 2, stk. 2"))
	if !ok {
		t.Fatal("§ 2, stk. 2 not rendered")
	}
	for _, want := range []string{"### § 2. Bemærkninger", "Andet stykke."} {
		if !strings.Contains(got, want) {
			t.Errorf("stykke answer is missing %q:\n%s", want, got)
		}
	}
	for _, not := range []string{"Første stykke.", "Tredje stykke.", "Nr een."} {
		if strings.Contains(got, not) {
			t.Errorf("stykke answer carries more than was asked for (%q):\n%s", not, got)
		}
	}

	got, ok = c.MarkdownProvisions(ParseAddress("§ 2, stk. 1, nr. 2"))
	if !ok || !strings.Contains(got, "2) Nr to.") || strings.Contains(got, "1) Nr een.") {
		t.Errorf("item answer wrong: ok=%v\n%s", ok, got)
	}
}

// The plain form is the same walk with the markup left out, so an answer and a search snippet
// cannot disagree about what a provision says: they differ only in characters neither the law nor
// a reader needs. One walker is what makes that a fact rather than a hope.
func TestPlainRenderingKeepsTheWords(t *testing.T) {
	c := sampleLaw()

	md, plain := c.Markdown(), c.PlainText()
	for _, marker := range []string{"#", ">", "*"} {
		if strings.Contains(plain, marker) {
			t.Errorf("plain rendering carries markup %q:\n%s", marker, plain)
		}
	}
	if squashRendered(md) != squashRendered(plain) {
		t.Errorf("the two forms disagree about the words:\nplain:\n%s\nmarkdown:\n%s", plain, md)
	}

	// The same addresses resolve — and refuse — in both forms, so a hit whose snippet rendered
	// cannot be an address the answer refuses to open.
	for _, addr := range []string{"§ 1", "§ 2, stk. 1", "§ 2, stk. 1, nr. 1", "kapitel 1", "bilag 1", "indledningen", "titel", "§ 99", "bilag 7"} {
		_, mdOK := c.MarkdownProvisions(ParseAddress(addr))
		_, plainOK := c.PlainTextProvisions(ParseAddress(addr))
		if mdOK != plainOK {
			t.Errorf("%q resolves in one form only: markdown=%v plain=%v", addr, mdOK, plainOK)
		}
	}
}

// squashRendered leaves only the words a reader of either form sees: whitespace and the characters
// markdown uses for structure carry no content of their own.
func squashRendered(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '#' || r == '>' || r == '*' || unicode.IsSpace(r):
			return -1
		}
		return r
	}, s)
}
