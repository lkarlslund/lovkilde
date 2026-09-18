// Structural census over every fetched payload, as opposed to the paired run: this
// needs no reference text, so it can cover the 101,727 documents whose text exists
// only as HTML -- the population the reader will actually be asked to read.
//
// Paired truth says whether the reader is right, on the ~90,000 documents that have
// canonical XML to compare with. Those documents are overwhelmingly modern: 8 of
// them predate 1945 and 61 more predate 1980. So correctness can only be measured
// on the new end of the corpus, while frequency -- which residual class costs the
// most text across the whole archive -- has to be measured without ground truth.
// Both numbers are needed to rank a fix, and neither is a substitute for the other.
//
//	RETSIN_BODIES_CENSUS=1 go test -timeout 4h -run TestBodyCensus ./internal/lexdania/ -v
//
// It reports histograms rather than asserting. A new rare class is information, and
// a full-corpus scan that fails on the first oddity is a scan nobody finishes. The
// assertions belong to the pinned pilot tests.
package lexdania

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const bodiesCensusEnv = "RETSIN_BODIES_CENSUS"

// censusLimit bounds a deliberately short run, so the harness itself can be checked
// on a slice before committing an hour of CPU to the whole archive.
func censusLimit(t *testing.T) int {
	v := os.Getenv("RETSIN_BODIES_CENSUS_LIMIT")
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		t.Fatalf("RETSIN_BODIES_CENSUS_LIMIT=%q is not a positive number", v)
	}
	return n
}

// censusAgg is everything worth counting while reading every payload once. Fields
// are counters and maps, so a worker's result merges into the total with merge().
type censusAgg struct {
	payloads, empty, undecodable, parseFail int
	bytes                                   int64
	parseFailSample                         []string
	sgml, noParagraphText                   int

	unmapped     map[string]int // tag.class -> occurrences, corpus-wide
	classDrops   map[string]int // deliberate skips by class
	elements     map[string]int // canonical element names the reader produced
	sectionKinds map[string]int // what Sections() found

	zeroSectionDocs   int
	zeroSectionSample []string
	provisions        int
	emptyProvisions   int
	invariantBad      int
	invariantSample   []string
	stkUnderNothing   int // a Stk whose parent is not a Paragraf: Sections() only knows a stykke under a
	// paragraph, so such a stykke produces no section at all and its text is unreachable
	emptyNames      int      // elements the reader built with no name at all
	emptyNameSample []string // documents carrying them

	docsByKind                                             map[string]int
	zeroByKind                                             map[string]int
	docsByDecade                                           map[string]int
	unmappedDocs                                           int
	notesMoved, notesUnanchored, implicitCloses, strayEnds int
	absorbed, wrappedParagraphs                            int

	textBuckets map[string]int // prose volume per document, bucketed
	// worst keeps a few paths per bucket: the counts rank the work, but fixing it
	// needs a document to look at, and data/pilot-bodies is not in git.
	worstNoSections []string
	worstUnmapped   []string
}

func newCensusAgg() *censusAgg {
	return &censusAgg{
		unmapped: map[string]int{}, classDrops: map[string]int{},
		elements: map[string]int{}, sectionKinds: map[string]int{},
		docsByKind: map[string]int{}, zeroByKind: map[string]int{},
		docsByDecade: map[string]int{}, textBuckets: map[string]int{},
	}
}

