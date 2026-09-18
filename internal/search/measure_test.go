package search

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
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

// TestMeasureIndex compares index variants against brute-force truth and writes the
// report this milestone's decision rests on.
//
// Gated on RETSIN_SEARCH_MEASURE because it reads the real mirror: it needs the
// corpus, and an ordinary `go test ./...` must not depend on 14 GB of data that CI
// and fresh clones do not have.
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
	docs, units, err := Sample(root, perStratum)
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if len(docs) < 500 || len(units) < 2000 {
		t.Fatalf("sample too small to measure: %d docs, %d units", len(docs), len(units))
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
	fmt.Fprintf(&out, "# retsin search index measurement\n")
	fmt.Fprintf(&out, "# generated: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&out, "# reproduce: RETSIN_SEARCH_MEASURE=1 RETSIN_SEARCH_SAMPLE=%d go test ./internal/search -run TestMeasureIndex -v\n", perStratum)
	fmt.Fprintf(&out, "# corpus sample: %d documents, %d index units, %.1f MB of indexed text, sampled in %s\n",
		len(docs), len(units), float64(totalRunes)/1e6, sampleText.Round(time.Millisecond))
	fmt.Fprintf(&out, "# sources: %s\n", joinCounts(bySource))
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

	dest := "testdata/index-measurement-20260917.txt"
	if _, err := os.Stat("internal/search/testdata"); err == nil {
		dest = "internal/search/testdata/index-measurement-20260917.txt"
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
	}{
		{"raw", Options{}},
		{"raw+folded", Options{Folded: true}},
	}

	type recallRow struct {
		group string
		sum   float64
		n     int
		miss  []string
	}

	for _, v := range variants {
		buildStart := time.Now()
		idx, err := Build(docs, units, v.opts)
		if err != nil {
			t.Fatalf("build %s: %v", v.name, err)
		}
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
			fmt.Fprintf(&out, "  %-14s %.3f over %d queries%s\n", g, r.sum/float64(r.n), r.n,
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

	fmt.Fprintf(&out, "## fold suffix audit\n")
	fmt.Fprintf(&out, "candidate endings, measured over the sampled vocabulary:\n")
	for _, line := range auditSuffixes(units) {
		fmt.Fprintln(&out, line)
	}

	checkpoint()
	t.Log("report written to " + dest)
	fmt.Print(out.String())
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
