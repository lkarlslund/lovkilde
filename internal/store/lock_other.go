//go:build !unix

package store

// lockDir does nothing where flock is not available: the updater is run on Unix.
func lockDir(string) (func(), error) { return func() {}, nil }
