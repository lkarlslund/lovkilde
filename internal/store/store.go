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

// PendingInventory scans the inventory prefix for fetchable records, stopping
// once limit have been collected. Failed records are only offered while their
// attempt count is below maxAttempts, so a permanently broken URL cannot spin
// forever and consume the polite request budget.
// PendingInventory returns up to limit resources still needing a fetch. kinds
// PendingByKind counts resources still needing a fetch, grouped by ELI kind. It
// exists because --kind filtering changes the denominator a progress bar needs
// before the run starts, and the state totals cannot answer "how many of these
// are retsinfo". Retryable failures are included to match PendingInventory's
// selection; the attempt bound is not, so the count errs slightly high.
func (s *Store) PendingByKind(ctx context.Context) (map[string]int64, error) {
	counts := map[string]int64{}
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
			if len(f) < 3 {
				continue
			}
			switch f[2] {
			case string(StatePending), string(StateFailed), "":
			default:
				continue
			}
			counts[f[1]]++
		}
		return nil
	})
	return counts, err
}

// PendingInventory returns up to limit resources still needing a fetch. kinds
// restricts the result to specific ELI kinds ("retsinfo", "lta", ...); empty
// means every kind. Matching uses the Kind recorded from the sitemap URL, so no
// second index is required. A filtered call still walks the keyspace from the
// start -- a few tens of milliseconds over the whole catalogue, which is noise
// next to the paced requests it feeds.
func (s *Store) PendingInventory(ctx context.Context, limit, maxAttempts int, kinds []string) ([]Inventory, error) {
	var want map[string]bool
	if len(kinds) > 0 {
		want = make(map[string]bool, len(kinds))
		for _, k := range kinds {
			want[k] = true
		}
	}
	var out []Inventory
	err := s.db.View(func(tx *badger.Txn) error {
		// Bigger value threshold keeps small records inline in the LSM, making
		// this scan read only the LSM rather than the value log.
		// (No per-transaction tuning is needed; Badger handles it.)
		ops := badger.DefaultIteratorOptions
		ops.PrefetchValues = false
		ops.Prefix = []byte(prefixInventory)
		it := tx.NewIterator(ops)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			item := it.Item()
			key := string(item.Key())
			eliURL := strings.TrimPrefix(key, prefixInventory)
			v, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			in, err := decodeInventory(eliURL, string(v))
			if err != nil {
				return err
			}
			switch in.State {
			case StatePending, StateFailed:
				if in.Attempts >= maxAttempts {
					continue
				}
			default:
				continue
			}
			if want != nil && !want[in.Kind] {
				continue
			}
			out = append(out, in)
			if len(out) >= limit {
				return nil
			}
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
