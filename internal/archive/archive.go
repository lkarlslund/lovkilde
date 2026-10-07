// Package archive stores document bytes as ordinary files on disk.
//
// This is deliberately not a database. The XML files are the product of the
// mirror: they must stay greppable, diffable, rsync-able and readable by
// anything, decades from now, without this program. The state store (internal/store)
// holds only derived state, so losing data/state costs an offline rescan and zero
// HTTP requests.
//
// Most documents are one file. Half of the law corpus is not: the canonical XML
// for older documents carries metadata but no <DokumentIndhold>, and the site
// serves their text only as HTML inside its JSON document payload. Those get a
// second file with the same stem and a .json extension next to the XML, written
// through PutExt, so a stub and its body are found together. The XML keeps its
// byte-exact relationship with the ELI /xml endpoint either way: PutExt refuses
// to write .xml.
package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// XMLExt is the canonical representation's extension. It is not spelled out at
// call sites so the sidecar rules below have one name to reason about.
const XMLExt = ".xml"

// ErrCanonicalExt is returned when a sidecar write names the XML extension.
// The XML files are the mirror of the site's canonical endpoint; only Put may
// write them, so a representation-aware call that could reach them is a bug at
// the call site and must not quietly overwrite a canonical file.
var ErrCanonicalExt = errors.New("refusing to write .xml through a representation path; use Put")

// Archive is a directory of XML documents, sharded by ELI kind.
type Archive struct {
	dir string
}

// Open returns an archive rooted at dir, creating it if necessary.
func Open(dir string) (*Archive, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create archive dir: %w", err)
	}
	return &Archive{dir: dir}, nil
}

// Dir is the archive root.
func (a *Archive) Dir() string { return a.dir }

// Path maps an ELI resource URL onto a shard path, e.g.
//
//	https://www.retsinformation.dk/eli/retsinfo/2026/10019 -> retsinfo/2026/10019.xml
//	https://www.retsinformation.dk/eli/lta/2026/789        -> lta/2026/789.xml
//	https://www.retsinformation.dk/eli/ft/202523L00005     -> ft/2025/202523L00005.xml
//	https://www.retsinformation.dk/eli/accn/A20240039929   -> accn/A/2024/A20240039929.xml
//
// Sharding keeps any single directory from holding 200k entries while the path
// still mirrors the ELI identifier, so a file can be found by eye.
func (a *Archive) Path(eliURL string) string { return a.PathExt(eliURL, XMLExt) }

// PathExt is Path for a representation other than the canonical XML, e.g.
// PathExt(u, ".json") -> lta/1995/498.json beside lta/1995/498.xml. Only the
// extension differs, which is the point: one document, one directory, and the
// representation is the extension rather than a second tree to keep in sync.
func (a *Archive) PathExt(eliURL, ext string) string {
	rel := relativeELIPath(eliURL, safeExt(ext))
	if rel == "" {
		// Unparseable URL: fall back to a content-hash name rather than drop data.
		sum := sha256.Sum256([]byte(eliURL))
		rel = "unmapped/" + hex.EncodeToString(sum[:8]) + safeExt(ext)
	}
	return filepath.Join(a.dir, rel)
}

// safeExt normalises an extension so it cannot smuggle path segments: an
// extension is a dot plus filename characters, nothing else.
func safeExt(ext string) string {
	if ext == "" {
		return XMLExt
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	var b strings.Builder
	b.WriteByte('.')
	for _, r := range strings.TrimPrefix(ext, ".") {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		}
	}
	if b.String() == "." {
		return XMLExt
	}
	return b.String()
}

