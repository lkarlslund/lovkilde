// Package settings keeps what the server remembers between runs in one file in the data
// directory, settings.json: the intervals and last outcomes of the scheduled checks, the API key's
// hash, and the acceptance of the terms of use. Each is a section of the file, owned by its own
// package (checks, apikey, eula), which reads and writes only its section.
package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Name is the file's name in the data directory.
const Name = "settings.json"

// File is the settings file of one data directory. Every opener of the same directory shares
// one, so writes from different sections cannot undo each other.
type File struct {
	path string
	mu   sync.Mutex
}

var (
	openMu sync.Mutex
	open   = map[string]*File{}
)

// Open returns the settings file of dataDir. Settings kept in the files used before there was one
// are moved into it the first time.
func Open(dataDir string) *File {
	path := filepath.Join(dataDir, Name)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	openMu.Lock()
	defer openMu.Unlock()
	if f := open[path]; f != nil {
		return f
	}
	f := &File{path: path}
	f.migrate(dataDir)
	open[path] = f
	return f
}

func (f *File) read() map[string]json.RawMessage {
	all := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(f.path); err == nil {
		_ = json.Unmarshal(raw, &all)
	}
	return all
}

// Get reads section into v, and says whether the file has it.
func (f *File) Get(section string, v any) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, ok := f.read()[section]
	return ok && json.Unmarshal(raw, v) == nil
}

// Put replaces section with v; a nil v removes it.
func (f *File) Put(section string, v any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.put(section, v)
}

func (f *File) put(section string, v any) error {
	all := f.read()
	if v == nil {
		delete(all, section)
	} else {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		all[section] = raw
	}
	out, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return err
	}
	// The file holds the API key's hash, so it is the owner's alone.
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

// migrate moves the check intervals (checks.json) and the API key's hash (apikey), kept in files
// of their own before, into their sections, and removes the old files once moved.
func (f *File) migrate(dataDir string) {
	all := f.read()
	if raw, err := os.ReadFile(filepath.Join(dataDir, "checks.json")); err == nil {
		if _, ok := all["checks"]; !ok && json.Valid(raw) {
			if f.put("checks", json.RawMessage(raw)) != nil {
				return
			}
		}
		_ = os.Remove(filepath.Join(dataDir, "checks.json"))
	}
	if raw, err := os.ReadFile(filepath.Join(dataDir, "apikey")); err == nil {
		if _, ok := all["apikey"]; !ok {
			if f.put("apikey", map[string]string{"sha256": strings.TrimSpace(string(raw))}) != nil {
				return
			}
		}
		if err := os.Remove(filepath.Join(dataDir, "apikey")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return
		}
	}
}
