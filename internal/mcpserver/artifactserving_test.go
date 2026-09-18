package mcpserver

import (
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
	if s.textSource() == "" || !strings.Contains(s.textSource(), "parsing XML") {
		t.Fatalf("a server with no artifact should say it parses: %q", s.textSource())
	}

	artPath := writeFixtureArtifact(t, s.archive)
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
	artPath := writeFixtureArtifact(t, s.archive, accCanon)
	if err := s.UseArtifact(artPath, 1<<20); err != nil {
		t.Fatal(err)
	}

	_, err := s.Lookup(LookupRequest{Document: accCanon})
	if err == nil {
		t.Fatal("a document missing from the artifact answered without an error")
	}
	for _, want := range []string{accCanon, "artifact", "retsin artifact build"} {
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

// A server and an artifact from different builds have to refuse each other at startup, and the
// message has to name the side that needs rebuilding. The artifact holds the model as JSON, so a
// file from another revision decodes into this server's types with another revision's meaning and
// answers with fields that no longer say what they said.
func TestServingRefusesAnArtifactFromAnotherBuild(t *testing.T) {
	s := newTestServer(t)
	restore := compiled.CurrentBuild
	t.Cleanup(func() { compiled.CurrentBuild = restore })

	wrote := compiled.BuildStamp{Revision: strings.Repeat("a", 40), Time: time.Now().Add(-2 * time.Hour)}
	compiled.CurrentBuild = func() (compiled.BuildStamp, bool) { return wrote, true }
	artPath := writeFixtureArtifact(t, s.archive)

	if err := s.UseArtifact(artPath, 1<<20); err != nil {
		t.Fatalf("an artifact this build wrote was refused: %v", err)
	}

	// A server built from a commit the artifact predates: the artifact is the stale side.
	compiled.CurrentBuild = func() (compiled.BuildStamp, bool) {
		return compiled.BuildStamp{Revision: strings.Repeat("b", 40), Time: time.Now()}, true
	}
	fresh := New(s.archive, "https://www.retsinformation.dk/", "test")
	err := fresh.UseArtifact(artPath, 1<<20)
	if err == nil {
		t.Fatal("an artifact from another build was accepted")
	}
	for _, want := range []string{"is stale", artPath, "retsin artifact build"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	if !strings.Contains(fresh.textSource(), "parsing XML") {
		t.Errorf("a server that refused its artifact does not say what it is serving from: %q", fresh.textSource())
	}

	// The other direction: the artifact is newer than the server, so telling the operator to rebuild
	// the artifact would send them the wrong way.
	compiled.CurrentBuild = func() (compiled.BuildStamp, bool) {
		return compiled.BuildStamp{Revision: strings.Repeat("b", 40), Time: wrote.Time.Add(-time.Hour)}, true
	}
	err = New(s.archive, "https://www.retsinformation.dk/", "test").UseArtifact(artPath, 1<<20)
	if err == nil || strings.Contains(err.Error(), "is stale") {
		t.Errorf("an artifact newer than its server was reported as stale: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "newer source") {
		t.Errorf("the refusal does not name the server as the side to rebuild: %v", err)
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
	wr, err := compiled.NewWriter(f, a.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range a.Entries() {
		if left[e.Accession] {
			continue
		}
		doc, _, ok := a.Open(e.Accession)
		if !ok {
			t.Fatalf("opening %s", e.Accession)
		}
		if err := wr.Add(e.Accession, doc.Model()); err != nil {
			t.Fatal(err)
		}
	}
	if err := wr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
