package search

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestSearchOrderIsStableAcrossIdenticalQueries guards the tie-break in sortHits.
//
// Equal scores are the ordinary case rather than a corner: two provisions of one act
// that repeat a word equally often in units of equal length score the same to the last
// bit. Search collects its hits by iterating a map, so unless the comparison is a total
// order, which page a reader gets depends on Go's randomised map order — the same query
// answered differently a minute later, and with a limit below the tie count, different
// *hits* rather than a different order. The tie-break on accession alone did not close
// this, because provisions of one act share an accession.
func TestSearchOrderIsStableAcrossIdenticalQueries(t *testing.T) {
	var docs []DocMeta
	var units []Unit
	for i := range 4 {
		docs = append(docs, DocMeta{
			Accession: fmt.Sprintf("A20000000%02d5", i),
			ELI:       fmt.Sprintf("eli/lta/2000/%d", i),
			Kind:      "lta", Year: 2000, Number: i, Title: "Bekendtgørelse om hvidvask", Source: "xml",
		})
		// Three provisions per act, identical text in all of them. The provisions of one
		// act share an accession, which is what the tie-break on accession cannot order;
		// this is the shape that made the answer vary from process to process.
		for _, addr := range []string{"§ 1", "§ 2", "§ 3"} {
			units = append(units, Unit{Doc: i, Addr: addr, Text: "hvidvask af penge i selskaber"})
		}
	}
	idx, err := Build(context.Background(), docs, units, Options{})
	if err != nil {
		t.Fatal(err)
	}

	const limit = 10 // below the 12 tied hits, so an unstable order changes membership too
	want := idx.Search(Query{Text: "hvidvask", Limit: limit})
	if len(want) != limit {
		t.Fatalf("got %d hits, want %d: the fixture has no ties to lose", len(want), limit)
	}
	for _, h := range want[1:] {
		if h.Score != want[0].Score {
			t.Fatalf("the fixture has no equal scores (%v vs %v), so it tests nothing", h.Score, want[0].Score)
		}
	}

	for i := range 200 {
		got := idx.Search(Query{Text: "hvidvask", Limit: limit})
		if len(got) != len(want) {
			t.Fatalf("run %d returned %d hits, want %d", i, len(got), len(want))
		}
		for j := range got {
			if got[j].Unit != want[j].Unit || got[j].Addr != want[j].Addr {
				t.Fatalf("run %d hit %d is unit %d %q, want unit %d %q: equal scores must still answer in one order",
					i, j, got[j].Unit, got[j].Addr, want[j].Unit, want[j].Addr)
			}
		}
	}
}

func TestParseQueryBindsOperatorsToWhatTheyJoin(t *testing.T) {
	cases := []struct {
		text string
		want []operand
	}{
		// AND joins two things. Requiring only the right-hand side would answer an OR
		// query while claiming it was an AND.
		{text: "pligt AND aalborg", want: []operand{{word: "pligt", op: opAnd}, {word: "ålborg", op: opAnd}}},
		{text: "pligt or aalborg", want: []operand{{word: "pligt", op: opOr}, {word: "ålborg", op: opOr}}},
		{text: "+pligt -lov", want: []operand{{word: "pligt", op: opAnd}, {word: "lov", op: opNot}}},
		{text: "pligt NOT aalborg", want: []operand{{word: "pligt", op: opOr}, {word: "ålborg", op: opNot}}},
		{text: "pligt andnot aalborg", want: []operand{{word: "pligt", op: opAnd}, {word: "ålborg", op: opNot}}},
		// A quoted span requires its words and says so, which is what lets the proximity
		// grade treat distance as the criterion rather than as a tie-break.
		{text: `"offentlige veje"`, want: []operand{{word: "offentlige", op: opAnd, phrase: true}, {word: "veje", op: opAnd, phrase: true}}},
		// An ordinary sentence stays an ordinary sentence, including the Danish words
		// that happen to look like operators when written the way Danes write them.
		{text: "offentlige veje", want: []operand{{word: "offentlige", op: opOr}, {word: "veje", op: opOr}}},
		{text: "pligt og ansvar", want: []operand{{word: "pligt", op: opOr}, {word: "og", op: opOr}, {word: "ansvar", op: opOr}}},
		{text: "NOT", want: nil},
	}
	for _, c := range cases {
		got := parseQuery(c.text)
		if len(got) != len(c.want) {
			t.Errorf("parseQuery(%q) = %v, want %v", c.text, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseQuery(%q)[%d] = %+v, want %+v", c.text, i, got[i], c.want[i])
			}
		}
	}
}