func (a *censusAgg) merge(b *censusAgg) {
	a.payloads += b.payloads
	a.bytes += b.bytes
	a.empty += b.empty
	a.undecodable += b.undecodable
	a.parseFail += b.parseFail
	a.sgml += b.sgml
	a.noParagraphText += b.noParagraphText
	a.zeroSectionDocs += b.zeroSectionDocs
	a.provisions += b.provisions
	a.emptyProvisions += b.emptyProvisions
	a.invariantBad += b.invariantBad
	a.stkUnderNothing += b.stkUnderNothing
	a.emptyNames += b.emptyNames
	a.unmappedDocs += b.unmappedDocs
	a.notesMoved += b.notesMoved
	a.notesUnanchored += b.notesUnanchored
	a.implicitCloses += b.implicitCloses
	a.strayEnds += b.strayEnds
	a.absorbed += b.absorbed
	a.wrappedParagraphs += b.wrappedParagraphs
	for k, v := range b.unmapped {
		a.unmapped[k] += v
	}
	for k, v := range b.classDrops {
		a.classDrops[k] += v
	}
	for k, v := range b.elements {
		a.elements[k] += v
	}
	for k, v := range b.sectionKinds {
		a.sectionKinds[k] += v
	}
	for k, v := range b.docsByKind {
		a.docsByKind[k] += v
	}
	for k, v := range b.zeroByKind {
		a.zeroByKind[k] += v
	}
	for k, v := range b.docsByDecade {
		a.docsByDecade[k] += v
	}
	for k, v := range b.textBuckets {
		a.textBuckets[k] += v
	}
	a.parseFailSample = appendCap(a.parseFailSample, b.parseFailSample)
	a.zeroSectionSample = appendCap(a.zeroSectionSample, b.zeroSectionSample)
	a.invariantSample = appendCap(a.invariantSample, b.invariantSample)
	a.worstNoSections = appendCap(a.worstNoSections, b.worstNoSections)
	a.worstUnmapped = appendCap(a.worstUnmapped, b.worstUnmapped)
	a.emptyNameSample = appendCap(a.emptyNameSample, b.emptyNameSample)
}

// appendCap keeps a handful of example paths per bucket rather than 100k.
func appendCap(dst, src []string) []string {
	const keep = 8
	for _, s := range src {
		if len(dst) >= keep {
			break
		}
		dst = append(dst, s)
	}
	return dst
}

// censusOne reads a single payload. It never fails the test: an unreadable payload
// is a data point about the corpus, not a reason to stop.
func censusOne(path string) *censusAgg {
	a := newCensusAgg()
	raw, err := os.ReadFile(path)
	if err != nil {
		a.undecodable++
		return a
	}
	a.payloads++
	a.bytes += int64(len(raw))
	var list []pairedPayload
	if err := json.Unmarshal(raw, &list); err != nil {
		a.undecodable++
		a.parseFailSample = []string{path}
		return a
	}
	var html strings.Builder
	for _, d := range list {
		html.WriteString(d.DocumentHTML)
	}
	rel := filepath.ToSlash(path)
	// Whitespace counts as no text: this bucket answers "did the site have any prose
	// for this document", not "were any bytes present".
	if strings.TrimSpace(html.String()) == "" {
		a.empty++
		return a
	}
	body, err := ParseHTMLBody(html.String())
	if err != nil {
		a.parseFail++
		a.parseFailSample = []string{rel}
		return a
	}
	kind, decade := censusBucket(rel)
	a.docsByKind[kind]++
	a.docsByDecade[decade]++
	if body.SGML() {
		a.sgml++
	}
	for _, u := range body.Unmapped() {
		a.unmapped[u.Tag+"."+u.Class] += u.Count
		a.unmappedDocs++
	}
	for k, v := range body.Skips() {
		a.classDrops[k] += v
	}
	a.notesMoved += body.RelocatedNotes()
	a.notesUnanchored += body.UnmatchedNotes()
	a.implicitCloses += body.ImplicitCloses()
	a.strayEnds += body.StrayEnds()
	a.absorbed += body.Absorbed()
	a.wrappedParagraphs += body.WrappedParagraphs()
	if err := body.Root.checkInvariant(rel); err != nil {
		a.invariantBad++
		a.invariantSample = []string{rel}
	}
	countCensusTree(body.Root, a)
	if a.elements[""] > 0 {
		a.emptyNameSample = append(a.emptyNameSample, rel)
	}

	secs := sectionsIn(body.Root)
	if len(secs) == 0 {
		a.zeroSectionDocs++
		a.zeroByKind[kind]++
		a.zeroSectionSample = []string{rel}
		a.worstNoSections = []string{rel}
	}
	for _, s := range secs {
		a.sectionKinds[s.Kind]++
		if strings.TrimSpace(s.Node.Text()) == "" {
			a.emptyProvisions++
		}
	}
	a.provisions += len(secs)
	text := len(strings.TrimSpace(docText(body.Root)))
	if text == 0 {
		a.noParagraphText++
	}
	a.textBuckets[textBucket(text)]++
	return a
}

