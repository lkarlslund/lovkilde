package artifactfetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/artifactdir"
	"github.com/lkarlslund/retsinformation/internal/compiled"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// publish serves a one-document artifact the way the Hugging Face resolve endpoint would, with a
// manifest that edit may spoil, and returns the source and the artifact's file name.
func publish(t *testing.T, edit func(*Manifest, []byte) []byte) (Source, string, *int) {
	t.Helper()
	var buf bytes.Buffer
	doc := &lexdoc.Document{Identity: lexdoc.Identity{Accession: "A1", Title: "Lov"}}
	h, _, err := compiled.Build(&buf, "test", []compiled.ArtifactDoc{{Accession: "A1", Document: doc}})
	if err != nil {
		t.Fatal(err)
	}
	body := buf.Bytes()
	sum := sha256.Sum256(body)
	m := Manifest{File: artifactdir.Name(*h), Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), Model: compiled.ModelVersion}
	if edit != nil {
		body = edit(&m, body)
	}
	downloads := new(int)
	mux := http.NewServeMux()
	mux.HandleFunc("/datasets/owner/repo/resolve/main/"+ManifestFile, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(m)
	})
	mux.HandleFunc("/datasets/owner/repo/resolve/main/"+m.File, func(w http.ResponseWriter, _ *http.Request) {
		*downloads++
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return Source{Repo: "owner/repo", BaseURL: srv.URL}, m.File, downloads
}

// A data directory without an artifact gets the published one, current; asking again downloads
// nothing.
func TestFetchInstallsThePublishedArtifactOnce(t *testing.T) {
	src, name, downloads := publish(t, nil)
	data := t.TempDir()
	res, err := src.Fetch(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if cur, err := artifactdir.Current(data); err != nil || filepath.Base(cur) != name || !res.Installed {
		t.Fatalf("current is %q (%v), installed %v; want %s", cur, err, res.Installed, name)
	}
	if res, err = src.Fetch(context.Background(), data); err != nil || res.Installed || *downloads != 1 {
		t.Errorf("a second fetch: installed %v, %d downloads, %v", res.Installed, *downloads, err)
	}
}

// What the manifest promises is checked before anything becomes current: the size and hash of
// the download, and the document model before downloading at all.
func TestFetchRefusesWhatTheManifestDoesNotVouchFor(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(*Manifest, []byte) []byte
		want string
	}{
		"altered":     {func(_ *Manifest, b []byte) []byte { b = bytes.Clone(b); b[len(b)-1] ^= 1; return b }, "SHA-256"},
		"truncated":   {func(_ *Manifest, b []byte) []byte { return b[:len(b)/2] }, "bytes"},
		"other model": {func(m *Manifest, b []byte) []byte { m.Model++; return b }, "upgrade retsin"},
	} {
		t.Run(name, func(t *testing.T) {
			src, _, _ := publish(t, c.edit)
			data := t.TempDir()
			_, err := src.Fetch(context.Background(), data)
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

// Until something is published there is no repository, and fetching says so.
func TestFetchWithoutARepositorySaysSo(t *testing.T) {
	if _, err := (Source{}).Fetch(context.Background(), t.TempDir()); !errors.Is(err, ErrNoRepo) {
		t.Errorf("fetch with no repository: %v", err)
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
	if _, err := os.Stat(filepath.Join(data, CheckedFile)); err != nil {
		t.Error(err)
	}
}
