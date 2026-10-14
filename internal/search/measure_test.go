package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/lovkilde/internal/lexdania"
)

// Query sets for the index measurement.
//
// These are the queries a Danish legal search box actually receives, grouped by the
// failure each one is designed to expose, because "recall 0.8" is not a finding
// until you know which kind of query lost. The baseline for every number is a brute
// force substring scan over the same sampled text, so recall means "found what a
// grep would have found", not "found what the index itself thinks exists".
type measurementQuery struct {
	Text      string
	Group     string
	Precision bool // a trap: the correct answer is few or no hits
}

var measurementQueries = []measurementQuery{
	// Plain terms: the floor. If these miss, nothing else matters.
	{Text: "miljøbeskyttelse", Group: "term"},
	{Text: "ejendomsvurdering", Group: "term"},
	{Text: "persondata", Group: "term"},
	{Text: "skattepligt", Group: "term"},
	{Text: "råden", Group: "term"},

	// Inflected forms: the reason a folded stream is proposed at all.
	{Text: "pligtige", Group: "inflected"},
	{Text: "ændringer", Group: "inflected"},
	{Text: "bekendtgørelsen", Group: "inflected"},
	{Text: "virksomhedsområde", Group: "inflected"},
	{Text: "ansat", Group: "inflected"},

	// Compound heads: Danish puts the head of a compound last, so a prefix index
	// cannot reach "ejendomsafgift" from a query for "afgift". This group measures
	// exactly what that costs.
	{Text: "afgift", Group: "compound-head"},
	{Text: "pligt", Group: "compound-head"},
	{Text: "lov", Group: "compound-head"},
	{Text: "bestemmelse", Group: "compound-head"},

	// Phrases: measures whether term-AND ranking finds units where the words are
	// actually adjacent, which is the case for positions in the index.
	{Text: "offentlige veje", Group: "phrase"},
	{Text: "særlig indkomstskat", Group: "phrase"},
	{Text: "ubetinget råderet", Group: "phrase"},
	{Text: "henvendt til", Group: "phrase"},

	// Citations: number-bearing queries, where dropping digits would be fatal.
	{Text: "lov nr. 1399", Group: "citation"},
	{Text: "bekendtgørelse nr. 1399", Group: "citation"},
	{Text: "§ 267", Group: "citation"},

	// Pre-1948 spelling: the aa fold either works or it does not.
	{Text: "aalborg", Group: "aa-spelling"},
	{Text: "aa-sag", Group: "aa-spelling"},

	// Precision traps. æ/ø/å are separate letters, so a diacritic-insensitive index
	// would return these as hits and be confidently wrong. Single-rune terms are not
	// indexable at all, which is a tokeniser property with its own unit test rather
	// than a recall question here.
	{Text: "tænke", Group: "trap"},
	{Text: "mø", Group: "trap"},

	// Wrong language: should return little, and must not error.
	{Text: "environmental protection", Group: "other-language"},
}

// Boolean and proximity queries, measured separately because their truth is a different
// question. For a plain query the grep baseline is "text contains the word"; for these it is
// "text contains both", "text contains one and not the other", or "text contains them next to
// each other", which is what the operators promise and what the index alone cannot guarantee —
// it stores no word positions.
//
// These are constructed from the shapes a legal search box receives, not from real query logs:
// the service keeps no logs, so claiming they came from users would be invented evidence. Each
// group's truth-set size is printed alongside its recall, because a boolean query that matches
// nothing in the sample measures nothing and a reader has to be able to see that.
type booleanMeasurement struct {
	Text  string
	Group string
	All   []string // words that must all occur
	None  []string // words none of which may occur

	// Adjacent names a pair that must sit next to each other for a unit to be a real answer.
	// It is what a quoted span promises, and term co-occurrence does not deliver.
	Adjacent []string
}

var booleanQueries = []booleanMeasurement{
	{Text: "pligt AND virksomhed", Group: "and", All: []string{"pligt", "virksomhed"}},
	{Text: "erstatning AND ekspropriering", Group: "and", All: []string{"erstatning", "ekspropriering"}},
	{Text: "miljøbeskyttelse AND virksomhed", Group: "and", All: []string{"miljøbeskyttelse", "virksomhed"}},
	{Text: "persondata AND behandling", Group: "and", All: []string{"persondata", "behandling"}},
	{Text: "sundhed AND kostvaner", Group: "and", All: []string{"sundhed", "kostvaner"}},
	{Text: "uigenkaldelig AND pantehæftelse", Group: "and", All: []string{"uigenkaldelig", "pantehæftelse"}},

	// Quoted spans: the promise is adjacency, so the truth is adjacency.
	{Text: `"offentlige veje"`, Group: "quoted", All: []string{"offentlige", "veje"}, Adjacent: []string{"offentlige", "veje"}},
	{Text: `"ubetinget råderet"`, Group: "quoted", All: []string{"ubetinget", "råderet"}, Adjacent: []string{"ubetinget", "råderet"}},
	{Text: `"særlig indkomstskat"`, Group: "quoted", All: []string{"særlig", "indkomstskat"}, Adjacent: []string{"særlig", "indkomstskat"}},
	{Text: `"henvendt til"`, Group: "quoted", All: []string{"henvendt", "til"}, Adjacent: []string{"henvendt", "til"}},

	// Exclusions: the answer must contain the first word and not the second, which is a
	// claim about absence that an OR query cannot make at all.
	{Text: "andel -boliganpart", Group: "not", All: []string{"andel"}, None: []string{"boliganpart"}},
	{Text: "skyld -skyldner", Group: "not", All: []string{"skyld"}, None: []string{"skyldner"}},
	{Text: "råden -ejendom", Group: "not", All: []string{"råden"}, None: []string{"ejendom"}},
}

