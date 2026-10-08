package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const (
	MetaFile      = "meta.json"
	InventoryFile = "inventory.jsonl"
	BodiesFile    = "bodies.jsonl"
	ChangesFile   = "changes.jsonl"
	JournalFile   = "journal.jsonl"
	lockFile      = "lock"

	// formatVersion is written into meta.json. Another version is refused rather than
	// half-read.
	formatVersion = 2

	// compactAfter is how large the journal may grow before the files are rewritten. A full
	// sitemap pass that touches every row journals about 40 MB; rewriting the files costs a
	// second or two, so this keeps a replay short without rewriting during ordinary fetching.
	compactAfter = 64 << 20

	// changeRetention is how long feed changes are kept. The feed answers for ten days and a
	// change matters until the document it names has been fetched after it
	// (crawl.QueueFeedChanges), so two months is ample, and the table stops growing forever.
	changeRetention = 60 * 24 * time.Hour
)

// Published are the files a release carries: what a fresh machine needs to carry on collecting.
// The journal stays local.
var Published = []string{MetaFile, InventoryFile, BodiesFile, ChangesFile}

// entry is one journal line: exactly one field is set.
type entry struct {
	Inv  *record   `json:"inv,omitempty"`
	Body *Body     `json:"body,omitempty"`
	Chg  *Change   `json:"chg,omitempty"`
	Day  string    `json:"day,omitempty"`
	Meta *metaPair `json:"meta,omitempty"`
}

type metaPair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// metaDoc is meta.json.
type metaDoc struct {
	Version int               `json:"version"`
	Meta    map[string]string `json:"meta"`
	Days    []string          `json:"harvested_days"`
}

// Open loads the state kept in dir, creating the directory when it does not exist, and
// takes the directory's lock: one process collects at a time, because two writing the same
// files would each lose the other's rows.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	unlock, err := lockDir(filepath.Join(dir, lockFile))
	if err != nil {
		return nil, fmt.Errorf("state in %s: %w", dir, err)
	}
	s := &Store{
		dir:       dir,
		inventory: newTable[record](),
		bodies:    newTable[Body](),
		changes:   newTable[Change](),
		days:      map[string]bool{},
		meta:      map[string]string{},
		held:      map[string]string{},
		unlock:    unlock,
	}
	if err := s.load(); err != nil {
		unlock()
		return nil, fmt.Errorf("state in %s: %w", dir, err)
	}
	if s.journal, err = openJournal(filepath.Join(dir, JournalFile)); err != nil {
		unlock()
		return nil, err
	}
	return s, nil
}

// Close writes the files that changed, empties the journal and releases the lock.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.journal == nil {
		return nil
	}
	err := s.save()
	if cerr := s.journal.close(); err == nil {
		err = cerr
	}
	s.journal = nil
	s.unlock()
	return err
}

// Save writes the files that changed and empties the journal, without closing.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save()
}

// log appends entries to the journal, and rewrites the files once the journal has grown
// past compactAfter. The caller holds the write lock.
func (s *Store) log(es ...entry) error {
	if len(es) == 0 {
		return nil
	}
	if s.journal == nil {
		return errors.New("state store is closed")
	}
	if err := s.journal.append(es); err != nil {
		return err
	}
	if s.journal.size > compactAfter {
		return s.save()
	}
	return nil
}

// apply folds one journal entry into the tables, the same way the method that wrote it did.
func (s *Store) apply(e entry) {
	switch {
	case e.Inv != nil:
		s.setRecord(*e.Inv)
	case e.Body != nil:
		s.bodies.put(e.Body.ELIURL, *e.Body)
	case e.Chg != nil:
		if _, ok := s.changes.rows[e.Chg.key()]; !ok {
			s.changes.put(e.Chg.key(), *e.Chg)
		}
	case e.Day != "":
		s.days[e.Day], s.metaDirty = true, true
	case e.Meta != nil:
		s.meta[e.Meta.Key], s.metaDirty = e.Meta.Value, true
	}
}

