package lexdania

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// difficulty is one law's stress profile. Every field is a shape that has either
// broken this parser during the current work or is a refusal the consolidator still
// makes, so "hardest" means hardest for *this* code, not largest by word count.
type difficulty struct {
	Accession string
	Kind      string
	Year      int
	Number    int
	DiesSigni string
	Title     string

	// Own-text signals: things wrong or unusual inside the archived document.
	appendixCollisions int    // appendix provisions numbered like the law's own (Bug 10)
	localIDMismatch    int    // localId-derived number disagrees with the printed one
	dupNumbers         int    // same number twice outside appendix material
	romanAfsnit        int    // afsnit whose number only exists as a roman numeral
	firstEff           string // date of the first amendment in the chain
	distinctKinds      int    // structural variety
	paras              int
	runes              int

	// Chain signals: things the amendments make the consolidator do.
	depth       int // chain members named by this law's own Change anchors
	edits       int // numbered changes in the chain aimed at this law
	refused     int // of those, changes the library has no mechanism for
	unresolved  int // of those, addresses that do not resolve in the current text
	pktRefs     int // changes citing a sentence ("2. pkt.")
	dateClashes int // chain members sharing an effective date: ordering stress
	bills       int // chain members that are bills and must never be applied
	applied     int // end-to-end LawAsOf: what actually applied
	viewUnres   int // end-to-end LawAsOf: what it refused
	chainErrs   int // chain members missing from the archive

	score int
}

// scoreLaw ranks; it is a device for ordering the table, not a claim about the law. The
// weights follow what each shape cost in practice: an edit that cannot be applied and an
// address that resolves to the wrong provision are the failures a reader sees, an
// appendix collision and a localId that disagrees with the printed number are the
// failures that were invisible until they were looked for, and a chain where two
// amendments share an effective date is where "apply in date order" stops being a
// tautology. "unresolved" is divided rather than multiplied: an address that does not
// match the archived text is usually the archive's bookkeeping (a consolidated act
// already contains later amendments, so the numbers an older amender cited have moved),
// which the README already declines to measure as a rate. Raw signals are printed
// alongside so the ranking can be argued with.
func scoreLaw(d difficulty) int {
	score := 3*d.refused + 2*d.viewUnres + d.unresolved/4 + 6*d.localIDMismatch +
		4*d.dupNumbers + 4*d.dateClashes + 3*d.appendixCollisions +
		2*d.depth + d.pktRefs/8 + d.distinctKinds + d.chainErrs/2
	// The strongest single signal: a law with a real chain whose consolidation
	// achieves nothing. Counting absolute refusals would rank a 500-change law above a
	// 60-change law that failed completely, and the completely failed one is the one
	// worth reading about.
	if total := d.applied + d.viewUnres; total >= 20 {
		failure := 1.0 - float64(d.applied)/float64(total)
		score += int(40 * failure)
	}
	return score
}

