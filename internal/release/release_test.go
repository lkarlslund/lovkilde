package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/artifactdir"
	"github.com/lkarlslund/retsinformation/internal/compiled"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// buildArtifact writes a one-document artifact into dir and returns its path.
func buildArtifact(t *testing.T, dir, title string) string {
	t.Helper()
	var buf bytes.Buffer
	doc := &lexdoc.Document{Identity: lexdoc.Identity{Accession: "A1", Title: title}}
	h, _, err := compiled.Build(&buf, "test", []compiled.ArtifactDoc{{Accession: "A1", Document: doc}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, artifactdir.Name(*h))
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// stateDir writes two state files and returns their directory.
func stateDir(t *testing.T, inventory string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"meta.json": `{"version":2}`, "inventory.jsonl": inventory} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func publish(t *testing.T, r Repo, artifact, state string) Manifest {
	t.Helper()
	m, err := Publish(context.Background(), r, Release{Artifact: artifact, StateDir: state, StateFiles: []string{"meta.json", "inventory.jsonl", "bodies.jsonl"}})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A release published to a directory comes back out of it: the artifact installs and is
// current, the state restores byte for byte, and asking again downloads nothing.
func TestAReleaseRoundTripsThroughADirectory(t *testing.T) {
	repo := &Local{Dir: t.TempDir()}
	art := buildArtifact(t, t.TempDir(), "Lov")
	m := publish(t, repo, art, stateDir(t, `{"url":"u1"}`+"\n"))
	if len(m.State) != 2 {
		t.Fatalf("published state %+v; a missing file is left out, the others are in", m.State)
	}

	data := t.TempDir()
	res, err := FetchArtifact(context.Background(), repo, data)
	if err != nil || !res.Installed {
		t.Fatalf("fetch: %+v, %v", res, err)
	}
	if cur, _ := artifactdir.Current(data); filepath.Base(cur) != filepath.Base(art) {
		t.Errorf("current is %s, want %s", cur, art)
	}
	if res, err = FetchArtifact(context.Background(), repo, data); err != nil || res.Installed {
		t.Errorf("a second fetch installed again: %+v, %v", res, err)
	}

	state := filepath.Join(t.TempDir(), "state")
	if _, err := RestoreState(context.Background(), repo, state); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(state, "inventory.jsonl")); string(got) != `{"url":"u1"}`+"\n" {
		t.Errorf("restored inventory %q", got)
	}
}

// A new release replaces the artifact it published before, and a state-only release keeps it.
func TestANewReleaseReplacesTheArtifact(t *testing.T) {
	repo := &Local{Dir: t.TempDir()}
	first := buildArtifact(t, t.TempDir(), "Lov")
	publish(t, repo, first, stateDir(t, "a\n"))
	time.Sleep(10 * time.Millisecond) // a distinct build time, so a distinct name
	second := buildArtifact(t, t.TempDir(), "Lov 2")
	m := publish(t, repo, second, stateDir(t, "b\n"))
	if _, err := os.Stat(filepath.Join(repo.Dir, filepath.Base(first))); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the replaced artifact is still published: %v", err)
	}
	kept, err := Publish(context.Background(), repo, Release{StateDir: stateDir(t, "c\n"), StateFiles: []string{"inventory.jsonl"}})
	if err != nil || kept.File != m.File || kept.SHA256 != m.SHA256 {
		t.Errorf("a state-only release: %+v, %v; want the artifact %s kept", kept, err, m.File)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, m.File)); err != nil {
		t.Errorf("a state-only release removed the artifact: %v", err)
	}
	// The card describes the release it was published with, the state-only one included.
	card, err := os.ReadFile(filepath.Join(repo.Dir, CardFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| Documents in the artifact | 1 |", "`" + m.File + "`", kept.Published.Format("2006-01-02 15:04 UTC")} {
		if !strings.Contains(string(card), want) {
			t.Errorf("the card does not say %q:\n%s", want, card)
		}
	}
}

// What the manifest promises is checked before anything becomes current: the size and hash of
// the download, and the document model before downloading at all.
func TestADownloadThatIsNotWhatTheManifestSaysIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		spoil func(repo, file string, m *Manifest)
		want  string
	}{
		"altered": {func(repo, file string, _ *Manifest) {
			b, _ := os.ReadFile(file)
			b[len(b)-1] ^= 1
			os.WriteFile(file, b, 0o644)
		}, "SHA-256"},
		"truncated": {func(repo, file string, _ *Manifest) {
			b, _ := os.ReadFile(file)
			os.WriteFile(file, b[:len(b)/2], 0o644)
		}, "bytes"},
		"other model": {func(_, _ string, m *Manifest) { m.Model++ }, "upgrade retsin"},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &Local{Dir: t.TempDir()}
			m := publish(t, repo, buildArtifact(t, t.TempDir(), "Lov"), stateDir(t, ""))
			published := filepath.Join(repo.Dir, m.File)
			// Break the hard link first, so spoiling the published copy leaves the source alone.
			b, _ := os.ReadFile(published)
			os.Remove(published)
			os.WriteFile(published, b, 0o644)
			c.spoil(repo.Dir, published, &m)
			raw, _ := json.MarshalIndent(m, "", " ")
			os.WriteFile(filepath.Join(repo.Dir, ManifestFile), raw, 0o644)

			data := t.TempDir()
			_, err := FetchArtifact(context.Background(), repo, data)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("fetch: %v, want an error naming %q", err, c.want)
			}
			if _, err := artifactdir.Current(data); !errors.Is(err, artifactdir.ErrNone) {
				t.Errorf("a refused download became current: %v", err)
			}
			if parts, _ := filepath.Glob(filepath.Join(artifactdir.Dir(data), "*"+artifactdir.PartExt)); len(parts) != 0 {
				t.Errorf("a refused download was left behind: %v", parts)
			}
		})
	}
}

