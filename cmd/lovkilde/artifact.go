// The artifact commands a server has: whether it can serve the artifact on disk, and downloading
// the published one. Building one is lovkilde-update's.
package main

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"github.com/lkarlslund/retsinformation/internal/artifactdir"
	"github.com/lkarlslund/retsinformation/internal/compiled"
	"github.com/lkarlslund/retsinformation/internal/release"
)

func artifactCmd(resolve configFunc) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifact",
		Short: "The compiled artifact a server reads",
	}
	cmd.AddCommand(artifactCheckCmd(resolve), artifactDownloadCmd(resolve))
	return cmd
}

// artifactCheckCmd answers the question an operator facing a refused server is actually
// asking: can this binary serve the artifact on disk, and if not, which side moved.
//
// It deliberately reuses the reader's own comparison rather than restating it, so the
// answer here cannot disagree with the answer the server gives at startup — a check
// command with its own logic would be a second source of truth about a pin whose whole
// job is to be one.
func artifactCheckCmd(resolve configFunc) *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Say whether this build can serve the artifact on disk",
		Long: `Read the artifact's header and compare its document model version with the one this
binary reads. Any build reading the same model can serve the file; an artifact written before
model versions were recorded is compared by build stamp instead.

Exits non-zero with the reason the server would give, naming which side is out of date. That makes it the question a deploy can ask before restarting: the unit fails
at startup rather than serving documents it cannot interpret, and this answers the same
question in milliseconds instead of a failed start.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolve()
			if err != nil {
				return err
			}
			path, err := artifactdir.Current(cfg.DataDir)
			if err != nil {
				return err
			}
			a, err := compiled.OpenArtifact(path)
			if err != nil {
				return err
			}
			defer a.Close()
			current, _ := compiled.CurrentBuild()
			wrote := compiled.BuildStamp{Revision: a.Header.Build, Modified: a.Header.Modified, Time: a.Header.Created}
			model := "no model version recorded"
			if a.Header.Model != 0 {
				model = fmt.Sprintf("document model %d", a.Header.Model)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %d documents, written %s by %s (%s, zstd dictionary %#x, %d KiB); this binary is %s and reads document model %d\n",
				path, len(a.Documents()), a.Header.Created.UTC().Format(time.RFC3339), wrote, model, a.Header.Dictionary.ID, a.Header.Dictionary.Bytes>>10, current, compiled.ModelVersion)
			return a.CheckModel()
		},
	}
}

// artifactDownloadCmd installs the published artifact now, the thing serve does by itself when
// there is none and every --auto-update after that.
func artifactDownloadCmd(resolve configFunc) *cobra.Command {
	var from string
	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download the published artifact and make it current",
		Long: `Read the release manifest in the release repository (a directory, or a Hugging Face dataset
repository), download the artifact it names unless it is already current, check its size, SHA-256 and document model, and make it
current. A running server moves to it by itself.

An artifact of another document model than this binary reads is not downloaded: the answer to
that is a newer lovkilde.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolve()
			if err != nil {
				return err
			}
			repo, err := release.Open(from)
			if err != nil {
				return err
			}
			res, err := release.FetchArtifact(cmd.Context(), repo, cfg.DataDir)
			if err != nil {
				return err
			}
			_ = release.MarkChecked(cfg.DataDir)
			if !res.Installed {
				fmt.Fprintf(cmd.OutOrStdout(), "%s is already current\n", res.Path)
				return nil
			}
			if removed, err := artifactdir.Prune(cfg.DataDir); err == nil && len(removed) > 0 {
				slog.Info("pruned artifacts no longer kept", "removed", removed)
			}
			logTransfer(res.Transfer)
			fmt.Fprintf(cmd.OutOrStdout(), "installed %s: downloaded %d bytes, reused %d from the artifacts on disk\n", res.Path, res.Downloaded, res.Reused)
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "release", release.DefaultSource,
		"release repository to download from: a directory, or hf://owner/name[@revision]")
	return cmd
}
