// Corpus-scale paired truth: the same comparison the 51-document pilot makes, run
// over a stratified sample of thousands.
//
// The pilot established that the reader can be measured; it could not say which
// residual class is worth fixing first, because 51 documents chosen by hand are an
// anecdote about a distribution. This run says that, and it is gated rather than
// always-on because its corpus (data/paired-truth, written by `retsin sample
// --fetch`) is 60 MB of fetched payloads that are not in git.
//
//	RETSIN_PAIRED_TRUTH=../../data/paired-truth \
//	  go test -timeout 2h -run TestPairedTruthParity ./internal/lexdania/ -v
//
// The reference on every document is the canonical XML in the archive, never a
// third party's text: see the rationale in internal/crawl/sample.go.
package lexdania

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// pairedTruthEnv points the run at a directory of sampled payloads laid out like
// the archive (kind/year/leaf.json).
const pairedTruthEnv = "RETSIN_PAIRED_TRUTH"

func pairedTruthDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv(pairedTruthEnv)
	if dir == "" {
		t.Skipf("set %s to a directory of sampled payloads (retsin sample --fetch) to run the corpus measurement", pairedTruthEnv)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("no paired-truth corpus at %s: %v", dir, err)
	}
	return dir
}

// pairedTruthArchive is the reference side. Unlike the pilot, the corpus run reads
// the real archive: the sample was drawn from it, and pointing at a copy would let
// the two sides drift apart without either measurement noticing.
func pairedTruthArchive(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "data", "xml")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("no archive at %s: %v", dir, err)
	}
	return dir
}

// readStrataLabels reads the sampler's manifest: which stratum asked for which
// document. Parsed here rather than imported from internal/crawl so the reader
// package keeps no dependency on the crawler -- a measurement harness that cannot
// run without the fetch machinery is harder to run against an old corpus.
func readStrataLabels(t *testing.T, path string) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Logf("no manifest at %s (%v): reporting the corpus as one pool", path, err)
		return nil
	}
	out := map[string][]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		cols := strings.Split(line, "\t")
		if len(cols) < 5 || strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		out[cols[0]] = strings.Split(cols[4], ";")
	}
	if len(out) == 0 {
		t.Logf("manifest %s yielded no labels; reporting the corpus as one pool", path)
		return nil
	}
	return out
}

// truthTally is one bucket's result: a stratum, a decade, or the whole corpus.
type truthTally struct {
	counts   parityCounts
	missing  map[string]int // canonical provisions the reader never addressed, by kind
	unmapped map[string]int // payload classes outside the mapping table
}

func newTruthTally() *truthTally {
	return &truthTally{missing: map[string]int{}, unmapped: map[string]int{}}
}

func (t *truthTally) add(o parityCounts) { t.counts.add(o) }

// stkMarkersAgainstPosition counts, under every paragraph, whether the printed
// "Stk. N" agrees with the stykke's position among its siblings. Sections() numbers
// stykker by position, so wherever a marker disagrees, an address like "§ 5, stk. 2"
// resolves to different text than the page shows. The canonical side is the authority:
// if the register itself writes numbers that do not match positions, then position
// numbering is the defect and not the payload.
func stkMarkersAgainstPosition(n *Node, out map[string]int) {
	if n == nil {
		return
	}
	switch n.Name {
	case "Paragraf", "AendringCentreretParagraf", "IkraftCentreretParagraf", "CentreretParagraf":
		pos := 0
		for _, c := range n.Elems {
			if c.Name != "Stk" {
				continue
			}
			pos++
			marker := ""
			if e := c.Elem("Explicatus"); e != nil {
				marker = strings.TrimSpace(e.Text())
			}
			num := lexdoc.FirstNumber(marker)
			switch {
			case num == "":
				out["no_marker"]++
			case num == strconv.Itoa(pos):
				out["matches_position"]++
			default:
				out["disagrees"]++
			}
		}
	}
	for _, c := range n.Elems {
		stkMarkersAgainstPosition(c, out)
	}
}

// ranked sorts buckets by the rate at which canonical provisions went unaddressed,
// which is the number that decides what to fix first.
func ranked(buckets map[string]*truthTally) []string {
	out := make([]string, 0, len(buckets))
	for name := range buckets {
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := buckets[out[i]], buckets[out[j]]
		if a.counts.missingRate() != b.counts.missingRate() {
			return a.counts.missingRate() > b.counts.missingRate()
		}
		return out[i] < out[j]
	})
	return out
}

func payloadList(t *testing.T, dir string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".json") {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	sort.Strings(paths)
	return paths
}

