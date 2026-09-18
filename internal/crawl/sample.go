// Paired ground truth: the site's own HTML measured against the canonical XML of
// the very same document.
//
// The HTML reader (internal/lexdania) has so far been validated against 51
// documents. Every claim about which residual classes matter is therefore a claim
// about a sample chosen by hand, and corner cases finetuned in that order get
// fixed in the wrong sequence. This fixes the sampling, not the reader.
//
// The reference is the canonical <DokumentIndhold> the archive already holds, not
// the text served by a third-party API. An aggregator's transcription cannot
// settle whether our reader is wrong; two representations released by the same
// publisher from the same source can, because a disagreement between them is
// evidence about the reader either way -- and when they agree, nothing is proven
// about either. So a candidate here is a document that HAS an XML body: for those
// the XML is the reference and documentHtml is the thing on trial. That is the
// opposite population to the body backfill, which fetches precisely the documents
// whose XML has no text.
//
// Two phases, because reading 190k files costs CPU and 2k requests cost
// politeness budget. PlanBodySample reads the archive, picks a stratified sample,
// and writes a manifest; the operator looks at it. FetchBodySample then works off
// that manifest, resumably, and writes payloads into its own directory.
package crawl

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lkarlslund/retsinformation/internal/archive"
	"github.com/lkarlslund/retsinformation/internal/docapi"
)

// SampleManifestHeader marks a manifest file and carries the seed, so a plan made
// today can be reproduced or compared after a month of archive growth.
const SampleManifestHeader = "# retsin sample manifest v1"

// SampleStratum is one labelled bucket of the paired-truth sample.
//
// Needles are canonical element names, and only canonical element names: the list
// pinned in internal/lexdania/testdata/canonical-elements.txt is the vocabulary
// the archive actually uses, and sample_test.go refuses a needle that is not in
// it. Sampling for an element name invented from memory selects documents that do
// not have the shape being studied.
type SampleStratum struct {
	Name    string
	Needles []string // any of these, as XML start tags; empty means no requirement
	MinKB   int      // file size gate, for the stress cases
	MinYear int      // publication-year gate, lower bound
	MaxYear int      // publication-year gate, upper bound
	Why     string   // printed in the report, so a quota is never a bare number
}

// sampleStrata are declared in the order they are filled, with keyed fields so that
// adding a gate does not silently shift every row.
//
// The targeted strata pick first and "random" picks from what is left, which is
// the right way round: the targeted shapes are the scarce ones, and a quota that
// lost a contention with the large unbiased bucket might get none at all. The
// cost is that "random" is random among the unchosen rather than among the whole
// corpus, which is stated here rather than hidden in a distribution nobody reads.
var sampleStrata = []SampleStratum{
	{Name: "centreret", Needles: []string{"<AendringCentreretParagraf", "<IkraftCentreretParagraf"},
		Why: "centred provisions: the payload classes them as prose, so the reader loses the address"},
	{Name: "kapitel", Needles: []string{"<Kapitel"},
		Why: "chapter numbers: the payload prints them in the heading, not in the element"},
	{Name: "afsnit", Needles: []string{"<Afsnit"},
		Why: "sub-chapter headings, the other half of the chapter-key mismatch"},
	{Name: "nota", Needles: []string{"<Nota"},
		Why: "footnotes, which travel from the payload's bottom to the reference"},
	{Name: "tabel", Needles: []string{"<Table"},
		Why: "tables, which the reader does not rebuild yet"},
	{Name: "bilag", Needles: []string{"<Bilag"},
		Why: "appendices, usually figures the payload renders as tables"},
	{Name: "aendring", Needles: []string{"<AendringsNummer", "<AendringNyTekst"},
		Why: "amendment-heavy documents, the population BR10 refused 135 edits in"},
	{Name: "liste", Needles: []string{"<Indentatio", "<Index"},
		Why: "numbered items, the nr. 2 level of an address"},
	{Name: "stor", MinKB: 200,
		Why: "documents over 200 KB, where a structural bug costs the most text"},
	{Name: "gammel", MaxYear: 1944,
		Why: "legacy generation: SGML quoting and pre-modern markup"},
	{Name: "mellem", MinYear: 1945, MaxYear: 1979,
		Why: "the lowercase mid-century generation between SGML and modern anchored markup"},
	{Name: "random",
		Why: "unbiased baseline, for ranking residual classes by frequency"},
}

