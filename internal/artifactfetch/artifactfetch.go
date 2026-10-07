// Package artifactfetch installs a published artifact from Hugging Face, so a server needs no
// retsin-update of its own: it downloads the artifact when it has none, and checks for a newer one
// on a schedule.
//
// PLACEHOLDER: nothing is published yet. DefaultRepo is empty, which turns every download off with
// a message saying so; the repository layout below is the one the publishing side is expected to
// write, and is the thing to adjust when it exists.
//
// A release is a manifest beside the artifact it names, in a Hugging Face dataset repository:
//
//	latest.json                                   {"file": …, "size": …, "sha256": …, "model": …}
//	artifact-20261007T0648Z-61f208f4.retsin       the artifact it names
//
// The manifest carries the document model so that a binary never downloads an artifact it would
// refuse to serve, and the size and SHA-256 so that a truncated or altered download never becomes
// current. A downloaded file is then opened and its model checked before it is installed
// (artifactdir.Install), because the manifest is a claim and the header is the file.
package artifactfetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lkarlslund/retsinformation/internal/artifactdir"
	"github.com/lkarlslund/retsinformation/internal/compiled"
)

const (
	// DefaultRepo is the dataset repository artifacts are published to. PLACEHOLDER: empty until
	// the first artifact is uploaded; set it then.
	DefaultRepo = ""
	// DefaultRevision is the branch or tag read.
	DefaultRevision = "main"
	// DefaultBaseURL is Hugging Face's.
	DefaultBaseURL = "https://huggingface.co"
	// ManifestFile names the release manifest at the repository root.
	ManifestFile = "latest.json"
	// DefaultEvery is how often a server checks for a newer artifact.
	DefaultEvery = 7 * 24 * time.Hour
	// CheckedFile, in the data directory, records when the last check finished, so the schedule
	// survives restarts: a server restarted every few days would otherwise never reach a week.
	CheckedFile = "artifact.checked"
)

// ErrNoRepo is returned when no repository is configured, which until the first upload is always.
var ErrNoRepo = errors.New("no Hugging Face repository is configured for artifacts (none is published yet; pass --hf-repo when one is)")

// Source is where artifacts are published.
type Source struct {
	Repo     string // dataset repository, "owner/name"
	Revision string // branch or tag; DefaultRevision when empty
	BaseURL  string // DefaultBaseURL when empty; tests point it at a local server
	Client   *http.Client
}

// Manifest is a release: the artifact it names and what that file must be.
type Manifest struct {
	File   string `json:"file"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Model  int    `json:"model"`
}

// Result says what Fetch did.
type Result struct {
	Manifest  Manifest
	Path      string // the installed artifact, or the current one when nothing was downloaded
	Installed bool   // false when the current artifact is already the published one
}

func (s Source) url(file string) string {
	base, rev := s.BaseURL, s.Revision
	if base == "" {
		base = DefaultBaseURL
	}
	if rev == "" {
		rev = DefaultRevision
	}
	return strings.TrimRight(base, "/") + "/datasets/" + s.Repo + "/resolve/" + url.PathEscape(rev) + "/" + file
}

func (s Source) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

func (s Source) get(ctx context.Context, file string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url(file), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", s.url(file), resp.Status)
	}
	return resp, nil
}

// Latest reads the release manifest.
func (s Source) Latest(ctx context.Context) (Manifest, error) {
	var m Manifest
	if s.Repo == "" {
		return m, ErrNoRepo
	}
	resp, err := s.get(ctx, ManifestFile)
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m); err != nil {
		return m, fmt.Errorf("%s: %w", s.url(ManifestFile), err)
	}
	if m.File == "" || strings.ContainsAny(m.File, `/\`) || !strings.HasSuffix(m.File, artifactdir.Ext) {
		return m, fmt.Errorf("%s names %q, which is not an artifact file name", s.url(ManifestFile), m.File)
	}
	if len(m.SHA256) != 64 || m.Size <= 0 {
		return m, fmt.Errorf("%s gives no size or SHA-256 for %s", s.url(ManifestFile), m.File)
	}
	return m, nil
}

// Fetch installs the published artifact into dataDir and makes it current, unless the current
// artifact already is it. A running server follows the new pointer by itself.
func (s Source) Fetch(ctx context.Context, dataDir string) (Result, error) {
	m, err := s.Latest(ctx)
	if err != nil {
		return Result{}, err
	}
	if m.Model != compiled.ModelVersion {
		return Result{Manifest: m}, fmt.Errorf("the published artifact %s holds document model %d and this binary reads model %d: upgrade retsin", m.File, m.Model, compiled.ModelVersion)
	}
	if cur, err := artifactdir.Current(dataDir); err == nil && filepath.Base(cur) == m.File {
		return Result{Manifest: m, Path: cur}, nil
	}
	if have := filepath.Join(artifactdir.Dir(dataDir), m.File); fileExists(have) {
		// Downloaded before and since replaced, or installed by hand: verified when it was installed.
		if err := artifactdir.SetCurrent(dataDir, m.File); err != nil {
			return Result{Manifest: m}, err
		}
		return Result{Manifest: m, Path: have, Installed: true}, nil
	}
	part, err := s.download(ctx, dataDir, m)
	if err != nil {
		return Result{Manifest: m}, err
	}
	if err := checkModel(part); err != nil {
		os.Remove(part)
		return Result{Manifest: m}, err
	}
	path, err := artifactdir.Install(dataDir, part)
	if err != nil {
		os.Remove(part)
		return Result{Manifest: m}, err
	}
	if filepath.Base(path) != m.File {
		// The header names the file; a manifest naming it otherwise is a publishing slip, and the
		// next check would download it again. Say so rather than loop quietly.
		return Result{Manifest: m, Path: path, Installed: true}, fmt.Errorf("installed %s, but the manifest calls it %s", filepath.Base(path), m.File)
	}
	return Result{Manifest: m, Path: path, Installed: true}, nil
}

// download writes the artifact under a .part name beside the others, checking size and hash.
func (s Source) download(ctx context.Context, dataDir string, m Manifest) (string, error) {
	if err := os.MkdirAll(artifactdir.Dir(dataDir), 0o755); err != nil {
		return "", err
	}
	part := filepath.Join(artifactdir.Dir(dataDir), m.File+artifactdir.PartExt)
	resp, err := s.get(ctx, m.File)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.Create(part)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, m.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
	case n != m.Size:
		err = fmt.Errorf("downloaded %d bytes of %s, the manifest says %d", n, m.File, m.Size)
	case hex.EncodeToString(h.Sum(nil)) != strings.ToLower(m.SHA256):
		err = fmt.Errorf("%s does not match the SHA-256 in the manifest", m.File)
	}
	if err != nil {
		os.Remove(part)
		return "", err
	}
	return part, nil
}

func checkModel(path string) error {
	a, err := compiled.OpenArtifact(path)
	if err != nil {
		return err
	}
	defer a.Close()
	return a.CheckModel()
}

// Due says whether a scheduled check is due: every has passed since the last one finished.
func Due(dataDir string, every time.Duration, now time.Time) bool {
	info, err := os.Stat(filepath.Join(dataDir, CheckedFile))
	return err != nil || now.Sub(info.ModTime()) >= every
}

// MarkChecked records that a check finished now, whatever it found.
func MarkChecked(dataDir string) error {
	return os.WriteFile(filepath.Join(dataDir, CheckedFile), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
