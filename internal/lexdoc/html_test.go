package lexdoc

import (
	"html"
	"regexp"
	"strings"
	"testing"
)

// The claim this file's tests exist for: HTML is a third skin on one walker, so it cannot say a
// different thing from the text an answer is served in. Everything else in the file checks that
// the skin holds its shape — escapes what looks like markup, and puts anchors where the table of
// contents says they are.

func TestHTMLSaysWhatPlainTextSays(t *testing.T) {
	c := sampleLaw()
	plain := words(c.PlainText())
	got := words(plainTextOf(c.HTML()))
	if len(plain) == 0 || len(got) == 0 {
		t.Fatalf("one side rendered no words at all: plain %d, html %d", len(plain), len(got))
	}
	if strings.Join(plain, " ") != strings.Join(got, " ") {
		// Naming the first divergence rather than dumping both documents: a renderer change that
		// drops or reorders words is one defect, and its position identifies it.
		for i := range got {
			if i >= len(plain) || plain[i] != got[i] {
				at := i
				if at > len(plain)-1 {
					at = len(plain) - 1
				}
				t.Errorf("HTML and plain text diverge at word %d: plain has %q, HTML has %q\nplain: %s\nhtml:  %s",
					i+1, nth(plain, at), nth(got, i), strings.Join(plain, " "), strings.Join(got, " "))
				break
			}
		}
	}
}

// A law is not a place for markup: the register's own text contains angle brackets in tables and
// in quoted provisions, and the page that renders them as tags has been written by the document.
func TestHTMLEscapesTextThatLooksLikeMarkup(t *testing.T) {
	provision := &Provision{Number: "1", Printed: "§ 1."}
	provision.Segments = []Segment{{Text: `Tekst med <script>alert("i loven")</script> og & ampersand.`}}
	c := &Content{}
	c.Order = []Node{{Provision: provision}}
	out := c.HTML()
	if strings.Contains(out, "<script") {
		t.Errorf("the document wrote a tag into the page: %s", out)
	}
	for _, want := range []string{"&lt;script&gt;", "&amp;"} {
		if !strings.Contains(out, want) {
			t.Errorf("escaped form %q missing from: %s", want, out)
		}
	}
}

// An anchor the table of contents promises but the render did not write is a link that goes
// nowhere, so both sides are compared against each other rather than against a list someone typed.
func TestHTMLAnchorsAreWhereContentsSays(t *testing.T) {
	c := sampleLaw()
	body := c.HTML()
	ids := map[string]bool{}
	for _, m := range idPattern.FindAllStringSubmatch(body, -1) {
		ids[m[1]] = true
	}
	var missing []string
	for _, line := range c.Contents() {
		if line.ID == "" {
			continue
		}
		if !ids[line.ID] {
			missing = append(missing, line.ID+" ("+line.Label+")")
		}
	}
	if len(missing) > 0 {
		t.Errorf("the table of contents links to %d anchors the render did not write: %s\nrendered: %s",
			len(missing), strings.Join(missing, ", "), body)
	}
}

// The address a search result carries has to become the anchor the page holds. Both come from the
// model, and neither is allowed to guess.
func TestAnchorForNamesWhatTheRenderWrote(t *testing.T) {
	c := sampleLaw()
	body := c.HTML()
	for _, tc := range []struct{ address, want string }{
		{"§ 1", "p-1"},
		{"§ 2, stk. 2", "p-2"},
		{"kapitel 1", "kapitel-1"},
		{"bilag 1", "bilag-1"},
		{"§ 99, ikrafttrædelsesbestemmelse", "ikraft-99"},
	} {
		got := c.AnchorFor(ParseAddress(tc.address))
		if got != tc.want {
			t.Errorf("AnchorFor(%q) = %q, want %q", tc.address, got, tc.want)
		}
		if got != "" && !strings.Contains(body, `id="`+got+`"`) {
			t.Errorf("AnchorFor(%q) = %q, which the render never wrote", tc.address, got)
		}
	}
	if got := c.AnchorFor(ParseAddress("§ 404")); got != "" {
		t.Errorf("an unresolvable address produced anchor %q, which would be a link to nothing", got)
	}
	if got := c.AnchorFor(ParseAddress("")); got != "" {
		t.Errorf("an empty address produced anchor %q", got)
	}
}

