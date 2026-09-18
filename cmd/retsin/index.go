// The index and search commands: building the derived full-text index, and asking it
// questions from a terminal.
//
// Neither opens the Badger state store. The index is derived from the mirror the same
// way data/meta/kv is — rebuildable from files at zero HTTP cost — and Badger locks
// exclusively per process, so a command that opened it would block the nightly crawl
// for the length of a full-corpus build, and `retsin mcp` could not start alongside it.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lkarlslund/retsinformation/internal/search"
)

// indexDefaultName is where the index goes under --data-dir.
const indexDefaultName = "search.idx"

func indexCmd(resolve configFunc) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Build and inspect the derived full-text index",
	}
	cmd.AddCommand(indexBuildCmd(resolve))
	return cmd
}

func indexBuildCmd(resolve configFunc) *cobra.Command {
	var out string
	var perStratum int
	var folded bool
	var b float64

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Index every document in the mirror for full-text search",
		Long: `Build the search index from the mirror on disk. No network traffic: the index is
derived data, rebuilt from the files rather than fetched.

  retsin index build
  retsin index build --sample 50      # a stratum-balanced subset, for measuring

The unit of the index is the provision, not the document: a hit comes back as an ELI
path plus a section address ("§ 267, stk. 3"), because a document-level hit in a
300-page act is a pointer at a haystack. Title text is indexed too, since searching for
a law by name is the most common legal query there is.

The index stores no statute text. Snippets are rendered by reading the archive again at
query time, which keeps 12.6 GB of legal text in one place instead of two.

Inflected forms are folded ("pligt" reaches "pligtige") and query terms extend right
("miljøbeskyttelse" reaches "miljøbeskyttelsesloven"). æ, ø and å are never folded into
other letters, and the pre-1948 spelling of Å as "Aa" is folded to Å so a modern query
finds old text.

The file is written to a temporary name and renamed, so a reader never sees a
half-built index.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolve()
			if err != nil {
				return err
			}
			if out == "" {
				out = cfg.DataDir + "/" + indexDefaultName
			}
			start := time.Now()
			slog.Info("scanning the mirror", "dir", cfg.XMLDir())
			docs, units, err := search.Sample(cfg.XMLDir(), perStratum)
			if err != nil {
				return err
			}
			slog.Info("read documents", "documents", len(docs), "units", len(units), "elapsed", time.Since(start).Round(time.Millisecond))

			opts := search.Options{Folded: folded}
			idx, err := search.Build(docs, units, opts)
			if err != nil {
				return err
			}
			if b >= 0 {
				idx.B = b
			}
			buildTime := time.Since(start)

			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			tmp := out + ".writing"
			if err := idx.Save(tmp); err != nil {
				return err
			}
			if err := os.Rename(tmp, out); err != nil {
				return err
			}
			st, err := os.Stat(out)
			if err != nil {
				return err
			}

			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			slog.Info("index built",
				"path", out,
				"documents", len(docs),
				"units", len(units),
				"terms", len(idx.Terms),
				"bytes", st.Size(),
				"folded", folded,
				"b", idx.B,
				"elapsed", buildTime.Round(time.Millisecond),
				"peak_heap_mb", float64(ms.HeapAlloc)/1e6,
				"heap_in_use_mb", float64(ms.HeapInuse)/1e6,
				"rss", peakRSS(),
			)
			fmt.Fprintf(gate.LogWriter(), "%s: %d documents, %d provisions, %d terms, %.1f MB, %s\n",
				out, len(docs), len(units), len(idx.Terms), float64(st.Size())/1e6, buildTime.Round(time.Millisecond))
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "where to write the index (default <data-dir>/"+indexDefaultName+")")
	cmd.Flags().IntVar(&perStratum, "sample", 0, "index only this many documents per kind/decade stratum (0 indexes everything; a non-zero value is for measuring, and says so in the file name you should not ship)")
	cmd.Flags().BoolVar(&folded, "folded", false, "also index a stream with Danish inflectional endings folded; measured as adding no recall on real queries while costing 31% of the index size, so it is off (see internal/search/testdata/index-measurement-20260917.txt)")
	cmd.Flags().Float64Var(&b, "bm25-b", -1, "override BM25 length normalisation; the measured default for this corpus is 0 (sweep in internal/search/testdata/)")
	return cmd
}

func searchCmd(resolve configFunc) *cobra.Command {
	var index, kind, source string
	var since, until, limit int
	var noSnippet bool

	cmd := &cobra.Command{
		Use:   "search <words>",
		Short: "Full-text search the mirror: hits are provisions, not documents",
		Example: `  retsin search "miljøbeskyttelse pligt"
  retsin search "lov nr. 1399" --kind lta --since 2015
  retsin search "ubetinget råderet" --source xml   # only the register's own text`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolve()
			if err != nil {
				return err
			}
			if index == "" {
				index = cfg.DataDir + "/" + indexDefaultName
			}
			idx, err := search.Load(index)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("%s: %w — build it first with `retsin index build`", index, err)
				}
				return err
			}
			hits := idx.Search(search.Query{
				Text:    strings.Join(args, " "),
				Kind:    kind,
				MinYear: since,
				MaxYear: until,
				Source:  source,
				Limit:   limit,
			})
			w := gate.LogWriter()
			if len(hits) == 0 {
				fmt.Fprintf(w, "no provision matches %q in %s (%d documents indexed)\n",
					strings.Join(args, " "), index, len(idx.Docs))
				return nil
			}
			for i, h := range hits {
				fmt.Fprintf(w, "%2d. %s — %s\n     %s [%s] score %.2f matched: %s\n",
					i+1, h.Doc.Title, h.Addr, h.Doc.ELI, h.Doc.Source, h.Score, strings.Join(h.Matched, ","))
				if noSnippet {
					continue
				}
				// The snippet re-reads the document. A hit whose snippet cannot be
				// rendered still prints its address rather than disappearing.
				if s := search.Snippet(h.Doc, h.Addr, h.Matched, 0); s != "" {
					fmt.Fprintf(w, "     %s\n", strings.ReplaceAll(s, "\n", " "))
				} else {
					fmt.Fprintf(w, "     (no snippet: %s)\n", h.Doc.Path)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&index, "index", "", "index file (default <data-dir>/"+indexDefaultName+")")
	cmd.Flags().StringVar(&kind, "kind", "", "limit to one medium: lta, ltb, ltc, mt, retsinfo, ft, fob, accn")
	cmd.Flags().StringVar(&source, "source", "", "limit to one text source: xml or html")
	cmd.Flags().IntVar(&since, "since", 0, "only documents from this year")
	cmd.Flags().IntVar(&until, "until", 0, "only documents up to this year")
	cmd.Flags().IntVar(&limit, "limit", 10, "maximum hits")
	cmd.Flags().BoolVar(&noSnippet, "no-snippet", false, "skip rendering passages (each one re-reads a document from the mirror)")
	return cmd
}

// peakRSS reports the high-water mark from /proc, or "" where that is unavailable.
//
// The build holds a whole-corpus term map in memory, so the honest number has to be
// printed rather than guessed at: a derived index nobody can afford to rebuild is not
// a derived index.
func peakRSS() string {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "VmHWM:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "VmHWM:"))
		}
	}
	return ""
}
