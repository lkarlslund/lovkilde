package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/compiled"
	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

// The artifact is a second representation of the same documents, so the rule for serving from it is
// the rule for any second representation of a legal corpus: an answer has to be the answer the other
// path gives, word for word. Speed is the reason it exists; it is not a licence to be different.
//
// The fixture mirror holds the three text states an answer has to tell apart — the register's own
// body, text recovered from an HTML payload, and metadata with no text — so the comparison covers a
// provision read out of each, and a refusal.
func TestArtifactServingAnswersWhatTheParseAnswers(t *testing.T) {
	s := newTestServer(t)

	ask := func() map[string]LookupResult {
		out := map[string]LookupResult{}
		for _, acc := range []string{accCanon, accHTML, accEmpty} {
			for _, addr := range []string{"", "§ 1", "§ 1, stk. 2", "kapitel 9"} {
				key := acc + "|" + addr
				res, err := s.Lookup(LookupRequest{Document: acc, Address: addr, MaxChars: 4000})
				if err != nil {
					out[key] = LookupResult{Text: "ERROR: " + err.Error()}
					continue
				}
				out[key] = *res
			}
		}
		return out
	}

	byParse := ask()
	if !strings.Contains(s.textSource(), testArchive(s).Root) {
		t.Fatalf("a server with no artifact should name the library it reads: %q", s.textSource())
	}

	artPath := writeFixtureArtifact(t, testArchive(s))
	if err := s.UseArtifact(artPath, 1<<20); err != nil {
		t.Fatalf("serving from the fixture artifact: %v", err)
	}
	if !strings.Contains(s.textSource(), artPath) {
		t.Errorf("the status line does not say the artifact is being served: %q", s.textSource())
	}
	byArtifact := ask()

	for key, want := range byParse {
		got, ok := byArtifact[key]
		if !ok {
			t.Fatalf("%s: no answer from the artifact path", key)
		}
		if got.Text != want.Text {
			t.Errorf("%s: text differs between the two read paths:\nparse:\n%s\nartifact:\n%s", key, want.Text, got.Text)
		}
		if got.Provenance != want.Provenance || got.ProvenanceNote != want.ProvenanceNote {
			t.Errorf("%s: provenance differs: %s/%q vs %s/%q", key,
				want.Provenance, want.ProvenanceNote, got.Provenance, got.ProvenanceNote)
		}
		if got.Amendments != want.Amendments {
			t.Errorf("%s: amendments differ: %q vs %q", key, want.Amendments, got.Amendments)
		}
		if strings.Join(got.Warnings, "\n") != strings.Join(want.Warnings, "\n") {
			t.Errorf("%s: warnings differ: %q vs %q", key, want.Warnings, got.Warnings)
		}
		if want.Text == "" && !strings.HasPrefix(want.Text, "ERROR") && got.Path == "" {
			t.Errorf("%s: an answer that carries text carries no source path", key)
		}
	}

	// The path is the one field allowed to differ, because it names what was actually read: an
	// answer served from the artifact that claimed a file it never opened would send a reader to
	// check the wrong thing.
	whole := byArtifact[accCanon+"|"]
	if !strings.Contains(whole.Path, artPath) {
		t.Errorf("the answer names %q as its source, not the artifact it was read from", whole.Path)
	}
}

// A document the artifact does not hold is an error that says what to rebuild. The quiet alternative
// — an empty text — turns a stale file into a law with nothing in it, which is how an incomplete
// mirror gets quoted as a complete one.
func TestServingNamesTheRemedyForADocumentTheArtifactLacks(t *testing.T) {
	s := newTestServer(t)
	artPath := writeFixtureArtifact(t, testArchive(s), accCanon)
	if err := s.UseArtifact(artPath, 1<<20); err != nil {
		t.Fatal(err)
	}

	_, err := s.Lookup(LookupRequest{Document: accCanon})
	if err == nil {
		t.Fatal("a document missing from the artifact answered without an error")
	}
	for _, want := range []string{accCanon, "artifact", "lovkilde-update artifact build"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}

	// The documents it does hold still answer, so one gap is one error rather than a dead server.
	res, err := s.Lookup(LookupRequest{Document: accHTML, Address: "§ 1"})
	if err != nil || !strings.Contains(res.Text, "Bekendtgørelsen finder anvendelse") {
		t.Errorf("a document the artifact holds did not answer: %v\n%q", err, res.Text)
	}
}