// StratumQuota is one --quota value: a stratum name and how many documents to take.
type StratumQuota struct {
	Stratum SampleStratum
	Count   int
}

// ParseQuotas turns ["centreret=300", "random=500"] into quotas.
//
// An unknown name is an error rather than a warning. A quota silently dropped
// would leave a hole in the sample that only shows up when the measurement it was
// meant to make comes out thin.
func ParseQuotas(specs []string) ([]StratumQuota, error) {
	seen := map[string]bool{}
	var out []StratumQuota
	for _, spec := range specs {
		name, digits, ok := strings.Cut(spec, "=")
		if !ok {
			return nil, fmt.Errorf("quota %q: want name=count", spec)
		}
		n, err := strconv.Atoi(strings.TrimSpace(digits))
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("quota %q: count must be a positive whole number", spec)
		}
		s, found := lookupStratum(strings.TrimSpace(name))
		if !found {
			return nil, fmt.Errorf("quota %q: unknown stratum (known: %s)", spec, strings.Join(stratumNames(), ", "))
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("quota %q: stratum %s given twice", spec, s.Name)
		}
		seen[s.Name] = true
		out = append(out, StratumQuota{Stratum: s, Count: n})
	}
	// Fill in the declared order regardless of how they were typed, so the
	// contention above is decided by the corpus and not by the command line.
	slices.SortStableFunc(out, func(a, b StratumQuota) int {
		return stratumIndex(a.Stratum.Name) - stratumIndex(b.Stratum.Name)
	})
	return out, nil
}

// StratumNames returns the known stratum names, for --help.
func StratumNames() []string { return stratumNames() }

// Strata returns the table, for --help and for the report's rationale column.
func Strata() []SampleStratum { return sampleStrata }

func stratumNames() []string {
	out := make([]string, len(sampleStrata))
	for i, s := range sampleStrata {
		out[i] = s.Name
	}
	return out
}

func lookupStratum(name string) (SampleStratum, bool) {
	for _, s := range sampleStrata {
		if s.Name == name {
			return s, true
		}
	}
	return SampleStratum{}, false
}

func stratumIndex(name string) int {
	for i, s := range sampleStrata {
		if s.Name == name {
			return i
		}
	}
	return len(sampleStrata)
}

// matches decides from the raw XML bytes, the publication year and the file size.
//
// An undeterminable year (zero) is excluded from both year gates rather than
// included in one: a stratum defined by a decade must not be filled by documents
// whose decade is unknown, or the report attributes a shape to the wrong era.
func (s SampleStratum) matches(xml []byte, year int, size int64) bool {
	if s.MinKB > 0 && size < int64(s.MinKB)*1024 {
		return false
	}
	if s.MinYear > 0 && (year <= 0 || year < s.MinYear) {
		return false
	}
	if s.MaxYear > 0 && (year <= 0 || year > s.MaxYear) {
		return false
	}
	for _, n := range s.Needles {
		if bytes.Contains(xml, []byte(n)) {
			return true
		}
	}
	return len(s.Needles) == 0
}

// SampleCandidate is one document the archive holds with an XML body.
type sampleCandidate struct {
	eliPath string // lta/1942/542, the same string as the archive's relative path
	kind    string
	year    int
	size    int64
	strata  []int // indices into sampleStrata
}

// SamplePlanOptions configures a planning pass.
type SamplePlanOptions struct {
	// Quotas is what to take per stratum, in the order it should be filled.
	Quotas []StratumQuota
	// Seed fixes the shuffle; zero picks one and logs it.
	Seed int64
	// Kinds restricts candidates to specific ELI kinds; empty means every kind.
	Kinds []string
	// Out is the manifest to write.
	Out string
}

