package search

import (
	"math"
	"slices"
	"sort"
	"strings"
	"unsafe"
)

// Query is one search.
//
// Plain terms combine with coverage-weighted OR, because a strict AND over a corpus this
// repetitive returns nothing for most queries people actually type, and silently returning
// nothing is worse than ranking a partial match below a full one. Coverage is reported on
// every hit so a caller can say "this matched 2 of 3 terms" instead of the user guessing.
//
// Where the caller means AND, they say so: see query.go for +term, -term, AND, OR and NOT.
// Those need nothing new in the index — the postings already support intersection and
// difference.
type Query struct {
	Text string

	// Kind, MinYear, MaxYear and Source narrow the result set. Source is the provenance
	// filter — an agent that must quote canonical text can ask for xml-only hits and get an
	// honest empty result rather than HTML text it has to notice was HTML.
	Kind string

	// Kinds, Statuses and Sources are the same filters as a set, for a reader who ticks several
	// boxes: a unit passes when its document matches any of them. They combine with Kind and
	// Status rather than replacing them, so a caller using one form is not surprised by the other.
	Kinds    []string
	Statuses []string
	Sources  []string

	// Accession keeps the search inside one document, which is how a reader looks for a
	// word in the law in front of them without leaving it.
	Accession string

	// Status filters on the register's own claim: "Valid", "Historic", or "unknown" for a
	// document whose payload does not say. The empty string means no opinion from the
	// caller. "unknown" has to be selectable, because a filter that silently drops the
	// documents the register says nothing about turns "only current law" into "only law
	// the register happens to label", and reports both as the same result.
	Status string

	// TypeCode is a substring of the publisher's document-type code, because the box beside
	// it searches titles by substring and a code field next to it that behaved differently
	// would be a trap. Substring rather than equality because the codes come in families
	// ("KEN#LOKDOK23") and the mirror has no name table to offer as a dropdown.
	TypeCode string

	MinYear   int
	MaxYear   int
	Source    string
	MinDecade int
	MaxDecade int

	Limit int

	// Near asks for proximity grading: hits whose words sit close together in the same
	// provision rank above hits that merely contain them. Grading happens after ranking, on
	// text the caller supplies, because the index stores no positions and the answer is
	// going to read that text anyway to quote it. See nearness.go.
	Near bool
}

// Hit is one matching unit.
type Hit struct {
	// Unit is the index of the unit within Index.Units, which is what snippet rendering and
	// the measurement both need: a hit that cannot be traced back to the text it matched is
	// a claim, not a result.
	Unit  int
	Doc   *DocMeta // the index's own record, shared by every hit in the document
	Addr  string
	Score float64

	// Matched lists the query terms found in this unit, in query order.
	Matched []string

	// Covered and Requested are what a boolean answer has to carry: "2 of 3" means one
	// thing when the missing word was optional and another when it was required.
	Covered   int
	Requested int

	// Gap is how many words apart the query words sat, when proximity was graded. Zero
	// means adjacent, -1 means the grading could not see the words (or was not asked for).
	// It is reported rather than folded silently into the score, because "these two words
	// appear next to each other" is the fact the user asked about.
	Gap int

	// FoldedOnly marks a hit whose matches came only from the folded stream, i.e. the exact
	// word the user typed does not appear, an inflected form does. That is useful and has to
	// be visible, because a quotable answer should say which word it actually found.
	FoldedOnly bool
}

// maxPrefixExpansions bounds how many dictionary terms a single query word may expand to.
//
// Danish compounds mean "ret" is a prefix of thousands of terms, and scanning all of them per
// query would make search latency depend on how short the word is. The cap takes the
// highest-frequency expansions, which is the useful end of the range.
const maxPrefixExpansions = 12

// commonShare is the share of all units above which a word only adds to hits found by rarer
// words. Two percent of the full index is about 120,000 provisions: "af" and "og" are far
// above it, "lejemål", "forældremyndighed" and "testamente" far below.
const commonShare = 0.02

const (
	bm25K1     = 1.2
	foldWeight = 0.5
	prefixW    = 0.7

	// bm25B is BM25's length normalisation, and the measured value for this corpus is zero.
	// The textbook 0.75 was swept against real queries; see
	// testdata/index-measurement-20260917.txt. Citation recall was 0.500 at b=0.75, 0.45 and
	// 0.20 and 1.000 at b=0, with term (0.700 to 0.740) and phrase (0.967 to 1.000) also
	// improving. The reason is the unit design: a document with no numbered sections indexes
	// as one long unit, and that unit is exactly what a citation query like "lov nr. 1399"
	// needs, so penalising length penalised the right answer.
	//
	// Zero is not "no ranking": idf, term frequency and coverage still order results.
	//
	// This is the value written into the index file. Serving ranks with DefaultRanking.B
	// (ranking.go), which judging against questions with known answers set to 0.2 once
	// document priors existed: that measurement was recall against grep, which cannot see
	// that a whole annex answering every query is the wrong answer to each of them. A
	// citation is now answered from the register's numbers instead (mcpserver.Cited).
	bm25B = 0.0
)

