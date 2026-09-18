package lexdania

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"

	stdhtml "golang.org/x/net/html"
)

// The builder's contract is that a payload reads into the same shape the XML
// decoder produces, and that anything it cannot map is reported rather than lost.
// Both halves get tested: the first with small hand-written payloads for each
// construct the corpus contains, the second against real payloads where the claim
// under test is that no text disappeared.

func parseBodyFixture(t *testing.T, src string) *HTMLBody {
	body, err := ParseHTMLBody(src)
	if err != nil {
		t.Fatalf("ParseHTMLBody: %v", err)
	}
	if err := body.Root.checkInvariant("body"); err != nil {
		t.Fatalf("mixed-content invariant broken after parse: %v\ntree:\n%s", err, dumpNode(body.Root, ""))
	}
	return body
}

// dumpNode renders a tree compactly for failure output, showing runs between
// elements so an interleave violation is visible rather than inferred.
func dumpNode(n *Node, indent string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s<%s %v>", indent, n.Name, n.Attrs)
	for i, run := range n.Runs {
		if t := strings.TrimSpace(run); t != "" {
			fmt.Fprintf(&b, " %q", t)
		}
		if i < len(n.Elems) {
			b.WriteString("\n" + dumpNode(n.Elems[i], indent+"  "))
		}
	}
	return b.String()
}

func findElem(n *Node, name string) *Node {
	var found *Node
	n.Descendants(func(c *Node) {
		if found == nil && c.Name == name {
			found = c
		}
	})
	return found
}

func countElems(n *Node, name string) int {
	n2 := 0
	n.Descendants(func(c *Node) {
		if c.Name == name {
			n2++
		}
	})
	return n2
}

// elemNames lists the element names in document order, which is how nesting is
// asserted without spelling out a whole tree.
func elemNames(n *Node) []string {
	var out []string
	n.Descendants(func(c *Node) { out = append(out, c.Name) })
	return out
}

func TestParseHTMLBodyProvisionWithMarker(t *testing.T) {
	body := parseBodyFixture(t, `<p class="Paragraf"><span class="ParagrafNr">§ 1</span> Kongen fastsætter nærmere bestemmelser.</p>`)

	p := findElem(body.Root, "Paragraf")
	if p == nil {
		t.Fatalf("no Paragraf: %s", dumpNode(body.Root, ""))
	}
	marker := p.Elem("Explicatus")
	if marker == nil {
		t.Fatalf("marker did not become Explicatus: %s", dumpNode(p, ""))
	}
	if got := strings.TrimSpace(marker.Text()); got != "§ 1" {
		t.Errorf("marker text = %q, want %q", got, "§ 1")
	}
	// The provision's own text has to survive next to the marker, not replace it.
	if !strings.Contains(docText(p), "Kongen fastsætter") {
		t.Errorf("provision text lost: %q", docText(p))
	}
	// The marker appears once, in the marker element -- not also as loose text.
	if got := strings.Count(docText(p), "§ 1"); got != 1 {
		t.Errorf("marker text appears %d times: %q", got, docText(p))
	}
}

func TestParseHTMLBodyNumberedItemList(t *testing.T) {
	body := parseBodyFixture(t, `<p class="Liste1"><span class="Liste1Nr">1)</span> første punkt</p><p class="Liste2"><span class="Liste2Nr">a)</span> andet punkt</p>`)

	items := body.Root.FindNamed("Indentatio")
	if len(items) != 2 {
		t.Fatalf("got %d Indentatio, want 2: %v", len(items), elemNames(body.Root))
	}
	for _, item := range items {
		// Sections() only treats an Indentatio as a numbered item when formaInd says
		// so; without the attribute the list disappears from the section tree.
		if got := item.Attrs["formaInd"]; got != "Nummer" {
			t.Errorf("Indentatio formaInd = %q, want Nummer", got)
		}
		if item.Elem("Explicatus") == nil {
			t.Errorf("numbered item lost its marker: %s", dumpNode(item, ""))
		}
	}
}

