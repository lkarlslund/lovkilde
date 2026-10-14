package compiled

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/lovkilde/internal/lexdania"
	"github.com/lkarlslund/lovkilde/internal/lexdoc"
)

// TestArtifactAnswersWhatTheParseAnswers is the gate that lets serving read the artifact instead of
// parsing XML: for real documents of every medium, the artifact's answer has to be the same answer.
//
// It compares four things, because they are the four ways a serving answer is built and each can
// break alone: the whole model, the text rendered for every address the search index would cite, the
// dates and version graph an as-of answer reads, and the provenance and warnings a reader is shown.
// Comparing the whole model would subsume the rest, but a gate that reports only "not equal" over a
// 200 KB struct teaches nobody what broke, so each is checked and named.
//
// Run it with:
//
//	RETSIN_ARTIFACT_PARITY=1 go test ./internal/compiled -run TestArtifactAnswersWhatTheParseAnswers -v
func TestArtifactAnswersWhatTheParseAnswers(t *testing.T) {
	if os.Getenv("RETSIN_ARTIFACT_PARITY") == "" {
		t.Skip("set RETSIN_ARTIFACT_PARITY=1 to run; it reads the mirror and takes a couple of minutes")
	}
	root := os.Getenv("RETSIN_DATA_DIR")
	if root == "" {
		root = filepath.Join("..", "..", "data")
	}
	perKind := 8
	if v := os.Getenv("RETSIN_ARTIFACT_PARITY_PER_KIND"); v != "" {
		fmt.Sscanf(v, "%d", &perKind)
	}
	sample := samplePerKind(t, root, perKind)
	kinds := map[string]bool{}
	for _, s := range sample {
		kinds[s.kind] = true
	}
	if len(kinds) < 5 {
		t.Fatalf("only %d media in the sample (%v); the gate is meant to cover all seven shelves, not whichever sort came first",
			len(kinds), sortedKeys(kinds))
	}
	t.Logf("sampling %d documents across %d media: %v", len(sample), len(kinds), sortedKeys(kinds))

	docs := make([]ArtifactDoc, 0, len(sample))
	parsed := make([]*lexdania.Document, 0, len(sample))
	for _, s := range sample {
		doc, err := lexdania.Parse(s.xml)
		if err != nil {
			t.Fatalf("%s: %v", s.path, err)
		}
		model := doc.Model()
		if model.Identity.Accession == "" {
			t.Fatalf("%s: model has no accession", s.path)
		}
		docs = append(docs, ArtifactDoc{Accession: model.Identity.Accession, Document: model})
		parsed = append(parsed, doc)
	}
	path := filepath.Join(t.TempDir(), "parity.bin")
	if err := writeArtifactFile(t, path, root, docs); err != nil {
		t.Fatal(err)
	}
	a, err := OpenArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if len(a.Documents()) != len(docs) {
		t.Fatalf("the artifact holds %d of %d documents", len(a.Documents()), len(docs))
	}

	var addressChecks, provisionChecks int
	for i, s := range sample {
		acc := docs[i].Accession
		want := parsed[i].Model() // fresh, from the parse the server does today
		got, err := a.Document(acc)
		if err != nil {
			t.Fatalf("%s (%s): %v", s.kind, acc, err)
		}

		// 1. The whole model.
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s %s: the artifact's model differs from the parse", s.kind, acc)
		}

		// 2. Every address the index would cite for this document, answered through the model.
		// Addresses come from the shipped unitiser rather than a fixture list, so the gate covers
		// the forms that actually reach a lookup: sections the index cites, and nothing the index
		// cannot name.
		for _, section := range parsed[i].Sections() {
			addr := lexdania.AddressOf(section)
			if addr == "" {
				continue
			}
			wantText, wantOK := want.Content.MarkdownProvisions(lexdoc.ParseAddress(addr))
			gotText, gotOK := got.Content.MarkdownProvisions(lexdoc.ParseAddress(addr))
			addressChecks++
			if wantOK != gotOK {
				t.Errorf("%s %s %q: parsed resolves it, artifact does not (or the reverse): %v vs %v",
					s.kind, acc, addr, wantOK, gotOK)
				continue
			}
			if wantText != gotText {
				t.Errorf("%s %s %q: answered text differs\n parsed: %q\nartifact: %q",
					s.kind, acc, addr, cut(wantText, 200), cut(gotText, 200))
			}
		}

		// 3. What an as-of answer reads: the dates, the version graph, and whether the changing act
		// is present. These decide which text a reader is told was in force, so they are named.
		if got.Identity.Dates != want.Identity.Dates {
			t.Errorf("%s %s: dates differ: %+v vs %+v", s.kind, acc, got.Identity.Dates, want.Identity.Dates)
		}
		if !reflect.DeepEqual(got.Identity.AmendedBy, want.Identity.AmendedBy) {
			t.Errorf("%s %s: AmendedBy differs: %+v vs %+v", s.kind, acc, got.Identity.AmendedBy, want.Identity.AmendedBy)
		}
		if !reflect.DeepEqual(got.Identity.Concerns, want.Identity.Concerns) {
			t.Errorf("%s %s: Concerns differ: %+v vs %+v", s.kind, acc, got.Identity.Concerns, want.Identity.Concerns)
		}

		// 4. Provenance and warnings, which are what an answer shows about itself.
		if got.Provenance != want.Provenance {
			t.Errorf("%s %s: provenance differs: %+v vs %+v", s.kind, acc, got.Provenance, want.Provenance)
		}
		if !reflect.DeepEqual(got.Diagnostics.Warnings, want.Diagnostics.Warnings) {
			t.Errorf("%s %s: warnings differ: %v vs %v", s.kind, acc, got.Diagnostics.Warnings, want.Diagnostics.Warnings)
		}
		provisionChecks += len(got.Content.Provisions())
	}

	// The coverage claim cuts both ways: an artifact that answered for a document it does not hold
	// would be worse than an empty file, because the answer would look authoritative.
	if _, err := a.Document("A29990000009"); err == nil || !strings.Contains(err.Error(), "not in this artifact") {
		t.Errorf("a document outside the artifact answered: %v", err)
	}
	t.Logf("%d documents, %d provision units, %d index-cited addresses answered identically across %d media",
		len(sample), provisionChecks, addressChecks, len(kinds))
	if addressChecks < len(sample) {
		t.Errorf("only %d addresses were compared over %d documents, so the address claim proved little", addressChecks, len(sample))
	}
}

