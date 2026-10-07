// Package store keeps the mirror's mutable bookkeeping: which ELI resources exist, which
// bodies are on disk, and how far the harvest feed has been consumed.
//
// Deliberate boundary: this is STATE, not the ARCHIVE. Legal document bytes live as plain
// XML files under data/xml (see internal/archive), because those files are the product and
// must stay greppable, diffable and portable. The state is a derived index: if it is lost,
// `retsin-update rebuild` reconstructs it offline by re-scanning the XML, costing zero
// requests.
//
// The whole state is a few hundred thousand small rows, so it is held in memory and kept on
// disk as JSON in one directory (see files.go):
//
//	current.json    what the mirror holds and knows: every row, every stored body, the feed
//	                changes, the harvested days and the pull record. It is what a fresh
//	                machine needs to carry on collecting, and what gets published with it.
//	queue.json      retry bookkeeping: attempts and the last error, per row. Local; losing it
//	                only resets the counters, because a row still owed says so in current.json
//	                (pending, failed or stale).
//	journal.jsonl   every change since the files were last written, one per line, so a run
//	                that is killed loses nothing. Close folds it into the two files.
//
// Both JSON files are written one row per line in key order, so two states diff line by
// line and a content-chunked store (Hugging Face's) keeps the rows that did not change.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// State of an inventory row.
type State string

const (
	StatePending State = "pending"
	StateFetched State = "fetched"
	StateFailed  State = "failed"
	StateSkipped State = "skipped" // duplicate body already stored under another URL
	StateGone    State = "gone"    // no representation available: 404, or HTML served where XML was expected
	// StateStale means the mirror holds bytes for a resource the publisher has
	// since republished: the sitemap's lastmod moved, so what is on disk is a
	// version the world has moved past. It is deliberately not "pending", because
	// the fetch loop closes any pending row whose file is already on disk without
	// asking the site -- the thing that makes a re-pended catalogue cheap -- and a
	// row that merely wants re-verification has to survive that shortcut. Without
	// a state of its own, an amended law is unreachable forever after its first
	// download.
	StateStale State = "stale"
	// StateEmpty is a body-queue state: the payload arrived and is stored, but
	// carries no document text at all. Published-as-tables documents (Finanslove
	// and their tillægsbevillinger) are the common case. It is terminal and
	// distinct from gone: here the answer came back, there was nothing in it, and
	// re-requesting it on every future pass would spend the request budget to
	// re-learn that.
	StateEmpty State = "empty"
)

// Store is the in-memory state with its files. Every method is safe for concurrent use.
type Store struct {
	mu  sync.RWMutex
	dir string

	// inventory is the sitemap queue: one row per ELI resource, holding the canonical XML.
	inventory table[Inventory]
	// bodies is the second-representation queue: one row per document whose canonical XML
	// has no body, tracking the site JSON payload that does. It is a separate table rather
	// than another State on the inventory row because the two questions are independent --
	// an inventory row says "we hold the canonical XML" (StateFetched) and that must stay
	// true while the same document is still pending a body. Overloading one State field
	// would make PendingInventory offer already-archived XML again, which is the one thing
	// the fetch loop's idempotency depends on.
	bodies     table[Body]
	accessions map[string]Accession
	changes    table[Change] // keyed date|accession|reason, so key order is date order
	days       map[string]bool
	meta       map[string]string

	journal *journal
	unlock  func()
}

// table is one keyed set of rows with its keys sorted on demand. Scans walk the keys in
// order, which is what makes cursor paging and the issue fingerprint reproducible.
type table[T any] struct {
	rows map[string]T
	keys []string // sorted; nil after a new key is added
}

func newTable[T any]() table[T] { return table[T]{rows: map[string]T{}} }

func (t *table[T]) put(key string, v T) {
	if _, ok := t.rows[key]; !ok {
		t.keys = nil
	}
	t.rows[key] = v
}

func (t *table[T]) sorted() []string {
	if t.keys == nil {
		t.keys = make([]string, 0, len(t.rows))
		for k := range t.rows {
			t.keys = append(t.keys, k)
		}
		slices.Sort(t.keys)
	}
	return t.keys
}

