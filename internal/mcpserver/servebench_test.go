package mcpserver

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/search"
)

// TestServingBaselineCost measures what the shipped serving path costs per query, so that moving it
// onto the compiled artifact is a measured change rather than a refactor with a story. Today a
// lookup parses the publisher's XML for the document asked for, and a search answer parses it again
// for each snippet; these are the numbers those paths have to beat.
//
//	RETSIN_SERVE_BENCH=1 go test ./internal/mcpserver -run TestServingBaselineCost -v
//
// The fixture is the mirror itself rather than a hand-written list: every nth document in archive
// order, each looked up whole and once addressed at an address the shipped unitiser produced for it.
// An address list someone typed by hand would flatter the measurement by only containing addresses
// that work.
func TestServingBaselineCost(t *testing.T) {
	if os.Getenv("RETSIN_SERVE_BENCH") == "" {
		t.Skip("set RETSIN_SERVE_BENCH=1 to run; it reads the mirror")
	}
	root := os.Getenv("RETSIN_DATA_DIR")
	if root == "" {
		root = filepath.Join("..", "..", "data")
	}
	want := 1000
	if v := os.Getenv("RETSIN_SERVE_BENCH_DOCS"); v != "" {
		fmt.Sscanf(v, "%d", &want)
	}

	scanStart := time.Now()
	a, err := lexdania.Scan(t.Context(), filepath.Join(root, "xml"))
	if err != nil {
		t.Fatal(err)
	}
	scanCost := time.Since(scanStart)
	entries := a.Entries()
	if len(entries) < want {
		t.Fatalf("only %d entries in %s", len(entries), root)
	}

	// Pick the sample first, and derive an address for each from its own sections, before anything
	// is timed: choosing an address is not part of what a query costs.
	stride := len(entries) / want
	type fixture struct {
		accession string
		address   string
	}
	var fixtures []fixture
	for i := 0; i < len(entries) && len(fixtures) < want; i += max(stride, 1) {
		e := entries[i]
		if e.Accession == "" {
			continue
		}
		f := fixture{accession: e.Accession}
		if doc, _, ok := a.Open(e.Accession); ok {
			sections := doc.Sections()
			if len(sections) > 0 {
				f.address = search.AddressOf(sections[len(sections)/2])
			}
		}
		fixtures = append(fixtures, f)
	}
	if len(fixtures) < want/2 {
		t.Fatalf("only %d usable documents", len(fixtures))
	}

	s := New(a, "https://www.retsinformation.dk", "bench")

	var parse, whole, addressed []time.Duration
	var answered, withAddress int
	cited := 0
	unresolved := map[string][]string{}
	for _, f := range fixtures {
		start := time.Now()
		doc, _, ok := a.Open(f.accession)
		if ok {
			doc.Model()
		}
		parse = append(parse, time.Since(start))
		if !ok {
			continue
		}

		start = time.Now()
		res, err := s.Lookup(LookupRequest{Document: f.accession})
		if err != nil {
			t.Errorf("%s: %v", f.accession, err)
			continue
		}
		whole = append(whole, time.Since(start))
		if res != nil && res.Text != "" {
			answered++
		}

		if f.address == "" {
			continue
		}
		cited++
		start = time.Now()
		res, err = s.Lookup(LookupRequest{Document: f.accession, Address: f.address})
		if err != nil {
			// An address the shipped unitiser produced that lookup cannot open is a finding, not
			// a harness bug: a search hit whose address cannot be re-opened is a hit the reader
			// cannot read. Counted by shape, bounded below, and reported.
			unresolved[addressShape(f.address)] = append(unresolved[addressShape(f.address)], f.accession+" "+f.address)
			continue
		}
		addressed = append(addressed, time.Since(start))
		if res != nil && res.Text != "" {
			withAddress++
		}
	}
	if len(whole) < want/2 {
		t.Fatalf("only %d lookups completed", len(whole))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# serving path baseline, before the compiled artifact\n# generated: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "# reproduce: RETSIN_SERVE_BENCH=1 RETSIN_SERVE_BENCH_DOCS=%d go test ./internal/mcpserver -run TestServingBaselineCost -v\n", want)
	fmt.Fprintf(&b, "# mirror: %d entries scanned in %v; %d documents queried, %d answered text\n",
		len(entries), scanCost.Round(time.Millisecond), len(whole), answered)
	fmt.Fprintf(&b, "  open+parse+model (the share the artifact removes): %s\n", percentiles(parse))
	fmt.Fprintf(&b, "  whole-document lookup:                            %s\n", percentiles(whole))
	fmt.Fprintf(&b, "  addressed lookup:                                 %s\n", percentiles(addressed))
	// This is the finding the latency numbers would otherwise hide: the index's cited address is
	// how a hit becomes a readable answer, and a share of them do not open. Counted per shape,
	// bounded so it may only fall, and stated as a rate because the raw count means nothing
	// without how many were tried.
	failed := len(unresolvedFlatten(unresolved))
	fmt.Fprintf(&b, "\n## addresses the shipped unitiser cites\n")
	fmt.Fprintf(&b, "  %d of %d queried documents yielded a cited address; %d of those (%.0f%%) do not open through lookup:\n",
		cited, len(whole), failed, 100*float64(failed)/float64(max(cited, 1)))
	for shape, examples := range unresolved {
		// The bound is a ratchet: this class of address is measured debt with a number attached, so
		// a change that made it worse fails rather than blending into a latency report, and a fix
		// shows up as a bound that has to be lowered.
		if len(examples) > unresolvedBound(shape) {
			t.Errorf("%d cited addresses of shape %q do not resolve, the recorded debt was %d; first two: %v",
				len(examples), shape, unresolvedBound(shape), examples[:min(len(examples), 2)])
		} else if len(examples) < unresolvedBound(shape) {
			t.Logf("shape %q fell to %d unresolved from its recorded %d: lower the bound to lock the gain in",
				shape, len(examples), unresolvedBound(shape))
		}
		fmt.Fprintf(&b, "  shape %-12s %4d unresolved (recorded debt %d) e.g. %v\n",
			shape, len(examples), unresolvedBound(shape), examples[:min(len(examples), 2)])
	}
	fmt.Fprintf(&b, "\n  startup index build is %v over %d documents, which is why it is rebuilt\n", scanCost.Round(time.Millisecond), len(entries))
	fmt.Fprintf(&b, "  at boot rather than cached; the cost a query feels is the parse, and that\n")
	fmt.Fprintf(&b, "  is the number the artifact has to move.\n")

	report := filepath.Join("testdata", "serve-baseline-"+time.Now().Format("20060102")+".txt")
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + b.String() + "\nreport written to " + report)
}

