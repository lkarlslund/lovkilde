package main

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/lkarlslund/retsinformation/internal/artifactdir"
	"github.com/lkarlslund/retsinformation/internal/builder"
	"github.com/lkarlslund/retsinformation/internal/config"
)

// configFunc resolves the validated flags without opening any storage.
type configFunc func() (config.Config, error)

func artifactCmd(resolve configFunc) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifact",
		Short: "Compile the mirror's document model into one seekable file",
	}
	cmd.AddCommand(artifactBuildCmd(resolve))
	return cmd
}

// Nothing here opens the state store: the artifact is derived data, rebuilt from the files at zero
// network cost, and the store is locked to one process, so a command that opened it would block the
// nightly crawl.
func artifactBuildCmd(resolve configFunc) *cobra.Command {
	var out, mode, from string

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Write the compiled document artifact from the mirror on disk",
		Long: `Compile every document in the mirror into the artifact the server reads.

  lovkilde-update artifact build

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
internal/compiled/testdata/artifact-20260918.txt).

The artifact records the last collection pass (data/sweep.stamp), which is what a
server quotes as how current its text is.

Modes (--mode):
  full    compile every document in the archive (the default)
  patch   continue an earlier artifact (--from, default the current one): compile only the
          documents whose XML or body sidecar on disk differs from what it was compiled
          from, and copy every other document's frame from it byte for byte. Documents
          whose XML is not on disk are carried, so a machine holding only the latest pass's
          files builds the whole artifact; on a machine holding every file the result is
          what a full build makes. Refused when the earlier artifact holds another
          document model or dictionary: build in full then. When no document changed,
          nothing is written and the earlier artifact stays current.

A server downloading the next artifact fetches only the chunks it does not share with the
one it has (Xet storage), so an artifact is always written whole.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolve()
			if err != nil {
				return err
			}
			m, err := builder.ParseMode(mode)
			if err != nil {
				return err
			}
			_, err = builder.Artifact(cmd.Context(), builder.Options{
				Mode:             m,
				From:             from,
				XMLDir:           cfg.XMLDir(),
				DataDir:          cfg.DataDir,
				Out:              out,
				CollectionRecord: cfg.SweepStampPath(),
				Report:           gate.LogWriter(),
			})
			if errors.Is(err, builder.ErrUnchanged) {
				cmd.Println("no document changed since the earlier artifact: nothing written")
				return nil
			}
			return err
		},
	}
	cmd.Flags().StringVar(&mode, "mode", string(builder.Full), "full or patch (see above)")
	cmd.Flags().StringVar(&from, "from", "", "the earlier artifact a patch build continues (default: the current one)")
	cmd.Flags().StringVar(&out, "out", "", "write the artifact to this file instead of installing it as the current one in <data-dir>/"+artifactdir.Subdir)
	return cmd
}
