package archive

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathMapping(t *testing.T) {
	a := &Archive{dir: "/data/xml"}
	cases := []struct {
		url  string
		want string
	}{
		{"https://www.retsinformation.dk/eli/retsinfo/2026/10019", "retsinfo/2026/10019.xml"},
		{"https://www.retsinformation.dk/eli/lta/2026/789", "lta/2026/789.xml"},
		{"https://www.retsinformation.dk/eli/ft/202523L00005", "ft/2025/202523L00005.xml"},
		{"https://www.retsinformation.dk/eli/accn/A20240039929", "accn/A/2024/A20240039929.xml"},
		{"https://www.retsinformation.dk/eli/fob/2024/1234", "fob/2024/1234.xml"},
	}
	for _, c := range cases {
		got, err := filepath.Rel(a.dir, a.Path(c.url))
		if err != nil {
			t.Fatalf("Rel(%s): %v", a.dir, err)
		}
		if got != filepath.FromSlash(c.want) {
			t.Errorf("Path(%s)\n got %s\nwant %s", c.url, got, c.want)
		}
	}
}

// Path traversal must be impossible: the mirror builds paths from remote URLs.
func TestPathCannotEscapeArchive(t *testing.T) {
	a := &Archive{dir: "/data/xml"}
	for _, url := range []string{
		"https://x/eli/../../etc/passwd",
		"https://x/eli/%2e%2e/%2e%2e/etc/passwd",
		"https://x/nothing-here",
		"https://x/eli/",
	} {
		p := a.Path(url)
		if !strings.HasPrefix(p, filepath.Clean("/data/xml")+"/") && p != filepath.Clean("/data/xml") {
			t.Errorf("Path(%s) escaped archive: %s", url, p)
		}
		if strings.Contains(p, "..") {
			t.Errorf("Path(%s) kept traversal sequence: %s", url, p)
		}
	}
}

func TestPutIsIdempotentAndAtomic(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	url := "https://www.retsinformation.dk/eli/lta/2026/789"
	body := []byte("<?xml version=\"1.0\"?><Dokument/>")

	hash1, n1, changed1, err := a.Put(url, body)
	if err != nil {
		t.Fatal(err)
	}
	if !changed1 {
		t.Error("first write should report changed")
	}
	if n1 != int64(len(body)) {
		t.Errorf("bytes = %d, want %d", n1, len(body))
	}

	// Same bytes again: reported unchanged, and the file is untouched.
	hash2, _, changed2, err := a.Put(url, body)
	if err != nil {
		t.Fatal(err)
	}
	if hash1 != hash2 {
		t.Errorf("hash not stable: %s != %s", hash1, hash2)
	}
	if changed2 {
		t.Error("rewriting identical bytes should report unchanged")
	}

	// Different bytes: changed, and the old content is replaced wholly.
	if _, _, changed3, err := a.Put(url, []byte("<Dokument>x</Dokument>")); err != nil {
		t.Fatal(err)
	} else if !changed3 {
		t.Error("different bytes should report changed")
	}
	got, err := a.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "<Dokument>x</Dokument>" {
		t.Errorf("Get = %s", got)
	}

	// No temp files may survive a successful write.
	files, _, err := a.Count()
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Errorf("Count() = %d files, want 1", files)
	}
	entries, err := os.ReadDir(filepath.Dir(a.Path(url)))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestHas(t *testing.T) {
	a, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	url := "https://www.retsinformation.dk/eli/retsinfo/2026/10019"
	if a.Has(url) {
		t.Fatal("fresh archive should not report the document as held")
	}
	if _, _, _, err := a.Put(url, []byte("<Dokument/>")); err != nil {
		t.Fatal(err)
	}
	if !a.Has(url) {
		t.Fatal("archive should report the document as held after Put")
	}
}

func TestSizeHint(t *testing.T) {
	for _, c := range []struct {
		in   int64
		want string
	}{
		{512, "512 B"},
		{2048, "2.0 KiB"},
		{5 << 30, "5.0 GiB"},
	} {
		if got := SizeHint(c.in); got != c.want {
			t.Errorf("SizeHint(%d) = %s, want %s", c.in, got, c.want)
		}
	}
}