// booleanTruth is the grep-equivalent truth set for one boolean query.
func booleanTruth(texts []string, q booleanMeasurement) []int {
	var out []int
	for i, text := range texts {
		ok := true
		for _, w := range q.All {
			if !strings.Contains(text, w) {
				ok = false
				break
			}
		}
		if ok {
			for _, w := range q.None {
				if strings.Contains(text, w) {
					ok = false
					break
				}
			}
		}
		if ok && len(q.Adjacent) == 2 && !adjacentPair(text, q.Adjacent[0], q.Adjacent[1]) {
			ok = false
		}
		if ok {
			out = append(out, i)
		}
	}
	return out
}

// adjacentPair is the strict form: the two words with nothing but a space, hyphen or slash
// between them. containsPhrase is deliberately looser because it audits a ranking; this one
// defines truth, so it does not give the index credit for a near miss.
func adjacentPair(text, first, second string) bool {
	for _, sep := range []string{" ", "-", "/"} {
		if strings.Contains(text, first+sep+second) {
			return true
		}
	}
	return false
}

// TestMeasureIndex compares index variants against brute-force truth and writes the
// report this milestone's decision rests on.
//
// Gated on RETSIN_SEARCH_MEASURE because it reads the real mirror: it needs the corpus, and an
// ordinary `go test ./...` must not depend on 14 GB of data that CI and fresh clones do not
// have.
func TestMeasureIndex(t *testing.T) {
	if os.Getenv("RETSIN_SEARCH_MEASURE") == "" {
		t.Skip("set RETSIN_SEARCH_MEASURE=1 to measure the index against the real mirror")
	}
	root := os.Getenv("RETSIN_MIRROR")
	if root == "" {
		// go test runs in the package directory, so the repo-relative path that works
		// from the command line is not the one that resolves here. Try both rather
		// than depending on where the caller stood.
		for _, cand := range []string{"data/xml", "../../data/xml"} {
			if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
				root = cand
				break
			}
		}
	}
	if root == "" {
		t.Skip("no mirror found: set RETSIN_MIRROR to the archive directory")
	}
	perStratum := 50
	if v := os.Getenv("RETSIN_SEARCH_SAMPLE"); v != "" {
		if n, err := fmt.Sscanf(v, "%d", &perStratum); n != 1 || err != nil {
			t.Fatalf("RETSIN_SEARCH_SAMPLE=%q is not a number", v)
		}
	}

	start := time.Now()
	docs, units, err := Sample(context.Background(), root, perStratum)
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if len(docs) < 500 || len(units) < 2000 {
		t.Fatalf("sample too small to measure: %d docs, %d units", len(docs), len(units))
	}
	// The unitisation is a measured dimension, not an assumed improvement: with
	// RETSIN_UNITS=sections the same documents, queries and grep truth are used over the
	// units this milestone replaces, so the two reports differ only in how a document was
	// cut. The sample is seeded, so it reproduces unit for unit.
	if os.Getenv("RETSIN_UNITS") == "sections" {
		units = sectionsUnits(t, docs)
	}
	sampleText := time.Since(start)

	var totalRunes int
	bySource := map[string]int{}
	for _, d := range docs {
		bySource[d.Source]++
	}
	for _, u := range units {
		totalRunes += len([]rune(u.Text))
	}

	var out strings.Builder
	// How much of the index cannot say where it came from. Every unit here is findable;
	// an unaddressed one just cites the document instead of a location inside it.
	unaddressed := 0
	for _, u := range units {
		if u.Addr == "" {
			unaddressed++
		}
	}
	fmt.Fprintf(&out, "# lovkilde search index measurement\n")
	fmt.Fprintf(&out, "# generated: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	// The unit mode belongs in the reproduce line: the two reports differ only in it, and a
	// reader who does not set it regenerates the wrong side of the comparison.
	unitsEnv := ""
	if os.Getenv("RETSIN_UNITS") == "sections" {
		unitsEnv = "RETSIN_UNITS=sections "
	}
	fmt.Fprintf(&out, "# reproduce: %sRETSIN_SEARCH_MEASURE=1 RETSIN_SEARCH_SAMPLE=%d go test ./internal/search -run TestMeasureIndex -v\n", unitsEnv, perStratum)
	fmt.Fprintf(&out, "# corpus sample: %d documents, %d index units (%d unaddressed, %.0f%%), %.1f MB of indexed text, sampled in %s\n",
		len(docs), len(units), unaddressed, 100*float64(unaddressed)/float64(len(units)), float64(totalRunes)/1e6, sampleText.Round(time.Millisecond))
	fmt.Fprintf(&out, "# sources: %s\n", joinCounts(bySource))
	// The coverage bound, measured rather than assumed: an index must not know less than
	// the text a reader is served, and the two are produced by different code — the
	// unitiser and the renderer — so either can drift without the other noticing. Over
	// 100% means the index holds the same words more than once, which is what a unit that
	// contains another unit does.
	servedRunes := 0
	for _, d := range docs {
		doc, err := lexdania.LoadWithBody(d.Path)
		if err != nil {
			continue
		}
		servedRunes += len([]rune(doc.Model().Content.PlainText()))
	}
	// The MB ratio above is crude in both directions: a unit that contains another unit
	// inflates it, and text that is merely reworded deflates it. The word coverage below
	// is the precise form: for each document, what fraction of the words a reader is
	// served also appear in some unit of that same document.
	fmt.Fprintf(&out, "# served text: %.1f MB; indexed text is %.0f%% of it\n",
		float64(servedRunes)/1e6, 100*float64(totalRunes)/float64(servedRunes))
	fmt.Fprintf(&out, "# word coverage over %d documents: %.1f%% of served words of six letters or more appear in a unit of the same document\n",
		coverageDocs, 100*wordCoverage(t, docs, units, coverageDocs))
	fmt.Fprintf(&out, "# baseline: brute-force substring scan over the same unit texts (grep-equivalent),\n")
	fmt.Fprintf(&out, "# so recall below is recall against what grep would find, in the same normalised case.\n")
	// The honest limit of this metric: when a query has far more than ten matches,
	// recall@10 measures whether two correct orderings agree about which ten to show,
	// not whether the index is broken. The counts are printed per query for that reason.
	fmt.Fprintf(&out, "# caveat: recall@10 against a grep baseline measures ORDERING AGREEMENT where a query\n")
	fmt.Fprintf(&out, "# has far more than ten matches. Both orderings can be correct. Read the match counts.\n\n")

	// Baseline: how slow is searching without any index at all? This is the number
	// that decides whether the index is worth its build cost, and it is measured
	// rather than assumed.
	normalised := make([]string, len(units))
	for i, u := range units {
		normalised[i] = strings.ToLower(u.Text)
	}
	for _, q := range measurementQueries {
		_ = bruteForce(normalised, q.Text)
	}
	baseline := time.Since(start) - sampleText
	perQuery := baseline / time.Duration(len(measurementQueries))
	fullDocs := 191394
	fmt.Fprintf(&out, "## no-index baseline\n")
	fmt.Fprintf(&out, "brute-force scan of the sample: %s total, %s per query\n", baseline.Round(time.Millisecond), perQuery.Round(time.Millisecond))
	fmt.Fprintf(&out, "extrapolated to the full mirror (%d documents): %s per query, single process\n\n",
		fullDocs, (perQuery * time.Duration(fullDocs) / time.Duration(len(docs))).Round(time.Millisecond))

	// Each run writes its own dated report rather than overwriting the previous one: these
	// files are the evidence behind constants cited in source comments, and a re-measurement
	// that silently replaced the numbers somebody quoted would destroy the audit trail it is
	// supposed to extend. RETSIN_MEASURE_OUT overrides the name.
	dest := fmt.Sprintf("testdata/index-measurement-%s.txt", time.Now().UTC().Format("20060102"))
	// The override names the file inside testdata, because that is where the evidence lives;
	// a bare name here used to drop the report in the package directory instead, where it is
	// neither committed nor compared. A value with a slash in it is taken as a path.
	if env := os.Getenv("RETSIN_MEASURE_OUT"); env != "" {
		if strings.Contains(env, "/") {
			dest = env
		} else {
			dest = "testdata/" + env
		}
	}
	if _, err := os.Stat("internal/search/testdata"); err == nil {
		dest = "internal/search/" + dest
	}
	// checkpoint writes the report after every variant: a measurement that panics on
	// the second of three would otherwise lose the numbers it had already earned.
	checkpoint := func() {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatalf("mkdir report dir: %v", err)
		}
		if err := os.WriteFile(dest, []byte(out.String()), 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}
	}
	checkpoint()

	var sweepIdx *Index

	variants := []struct {
		name string
		opts Options
		b    float64
	}{
		{"raw", Options{}, 0},
		// The length-penalty sweep belongs in the report, not in a README table: the shipped
		// b=0 was decided when a document with no numbered sections was one long unit, and the
		// units have changed. b travels in the file header, so setting it before Save is what
		// the queries actually score with.
		{"raw b=0.75", Options{}, 0.75},
		{"raw+folded", Options{Folded: true}, 0},
	}

	type recallRow struct {
		group    string
		sum      float64
		n        int
		truth    int
		maxTruth int
		miss     []string
	}

	for _, v := range variants {
		buildStart := time.Now()
		idx, err := Build(context.Background(), docs, units, v.opts)
		if err != nil {
			t.Fatalf("build %s: %v", v.name, err)
		}
		// The ranking's own B wins over the index's, so a variant's b is set there.
		rk := DefaultRanking
		rk.B = v.b
		idx.Ranking = &rk
		buildTime := time.Since(buildStart)

		path := t.TempDir() + "/index.bin"
		saveStart := time.Now()
		if err := idx.Save(path); err != nil {
			t.Fatalf("save: %v", err)
		}
		saveTime := time.Since(saveStart)
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		size := st.Size()

		loadStart := time.Now()
		loaded, err := Load(path)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		loadTime := time.Since(loadStart)
		if len(loaded.Terms) != len(idx.Terms) || len(loaded.Units) != len(idx.Units) {
			t.Fatalf("%s: round-trip lost data: %d terms vs %d, %d units vs %d",
				v.name, len(loaded.Terms), len(idx.Terms), len(loaded.Units), len(idx.Units))
		}

		fmt.Fprintf(&out, "## variant %s\n", v.name)
		fmt.Fprintf(&out, "build: %s   write: %s   load: %s\n", buildTime.Round(time.Millisecond), saveTime.Round(time.Millisecond), loadTime.Round(time.Millisecond))
		fmt.Fprintf(&out, "index size: %.1f MB (%.2f bytes per indexed rune)\n", float64(size)/1e6, float64(size)/float64(totalRunes))
		fmt.Fprintf(&out, "extrapolated to the full mirror by document count: %.1f MB. This is a per-document average,\n", float64(size)*float64(fullDocs)/float64(len(docs))/1e6)
		fmt.Fprintf(&out, "not a per-byte one, and folketingssager are over-represented per MB in the sample, so treat\n")
		fmt.Fprintf(&out, "it as an order of magnitude rather than an estimate.\n")
		fmt.Fprintf(&out, "terms: %d   postings: %d\n\n", len(idx.Terms), countPostings(idx))
		if v.name == "raw+folded" {
			sweepIdx = idx
		}

		rows := map[string]*recallRow{}
		var latencies []time.Duration
		phraseChecked, phraseOK := 0, 0
		for _, q := range measurementQueries {
			truth := bruteForce(normalised, q.Text)
			perQueryStart := time.Now()
			hits := loaded.Search(Query{Text: q.Text, Limit: 10})
			latencies = append(latencies, time.Since(perQueryStart))

			row := rows[q.Group]
			if row == nil {
				row = &recallRow{group: q.Group}
				rows[q.Group] = row
			}
			row.truth += len(truth)
			if len(truth) > row.maxTruth {
				row.maxTruth = len(truth)
			}
			denom := len(truth)
			if denom == 0 {
				if !q.Precision {
					row.miss = append(row.miss, q.Text+" (nothing in the sample to find)")
				}
				continue
			}
			if denom > 10 {
				denom = 10
			}
			got := 0
			hitUnits := map[int]bool{}
			for _, h := range hits {
				hitUnits[h.Unit] = true
			}
			for _, u := range truth {
				if hitUnits[u] {
					got++
				}
			}
			row.sum += float64(got) / float64(denom)
			row.n++
			if float64(got)/float64(denom) < 0.5 {
				row.miss = append(row.miss, fmt.Sprintf("%s (%d/%d of %d corpus matches)", q.Text, got, denom, len(truth)))
			}

			if q.Group == "phrase" && len(hits) > 0 {
				phraseChecked++
				adjacent := false
				for _, h := range hits {
					if h.Unit < len(normalised) && containsPhrase(normalised[h.Unit], q.Text) {
						adjacent = true
						break
					}
				}
				if adjacent {
					phraseOK++
				}
			}
		}

		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		fmt.Fprintf(&out, "query latency over %d queries: p50 %s, p95 %s\n", len(latencies),
			latencies[len(latencies)/2].Round(time.Microsecond), latencies[int(float64(len(latencies))*0.95)].Round(time.Microsecond))
		fmt.Fprintf(&out, "recall@10 by query group:\n")
		groups := make([]string, 0, len(rows))
		for g := range rows {
			groups = append(groups, g)
		}
		sort.Strings(groups)
		for _, g := range groups {
			r := rows[g]
			if r.n == 0 {
				fmt.Fprintf(&out, "  %-14s (no measurable queries)\n", g)
				continue
			}
			fmt.Fprintf(&out, "  %-14s %.3f over %d queries, %d truth units matched (max %d for one query)%s\n",
				g, r.sum/float64(r.n), r.n, r.truth, r.maxTruth,
				func() string {
					if len(r.miss) == 0 {
						return ""
					}
					return "  worst: " + strings.Join(r.miss, ", ")
				}())
		}
		if phraseChecked > 0 {
			fmt.Fprintf(&out, "phrase queries whose top 10 actually contain the words adjacent: %d of %d\n", phraseOK, phraseChecked)
			fmt.Fprintf(&out, "(below 1.0 means term-AND ranks units where the phrase is not contiguous, which is what a\n position-storing index would buy)\n")
		}

		// Boolean operators and the proximity grade, against the same grep baseline. The
		// question is not whether they work — the unit tests settle that — but whether each
		// one beats the ranking that already existed. An operator that costs reads and
		// returns worse answers than plain OR is a feature to delete, so both numbers go on
		// the report side by side.
		fmt.Fprintf(&out, "\nboolean operators and proximity grading (grep truth over the same texts)\n")
		type boolRow struct {
			group                                          string
			miss                                           []string
			n, truthUnits, gradedUnits                     int
			plainRecall, gradedRecall                      float64
			plainGaps, gradedGaps                          []int
			adjacentPlain, adjacentGraded, adjacentQueries int
		}
		bGroups := map[string]*boolRow{}
		for _, q := range booleanQueries {
			row := bGroups[q.Group]
			if row == nil {
				row = &boolRow{group: q.Group}
				bGroups[q.Group] = row
			}
			truth := booleanTruth(normalised, q)
			row.truthUnits += len(truth)
			if len(truth) == 0 {
				row.miss = append(row.miss, q.Text+" (nothing in the sample to find)")
				continue
			}
			row.n++
			plain := loaded.Search(Query{Text: q.Text, Limit: 10})
			graded := loaded.Search(Query{Text: q.Text, Limit: 2 * 10})
			graded = loaded.GradeNear(graded, q.Text, func(h *Hit) string { return units[h.Unit].Text })
			if len(graded) > len(plain) {
				graded = graded[:len(plain)]
			}
			row.plainRecall += recallOf(truth, plain)
			row.gradedRecall += recallOf(truth, graded)
			row.gradedUnits += min(len(graded), NearWindow)
			words := loaded.queryWords(q.Text)
			for _, h := range plain {
				row.plainGaps = append(row.plainGaps, MinWordGap(units[h.Unit].Text, words))
			}
			for _, h := range graded {
				row.gradedGaps = append(row.gradedGaps, MinWordGap(units[h.Unit].Text, words))
			}
			if len(q.Adjacent) == 2 {
				row.adjacentQueries++
				if anyAdjacent(plain, normalised, q.Adjacent) {
					row.adjacentPlain++
				}
				if anyAdjacent(graded, normalised, q.Adjacent) {
					row.adjacentGraded++
				}
			}
		}
		bnames := make([]string, 0, len(bGroups))
		for g := range bGroups {
			bnames = append(bnames, g)
		}
		sort.Strings(bnames)
		for _, g := range bnames {
			r := bGroups[g]
			fmt.Fprintf(&out, "  %-6s %2d queries, %d truth units in the sample\n", g, r.n, r.truthUnits)
			if r.n == 0 {
				fmt.Fprintf(&out, "    nothing measurable: %s\n", strings.Join(r.miss, ", "))
				continue
			}
			fmt.Fprintf(&out, "    recall@10 index-only %.3f -> graded %.3f\n",
				r.plainRecall/float64(r.n), r.gradedRecall/float64(r.n))
			fmt.Fprintf(&out, "    median word gap in the top %d: %s -> %s  (%d text reads per query)\n",
				10, medianGap(r.plainGaps), medianGap(r.gradedGaps), r.gradedUnits/r.n)
			if r.adjacentQueries > 0 {
				fmt.Fprintf(&out, "    quoted queries whose top 10 contains the words adjacent: %d -> %d of %d\n",
					r.adjacentPlain, r.adjacentGraded, r.adjacentQueries)
			}
			if len(r.miss) > 0 {
				fmt.Fprintf(&out, "    not measurable: %s\n", strings.Join(r.miss, ", "))
			}
		}
		out.WriteString("\n")
		checkpoint()
	}

	// Suffix audit: which candidate endings actually merge corpus tokens, and which
	// invent stems that are not words. The list in tokens.go is pruned by this.
	// Length-normalisation sweep on one index: b is the only thing that changes, so
	// this costs three scorings rather than three builds. It exists to replace a
	// textbook constant with a corpus measurement.
	if sweepIdx != nil {
		fmt.Fprintf(&out, "## length-normalisation sweep (same index, scoring only)\n")
		for _, b := range []float64{0.75, 0.45, 0.2, 0.0} {
			sweepIdx.B = b
			line := fmt.Sprintf("  b=%.2f ", b)
			for _, group := range []string{"citation", "term", "compound-head", "phrase"} {
				sum, n := 0.0, 0
				for _, q := range measurementQueries {
					if q.Group != group {
						continue
					}
					truth := bruteForce(normalised, q.Text)
					if len(truth) == 0 {
						continue
					}
					denom := len(truth)
					if denom > 10 {
						denom = 10
					}
					got := 0
					for _, h := range sweepIdx.Search(Query{Text: q.Text, Limit: 10}) {
						for _, t := range truth {
							if t == h.Unit {
								got++
							}
						}
					}
					sum += float64(got) / float64(denom)
					n++
				}
				if n > 0 {
					line += fmt.Sprintf(" %s %.3f", group, sum/float64(n))
				}
			}
			fmt.Fprintln(&out, line)
		}
		out.WriteString("\n")
		checkpoint()
	}

	// The parity question this milestone turns on, asked of the two systems rather than of
	// a grep baseline. Comparing each unitisation's recall against grep compares each one's
	// agreement with file order over a *different* set of units: indexing twice the words
	// triples what a query matches, and two correct orderings then disagree more often
	// without either system being worse for the reader. So this asks the reader's question
	// instead: for the same query, does the model's cut still return the answers the
	// section list returned, and what does it return that the old index never held?
	if os.Getenv("RETSIN_UNITS") != "sections" {
		paritySection(t, &out, docs, units, checkpoint)
	}

	fmt.Fprintf(&out, "## fold suffix audit\n")
	fmt.Fprintf(&out, "candidate endings, measured over the sampled vocabulary:\n")
	for _, line := range auditSuffixes(units) {
		fmt.Fprintln(&out, line)
	}

	checkpoint()
	t.Log("report written to " + dest)
	fmt.Print(out.String())
}

