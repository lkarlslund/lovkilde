package compiled

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
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
	wr, err := newWriter(f, "/mirror", artifactVersion+1)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		if err := wr.Add(d.Accession, d.Document); err != nil {
			t.Fatal(err)
		}
	}
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
	// The header frame carries the build stamp, so a difference is expected inside that one frame
	// and nowhere else. Prove where it is rather than tolerating drift: the document table and the
	// document frames must be byte-identical, because a build that is not reproducible cannot be
	// diffed between nights.
	a2, err := OpenArtifact(twice)
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	ref, _ := a2.Ref("A20260000001")
	if ref.Offset == 0 || ref.Offset >= uint64(len(first)) {
		t.Fatalf("first document at offset %d of a %d byte file", ref.Offset, len(first))
	}
	if !bytes.Equal(first[ref.Offset:], second[ref.Offset:]) {
		t.Errorf("everything after the header frame differs between two builds of the same documents, first at byte %d",
			firstDiffByte(first[ref.Offset:], second[ref.Offset:]))
	}
	if a2.Header.Created.Equal(a.Header.Created) {
		t.Log("both builds carry the same stamp, so the byte comparison above proved less than it looks like")
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
		len(sample), mb(float64(totalXML(sample))), root)

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

	// The container must still be a plain zstd stream: one decode of the content frames has to
	// produce the header, every document and the document table, in order. That is what makes the
	// seek table an addition rather than a private format, and it is checked rather than asserted.
	plain, err := decodeStream(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != jsonBytes+len(mustMarshalHeader(t, a.Header))+len(mustMarshalRefs(t, a)) {
		t.Errorf("the whole stream is %d bytes, the frames add up to %d", len(plain),
			jsonBytes+len(mustMarshalHeader(t, a.Header))+len(mustMarshalRefs(t, a)))
	} else {
		fmt.Fprintf(&b, "## container\n  %d content frames decode as one plain zstd stream to %d bytes, matching the frames exactly\n",
			len(models)+2, len(plain))
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
	dec, err := zstd.NewReader(nil)
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
