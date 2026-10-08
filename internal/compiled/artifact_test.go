package compiled

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/lkarlslund/retsinformation/internal/compiled/dictionary"
	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// firstDiffByte is the first index where two files differ, or -1 when they are equal.
func firstDiffByte(a, b []byte) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return -1
}

// TestArtifactRoundTrip pins the container's promises on a document with every shape the model
// carries: a nested part, provisions with subsections and conditions, footnote markers, the
// entry-into-force series, an annex, unmapped content, cites with effective dates, and a warning.
// Nothing here needs the mirror, so these promises are checked on every run rather than only when
// somebody with 12 GB of data remembers to ask.
func TestArtifactRoundTrip(t *testing.T) {
	docs := []ArtifactDoc{{Accession: "A20260000001", Document: fixtureDocument()}}
	path := filepath.Join(t.TempDir(), "artifact.bin")
	if err := writeArtifactFile(t, path, "", docs); err != nil {
		t.Fatal(err)
	}
	a, err := OpenArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if got := a.Documents(); len(got) != 1 || got[0] != "A20260000001" {
		t.Errorf("document table lists %v", got)
	}
	back, err := a.Document("A20260000001")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, docs[0].Document) {
		t.Errorf("the decoded document is not the one that went in:\n got: %s\nwant: %s",
			brief(back), brief(docs[0].Document))
	}
	// Equal in the sense that matters: the decoded thing answers the way the original does.
	if got, want := back.Content.Markdown(), docs[0].Document.Content.Markdown(); got != want {
		t.Errorf("markdown changed through the artifact:\n got:\n%s\nwant:\n%s", got, want)
	}
	if _, err := a.Document("A29990000009"); err == nil || !strings.Contains(err.Error(), "not in this artifact") {
		t.Errorf("a document the artifact does not hold answered anyway: %v", err)
	}
}

// The first build refused any document whose model JSON reached 8 MiB and died on the mirror's own
// largest documents. That size was an assumption about documents, not a limit of the container, so
// the document that used to be refused now has to go in and come back out unchanged, with the table
// describing where its frame sits.
func TestArtifactCarriesADocumentTheFirstBuildRefused(t *testing.T) {
	d := fixtureDocument()
	body := "Stk. 3. Der ydes tilskud til de formål der er beskrevet i bilaget, og som kan ændres ved styrelsens bekendtgørelse. "
	d.Content.Appendices = []lexdoc.Appendix{{
		Number: "9", Label: "Bilag 9", Heading: "Tillægsliste",
		Segments: []lexdoc.Segment{{Text: strings.Repeat(body, 80_000)}},
	}}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 8<<20 {
		t.Fatalf("the fixture is only %d bytes of JSON, which is not over the 8 MiB the first build refused", len(raw))
	}

	path := filepath.Join(t.TempDir(), "artifact.bin")
	if err := writeArtifactFile(t, path, "", []ArtifactDoc{{Accession: "A20260000001", Document: d}}); err != nil {
		t.Fatal(err)
	}
	a, err := OpenArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	ref, ok := a.Ref("A20260000001")
	if !ok {
		t.Fatal("the large document is not in the table")
	}
	if ref.Raw != uint64(len(raw)) {
		t.Errorf("the table records %d raw bytes, the document is %d", ref.Raw, len(raw))
	}
	back, err := a.Document("A20260000001")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, d) {
		got, _ := json.Marshal(back)
		t.Errorf("the large document did not come back as it went in: %d bytes out against %d in, first difference at byte %d",
			len(got), len(raw), firstDiffByte(got, raw))
	}
	if got, want := back.Content.Markdown(), d.Content.Markdown(); got != want {
		t.Errorf("markdown changed through the artifact (%d bytes against %d)", len(got), len(want))
	}
}

