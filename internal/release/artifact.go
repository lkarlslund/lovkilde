package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lkarlslund/lovkilde/internal/artifactdir"
	"github.com/lkarlslund/lovkilde/internal/compiled"
	"github.com/lkarlslund/lovkilde/internal/xet"
)

const (
	// DefaultEvery is how often a server checks for a newer artifact: a release is published
	// daily, and a check that finds the current one downloads only the manifest.
	DefaultEvery = 24 * time.Hour
	// CheckedFile, in the data directory, records when the last check finished, so the schedule
	// survives restarts: a server restarted more often than it checks would otherwise never check.
	CheckedFile = "artifact.checked"
)

// Installed says what FetchArtifact did.
type Installed struct {
	Manifest  Manifest
	Path      string // the installed artifact, or the current one when nothing was downloaded
	Installed bool   // false when the current artifact is already the published one
	// Newer is the document model of published data this program cannot read, 0 when there is
	// none: the latest release needs a newer program, and until it is upgraded the server keeps
	// the last artifact of its own model.
	Newer int
	Transfer
}

// ErrProgramTooOld is published data that needs a newer program, with none of this program's
// model published beside it.
var ErrProgramTooOld = errors.New("the published data needs a newer version of lovkilde")

// ErrDataTooOld is a program newer than every published artifact: the release with its model is
// not out yet.
var ErrDataTooOld = errors.New("no published artifact is of the document model this version of lovkilde reads yet")

// Transfer says how the downloaded files came to be on disk.
type Transfer struct {
	Downloaded int64 // bytes received over the network
	Reused     int64 // bytes copied from the artifacts already on disk, which Xet storage shares
	// XetErr is why a file in Xet storage was downloaded whole instead; nil when none was.
	XetErr error
}

func (t *Transfer) add(u Transfer) {
	t.Downloaded += u.Downloaded
	t.Reused += u.Reused
	if u.XetErr != nil {
		t.XetErr = u.XetErr
	}
}

// Available is the published artifact this program reads, as CheckArtifact found it.
type Available struct {
	Manifest Manifest
	Ref      FileRef // the artifact this program reads
	// Current is the artifact in use when it is the published one, "" when a newer one is
	// published.
	Current string
	// Newer is the document model of published data this program cannot read, 0 when there is
	// none.
	Newer int
}

// CheckArtifact reads the release manifest and says whether the published artifact this program
// reads is the current one in dataDir. It downloads nothing but the manifest.
func CheckArtifact(ctx context.Context, r Repo, dataDir string) (Available, error) {
	m, err := Latest(ctx, r)
	if err != nil {
		return Available{}, err
	}
	out := Available{Manifest: m}
	if m.File == "" {
		return out, fmt.Errorf("the release in %s holds no artifact", r)
	}
	// The artifact this program reads: the latest one, or the last one of its own model when the
	// latest needs a newer program. The model is checked before anything is downloaded.
	ref, ok, newer := m.For(compiled.ModelVersion)
	out.Ref, out.Newer = ref, newer
	switch {
	case !ok && newer > 0:
		return out, fmt.Errorf("the published artifact %s holds document model %d and this version of lovkilde reads model %d: upgrade lovkilde: %w", m.File, m.Model, compiled.ModelVersion, ErrProgramTooOld)
	case !ok:
		return out, fmt.Errorf("the published artifact %s holds document model %d and this version of lovkilde reads model %d, which is not published yet: %w", m.File, m.Model, compiled.ModelVersion, ErrDataTooOld)
	case !artifactdir.IsArtifactName(ref.Path):
		return out, fmt.Errorf("the release in %s names %q, which is not an artifact file name", r, ref.Path)
	}
	if cur, err := artifactdir.Current(dataDir); err == nil && filepath.Base(cur) == ref.Path {
		out.Current = cur
	}
	return out, nil
}

