package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/lovkilde/internal/compiled"
	"github.com/lkarlslund/lovkilde/internal/lexdania"
)

// servedAfter serves the test server's library as an artifact built after a collection pass, which
// is where a server learns how current its text is.
func servedAfter(t *testing.T, s *Server, pass compiled.Collection) {
	t.Helper()
	if err := s.UseArtifact(writeFixtureArtifactAfter(t, testArchive(s), &pass), 1<<20); err != nil {
		t.Fatal(err)
	}
}

// pulledAt is a clean collection pass at ts.
func pulledAt(ts time.Time) compiled.Collection {
	return compiled.Collection{PulledAt: ts, IssuesKnown: true}
}

func TestHorizonStatesTheArtifactsCollectionPass(t *testing.T) {
	s := newTestServer(t)
	servedAfter(t, s, pulledAt(time.Date(2026, 9, 18, 9, 16, 52, 0, time.UTC)))
	line := s.horizon().Line()
	if !strings.Contains(line, "last pulled 2026-09-18 09:16:52") {
		t.Errorf("horizon drops the artifact's pull time: %q", line)
	}
	if !strings.Contains(line, "nothing left unresolved") {
		t.Errorf("a clean pass is not reported as one: %q", line)
	}
}

func TestHorizonWithoutACollectionPassSaysUnknown(t *testing.T) {
	s := newTestServer(t)
	if err := s.UseArtifact(writeFixtureArtifact(t, testArchive(s)), 1<<20); err != nil {
		t.Fatal(err)
	}
	h := s.horizon()
	if h.Known {
		t.Fatal("an artifact that records no collection pass claims one")
	}
	line := h.Line()
	if !strings.Contains(line, "never pulled") {
		t.Errorf("an artifact with no collection record does not say so: %q", line)
	}
	// The fallback that must never happen: filling the gap with a timestamp, which is
	// how a mirror that has never collected anything starts claiming to be current.
	if strings.Contains(line, "last pulled 20") {
		t.Errorf("an unknown horizon was filled in with a date: %q", line)
	}
}

// A server that started before tonight's artifact has to report tonight's pass once it serves
// that artifact, and not before: the horizon is the served text's, not the newest on disk.
func TestHorizonFollowsTheServedArtifact(t *testing.T) {
	s := newTestServer(t)
	servedAfter(t, s, pulledAt(time.Date(2026, 9, 1, 3, 4, 5, 0, time.UTC)))
	if got := s.horizon().Line(); !strings.Contains(got, "2026-09-01") {
		t.Fatalf("first horizon wrong: %q", got)
	}
	servedAfter(t, s, pulledAt(time.Date(2026, 9, 18, 9, 16, 52, 0, time.UTC)))
	got := s.horizon().Line()
	if !strings.Contains(got, "2026-09-18") || strings.Contains(got, "2026-09-01") {
		t.Errorf("horizon kept the replaced artifact's pass: %q", got)
	}
}

func TestHorizonWithoutAnIndexSaysSo(t *testing.T) {
	s := newTestServer(t)
	servedAfter(t, s, pulledAt(time.Date(2026, 9, 18, 9, 16, 52, 0, time.UTC)))
	line := s.horizon().Line()
	if !strings.Contains(line, "no full-text index attached") {
		t.Errorf("a lookup-only server reports fake index coverage: %q", line)
	}
	if strings.Contains(line, "covering 0 documents") {
		t.Errorf("zero coverage reads as an empty corpus: %q", line)
	}
}

func TestPolicyAnchorsCurrentLawOnDates(t *testing.T) {
	p := strings.ToLower(newTestServer(t).horizon().Policy())
	for _, want := range []string{"as of", "last pull", "cannot answer"} {
		if !strings.Contains(p, want) {
			t.Errorf("policy does not tell the client how to treat %q: %s", want, p)
		}
	}
}