// termMatch is one dictionary term answering one query word, with the weight the answer
// deserves: an exact term full, a right-extension below that, a folded stem below again.
type termMatch struct {
	display string // the dictionary term that matched, for reporting
	query   string // the query word it answers, so coverage counts the user's words
	weight  float64
	folded  bool
	op      operator
	posting PostingList
}

// Search ranks units against the query and returns the best q.Limit of them.
func (idx *Index) Search(q Query) []Hit {
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}
	hits := idx.Rank(q)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// Rank is Search without the limit: every matching unit, best first. Its length is the honest
// total a results page reports, and a page further down is a slice of the same order, so
// scrolling never shows a hit twice or skips one.
func (idx *Index) Rank(q Query) []Hit {
	rk := DefaultRanking
	if idx.Ranking != nil {
		rk = *idx.Ranking
	}
	ops := parseQuery(q.Text)
	if len(ops) == 0 {
		return nil
	}

	// Resolve each distinct query word once, in query order so Matched reads as written.
	words := make([]string, 0, len(ops))
	byWord := map[string][]termMatch{}
	opOf := map[string]operator{}
	for _, o := range ops {
		if _, done := byWord[o.word]; done {
			// A word given twice keeps the stronger operator: in "a AND b -a" the
			// exclusion wins, and in "a AND b OR a" the requirement does, because
			// requiring is the reading that cannot silently widen the answer.
			if o.op == opNot {
				opOf[o.word] = opNot
			} else if o.op == opAnd && opOf[o.word] != opNot {
				opOf[o.word] = opAnd
			}
			continue
		}
		words = append(words, o.word)
		byWord[o.word] = idx.resolve(o.word, o.op, rk.Extend)
		opOf[o.word] = o.op
	}

	// Required words are the difference between an OR that ranks and an AND that answers: a
	// required word nothing in the index carries must produce nothing, not a list of near
	// misses dressed up as an answer.
	var required []unitSet
	var excluded []termMatch
	for _, w := range words {
		switch opOf[w] {
		case opAnd:
			if len(byWord[w]) == 0 {
				return nil
			}
			required = append(required, sortedUnitSet(byWord[w]))
		case opNot:
			excluded = append(excluded, byWord[w]...)
		}
	}
	must := intersect(required...)
	not := sortedUnitSet(excluded)

	// Requested counts the words that had to be present. An exclusion is not part of the
	// asking: "pligt AND virksomhed -lejlighed" requests two words and forbids a third, so
	// reporting "2 of 3" would tell the reader a required word went unfound when in fact the
	// forbidden one is why the hit is here at all.
	total := 0
	for _, w := range words {
		if opOf[w] != opNot {
			total++
		}
	}
	if total == 0 {
		// Nothing was asked for except absences. That is not a search this index can
		// answer — it would be every unit in the corpus minus some — so it says so.
		return nil
	}
	nUnits := float64(len(idx.Units))

	// A word that sits in a large share of all provisions — "af", "og", "med" — says
	// almost nothing about which provision is meant, and scoring every unit it appears in
	// is what made a three-word question take seconds: "opsigelse af lejemål" scored 2.2
	// million units for the sake of "af". Such a word only adds to units a rarer word has
	// already found, unless every word asked is that common. Rare words go first so the
	// units exist by the time the common ones arrive.
	df := map[string]int{}
	for _, w := range words {
		for _, ch := range byWord[w] {
			df[w] += ch.posting.Len()
		}
	}
	common := func(w string) bool { return float64(df[w]) > commonShare*nUnits }
	anyRare := false
	order := make([]string, 0, len(words))
	for _, w := range words {
		if opOf[w] == opNot {
			continue
		}
		order = append(order, w)
		if !common(w) {
			anyRare = true
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return df[order[i]] < df[order[j]] })

	// One row per unit a word reaches, found through row: units are a dense range, so a slice
	// indexed by unit does what a map would, and which words a unit carries is a bit per word.
	// A question of only common words reaches most of the corpus — "af og med" four of its six
	// million provisions — and a map entry, a hit and a set of words per unit took several
	// gigabytes and twelve seconds to answer it.
	stride := (len(words) + 63) / 64
	wordBit := make(map[string]int, len(words))
	for i, w := range words {
		wordBit[w] = i
	}
	row := make([]int32, len(idx.Units)) // 1 + the unit's row in rows, 0 for none
	var rows []scoreRow
	var carries []uint64 // stride words of bits per row
	has := func(r int, w string) bool {
		bit := wordBit[w]
		return carries[r*stride+bit/64]&(1<<(bit%64)) != 0
	}

	// Exclusions are scored too, because a term that has to be absent still has to be found
	// before it can be removed; the units it names are then dropped.
	for _, w := range order {
		boostOnly := anyRare && common(w)
		for _, ch := range byWord[w] {
			df := float64(ch.posting.Len())
			idf := math.Log(1 + (nUnits-df+0.5)/(df+0.5))
			bit := wordBit[ch.query]
			for p := range ch.posting.All() {
				if len(not) > 0 && not.has(p.Unit) {
					continue
				}
				if len(must) > 0 && !must.has(p.Unit) {
					continue
				}
				if boostOnly && row[p.Unit] == 0 {
					continue
				}
				u := &idx.Units[p.Unit]
				if !idx.accept(u, q) {
					continue
				}
				tf := float64(p.Tf)
				b := idx.B
				if rk.B >= 0 {
					b = rk.B
				}
				tfPart := tf * (bm25K1 + 1) / (tf + bm25K1*(1-b+b*float64(u.Len)/idx.avgUnitLen))
				r := int(row[p.Unit]) - 1
				if r < 0 {
					rows = append(rows, scoreRow{unit: p.Unit})
					carries = append(carries, make([]uint64, stride)...)
					r = len(rows) - 1
					row[p.Unit] = int32(len(rows))
				}
				h := &rows[r]
				h.score += idf * tfPart * ch.weight
				carries[r*stride+bit/64] |= 1 << (bit % 64)
				if !ch.folded {
					h.raw = true
				}
			}
		}
	}
	row = nil

	// A required word must still be satisfied after the metadata filters have run, because
	// those filters take units out of the intersection: "kind=lta AND x" must not answer with
	// a unit that lost its required word to the kind filter.
	if len(must) > 0 {
		for r := range rows {
			for _, w := range words {
				if opOf[w] == opAnd && !has(r, w) {
					rows[r].gone = true
					break
				}
			}
		}
	}

	// What the document is, how much of it answers, and whether its title names the subject:
	// see ranking.go for why words alone rank a law below its own paperwork.
	perDoc := map[uint32]int{}
	for _, h := range rows {
		if !h.gone {
			perDoc[idx.Units[h.unit].Doc]++
		}
	}
	// The title is judged on the words that say something: "lov om leje" is asking for leje.
	asked := make([]string, 0, len(words))
	for _, w := range order {
		if !common(w) {
			asked = append(asked, w)
		}
	}
	named, typed := namesDocument(words)
	titleW := rk.Title
	if named {
		titleW = rk.Named
	}
	docFactor := map[uint32]float64{}
	for doc, n := range perDoc {
		d := &idx.Docs[doc]
		f := rk.prior(d, typed) * rk.about(n)
		if titleW != 1 {
			if share := idx.titleShare(d.Title, asked); share > 0 {
				f *= math.Pow(titleW, share)
			}
		}
		docFactor[doc] = f
	}

	// Units that carry the same words share one Matched list: there are as many lists as
	// combinations of words, not as hits.
	matched := map[string][]string{}
	hits := make([]Hit, 0, len(rows))
	for r, h := range rows {
		if h.gone {
			continue
		}
		u := &idx.Units[h.unit]
		hit := Hit{Unit: int(h.unit), Doc: &idx.Docs[u.Doc], Addr: u.Addr, Gap: -1, Score: h.score}
		hit.Score *= docFactor[u.Doc]
		key := unsafe.String((*byte)(unsafe.Pointer(&carries[r*stride])), stride*8)
		m, ok := matched[key]
		if !ok {
			m = make([]string, 0, len(words))
			for _, w := range words {
				if has(r, w) {
					m = append(m, w)
				}
			}
			matched[strings.Clone(key)] = m
		}
		covered := 0
		for _, w := range words {
			if opOf[w] != opNot && has(r, w) {
				covered++
			}
		}
		hit.Matched = m
		hit.Covered = covered
		hit.Requested = total
		hit.FoldedOnly = !h.raw
		// Coverage multiplies rather than adds: a unit matching all three terms of a
		// three-term query should beat one matching a single very common term, and adding a
		// constant would let high-frequency noise win.
		hit.Score *= 0.5 + 0.5*float64(covered)/float64(total)
		hits = append(hits, hit)
	}
	sortHits(hits)
	return hits
}