func TestParseHTMLBodyCentreretRegion(t *testing.T) {
	src := `<p class="CentreretParagraf">først</p><hr class="ikraftstreg"><p class="CentreretParagraf">ikraft</p>`
	body := parseBodyFixture(t, src)

	names := elemNames(body.Root)
	if strings.Contains(strings.Join(names, " "), "IkraftCentreretParagraf IkraftCentreretParagraf") {
		t.Fatalf("both centred lines became appendix provisions: %v", names)
	}
	var first, second *Node
	for _, name := range []string{"Exitus", "IkraftCentreretParagraf"} {
		for _, c := range body.Root.Elems {
			if c.Name == name {
				if name == "Exitus" {
					first = c
				} else {
					second = c
				}
			}
		}
	}
	if first == nil || second == nil {
		t.Fatalf("centreret did not resolve both ways: %s", dumpNode(body.Root, ""))
	}
	if strings.TrimSpace(first.Text()) != "først" || strings.TrimSpace(second.Text()) != "ikraft" {
		t.Errorf("centred lines swapped: %q / %q", first.Text(), second.Text())
	}
}

func TestParseHTMLBodyFootnoteRelocation(t *testing.T) {
	// The payload puts the note text at the bottom; the canonical tree puts it where
	// the note is referenced.
	src := `<p class="Titel2">Bekendtgørelse<a class="FodnoteHenvisning" name="Henvisning_Not1" href="#Not1">1)</a></p>` +
		`<p class="Exitus2">Brug for note<a class="FodnoteHenvisning" name="Henvisning_Not2" href="#Not2">2)</a></p>` +
		`<p class="Fodnote"><span class="FodnoteNr" name="Not1">1)</span> Dette er noten.</p>`
	body := parseBodyFixture(t, src)

	if body.Root.Elems == nil {
		t.Fatal("empty tree")
	}
	if got := countElems(body.Root, "Nota"); got != 2 {
		t.Fatalf("got %d Nota, want 2 (anchor plus relocated text): %s", got, dumpNode(body.Root, ""))
	}
	// Note 1 has an anchor, so its text must sit inside the anchor under Titel2.
	var anchored *Node
	body.Root.Descendants(func(c *Node) {
		if c.Name == "Nota" && c.Attrs["id"] == "Not1" && strings.Contains(docText(c), "Dette er noten") {
			anchored = c
		}
	})
	if anchored == nil {
		t.Fatalf("note text was not moved to its reference: %s", dumpNode(body.Root, ""))
	}
	if !strings.Contains(anchored.Text(), "Dette er noten") {
		t.Errorf("relocated note text = %q", anchored.Text())
	}
	if body.Root.Elem("Nota") != nil {
		t.Errorf("empty note container left behind at document level: %s", dumpNode(body.Root, ""))
	}
	// Note 2 has text but no text arrived for it, which must be counted, not hidden.
	if body.UnmatchedNotes()+body.RelocatedNotes() != 1 {
		t.Errorf("relocated=%d unmatched=%d, want exactly one note handled", body.RelocatedNotes(), body.UnmatchedNotes())
	}
	if body.RelocatedNotes() != 1 {
		t.Errorf("RelocatedNotes = %d, want 1", body.RelocatedNotes())
	}
}

func TestParseHTMLBodyUnanchoredFootnoteIsReported(t *testing.T) {
	// A note whose number appears in the note but nowhere in the text has no
	// sentence to attach to. Leaving it at the bottom is the honest outcome, but it
	// has to be counted: silently keeping it is how a note ends up attributed to the
	// wrong provision.
	src := `<p class="Exitus2">Brug for note</p><p class="Fodnote"><a class="FodnoteNr" name="Not9" href="#Henvisning_Not9">9)</a> Noten hører til et sted, der ikke findes.</p>`
	body := parseBodyFixture(t, src)

	if body.UnmatchedNotes() != 1 {
		t.Errorf("UnmatchedNotes = %d, want 1 (relocated=%d)", body.UnmatchedNotes(), body.RelocatedNotes())
	}
	if !strings.Contains(treeText(body.Root), "Noten hører til et sted") {
		t.Errorf("unanchored note text was dropped: %s", dumpNode(body.Root, ""))
	}
}

