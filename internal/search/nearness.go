package search

import "strings"

// Proximity grading without positions in the index.
//
// The index deliberately stores no word positions: they were measured at 62 MB of a 741 MB
// index with no recall mechanism to pay for them (M010T002), and dropping them was the right
// call for ranking. Proximity is still worth having, and it does not need the index at all:
// AND over provision-level postings already restricts candidates to units containing every
// required word, and the caller is about to read those units' text anyway in order to quote
// them. Grading nearness on the text the answer is going to load costs the reads it was
// already doing, and buys the thing positions were supposed to buy.
//
// The cost is honest and bounded: NearWindow units per query, one text read each, and only
// when the caller asks. A search that returns ten snippets already paid for ten reads; this
// reorders those ten by a fact it can now see.
//
// What this cannot do is find proximity in units it never retrieved. If the useful near-match
// is ranked 400th, grading the top 20 will not rescue it. That is a ranking problem, and the
// answer to it is a better ranking function, not a second index.
//
// Measured, not assumed (internal/search/testdata/index-measurement-20260918.txt, 200 documents
// per kind/decade stratum, grep-equivalent truth over the same texts):
//
//	quoted spans (truth = the words adjacent)  recall@10 0.583 -> 0.917
//	AND queries                                 recall@10 1.000 -> 1.000, median gap 4 -> 3
//	exclusions                                  recall@10 unchanged
//
// The comparison has a control built in, which is why the over-fetch is twice the page rather
// than the page: BM25 ordering is deterministic, so fetching twenty and keeping ten without
// grading returns exactly the ten that a limit of ten would have returned. Every point of that
// recall change is the proximity signal, not a bigger candidate set.
//
// This is also the answer to the word-positions question M010T002 left open. Positions were
// measured at 62 MB of a 741 MB index with no recall mechanism to pay for them; proximity turns
// out to be worth 0.337 recall on the queries whose truth is adjacency, and it is bought with
// ten text reads the answer was going to do anyway. The index still stores no positions.
const NearWindow = 20

// GradeNear reorders the front of hits by how close the words of query sit in each unit's own
// text, filling in Hit.Gap as it goes.
//
// text supplies a unit's text and may return "" for units it cannot read; those keep their
// rank and Gap stays -1, because "we could not look" must not become "we looked and it was
// far". Grading only reorders: it never drops a hit, since the words are present in the index
// whether or not the text provider could show them.
func (idx *Index) GradeNear(hits []Hit, query string, text func(*Hit) string) []Hit {
	return idx.GradeNearWords(hits, idx.queryWords(query), queryAsksForPhrase(query), text)
}

// GradeNearWords is GradeNear for a caller that already knows which words to look for, which
// is what the tests drive. phrase says whether the words were written as one quoted span.
func (idx *Index) GradeNearWords(hits []Hit, words []string, phrase bool, text func(*Hit) string) []Hit {
	if len(words) < 2 || len(hits) == 0 {
		return hits
	}
	window := len(hits)
	if window > NearWindow {
		window = NearWindow
	}
	for i := 0; i < window; i++ {
		body := text(&hits[i])
		if body == "" {
			continue
		}
		gap := MinWordGap(body, words)
		if gap < 0 {
			continue
		}
		hits[i].Gap = gap
		hits[i].Score *= proximityFactor(gap, phrase)
	}
	sortHits(hits)
	return hits
}

// proximityFactor turns a measured distance into a score factor, and the shape depends on
// what the query asked for.
//
// For plain co-occurrence — words in one provision, no operator promising more — distance is a
// preference worth up to a doubling: adjacent words get 1+1/(1+gap), so a gap of one is worth
// 1.5× and a gap of nine is worth 1.1×. That is enough to beat a modest advantage in idf and
// frequency, which is intended: in one provision, words that sit together are usually talking
// about one thing, and words at opposite ends are usually talking about two.
//
// For a quoted span, distance is the criterion the asker named, so it must be able to overturn
// the ranking rather than nudge it: the factor is 1/(1+gap/16), which leaves an adjacent hit at
// about its own score and drops a hit two thousand words apart to one hundred and fifth of it.
// Without this the promise a quote mark makes is decorative — measured on the real index,
// "ubetinget råderet" came back with its best-scoring hit 1688 words apart, which is a
// different provision talking about different things, not the phrase.
//
// Neither form drops a hit. A far-apart unit is still a unit containing the words, and an empty
// page would be a worse answer than a page whose first line states the distance.
func proximityFactor(gap int, phrase bool) float64 {
	if phrase {
		return 1 / (1 + float64(gap)/16)
	}
	return 1 + 1/float64(1+gap)
}

// MinWordGap returns the smallest number of words between any two distinct query words in
// body, or -1 when fewer than two of the words appear at all.
//
// Word matching mirrors what the index was allowed to do with the same word — exact, a
// right-extension of a compound, or a folded form — so a query whose word reached the index
// through folding still sees its proximity. Where it differs is in being conservative: if the
// words cannot be located in the text, the answer is -1 and nothing is graded, rather than a
// confident distance built from a tokeniser that disagreed with the index.
func MinWordGap(body string, words []string) int {
	// positions[w] holds every token index where word w occurred.
	positions := make([][]int, len(words))
	tokens := Tokenize(body)
	for i, tok := range tokens {
		for w, want := range words {
			if tokenAnswers(tok, want) {
				positions[w] = append(positions[w], i)
			}
		}
	}
	best := -1
	for a := range words {
		for b := range words {
			if a == b || len(positions[a]) == 0 || len(positions[b]) == 0 {
				continue
			}
			gap := closestGap(positions[a], positions[b])
			if best < 0 || gap < best {
				best = gap
			}
		}
	}
	return best
}

// tokenAnswers reports whether a token in the text is an occurrence of a query word, using
// the same three allowances the index makes.
func tokenAnswers(tok, want string) bool {
	if tok == want {
		return true
	}
	if len(tok) > len(want) && strings.HasPrefix(tok, want) {
		return true
	}
	return Fold(tok) == want
}

// closestGap is the distance between the nearest pair from two sorted position lists.
func closestGap(a, b []int) int {
	best := abs(a[0] - b[0])
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		d := a[i] - b[j]
		if abs(d) < best {
			best = abs(d)
		}
		if d <= 0 {
			i++
		} else {
			j++
		}
	}
	return best
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
