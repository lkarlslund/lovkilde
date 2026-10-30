//go:build !windows

package main

import "context"

// The notification area icon is Windows only: elsewhere Lovkilde runs in its terminal or under
// systemd or launchd.
func startTray(context.Context, string, string, func()) func() { return func() {} }
func showConsole() bool                                        { return false }
