package lexdania

import (
	"strings"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// The legacy HTML bodies the site serves print a provision's number as the first words of its
// text, inside a bold run, rather than in a separate element the way the register's own XML does.
// This is the shape measured in the corpus and reproduced here with nothing changed but the length
// of the prose: see the comment on promotePrintedParagraphNumbers.
const printedNumberBody = `<DIV CLASS=PARAGRAF ID="P6">
<P class="Paragraftekst"><B>&sect;&nbsp;6.</B> Arbejdsformidlingen kan benyttes af alle arbejdssøgende.</P>
<P class="Stk"><I>Stk.&nbsp;2.</I> Arbejdssøgende kan stå tilmeldt i den region, de vælger.</P>
</DIV>`

// printedSpaces compares printed text with the ordinary space in place of the non-breaking one the
// corpus prints, which is what the register's own XML does too: both spellings occur there, so the
// test says what the provision is named without depending on which one a given run happens to hold.
func printedSpaces(s string) string { return strings.ReplaceAll(s, "\u00a0", " ") }

func TestPrintedParagraphNumberBecomesTheMarker(t *testing.T) {
	body, err := ParseHTMLBody(printedNumberBody)
	if err != nil {
		t.Fatal(err)
	}
	// The text the page shows, captured before the body is attached. Promotion has to move the
	// number to where the XML body keeps it, not take it out of the document.
	before := strings.Join(strings.Fields(body.Root.PlainText()), " ")

	doc := &Document{Root: &Node{Name: "Dokument"}}
	doc.attachBody(body.Root, "test")

	if after := strings.Join(strings.Fields(body.Root.PlainText()), " "); after != before {
		t.Errorf("promotion changed the document's text:\n before %q\n  after %q", before, after)
	}

	m := doc.Model()
	refs := m.Content.Provisions()
	if len(refs) != 1 {
		t.Fatalf("the body holds %d provisions, want 1", len(refs))
	}
	p := refs[0].Provision
	if p.Number != "6" {
		t.Errorf("the provision reads its number as %q, want \"6\": the printed form is \"§ 6.\"", p.Number)
	}
	if got := printedSpaces(p.Printed); got != "§ 6." {
		t.Errorf("Printed is %q, want the form the page printed, \"§ 6.\"", p.Printed)
	}
	// The number belongs to the provision's text in both engines: the tree reads a section's text
	// out of its element, and the model's segments start where the element starts. So the claim
	// here is that the number is still printed once, and that the provision can now be asked for.
	text := printedSpaces(p.SpanText(lexdoc.Span{From: 0, To: len(p.Segments)}))
	if !strings.HasPrefix(strings.TrimSpace(text), "§ 6.") {
		t.Errorf("the provision's text no longer starts with its printed number: %q", text)
	}
	if strings.Count(text, "§ 6.") != 1 {
		t.Errorf("the number is not printed exactly once: %q", text)
	}
	if _, ok := m.Content.Find(lexdoc.ParseAddress("§ 6")); !ok {
		t.Error("§ 6 does not resolve in a document that prints § 6")
	}
}

// TestHTMLBodyProvisionsGetNumbers measures the shape on the mirror rather than on a fixture, and
// bounds it, because the whole claim of the promotion is corpus-wide: before it, 236 of the 750
// sampled documents that have provisions gave no provision a number at all.
//
// Documents whose XML carries its own body are excluded on purpose: they were numbered before and
// are measured by the parity gates. This counts only the sidecar bodies, whose numbering is what
// the promotion is for.
func TestHTMLBodyProvisionsGetNumbers(t *testing.T) {
	t.Parallel() // reads documents only; it overlaps the other long walk
	dir := corpusDir(t)
	var docs, provisions, unnumbered, shown int
	for _, f := range sampleFiles(t, dir, 3000) {
		doc, err := LoadWithBody(f)
		if err != nil || doc.Source != TextFromHTML {
			continue
		}
		docs++
		for _, ref := range doc.Model().Content.Provisions() {
			provisions++
			if ref.Provision.Number == "" {
				unnumbered++
				if shown < 6 {
					shown++
					txt := printedSpaces(ref.Provision.SpanText(lexdoc.Span{From: 0, To: len(ref.Provision.Segments)}))
					if len(txt) > 90 {
						txt = txt[:90]
					}
					t.Logf("unnumbered in %s: printed=%q heading=%q text=%q", f, ref.Provision.Printed, ref.Provision.Heading, txt)
				}
			}
		}
	}
	if docs < 50 {
		t.Fatalf("only %d HTML-bodied documents in the sample; the gate is not measuring anything", docs)
	}
	t.Logf("%d HTML-bodied documents, %d provisions, %d without a number (%.1f%%)",
		docs, provisions, unnumbered, 100*float64(unnumbered)/float64(max(provisions, 1)))
	// Measured on 2026-09-18 after the promotion: 171 of 7,805 provisions. The bound may only fall.
	// The residual is not the same shape as the one the promotion fixed, and the six examples logged
	// above are it: text that begins by citing the provision it goes on to rewrite -- "§ 12, stk. 2,
	// affattes således: …" in a consolidated act, "§ 10, stk. 1, nr. 4: Mundtlig eksamen …" in a
	// ministry note. Those are not provisions of the document with their own number, and naming them
	// by the section they quote would aim an amendment at the wrong text, so they stay unnumbered on
	// purpose and are counted here rather than argued away.
	//
	// The bound is a rate: the sample is a stride over the archive, so a crawl that adds files
	// samples different documents, and a pinned count drifted on 2026-10-02 (193 of 8,855, the same
	// 2.2 %) without a line of code changing.
	const unnumberedRate = 0.022
	if rate := float64(unnumbered) / float64(max(provisions, 1)); rate > unnumberedRate {
		t.Errorf("%d of %d provisions in HTML-bodied documents still have no number (%.2f%%), of %d documents; the bound measured on 2026-09-18 was %.1f%%",
			unnumbered, provisions, 100*rate, docs, 100*unnumberedRate)
	}
}

// TestPrintedNumberRefusesWhereThereIsNoNumber is the half that keeps the rule honest: a provision
// whose text happens to begin with a citation of another section is not named by that citation. The
// period is what tells the two apart -- "§ 6." names this provision, "§ 3 finder ikke anvendelse"
// names another one -- and the corpus prints a provision's own number with its period.
func TestPrintedNumberRefusesWhereThereIsNoNumber(t *testing.T) {
	for _, src := range []string{
		`<DIV CLASS=PARAGRAF ID="P9"><P class="Paragraftekst">Bestemmelsen i §&nbsp;3 finder ikke anvendelse.</P></DIV>`,
		`<DIV CLASS=PARAGRAF ID="P10"><P class="Paragraftekst">§&nbsp;3, stk.&nbsp;2 finder ikke anvendelse.</P></DIV>`,
	} {
		body, err := ParseHTMLBody(src)
		if err != nil {
			t.Fatal(err)
		}
		doc := &Document{Root: &Node{Name: "Dokument"}}
		doc.attachBody(body.Root, "test")
		refs := doc.Model().Content.Provisions()
		if len(refs) != 1 {
			t.Fatalf("the body holds %d provisions, want 1", len(refs))
		}
		if refs[0].Provision.Number != "" {
			t.Errorf("a provision with no printed number of its own was given %q", refs[0].Provision.Number)
		}
	}
}
