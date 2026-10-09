package builder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lkarlslund/retsinformation/internal/archive"
	"github.com/lkarlslund/retsinformation/internal/compiled"
	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/parallel"
)

// Mode is how a build gets its documents.
type Mode string

const (
	// Full compiles every document in the archive.
	Full Mode = "full"
	// Patch writes a whole artifact, compiling only the documents whose files changed since an
	// earlier artifact and copying every other document's frame from it byte for byte. It needs
	// the earlier artifact and the changed files, not the archive.
	Patch Mode = "patch"
)

// ParseMode reads a --mode value.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case Full, Patch:
		return m, nil
	case "":
		return Full, nil
	}
	return "", fmt.Errorf("build mode %q: full or patch", s)
}

// plan is what an incremental build works from: the archive as a full scan of the earlier
// artifact's files plus the files on disk would see it, and the documents it carries over.
type plan struct {
	archive *lexdania.Archive
	// carry is each document taken over from the earlier artifact, by accession, with the
	// catalogue record it had there.
	carry map[string]lexdania.CatalogueRecord
	// unbodied counts sidecars on disk that changed while their XML is not on disk: the
	// document cannot be compiled again here, so it keeps the text it had.
	unbodied int
}

// planIncremental works out what an incremental build over prev compiles and what it carries.
//
// A document is carried when its XML is not on disk (a fresh machine holds only what this week's
// pass fetched), or when its XML and sidecar hash to what prev was compiled from. Everything
// else on disk is scanned and compiled, so on a machine holding the whole archive the result is
// exactly what a full build makes, document frames and catalogue alike.
//
// Carrying is only right while this build compiles a document the way prev's did: the same
// document model (compiled.ModelVersion, which a change to what a document compiles to must
// bump) and the same dictionary, since a carried frame is compressed against it.
func planIncremental(ctx context.Context, prev *compiled.Artifact, root string) (*plan, error) {
	if prev.Header.Model != compiled.ModelVersion {
		return nil, fmt.Errorf("%s holds document model %d and this build compiles model %d: its documents cannot be carried over, so build in full", prev.Path(), prev.Header.Model, compiled.ModelVersion)
	}
	if prev.Header.Dictionary != compiled.CurrentDictionary() {
		return nil, fmt.Errorf("%s is compressed against dictionary %d and this build writes %d: its frames cannot be carried over, so build in full", prev.Path(), prev.Header.Dictionary.ID, compiled.CurrentDictionary().ID)
	}
	// A document's model records where its sidecar was read from, under the archive root, so a
	// frame is only what compiling it again would make when it is compiled from the same root.
	if filepath.Clean(prev.Header.Source) != filepath.Clean(root) {
		return nil, fmt.Errorf("%s was built from the archive at %s and this build reads %s: documents record paths under it, so build in full, or use the --data-dir it was built with", prev.Path(), prev.Header.Source, root)
	}
	raw, err := prev.Catalogue()
	if err != nil {
		return nil, fmt.Errorf("%s: catalogue: %w", prev.Path(), err)
	}
	var records []lexdania.CatalogueRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, fmt.Errorf("%s: catalogue: %w", prev.Path(), err)
	}
	earlier := map[string]lexdania.CatalogueRecord{}
	for _, rec := range records {
		rel, err := filepath.Rel(root, rec.Entry.Path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s was built from %s and lists %s outside it", prev.Path(), prev.Header.Source, rec.Entry.Path)
		}
		earlier[filepath.ToSlash(rel)] = rec
	}

	var xmls []string
	sidecars := map[string]bool{}
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && path == root {
				return filepath.SkipDir // nothing fetched yet: everything is carried
			}
			return err
		}
		if d.IsDir() {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		switch {
		case strings.HasSuffix(rel, archive.XMLExt):
			xmls = append(xmls, rel)
		case strings.HasSuffix(rel, lexdania.SidecarExt):
			sidecars[rel] = true
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Hashing is the only read every file on disk costs, and it is what makes carrying safe on a
	// machine where the files changed by any route, not only through the collector.
	type local struct {
		xml, body string
		entry     lexdania.Entry
		carried   bool // compiled from exactly these files before
		scanned   bool // read as a scan would, because it cannot be carried
		err       error
	}
	found := make([]local, len(xmls))
	parallel.For(ctx, len(xmls), func(i int) {
		path := filepath.Join(root, filepath.FromSlash(xmls[i]))
		l := &found[i]
		if l.xml, l.body, l.err = sourceHashes(path); l.err != nil {
			return
		}
		if rec, ok := earlier[xmls[i]]; ok && rec.XMLHash != "" && rec.XMLHash == l.xml && rec.BodyHash == l.body {
			l.carried = true
			return
		}
		l.entry, l.scanned = lexdania.ReadEntry(path) // not scanned: a scan skips it too
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p := &plan{carry: map[string]lexdania.CatalogueRecord{}}
	var entries []lexdania.Entry
	keep := func(rec lexdania.CatalogueRecord) {
		entries = append(entries, rec.Entry)
		p.carry[rec.Entry.Accession] = rec
	}
	for i, rel := range xmls {
		l := found[i]
		if l.err != nil {
			continue // vanished or unreadable while this ran: a scan skips it as well
		}
		rec := earlier[rel]
		delete(earlier, rel)
		switch {
		case l.carried:
			keep(rec)
		case l.scanned:
			entries = append(entries, l.entry)
		}
	}
	// What is left was not on disk: carried as it was, unless only its sidecar arrived.
	for rel, rec := range earlier {
		sidecar := strings.TrimSuffix(rel, archive.XMLExt) + lexdania.SidecarExt
		if sidecars[sidecar] {
			if h, err := hashFile(filepath.Join(root, filepath.FromSlash(sidecar))); err == nil && h != rec.BodyHash {
				p.unbodied++
			}
		}
		keep(rec)
	}
	if p.archive, err = lexdania.NewArchive(root, entries); err != nil {
		return nil, err
	}
	// The archive keeps one entry per accession. A carried document whose accession now belongs
	// to a file that was scanned is not carried: the scanned one is what a full build would hold.
	for acc, rec := range p.carry {
		if e, ok := p.archive.Find(acc); !ok || e.Path != rec.Entry.Path {
			delete(p.carry, acc)
		}
	}
	return p, nil
}

// sourceHashes is what a document is compiled from, as the catalogue records it: the hash of its
// XML and of its body sidecar, "" for a sidecar that is not there.
func sourceHashes(path string) (xml, body string, err error) {
	if xml, err = hashFile(path); err != nil {
		return "", "", err
	}
	body, err = hashFile(lexdania.SidecarPath(path))
	if errors.Is(err, fs.ErrNotExist) {
		return xml, "", nil
	}
	return xml, body, err
}

func hashFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return archive.Hash(raw), nil
}
