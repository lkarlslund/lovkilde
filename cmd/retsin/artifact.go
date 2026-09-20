// The artifact commands: compiling the mirror's own document model into one seekable file that
// serving can read without parsing the publisher's XML.
//
// Nothing here opens the Badger state store: the artifact is derived data, rebuilt from the files
// at zero network cost, and Badger locks exclusively per process, so a command that opened it would
// block the nightly crawl.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lkarlslund/retsinformation/internal/archive"
	"github.com/lkarlslund/retsinformation/internal/compiled"
	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

// artifactDefaultName is where the artifact goes under --data-dir.
const artifactDefaultName = "artifact.bin"

// sizeHistogram renders the compiled document sizes as one log field, e.g.
// "<8 KiB:152310 <64 KiB:31000 <1 MiB:6012 <8 MiB:990 <64 MiB:13 >=64 MiB:4". The last band is
// open at the top because it is the one that matters: it holds the documents the old bound refused.
func sizeHistogram(bands []int, counts []int64) string {
	parts := make([]string, 0, len(counts))
	for i, c := range counts {
		if i < len(bands) {
			parts = append(parts, fmt.Sprintf("<%s:%d", archive.SizeHint(int64(bands[i])), c))
			continue
		}
		parts = append(parts, ">="+archive.SizeHint(int64(bands[len(bands)-1]))+":"+strconv.FormatInt(c, 10))
	}
	return strings.Join(parts, " ")
}

func artifactCmd(resolve configFunc) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifact",
		Short: "Compile the mirror's document model into one seekable file",
	}
	cmd.AddCommand(artifactBuildCmd(resolve))
	return cmd
}