// kindSampleDoc is one document with the shelf it came from attached, because the gate's claim is
// about coverage of all seven media and a flat list cannot show that.
type kindSampleDoc struct {
	kind string
	path string
	xml  []byte
}

// samplePerKind takes up to n documents from each medium directory, sorted, so the sample is the
// same run to run and a medium that exists is never passed over by a stride that landed elsewhere.
func samplePerKind(t *testing.T, root string, n int) []kindSampleDoc {
	t.Helper()
	kinds, err := os.ReadDir(filepath.Join(root, "xml"))
	if err != nil {
		t.Skipf("no mirror at %s: %v", root, err)
	}
	var out []kindSampleDoc
	for _, k := range kinds {
		if !k.IsDir() {
			continue
		}
		var files []string
		err := filepath.WalkDir(filepath.Join(root, "xml", k.Name()), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() && strings.HasSuffix(path, ".xml") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Errorf("%s: %v", k.Name(), err)
			continue
		}
		sort.Strings(files)
		if len(files) == 0 {
			continue
		}
		stride := max(len(files)/n, 1)
		taken := 0
		for i := 0; i < len(files) && taken < n; i += stride {
			raw, err := os.ReadFile(files[i])
			if err != nil {
				continue
			}
			if _, err := lexdania.Parse(raw); err != nil {
				continue // documents the parser refuses are not the serving path's problem
			}
			out = append(out, kindSampleDoc{kind: k.Name(), path: files[i], xml: raw})
			taken++
		}
		if taken < n {
			t.Errorf("medium %s contributed %d of %d documents", k.Name(), taken, n)
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cut shortens a string for a test log, marking that it was shortened.
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// TestArtifactReadCosts measures what the read path costs, because the artifact's whole
// justification is speed and a latency claim without a number is not done. It reports the three
// numbers that matter: the parse the artifact replaces, a cold artifact read, and a read from the
// byte-bounded cache, plus the cache's hit rate so the bound can be argued about rather than tuned
// by feel.
//
//	RETSIN_ARTIFACT_PARITY=1 go test ./internal/compiled -run TestArtifactReadCosts -v
func TestArtifactReadCosts(t *testing.T) {
	if os.Getenv("RETSIN_ARTIFACT_PARITY") == "" {
		t.Skip("set RETSIN_ARTIFACT_PARITY=1 to run")
	}
	root := os.Getenv("RETSIN_DATA_DIR")
	if root == "" {
		root = filepath.Join("..", "..", "data")
	}
	sample := samplePerKind(t, root, 12)

	docs := make([]ArtifactDoc, 0, len(sample))
	var xmlBytes int
	for _, s := range sample {
		parsed, err := lexdania.Parse(s.xml)
		if err != nil {
			t.Fatal(err)
		}
		model := parsed.Model()
		docs = append(docs, ArtifactDoc{Accession: model.Identity.Accession, Document: model})
		xmlBytes += len(s.xml)
	}
	path := filepath.Join(t.TempDir(), "cost.bin")
	if err := writeArtifactFile(t, path, root, docs); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// The path being replaced: parse the publisher's XML on every lookup, as serving does today.
	var parse time.Duration
	for _, s := range sample {
		start := time.Now()
		if _, err := lexdania.Parse(s.xml); err != nil {
			t.Fatal(err)
		}
		parse += time.Since(start)
	}

	a, err := OpenArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var cold time.Duration
	for _, d := range docs {
		start := time.Now()
		if _, err := a.Document(d.Accession); err != nil {
			t.Fatal(err)
		}
		cold += time.Since(start)
	}

	// The same reads with the cache warm, held to the bytes the serving default would carry.
	budget := int64(64 << 20)
	c, err := OpenCached(path, budget)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, d := range docs {
		if _, err := c.Document(d.Accession); err != nil {
			t.Fatal(err)
		}
	}
	var warm time.Duration
	for _, d := range docs {
		start := time.Now()
		if _, err := c.Document(d.Accession); err != nil {
			t.Fatal(err)
		}
		warm += time.Since(start)
	}
	stats := c.Stats()

	perDoc := func(d time.Duration) time.Duration { return d / time.Duration(len(docs)) }
	var b strings.Builder
	fmt.Fprintf(&b, "# artifact read path measurement\n# generated: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "# reproduce: RETSIN_ARTIFACT_PARITY=1 go test ./internal/compiled -run TestArtifactReadCosts -v\n")
	fmt.Fprintf(&b, "# sample: %d documents from all media, %d bytes of publisher XML, artifact %d bytes\n\n",
		len(docs), xmlBytes, st.Size())
	fmt.Fprintf(&b, "  publisher xml parse:      %v per lookup  (the path this replaces)\n", perDoc(parse))
	fmt.Fprintf(&b, "  artifact read, cold:      %v per lookup  (%.1fx faster than parsing)\n", perDoc(cold), float64(parse)/float64(cold))
	fmt.Fprintf(&b, "  artifact read, cached:    %v per lookup  (%.1fx faster than parsing)\n", perDoc(warm), float64(parse)/float64(warm))
	fmt.Fprintf(&b, "  cache: budget %d bytes, holding %d bytes in %d entries, %d hits, %d misses, %d drops\n",
		stats.Budget, stats.UsedBytes, stats.Entries, stats.Hits, stats.Misses, stats.Drops)
	fmt.Fprintf(&b, "  note: a cached lookup still unmarshals, because a decoded document is mutable\n")
	fmt.Fprintf(&b, "  and may not be shared between concurrent requests; what caching skips is the\n")
	fmt.Fprintf(&b, "  seek, the frame and the decompression.\n")

	report := filepath.Join("testdata", "artifact-read-"+time.Now().Format("20060102")+".txt")
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + b.String() + "\nreport written to " + report)
}
