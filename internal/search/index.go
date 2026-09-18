package search

import (
	"errors"
	"sort"
	"time"
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
	formatVersion = 2
)

// builder accumulates postings.
//
// Build holds the whole corpus's term map in memory, which is fine for a measured
// subset and is the first thing that has to change for the full mirror: the
// full-corpus map is gigabytes, so the shipped build is a two-pass sort-merge
// (count to a temp file, then emit sorted terms) rather than a map.
type builder struct {
	raw   map[string]map[uint32]byte
	fold  map[string]map[uint32]byte
	docs  []DocMeta
	units []UnitMeta
}

// Build indexes units in memory.
func Build(docs []DocMeta, units []Unit, opts Options) (*Index, error) {
	b := &builder{
		raw:   map[string]map[uint32]byte{},
		fold:  map[string]map[uint32]byte{},
		docs:  docs,
		units: make([]UnitMeta, 0, len(units)),
	}
	var totalLen uint64
	for i, u := range units {
		b.units = append(b.units, UnitMeta{Doc: u.Doc, Addr: u.Addr, Len: len([]rune(u.Text))})
		totalLen += uint64(len([]rune(u.Text)))

		tf := map[string]int{}
		for _, tok := range Tokenize(u.Text) {
			tf[tok]++
		}
		for term, count := range tf {
			addPosting(b.raw, term, uint32(i), count)
			if opts.Folded {
				if folded := Fold(term); folded != "" {
					addPosting(b.fold, folded, uint32(i), count)
				}
			}
		}
	}
	if len(b.units) == 0 {
		return nil, errors.New("no units to index: refusing to build an empty index")
	}
	idx := &Index{Docs: docs, Units: b.units, Flags: opts.Flags(), B: bm25B, Built: time.Now().Unix(), avgUnitLen: float64(totalLen) / float64(len(b.units))}
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

// collect flattens the per-stream maps into sorted term/posting slices.
func (idx *Index) collect(b *builder, opts Options) {
	type entry struct {
		term     string
		stream   byte
		postings [][]Posting
	}
	all := make([]entry, 0, len(b.raw)+len(b.fold))
	flatten := func(m map[string]map[uint32]byte, stream byte) {
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
			all = append(all, entry{term: term, stream: stream, postings: [][]Posting{postings}})
		}
	}
	flatten(b.raw, StreamRaw)
	if opts.Folded {
		flatten(b.fold, StreamFolded)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].term != all[j].term {
			return all[i].term < all[j].term
		}
		return all[i].stream < all[j].stream
	})
	idx.Terms = make([]string, len(all))
	idx.Stream = make([]byte, len(all))
	idx.Postings = make([][]Posting, len(all))
	for i, e := range all {
		idx.Terms[i] = e.term
		idx.Stream[i] = e.stream
		idx.Postings[i] = e.postings[0]
	}
}
