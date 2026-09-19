package search

import (
	"sort"
	"strings"
)

// The query language.
//
// A plain query keeps its old meaning: coverage-weighted OR, because a strict AND over a
// corpus this repetitive returns nothing for most sentences people actually type, and a
// silent empty result is worse than ranking a partial match below a full one.
//
// Operators are for the case where the user knows better and says so:
//
//	+huske AND -lejlighed     both required, one forbidden
//	bilag AND notering        both required
//	bilag OR notering         explicit union (the default anyway)
//	NOT lejlighed             exclusion on its own
//	"ubetinget råderet"       every word required, in one unit
//
// The operators are ASCII on purpose. Danish "og" is the ordinary word for "and" and sits
// inside phrases people search for ("erhvervsdrivende og fonde"), so reading it as an
// operator would silently change the meaning of the most common kind of query to fix a
// shorthand nobody asked for.
//
// What is deliberately NOT here yet: adjacency. A quoted span requires its words in the same
// unit, not side by side, and saying "phrase" while meaning "co-occurrence" would be the
// kind of small lie a search box repeats forever. Word positions are not stored — they were
// dropped when the index was built because every phrase query's top ten already contained
// the words adjacent — so adjacency is a measured decision, not an omission.
type operator int

const (
	opOr operator = iota
	opAnd
	opNot
)

// operand is one word of the query with the operator it carries.
type operand struct {
	word string
	op   operator

	// phrase marks a word that came from a quoted span, i.e. the asker wrote the words
	// together and meant them together. It changes what distance means: see nearness.go.
	phrase bool
}

// parseQuery splits a query into its operators. It never fails: text it cannot parse is
// words, and words are a plain OR query, which is the query that existed before operators.
//
// A quoted span requires its words in the same unit. It does not claim adjacency: word
// positions are not in the index, and a box that says "phrase" while meaning "same provision"
// would be telling a small lie on every answer forever. Adjacency is what the proximity grade
// in nearness.go then measures on the text the caller reads anyway, and it reports the gap it
// saw rather than asserting one.
func parseQuery(text string) []operand {
	var out []operand
	// pending is the operator the next word inherits, set by AND/OR/NOT.
	pending := opOr
	// inQuote spans fields: "offentlige veje" arrives as two fields and means one thing.
	inQuote := false
	// prevStart is where the previous field's tokens began, so that "a AND b" can
	// upgrade a as well as b: AND joins two things, and requiring only the right-hand one
	// would silently answer an OR query while telling the user it was an AND.
	prevStart := 0
	for _, f := range strings.Fields(text) {
		kw := strings.ToUpper(strings.Trim(f, "()"))
		switch kw {
		case "AND", "ANDNOT":
			for i := prevStart; i < len(out); i++ {
				if out[i].op == opOr {
					out[i].op = opAnd
				}
			}
			// "and not" joins on the left and excludes on the right.
			pending = opAnd
			if kw == "ANDNOT" {
				pending = opNot
			}
			continue
		case "OR":
			pending = opOr
			continue
		case "NOT":
			// A negation with no word after it excludes nothing and is not worth an
			// error: the query still means whatever it meant before.
			pending = opNot
			continue
		}
		op := pending
		pending = opOr
		switch {
		case strings.HasPrefix(f, "+") && len(f) > 1:
			op, f = opAnd, f[1:]
		case strings.HasPrefix(f, "-") && len(f) > 1:
			op, f = opNot, f[1:]
		}
		quoted := inQuote
		if len(f) > 0 {
			if f[0] == '"' || f[0] == '\'' {
				quoted = true
			}
			last := f[len(f)-1]
			if last == '"' || last == '\'' {
				if !inQuote {
					// The span opened and closed in one field, or never opened at all.
					quoted = len(f) > 1 && (f[0] == '"' || f[0] == '\'')
				}
				inQuote = false
			} else if quoted && !inQuote {
				inQuote = true
			}
		}
		if quoted && op == opOr {
			op = opAnd
		}
		start, end := 0, len(f)
		if len(f) > 0 && (f[0] == '"' || f[0] == '\'') {
			start = 1
			if end > start && (f[end-1] == '"' || f[end-1] == '\'') {
				end--
			}
		}
		prevStart = len(out)
		for _, w := range Tokenize(f[start:end]) {
			out = append(out, operand{word: w, op: op, phrase: quoted})
		}
	}
	return out
}

// queryAsksForPhrase reports whether any word of the query came from a quoted span.
func queryAsksForPhrase(text string) bool {
	for _, o := range parseQuery(text) {
		if o.phrase {
			return true
		}
	}
	return false
}

// unitSet is a set of unit numbers, used for the required intersection and the exclusion
// difference. A sorted slice rather than a map keeps the working set proportional to the
// postings it came from, which for a required term in a 5.5 million unit index is the
// difference between a bounded intersection and a large allocation.
type unitSet []uint32

func newUnitSet() unitSet {
	return nil
}

func (s unitSet) has(u uint32) bool {
	i := sort.Search(len(s), func(i int) bool { return s[i] >= u })
	return i < len(s) && s[i] == u
}

// addRecords inserts, keeping the slice sorted. Postings arrive in unit order per term, so
// appends are cheap and the final sort is what makes intersection possible.
func (s *unitSet) addMany(us ...uint32) {
	*s = append(*s, us...)
}

// sortedUnitSet returns the postings' units as a sorted set with duplicates removed, which
// is what intersection and membership need.
func sortedUnitSet(terms []termMatch) unitSet {
	var s unitSet
	for _, t := range terms {
		for _, p := range t.posting {
			s = append(s, p.Unit)
		}
	}
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	out := s[:0]
	for i, u := range s {
		if i == 0 || s[i-1] != u {
			out = append(out, u)
		}
	}
	return out
}

// intersect keeps the units present in every set.
func intersect(sets ...unitSet) unitSet {
	if len(sets) == 0 {
		return nil
	}
	base := sets[0]
	for _, s := range sets[1:] {
		out := make(unitSet, 0, min(len(base), len(s)))
		i, j := 0, 0
		for i < len(base) && j < len(s) {
			switch {
			case base[i] == s[j]:
				out = append(out, base[i])
				i++
				j++
			case base[i] < s[j]:
				i++
			default:
				j++
			}
		}
		base = out
		if len(base) == 0 {
			return nil
		}
	}
	return base
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ExcludedWords returns the words a query forbids, in the spelling the reader typed. The index
// applies exclusions to the unit it matched; this serves the reader, who is shown a passage
// rather than a unit and has no way to tell the two apart.
func ExcludedWords(text string) []string {
	out := make([]string, 0, 4)
	for _, o := range parseQuery(text) {
		if o.op == opNot && o.word != "" {
			out = append(out, o.word)
		}
	}
	return out
}
