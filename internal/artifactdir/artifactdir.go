// Package artifactdir is where compiled artifacts live on disk, how they are named, and which one
// is current.
//
// An artifact is named for when it was built and for its stamp (compiled.ArtifactHeader.Stamp),
// and its search index for the stamp alone:
//
//	data/artifacts/artifact-20261006T1520Z-8f3a2c1d.retsin   the artifact
//	data/artifacts/search-8f3a2c1d.idx                       the index cut from it
//	data/artifacts/artifact-20261007T0310Z-c41e9b07.retsin.part   one being written or downloaded
//	data/artifacts/artifact-20261006T1520Z-8f3a2c1d.retsin.xet    its Xet hash, when downloaded
//	data/artifact.current                                    the current artifact's file name
//
// Two artifacts are kept: the current one and the one before it. The new one is written beside the
// old under a .part name, becomes a .retsin only once it is complete and verified, and becomes
// current when the pointer is replaced — a rename, so a reader sees the old name or the new one
// and never half of either. A serving process notices the new pointer, builds the new index while
// it still answers from the old pair, and swaps both at once. The old pair stays on disk as the
// fallback until the next one replaces it.
//
// The extension is .retsin rather than .zst: the file is a zstd stream, but its documents are
// compressed against the dictionary in its second frame, and a name that invited `zstd -d` would
// fail on every document.
package artifactdir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lkarlslund/retsinformation/internal/compiled"
)

const (
	// Subdir holds the artifacts and their indexes, inside the data directory.
	Subdir = "artifacts"
	// PointerFile names the current artifact, inside the data directory.
	PointerFile = "artifact.current"
	// Ext is an artifact's extension; PartExt is appended while it is being written.
	Ext     = ".retsin"
	PartExt = ".part"
	// XetExt is appended for the file holding an artifact's Xet hash: what Hugging Face stores
	// it under, so that a later download reads the chunks the two share from it.
	XetExt = ".xet"

	// legacyArtifact and legacyIndex are where artifacts and indexes lived before this layout.
	legacyArtifact = "artifact.bin"
	legacyIndex    = "search.idx"

	// abandonedAfter is how old a .part file must be before pruning removes it: younger ones
	// may belong to a build or download that is still running.
	abandonedAfter = 24 * time.Hour
)

// ErrNone is returned when the data directory has no current artifact.
var ErrNone = errors.New("no current artifact")

// Dir returns the artifacts directory inside a data directory.
func Dir(dataDir string) string { return filepath.Join(dataDir, Subdir) }

// shortStamp is the part of a stamp a file name carries.
func shortStamp(stamp string) string {
	if len(stamp) >= 8 {
		return stamp[:8]
	}
	if stamp == "" {
		return "nostamp"
	}
	return stamp
}

// Name is the file name an artifact with this header is kept under.
func Name(h compiled.ArtifactHeader) string {
	return "artifact-" + h.Created.UTC().Format("20060102T1504Z") + "-" + shortStamp(h.Stamp) + Ext
}

// IndexName is the file name of the search index cut from the artifact with this stamp.
func IndexName(stamp string) string { return "search-" + shortStamp(stamp) + ".idx" }

// IndexPath is where the index for an artifact file is kept: beside it, named for its stamp.
func IndexPath(artifactPath, stamp string) string {
	return filepath.Join(filepath.Dir(artifactPath), IndexName(stamp))
}

