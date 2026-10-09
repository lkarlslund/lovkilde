// The release commands: publishing the artifact and the collection state to a release
// repository, and restoring the state from one into an empty data directory.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/lkarlslund/retsinformation/internal/artifactdir"
	"github.com/lkarlslund/retsinformation/internal/release"
	"github.com/lkarlslund/retsinformation/internal/store"
)

const releaseFlagHelp = "release repository: a directory, or hf://owner/name[@revision]"

func publishCmd(resolve configFunc) *cobra.Command {
	var to string
	var stateOnly, withXML bool
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Publish the current artifact and the collection state to a release repository",
		Long: `Publish what a server downloads and what the next collection continues from: the current
artifact, the collection state (state/meta.json, inventory, bodies and feed changes; the
journal stays local), and a manifest naming every file with its size and SHA-256, committed
last. The artifact is always whole: Xet storage keeps only the chunks a new one does not share
with the last, and a server downloads only those.

--with-xml also publishes the XML archive, one pack per kind and year (xml/lta/1995.tar.zst):
what a machine needs to build an artifact in full without crawling. Only the packs whose
files changed since the last release are packed and sent.

  retsin-update publish --release /srv/retsin-releases
  retsin-update publish --release hf://owner/name

A directory works completely, and is how publishing is tried out and how a snapshot is kept to
start from again (see "restore"); the artifact is hard-linked into it where it can be, so a
snapshot costs the state's size, not the artifact's. Publishing to Hugging Face is one commit
made with the hf command (pip install huggingface_hub), which needs HF_TOKEN with write access.

With --with-xml on a machine holding only part of the archive (a runner holds what the latest
pass fetched), a changed shard is completed from its published pack before it is packed again,
and a shard not on disk stays published as it is.

The state is held still while it is published: this takes the state's lock, so it does not
run beside a collection pass.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolve()
			if err != nil {
				return err
			}
			repo, err := release.Open(to)
			if err != nil {
				return err
			}
			st, err := store.Open(cfg.StoreDir())
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.Save(); err != nil {
				return err
			}
			rel := release.Release{StateDir: cfg.StoreDir(), StateFiles: store.Published}
			if withXML {
				rel.XMLDir = cfg.XMLDir()
			}
			if !stateOnly {
				if rel.Artifact, err = artifactdir.Current(cfg.DataDir); err != nil {
					return fmt.Errorf("%w (publish the state alone with --state-only)", err)
				}
			}
			m, err := release.Publish(cmd.Context(), repo, rel)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "published to %s: artifact %s, %d state files, %d XML packs\n", repo, orNone(m.File), len(m.State), len(m.XML))
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "release", "", releaseFlagHelp)
	cmd.Flags().BoolVar(&stateOnly, "state-only", false, "publish the state and keep the artifact already published")
	cmd.Flags().BoolVar(&withXML, "with-xml", false, "also publish the XML archive, as packs per kind and year")
	_ = cmd.MarkFlagRequired("release")
	return cmd
}

func restoreCmd(resolve configFunc) *cobra.Command {
	var from string
	var withArtifact, withXML, xmlOnly bool
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore the collection state from a release repository into an empty data directory",
		Long: `Download the published collection state into this data directory, so a collection pass
continues from it: a fresh machine needs this and nothing else to run "update". Every file is
checked against the manifest, and none is put in place until all of them arrived whole.

  retsin-update --data-dir /tmp/try restore --release /srv/retsin-releases
  retsin-update --data-dir /tmp/try update

The data directory must not hold state already. --with-artifact also installs the published
artifact: what "artifact build --mode patch" continues from, so the XML archive is not needed
and an incremental pass fetches only what changed. --with-xml also unpacks the published XML archive, for a full build; a file
already on disk is kept (it was fetched since), and a shard already on disk as published is
not downloaded, so an interrupted restore continues. --xml-only unpacks the archive and
nothing else, beside state that is already there: what a machine that ran "update" needs
before a full build.`,
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
			if xmlOnly {
				_, n, err := release.RestoreXML(cmd.Context(), repo, cfg.XMLDir())
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "unpacked %d XML packs into %s\n", n, cfg.XMLDir())
				return nil
			}
			if _, err := os.Stat(filepath.Join(cfg.StoreDir(), store.MetaFile)); err == nil {
				return fmt.Errorf("%s already holds collection state; restore into an empty data directory", cfg.StoreDir())
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			m, err := release.RestoreState(cmd.Context(), repo, cfg.StoreDir())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "restored %d state files published %s from %s into %s\n",
				len(m.State), m.Published.Format("2006-01-02 15:04:05Z"), repo, cfg.StoreDir())
			if withXML {
				_, n, err := release.RestoreXML(cmd.Context(), repo, cfg.XMLDir())
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "unpacked %d XML packs into %s\n", n, cfg.XMLDir())
			}
			if withArtifact {
				res, err := release.FetchArtifact(cmd.Context(), repo, cfg.DataDir)
				if err != nil {
					return err
				}
				if res.XetErr != nil {
					slog.Warn("downloaded the artifact whole: Xet storage failed", "err", res.XetErr)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "installed %s: downloaded %d bytes, reused %d from the artifacts on disk\n", res.Path, res.Downloaded, res.Reused)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "release", "", releaseFlagHelp)
	cmd.Flags().BoolVar(&withArtifact, "with-artifact", false, "also install the published artifact")
	cmd.Flags().BoolVar(&withXML, "with-xml", false, "also unpack the published XML archive")
	cmd.Flags().BoolVar(&xmlOnly, "xml-only", false, "unpack the published XML archive only, beside existing state")
	cmd.MarkFlagsMutuallyExclusive("xml-only", "with-xml")
	cmd.MarkFlagsMutuallyExclusive("xml-only", "with-artifact")
	_ = cmd.MarkFlagRequired("release")
	return cmd
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
