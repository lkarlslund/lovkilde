//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/lovkilde/internal/compiled"
)

// A server restarts as a newer build kept in its data directory, once that build has shown it
// runs; an older build, or one that does not run, leaves it as it is.
func TestServerWatchesForANewerProgram(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	restore := compiled.CurrentBuild
	t.Cleanup(func() { compiled.CurrentBuild = restore })
	compiled.CurrentBuild = func() (compiled.BuildStamp, bool) {
		return compiled.BuildStamp{Revision: strings.Repeat("a", 40), Time: now}, true
	}
	keep := func(t *testing.T, dataDir string, at time.Time, script string) string {
		t.Helper()
		dir := programDir(dataDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "lovkilde-"+at.Format("20060102T150405Z")+"-bbbbbbbbbbbb")
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	watch := func(dataDir string, wait time.Duration) string {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		return watchProgram(ctx, dataDir, 10*time.Millisecond)
	}

	newer := t.TempDir()
	path := keep(t, newer, now.Add(time.Hour), `echo "lovkilde version r00200"`)
	if got := watch(newer, 5*time.Second); got != path {
		t.Errorf("a newer build that runs: got %q, want %q", got, path)
	}

	older := t.TempDir()
	keep(t, older, now.Add(-time.Hour), `echo "lovkilde version r00001"`)
	if got := watch(older, 200*time.Millisecond); got != "" {
		t.Errorf("an older build was taken: %q", got)
	}

	broken := t.TempDir()
	keep(t, broken, now.Add(time.Hour), "exit 1")
	if got := watch(broken, 200*time.Millisecond); got != "" {
		t.Errorf("a build that does not run was taken: %q", got)
	}
}
