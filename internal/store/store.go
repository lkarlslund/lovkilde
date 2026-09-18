// Package store persists the mirror's mutable bookkeeping in Badger: which ELI
// resources exist, which bodies are on disk, and how far the harvest feed has
// been consumed.
//
// Deliberate boundary: Badger holds STATE, not the ARCHIVE. Legal document bytes
// live as plain XML files under data/xml (see internal/archive), because those
// files are the product and must stay greppable, diffable and portable. The
// database is a derived index: if meta/kv is lost, `retsin rebuild` reconstructs
// it offline by re-scanning the XML, costing zero requests.
//
// Key layout (all values are tab-separated text so the store remains readable
// with ordinary tools):
//
//	i|<eli_url>   lastmod  kind  state  attempts  accession  sha256  bytes  error
//	a|<accession> eli_url  sha256  bytes  changed_at
//	h|<date>|<accn>|<reason>  doc_id  type  href  seen_at
//	H|<YYYY-MM-DD>            "1"    fully harvested day checkpoint
//	m|<key>       value
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/dgraph-io/badger/v4/options"
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

// Key prefixes. Single-character prefixes keep the keyspace small and make
// prefix scans cheap.
const (
	prefixInventory = "i|"
	prefixAccession = "a|"
	prefixChange    = "h|"
	prefixDay       = "H|"
	prefixMeta      = "m|"
	// prefixBody keys the second-representation queue: one row per document whose
	// canonical XML has no body, tracking the site JSON payload that does. It is a
	// separate keyspace rather than another State on the inventory row because the
	// two questions are independent -- an inventory row says "we hold the canonical
	// XML" (StateFetched) and that must stay true while the same document is still
	// pending a body. Overloading one State field would make PendingInventory
	// offer already-archived XML again, which is the one thing the fetch loop's
	// idempotency depends on.
	prefixBody = "b|"
)

// Store is the Badger-backed manifest.
type Store struct {
	db *badger.DB
}

// Open opens (creating if needed) the Badger store at dir.
func Open(dir string) (*Store, error) {
	opts := badger.DefaultOptions(dir)
	// This workload is millions of tiny records, read mostly by prefix scan.
	// Compression buys little (metadata compresses well but is small) and costs
	// CPU on every scan, so keep it off for predictable pacing.
	opts.Compression = options.None
	opts.NumVersionsToKeep = 1
	opts.NumMemtables = 2
	opts.NumLevelZeroTables = 1
	opts.NumLevelZeroTablesStall = 2
	opts.ValueLogFileSize = 32 << 20
	// Silence Badger's own logging: the fetch loop reports progress itself and
	// must not interleave with library chatter.
	opts.Logger = nil
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open state store %s: %w", dir, err)
	}
	return &Store{db: db}, nil
}

// Close flushes and closes the store.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the raw handle for maintenance commands (value-log GC).
func (s *Store) DB() *badger.DB { return s.db }

// Inventory is one ELI resource's manifest record.
type Inventory struct {
	ELIURL    string
	LastMod   string
	Kind      string
	State     State
	Attempts  int
	Accession string
	SHA256    string
	Bytes     int64
	Error     string
}

func (in Inventory) encode() string {
	return strings.Join([]string{
		in.LastMod, in.Kind, string(in.State), strconv.Itoa(in.Attempts),
		in.Accession, in.SHA256, strconv.FormatInt(in.Bytes, 10), in.Error,
	}, "\t")
}

func decodeInventory(eliURL, v string) (Inventory, error) {
	f := strings.Split(v, "\t")
	// Tolerate short records so a future field addition does not break reads.
	pad := &f
	if len(f) < 8 {
		g := make([]string, 8)
		copy(g, f)
		*pad = g
		f = g
	}
	attempts, _ := strconv.Atoi(f[3])
	bytes, _ := strconv.ParseInt(f[6], 10, 64)
	state := State(f[2])
	if state == "" {
		state = StatePending
	}
	return Inventory{
		ELIURL: eliURL, LastMod: f[0], Kind: f[1], State: state, Attempts: attempts,
		Accession: f[4], SHA256: f[5], Bytes: bytes, Error: f[7],
	}, nil
}

