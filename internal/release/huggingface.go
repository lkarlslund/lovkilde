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
)

// HuggingFace is a release repository in a Hugging Face dataset repository.
//
// Downloads go through the resolve endpoint, with HF_TOKEN when it is set (a private repository
// needs it). Commits go through the Hub's own client, the hf command (pip install
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

// Commit publishes the files in one commit of the repository, deleting c.Delete in the same
// commit, so a reader sees the release before it or after it and never half of it. The files are
// staged in a temporary directory under their paths in the repository — hard-linked where they
// can be — and handed to hf upload.
func (h *HuggingFace) Commit(ctx context.Context, c Commit) error {
	stage, err := os.MkdirTemp("", "retsin-commit-*")
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