func TestSearchAndRequiresBothWords(t *testing.T) {
	idx := fixtureIndex(t, Options{})
	hits := idx.Search(Query{Text: "pligt AND aalborg", Limit: 10})
	if len(hits) == 0 {
		t.Fatal("an AND of two words that co-occur returned nothing")
	}
	for _, h := range hits {
		if !strings.Contains(strings.Join(h.Matched, " "), "pligt") || !strings.Contains(strings.Join(h.Matched, " "), "ålborg") {
			t.Errorf("AND returned %s %q with matched=%v: a required word is missing",
				h.Doc.Accession, h.Addr, h.Matched)
		}
	}
	// The pre-1948 spelling unit is the only one carrying both.
	if hits[0].Doc.Accession != "A19450050070" {
		t.Errorf("top hit %s, want the Aa-spelling act that carries both words", hits[0].Doc.Accession)
	}
}

func TestSearchAndWithAnUnknownWordAnswersNothing(t *testing.T) {
	// This is the whole point of AND over OR: a required word the index has never seen
	// must produce an empty answer, not a list of units that matched the other words.
	idx := fixtureIndex(t, Options{})
	if hits := idx.Search(Query{Text: "pligt AND vegetabilie", Limit: 10}); len(hits) != 0 {
		t.Errorf("required unknown word still returned %d hits: %v", len(hits), hits[0].Addr)
	}
	if hits := idx.Search(Query{Text: "vegetabilie", Limit: 10}); len(hits) != 0 {
		t.Errorf("unknown word alone returned %d hits", len(hits))
	}
}

func TestSearchNotRemovesUnits(t *testing.T) {
	idx := fixtureIndex(t, Options{})
	all := idx.Search(Query{Text: "pligt", Limit: 20})
	filtered := idx.Search(Query{Text: "pligt -virksomheder", Limit: 20})
	if len(filtered) >= len(all) {
		t.Fatalf("exclusion removed nothing: %d of %d hits", len(filtered), len(all))
	}
	for _, h := range filtered {
		if strings.Contains(strings.Join(h.Matched, " "), "virksomheder") {
			t.Errorf("excluded word still matched in %s %q", h.Doc.Accession, h.Addr)
		}
	}
	// The unit that had to disappear is the one carrying virksomheder.
	for _, h := range all {
		if h.Doc.Accession == "A20191399070" && h.Addr == "§ 267, stk. 3 nr. 2" {
			for _, g := range filtered {
				if g.Unit == h.Unit {
					t.Errorf("the excluded provision survived the exclusion")
				}
			}
			break
		}
	}
}

func TestSearchRequiredWordsSurviveMetadataFilters(t *testing.T) {
	// The filters take units out of the required intersection after the postings have been
	// intersected, so a hit whose required word was lost to a kind filter has to go too.
	idx := fixtureIndex(t, Options{})
	hits := idx.Search(Query{Text: "pligt AND aalborg", Kind: "fob", Limit: 10})
	if len(hits) != 0 {
		for _, h := range hits {
			t.Errorf("kind=fob AND returned %s %q matched=%v", h.Doc.Accession, h.Addr, h.Matched)
		}
	}
	// The same query with the kind the unit actually has must still answer.
	if hits := idx.Search(Query{Text: "pligt AND aalborg", Kind: "lta", Limit: 10}); len(hits) == 0 {
		t.Error("kind=lta rejected the only matching provision, which is an lta act")
	}
}

func TestSearchReportsCoveragePerHit(t *testing.T) {
	idx := fixtureIndex(t, Options{})
	hits := idx.Search(Query{Text: "pligt virksomheder", Limit: 10})
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	full, partial := false, false
	for _, h := range hits {
		if h.Requested != 2 {
			t.Fatalf("Requested = %d, want 2 for a two-word query", h.Requested)
		}
		if h.Covered != len(h.Matched) {
			t.Errorf("Covered %d disagrees with Matched %v", h.Covered, h.Matched)
		}
		switch h.Covered {
		case 2:
			full = true
		case 1:
			partial = true
		}
	}
	if !full || !partial {
		t.Errorf("the fixture should offer both a two-word and a one-word hit; got full=%v partial=%v", full, partial)
	}
	// A required word is not "missing coverage": it cannot be missing, and saying 2 of 2
	// every time would make the field meaningless for the queries that use it.
	for _, h := range idx.Search(Query{Text: "pligt AND aalborg", Limit: 10}) {
		if h.Covered != h.Requested {
			t.Errorf("AND hit reports %d of %d covered", h.Covered, h.Requested)
		}
	}
}

