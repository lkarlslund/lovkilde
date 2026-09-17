// Package archive stores document bytes as ordinary files on disk.
//
// This is deliberately not a database. The XML files are the product of the
// mirror: they must stay greppable, diffable, rsync-able and readable by
// anything, decades from now, without this program. Badger (internal/store)
// holds only derived state, so losing meta/kv costs an offline rescan and zero
// HTTP requests.
package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

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
func (a *Archive) Path(eliURL string) string {
	rel := relativeELIPath(eliURL)
	if rel == "" {
		// Unparseable URL: fall back to a content-hash name rather than drop data.
		sum := sha256.Sum256([]byte(eliURL))
		rel = "unmapped/" + hex.EncodeToString(sum[:8]) + ".xml"
	}
	return filepath.Join(a.dir, rel)
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
func relativeELIPath(eliURL string) string {
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
		return filepath.Join(kind, kind) + ".xml"
	}
	leaf := rest[len(rest)-1]
	switch {
	case kind == "accn":
		// Accessions are rank letter + year + serial, e.g. A20240039929. Shard on
		// rank and year so the files a person looks for sit together.
		if len(leaf) >= 5 {
			return filepath.Join(kind, leaf[:1], leaf[1:5], leaf) + ".xml"
		}
	default:
		// Most kinds carry a year, or a semester code that starts with one
		// (202523L00005 -> 2025). Shard on it and keep the full leaf name so the
		// file is identifiable without opening it.
		if len(rest[0]) >= 4 && allDigits(rest[0][:4]) {
			return filepath.Join(kind, rest[0][:4], leaf) + ".xml"
		}
	}
	return filepath.Join(append([]string{kind}, rest...)...) + ".xml"
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
func (a *Archive) Has(eliURL string) bool {
	st, err := os.Stat(a.Path(eliURL))
	return err == nil && st.Size() > 0
}

// Put writes the document body atomically (temp file plus rename), so an
// interrupted run can never leave a half-written XML file behind. It returns
// the content hash, byte count and whether the file is new or changed.
func (a *Archive) Put(eliURL string, body []byte) (hash string, bytes int64, changed bool, err error) {
	sum := sha256.Sum256(body)
	hash = hex.EncodeToString(sum[:])
	dest := a.Path(eliURL)

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
func (a *Archive) Get(eliURL string) ([]byte, error) { return os.ReadFile(a.Path(eliURL)) }

// Count walks the archive and reports files plus total bytes, for the rebuild
// and status commands.
func (a *Archive) Count() (files int64, totalBytes int64, err error) {
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
