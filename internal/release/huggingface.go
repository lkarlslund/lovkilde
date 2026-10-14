package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lkarlslund/lovkilde/internal/artifactdir"
	"github.com/lkarlslund/lovkilde/internal/xet"
)

// HuggingFace is a release repository in a Hugging Face dataset repository.
//
// Downloads go through the resolve endpoint, with HF_TOKEN when it is set (a private repository
// needs it); an artifact goes through Xet storage, which reads the chunks it shares with the
// artifacts already on disk from them. Commits go through the Hub's own client, the hf command (pip install
// huggingface_hub): it speaks the upload protocol — large files go to Xet storage in
// content-defined chunks, so a file that is mostly unchanged costs only its changed chunks —
// and it reads HF_TOKEN itself.
type HuggingFace struct {
	Repo     string // "owner/name"
	Revision string // branch or tag; "main" when empty
	BaseURL  string // https://huggingface.co when empty; tests point it at a local server
	Client   *http.Client
	// CLI is the hf command; "hf" on PATH when empty.
	CLI string
}

func (h *HuggingFace) String() string {
	if h.Revision != "" {
		return "hf://" + h.Repo + "@" + h.Revision
	}
	return "hf://" + h.Repo
}

func (h *HuggingFace) url(name string) string {
	base, rev := h.BaseURL, h.Revision
	if base == "" {
		base = "https://huggingface.co"
	}
	if rev == "" {
		rev = "main"
	}
	return strings.TrimRight(base, "/") + "/datasets/" + h.Repo + "/resolve/" + url.PathEscape(rev) + "/" + name
}

// Open downloads a published file through the repository's resolve endpoint.
func (h *HuggingFace) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	if !validName(name) {
		return nil, fmt.Errorf("%q is not a file in %s", name, h)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url(name), nil)
	if err != nil {
		return nil, err
	}
	if token := os.Getenv("HF_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp.Body, nil
	case http.StatusNotFound:
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %w", h.url(name), fs.ErrNotExist)
	default:
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", h.url(name), resp.Status)
	}
}

func (h *HuggingFace) xetHub() *xet.Hub {
	return &xet.Hub{Repo: h.Repo, Revision: h.Revision, BaseURL: h.BaseURL, Token: os.Getenv("HF_TOKEN"), Client: h.Client}
}

// downloadXet writes the published file name to dst through Xet storage, reading what it shares
// with the artifacts beside dst from them, and returns its Xet hash.
func (h *HuggingFace) downloadXet(ctx context.Context, name, dst string) (string, xet.Stats, error) {
	hub := h.xetHub()
	hash, err := hub.FileHash(ctx, name)
	if err != nil {
		return "", xet.Stats{}, err
	}
	st, err := hub.Download(ctx, hash, dst, artifactsInXet(ctx, hub, filepath.Dir(dst)))
	return hash, st, err
}

// artifactsInXet is the artifacts in dir with their Xet hashes: recorded beside each when it was
// downloaded, or, for one downloaded or built before, asked of the repository while it still
// publishes it — and then recorded.
func artifactsInXet(ctx context.Context, hub *xet.Hub, dir string) []xet.Have {
	entries, _ := os.ReadDir(dir)
	var have []xet.Have
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "artifact-") || !strings.HasSuffix(name, artifactdir.Ext) {
			continue
		}
		path := filepath.Join(dir, name)
		recorded, err := os.ReadFile(path + artifactdir.XetExt)
		hash := strings.TrimSpace(string(recorded))
		if err != nil {
			if hash, err = hub.FileHash(ctx, name); err != nil {
				continue
			}
			_ = os.WriteFile(path+artifactdir.XetExt, []byte(hash+"\n"), 0o644)
		}
		have = append(have, xet.Have{Path: path, Hash: hash})
	}
	return have
}

// Commit publishes the files in one commit of the repository, deleting c.Delete in the same
// commit, so a reader sees the release before it or after it and never half of it. The files are
// staged in a temporary directory under their paths in the repository — hard-linked where they
// can be — and handed to hf upload.
func (h *HuggingFace) Commit(ctx context.Context, c Commit) error {
	stage, err := os.MkdirTemp("", "lovkilde-commit-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, u := range c.Add {
		if !validName(u.Path) {
			return fmt.Errorf("%q is not a file in %s", u.Path, h)
		}
		dst := filepath.Join(stage, filepath.FromSlash(u.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.Link(u.From, dst); err != nil {
			if err := copyFile(u.From, dst); err != nil {
				return err
			}
		}
	}
	cli := h.CLI
	if cli == "" {
		cli = "hf"
	}
	args := []string{"upload", h.Repo, stage, ".", "--repo-type", "dataset", "--commit-message", c.Message}
	if h.Revision != "" {
		args = append(args, "--revision", h.Revision)
	}
	for _, name := range c.Delete {
		if !validName(name) || strings.ContainsAny(name, "*?[") {
			return fmt.Errorf("%q is not a file in %s", name, h)
		}
		args = append(args, "--delete", name)
	}
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("publishing to %s needs the hf command (pip install huggingface_hub) and HF_TOKEN: %w", h, err)
		}
		return fmt.Errorf("hf upload to %s: %w", h, err)
	}
	return nil
}