// recallOf is recall@len(hits) of one result list against a grep truth set: what fraction of
// the ten units grep would have shown first are actually in this list. The denominator is
// capped the same way the harness caps it everywhere else, so the number means "agreement about
// the visible page", not "found everything".
func recallOf(truth []int, hits []Hit) float64 {
	if len(truth) == 0 {
		return 0
	}
	denom := min(len(truth), len(hits))
	if denom == 0 {
		return 0
	}
	got := 0
	for _, h := range hits {
		for _, u := range truth {
			if u == h.Unit {
				got++
				break
			}
		}
	}
	return float64(got) / float64(denom)
}

// anyAdjacent reports whether any hit in the list is a unit where the pair really does sit next
// to each other.
func anyAdjacent(hits []Hit, normalised []string, pair []string) bool {
	for _, h := range hits {
		if h.Unit < len(normalised) && adjacentPair(normalised[h.Unit], pair[0], pair[1]) {
			return true
		}
	}
	return false
}

// medianGap summarises how close the query words sat across a result page. "none" means the
// words could not be located in the returned text at all, which is a finding, not a zero.
func medianGap(gaps []int) string {
	seen := make([]int, 0, len(gaps))
	for _, g := range gaps {
		if g >= 0 {
			seen = append(seen, g)
		}
	}
	if len(seen) == 0 {
		return "none"
	}
	sort.Ints(seen)
	return fmt.Sprintf("%d over %d hits", seen[len(seen)/2], len(seen))
}

