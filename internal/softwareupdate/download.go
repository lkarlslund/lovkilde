package softwareupdate

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Stamp is a build's identity: the commit it was built from and that commit's time, which orders
// two builds.
type Stamp struct {
	Revision string
	Time     time.Time
}

func (s Stamp) short() string { return short(s.Revision) }

// readStamp reads the stamp the Go toolchain left in the binary at path. It is a variable so a
// test can download something that is not a Go binary.
var readStamp = func(path string) (Stamp, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return Stamp{}, err
	}
	var s Stamp
	for _, kv := range info.Settings {
		switch kv.Key {
		case "vcs.revision":
			s.Revision = kv.Value
		case "vcs.time":
			s.Time, _ = time.Parse(time.RFC3339, kv.Value)
		}
	}
	if s.Revision == "" || s.Time.IsZero() {
		return Stamp{}, errors.New("the build does not say which commit it was built from")
	}
	return s, nil
}

// exeName is the program's file name on this platform.
func exeName() string {
	if runtime.GOOS == "windows" {
		return "lovkilde.exe"
	}
	return "lovkilde"
}

// assetName is the published archive for this platform, as scripts/build-server.sh names it.
func assetName() string {
	name := "lovkilde-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		return name + ".zip"
	}
	return name + ".tar.gz"
}

// downloadBase is where the published build's files are, unless DownloadBase says otherwise.
func (c *Checker) downloadBase() string {
	if c.DownloadBase != "" {
		return strings.TrimRight(c.DownloadBase, "/")
	}
	return "https://github.com/" + c.Repo + "/releases/download/" + url.PathEscape(c.Tag)
}

func (c *Checker) get(ctx context.Context, name string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.downloadBase()+"/"+name, nil)
	if err != nil {
		return nil, err
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: GitHub answered %s", name, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// Download fetches the published build for this platform, checks it against the release's
// SHA256SUMS, and keeps it in dir named for its stamp, where Newest finds it. A build that is
// already there is not kept twice. It returns the kept program and its stamp.
func (c *Checker) Download(ctx context.Context, dir string) (string, Stamp, error) {
	asset := assetName()
	sums, err := c.get(ctx, "SHA256SUMS", 1<<20)
	if err != nil {
		return "", Stamp{}, err
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			want = f[0]
		}
	}
	if want == "" {
		return "", Stamp{}, fmt.Errorf("the published build has no %s for this platform", asset)
	}
	archive, err := c.get(ctx, asset, 512<<20)
	if err != nil {
		return "", Stamp{}, err
	}
	if sum := sha256.Sum256(archive); hex.EncodeToString(sum[:]) != want {
		return "", Stamp{}, fmt.Errorf("%s does not match its SHA-256 in SHA256SUMS", asset)
	}
	exe, err := extract(asset, archive)
	if err != nil {
		return "", Stamp{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", Stamp{}, err
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", Stamp{}, err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(exe)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755)
	}
	if err != nil {
		return "", Stamp{}, err
	}
	stamp, err := readStamp(tmp.Name())
	if err != nil {
		return "", Stamp{}, fmt.Errorf("%s: %w", asset, err)
	}
	dest := filepath.Join(dir, programName(stamp))
	if _, err := os.Stat(dest); err == nil {
		return dest, stamp, nil
	}
	return dest, stamp, os.Rename(tmp.Name(), dest)
}

// extract takes the program out of a published archive.
func extract(asset string, archive []byte) ([]byte, error) {
	want := exeName()
	if strings.HasSuffix(asset, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == want {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(archive))
		if err != nil {
			return nil, err
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err != nil {
				if err == io.EOF {
					break
				}
				return nil, err
			}
			if h.Typeflag == tar.TypeReg && path.Base(h.Name) == want {
				return io.ReadAll(tr)
			}
		}
	}
	return nil, fmt.Errorf("%s holds no %s", asset, want)
}

// stampLayout is how a kept program's name carries its commit time; it sorts as it orders.
const stampLayout = "20060102T150405Z"

// programName is a kept program's file name: lovkilde-<commit time>-<revision>.
func programName(s Stamp) string {
	name := "lovkilde-" + s.Time.UTC().Format(stampLayout) + "-" + s.short()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// Newest is the newest program Download has kept in dir, if any.
func Newest(dir string) (string, Stamp, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", Stamp{}, false
	}
	var best string
	var bestStamp Stamp
	for _, e := range entries {
		rest, ok := strings.CutPrefix(strings.TrimSuffix(e.Name(), ".exe"), "lovkilde-")
		if !ok || e.IsDir() {
			continue
		}
		at, rev, ok := strings.Cut(rest, "-")
		t, err := time.Parse(stampLayout, at)
		if !ok || err != nil || rev == "" {
			continue
		}
		if best == "" || t.After(bestStamp.Time) {
			best, bestStamp = filepath.Join(dir, e.Name()), Stamp{Revision: rev, Time: t}
		}
	}
	return best, bestStamp, best != ""
}

// Prune removes the kept programs other than keep. One that is running (on Windows) stays until
// a later prune.
func Prune(dir, keep string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if strings.HasPrefix(e.Name(), "lovkilde-") && p != keep {
			_ = os.Remove(p)
		}
	}
}