// SamplePlanReport is what the planning pass learned, printed so the operator can
// judge the sample before any request is made.
type SamplePlanReport struct {
	Scanned     int64
	WithXMLBody int64
	Chosen      int
	Manifest    string
	Seed        int64
	// Got and Short per stratum: a quota the corpus cannot fill is a finding about
	// the corpus, and the report says so rather than printing a smaller number.
	Got   map[string]int
	Short map[string]int
	Kinds map[string]int
}

// PlanBodySample reads the archive and writes a stratified manifest. It requests
// nothing.
func (c *Crawler) PlanBodySample(ctx context.Context, opt SamplePlanOptions) (SamplePlanReport, error) {
	rep := SamplePlanReport{Got: map[string]int{}, Short: map[string]int{}, Kinds: map[string]int{}}
	if len(opt.Quotas) == 0 {
		return rep, fmt.Errorf("no quotas given (known strata: %s)", strings.Join(stratumNames(), ", "))
	}
	if opt.Out == "" {
		return rep, fmt.Errorf("no manifest path given")
	}
	seed := opt.Seed
	if seed == 0 {
		// Logged rather than only generated: the whole point of a seed is that a
		// later run can be told which sample this was.
		seed = time.Now().UnixNano()
		slog.Debug("sampling seed", "seed", seed)
	}
	rep.Seed = seed

	var candidates []sampleCandidate
	root := c.archive.Dir()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name := d.Name()
		if !strings.HasSuffix(name, archive.XMLExt) || strings.HasPrefix(name, ".tmp-") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		eliURL := c.eliURLForPath(rel)
		if eliURL == "" {
			return nil
		}
		rep.Scanned++
		kind := eliKind(eliURL)
		if kind == "accn" || kind == "unmapped" {
			// An accession path is a second route to a document the archive also
			// holds under its own ELI path: sampling it would spend a request on a
			// duplicate and double-count one document in the frequency ranking.
			return nil
		}
		if len(opt.Kinds) > 0 && !slices.Contains(opt.Kinds, kind) {
			return nil
		}
		xml, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Contains(xml, dokumentIndhold) {
			// No XML body means no reference text, which is the whole premise:
			// these are the backfill population, not the paired population.
			return nil
		}
		rep.WithXMLBody++
		cand := sampleCandidate{
			eliPath: strings.TrimSuffix(filepath.ToSlash(rel), archive.XMLExt),
			kind:    kind,
			size:    int64(len(xml)),
			year:    yearFromELIPath(rel),
		}
		for i, s := range sampleStrata {
			if s.matches(xml, cand.year, cand.size) {
				cand.strata = append(cand.strata, i)
			}
		}
		candidates = append(candidates, cand)
		return nil
	})
	if err != nil {
		return rep, err
	}

	rng := rand.New(rand.NewSource(seed))
	chosen := map[string]*SampleManifestRow{}
	order := make([]string, 0, 64)

	for _, q := range opt.Quotas {
		si := stratumIndex(q.Stratum.Name)
		// Grouped by kind and round-robined, because a flat random pick is
		// dominated by whichever kind is largest: a defect living in one series
		// would then be invisible at exactly the sample size chosen to find it.
		byKind := map[string][]*sampleCandidate{}
		for i := range candidates {
			cand := &candidates[i]
			if !slices.Contains(cand.strata, si) {
				continue
			}
			byKind[cand.kind] = append(byKind[cand.kind], cand)
		}
		kinds := make([]string, 0, len(byKind))
		for k := range byKind {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			rng.Shuffle(len(byKind[k]), func(a, b int) {
				byKind[k][a], byKind[k][b] = byKind[k][b], byKind[k][a]
			})
		}

		taken := 0
		for taken < q.Count && len(kinds) > 0 {
			progress := false
			for _, k := range kinds {
				if taken >= q.Count {
					break
				}
				if len(byKind[k]) == 0 {
					continue
				}
				cand := byKind[k][0]
				byKind[k] = byKind[k][1:]
				progress = true
				if row, had := chosen[cand.eliPath]; had {
					// Already in the sample: it counts toward this quota, because
					// its ground truth is about to be on disk either way, and it
					// records both labels. Spending a second request on it would
					// be polite to nobody.
					if !slices.Contains(row.Strata, q.Stratum.Name) {
						row.Strata = append(row.Strata, q.Stratum.Name)
					}
					taken++
					continue
				}
				chosen[cand.eliPath] = &SampleManifestRow{
					ELIPath: cand.eliPath, Kind: cand.kind, Year: cand.year, Bytes: cand.size,
					Strata: []string{q.Stratum.Name},
				}
				order = append(order, cand.eliPath)
				rep.Kinds[cand.kind]++
				taken++
			}
			if !progress {
				break // every kind in this stratum is exhausted
			}
		}
		rep.Got[q.Stratum.Name] = taken
		if taken < q.Count {
			rep.Short[q.Stratum.Name] = q.Count - taken
		}
	}

	rows := make([]*SampleManifestRow, 0, len(chosen))
	for _, p := range order {
		rows = append(rows, chosen[p])
	}
	rep.Chosen = len(rows)
	if err := WriteSampleManifest(opt.Out, rep.Seed, rows); err != nil {
		return rep, err
	}
	rep.Manifest = opt.Out
	return rep, nil
}

