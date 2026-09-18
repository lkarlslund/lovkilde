// The census is a measuring instrument, and an instrument that quietly undercounts
// is worse than none: the ranking it produces still looks like evidence. These check
// the counting itself against one payload whose shape is known, from markup checked
// into testdata, so they run without the archive or the gitignored corpora.
package lexdania

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBodyCensusCountsOnePayload(t *testing.T) {
	rawHTML, err := os.ReadFile(filepath.Join("testdata", "payload-lta_1942_542.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(rawHTML)
	if strings.TrimSpace(html) == "" {
		t.Fatal("the checked-in payload has no markup")
	}
	raw, err := json.Marshal([]map[string]any{{"documentHtml": html}})
	if err != nil {
		t.Fatal(err)
	}
	// The path layout is what kind and decade are read from, so the fixture goes
	// where the archive would put it.
	dir := t.TempDir()
	dest := filepath.Join(dir, "lta", "1942", "542.json")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	a := censusOne(dest)
	if a.payloads != 1 || a.empty != 0 || a.undecodable != 0 || a.parseFail != 0 {
		t.Errorf("tally = {payloads:%d empty:%d undecodable:%d parseFail:%d}, want one clean payload",
			a.payloads, a.empty, a.undecodable, a.parseFail)
	}
	if a.docsByKind["lta"] != 1 {
		t.Errorf("docsByKind = %v, want the kind read off the path", a.docsByKind)
	}
	if a.docsByDecade["1940s"] != 1 {
		t.Errorf("docsByDecade = %v, want the decade read off the path", a.docsByDecade)
	}
	if a.provisions == 0 {
		t.Error("no sections counted for a payload with paragraphs: Sections() output is not being counted")
	}
	if a.sectionKinds["paragraf"] == 0 {
		t.Errorf("sectionKinds = %v, want a paragraph among them", a.sectionKinds)
	}
	if a.elements["Paragraf"] == 0 {
		t.Errorf("elements = %v, want the tree's paragraph elements counted", a.elements)
	}
	if a.zeroSectionDocs != 0 {
		t.Error("a document with sections was counted as having none")
	}
	// The prose bucket is re-derived here rather than read back from the census: the
	// point is that the instrument and an independent reading agree.
	body, err := ParseHTMLBody(html)
	if err != nil {
		t.Fatal(err)
	}
	want := textBucket(len(strings.TrimSpace(docText(body.Root))))
	if a.textBuckets[want] != 1 {
		t.Errorf("textBuckets = %v, want one document in %q", a.textBuckets, want)
	}
	if a.bytes != int64(len(raw)) {
		t.Errorf("bytes = %d, want the payload size %d", a.bytes, len(raw))
	}
}

// TestBodyCensusCountsEmptyPayload pins the availability counter: "the site's own
// payload has no text" is the finding that tells us a document is unrecoverable
// rather than unfetched, and a census that tallies it as anything else turns a
// corpus gap into a parsing bug, or the other way round.
func TestBodyCensusCountsEmptyPayload(t *testing.T) {
	raw, err := json.Marshal([]map[string]any{{"documentHtml": "   "}})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "lta", "1990", "9.json")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	a := censusOne(dest)
	if a.payloads != 1 || a.empty != 1 || a.provisions != 0 {
		t.Errorf("tally = {payloads:%d empty:%d provisions:%d}, want one payload counted as empty with no sections",
			a.payloads, a.empty, a.provisions)
	}
}

// TestBodyCensusCountsUndecodablePayload pins the other failure door: a sidecar that
// is not the site's JSON at all must be tallied as undecodable, not silently as
// empty, or the census blames the publisher for a truncated download.
func TestBodyCensusCountsUndecodablePayload(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "lta", "1990", "8.json")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("<html><body>challenge page</body></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := censusOne(dest)
	if a.payloads != 1 || a.undecodable != 1 || a.empty != 0 {
		t.Errorf("tally = {payloads:%d undecodable:%d empty:%d}, want an undecodable payload, not an empty document",
			a.payloads, a.undecodable, a.empty)
	}
}

// payloads through workers and merges their results: a field that fails to merge
// under-reports by a factor of the worker count, which is invisible in a small run
// and quietly wrong in the real one.
func TestBodyCensusMergeIsAdditive(t *testing.T) {
	a, b := newCensusAgg(), newCensusAgg()
	a.payloads, a.empty, a.provisions, a.bytes = 3, 1, 7, 5000
	a.sgml, a.absorbed, a.stkUnderNothing = 2, 11, 4
	a.unmapped["div.bilag"], a.docsByKind["lta"], a.docsByDecade["1960s"] = 5, 3, 2
	a.textBuckets["1-10k"], a.sectionKinds["paragraf"], a.elements["Paragraf"] = 3, 7, 9
	a.parseFailSample = []string{"x"}
	b.payloads, b.empty, b.provisions, b.bytes = 4, 2, 9, 6000
	b.sgml, b.absorbed, b.stkUnderNothing = 1, 5, 2
	b.unmapped["div.bilag"], b.docsByKind["lta"], b.docsByDecade["1960s"] = 1, 4, 4
	b.textBuckets["1-10k"], b.sectionKinds["paragraf"], b.elements["Paragraf"] = 4, 9, 11

	a.merge(b)

	merged := []struct {
		name string
		got  int64
		want int64
	}{
		{"payloads", int64(a.payloads), 7},
		{"empty", int64(a.empty), 3},
		{"provisions", int64(a.provisions), 16},
		{"bytes", a.bytes, 11000},
		{"sgml", int64(a.sgml), 3},
		{"absorbed", int64(a.absorbed), 16},
		{"stkUnderNothing", int64(a.stkUnderNothing), 6},
		{"unmapped", int64(a.unmapped["div.bilag"]), 6},
		{"docsByKind", int64(a.docsByKind["lta"]), 7},
		{"docsByDecade", int64(a.docsByDecade["1960s"]), 6},
		{"textBuckets", int64(a.textBuckets["1-10k"]), 7},
		{"sectionKinds", int64(a.sectionKinds["paragraf"]), 16},
		{"elements", int64(a.elements["Paragraf"]), 20},
	}
	for _, tc := range merged {
		if tc.got != tc.want {
			t.Errorf("merged %s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
	if len(a.parseFailSample) != 1 {
		t.Errorf("sample paths = %v, want a few examples kept, not one per worker", a.parseFailSample)
	}
}

// TestCensusBucketReadsTheArchiveLayout pins bucket derivation on the shapes the
// archive produces, including the one that must not be guessed at.
func TestCensusBucketReadsTheArchiveLayout(t *testing.T) {
	for _, tc := range []struct {
		path, kind, decade string
	}{
		{"../../data/xml/lta/1942/542.json", "lta", "1940s"},
		{"../../data/xml/retsinfo/2024/1234.json", "retsinfo", "2020s"},
		{"../../data/xml/ft/2008/200813L00071.json", "ft", "2000s"},
		{"../../data/xml/lta/nonsense/5.json", "lta", "unknown"},
		{"shallow.json", "?", "unknown"},
	} {
		kind, decade := censusBucket(tc.path)
		if kind != tc.kind || decade != tc.decade {
			t.Errorf("censusBucket(%q) = %q,%q want %q,%q", tc.path, kind, decade, tc.kind, tc.decade)
		}
	}
}

// TestInventedElementsAreCaught pins the vocabulary check the census ends with,
// including the names the reader is allowed to synthesise.
func TestInventedElementsAreCaught(t *testing.T) {
	vocab := "Paragraf\nStk\nNota\n# comment\nAfsnit\n"
	bad := inventedElements(map[string]int{"Paragraf": 5, "Stk": 2, "Nota": 1, "AfsnitUdenNavn": 3}, vocab)
	if len(bad) != 1 || bad[0] != "AfsnitUdenNavn" {
		t.Errorf("inventedElements = %v, want only the name outside the pinned vocabulary", bad)
	}
	// A name the reader has to invent to make a payload addressable at all must not
	// be reported, or the census's one hard assertion fires on every run.
	for _, synthesised := range []string{"DokumentIndhold", "Exitus", "Linea", "Char", "Nr"} {
		if bad := inventedElements(map[string]int{synthesised: 1}, vocab); len(bad) != 0 {
			t.Errorf("allowed synthesised name %s reported as invented: %v", synthesised, bad)
		}
	}
}

// TestTextBucketBoundaries pins the volume buckets directly. Deriving the expected
// bucket from textBucket itself would be circular -- the same mistake that let a
// relaxed gate pass as checked -- so the boundaries are written out.
func TestTextBucketBoundaries(t *testing.T) {
	for _, tc := range []struct {
		chars int
		want  string
	}{
		{0, "0"},
		{1, "<1k"},
		{999, "<1k"},
		{1000, "1-10k"},
		{9999, "1-10k"},
		{10000, "10-100k"},
		{99999, "10-100k"},
		{100000, ">100k"},
	} {
		if got := textBucket(tc.chars); got != tc.want {
			t.Errorf("textBucket(%d) = %q, want %q", tc.chars, got, tc.want)
		}
	}
}

// TestBodyCensusCountsOrphanStykke is the one number the census exists to produce
// for M005T006: stykker that never got a paragraph. If the detector regressed, the
// census would report a clean zero, which is the most convincing possible way to be
// wrong.
func TestBodyCensusCountsOrphanStykke(t *testing.T) {
	// A numbered stykke with no paragraph before it: nothing for the nesting pass to
	// attach it to, so it stays a top-level Stk. Sections() yields nothing at all for
	// it, which is the finding: the text is there and unreachable.
	html := `<div class="bjelke">Den fulde tekst</div>` +
		`<p class="Stk2"><span class="StkNr">3.</span> Selvaendigt stykke uden paragraph.</p>`
	raw, err := json.Marshal([]map[string]any{{"documentHtml": html}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	dest := filepath.Join(dir, "lta", "1990", "7.json")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	a := censusOne(dest)
	if a.stkUnderNothing != 1 {
		t.Errorf("stkUnderNothing = %d, want 1: a stykke with no paragraph must be counted, "+
			"or the census reports a clean zero for the very defect it was written to measure", a.stkUnderNothing)
	}
}