// countCensusTree tallies the element names the reader produced, which is how an
// invented element shows up: a name outside the pinned canonical vocabulary is a
// tree that looks fine and matches nothing.
//
// It also counts stykker that have no paragraph above them, and it does so on the
// tree rather than on Sections(). That is not a stylistic choice: Sections() only
// recognises a Stk as a child of a paragraph, so an orphan stykke yields no section
// at all and its text is simply unaddressable. Counting sections would report a
// clean zero for the one defect this number exists to find.
func countCensusTree(n *Node, a *censusAgg) {
	if n == nil {
		return
	}
	a.elements[n.Name]++
	if n.Name == "" {
		a.emptyNames++
	}
	for _, c := range n.Elems {
		if c.Name == "Stk" && n.Name != "Paragraf" {
			a.stkUnderNothing++
		}
		countCensusTree(c, a)
	}
}

func censusBucket(rel string) (kind, decade string) {
	segs := strings.Split(filepath.ToSlash(rel), "/")
	if len(segs) < 3 {
		return "?", "unknown"
	}
	// kind/year/leaf.json relative to the archive root, whatever prefix the caller
	// walked it with.
	kind = segs[len(segs)-3]
	year, err := strconv.Atoi(segs[len(segs)-2])
	if err != nil || year < 1000 || year > 9999 {
		return kind, "unknown"
	}
	return kind, strconv.Itoa(year/10*10) + "s"
}

func textBucket(chars int) string {
	switch {
	case chars == 0:
		return "0"
	case chars < 1000:
		return "<1k"
	case chars < 10000:
		return "1-10k"
	case chars < 100000:
		return "10-100k"
	default:
		return ">100k"
	}
}