func TestParseHTMLBodyFootnoteHrefIdentifiesTheNote(t *testing.T) {
	// Two conventions identify a note: modern payloads link by fragment, legacy ones
	// use a "Henvisning_" prefixed name. When both are present the link is what the
	// site uses, and the name is only its label.
	src := `<p class="Exitus2">Tekst<a class="FodnoteHenvisning" name="Henvisning_B" href="#A">1)</a></p>` +
		`<p class="Fodnote"><span class="FodnoteNr" name="A">1)</span> Notetekst.</p>`
	body := parseBodyFixture(t, src)

	var anchored *Node
	body.Root.Descendants(func(c *Node) {
		if c.Name == "Nota" && strings.Contains(docText(c), "Notetekst") {
			anchored = c
		}
	})
	if anchored == nil {
		t.Fatalf("note not relocated: %s", dumpNode(body.Root, ""))
	}
	if got := anchored.Attrs["id"]; got != "A" {
		t.Errorf("anchor id = %q, want A from the href fragment", got)
	}
	if body.RelocatedNotes() != 1 {
		t.Errorf("RelocatedNotes = %d, want 1", body.RelocatedNotes())
	}
}

func TestParseHTMLBodyCellsStayInTheirRow(t *testing.T) {
	// The row is the thing a payload leaves unclosed, so a second cell must not be
	// read as a new row: that turns a table's columns into separate rows.
	src := `<table><tr><td><div class="TabelTekst">kolonne 1</div><td><div class="TabelTekst">kolonne 2</div></table>`
	body := parseBodyFixture(t, src)

	table := body.Root.FindNamed("Table")
	if len(table) != 1 {
		t.Fatalf("no Table: %v", elemNames(body.Root))
	}
	rows := table[0].ElemsNamed("Tr")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %s", len(rows), dumpNode(table[0], ""))
	}
	if got := len(rows[0].ElemsNamed("Td")); got != 2 {
		t.Errorf("got %d cells in the row, want 2: %s", got, dumpNode(rows[0], ""))
	}
}

func TestParseHTMLBodyUnclosedElements(t *testing.T) {
	// Both shapes are present in the payloads: table rows without </tr> and inline
	// spans without </span>.
	// Measured shape: the td carries no class and the prose inside it does, which is
	// why td maps to Td and TabelTekst maps to Exitus rather than both becoming cells.
	rows := parseBodyFixture(t, `<table><tr><td><div class="TabelTekst">a</div><div class="TabelTekst">b<tr><td><div class="TabelTekst">c`)
	tables := rows.Root.FindNamed("Table")
	if len(tables) != 1 {
		t.Fatalf("no Table: %v", elemNames(rows.Root))
	}
	trs := tables[0].ElemsNamed("Tr")
	if len(trs) != 2 {
		t.Fatalf("got %d rows, want 2: %s", len(trs), dumpNode(tables[0], ""))
	}
	// One cell per row here: the row is what the payload left unclosed, and a row
	// break ends the open cell, so its two prose blocks stay together in it.
	if len(trs[0].ElemsNamed("Td")) != 1 || len(trs[1].ElemsNamed("Td")) != 1 {
		t.Errorf("cells mis-nested: %s", dumpNode(tables[0], ""))
	}
	if rows.ImplicitCloses() == 0 {
		t.Error("unclosed cells and rows were not recorded as implicitly closed")
	}

	spans := parseBodyFixture(t, `<p class="Exitus2"><span>indledningsvis<p class="Exitus2">næste afsnit</p></p>`)
	// An unclosed span must not swallow the following paragraph: they are siblings
	// when rendered, and a nested one changes which section the text belongs to.
	if got := countElems(spans.Root, "Exitus"); got != 2 {
		t.Fatalf("got %d Exitus, want 2 siblings: %s", got, dumpNode(spans.Root, ""))
	}
	if spans.Root.Elem("Exitus") != nil && len(spans.Root.Elem("Exitus").ElemsNamed("Exitus")) != 0 {
		// The paragraphs may only contain the span's Char, not another Exitus.
		for _, p := range spans.Root.Elems {
			if p.Name == "Exitus" && len(p.ElemsNamed("Exitus")) > 0 {
				t.Fatalf("paragraphs nested inside each other: %s", dumpNode(spans.Root, ""))
			}
		}
	}
}

