// The index and search commands: building the derived full-text index, and asking it
// questions from a terminal.
//
// Neither opens anything but the artifact: the index is cut from it, and is never
// distributed.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/lkarlslund/lovkilde/internal/artifactdir"
	"github.com/lkarlslund/lovkilde/internal/chain"
	"github.com/lkarlslund/lovkilde/internal/compiled"
	"github.com/lkarlslund/lovkilde/internal/lexdoc"
	"github.com/lkarlslund/lovkilde/internal/progress"
	"github.com/lkarlslund/lovkilde/internal/search"
)

func indexCmd(resolve configFunc) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Build and inspect the derived full-text index",
	}
	cmd.AddCommand(indexBuildCmd(resolve))
	return cmd
}

func indexBuildCmd(resolve configFunc) *cobra.Command {
	var out, artifactPath string
	var perStratum int
	var folded bool
	var b float64

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Index every document in the compiled artifact for full-text search",
		Long: `Build the search index from the compiled artifact (lovkilde-update artifact build). No
network traffic, and no mirror needed: anyone holding the artifact can build the index
that goes with it, and the index records which artifact it came from so a server can
tell a matching pair from a mismatched one.

  lovkilde index build
  lovkilde index build --artifact some/artifact.lkz --out some/search.lkiz
  lovkilde index build --sample 50 --out some.lkiz   # a stratum-balanced subset, for measuring

The build streams: documents are read, cut and counted a batch at a time, and their
text is let go, so memory is the index's postings rather than the corpus. A full build
peaks at about 4 GB: the collector works to a 4 GiB soft ceiling unless GOMEMLIMIT says
otherwise.

The unit of the index is the provision, not the document: a hit comes back as an ELI
path plus a section address ("§ 267, stk. 3"), because a document-level hit in a
300-page act is a pointer at a haystack. Title text is indexed too, since searching for
a law by name is the most common legal query there is.

The index stores no statute text. Snippets are rendered by reading the artifact again at
query time, which keeps the legal text in one place instead of two.

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
			start := time.Now()
			opts := search.Options{Folded: folded}
			var idx *search.Index
			if artifactPath == "" {
				if artifactPath, err = artifactdir.Current(cfg.DataDir); err != nil {
					return err
				}
			}
			if out == "" {
				if perStratum > 0 {
					// A sample is a measurement, not the index a server would load for this artifact.
					return fmt.Errorf("--sample needs --out: the index beside the artifact is the whole artifact's")
				}
				// Beside its artifact, named for its stamp: the file a server looks for.
				if out, err = artifactdir.IndexFor(artifactPath); err != nil {
					return err
				}
			}
			// A soft ceiling for the collector unless the caller set one. A full build has
			// about 3 GB live; without a ceiling the heap runs to twice that before each
			// collection and the process to 7 GB, with one it peaks at 4 GB and finishes
			// sooner (measured on 4 cores: 7.0 GB in 134 s, 4.0 GB in 95 s). It is a soft
			// limit: a corpus that outgrows it makes the collector busier, not the build fail.
			if os.Getenv("GOMEMLIMIT") == "" {
				debug.SetMemoryLimit(4 << 30)
			}
			if idx, err = indexFromArtifact(cmd.Context(), artifactPath, opts, perStratum); err != nil {
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
				"documents", len(idx.Docs),
				"units", len(idx.Units),
				"source", idx.Source,
				"terms", len(idx.Terms),
				"bytes", st.Size(),
				"folded", folded,
				"b", idx.B,
				"elapsed", buildTime.Round(time.Millisecond),
				"peak_heap_mb", float64(ms.HeapAlloc)/1e6,
				"heap_in_use_mb", float64(ms.HeapInuse)/1e6,
				"rss", progress.PeakRSS(),
			)
			fmt.Fprintf(gate.LogWriter(), "%s: %d documents, %d provisions, %d terms, %.1f MB, %s\n",
				out, len(idx.Docs), len(idx.Units), len(idx.Terms), float64(st.Size())/1e6, buildTime.Round(time.Millisecond))
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "where to write the index (default: beside the artifact, named for its stamp, where a server looks for it)")
	cmd.Flags().IntVar(&perStratum, "sample", 0, "index only this many documents per kind/decade stratum (0 indexes everything; a non-zero value is for measuring, and needs --out)")
	cmd.Flags().StringVar(&artifactPath, "artifact", "", "the compiled artifact to index (default: the current one, see <data-dir>/"+artifactdir.PointerFile+")")
	cmd.Flags().BoolVar(&folded, "folded", false, "also index a stream with Danish inflectional endings folded; measured as adding no recall on real queries while costing 31% of the index size, so it is off (see internal/search/testdata/index-measurement-20260917.txt)")
	cmd.Flags().Float64Var(&b, "bm25-b", -1, "override BM25 length normalisation; the measured default for this corpus is 0 (sweep in internal/search/testdata/)")
	return cmd
}

func searchCmd(resolve configFunc) *cobra.Command {
	var index, kind, source, status, typeCode string
	var since, until, limit int
	var noSnippet, noNear bool

	cmd := &cobra.Command{
		Use:   "search <words>",
		Short: "Full-text search the mirror: hits are provisions, not documents",
		Example: `  lovkilde search "miljøbeskyttelse pligt"
  lovkilde search "lov nr. 1399" --kind lta --since 2015
  lovkilde search "ubetinget råderet" --source xml   # only the register's own text
  lovkilde search "pligt AND virksomhed -lejlighed"  # require, require-not; plain words are OR`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolve()
			if err != nil {
				return err
			}
			artifactPath, artErr := artifactdir.Current(cfg.DataDir)
			if index == "" {
				if artErr != nil {
					return artErr
				}
				if index, err = artifactdir.IndexFor(artifactPath); err != nil {
					return err
				}
			}
			idx, err := search.Load(index)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("%s: %w — build it first with `lovkilde index build`", index, err)
				}
				return err
			}
			// The index cannot see how far apart two words sit, so the page it ranks best
			// is not necessarily the page worth reading. Over-fetching gives the proximity
			// grade something to reorder, and the reads it needs are the same ones the
			// snippets were going to do.
			query := strings.Join(args, " ")
			forbidden := search.ExcludedWords(query)
			fetch := limit
			// Extra candidates whenever something after ranking can remove hits: grading
			// re-ranks, and a forbidden word can make a passage unshowable.
			if !noNear || len(forbidden) > 0 {
				fetch = limit * 2
			}
			hits := idx.Search(search.Query{
				Text:     query,
				Kind:     kind,
				MinYear:  since,
				MaxYear:  until,
				Source:   source,
				Status:   status,
				TypeCode: typeCode,
				Limit:    fetch,
			})
			// Passages are read from the artifact the index was cut from.
			if artErr != nil {
				return artErr
			}
			a, err := compiled.OpenArtifact(artifactPath)
			if err != nil {
				return err
			}
			defer a.Close()
			texts := search.NewUnitTexts(artifactDocuments{a})
			if !noNear {
				hits = idx.GradeNear(hits, query, func(h *search.Hit) string {
					return texts.Unit(*h.Doc, h.Addr)
				})
			}
			if len(hits) > limit {
				hits = hits[:limit]
			}
			w := gate.LogWriter()
			if len(hits) == 0 {
				fmt.Fprintf(w, "no provision matches %q in %s (%d documents indexed)\n",
					strings.Join(args, " "), index, len(idx.Docs))
				return nil
			}
			for i, h := range hits {
				extra := ""
				if h.Gap >= 0 {
					extra = fmt.Sprintf(" %d apart", h.Gap)
				}
				fmt.Fprintf(w, "%2d. %s — %s\n     %s [%s] score %.2f matched %d/%d: %s%s\n",
					i+1, h.Doc.Title, h.Addr, h.Doc.ELI, h.Doc.Source, h.Score,
					h.Covered, h.Requested, strings.Join(h.Matched, ","), extra)
				if noSnippet {
					continue
				}
				// The snippet re-reads the document, sharing one parse per document with the
				// proximity grade above. A hit whose snippet cannot be rendered still prints
				// its address rather than disappearing.
				unitText := texts.Unit(*h.Doc, h.Addr)
				snippet := search.SnippetFromWithout(unitText, h.Matched, forbidden, 0)
				switch {
				case snippet != "":
					fmt.Fprintf(w, "     %s\n", strings.ReplaceAll(snippet, "\n", " "))
				case unitText != "" && len(forbidden) > 0:
					// Said out loud rather than as a missing snippet: the query forbade a word,
					// and the passage that would prove this hit contains it.
					fmt.Fprintf(w, "     (passage would show %v, which the query forbids: %s)\n",
						forbidden, h.Doc.Accession)
				default:
					fmt.Fprintf(w, "     (no snippet: %s)\n", h.Doc.Accession)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&index, "index", "", "index file (default: the current artifact's)")
	cmd.Flags().StringVar(&kind, "kind", "", "limit to one medium: lta, ltb, ltc, mt, retsinfo, ft, fob, accn")
	cmd.Flags().StringVar(&source, "source", "", "limit to one text source: xml or html")
	cmd.Flags().StringVar(&status, "status", "", "only what the register labels current (valid/gældende), repealed or superseded (historic/historisk), or unlabelled (unknown/ukendt); the register's own claim, not a check that nothing newer has changed the text")
	cmd.Flags().StringVar(&typeCode, "type-code", "", "only documents whose publisher type code contains this, e.g. LOKDOK or KEN# (the codes are the register's own and are not translated)")
	cmd.Flags().IntVar(&since, "since", 0, "only documents from this year")
	cmd.Flags().IntVar(&until, "until", 0, "only documents up to this year")
	cmd.Flags().IntVar(&limit, "limit", 10, "maximum hits")
	cmd.Flags().BoolVar(&noSnippet, "no-snippet", false, "skip rendering passages (each one re-reads a document from the artifact)")
	cmd.Flags().BoolVar(&noNear, "no-near", false, "rank from the index alone: skip the text reads that measure how far apart the words sit")
	return cmd
}