// A provision with no rubric gets no heading, and still needs an anchor: the index cites "§ 2" for
// exactly those provisions, because the register prints the number in the body and nothing above it.
func TestHTMLAnchorsCoverProvisionsWithoutARubric(t *testing.T) {
	body := sampleLaw().HTML()
	if !strings.Contains(body, `id="p-1"`) {
		t.Errorf("the rubricless § 1 has no anchor:\n%s", body)
	}
	if !strings.Contains(body, `id="p-2"`) {
		t.Errorf("the rubricless § 2 has no anchor:\n%s", body)
	}
}

var idPattern = regexp.MustCompile(`(?s)<(?:section|h[1-6])[^>]*\bid="([^"]+)"`)

// plainTextOf strips the markup back out of the render. It is deliberately blunt — every tag and
// every attribute goes, then the entities the escaper added come back — because the test wants the
// words, and a smarter parser would be a second renderer to keep correct.
func plainTextOf(markup string) string {
	stripped := regexp.MustCompile(`(?s)<[^>]*>`).ReplaceAllString(markup, "\n")
	return html.UnescapeString(stripped)
}

func words(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == '\v' || r == '\f' || r == '\u00a0'
	})
}

func nth(xs []string, i int) string {
	if i < 0 || i >= len(xs) {
		return "(end)"
	}
	return xs[i]
}

// The mirror contains provisions and divisions the publisher left unnumbered. They are a small
// share of the corpus and a complete share of the trouble: an anchor built from "there is no
// number" is either empty — a contents link to "#", which a real crawl found twice — or the bare
// kind word, which two unnumbered divisions would then share, sending a reader to whichever the
// walk reached first. This test is the case the fixture's numbered provisions could not show.
func TestHTMLAnchorsStayUniqueWhenTheDocumentGaveNoNumbers(t *testing.T) {
	unnumbered := &Content{
		Order: []Node{
			{Part: &Part{Kind: PartAfsnit, Printed: "Afsnit uden nummer", Order: []Node{
				{Provision: &Provision{Heading: "Første uden nummer", Segments: []Segment{{Text: "Første utitlede bestemmelse."}}}},
				{Provision: &Provision{Heading: "Anden uden nummer", Segments: []Segment{{Text: "Anden utitlede bestemmelse."}}}},
			}}},
			{Part: &Part{Kind: PartAfsnit, Printed: "Endnu et afsnit uden nummer", Order: []Node{
				{Provision: &Provision{Segments: []Segment{{Text: "Tredje utitlede bestemmelse, helt uden rubrik."}}}},
			}}},
		},
	}
	html := unnumbered.HTML()
	ids := map[string]int{}
	for _, m := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		ids[m[1]]++
	}
	if len(ids) == 0 {
		t.Fatalf("a document of unnumbered parts and provisions got no anchors at all:\n%s", html)
	}
	for id, n := range ids {
		if n > 1 {
			t.Errorf("anchor %q is written %d times; the second one wins the fragment and the other is unreachable", id, n)
		}
	}
	contents := unnumbered.Contents()
	// Two divisions and three provisions, all of them unnumbered: every one of them is a place a
	// reader should be able to link to.
	if len(contents) != 5 {
		t.Fatalf("contents listed %d lines, want 5: %+v", len(contents), contents)
	}
	for _, line := range contents {
		if line.ID == "" {
			t.Errorf("contents advertises %q with no anchor to link to", line.Label)
			continue
		}
		if ids[line.ID] != 1 {
			t.Errorf("contents links to %q, which the render wrote %d times", line.ID, ids[line.ID])
		}
		if strings.TrimSpace(line.Label) == "" || line.Label == "§" {
			t.Errorf("contents line for an unnumbered provision is labelled %q", line.Label)
		}
	}
}