func TestParseHTMLBodyVoidElementsDoNotNest(t *testing.T) {
	body := parseBodyFixture(t, `<p class="Exitus2">linje 1<br>linje 2</p><p class="Exitus2">efter</p>`)
	if got := countElems(body.Root, "Exitus"); got != 2 {
		t.Fatalf("got %d Exitus, want 2: <br> nested content: %s", got, dumpNode(body.Root, ""))
	}
	// A line break is a line break in canonical text too, so the two lines stay
	// separable instead of concatenating.
	first := body.Root.Elem("Exitus")
	if first == nil || !strings.Contains(docText(first), "\n") {
		t.Errorf("<br> did not become a newline: %q", docText(first))
	}
}

func TestParseHTMLBodySkippedSubtreeIsCounted(t *testing.T) {
	body := parseBodyFixture(t, `<div class="bjelke">Den fulde tekst</div><p class="Exitus2">brødtekst</p>`)
	all := treeText(body.Root)
	if strings.Contains(all, "Den fulde tekst") {
		t.Errorf("navigation text kept: %q", all)
	}
	if !strings.Contains(all, "brødtekst") {
		t.Errorf("document text lost: %q", all)
	}
	if len(body.Skips()) == 0 {
		t.Fatal("dropped subtree was not recorded with a reason")
	}
	total := 0
	for _, n := range body.Skips() {
		total += n
	}
	if total != 1 {
		t.Errorf("skip count = %d, want 1: %v", total, body.Skips())
	}
}

func TestParseHTMLBodyUnknownClassIsReported(t *testing.T) {
	// A class the table has never seen is the case where a new provision type would
	// silently become prose.
	body := parseBodyFixture(t, `<p class="HeltNyKlasse">nyttigt indhold</p>`)
	if body.UnmappedCount() != 1 {
		t.Fatalf("UnmappedCount = %d, want 1: %+v", body.UnmappedCount(), body.Unmapped())
	}
	u := body.Unmapped()[0]
	if u.Tag != "p" || u.Class != "HeltNyKlasse" {
		t.Errorf("unmapped entry = %+v, want p.HeltNyKlasse", u)
	}
	if !strings.Contains(treeText(body.Root), "nyttigt indhold") {
		t.Errorf("text of an unknown element was lost: %s", dumpNode(body.Root, ""))
	}
}

func TestParseHTMLBodySGMLAttributes(t *testing.T) {
	// The legacy generation quotes attribute values with doubled quotes, which no
	// HTML tokenizer reads. NAME=""P1"" is the one that matters: it is the only link
	// between the payload's element and the canonical id.
	body := parseBodyFixture(t, `<P CLASS=""PARA"" ID=""P1"">Gamle regler</P>`)
	if !body.SGML() {
		t.Error("SGML payload not identified")
	}
	p := findElem(body.Root, "Exitus")
	if p == nil {
		t.Fatalf("legacy PARA not mapped to prose: %s", dumpNode(body.Root, ""))
	}
	if got := p.Attrs["id"]; got != "P1" {
		t.Errorf("legacy id = %q, want P1: the pre-pass let the tokenizer stop at the inner quote", got)
	}
	if !strings.Contains(p.Text(), "Gamle regler") {
		t.Errorf("legacy text lost: %q", p.Text())
	}
}

