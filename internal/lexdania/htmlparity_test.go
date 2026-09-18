package lexdania

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This is the structural acceptance measurement for the payload reader: the paired
// corpus has the canonical tree and the payload side by side, so the sections the
// reader produces can be compared with the ones that are authoritative -- not in
// text, which TestPairedPayloadFidelity already covers, but as addresses, because
// an amendment resolves "§ 2, stk. 3" by structure and a paragraph that never had
// its stykker split is a law whose subsections cannot be cited.
//
// The comparison is a multiset of addresses rather than a diff of trees. Where the
// XML is a metadata-only stub the payload tree is *better* than the canonical
// document, which is the point of the backfill, and a tree diff would score that
// as a regression.

// sectionParityFile pins the measurement, checked in because the payloads it was
// taken from are gitignored.
const sectionParityFile = "testdata/paired-section-parity.txt"

// sectionAddress is the key an amendment resolves, in a form both trees can produce.
//
// It deliberately ignores the fields the two sides disagree about for reasons that
// are not losses: canonical <Stk> carries a localId about never (so its number is
// position), while a payload stykke prints "Stk. 2." in an Explicatus. Comparing
// RawNumber would report those as different provisions.
func sectionAddress(s *Section) string {
	switch s.Kind {
	case "stk":
		par := ""
		if s.Parent != nil {
			par = s.Parent.Number + s.Parent.Litra
		}
		return "§" + par + " stk" + strconv.Itoa(s.Stk)
	case "nr":
		par, stk := "", ""
		if s.Parent != nil {
			stk = strconv.Itoa(s.Parent.Stk)
			if s.Parent.Parent != nil {
				par = s.Parent.Parent.Number + s.Parent.Parent.Litra
			}
		}
		return "§" + par + " stk" + stk + " nr" + strconv.Itoa(s.Nr) + " " + s.Litra
	case "paragraf":
		return "§" + s.Number + s.Litra
	default:
		return s.Kind + " " + s.Number
	}
}

// parityCounts is one run's result, in the shape that gets pinned.
type parityCounts struct {
	Documents       int
	XMLBodyless     int // canonical file with no provisions at all: the payload adds structure
	XMLSections     int
	TreeSections    int
	AddressMatched  int // same address on both sides
	TextEqual       int // ... and the same text
	TextDiffers     int // same address, different text: the stykke boundaries are wrong
	MissingFromTree int // canonical has it, the tree does not: the reader lost it
	ExtraInTree     int // the tree has it, canonical does not: often a stub XML
	SequenceEqual   int // documents whose whole ordered address list agrees
}

func (c parityCounts) String() string {
	lines := []string{
		"documents=" + strconv.Itoa(c.Documents),
		"canonical_without_provisions=" + strconv.Itoa(c.XMLBodyless),
		"canonical_sections=" + strconv.Itoa(c.XMLSections),
		"tree_sections=" + strconv.Itoa(c.TreeSections),
		"addresses_matched=" + strconv.Itoa(c.AddressMatched),
		"matched_with_equal_text=" + strconv.Itoa(c.TextEqual),
		"matched_with_different_text=" + strconv.Itoa(c.TextDiffers),
		"missing_from_tree=" + strconv.Itoa(c.MissingFromTree),
		"extra_in_tree=" + strconv.Itoa(c.ExtraInTree),
		"documents_with_identical_address_order=" + strconv.Itoa(c.SequenceEqual),
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// residual describes what is left by kind, so a regression names itself instead of
// moving one aggregate number.
type residual struct {
	missing, extra, textDiffers map[string]int
}

// sectionsByKey groups sections by the address a caller would use to ask for them.
func sectionsByKey(secs []*Section) map[string][]*Section {
	out := map[string][]*Section{}
	for _, s := range secs {
		key := sectionAddress(s)
		out[key] = append(out[key], s)
	}
	return out
}

// comparePair folds one document's canonical and payload-tree sections into the
// tallies.
//
// Extracted from the pilot measurement rather than rewritten for the corpus run: if
// the two scales disagreed about what "the same address" means, the larger number
// would stop being the same measurement, and the larger number is the reason for
// doing it at all.
func comparePair(want, got []*Section, counts *parityCounts, res *residual) (map[string][]*Section, map[string][]*Section) {
	counts.XMLSections += len(want)
	counts.TreeSections += len(got)
	if len(want) == 0 {
		counts.XMLBodyless++
	}
	w, g := sectionsByKey(want), sectionsByKey(got)
	for key, wsec := range w {
		gsec := g[key]
		n := len(wsec)
		if len(gsec) < n {
			n = len(gsec)
		}
		counts.AddressMatched += n
		for i := 0; i < n; i++ {
			if nodeComparisonText(wsec[i].Node) == nodeComparisonText(gsec[i].Node) {
				counts.TextEqual++
			} else {
				counts.TextDiffers++
				res.textDiffers[wsec[i].Kind]++
			}
		}
		if d := len(wsec) - n; d > 0 {
			counts.MissingFromTree += d
			// Same address, same kind: any entry names the kind the report needs.
			res.missing[wsec[0].Kind] += d
		}
	}
	for key, gsec := range g {
		if d := len(gsec) - len(w[key]); d > 0 {
			counts.ExtraInTree += d
			res.extra[gsec[0].Kind] += d
		}
	}
	if strings.Join(addressList(want), "|") == strings.Join(addressList(got), "|") {
		counts.SequenceEqual++
	}
	return w, g
}

// add folds another run's totals into this one, for per-stratum reporting.
func (c *parityCounts) add(o parityCounts) {
	c.Documents += o.Documents
	c.XMLBodyless += o.XMLBodyless
	c.XMLSections += o.XMLSections
	c.TreeSections += o.TreeSections
	c.AddressMatched += o.AddressMatched
	c.TextEqual += o.TextEqual
	c.TextDiffers += o.TextDiffers
	c.MissingFromTree += o.MissingFromTree
	c.ExtraInTree += o.ExtraInTree
	c.SequenceEqual += o.SequenceEqual
}

// missingRate is the number the ranking is ordered by: what fraction of the
// canonical provisions of this shape the reader failed to address.
func (c parityCounts) missingRate() float64 {
	if c.XMLSections == 0 {
		return 0
	}
	return 100 * float64(c.MissingFromTree) / float64(c.XMLSections)
}

func measurePairedSectionParity(t *testing.T) (parityCounts, residual) {
	t.Helper()
	dir := payloadDir(t)
	archive := filepath.Join("..", "..", "data", "xml")
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Skipf("no payloads to measure: %v", err)
	}
	sort.Strings(paths)

	var counts parityCounts
	res := residual{missing: map[string]int{}, extra: map[string]int{}, textDiffers: map[string]int{}}

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
			continue
		}
		doc, err := Parse(xmlRaw)
		if err != nil {
			continue
		}
		body, err := ParseHTMLBody(payloadHTML(t, payloadPath))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		want, got := doc.Sections(), sectionsIn(body.Root)
		counts.Documents++
		w, g := comparePair(want, got, &counts, &res)

		// Per document, because the two failure shapes need different fixes: an
		// address missing on one side and extra on the other is the same section
		// numbered differently by two readers, while an address missing alone is a
		// provision the reader never built.
		t.Logf("%-22s canonical=%-4d tree=%-4d missing=%-3d extra=%-3d textDiffers=%-3d stkMarkers=%s",
			stem, len(want), len(got),
			countMissing(w, g), countExtra(w, g), countTextDiffers(w, g, t),
			payloadMarkerKind(payloadHTML(t, payloadPath)))
	}
	return counts, res
}

