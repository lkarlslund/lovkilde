// The mcp command: the mirror as a tool an agent can call.
//
// It deliberately does not use the shared runtime. The runtime opens the Badger state
// store, which locks it: a serving process would then keep the nightly crawl out, or
// fail to start while the crawl holds the lock. Serving needs the file archive and
// nothing else, so that is all this opens — and a process that cannot write is a much
// easier thing to trust in front of a legal corpus.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/lkarlslund/retsinformation/internal/config"
	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/mcpserver"
	"github.com/lkarlslund/retsinformation/internal/search"
)

// serveOptions are the settings a serving process takes. One struct because every serving
// command loads the same three things the same way: the file archive, the search index, and the
// compiled artifact. A flag that meant something different in two commands would be the bug.
type serveOptions struct {
	MaxChars        int
	Index           string
	Artifact        string
	ArtifactCacheMB int
}

// openServingServer loads what a serving process reads and nothing it can write.
//
// It deliberately does not use the shared runtime. The runtime opens the Badger state store,
// which locks it: a serving process would then keep the nightly crawl out, or fail to start
// while the crawl holds the lock. Serving needs the file archive and nothing else, so that is
// all this opens — and a process that cannot write is a much easier thing to trust in front of
// a legal corpus.
//
// Two of the three loads can downgrade and one cannot. A missing index or artifact is a
// degradation the operator did not ask for by name, so the server says what it lost and keeps
// going. A file that is there but unreadable is an error: it was named, and serving answers
// compiled from something else is not a degradation anyone should miss.
func openServingServer(ctx context.Context, resolve configFunc, o serveOptions) (*mcpserver.Server, error) {
	cfg, err := resolve()
	if err != nil {
		return nil, err
	}
	a, err := lexdania.Scan(ctx, cfg.XMLDir())
	if err != nil {
		return nil, err
	}
	if a.Skipped > 0 {
		slog.Warn("archive scan skipped unreadable files", "skipped", a.Skipped)
	}
	srv := mcpserver.New(a, cfg.SiteBase, version)
	// The horizon comes from the file the collection pass publishes, not from this process's
	// start time or the archive's mtimes: the server must state how current the data is, not
	// how long it has been running.
	srv.SweepStampPath = cfg.SweepStampPath()
	if o.MaxChars > 0 {
		srv.MaxChars = o.MaxChars
	}
	// Search is offered only if its index is actually there. Starting a server that advertises
	// a tool it cannot honour would leave the client to discover that mid-conversation, so an
	// unreadable index is an error here (the operator asked for it by name) while a missing one
	// is a logged downgrade to lookup.
	index := o.Index
	if index == "" {
		index = cfg.DataDir + "/" + indexDefaultName
	}
	idx, err := search.Load(index)
	switch {
	case err == nil:
		srv.UseIndex(idx)
		slog.Info("search index loaded", "path", index, "terms", len(idx.Terms), "documents", len(idx.Docs))
	case os.IsNotExist(err):
		slog.Warn("no search index, serving lookup only", "looked_for", index, "remedy", "run `retsin index build`")
	default:
		return nil, fmt.Errorf("search index %s: %w — rebuild it with `retsin index build`, or omit --index to serve lookups only", index, err)
	}
	// Document text comes from the artifact when one is there. A missing one downgrades to
	// parsing the XML — correct, slower, and named in the status line a client reads — while an
	// artifact from another build is an error: the operator asked for that file by name, and
	// serving a model compiled from other code is not a degradation anyone should miss.
	artifact := o.Artifact
	if artifact == "" {
		artifact = cfg.DataDir + "/" + artifactDefaultName
	}
	err = srv.UseArtifact(artifact, int64(o.ArtifactCacheMB)<<20)
	switch {
	case err == nil:
		slog.Info("serving document text from the compiled artifact", "path", artifact)
	case os.IsNotExist(err):
		slog.Warn("no compiled artifact, serving lookups by parsing XML",
			"looked_for", artifact, "remedy", "run `retsin artifact build`")
	default:
		return nil, err
	}
	return srv, nil
}

// configFunc resolves the validated flags without opening any storage.
type configFunc func() (config.Config, error)

func mcpCmd(resolve configFunc) *cobra.Command {
	var maxChars int
	var index string
	var artifact string
	var artifactCacheMB int
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the mirror over the Model Context Protocol on stdin/stdout",
		Long: `Serve lookups against the local mirror for an MCP client.

The transport is stdio, which is what a client that starts the process expects: this
process writes protocol on stdout and logs on stderr, so it must not be run with its
log redirected onto the same pipe.

  retsin mcp
  { "jsonrpc": "2.0", "method": "tools/call", "params": { "name": "lookup",
    "arguments": { "document": "eli/lta/2019/1399", "address": "§ 5, stk. 2" } } }

Two tools are offered. "lookup" reads a document by ELI path or accession, optionally
one provision in Danish citation form ("§ 267, stk. 3 nr. 2"), optionally consolidated
to a date with as_of. "search" needs the index and is advertised only when one is
loaded: build it with "retsin index build". Without it this process serves lookup and
says so on stderr, because a tool that answers "actually that needs an index" is worse
than a tool that is not there.

Every answer is markdown, and every answer says where its text came from. That is not
decoration: the mirror holds both the register's own structured body and, for documents
whose XML has none, text our reader recovered from the site's HTML. The paired sample
says the recovered text reproduces the canonical wording byte for byte in 84.9 % of
documents, which is worth reading and not worth quoting without saying so.

The document text is read from the compiled artifact ("retsin artifact build") when one is
there, which is what makes a lookup a seek and a decompression instead of an XML parse; without
one the server parses and says so in every answer's dataset line. An artifact written by another
build is refused outright, with a message naming which side is stale: the artifact holds the
document model as JSON, so a file from another revision decodes into this server's types with
another revision's meaning.

Stdout is reserved for the protocol; logs go to stderr either way.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			srv, err := openServingServer(cmd.Context(), resolve, serveOptions{
				MaxChars: maxChars, Index: index, Artifact: artifact, ArtifactCacheMB: artifactCacheMB,
			})
			if err != nil {
				return err
			}
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
		"full-text index to serve (default <data-dir>/"+indexDefaultName+"); if it is missing, only lookup is advertised")
	cmd.Flags().StringVar(&artifact, "artifact", "",
		"compiled document artifact to read text from (default <data-dir>/"+artifactDefaultName+"); if it is missing, lookups parse the XML instead and say so")
	cmd.Flags().IntVar(&artifactCacheMB, "artifact-cache-mb", mcpserver.DefaultArtifactCacheBytes>>20,
		"megabytes of artifact payloads the serving cache may hold; the bound is in bytes, because that is what a cached document costs")
	return cmd
}