func TestParseHTMLBodyNonSGMLPayloadUntouched(t *testing.T) {
	// The rewrite is opt-in per payload so that "the pre-pass did not touch this
	// document" is checkable for the overwhelming majority of the corpus.
	src := `<p class="Exitus2">citato: ""citato""</p>`
	body := parseBodyFixture(t, src)
	if body.SGML() {
		t.Fatal("a payload with no attribute-style doubled quotes reported SGML")
	}
	if !strings.Contains(docText(body.Root), `""citato""`) {
		t.Errorf("prose doubled quotes were rewritten: %q", docText(body.Root))
	}
}

func TestParseHTMLBodyStrayEndTagIgnored(t *testing.T) {
	body := parseBodyFixture(t, `<p class="Exitus2">tekst</p></p></div><p class="Exitus2">mere</p>`)
	if body.StrayEnds() < 2 {
		t.Errorf("StrayEnds = %d, want the payload's stray end tags counted", body.StrayEnds())
	}
	if got := countElems(body.Root, "Exitus"); got != 2 {
		t.Fatalf("got %d Exitus after stray end tags, want 2: %s", got, dumpNode(body.Root, ""))
	}
}

// allPayloadText concatenates every text token in a payload, with no drop logic at
// all, so it cannot agree with the builder about what was thrown away.
func allPayloadText(t *testing.T, src string) string {
	t.Helper()
	z := stdhtml.NewTokenizer(strings.NewReader(unquoteSGML(src)))
	var b strings.Builder
	for {
		switch tt := z.Next(); tt {
		case stdhtml.ErrorToken:
			if err := z.Err(); err != io.EOF {
				t.Fatalf("tokenizing payload: %v", err)
			}
			return b.String()
		case stdhtml.TextToken:
			b.Write(z.Text())
		}
	}
}

// corpusVisibleText re-tokenizes a payload independently of the builder's stack and
// returns the text a reader would see, minus subtrees the mapping drops on purpose.
// It shares only the mapping table with the builder, which is separately pinned by
// the vocabulary test, so agreement means the tree kept what the payload held.
func corpusVisibleText(t *testing.T, src string) string {
	t.Helper()
	z := stdhtml.NewTokenizer(strings.NewReader(unquoteSGML(src)))
	var out []string
	// Tag-aware, not a bare depth counter: these payloads contain stray end tags and
	// unclosed elements, and a counter drifts out of step with the document, which
	// hides text loss by claiming the text was inside something dropped.
	type skipFrame struct {
		tag  string
		drop bool
	}
	var skip []skipFrame
	for {
		switch tt := z.Next(); tt {
		case stdhtml.ErrorToken:
			if err := z.Err(); err != io.EOF {
				t.Fatalf("re-tokenizing payload: %v", err)
			}
			// Concatenated, not joined: the payload's own whitespace is the only
			// separator, and inventing one would hide a builder that glued two
			// words together.
			return squeezeText(strings.Join(out, ""))
		case stdhtml.TextToken:
			if len(skip) > 0 && skip[len(skip)-1].drop {
				continue
			}
			out = append(out, string(z.Text()))
		case stdhtml.StartTagToken, stdhtml.SelfClosingTagToken:
			tag, _ := z.TagName()
			name := strings.ToLower(string(tag))
			attrs := map[string]string{}
			for {
				k, v, more := z.TagAttr()
				attrs[strings.ToLower(string(k))] = string(v)
				if !more {
					break
				}
			}
			drop := classifyHTML(name, strings.TrimSpace(attrs["class"])).Role == roleSkip
			if len(skip) > 0 && skip[len(skip)-1].drop {
				drop = true
			}
			if !htmlVoidTags[name] && tt == stdhtml.StartTagToken {
				skip = append(skip, skipFrame{tag: name, drop: drop})
			}
		case stdhtml.EndTagToken:
			tag, _ := z.TagName()
			name := strings.ToLower(string(tag))
			for i := len(skip) - 1; i >= 0; i-- {
				if skip[i].tag == name {
					skip = skip[:i]
					break
				}
			}
		}
	}
}

