package compiled

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A server and an artifact from different builds have to refuse each other, and the message has to
// say which side is out of date. The operator can rebuild either one; only the artifact's own
// timestamp says which of them needs it.

// withStamp makes this test binary a stamped build for the duration of a test. The toolchain records
// source stamps in main packages only, so without this seam nothing in the repository could exercise
// what a server does when an artifact disagrees with its own build — the comparison would only ever
// run against "no stamp".
func withStamp(t *testing.T, stamp BuildStamp) {
	restore := CurrentBuild
	t.Cleanup(func() { CurrentBuild = restore })
	CurrentBuild = func() (BuildStamp, bool) { return stamp, true }
}

// openFixture writes one fixture document as an artifact and opens it back.
func openFixture(t *testing.T) *Artifact {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifact.bin")
	docs := []ArtifactDoc{{Accession: "A20201399070", Document: fixtureDocument()}}
	if err := writeArtifactFile(t, path, "test mirror", docs); err != nil {
		t.Fatal(err)
	}
	a, err := OpenArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func TestArtifactAcceptsItsOwnBuild(t *testing.T) {
	withStamp(t, BuildStamp{Revision: strings.Repeat("a", 40), Time: time.Now().Add(-2 * time.Hour)})
	a := openFixture(t)
	if a.Header.Build == "" {
		t.Fatal("the writer did not record the build that wrote it")
	}
	if err := a.CheckCurrentBuild(); err != nil {
		t.Errorf("an artifact this build wrote was refused: %v", err)
	}
}

func TestArtifactNamesTheStaleSide(t *testing.T) {
	// Written at the real now, by a revision the test names below.
	a := openFixture(t)

	// Another revision, written before this binary's commit: the artifact is the stale side.
	a.Header.Build = strings.Repeat("b", 40)
	withStamp(t, BuildStamp{Revision: strings.Repeat("c", 40), Time: a.Header.Created.Add(time.Hour)})
	err := a.CheckCurrentBuild()
	if err == nil || !strings.Contains(err.Error(), "is stale") {
		t.Fatalf("an older artifact did not name itself as stale: %v", err)
	}
	if !strings.Contains(err.Error(), "retsin artifact build") {
		t.Errorf("the refusal does not say what to rebuild: %v", err)
	}

	// Another revision, written after this binary's commit: the server is the stale side, and
	// pointing the operator at `artifact build` would send them the wrong way.
	withStamp(t, BuildStamp{Revision: strings.Repeat("d", 40), Time: a.Header.Created.Add(-time.Hour)})
	err = a.CheckCurrentBuild()
	if err == nil || strings.Contains(err.Error(), "is stale") {
		t.Fatalf("a newer artifact was reported as stale: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "newer source") {
		t.Errorf("a newer artifact did not name the server as the side to rebuild: %v", err)
	}

	// An artifact written before headers carried a stamp cannot be placed on any timeline.
	a.Header.Build = ""
	err = a.CheckCurrentBuild()
	if err == nil || !strings.Contains(err.Error(), "without recording which build wrote it") {
		t.Errorf("an unstamped artifact was not refused for the right reason: %v", err)
	}

	// A server that carries no stamp cannot compare itself to one that does, which is a different
	// problem from being out of date and has a different remedy.
	a.Header.Build = strings.Repeat("e", 40)
	restore := CurrentBuild
	CurrentBuild = func() (BuildStamp, bool) { return BuildStamp{}, false }
	t.Cleanup(func() { CurrentBuild = restore })
	err = a.CheckCurrentBuild()
	if err == nil || !strings.Contains(err.Error(), "no source stamp") {
		t.Errorf("an unstamped server did not say it could not compare: %v", err)
	}
}

// A dirty tree is a different build from a clean one at the same commit: that is the state in which a
// model type changed and was not committed, which is exactly the drift this check exists for. The
// refusal has to say so rather than call either side stale, because neither commit is older.
func TestDirtyTreeIsAnotherBuild(t *testing.T) {
	a := openFixture(t)
	a.Header.Build = strings.Repeat("f", 40)
	a.Header.Modified = true
	withStamp(t, BuildStamp{Revision: strings.Repeat("f", 40), Modified: false, Time: a.Header.Created})
	err := a.CheckCurrentBuild()
	if err == nil {
		t.Fatal("an artifact from a dirty tree was accepted by a server built from the clean one")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("the refusal does not name the working tree as the difference: %v", err)
	}
	if strings.Contains(err.Error(), "is stale") || strings.Contains(err.Error(), "newer source") {
		t.Errorf("the refusal picked a side that the evidence does not support: %v", err)
	}
	if dirty := (BuildStamp{Revision: "abc", Modified: true}).String(); !strings.HasSuffix(dirty, "-dirty") {
		t.Errorf("a dirty stamp does not say it is dirty: %q", dirty)
	}
}
