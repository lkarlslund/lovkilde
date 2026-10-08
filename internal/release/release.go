// Package release is where published artifacts and collection state live, and moving them in
// and out: a server downloads the artifact when it has none and checks for a newer one on a
// schedule, and the collector publishes what it built and restores the state it continues from.
//
// A release repository is anything that can open a published file and commit a set of files
// at once (Repo). Two exist: a local directory, which works completely and is how publishing is
// tried out and how a snapshot is kept to start from again; and a Hugging Face dataset
// repository, which uploads through the hf command. Both are named by one string (Open): a
// directory path, or hf://owner/name[@revision].
//
// A release is a manifest beside the files it names:
//
//	latest.json                                   Manifest
//	artifact-20261007T0648Z-61f208f4.retsin       the artifact it names
//	artifact-20261001T0310Z-c41e9b07.retsin       its base, when the artifact is a layer
//	state/meta.json, state/inventory.jsonl, …     the collection state (internal/store)
//	xml/lta/1995.tar.zst, …                       the XML archive, one pack per shard (optional)
//	README.md                                     the dataset card, written from the manifest
//
// The XML archive is published only when asked for: the state and the artifact are all a
// collection pass needs to continue, and the archive is what a machine needs to build an
// artifact in full (or to build with another document model) without crawling for days.
// The manifest carries the artifact's document model, so a binary never downloads an artifact
// it would refuse to serve, and the size and SHA-256 of every file, so a truncated or altered
// download never becomes current. It is committed last, so a reader never sees one naming files
// that are not there yet.
package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"time"
)

const (
	// DefaultSource is where a server downloads artifacts from: the dataset the daily release
	// (.github/workflows/hf-release.yml) publishes to.
	DefaultSource = "hf://lkarlslund/retsinformation"
	// ManifestFile names the release manifest at the repository root.
	ManifestFile = "latest.json"
	// StateDir is where the collection state is published, inside the repository.
	StateDir = "state"
)

// ErrNoRepo is returned when no release repository is named.
var ErrNoRepo = errors.New("no release repository is configured (name a directory or hf://owner/name with --release)")

// Repo is a release repository.
type Repo interface {
	// Open reads a published file. A file that is not there is an error wrapping
	// fs.ErrNotExist.
	Open(ctx context.Context, name string) (io.ReadCloser, error)
	// Commit publishes files together: each Add is copied from a local file to its path, in
	// order, and Delete removes paths afterwards.
	Commit(ctx context.Context, c Commit) error
	// String names the repository for messages.
	String() string
}

// Commit is a set of changes published together.
type Commit struct {
	Message string
	Add     []Upload
	Delete  []string
}

// Upload is one file to publish: From on local disk, at Path in the repository.
type Upload struct {
	Path string
	From string
}

// Manifest is a release: the artifact and the state, and what each file must be.
type Manifest struct {
	File   string `json:"file"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Model  int    `json:"model"`
	// Documents is how many documents the artifact holds, for the dataset card.
	Documents int `json:"documents,omitempty"`
	// Base is the whole artifact File is a layer over (compiled.ArtifactHeader.Base); nil when
	// File is whole.
	Base      *FileRef  `json:"base,omitempty"`
	State     []FileRef `json:"state,omitempty"`
	XML       []Pack    `json:"xml,omitempty"`
	Published time.Time `json:"published"`
}

// FileRef is a published file and what it must be.
type FileRef struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Open names a release repository: hf://owner/name[@revision] for Hugging Face, otherwise a
// local directory (a file:// prefix is allowed).
func Open(spec string) (Repo, error) {
	switch {
	case spec == "":
		return nil, ErrNoRepo
	case strings.HasPrefix(spec, "hf://"):
		name, rev, _ := strings.Cut(strings.TrimPrefix(spec, "hf://"), "@")
		if strings.Count(name, "/") != 1 || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
			return nil, fmt.Errorf("%q: a Hugging Face repository is hf://owner/name[@revision]", spec)
		}
		return &HuggingFace{Repo: name, Revision: rev}, nil
	default:
		return &Local{Dir: strings.TrimPrefix(spec, "file://")}, nil
	}
}

// Latest reads a repository's manifest.
func Latest(ctx context.Context, r Repo) (Manifest, error) {
	var m Manifest
	rc, err := r.Open(ctx, ManifestFile)
	if err != nil {
		return m, err
	}
	defer rc.Close()
	if err := json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(&m); err != nil {
		return m, fmt.Errorf("%s in %s: %w", ManifestFile, r, err)
	}
	if m.File != "" && !validName(m.File) {
		return m, fmt.Errorf("%s in %s names %q, which is not a file in it", ManifestFile, r, m.File)
	}
	if m.Base != nil && (!validName(m.Base.Path) || strings.Contains(m.Base.Path, "/")) {
		return m, fmt.Errorf("%s in %s names %q as the base, which is not an artifact file in it", ManifestFile, r, m.Base.Path)
	}
	refs := slices.Clone(m.State)
	for _, p := range m.XML {
		refs = append(refs, p.FileRef)
	}
	for _, f := range refs {
		if !validName(f.Path) {
			return m, fmt.Errorf("%s in %s names %q, which is not a file in it", ManifestFile, r, f.Path)
		}
	}
	return m, nil
}

// validName is a relative slash-separated path inside the repository.
func validName(name string) bool { return fs.ValidPath(name) && name != "." }