// payloadDocumentHTML extracts the markup from a stored sidecar: the site answers
// with a list, and a payload with no markup at all is a document the site cannot
// serve as text.
func payloadDocumentHTML(t *testing.T, path string) (string, error) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var list []pairedPayload
	if err := json.Unmarshal(raw, &list); err != nil {
		var one pairedPayload
		if err2 := json.Unmarshal(raw, &one); err2 != nil {
			return "", err2
		}
		var b strings.Builder
		b.WriteString(one.DocumentHTML)
		return b.String(), nil
	}
	var b strings.Builder
	for _, d := range list {
		b.WriteString(d.DocumentHTML)
	}
	return b.String(), nil
}

func decadeOf(rel string) string {
	segs := strings.Split(filepath.ToSlash(rel), "/")
	if len(segs) < 2 {
		return "unknown"
	}
	y, err := strconv.Atoi(segs[1])
	if err != nil || y < 1000 {
		return "unknown"
	}
	return strconv.Itoa(y/10*10) + "s"
}

func TestPairedTruthParity(t *testing.T) {
	dir := pairedTruthDir(t)
	archive := pairedTruthArchive(t)
	paths := payloadList(t, dir)
	if len(paths) == 0 {
		t.Skipf("no payloads under %s", dir)
	}
	labels := readStrataLabels(t, filepath.Join(dir, "manifest.tsv"))

	total := newTruthTally()
	byStratum := map[string]*truthTally{}
	byDecade := map[string]*truthTally{}
	unmappedTotal := map[string]int{}
	stkMarkerCanonical := map[string]int{}
	stkMarkerTree := map[string]int{}
	lastDisagreeSample := -1
	var stkDisagreeSample []string
	// skips is the availability census: how many sampled documents could not be
	// compared, and why. A measurement that silently compares 60 % of its sample
	// because the rest were dropped for one boring reason is a biased measurement.
	skips := map[string]int{}

	for _, path := range paths {
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			t.Fatal(err)
		}
		rel = filepath.ToSlash(rel)
		xmlPath := filepath.Join(archive, strings.TrimSuffix(rel, ".json")+".xml")
		xmlRaw, err := os.ReadFile(xmlPath)
		if err != nil {
			skips["no canonical xml"]++
			continue
		}
		doc, err := Parse(xmlRaw)
		if err != nil {
			skips["canonical xml unparseable"]++
			continue
		}
		html, err := payloadDocumentHTML(t, path)
		if err != nil {
			skips["payload undecodable"]++
			continue
		}
		if strings.TrimSpace(html) == "" {
			skips["payload has no documentHtml"]++
			continue
		}
		body, err := ParseHTMLBody(html)
		if err != nil {
			// Not fatal, but never silent: one payload that aborts a 2,000-document
			// run turns the measurement into something nobody finishes.
			skips["payload parse failed"]++
			t.Logf("%s: %v", rel, err)
			continue
		}
		for _, u := range body.Unmapped() {
			key := u.Tag + "." + u.Class
			unmappedTotal[key]++
			total.unmapped[key]++
		}
		if err := body.Root.checkInvariant(rel); err != nil {
			skips["mixed-content invariant violated"]++
			t.Errorf("%s: %v", rel, err)
		}

		per := parityCounts{Documents: 1}
		res := residual{missing: map[string]int{}, extra: map[string]int{}, textDiffers: map[string]int{}}
		comparePair(doc.Sections(), sectionsIn(body.Root), &per, &res)
		stkMarkersAgainstPosition(doc.Root, stkMarkerCanonical)
		stkMarkersAgainstPosition(body.Root, stkMarkerTree)
		if stkMarkerTree["disagrees"] > lastDisagreeSample {
			lastDisagreeSample = stkMarkerTree["disagrees"]
			stkDisagreeSample = []string{rel}
		}
		total.add(per)
		for kind, n := range res.missing {
			total.missing[kind] += n
		}
		decade := decadeOf(rel)
		if byDecade[decade] == nil {
			byDecade[decade] = newTruthTally()
		}
		byDecade[decade].add(per)
		// The manifest keys rows by the ELI path as the sampler wrote it
		// ("fob/1980/181"); the payload on disk is that path plus .json. Joining on
		// the raw rel silently labelled nothing, which is what the vacuity guard at
		// the bottom of this test caught on the first real run.
		for _, s := range labels[strings.TrimSuffix(rel, ".json")] {
			if byStratum[s] == nil {
				byStratum[s] = newTruthTally()
			}
			byStratum[s].add(per)
		}
	}

	t.Logf("%d payloads, %d documents compared", len(paths), total.counts.Documents)
	if len(skips) > 0 {
		keys := make([]string, 0, len(skips))
		for k := range skips {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+strconv.Itoa(skips[k]))
		}
		t.Logf("not compared: %s", strings.Join(parts, " "))
	}

	t.Logf("\n%s", total.counts)
	t.Logf("missing by kind: %s", formatResidual(total.missing))
	t.Logf("stykke marker against position, canonical XML: %s", strings.Join(histogram(stkMarkerCanonical, 6), " "))
	t.Logf("stykke marker against position, HTML tree:      %s", strings.Join(histogram(stkMarkerTree, 6), " "))
	if len(stkDisagreeSample) > 0 {
		t.Logf("example document with a disagreeing marker: %s", strings.Join(stkDisagreeSample, " "))
	}
	t.Logf("addresses resolved identically with identical text: %d of %d canonical (%.1f%%)",
		total.counts.TextEqual, total.counts.XMLSections, 100*float64(total.counts.TextEqual)/float64(max(total.counts.XMLSections, 1)))

	// The ranking this whole exercise exists to produce.
	t.Logf("== unaddressed rate by stratum ==")
	for _, name := range ranked(byStratum) {
		c := byStratum[name].counts
		t.Logf("%-10s documents=%-4d canonical=%-6d matched=%-6d equal_text=%-6d missing=%-5d rate=%.1f%%",
			name, c.Documents, c.XMLSections, c.AddressMatched, c.TextEqual, c.MissingFromTree, c.missingRate())
	}
	t.Logf("== unaddressed rate by decade ==")
	for _, name := range ranked(byDecade) {
		c := byDecade[name].counts
		t.Logf("%-8s documents=%-4d canonical=%-6d matched=%-6d equal_text=%-6d missing=%-5d rate=%.1f%%",
			name, c.Documents, c.XMLSections, c.AddressMatched, c.TextEqual, c.MissingFromTree, c.missingRate())
	}
	if len(unmappedTotal) > 0 {
		keys := make([]string, 0, len(unmappedTotal))
		for k := range unmappedTotal {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if unmappedTotal[keys[i]] != unmappedTotal[keys[j]] {
				return unmappedTotal[keys[i]] > unmappedTotal[keys[j]]
			}
			return keys[i] < keys[j]
		})
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+strconv.Itoa(unmappedTotal[k]))
		}
		t.Logf("== payload classes outside the mapping table (%d distinct) ==\n%s",
			len(keys), strings.Join(parts, " "))
	}

	// Guards against a run that reports success on nothing.
	if total.counts.Documents == 0 {
		t.Fatal("compared no documents: the measurement is vacuous")
	}
	if total.counts.XMLSections == 0 {
		t.Fatal("compared documents yielded no canonical sections")
	}
	if total.counts.AddressMatched == 0 {
		t.Fatal("no address matched on either side: the two trees are not being joined the same way")
	}
	if len(labels) > 0 && len(byStratum) == 0 {
		t.Fatal("the manifest labelled documents but none of them were compared: the join between manifest and payload directory is broken")
	}
}