func TestExcludedWordIsNotMissingCoverage(t *testing.T) {
	// "2 of 3" on a query that asked for two and forbade one would tell the reader a
	// required word went unfound, when the forbidden word is why the hit is here.
	idx := fixtureIndex(t, Options{})
	hits := idx.Search(Query{Text: "pligt AND aalborg -affaldsplaner", Limit: 10})
	if len(hits) == 0 {
		t.Fatal("no hits for words that co-occur")
	}
	for _, h := range hits {
		if h.Requested != 2 {
			t.Errorf("Requested = %d, want 2: the query asked for two words and forbade a third", h.Requested)
		}
		if h.Covered != 2 {
			t.Errorf("Covered = %d, want 2 for a fully satisfied requirement: matched %v", h.Covered, h.Matched)
		}
	}
	// A query of pure exclusions asks for nothing this index can answer, and must not
	// answer with the whole corpus.
	if hits := idx.Search(Query{Text: "-pligt", Limit: 10}); len(hits) != 0 {
		t.Errorf("an exclusion-only query returned %d hits, want 0", len(hits))
	}
}

func TestQuotedSpanOverturnsAUnitThatMerelyContainsTheWords(t *testing.T) {
	// The far unit repeats both words and so scores higher on the index alone. A quote mark
	// is a request for the words together, so the grade has to be strong enough to put the
	// adjacent provision first anyway; a nudge would leave the promise decorative.
	docs := []DocMeta{
		{Accession: "A1", ELI: "eli/lta/2000/1", Kind: "lta", Year: 2000, Title: "Lov", Source: "xml"},
	}
	units := []Unit{
		{Doc: 0, Addr: "§ 1", Text: "Affaldsplaner for offentlige veje."},
		{Doc: 0, Addr: "§ 2", Text: "Kommunen skal udarbejde affaldsplaner, der omfatter alle kommunens veje, stier og parker, herunder også de veje, der udgør forbindelse mellem bydele, havneområder, industrigrunde og boligkvarterer, idet planen tillige skal beskrive vedligeholdelse, efterse, inddragelse af arealer, belysning, belægning, afvanding, skiltning og den øvrige trafikale afvikling i det åbne land samt i de offentlige områder."},
	}
	idx, err := Build(context.Background(), docs, units, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	words := []string{"offentlige", "veje"}
	plain := idx.Search(Query{Text: `"offentlige veje"`, Limit: 5})
	if len(plain) != 2 {
		t.Fatalf("quoted span returned %d hits, want both units", len(plain))
	}
	// GradeNear reorders and rescores the slice it is given, which is what a caller holding a
	// fresh Search result wants. Comparing two grades therefore needs two copies, or the
	// second grade measures the first one's output.
	softIn, phraseIn := make([]Hit, len(plain)), make([]Hit, len(plain))
	copy(softIn, plain)
	copy(phraseIn, plain)
	if plain[0].Addr != "§ 2" {
		t.Skipf("premise not met: the index already prefers § 1 (%s first), so the overturn cannot be demonstrated", plain[0].Addr)
	}
	graded := idx.GradeNearWords(phraseIn, words, true, func(h *Hit) string { return units[h.Unit].Text })
	if graded[0].Addr != "§ 1" {
		t.Errorf("a quoted span still ranked a unit %d words apart first; the quote mark changed nothing", graded[0].Gap)
	}
	if graded[1].Gap < 10 {
		t.Errorf("far unit reported Gap %d, want the long distance actually measured", graded[1].Gap)
	}
	// Without the quote the same distance is still a preference — a gap of one word over a
	// gap of twenty-four is a real reason to prefer a provision, and pretending otherwise
	// would be its own kind of lie. What the quote adds is weight: the far unit has to fall
	// much further when the asker named adjacency.
	soft := idx.GradeNearWords(softIn, words, false, func(h *Hit) string { return units[h.Unit].Text })
	ratio := func(hits []Hit) float64 {
		var far, near float64
		for _, h := range hits {
			if h.Addr == "§ 2" {
				far = h.Score
			} else {
				near = h.Score
			}
		}
		return far / near
	}
	if phraseRatio, softRatio := ratio(graded), ratio(soft); phraseRatio >= softRatio {
		t.Errorf("a quoted span demoted the far unit no more than plain co-occurrence did: ratio %.4f vs %.4f",
			phraseRatio, softRatio)
	}
}

func TestProximityGradePrefersAdjacentWords(t *testing.T) {
	// Two provisions contain both words; one has them side by side and the other has them
	// a sentence apart. The index cannot tell these apart -- it stores no positions -- so
	// the grade that reads the text has to.
	docs := []DocMeta{
		{Accession: "A1", ELI: "eli/lta/2000/1", Kind: "lta", Year: 2000, Title: "Lov", Source: "xml"},
	}
	units := []Unit{
		{Doc: 0, Addr: "§ 1", Text: "Affaldsplaner for offentlige veje."},
		{Doc: 0, Addr: "§ 2", Text: "Affaldsplaner for veje skal vedtages af kommunalbestyrelsen inden udgangen af cadastralåret for at sikre den offentlige planlægning."},
	}
	idx, err := Build(context.Background(), docs, units, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	words := []string{"offentlige", "veje"}
	// Both units carry both words, so index ranking alone has no reason to prefer one.
	plain := idx.Search(Query{Text: "offentlige AND veje", Limit: 5})
	if len(plain) != 2 {
		t.Fatalf("AND found %d units, want the two planted ones", len(plain))
	}
	graded := idx.GradeNearWords(plain, words, false, func(h *Hit) string { return units[h.Unit].Text })
	if graded[0].Addr != "§ 1" {
		t.Errorf("proximity ranked %s first; § 1 has the words side by side", graded[0].Addr)
	}
	if graded[0].Gap > 1 {
		t.Errorf("side-by-side words reported Gap %d, want 1 or less", graded[0].Gap)
	}
	if graded[1].Gap < 5 {
		t.Errorf("far-apart words reported Gap %d, want the distance actually measured", graded[1].Gap)
	}
}

func TestProximityGradeRefusesToGuessWhatItCouldNotRead(t *testing.T) {
	docs := []DocMeta{{Accession: "A1", ELI: "eli/lta/2000/1", Kind: "lta", Year: 2000, Title: "Lov", Source: "xml"}}
	units := []Unit{
		{Doc: 0, Addr: "§ 1", Text: "Der ydes erstatning for ekspropriering af jord."},
		{Doc: 0, Addr: "§ 2", Text: "Ekspropriering af jord giver krav på erstatning efter reglerne."},
	}
	idx, err := Build(context.Background(), docs, units, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	before := idx.Search(Query{Text: "erstatning AND ekspropriering", Limit: 5})
	order := make([]string, 0, len(before))
	for _, h := range before {
		order = append(order, h.Addr)
	}
	// A provider that cannot read anything must leave the ranking alone rather than
	// inventing a gap of zero for every hit it was shown. The copy matters: GradeNear reorders
	// the slice it is handed, so comparing its output against the same array would compare a
	// slice with itself and pass whatever the grader did.
	graded := idx.GradeNearWords(slices.Clone(before), []string{"erstatning", "ekspropriering"}, false, func(h *Hit) string { return "" })
	for i := range graded {
		if graded[i].Addr != order[i] {
			t.Fatalf("unreadable units were reordered: %s before %s", order[i], graded[i].Addr)
		}
		if graded[i].Gap != -1 {
			t.Errorf("Gap = %d for a unit whose text could not be read, want -1", graded[i].Gap)
		}
	}
	if graded := idx.GradeNearWords(before, []string{"erstatning"}, false, func(h *Hit) string { return units[h.Unit].Text }); graded[0].Gap != -1 {
		t.Errorf("a one-word query has no distance; got Gap %d", graded[0].Gap)
	}
}

func TestMinWordGapMeasuresWordsNotCharacters(t *testing.T) {
	body := "sagen handler om erstatning for det ekspropriering der skete"
	if got := MinWordGap(body, []string{"erstatning", "ekspropriering"}); got != 3 {
		t.Errorf("MinWordGap = %d, want 3 words between the two", got)
	}
	// A compound and an inflected form count as occurrences, because that is how the
	// words reached the index in the first place: "pligtige" answers "pligt".
	if got := MinWordGap("pligt pligtige virksomheder", []string{"pligt", "virksomheder"}); got != 1 {
		t.Errorf("MinWordGap over an inflected form = %d, want 1: pligtige is one word from virksomheder", got)
	}
	if got := MinWordGap("kun det ene ord forekommer", []string{"erstatning", "ekspropriering"}); got >= 0 {
		t.Errorf("MinWordGap = %d when one word is absent, want -1", got)
	}
}
