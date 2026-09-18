package lexdania

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"

	stdhtml "golang.org/x/net/html"
)

// This is the acceptance measurement for the payload reader: the paired corpus has
// both the canonical XML and the payload the site rendered from it, so the tree
// built from the payload can be checked against the tree that is authoritative.
//
// The claim is deliberately the one that can be falsified. "The payload contains
// this text" was already measured before any code was written; what matters is
// whether the *builder* loses text that the payload had, so every provision whose
// text is missing from the tree is asked whether the raw payload had it, and a
// missing probe that the payload did have is reported as a loss rather than
// explained away.

// fidelityProbes is the baseline measurement, checked in because the payloads it was
// taken from are gitignored: without it the claim is an anecdote. See pairedFidelity.
const fidelityBaselineFile = "testdata/paired-fidelity.txt"

// provisionOrder lists the structural elements whose text a reader must reproduce,
// in a fixed order so a failure report reads the same twice. These are what
// Sections() turns into provisions, so losing one is losing a § number.
var provisionOrder = []string{"Paragraf", "Stk", "Indledning", "Afsnit", "Kapitel", "Bilag"}

// comparisonText normalises for the differences that are not losses: the typesetting
// marks a drafter would not reproduce (measured: soft hyphen in 7,564 archive
// documents), the non-breaking spaces HTML entities decode to, and whitespace, which
// element boundaries make incomparable.
func comparisonText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\u00ad', '\u200b', '\u200c', '\u200d', '\ufeff':
			continue
		case '\u00a0':
			r = ' '
		}
		if unicode.IsSpace(r) {
			r = ' '
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// nodeComparisonText is a subtree's text in comparison form, with footnote material
// left out on both sides of the comparison. Notes move: the payload keeps their text
// at the bottom, the canonical tree keeps it at the reference, and the builder puts
// it at the reference too, so including them would compare two different orderings.
func nodeComparisonText(n *Node) string {
	var parts []string
	var walk func(cur *Node)
	walk = func(cur *Node) {
		if cur.Name == "Nota" {
			return
		}
		for i, run := range cur.Runs {
			parts = append(parts, run)
			if i < len(cur.Elems) {
				walk(cur.Elems[i])
			}
		}
	}
	walk(n)
	return comparisonText(strings.Join(parts, " "))
}

// payloadDir locates the pilot payloads. They are gitignored, so the measurement
// runs on a machine holding them and says so elsewhere.
func payloadDir(t *testing.T) string {
	dir := os.Getenv("RETSIN_TEST_BODIES")
	if dir == "" {
		dir = filepath.Join("..", "..", "data", "pilot-bodies")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("no payload corpus at %s; set RETSIN_TEST_BODIES to measure fidelity", dir)
	}
	return dir
}

// pairedPayload is the shape of a stored sidecar: the site returns a list, and only
// the markup matters here.
type pairedPayload struct {
	DocumentHTML string `json:"documentHtml"`
}

func payloadHTML(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading payload: %v", err)
	}
	var list []pairedPayload
	if err := json.Unmarshal(raw, &list); err != nil {
		var one pairedPayload
		if err2 := json.Unmarshal(raw, &one); err2 != nil {
			t.Fatalf("decoding payload: %v / %v", err, err2)
		}
		return one.DocumentHTML
	}
	var b strings.Builder
	for _, d := range list {
		b.WriteString(d.DocumentHTML)
	}
	return b.String()
}

// fidelityCounts is one run's result.
type fidelityCounts struct {
	Compared     int
	Verbatim     int
	Whitespace   int // same text once whitespace is collapsed
	Spanning     int // heading whose body is a sibling: the probe cannot span it
	Table        int // inside a document whose tables the reader does not yet rebuild
	BuilderLoss  int // the payload had the text and the tree does not: a bug
	NotInPayload int // the payload itself lacked it: not the reader's doing
	Documents    int
}

