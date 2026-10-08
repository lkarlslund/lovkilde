package release

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/lkarlslund/retsinformation/internal/parallel"
)

// XMLDir is where the XML archive is published, inside the repository: one pack per shard of
// the archive, xml/<kind>/<year>.tar.zst, so a week's changes replace a few packs, not all.
const XMLDir = "xml"

// Pack is one published shard of the XML archive.
type Pack struct {
	FileRef
	// Shard is the archive directory the pack holds, e.g. lta/1995.
	Shard string `json:"shard"`
	// Digest names the shard's content: its files' paths and SHA-256s, so an
	// unchanged shard is recognised without packing it, and a restored one without unpacking.
	Digest string `json:"digest"`
	Files  int    `json:"files"`
}

// shard is one archive directory as it is on disk.
type shard struct {
	name   string   // lta/1995
	files  []string // slash-separated paths relative to the archive root, sorted
	digest string
}

// shardOf is the shard a file of the archive belongs to: its kind and year directories.
func shardOf(rel string) string {
	parts := strings.Split(rel, "/")
	if len(parts) <= 2 {
		return parts[0]
	}
	return parts[0] + "/" + parts[1]
}

// readShards lists the archive's files by shard and computes each shard's digest; only, when
// set, limits it to one shard. A directory that does not exist is an empty archive.
func readShards(ctx context.Context, root, only string) ([]shard, error) {
	var files []string
	top := filepath.Join(root, filepath.FromSlash(only))
	err := filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == top && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipDir
			}
			return err
		}
		if d.Type().IsRegular() && !strings.HasSuffix(p, ".tmp") && !strings.HasSuffix(p, ".restoring") {
			rel, _ := filepath.Rel(root, p)
			if rel = filepath.ToSlash(rel); only == "" || shardOf(rel) == only {
				files = append(files, rel)
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	hashes := make([]string, len(files))
	errs := make([]error, len(files))
	parallel.For(ctx, len(files), func(i int) {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(files[i])))
		sum := sha256.Sum256(raw)
		hashes[i], errs[i] = hex.EncodeToString(sum[:]), err
	})
	if err := errors.Join(append(errs, ctx.Err())...); err != nil {
		return nil, err
	}
	byName := map[string]*shard{}
	var names []string
	sums := map[string][]string{}
	for i, f := range files {
		name := shardOf(f)
		s := byName[name]
		if s == nil {
			s = &shard{name: name}
			byName[name] = s
			names = append(names, name)
		}
		s.files = append(s.files, f)
		sums[name] = append(sums[name], f+"\t"+hashes[i]+"\n")
	}
	slices.Sort(names)
	out := make([]shard, 0, len(names))
	for _, name := range names {
		s := byName[name]
		h := sha256.New()
		lines := sums[name]
		slices.Sort(lines)
		for _, l := range lines {
			io.WriteString(h, l)
		}
		slices.Sort(s.files)
		s.digest = hex.EncodeToString(h.Sum(nil))
		out = append(out, *s)
	}
	return out, nil
}

// packPath is where a shard's pack is published.
func packPath(shard string) string { return path.Join(XMLDir, shard+".tar.zst") }