// The sidecar rule that matters most: the backfill writes .json next to the
// canonical mirror, and must never be able to reach the mirror itself. A
// representation-aware write that could overwrite canonical bytes would corrupt
// the archive with the site's non-canonical rendering, silently, for 29k files.
func TestPutExtCannotTouchCanonicalXML(t *testing.T) {
	a, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	url := "https://www.retsinformation.dk/eli/lta/1995/498"
	canonical := []byte("<Dokument><DokumentIndhold/></Dokument>")
	if _, _, _, err := a.Put(url, canonical); err != nil {
		t.Fatal(err)
	}

	for _, ext := range []string{XMLExt, ".XML", "xml", ""} {
		if _, _, _, err := a.PutExt(url, ext, []byte(`[{"documentHtml":"<p>nope</p>"}]`)); !errors.Is(err, ErrCanonicalExt) {
			t.Errorf("PutExt with ext %q: err = %v, want ErrCanonicalExt", ext, err)
		}
	}
	got, err := a.Get(url)
	if err != nil || string(got) != string(canonical) {
		t.Errorf("canonical xml altered by a refused write: %q, err %v", got, err)
	}
}

// One document, one directory: the representation is the extension, not a second
// tree that has to be kept in sync with the first.
func TestSidecarSharesItsXMLsDirectory(t *testing.T) {
	a, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	url := "https://www.retsinformation.dk/eli/lta/1995/498"
	xmlPath := a.Path(url)
	jsonPath := a.PathExt(url, ".json")
	if filepath.Dir(jsonPath) != filepath.Dir(xmlPath) {
		t.Errorf("sidecar dir %s, want %s", filepath.Dir(jsonPath), filepath.Dir(xmlPath))
	}
	if strings.TrimSuffix(xmlPath, ".xml") != strings.TrimSuffix(jsonPath, ".json") {
		t.Errorf("sidecar %s is not the xml %s with another extension", jsonPath, xmlPath)
	}

	if _, _, _, err := a.PutExt(url, ".json", []byte(`[{"documentHtml":"x"}]`)); err != nil {
		t.Fatal(err)
	}
	if a.Has(url) {
		t.Error("a sidecar must not make the canonical representation look held")
	}
	if !a.HasExt(url, ".json") {
		t.Error("HasExt should see the sidecar")
	}
	if a.HasExt(url, ".JSON") {
		t.Error("ext normalisation made a second representation appear")
	}
}

// An extension arrives from a constant today, but the path it produces is
// filesystem-visible: a caller (or a future flag) that passes something hostile
// must not be able to write outside the archive.
func TestPathExtCannotEscapeArchive(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	url := "https://www.retsinformation.dk/eli/lta/1995/498"
	for _, ext := range []string{"../../evil", ".json/../../x", ".js\u0000on", "."} {
		p := a.PathExt(url, ext)
		if rel, err := filepath.Rel(dir, p); err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("PathExt(%q) = %s, escapes the archive rooted at %s", ext, p, dir)
		}
		if filepath.Ext(p) == "" || filepath.Ext(p) == "." {
			t.Errorf("PathExt(%q) = %s, want a usable extension", ext, p)
		}
	}
}

// Status reports "canonical xml" and "body payloads" separately; once both exist
// in one tree, a count that lumps them together reports 29k phantom documents.
func TestCountExtSplitsRepresentations(t *testing.T) {
	a, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{
		"https://www.retsinformation.dk/eli/lta/1995/498",
		"https://www.retsinformation.dk/eli/lta/1963/7",
	} {
		if _, _, _, err := a.Put(url, []byte("<Dokument/>")); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := a.PutExt("https://www.retsinformation.dk/eli/lta/1995/498", ".json", []byte(`[{"documentHtml":"x"}]`)); err != nil {
		t.Fatal(err)
	}

	xmlFiles, xmlBytes, err := a.CountExt(XMLExt)
	if err != nil {
		t.Fatal(err)
	}
	if xmlFiles != 2 {
		t.Errorf("CountExt(.xml) = %d files, want 2", xmlFiles)
	}
	if xmlBytes == 0 {
		t.Error("CountExt(.xml) reported no bytes")
	}
	jsonFiles, _, err := a.CountExt(".json")
	if err != nil {
		t.Fatal(err)
	}
	if jsonFiles != 1 {
		t.Errorf("CountExt(.json) = %d files, want 1", jsonFiles)
	}
	allFiles, _, err := a.Count()
	if err != nil {
		t.Fatal(err)
	}
	if allFiles != 3 {
		t.Errorf("Count() = %d files, want 3 across both representations", allFiles)
	}
}

// Hash is the first 64 bits of SHA-256: a known answer, because a wrong hash silently breaks
// change detection.
func TestHash(t *testing.T) {
	if got := Hash([]byte("abc")); got != "ba7816bf8f01cfea" {
		t.Errorf("Hash(abc) = %s", got)
	}
}
