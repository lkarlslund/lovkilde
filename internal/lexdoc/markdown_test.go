package lexdoc

import (
	"strings"
	"testing"
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
	if !strings.Contains(got, "Loven gælder her\n\n1) Jf. lovbekendtgørelsens § 4.") {
		t.Errorf("the note is not separated as its own block:\n%s", got)
	}
}
