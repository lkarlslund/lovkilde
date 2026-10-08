package builder

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/artifactdir"
	"github.com/lkarlslund/retsinformation/internal/compiled"
)

// put writes an XML file into the mirror under data, from a fixture with replacements applied.
func put(t *testing.T, data, rel, fixture string, replace ...string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "cmd", "retsin", "testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.NewReplacer(replace...).Replace(string(raw))
	dst := filepath.Join(data, "xml", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func build(t *testing.T, data string, mode Mode, out string) string {
	t.Helper()
	path, err := Artifact(context.Background(), Options{XMLDir: filepath.Join(data, "xml"), DataDir: data, Out: out, Mode: mode, RebaseAt: 1})
	if err != nil {
		t.Fatalf("%s build: %v", mode, err)
	}
	return path
}

// sameContent says how two artifacts differ as a reader sees them: documents and their order,
// each document's frame byte for byte, and the catalogue.
func sameContent(t *testing.T, what, wantPath, gotPath string) {
	t.Helper()
	want, err := compiled.OpenArtifact(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	defer want.Close()
	got, err := compiled.OpenArtifact(gotPath)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	if !slices.Equal(got.Documents(), want.Documents()) {
		t.Fatalf("%s holds %v, a full build %v", what, got.Documents(), want.Documents())
	}
	for _, acc := range want.Documents() {
		w, _, err1 := want.Frame(acc)
		g, _, err2 := got.Frame(acc)
		if err1 != nil || err2 != nil || !bytes.Equal(w.Compressed, g.Compressed) {
			t.Errorf("%s: %s is not the frame a full build writes (%v, %v)", what, acc, err1, err2)
		}
	}
	wc, _ := want.Catalogue()
	gc, _ := got.Catalogue()
	if !bytes.Equal(wc, gc) {
		t.Errorf("%s: the catalogue is not the one a full build writes:\n got %s\nwant %s", what, gc, wc)
	}
}

// An incremental build is only worth having if it makes what a full build makes. The mirror
// changes the ways it does in a week — a document is corrected, a new one is published — and
// the patch, the layer, and a patch on a machine that holds only the changed files must each
// read exactly like a full build of the result.
func TestIncrementalBuildsMakeWhatAFullBuildMakes(t *testing.T) {
	data := t.TempDir()
	put(t, data, "lta/1942/542.xml", "lta-1942-542.xml")
	put(t, data, "fob/1989/104.xml", "fob-1989-104.xml")
	base := build(t, data, Full, "")

	put(t, data, "fob/1989/104.xml", "fob-1989-104.xml", "<DocumentTitle>", "<DocumentTitle>Rettet: ")
	put(t, data, "lta/2099/1.xml", "lta-1942-542.xml",
		"<AccessionNumber>A19420054230", "<AccessionNumber>A20990000010", "<Year>1942", "<Year>2099", "<Number>542", "<Number>1")
	full := build(t, data, Full, filepath.Join(t.TempDir(), "full.retsin"))

	patch := build(t, data, Patch, filepath.Join(t.TempDir(), "patch.retsin"))
	sameContent(t, "the patch", full, patch)
	p, err := compiled.OpenArtifact(patch)
	if err != nil {
		t.Fatal(err)
	}
	if inc := p.Header.Incremental; inc == nil || inc.Compiled != 2 || inc.Carried != 1 {
		t.Errorf("the patch says it compiled and carried %+v, want 2 compiled and 1 carried", inc)
	}
	p.Close()

	layer := build(t, data, Layer, "")
	sameContent(t, "the layer", full, layer)
	// The next layer goes over the same base, not over the layer.
	again := build(t, data, Layer, "")
	a, err := compiled.OpenArtifact(again)
	if err != nil {
		t.Fatal(err)
	}
	if a.Header.Base == nil || a.Header.Base.File != filepath.Base(base) {
		t.Errorf("a layer over a layer names base %+v, want %s", a.Header.Base, filepath.Base(base))
	}
	a.Close()

	// A fresh machine holds the published artifact and the files this week's pass fetched.
	if err := artifactdir.SetCurrent(data, filepath.Base(base)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(data, "xml", "lta", "1942", "542.xml")); err != nil {
		t.Fatal(err)
	}
	sameContent(t, "a patch with only the changed files", full, build(t, data, Patch, filepath.Join(t.TempDir(), "runner.retsin")))
}

// Carrying a frame is only right when this build would compile it the same way.
func TestAnIncrementalBuildRefusesAnotherArchiveRoot(t *testing.T) {
	data := t.TempDir()
	put(t, data, "lta/1942/542.xml", "lta-1942-542.xml")
	base := build(t, data, Full, "")
	other := t.TempDir()
	put(t, other, "lta/1942/542.xml", "lta-1942-542.xml")
	_, err := Artifact(context.Background(), Options{XMLDir: filepath.Join(other, "xml"), DataDir: other, Out: filepath.Join(other, "x.retsin"), Mode: Patch, From: base})
	if err == nil || !strings.Contains(err.Error(), "build in full") {
		t.Errorf("a patch over an artifact built from another root: %v", err)
	}
}
