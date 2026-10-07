package search

import (
	"math"
	"strings"
)

// Ranking holds what decides order beyond the words themselves. Term statistics say whether a
// provision contains the words; they cannot say whether it is the provision a reader means. The
// corpus is mostly texts that *talk about* a law — the bills that proposed it, the acts that
// amended it, the orders that put it into force for the Faroe Islands — and every one of them
// repeats the law's words, so on words alone the law itself loses to its own paperwork.
//
// Each factor below is a multiplier on a unit's score. The values were set by sweeping them
// against 57 questions, each with the document a lawyer would open, and checked on 43
// others that took no part in the tuning: mean reciprocal rank went from 0.003 to 0.86 on the
// first set and from 0.003 to 0.69 on the second, with no filters set, and from 0.17 and 0.14
// to 0.84 and 0.67 with "love og bekendtgørelser, gældende".
type Ranking struct {
	// B is BM25 length normalisation, or -1 for the value the index was built with (b=0,
	// which suited citation queries before the priors below existed). Some units are whole
	// annexes — an accounting standard printed as one bilag — that contain almost every word
	// several times over; without any normalisation they answer every query.
	B float64

	// Extend also reaches a word's longer forms when the word itself exists: "ansættelsesbevis"
	// should find "ansættelsesbeviser", and without this only an absent word was extended.
	Extend bool

	// Document priors, by what the register says the document is.
	Consolidated float64 // the law itself: a lov or lovbekendtgørelse in Lovtidende A
	Bill         float64 // a bill or its papers (Folketingstidende)
	Amending     float64 // an act or order whose title says it amends something
	Territory    float64 // a version for the Faroe Islands or Greenland, or an entry-into-force order
	Historic     float64 // the register calls it historic
	Other        float64 // guidance, decisions, circulars: useful, but not the rule itself

	// About rewards a document that answers in many provisions: a law that uses a word in forty
	// sections is about it, an act that mentions it once is not. The factor is 1+About·ln(n).
	About float64

	// Title multiplies a unit whose document's title carries the query's words, raised to the
	// share of them it carries. Named is the same factor when the query names a kind of
	// document ("bekendtgørelse om jagttegn", "vejledning om …", "færdselsloven"): then the
	// reader means the document with that title, and neither the law boost nor the penalty on
	// guidance applies.
	Title float64
	Named float64
}

// DefaultRanking is the ranking the server uses.
var DefaultRanking = Ranking{
	B:            0.2,
	Extend:       true,
	Consolidated: 3,
	Bill:         0.25,
	Amending:     0.5,
	Territory:    0.5,
	Historic:     0.25,
	Other:        0.3,
	About:        0.4,
	Title:        1.3,
	Named:        4,
}

// docClass sorts a document into the classes the priors speak of.
type docClass int

const (
	classOther docClass = iota
	classLaw
	classBill
	classAmending
	classTerritory
)

func classify(d *DocMeta) docClass {
	title := strings.ToLower(d.Title)
	switch {
	case d.Kind == "ft" || strings.HasPrefix(title, "forslag til"):
		return classBill
	case strings.Contains(title, "ændring af") || strings.Contains(title, "ophævelse af"):
		return classAmending
	case strings.Contains(title, "færøerne") || strings.Contains(title, "grønland") || strings.Contains(title, "ikrafttræden"):
		return classTerritory
	case d.Kind == "lta" && isLawType(d.TypeCode):
		return classLaw
	}
	return classOther
}

// isLawType says whether a publisher type code names a statute or its consolidation. Newer
// documents carry codes ("LOV H#LOKDOK01", "LBK H#LOKDOK03"), older ones the word.
func isLawType(code string) bool {
	c := strings.ToUpper(strings.TrimSpace(code))
	return strings.HasPrefix(c, "LOV") || strings.HasPrefix(c, "LBK") || strings.HasPrefix(c, "LOVBEKENDTGØRELSE")
}

func (r Ranking) prior(d *DocMeta, named bool) float64 {
	p := 1.0
	switch classify(d) {
	case classLaw:
		// A reader who names a document has said which one; being a law is no reason to
		// rank above the one they named.
		if !named {
			p = r.Consolidated
		}
	case classBill:
		p = r.Bill
	case classAmending:
		p = r.Amending
	case classTerritory:
		p = r.Territory
	default:
		if d.Kind != "lta" && !named {
			p = r.Other
		}
	}
	if statusKey(d.Status) == "Historic" {
		p *= r.Historic
	}
	return p
}

func (r Ranking) about(n int) float64 {
	if n <= 1 || r.About == 0 {
		return 1
	}
	return 1 + r.About*math.Log(float64(n))
}

// titleShare is the share of the query's words a title carries, each weighted by how rare it
// is among titles: "bekendtgørelse" and "lov" open most titles and say little, "jagttegn" names
// one. A title word answers a query word that starts it ("ferie" in "lov om ferie") or that it
// starts ("sygedagpengeloven" asked, "sygedagpenge" printed), which is how a title names its
// subject without the index's help.
func (idx *Index) titleShare(title string, words []string) float64 {
	if len(words) == 0 {
		return 0
	}
	toks := Tokenize(title)
	var got, all float64
	for _, w := range words {
		weight := idx.titleIDF(w)
		all += weight
		for _, tok := range toks {
			if strings.HasPrefix(tok, w) || (len([]rune(tok)) >= 5 && strings.HasPrefix(w, tok)) {
				got += weight
				break
			}
		}
	}
	if all == 0 {
		return 0
	}
	return got / all
}

// titleIDF is a word's inverse frequency among document titles, counted once per index.
func (idx *Index) titleIDF(w string) float64 {
	idx.titleOnce.Do(func() {
		idx.titleDF = map[string]int{}
		for i := range idx.Docs {
			seen := map[string]bool{}
			for _, tok := range Tokenize(idx.Docs[i].Title) {
				if !seen[tok] {
					seen[tok] = true
					idx.titleDF[tok]++
				}
			}
		}
	})
	n := float64(len(idx.Docs))
	return math.Log(1 + n/float64(1+idx.titleDF[w]))
}

// namesDocument says whether a query names a document rather than a subject, and whether it
// names a kind of document other than a law. "bekendtgørelse om jagttegn" and "vejledning om
// forvaltningsloven" name a type, so the type priors give way; "færdselsloven" and "lov om
// social service" name a law, which the priors already favour, so only the title weighs more.
func namesDocument(words []string) (named, typed bool) {
	for _, w := range words {
		switch w {
		case "bekendtgørelse", "bekendtgørelsen", "vejledning", "vejledningen", "cirkulære", "cirkulæret",
			"cirkulæreskrivelse", "anordning", "anordningen", "skrivelse", "betænkning":
			return true, true
		case "lov", "loven":
			named = true
		}
		for _, suffix := range []string{"loven", "lovens", "lov"} {
			if len([]rune(w)) > len([]rune(suffix))+3 && strings.HasSuffix(w, suffix) {
				named = true
			}
		}
	}
	return named, false
}