// bruteForce returns the unit indexes whose lower-cased text contains every query
// word as a substring — the grep-equivalent truth set.
func bruteForce(texts []string, query string) []int {
	words := Tokenize(query)
	if len(words) == 0 {
		return nil
	}
	var out []int
	for i, text := range texts {
		all := true
		for _, w := range words {
			if !strings.Contains(text, w) {
				all = false
				break
			}
		}
		if all {
			out = append(out, i)
		}
	}
	return out
}

// containsPhrase reports whether the words appear next to each other, which term-AND
// alone does not guarantee.
//
// It accepts the phrase joined by a space, hyphen or slash, and as a looser second
// chance, the first word anywhere within a short window before the second — Danish
// puts an inflection on the first word of some fixed phrases, and the point of this
// check is to measure how often term-AND ranking finds the phrase, not to be a
// stricter matcher than the thing it is auditing.
func containsPhrase(text, query string) bool {
	words := Tokenize(query)
	if len(words) < 2 {
		return true
	}
	for _, sep := range []string{" ", "-", "/"} {
		if strings.Contains(text, strings.Join(words, sep)) {
			return true
		}
	}
	for at := strings.Index(text, words[1]); at >= 0; {
		from := at - 60
		if from < 0 {
			from = 0
		}
		if strings.Contains(text[from:at], words[0]) {
			return true
		}
		rest := at + len(words[1])
		if rest > len(text) {
			break
		}
		rel := strings.Index(text[rest:], words[1])
		if rel < 0 {
			break
		}
		at = rest + rel
	}
	return false
}