// TestHardestLaws ranks laws by how hard they are for this parser and prints the top of
// the list with its evidence, including the API path and the date to ask the
// third-party JSON service about.
//
// It reads the whole archive, so it is opt-in: an explicit hunt, not a regression check.
func TestHardestLaws(t *testing.T) {
	if os.Getenv("RETSIN_FULL_SCAN") != "1" {
		t.Skip("walks the entire archive; set RETSIN_FULL_SCAN=1")
	}
	dir := corpusDir(t)
	a, err := Scan(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	entries := a.Entries()
	workers := runtime.NumCPU()
	if workers > 12 {
		workers = 12
	}
	// Only lta/ltb/ltc/mt are scored: retsinfo and fob rulings carry Change anchors that
	// link to a newer accession of the same ruling rather than to amending acts, so
	// scoring them would rank a document-type quirk as difficulty.

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		out   []difficulty
		chunk = make(chan []Entry, workers*2)
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local []difficulty
			for es := range chunk {
				for _, e := range es {
					if d, ok := scoreOne(a, e); ok {
						local = append(local, d)
					}
				}
			}
			mu.Lock()
			out = append(out, local...)
			mu.Unlock()
		}()
	}
	const perSend = 400
	for i := 0; i < len(entries); i += perSend {
		end := min(i+perSend, len(entries))
		chunk <- entries[i:end]
	}
	close(chunk)
	wg.Wait()

	t.Logf("scored %d law documents of %d indexed", len(out), len(entries))
	stub := 0
	for _, d := range out {
		if d.score < 0 {
			stub++
		}
	}
	// Kept visible: these are laws whose archived file has no provisions at all, so they
	// dominate any metric that counts amendments against a document that is not there.
	t.Logf("%d scored documents have no provisions in the archived text and are excluded from the ranking", stub)
	keep := out[:0]
	for _, d := range out {
		if d.score >= 0 {
			keep = append(keep, d)
		}
	}
	out = keep
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].Accession < out[j].Accession
	})
	// How many rows to print is a caller choice, not a property of the corpus: the
	// same run is used both for a short headline list and to find the next distinct
	// law when the top of the table is several consolidations of one statute.
	limit := 12
	if v := os.Getenv("RETSIN_TOP_N"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("RETSIN_TOP_N=%q", v)
		}
		limit = n
	}
	for rank, d := range out[:min(limit, len(out))] {
		t.Logf("#%-2d score=%-5d %s  %s/%d  %d changes, %d refused, %d unresolved, %d pkt | "+
			"chain=%d clashes=%d bills=%d missing=%d | applied=%d viewUnres=%d | "+
			"appendix=%d localId=%d dup=%d roman=%d kinds=%d §=%d runes=%d",
			rank+1, d.score, d.Accession, d.Kind, d.Number, d.edits, d.refused, d.unresolved,
			d.pktRefs, d.depth, d.dateClashes, d.bills, d.chainErrs, d.applied, d.viewUnres,
			d.appendixCollisions, d.localIDMismatch, d.dupNumbers, d.romanAfsnit,
			d.distinctKinds, d.paras, d.runes)
		t.Logf("     %s | signed %s | as enacted: %s | chain from %s",
			Squeeze(d.Title), d.DiesSigni,
			VersionAPIPath(d.Year, d.Number, 1),
			d.firstEffective())
	}
}

// TestDumpChains prints, as JSON, the version chain this parser derives for each
// accession in RETSIN_DUMP_CHAINS, which is the input the comparison against
// retsinformation-api.dk needs. It is a printer, not an assertion: what it dumps is meant
// to be looked at, and the API side is fetched separately and saved, so neither side is
// re-run to answer a question about the other.
func TestDumpChains(t *testing.T) {
	list := os.Getenv("RETSIN_DUMP_CHAINS")
	if list == "" {
		t.Skip("set RETSIN_DUMP_CHAINS=A,B,C to dump those chains as JSON")
	}
	a, err := Scan(t.Context(), corpusDir(t))
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		OurAccession string    `json:"our_accession"`
		Year         int       `json:"year"`
		Number       int       `json:"number"`
		Title        string    `json:"title"`
		DiesSigni    string    `json:"dies_signi"`
		OurVersions  []Version `json:"our_versions"`
	}
	// The API wants year/number; the entry carries both, so the dump is enough to fetch.
	var out []row
	for _, acc := range strings.Split(list, ",") {
		acc = strings.TrimSpace(acc)
		if acc == "" {
			continue
		}
		e, ok := a.Find(acc)
		if !ok {
			t.Errorf("%s: not in the archive", acc)
			continue
		}
		vs, err := a.Versions(acc)
		if err != nil {
			t.Errorf("%s: %v", acc, err)
			continue
		}
		out = append(out, row{OurAccession: acc, Year: e.Year, Number: e.Number,
			Title: Squeeze(e.Title), DiesSigni: e.DiesSigni, OurVersions: vs})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	// A file destination keeps the JSON parseable: -v prefixes every log line with the
	// test's location, which the comparison script would then have to strip.
	dest := os.Getenv("RETSIN_DUMP_OUT")
	if dest == "" {
		t.Log("\n" + string(b))
		return
	}
	if err := os.WriteFile(dest, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", dest, err)
	}
	t.Logf("dumped %d chains to %s", len(out), dest)
}

// VersionAPIPath is the third-party service's endpoint for one version of a law. Version
// 1 is the text as enacted, which is what the comparison asks for.
func VersionAPIPath(year, number, version int) string {
	return fmt.Sprintf("https://retsinformation-api.dk/v1/lovgivning/%d/%d/versions/%d",
		year, number, version)
}

// firstEffective is the date of the first amendment in the chain: the day the archived
// text stopped reading as it was published, which is the boundary to ask the JSON service
// about ("what did version 1 say, and what did it say the day before this").
func (d difficulty) firstEffective() string {
	if d.firstEff == "" {
		return "no amendment in the chain"
	}
	return d.firstEff
}

