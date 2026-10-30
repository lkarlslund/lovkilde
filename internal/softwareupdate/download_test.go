package softwareupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// published packs body as this platform's archive, as scripts/build-server.sh does.
func published(t *testing.T, body string) []byte {
	t.Helper()
	dir := strings.TrimSuffix(strings.TrimSuffix(assetName(), ".zip"), ".tar.gz")
	var buf bytes.Buffer
	if strings.HasSuffix(assetName(), ".zip") {
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create(dir + "/" + exeName())
		w.Write([]byte(body))
		zw.Close()
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: dir + "/" + exeName(), Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestDownloadKeepsTheBuildNamedForItsStamp(t *testing.T) {
	archive := published(t, "new build")
	sum := sha256.Sum256(archive)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/SHA256SUMS":
			fmt.Fprintf(w, "%x  %s\n", sum, assetName())
		case "/" + assetName():
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	stamp := Stamp{Revision: "0123456789abcdef", Time: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	defer func(f func(string) (Stamp, error)) { readStamp = f }(readStamp)
	readStamp = func(string) (Stamp, error) { return stamp, nil }

	dir := t.TempDir()
	older := filepath.Join(dir, ProgramName(Stamp{Revision: "fedcba987654", Time: stamp.Time.Add(-time.Hour)}))
	os.WriteFile(older, []byte("old build"), 0o755)

	c := &Checker{DownloadBase: srv.URL}
	path, _, err := c.Download(context.Background(), dir, "r00001")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new build" {
		t.Fatalf("kept %q", got)
	}
	newest, s, ok := Newest(dir)
	if !ok || newest != path || s.Revision != "0123456789ab" || !s.Time.Equal(stamp.Time) {
		t.Fatalf("Newest = %s %+v %v, want %s", newest, s, ok, path)
	}
	Prune(dir, path)
	if _, err := os.Stat(older); !os.IsNotExist(err) {
		t.Errorf("the older build is still kept")
	}
}

func TestDownloadRefusesAnArchiveThatDoesNotMatchItsSum(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/SHA256SUMS" {
			fmt.Fprintf(w, "%064x  %s\n", 0, assetName())
			return
		}
		w.Write(published(t, "tampered"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	if _, _, err := (&Checker{DownloadBase: srv.URL}).Download(context.Background(), dir, "r00001"); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("err = %v, want a SHA-256 mismatch", err)
	}
	if _, _, ok := Newest(dir); ok {
		t.Error("a build that did not match its sum was kept")
	}
}