func histogram(m map[string]int, n int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	out := make([]string, 0, min(n, len(keys)))
	for _, k := range keys[:min(n, len(keys))] {
		out = append(out, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return out
}

func TestBodyCensus(t *testing.T) {
	if os.Getenv(bodiesCensusEnv) == "" {
		t.Skipf("set %s=1 to census every payload in the archive", bodiesCensusEnv)
	}
	root := filepath.Join("..", "..", "data", "xml")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("no archive at %s: %v", root, err)
	}
	var paths []string
	// The paired sample lives outside data/xml on purpose, so everything found here
	// is a real sidecar of a document whose XML had no text.
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".json") {
			paths = append(paths, p)
		}
		return nil
	})
	limit := censusLimit(t)
	if limit > 0 && len(paths) > limit {
		paths = paths[:limit]
	}
	if len(paths) == 0 {
		t.Skip("no payloads in the archive")
	}
	t.Logf("censusing %d payloads on %d workers", len(paths), runtime.NumCPU())

	total := newCensusAgg()
	fanOut(paths, runtime.NumCPU(), censusOne, func(a *censusAgg) { total.merge(a) })

	t.Logf("payloads=%d bytes=%s empty=%d undecodable=%d parse_fail=%d sgml_generation=%d",
		total.payloads, archiveSize(total.bytes), total.empty, total.undecodable, total.parseFail, total.sgml)
	t.Logf("documents with no prose at all=%d; no sections built=%d (%.1f%%)",
		total.noParagraphText, total.zeroSectionDocs,
		100*float64(total.zeroSectionDocs)/float64(max(total.payloads, 1)))
	t.Logf("sections: %d total, %d of them empty; nesting pass absorbed %d elements into %d paragraphs",
		total.provisions, total.emptyProvisions, total.absorbed, total.wrappedParagraphs)
	t.Logf("orphan stykker (no paragraph parent)=%d", total.stkUnderNothing)
	t.Logf("elements with no name at all=%d", total.emptyNames)
	if len(total.emptyNameSample) > 0 {
		t.Logf("example unnamed-element documents: %s", strings.Join(total.emptyNameSample, " "))
	}
	t.Logf("footnotes relocated=%d unanchored=%d; implicit closes=%d stray ends=%d; invariant failures=%d",
		total.notesMoved, total.notesUnanchored, total.implicitCloses, total.strayEnds, total.invariantBad)
	t.Logf("prose per document: %s", strings.Join(histogram(total.textBuckets, 8), " "))
	t.Logf("payload classes outside the table: %s", strings.Join(histogram(total.unmapped, 40), " "))
	// A tag with no entry is a different failure than a class with no entry: the class
	// falls back to the tag's element, but an unknown tag has no element to fall back
	// to, which is what produces an unnamed node.
	noTag := map[string]int{}
	for k, v := range total.unmapped {
		tag := strings.SplitN(k, ".", 2)[0]
		if _, ok := htmlTags[tag]; !ok {
			noTag[k] += v
		}
	}
	t.Logf("payload tags with no entry at all: %s", strings.Join(histogram(noTag, 25), " "))
	t.Logf("deliberate class drops: %s", strings.Join(histogram(total.classDrops, 25), " "))
	t.Logf("sections by kind: %s", strings.Join(histogram(total.sectionKinds, 25), " "))
	t.Logf("elements produced: %s", strings.Join(histogram(total.elements, 40), " "))
	t.Logf("documents by kind: %s", strings.Join(histogram(total.docsByKind, 25), " "))
	t.Logf("no-section documents by kind: %s", strings.Join(histogram(total.zeroByKind, 25), " "))
	t.Logf("documents by decade: %s", strings.Join(histogram(total.docsByDecade, 30), " "))

	// The one assertion: an element name the reader invented is not a style problem,
	// it is a tree that matches nothing downstream.
	vocabRaw, err := os.ReadFile(filepath.Join("testdata", "canonical-elements.txt"))
	if err != nil {
		t.Fatalf("the pinned canonical vocabulary is missing: %v", err)
	}
	if bad := inventedElements(total.elements, string(vocabRaw)); len(bad) > 0 {
		t.Errorf("the reader produced element names outside the canonical vocabulary: %q", bad)
	}
	for _, line := range []struct {
		label string
		paths []string
	}{
		{"parse failures", total.parseFailSample},
		{"no sections", total.zeroSectionSample},
		{"invariant violations", total.invariantSample},
	} {
		if len(line.paths) > 0 {
			t.Logf("example %s (check these into testdata before fixing): %s",
				line.label, strings.Join(line.paths, " "))
		}
	}
}

// inventedElements compares what the reader built against the pinned canonical
// vocabulary, allowing the names the reader must synthesise to make a payload
// addressable at all.
//
// The vocabulary arrives as an argument rather than being read here: a helper that
// returns "nothing invented" when it cannot read its own reference file turns the
// census's one hard assertion into a no-op exactly when the testdata is missing.
func inventedElements(built map[string]int, vocabulary string) []string {
	allowed := map[string]bool{}
	for _, line := range strings.Split(vocabulary, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			allowed[line] = true
		}
	}
	for _, name := range []string{"DokumentIndhold", "Root", "Paragraf", "Stk", "Explicatus", "Exitus", "Indentatio", "Nota", "Kapitel", "Afsnit", "Titel", "Nr", "Linea", "Char", "Table", "Td", "Th", "Tr"} {
		allowed[name] = true
	}
	var bad []string
	for name := range built {
		if !allowed[name] {
			bad = append(bad, name)
		}
	}
	sort.Strings(bad)
	return bad
}

func archiveSize(n int64) string {
	switch {
	case n > 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(1<<30))
	case n > 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(1<<20))
	default:
		return strconv.FormatInt(n, 10) + " B"
	}
}
