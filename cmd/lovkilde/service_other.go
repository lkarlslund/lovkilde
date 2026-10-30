//go:build !windows

package main

import "github.com/spf13/cobra"

// Only Windows has a service command: elsewhere systemd or launchd starts lovkilde serve, as
// docs/install-for-ai.md describes.
func serviceArgs() ([]string, bool)            { return nil, false }
func runService([]string)                      {}
func switchService(string) error               { return nil }
func addServiceCmd(*cobra.Command, configFunc) {}