// The container's real boundary is the seek table's two uint32 sizes per frame, so the guard is
// tested where it lives rather than by writing four gigabytes into a temporary file.
func TestFrameSizeGuardMatchesTheSeekTablesSizes(t *testing.T) {
	if err := frameSizeFits("uncompressed", 1<<32-1); err != nil {
		t.Errorf("the largest size the table can describe was refused: %v", err)
	}
	for _, n := range []int{1 << 32, 1 << 33} {
		if err := frameSizeFits("uncompressed", n); err == nil {
			t.Errorf("a frame of %d bytes was accepted although the seek table stores uint32", n)
		} else if !strings.Contains(err.Error(), "uint32") {
			t.Errorf("the refusal does not name the limit it hit: %v", err)
		}
	}
}

// The build report prints the size distribution because a bound on a document's size is a claim
// about the corpus, and the last band is the one that says how many documents the old bound made
// impossible.
func TestSizeDistributionCountsDocumentsInTheirBands(t *testing.T) {
	wr, err := newWriter(io.Discard, "test", artifactVersion, dictionary.Current, Provenance{})
	if err != nil {
		t.Fatal(err)
	}
	sizes := []int{100, 1 << 12, 1 << 15, 1 << 17, 1 << 21, 1 << 23, 100 << 20}
	for _, n := range sizes {
		wr.recordSize(n)
	}
	bands, counts, largest, total := wr.SizeDistribution()
	if largest != 100<<20 {
		t.Errorf("largest = %d, want %d", largest, 100<<20)
	}
	var want uint64
	for _, n := range sizes {
		want += uint64(n)
	}
	if total != want {
		t.Errorf("total = %d, want %d", total, want)
	}
	if len(counts) != len(bands)+1 {
		t.Fatalf("%d counts for %d bands: the last band has to be open at the top", len(counts), len(bands))
	}
	var counted int64
	for _, c := range counts {
		counted += c
	}
	if counted != int64(len(sizes)) {
		t.Errorf("the bands hold %d documents, %d were recorded", counted, len(sizes))
	}
	if counts[len(counts)-1] != 1 {
		t.Errorf("the open band holds %d, want the one document over the last band", counts[len(counts)-1])
	}
}

