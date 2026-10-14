//go:build unix

package store

import (
	"errors"
	"os"
	"syscall"
)

// lockDir takes an exclusive advisory lock on path, refusing at once when another process
// holds it. The kernel drops the lock when the process dies, so a killed run leaves nothing
// to clean up.
func lockDir(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("another lovkilde-update is using it")
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}