// packXML packs every shard that differs from the published packs into tmp, and returns the
// packs of the new release and what to upload.
//
// The archive on disk may hold only part of each shard: a runner that restored the state and not
// the archive holds the files this week's pass fetched. So a published shard is never dropped
// for being absent here, and a shard that differs is first completed from its published pack,
// keeping every file on disk (it was fetched since), before it is packed again. On a machine
// holding the whole archive that completes nothing, and the packs are its shards exactly.
func packXML(ctx context.Context, r Repo, root, tmp string, published []Pack) ([]Pack, []Upload, error) {
	shards, err := readShards(ctx, root, "")
	if err != nil {
		return nil, nil, err
	}
	before := map[string]Pack{}
	for _, p := range published {
		before[p.Shard] = p
	}
	var packs []Pack
	var adds []Upload
	for _, s := range shards {
		p, ok := before[s.name]
		delete(before, s.name)
		if ok && p.Digest != s.digest {
			if err := fetchPack(ctx, r, p, root); err != nil {
				return nil, nil, err
			}
			again, err := readShards(ctx, root, s.name)
			if err != nil {
				return nil, nil, err
			}
			s = again[0]
		}
		if ok && p.Digest == s.digest {
			packs = append(packs, p)
			continue
		}
		file := filepath.Join(tmp, strings.ReplaceAll(s.name, "/", "_")+".tar.zst")
		if err := writePack(root, s.files, file); err != nil {
			return nil, nil, fmt.Errorf("packing %s: %w", s.name, err)
		}
		ref, err := describe(file, packPath(s.name))
		if err != nil {
			return nil, nil, err
		}
		packs = append(packs, Pack{FileRef: ref, Shard: s.name, Digest: s.digest, Files: len(s.files)})
		adds = append(adds, Upload{Path: ref.Path, From: file})
	}
	for _, p := range before {
		packs = append(packs, p)
	}
	slices.SortFunc(packs, func(a, b Pack) int { return strings.Compare(a.Shard, b.Shard) })
	return packs, adds, nil
}

// writePack writes files, relative to root, as a zstd-compressed tar. The entries carry no
// owner and no time, so a pack is a function of its files.
func writePack(root string, files []string, to string) error {
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	zw, err := zstd.NewWriter(out, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		out.Close()
		return err
	}
	tw := tar.NewWriter(zw)
	for _, rel := range files {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			out.Close()
			return err
		}
		hdr := &tar.Header{Name: rel, Mode: 0o644, Size: int64(len(raw)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			out.Close()
			return err
		}
		if _, err := tw.Write(raw); err != nil {
			out.Close()
			return err
		}
	}
	err = tw.Close()
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// RestoreXML unpacks the published XML archive into root, checking every pack against the
// manifest. A file already on disk is kept — it is the one fetched since, or one an interrupted
// restore already wrote — and a shard on disk with the published content is not downloaded at
// all. It reports how many packs it unpacked.
func RestoreXML(ctx context.Context, r Repo, root string) (Manifest, int, error) {
	m, err := Latest(ctx, r)
	if err != nil {
		return m, 0, err
	}
	if len(m.XML) == 0 {
		return m, 0, fmt.Errorf("the release in %s holds no XML archive (publish it with --with-xml)", r)
	}
	have, err := readShards(ctx, root, "")
	if err != nil {
		return m, 0, err
	}
	digest := map[string]string{}
	for _, s := range have {
		digest[s.name] = s.digest
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return m, 0, err
	}
	var unpacked int
	for _, p := range m.XML {
		if digest[p.Shard] == p.Digest {
			continue
		}
		if err := fetchPack(ctx, r, p, root); err != nil {
			return m, unpacked, err
		}
		unpacked++
	}
	return m, unpacked, nil
}

// fetchPack downloads a published pack and unpacks it into root, keeping files already there.
func fetchPack(ctx context.Context, r Repo, p Pack, root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(root, strings.ReplaceAll(p.Shard, "/", "_")+".tar.zst.restoring")
	if err := download(ctx, r, p.FileRef, tmp); err != nil {
		return err
	}
	err := unpack(tmp, root, p.Shard)
	os.Remove(tmp)
	if err != nil {
		return fmt.Errorf("unpacking %s: %w", p.Path, err)
	}
	return nil
}

// unpack writes a pack's files under root, except those already there. Every entry must be a
// plain file inside the shard the manifest says the pack holds; anything else is refused before
// it is written.
func unpack(file, root, shard string) error {
	in, err := os.Open(file)
	if err != nil {
		return err
	}
	defer in.Close()
	zr, err := zstd.NewReader(in)
	if err != nil {
		return err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || !fs.ValidPath(hdr.Name) || shardOf(hdr.Name) != shard {
			return fmt.Errorf("%q is not a file of shard %s", hdr.Name, shard)
		}
		dst := filepath.Join(root, filepath.FromSlash(hdr.Name))
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		out, err := os.Create(dst + ".tmp")
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(dst+".tmp", dst)
		}
		if err != nil {
			os.Remove(dst + ".tmp")
			return err
		}
	}
}