// A state file that does not match the manifest restores nothing at all.
func TestARestoreIsAllOrNothing(t *testing.T) {
	repo := &Local{Dir: t.TempDir()}
	publish(t, repo, buildArtifact(t, t.TempDir(), "Lov"), stateDir(t, "rows\n"))
	inv := filepath.Join(repo.Dir, StateDir, "inventory.jsonl")
	os.Remove(inv) // the hard link
	os.WriteFile(inv, []byte("other\n"), 0o644)
	state := t.TempDir()
	if _, err := RestoreState(context.Background(), repo, state); err == nil {
		t.Fatal("a state file that does not match the manifest was restored")
	}
	if left, _ := os.ReadDir(state); len(left) != 0 {
		t.Errorf("a failed restore left %d files", len(left))
	}
}

// A repository is named by one string, for both programs.
func TestARepositoryIsNamedByOneString(t *testing.T) {
	for spec, want := range map[string]string{
		"/srv/releases":           "/srv/releases",
		"file:///srv/releases":    "/srv/releases",
		"hf://owner/name":         "hf://owner/name",
		"hf://owner/name@nightly": "hf://owner/name@nightly",
	} {
		r, err := Open(spec)
		if err != nil || r.String() != want {
			t.Errorf("Open(%q) = %v, %v; want %s", spec, r, err, want)
		}
	}
	for _, bad := range []string{"hf://owner", "hf://a/b/c"} {
		if _, err := Open(bad); err == nil {
			t.Errorf("Open(%q) accepted", bad)
		}
	}
	if _, err := Open(""); !errors.Is(err, ErrNoRepo) {
		t.Errorf("Open(\"\") = %v", err)
	}
}

// Hugging Face downloads through the resolve endpoint, sending HF_TOKEN, and a missing file is
// fs.ErrNotExist.
func TestHuggingFaceDownloads(t *testing.T) {
	t.Setenv("HF_TOKEN", "secret")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/datasets/owner/name/resolve/main/latest.json" {
			io.WriteString(w, `{"file":"x.retsin"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	hf := &HuggingFace{Repo: "owner/name", BaseURL: srv.URL}
	if m, err := Latest(context.Background(), hf); err != nil || m.File != "x.retsin" {
		t.Errorf("manifest: %+v, %v", m, err)
	}
	if _, err := hf.Open(context.Background(), "state/meta.json"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing file: %v", err)
	}
}

// A commit is one hf upload of the files staged under their paths, with the deletions in the
// same commit.
func TestAHuggingFaceCommitIsOneUpload(t *testing.T) {
	dir := t.TempDir()
	got := filepath.Join(dir, "got")
	fake := filepath.Join(dir, "hf")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + got + ".args\ncp -r \"$3\" " + got + "\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	from := filepath.Join(dir, "meta.json")
	if err := os.WriteFile(from, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	hf := &HuggingFace{Repo: "owner/name", Revision: "weekly", CLI: fake}
	err := hf.Commit(context.Background(), Commit{Message: "Release", Add: []Upload{{Path: "state/meta.json", From: from}}, Delete: []string{"old.retsin"}})
	if err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(got, "state", "meta.json")); err != nil || string(body) != "{}" {
		t.Errorf("staged state/meta.json: %q, %v", body, err)
	}
	args, _ := os.ReadFile(got + ".args")
	want := "upload\nowner/name\n.*\n.\n--repo-type\ndataset\n--commit-message\nRelease\n--revision\nweekly\n--delete\nold.retsin\n"
	if ok, _ := regexp.MatchString("^"+want+"$", string(args)); !ok {
		t.Errorf("hf was run with\n%s", args)
	}
	if err := hf.Commit(context.Background(), Commit{Delete: []string{"*.retsin"}}); err == nil {
		t.Errorf("a deletion that is a pattern was handed to hf")
	}
}

// The schedule is kept on disk, so a restart neither resets nor repeats it.
func TestTheCheckScheduleSurvivesARestart(t *testing.T) {
	data := t.TempDir()
	if !Due(data, time.Hour, time.Now()) {
		t.Error("a directory never checked is not due")
	}
	if err := MarkChecked(data); err != nil {
		t.Fatal(err)
	}
	if Due(data, time.Hour, time.Now()) {
		t.Error("due right after a check")
	}
	if !Due(data, time.Hour, time.Now().Add(2*time.Hour)) {
		t.Error("not due after the interval")
	}
}
