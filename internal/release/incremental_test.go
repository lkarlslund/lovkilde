package release

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
