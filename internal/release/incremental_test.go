package release

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/artifactdir"
	"github.com/lkarlslund/retsinformation/internal/compiled"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// writeLayer writes a layer over base, beside it: A1 read from the base, A2 its own.
func writeLayer(t *testing.T, base string) string {
	t.Helper()
	b, err := compiled.OpenArtifact(base)
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := b.Ref("A1")
	stamp := b.Header.Stamp
	b.Close()
	tmp := filepath.Join(filepath.Dir(base), "layer.part")
	f, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	wr, err := compiled.NewWriterAfter(f, "test", compiled.Provenance{Base: &compiled.BaseRef{File: filepath.Base(base), Stamp: stamp}})
	if err != nil {
		t.Fatal(err)
	}
	if err := wr.WriteBaseRef("A1", ref.Raw); err != nil {
		t.Fatal(err)
	}
	if err := wr.Add("A2", &lexdoc.Document{Identity: lexdoc.Identity{Accession: "A2", Title: "Ny"}}); err != nil {
		t.Fatal(err)
	}
	wr.SetCatalogue([]byte("[]"))
	if err := wr.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	path := filepath.Join(filepath.Dir(base), artifactdir.Name(wr.Header()))
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	return path
}

// A layer is released with the base it is over, which the repository and a server usually have
// already: the release sends the layer, a server that has the base downloads only the layer, and
// one that has nothing gets both. A whole artifact released afterwards takes both away.
func TestALayerIsReleasedOverTheBaseAlreadyPublished(t *testing.T) {
	ctx := context.Background()
	repo := &Local{Dir: t.TempDir()}
	built := t.TempDir()
	base := buildArtifact(t, built, "Lov")
	state := stateDir(t, "")
	publish(t, repo, base, state)
	server := t.TempDir()
	if _, err := FetchArtifact(ctx, repo, server); err != nil {
		t.Fatal(err)
	}

	layer := writeLayer(t, base)
	m := publish(t, repo, layer, state)
	if m.Base == nil || m.Base.Path != filepath.Base(base) || m.File != filepath.Base(layer) {
		t.Fatalf("the layer's release names %s over %+v", m.File, m.Base)
	}
	for _, name := range []string{filepath.Base(base), filepath.Base(layer)} {
		if !fileExists(filepath.Join(repo.Dir, name)) {
			t.Errorf("%s is not in the repository", name)
		}
	}

	for name, data := range map[string]string{"a server with the base": server, "a new server": t.TempDir()} {
		res, err := FetchArtifact(ctx, repo, data)
		if err != nil || !res.Installed {
			t.Fatalf("%s: %+v, %v", name, res, err)
		}
		a, err := compiled.OpenArtifact(res.Path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(a.Documents(), []string{"A1", "A2"}) {
			t.Errorf("%s reads %v", name, a.Documents())
		}
		if d, err := a.Document("A1"); err != nil || d.Identity.Title != "Lov" {
			t.Errorf("%s: A1 from the base: %v, %v", name, d, err)
		}
		a.Close()
		if removed, err := artifactdir.Prune(data); err != nil || slices.Contains(removed, filepath.Base(base)) {
			t.Errorf("%s: pruning removed %v (%v), the base the current layer is read with", name, removed, err)
		}
	}

	whole := buildArtifact(t, t.TempDir(), "Lov 2")
	publish(t, repo, whole, state)
	entries, _ := os.ReadDir(repo.Dir)
	for _, e := range entries {
		if e.Name() == filepath.Base(base) || e.Name() == filepath.Base(layer) {
			t.Errorf("%s is still published after a whole artifact replaced the layer", e.Name())
		}
	}
}

// The XML archive is published as packs per shard. A release packs only the shards that changed,
// a restore makes the archive file for file — skipping the shards a machine already has as
// published — and a machine holding part of the archive, as a runner does, adds to the packs
// rather than replacing them with what it holds.
func TestTheXMLArchiveIsPublishedAsPacksThatChangeAlone(t *testing.T) {
	ctx := context.Background()
	repo := &Local{Dir: t.TempDir()}
	xml := t.TempDir()
	files := map[string]string{
		"lta/1995/498.xml":  "<a/>",
		"lta/1995/498.json": `{"b":1}`,
		"lta/1995/499.xml":  "<c/>",
		"fob/2001/7.xml":    "<d/>",
	}
	write := func(rel, body string) {
		p := filepath.Join(xml, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for rel, body := range files {
		write(rel, body)
	}
	release := func() Manifest {
		m, err := Publish(ctx, repo, Release{StateDir: t.TempDir(), XMLDir: xml})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	first := release()
	if len(first.XML) != 2 || first.XML[0].Shard != "fob/2001" || first.XML[1].Files != 3 {
		t.Fatalf("packs %+v, want fob/2001 and lta/1995 with 3 files", first.XML)
	}

	restored := t.TempDir()
	if _, n, err := RestoreXML(ctx, repo, restored); err != nil || n != 2 {
		t.Fatalf("restore unpacked %d packs: %v", n, err)
	}
	for rel, body := range files {
		if got, err := os.ReadFile(filepath.Join(restored, filepath.FromSlash(rel))); err != nil || string(got) != body {
			t.Errorf("%s restored as %q (%v), want %q", rel, got, err, body)
		}
	}

	if unchanged := release(); unchanged.XML[1].SHA256 != first.XML[1].SHA256 {
		t.Errorf("an unchanged shard was packed again")
	}

	// A runner: only this week's file on disk, one changed and one new in the same shard.
	if err := os.RemoveAll(xml); err != nil {
		t.Fatal(err)
	}
	write("lta/1995/499.xml", "<c changed/>")
	write("lta/1995/500.xml", "<e/>")
	second := release()
	if len(second.XML) != 2 || second.XML[0] != first.XML[0] || second.XML[1].Files != 4 {
		t.Fatalf("after a runner's release the packs are %+v; want fob/2001 kept and lta/1995 with 4 files", second.XML)
	}
	files["lta/1995/499.xml"] = "<c changed/>"
	files["lta/1995/500.xml"] = "<e/>"
	fresh := t.TempDir()
	if _, _, err := RestoreXML(ctx, repo, fresh); err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		if got, err := os.ReadFile(filepath.Join(fresh, filepath.FromSlash(rel))); err != nil || string(got) != body {
			t.Errorf("%s restored as %q (%v), want %q", rel, got, err, body)
		}
	}
	if _, n, _ := RestoreXML(ctx, repo, fresh); n != 0 {
		t.Errorf("a restore over an archive as published unpacked %d packs", n)
	}
	// A file on disk is newer than the release: a restore keeps it.
	write("lta/1995/498.xml", "<fetched since/>")
	if _, _, err := RestoreXML(ctx, repo, xml); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(xml, "lta", "1995", "498.xml")); string(got) != "<fetched since/>" {
		t.Errorf("a restore replaced a file fetched since with %q", got)
	}
}