// relativeELIPath turns an ELI URL into a sanitized relative path.
//
// The shape mirrors the ELI identifier with the kind appearing exactly once and
// a year shard so no directory accumulates hundreds of thousands of entries:
//
//	eli/retsinfo/2026/10019    -> retsinfo/2026/10019.xml
//	eli/lta/2026/789           -> lta/2026/789.xml
//	eli/ft/202523L00005        -> ft/2025/202523L00005.xml
//	eli/accn/A20240039929      -> accn/A/2024/A20240039929.xml
func relativeELIPath(eliURL, ext string) string {
	i := strings.Index(eliURL, "/eli/")
	if i < 0 {
		return ""
	}
	p := eliURL[i+len("/eli/"):]
	if j := strings.IndexByte(p, '?'); j >= 0 {
		p = p[:j]
	}
	p = strings.TrimSuffix(p, "/")
	segs := strings.Split(p, "/")
	clean := make([]string, 0, len(segs))
	for _, s := range segs {
		if s = sanitizeSegment(s); s != "" {
			clean = append(clean, s)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	kind := clean[0]
	rest := clean[1:]
	if len(rest) == 0 {
		return filepath.Join(kind, kind) + ext
	}
	leaf := rest[len(rest)-1]
	switch {
	case kind == "accn":
		// Accessions are rank letter + year + serial, e.g. A20240039929. Shard on
		// rank and year so the files a person looks for sit together.
		if len(leaf) >= 5 {
			return filepath.Join(kind, leaf[:1], leaf[1:5], leaf) + ext
		}
	default:
		// Most kinds carry a year, or a semester code that starts with one
		// (202523L00005 -> 2025). Shard on it and keep the full leaf name so the
		// file is identifiable without opening it.
		if len(rest[0]) >= 4 && allDigits(rest[0][:4]) {
			return filepath.Join(kind, rest[0][:4], leaf) + ext
		}
	}
	return filepath.Join(append([]string{kind}, rest...)...) + ext
}

// allDigits guards against sharding on a non-year segment.
func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// sanitizeSegment rejects anything that could escape the archive directory.
func sanitizeSegment(s string) string {
	if s == "" || s == "." || s == ".." {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			// Percent-encode the unexpected instead of trusting it.
			b.WriteString("%")
			b.WriteString(hex.EncodeToString([]byte{byte(r)}))
		}
	}
	return b.String()
}

// Has reports whether the document file already exists. Used to avoid
// re-requesting content we already hold.
func (a *Archive) Has(eliURL string) bool { return a.HasExt(eliURL, XMLExt) }

// HasExt reports whether a given representation of the document is on disk with
// content. Existence of the file, not the state store, is what makes a crawl
// resumable, so the sidecar question has to be answerable the same way.
func (a *Archive) HasExt(eliURL, ext string) bool {
	st, err := os.Stat(a.PathExt(eliURL, ext))
	return err == nil && st.Size() > 0
}

// Put writes the document body atomically (temp file plus rename), so an
// interrupted run can never leave a half-written XML file behind. It returns
// the content hash, byte count and whether the file is new or changed.
func (a *Archive) Put(eliURL string, body []byte) (hash string, bytes int64, changed bool, err error) {
	return a.put(a.Path(eliURL), body)
}

// PutExt writes a non-canonical representation, e.g. the site JSON payload as
// .json beside the .xml. It refuses XMLExt: canonical files are written by Put
// only, so a bug in representation handling cannot overwrite mirror bytes.
func (a *Archive) PutExt(eliURL, ext string, body []byte) (hash string, bytes int64, changed bool, err error) {
	// Case-insensitive on purpose: ".XML" is a different filename on Linux but the
	// same file on the case-insensitive filesystems an archive copy lands on, and a
	// payload written under it would hide from every consumer globbing *.xml.
	if strings.EqualFold(safeExt(ext), XMLExt) {
		return "", 0, false, ErrCanonicalExt
	}
	return a.put(a.PathExt(eliURL, ext), body)
}

// put is the shared atomic write: temp file in the destination directory (same
// filesystem, so the rename cannot fail across devices), synced before the
// rename so a crash leaves either the old file or the new one.
func (a *Archive) put(dest string, body []byte) (hash string, bytes int64, changed bool, err error) {
	sum := sha256.Sum256(body)
	hash = hex.EncodeToString(sum[:])

	// Skip the write entirely when we already hold these exact bytes.
	if old, err := os.ReadFile(dest); err == nil {
		if oldSum := sha256.Sum256(old); hex.EncodeToString(oldSum[:]) == hash {
			return hash, int64(len(body)), false, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return hash, int64(len(body)), false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".tmp-*")
	if err != nil {
		return hash, int64(len(body)), false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // no-op once the rename succeeds
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return hash, int64(len(body)), false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return hash, int64(len(body)), false, err
	}
	if err := tmp.Close(); err != nil {
		return hash, int64(len(body)), false, err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return hash, int64(len(body)), false, fmt.Errorf("install %s: %w", dest, err)
	}
	return hash, int64(len(body)), true, nil
}

// Get reads a stored document body.
func (a *Archive) Get(eliURL string) ([]byte, error) { return a.GetExt(eliURL, XMLExt) }

// GetExt reads one representation of a document.
func (a *Archive) GetExt(eliURL, ext string) ([]byte, error) {
	return os.ReadFile(a.PathExt(eliURL, ext))
}

// Count walks the archive and reports files plus total bytes, for the rebuild
// and status commands. It counts every representation; CountExt is the one that
// answers "how many XML files" once sidecars exist.
func (a *Archive) Count() (files int64, totalBytes int64, err error) {
	return a.count("")
}

// CountExt counts the files of one representation, e.g. ".json" sidecars. A run
// that adds bodies must not silently change what "archive files" means in
// status output, so the two representations are countable separately.
func (a *Archive) CountExt(ext string) (files int64, totalBytes int64, err error) {
	return a.count(safeExt(ext))
}

func (a *Archive) count(ext string) (files int64, totalBytes int64, err error) {
	err = filepath.WalkDir(a.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".tmp-") {
			return nil // abandoned temp file from an interrupted run
		}
		if ext != "" && !strings.HasSuffix(d.Name(), ext) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files++
		totalBytes += info.Size()
		return nil
	})
	return files, totalBytes, err
}

// SizeHint formats byte counts for logs.
func SizeHint(b int64) string {
	const unit = 1024
	if b < unit {
		return strconv.FormatInt(b, 10) + " B"
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	f := float64(b)
	for _, u := range units {
		f /= unit
		if f < unit {
			return fmt.Sprintf("%.1f %s", f, u)
		}
	}
	return fmt.Sprintf("%.1f PiB", f/unit)
}
