package crawl

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// canonicalVocabularyFile is the pinned list of element names the archive really
// uses. The sampler's needles are checked against it, because a stratum named for
// an element that does not exist selects documents that do not have the shape it
// claims to measure, and the resulting measurement looks authoritative.
const canonicalVocabularyFile = "../lexdania/testdata/canonical-elements.txt"

// bodyDoc is canonical XML with a body, carrying whatever elements a stratum is
// meant to find. Pad makes it large enough for the size-gated stratum.
func bodyDoc(t *testing.T, pad int, markers ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<Dokument>
  <Meta><AccessionNumber>A19800000011</AccessionNumber></Meta>
  <DokumentIndhold id="id1">
`)
	for _, m := range markers {
		fmt.Fprintf(&b, "    <%s id=\"id2\">Tekst.</%s>\n", m, m)
	}
	if pad > 0 {
		b.WriteString(strings.Repeat(" ", pad) + "\n")
	}
	b.WriteString("  </DokumentIndhold>\n</Dokument>\n")
	return b.String()
}

func TestSampleNeedlesNameRealCanonicalElements(t *testing.T) {
	raw, err := os.ReadFile(canonicalVocabularyFile)
	if err != nil {
		t.Fatalf("reading the pinned vocabulary: %v", err)
	}
	vocab := string(raw)
	for _, s := range Strata() {
		for _, needle := range s.Needles {
			name := strings.TrimPrefix(needle, "<")
			if !strings.Contains(vocab, name) {
				t.Errorf("stratum %s samples for <%s>, which is not in the canonical vocabulary; "+
					"the quota would then be filled with documents that do not have the shape", s.Name, name)
			}
		}
	}
}

func TestParseQuotasRejectsNonsense(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"centreret"}, "want name=count"},
		{[]string{"centreret=0"}, "positive whole number"},
		{[]string{"centreret=-3"}, "positive whole number"},
		{[]string{"centreret=lots"}, "positive whole number"},
		{[]string{"centret=10"}, "unknown stratum"},
		{[]string{"centreret=10", "centreret=5"}, "given twice"},
	}
	for _, tc := range cases {
		_, err := ParseQuotas(tc.in)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParseQuotas(%q) = %v, want an error mentioning %q", tc.in, err, tc.want)
		}
	}
}

// TestParseQuotasFillsInDeclaredOrder is the contention rule: typing random first
// must not let the largest bucket claim documents before the scarce shapes get
// theirs.
func TestParseQuotasFillsInDeclaredOrder(t *testing.T) {
	got, err := ParseQuotas([]string{"random=5", "centreret=3", "nota=2"})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, q := range got {
		order = append(order, fmt.Sprintf("%s=%d", q.Stratum.Name, q.Count))
	}
	want := "centreret=3 nota=2 random=5"
	if strings.Join(order, " ") != want {
		t.Errorf("fill order = %q, want %q", strings.Join(order, " "), want)
	}
}

// sampleFixture writes an archive slice with a known shape: two kinds, three decades,
// one stub, one alias, and a marker per stratum.
type sampleFixture struct {
	h *bodyHarness
	t *testing.T
}

func sampleFixtureWith(t *testing.T, handler http.HandlerFunc) *sampleFixture {
	h := bodyTester(t, handler)
	write := func(path, content string) { writeDoc(t, h.ar, h.eli(path), content) }
	write("lta/1980/1.xml", bodyDoc(t, 0, "AendringCentreretParagraf"))
	write("lta/1980/2.xml", bodyDoc(t, 0, "Kapitel"))
	write("ltb/1980/3.xml", bodyDoc(t, 0, "Nota"))
	write("ltb/1980/4.xml", bodyDoc(t, 0))
	write("lta/1930/5.xml", bodyDoc(t, 0, "Kapitel"))
	write("ltb/1930/6.xml", bodyDoc(t, 220*1024))
	write("lta/1985/7.xml", stubXML)          // no XML body: not eligible
	write("accn/2/1985/x.xml", bodyDoc(t, 0)) // alias: a second route to a document
	write("lta/1985/9.xml", bodyDoc(t, 0, "Table"))
	write("ltb/1986/10.xml", bodyDoc(t, 0, "Indentatio"))
	write("lta/1987/11.xml", bodyDoc(t, 0, "AendringsNummer"))
	write("lta/1955/12.xml", bodyDoc(t, 0)) // the only mid-century document
	return &sampleFixture{h: h, t: t}
}

func (f *sampleFixture) plan(t *testing.T, seed int64, specs ...string) (SamplePlanReport, []*SampleManifestRow) {
	t.Helper()
	return f.planKinds(t, seed, nil, specs...)
}

// planKinds is the same pass with --kind narrowing, which is how a series is
// sampled without waiting for everything that sorts before it.
func (f *sampleFixture) planKinds(t *testing.T, seed int64, kinds []string, specs ...string) (SamplePlanReport, []*SampleManifestRow) {
	t.Helper()
	quotas, err := ParseQuotas(specs)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "manifest.tsv")
	rep, err := f.h.c.PlanBodySample(context.Background(), SamplePlanOptions{
		Quotas: quotas, Seed: seed, Kinds: kinds, Out: out,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	rows, err := ReadSampleManifest(out)
	if err != nil {
		t.Fatalf("reading back the manifest: %v", err)
	}
	rep.Manifest = out
	return rep, rows
}

func rowPaths(rows []*SampleManifestRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ELIPath
	}
	return out
}

func hasPath(rows []*SampleManifestRow, p string) bool {
	return strings.Contains(strings.Join(rowPaths(rows), " "), p)
}

// TestPlanSkipsUnpairableDocuments is the premise of the whole tool: a document
// with no canonical body has no reference text, and an accession alias is the same
// document twice.
func TestPlanSkipsUnpairableDocuments(t *testing.T) {
	f := sampleFixtureWith(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	rep, rows := f.plan(t, 1, "random=50")
	if rep.Manifest == "" {
		t.Fatal("no manifest written")
	}
	if h := hasPath(rows, "lta/1985/7.xml"); h {
		t.Error("a document with no <DokumentIndhold> was sampled: it has no reference text to compare against")
	}
	if h := hasPath(rows, "accn/2/1985/x.xml"); h {
		t.Error("an accn alias was sampled: it would spend a second request on one document")
	}
	// Ten eligible documents: twelve files, minus the stub with no body and the
	// accn file that is a second route to a document rather than a second document.
	if len(rows) != 10 {
		t.Errorf("sampled %d rows (%v), want the fixture's 10 eligible documents", len(rows), rowPaths(rows))
	}
	if rep.Got["random"] != 10 {
		t.Errorf("random stratum took %d, want 10", rep.Got["random"])
	}
	if rep.Short["random"] != 40 {
		t.Errorf("shortfall = %d, want 40 reported rather than a silently smaller sample", rep.Short["random"])
	}
	if f.h.requests.Load() != 0 {
		t.Errorf("planning made %d requests, want 0", f.h.requests.Load())
	}
}

// TestPlanStratumSelectsByMarker checks that a marker quota lands on documents that
// really contain the marker -- read back off disk, not from the label the planner
// wrote -- and that a document claimed by two requested strata is fetched once and
// labelled twice. Counting it twice would inflate the frequency ranking the sample
// exists to produce.
//
// The quotas are chosen so the outcome is deterministic: two kapitel documents and
// two pre-1945 documents, one of which is in both sets.
func TestPlanStratumSelectsByMarker(t *testing.T) {
	f := sampleFixtureWith(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	rep, rows := f.plan(t, 3, "kapitel=2", "gammel=2", "nota=1", "stor=1", "mellem=1")

	byPath := map[string]*SampleManifestRow{}
	for _, r := range rows {
		byPath[r.ELIPath] = r
	}
	if len(rows) != 5 {
		t.Fatalf("sampled %v, want five documents: two kapitel, the second pre-1945 one, one nota, one mid-century", rowPaths(rows))
	}
	for _, name := range []string{"kapitel", "gammel", "nota"} {
		if rep.Got[name] == 0 {
			t.Errorf("stratum %s took nothing", name)
		}
	}
	if rep.Got["gammel"] != 2 {
		t.Errorf("gammel consumed %d quota units, want 2: one of them already on the list, which is why it needs no request", rep.Got["gammel"])
	}
	if _, ok := byPath["lta/1987/11.xml"]; ok {
		t.Errorf("an amendment document was sampled for quotas that never asked for one (%v)", rowPaths(rows))
	}
	// The document in both requested sets must carry both labels.
	both := byPath["lta/1930/5.xml"]
	if both == nil {
		t.Fatalf("the pre-1945 kapitel document is missing from %v", rowPaths(rows))
	}
	labels := strings.Join(both.Strata, ";")
	if !strings.Contains(labels, "kapitel") || !strings.Contains(labels, "gammel") {
		t.Errorf("%s labelled %q, want both kapitel and gammel. One label per document would hide that these shapes overlap",
			"lta/1930/5.xml", labels)
	}
	// Every label must be justified by the file's own bytes. The predicate is
	// re-implemented here rather than called: asking the code under test whether its
	// own label is justified is circular, because a mutation that relaxes the gate
	// relaxes the check in the same breath. That is exactly how the size gate
	// survived its first guard.
	for _, r := range rows {
		raw, err := os.ReadFile(f.h.ar.Path(f.h.eli(r.ELIPath)))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range r.Strata {
			s, found := lookupStratum(name)
			if !found {
				t.Errorf("%s labelled with unknown stratum %s", r.ELIPath, name)
				continue
			}
			if len(s.Needles) > 0 {
				matched := false
				for _, n := range s.Needles {
					if strings.Contains(string(raw), n) {
						matched = true
					}
				}
				if !matched {
					t.Errorf("%s labelled %s but contains none of %v", r.ELIPath, name, s.Needles)
				}
			}
			if s.MinKB > 0 && r.Bytes < int64(s.MinKB)*1024 {
				t.Errorf("%s labelled %s, which wants at least %d KB, but is %d bytes",
					r.ELIPath, name, s.MinKB, r.Bytes)
			}
			if s.MinYear > 0 && (r.Year <= 0 || r.Year < s.MinYear) {
				t.Errorf("%s labelled %s, which wants a year from %d, but is dated %d",
					r.ELIPath, name, s.MinYear, r.Year)
			}
			if s.MaxYear > 0 && (r.Year <= 0 || r.Year > s.MaxYear) {
				t.Errorf("%s labelled %s, which wants a year up to %d, but is dated %d",
					r.ELIPath, name, s.MaxYear, r.Year)
			}
		}
	}
}

// TestPlanDeterministicAndSeedSensitive is the reproducibility claim in the flag
// help: one seed, one sample.
func TestPlanDeterministicAndSeedSensitive(t *testing.T) {
	f := sampleFixtureWith(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// A larger pool than the fixture's ten, so a different seed picking the same
	// five is not merely likely but vanishingly unlikely.
	for i := 0; i < 40; i++ {
		writeDoc(f.t, f.h.ar, f.h.eli(fmt.Sprintf("lta/1990/%d.xml", i)), bodyDoc(t, 0))
	}
	a, arows := f.plan(t, 11, "random=5")
	_, again := f.plan(t, 11, "random=5")
	if strings.Join(rowPaths(arows), " ") != strings.Join(rowPaths(again), " ") {
		t.Errorf("seed 11 gave %v then %v: the same seed must give the same sample", rowPaths(arows), rowPaths(again))
	}
	if a.Seed != 11 {
		t.Errorf("report seed = %d, want the seed it was given", a.Seed)
	}
	_, brows := f.plan(t, 12, "random=5")
	if strings.Join(rowPaths(arows), " ") == strings.Join(rowPaths(brows), " ") {
		t.Errorf("seeds 11 and 12 gave the same 5 of 40 documents (%v): the seed is not reaching the shuffle", rowPaths(arows))
	}
}

// TestPlanRoundRobinsKinds is why selection is not one flat shuffle: an unstratified
// pick over a corpus dominated by one series cannot find a defect confined to
// another.
func TestPlanRoundRobinsKinds(t *testing.T) {
	f := sampleFixtureWith(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	for i := 0; i < 20; i++ {
		writeDoc(f.t, f.h.ar, f.h.eli(fmt.Sprintf("aaa/1980/%d.xml", i)), bodyDoc(t, 0))
	}
	for i := 0; i < 3; i++ {
		writeDoc(f.t, f.h.ar, f.h.eli(fmt.Sprintf("bbb/1980/%d.xml", i)), bodyDoc(t, 0))
	}
	// Restricted to the two synthetic kinds: the fixture's own documents are
	// candidates too, and leaving them in would measure the fixture rather than the
	// selection.
	_, rows := f.planKinds(t, 5, []string{"aaa", "bbb"}, "random=6")
	byKind := map[string]int{}
	for _, r := range rows {
		byKind[r.Kind]++
	}
	if byKind["bbb"] != 3 || byKind["aaa"] != 3 {
		t.Errorf("kind counts %v, want three of each: a flat shuffle takes the scarce series last, "+
			"which is how a kind-specific defect survives a sample", byKind)
	}
}

// TestStratumYearGateIgnoresUnknownYears pins the decade rule for a document whose
// year cannot be read from its path. Calling the predicate directly is not the
// circularity that sank the label check: there, the predicate was validating its own
// selection; here, the predicate is the thing under test.
//
// The single-bound strata are not hypothetical tidiness. A stratum with both bounds
// hides a patch to either one -- the surviving gate rejects an unknown year anyway,
// which is exactly why the first version of this guard could not be broken. Only a
// floor-only or ceiling-only stratum shows that each bound refuses an unknown year
// on its own.
func TestStratumYearGateIgnoresUnknownYears(t *testing.T) {
	s, ok := lookupStratum("mellem")
	if !ok {
		t.Fatal("no mid-century stratum to test")
	}
	cases := []struct {
		name  string
		strat SampleStratum
		want  map[int]bool
	}{
		{"mellem", s, map[int]bool{0: false, 1930: false, 1955: true, 1979: true, 1980: false}},
		{"floor only", SampleStratum{Name: "floor", MinYear: 1945},
			map[int]bool{0: false, 1930: false, 1945: true, 2024: true}},
		{"ceiling only", SampleStratum{Name: "ceiling", MaxYear: 1944},
			map[int]bool{0: false, 1930: true, 1944: true, 1945: false}},
	}
	for _, tc := range cases {
		for year, want := range tc.want {
			if got := tc.strat.matches(nil, year, 0); got != want {
				t.Errorf("%s.matches(year %d) = %v, want %v", tc.name, year, got, want)
			}
		}
	}
}

func TestPlanRequiresQuotasAndOut(t *testing.T) {
	f := sampleFixtureWith(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	if _, err := f.h.c.PlanBodySample(context.Background(), SamplePlanOptions{Out: filepath.Join(t.TempDir(), "m.tsv")}); err == nil {
		t.Error("a plan with no quotas succeeded: it would write an empty manifest and report a sample of nothing")
	}
	quotas, err := ParseQuotas([]string{"random=1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.c.PlanBodySample(context.Background(), SamplePlanOptions{Quotas: quotas}); err == nil {
		t.Error("a plan with no output path succeeded")
	}
}

func TestManifestRoundTripAndRejection(t *testing.T) {
	rows := []*SampleManifestRow{
		{ELIPath: "lta/1942/542", Kind: "lta", Year: 1942, Bytes: 3011, Strata: []string{"centreret", "gammel"}},
		{ELIPath: "ltb/1995/1", Kind: "ltb", Year: 1995, Bytes: 1, Strata: []string{"random"}},
	}
	out := filepath.Join(t.TempDir(), "m.tsv")
	if err := WriteSampleManifest(out, 7, rows); err != nil {
		t.Fatal(err)
	}
	back, err := ReadSampleManifest(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 || back[0].ELIPath != "lta/1942/542" || back[0].Year != 1942 || back[0].Bytes != 3011 {
		t.Fatalf("round trip lost data: %+v", back)
	}
	if strings.Join(back[0].Strata, ";") != "centreret;gammel" {
		t.Errorf("strata = %v, want both labels kept", back[0].Strata)
	}
	// A truncated manifest is not a smaller sample, it is a corrupted one: a
	// silently dropped column changes which documents the measurement rests on.
	// The four-column case matters most: it is the boundary, and a guard written as
	// "< 4" reads the same and only fails here.
	for _, bad := range []string{
		"# header\nlta/1942/542\tlta\t1942\n",
		"# header\nlta/1942/542\tlta\t1942\t3011\n",
		"# header\nlta/1942/542\tlta\tnotayear\t1\trandom\n",
		"# header\nlta/1942/542\tlta\t1942\tnotabytes\trandom\n",
		"# header only\n",
	} {
		p := filepath.Join(t.TempDir(), "bad.tsv")
		if err := os.WriteFile(p, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadSampleManifest(p); err == nil {
			t.Errorf("manifest %q read back without an error", bad)
		}
	}
}

// sampleServer answers the document JSON endpoint for any path.
func sampleServer(t *testing.T, payloads map[string]string, seen *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		want := "/api/document/eli/"
		if !strings.HasPrefix(r.URL.Path, want) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, want)
		*seen = append(*seen, path)
		body, ok := payloads[path]
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}
}

func manifestFor(dir string, rows []*SampleManifestRow) string {
	p := filepath.Join(dir, "m.tsv")
	if err := WriteSampleManifest(p, 1, rows); err != nil {
		panic(err)
	}
	return p
}

// TestFetchSampleWritesOutsideTheArchive is the containment rule for the whole tool:
// measurement payloads must not become archive content, because the loader's rule
// ("use the sidecar when the XML has no body") would then be ambiguous in exactly
// the documents written to test it.
func TestFetchSampleWritesOutsideTheArchive(t *testing.T) {
	var seen []string
	payloads := map[string]string{
		"lta/1980/1": bodyPayload(realBodyHTML),
		"lta/1980/2": bodyPayload(realBodyHTML),
		"lta/1980/3": bodyPayload(`<div class="bjelke">Tom</div>`),
	}
	f := sampleFixtureWith(t, sampleServer(t, payloads, &seen))
	rows := []*SampleManifestRow{
		{ELIPath: "lta/1980/1", Kind: "lta", Year: 1980, Bytes: 1, Strata: []string{"random"}},
		{ELIPath: "lta/1980/2", Kind: "lta", Year: 1980, Bytes: 1, Strata: []string{"random"}},
		{ELIPath: "lta/1980/3", Kind: "lta", Year: 1980, Bytes: 1, Strata: []string{"random"}},
	}
	out := t.TempDir()
	manifest := manifestFor(t.TempDir(), rows)
	st, err := f.h.c.FetchBodySample(context.Background(), SampleFetchOptions{Manifest: manifest, Dir: out})
	if err != nil {
		t.Fatal(err)
	}
	if st.Stored != 3 || st.Failed != 0 {
		t.Errorf("stats = %+v, want three stored", st)
	}
	if st.Empty != 1 {
		t.Errorf("empty = %d, want the payload with no prose flagged", st.Empty)
	}
	if got := filepath.Join(out, "lta", "1980", "1.json"); !fileExists(t, got) {
		t.Errorf("payload not at the mirrored path %s", got)
	}
	// The archive must not have gained a single file, and its XML must be untouched.
	var archiveJSON []string
	filepath.WalkDir(f.h.ar.Dir(), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), JSONExt) {
			archiveJSON = append(archiveJSON, p)
		}
		return nil
	})
	if len(archiveJSON) != 0 {
		t.Errorf("the archive gained sidecars %v: a payload beside a document that already has canonical XML "+
			"makes the loader's rule ambiguous in the files written to test it", archiveJSON)
	}
	// And the state store must have no body rows in either key form: a sampled
	// document is not a backfill result, and an assertion that only checks the one
	// spelling the bug happens not to use is not a containment check.
	got, err := f.h.st.Bodies(context.Background(), []string{f.h.eli("lta/1980/1"), "lta/1980/1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("sample fetch wrote state rows %+v: a sampled document is not a backfill result", got)
	}
}

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
	return err == nil
}

// TestFetchSampleResumesAndSurvivesFailures: an interrupted sample must not
// re-request what it already has, and one 500 must not abandon the tail of the
// manifest.
func TestFetchSampleResumesAndSurvivesFailures(t *testing.T) {
	var seen []string
	payloads := map[string]string{
		"lta/1980/1": bodyPayload(realBodyHTML),
		// lta/1980/2 is absent from the map: the server answers 500.
		"lta/1980/3": bodyPayload(realBodyHTML),
	}
	f := sampleFixtureWith(t, sampleServer(t, payloads, &seen))
	rows := []*SampleManifestRow{
		{ELIPath: "lta/1980/1", Kind: "lta", Year: 1980, Bytes: 1, Strata: []string{"random"}},
		{ELIPath: "lta/1980/2", Kind: "lta", Year: 1980, Bytes: 1, Strata: []string{"random"}},
		{ELIPath: "lta/1980/3", Kind: "lta", Year: 1980, Bytes: 1, Strata: []string{"random"}},
	}
	out := t.TempDir()
	manifest := manifestFor(t.TempDir(), rows)
	st, err := f.h.c.FetchBodySample(context.Background(), SampleFetchOptions{Manifest: manifest, Dir: out})
	if err != nil {
		t.Fatalf("a failing document must not abort the sample: %v", err)
	}
	if st.Stored != 2 || st.Failed != 1 {
		t.Errorf("stats = %+v, want two stored and one failed", st)
	}
	if !fileExists(t, filepath.Join(out, "lta", "1980", "3.json")) {
		t.Error("the document after the failure was not fetched: a run that stops on error discovers a biased tail")
	}
	before := len(seen)
	st, err = f.h.c.FetchBodySample(context.Background(), SampleFetchOptions{Manifest: manifest, Dir: out})
	if err != nil {
		t.Fatal(err)
	}
	if st.Held != 2 || st.Stored != 0 || st.Failed != 1 {
		t.Errorf("second run = %+v, want the two payloads on disk counted as held, nothing new stored, and only the failure retried", st)
	}
	if len(seen) != before+1 {
		t.Errorf("second run made %d requests, want 1: what is on disk must not be re-requested", len(seen)-before)
	}
	// --overwrite is the operator's escape hatch and must actually re-request.
	if _, err := f.h.c.FetchBodySample(context.Background(), SampleFetchOptions{Manifest: manifest, Dir: out, Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != before+1+3 {
		t.Errorf("--overwrite made %d requests, want all three rows re-fetched", len(seen)-before-1)
	}
}

func TestFetchSampleLimitAndArgumentChecks(t *testing.T) {
	var seen []string
	payloads := map[string]string{
		"lta/1980/1": bodyPayload(realBodyHTML),
		"lta/1980/2": bodyPayload(realBodyHTML),
	}
	f := sampleFixtureWith(t, sampleServer(t, payloads, &seen))
	rows := []*SampleManifestRow{
		{ELIPath: "lta/1980/1", Kind: "lta", Year: 1980, Bytes: 1},
		{ELIPath: "lta/1980/2", Kind: "lta", Year: 1980, Bytes: 1},
	}
	manifest := manifestFor(t.TempDir(), rows)
	if _, err := f.h.c.FetchBodySample(context.Background(), SampleFetchOptions{Manifest: manifest}); err == nil {
		t.Error("a fetch with no output directory succeeded: it would have to guess where measurement data goes")
	}
	if _, err := f.h.c.FetchBodySample(context.Background(), SampleFetchOptions{Dir: t.TempDir()}); err == nil {
		t.Error("a fetch with no manifest succeeded")
	}
	st, err := f.h.c.FetchBodySample(context.Background(), SampleFetchOptions{Manifest: manifest, Dir: t.TempDir(), Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if st.Attempted != 1 || len(seen) != 1 {
		t.Errorf("--limit 1 attempted %d and requested %d, want one of each", st.Attempted, len(seen))
	}
	// A manifest path that does not exist is an error, not an empty run.
	if _, err := f.h.c.FetchBodySample(context.Background(), SampleFetchOptions{
		Manifest: filepath.Join(t.TempDir(), "nope.tsv"), Dir: t.TempDir(),
	}); err == nil {
		t.Error("a missing manifest reported success: that is how a sample silently becomes nothing")
	}
}
