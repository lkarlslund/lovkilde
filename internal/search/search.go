package search

import (
	"math"
	"sort"
	"strings"
)

// Query is one search.
//
// Terms are combined with coverage-weighted OR rather than strict AND, because a
// strict AND over a corpus this repetitive returns nothing for most queries people
// actually type, and silently returning nothing is worse than ranking a partial
// match below a full one. Coverage is reported on every hit so a caller can say
// "this matched 2 of 3 terms" instead of the user guessing.
type Query struct {
	Text string

	// Kind, MinYear, MaxYear and Source narrow the result set. Source is the
	// provenance filter — an agent that must quote canonical text can ask for
	// xml-only hits and get an honest empty result rather than HTML text it has to
	// notice was HTML.
	Kind      string
	MinYear   int
	MaxYear   int
	Source    string
	MinDecade int
	MaxDecade int

	Limit int
}

// Hit is one matching unit.
type Hit struct {
	// Unit is the index of the unit within Index.Units, which is what snippet
	// rendering and the measurement both need: a hit that cannot be traced back to
	// the text it matched is a claim, not a result.
	Unit  int
	Doc   DocMeta
	Addr  string
	Score float64

	// Matched lists the query terms found in this unit, in query order.
	Matched []string

	// FoldedOnly marks a hit whose matches came only from the folded stream, i.e.
	// the exact word the user typed does not appear, an inflected form does. That is
	// useful and has to be visible, because a quotable answer should say which word
	// it actually found.
	FoldedOnly bool
}

// maxPrefixExpansions bounds how many dictionary terms a single query word may
// expand to.
//
// Danish compounds mean "ret" is a prefix of thousands of terms, and scanning all of
// them per query would make search latency depend on how short the word is. The
// cap takes the highest-frequency expansions, which is the useful end of the range.
const maxPrefixExpansions = 12

const (
	bm25K1     = 1.2
	foldWeight = 0.5
	prefixW    = 0.7

	// bm25B is BM25's length normalisation, and the measured value for this corpus is
	// zero. The textbook 0.75 was swept against real queries; see
	// testdata/index-measurement-20260917.txt. Citation recall was 0.500 at b=0.75,
	// 0.45 and 0.20 and 1.000 at b=0, with term (0.700 to 0.740) and phrase (0.967 to
	// 1.000) also improving. The reason is the unit design: a document with no numbered
	// sections indexes as one long unit, and that unit is exactly what a citation query
	// like "lov nr. 1399" needs, so penalising length penalised the right answer.
	//
	// Zero is not "no ranking": idf, term frequency and coverage still order results.
	bm25B = 0.0
)