// indexFromArtifact builds the index from the compiled artifact alone, streaming its documents.
// With perStratum > 0 it indexes a stratum-balanced sample instead (search.Strata), for measuring.
func indexFromArtifact(ctx context.Context, path string, opts search.Options, perStratum int) (*search.Index, error) {
	a, err := compiled.OpenArtifact(path)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	if err := a.CheckModel(); err != nil {
		return nil, err
	}
	accs := a.Documents()
	if perStratum > 0 {
		if accs, err = sampleArtifact(a, accs, perStratum); err != nil {
			return nil, err
		}
	}
	slog.Info("indexing the artifact", "path", path, "documents", len(accs), "built", a.Header.Created.Format(time.RFC3339))
	var unreadable, unshelved atomic.Int64
	idx, err := search.BuildStream(ctx, len(accs), func(i int) (search.DocMeta, []search.Unit, bool) {
		doc, err := a.Document(accs[i])
		if err != nil {
			unreadable.Add(1)
			slog.Warn("document unreadable in the artifact", "accession", accs[i], "err", err)
			return search.DocMeta{}, nil, false
		}
		meta := search.MetaOf(doc)
		// The artifact's key is the accession the mirror filed the document under.
		meta.Accession = accs[i]
		if meta.Kind == "" {
			unshelved.Add(1)
		}
		return meta, search.DocUnits(i, meta, doc), true
	}, opts)
	if err != nil {
		return nil, err
	}
	if n := unshelved.Load(); n == int64(len(idx.Docs)) && n > 0 {
		// An artifact from before its documents carried their shelf would give an index
		// without kinds, years or ELI paths: every filter and every citation would be empty.
		return nil, fmt.Errorf("%s records no shelf (lta, ft, …) for any document: it predates the artifact's identity fields, rebuild it with `lovkilde-update artifact build`", path)
	}
	if n := unreadable.Load(); n > 0 {
		return nil, fmt.Errorf("%d documents could not be read from %s", n, path)
	}
	idx.Source = search.SourceFor(a.Identity())
	return idx, nil
}

// sampleArtifact picks up to perStratum of the artifact's documents per kind and decade, by the
// catalogue's entries.
func sampleArtifact(a *compiled.Artifact, accs []string, perStratum int) ([]string, error) {
	raw, err := a.Catalogue()
	if err != nil {
		return nil, err
	}
	var recs []chain.CatalogueRecord
	if err := json.Unmarshal(raw, &recs); err != nil {
		return nil, fmt.Errorf("%s: catalogue: %w", a.Path(), err)
	}
	entries := make(map[string]chain.Entry, len(recs))
	for _, r := range recs {
		entries[r.Entry.Accession] = r.Entry
	}
	var out []string
	for _, i := range search.Strata(len(accs), func(i int) string {
		e := entries[accs[i]]
		return search.StratumOf(e.Kind, e.Year)
	}, perStratum) {
		out = append(out, accs[i])
	}
	return out, nil
}

// artifactDocuments reads a hit's document from the compiled artifact.
type artifactDocuments struct{ a *compiled.Artifact }

func (d artifactDocuments) Document(meta search.DocMeta) (*lexdoc.Document, error) {
	return d.a.Document(meta.Accession)
}
