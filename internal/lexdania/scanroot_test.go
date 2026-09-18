package lexdania

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestScanRejectsMissingRoot guards a failure mode that produced a confusing
// measurement: walking a path that is not a directory visits nothing, and the walk's
// error callback counts that as skipped files rather than as a problem. The result
// was an empty Archive with a nil error, so every count downstream — documents,
// sections, coverage — reported zero and looked like a finding about the corpus
// rather than a wrong path.
func TestScanRejectsMissingRoot(t *testing.T) {
	if _, err := Scan(context.Background(), t.TempDir()+"/does-not-exist"); err == nil {
		t.Error("Scan accepted a mirror root that does not exist; it must not report an empty archive as success")
	}

	// A file is not a directory either, and the same silence would apply.
	dir := t.TempDir()
	file := dir + "/not-a-dir.xml"
	if err := os.WriteFile(file, []byte("<x/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(context.Background(), file); err == nil {
		t.Error("Scan accepted a file as a mirror root")
	}

	// An empty directory is a real (if unusual) root: it exists, so scanning it is
	// legitimate and must succeed with zero entries rather than error.
	empty := t.TempDir()
	a, err := Scan(context.Background(), empty)
	if err != nil {
		t.Fatalf("Scan of an existing empty directory failed: %v", err)
	}
	if a.Len() != 0 {
		t.Errorf("Len() = %d for an empty directory", a.Len())
	}
}

// TestScanCountsUnreadableFiles guards the Skipped counter's reason for existing: a
// file the reader cannot parse must be visible in the report, not silently absent.
func TestScanCountsUnreadableFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/junk.xml", []byte("this is not legal xml at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.Len() != 0 {
		t.Fatalf("indexed %d entries from a file with no Meta block", a.Len())
	}
	if a.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1: an unreadable file has to show up somewhere", a.Skipped)
	}
	if !strings.Contains(a.Root, dir) {
		t.Errorf("Root = %q, want the scanned directory", a.Root)
	}
}
