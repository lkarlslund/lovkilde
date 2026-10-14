package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/compiled"
)

// "lovkilde artifact check" is what a deploy consults before restarting a service, so its answer has
// to be real. The refusals by model are pinned in internal/compiled. A check that always exited 0 would skip every artifact rebuild and the
// unit would then fail at startup — the failure the check exists to predict — and nothing would say
// which of the two lied. So the matching case is pinned as well as the refusal, and the refusal has
// to name the remedy, because the message is the only thing an operator reads.
func TestArtifactCheckCommand(t *testing.T) {
	dir := t.TempDir()
	for into, from := range map[string]string{
		"xml/lta/1942/542.xml": "testdata/lta-1942-542.xml",
		"xml/fob/1989/104.xml": "testdata/fob-1989-104.xml",
	} {
		raw, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, into)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A test binary carries no source stamp, so the seam is the only way to ask what a stamped
	// build would say about a file it wrote itself.
	restore := compiled.CurrentBuild
	t.Cleanup(func() { compiled.CurrentBuild = restore })
	// The commit is dated after the artifact was written, which is the deploy case: the file on
	// disk predates the binary about to serve it.
	stamp := compiled.BuildStamp{Revision: "1111111111111111111111111111111111111111", Time: time.Now().Add(time.Hour)}
	compiled.CurrentBuild = func() (compiled.BuildStamp, bool) { return stamp, true }

	buildArtifact(t, dir)

	if err := runCLI(t, newRootCmd(), "--data-dir", dir, "artifact", "check"); err != nil {
		t.Errorf("check refused an artifact this build wrote itself: %v", err)
	}

	// A newer commit, and a dirty tree at the same commit: the file holds this binary's document
	// model, so either build serves it. Binaries and artifacts are pinned by model, not by build.
	stamp.Revision = "2222222222222222222222222222222222222222"
	if err := runCLI(t, newRootCmd(), "--data-dir", dir, "artifact", "check"); err != nil {
		t.Errorf("check refused an artifact of this model written by another build: %v", err)
	}
	stamp.Revision = "1111111111111111111111111111111111111111"
	stamp.Modified = true
	if err := runCLI(t, newRootCmd(), "--data-dir", dir, "artifact", "check"); err != nil {
		t.Errorf("check refused an artifact of this model written from a clean tree: %v", err)
	}
}
