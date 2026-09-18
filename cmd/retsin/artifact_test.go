package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/compiled"
)

// The build command is the only way the artifact gets made in production, so the wiring between
// "retsin artifact build" and a file that opens and answers is tested end to end rather than by
// trusting that a scan, a writer and a rename agree. The two pinned documents are real: one a short
// law with provisions, one a gazette item with none, because a build that only works on the
// documents it likes is a build that will quietly produce a smaller mirror.
func TestArtifactBuildCommand(t *testing.T) {
	dir := t.TempDir()
	mirror := map[string]string{
		"xml/lta/1942/542.xml": "testdata/lta-1942-542.xml",
		"xml/fob/1989/104.xml": "testdata/fob-1989-104.xml",
	}
	for into, from := range mirror {
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

	root := newRootCmd()
	if err := runCLI(t, root, "--data-dir", dir, "artifact", "build"); err != nil {
		t.Fatalf("artifact build: %v", err)
	}

	path := filepath.Join(dir, artifactDefaultName)
	a, err := compiled.OpenArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if len(a.Documents()) != len(mirror) {
		t.Errorf("artifact holds %d documents, the mirror held %d: %v", len(a.Documents()), len(mirror), a.Documents())
	}
	if _, err := os.Stat(path + ".writing"); !os.IsNotExist(err) {
		t.Errorf("the half-built file is still there: a rename failure would go unnoticed")
	}
	// The point of the artifact is that a document answers without the XML: read one back and
	// check it is the law it claims to be.
	var withProvisions int
	for _, acc := range a.Documents() {
		doc, err := a.Document(acc)
		if err != nil {
			t.Fatalf("%s: %v", acc, err)
		}
		if doc.Identity.Accession != acc {
			t.Errorf("%s came back as %s", acc, doc.Identity.Accession)
		}
		if n := len(doc.Content.Provisions()); n > 0 {
			withProvisions++
			if md := doc.Content.Markdown(); !strings.Contains(md, "§") {
				t.Errorf("%s renders without any provision text:\n%s", acc, md)
			}
		}
	}
	if withProvisions == 0 {
		t.Error("no document in the artifact has provisions, so the read-back proved less than it looks like")
	}
}

// A mirror root with nothing in it must fail rather than write an empty artifact, which is the
// difference between a broken path and a mirror that appears to be empty.
func TestArtifactBuildRefusesEmptyMirror(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "xml"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd()
	if err := runCLI(t, root, "--data-dir", dir, "artifact", "build"); err == nil {
		t.Fatal("an artifact was built from an empty mirror")
	}
	if _, err := os.Stat(filepath.Join(dir, artifactDefaultName)); !os.IsNotExist(err) {
		t.Error("the refused artifact was left in place anyway")
	}
}
