package apikey

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A key is kept as its hash, survives a reopen, and is gone once cleared.
func TestKeyIsKeptAsItsHash(t *testing.T) {
	dir := t.TempDir()
	k := Open(dir)
	if k.Enabled() || k.Match("") {
		t.Fatal("a fresh server has a key")
	}
	if err := k.Set("kort"); err != ErrTooShort {
		t.Errorf("a short key: %v", err)
	}
	key := Generate()
	if err := k.Set(key); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, File))
	if strings.Contains(string(raw), key) {
		t.Error("the file holds the key itself")
	}
	k = Open(dir)
	if !k.Match(key) || k.Match(key+"x") || !k.MatchToken(k.Token()) {
		t.Error("the reopened key does not match as it should")
	}
	if err := k.Clear(); err != nil || k.Enabled() || Open(dir).Enabled() {
		t.Errorf("cleared: %v, still enabled", err)
	}
	var none *Key
	if none.Enabled() || none.Match(key) {
		t.Error("no key matched")
	}
}