func (c fidelityCounts) String() string {
	lines := []string{
		"documents=" + strconv.Itoa(c.Documents),
		"provisions_compared=" + strconv.Itoa(c.Compared),
		"verbatim=" + strconv.Itoa(c.Verbatim),
		"whitespace_or_entity_only=" + strconv.Itoa(c.Whitespace),
		"heading_spanning_probe=" + strconv.Itoa(c.Spanning),
		"table_bearing_document=" + strconv.Itoa(c.Table),
		"lost_by_builder=" + strconv.Itoa(c.BuilderLoss),
		"absent_from_payload=" + strconv.Itoa(c.NotInPayload),
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

// measurePairedFidelity compares every provision in every paired document.
func measurePairedFidelity(t *testing.T) fidelityCounts {
	t.Helper()
	dir := payloadDir(t)
	archive := filepath.Join("..", "..", "data", "xml")
	// Paired means both files are present. Deriving it by intersection rather than
	// trusting a list matters here: the legacy payloads and the legacy XML corpus do
	// not overlap at all, so a list of legacy *targets* is not a list of pairings.
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Skipf("no payloads to measure: %v", err)
	}
	sort.Strings(paths)

	var counts fidelityCounts
	for _, payloadPath := range paths {
		stem := strings.TrimSuffix(filepath.Base(payloadPath), ".json")
		rel := stem + ".xml"
		for i, r := range stem {
			if r == '_' {
				rel = stem[:i] + "/" + strings.Replace(stem[i+1:], "_", "/", 1) + ".xml"
				break
			}
		}
		xmlRaw, err := os.ReadFile(filepath.Join(archive, rel))
		if err != nil {
			continue // payload without canonical XML: nothing to compare against
		}
		doc, err := Parse(xmlRaw)
		if err != nil {
			continue
		}
		html := payloadHTML(t, payloadPath)
		body, err := ParseHTMLBody(html)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		counts.Documents++

		treeText := nodeComparisonText(body.Root)
		// Whitespace-free form as well: link and entity boundaries split words in the
		// payload exactly where they do not in the XML.
		treeNoSpace := strings.Join(strings.Fields(treeText), "")

		payloadNoSpace := stripAllSpace(comparisonText(allPayloadText(t, html)))

		for _, name := range provisionOrder {
			for _, prov := range doc.Root.FindNamed(name) {
				text := nodeComparisonText(prov)
				if len(text) < 40 {
					continue
				}
				counts.Compared++
				probe := text
				if len(probe) > 240 {
					// Cut on runes: a byte cut can land inside an å or ø, and an
					// incomplete rune never matches anything, which would be reported
					// as text the builder lost.
					probe = cutRunes(probe, 160)
				}
				probeNoSpace := strings.Join(strings.Fields(probe), "")
				switch {
				case strings.Contains(treeText, probe):
					counts.Verbatim++
				case strings.Contains(treeNoSpace, probeNoSpace):
					counts.Whitespace++
				case !strings.Contains(payloadNoSpace, probeNoSpace):
					// Not the reader's fault: the text never arrived. Reported because
					// it bounds what the payload can ever reconstruct.
					counts.NotInPayload++
				case strings.HasPrefix(text, "Kapitel "), strings.HasPrefix(text, "Afsnit "),
					strings.HasPrefix(text, "Bilag "):
					counts.Spanning++
				case strings.Contains(html, "<table"):
					counts.Table++
				default:
					counts.BuilderLoss++
					if counts.BuilderLoss <= 8 {
						t.Errorf("%s: %s id=%s lost, probe %q is in the payload but not in the tree",
							rel, name, prov.ID(), cutRunes(probe, 90))
					}
				}
			}
		}
	}
	return counts
}

func TestPairedPayloadFidelity(t *testing.T) {
	got := measurePairedFidelity(t)
	t.Logf("\n%s", got)

	// A measurement over zero documents passes everything. The payloads are optional,
	// but once they are here, finding none of them paired is a broken measurement and
	// not a result.
	if got.Documents == 0 {
		t.Fatal("found payloads but no paired documents: the fidelity measurement is vacuous")
	}
	if got.Compared == 0 {
		t.Fatal("paired documents yielded no provisions to compare")
	}
	if got.BuilderLoss != 0 {
		t.Errorf("%d provisions' text is in the payload but not in the built tree", got.BuilderLoss)
	}
	found := got.Verbatim + got.Whitespace
	if got.Compared > 0 {
		t.Logf("recoverable %.2f%% (verbatim %.2f%% + whitespace-only %.2f%%)",
			100*float64(found+got.Spanning+got.Table)/float64(got.Compared),
			100*float64(got.Verbatim)/float64(got.Compared),
			100*float64(got.Whitespace)/float64(got.Compared))
	}

	raw, err := os.ReadFile(fidelityBaselineFile)
	if err != nil {
		t.Logf("no baseline at %s: write one from this run to pin the measurement", fidelityBaselineFile)
		return
	}
	want := strings.TrimSpace(string(raw))
	if strings.TrimSpace(got.String()) != want {
		t.Errorf("measurement differs from the pinned baseline\nbaseline:\n%s\nnow:\n%s", want, got)
	}
}

// TestPayloadCorpusConservation is the validator for documents that cannot be
// aligned: the legacy payloads have no canonical XML counterpart at all, so the
// claim being checked is that reading a payload neither loses text nor invents any,
// and that every class it contains is one the mapping table can name.
func TestPayloadCorpusConservation(t *testing.T) {
	dir := payloadDir(t)
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Skipf("no payloads to check: %v", err)
	}
	sort.Strings(paths)

	var sgml, relocated, unanchored, implicitCloses, stray, checked int
	for _, path := range paths {
		src := payloadHTML(t, path)
		body, err := ParseHTMLBody(src)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if n := body.UnmappedCount(); n != 0 {
			t.Errorf("%s: %d elements with a class outside the table: %+v", path, n, body.Unmapped())
		}
		if err := body.Root.checkInvariant(filepath.Base(path)); err != nil {
			t.Errorf("%s: %v", path, err)
		}
		kept := docText(body.Root) + strings.Join(body.SkippedText(), "")
		// The control is tokenized, not tag-stripped with a scan: raw scanning keeps
		// the quotes and punctuation inside malformed tags, which is the very thing the
		// reader exists not to do, and it reported tens of thousands of invented "
		// characters before it was replaced.
		if r, want, have, ok := firstRuneDiff(textChars(allPayloadText(t, src)), textChars(kept)); !ok {
			t.Errorf("%s: %q appears %d times in the payload and %d times in the tree+drops", path, r, want, have)
		}
		// Order is comparable only where nothing could have moved. Footnote material
		// travels by design -- payload at the bottom, tree at the reference -- so a
		// document containing any footnote is exempt from this half of the check, and
		// the count below records how many were actually compared.
		if countElems(body.Root, "Nota") == 0 {
			checked++
			if a, b := stripAllSpace(docText(body.Root)), stripAllSpace(payloadVisibleTextForTest(t, src)); a != b {
				i := 0
				for i < len(a) && i < len(b) && a[i] == b[i] {
					i++
				}
				// a is the tree and b is the payload: the labels have to match, or the
				// first reading of a failure points at the wrong side.
				t.Errorf("%s: text reordered at char %d of %d\ntree:    …%s…\npayload: …%s…",
					path, i, len(a), sliceAround(a, i), sliceAround(b, i))
			}
		}
		if body.SGML() {
			sgml++
		}
		relocated += body.RelocatedNotes()
		unanchored += body.UnmatchedNotes()
		implicitCloses += body.ImplicitCloses()
		stray += body.StrayEnds()
	}
	t.Logf("%d payloads: %d SGML generation, %d notes relocated, %d unanchored, %d implicit closes, %d stray end tags; order compared on %d",
		len(paths), sgml, relocated, unanchored, implicitCloses, stray, checked)
}