// squeezeText normalises whitespace runs, but see stripText: element boundaries are
// where the tree adds line breaks the payload expressed as tags, so even that is
// too strict a comparison.
func squeezeText(s string) string { return strings.Join(strings.Fields(s), " ") }

// stripText removes whitespace entirely. Conservation is a claim about content: the
// payload's paragraph ends after "rette." and the next one starts with "Givet" with
// no space in either, and the tree is allowed to render that as a break.
func stripText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' && r != '\u00a0' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// docText renders a subtree in document order. Node.Text() is not this: it joins
// one node's own runs, which puts a paragraph's trailing text ahead of its marker's
// text. Comparing a tree against a payload needs the interleaved order, which is
// what the len(Runs)==len(Elems)+1 invariant exists to make possible.
func docText(n *Node) string {
	var b strings.Builder
	var walk func(cur *Node)
	walk = func(cur *Node) {
		for i, run := range cur.Runs {
			b.WriteString(run)
			if i < len(cur.Elems) {
				walk(cur.Elems[i])
			}
		}
	}
	walk(n)
	return b.String()
}

func treeText(n *Node) string { return squeezeText(docText(n)) }

// TestParseHTMLBodyFixtures is the conservation test: every word of a real payload
// that the mapping does not deliberately drop must be in the tree, in order, with
// the mixed-content invariant intact.
func TestParseHTMLBodyFixtures(t *testing.T) {
	files, err := filepath.Glob("testdata/payload-*.html")
	if err != nil || len(files) == 0 {
		t.Fatalf("no payload fixtures: %v", err)
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src := readFileForHTMLTest(t, path)
			body := parseBodyFixture(t, src)

			if n := body.UnmappedCount(); n != 0 {
				t.Errorf("%d elements carried a class outside the table: %+v", n, body.Unmapped())
			}
			// Conservation is two claims, and a relocated footnote makes them differ.
			// Nothing may be lost or invented, which is a statement about content and
			// holds regardless of where a note ended up. Nothing may be reordered,
			// which only holds where the builder moved nothing: canonical XML puts a
			// footnote at its reference, and the payload keeps the text at the bottom,
			// so a document with relocated notes differs in order by design.
			// Everything the payload holds must end up in the tree or in the recorded
			// drops, so this needs no drop rules of its own. It counts characters
			// rather than words and ignores whitespace entirely: adjacent elements are
			// glued in the payload ("1942Under") and separated in the tree, and neither
			// is wrong -- which is exactly why ordering gets its own check below.
			kept := treeText(body.Root) + strings.Join(body.SkippedText(), "")
			want, got := textChars(allPayloadText(t, src)), textChars(kept)
			if r, wa, ga, ok := firstRuneDiff(want, got); !ok {
				t.Errorf("content differs: %q appears %d times in the payload and %d times in the tree+drops",
					r, wa, ga)
			}
			if body.RelocatedNotes() == 0 {
				if want, got := stripText(corpusVisibleText(t, src)), stripText(treeText(body.Root)); want != got {
					i := 0
					for i < len(want) && i < len(got) && want[i] == got[i] {
						i++
					}
					t.Errorf("text conservation failed at byte %d of %d/%d\npayload: …%s…\ntree:    …%s…",
						i, len(want), len(got), sliceAround(want, i), sliceAround(got, i))
				}
			}
			t.Logf("elements=%d implicitCloses=%d strayEnds=%d notes relocated=%d unmatched=%d skips=%v",
				body.Elements(), body.ImplicitCloses(), body.StrayEnds(),
				body.RelocatedNotes(), body.UnmatchedNotes(), body.Skips())
		})
	}
}

// textChars counts visible characters, ignoring whitespace.
func textChars(s string) map[rune]int {
	m := map[rune]int{}
	for _, r := range s {
		if !unicode.IsSpace(r) {
			m[r]++
		}
	}
	return m
}

