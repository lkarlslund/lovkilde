package release

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/artifactdir"
	"github.com/lkarlslund/retsinformation/internal/compiled"
)

// A release in a new document model keeps the last artifact of the model before it published,
// so a server that has not been upgraded keeps something it can read; a release in the same
// model replaces its artifact, and only the last two older models are kept.
func TestANewModelKeepsTheLastArtifactOfTheOldOne(t *testing.T) {
	ref := func(p string) FileRef { return FileRef{Path: p} }
	paths := func(o []ModelArtifact) (out []string) {
		for _, a := range o {
			out = append(out, a.Path)
		}
		return out
	}
	steps := []struct {
		prev    Manifest
		model   int
		older   []string
		dropped []string
	}{
		{Manifest{File: "a", Model: 1}, 1, nil, []string{"a"}},
		{Manifest{File: "b", Model: 1}, 2, []string{"b"}, nil},
		{Manifest{File: "c", Model: 2, Older: []ModelArtifact{{1, ref("b")}}}, 2, []string{"b"}, []string{"c"}},
		{Manifest{File: "d", Model: 2, Older: []ModelArtifact{{1, ref("b")}}}, 3, []string{"d", "b"}, nil},
		{Manifest{File: "e", Model: 3, Older: []ModelArtifact{{2, ref("d")}, {1, ref("b")}}}, 4, []string{"e", "d"}, []string{"b"}},
	}
	for i, s := range steps {
		older, drop := keepOlder(s.prev, s.model)
		if !slices.Equal(paths(older), s.older) || !slices.Equal(drop, s.dropped) {
			t.Errorf("step %d: kept %v and dropped %v, want %v and %v", i, paths(older), drop, s.older, s.dropped)
		}
	}
}

// A server whose program reads an older model than the newest release installs the last artifact
// of its own model and reports that the newer data needs an upgrade; with none of its model
// published it installs nothing; and a program ahead of every release says it is waiting.
func TestAServerInstallsOnlyWhatItsProgramReads(t *testing.T) {
	repo := &Local{Dir: t.TempDir()}
	art := buildArtifact(t, t.TempDir(), "Lov")
	m := publish(t, repo, art, stateDir(t, "a\n"))
	setManifest := func(m Manifest) {
		t.Helper()
		raw, _ := json.Marshal(m)
		if err := os.WriteFile(filepath.Join(repo.Dir, ManifestFile), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mine := ModelArtifact{Model: compiled.ModelVersion, FileRef: FileRef{Path: m.File, Size: m.Size, SHA256: m.SHA256}}
	newer := Manifest{File: "artifact-29990101T0000Z-ffffffff.retsin", Model: compiled.ModelVersion + 1}

	withOlder := newer
	withOlder.Older = []ModelArtifact{mine}
	setManifest(withOlder)
	data := t.TempDir()
	res, err := FetchArtifact(context.Background(), repo, data)
	if err != nil || !res.Installed || res.Newer != compiled.ModelVersion+1 {
		t.Fatalf("with its own model kept: %+v, %v", res, err)
	}
	if cur, _ := artifactdir.Current(data); filepath.Base(cur) != m.File {
		t.Errorf("current is %s, want the kept artifact %s", cur, m.File)
	}

	setManifest(newer)
	empty := t.TempDir()
	if res, err := FetchArtifact(context.Background(), repo, empty); !errors.Is(err, ErrProgramTooOld) || res.Newer != compiled.ModelVersion+1 {
		t.Errorf("with none of its model published: %+v, %v", res, err)
	}
	if _, err := artifactdir.Current(empty); !errors.Is(err, artifactdir.ErrNone) {
		t.Errorf("an artifact the program cannot read was installed: %v", err)
	}

	setManifest(Manifest{File: newer.File, Model: compiled.ModelVersion - 1})
	if _, err := FetchArtifact(context.Background(), repo, t.TempDir()); !errors.Is(err, ErrDataTooOld) {
		t.Errorf("a program ahead of the release: %v", err)
	}
}