// payloadVisibleTextForTest is the payload's visible text, dropping the subtrees the
// mapping skips on purpose. It tracks tags rather than depth: these payloads contain
// stray end tags, and a counter drifts.
func payloadVisibleTextForTest(t *testing.T, src string) string {
	t.Helper()
	z := stdhtml.NewTokenizer(strings.NewReader(unquoteSGML(src)))
	type frame struct {
		tag  string
		drop bool
	}
	var stack []frame
	var out strings.Builder
	for {
		switch tt := z.Next(); tt {
		case stdhtml.ErrorToken:
			if err := z.Err(); err != io.EOF {
				t.Fatalf("tokenizing: %v", err)
			}
			return out.String()
		case stdhtml.TextToken:
			if len(stack) == 0 || !stack[len(stack)-1].drop {
				out.Write(z.Text())
			}
		case stdhtml.StartTagToken:
			tagBytes, _ := z.TagName()
			tag := strings.ToLower(string(tagBytes))
			class := ""
			for {
				k, v, more := z.TagAttr()
				if strings.EqualFold(string(k), "class") {
					class = strings.TrimSpace(string(v))
				}
				if !more {
					break
				}
			}
			drop := classifyHTML(tag, class).Role == roleSkip
			if len(stack) > 0 && stack[len(stack)-1].drop {
				drop = true
			}
			if !htmlVoidTags[tag] {
				stack = append(stack, frame{tag: tag, drop: drop})
			}
		case stdhtml.SelfClosingTagToken, stdhtml.EndTagToken:
			tagBytes, _ := z.TagName()
			tag := strings.ToLower(string(tagBytes))
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].tag == tag {
					stack = stack[:i]
					break
				}
			}
		}
	}
}

func stripAllSpace(s string) string { return strings.Join(strings.Fields(s), "") }

// cutRunes truncates without splitting a character.
func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
