// Package apikey keeps the server's optional access key. It is set on the settings page and is
// never required: a server without one answers everyone, which is right on a loopback address and
// a choice the settings page warns about on any other. Only the key's SHA-256 is kept, in the
// settings file (internal/settings), so the file does not hand out the key.
package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/lkarlslund/lovkilde/internal/settings"
)

// section is where the key's hash is kept in the settings file.
const section = "apikey"

type saved struct {
	SHA256 string `json:"sha256"`
}

// MinLength is the shortest key Set takes, in characters.
const MinLength = 16

// ErrTooShort is a key shorter than MinLength.
var ErrTooShort = errors.New("api key too short")

// Key is the server's key, or none. A nil *Key is no key.
type Key struct {
	file *settings.File
	mu   sync.RWMutex
	hash string // hex SHA-256 of the key; "" when none is set
}

// Open reads the key kept in dataDir; a missing or unreadable file is no key.
func Open(dataDir string) *Key {
	k := &Key{file: settings.Open(dataDir)}
	var sv saved
	if k.file.Get(section, &sv) && len(sv.SHA256) == sha256.Size*2 {
		k.hash = sv.SHA256
	}
	return k
}

func hashOf(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// Generate returns a new random key.
func Generate() string { return rand.Text() }

// Set makes key the server's key, and saves it.
func (k *Key) Set(key string) error {
	key = strings.TrimSpace(key)
	if utf8.RuneCountInString(key) < MinLength {
		return ErrTooShort
	}
	h := hashOf(key)
	if err := k.file.Put(section, saved{SHA256: h}); err != nil {
		return err
	}
	k.mu.Lock()
	k.hash = h
	k.mu.Unlock()
	return nil
}

// Clear removes the key: the server answers everyone again.
func (k *Key) Clear() error {
	if err := k.file.Put(section, nil); err != nil {
		return err
	}
	k.mu.Lock()
	k.hash = ""
	k.mu.Unlock()
	return nil
}

// Enabled says whether a key is set.
func (k *Key) Enabled() bool { return k.Token() != "" }

// Token is what a browser keeps once it has given the key: the key's hash, so the cookie is not
// the key. It is "" when no key is set.
func (k *Key) Token() string {
	if k == nil {
		return ""
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.hash
}

// Match says whether key is the server's key.
func (k *Key) Match(key string) bool { return k.MatchToken(hashOf(strings.TrimSpace(key))) }

// MatchToken says whether tok is the token of the server's key.
func (k *Key) MatchToken(tok string) bool {
	want := k.Token()
	return want != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(want)) == 1
}