// Inventory is one ELI resource's manifest record.
type Inventory struct {
	ELIURL    string `json:"url,omitempty"`
	LastMod   string `json:"lastmod,omitempty"`
	Kind      string `json:"kind,omitempty"`
	State     State  `json:"state,omitempty"`
	Attempts  int    `json:"attempts,omitempty"`
	Accession string `json:"accession,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	Error     string `json:"error,omitempty"`
}

// head reports the fields a scan selects on, so the shared scan code can serve
// the inventory and body queues without knowing which record it is holding.
func (in Inventory) head() rowHead {
	return rowHead{kind: in.Kind, state: string(in.State), attempts: in.Attempts}
}

// normal fills what an absent field means: a row without a state is pending.
func (in Inventory) normal() Inventory {
	if in.State == "" {
		in.State = StatePending
	}
	return in
}

// PutInventory upserts one inventory record.
func (s *Store) PutInventory(ctx context.Context, in Inventory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	in = in.normal()
	s.inventory.put(in.ELIURL, in)
	return s.log(entry{Inv: &in})
}

// PutInventoryBatch upserts many records at once, which is the path for the ~200k-row
// sitemap import.
//
// Known rows are merged, not replaced. The sitemap reports every resource it has
// ever advertised on every pass, so a blind write would re-pend the whole
// catalogue each night: the fetch loop would then close 200k files locally and
// re-request the ~10k resources recorded as gone, and every attempt counter and
// stored hash would be thrown away until re-derived. The only change signal the
// sitemap carries is lastmod, so that is what re-pends a row.
func (s *Store) PutInventoryBatch(ctx context.Context, rows []Inventory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var changed []entry
	for _, in := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		cur, ok := s.inventory.rows[in.ELIURL]
		if !ok {
			cur = Inventory{}
		}
		merged := mergeInventory(cur, in.normal())
		if ok && merged == cur {
			continue // the observation changed nothing, so there is nothing to record
		}
		s.inventory.put(merged.ELIURL, merged)
		changed = append(changed, entry{Inv: &merged})
	}
	return s.log(changed...)
}

// mergeInventory decides what a sitemap observation means for a row already in
// the store. cur.ELIURL is empty when the resource is new, and then the
// observation is the whole record.
//
// An unchanged lastmod means nothing happened, so the row is left exactly as it
// is -- including a pending state and its attempt counter, which is what keeps a
// half-finished crawl resumable and a permanently dead resource from being
// retried forever.
//
// Only a *later* lastmod counts as a change. The sitemap advertises the same
// resource more than once per pass -- 203,188 entries for 201,616 resources --
// and the duplicates do not always agree on the date, so treating any difference
// as a change marked 872 rows stale on a pass that followed another one half an
// hour earlier, and would spend hundreds of requests per day re-fetching
// documents nobody amended. A date moving backwards is not the publisher saying
// this changed.
func mergeInventory(cur, observed Inventory) Inventory {
	if cur.ELIURL == "" {
		return observed
	}
	if observed.LastMod <= cur.LastMod {
		return cur
	}
	next := cur
	next.LastMod = observed.LastMod
	next.Kind = observed.Kind
	next.Error = ""
	// Holding bytes and having them declared out of date is not the same problem as
	// never having them: the fetch loop must ask the site this time, and it only
	// does that for a row it cannot satisfy from disk.
	if cur.SHA256 != "" {
		next.State = StateStale
	} else {
		next.State = StatePending
	}
	return next
}

// GetInventory reads one record.
func (s *Store) GetInventory(ctx context.Context, eliURL string) (Inventory, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	in, ok := s.inventory.rows[eliURL]
	return in, ok, nil
}

// rowHead is the part of a stored record a selection decision is made from. Both
// record types in the store (inventory rows and body rows) produce one, which is
// what lets one scan implementation serve both queues.
type rowHead struct {
	kind     string
	state    string
	attempts int
}

// row is a stored record that can report what a scan needs to decide on it
// without the caller knowing which record type it is holding.
type row interface{ head() rowHead }

// scanAll walks every row of a table in key order. The caller holds the lock.
func scanAll[T any](ctx context.Context, t *table[T], fn func(key string, r T) (stop bool)) error {
	for _, k := range t.sorted() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if fn(k, t.rows[k]) {
			return nil
		}
	}
	return nil
}

func pendingByKind[T row](ctx context.Context, t *table[T], sel selector) (map[string]int64, error) {
	counts := map[string]int64{}
	err := scanAll(ctx, t, func(_ string, r T) bool {
		h := r.head()
		if sel.wantState(h.state) && sel.wantKind(h.kind) {
			counts[h.kind]++
		}
		return false
	})
	return counts, err
}

// PendingByKind counts resources still needing a fetch, grouped by ELI kind. It
// exists because --kind filtering changes the denominator a progress bar needs
// before the run starts, and the state totals cannot answer "how many of these
// are retsinfo". The selection follows Filter, except that the attempt bound is
// not applied, so a default count errs slightly high.
func (s *Store) PendingByKind(ctx context.Context, f Filter) (map[string]int64, error) {
	sel, err := f.selector()
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return pendingByKind(ctx, &s.inventory, sel)
}

// Order in which a scan hands out rows. --order exposes all three because they
// answer different questions: the bootstrap wants chronology, a live mirror wants
// the newest law first, and a sample wants neither.
const (
	// OrderAsc is ELI URL order, which for these paths is chronological within a
	// kind. It is the default: a mirror aiming to finish should finish in order,
	// and a random default would turn a partial crawl into a shallow skim.
	OrderAsc = "asc"
	// OrderDesc is the same keyspace walked backwards, so the newest documents come
	// first -- the part of the corpus that is read most and amended most.
	OrderDesc = "desc"
	// OrderRandom hands out the selection in shuffled order. It is not a scan
	// direction: see Store.PendingKeys for why it is materialised once per run.
	OrderRandom = "random"
)

// parseOrder normalises and validates --order. An unknown value is an error rather
// than a silent fallback to asc, because a sample taken in the wrong order looks
// exactly like a sample taken in the right one.
func parseOrder(name string) (string, error) {
	switch name = strings.ToLower(strings.TrimSpace(name)); name {
	case "", OrderAsc:
		return OrderAsc, nil
	case OrderDesc, OrderRandom:
		return name, nil
	default:
		return "", fmt.Errorf("unknown order %q (valid: asc, desc, random)", name)
	}
}

// Filter narrows which inventory rows a scan returns. The zero Filter is the
// ordinary crawl sweep: pending rows plus failed rows still under MaxAttempts, in
// ELI URL order.
type Filter struct {
	// Kinds restricts to ELI series as parsed from the sitemap URL ("retsinfo",
	// "lta", ...). Empty means every kind.
	Kinds []string
	// States restricts to rows in these states. Empty means pending and failed.
	// Naming states lifts MaxAttempts: the attempt ceiling exists to stop an
	// automatic sweep spinning on a dead URL, and "retire these failures anyway"
	// is precisely the case that ceiling must not veto.
	States []string
	// MaxAttempts bounds retries in the default sweep.
	MaxAttempts int
	// Order is OrderAsc, OrderDesc or OrderRandom.
	Order string
	// Cursor is the last ELI URL the caller was handed. The scan resumes on the far
	// side of it, in the direction Order asks for, which is what guarantees each row
	// is offered once per run -- the attempt ceiling cannot do that for a
	// hand-picked state, because re-fetching a row that stays in that state (a 404
	// retried under --state gone) would otherwise re-select it forever.
	Cursor string
}

// selector is a Filter with its lists turned into sets and its order resolved,
// compiled once per scan so a 200k-row walk does not re-parse the same flag values
// per row.
type selector struct {
	kinds      map[string]bool
	states     map[string]bool // nil means the default sweep: pending plus failed
	exact      bool            // states named by hand, so MaxAttempts does not apply
	maxAttempt int
	cursor     string
	reverse    bool
}

// selector compiles the filter, refusing state and order names that cannot mean
// anything. A mistyped kind is only a warning later, because a kind can
// legitimately have nothing pending; a mistyped state or order can never do what
// was asked, so they are errors.
func (f Filter) selector() (selector, error) {
	order, err := parseOrder(f.Order)
	if err != nil {
		return selector{}, err
	}
	sel := selector{maxAttempt: f.MaxAttempts, cursor: f.Cursor, reverse: order == OrderDesc}
	for _, k := range f.Kinds {
		if sel.kinds == nil {
			sel.kinds = map[string]bool{}
		}
		sel.kinds[k] = true
	}
	for _, st := range f.States {
		st = strings.ToLower(strings.TrimSpace(st))
		switch State(st) {
		case StatePending, StateFetched, StateFailed, StateSkipped, StateGone, StateEmpty:
		default:
			return selector{}, fmt.Errorf("unknown state %q (valid: pending, fetched, failed, skipped, gone, empty)", st)
		}

		if sel.states == nil {
			sel.states = map[string]bool{}
		}
		sel.states[st] = true
		sel.exact = true
	}
	return sel, nil
}

// wantKind reports whether a row of this ELI kind is in scope.
func (sel selector) wantKind(kind string) bool {
	return sel.kinds == nil || sel.kinds[kind]
}

// wantState reports whether a row in this state is in scope. An empty state is a
// record written before the state column existed, and counts as pending.
func (sel selector) wantState(state string) bool {
	if state == "" {
		state = string(StatePending)
	}
	if sel.states == nil {
		// Stale belongs in the default sweep: the row is work the automatic crawl
		// owes, and excluding it would leave amended documents sitting there for a
		// pass nobody runs.
		return state == string(StatePending) || state == string(StateFailed) || state == string(StateStale)
	}
	return sel.states[state]
}

// wantAttempts reports whether the retry ceiling still admits a row. The ceiling
// belongs to the automatic sweep; a hand-picked state is a deliberate retry.
func (sel selector) wantAttempts(attempts int) bool {
	return sel.exact || attempts < sel.maxAttempt
}

// scanRows walks the rows on the far side of the selection's cursor, in its direction, and
// hands each to fn until fn says it has had enough. Both queues share it, so the cursor and
// direction handling exists once. The caller holds the lock.
func scanRows[T any](ctx context.Context, t *table[T], sel selector, fn func(key string, r T) (stop bool)) error {
	keys := t.sorted()
	if !sel.reverse {
		i := 0
		if sel.cursor != "" {
			i, _ = slices.BinarySearch(keys, sel.cursor)
			if i < len(keys) && keys[i] == sel.cursor {
				i++
			}
		}
		for ; i < len(keys); i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if fn(keys[i], t.rows[keys[i]]) {
				return nil
			}
		}
		return nil
	}
	i := len(keys) - 1
	if sel.cursor != "" {
		j, _ := slices.BinarySearch(keys, sel.cursor) // first key >= cursor; the walk starts below it
		i = j - 1
	}
	for ; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		if fn(keys[i], t.rows[keys[i]]) {
			return nil
		}
	}
	return nil
}

// pendingRows is the shared selection loop behind PendingInventory and
// PendingBodies: same filter, same cursor paging, same attempt ceiling. Two
// independent copies of this loop is where a queue would drift out of agreement
// with the other.
func pendingRows[T row](ctx context.Context, t *table[T], limit int, f Filter) ([]T, error) {
	sel, err := f.selector()
	if err != nil {
		return nil, err
	}
	var out []T
	err = scanRows(ctx, t, sel, func(_ string, r T) bool {
		h := r.head()
		if !sel.wantState(h.state) || !sel.wantAttempts(h.attempts) || !sel.wantKind(h.kind) {
			return false
		}
		out = append(out, r)
		// Zero means unlimited here, as it does for --limit; comparing directly
		// would stop after the first row.
		return limit > 0 && len(out) >= limit
	})
	return out, err
}

// PendingInventory returns up to limit resources the filter selects, in the order
// the filter asks for.
func (s *Store) PendingInventory(ctx context.Context, limit int, f Filter) ([]Inventory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return pendingRows(ctx, &s.inventory, limit, f)
}

// PendingKeys returns the ELI URLs the filter selects. Order=random needs the whole
// selection to shuffle it, and paging a shuffled order is impossible from a table kept in
// ELI URL order, so it is materialised once per run.
func (s *Store) PendingKeys(ctx context.Context, f Filter) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return pendingKeys(ctx, &s.inventory, f)
}

// Inventories returns the rows for specific ELI URLs, in the order given, skipping
// any that have disappeared. It is the point-lookup path Order=random uses after
// shuffling.
func (s *Store) Inventories(ctx context.Context, eliURLs []string) ([]Inventory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return rowsByKeys(&s.inventory, eliURLs), nil
}

func pendingKeys[T row](ctx context.Context, t *table[T], f Filter) ([]string, error) {
	sel, err := f.selector()
	if err != nil {
		return nil, err
	}
	var out []string
	err = scanRows(ctx, t, sel, func(key string, r T) bool {
		h := r.head()
		if sel.wantState(h.state) && sel.wantAttempts(h.attempts) && sel.wantKind(h.kind) {
			out = append(out, key)
		}
		return false
	})
	return out, err
}

func rowsByKeys[T any](t *table[T], keys []string) []T {
	var out []T
	for _, k := range keys {
		if r, ok := t.rows[k]; ok {
			out = append(out, r)
		}
	}
	return out
}

func countByState[T row](ctx context.Context, t *table[T]) (map[State]int64, int64, error) {
	counts := map[State]int64{}
	var total int64
	err := scanAll(ctx, t, func(_ string, r T) bool {
		counts[State(r.head().state)]++
		total++
		return false
	})
	return counts, total, err
}

// CountByState tallies inventory records per state.
func (s *Store) CountByState(ctx context.Context) (map[State]int64, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return countByState(ctx, &s.inventory)
}

// Body is one document's second-representation record: the site JSON payload that
// carries documentHtml for documents whose canonical ELI XML is metadata only.
//
// The row exists so a backfill run does not repeat itself. Half the law corpus has
// no XML body, so "does the sidecar file exist" is not enough to decide what to
// request: a document that 404s, or whose payload turns out to hold no text at all
// (Finanslove are published as tables), would otherwise be requested again on every
// future pass. That is the same mistake the XML path guards against by recording a
// dead representation as gone rather than failed.
type Body struct {
	ELIURL    string `json:"url,omitempty"`
	Kind      string `json:"kind,omitempty"`
	State     State  `json:"state,omitempty"`
	Attempts  int    `json:"attempts,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	TextChars int64  `json:"text_chars,omitempty"`
	Error     string `json:"error,omitempty"`
}

