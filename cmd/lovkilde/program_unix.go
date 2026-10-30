//go:build !windows

package main

import (
	"os"
	"syscall"
)

// runInstead replaces this process with the program at path, with the same arguments.
func runInstead(path string) error {
	return syscall.Exec(path, append([]string{path}, os.Args[1:]...), os.Environ())
}

// replaceWith is runInstead: the process keeps its id, so a service manager sees the same server.
func replaceWith(path string) error { return runInstead(path) }