// yearFromELIPath reads the four-digit year segment of kind/year/leaf. Zero means
// "not determinable", which gates the year-based strata shut rather than letting
// them match everything.
func yearFromELIPath(rel string) int {
	segs := strings.Split(filepath.ToSlash(rel), "/")
	if len(segs) < 2 {
		return 0
	}
	y, err := strconv.Atoi(segs[1])
	if err != nil || y < 1000 || y > 9999 {
		return 0
	}
	return y
}

// SampleManifestRow is one line of a manifest: a document and every stratum that
// claimed it.
type SampleManifestRow struct {
	ELIPath string
	Kind    string
	Year    int
	Bytes   int64
	Strata  []string
}

// String renders the manifest line: eli_path, kind, year, bytes, strata.
func (r *SampleManifestRow) String() string {
	return strings.Join([]string{
		r.ELIPath, r.Kind, strconv.Itoa(r.Year), strconv.FormatInt(r.Bytes, 10),
		strings.Join(r.Strata, ";"),
	}, "\t")
}

// WriteSampleManifest writes a manifest, sorted by path so that two plans of the
// same corpus diff cleanly.
func WriteSampleManifest(path string, seed int64, rows []*SampleManifestRow) error {
	sorted := slices.Clone(rows)
	slices.SortFunc(sorted, func(a, b *SampleManifestRow) int {
		return strings.Compare(a.ELIPath, b.ELIPath)
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%s seed=%d rows=%d\n", SampleManifestHeader, seed, len(sorted))
	b.WriteString("# eli_path\tkind\tyear\tbytes\tstrata\n")
	for _, r := range sorted {
		b.WriteString(r.String() + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// ReadSampleManifest parses a manifest, skipping comment lines.
func ReadSampleManifest(path string) ([]*SampleManifestRow, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []*SampleManifestRow
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 5 {
			return nil, fmt.Errorf("manifest line %q: want 5 tab-separated columns", line)
		}
		year, err := strconv.Atoi(cols[2])
		if err != nil {
			return nil, fmt.Errorf("manifest line %q: year: %w", line, err)
		}
		size, err := strconv.ParseInt(cols[3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("manifest line %q: bytes: %w", line, err)
		}
		var strata []string
		if cols[4] != "" {
			strata = strings.Split(cols[4], ";")
		}
		out = append(out, &SampleManifestRow{
			ELIPath: cols[0], Kind: cols[1], Year: year, Bytes: size, Strata: strata,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("manifest %s has no rows", path)
	}
	return out, nil
}

// SampleFetchOptions configures a fetch pass over a manifest.
type SampleFetchOptions struct {
	Manifest string
	// Dir is where payloads land, mirroring the archive's layout: Dir/lta/1942/542.json.
	Dir string
	// Limit caps attempts; zero means the whole manifest.
	Limit int
	// Overwrite re-fetches rows whose payload is already on disk.
	Overwrite bool
	// OnProgress receives the running totals.
	OnProgress func(SampleStats)
}

// SampleStats is a fetch pass's tally.
type SampleStats struct {
	Attempted int
	Stored    int
	Held      int
	Empty     int
	Failed    int
	Bytes     int64
}

// FetchBodySample fetches the manifest's payloads into Dir.
//
// It writes nothing to the archive and nothing to the state store, and that is the
// point. A sidecar beside a document that already has canonical XML would make the
// loader's rule ("use the sidecar when the XML has no body") ambiguous in exactly
// the files used to test it, and would inflate the body census by a thousand
// deliberate duplicates. This is measurement scaffolding: it lives in its own
// directory and the archive stays the product.
func (c *Crawler) FetchBodySample(ctx context.Context, opt SampleFetchOptions) (SampleStats, error) {
	var st SampleStats
	if opt.Manifest == "" || opt.Dir == "" {
		return st, fmt.Errorf("both a manifest and an output directory are required")
	}
	rows, err := ReadSampleManifest(opt.Manifest)
	if err != nil {
		return st, err
	}
	report := func() {
		if opt.OnProgress != nil {
			opt.OnProgress(st)
		}
	}
	start := time.Now()
	for i, row := range rows {
		if opt.Limit > 0 && i >= opt.Limit {
			break
		}
		if err := ctx.Err(); err != nil {
			return st, err
		}
		st.Attempted++
		dest := filepath.Join(opt.Dir, filepath.FromSlash(row.ELIPath)+JSONExt)
		if !opt.Overwrite {
			if _, err := os.Stat(dest); err == nil {
				// Resumable by inspection, the same way the archive pass works: the
				// file is the record, so an interrupted sample needs no state.
				st.Held++
				report()
				continue
			}
		}

		// The request path comes from the same mapping the archive path came from,
		// not from the manifest's column: the document endpoint wants "eli/lta/1980/1"
		// where the archive file is "lta/1980/1.xml", and hand-applying that
		// difference here is how a sample quietly requests 404s for everything.
		eliPath := docapi.PathFromELIURL(c.eliURLForPath(filepath.ToSlash(row.ELIPath) + archive.XMLExt))
		if eliPath == "" {
			st.Failed++
			slog.Warn("manifest path is not an ELI document", "path", row.ELIPath)
			report()
			continue
		}
		payload, err := c.doc.FetchJSON(ctx, eliPath)
		if err != nil {
			// One 500 must not abandon a sample: a run that stops at the first
			// error loses the tail of the manifest, which is a biased way to
			// discover that the tail was fine.
			st.Failed++
			slog.Warn("sample fetch failed", "eli", row.ELIPath, "err", err)
			report()
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return st, err
		}
		if err := os.WriteFile(dest, payload, 0o600); err != nil {
			return st, fmt.Errorf("write sample %s: %w", dest, err)
		}
		st.Stored++
		st.Bytes += int64(len(payload))
		if doc, err := docapi.ParseJSON(payload); err != nil {
			st.Failed++
			slog.Warn("unparseable sample payload", "eli", row.ELIPath, "err", err)
		} else if docapi.TextLen(doc.DocumentHTML) < minBodyTextChars {
			// Kept on disk anyway: "the site has no text for this either" is a
			// result about the document, and the paired sample needs to know which
			// documents cannot be paired at all.
			st.Empty++
		}
		report()
	}
	slog.Info("sample fetch complete", "attempted", st.Attempted, "stored", st.Stored,
		"already_held", st.Held, "without_text", st.Empty, "failed", st.Failed,
		"bytes", archive.SizeHint(st.Bytes), "elapsed", time.Since(start).Round(time.Second).String())
	return st, nil
}