// payloadMarkerKind reports how the payload prints paragraph numbers, which is the
// difference between the modern and the legacy generation.
func payloadMarkerKind(html string) string {
	switch {
	case strings.Contains(html, "ParagrafNr"):
		return "span.ParagrafNr"
	case strings.Contains(html, "PARAB"), strings.Contains(html, "Parab"):
		return "bold"
	default:
		return "none"
	}
}

func countMissing(w, g map[string][]*Section) int {
	n := 0
	for key, wsec := range w {
		if d := len(wsec) - len(g[key]); d > 0 {
			n += d
		}
	}
	return n
}

func countExtra(w, g map[string][]*Section) int {
	n := 0
	for key, gsec := range g {
		if d := len(gsec) - len(w[key]); d > 0 {
			n += d
		}
	}
	return n
}

func countTextDiffers(w, g map[string][]*Section, _ *testing.T) int {
	n := 0
	for key, wsec := range w {
		gsec := g[key]
		for i := 0; i < len(gsec) && i < len(wsec); i++ {
			if nodeComparisonText(wsec[i].Node) != nodeComparisonText(gsec[i].Node) {
				n++
			}
		}
	}
	return n
}

func addressList(secs []*Section) []string {
	out := make([]string, 0, len(secs))
	for _, s := range secs {
		out = append(out, sectionAddress(s))
	}
	return out
}

func TestPairedSectionParity(t *testing.T) {
	counts, res := measurePairedSectionParity(t)
	t.Logf("\n%s", counts)
	t.Logf("missing by kind: %s", formatResidual(res.missing))
	t.Logf("extra by kind: %s", formatResidual(res.extra))
	t.Logf("text differs by kind: %s", formatResidual(res.textDiffers))

	// A run over no paired documents matches the baseline trivially and means
	// nothing, so it fails rather than passing quietly.
	if counts.Documents == 0 {
		t.Fatal("found payloads but no paired documents: the parity measurement is vacuous")
	}
	if counts.XMLSections == 0 {
		t.Fatal("paired documents yielded no canonical sections to compare")
	}
	// Missing addresses are reported rather than forbidden. Three classes remain and
	// each is a separate decision: chapter numbers the payload prints only in the
	// heading beside the chapter (kapitel 217 missing, 222 extra: the same chapters
	// read differently by two trees), centred provisions whose canonical element is a
	// provision but whose payload class the mapping files under prose (that is where
	// most of the remaining stk/paragraf residual lives), and quoted stykker inside an
	// amendment's proposed text, which canonical excludes on purpose. What is not
	// allowed is drift: the pinned baseline below fails the moment any of these
	// numbers moves, in either direction.
	if counts.MissingFromTree != 0 {
		t.Logf("%d canonical addresses still absent from the payload tree (%.1f%% of %d): %s",
			counts.MissingFromTree, 100*float64(counts.MissingFromTree)/float64(counts.XMLSections),
			counts.XMLSections, formatResidual(res.missing))
	}
	t.Logf("addresses resolved identically with identical text: %d of %d canonical (%.1f%%)",
		counts.TextEqual, counts.XMLSections, 100*float64(counts.TextEqual)/float64(counts.XMLSections))

	raw, err := os.ReadFile(sectionParityFile)
	if err != nil {
		t.Logf("no baseline at %s: write one from this run to pin the measurement", sectionParityFile)
		return
	}
	if strings.TrimSpace(counts.String()) != strings.TrimSpace(string(raw)) {
		t.Errorf("parity differs from the pinned baseline\nbaseline:\n%s\nnow:\n%s", strings.TrimSpace(string(raw)), counts)
	}
}

func formatResidual(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+strconv.Itoa(m[k]))
	}
	return strings.Join(parts, " ")
}
