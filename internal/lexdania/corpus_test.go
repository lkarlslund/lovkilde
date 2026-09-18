package lexdania

import (
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// corpusDir locates the archive for real-data tests. It is skipped unless the
// directory exists, so CI without a crawl still passes while a machine holding the
// archive measures the theory against real documents.
//
// The crawl writes concurrently with these tests. A file can therefore be read half
// written, so parse failures are counted and tolerated up to a small fraction rather
// than treated as package bugs — and the count is logged, because quietly ignoring
// corruption would be worse.
func corpusDir(t *testing.T) string {
	dir := os.Getenv("RETSIN_TEST_DATA")
	if dir == "" {
		dir = filepath.Join("..", "..", "data", "xml")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("no archive at %s; set RETSIN_TEST_DATA to run against real documents", dir)
	}
	return dir
}

// recentLawEntries orders archive entries so a test that needs a modern amending-act
// chain meets one within its first few looks. Index order starts at 1852, where a "chain"
// is a re-accession of a century-old act carrying no amendment markup, so walking in that
// order means parsing and consolidating thousands of documents before anything interesting
// appears — which is how a 25-second test turned into a four-minute one under -race.
//
// limit bounds the work. Zero means walk everything, which is what a deliberate hunt asks
// for; ordinary runs, including -race, pass a budget, because reading the whole archive is
// something to do when looking for an error, not on every regression check.
func recentLawEntries(entries []Entry, limit int) []Entry {
	recent := make([]Entry, 0, len(entries))
	for _, e := range entries {
		switch e.Kind {
		case "lta", "ltb", "mt", "ltc":
			if e.Year >= 2010 {
				recent = append(recent, e)
			}
		}
	}
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].Year > recent[j].Year })
	if limit <= 0 {
		// Unbounded: recent first, then the rest, so a hunt that walks to the end still
		// reaches the old material.
		return append(recent, entries...)
	}
	if len(recent) > limit {
		return recent[:limit]
	}
	return recent
}

// corpusEntryBudget is how many entries a corpus test examines by default.
func corpusEntryBudget(t *testing.T) int {
	if os.Getenv("RETSIN_FULL_SCAN") == "1" {
		return 0
	}
	return 400
}