// speculative framing is what the horizon rule forbids: an answer either speaks as
// of the dates it names, or says it cannot answer for the date asked about. Wandering
// "this may have changed" hedges are how a mirror talks like a live service while
// being a snapshot.
func TestAnswersAnchorOnDatesNotSpeculation(t *testing.T) {
	s := newTestServer(t)
	s.UseIndex(mustIndex(t))
	servedAfter(t, s, pulledAt(time.Date(2026, 9, 18, 9, 16, 52, 0, time.UTC)))

	look, err := s.Lookup(LookupRequest{Document: accCanon})
	if err != nil {
		t.Fatal(err)
	}
	searched, err := s.Search(SearchRequest{Query: "kanonisk"})
	if err != nil {
		t.Fatal(err)
	}
	hedges := []string{"may have", "might have", "could have been", "may not be",
		"possibly", "not necessarily", "as far as we know", "to our knowledge", "probably"}
	answers := map[string]string{"lookup": look.String(), "search": searched.String()}
	for name, text := range answers {
		low := strings.ToLower(text)
		for _, hedge := range hedges {
			if strings.Contains(low, hedge) {
				t.Errorf("%s answer hedges with %q instead of naming a date:\n%s", name, hedge, text)
			}
		}
		if !strings.Contains(text, "2026-09-18 09:16:52") {
			t.Errorf("%s answer drops the dataset line:\n%s", name, text)
		}
	}
}

// serverWithAnchoredDoc serves one act whose metadata names a document amending it
// from eff. Change anchors are how the register itself says "a later document changed
// me", which is data in the payload rather than a guess made at answer time.
func serverWithAnchoredDoc(t *testing.T, eff string) *Server {
	root := t.TempDir()
	doc := "<Dokument><Meta><AccessionNumber>" + accCanon + "</AccessionNumber><Year>1998</Year>" +
		"<Number>2</Number><DocumentType>Lov</DocumentType><Status>Valid</Status>" +
		"<DiesSigni>1998-01-01</DiesSigni><DocumentTitle>Testlov</DocumentTitle>" +
		`<Change id="c_1"/><Ref_Accn REFid="c_1">A20260001X</Ref_Accn><Ref_Af REFid="c_1">` + eff + `</Ref_Af>` +
		"</Meta><DokumentIndhold><Paragraf><Explicatus>§ 1.</Explicatus>" +
		"<Stk><Explicatus>Stk. 1.</Explicatus>KANONISK TEKST</Stk></Paragraf></DokumentIndhold></Dokument>"
	path := filepath.Join(root, "lta/1998/2.xml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := lexdania.Scan(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	s := New(a, "https://www.retsinformation.dk", "test")
	servedAfter(t, s, pulledAt(time.Date(2026, 9, 18, 9, 16, 52, 0, time.UTC)))
	// The clock is pinned rather than read. These fixtures name absolute dates, and the answer
	// sorts amendments against today, so a test that reads the real clock is only correct on the
	// day it was written — which is how the tests below agreed with the code on 2026-09-18 and
	// fell over the day after.
	s.nowFunc = func() time.Time { return anchoredNow }
	return s
}

// anchoredNow is the moment these fixtures pretend the question was asked: one day after the
// collection pass they describe. Deliberately not the same date as the pull, so an answer that
// confuses "today" with "how current the data is" fails a test instead of passing by coincidence.
var anchoredNow = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func TestLookupNamesAmendmentsAlreadyEffective(t *testing.T) {
	s := serverWithAnchoredDoc(t, "2026-01-01")
	res, err := s.Lookup(LookupRequest{Document: accCanon})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Amendments, "A20260001X eff. 2026-01-01") {
		t.Errorf("the amendment named by the document's own metadata is missing: %q", res.Amendments)
	}
	joined := strings.ToLower(strings.Join(res.Warnings, " "))
	if !strings.Contains(joined, "as_of="+anchoredNow.Format("2006-01-02")) {
		t.Errorf("the answer does not offer the consolidated reading for the date it names:\n%s",
			strings.Join(res.Warnings, "\n"))
	}
}

