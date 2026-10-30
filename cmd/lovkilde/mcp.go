// The mcp command: the mirror as a tool an agent can call.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/spf13/cobra"

	"github.com/lkarlslund/lovkilde/internal/artifactdir"
	"github.com/lkarlslund/lovkilde/internal/checks"
	"github.com/lkarlslund/lovkilde/internal/compiled"
	"github.com/lkarlslund/lovkilde/internal/config"
	"github.com/lkarlslund/lovkilde/internal/eula"
	"github.com/lkarlslund/lovkilde/internal/mcpserver"
	"github.com/lkarlslund/lovkilde/internal/release"
	"github.com/lkarlslund/lovkilde/internal/search"
	"github.com/lkarlslund/lovkilde/internal/updates"
)

// serveOptions are the settings a serving process takes. One struct because every serving
// command loads the same two things the same way: the compiled artifact and the search index. A flag that meant something different in two commands would be the bug.
type serveOptions struct {
	MaxChars        int
	Index           string
	Artifact        string
	ArtifactCacheMB int
	// Release names where a missing artifact is downloaded from, and where newer ones are looked
	// for every CheckEvery (0: never). Both apply only when following the current artifact.
	Release    string
	CheckEvery time.Duration
	// Restarts says the process restarts as a newer build once one is downloaded (serve).
	Restarts bool
}

// addFetchFlags registers the download settings serve and mcp share.
func addFetchFlags(cmd *cobra.Command, o *serveOptions) {
	cmd.Flags().StringVar(&o.Release, "release", release.DefaultSource,
		"release repository the artifact is downloaded from when there is none, and updated from: a directory, or hf://owner/name[@revision]")
	cmd.Flags().DurationVar(&o.CheckEvery, "check-every", release.DefaultEvery,
		"check this often whether newer law texts or a newer program are published; nothing is downloaded until asked. 0 turns it off. Rounded to daily, weekly or monthly; the website's settings page overrides it")
}