// Current returns the path of the current artifact. A data directory from before this layout —
// data/artifact.bin with data/search.idx beside it — is migrated on the way: the artifact is moved
// to its proper name, its index with it when the index was cut from it, and the pointer written.
func Current(dataDir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, PointerFile))
	if errors.Is(err, fs.ErrNotExist) {
		return migrate(dataDir)
	}
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(raw))
	if name == "" || strings.ContainsAny(name, `/\`) || !strings.HasSuffix(name, Ext) {
		return "", fmt.Errorf("%s names %q, which is not an artifact file name", filepath.Join(dataDir, PointerFile), name)
	}
	path := filepath.Join(Dir(dataDir), name)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("%s names %s: %w", filepath.Join(dataDir, PointerFile), name, err)
	}
	return path, nil
}

// SetCurrent makes the artifact file in the artifacts directory current. The pointer is written
// under a temporary name and renamed, so a reader sees the old name or the new one.
func SetCurrent(dataDir, name string) error {
	if _, err := os.Stat(filepath.Join(Dir(dataDir), name)); err != nil {
		return fmt.Errorf("cannot make %s current: %w", name, err)
	}
	tmp := filepath.Join(dataDir, PointerFile+".writing")
	if err := os.WriteFile(tmp, []byte(name+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dataDir, PointerFile))
}

// Install moves a complete artifact file into the artifacts directory under its proper name and
// makes it current. It is opened first, so a file that is not a readable artifact never becomes
// current, and its name is taken from its own header.
func Install(dataDir, file string) (string, error) {
	a, err := compiled.OpenArtifact(file)
	if err != nil {
		return "", err
	}
	name := Name(a.Header)
	a.Close()
	if err := os.MkdirAll(Dir(dataDir), 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(Dir(dataDir), name)
	if err := os.Rename(file, dst); err != nil {
		return "", err
	}
	if err := SetCurrent(dataDir, name); err != nil {
		return "", err
	}
	return dst, nil
}

// migrate moves a data directory from the single-file layout to this one, or reports ErrNone.
func migrate(dataDir string) (string, error) {
	legacy := filepath.Join(dataDir, legacyArtifact)
	if _, err := os.Stat(legacy); errors.Is(err, fs.ErrNotExist) {
		return "", ErrNone
	}
	a, err := compiled.OpenArtifact(legacy)
	if err != nil {
		return "", fmt.Errorf("migrating %s: %w", legacy, err)
	}
	stamp := a.Header.Stamp
	a.Close()
	// The index is derived and the server rebuilds it when it is missing, so it is moved along
	// only as a convenience; a failure to move it is not a failure to migrate.
	if idx := filepath.Join(dataDir, legacyIndex); fileExists(idx) {
		if err := os.MkdirAll(Dir(dataDir), 0o755); err == nil {
			_ = os.Rename(idx, filepath.Join(Dir(dataDir), IndexName(stamp)))
		}
	}
	return Install(dataDir, legacy)
}

// Prune removes what is no longer kept: every artifact but the current one and the newest other,
// the indexes and Xet hashes whose artifact is gone, and .part files old enough to be abandoned.
// It returns what it removed.
func Prune(dataDir string) ([]string, error) {
	current, err := Current(dataDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(Dir(dataDir))
	if err != nil {
		return nil, err
	}
	var artifacts []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "artifact-") && strings.HasSuffix(e.Name(), Ext) {
			artifacts = append(artifacts, e.Name())
		}
	}
	// Names sort by build time, so the newest other than the current one is the previous.
	sort.Sort(sort.Reverse(sort.StringSlice(artifacts)))
	keep := map[string]bool{filepath.Base(current): true}
	for _, n := range artifacts {
		if !keep[n] {
			keep[n] = true
			break
		}
	}
	keptStamps := map[string]bool{}
	for n := range keep {
		keptStamps[stampOf(n)] = true
	}
	var removed []string
	remove := func(name string) {
		if err := os.Remove(filepath.Join(Dir(dataDir), name)); err == nil {
			removed = append(removed, name)
		}
	}
	for _, e := range entries {
		n := e.Name()
		switch {
		case e.IsDir():
		case strings.HasSuffix(n, PartExt):
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > abandonedAfter {
				remove(n)
			}
		case strings.HasPrefix(n, "artifact-") && strings.HasSuffix(n, Ext):
			if !keep[n] {
				remove(n)
			}
		case strings.HasPrefix(n, "artifact-") && strings.HasSuffix(n, Ext+XetExt):
			if !keep[strings.TrimSuffix(n, XetExt)] {
				remove(n)
			}
		case strings.HasPrefix(n, "search-") && strings.HasSuffix(n, ".idx"):
			if !keptStamps[strings.TrimSuffix(strings.TrimPrefix(n, "search-"), ".idx")] {
				remove(n)
			}
		}
	}
	return removed, nil
}

// stampOf is the short stamp an artifact file name carries.
func stampOf(name string) string {
	base := strings.TrimSuffix(name, Ext)
	if i := strings.LastIndex(base, "-"); i >= 0 {
		return base[i+1:]
	}
	return ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// IndexFor is where the index cut from the artifact at path is kept, read from the artifact's own
// header.
func IndexFor(artifactPath string) (string, error) {
	a, err := compiled.OpenArtifact(artifactPath)
	if err != nil {
		return "", err
	}
	defer a.Close()
	return IndexPath(artifactPath, a.Header.Stamp), nil
}
