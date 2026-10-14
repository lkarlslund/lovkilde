package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/artifactdir"
	"github.com/lkarlslund/retsinformation/internal/config"
	"github.com/lkarlslund/retsinformation/internal/mcpserver"
	"github.com/lkarlslund/retsinformation/internal/release"
	"github.com/lkarlslund/retsinformation/internal/search"
)

// TestTheServerKeepsItsOwnIndex: indexes are never distributed, so a serving process builds the
// index from the artifact it serves when there is none, saves it, and builds it again when the
// artifact on disk is a different build — and a matching one on disk is simply used.
func TestTheServerKeepsItsOwnIndex(t *testing.T) {
	dir := t.TempDir()
	for into, from := range map[string]string{
		"xml/lta/1942/542.xml": "testdata/lta-1942-542.xml",
		"xml/fob/1989/104.xml": "testdata/fob-1989-104.xml",
	} {
		raw, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, into)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func() (config.Config, error) {
		cfg := config.Default()
		cfg.DataDir = dir
		return cfg, nil
	}
	var indexPath string // the current artifact's, found after each build
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stops the servers' watchers with the test
	start := func() *mcpserver.Server {
		t.Helper()
		srv, _, err := openServingServer(ctx, resolve, serveOptions{})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for !srv.HasSearch() {
			if time.Now().After(deadline) {
				t.Fatal("the server did not get a search index")
			}
			time.Sleep(10 * time.Millisecond)
		}
		return srv
	}
	saved := func() string {
		t.Helper()
		artifact, err := artifactdir.Current(dir)
		if err != nil {
			t.Fatal(err)
		}
		if indexPath, err = artifactdir.IndexFor(artifact); err != nil {
			t.Fatal(err)
		}
		idx, err := search.Load(indexPath)
		if err != nil {
			t.Fatalf("no saved index: %v", err)
		}
		return idx.Source
	}

	buildArtifact(t, dir)
	srv := start()
	if got := saved(); got != search.SourceFor(srv.ArtifactIdentity()) || got == "" {
		t.Fatalf("saved index names %q, the artifact is %q", got, srv.ArtifactIdentity())
	}
	if res, err := srv.Search(mcpserver.SearchRequest{Query: "lov"}); err != nil || res.Total == 0 {
		t.Errorf("search after the build: %v, %+v", err, res)
	}
	first := saved()

	// The same artifact: the saved index is used as it is.
	before, _ := os.Stat(indexPath)
	start()
	if after, _ := os.Stat(indexPath); !after.ModTime().Equal(before.ModTime()) {
		t.Error("a matching index was rebuilt")
	}

	// The same artifact, cut by older code: rebuilt too.
	old, err := search.Load(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	old.Source = srv.ArtifactIdentity() + " cut 0"
	if err := old.Save(indexPath); err != nil {
		t.Fatal(err)
	}
	start()
	if got := saved(); got != first {
		t.Errorf("an index cut by older code was kept: it names %q, want %q", got, first)
	}

	// A new build of the artifact: the index is rebuilt for it.
	buildArtifact(t, dir)
	srv = start()
	if got := saved(); got == first || got != search.SourceFor(srv.ArtifactIdentity()) {
		t.Errorf("after a new artifact the saved index names %q (was %q), the artifact is %q", got, first, srv.ArtifactIdentity())
	}
}

// TestTheServerFollowsANewArtifact: installing a new artifact moves a running server to it —
// artifact and index together — without a restart, while it keeps answering from the old pair
// until the new index is ready.
func TestTheServerFollowsANewArtifact(t *testing.T) {
	dir := t.TempDir()
	for into, from := range map[string]string{
		"xml/lta/1942/542.xml": "testdata/lta-1942-542.xml",
		"xml/fob/1989/104.xml": "testdata/fob-1989-104.xml",
	} {
		raw, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, into)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func() (config.Config, error) {
		cfg := config.Default()
		cfg.DataDir = dir
		return cfg, nil
	}
	defer func(was time.Duration) { followInterval = was }(followInterval)
	followInterval = 10 * time.Millisecond

	buildArtifact(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, _, err := openServingServer(ctx, resolve, serveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitFor("the first index", srv.HasSearch)
	first := srv.ArtifactIdentity()

	buildArtifact(t, dir)
	waitFor("the server to move to the new artifact", func() bool { return srv.ArtifactIdentity() != first })
	if res, err := srv.Search(mcpserver.SearchRequest{Query: "lov"}); err != nil || res.Total == 0 {
		t.Errorf("search on the new pair: %v, %+v", err, res)
	}
	if res, err := srv.Lookup(mcpserver.LookupRequest{Document: "eli/lta/1942/542", MaxChars: 200}); err != nil || res.Text == "" {
		t.Errorf("lookup on the new pair: %v", err)
	}
	// Both artifacts are kept: the new one and the one before it.
	kept, _ := filepath.Glob(filepath.Join(artifactdir.Dir(dir), "artifact-*"+artifactdir.Ext))
	if len(kept) != 2 {
		t.Errorf("%d artifacts on disk after the move, want the new and the previous: %v", len(kept), kept)
	}
}

// TestAServerNeedsOnlyTheArtifact: with the XML archive gone, a server started on the artifact
// alone still finds documents, lists them, reads them, answers as of a date, and searches.
func TestAServerNeedsOnlyTheArtifact(t *testing.T) {
	dir := t.TempDir()
	for into, from := range map[string]string{
		"xml/lta/1942/542.xml": "testdata/lta-1942-542.xml",
		"xml/fob/1989/104.xml": "testdata/fob-1989-104.xml",
	} {
		raw, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, into)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	buildArtifact(t, dir)
	if err := os.RemoveAll(filepath.Join(dir, "xml")); err != nil {
		t.Fatal(err)
	}
	resolve := func() (config.Config, error) {
		cfg := config.Default()
		cfg.DataDir = dir
		return cfg, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, _, err := openServingServer(ctx, resolve, serveOptions{})
	if err != nil {
		t.Fatalf("a server with no XML archive: %v", err)
	}
	if res := srv.Browse(mcpserver.BrowseRequest{Limit: 10}); res.Total != 2 {
		t.Errorf("browse lists %d documents, want the artifact's 2", res.Total)
	}
	res, err := srv.Lookup(mcpserver.LookupRequest{Document: "eli/lta/1942/542", MaxChars: 400})
	if err != nil || res.Text == "" {
		t.Fatalf("lookup without the XML: %v", err)
	}
	if _, err := srv.Lookup(mcpserver.LookupRequest{Document: "eli/lta/1942/542", AsOf: "2020-01-01", MaxChars: 400}); err != nil {
		t.Errorf("as-of lookup without the XML: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for !srv.HasSearch() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got, err := srv.Search(mcpserver.SearchRequest{Query: "lov"}); err != nil || got.Total == 0 {
		t.Errorf("search without the XML: %v", err)
	}
}

// TestAServerWithoutAnArtifactDownloadsThePublishedOne: a data directory with nothing in it gets
// the published artifact before the server starts, and serves it. The release repository is a
// local directory, which goes through the same interface as a Hugging Face one.
func TestAServerWithoutAnArtifactDownloadsThePublishedOne(t *testing.T) {
	built := t.TempDir()
	for into, from := range map[string]string{
		"xml/lta/1942/542.xml": "testdata/lta-1942-542.xml",
	} {
		raw, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(built, into)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	buildArtifact(t, built)
	path, err := artifactdir.Current(built)
	if err != nil {
		t.Fatal(err)
	}
	// Published to a local release repository, the way a Hugging Face one is laid out.
	repo := t.TempDir()
	local, _ := release.Open(repo)
	if _, err := release.Publish(context.Background(), local, release.Release{Artifact: path}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	resolve := func() (config.Config, error) {
		cfg := config.Default()
		cfg.DataDir = dir
		return cfg, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, _, err := openServingServer(ctx, resolve, serveOptions{}); err == nil || !strings.Contains(err.Error(), "--release") {
		t.Fatalf("an empty directory with no repository: %v", err)
	}
	srv, _, err := openServingServer(ctx, resolve, serveOptions{Release: repo})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := srv.Lookup(mcpserver.LookupRequest{Document: "eli/lta/1942/542", MaxChars: 200}); err != nil || res.Text == "" {
		t.Errorf("lookup on the downloaded artifact: %v", err)
	}
}
