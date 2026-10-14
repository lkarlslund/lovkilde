package lexdania

import (
	"strings"
	"testing"

	"github.com/lkarlslund/lovkilde/internal/lexdoc"
)

// Rendering real documents as markdown, to check that the rendering loses nothing the model holds
// and that the structure the model knows reaches the reader. The model-side test pins the shape on
// a hand-built document; only the register can say whether a chapter heading, a rubric and an
// annex end up where a reader will find them.
func TestModelMarkdownOnCorpus(t *testing.T) {
	dir := corpusDir(t)
	files := sampleFiles(t, dir, 400)

	var docs, provisions, parts, printedLabels, entryProvisions int
	for _, f := range files {
		doc, err := Load(f)
		if err != nil {
			t.Logf("parse failed (the crawl may be mid-write): %v", err)
			continue
		}
		m := doc.Model()
		rendered := lexdoc.MatchText(m.Content.Markdown())
		docs++

		// Nothing the model can read may disappear from what the reader is shown.
		for _, ref := range m.Content.Provisions() {
			p := ref.Provision
			provisions++
			text := lexdoc.MatchText(p.Text())
			if text == "" {
				continue
			}
			if !strings.Contains(rendered, text) {
				t.Errorf("%s: § %s is missing from the rendering:\n%s", f, p.Address(), firstRunes(m.Content.Markdown(), 1200))
				break
			}
		}

		// A division the document labelled must be findable by that label, not only by the
		// provision numbers it happens to contain.
		for _, part := range m.Content.Parts() {
			parts++
			if label := strings.TrimSpace(part.Printed); label != "" {
				printedLabels++
				if !strings.Contains(rendered, lexdoc.MatchText(label)) {
					t.Errorf("%s: the document prints %q, the rendering does not show it", f, label)
				}
			}
		}

		// The entry-into-force series has to sit under its own heading. Filed among the law's
		// sections it reads as if § 1 of the law were a transitional rule, which is the mistake
		// the flat section list made.
		if len(m.Content.EntryIntoForce) > 0 {
			headingAt := strings.Index(rendered, lexdoc.MatchText("Entry into force and transitional provisions"))
			if headingAt < 0 {
				t.Fatalf("%s: no entry-into-force heading although the model holds %d provisions there",
					f, len(m.Content.EntryIntoForce))
			}
			for i := range m.Content.EntryIntoForce {
				p := &m.Content.EntryIntoForce[i]
				entryProvisions++
				text := lexdoc.MatchText(p.Text())
				if text == "" {
					continue
				}
				where := strings.Index(rendered, text)
				if where < 0 {
					t.Errorf("%s: entry-into-force provision %s is missing from the rendering", f, p.Address())
					continue
				}
				if where < headingAt {
					t.Errorf("%s: entry-into-force provision %s is filed among the law's own sections", f, p.Address())
				}
			}
		}
	}

	t.Logf("rendered %d documents, %d provisions, %d divisions (%d printed a label), %d entry-into-force provisions",
		docs, provisions, parts, printedLabels, entryProvisions)
	if docs == 0 {
		t.Skip("no corpus samples")
	}
}

func firstRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
