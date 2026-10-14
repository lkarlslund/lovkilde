// The program commands: whether a newer build of lovkilde is published, and downloading it. A
// downloaded build is kept in the data directory, and lovkilde started from anywhere runs the
// newest one there, so a double-clicked old copy still becomes the new version.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/lkarlslund/lovkilde/internal/compiled"
	"github.com/lkarlslund/lovkilde/internal/config"
	"github.com/lkarlslund/lovkilde/internal/softwareupdate"
)

// programDir is where downloaded builds are kept.
func programDir(dataDir string) string { return filepath.Join(dataDir, "program") }

// switchedEnv is set for a program started by switchToNewer, which then does not look again.
const switchedEnv = "LOVKILDE_SWITCHED"

// switchToNewer runs the newest downloaded build in place of this one when it is newer, and does
// not return when it does. A build from uncommitted files, or one that does not know its commit,
// is a developer's and stays itself.
func switchToNewer(dataDir string) {
	current, ok := compiled.CurrentBuild()
	if !ok || current.Modified || os.Getenv(switchedEnv) != "" {
		return
	}
	path, stamp, ok := softwareupdate.Newest(programDir(dataDir))
	if !ok || !stamp.Time.After(current.Time) {
		return
	}
	log := gate.LogWriter()
	fmt.Fprintf(log, "this is lovkilde %s from %s; a newer version, %s from %s, is in %s. Starting that instead.\n",
		current, current.Time.Format("2006-01-02"), stamp.Revision, stamp.Time.Format("2006-01-02"), path)
	os.Setenv(switchedEnv, current.String())
	if err := runInstead(path); err != nil {
		fmt.Fprintf(log, "could not start %s (%v); going on with this version\n", path, err)
		os.Unsetenv(switchedEnv)
	}
}

// sayStartedBy tells, in the program switchToNewer started, that this is the newer version.
func sayStartedBy() {
	if from := os.Getenv(switchedEnv); from != "" {
		current, _ := compiled.CurrentBuild()
		fmt.Fprintf(gate.LogWriter(), "running lovkilde %s from %s, started by the older version %s\n",
			current, current.Time.Format("2006-01-02"), from)
	}
}

func programCmd(resolve configFunc) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "program",
		Short: "Newer builds of this program",
	}
	cmd.AddCommand(programCheckCmd(resolve), programDownloadCmd(resolve))
	return cmd
}

// publishedChecker asks about the published builds on behalf of the running one.
func publishedChecker() (*softwareupdate.Checker, error) {
	build, ok := compiled.CurrentBuild()
	if !ok {
		return nil, fmt.Errorf("this build does not know which commit it was built from, so it cannot be compared with the published one")
	}
	return &softwareupdate.Checker{Repo: softwareRepo, Tag: softwareTag, Revision: build.Revision, UserAgent: config.DefaultUserAgent}, nil
}

func programCheckCmd(resolve configFunc) *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Say whether a newer build of this program is published",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolve()
			if err != nil {
				return err
			}
			current, _ := compiled.CurrentBuild()
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "this build is %s\n", current)
			if path, stamp, ok := softwareupdate.Newest(programDir(cfg.DataDir)); ok {
				fmt.Fprintf(out, "downloaded: %s, from %s\n", path, stamp.Time.Format("2006-01-02"))
			}
			sw, err := publishedChecker()
			if err != nil {
				return err
			}
			n, err := sw.Behind(cmd.Context())
			if err != nil {
				return err
			}
			if n == 0 {
				fmt.Fprintln(out, "the published build is not newer")
				return nil
			}
			fmt.Fprintf(out, "the published build is %d commits newer: lovkilde program download\n", n)
			return nil
		},
	}
}

func programDownloadCmd(resolve configFunc) *cobra.Command {
	return &cobra.Command{
		Use:   "download",
		Short: "Download the published build; lovkilde runs it from the next start",
		Long: `Download the published build for this platform, check it against the release's SHA256SUMS
and keep it in <data-dir>/program. From then on, lovkilde started from anywhere runs the newest
build kept there, if it is newer than itself. A running server goes on as it is until it is
restarted.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolve()
			if err != nil {
				return err
			}
			sw, err := publishedChecker()
			if err != nil {
				return err
			}
			if n, err := sw.Behind(cmd.Context()); err == nil && n == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "the published build is not newer than this one")
				return nil
			}
			dir := programDir(cfg.DataDir)
			path, stamp, err := sw.Download(cmd.Context(), dir)
			if err != nil {
				return err
			}
			softwareupdate.Prune(dir, path)
			fmt.Fprintf(cmd.OutOrStdout(), "downloaded %s (%s); it runs from the next start\n", path, stamp.Time.Format("2006-01-02"))
			return nil
		},
	}
}
