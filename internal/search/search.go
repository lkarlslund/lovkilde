package search

import (
	"math"
	"sort"
	"strings"
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
	Doc   DocMeta
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
	posting []Posting
}

// Search ranks units against the query.
func (idx *Index) Search(q Query) []Hit {
	limit := q.Limit
	if limit <= 0 {
		limit = 10
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
		byWord[o.word] = idx.resolve(o.word, o.op)
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
	scored := map[uint32]*Hit{}
	perUnitTerms := map[uint32]map[string]bool{}
	sawRaw := map[uint32]bool{}
	nUnits := float64(len(idx.Units))

	// Exclusions are scored too, because a term that has to be absent still has to be found
	// before it can be removed; the units it names are then dropped.
	for _, w := range words {
		if opOf[w] == opNot {
			continue
		}
		for _, ch := range byWord[w] {
			df := float64(len(ch.posting))
			idf := math.Log(1 + (nUnits-df+0.5)/(df+0.5))
			for _, p := range ch.posting {
				if len(not) > 0 && not.has(p.Unit) {
					continue
				}
				if len(must) > 0 && !must.has(p.Unit) {
					continue
				}
				u := &idx.Units[p.Unit]
				if !idx.accept(u, q) {
					continue
				}
				tf := float64(p.Tf)
				b := idx.B
				tfPart := tf * (bm25K1 + 1) / (tf + bm25K1*(1-b+b*float64(u.Len)/idx.avgUnitLen))
				h := scored[p.Unit]
				if h == nil {
					h = &Hit{Unit: int(p.Unit), Doc: idx.Docs[u.Doc], Addr: u.Addr, Gap: -1}
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
	}

	// A required word must still be satisfied after the metadata filters have run, because
	// those filters take units out of the intersection: "kind=lta AND x" must not answer with
	// a unit that lost its required word to the kind filter.
	if len(must) > 0 {
		for unit := range scored {
			for _, w := range words {
				if opOf[w] == opAnd && !perUnitTerms[unit][w] {
					delete(scored, unit)
					break
				}
			}
		}
	}

	hits := make([]Hit, 0, len(scored))
	for unit, h := range scored {
		covered := 0
		for _, w := range words {
			if opOf[w] == opNot {
				continue
			}
			if perUnitTerms[unit][w] {
				covered++
			}
		}
		h.Matched = matchedFor(words, perUnitTerms[unit])
		h.Covered = covered
		h.Requested = total
		h.FoldedOnly = !sawRaw[unit]
		// Coverage multiplies rather than adds: a unit matching all three terms of a
		// three-term query should beat one matching a single very common term, and adding a
		// constant would let high-frequency noise win.
		h.Score *= 0.5 + 0.5*float64(covered)/float64(total)
		hits = append(hits, *h)
	}
	sortHits(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
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
		return hits[i].Doc.Accession < hits[j].Doc.Accession
	})
}

// resolve turns one query word into the dictionary terms that answer it: the exact term, or
// its right-extensions, plus the folded stem when the index carries that stream.
func (idx *Index) resolve(word string, op operator) []termMatch {
	var out []termMatch
	if i := idx.findTerm(word, StreamRaw); i >= 0 {
		out = append(out, termMatch{display: word, query: word, weight: 1, op: op, posting: idx.Postings[i]})
	} else {
		// No exact term: extend right. This is what lets "miljøbeskyttelse" reach
		// "miljøbeskyttelsesloven" without a second index.
		for _, i := range idx.prefixTerms(word, maxPrefixExpansions) {
			out = append(out, termMatch{display: idx.Terms[i], query: word, weight: prefixW, op: op, posting: idx.Postings[i]})
		}
	}
	if idx.HasFolded() {
		if stem := Fold(word); stem != "" {
			if i := idx.findTerm(stem, StreamFolded); i >= 0 {
				out = append(out, termMatch{display: stem, query: word, weight: foldWeight, folded: true, op: op, posting: idx.Postings[i]})
			}
		}
	}
	return out
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
	sort.Slice(found, func(a, b int) bool { return len(idx.Postings[found[a]]) > len(idx.Postings[found[b]]) })
	if len(found) > cap {
		found = found[:cap]
	}
	return found
}
