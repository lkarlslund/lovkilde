package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The report is the evidence for an open decision, so the two things it must never lose are the
// counts it was asked to keep and the rule those counts are being tested against.
func TestWriteChangeReportKeepsCountsAndRule(t *testing.T) {
	dir := t.TempDir()
	stamp := time.Date(2026, 9, 18, 14, 42, 10, 0, time.UTC)
	name, err := writeChangeReport(dir, changeReport{
		Generated: stamp, Since: "2026-08-09", Total: 424,
		ByDate:    map[string]int{"2026-09-09": 154, "2026-09-10": 86},
		Refetched: 424, FreshContent: 424,
		Unresolved: []unresolvedChange{{date: "2026-09-10", accession: "B20269999999", docType: "BEK", reason: "new"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"| 2026-09-09 | 154 |", "| already collected | 424 |",
		"| named an accession this mirror has never seen | 0 |",
		"B20269999999", "Decision rule:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "unknown to this mirror | 424") {
		t.Error("the unknown count picked up a different counter")
	}
}

// An earlier measurement must survive a later one: the file name carries the second, and a second
// run at the same second is refused rather than rewritten.
func TestWriteChangeReportRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	stamp := time.Date(2026, 9, 18, 14, 42, 10, 0, time.UTC)
	if _, err := writeChangeReport(dir, changeReport{Generated: stamp, Total: 1, ByDate: map[string]int{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := writeChangeReport(dir, changeReport{Generated: stamp, Total: 2, ByDate: map[string]int{}}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second report at the same stamp: %v, want a refusal naming an existing file", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "reports", "*.md"))
	if err != nil || len(files) != 1 {
		t.Fatalf("reports written: %v (%v)", files, err)
	}
}