// FetchArtifact installs the published artifact into dataDir and makes it current, unless the
// current artifact already is it. A running server follows the new pointer by itself.
func FetchArtifact(ctx context.Context, r Repo, dataDir string) (Installed, error) {
	av, err := CheckArtifact(ctx, r, dataDir)
	out := Installed{Manifest: av.Manifest, Newer: av.Newer}
	if err != nil {
		return out, err
	}
	ref := av.Ref
	if av.Current != "" {
		out.Path = av.Current
		return out, nil
	}
	if err := os.MkdirAll(artifactdir.Dir(dataDir), 0o755); err != nil {
		return out, err
	}
	if have := filepath.Join(artifactdir.Dir(dataDir), ref.Path); fileExists(have) {
		// Downloaded before and since replaced, or installed by hand: verified when it was installed.
		if err := checkModel(have); err != nil {
			return out, err
		}
		if err := artifactdir.SetCurrent(dataDir, ref.Path); err != nil {
			return out, err
		}
		out.Path, out.Installed = have, true
		return out, nil
	}
	part := filepath.Join(artifactdir.Dir(dataDir), ref.Path+artifactdir.PartExt)
	t, err := downloadArtifact(ctx, r, ref, part)
	out.add(t)
	if err != nil {
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
	if filepath.Base(path) != ref.Path {
		// The header names the file; a manifest naming it otherwise is a publishing slip, and the
		// next check would download it again. Say so rather than loop quietly.
		return out, fmt.Errorf("installed %s, but the manifest calls it %s", filepath.Base(path), ref.Path)
	}
	return out, nil
}

// downloadArtifact is download for an artifact file. One in Xet storage is assembled from the
// chunks it shares with the artifacts beside dst and the others, fetched; when that fails, it is
// downloaded whole.
func downloadArtifact(ctx context.Context, r Repo, f FileRef, dst string) (Transfer, error) {
	var t Transfer
	if err := f.described(r); err != nil {
		return t, err
	}
	if hf, ok := r.(*HuggingFace); ok {
		hash, st, err := hf.downloadXet(ctx, f.Path, dst)
		t.Downloaded, t.Reused = st.Fetched, st.Reused
		if err == nil {
			if err = verify(dst, f); err == nil {
				// Recorded for the next download, which reads what the two share from this one.
				_ = os.WriteFile(filepath.Join(filepath.Dir(dst), f.Path+artifactdir.XetExt), []byte(hash+"\n"), 0o644)
				return t, nil
			}
			os.Remove(dst)
		}
		if !errors.Is(err, xet.ErrNotXet) {
			t.XetErr = err
		}
		t.Reused = 0
	}
	n, err := download(ctx, r, f, dst)
	t.Downloaded += n
	return t, err
}

// download copies a published file to dst, checking its size and hash, and returns how many
// bytes it received; dst is removed when they do not match.
func download(ctx context.Context, r Repo, f FileRef, dst string) (int64, error) {
	if err := f.described(r); err != nil {
		return 0, err
	}
	rc, err := r.Open(ctx, f.Path)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(rc, f.Size+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = f.check(n, h.Sum(nil))
	}
	if err != nil {
		os.Remove(dst)
	}
	return n, err
}

// verify checks a file on disk against what the manifest says it is.
func verify(path string, f FileRef) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	h := sha256.New()
	n, err := io.Copy(h, in)
	if err != nil {
		return err
	}
	return f.check(n, h.Sum(nil))
}

// described says whether the manifest gives what f must be.
func (f FileRef) described(r Repo) error {
	if len(f.SHA256) != 64 || f.Size < 0 {
		return fmt.Errorf("the release in %s gives no size or SHA-256 for %s", r, f.Path)
	}
	return nil
}

func (f FileRef) check(size int64, sum []byte) error {
	switch {
	case size != f.Size:
		return fmt.Errorf("downloaded %d bytes of %s, the manifest says %d", size, f.Path, f.Size)
	case hex.EncodeToString(sum) != strings.ToLower(f.SHA256):
		return fmt.Errorf("%s does not match the SHA-256 in the manifest", f.Path)
	}
	return nil
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

// LastChecked is when MarkChecked last ran for dataDir; zero when it never did.
func LastChecked(dataDir string) time.Time {
	info, err := os.Stat(filepath.Join(dataDir, CheckedFile))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