// unitIndexOf is unnecessary: Search reports the sampled unit index directly, which
// is also what snippet rendering needs.

func countPostings(idx *Index) int {
	n := 0
	for _, p := range idx.Postings {
		n += len(p)
	}
	return n
}

func joinCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	return strings.Join(parts, " ")
}

// auditSuffixes reports, per candidate ending, how many vocabulary tokens it folds
// and how many of those folds land on another real word.
func auditSuffixes(units []Unit) []string {
	vocab := map[string]int{}
	for _, u := range units {
		for _, tok := range Tokenize(u.Text) {
			vocab[tok]++
		}
	}
	byEnding := map[string]struct{ folded, merged, hits int }{}
	for _, ending := range FoldSuffixes {
		folded, merged := 0, 0
		for tok, n := range vocab {
			if len(tok) <= len(ending)+2 || !strings.HasSuffix(tok, ending) {
				continue
			}
			stem := tok[:len(tok)-len(ending)]
			folded += n
			if _, ok := vocab[stem]; ok {
				merged++
			}
		}
		byEnding[ending] = struct{ folded, merged, hits int }{folded, merged, vocab[ending]}
	}
	lines := make([]string, 0, len(FoldSuffixes))
	for _, ending := range FoldSuffixes {
		v := byEnding[ending]
		lines = append(lines, fmt.Sprintf("  -%-6s folds %d token occurrences; %d distinct folds that are themselves corpus words",
			ending, v.folded, v.merged))
	}
	return lines
}

