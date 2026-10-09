package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/lkarlslund/retsinformation/internal/compiled"
)

// Release is what Publish publishes.
type Release struct {
	// Artifact is the artifact file to publish. Empty keeps the one already published.
	Artifact string
	// StateDir holds the state files, named in StateFiles; files that do not exist are left
	// out, which a restore reads as empty tables. The caller holds the state still while
	// publishing (store.Open takes its lock).
	StateDir   string
	StateFiles []string
	// XMLDir, when set, publishes the XML archive too: the shards whose content changed since the
	// last release that published it are packed and uploaded, the others are kept as they are,
	// and so is every shard not on disk (see packXML). Empty keeps the packs already published.
	XMLDir string
}

// Publish commits a release: the state files, the artifact, the dataset card describing them,
// and the manifest naming them last.
// An artifact the repository held before and this release replaces is deleted with it.
func Publish(ctx context.Context, r Repo, rel Release) (Manifest, error) {
	prev, err := Latest(ctx, r)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Manifest{}, err
	}
	m := Manifest{File: prev.File, Size: prev.Size, SHA256: prev.SHA256, Model: prev.Model, Documents: prev.Documents, Base: prev.Base, XML: prev.XML, Published: time.Now().UTC()}
	var c Commit
	if rel.XMLDir != "" {
		tmp, err := os.MkdirTemp("", "retsin-packs-*")
		if err != nil {
			return Manifest{}, err
		}
		defer os.RemoveAll(tmp)
		packs, adds, err := packXML(ctx, r, rel.XMLDir, tmp, prev.XML)
		if err != nil {
			return Manifest{}, err
		}
		m.XML = packs
		c.Add = append(c.Add, adds...)
	}
	for _, name := range rel.StateFiles {
		from := filepath.Join(rel.StateDir, name)
		if !fileExists(from) {
			continue
		}
		ref, err := describe(from, path.Join(StateDir, name))
		if err != nil {
			return Manifest{}, err
		}
		m.State = append(m.State, ref)
		c.Add = append(c.Add, Upload{Path: ref.Path, From: from})
	}
	// The artifact's name carries its build stamp, so the one already published is not hashed
	// again: re-publishing it is the state-only release a collection pass with no new build makes.
	if rel.Artifact != "" && filepath.Base(rel.Artifact) != prev.File {
		a, err := compiled.OpenArtifact(rel.Artifact)
		if err != nil {
			return Manifest{}, err
		}
		m.Model = a.Header.Model
		m.Documents = len(a.Documents())
		base := a.Header.Base
		a.Close()
		ref, err := describe(rel.Artifact, filepath.Base(rel.Artifact))
		if err != nil {
			return Manifest{}, err
		}
		m.File, m.Size, m.SHA256 = ref.Path, ref.Size, ref.SHA256
		c.Add = append(c.Add, Upload{Path: ref.Path, From: rel.Artifact})
		// A layer is published with its base. The base is usually published already — as the
		// last release's base, or as the last release's whole artifact — and is then not sent
		// again: that is what makes a layer release megabytes.
		m.Base = nil
		if base != nil {
			switch {
			case prev.Base != nil && prev.Base.Path == base.File:
				m.Base = prev.Base
			case prev.File == base.File:
				m.Base = &FileRef{Path: prev.File, Size: prev.Size, SHA256: prev.SHA256}
			default:
				from := filepath.Join(filepath.Dir(rel.Artifact), base.File)
				ref, err := describe(from, base.File)
				if err != nil {
					return Manifest{}, err
				}
				m.Base = &ref
				c.Add = append(c.Add, Upload{Path: ref.Path, From: from})
			}
		}
		for _, old := range []string{prev.File, prev.Base.pathOrEmpty()} {
			if old != "" && old != m.File && (m.Base == nil || old != m.Base.Path) {
				c.Delete = append(c.Delete, old)
			}
		}
	}
	// The card describes the release, so it is rewritten with it.
	card, err := writeCard(m, r.String())
	if err != nil {
		return Manifest{}, err
	}
	defer os.Remove(card)
	c.Add = append(c.Add, Upload{Path: CardFile, From: card})
	// The manifest goes last: a reader that reads it first never finds it naming a file that
	// is not there yet.
	raw, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return Manifest{}, err
	}
	tmp, err := os.CreateTemp("", "latest-*.json")
	if err != nil {
		return Manifest{}, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return Manifest{}, err
	}
	if err := tmp.Close(); err != nil {
		return Manifest{}, err
	}
	c.Add = append(c.Add, Upload{Path: ManifestFile, From: tmp.Name()})
	c.Message = fmt.Sprintf("Release %s", m.Published.Format(time.RFC3339))
	if err := r.Commit(ctx, c); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// RestoreState downloads the published state into dir, checking every file against the
// manifest. Each file is downloaded beside its destination and renamed only once all of them
// arrived whole, so a failed restore leaves dir as it was.
func RestoreState(ctx context.Context, r Repo, dir string) (Manifest, error) {
	m, err := Latest(ctx, r)
	if err != nil {
		return m, err
	}
	if len(m.State) == 0 {
		return m, fmt.Errorf("the release in %s holds no state", r)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return m, err
	}
	type got struct{ tmp, dst string }
	var all []got
	defer func() {
		for _, g := range all {
			os.Remove(g.tmp)
		}
	}()
	for _, f := range m.State {
		dst := filepath.Join(dir, path.Base(f.Path))
		tmp := dst + ".restoring"
		if _, err := download(ctx, r, f, tmp); err != nil {
			return m, err
		}
		all = append(all, got{tmp, dst})
	}
	for _, g := range all {
		if err := os.Rename(g.tmp, g.dst); err != nil {
			return m, err
		}
	}
	return m, nil
}

// pathOrEmpty is the path of a reference that may be absent.
func (f *FileRef) pathOrEmpty() string {
	if f == nil {
		return ""
	}
	return f.Path
}

// describe is the reference a manifest keeps for a local file published at name.
func describe(from, name string) (FileRef, error) {
	f, err := os.Open(from)
	if err != nil {
		return FileRef{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return FileRef{}, err
	}
	return FileRef{Path: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}
