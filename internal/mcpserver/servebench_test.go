package mcpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/compiled"
	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/search"
)

// TestServingBaselineCost measures what a query costs on both read paths, over the mirror itself.
//
// The first version of this harness measured only the parse path, because its job was to record what
// the compiled artifact had to beat. Now that serving can read from the artifact, the two numbers
// have to come from the same run on the same sample: a speedup claimed against a measurement taken
// on another day, on another file cache, is a story. So this run queries the sample twice — once
// parsing the publisher's XML, once from an artifact built from those same documents in this run —
// and reports both, plus what the snippet path costs through each reader.
//
//	RETSIN_SERVE_BENCH=1 go test ./internal/mcpserver -run TestServingBaselineCost -v
//
// The fixture is the mirror rather than a hand-written list: every nth document in archive order,
// each looked up whole and once at an address the shipped unitiser produced for it. An address list
// someone typed by hand would flatter the measurement by only containing addresses that work.
//
// The second pass is also a parity gate: the answers have to hash the same. Speed is the artifact's
// argument, and it is not an argument for answering differently.
func TestServingBaselineCost(t *testing.T) {
	if os.Getenv("RETSIN_SERVE_BENCH") == "" {
		t.Skip("set RETSIN_SERVE_BENCH=1 to run; it reads the mirror")
	}
	root := os.Getenv("RETSIN_DATA_DIR")
	if root == "" {
		root = filepath.Join("..", "..", "data")
	}
	want := 1000
	if v := os.Getenv("RETSIN_SERVE_BENCH_DOCS"); v != "" {
		fmt.Sscanf(v, "%d", &want)
	}

	scanStart := time.Now()
	a, err := lexdania.Scan(t.Context(), filepath.Join(root, "xml"))
	if err != nil {
		t.Fatal(err)
	}
	scanCost := time.Since(scanStart)
	entries := a.Entries()
	if len(entries) < want {
		t.Fatalf("only %d entries in %s", len(entries), root)
	}

	// Pick the sample first, and derive an address for each from its own sections, before anything
	// is timed: choosing an address is not part of what a query costs. The same pass compiles the
	// sample into an artifact, so both paths are measured over exactly the same documents.
	stride := len(entries) / want
	type fixture struct {
		accession string
		address   string
		meta      search.DocMeta
	}
	var fixtures []fixture
	buildStart := time.Now()
	// A test binary carries no source stamp — the toolchain records them in main packages only — so
	// pose as one build, or every artifact written here is refused as unverifiable and the bench
	// measures nothing.
	stamp := compiled.BuildStamp{Revision: "servebench", Time: time.Now().Add(-time.Hour)}
	restoreStamp := compiled.CurrentBuild
	compiled.CurrentBuild = func() (compiled.BuildStamp, bool) { return stamp, true }
	t.Cleanup(func() { compiled.CurrentBuild = restoreStamp })

	artPath := filepath.Join(t.TempDir(), "sample.bin")
	f, err := os.Create(artPath)
	if err != nil {
		t.Fatal(err)
	}
	wr, err := compiled.NewWriter(f, filepath.Join(root, "xml"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(entries) && len(fixtures) < want; i += max(stride, 1) {
		e := entries[i]
		if e.Accession == "" {
			continue
		}
		doc, path, ok := a.Open(e.Accession)
		if !ok {
			continue
		}
		m := doc.Model()
		f := fixture{
			accession: e.Accession,
			meta: search.DocMeta{
				Accession: e.Accession, ELI: e.ELIPath(), Kind: e.Kind, Year: e.Year,
				Number: e.Number, Title: e.Title, Source: string(m.Provenance.Source), Path: path,
			},
		}
		if sections := doc.Sections(); len(sections) > 0 {
			f.address = search.AddressOf(sections[len(sections)/2])
		}
		if err := wr.Add(e.Accession, m); err != nil {
			t.Fatal(err)
		}
		fixtures = append(fixtures, f)
	}
	if len(fixtures) < want/2 {
		t.Fatalf("only %d usable documents", len(fixtures))
	}
	if err := wr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	buildCost := time.Since(buildStart)
	artStat, err := os.Stat(artPath)
	if err != nil {
		t.Fatal(err)
	}

	s := New(a, "https://www.retsinformation.dk", "bench")

	// What the artifact replaces, measured through the archive on the same sample.
	openParse := func() []time.Duration {
		var out []time.Duration
		for _, fx := range fixtures {
			start := time.Now()
			if doc, _, ok := a.Open(fx.accession); ok {
				doc.Model()
			}
			out = append(out, time.Since(start))
		}
		return out
	}

	// One pass over the sample through whatever the server is configured to read.
	run := func(label string) pass {
		var p pass
		p.unresolved = map[string][]string{}
		p.hashes = map[string]string{}
		for _, fx := range fixtures {
			if fx.address != "" {
				p.cited++
			}
			start := time.Now()
			res, err := s.Lookup(LookupRequest{Document: fx.accession})
			if err != nil {
				t.Errorf("%s (%s): %v", fx.accession, label, err)
				continue
			}
			p.whole = append(p.whole, time.Since(start))
			p.hashes[fx.accession] = hashText(res.Text)
			if res.Text != "" {
				p.answered++
			}
			if fx.address == "" {
				continue
			}
			start = time.Now()
			res, err = s.Lookup(LookupRequest{Document: fx.accession, Address: fx.address})
			if err != nil {
				// An address the shipped unitiser produced that lookup cannot open is a finding,
				// not a harness bug: a search hit whose address cannot be re-opened is a hit the
				// reader cannot read. There is no allowance to fall back on — over the sample every
				// address the unitiser emits now opens — so a citation that fails is a defect from
				// its first occurrence. The shapes stay named because each needed a different fix.
				sh := addressShape(fx.address)
				p.unresolved[sh] = append(p.unresolved[sh], fx.accession+" "+fx.address)
				continue
			}
			p.addressed = append(p.addressed, time.Since(start))
			p.hashes[fx.accession+"|"+fx.address] = hashText(res.Text)
			if res.Text != "" {
				p.withAddress++
			}
		}
		for shape, examples := range p.unresolved {
			t.Errorf("%d cited addresses of shape %q do not open through lookup (%s); first two: %v",
				len(examples), shape, label, examples[:min(len(examples), 2)])
		}
		return p
	}

	byParse := run("parse")

	if err := s.UseArtifact(artPath, int64(len(fixtures))*1<<20); err != nil {
		t.Fatal(err)
	}
	byArtifact := run("artifact")

	differences := 0
	for key, want := range byParse.hashes {
		if got, ok := byArtifact.hashes[key]; !ok || got != want {
			differences++
			if differences < 4 {
				t.Errorf("%s: the two read paths answered differently", key)
			}
		}
	}

	// The snippet path: what one hit's text costs through each reader. The index stores no text, so
	// a search answer pays this once per hit, and it is the second reason serving was parsing XML
	// twice per query.
	unitCost := func(label string, src search.Documents) ([]time.Duration, int) {
		texts := search.NewUnitTexts(src)
		var samples []time.Duration
		var got int
		for _, fx := range fixtures {
			if fx.address == "" {
				continue
			}
			start := time.Now()
			text := texts.Unit(fx.meta, fx.address)
			samples = append(samples, time.Since(start))
			if text != "" {
				got++
			}
		}
		return samples, got
	}
	xmlUnits, xmlUnitTexts := unitCost("xml reader", search.XMLDocuments{})
	artUnits, artUnitTexts := unitCost("artifact", artifactDocs{cache: s.docs})
	// Cached, because a page of ten hits often asks for the same document two or three times: the
	// cache's whole purpose is the second read of one document inside one query.
	artCached, artCachedTexts := unitCost("artifact warm", artifactDocs{cache: s.docs})

	// Snippet text has to agree across the two readers too, or the index would be grading proximity
	// against words the answer does not have.
	snippetDifferences := 0
	{
		parseTexts := search.NewUnitTexts(search.XMLDocuments{})
		artifactTexts := search.NewUnitTexts(artifactDocs{cache: s.docs})
		for _, fx := range fixtures {
			if fx.address == "" {
				continue
			}
			if parseTexts.Unit(fx.meta, fx.address) != artifactTexts.Unit(fx.meta, fx.address) {
				snippetDifferences++
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# serving path cost: parsing the publisher's XML vs the compiled artifact\n# generated: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "# reproduce: RETSIN_SERVE_BENCH=1 RETSIN_SERVE_BENCH_DOCS=%d go test ./internal/mcpserver -run TestServingBaselineCost -v\n", want)
	fmt.Fprintf(&b, "# mirror: %d entries scanned in %v; %d documents queried, %d answered text\n",
		len(entries), scanCost.Round(time.Millisecond), len(byParse.whole), byParse.answered)
	fmt.Fprintf(&b, "\n## by parse (the path serving had)\n")
	fmt.Fprintf(&b, "  open+parse+model (the share the artifact removes): %s\n", percentiles(openParse()))
	fmt.Fprintf(&b, "  whole-document lookup:                            %s\n", percentiles(byParse.whole))
	fmt.Fprintf(&b, "  addressed lookup:                                 %s\n", percentiles(byParse.addressed))
	fmt.Fprintf(&b, "  one hit's unit text (snippet, proximity):         %s\n", percentiles(xmlUnits))
	fmt.Fprintf(&b, "\n## by artifact (the path serving has when one is built)\n")
	fmt.Fprintf(&b, "  whole-document lookup:                            %s\n", percentiles(byArtifact.whole))
	fmt.Fprintf(&b, "  addressed lookup:                                 %s\n", percentiles(byArtifact.addressed))
	fmt.Fprintf(&b, "  one hit's unit text, cold:                        %s\n", percentiles(artUnits))
	fmt.Fprintf(&b, "  one hit's unit text, already read this query:     %s\n", percentiles(artCached))
	fmt.Fprintf(&b, "  artifact: %d documents, %.1f MB, built in %v; cache held %d payloads in %s of %s, %d hits, %d misses, %d drops\n",
		len(fixtures), float64(artStat.Size())/1e6, buildCost.Round(time.Millisecond),
		s.docs.Stats().Entries, humanBytes(s.docs.Stats().UsedBytes), humanBytes(s.docs.Stats().Budget),
		s.docs.Stats().Hits, s.docs.Stats().Misses, s.docs.Stats().Drops)
	fmt.Fprintf(&b, "\n## the difference, same run and same sample\n")
	fmt.Fprintf(&b, "  whole-document lookup: p50 %v -> %v (%.1fx), mean %v -> %v (%.1fx)\n",
		p50(byParse.whole), p50(byArtifact.whole), ratio(p50(byParse.whole), p50(byArtifact.whole)),
		mean(byParse.whole), mean(byArtifact.whole), ratio(mean(byParse.whole), mean(byArtifact.whole)))
	fmt.Fprintf(&b, "  addressed lookup:      p50 %v -> %v (%.1fx), mean %v -> %v (%.1fx)\n",
		p50(byParse.addressed), p50(byArtifact.addressed), ratio(p50(byParse.addressed), p50(byArtifact.addressed)),
		mean(byParse.addressed), mean(byArtifact.addressed), ratio(mean(byParse.addressed), mean(byArtifact.addressed)))
	fmt.Fprintf(&b, "  unit text per hit:     p50 %v -> %v cold (%.1fx), %v when the same document was already read this query\n",
		p50(xmlUnits), p50(artUnits), ratio(p50(xmlUnits), p50(artUnits)), p50(artCached))
	// The payload cache's own share, stated rather than assumed: it saves the seek and the frame
	// decompression, and on documents this size that is a fraction of decoding the JSON, so the
	// number is small and says so. Claiming the cache is where the speed came from would be wrong.
	fmt.Fprintf(&b, "  payload cache saves the seek and one frame decompression: %.2fµs of the %.0fµs an artifact read costs.\n    On documents this size the win is over the XML parse, not over the cache.\n",
		(mean(artUnits)-mean(artCached)).Seconds()*1e6, mean(artUnits).Seconds()*1e6)
	fmt.Fprintf(&b, "  answers differing between the two paths: %d of %d, snippets: %d of %d\n",
		differences, len(byParse.hashes), snippetDifferences, artUnitTexts)
	// What did not move, so that nobody reads this file as the whole serving path having moved: an
	// as-of answer still consolidates through the tree engine and reads the XML it splices into. That
	// is M012T011's decision to make, not this measurement's to hide.
	fmt.Fprintf(&b, "  not moved: an as_of answer still consolidates and parses, pending M012T011\n")
	if differences > 0 {
		t.Errorf("the artifact answered %d queries differently from the parse", differences)
	}
	if snippetDifferences > 0 {
		t.Errorf("the artifact's snippet text differs from the parse's in %d units", snippetDifferences)
	}
	if artUnitTexts != xmlUnitTexts || artCachedTexts != xmlUnitTexts {
		t.Errorf("one reader produced text for %d units and the other for %d", xmlUnitTexts, artUnitTexts)
	}

	// The finding the latency numbers would otherwise hide: the index's cited address is how a hit
	// becomes a readable answer. Both paths must open every citation, so this is asserted in each
	// pass above and stated here as a rate.
	failed := len(flattenUnresolved(byArtifact.unresolved))
	fmt.Fprintf(&b, "\n## addresses the shipped unitiser cites\n")
	fmt.Fprintf(&b, "  %d of %d queried documents yielded a cited address; %d of those (%.0f%%) do not open through lookup, on either path\n",
		byParse.cited, len(byParse.whole), failed, 100*float64(failed)/float64(max(byParse.cited, 1)))

	fmt.Fprintf(&b, "\n  startup index build is %v over %d documents, which is why it is rebuilt\n", scanCost.Round(time.Millisecond), len(entries))
	fmt.Fprintf(&b, "  at boot rather than cached; the cost a query feels is the read, and that\n")
	fmt.Fprintf(&b, "  is what the artifact moved.\n")

	report := filepath.Join("testdata", "serve-baseline-"+time.Now().Format("20060102")+".txt")
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + b.String() + "\nreport written to " + report)
}

// pass is one trip over the sample: the latencies, how many answers carried text, and a hash of each
// answer so two passes can be compared without holding two copies of the corpus in memory.
type pass struct {
	whole       []time.Duration
	addressed   []time.Duration
	answered    int
	withAddress int
	cited       int
	hashes      map[string]string
	unresolved  map[string][]string
}

// hashText fingerprints an answer.
func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// percentiles summarises a latency sample the way a reader of a report wants it: the typical case,
// the bad tail, and how many observations those came from.
func percentiles(samples []time.Duration) string {
	if len(samples) == 0 {
		return "no samples"
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	at := func(q float64) time.Duration {
		return sorted[min(int(float64(len(sorted)-1)*q), len(sorted)-1)]
	}
	return fmt.Sprintf("p50 %v, p95 %v, p99 %v, mean %v (n=%d)",
		at(0.50).Round(time.Microsecond), at(0.95).Round(time.Microsecond), at(0.99).Round(time.Microsecond),
		mean(samples), len(samples))
}

func p50(samples []time.Duration) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2].Round(time.Microsecond)
}

func mean(samples []time.Duration) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	var total time.Duration
	for _, d := range samples {
		total += d
	}
	return (total / time.Duration(len(samples))).Round(time.Microsecond)
}

// ratio is the speedup between two durations, or 0 when either is missing: a claim of improvement
// needs both numbers, and a division by an absent one is not a number.
func ratio(before, after time.Duration) float64 {
	if before == 0 || after == 0 {
		return 0
	}
	return float64(before) / float64(after)
}

func humanBytes(n int64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/1e6)
}

// addressShape reduces a cited address to the form that failed, so the report says what kind of
// thing is unresolvable rather than listing thousands of near-identical strings.
func addressShape(addr string) string {
	switch {
	case strings.TrimSpace(addr) == "":
		return "empty"
	case !strings.Contains(addr, "\u00a7") && strings.Contains(addr, "stk."):
		return "stykke with no paragraph"
	case strings.HasSuffix(addr, " ") || strings.HasSuffix(strings.TrimSpace(addr), "stk."):
		return "number missing after the word"
	case strings.HasPrefix(addr, "\u00a7"):
		return "paragraph number"
	default:
		return "other"
	}
}

func flattenUnresolved(m map[string][]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v...)
	}
	return out
}
