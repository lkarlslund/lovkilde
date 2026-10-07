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
)

const (
	// CurrentFile is what the mirror holds and knows; QueueFile the local retry bookkeeping;
	// JournalFile the changes since either was written.
	CurrentFile = "current.json"
	QueueFile   = "queue.json"
	JournalFile = "journal.jsonl"
	lockFile    = "lock"

	// formatVersion is written into current.json. A file of another version is refused rather
	// than half-read.
	formatVersion = 1

	// compactAfter is how large the journal may grow before the files are rewritten. A full
	// sitemap pass that touches every row journals about 40 MB; rewriting the files costs a
	// second or two, so this keeps a replay short without rewriting during ordinary fetching.
	compactAfter = 64 << 20
)

// entry is one journal line: exactly one field is set.
type entry struct {
	Inv  *Inventory `json:"inv,omitempty"`
	Body *Body      `json:"body,omitempty"`
	Acc  *Accession `json:"acc,omitempty"`
	Chg  *Change    `json:"chg,omitempty"`
	Day  string     `json:"day,omitempty"`
	Meta *metaEntry `json:"meta,omitempty"`
}

type metaEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
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
		dir:        dir,
		inventory:  newTable[Inventory](),
		bodies:     newTable[Body](),
		changes:    newTable[Change](),
		accessions: map[string]Accession{},
		days:       map[string]bool{},
		meta:       map[string]string{},
		unlock:     unlock,
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

// Close writes the state files, empties the journal and releases the lock.
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

// Save writes the state files and empties the journal, without closing.
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
		s.inventory.put(e.Inv.ELIURL, *e.Inv)
	case e.Body != nil:
		s.bodies.put(e.Body.ELIURL, *e.Body)
	case e.Acc != nil:
		s.accessions[e.Acc.Accession] = *e.Acc
	case e.Chg != nil:
		if _, ok := s.changes.rows[e.Chg.key()]; !ok {
			s.changes.put(e.Chg.key(), *e.Chg)
		}
	case e.Day != "":
		s.days[e.Day] = true
	case e.Meta != nil:
		s.meta[e.Meta.Key] = e.Meta.Value
	}
}

// currentDoc is current.json. Rows are keyed by ELI URL (accessions by accession number,
// changes by date|accession|reason), so a row's key is not repeated inside it.
type currentDoc struct {
	Version    int                  `json:"version"`
	Meta       map[string]string    `json:"meta"`
	Days       []string             `json:"harvested_days"`
	Inventory  map[string]Inventory `json:"inventory"`
	Bodies     map[string]Body      `json:"bodies"`
	Accessions map[string]Accession `json:"accessions"`
	Changes    map[string]Change    `json:"changes"`
}

// retry is a row's entry in queue.json.
type retry struct {
	Attempts int    `json:"attempts,omitempty"`
	Error    string `json:"error,omitempty"`
}

// queueDoc is queue.json.
type queueDoc struct {
	Inventory map[string]retry `json:"inventory"`
	Bodies    map[string]retry `json:"bodies"`
}

// load reads current.json, overlays queue.json and replays the journal. A directory with
// none of them is an empty state.
func (s *Store) load() error {
	var cur currentDoc
	switch err := readJSON(filepath.Join(s.dir, CurrentFile), &cur); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case cur.Version != formatVersion:
		return fmt.Errorf("%s is format %d, this build reads %d", CurrentFile, cur.Version, formatVersion)
	}
	var q queueDoc
	if err := readJSON(filepath.Join(s.dir, QueueFile), &q); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for u, in := range cur.Inventory {
		in.ELIURL = u
		r := q.Inventory[u]
		in.Attempts, in.Error = r.Attempts, r.Error
		s.inventory.rows[u] = in.normal()
	}
	for u, b := range cur.Bodies {
		b.ELIURL = u
		r := q.Bodies[u]
		b.Attempts, b.Error = r.Attempts, r.Error
		s.bodies.rows[u] = b.normal()
	}
	for acc, a := range cur.Accessions {
		a.Accession = acc
		s.accessions[acc] = a
	}
	for _, c := range cur.Changes {
		s.changes.rows[c.key()] = c
	}
	for _, d := range cur.Days {
		s.days[d] = true
	}
	for k, v := range cur.Meta {
		s.meta[k] = v
	}
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