// sectionsUnits replays the unitisation this milestone replaces, over the same sampled
// documents, so the measurement compares two ways of cutting a document rather than two
// samples of the corpus. It reads the archived XML again rather than caching a copy: the
// unitiser under test reads the same files, and a fixture here would drift from them.
func sectionsUnits(t *testing.T, docs []DocMeta) []Unit {
	out := make([]Unit, 0, len(docs)*12)
	for docID, meta := range docs {
		if strings.TrimSpace(meta.Title) != "" {
			out = append(out, Unit{Doc: docID, Addr: "titel", Text: meta.Title})
		}
		// LoadWithBody, not Parse: the shipped sampler opens documents this way, and the
		// HTML body is most of an html-sourced document. Replaying with bare Parse silently
		// dropped half the indexed text and made the comparison measure a reader bug.
		doc, err := lexdania.LoadWithBody(meta.Path)
		if err != nil {
			t.Fatalf("reopen %s for the sections comparison: %v", meta.Path, err)
		}
		secs := doc.Sections()
		if len(secs) == 0 {
			if text := strings.TrimSpace(lexdania.DisplayText(doc.Root)); text != "" {
				out = append(out, Unit{Doc: docID, Addr: "", Text: text})
			}
			continue
		}
		for _, s := range secs {
			text := strings.TrimSpace(s.Display())
			if text == "" {
				continue
			}
			out = append(out, Unit{Doc: docID, Addr: lexdania.AddressOf(s), Text: text})
		}
	}
	return out
}

