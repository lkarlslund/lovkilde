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
