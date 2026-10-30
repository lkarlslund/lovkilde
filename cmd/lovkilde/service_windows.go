// The Windows service: "lovkilde service install" registers "lovkilde service run" with the service
// manager, which starts it at boot and starts it again when it exits with an error. service run is
// lovkilde serve, answering the service manager. A newer build
// is not started by the service itself, because the manager would not follow a process it did not
// start: the service is pointed at the newer build and exits with an error, and the manager starts
// that (switchService).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/lkarlslund/lovkilde/internal/compiled"
	"github.com/lkarlslund/lovkilde/internal/config"
	"github.com/lkarlslund/lovkilde/internal/softwareupdate"
)

// serviceName is the service's name with the service manager.
const serviceName = "Lovkilde"

// serviceLog is the service's log in the data directory: a service has no console.
const serviceLog = "lovkilde.log"

// serviceArgs are serve's arguments when this process was started as "lovkilde service run …",
// which is what the service is installed as. The command line says it, rather than a guess from
// the parent process, which does not hold everywhere a service manager runs.
func serviceArgs() ([]string, bool) {
	if len(os.Args) < 3 || os.Args[1] != "service" || os.Args[2] != "run" {
		return nil, false
	}
	return os.Args[3:], true
}

// runService runs this process as the service: lovkilde serve with args.
func runService(args []string) {
	inService = true
	logToFile(argValue(args, "--data-dir"))
	if err := svc.Run(serviceName, service{args: args}); err != nil {
		fmt.Fprintf(gate.LogWriter(), "lovkilde service run is started by the Windows service manager: %v\n", err)
		os.Exit(1)
	}
}

// logToFile sends the log to the data directory's log file; one that has grown past 10 MB is kept
// as the .old file and a new one started.
func logToFile(dataDir string) {
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}
	path := filepath.Join(dataDir, serviceLog)
	if info, err := os.Stat(path); err == nil && info.Size() > 10<<20 {
		_ = os.Rename(path, path+".old")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	gate.SetOutput(f)
}

type service struct{ args []string }

// Execute runs the command the service was installed with until the service manager stops it.
// An exit to start a newer build, or an error, is reported as a failure, which the recovery set
// at install answers by starting the service again.
func (s service) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		root := newRootCmd()
		root.SetArgs(append([]string{"serve"}, s.args...))
		done <- root.ExecuteContext(ctx)
	}()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			switch {
			case errors.Is(err, errServiceRestart):
				return false, 1
			case err != nil:
				fmt.Fprintf(gate.LogWriter(), "error: %v\n", err)
				return false, 1
			}
			return false, 0
		case r := <-req:
			switch r.Cmd {
			case svc.Interrogate:
				status <- r.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(serveShutdown + 5*time.Second):
				}
				return false, 0
			}
		}
	}
}

// switchService points the service at the program at path, with the arguments it runs with now.
func switchService(path string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return err
	}
	defer s.Close()
	line := syscall.EscapeArg(path)
	for _, a := range os.Args[1:] {
		line += " " + syscall.EscapeArg(a)
	}
	// Only the command line changes; everything else is left as installed.
	cmdline, err := windows.UTF16PtrFromString(line)
	if err != nil {
		return err
	}
	if err := windows.ChangeServiceConfig(s.Handle, windows.SERVICE_NO_CHANGE, windows.SERVICE_NO_CHANGE, windows.SERVICE_NO_CHANGE,
		cmdline, nil, nil, nil, nil, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(gate.LogWriter(), "the service now runs %s, and restarts as it\n", path)
	return nil
}