// scoreOne measures one document. ok is false for anything that is not a law with a
// chain worth scoring.
func scoreOne(a *Archive, e Entry) (difficulty, bool) {
	lawKinds := map[string]bool{"lta": true, "ltb": true, "ltc": true, "mt": true}
	if !lawKinds[e.Kind] {
		return difficulty{}, false
	}
	doc, _, ok := a.Open(e.Accession)
	if !ok || doc == nil {
		return difficulty{}, false
	}
	// An amending act is single-shot: it is applied once and has no version history of
	// its own, so ranking it would spend the comparison on a law with one version.
	if doc.IsAmendment() {
		return difficulty{}, false
	}
	d := difficulty{
		Accession: e.Accession, Kind: e.Kind, Year: e.Year, Number: e.Number,
		DiesSigni: e.DiesSigni, Title: e.Title,
	}
	secs := doc.Sections()
	d.appendixCollisions = appendixCollisions(secs)
	d.localIDMismatch = localIDMismatches(secs)
	d.dupNumbers = duplicateNumbers(secs)
	d.romanAfsnit = afsnitWithRomanNumber(doc.Root)
	d.paras = len(paragraphs(secs))
	kinds := map[string]bool{}
	for _, s := range secs {
		kinds[s.Kind] = true
	}
	d.distinctKinds = len(kinds)
	d.runes = len(doc.Root.PlainText())
	if d.paras == 0 {
		// Archived without usable provisions: nothing to process, so nothing to rank.
		// Counted separately because it is an archive finding, not a parser one.
		d.score = -1
		return d, true
	}

	vs, err := a.Versions(e.Accession)
	if err != nil || len(vs) < 2 {
		// A law with no amender in its own anchors has nothing to consolidate; it can
		// still be structurally odd, but that is already in the own-text signals.
		d.score = scoreLaw(d)
		return d, d.paras > 0
	}
	d.depth = len(vs)
	seen := map[string]bool{}
	dates := make([]string, 0, len(vs))
	for _, v := range vs {
		if v.Found && !v.Effective.IsZero() {
			dates = append(dates, v.Effective.Format("2006-01-02"))
		}
		if !v.Found {
			d.chainErrs++
			continue
		}
		key := v.Effective.Format("2006-01-02")
		if seen[key] {
			d.dateClashes++
		}
		seen[key] = true
		amender, _, ok := a.Open(v.Accession)
		if !ok {
			continue
		}
		if amender.IsBill() {
			d.bills++
			continue
		}
		if !amender.IsAmendment() {
			continue
		}
		for _, ed := range amender.Amendments() {
			// The chain anchor says this act touched this law, but a multi-law act
			// carries changes aimed at every law it amends: only its own count here,
			// or a multi-law act scores as hard for all its targets.
			if !targetsLaw(ed.Target, doc.Meta, doc.Accession()) {
				continue
			}
			d.edits++
			if strings.Contains(ed.Address.Raw, "pkt.") {
				d.pktRefs++
			}
			if unhandledReason(ed) != "" {
				d.refused++
				continue
			}
			if ed.Address.Recognised && ed.Address.Paragraph != "" && Find(secs, ed.Address) == nil {
				d.unresolved++
			}
		}
	}
	// End to end, because every signal above is a proxy: this is the call that shows
	// what the consolidator actually manages. Only for laws with a chain, which is
	// where the work is.
	if v, err := a.LawAsOf(e.Accession, time.Now()); err == nil {
		d.applied = len(v.Applied)
		d.viewUnres = len(v.Unresolved)
	}
	sort.Strings(dates)
	if len(dates) > 0 {
		d.firstEff = dates[0]
	}
	d.score = scoreLaw(d)
	return d, true
}

// dumpParagraph is one provision in the shape the API reports: its printed
// Explicatus plus its subsections with text.
type dumpParagraph struct {
	Number string    `json:"number"`
	Stk    []dumpStk `json:"stk"`
}

type dumpStk struct {
	Number string `json:"number"`
	Text   string `json:"text"`
}

type dumpStructure struct {
	Accession  string          `json:"accession"`
	AsOf       string          `json:"as_of,omitempty"`
	Title      string          `json:"title"`
	Notes      int             `json:"notes"`
	Paragraphs []dumpParagraph `json:"paragraphs"`
	Applied    int             `json:"amendments_applied"`
}