// head reports the selection fields, which is what lets Body share the inventory
// queue's scan, cursor paging, ordering and attempt ceiling.
func (b Body) head() rowHead {
	return rowHead{kind: b.Kind, state: string(b.State), attempts: b.Attempts}
}

func (b Body) normal() Body {
	if b.State == "" {
		b.State = StatePending
	}
	return b
}

// PutBody upserts one body-queue record.
func (s *Store) PutBody(ctx context.Context, b Body) error {
	return s.PutBodyBatch(ctx, []Body{b})
}

// PutBodyBatch upserts many body rows at once, which is the path the detection scan
// uses: it queues tens of thousands of documents in one pass.
func (s *Store) PutBodyBatch(ctx context.Context, rows []Body) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	es := make([]entry, 0, len(rows))
	for _, b := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		b = b.normal()
		s.bodies.put(b.ELIURL, b)
		es = append(es, entry{Body: &b})
	}
	return s.log(es...)
}

// GetBody reads one body-queue record.
func (s *Store) GetBody(ctx context.Context, eliURL string) (Body, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.bodies.rows[eliURL]
	return b, ok, nil
}

// PendingBodies returns up to limit documents the filter selects from the body
// queue, in the order the filter asks for. Selection semantics are the inventory
// queue's by construction: both go through pendingRows.
func (s *Store) PendingBodies(ctx context.Context, limit int, f Filter) ([]Body, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return pendingRows(ctx, &s.bodies, limit, f)
}