func addServiceCmd(root *cobra.Command, resolve configFunc) {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Run lovkilde serve as a Windows service, started at boot",
	}
	var addr string
	install := &cobra.Command{
		Use:   "install",
		Short: "Install and start the Lovkilde service (as administrator)",
		Long: `Copy this program into <data-dir>\program and register it with the service manager as
"lovkilde service run", which is "lovkilde serve --no-browser", started at boot and started again when it fails. Its log is
<data-dir>\` + serviceLog + `. A newer build downloaded from the settings page or the MCP tool update
becomes the service's program, and the service restarts as it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolve()
			if err != nil {
				return err
			}
			dataDir, err := filepath.Abs(cfg.DataDir)
			if err != nil {
				return err
			}
			m, err := mgr.Connect()
			if err != nil {
				return fmt.Errorf("connecting to the service manager: %w (run this as administrator)", err)
			}
			defer m.Disconnect()
			if s, err := m.OpenService(serviceName); err == nil {
				s.Close()
				return fmt.Errorf("the %s service is installed already: lovkilde service uninstall first", serviceName)
			}
			path, err := keepForService(dataDir)
			if err != nil {
				return err
			}
			s, err := m.CreateService(serviceName, path, mgr.Config{
				DisplayName:      "Lovkilde",
				Description:      "Dansk lovgivning fra retsinformation.dk som hjemmeside og MCP-server. Uofficiel kopi, ikke juridisk rådgivning.",
				StartType:        mgr.StartAutomatic,
				DelayedAutoStart: true,
			}, "service", "run", "--no-browser", "--data-dir", dataDir, "--addr", addr)
			if err != nil {
				return fmt.Errorf("installing the service: %w", err)
			}
			defer s.Close()
			// Without restarting on failure the service would not come back as a newer build, so
			// a service that cannot be set to is removed again rather than left half installed.
			restart := []mgr.RecoveryAction{
				{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
				{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
				{Type: mgr.ServiceRestart, Delay: time.Minute},
			}
			err = s.SetRecoveryActions(restart, uint32((24 * time.Hour).Seconds()))
			if err == nil {
				err = s.SetRecoveryActionsOnNonCrashFailures(true)
			}
			if err != nil {
				_ = s.Delete()
				return fmt.Errorf("setting the service to restart when it fails: %w; the service is not installed", err)
			}
			if err := s.Start(); err != nil {
				return fmt.Errorf("the service is installed but did not start: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "installed and started the %s service, running %s; it serves at %s, and its log is %s\n",
				serviceName, path, browserURL(addr), filepath.Join(dataDir, serviceLog))
			return nil
		},
	}
	install.Flags().StringVar(&addr, "addr", defaultServeAddr, "address the service listens on")
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the Lovkilde service (as administrator); the data directory is kept",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := mgr.Connect()
			if err != nil {
				return fmt.Errorf("connecting to the service manager: %w (run this as administrator)", err)
			}
			defer m.Disconnect()
			s, err := m.OpenService(serviceName)
			if err != nil {
				return fmt.Errorf("the %s service is not installed", serviceName)
			}
			defer s.Close()
			if _, err := s.Control(svc.Stop); err == nil {
				for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
					if st, err := s.Query(); err != nil || st.State == svc.Stopped {
						break
					}
				}
			}
			if err := s.Delete(); err != nil {
				return fmt.Errorf("removing the service: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed the %s service; the data directory is kept\n", serviceName)
			return nil
		},
	}
	cmd.AddCommand(install, uninstall)
	root.AddCommand(cmd)
}

// keepForService copies this program into the data directory's program folder, named as a
// downloaded build is, so the service does not depend on where it was started from — a download
// folder, say — and a newer download supersedes it the same way. A build that does not know its
// commit is copied under a name the downloads leave alone.
func keepForService(dataDir string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := softwareupdate.Dir(dataDir)
	name := "lovkilde-service.exe"
	if b, ok := compiled.CurrentBuild(); ok && !b.Modified {
		name = softwareupdate.ProgramName(softwareupdate.Stamp{Revision: b.Revision, Time: b.Time})
	}
	dest := filepath.Join(dir, name)
	if same, err := filepath.Abs(exe); err == nil && strings.EqualFold(same, dest) {
		return dest, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	in, err := os.Open(exe)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return "", err
	}
	return dest, out.Close()
}