func TestLookupSeparatesAmendmentsNotYetEffective(t *testing.T) {
	s := serverWithAnchoredDoc(t, "2027-06-05")
	res, err := s.Lookup(LookupRequest{Document: accCanon})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Amendments, "all effective after "+anchoredNow.Format("2006-01-02")) ||
		!strings.Contains(res.Amendments, "earliest 2027-06-05") {
		t.Errorf("a future amendment is not placed on the timeline: %q", res.Amendments)
	}
	for _, w := range res.Warnings {
		if strings.Contains(strings.ToLower(w), "as_of=") {
			t.Errorf("an amendment that has not taken effect asks for consolidation: %q", w)
		}
	}
}

// An amendment effective on the day itself is already operative: an answer that
// compares with Before() rather than !After() would present the pre-amendment text as
// current for the whole of the day the new text takes effect.
func TestLookupCountsAnAmendmentEffectiveTodayAsEffective(t *testing.T) {
	today := anchoredNow.Format("2006-01-02")
	s := serverWithAnchoredDoc(t, today)
	res, err := s.Lookup(LookupRequest{Document: accCanon})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Amendments, "eff. "+today) {
		t.Errorf("an amendment effective today is treated as future: %q", res.Amendments)
	}
	if strings.Contains(res.Amendments, "all effective after") {
		t.Errorf("answer claims this accession is operative while its amendment takes effect today: %q", res.Amendments)
	}
}

// Consolidating onto a date has to report a chain with gaps as a fact about the
// mirror's contents, not as a hedge about what the law might have been.
func TestIncompleteChainReportsAGapNotAHedge(t *testing.T) {
	s := serverWithAnchoredDoc(t, "2026-01-01")
	res, err := s.Lookup(LookupRequest{Document: accCanon, AsOf: "2026-06-01"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ChainComplete {
		t.Skip("this fixture's chain is complete: no gap to report")
	}
	joined := strings.ToLower(strings.Join(res.Warnings, " "))
	if !strings.Contains(joined, "not in the mirror") {
		t.Errorf("incomplete chain does not name the missing documents: %q", joined)
	}
	for _, hedge := range []string{"may not be", "may have", "might have", "possibly"} {
		if strings.Contains(joined, hedge) {
			t.Errorf("chain gap reported as speculation with %q: %q", hedge, joined)
		}
	}
}

// The unresolved count and its fingerprint are part of the horizon: "we pulled at ten
// and fourteen documents would not come" is the whole truth about how current this
// mirror is, and the count is what tells tonight's problems from last night's.
func TestHorizonCarriesUnresolvedWork(t *testing.T) {
	s := newTestServer(t)
	servedAfter(t, s, compiled.Collection{
		PulledAt:    time.Date(2026, 9, 18, 9, 16, 52, 0, time.UTC),
		Owed:        1,
		Failed:      14,
		Digest:      "6f2c1a9d",
		IssuesKnown: true,
	})
	line := s.horizon().Line()
	for _, want := range []string{"15 unresolved", "issues 6f2c1a9d", "1 owed", "14 refused by the site"} {
		if !strings.Contains(line, want) {
			t.Errorf("horizon drops %q: %q", want, line)
		}
	}
	if strings.Contains(line, "nothing left unresolved") {
		t.Errorf("a pass with holes reports itself clean: %q", line)
	}
}

// A record from before the problems were counted says that nobody knows, which is not
// the same as saying there was nothing wrong.
func TestHorizonDoesNotInventACleanPass(t *testing.T) {
	s := newTestServer(t)
	// A record from an older build counted nothing, so what was outstanding is not known.
	servedAfter(t, s, compiled.Collection{PulledAt: time.Date(2026, 9, 18, 9, 16, 52, 0, time.UTC)})
	line := s.horizon().Line()
	if !strings.Contains(line, "problems not recorded") {
		t.Errorf("an uncounted pass is not reported as uncounted: %q", line)
	}
	if strings.Contains(line, "nothing left unresolved") {
		t.Errorf("an uncounted pass claims to be clean: %q", line)
	}
}
