package updates

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/lovkilde/internal/checks"
	"github.com/lkarlslund/lovkilde/internal/compiled"
	"github.com/lkarlslund/lovkilde/internal/release"
	"github.com/lkarlslund/lovkilde/internal/softwareupdate"
)

// The checks find what is published and download nothing; the note tells the assistant what the
// user can start; an update is started only by Start, and one that fails says so.
func TestChecksOfferAndStartDownloads(t *testing.T) {
	restore := compiled.CurrentBuild
	t.Cleanup(func() { compiled.CurrentBuild = restore })
	compiled.CurrentBuild = func() (compiled.BuildStamp, bool) {
		return compiled.BuildStamp{Revision: "old", Time: time.Now().Add(-time.Hour)}, true
	}
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/n/releases/latest":
			fmt.Fprint(w, `{"tag_name":"r00042"}`)
		case "/repos/o/n/compare/old...r00042":
			fmt.Fprint(w, `{"status":"ahead","ahead_by":3}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer github.Close()

	// A release holding a manifest and no artifact file: the check reads only the manifest, and
	// the download that follows fails.
	repoDir := t.TempDir()
	m := release.Manifest{File: "artifact-20261029T1009Z-3dfba6d1.lkz", Size: 10, SHA256: strings.Repeat("0", 64),
		Model: compiled.ModelVersion, Published: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(repoDir, release.ManifestFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := release.Open(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	u := New(Config{
		DataDir:  dataDir,
		Checks:   checks.Open(dataDir),
		Repo:     repo,
		Software: &softwareupdate.Checker{Repo: "o/n", Revision: "old", APIBase: github.URL},
		Every:    checks.Off,
	})
	if u.Note() != "" {
		t.Errorf("a note before anything was checked: %q", u.Note())
	}

	got := u.Check(context.Background())
	for _, want := range []string{"Programmet: Version r00042 er udgivet, 3 ændringer", "Lovteksterne: Der er udgivet en nyere udgave af lovteksterne (30. oktober 2026)"} {
		if !strings.Contains(got, want) {
			t.Errorf("the check does not say %q:\n%s", want, got)
		}
	}
	entries, _ := os.ReadDir(dataDir)
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("a check downloaded something into %s", e.Name())
		}
	}
	note := u.Note()
	for _, want := range []string{"r00042", "law texts", `action "app"`, "only when the user asks", "never on your own"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not say %q: %s", want, note)
		}
	}

	if _, err := u.Start(context.Background(), "everything"); err == nil {
		t.Error("an unknown update was started")
	}
	if _, err := u.Start(context.Background(), Dataset); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 400 && u.State(Dataset).Running; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if st := u.State(Dataset); st.Running || !strings.Contains(st.Result, "kunne ikke hentes") {
		t.Errorf("a failed download: %+v", st)
	}
}
