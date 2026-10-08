package release

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Local is a release repository in a directory: the same layout a Hugging Face repository has,
// for trying publishing out and for keeping a snapshot to start from again.
type Local struct {
	Dir string
}

func (l *Local) String() string { return l.Dir }

func (l *Local) path(name string) (string, error) {
	if !validName(name) {
		return "", fmt.Errorf("%q is not a file in %s", name, l.Dir)
	}
	return filepath.Join(l.Dir, filepath.FromSlash(name)), nil
}

// Open reads a published file.
func (l *Local) Open(_ context.Context, name string) (io.ReadCloser, error) {
	p, err := l.path(name)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// Commit stages every file beside its destination, then renames them into place in order and
// removes the deleted paths. The manifest is added last, so a reader that reads it first never
// finds it naming a file that is not there yet. A file is hard-linked rather than copied when it
// can be: a published artifact is never rewritten in place, so the link costs no space and
// cannot change under the repository.
func (l *Local) Commit(_ context.Context, c Commit) error {
	type staged struct{ tmp, dst string }
	var done []staged
	defer func() {
		for _, s := range done {
			os.Remove(s.tmp) // a no-op once renamed
		}
	}()
	for _, u := range c.Add {
		dst, err := l.path(u.Path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		tmp := dst + ".committing"
		os.Remove(tmp)
		if err := os.Link(u.From, tmp); err != nil {
			if err := copyFile(u.From, tmp); err != nil {
				return err
			}
		}
		done = append(done, staged{tmp, dst})
	}
	for _, s := range done {
		if err := os.Rename(s.tmp, s.dst); err != nil {
			return err
		}
	}
	for _, name := range c.Delete {
		p, err := l.path(name)
		if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