func artifactBuildCmd(resolve configFunc) *cobra.Command {
	var out string

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Write the compiled document artifact from the mirror on disk",
		Long: `Compile every document in the mirror into the artifact the server reads.

  retsin artifact build

No network traffic: the artifact is derived data, rebuilt from the files. Each document
becomes its own independently decodable zstd frame in the Zstandard Seekable Format, so a
lookup seeks to one frame and decodes one document, and the file is still a plain zstd
stream that standard tools read.

What is stored is the mirror's own model (internal/lexdoc): provisions, subsections and
conditions as the model says they are, with the document's own printed numbers, notes and
entry-into-force provisions kept in the model's vocabulary. The publisher's element names
and attribute maps are not in it, so nothing compiled or served can leak the schema.

The document table and the frame table are cross-checked against each other when the file
is opened, and a document the artifact does not hold is an error rather than an empty
answer. The file is written to a temporary name and renamed, so a reader never sees a
half-built artifact.

Measured on 1,000 documents: 28.5 MB of model JSON, 5.6 MB on disk, 122 microseconds to
read and decode a document against 691 to parse its XML (see
internal/compiled/testdata/artifact-20260918.txt).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolve()
			if err != nil {
				return err
			}
			if out == "" {
				out = cfg.DataDir + "/" + artifactDefaultName
			}
			start := time.Now()
			slog.Info("scanning the mirror", "dir", cfg.XMLDir())
			a, err := lexdania.Scan(cmd.Context(), cfg.XMLDir())
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			tmp := out + ".writing"
			f, err := os.Create(tmp)
			if err != nil {
				return err
			}
			wr, err := compiled.NewWriter(f, cfg.XMLDir())
			if err != nil {
				f.Close()
				return err
			}

			// Which entries make it into the file is decided here, in entry order: the
			// mirror can hold one accession twice, and which copy wins has to be the same
			// answer every build, so the filter cannot run on workers.
			var want []string
			seen := map[string]bool{}
			var noAccession, duplicates int
			for _, e := range a.Entries() {
				if e.Accession == "" {
					noAccession++
					continue
				}
				if seen[e.Accession] {
					duplicates++
					continue
				}
				want = append(want, e.Accession)
				seen[e.Accession] = true
			}

			// Open, model, marshal and compress on workers; write here, in order.
			//
			// A build spends its time in exactly those four steps — 191,429 documents
			// parsed, their models marshalled, one zstd frame per document — and none of
			// them touch the writer. The file itself is a byte stream whose offsets are the
			// seek table, so the writing stays single-threaded and in mirror order. Chunks
			// hand over on a channel, which means the file is being written while the next
			// chunk compiles.
			ctx := cmd.Context()
			const chunk = 512
			// The phases are timed separately because they answer different questions:
			// compile is how long the corpus takes to parse, model, marshal and compress;
			// write is how long the file takes to leave the process; loop is the wall time
			// of the two running against each other. Compile and write overlap, so the
			// first two do not add up to the third, and the gap between them is what the
			// parallelism bought.
			var compileNanos, writeNanos int64
			tLoop := time.Now()
			type ready struct {
				frames   []compiled.CompiledFrame
				missing  int
				failed   string
				firstErr error
			}
			batches := make(chan ready, 1)
			go func() {
				defer close(batches)
				for lo := 0; lo < len(want); lo += chunk {
					hi := min(lo+chunk, len(want))
					batch := ready{frames: make([]compiled.CompiledFrame, hi-lo)}
					errs := make([]error, hi-lo)
					opened := make([]bool, hi-lo)
					tCompile := time.Now()
					lexdania.Parallel(ctx, hi-lo, func(i int) {
						acc := want[lo+i]
						doc, _, ok := a.Open(acc)
						if !ok {
							return
						}
						opened[i] = true
						// The accession comes from the entry rather than the document: the entry is where
						// the mirror found it, and a file whose own metadata disagrees about its key is
						// filed under the address the archive gave it.
						batch.frames[i], errs[i] = wr.Compile(acc, doc.Model())
					})
					compileNanos += int64(time.Since(tCompile))
					for i := range opened {
						if !opened[i] {
							batch.missing++
						}
					}
					// The first failing document by file order, not by whichever worker
					// noticed first: the error names one document and the reader should get
					// the same name from the same broken mirror.
					for i := range errs {
						if errs[i] != nil {
							batch.firstErr, batch.failed = errs[i], want[lo+i]
							break
						}
					}
					batches <- batch
					if batch.firstErr != nil {
						return
					}
				}
			}()

			var unreadable int
			for batch := range batches {
				if batch.firstErr != nil {
					f.Close()
					// The half-written file is 400 MB of nothing on a build that died this way once
					// already; leaving it is a second problem the same failure has to cause.
					os.Remove(tmp)
					return fmt.Errorf("%s: %w", batch.failed, batch.firstErr)
				}
				unreadable += batch.missing
				tWrite := time.Now()
				for _, fr := range batch.frames {
					if err := wr.WriteFrame(fr); err != nil {
						f.Close()
						os.Remove(tmp)
						return fmt.Errorf("%s: %w", fr.Accession, err)
					}
				}
				writeNanos += int64(time.Since(tWrite))
			}
			loop := time.Since(tLoop)
			tTables := time.Now()
			if err := wr.Close(); err != nil {
				f.Close()
				os.Remove(tmp)
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			if err := os.Rename(tmp, out); err != nil {
				return err
			}
			tables := time.Since(tTables)
			st, err := os.Stat(out)
			if err != nil {
				return err
			}

			opened, err := compiled.OpenArtifact(out)
			if err != nil {
				return err
			}
			defer opened.Close()

			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			// The size distribution is printed because the bound on a document's size is a claim
			// about this corpus, and a claim of that kind was previously asserted at 8 MiB without
			// ever having been counted — which is what made a whole-mirror build impossible.
			bands, counts, largest, rawTotal := wr.SizeDistribution()
			slog.Info("artifact built",
				"path", out,
				"documents", wr.Documents(),
				"bytes", st.Size(),
				"model_json", archive.SizeHint(int64(rawTotal)),
				"largest_document_json", archive.SizeHint(int64(largest)),
				"document_sizes", sizeHistogram(bands, counts),
				"entries without an accession", noAccession,
				"duplicate accessions", duplicates,
				"unreadable", unreadable,
				"elapsed", time.Since(start).Round(time.Millisecond),
				"loop", loop.Round(time.Millisecond),
				"compile", time.Duration(compileNanos).Round(time.Millisecond),
				"write", time.Duration(writeNanos).Round(time.Millisecond),
				"tables", tables.Round(time.Millisecond),
				"heap_in_use_mb", float64(ms.HeapInuse)/1e6,
				"rss", peakRSS(),
			)
			// The coverage claim is the number a reader of an answer cannot see for themselves, so
			// it is printed with what was left out rather than as a bare count.
			fmt.Fprintf(gate.LogWriter(), "%s: %d documents, %.1f MB, built %s by %s, %s; model JSON %s, largest document %s (%s); left out: %d without an accession, %d duplicate, %d unreadable\n",
				out, wr.Documents(), float64(st.Size())/1e6,
				opened.Header.Created.Format(time.RFC3339), stampedAs(opened.Header),
				time.Since(start).Round(time.Millisecond),
				archive.SizeHint(int64(rawTotal)), archive.SizeHint(int64(largest)), sizeHistogram(bands, counts),
				noAccession, duplicates, unreadable)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "where to write the artifact (default <data-dir>/"+artifactDefaultName+")")
	return cmd
}

// stampedAs names the build that wrote an artifact the way the refusal names it, so the line an
// operator reads after a build matches the line they read after a server refuses the file.
func stampedAs(h compiled.ArtifactHeader) string {
	stamp := h.Build
	if len(stamp) > 12 {
		stamp = stamp[:12]
	}
	if stamp == "" {
		return "an unstamped build"
	}
	if h.Modified {
		return stamp + "-dirty"
	}
	return stamp
}
