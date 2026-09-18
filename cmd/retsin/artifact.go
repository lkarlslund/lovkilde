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
	"time"

	"github.com/spf13/cobra"

	"github.com/lkarlslund/retsinformation/internal/compiled"
	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

// artifactDefaultName is where the artifact goes under --data-dir.
const artifactDefaultName = "artifact.bin"

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

			seen := map[string]bool{}
			var noAccession, duplicates, unreadable int
			for _, e := range a.Entries() {
				if e.Accession == "" {
					noAccession++
					continue
				}
				if seen[e.Accession] {
					duplicates++
					continue
				}
				doc, _, ok := a.Open(e.Accession)
				if !ok {
					unreadable++
					continue
				}
				// The accession comes from the entry rather than the document: the entry is where
				// the mirror found it, and a file whose own metadata disagrees about its key is
				// filed under the address the archive gave it.
				if err := wr.Add(e.Accession, doc.Model()); err != nil {
					f.Close()
					return fmt.Errorf("%s: %w", e.Accession, err)
				}
				seen[e.Accession] = true
			}
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
			slog.Info("artifact built",
				"path", out,
				"documents", wr.Documents(),
				"bytes", st.Size(),
				"entries without an accession", noAccession,
				"duplicate accessions", duplicates,
				"unreadable", unreadable,
				"elapsed", time.Since(start).Round(time.Millisecond),
				"heap_in_use_mb", float64(ms.HeapInuse)/1e6,
				"rss", peakRSS(),
			)
			// The coverage claim is the number a reader of an answer cannot see for themselves, so
			// it is printed with what was left out rather than as a bare count.
			fmt.Fprintf(gate.LogWriter(), "%s: %d documents, %.1f MB, built %s, %s; left out: %d without an accession, %d duplicate, %d unreadable\n",
				out, wr.Documents(), float64(st.Size())/1e6,
				opened.Header.Created.Format(time.RFC3339), time.Since(start).Round(time.Millisecond),
				noAccession, duplicates, unreadable)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "where to write the artifact (default <data-dir>/"+artifactDefaultName+")")
	return cmd
}