// scoreRow is one unit's score while a query is ranked.
type scoreRow struct {
	score float64
	unit  uint32
	raw   bool // a word matched it in the raw stream, not only the folded one
	gone  bool // it lacks a required word
}

// queryWords lists the words of a query that a proximity grade should look for, exclusions
// apart: an excluded word is deliberately absent, so measuring distance to it is nonsense.
func (idx *Index) queryWords(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, o := range parseQuery(text) {
		if o.op == opNot || seen[o.word] {
			continue
		}
		seen[o.word] = true
		out = append(out, o.word)
	}
	return out
}

// sortHits orders by score, with the accession as the tie-break so two runs of the same query
// on the same index give the same list. Without a total order, map iteration decides the tail
// of the result and every answer becomes partly random.
//
// Gap deliberately does not participate: the proximity grade has already multiplied it into
// the score, and ordering unknown distance (-1) ahead of measured distance would be ranking a
// thing we could not look at above a thing we looked at and counted.
func sortHits(hits []Hit) {
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if a, b := hits[i].Doc.Accession, hits[j].Doc.Accession; a != b {
			return a < b
		}
		// The unit index is the only key that is unique per hit, and it has to be there:
		// provisions of one act share an accession, and two provisions that repeat a word
		// equally often in units of equal length score the same to the last bit. Search
		// collects hits by iterating a map, so without this the page a reader gets — and,
		// when the tie count exceeds the limit, which provisions are on it at all — varies
		// between two runs of the same query against the same index file.
		return hits[i].Unit < hits[j].Unit
	})
}