// head reports the fields a scan selects on, so the shared scan code can serve
// the inventory and body queues without knowing which record it is holding.
func (in Inventory) head() rowHead {
	return rowHead{kind: in.Kind, state: string(in.State), attempts: in.Attempts}
}

// PutInventory upserts one inventory record.
func (s *Store) PutInventory(ctx context.Context, in Inventory) error {
	return s.db.Update(func(tx *badger.Txn) error {
		return tx.Set([]byte(prefixInventory+in.ELIURL), []byte(in.encode()))
	})
}

// PutInventoryBatch upserts many records in one Badger batch, which is the
// efficient path for the ~200k-row sitemap import.
//
// Known rows are merged, not replaced. The sitemap reports every resource it has
// ever advertised on every pass, so a blind write would re-pend the whole
// catalogue each night: the fetch loop would then close 200k files locally and
// re-request the ~10k resources recorded as gone, and every attempt counter and
// stored hash would be thrown away until re-derived. The only change signal the
// sitemap carries is lastmod, so that is what re-pends a row.
func (s *Store) PutInventoryBatch(ctx context.Context, rows []Inventory) error {
	if len(rows) == 0 {
		return nil
	}

	// One read snapshot for the page rather than a transaction per row: the
	// sitemap arrives in pages of thousands, and each row needs its current
	// state to be merged.
	known := make(map[string]Inventory, len(rows))
	err := s.db.View(func(tx *badger.Txn) error {
		for _, in := range rows {
			item, err := tx.Get([]byte(prefixInventory + in.ELIURL))
			if errors.Is(err, badger.ErrKeyNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			v, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			cur, err := decodeInventory(in.ELIURL, string(v))
			if err != nil {
				return fmt.Errorf("inventory %s: %w", in.ELIURL, err)
			}
			known[in.ELIURL] = cur
		}
		return nil
	})
	if err != nil {
		return err
	}

	b := s.db.NewWriteBatch()
	defer b.Cancel()
	for _, in := range rows {
		merged := mergeInventory(known[in.ELIURL], in)
		if err := b.Set([]byte(prefixInventory+merged.ELIURL), []byte(merged.encode())); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return b.Flush()
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
	var out Inventory
	var found bool
	err := s.db.View(func(tx *badger.Txn) error {
		item, err := tx.Get([]byte(prefixInventory + eliURL))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		v, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		out, err = decodeInventory(eliURL, string(v))
		found = true
		return err
	})
	return out, found, err
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

func pendingByKind[T row](s *Store, ctx context.Context, prefix string, sel selector, decode func(key, value string) (T, error)) (map[string]int64, error) {
	counts := map[string]int64{}
	err := s.scanAll(ctx, prefix, func(key, value string) (bool, error) {
		r, err := decode(key, value)
		if err != nil {
			return false, err
		}
		h := r.head()
		if !sel.wantState(h.state) || !sel.wantKind(h.kind) {
			return false, nil
		}
		counts[h.kind]++
		return false, nil
	})
	return counts, err
}

// scanAll is a forward key-and-value walk over one prefix, for the counting and
// selection paths that do not need direction or a cursor.
func (s *Store) scanAll(ctx context.Context, prefix string, fn func(key, value string) (stop bool, err error)) error {
	return s.db.View(func(tx *badger.Txn) error {
		ops := badger.DefaultIteratorOptions
		ops.PrefetchValues = false
		ops.Prefix = []byte(prefix)
		it := tx.NewIterator(ops)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			key := string(it.Item().Key())
			v, err := it.Item().ValueCopy(nil)
			if err != nil {
				return err
			}
			stop, err := fn(strings.TrimPrefix(key, prefix), string(v))
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
		}
		return nil
	})
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
	return pendingByKind(s, ctx, prefixInventory, sel, decodeInventory)
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

// pastCursor reports whether a key is on the already-covered side of the cursor,
// which depends on the direction being walked.
func (sel selector) pastCursor(eliURL string) bool {
	if sel.cursor == "" {
		return false
	}
	if sel.reverse {
		return eliURL >= sel.cursor
	}
	return eliURL <= sel.cursor
}

// scanInventory walks the selection in the filter's direction and hands each
// matching row to fn, stopping when fn says it has had enough. PendingInventory and
// PendingKeys are thin wrappers so the direction and cursor handling exists once.
func (s *Store) scanInventory(ctx context.Context, sel selector, fn func(eliURL, value string) (stop bool, err error)) error {
	return s.scanRows(ctx, prefixInventory, sel, fn)
}

// scanRows is scanInventory for any key prefix. The body queue (prefixBody)
// needs the same cursor paging, the same reverse-walk bound and the same
// attempt/state narrowing as the XML inventory, and the subtleties in here --
// the hand-rolled reverse bound, Seek landing on the cursor, the 0xff start
// sentinel -- are exactly the parts that must not be reimplemented and got subtly
// wrong a second time.
func (s *Store) scanRows(ctx context.Context, prefix string, sel selector, fn func(eliURL, value string) (stop bool, err error)) error {
	return s.db.View(func(tx *badger.Txn) error {
		// Bigger value threshold keeps small records inline in the LSM, making
		// this scan read only the LSM rather than the value log.
		// (No per-transaction tuning is needed; Badger handles it.)
		ops := badger.DefaultIteratorOptions
		ops.PrefetchValues = false
		if sel.reverse {
			// Reverse + Prefix together iterated empty in this Badger version --
			// a bug that once silently broke harvest resumption -- so the bound
			// is enforced by hand: start above every key in the prefix and stop
			// as soon as a key falls out of it.
			ops.Reverse = true
		} else {
			ops.Prefix = []byte(prefix)
		}
		it := tx.NewIterator(ops)
		defer it.Close()

		for it.Seek(sel.scanStartAt(prefix)); it.Valid(); it.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			item := it.Item()
			key := string(item.Key())
			if sel.reverse && !strings.HasPrefix(key, prefix) {
				return nil // walked out of the bottom of the range
			}
			eliURL := strings.TrimPrefix(key, prefix)
			// Seek lands *on* the target, so the cursor row itself needs dropping
			// in both directions.
			if sel.pastCursor(eliURL) {
				continue
			}
			v, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			stop, err := fn(eliURL, string(v))
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
		}
		return nil
	})
}

// scanStart is the key a walk begins from. It is a separate function only so the
// cursor case can be asserted directly: a descending batch that starts at the top of
// the prefix instead of at its cursor still returns the right rows -- pastCursor drops
// everything above -- but re-walks every row above the cursor once per batch, which
// over a 200k catalogue is the quadratic scan that already bit the ascending path
// once. Cost like that is invisible to result assertions.
func (sel selector) scanStart() []byte { return sel.scanStartAt(prefixInventory) }

// scanStartAt is scanStart for a given prefix.
func (sel selector) scanStartAt(prefix string) []byte {
	switch {
	case sel.reverse && sel.cursor != "":
		// Badger's reverse Seek lands at or below the target, so aiming at the
		// cursor itself starts the walk on its near side; pastCursor drops the row
		// the cursor names.
		return []byte(prefix + sel.cursor)
	case sel.reverse:
		// 0xff cannot appear in an ELI URL, so this sits above every key with this
		// prefix and below the next one (the keyspace uses single-letter prefixes),
		// which is where a backwards walk with nothing covered yet should begin.
		return append([]byte(prefix), 0xff, 0xff, 0xff, 0xff)
	case sel.cursor != "":
		return []byte(prefix + sel.cursor)
	}
	return []byte(prefix)
}

// pendingRows is the shared selection loop behind PendingInventory and
// PendingBodies: same filter, same cursor paging, same attempt ceiling, one
// decode function each. Two independent copies of this loop is where a queue
// would drift out of agreement with the other.
func pendingRows[T row](s *Store, ctx context.Context, prefix string, limit int, f Filter, decode func(key, value string) (T, error)) ([]T, error) {
	sel, err := f.selector()
	if err != nil {
		return nil, err
	}
	var out []T
	err = s.scanRows(ctx, prefix, sel, func(key, value string) (bool, error) {
		r, err := decode(key, value)
		if err != nil {
			return false, err
		}
		h := r.head()
		if !sel.wantState(h.state) || !sel.wantAttempts(h.attempts) || !sel.wantKind(h.kind) {
			return false, nil
		}
		out = append(out, r)
		// Zero means unlimited here, as it does for --limit; comparing directly
		// would stop after the first row.
		return limit > 0 && len(out) >= limit, nil
	})
	return out, err
}

// PendingInventory returns up to limit resources the filter selects, in the order
// the filter asks for. A filtered call still walks the keyspace from the cursor --
// a few tens of milliseconds over the whole catalogue, which is noise next to the
// paced requests it feeds.
func (s *Store) PendingInventory(ctx context.Context, limit int, f Filter) ([]Inventory, error) {
	return pendingRows(s, ctx, prefixInventory, limit, f, decodeInventory)
}

// PendingKeys returns the ELI URLs the filter selects, without decoding the rows.
// Order=random needs the whole selection in memory to shuffle it: paging a shuffled
// order is impossible from a keyspace that is stored in ELI URL order, and
// re-scanning per batch to re-derive it would put the quadratic walk back. At
// roughly 60 bytes per URL, the full catalogue costs a few tens of megabytes --
// paid once, on a run that asked for it.
func (s *Store) PendingKeys(ctx context.Context, f Filter) ([]string, error) {
	return pendingKeys(s, ctx, prefixInventory, f, decodeInventory)
}

// Inventories returns the rows for specific ELI URLs, in the order given, skipping
// any that have disappeared. It is the point-lookup path Order=random uses after
// shuffling, instead of re-scanning the keyspace per batch.
func (s *Store) Inventories(ctx context.Context, eliURLs []string) ([]Inventory, error) {
	return rowsByKeys(s, ctx, prefixInventory, eliURLs, decodeInventory)
}

func pendingKeys[T row](s *Store, ctx context.Context, prefix string, f Filter, decode func(key, value string) (T, error)) ([]string, error) {
	sel, err := f.selector()
	if err != nil {
		return nil, err
	}
	var out []string
	err = s.scanRows(ctx, prefix, sel, func(key, value string) (bool, error) {
		r, err := decode(key, value)
		if err != nil {
			return false, err
		}
		// Decoding the row costs nothing extra -- the value has to be read to apply
		// the filter either way -- and going through the same decoder PendingRows
		// uses keeps this selection identical to its rather than a second parse.
		h := r.head()
		if !sel.wantState(h.state) || !sel.wantAttempts(h.attempts) || !sel.wantKind(h.kind) {
			return false, nil
		}
		out = append(out, key)
		return false, nil
	})
	return out, err
}

// rowsByKeys returns the decoded rows for specific keys, in the order given,
// skipping any that have disappeared. It is the point-lookup path Order=random
// uses after shuffling, instead of re-scanning the keyspace per batch.
func rowsByKeys[T any](s *Store, ctx context.Context, prefix string, keys []string, decode func(key, value string) (T, error)) ([]T, error) {
	var out []T
	err := s.db.View(func(tx *badger.Txn) error {
		for _, u := range keys {
			if err := ctx.Err(); err != nil {
				return err
			}
			item, err := tx.Get([]byte(prefix + u))
			if errors.Is(err, badger.ErrKeyNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			v, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			r, err := decode(u, string(v))
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return nil
	})
	return out, err
}

// countByState tallies records per state using a key-and-value scan over one prefix.
func countByState[T row](s *Store, ctx context.Context, prefix string, decode func(key, value string) (T, error)) (map[State]int64, int64, error) {
	counts := map[State]int64{}
	var total int64
	err := s.scanAll(ctx, prefix, func(key, value string) (bool, error) {
		r, err := decode(key, value)
		if err != nil {
			return false, err
		}
		counts[State(r.head().state)]++
		total++
		return false, nil
	})
	return counts, total, err
}

// CountByState tallies inventory records per state using a key-only scan.
func (s *Store) CountByState(ctx context.Context) (map[State]int64, int64, error) {
	return countByState(s, ctx, prefixInventory, decodeInventory)
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
	ELIURL    string
	Kind      string
	State     State
	Attempts  int
	SHA256    string
	Bytes     int64
	TextChars int64
	Error     string
}

// head reports the selection fields, which is what lets Body share the inventory
// queue's scan, cursor paging, ordering and attempt ceiling.
func (b Body) head() rowHead {
	return rowHead{kind: b.Kind, state: string(b.State), attempts: b.Attempts}
}

func (b Body) encode() string {
	return strings.Join([]string{
		b.Kind, string(b.State), strconv.Itoa(b.Attempts),
		b.SHA256, strconv.FormatInt(b.Bytes, 10),
		strconv.FormatInt(b.TextChars, 10), b.Error,
	}, "\t")
}

func decodeBody(eliURL, v string) (Body, error) {
	f := strings.Split(v, "\t")
	// Tolerate short records so a future field addition does not break reads,
	// exactly as decodeInventory does.
	if len(f) < 7 {
		g := make([]string, 7)
		copy(g, f)
		f = g
	}
	attempts, _ := strconv.Atoi(f[2])
	bytes, _ := strconv.ParseInt(f[4], 10, 64)
	chars, _ := strconv.ParseInt(f[5], 10, 64)
	state := State(f[1])
	if state == "" {
		state = StatePending
	}
	return Body{
		ELIURL: eliURL, Kind: f[0], State: state, Attempts: attempts,
		SHA256: f[3], Bytes: bytes, TextChars: chars, Error: f[6],
	}, nil
}

// PutBody upserts one body-queue record.
func (s *Store) PutBody(ctx context.Context, b Body) error {
	return s.db.Update(func(tx *badger.Txn) error {
		return tx.Set([]byte(prefixBody+b.ELIURL), []byte(b.encode()))
	})
}

// PutBodyBatch upserts many body rows in one Badger batch, which is the path the
// detection scan uses: it queues tens of thousands of documents in one pass.
func (s *Store) PutBodyBatch(ctx context.Context, rows []Body) error {
	if len(rows) == 0 {
		return nil
	}
	b := s.db.NewWriteBatch()
	defer b.Cancel()
	for _, row := range rows {
		if err := b.Set([]byte(prefixBody+row.ELIURL), []byte(row.encode())); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return b.Flush()
}

// GetBody reads one body-queue record.
func (s *Store) GetBody(ctx context.Context, eliURL string) (Body, bool, error) {
	var out Body
	var found bool
	err := s.db.View(func(tx *badger.Txn) error {
		item, err := tx.Get([]byte(prefixBody + eliURL))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		v, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		out, err = decodeBody(eliURL, string(v))
		found = true
		return err
	})
	return out, found, err
}

// PendingBodies returns up to limit documents the filter selects from the body
// queue, in the order the filter asks for. Selection semantics are the inventory
// queue's by construction: both go through pendingRows.
func (s *Store) PendingBodies(ctx context.Context, limit int, f Filter) ([]Body, error) {
	return pendingRows(s, ctx, prefixBody, limit, f, decodeBody)
}

// PendingBodyKeys is PendingKeys for the body queue, for --order random sampling.
func (s *Store) PendingBodyKeys(ctx context.Context, f Filter) ([]string, error) {
	return pendingKeys(s, ctx, prefixBody, f, decodeBody)
}

// Bodies reads specific body rows by ELI URL.
func (s *Store) Bodies(ctx context.Context, eliURLs []string) ([]Body, error) {
	return rowsByKeys(s, ctx, prefixBody, eliURLs, decodeBody)
}

// PendingBodiesByKind is the body queue's denominator for the progress bar.
func (s *Store) PendingBodiesByKind(ctx context.Context, f Filter) (map[string]int64, error) {
	sel, err := f.selector()
	if err != nil {
		return nil, err
	}
	return pendingByKind(s, ctx, prefixBody, sel, decodeBody)
}

// CountBodiesByState reports the queue's state totals, which after a run is also
// the census: fetched rows have text, empty rows are the documents the site has no
// text for either.
func (s *Store) CountBodiesByState(ctx context.Context) (map[State]int64, int64, error) {
	return countByState(s, ctx, prefixBody, decodeBody)
}

// Accession is a stored document body keyed by accession number, which is the
// deduplication unit: several ELI resources can resolve to the same accession.
type Accession struct {
	Accession string
	ELIURL    string
	SHA256    string
	Bytes     int64
	ChangedAt string
}

// GetAccession reads a stored-body record.
func (s *Store) GetAccession(ctx context.Context, accession string) (Accession, bool, error) {
	var out Accession
	var found bool
	err := s.db.View(func(tx *badger.Txn) error {
		item, err := tx.Get([]byte(prefixAccession + accession))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		v, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		f := strings.Split(string(v), "\t")
		if len(f) < 5 {
			g := make([]string, 5)
			copy(g, f)
			f = g
		}
		out = Accession{Accession: accession, ELIURL: f[0], SHA256: f[1], ChangedAt: f[3]}
		out.Bytes, _ = strconv.ParseInt(f[2], 10, 64)
		found = true
		return nil
	})
	return out, found, err
}

// PutAccession records a stored body.
func (s *Store) PutAccession(ctx context.Context, a Accession) error {
	v := strings.Join([]string{a.ELIURL, a.SHA256, strconv.FormatInt(a.Bytes, 10), a.ChangedAt, a.Accession}, "\t")
	return s.db.Update(func(tx *badger.Txn) error { return tx.Set([]byte(prefixAccession+a.Accession), []byte(v)) })
}

// Change is one harvesting-feed entry.
type Change struct {
	Date       string // change date, YYYY-MM-DD
	Accession  string
	Reason     string
	DocumentID string
	DocType    string
	Href       string
	SeenAt     string
}

// PutChanges stores harvesting-feed entries. Re-recording the same change is a
// no-op, which makes re-running the harvest window harmless.
func (s *Store) PutChanges(ctx context.Context, changes []Change) error {
	if len(changes) == 0 {
		return nil
	}
	b := s.db.NewWriteBatch()
	defer b.Cancel()
	for _, c := range changes {
		key := []byte(prefixChange + c.Date + "|" + c.Accession + "|" + c.Reason)
		v := strings.Join([]string{c.DocumentID, c.DocType, c.Href, c.SeenAt}, "\t")
		if err := b.Set(key, []byte(v)); err != nil {
			return err
		}
	}
	return b.Flush()
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

// String is the form both the state store and the published file carry.
func (i Issues) String() string {
	if i.Total() == 0 {
		return "clean"
	}
	if i.Digest == "" {
		return fmt.Sprintf("owed=%d stalled=%d", i.Owed, i.Stalled)
	}
	return fmt.Sprintf("owed=%d stalled=%d issues=%s", i.Owed, i.Stalled, i.Digest)
}

// InventoryIssues walks the sitemap queue once and reports what it still intends to
// fetch.
func (s *Store) InventoryIssues(ctx context.Context, maxAttempts int) (Issues, error) {
	return pendingIssues(s, ctx, prefixInventory, maxAttempts, decodeInventory)
}

// BodyIssues is InventoryIssues for the body sidecar queue, where empty is also a
// conclusion: the payload genuinely had no text.
func (s *Store) BodyIssues(ctx context.Context, maxAttempts int) (Issues, error) {
	return pendingIssues(s, ctx, prefixBody, maxAttempts, decodeBody)
}

// pendingIssues is the shared scan: one walk per queue. Rows come back in key order,
// so the fingerprint is reproducible without collecting anything.
func pendingIssues[T row](s *Store, ctx context.Context, prefix string, maxAttempts int, decode func(key, value string) (T, error)) (Issues, error) {
	var out Issues
	h := fnv.New64a()
	err := s.scanAll(ctx, prefix, func(key, value string) (bool, error) {
		r, err := decode(key, value)
		if err != nil {
			return false, err
		}
		hh := r.head()
		// An empty state is a pre-state-column record, which means pending.
		switch hh.state {
		case "", string(StatePending), string(StateFailed), string(StateStale):
		default:
			return false, nil
		}
		if hh.attempts < maxAttempts {
			out.Owed++
		} else {
			out.Stalled++
		}
		_, _ = h.Write([]byte(prefix))
		_, _ = h.Write([]byte(key))
		return false, nil
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
	var out []Inventory
	err := s.scanAll(ctx, prefixInventory, func(key, value string) (bool, error) {
		in, err := decodeInventory(key, value)
		if err != nil {
			return false, err
		}
		// Stale rows belong here: they are exactly the ones the publisher says were
		// republished while we hold an older version.
		if (in.State != StateFetched && in.State != StateStale) || in.LastMod < since {
			return false, nil
		}
		out = append(out, in)
		return limit > 0 && len(out) >= limit*4, nil
	})
	if err != nil {
		return nil, err
	}
	// The scan stops after a handful of times the limit rather than walking all
	// 200k rows, so it then needs the newest ones: sorting what it collected and
	// keeping the top limit. Collecting more than limit keeps that truncation
	// honest even when the early keyspace is dominated by one medium.
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
// oldest first. Keys start with the date, so the range is a prefix scan rather
// than a filter over every change the feed has ever reported.
//
// It exists because a change row is otherwise write-only data: the feed's value
// is knowing *which* documents moved, and that is only useful if it can be read
// back next to what the mirror actually did about them.
func (s *Store) Changes(ctx context.Context, since string, limit int) ([]Change, error) {
	var out []Change
	err := s.scanAll(ctx, prefixChange, func(key, value string) (bool, error) {
		f := strings.SplitN(key, "|", 3)
		if len(f) < 3 {
			return false, nil
		}
		c := Change{Date: f[0], Accession: f[1], Reason: f[2]}
		if since != "" && c.Date < since {
			return false, nil
		}
		if vf := strings.Split(value, "\t"); len(vf) >= 4 {
			c.DocumentID, c.DocType, c.Href, c.SeenAt = vf[0], vf[1], vf[2], vf[3]
		}
		out = append(out, c)
		return limit > 0 && len(out) >= limit, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UnresolvedAccessions lists change entries whose accession is not stored yet.
func (s *Store) UnresolvedAccessions(ctx context.Context, limit int) ([]Change, error) {
	var out []Change
	err := s.db.View(func(tx *badger.Txn) error {
		ops := badger.DefaultIteratorOptions
		ops.PrefetchValues = false
		ops.Prefix = []byte(prefixChange)
		it := tx.NewIterator(ops)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			key := string(it.Item().Key())
			f := strings.Split(strings.TrimPrefix(key, prefixChange), "|")
			if len(f) < 3 {
				continue
			}
			c := Change{Date: f[0], Accession: f[1], Reason: f[2]}
			v, err := it.Item().ValueCopy(nil)
			if err != nil {
				return err
			}
			vf := strings.Split(string(v), "\t")
			if len(vf) >= 4 {
				c.DocumentID, c.DocType, c.Href, c.SeenAt = vf[0], vf[1], vf[2], vf[3]
			}
			if _, found, err := s.GetAccession(ctx, c.Accession); err != nil {
				return err
			} else if found {
				continue
			}
			out = append(out, c)
			if len(out) >= limit {
				return nil
			}
		}
		return nil
	})
	return out, err
}

// MarkDayHarvested records that a whole calendar day of the feed was consumed.
// This is the gap-detection checkpoint: the 10-day window means a multi-day
// outage must be backfilled before entries silently expire.
func (s *Store) MarkDayHarvested(ctx context.Context, day time.Time) error {
	return s.db.Update(func(tx *badger.Txn) error {
		return tx.Set([]byte(prefixDay+day.UTC().Format(time.DateOnly)), []byte("1"))
	})
}

// LastHarvestedDay returns the newest recorded day checkpoint.
func (s *Store) LastHarvestedDay(ctx context.Context) (time.Time, bool, error) {
	var newest string
	// Forward scan rather than Reverse+Prefix: there are at most a few hundred
	// day keys ever, and combining Reverse with Prefix is an easy way to get an
	// empty iteration.
	err := s.db.View(func(tx *badger.Txn) error {
		ops := badger.DefaultIteratorOptions
		ops.PrefetchValues = false
		ops.Prefix = []byte(prefixDay)
		it := tx.NewIterator(ops)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			if k := strings.TrimPrefix(string(it.Item().Key()), prefixDay); k > newest {
				newest = k
			}
		}
		return nil
	})
	if err != nil || newest == "" {
		return time.Time{}, false, err
	}
	t, err := time.Parse(time.DateOnly, newest)
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}

// SetMeta stores an arbitrary marker.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	return s.db.Update(func(tx *badger.Txn) error {
		return tx.Set([]byte(prefixMeta+key), []byte(value))
	})
}

// GetMeta reads an arbitrary marker.
func (s *Store) GetMeta(ctx context.Context, key string) (string, bool, error) {
	var out string
	var found bool
	err := s.db.View(func(tx *badger.Txn) error {
		item, err := tx.Get([]byte(prefixMeta + key))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		v, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		out, found = string(v), true
		return nil
	})
	return out, found, err
}

// Sha256Hex is the content hash used for change detection and deduplication.
func Sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
