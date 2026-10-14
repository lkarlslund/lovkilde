package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
)

// runInstead runs the program at path with the same arguments and console, and exits with its
// exit code: Windows cannot replace a running process. Ctrl-C reaches both, and the program at
// path is the one that answers it.
func runInstead(path string) error {
	cmd := exec.Command(path, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	signal.Ignore(os.Interrupt)
	err := cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
	return nil
}
