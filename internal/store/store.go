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
)

// Key prefixes. Single-character prefixes keep the keyspace small and make
// prefix scans cheap.
const (
	prefixInventory = "i|"
	prefixAccession = "a|"
	prefixChange    = "h|"
	prefixDay       = "H|"
	prefixMeta      = "m|"
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

// PutInventory upserts one inventory record.
func (s *Store) PutInventory(ctx context.Context, in Inventory) error {
	return s.db.Update(func(tx *badger.Txn) error {
		return tx.Set([]byte(prefixInventory+in.ELIURL), []byte(in.encode()))
	})
}

// PutInventoryBatch upserts many records in one Badger batch, which is the
// efficient path for the ~200k-row sitemap import.
func (s *Store) PutInventoryBatch(ctx context.Context, rows []Inventory) error {
	if len(rows) == 0 {
		return nil
	}
	b := s.db.NewWriteBatch()
	defer b.Cancel()
	for _, in := range rows {
		if err := b.Set([]byte(prefixInventory+in.ELIURL), []byte(in.encode())); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return b.Flush()
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
	counts := map[string]int64{}
	err = s.db.View(func(tx *badger.Txn) error {
		ops := badger.DefaultIteratorOptions
		ops.PrefetchValues = false
		ops.Prefix = []byte(prefixInventory)
		it := tx.NewIterator(ops)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			v, err := it.Item().ValueCopy(nil)
			if err != nil {
				return err
			}
			f := strings.Split(string(v), "\t")
			if len(f) < 3 {
				continue
			}
			if !sel.wantState(f[2]) || !sel.wantKind(f[1]) {
				continue
			}
			counts[f[1]]++
		}
		return nil
	})
	return counts, err
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
		case StatePending, StateFetched, StateFailed, StateSkipped, StateGone:
		default:
			return selector{}, fmt.Errorf("unknown state %q (valid: pending, fetched, failed, skipped, gone)", st)
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
		return state == string(StatePending) || state == string(StateFailed)
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
			ops.Prefix = []byte(prefixInventory)
		}
		it := tx.NewIterator(ops)
		defer it.Close()

		for it.Seek(sel.scanStart()); it.Valid(); it.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			item := it.Item()
			key := string(item.Key())
			if sel.reverse && !strings.HasPrefix(key, prefixInventory) {
				return nil // walked out of the bottom of the range
			}
			eliURL := strings.TrimPrefix(key, prefixInventory)
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
func (sel selector) scanStart() []byte {
	switch {
	case sel.reverse && sel.cursor != "":
		// Badger's reverse Seek lands at or below the target, so aiming at the
		// cursor itself starts the walk on its near side; pastCursor drops the row
		// the cursor names.
		return []byte(prefixInventory + sel.cursor)
	case sel.reverse:
		// 0xff cannot appear in an ELI URL, so this sits above every key with this
		// prefix and below the next one (the keyspace uses single-letter prefixes),
		// which is where a backwards walk with nothing covered yet should begin.
		return append([]byte(prefixInventory), 0xff, 0xff, 0xff, 0xff)
	case sel.cursor != "":
		return []byte(prefixInventory + sel.cursor)
	}
	return []byte(prefixInventory)
}

// PendingInventory returns up to limit resources the filter selects, in the order
// the filter asks for. A filtered call still walks the keyspace from the cursor --
// a few tens of milliseconds over the whole catalogue, which is noise next to the
// paced requests it feeds.
func (s *Store) PendingInventory(ctx context.Context, limit int, f Filter) ([]Inventory, error) {
	sel, err := f.selector()
	if err != nil {
		return nil, err
	}
	var out []Inventory
	err = s.scanInventory(ctx, sel, func(eliURL, value string) (bool, error) {
		in, err := decodeInventory(eliURL, value)
		if err != nil {
			return false, err
		}
		if !sel.wantState(string(in.State)) || !sel.wantAttempts(in.Attempts) || !sel.wantKind(in.Kind) {
			return false, nil
		}
		out = append(out, in)
		// Zero means unlimited here, as it does for --limit; comparing directly
		// would stop after the first row.
		return limit > 0 && len(out) >= limit, nil
	})
	return out, err
}

// PendingKeys returns the ELI URLs the filter selects, without decoding the rows.
// Order=random needs the whole selection in memory to shuffle it: paging a shuffled
// order is impossible from a keyspace that is stored in ELI URL order, and
// re-scanning per batch to re-derive it would put the quadratic walk back. At
// roughly 60 bytes per URL, the full catalogue costs a few tens of megabytes --
// paid once, on a run that asked for it.
func (s *Store) PendingKeys(ctx context.Context, f Filter) ([]string, error) {
	sel, err := f.selector()
	if err != nil {
		return nil, err
	}
	var out []string
	err = s.scanInventory(ctx, sel, func(eliURL, value string) (bool, error) {
		// Decoding the row costs nothing extra -- the value has to be read to apply
		// the filter either way -- and going through decodeInventory keeps this
		// selection identical to PendingInventory's rather than a second parse.
		in, err := decodeInventory(eliURL, value)
		if err != nil {
			return false, err
		}
		if !sel.wantState(string(in.State)) || !sel.wantAttempts(in.Attempts) || !sel.wantKind(in.Kind) {
			return false, nil
		}
		out = append(out, eliURL)
		return false, nil
	})
	return out, err
}

// Inventories returns the rows for specific ELI URLs, in the order given, skipping
// any that have disappeared. It is the point-lookup path Order=random uses after
// shuffling, instead of re-scanning the keyspace per batch.
func (s *Store) Inventories(ctx context.Context, eliURLs []string) ([]Inventory, error) {
	var out []Inventory
	err := s.db.View(func(tx *badger.Txn) error {
		for _, u := range eliURLs {
			if err := ctx.Err(); err != nil {
				return err
			}
			item, err := tx.Get([]byte(prefixInventory + u))
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
			in, err := decodeInventory(u, string(v))
			if err != nil {
				return err
			}
			out = append(out, in)
		}
		return nil
	})
	return out, err
}

// CountByState tallies inventory records per state using a key-only scan.
func (s *Store) CountByState(ctx context.Context) (map[State]int64, int64, error) {
	counts := map[State]int64{}
	var total int64
	err := s.db.View(func(tx *badger.Txn) error {
		ops := badger.DefaultIteratorOptions
		ops.PrefetchValues = false
		ops.Prefix = []byte(prefixInventory)
		it := tx.NewIterator(ops)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			v, err := it.Item().ValueCopy(nil)
			if err != nil {
				return err
			}
			f := strings.Split(string(v), "\t")
			st := State("pending")
			if len(f) > 2 && f[2] != "" {
				st = State(f[2])
			}
			counts[st]++
			total++
		}
		return nil
	})
	return counts, total, err
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