// coverageDocs is how many documents the word-coverage measure walks. It is a subset
// because it re-reads each document and compares word sets, which is quadratic in nothing
// but still costs a full pass over the sample.
const coverageDocs = 500

// wordCoverage reports the fraction of served words that the document's own indexed units
// also contain. Per document, so that a unit in one law cannot pay for a gap in another.
func wordCoverage(t *testing.T, docs []DocMeta, units []Unit, limit int) float64 {
	byDoc := make(map[int][]string, limit)
	for _, u := range units {
		byDoc[u.Doc] = append(byDoc[u.Doc], u.Text)
	}
	// Strided rather than the first N: the sample is grouped by stratum, so the first N
	// documents are one kind of law and their coverage says nothing about the rest.
	step := len(docs) / limit
	if step < 1 {
		step = 1
	}
	seen, hit, walked := 0, 0, 0
	for id, meta := range docs {
		if walked >= limit {
			break
		}
		if id%step != 0 {
			continue
		}
		doc, err := lexdania.LoadWithBody(meta.Path)
		if err != nil {
			continue
		}
		walked++
		served := strings.ToLower(doc.Model().Content.PlainText())
		indexed := strings.ToLower(strings.Join(byDoc[id], " "))
		for _, w := range strings.Fields(served) {
			if len(w) < 6 {
				continue
			}
			seen++
			if strings.Contains(indexed, w) {
				hit++
			}
		}
	}
	if seen == 0 {
		return 0
	}
	return float64(hit) / float64(seen)
}