// firstRuneDiff reports a character whose count differs, and why the comparison
// failed. Equal totals with different counts are the interesting case, so the
// search covers both maps rather than only the keys of one.
func firstRuneDiff(a, b map[rune]int) (rune, int, int, bool) {
	runes := []rune{}
	for r := range a {
		runes = append(runes, r)
	}
	for r := range b {
		if _, ok := a[r]; !ok {
			runes = append(runes, r)
		}
	}
	sort.Slice(runes, func(i, j int) bool { return runes[i] < runes[j] })
	for _, r := range runes {
		if a[r] != b[r] {
			return r, a[r], b[r], false
		}
	}
	return 0, 0, 0, true
}

func sliceAround(s string, i int) string {
	lo := max(0, i-60)
	hi := min(len(s), i+90)
	// Runes, not bytes: the corpus is Danish, and a slice ending inside an ø prints
	// invalid UTF-8 -- and truncating a comparison string by bytes can also make it
	// fail to match.
	return strings.ToValidUTF8(s[lo:hi], "?")
}

func readFileForHTMLTest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return string(data)
}

// countNamelessNodes counts elements the builder gave no name. Such an element holds
// its text fine and is invisible to every walk that resolves elements by name, so it
// reads as lost text without any walk reporting a loss.
func countNamelessNodes(n *Node) int {
	if n == nil {
		return 0
	}
	out := 0
	if n.Name == "" {
		out++
	}
	for _, c := range n.Elems {
		out += countNamelessNodes(c)
	}
	return out
}

// A tag with neither a known class nor a known entry must cost a name and nothing
// else. The first corpus-wide census counted 12,371 elements named "" — the builder
// was constructing a node for whatever it could not classify.
func TestUnknownTagKeepsTextWithoutANamelessNode(t *testing.T) {
	body := parseBodyFixture(t, `<div>before<zzzunknown>inner text</zzzunknown>after</div>`)
	if n := countNamelessNodes(body.Root); n != 0 {
		t.Errorf("%d elements have no name, want 0\ntree:\n%s", n, dumpNode(body.Root, ""))
	}
	got := strings.Join(strings.Fields(body.Root.Text()), " ")
	if !strings.Contains(got, "inner text") || !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("text around an unknown tag was lost: %q", got)
	}
	if len(body.Unmapped()) == 0 {
		t.Error("an unknown tag has to be reported as unmapped, not quietly containerised")
	}
}

// Table sections are wrappers around rows: they must not become a level of the tree,
// because the canonical shape puts Tr directly under Table.
func TestTableSectionsStayTransparentToRows(t *testing.T) {
	body := parseBodyFixture(t, `<table class="MsoNormalTable"><thead><tr><td>head</td></tr></thead><tbody><tr><td>cell</td></tr></tbody></table>`)
	table := body.Root.FindNamed("Table")
	if len(table) != 1 {
		t.Fatalf("Table elements = %d, want 1\ntree:\n%s", len(table), dumpNode(body.Root, ""))
	}
	// Direct children only. FindNamed walks the whole subtree, which is the right
	// question for "is there a row anywhere in this table" and the wrong one here:
	// the claim under test is that thead and tbody add no level, and a recursive
	// search keeps passing when the rows have been pushed a level deeper or dropped.
	rows := table[0].ElemsNamed("Tr")
	if len(rows) != 2 {
		t.Errorf("Tr as a direct child of Table = %d, want 2\ntree:\n%s", len(rows), dumpNode(table[0], ""))
	}
	if got := strings.TrimSpace(table[0].PlainText()); !strings.Contains(got, "head") || !strings.Contains(got, "cell") {
		t.Errorf("a table section dropped the text of its rows: %q", got)
	}
	if n := countNamelessNodes(body.Root); n != 0 {
		t.Errorf("%d elements have no name, want 0\ntree:\n%s", n, dumpNode(body.Root, ""))
	}
}