// The stykke-marker counts are a published claim about the register's own numbering, so
// the instrument that produced them is pinned here rather than only exercised on the
// gated corpus run. Numbers are exact on purpose: a counter that never fires still
// prints a tidy zero, which is how a measurement survives its own bug.
func TestPairedTruthStykkeMarkerInstrument(t *testing.T) {
	doc := mustParse(t, `<Dokument><Meta><AccessionNumber>A1</AccessionNumber></Meta><DokumentIndhold>
		<Paragraf><Explicatus>§ 1.</Explicatus>
			<Stk><Explicatus>Stk. 2.</Explicatus>Fejlnummerering.</Stk>
		</Paragraf>
		<Paragraf><Explicatus>§ 2.</Explicatus>
			<Stk>Umarkeret forstekst.</Stk>
			<Stk><Explicatus>Stk. 2.</Explicatus>Anden.</Stk>
		</Paragraf>
	</DokumentIndhold></Dokument>`)
	out := map[string]int{}
	stkMarkersAgainstPosition(doc.Root, out)
	want := map[string]int{"disagrees": 1, "matches_position": 1, "no_marker": 1}
	for k, w := range want {
		if out[k] != w {
			t.Errorf("marker counter %s = %d, want %d (all: %v)", k, out[k], w, out)
		}
	}
	if len(out) != len(want) {
		t.Errorf("marker counter reported unexpected buckets: %v", out)
	}
}
