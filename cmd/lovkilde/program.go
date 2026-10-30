// The program commands: whether a newer build of lovkilde is published, and downloading it. A
// downloaded build is kept in the data directory, and lovkilde started from anywhere runs the
// newest one there, so a double-clicked old copy still becomes the new version. A running server
// moves to a newer build as soon as one is kept there (watchProgram).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lkarlslund/lovkilde/internal/compiled"
	"github.com/lkarlslund/lovkilde/internal/config"
	"github.com/lkarlslund/lovkilde/internal/softwareupdate"
)

// switchedEnv is set for a program started by switchToNewer or restartAs, which then does not
// look again; restartedEnv only by restartAs, for a server that should not open the browser again.
const (
	switchedEnv  = "LOVKILDE_SWITCHED"
	restartedEnv = "LOVKILDE_RESTARTED"
)

// inService is set in a process the Windows service manager started (runService).
var inService bool

// errServiceRestart ends a Windows service that has been pointed at a newer build: it exits with an
// error, which the service manager answers by starting it again, as the newer build.
var errServiceRestart = errors.New("restarting the service as the newer build")

// switchToNewer runs the newest downloaded build in place of this one when it is newer, and does
// not return when it does. A build from uncommitted files, or one that does not know its commit,
// is a developer's and stays itself. A Windows service is pointed at the newer build instead and
// gets errServiceRestart, to end with.
func switchToNewer(dataDir string) error {
	if os.Getenv(switchedEnv) != "" {
		return nil
	}
	path, stamp, ok := newerProgram(dataDir)
	if !ok {
		return nil
	}
	if inService {
		if err := switchService(path); err != nil {
			fmt.Fprintf(gate.LogWriter(), "could not point the service at %s (%v); going on with this version\n", path, err)
			return nil
		}
		return errServiceRestart
	}
	current, _ := compiled.CurrentBuild()
	log := gate.LogWriter()
	fmt.Fprintf(log, "this is lovkilde %s (%s, %s); a newer version (%s, %s) is in %s. Starting that instead.\n",
		version, current, current.Time.Format("2006-01-02"), stamp.Revision, stamp.Time.Format("2006-01-02"), path)
	os.Setenv(switchedEnv, version)
	if err := runInstead(path); err != nil {
		fmt.Fprintf(log, "could not start %s (%v); going on with this version\n", path, err)
		os.Unsetenv(switchedEnv)
	}
	return nil
}

// newerProgram is the newest downloaded build, when it is newer than this one, is not this one's
// own file, and this one is a published build: one from uncommitted files, or one that does not
// know its commit, stays itself.
func newerProgram(dataDir string) (string, softwareupdate.Stamp, bool) {
	current, ok := compiled.CurrentBuild()
	if !ok || current.Modified {
		return "", softwareupdate.Stamp{}, false
	}
	path, stamp, ok := softwareupdate.Newest(softwareupdate.Dir(dataDir))
	if !ok || !stamp.Time.After(current.Time) || isRunning(path) {
		return "", softwareupdate.Stamp{}, false
	}
	return path, stamp, true
}

// watchProgram waits for a newer build to be kept in the data directory — by the server's own
// check, or by "lovkilde program download" — and returns it once it has shown that it runs. It
// returns "" when ctx ends. A build that does not run is reported once and the server stays as it
// is, rather than stopping to start something that will not.
func watchProgram(ctx context.Context, dataDir string, every time.Duration) string {
	refused := ""
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ""
		case <-tick.C:
		}
		path, _, ok := newerProgram(dataDir)
		if !ok || path == refused {
			continue
		}
		if err := answersVersion(ctx, path); err != nil {
			if ctx.Err() != nil {
				return ""
			}
			fmt.Fprintf(gate.LogWriter(), "the newer version in %s does not run (%v); going on with this version\n", path, err)
			refused = path
			continue
		}
		return path
	}
}

// isRunning says whether path is this program's own file: a build whose name claims a later commit
// than the build records would otherwise switch to itself every time it started or looked.
func isRunning(path string) bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	a, errA := os.Stat(self)
	b, errB := os.Stat(path)
	return errA == nil && errB == nil && os.SameFile(a, b)
}

// answersVersion runs the program at path with --version: a build for another platform, or a file
// that is not a program, fails here instead of after the server has stopped.
func answersVersion(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = append(os.Environ(), switchedEnv+"="+version)
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(out), "lovkilde ") {
		return fmt.Errorf("it answered %q", strings.TrimSpace(string(out)))
	}
	return nil
}

// restartAs runs the program at path in place of this server, with the same arguments, once the
// server has stopped listening. It returns only if that could not be done, or, in a Windows
// service, with errServiceRestart once the service is pointed at it.
func restartAs(path string) error {
	fmt.Fprintf(gate.LogWriter(), "restarting as the newer version in %s\n", path)
	if inService {
		if err := switchService(path); err != nil {
			return err
		}
		return errServiceRestart
	}
	os.Setenv(switchedEnv, version)
	os.Setenv(restartedEnv, "1")
	return replaceWith(path)
}

// sayStartedBy tells, in the program switchToNewer started, that this is the newer version.
func sayStartedBy() {
	if from := os.Getenv(switchedEnv); from != "" {
		current, _ := compiled.CurrentBuild()
		fmt.Fprintf(gate.LogWriter(), "running lovkilde %s (%s, %s), started by the older version %s\n",
			version, current, current.Time.Format("2006-01-02"), from)
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
	return &softwareupdate.Checker{Repo: softwareRepo, Revision: build.Revision, UserAgent: config.DefaultUserAgent}, nil
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
			fmt.Fprintf(out, "this is lovkilde %s (%s)\n", version, current)
			if path, stamp, ok := softwareupdate.Newest(softwareupdate.Dir(cfg.DataDir)); ok {
				fmt.Fprintf(out, "downloaded: %s, from %s\n", path, stamp.Time.Format("2006-01-02"))
			}
			sw, err := publishedChecker()
			if err != nil {
				return err
			}
			rel, err := sw.Latest(cmd.Context())
			if err != nil {
				return err
			}
			if rel.Behind == 0 {
				fmt.Fprintf(out, "the published release, %s, is not newer\n", rel.Tag)
				return nil
			}
			fmt.Fprintf(out, "%s is published, %d commits newer: lovkilde program download\n", rel.Tag, rel.Behind)
			return nil
		},
	}
}

func programDownloadCmd(resolve configFunc) *cobra.Command {
	return &cobra.Command{
		Use:   "download",
		Short: "Download the published build; a running server restarts as it, and lovkilde runs it from then on",
		Long: `Download the published build for this platform, check it against the release's SHA256SUMS
and keep it in <data-dir>/program. A server running from that data directory restarts as the new
build within a minute, and lovkilde started from anywhere runs the newest build kept there, if it
is newer than itself.`,
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
			rel, err := sw.Latest(cmd.Context())
			if err != nil {
				return err
			}
			if rel.Behind == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "the published release, %s, is not newer than this one\n", rel.Tag)
				return nil
			}
			dir := softwareupdate.Dir(cfg.DataDir)
			path, _, err := sw.Download(cmd.Context(), dir, rel.Tag)
			if err != nil {
				return err
			}
			softwareupdate.Prune(dir, path)
			fmt.Fprintf(cmd.OutOrStdout(), "downloaded %s to %s; a running server restarts as it, and it runs from the next start\n", rel.Tag, path)
			return nil
		},
	}
}
