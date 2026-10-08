package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lkarlslund/retsinformation/internal/artifactdir"
	"github.com/lkarlslund/retsinformation/internal/compiled"
)

const (
	// DefaultEvery is how often a server checks for a newer artifact.
	DefaultEvery = 7 * 24 * time.Hour
	// CheckedFile, in the data directory, records when the last check finished, so the schedule
	// survives restarts: a server restarted every few days would otherwise never reach a week.
	CheckedFile = "artifact.checked"
)

// Installed says what FetchArtifact did.
type Installed struct {
	Manifest  Manifest
	Path      string // the installed artifact, or the current one when nothing was downloaded
	Installed bool   // false when the current artifact is already the published one
}

// FetchArtifact installs the published artifact into dataDir and makes it current, unless the
// current artifact already is it. A running server follows the new pointer by itself.
func FetchArtifact(ctx context.Context, r Repo, dataDir string) (Installed, error) {
	m, err := Latest(ctx, r)
	if err != nil {
		return Installed{}, err
	}
	out := Installed{Manifest: m}
	switch {
	case m.File == "":
		return out, fmt.Errorf("the release in %s holds no artifact", r)
	case !strings.HasSuffix(m.File, artifactdir.Ext) || strings.Contains(m.File, "/"):
		return out, fmt.Errorf("the release in %s names %q, which is not an artifact file name", r, m.File)
	case m.Model != compiled.ModelVersion:
		return out, fmt.Errorf("the published artifact %s holds document model %d and this binary reads model %d: upgrade retsin", m.File, m.Model, compiled.ModelVersion)
	}
	if cur, err := artifactdir.Current(dataDir); err == nil && filepath.Base(cur) == m.File {
		out.Path = cur
		return out, nil
	}
	if err := os.MkdirAll(artifactdir.Dir(dataDir), 0o755); err != nil {
		return out, err
	}
	// A layer is read with its base beside it, so the base comes first: usually it is the
	// artifact this server already has, and only the layer is downloaded.
	if b := m.Base; b != nil && !fileExists(filepath.Join(artifactdir.Dir(dataDir), b.Path)) {
		if !strings.HasSuffix(b.Path, artifactdir.Ext) {
			return out, fmt.Errorf("the release in %s names %q as the base, which is not an artifact file name", r, b.Path)
		}
		part := filepath.Join(artifactdir.Dir(dataDir), b.Path+artifactdir.PartExt)
		if err := download(ctx, r, *b, part); err != nil {
			return out, err
		}
		placed, err := artifactdir.Place(dataDir, part)
		if err != nil {
			os.Remove(part)
			return out, err
		}
		if filepath.Base(placed) != b.Path {
			return out, fmt.Errorf("placed the base as %s, but the manifest calls it %s", filepath.Base(placed), b.Path)
		}
	}
	if have := filepath.Join(artifactdir.Dir(dataDir), m.File); fileExists(have) {
		// Downloaded before and since replaced, or installed by hand: verified when it was installed.
		if err := artifactdir.SetCurrent(dataDir, m.File); err != nil {
			return out, err
		}
		out.Path, out.Installed = have, true
		return out, nil
	}
	part := filepath.Join(artifactdir.Dir(dataDir), m.File+artifactdir.PartExt)
	if err := download(ctx, r, FileRef{Path: m.File, Size: m.Size, SHA256: m.SHA256}, part); err != nil {
		return out, err
	}
	if err := checkModel(part); err != nil {
		os.Remove(part)
		return out, err
	}
	path, err := artifactdir.Install(dataDir, part)
	if err != nil {
		os.Remove(part)
		return out, err
	}
	out.Path, out.Installed = path, true
	if filepath.Base(path) != m.File {
		// The header names the file; a manifest naming it otherwise is a publishing slip, and the
		// next check would download it again. Say so rather than loop quietly.
		return out, fmt.Errorf("installed %s, but the manifest calls it %s", filepath.Base(path), m.File)
	}
	return out, nil
}

// download copies a published file to dst, checking its size and hash; dst is removed when
// they do not match.
func download(ctx context.Context, r Repo, f FileRef, dst string) error {
	if len(f.SHA256) != 64 || f.Size < 0 {
		return fmt.Errorf("the release in %s gives no size or SHA-256 for %s", r, f.Path)
	}
	rc, err := r.Open(ctx, f.Path)
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(rc, f.Size+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
	case n != f.Size:
		err = fmt.Errorf("downloaded %d bytes of %s, the manifest says %d", n, f.Path, f.Size)
	case hex.EncodeToString(h.Sum(nil)) != strings.ToLower(f.SHA256):
		err = fmt.Errorf("%s does not match the SHA-256 in the manifest", f.Path)
	}
	if err != nil {
		os.Remove(dst)
	}
	return err
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