// TestDumpStructure prints the sections our parser produces as JSON so an older
// version can be diffed offline against retsinformation-api.dk's revision
// structure, which carries the same information as paragraphs with their printed
// number and stk text:
//
//	RETSIN_DUMP_STRUCTURE=A20050112629,A20050112629@2007-08-30
//
// A date applies every amendment our chain says was in force then, which is the
// thing under test here: our consolidation versus their revision of the same law.
// The undated form is the archived document as published — what the API's version
// list does not contain, because it starts at the first amendment.
func TestDumpStructure(t *testing.T) {
	spec := os.Getenv("RETSIN_DUMP_STRUCTURE")
	if spec == "" {
		t.Skip("set RETSIN_DUMP_STRUCTURE=ACCESSION[@YYYY-MM-DD],... to dump structure")
	}
	a, err := Scan(t.Context(), corpusDir(t))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]dumpStructure, 0)
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		acc, asOf := item, ""
		if i := strings.IndexByte(item, '@'); i >= 0 {
			acc, asOf = item[:i], item[i+1:]
		}
		d := dumpStructure{Accession: acc, AsOf: asOf}
		if asOf == "" {
			doc, _, ok := a.Open(acc)
			if !ok {
				t.Fatalf("%s: not in archive", acc)
			}
			for _, s := range doc.Root.Elems {
				if s.Name == "Title" {
					d.Title = strings.TrimSpace(s.Text())
				}
			}
			d.Notes = countNamed(doc.Root, "Nota", "Fodnote")
			d.Paragraphs = dumpSections(doc.Sections())
		} else {
			day, err := time.Parse("2006-01-02", asOf)
			if err != nil {
				t.Fatalf("%s: bad date: %v", item, err)
			}
			v, err := a.LawAsOf(acc, day)
			if err != nil {
				t.Fatalf("%s: %v", item, err)
			}
			d.Applied = v.AppliedCount()
			d.Title = v.Title
			d.Notes = countNamed(v.Doc.Root, "Nota", "Fodnote")
			d.Paragraphs = dumpSections(v.Sections())
		}
		out = append(out, d)
	}
	blob, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dest := os.Getenv("RETSIN_DUMP_OUT")
	if dest == "" {
		dest = "/tmp/our-structure.json"
	}
	if err := os.WriteFile(dest, blob, 0o644); err != nil {
		t.Fatalf("write %s: %v", dest, err)
	}
	t.Logf("dumped %d structures to %s", len(out), dest)
}

// dumpSections keeps only the provisions in the law's own numbering series.
// Appendix provisions are the entry-into-force quotations of amending acts, and
// the API's revision does not carry them.
func dumpSections(secs []*Section) []dumpParagraph {
	out := make([]dumpParagraph, 0)
	for _, s := range secs {
		if s.Kind != "paragraf" || s.Appendix {
			continue
		}
		p := dumpParagraph{Number: s.RawNumber}
		for _, c := range secs {
			if c.Kind == "stk" && c.Parent == s {
				p.Stk = append(p.Stk, dumpStk{Number: c.RawNumber, Text: c.Node.PlainText()})
			}
		}
		out = append(out, p)
	}
	return out
}

// appendixCollisions counts appendix provisions printed with a number the law itself
// also uses: the shape that made address resolution edit the wrong text.
func appendixCollisions(secs []*Section) int {
	real := map[string]bool{}
	for _, s := range secs {
		if s.Kind == "paragraf" && !s.Appendix {
			real[s.Number] = true
		}
	}
	n := 0
	for _, s := range secs {
		if s.Kind == "paragraf" && s.Appendix && real[s.Number] {
			n++
		}
	}
	return n
}

// localIDMismatches counts provisions whose localId-derived number disagrees with the
// number printed in the Explicatus. Number comes from localId when it is present, so a
// disagreement means one of the two is lying about which provision this is.
func localIDMismatches(secs []*Section) int {
	n := 0
	for _, s := range secs {
		if s.Kind != "paragraf" || s.Appendix || s.RawNumber == "" {
			continue
		}
		printed := lexdoc.FirstNumber(s.RawNumber)
		if printed != "" && s.Number != "" && printed != s.Number {
			n++
		}
	}
	return n
}

func duplicateNumbers(secs []*Section) int {
	seen := map[string]int{}
	for _, s := range secs {
		if s.Kind == "paragraf" && !s.Appendix {
			seen[s.Number]++
		}
	}
	n := 0
	for _, c := range seen {
		if c > 1 {
			n++
		}
	}
	return n
}