// save writes both files, each under a temporary name renamed into place, and only then
// empties the journal: a crash between the steps leaves a journal whose entries the files
// already hold, and replaying them again changes nothing. The caller holds the write lock.
func (s *Store) save() error {
	if err := writeFile(filepath.Join(s.dir, CurrentFile), s.writeCurrent); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(s.dir, QueueFile), s.writeQueue); err != nil {
		return err
	}
	if s.journal != nil {
		return s.journal.truncate()
	}
	return nil
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

// writeCurrent writes current.json one row per line, in key order. The row's key is the
// object key and is left out of the row; attempts and errors are queue.json's.
func (s *Store) writeCurrent(w *bufio.Writer) error {
	days := make([]string, 0, len(s.days))
	for d := range s.days {
		days = append(days, d)
	}
	slices.Sort(days)
	head, err := json.Marshal(struct {
		Version int               `json:"version"`
		Meta    map[string]string `json:"meta"`
		Days    []string          `json:"harvested_days"`
	}{formatVersion, s.meta, days})
	if err != nil {
		return err
	}
	// The head object, reopened so the tables follow inside it.
	w.Write(head[:len(head)-1])
	if err := writeRows(w, "inventory", &s.inventory, func(in Inventory) any {
		in.ELIURL, in.Attempts, in.Error = "", 0, ""
		return in
	}); err != nil {
		return err
	}
	if err := writeRows(w, "bodies", &s.bodies, func(b Body) any {
		b.ELIURL, b.Attempts, b.Error = "", 0, ""
		return b
	}); err != nil {
		return err
	}
	acc := table[Accession]{rows: s.accessions}
	if err := writeRows(w, "accessions", &acc, func(a Accession) any {
		a.Accession = ""
		return a
	}); err != nil {
		return err
	}
	if err := writeRows(w, "changes", &s.changes, func(c Change) any { return c }); err != nil {
		return err
	}
	_, err = w.WriteString("\n}\n")
	return err
}

// writeQueue writes queue.json: attempts and errors of the rows not done. A fetched or empty
// row's attempt count is history nothing reads -- the ceiling applies to rows still owed -- so
// it is not kept, and a row that is republished starts its retries afresh.
func (s *Store) writeQueue(w *bufio.Writer) error {
	w.WriteString("{")
	inv := table[retry]{rows: map[string]retry{}}
	for u, in := range s.inventory.rows {
		if !done(in.State) && (in.Attempts != 0 || in.Error != "") {
			inv.rows[u] = retry{in.Attempts, in.Error}
		}
	}
	bodies := table[retry]{rows: map[string]retry{}}
	for u, b := range s.bodies.rows {
		if !done(b.State) && (b.Attempts != 0 || b.Error != "") {
			bodies.rows[u] = retry{b.Attempts, b.Error}
		}
	}
	keep := func(r retry) any { return r }
	if err := writeRowsFirst(w, "inventory", &inv, keep, true); err != nil {
		return err
	}
	if err := writeRows(w, "bodies", &bodies, keep); err != nil {
		return err
	}
	_, err := w.WriteString("\n}\n")
	return err
}

// done is a state whose row was answered with content: nothing about its retries matters.
func done(st State) bool { return st == StateFetched || st == StateEmpty }

// writeRows writes `,"name":{` and then one `"key":row` per line, in key order.
func writeRows[T any](w *bufio.Writer, name string, t *table[T], shape func(T) any) error {
	return writeRowsFirst(w, name, t, shape, false)
}

func writeRowsFirst[T any](w *bufio.Writer, name string, t *table[T], shape func(T) any, first bool) error {
	if !first {
		w.WriteString(",")
	}
	fmt.Fprintf(w, "\n%q:{", name)
	for i, k := range t.sorted() {
		key, _ := json.Marshal(k)
		row, err := json.Marshal(shape(t.rows[k]))
		if err != nil {
			return err
		}
		if i > 0 {
			w.WriteString(",")
		}
		w.WriteString("\n")
		w.Write(key)
		w.WriteString(":")
		if _, err := w.Write(row); err != nil {
			return err
		}
	}
	_, err := w.WriteString("\n}")
	return err
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