// TestArtifactRefusesWhatItCannotRead: a half-written file, an artifact from another build, and an
// empty build must all fail loudly. Each is a way the artifact could otherwise serve a silently
// smaller mirror.
func TestArtifactRefusesWhatItCannotRead(t *testing.T) {
	docs := []ArtifactDoc{{Accession: "A20260000001", Document: fixtureDocument()}}
	dir := t.TempDir()

	// A build that found no documents must fail rather than finish: an empty artifact is how a
	// broken source path would quietly claim the whole mirror.
	empty := filepath.Join(dir, "empty.bin")
	ef, err := os.Create(empty)
	if err != nil {
		t.Fatal(err)
	}
	wr0, err := NewWriter(ef, "/nowhere")
	if err != nil {
		t.Fatal(err)
	}
	if err := wr0.Close(); err == nil || !strings.Contains(err.Error(), "empty artifact") {
		t.Errorf("an empty artifact was finished anyway: %v", err)
	}
	ef.Close()
	if _, err := OpenArtifact(empty); err == nil {
		t.Error("the unfinished artifact opened")
	}

	good := filepath.Join(dir, "good.bin")
	if err := writeArtifactFile(t, good, "/mirror", docs); err != nil {
		t.Fatal(err)
	}
	a, err := OpenArtifact(good)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	raw, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(dir, "short.bin")
	if err := os.WriteFile(truncated, raw[:len(raw)*3/4], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenArtifact(truncated); err == nil {
		t.Error("a truncated artifact opened")
	} else if !strings.Contains(err.Error(), "seek table") && !strings.Contains(err.Error(), "truncated") {
		// A file that lost its tail has no seek table in its last bytes, which is how the reader
		// finds it: that message is the honest detection, not a detour around it.
		t.Errorf("truncation refused for the wrong reason: %v", err)
	}

	other := filepath.Join(dir, "other-build.bin")
	f, err := os.Create(other)
	if err != nil {
		t.Fatal(err)
	}
	wr, err := newWriter(f, "/mirror", layerVersion+1, dictionary.Current, Provenance{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		if err := wr.Add(d.Accession, d.Document); err != nil {
			t.Fatal(err)
		}
	}
	wr.SetCatalogue([]byte("[]")) // no archive: an empty catalogue
	if err := wr.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := OpenArtifact(other); err == nil || !strings.Contains(err.Error(), "rebuild") {
		t.Errorf("an artifact from another build was not refused with a remedy: %v", err)
	}

	// A plain zstd stream is the file most likely to be pointed at by mistake: the mirror's
	// compressed XML, or the codec bench's blob. It decodes fine, so a reader that only tried to
	// decode would report garbage; the seek table is what tells the two apart.
	plain := filepath.Join(dir, "plain.zst")
	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write([]byte("not an artifact")); err != nil {
		t.Fatal(err)
	}
	enc.Close()
	if err := os.WriteFile(plain, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenArtifact(plain); err == nil || !strings.Contains(err.Error(), "not a retsin artifact") {
		t.Errorf("a plain zstd stream was not recognised as the wrong file: %v", err)
	}

	// Building twice from the same documents must give the same bytes: a build that is not
	// reproducible cannot be diffed between nights, and the only thing in the header that could
	// move is its own timestamp.
	twice := filepath.Join(dir, "twice.bin")
	if err := writeArtifactFile(t, twice, "/mirror", docs); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(twice)
	if err != nil {
		t.Fatal(err)
	}
	// The header carries the build's random stamp, which compresses to a length of its own, so
	// everything after it may sit a few bytes further along — and the document table, which holds
	// absolute offsets, may differ by exactly that. What must not differ is each document's frame:
	// that is what lets a new version of the published file re-use the old one's bytes.
	a1, err := OpenArtifact(good)
	if err != nil {
		t.Fatal(err)
	}
	defer a1.Close()
	a2, err := OpenArtifact(twice)
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	for _, acc := range a1.Documents() {
		r1, _ := a1.Ref(acc)
		r2, ok := a2.Ref(acc)
		if !ok || r1.Length != r2.Length {
			t.Fatalf("%s: frame lengths %d and %d", acc, r1.Length, r2.Length)
		}
		f1, f2 := first[r1.Offset:r1.Offset+r1.Length], second[r2.Offset:r2.Offset+r2.Length]
		if !bytes.Equal(f1, f2) {
			t.Errorf("%s: its frame differs between two builds of the same documents, first at byte %d", acc, firstDiffByte(f1, f2))
		}
	}
	if a1.Header.Stamp == "" || a1.Header.Stamp == a2.Header.Stamp {
		t.Errorf("two builds carry stamps %q and %q: every build must have its own", a1.Header.Stamp, a2.Header.Stamp)
	}
}

// writeArtifactFile builds to a temp name and renames, which is the archive's rule for derived
// data: a reader sees the artifact complete or not at all.
func writeArtifactFile(t *testing.T, path, source string, docs []ArtifactDoc) error {
	t.Helper()
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, _, err := Build(f, source, docs); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// fixtureDocument is a small law with the shapes the model has to carry through a codec: a chapter
// holding a named group holding a provision, provisions with subsections and numbered conditions,
// a footnote, the entry-into-force series, an annex with its own numbering, and content the model
// has no concept for.
func fixtureDocument() *lexdoc.Document {
	eff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	d := &lexdoc.Document{
		Identity: lexdoc.Identity{
			Accession: "A20260000001",
			Type:      "Bekendtgørelse",
			Family:    "BEK",
			Title:     "Bekendtgørelse om noget",
			Medium:    "lta", PublishedYear: 2026, PublishedNumber: 1,
			Dates:     lexdoc.Dates{Signed: eff, Promulgated: eff, AnnouncedIn: "Lovtidende A"},
			Concerns:  []lexdoc.Cite{{Accession: "A20200000001", Title: "Den concerne lov"}},
			AmendedBy: []lexdoc.Cite{{Accession: "A20270000001", Effective: eff}},
			Validity:  []lexdoc.Period{{From: eff}},
			Status:    lexdoc.StatusValid,
		},
		Provenance: lexdoc.Provenance{Source: "xml", Schema: "retsin.xsd"},
		Diagnostics: lexdoc.Diagnostics{
			Warnings: []string{"a warning the reader raised"},
		},
	}
	provision := func(number, printed, heading string, segs []lexdoc.Segment, stykker ...lexdoc.Stykke) *lexdoc.Provision {
		return &lexdoc.Provision{
			Number: number, Printed: printed, Heading: heading,
			Segments: segs, Stykker: stykker,
		}
	}
	groupProvision := provision("2", "§ 2.", "Formål", []lexdoc.Segment{
		{Text: "Formålet er at ", Break: false},
		{Text: "gøre noget."},
	}, lexdoc.Stykke{Nr: 1, Span: lexdoc.Span{From: 0, To: 2}, Items: []lexdoc.Item{
		{Nr: 1, Span: lexdoc.Span{From: 0, To: 1}},
		{Nr: 2, Span: lexdoc.Span{From: 1, To: 2}},
	}})
	lastProvision := provision("3", "§ 3.", "", []lexdoc.Segment{
		{Text: "Der ydes tilskud."},
		{Text: "Nota.", Note: "Par1_Stk1_Not1"},
	}, lexdoc.Stykke{Nr: 1, Span: lexdoc.Span{From: 0, To: 1}})
	d.Content.Order = []lexdoc.Node{
		{Part: &lexdoc.Part{
			Kind: lexdoc.PartChapter, Number: "1", Printed: "Kapitel 1", Heading: "Almindelige bestemmelser",
			Order: []lexdoc.Node{{Part: &lexdoc.Part{
				Kind: lexdoc.PartGroup, Number: "1", Heading: "Om formål",
				Order: []lexdoc.Node{{Provision: groupProvision}},
			}}},
		}},
		{Provision: lastProvision},
	}
	d.Content.Preamble = []lexdoc.Segment{{Text: "Der er fastsat følgende bestemmelser."}}
	d.Content.Title = []lexdoc.Segment{{Text: "Bekendtgørelse om noget"}}
	d.Content.Signature = []lexdoc.Segment{{Text: "P. K. V. Ministeren"}}
	d.Content.EntryIntoForce = []lexdoc.Provision{{
		Number: "1", Printed: "§ 1.",
		Segments: []lexdoc.Segment{{Text: "Bekendtgørelsen træder i kraft den 1. juli 2026."}},
	}}
	d.Content.Appendices = []lexdoc.Appendix{{
		Number: "1", Label: "Bilag 1", Heading: "Oversigt",
		Segments: []lexdoc.Segment{{Text: "En tabel."}},
		Unmapped: []lexdoc.Unmapped{{Concept: "table", Text: "a\tb"}},
	}}
	d.Content.Unmapped = []lexdoc.Unmapped{{Concept: "remarks", Text: "Sådan."}}
	return d
}

// brief renders a document as JSON, truncated: a round-trip failure has to show what differed
// without printing two megabytes of provisions into the test log.
func brief(d *lexdoc.Document) string {
	b, err := json.Marshal(d)
	if err != nil {
		return err.Error()
	}
	if len(b) > 1200 {
		b = b[:1200]
	}
	return string(b)
}

// TestArtifactOnCorpus measures the artifact on real documents and answers the two questions that
// decide whether it is worth having: does every document come back identical, and what does a
// lookup cost against the parse it replaces. Run it with:
//
//	RETSIN_ARTIFACT_BENCH=1 RETSIN_ARTIFACT_DOCS=1000 go test ./internal/compiled -run TestArtifactOnCorpus -v
//
// It writes a dated report into testdata, in the same tradition as codec-bench: the decisions this
// package makes are supposed to be traceable to a measurement, not to a preference.
func TestArtifactOnCorpus(t *testing.T) {
	if os.Getenv("RETSIN_ARTIFACT_BENCH") == "" {
		t.Skip("set RETSIN_ARTIFACT_BENCH=1 to run; it reads the mirror and takes a minute or two")
	}
	root := os.Getenv("RETSIN_DATA_DIR")
	if root == "" {
		root = filepath.Join("..", "..", "data")
	}
	want := 1000
	if v := os.Getenv("RETSIN_ARTIFACT_DOCS"); v != "" {
		fmt.Sscanf(v, "%d", &want)
	}
	sample := sampleDocuments(t, root, want)
	if len(sample) < 20 {
		t.Fatalf("only %d documents parsed from %s; the sample is not representative", len(sample), root)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# compiled artifact measurement\n# generated: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "# reproduce: RETSIN_ARTIFACT_BENCH=1 RETSIN_ARTIFACT_DOCS=%d go test ./internal/compiled -run TestArtifactOnCorpus -v\n", len(sample))
	fmt.Fprintf(&b, "# sample: %d documents, %.1f MB of publisher XML, from %s\n\n",
		len(sample), mb(float64(totalXML(sample))), filepath.Base(root))

	// What the artifact is built from, and what it costs to build. Model() is part of the build
	// cost, not of the serving cost: the compile step pays it once per document.
	models := make([]ArtifactDoc, 0, len(sample))
	jsonBytes := 0
	buildStart := time.Now()
	for _, s := range sample {
		doc, err := lexdania.Parse(s.xml)
		if err != nil {
			t.Fatalf("parse %s: %v", s.path, err)
		}
		model := doc.Model()
		if model.Identity.Accession == "" {
			t.Errorf("%s: the model has no accession, so the document table would have a hole", s.path)
		}
		if raw, err := json.Marshal(model); err != nil {
			t.Fatalf("%s: %v", s.path, err)
		} else {
			jsonBytes += len(raw)
		}
		models = append(models, ArtifactDoc{Accession: model.Identity.Accession, Document: model})
	}
	buildCost := time.Since(buildStart)

	path := filepath.Join(t.TempDir(), "artifact.bin")
	writeStart := time.Now()
	if err := writeArtifactFile(t, path, root, models); err != nil {
		t.Fatal(err)
	}
	writeCost := time.Since(writeStart)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&b, "## build\n")
	fmt.Fprintf(&b, "  parse+model+size check: %v (%v per document)\n", buildCost.Round(time.Millisecond), (buildCost / time.Duration(len(models))).Round(time.Microsecond))
	fmt.Fprintf(&b, "  container write:        %v (%v per document)\n", writeCost.Round(time.Millisecond), (writeCost / time.Duration(len(models))).Round(time.Microsecond))
	fmt.Fprintf(&b, "  json in the clear: %.1f MB; artifact on disk: %.1f MB (%.1f%% of the JSON, %.1f%% of the XML)\n",
		mb(float64(jsonBytes)), mb(float64(st.Size())),
		100*float64(st.Size())/float64(jsonBytes), 100*float64(st.Size())/float64(totalXML(sample)))
	fmt.Fprintf(&b, "  extrapolated to 191393 documents: %.0f MB\n\n", float64(st.Size())/float64(len(models))*191393/1e6)

	// The baseline the size cost is argued against: the XML itself, compressed.
	xmlZ := mustZstd(t, concatXML(sample))
	fmt.Fprintf(&b, "## the baseline it must justify itself against\n")
	fmt.Fprintf(&b, "  publisher xml: %.1f MB raw, %.1f MB zstd (%.1f%% of raw)\n\n",
		mb(float64(totalXML(sample))), mb(float64(len(xmlZ))), 100*float64(len(xmlZ))/float64(totalXML(sample)))

	a, err := OpenArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if len(a.Documents()) != len(models) {
		t.Errorf("the artifact holds %d of %d documents", len(a.Documents()), len(models))
	}

	// Every document back, compared to what went in. This is the whole reason a compiled form may
	// replace a parse: if it is not identical, it is a different document.
	var decode time.Duration
	mismatches := 0
	for _, d := range models {
		start := time.Now()
		back, err := a.Document(d.Accession)
		decode += time.Since(start)
		if err != nil {
			t.Errorf("%s: %v", d.Accession, err)
			continue
		}
		if !reflect.DeepEqual(back, d.Document) {
			if mismatches < 3 {
				t.Errorf("%s round-trip differs:\n got: %s\nwant: %s", d.Accession, brief(back), brief(d.Document))
			}
			mismatches++
		}
	}
	if mismatches > 0 {
		t.Fatalf("%d of %d documents did not survive the artifact unchanged", mismatches, len(models))
	}

	// What the path being replaced costs, over the same documents: parse the XML on every lookup.
	var parse time.Duration
	for _, s := range sample {
		start := time.Now()
		if _, err := lexdania.Parse(s.xml); err != nil {
			t.Fatal(err)
		}
		parse += time.Since(start)
	}
	fmt.Fprintf(&b, "## lookup\n")
	fmt.Fprintf(&b, "  artifact read+decode: %v total, %v per document\n", decode.Round(time.Millisecond), (decode / time.Duration(len(models))).Round(time.Microsecond))
	fmt.Fprintf(&b, "  xml parse today:      %v total, %v per document\n", parse.Round(time.Millisecond), (parse / time.Duration(len(models))).Round(time.Microsecond))
	fmt.Fprintf(&b, "  ratio: %.1fx faster per lookup\n\n", float64(parse)/float64(decode))

	// The container must still be a zstd stream: one decode of the content frames, given the
	// dictionary, has to produce the header, the dictionary, every document and the document
	// table, in order. That is what makes the seek table an addition rather than a private format,
	// and it is checked rather than asserted.
	plain, err := decodeStream(path)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := jsonBytes + len(mustMarshalHeader(t, a.Header)) + len(dictionary.Current) + len(mustMarshalRefs(t, a))
	if len(plain) != wantBytes {
		t.Errorf("the whole stream is %d bytes, the frames add up to %d", len(plain), wantBytes)
	} else {
		fmt.Fprintf(&b, "## container\n  %d content frames decode as one zstd stream (with the dictionary) to %d bytes, matching the frames exactly\n",
			len(models)+3, len(plain))
	}

	report := filepath.Join("testdata", "artifact-"+time.Now().Format("20060102")+".txt")
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + b.String() + "\nreport written to " + report)
}

// concatXML joins the sample's XML for the baseline that needs no new format at all.
func concatXML(s []sampleDoc) []byte {
	out := make([]byte, 0, totalXML(s))
	for _, x := range s {
		out = append(out, x.xml...)
	}
	return out
}

// decodeStream decodes the whole file with an ordinary zstd decoder: the skippable seek table has
// to be ignored by it, and the content frames have to concatenate into exactly the payloads that
// went in. If this ever fails, the artifact has become a private format.
func decodeStream(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// The document frames need the dictionary, as `zstd -d -D <dictionary>` would.
	dec, err := zstd.NewReader(nil, zstd.WithDecoderDicts(dictionary.Current))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	return dec.DecodeAll(raw, nil)
}

func mustMarshalHeader(t *testing.T, h ArtifactHeader) []byte {
	t.Helper()
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustMarshalRefs(t *testing.T, a *Artifact) []byte {
	t.Helper()
	refs := make([]DocRef, 0, len(a.order))
	for _, acc := range a.order {
		r, _ := a.Ref(acc)
		refs = append(refs, r)
	}
	b, err := json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestCacheStaysInsideItsBytes checks the two promises the cache makes: the bound is in bytes and
// is actually held, and a document handed to one caller cannot be changed by another. The second one
// is the reason the cache stores payloads rather than decoded models, so it is tested rather than
// stated: consolidation mutates a document in place, and an answer that quietly shared one would
// let two concurrent lookups edit each other's law.
func TestCacheStaysInsideItsBytes(t *testing.T) {
	var docs []ArtifactDoc
	for i := range 4 {
		d := fixtureDocument()
		acc := fmt.Sprintf("A2026000000%d", i)
		d.Identity.Accession = acc
		// Different content per document, so the payloads differ in size and eviction has a
		// preference to reveal.
		d.Content.Unmapped = []lexdoc.Unmapped{{Concept: "remarks", Text: strings.Repeat("x", 200*i)}}
		docs = append(docs, ArtifactDoc{Accession: acc, Document: d})
	}
	path := filepath.Join(t.TempDir(), "artifact.bin")
	if err := writeArtifactFile(t, path, "/mirror", docs); err != nil {
		t.Fatal(err)
	}
	base, err := OpenArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	sizes := map[string]int{}
	for _, d := range docs {
		raw, err := base.Payload(d.Accession)
		if err != nil {
			t.Fatal(err)
		}
		sizes[d.Accession] = len(raw)
	}

	c, err := OpenCached(path, int64(sizes[docs[0].Accession]*2))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Reading every document once must leave at most two payloads held, and never over the bound.
	for _, d := range docs {
		if _, err := c.Document(d.Accession); err != nil {
			t.Fatal(err)
		}
		if st := c.Stats(); st.UsedBytes > st.Budget {
			t.Fatalf("cache holds %d bytes over a budget of %d", st.UsedBytes, st.Budget)
		}
	}
	st := c.Stats()
	if st.Misses != int64(len(docs)) || st.Hits != 0 {
		t.Errorf("first pass over a cold cache: %d hits, %d misses", st.Hits, st.Misses)
	}
	if st.Entries > 2 {
		t.Errorf("budget of %d holds %d entries, which is more than its bytes allow (smallest payload %d)",
			st.Budget, st.Entries, sizes[docs[0].Accession])
	}
	// The last read must be the one still held: eviction is least-recently-used, not insertion order.
	if _, err := c.Document(docs[len(docs)-1].Accession); err != nil {
		t.Fatal(err)
	}
	if c.Stats().Hits != 1 {
		t.Errorf("the most recent document was not still cached: %+v", c.Stats())
	}

	// Mutating an answer must not reach the next reader of the same document.
	first, err := c.Document(docs[len(docs)-1].Accession)
	if err != nil {
		t.Fatal(err)
	}
	first.Identity.Title = "edited by this caller"
	first.Content.Unmapped[0].Text = "edited"
	second, err := c.Document(docs[len(docs)-1].Accession)
	if err != nil {
		t.Fatal(err)
	}
	if second.Identity.Title == "edited by this caller" || second.Content.Unmapped[0].Text == "edited" {
		t.Error("a cached document was shared between two readers")
	}

	// A document that cannot fit the budget is counted rather than silently never cached.
	tiny, err := OpenCached(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer tiny.Close()
	if _, err := tiny.Document(docs[0].Accession); err != nil {
		t.Fatal(err)
	}
	if tiny.Stats().Drops == 0 {
		t.Errorf("a 10-byte budget reported no drops for a %d byte document: %+v", sizes[docs[0].Accession], tiny.Stats())
	}
	if tiny.Stats().Entries != 0 {
		t.Error("the 10-byte budget holds something")
	}
}

// TestCacheConcurrentReads exists because -race is what notices that a lock was in the wrong place,
// and because the cache's cold path deliberately reads outside the lock.
func TestCacheConcurrentReads(t *testing.T) {
	var docs []ArtifactDoc
	for i := range 6 {
		d := fixtureDocument()
		acc := fmt.Sprintf("A202600001%d", i)
		d.Identity.Accession = acc
		docs = append(docs, ArtifactDoc{Accession: acc, Document: d})
	}
	path := filepath.Join(t.TempDir(), "artifact.bin")
	if err := writeArtifactFile(t, path, "/mirror", docs); err != nil {
		t.Fatal(err)
	}
	c, err := OpenCached(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for round := range 20 {
				acc := docs[(worker+round)%len(docs)].Accession
				doc, err := c.Document(acc)
				if err != nil {
					t.Errorf("%s: %v", acc, err)
					return
				}
				if doc.Identity.Accession != acc {
					t.Errorf("asked for %s, got %s", acc, doc.Identity.Accession)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	if st := c.Stats(); st.UsedBytes > st.Budget {
		t.Errorf("concurrent reads pushed the cache to %d of %d bytes", st.UsedBytes, st.Budget)
	}
}