// resolve turns one query word into the dictionary terms that answer it: the exact term, or
// its right-extensions, plus the folded stem when the index carries that stream.
func (idx *Index) resolve(word string, op operator, extend bool) []termMatch {
	var out []termMatch
	if i := idx.findTerm(word, StreamRaw); i >= 0 {
		out = append(out, termMatch{display: word, query: word, weight: 1, op: op, posting: idx.Postings(i)})
		// The word exists, but so may its inflections and compounds; a short word is left
		// alone, because "lov" extended is half the dictionary.
		if extend && len([]rune(word)) >= 5 {
			for _, j := range idx.prefixTerms(word, maxPrefixExpansions) {
				if j != i {
					out = append(out, termMatch{display: idx.Terms[j], query: word, weight: prefixW, op: op, posting: idx.Postings(j)})
				}
			}
		}
	} else {
		// No exact term: extend right. This is what lets "miljøbeskyttelse" reach
		// "miljøbeskyttelsesloven" without a second index.
		for _, i := range idx.prefixTerms(word, maxPrefixExpansions) {
			out = append(out, termMatch{display: idx.Terms[i], query: word, weight: prefixW, op: op, posting: idx.Postings(i)})
		}
	}
	if idx.HasFolded() {
		if stem := Fold(word); stem != "" {
			if i := idx.findTerm(stem, StreamFolded); i >= 0 {
				out = append(out, termMatch{display: stem, query: word, weight: foldWeight, folded: true, op: op, posting: idx.Postings(i)})
			}
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
	if q.Accession != "" && d.Accession != q.Accession {
		return false
	}
	if len(q.Sources) > 0 && !slices.Contains(q.Sources, d.Source) {
		return false
	}
	if len(q.Kinds) > 0 && !slices.Contains(q.Kinds, d.Kind) {
		return false
	}
	if len(q.Statuses) > 0 && !slices.ContainsFunc(q.Statuses, func(s string) bool { return statusKey(s) == statusKey(d.Status) }) {
		return false
	}
	if q.Status != "" && statusKey(d.Status) != statusKey(q.Status) {
		return false
	}
	if q.TypeCode != "" && !strings.Contains(d.TypeCode, q.TypeCode) {
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

// statusKey folds the ways a caller may name the register's status into one form, so that
// "valid", "Valid" and "gældende" all mean what the payload says. "unknown" and "" mean the
// same thing: the register did not say.
func statusKey(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "valid", "gældende", "gaeldende":
		return "Valid"
	case "historic", "historisk", "ophævet", "ophaevet":
		return "Historic"
	case "", "unknown", "ukendt":
		return ""
	}
	return s
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

// prefixTerms returns the terms starting with prefix, most frequent first up to the cap.
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
			// A very short prefix can run into thousands of terms; bound the scan and rank
			// within what was seen rather than walking the whole dictionary.
			break
		}
	}
	sort.Slice(found, func(a, b int) bool { return idx.postingN[found[a]] > idx.postingN[found[b]] })
	if len(found) > cap {
		found = found[:cap]
	}
	return found
}