// paritySection measures the shipped unitisation against the section list's, query for
// query, on the same documents.
func paritySection(t *testing.T, out *strings.Builder, docs []DocMeta, newUnits []Unit, checkpoint func()) {
	oldUnits := sectionsUnits(t, docs)
	oldIdx, err := Build(context.Background(), docs, oldUnits, Options{})
	if err != nil {
		t.Fatalf("build the sections index for the parity run: %v", err)
	}
	newIdx, err := Build(context.Background(), docs, newUnits, Options{})
	if err != nil {
		t.Fatalf("build the model index for the parity run: %v", err)
	}
	// What the old index held, per document, so a new answer can be called new only if the
	// old index never had those words at all.
	// Keyed by accession: a Hit carries the document itself, not an index into this sample.
	newByDoc := make(map[string]string, len(docs))
	for _, u := range newUnits {
		newByDoc[docs[u.Doc].Accession] += " " + u.Text
	}
	for id := range newByDoc {
		newByDoc[id] = normaliseForCompare(newByDoc[id])
	}
	oldByDoc := map[string]string{}
	for _, u := range oldUnits {
		oldByDoc[docs[u.Doc].Accession] += " " + u.Text
	}
	for id := range oldByDoc {
		oldByDoc[id] = normaliseForCompare(oldByDoc[id])
	}

	type row struct {
		old, carried, gained, newOnly int
		moved                         int // its words are indexed, only ranked elsewhere
		lost                          []string
	}
	rows := map[string]*row{}
	for _, q := range measurementQueries {
		if strings.ContainsAny(q.Text, `~"-`) || strings.Contains(q.Text, " AND ") {
			continue // graded separately, against their own truth
		}
		r := rows[q.Group]
		if r == nil {
			r = &row{}
			rows[q.Group] = r
		}
		oldHits := oldIdx.Search(Query{Text: q.Text, Limit: 10})
		newHits := newIdx.Search(Query{Text: q.Text, Limit: 10})
		newTexts := make([]string, 0, len(newHits))
		for _, h := range newHits {
			newTexts = append(newTexts, normaliseForCompare(newUnits[h.Unit].Text))
		}
		r.old += len(oldHits)
		for _, h := range oldHits {
			ot := normaliseForCompare(oldUnits[h.Unit].Text)
			carried := false
			for _, nt := range newTexts {
				if sameAnswer(ot, nt) {
					carried = true
					break
				}
			}
			if carried {
				r.carried++
				continue
			}
			// Not in the new top 10. That is a ranking difference unless the words are
			// nowhere in the new index for that document, which is the only case worth
			// calling a loss.
			if wordsReachable(ot, newByDoc[h.Doc.Accession]) {
				r.moved++
				continue
			}
			if len(r.lost) < 3 {
				// Name what is gone rather than the answer it came from: the difference
				// between "a word is unfindable" and "a token the old index glued together
				// out of two elements is spelled differently now" is the whole point of
				// reporting an unreachable answer.
				var gone []string
				for _, w := range strings.Fields(ot) {
					if len(w) < 5 || strings.Contains(newByDoc[h.Doc.Accession], w) {
						continue
					}
					if len(gone) < 4 {
						gone = append(gone, w)
					}
				}
				r.lost = append(r.lost, q.Text+": "+strings.Join(gone, ", "))
			}
		}
		for j, nt := range newTexts {
			h := newHits[j]
			oldText := oldByDoc[h.Doc.Accession]
			if strings.Contains(oldText, nt) || (len(oldText) > 60 && strings.Contains(nt, oldText[:60])) {
				continue
			}
			r.gained++
		}
		r.newOnly += len(newHits)
	}
	groups := make([]string, 0, len(rows))
	for g := range rows {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	fmt.Fprintf(out, "## answer parity: model units vs section units\n")
	fmt.Fprintf(out, "For each query, the answers the section index returned in its top 10, and whether the\n")
	fmt.Fprintf(out, "model's index returned that answer too (same words, either direction of containment, since\n")
	fmt.Fprintf(out, "the two cut passages of different sizes). \u0022new content\u0022 counts answers whose words the\n")
	fmt.Fprintf(out, "section index did not hold anywhere in that document.\n")
	totOld, totCarried, totGained := 0, 0, 0
	for _, g := range groups {
		r := rows[g]
		totOld += r.old
		totCarried += r.carried
		totGained += r.gained
		fmt.Fprintf(out, "  %-14s %4d old answers, %4d carried (%.3f), %4d ranked elsewhere, %4d unreachable, %4d answers only the model's index can return\n",
			g, r.old, r.carried, float64(r.carried)/float64(max(r.old, 1)), r.moved, r.old-r.carried-r.moved, r.gained)
		for _, l := range r.lost {
			fmt.Fprintf(out, "      not carried: %s\n", l)
		}
	}
	fmt.Fprintf(out, "  TOTAL          %4d old answers, %4d carried (%.3f)\n", totOld, totCarried, float64(totCarried)/float64(max(totOld, 1)))
	out.WriteString("\n")
	checkpoint()
}

// wordsReachable reports whether an old answer's words are all still indexed for that
// document. Substring containment is too strict to ask this: a section's unit text was
// assembled by joining heading and body with different spacing than the renderer uses, so
// the same words can sit in a different-shaped unit and still be findable. What a reader
// can lose is a word, not a byte offset.
func wordsReachable(answer, doc string) bool {
	missing, seen := 0, 0
	for _, w := range strings.Fields(answer) {
		if len(w) < 5 {
			continue
		}
		seen++
		if !strings.Contains(doc, w) {
			missing++
		}
	}
	if seen == 0 {
		return true
	}
	return float64(missing)/float64(seen) < 0.05
}

// normaliseForCompare folds away the differences that are not different answers: case,
// repeated whitespace, and the citation a condition unit repeats.
func normaliseForCompare(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "\u00a0", " ")
	return strings.Join(strings.Fields(s), " ")
}

// sameAnswer reports whether two index units are the same passage for a reader: one
// contains the other, or they share most of their words.
func sameAnswer(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) > 40 && len(b) > 40 {
		if strings.Contains(a, b) || strings.Contains(b, a) {
			return true
		}
	}
	ta, tb := strings.Fields(a), strings.Fields(b)
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	set := make(map[string]bool, len(ta))
	for _, w := range ta {
		set[w] = true
	}
	shared := 0
	for _, w := range tb {
		if set[w] {
			shared++
		}
	}
	smaller := min(len(ta), len(tb))
	return float64(shared)/float64(smaller) > 0.6
}