// A server reads an artifact written by another build when both hold the same document model:
// binaries and artifacts are not locked together, so a change to rendering or wording deploys with
// a restart. The refusals — another model, either way, and a file from before model versions — are
// pinned in internal/compiled (TestAnArtifactIsReadByModelNotBuild).
func TestServingReadsAnArtifactOfItsModelFromAnotherBuild(t *testing.T) {
	s := newTestServer(t)
	restore := compiled.CurrentBuild
	t.Cleanup(func() { compiled.CurrentBuild = restore })

	wrote := compiled.BuildStamp{Revision: strings.Repeat("a", 40), Time: time.Now().Add(-2 * time.Hour)}
	compiled.CurrentBuild = func() (compiled.BuildStamp, bool) { return wrote, true }
	artPath := writeFixtureArtifact(t, testArchive(s))

	compiled.CurrentBuild = func() (compiled.BuildStamp, bool) {
		return compiled.BuildStamp{Revision: strings.Repeat("b", 40), Time: time.Now()}, true
	}
	fresh := New(testArchive(s), "https://www.retsinformation.dk/", "test")
	if err := fresh.UseArtifact(artPath, 1<<20); err != nil {
		t.Fatalf("an artifact of this model from another build was refused: %v", err)
	}
	res, err := fresh.Lookup(LookupRequest{Document: accHTML, Address: "§ 1"})
	if err != nil || !strings.Contains(res.Text, "Bekendtgørelsen finder anvendelse") {
		t.Errorf("the artifact did not answer: %v", err)
	}
}

// writeFixtureArtifact compiles the fixture mirror into an artifact, optionally leaving documents
// out so the coverage claim can be tested rather than assumed.
//
// A test binary is never stamped — the toolchain records source stamps in main packages only — so
// unless a test has already posed as one build, this poses as one. Without that the writer would
// record no stamp and every artifact in this package would be refused as unverifiable, which is
// correct behaviour for a real file and a wall between these tests and their subject.
func writeFixtureArtifact(t *testing.T, a *lexdania.Archive, skip ...string) string {
	t.Helper()
	return writeFixtureArtifactAfter(t, a, nil, skip...)
}

// writeFixtureArtifactAfter is writeFixtureArtifact for an artifact built after a collection pass.
func writeFixtureArtifactAfter(t *testing.T, a *lexdania.Archive, pass *compiled.Collection, skip ...string) string {
	t.Helper()
	if _, have := compiled.CurrentBuild(); !have {
		restore := compiled.CurrentBuild
		t.Cleanup(func() { compiled.CurrentBuild = restore })
		fixture := compiled.BuildStamp{Revision: "fixturebuild", Time: time.Now().Add(-time.Hour)}
		compiled.CurrentBuild = func() (compiled.BuildStamp, bool) { return fixture, true }
	}
	left := map[string]bool{}
	for _, acc := range skip {
		left[acc] = true
	}
	path := filepath.Join(t.TempDir(), "artifact.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	wr, err := compiled.NewWriterAfter(f, a.Root, compiled.Provenance{Pass: pass})
	if err != nil {
		t.Fatal(err)
	}
	// What the artifact build stores: each document's model with its changes, and the catalogue
	// in the archive's scan order (lexdania.ArtifactRecords).
	var catalogue []lexdania.CatalogueRecord
	for _, e := range a.ScanOrder() {
		if left[e.Accession] {
			catalogue = append(catalogue, lexdania.CatalogueRecord{Entry: e})
			continue
		}
		doc, path, ok := a.Open(e.Accession)
		if !ok {
			t.Fatalf("opening %s", e.Accession)
		}
		m, rec := lexdania.ArtifactRecords(e, doc, path)
		if err := wr.Add(e.Accession, m); err != nil {
			t.Fatal(err)
		}
		catalogue = append(catalogue, rec)
	}
	payload, err := json.Marshal(catalogue)
	if err != nil {
		t.Fatal(err)
	}
	wr.SetCatalogue(payload)
	if err := wr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// The status line carries the artifact's own date, so an operator can tell which build answers.
func TestStatusDatesTheServedArtifact(t *testing.T) {
	s := newTestServer(t)
	if err := s.UseArtifact(writeFixtureArtifact(t, testArchive(s)), 1<<20); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.textSource(), "built") {
		t.Errorf("the status line does not date the artifact: %q", s.textSource())
	}
}

// testArchive is the XML archive a test server was started with.
func testArchive(s *Server) *lexdania.Archive { return s.base.lib.(*lexdania.Archive) }