// PendingBodyKeys is PendingKeys for the body queue, for --order random sampling.
func (s *Store) PendingBodyKeys(ctx context.Context, f Filter) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return pendingKeys(ctx, &s.bodies, f)
}

// Bodies reads specific body rows by ELI URL.
func (s *Store) Bodies(ctx context.Context, eliURLs []string) ([]Body, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return rowsByKeys(&s.bodies, eliURLs), nil
}

// PendingBodiesByKind is the body queue's denominator for the progress bar.
func (s *Store) PendingBodiesByKind(ctx context.Context, f Filter) (map[string]int64, error) {
	sel, err := f.selector()
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return pendingByKind(ctx, &s.bodies, sel)
}

// CountBodiesByState reports the queue's state totals, which after a run is also
// the census: fetched rows have text, empty rows are the documents the site has no
// text for either.
func (s *Store) CountBodiesByState(ctx context.Context) (map[State]int64, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return countByState(ctx, &s.bodies)
}

// Accession is a stored document body keyed by accession number, which is the
// deduplication unit: several ELI resources can resolve to the same accession.
type Accession struct {
	Accession string `json:"accession,omitempty"`
	ELIURL    string `json:"url,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	ChangedAt string `json:"changed_at,omitempty"` // when the stored bytes last changed

	// FetchedAt is when the document was last downloaded, whether or not the bytes changed.
	// Empty on records written before it existed, where ChangedAt is the best lower bound.
	FetchedAt string `json:"fetched_at,omitempty"`
}

// HeldSince is the latest time the stored copy is known to have been current.
func (a Accession) HeldSince() string {
	if a.FetchedAt > a.ChangedAt {
		return a.FetchedAt
	}
	return a.ChangedAt
}

// GetAccession reads a stored-body record.
func (s *Store) GetAccession(ctx context.Context, accession string) (Accession, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.accessions[accession]
	return a, ok, nil
}

// PutAccession records a stored body.
func (s *Store) PutAccession(ctx context.Context, a Accession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accessions[a.Accession] = a
	return s.log(entry{Acc: &a})
}

// Change is one harvesting-feed entry.
type Change struct {
	Date       string `json:"date"` // change date, YYYY-MM-DD
	Accession  string `json:"accession"`
	Reason     string `json:"reason"`
	DocumentID string `json:"document_id,omitempty"`
	DocType    string `json:"doc_type,omitempty"`
	Href       string `json:"href,omitempty"`
	SeenAt     string `json:"seen_at,omitempty"`
}

func (c Change) key() string { return c.Date + "|" + c.Accession + "|" + c.Reason }

// PutChanges stores harvesting-feed entries. Re-recording the same change is a
// no-op, which makes re-running the harvest window harmless.
func (s *Store) PutChanges(ctx context.Context, changes []Change) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A change already recorded keeps its first sighting: SeenAt is what a held copy is
	// compared against to decide whether it predates the change (see crawl.QueueFeedChanges),
	// and the current day is harvested again on every run.
	var es []entry
	for _, c := range changes {
		if _, ok := s.changes.rows[c.key()]; ok {
			continue
		}
		s.changes.put(c.key(), c)
		es = append(es, entry{Chg: &c})
	}
	return s.log(es...)
}

// Issues is the work a collection pass could not finish, and a fingerprint of which
// work that was.
//
//	owed     pending, or failed while still under the retry ceiling
//	stalled  either of those, but past the ceiling, so only a deliberate pass
//	         with --state pending revisits them
//
// A failed row counts as owed rather than as a conclusion, because the site may serve
// it tomorrow. Rows in gone, skipped, fetched or empty states are conclusions and are
// ignored. Stalled rows are counted apart because they are not retryable: they are
// reported so a reader knows the mirror has holes, not so a run can refuse to run.
//
// Digest fingerprints the identities of the outstanding rows, which the counts cannot.
// The same digest on consecutive runs means the same handful of documents the site will
// not serve; a moving digest means something new broke.
type Issues struct {
	Owed    int64
	Stalled int64

	// Digest is 8 hex digits over the outstanding rows, empty when nothing is
	// outstanding. A fingerprint for comparing runs, not an integrity check.
	Digest string
}

// Total is everything outstanding, retryable or not.
func (i Issues) Total() int64 { return i.Owed + i.Stalled }

// Merge folds another queue's issues into these. The digests are combined rather than
// concatenated so the whole record stays one short field.
func (i Issues) Merge(j Issues) Issues {
	out := Issues{Owed: i.Owed + j.Owed, Stalled: i.Stalled + j.Stalled}
	if i.Digest != "" || j.Digest != "" {
		h := fnv.New64a()
		_, _ = h.Write([]byte(i.Digest))
		_, _ = h.Write([]byte(j.Digest))
		out.Digest = fmt.Sprintf("%08x", h.Sum64()>>32)
	}
	return out
}

// String is the form both the state and the published file carry.
func (i Issues) String() string {
	if i.Total() == 0 {
		return "clean"
	}
	if i.Digest == "" {
		return fmt.Sprintf("owed=%d stalled=%d", i.Owed, i.Stalled)
	}
	return fmt.Sprintf("owed=%d stalled=%d issues=%s", i.Owed, i.Stalled, i.Digest)
}

// The table tags the fingerprint hashes ahead of each key. They are the key prefixes of
// the store this one replaced, kept so that a digest computed before and after the move
// agrees: the digest is how two runs are compared, and a format change is not a change in
// which documents are outstanding.
const (
	digestInventory = "i|"
	digestBody      = "b|"
)

// InventoryIssues walks the sitemap queue once and reports what it still intends to
// fetch.
func (s *Store) InventoryIssues(ctx context.Context, maxAttempts int) (Issues, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return pendingIssues(ctx, &s.inventory, digestInventory, maxAttempts)
}

// BodyIssues is InventoryIssues for the body sidecar queue, where empty is also a
// conclusion: the payload genuinely had no text.
func (s *Store) BodyIssues(ctx context.Context, maxAttempts int) (Issues, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return pendingIssues(ctx, &s.bodies, digestBody, maxAttempts)
}

// pendingIssues is the shared scan: one walk per queue, in key order, so the fingerprint
// is reproducible.
func pendingIssues[T row](ctx context.Context, t *table[T], tag string, maxAttempts int) (Issues, error) {
	var out Issues
	h := fnv.New64a()
	err := scanAll(ctx, t, func(key string, r T) bool {
		hh := r.head()
		switch hh.state {
		case "", string(StatePending), string(StateFailed), string(StateStale):
		default:
			return false
		}
		if hh.attempts < maxAttempts {
			out.Owed++
		} else {
			out.Stalled++
		}
		_, _ = h.Write([]byte(tag))
		_, _ = h.Write([]byte(key))
		return false
	})
	if err != nil {
		return Issues{}, err
	}
	if out.Total() > 0 {
		out.Digest = fmt.Sprintf("%08x", h.Sum64()>>32)
	}
	return out, nil
}

// HeldSince lists rows the mirror holds whose sitemap lastmod is at or after a
// date, newest first, capped at limit (0 for no cap).
//
// It answers a health question nothing else can: the sitemap says these resources
// were republished by the publisher, so anything still held from before that date
// is a version the rest of the world has moved past. Counting them is the only
// way to know whether the mirror is a current law library or a snapshot that
// quietly stopped being one.
func (s *Store) HeldSince(ctx context.Context, since string, limit int) ([]Inventory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Inventory
	err := scanAll(ctx, &s.inventory, func(_ string, in Inventory) bool {
		// Stale rows belong here: they are exactly the ones the publisher says were
		// republished while we hold an older version.
		if (in.State == StateFetched || in.State == StateStale) && in.LastMod >= since {
			out = append(out, in)
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastMod != out[j].LastMod {
			return out[i].LastMod > out[j].LastMod
		}
		return out[i].ELIURL < out[j].ELIURL
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Changes lists recorded feed changes from a date (YYYY-MM-DD, inclusive) onward,
// oldest first.
//
// It exists because a change row is otherwise write-only data: the feed's value
// is knowing *which* documents moved, and that is only useful if it can be read
// back next to what the mirror actually did about them.
func (s *Store) Changes(ctx context.Context, since string, limit int) ([]Change, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Change
	err := scanAll(ctx, &s.changes, func(_ string, c Change) bool {
		if since != "" && c.Date < since {
			return false
		}
		out = append(out, c)
		return limit > 0 && len(out) >= limit
	})
	return out, err
}

// UnresolvedAccessions lists change entries whose accession is not stored yet.
func (s *Store) UnresolvedAccessions(ctx context.Context, limit int) ([]Change, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Change
	err := scanAll(ctx, &s.changes, func(_ string, c Change) bool {
		if _, ok := s.accessions[c.Accession]; ok {
			return false
		}
		out = append(out, c)
		return len(out) >= limit
	})
	return out, err
}

// MarkDayHarvested records that a whole calendar day of the feed was consumed.
// This is the gap-detection checkpoint: the 10-day window means a multi-day
// outage must be backfilled before entries silently expire.
func (s *Store) MarkDayHarvested(ctx context.Context, day time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := day.UTC().Format(time.DateOnly)
	if s.days[d] {
		return nil
	}
	s.days[d] = true
	return s.log(entry{Day: d})
}

// LastHarvestedDay returns the newest recorded day checkpoint.
func (s *Store) LastHarvestedDay(ctx context.Context) (time.Time, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var newest string
	for d := range s.days {
		newest = max(newest, d)
	}
	if newest == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.DateOnly, newest)
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}

// SetMeta stores an arbitrary marker.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meta[key] = value
	return s.log(entry{Meta: &metaEntry{Key: key, Value: value}})
}

// GetMeta reads an arbitrary marker.
func (s *Store) GetMeta(ctx context.Context, key string) (string, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.meta[key]
	return v, ok, nil
}

// Sha256Hex is the content hash used for change detection and deduplication.
func Sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