func sampleFiles(t *testing.T, dir string, n int) []string {
	var files []string
	for _, kind := range []string{"lta", "retsinfo", "ft", "fob", "mt", "ltc", "ltb"} {
		matches, err := filepath.Glob(filepath.Join(dir, kind, "*", "*.xml"))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	if len(files) == 0 {
		t.Skip("archive has no documents yet")
	}
	r := rand.New(rand.NewSource(7)) // fixed seed: failures must be reproducible
	r.Shuffle(len(files), func(i, j int) { files[i], files[j] = files[j], files[i] })
	if len(files) > n {
		files = files[:n]
	}
	return files
}

// TestCorpusParseSample proves the parser handles real documents across every kind,
// and that the mixed-content invariant holds on data rather than only on fixtures.
func TestCorpusParseSample(t *testing.T) {
	dir := corpusDir(t)
	files := sampleFiles(t, dir, 400)

	var failed, warned, unmapped int
	kinds := map[string]int{}
	for _, f := range files {
		doc, err := Load(f)
		if err != nil {
			// The crawl may have been mid-write; report the rate rather than failing.
			failed++
			t.Logf("parse failed: %v", err)
			continue
		}
		kinds[doc.Meta.AnnouncedIn]++
		if err := doc.Root.checkInvariant(""); err != nil {
			t.Errorf("%s: %v", f, err)
		}
		if !doc.Accession().WellFormed {
			t.Errorf("%s: accession %q is not rank+year+number+code", f, doc.Meta.Accession)
		}
		if len(doc.Meta.Rest) > 0 {
			unmapped++
			t.Errorf("%s: unmapped Meta children %v", f, names(doc.Meta.Rest))
		}
		if w := doc.Warnings(); len(w) > 0 {
			warned++
			t.Logf("%s: %v", f, w)
		}
	}

	rate := float64(failed) / float64(len(files))
	if rate > 0.02 {
		t.Errorf("%d of %d documents failed to parse (%.1f%%), above the 2%% tolerance for a concurrently-written archive",
			failed, len(files), 100*rate)
	}
	t.Logf("parsed %d documents, %d parse failures, %d with warnings, %d with unmapped Meta children",
		len(files), failed, warned, unmapped)
}

// TestCorpusElementCoverage reports which element names appear in real documents,
// making "did we map everything" a measured statement. It fails only if a Meta child
// is unmapped, since Meta is the part this package claims to model completely.
func TestCorpusElementCoverage(t *testing.T) {
	dir := corpusDir(t)
	files := sampleFiles(t, dir, 250)

	seen := map[string]int{}
	for _, f := range files {
		doc, err := Load(f)
		if err != nil {
			continue
		}
		for name, c := range doc.Root.Stats() {
			seen[name] += c.Seen
		}
	}
	if len(seen) == 0 {
		t.Skip("no documents parsed")
	}
	t.Logf("%d distinct element names across %d documents", len(seen), len(files))
	for name, n := range seen {
		if n > 20000 {
			t.Logf("  %-40s %d", name, n)
		}
	}
}

// TestCorpusAmendmentCoverage measures extraction against real amending acts. The
// thresholds are deliberately modest: they guard against a regression, while the
// logged numbers are what tells us how much of the pattern set is still missing.
func TestCorpusAmendmentCoverage(t *testing.T) {
	dir := corpusDir(t)
	files := sampleFiles(t, dir, 1200)

	var total, docs int
	byKind := map[EditKind]int{}
	unrecAddr := 0
	for _, f := range files {
		doc, err := Load(f)
		if err != nil || !doc.IsAmendment() {
			continue
		}
		edits := doc.Amendments()
		if len(edits) == 0 {
			continue
		}
		docs++
		st := Coverage(edits)
		total += st.Total
		unrecAddr += st.UnrecognisedAddress
		for k, n := range st.ByKind {
			byKind[k] += n
		}
		if docs >= 25 {
			break
		}
	}
	if total == 0 {
		t.Skip("no numbered amendments in this sample")
	}
	recognised := total - unrecAddr
	t.Logf("%d amending documents, %d numbered changes; %d of %d (%.0f%%) had a recognised address; kinds %v",
		docs, total, recognised, total, 100*float64(recognised)/float64(total), byKind)

	if docs == 0 {
		t.Error("no amending documents found in a sample of 1,200 documents")
	}
	// A regression that stopped extracting changes at all would show up here.
	if byKind[EditUnknown] == total {
		t.Errorf("no amendment was classified; kinds = %v", byKind)
	}
	if recognised*3 < total {
		t.Errorf("only %d of %d addresses recognised: the pattern set is inadequate", recognised, total)
	}
}

// TestCorpusChainWalksRealDocuments exercises Versions and LawAsOf end to end on
// documents that really have chains, and reports how complete the archive currently
// makes them. It cannot assert correctness of derived text — that needs the source's
// own consolidation as ground truth — but it does assert that nothing crashes, that
// nothing mutates the base, and that incompleteness is reported rather than hidden.
func TestCorpusChainWalksRealDocuments(t *testing.T) {
	dir := corpusDir(t)
	a, err := Scan(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.Len() == 0 {
		t.Skip("empty archive")
	}
	t.Logf("indexed %d documents (%d skipped)", a.Len(), a.Skipped)

	entries := a.Entries()

	// Recent laws and orders first: see recentLawEntries for why index order is the wrong
	// order to look in. This test has its own stop conditions, so it is not bounded here.
	preferred := recentLawEntries(entries, 0)

	chained := 0
	viewed := 0
	// "Law chains" are chains whose members are amending acts. Only those must yield
	// extractable changes: in the ruling collections (fob, retsinfo) a Change
	// reference links a document to a *newer accession of the same ruling*, which
	// carries no amendment markup at all. Requiring changes from those was a bug in
	// this test, not in the parser.
	lawChains := 0
	lawChainsWithChanges := 0
	totalApplied, totalUnresolved, totalForeign, totalBills := 0, 0, 0, 0
	for _, e := range preferred {
		if lawChains >= 5 || chained >= 25 || viewed >= 10 {
			break
		}
		vs, err := a.Versions(e.Accession)
		if err != nil || len(vs) == 0 {
			continue
		}
		chained++
		found := 0
		isLawChain := false
		for _, v := range vs {
			if !v.Found {
				continue
			}
			found++
			if d, _, ok := a.Open(v.Accession); ok && d.IsAmendment() && !d.IsBill() {
				isLawChain = true
			}
		}
		t.Logf("%s (%s): %d chain documents, %d present, amending-act chain: %v",
			e.Accession, e.ELIPath(), len(vs), found, isLawChain)
		if found == 0 {
			continue
		}

		view, err := a.LawAsOf(e.Accession, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		viewed++
		if err := view.Doc.Root.checkInvariant(""); err != nil {
			t.Errorf("%s: derived tree broken: %v", e.Accession, err)
		}
		if view.Trustworthy() && len(view.Unresolved) > 0 {
			t.Errorf("%s: Trustworthy() true with unresolved edits", e.Accession)
		}
		if isLawChain {
			lawChains++
			if len(view.Applied)+len(view.Unresolved) > 0 {
				lawChainsWithChanges++
			}
		}
		totalApplied += len(view.Applied)
		totalUnresolved += len(view.Unresolved)
		totalForeign += view.SkippedForeign
		totalBills += view.SkippedBills
		t.Logf("  as of today: %d applied, %d unresolved, %d aimed at other laws, %d bills skipped; %v",
			len(view.Applied), len(view.Unresolved), view.SkippedForeign, view.SkippedBills, view.Trustworthy())
		for _, u := range view.Unresolved[:min(3, len(view.Unresolved))] {
			t.Logf("  unresolved: %s (%s) — %s", u.Edit.Number, u.Edit.Kind, Squeeze(u.Reason))
		}
	}
	t.Logf("totals over %d viewed documents: %d applied, %d unresolved, %d foreign, %d bills",
		viewed, totalApplied, totalUnresolved, totalForeign, totalBills)
	if chained == 0 {
		t.Skip("no documents with a Change chain in this archive slice")
	}
	if lawChains > 0 && lawChainsWithChanges == 0 {
		t.Errorf("%d chains of amending acts produced no extractable changes at all", lawChains)
	}
}

// TestCorpusBaseNotMutatedOnRealData repeats the no-mutation guarantee on real
// documents, which are large enough that a shallow copy could hide a shared subtree.
func TestCorpusBaseNotMutatedOnRealData(t *testing.T) {
	dir := corpusDir(t)
	a, err := Scan(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	// A bounded slice, and more than the one document this used to stop at: the guarantee
	// is that the derived view shares no writable node with the base, and a few dozen
	// modern laws with applied chains say that better than the first document in accession
	// order does — which, in a corpus beginning in 1852, meant consolidating thousands of
	// documents to reach a single one.
	checked := 0
	for _, e := range recentLawEntries(a.Entries(), corpusEntryBudget(t)) {
		vs, err := a.Versions(e.Accession)
		if err != nil || len(vs) == 0 {
			continue
		}
		doc, _, ok := a.Open(e.Accession)
		if !ok {
			continue
		}
		before := doc.Root.PlainText()
		v := LawAsOf(doc, time.Now(), a.Open)
		if len(v.Applied) == 0 {
			continue
		}
		if after := doc.Root.PlainText(); after != before {
			t.Fatalf("%s: LawAsOf mutated the base document", e.Accession)
		}
		checked++
		if checked >= 25 {
			break
		}
	}
	if checked == 0 {
		t.Skip("no applicable chain in this archive slice")
	}
	t.Logf("base text unchanged over %d documents with applied chains", checked)
}
