// Package eula keeps whether the terms of use have been accepted on this server. They are
// accepted once, on the website or through the MCP tool, and the acceptance holds for both and
// across restarts, until the terms change (disclaimer.TermsVersion). It is kept in the settings
// file (internal/settings).
package eula

import (
	"sync/atomic"
	"time"

	"github.com/lkarlslund/lovkilde/internal/disclaimer"
	"github.com/lkarlslund/lovkilde/internal/settings"
)

// section is where the acceptance is kept in the settings file.
const section = "eula"

type saved struct {
	Version  string    `json:"version"`  // the accepted version of the terms
	Accepted time.Time `json:"accepted"` // when
}

// Store is the server's acceptance. A nil *Store has none and keeps none.
type Store struct {
	file     *settings.File
	accepted atomic.Bool
}

// Open reads the acceptance kept for dataDir; none, or one of other terms, is not accepted.
func Open(dataDir string) *Store {
	s := &Store{file: settings.Open(dataDir)}
	var sv saved
	s.accepted.Store(s.file.Get(section, &sv) && sv.Version == disclaimer.TermsVersion)
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
	return s.file.Put(section, saved{Version: disclaimer.TermsVersion, Accepted: time.Now().UTC()})
}
