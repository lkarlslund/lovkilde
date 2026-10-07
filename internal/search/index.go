package search

import (
	"cmp"
	"context"
	"errors"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/lkarlslund/retsinformation/internal/parallel"
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

	// Ranking replaces DefaultRanking for this index when set: how a measurement tries a
	// variant without touching the one the server uses.
	Ranking *Ranking

	// titleDF counts, per word, how many document titles carry it; see titleIDF.
	titleOnce sync.Once
	titleDF   map[string]int

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

	// Source names what the index was built from: "artifact <its creation time>" for an index
	// cut from the compiled artifact, "mirror <directory>" for one read from the XML. A server
	// holding both files compares it with the artifact it serves, because an index and an
	// artifact from different builds disagree about which provisions exist.
	Source string

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
	// formatVersion 4 adds Source to the header: what the index was built from.
	formatVersion = 4
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
// Sharding by term keeps each term in exactly one map, so the parallel build costs the
// same memory as a serial one rather than one map per worker. What the build holds is
// postings only: BuildStream lets each batch's text go once it is counted, and postList
// keeps a posting in five bytes plus slack (see the README's search section for the
// profile that drove both).
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
	raw  map[string]*postList
	fold map[string]*postList
}

// postList is one term's postings while the build runs: units and counts in two arrays,
// appended in whatever order the workers reach them and sorted once at the end. A unit
// arrives at most once per term, because each unit's terms are counted before they are
// added, so nothing here has to look a unit up. It replaced a map from unit to count per
// term, which spent several times the five bytes a posting holds and was a third of a full
// build's live heap.
type postList struct {
	units []uint32
	tf    []byte
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

func newBuilder(opts Options) *builder {
	b := &builder{}
	for i := range b.shards {
		b.shards[i].raw = map[string]*postList{}
		if opts.Folded {
			b.shards[i].fold = map[string]*postList{}
		}
	}
	return b
}

// addUnit counts one unit's terms and adds its postings. The folded stream takes the
// highest count among the raw terms that fold together, as a saturating count would.
func (b *builder) addUnit(unit uint32, text string, folded bool) {
	tf := map[string]int{}
	for _, tok := range Tokenize(text) {
		tf[tok]++
	}
	var ftf map[string]int
	if folded {
		ftf = map[string]int{}
	}
	for term, count := range tf {
		b.add(term, StreamRaw, unit, count)
		if folded {
			if f := Fold(term); f != "" && count > ftf[f] {
				ftf[f] = count
			}
		}
	}
	for term, count := range ftf {
		b.add(term, StreamFolded, unit, count)
	}
}

// Build indexes units in memory, counting from workers.
func Build(ctx context.Context, docs []DocMeta, units []Unit, opts Options) (*Index, error) {
	if len(units) == 0 {
		return nil, errors.New("no units to index: refusing to build an empty index")
	}
	b := newBuilder(opts)
	b.docs, b.units = docs, make([]UnitMeta, len(units))

	var totalLen atomic.Uint64
	parallel.For(ctx, len(units), func(i int) {
		u := &units[i]
		l := utf8.RuneCountInString(u.Text)
		b.units[i] = UnitMeta{Doc: u.Doc, Addr: u.Addr, Len: l}
		totalLen.Add(uint64(l))
		b.addUnit(uint32(i), u.Text, opts.Folded)
	})

	idx := &Index{Docs: docs, Units: b.units, Flags: opts.Flags(), B: bm25B, Built: time.Now().Unix(), avgUnitLen: float64(totalLen.Load()) / float64(len(b.units))}
	idx.collect(b, opts)
	return idx, nil
}

func addPosting(into map[string]*postList, term string, unit uint32, count int) {
	// Counts saturate at 255: a section that repeats a word 300 times is not more
	// relevant than one that repeats it 255 times, and the ranking flattens that curve.
	if count > 255 {
		count = 255
	}
	pl := into[term]
	if pl == nil {
		pl = &postList{}
		// A token can be a slice of the unit's text (lower-casing returns its input when
		// there is nothing to lower), and a dictionary key that aliases it keeps the whole
		// provision alive for the rest of the build.
		into[strings.Clone(term)] = pl
	}
	pl.units = append(pl.units, unit)
	pl.tf = append(pl.tf, byte(count))
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
	appendStream := func(m map[string]*postList, stream byte) {
		for term, pl := range m {
			postings := make([]Posting, len(pl.units))
			for i, u := range pl.units {
				postings[i] = Posting{Unit: u, Tf: pl.tf[i]}
			}
			slices.SortFunc(postings, func(a, b Posting) int { return cmp.Compare(a.Unit, b.Unit) })
			out = append(out, dictEntry{term: term, stream: stream, postings: postings})
			delete(m, term)
		}
	}
	appendStream(s.raw, StreamRaw)
	if folded {
		appendStream(s.fold, StreamFolded)
	}
	// The shard's maps are spent: dropping them here, shard by shard, is what keeps the
	// builder and the finished tables from being whole at the same time.
	s.raw, s.fold = nil, nil
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