// percentiles summarises a latency sample the way a reader of a report wants it: the typical case,
// the bad tail, and how many observations those came from.
func percentiles(samples []time.Duration) string {
	if len(samples) == 0 {
		return "no samples"
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	at := func(q float64) time.Duration {
		return sorted[min(int(float64(len(sorted)-1)*q), len(sorted)-1)]
	}
	var total time.Duration
	for _, d := range samples {
		total += d
	}
	return fmt.Sprintf("p50 %v, p95 %v, p99 %v, mean %v (n=%d)",
		at(0.50).Round(time.Microsecond), at(0.95).Round(time.Microsecond), at(0.99).Round(time.Microsecond),
		(total / time.Duration(len(samples))).Round(time.Microsecond), len(samples))
}

// addressShape reduces a cited address to the form that failed, so the report says what kind of
// thing is unresolvable rather than listing thousands of near-identical strings.
func addressShape(addr string) string {
	switch {
	case strings.TrimSpace(addr) == "":
		return "empty"
	case !strings.Contains(addr, "\u00a7") && strings.Contains(addr, "stk."):
		return "stykke with no paragraph"
	case strings.HasSuffix(addr, " ") || strings.HasSuffix(strings.TrimSpace(addr), "stk."):
		return "number missing after the word"
	case strings.HasPrefix(addr, "\u00a7"):
		return "paragraph number"
	default:
		return "other"
	}
}

// unresolvedBound records how many cited addresses of each shape failed to open on 2026-09-18,
// measured over 1,000 sampled documents. A number may only fall.
func unresolvedBound(shape string) int {
	switch shape {
	case "number missing after the word":
		// "bilag " and "kapitel " with the number left in the heading beside the element: the
		// unitiser keys the section without a number, so nothing matches it.
		return 135
	case "stykke with no paragraph":
		// ", stk. 1": a subsection of a section the document never numbers.
		return 44
	case "paragraph number":
		// "§ 2, stk. 1" in documents whose provisions restart or are not the § series the
		// address claims (parliamentary stages and proposals).
		return 71
	default:
		return 0
	}
}

func unresolvedFlatten(m map[string][]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v...)
	}
	return out
}
