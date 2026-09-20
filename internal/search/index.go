package search

import (
	"context"
	"errors"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

// Index is a term-to-unit inverted index over the mirror.
//
// Postings point at units (a section, a title, or a whole document with no
// sections), never at documents, because the answer has to name a provision.
//
// The file stores no text. Unit text lives in the archive and a snippet is rendered
// by re-reading the document at query time; the alternative would duplicate 12.6 GB
// of legal text into a derived file that then needs its own invalidation story. Ten
// documents re-parsed per query is a few tens of milliseconds, and the measurement
// records whether that is true rather than assuming it.
type Index struct {
	Docs  []DocMeta
	Units []UnitMeta

	// Terms is sorted, and Postings[i] belongs to Terms[i]. Both are derived from
	// the file on load; the sorted order is what makes prefix queries a range scan.
	Terms    []string
	Postings [][]Posting

	// Stream records which token stream each term came from, so a folded-only match
	// can rank below a raw one instead of being trusted identically.
	Stream []byte

	// Flags records which token streams the file contains, so a query can tell
	// "this index has no folded stream" from "this query found no fold match".
	Flags uint64

	// B is BM25's length-normalisation strength. It is a field rather than a
	// constant because the measured answer depends on unit granularity: whole-document
	// units in folketingssager are long by definition, and a strong penalty sinks the
	// exact units a citation query needs. The sweep in measure_test.go picks the value.
	B float64

	// Built is when this index was generated, in Unix seconds. It travels in the file
	// because "how current is this" is a property of the index, not of the process
	// reading it: a server that started a minute ago and loads a month-old index must
	// not report its own start time as the index's age.
	Built int64

	avgUnitLen float64
}

// UnitMeta is one indexable span, with just enough to render a hit.
type UnitMeta struct {
	Doc  int
	Addr string
	Len  int // runes, for length-normalised ranking
}

// Posting is one term's appearance in one unit. Tf saturates at 255: a section that
// repeats a word 300 times is not ten times more relevant than one that repeats it
// thirty, and the ranking function already flattens that curve.
type Posting struct {
	Unit uint32
	Tf   byte
}

// Options controls which streams a build writes.
//
// Folding is the only optional stream that survives: it buys inflectional recall
// ("pligt" reaching "pligtige") for about 38 % more index. A reversed-term stream was
// built and measured, and it lost — see testdata/index-measurement-20260917.txt. It
// cost 62 % more space and made recall *worse* (aa-spelling 0.75 to 0.25, citation
// 0.50 to 0.05), because reversed terms share the sorted dictionary and take
// prefix-expansion slots away from real ones. Compound-head queries did not need it:
// they already recalled 1.000 from exact terms plus right-extension.
type Options struct {
	Folded bool
}

// Flags is the on-disk record of which streams were built, so a query can tell
// "this index has no folded stream" from "this query has no fold match".
func (o Options) Flags() uint64 {
	var f uint64
	if o.Folded {
		f |= 1
	}
	return f
}

const (
	magic = "RTSINIDX1"

	// formatVersion is 2. Version 1 had no scoring parameter and no build time in its
	// header, so every table behind it starts eight bytes earlier.
	// formatVersion 3 adds the register's status and document-type code to each document
	// row. The rows are positional, so an old file cannot be read as a new one: bumping the
	// number is what turns "this index lacks the field you are filtering on" from a wrong
	// answer into an error that names the command to run.
	formatVersion = 3
)

// builder accumulates postings.
//
// The term dictionary is partitioned into 256 shards by the first byte of the term,
// which buys two things at once. Counting runs on worker goroutines, because a
// whole-corpus build spends most of its time tokenizing 5.3 million provisions and
// one goroutine reading the mirror at 30 MB/s was the bottleneck. And the sorted
// dictionary the file format needs comes out for free: every term in shard b starts
// with byte b, so flattening the shards in increasing b and sorting within a shard is
// a global byte order — no merge step, and no dependence on Go's map iteration order.
//
// The old build held one map for the whole corpus, which is fine for a measured subset
// and is the reason a full build peaks at 26.4 GB; sharding by term keeps each term in
// exactly one map, so the parallel build costs the same memory rather than one map per
// worker. A two-pass sort-merge build remains the known follow-up for smaller machines
// (see the README's search section).
type builder struct {
	shards [dictShards]dictShard
	docs   []DocMeta
	units  []UnitMeta
}

const dictShards = 256

// dictShard is one byte-range of the dictionary. One mutex per shard rather than a
// per-term lock: a term's postings are touched by whichever worker holds that unit, and
// 256 locks spread a corpus of 4.5 million terms thinly enough that the contention
// measured below is in the noise.
type dictShard struct {
	mu   sync.Mutex
	raw  map[string]map[uint32]byte
	fold map[string]map[uint32]byte
}

// add records one term occurrence. key selects the shard, and the key that selects it
// is always the key stored under it — the folded stream is routed by the folded term's
// own first byte, not the raw one's, or the ordering above would be a lie.
func (b *builder) add(key string, stream byte, unit uint32, count int) {
	if key == "" {
		return
	}
	s := &b.shards[key[0]]
	s.mu.Lock()
	if stream == StreamRaw {
		addPosting(s.raw, key, unit, count)
	} else {
		addPosting(s.fold, key, unit, count)
	}
	s.mu.Unlock()
}

// Build indexes units in memory, counting from workers.
func Build(ctx context.Context, docs []DocMeta, units []Unit, opts Options) (*Index, error) {
	if len(units) == 0 {
		return nil, errors.New("no units to index: refusing to build an empty index")
	}
	b := &builder{docs: docs, units: make([]UnitMeta, len(units))}
	for i := range b.shards {
		b.shards[i].raw = map[string]map[uint32]byte{}
		if opts.Folded {
			b.shards[i].fold = map[string]map[uint32]byte{}
		}
	}

	var totalLen atomic.Uint64
	lexdania.Parallel(ctx, len(units), func(i int) {
		u := &units[i]
		l := len([]rune(u.Text))
		b.units[i] = UnitMeta{Doc: u.Doc, Addr: u.Addr, Len: l}
		totalLen.Add(uint64(l))

		tf := map[string]int{}
		for _, tok := range Tokenize(u.Text) {
			tf[tok]++
		}
		for term, count := range tf {
			b.add(term, StreamRaw, uint32(i), count)
			if opts.Folded {
				if folded := Fold(term); folded != "" {
					b.add(folded, StreamFolded, uint32(i), count)
				}
			}
		}
	})

	idx := &Index{Docs: docs, Units: b.units, Flags: opts.Flags(), B: bm25B, Built: time.Now().Unix(), avgUnitLen: float64(totalLen.Load()) / float64(len(b.units))}
	idx.collect(b, opts)
	return idx, nil
}

func addPosting(into map[string]map[uint32]byte, term string, unit uint32, count int) {
	if count > 255 {
		count = 255
	}
	per := into[term]
	if per == nil {
		per = map[uint32]byte{}
		into[term] = per
	}
	// Saturating byte counts would otherwise let a 300-occurrence unit look like a
	// 45-occurrence one; the max is the honest compression here.
	if v := per[unit]; v < byte(count) {
		per[unit] = byte(count)
	}
}

// dictEntry is one (term, stream) row before the tables are assembled.
type dictEntry struct {
	term     string
	stream   byte
	postings []Posting
}

// flatten turns one shard into its sorted rows: every term in it shares a first byte,
// so sorting inside the shard is enough to make the concatenation over shards sorted.
func (b *builder) flatten(id byte, folded bool) []dictEntry {
	s := &b.shards[id]
	var out []dictEntry
	appendStream := func(m map[string]map[uint32]byte, stream byte) {
		for term, per := range m {
			units := make([]uint32, 0, len(per))
			for u := range per {
				units = append(units, u)
			}
			sort.Slice(units, func(i, j int) bool { return units[i] < units[j] })
			postings := make([]Posting, 0, len(units))
			for _, u := range units {
				postings = append(postings, Posting{Unit: u, Tf: per[u]})
			}
			out = append(out, dictEntry{term: term, stream: stream, postings: postings})
		}
	}
	appendStream(s.raw, StreamRaw)
	if folded {
		appendStream(s.fold, StreamFolded)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].term != out[j].term {
			return out[i].term < out[j].term
		}
		return out[i].stream < out[j].stream
	})
	return out
}