// load reads the files and replays the journal. A directory with
// none of them is an empty state.
func (s *Store) load() error {
	var m metaDoc
	switch err := readJSON(filepath.Join(s.dir, MetaFile), &m); {
	case errors.Is(err, fs.ErrNotExist):
		if _, err := os.Stat(filepath.Join(s.dir, "current.json")); err == nil {
			return errors.New("holds state in format 1 (current.json), which this build does not read")
		}
	case err != nil:
		return err
	case m.Version != formatVersion:
		return fmt.Errorf("%s is format %d, this build reads %d", MetaFile, m.Version, formatVersion)
	}
	for k, v := range m.Meta {
		s.meta[k] = v
	}
	for _, d := range m.Days {
		s.days[d] = true
	}
	// Rows are read in file order, which is key order, so when two rows claim the same
	// accession the index keeps the first, the same one on every load.
	if err := readLines(filepath.Join(s.dir, InventoryFile), func(r record) {
		r.Inventory = r.Inventory.normal()
		s.setRecord(r)
	}); err != nil {
		return err
	}
	if err := readLines(filepath.Join(s.dir, BodiesFile), func(b Body) { s.bodies.put(b.ELIURL, b.normal()) }); err != nil {
		return err
	}
	if err := readLines(filepath.Join(s.dir, ChangesFile), func(c Change) { s.changes.put(c.key(), c) }); err != nil {
		return err
	}
	// What was read so far is what the files hold; only the journal's replay is unwritten.
	s.inventory.dirty, s.bodies.dirty, s.changes.dirty, s.metaDirty = false, false, false, false
	return replayJournal(filepath.Join(s.dir, JournalFile), s.apply)
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// readLines decodes a JSON Lines file a line at a time. A missing file is an empty table.
func readLines[T any](path string, fn func(T)) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for n := 1; sc.Scan(); n++ {
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return fmt.Errorf("%s:%d: %w", path, n, err)
		}
		fn(v)
	}
	return sc.Err()
}

// save writes the files whose tables changed, each under a temporary name renamed into
// place, and only then empties the journal: a crash between the steps leaves a journal whose
// entries the files already hold, and replaying them again changes nothing. The caller holds
// the write lock.
func (s *Store) save() error {
	s.pruneChanges(time.Now())
	write := func(name string, dirty bool, fn func(w *bufio.Writer) error) error {
		if !dirty {
			return nil
		}
		return writeFile(filepath.Join(s.dir, name), fn)
	}
	for _, step := range []func() error{
		func() error { return write(MetaFile, s.metaDirty, s.writeMeta) },
		func() error {
			return write(InventoryFile, s.inventory.dirty, func(w *bufio.Writer) error {
				return writeLines(w, &s.inventory, func(r record) any { return r })
			})
		},
		func() error {
			return write(BodiesFile, s.bodies.dirty, func(w *bufio.Writer) error {
				return writeLines(w, &s.bodies, func(b Body) any { return b })
			})
		},
		func() error {
			return write(ChangesFile, s.changes.dirty, func(w *bufio.Writer) error {
				return writeLines(w, &s.changes, func(c Change) any { return c })
			})
		},
	} {
		if err := step(); err != nil {
			return err
		}
	}
	s.inventory.dirty, s.bodies.dirty, s.changes.dirty, s.metaDirty = false, false, false, false
	if s.journal != nil {
		return s.journal.truncate()
	}
	return nil
}

// pruneChanges drops feed changes older than changeRetention.
func (s *Store) pruneChanges(now time.Time) {
	cutoff := now.Add(-changeRetention).UTC().Format(time.DateOnly)
	for k, c := range s.changes.rows {
		if c.Date < cutoff {
			s.changes.remove(k)
		}
	}
}

func writeFile(path string, write func(w *bufio.Writer) error) error {
	tmp := path + ".writing"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	err = write(w)
	if err == nil {
		err = w.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) writeMeta(w *bufio.Writer) error {
	days := make([]string, 0, len(s.days))
	for d := range s.days {
		days = append(days, d)
	}
	slices.Sort(days)
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	return enc.Encode(metaDoc{Version: formatVersion, Meta: s.meta, Days: days})
}

// writeLines writes a table as JSON Lines, one row per line, in key order.
func writeLines[T any](w *bufio.Writer, t *table[T], shape func(T) any) error {
	for _, k := range t.sorted() {
		row, err := json.Marshal(shape(t.rows[k]))
		if err != nil {
			return err
		}
		w.Write(row)
		if err := w.WriteByte('\n'); err != nil {
			return err
		}
	}
	return nil
}

// journal is the append-only change log.
type journal struct {
	f    *os.File
	size int64
}

func openJournal(path string) (*journal, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &journal{f: f, size: info.Size()}, nil
}

// append writes the entries in one write, so a batch is on disk whole or, after a crash
// mid-write, up to a torn last line that replay ignores.
func (j *journal) append(es []entry) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i := range es {
		if err := enc.Encode(&es[i]); err != nil {
			return err
		}
	}
	n, err := j.f.Write(buf.Bytes())
	j.size += int64(n)
	return err
}

func (j *journal) truncate() error {
	if err := j.f.Truncate(0); err != nil {
		return err
	}
	j.size = 0
	return nil
}

func (j *journal) close() error { return j.f.Close() }

// replayJournal applies every whole line of the journal. A line that does not decode is the
// torn end of a write the process did not finish, so it ends the replay rather than failing
// it: everything before it was written whole.
func replayJournal(path string, apply func(entry)) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return nil // a final line without its newline is torn
		}
		if err != nil {
			return err
		}
		var e entry
		if json.Unmarshal(line, &e) != nil {
			return nil
		}
		apply(e)
	}
}