// Search ranks units against the query.
func (idx *Index) Search(q Query) []Hit {
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}
	terms := Tokenize(q.Text)
	if len(terms) == 0 {
		return nil
	}

	type termHits struct {
		// display is the dictionary term that matched, for reporting.
		display string
		// query is the query token it answers, so coverage and Matched are counted in
		// the words the user typed rather than in the stems or compounds found.
		query   string
		weight  float64
		folded  bool
		posting []Posting
	}
	matchedTerms := make([]string, 0, len(terms))
	seen := map[string]bool{}
	candidates := make([]termHits, 0, len(terms))

	for _, tok := range terms {
		if seen[tok] {
			continue
		}
		seen[tok] = true
		found := false
		if i := idx.findTerm(tok, StreamRaw); i >= 0 {
			candidates = append(candidates, termHits{display: tok, query: tok, weight: 1, posting: idx.Postings[i]})
			matchedTerms = append(matchedTerms, tok)
			found = true
		}
		if !found {
			// No exact term: extend right. This is what lets "miljøbeskyttelse" reach
			// "miljøbeskyttelsesloven" without a second index.
			for _, i := range idx.prefixTerms(tok, maxPrefixExpansions) {
				candidates = append(candidates, termHits{display: idx.Terms[i], query: tok, weight: prefixW, posting: idx.Postings[i]})
				found = true
			}
		}
		if idx.HasFolded() {
			if stem := Fold(tok); stem != "" {
				if i := idx.findTerm(stem, StreamFolded); i >= 0 {
					candidates = append(candidates, termHits{display: stem, query: tok, weight: foldWeight, folded: true, posting: idx.Postings[i]})
					matchedTerms = append(matchedTerms, tok)
					found = true
				}
			}
		}
		if !found {
			matchedTerms = append(matchedTerms, "") // keep position so coverage counts align
		}
	}

	total := len(terms)
	scored := map[uint32]*Hit{}
	perUnitTerms := map[uint32]map[string]bool{}
	sawRaw := map[uint32]bool{}
	nUnits := float64(len(idx.Units))

	for _, ch := range candidates {
		df := float64(len(ch.posting))
		idf := math.Log(1 + (nUnits-df+0.5)/(df+0.5))
		for _, p := range ch.posting {
			u := &idx.Units[p.Unit]
			if !idx.accept(u, q) {
				continue
			}
			tf := float64(p.Tf)
			b := idx.B
			tfPart := tf * (bm25K1 + 1) / (tf + bm25K1*(1-b+b*float64(u.Len)/idx.avgUnitLen))
			h := scored[p.Unit]
			if h == nil {
				h = &Hit{Unit: int(p.Unit), Doc: idx.Docs[u.Doc], Addr: u.Addr}
				scored[p.Unit] = h
				perUnitTerms[p.Unit] = map[string]bool{}
			}
			h.Score += idf * tfPart * ch.weight
			perUnitTerms[p.Unit][ch.query] = true
			if !ch.folded {
				sawRaw[p.Unit] = true
			}
		}
	}

	hits := make([]Hit, 0, len(scored))
	for unit, h := range scored {
		covered := 0
		for _, t := range matchedTerms {
			if t == "" {
				continue
			}
			if perUnitTerms[unit][t] {
				covered++
			}
		}
		h.Matched = matchedFor(terms, perUnitTerms[unit])
		h.FoldedOnly = !sawRaw[unit]
		// Coverage multiplies rather than adds: a unit matching all three terms of a
		// three-term query should beat one matching a single very common term, and
		// adding a constant would let high-frequency noise win.
		h.Score *= 0.5 + 0.5*float64(covered)/float64(total)
		hits = append(hits, *h)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Doc.Accession < hits[j].Doc.Accession
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// matchedFor lists which query terms this unit answered, in query order.
func matchedFor(terms []string, found map[string]bool) []string {
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		if found[t] {
			out = append(out, t)
		}
	}
	return out
}

// accept applies the metadata filters.
func (idx *Index) accept(u *UnitMeta, q Query) bool {
	d := idx.Docs[u.Doc]
	if q.Kind != "" && d.Kind != q.Kind {
		return false
	}
	if q.Source != "" && d.Source != q.Source {
		return false
	}
	if q.MinYear > 0 && d.Year < q.MinYear {
		return false
	}
	if q.MaxYear > 0 && d.Year > q.MaxYear {
		return false
	}
	if q.MinDecade > 0 && d.Year/10 < q.MinDecade {
		return false
	}
	if q.MaxDecade > 0 && d.Year/10 > q.MaxDecade {
		return false
	}
	return true
}

// findTerm binary-searches the dictionary for an exact term in one stream.
func (idx *Index) findTerm(term string, stream byte) int {
	i := sort.Search(len(idx.Terms), func(i int) bool {
		return idx.Terms[i] >= term
	})
	for ; i < len(idx.Terms) && idx.Terms[i] == term; i++ {
		if idx.Stream[i] == stream {
			return i
		}
	}
	return -1
}

// prefixTerms returns the terms starting with prefix, most frequent first up to the
// cap.
func (idx *Index) prefixTerms(prefix string, cap int) []int {
	start := sort.Search(len(idx.Terms), func(i int) bool {
		return idx.Terms[i] >= prefix
	})
	found := make([]int, 0, cap)
	for i := start; i < len(idx.Terms) && strings.HasPrefix(idx.Terms[i], prefix); i++ {
		if idx.Stream[i] != StreamRaw {
			continue
		}
		found = append(found, i)
		if len(found) >= cap*4 {
			// A very short prefix can run into thousands of terms; bound the scan and
			// rank within what was seen rather than walking the whole dictionary.
			break
		}
	}
	sort.Slice(found, func(a, b int) bool { return len(idx.Postings[found[a]]) > len(idx.Postings[found[b]]) })
	if len(found) > cap {
		found = found[:cap]
	}
	return found
}