// collect flattens the shards into the sorted term, stream and posting tables.
//
// Shards are flattened biggest first by whatever worker is free: the dictionary is
// steeply skewed (the byte that starts the most Danish legal words carries a large
// share of the terms), so handing out equal ranges of shard numbers would have one
// worker still sorting while the rest idled.
func (idx *Index) collect(b *builder, opts Options) {
	folded := opts.Folded
	order := make([]int, dictShards)
	for i := range order {
		order[i] = i
	}
	size := func(i int) int {
		s := &b.shards[i]
		n := len(s.raw)
		if folded {
			n += len(s.fold)
		}
		return n
	}
	sort.Slice(order, func(i, j int) bool { return size(order[i]) > size(order[j]) })

	rows := make([][]dictEntry, dictShards)
	workers := runtime.GOMAXPROCS(0)
	if workers > dictShards {
		workers = dictShards
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				k := int(next.Add(1)) - 1
				if k >= len(order) {
					return
				}
				rows[order[k]] = b.flatten(byte(order[k]), folded)
			}
		}()
	}
	wg.Wait()

	total := 0
	for _, r := range rows {
		total += len(r)
	}
	idx.Terms = make([]string, 0, total)
	idx.Stream = make([]byte, 0, total)
	idx.Postings = make([][]Posting, 0, total)
	for id := range rows { // shard order, which is term order
		for _, e := range rows[id] {
			idx.Terms = append(idx.Terms, e.term)
			idx.Stream = append(idx.Stream, e.stream)
			idx.Postings = append(idx.Postings, e.postings)
		}
	}
}