// openServingServer loads what a serving process reads, which is an artifact and nothing else: it
// opens no state store and reads no XML, and a process that cannot write is a much easier thing to
// trust in front of a legal corpus.
//
// Without an artifact there is nothing to serve, and that is an error naming the remedy. An
// artifact that is unreadable, from another document model or without a catalogue is the same:
// the operator built or installed it. The index is different: it is derived from the artifact,
// never distributed, and the server keeps it itself — rebuilding one that is missing, unreadable
// or cut from another artifact (buildServingIndex).
//
// The checks it returns, nil when it serves one pinned artifact, hold the scheduled check for a
// newer dataset; the caller adds any others and runs them.
func openServingServer(ctx context.Context, resolve configFunc, o serveOptions) (*mcpserver.Server, *updates.Updates, error) {
	cfg, err := resolve()
	if err != nil {
		return nil, nil, err
	}
	// With --artifact the server reads that one file for as long as it runs. Without it, it reads
	// the current artifact (see internal/artifactdir) and follows the pointer when a new one is
	// installed, swapping artifact, catalogue and index together.
	artifact := o.Artifact
	follow := artifact == ""
	if follow {
		artifact, err = artifactdir.Current(cfg.DataDir)
		if errors.Is(err, artifactdir.ErrNone) {
			artifact, err = downloadMissing(ctx, cfg.DataDir, o.Release)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	srv := newServingServer(cfg, o)
	err = srv.UseArtifact(artifact, int64(o.ArtifactCacheMB)<<20)
	if follow && errors.Is(err, compiled.ErrOtherModel) {
		// An upgraded program finding the artifact of the model it read before: it fetches the
		// published artifact of its own model, or says what it is waiting for.
		slog.Info("the current artifact is of another document model; fetching the published one this version reads", "path", artifact, "reads", compiled.ModelVersion)
		if artifact, err = downloadMissing(ctx, cfg.DataDir, o.Release); err == nil {
			err = srv.UseArtifact(artifact, int64(o.ArtifactCacheMB)<<20)
		}
	}
	if err != nil {
		return nil, nil, err
	}
	slog.Info("serving document text from the compiled artifact", "path", artifact)

	// The index is the server's to keep: derived from the artifact, never distributed, and
	// rebuilt in the background when the one on disk is missing, unreadable, from an older format,
	// cut from another artifact or by older code. Lookups answer meanwhile.
	index := o.Index
	if index == "" {
		if index, err = artifactdir.IndexFor(artifact); err != nil {
			return nil, nil, err
		}
	}
	want := search.SourceFor(srv.ArtifactIdentity())
	if idx, ok := loadMatchingIndex(index, want); ok {
		srv.UseIndex(idx)
	} else {
		srv.IndexBuilding(true)
		go func() {
			defer srv.IndexBuilding(false)
			if idx, err := buildServingIndex(ctx, artifact, index); err == nil {
				srv.UseIndex(idx)
			}
		}()
	}
	// The checks only ask: a newer program or newer texts are downloaded when the user asks for
	// them, on the settings page or through the tool update. A server serving one artifact named
	// on the command line follows no release.
	upd := updates.Config{DataDir: cfg.DataDir, Checks: checks.Open(cfg.DataDir), Restarts: o.Restarts, Every: checks.IntervalFor(o.CheckEvery)}
	if follow {
		go followCurrent(ctx, srv, cfg.DataDir, artifact, o, followInterval)
		if repo, err := release.Open(o.Release); err == nil {
			upd.Repo = repo
		}
	}
	if sw, err := publishedChecker(); err == nil {
		upd.Software = sw
	}
	u := updates.New(upd)
	srv.Updates = u
	return srv, u, nil
}

// downloadMissing installs the published artifact into a data directory that has none.
func downloadMissing(ctx context.Context, dataDir, spec string) (string, error) {
	repo, err := release.Open(spec)
	if errors.Is(err, release.ErrNoRepo) {
		return "", fmt.Errorf("no compiled artifact in %s: build one with `lovkilde-update artifact build`, or download one with --release", artifactdir.Dir(dataDir))
	}
	if err != nil {
		return "", err
	}
	slog.Info("no compiled artifact, downloading the published one", "release", repo.String(), "into", artifactdir.Dir(dataDir))
	res, err := release.FetchArtifact(ctx, repo, dataDir)
	if err != nil {
		return "", fmt.Errorf("no compiled artifact in %s, and downloading it from %s failed: %w", artifactdir.Dir(dataDir), repo, err)
	}
	_ = release.MarkChecked(dataDir)
	updates.LogTransfer(res.Transfer)
	slog.Info("downloaded the published artifact", "path", res.Path, "downloaded", res.Downloaded, "reused", res.Reused)
	return res.Path, nil
}

// newServingServer is a server with the settings every serving process shares.
func newServingServer(cfg config.Config, o serveOptions) *mcpserver.Server {
	srv := mcpserver.New(nil, cfg.SiteBase, version)
	srv.EULA = eula.Open(cfg.DataDir)
	if o.MaxChars > 0 {
		srv.MaxChars = o.MaxChars
	}
	return srv
}

// loadMatchingIndex loads the index at path if it was cut from the artifact named by want, by
// this build's cut, and says why not otherwise.
func loadMatchingIndex(path, want string) (*search.Index, bool) {
	idx, err := search.Load(path)
	switch {
	case err == nil && idx.Source == want:
		slog.Info("search index loaded", "path", path, "terms", len(idx.Terms), "documents", len(idx.Docs))
		return idx, true
	case err == nil:
		slog.Info("search index was built from another artifact or by older code, rebuilding it", "path", path, "index_source", idx.Source, "want", want)
	case os.IsNotExist(err):
		slog.Info("no search index, building it from the artifact", "path", path)
	default:
		slog.Info("search index unreadable, rebuilding it from the artifact", "path", path, "err", err)
	}
	return nil, false
}

// buildServingIndex builds the search index from the artifact and saves it. The build's memory is
// held to its own share: a soft limit of what the server already uses plus the 4 GiB the build
// needs, lifted afterwards, so the collector works harder during the build instead of the process
// growing by twice the build's live heap.
func buildServingIndex(ctx context.Context, artifact, index string) (*search.Index, error) {
	start := time.Now()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if os.Getenv("GOMEMLIMIT") == "" {
		prev := debug.SetMemoryLimit(int64(ms.HeapInuse) + 4<<30)
		defer debug.SetMemoryLimit(prev)
	}
	idx, err := indexFromArtifact(ctx, artifact, search.Options{}, 0)
	if err != nil {
		slog.Error("building the search index failed", "artifact", artifact, "err", err)
		return nil, err
	}
	if err := saveIndex(idx, index); err != nil {
		// Serving it is still right; the next start builds it again.
		slog.Warn("search index built but not saved", "path", index, "err", err)
	}
	debug.FreeOSMemory()
	slog.Info("search index ready", "path", index, "documents", len(idx.Docs), "terms", len(idx.Terms), "elapsed", time.Since(start).Round(time.Second))
	return idx, nil
}

// followInterval is how often a serving process looks at the pointer for a new artifact.
var followInterval = 30 * time.Second

// followCurrent watches for a new current artifact and moves the server to it: the new artifact is
// opened and checked and its index loaded or built while the old pair is still served, then both
// are swapped at once, and the artifacts no longer kept are pruned. A new artifact that cannot be
// served is reported once and the old pair kept.
func followCurrent(ctx context.Context, srv *mcpserver.Server, dataDir, serving string, o serveOptions, every time.Duration) {
	refused := ""
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		next, err := artifactdir.Current(dataDir)
		if err != nil || next == serving || next == refused {
			continue
		}
		slog.Info("a new artifact is current, preparing it", "path", next, "serving", serving)
		c, err := srv.PrepareArtifact(next, int64(o.ArtifactCacheMB)<<20)
		if err != nil {
			slog.Error("the new artifact cannot be served, keeping the current one", "path", next, "err", err)
			refused = next
			continue
		}
		index := artifactdir.IndexPath(next, c.Artifact().Header.Stamp)
		idx, ok := loadMatchingIndex(index, search.SourceFor(c.Artifact().Identity()))
		if !ok {
			if idx, err = buildServingIndex(ctx, next, index); err != nil {
				c.Close()
				refused = next
				continue
			}
		}
		srv.SwapCorpus(c, idx)
		slog.Info("now serving the new artifact", "path", next, "was", serving)
		serving, refused = next, ""
		if removed, err := artifactdir.Prune(dataDir); err == nil && len(removed) > 0 {
			slog.Info("pruned artifacts no longer kept", "removed", removed)
		}
	}
}

// saveIndex writes the index under a temporary name and renames it, so a reader never sees half.
func saveIndex(idx *search.Index, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".writing"
	if err := idx.Save(tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func mcpCmd(resolve configFunc) *cobra.Command {
	var maxChars int
	var index string
	var artifact string
	var artifactCacheMB int
	var o serveOptions
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the mirror over the Model Context Protocol on stdin/stdout",
		Long: `Serve lookups against the local mirror for an MCP client.

The transport is stdio, which is what a client that starts the process expects: this
process writes protocol on stdout and logs on stderr, so it must not be run with its
log redirected onto the same pipe.

  lovkilde mcp
  { "jsonrpc": "2.0", "method": "tools/call", "params": { "name": "lookup",
    "arguments": { "document": "eli/lta/2019/1399", "address": "§ 5, stk. 2" } } }

Two tools are offered. "lookup" reads a document by ELI path or accession, optionally
one provision in Danish citation form ("§ 267, stk. 3 nr. 2"), optionally consolidated
to a date with as_of. "search" needs the index and is advertised only when one is
loaded: build it with "lovkilde index build". Without it this process serves lookup and
says so on stderr, because a tool that answers "actually that needs an index" is worse
than a tool that is not there.

Every answer is markdown, and every answer says where its text came from. That is not
decoration: the mirror holds both the register's own structured body and, for documents
whose XML has none, text our reader recovered from the site's HTML. The paired sample
says the recovered text reproduces the canonical wording byte for byte in 84.9 % of
documents, which is worth reading and not worth quoting without saying so.

Everything is read from the compiled artifact ("lovkilde-update artifact build"): the catalogue,
each document's model, and the amending acts' changes that as_of applies. Without one it
downloads the published artifact from --release, and refuses to start when it cannot. While it
runs it checks --release for a newer artifact every --check-every and says so in its answers; the
tool update downloads it when the user asks, and the server moves to it. An artifact of another document model is refused too, with a message
naming which side is stale: the artifact holds the model as JSON, so a file from another
revision decodes into this server's types with another revision's meaning.

Stdout is reserved for the protocol; logs go to stderr either way.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.MaxChars, o.Index, o.Artifact, o.ArtifactCacheMB = maxChars, index, artifact, artifactCacheMB
			srv, upd, err := openServingServer(cmd.Context(), resolve, o)
			if err != nil {
				return err
			}
			go upd.Run(cmd.Context())
			// One line of orientation on stderr: what a client is about to be served,
			// and from where. A server that answers silently cannot be debugged.
			fmt.Fprintf(gate.LogWriter(), "serving %s over stdio\n", srv.Status())
			return srv.Serve(cmd.Context())
		},
	}
	cmd.Flags().IntVar(&maxChars, "max-chars", 0,
		fmt.Sprintf("cap every answer at this many characters (default %d; an answer larger than this is truncated and says so)",
			mcpserver.DefaultMaxChars))
	cmd.Flags().StringVar(&index, "index", "",
		"where the search index is kept (default: beside the artifact, named for its stamp); built from the artifact when it is missing or from another artifact")
	cmd.Flags().StringVar(&artifact, "artifact", "",
		"serve this artifact file and no other (default: the current one, followed as new ones are installed)")
	cmd.Flags().IntVar(&artifactCacheMB, "artifact-cache-mb", mcpserver.DefaultArtifactCacheBytes>>20,
		"megabytes of artifact payloads the serving cache may hold; the bound is in bytes, because that is what a cached document costs")
	addFetchFlags(cmd, &o)
	return cmd
}
