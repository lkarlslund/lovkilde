// Package eula keeps whether the terms of use have been accepted on this server. They are
// accepted once, on the website or through the MCP tool, and the acceptance holds for both and
// across restarts, until the terms change (disclaimer.TermsVersion). It is kept in one file in
// the data directory.
package eula

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lkarlslund/lovkilde/internal/disclaimer"
)

// File is where the acceptance is kept, in the data directory: the accepted version of the terms
// on the first line, and when on the second.
const File = "eula.accepted"

// Store is the server's acceptance. A nil *Store has none and keeps none.
type Store struct {
	path     string
	accepted atomic.Bool
}

// Open reads the acceptance kept in dataDir; a missing or unreadable file, or one for other terms,
// is none.
func Open(dataDir string) *Store {
	s := &Store{path: filepath.Join(dataDir, File)}
	if raw, err := os.ReadFile(s.path); err == nil {
		version, _, _ := strings.Cut(string(raw), "\n")
		s.accepted.Store(strings.TrimSpace(version) == disclaimer.TermsVersion)
	}
	return s
}

// Accepted says whether the current terms have been accepted on this server.
func (s *Store) Accepted() bool { return s != nil && s.accepted.Load() }

// Accept records that the current terms are accepted. It holds from now even if it cannot be
// saved; the error says it will not survive a restart.
func (s *Store) Accept() error {
	if s == nil {
		return nil
	}
	s.accepted.Store(true)
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	body := disclaimer.TermsVersion + "\n" + time.Now().UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
