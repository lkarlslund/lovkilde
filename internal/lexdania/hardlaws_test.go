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
	a, err := corpusArchive(t)
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
	a, err := corpusArchive(t)
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
	a, err := corpusArchive(t)
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
			m := doc.Model()
			d.Notes = countNotes(&m.Content)
			d.Paragraphs = dumpContent(&m.Content)
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
			d.Notes = countNotes(v.Content)
			d.Paragraphs = dumpContent(v.Content)
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
func dumpContent(c *lexdoc.Content) []dumpParagraph {
	out := make([]dumpParagraph, 0)
	for _, ref := range c.Provisions() {
		p := ref.Provision
		d := dumpParagraph{Number: p.Printed}
		for _, st := range p.Stykker {
			d.Stk = append(d.Stk, dumpStk{Number: st.Label, Text: p.SpanText(st.Span)})
		}
		out = append(out, d)
	}
	return out
}

// countNotes counts the footnotes a law's text carries.
func countNotes(c *lexdoc.Content) int {
	n := 0
	for _, ref := range c.Provisions() {
		for _, seg := range ref.Provision.Segments {
			if seg.Note != "" {
				n++
			}
		}
	}
	return n
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

// TestBR18ItemCitationWithoutStykkeLands is the real-archive proof for the address form
// Danish acts actually write. B20200079405 change 23 reads "I § 92, nr. 2, ændres
// »bygningen« til: »byggeriet«." — an act naming an item and never saying "stk. 1", because
// the provision has one stykke. The item was parsed, numbered and printed; only the address
// reached it, so the edit was refused and BR18 served the 2019 word for six years. 26 of the
// act's 109 refusals were this one shape.
func TestBR18ItemCitationWithoutStykkeLands(t *testing.T) {
	a, err := corpusArchive(t)
	if err != nil {
		t.Fatal(err)
	}
	base, _, ok := a.Open("B20190139905")
	if !ok {
		t.Fatal("BR18 is not in the archive")
	}

	before := LawAsOf(base, time.Date(2020, 3, 5, 12, 0, 0, 0, time.UTC), a.Open)
	s, ok := before.ProvisionText("§ 92, nr. 2")
	if !ok {
		t.Fatal(`"§ 92, nr. 2" does not resolve in BR18 as promulgated`)
	}
	if !strings.Contains(s, "bygn") {
		t.Fatalf(`"§ 92, nr. 2" = %q, want the promulgated wording`, s)
	}

	// The order was signed on 2 June 2020 and takes effect on 1 July 2020 (its § 2).
	after := LawAsOf(base, time.Date(2020, 7, 1, 12, 0, 0, 0, time.UTC), a.Open)
	if got, ok := after.ProvisionText("§ 92, nr. 2"); !ok {
		t.Fatal(`"§ 92, nr. 2" does not resolve after the amendment`)
	} else if !strings.Contains(got, "byggeriet") {
		t.Errorf(`"§ 92, nr. 2" = %q, want the word the amendment put there`, got)
	}
	// The stykke-naming citation has to keep working exactly as it did.
	if got, ok := after.ProvisionText("§ 92, stk. 1, nr. 2"); !ok || !strings.Contains(got, "byggeriet") {
		t.Errorf(`"§ 92, stk. 1, nr. 2" = %q, want the same item`, got)
	}
	for _, u := range after.Unresolved {
		if u.Edit.Address.String() == "§ 92, nr. 2" {
			t.Errorf("the item citation is still refused: %s", u.Reason)
		}
	}
}

// Changes the archive's metadata does not tie to the law they amend, and changes it ties to a law
// they do not amend.
func TestChainFollowsTheActsItsActsCite(t *testing.T) {
	a, err := corpusArchive(t)
	if err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Lov nr. 687 of 2017 lists only the 2021 act; that act cites "som ændret ved § 8 i lov nr.
	// 1585 af 27. december 2019", which changed § 7, stk. 3, from 2020 to 2022 first.
	v, err := a.LawAsOf("A20170068730", asOf)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := v.ProvisionText("§ 7, stk. 3"); !ok || !strings.Contains(got, "2031") {
		t.Errorf(`"§ 7, stk. 3" = %q, want the 2031 the 2021 act put in place of 2022`, got)
	}

	// Lov nr. 486 of 2009 and lov nr. 1398 of 2008 are both "Lov om ændring af udlændingeloven",
	// and neither is a consolidation of the other.
	v, err = a.LawAsOf("A20090048630", asOf)
	if err != nil {
		t.Fatal(err)
	}
	for _, ap := range v.Applied {
		// A20240170230's § 11 is lov nr. 486's; its § 10 is lov nr. 1398's.
		if ap.Edit.Source == "A20090151130" || ap.Edit.Source == "A20240170230" && ap.Edit.Section != "11" {
			t.Errorf("%s %s applied to lov nr. 486 of 2009, which it does not amend", ap.Edit.Source, ap.Edit.Number)
		}
	}
	for _, u := range v.Unresolved {
		if u.Edit.Source == "A20090151130" {
			t.Errorf("%s %s taken as a change of lov nr. 486 of 2009", u.Edit.Source, u.Edit.Number)
		}
	}

	// Lov nr. 504 of 2009 repeals changes 1 and 2 of § 3 in lov nr. 526 of 2006.
	v, err = a.LawAsOf("A20060052630", asOf)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := v.ProvisionText("§ 3"); !ok || strings.Contains(got, "§ 478, stk. 4") || !strings.Contains(got, "§ 510, stk. 1") {
		t.Errorf(`"§ 3" = %q, want changes 1 and 2 gone and the rest kept`, got)
	}
}

// Virksomhedsskatteloven's new chapter 6 came with its heading, its §§ 17 and 18 in place of the
// repealed ones, and BR18 § 344 prints "Stk. 2" inside the markup of stk. 1.
func TestNewTextLandsInTheUnitItNames(t *testing.T) {
	a, err := corpusArchive(t)
	if err != nil {
		t.Fatal(err)
	}
	v, err := a.LawAsOf("A20130111429", time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := v.ProvisionText("§ 17, stk. 2"); !ok || !strings.Contains(got, "indgiver selvangivelse") {
		t.Errorf(`"§ 17, stk. 2" = %q, want the henstand rule the 2015 act brought`, got)
	}

	v, err = a.LawAsOf("B20170161505", time.Date(2019, 7, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := v.ProvisionText("§ 344, stk. 2, nr. 9"); !ok || !strings.Contains(got, "DS/EN") {
		t.Errorf(`"§ 344, stk. 2, nr. 9" = %q, want the ninth standard`, got)
	}
	if got, ok := v.ProvisionText("§ 344, stk. 3"); !ok || !strings.Contains(got, "væksthuse") || strings.Contains(got, "DS/EN") {
		t.Errorf(`"§ 344, stk. 3" = %q, want the greenhouse rule alone`, got)
	}
}

// Lov nr. 720 of 2014 is older than the consolidation of lov om en aktiv beskæftigelsesindsats of
// 12 September 2014, which printed most of it and says in a note that § 2, nr. 41-43, taking effect
// on 1 January 2015, are not included. They are made, and the act's other changes are not made
// twice.
func TestTheLatePartOfAnOlderActIsMade(t *testing.T) {
	a, err := corpusArchive(t)
	if err != nil {
		t.Fatal(err)
	}
	v, err := a.LawAsOf("A20140099029", time.Date(2015, 6, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := v.ProvisionText("§ 118 a, stk. 1"); !ok || !strings.Contains(got, "rådighedsbeløb") {
		t.Errorf(`"§ 118 a, stk. 1" = %q, want the provision lov nr. 720 of 2014 inserted from 2015`, got)
	}
	for _, ap := range v.Applied {
		if ap.Edit.Source == "A20140072030" && ap.Edit.Section == "2" && lexdoc.Atoi(strings.TrimSuffix(ap.Edit.Number, ".")) < 41 {
			t.Errorf("%s § 2, nr. %s, which the consolidation printed, made again", ap.Edit.Source, ap.Edit.Number)
		}
	}
}

// Lov nr. 575 of 2005 amends almenboligloven "jf. lovbekendtgørelse nr. 106 af 21. februar 2005",
// the consolidation before the base, printed under the law's older title. Its changes are this
// law's, and the §§ 185 c and 185 d it brings are there for lov nr. 223 of 2006 to amend.
func TestAnActCitingAnOlderTitleIsThisLaws(t *testing.T) {
	a, err := corpusArchive(t)
	if err != nil {
		t.Fatal(err)
	}
	v, err := a.LawAsOf("A20050061029", time.Date(2007, 6, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := v.ProvisionText("§ 185 c, stk. 1"); !ok || !strings.Contains(got, "regionale") {
		t.Errorf(`"§ 185 c, stk. 1" = %q, want the provision lov nr. 575 of 2005 brought, as lov nr. 223 of 2006 changed it`, got)
	}
}

// Lov om gasforsyning of 2023 continues a consolidation of April 2023, which continues lov om
// naturgasforsyning as consolidated in 2020; an act amending the law under its former name is the
// law's, and the consolidation prints it already.
func TestAnActAmendingTheLawUnderAFormerNameIsItsOwn(t *testing.T) {
	a, err := corpusArchive(t)
	if err != nil {
		t.Fatal(err)
	}
	v, err := a.LawAsOf("A20230110029", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range v.InBase {
		found = found || e.Source == "A20210092330"
	}
	for _, ap := range v.Applied {
		found = found || ap.Edit.Source == "A20210092330"
	}
	if !found {
		t.Error("lov nr. 923 of 2021, amending lov om naturgasforsyning, was read as another law's")
	}
}

// An act older than the base that a consolidation printed is not made again, whatever its changes
// look like: lov nr. 1564 of 2010 changed the coal duty's rates, quoted as bare figures, and
// rewrote its § 7 b and annexes, all printed by the consolidation of 2015 or replaced since, but
// for the § 7 b a minister was to set in force.
// Sømandsloven's consolidation of 2024 notes that § 2 of lov nr. 1231 of 2003, which its minister
// was to set in force, still is not: it waits. A later act's part left to a minister goes with the
// act, as the parts of lov nr. 1555 of 2019 that aktivloven's consolidation of 2021 prints.
func TestAnOlderActIsMadeOnlyWhereTheBaseLacksIt(t *testing.T) {
	a, err := corpusArchive(t)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	v, err := a.LawAsOf("A20150108029", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, ap := range v.Applied {
		// Nr. 14, § 7 b, waited for the minister in 2015 and 2020; the consolidation of 2025 prints
		// it and no longer names it.
		if ap.Edit.Source == "A20100156430" && ap.Edit.Number != "14." {
			t.Errorf("lov nr. 1564 of 2010, nr. %s, made on the consolidation of 2015", ap.Edit.Number)
		}
	}
	v, err = a.LawAsOf("A20240033529", now)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := v.ProvisionText("§ 64 b, stk. 2"); !ok || !strings.Contains(got, "søfartsbøger") || strings.Contains(got, "identitetsbevis") {
		t.Errorf(`"§ 64 b, stk. 2" = %q, want the text lov nr. 1231 of 2003 has not replaced`, got)
	}
	v, err = a.LawAsOf("A20190098129", now)
	if err != nil {
		t.Fatal(err)
	}
	made := map[string]bool{}
	for _, ap := range v.Applied {
		if ap.Edit.Source == "A20190155830" && ap.Edit.Section == "1" {
			made[ap.Edit.Number] = true
		}
	}
	if !made["20."] || !made["56."] {
		t.Errorf("lov nr. 1555 of 2019, § 1, nr. 20 and 56, set in force by order, not made: %v", made)
	}
}

// Vandforsyningsloven's consolidation of June 2010 prints § 52 a, stk. 1, in its old wording, with a
// note: "Det følger af § 3, nr. 1, i lov nr. 1518 af 27. december 2009, at § 52 a, stk. 1, affattes
// således", from 1 January 2011. The act is not in the law's change list. It is read from the note,
// that change only, and the acts of 2011 then find the conditions they name: a new nr. 11 after nr.
// 10, and nr. 12 citing the water duty law.
func TestAChangeTheBaseNotesIsMade(t *testing.T) {
	a, err := corpusArchive(t)
	if err != nil {
		t.Fatal(err)
	}
	v, err := a.LawAsOf("A20100063529", time.Date(2012, 6, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := v.ProvisionText("§ 52 a, stk. 1")
	if !ok || !strings.Contains(got, "10) udgifter til kortlægning") || !strings.Contains(got, "11) udgifter til dækning af en fastsat økonomisk godtgørelse") ||
		!strings.Contains(got, "12) udgifter til dækning af afgifter, jf. § 3, stk. 2, i lov om afgift af ledningsført vand") {
		t.Errorf(`"§ 52 a, stk. 1" = %q`, got)
	}
	for _, u := range v.Unresolved {
		if u.Edit.Source == "A20110055330" || u.Edit.Source == "A20110138430" {
			t.Errorf("%s %s refused: %s", u.Edit.Source, u.Edit.Number, u.Reason)
		}
	}
}

// Aktivloven's consolidation of September 2019 prints repealed ranges as "§§ " with "34 a-34 b." on
// the line after, under a localId that is a sequence number (56): read by it, the stub was found as
// § 56, and "§§ 16-21" as § 35. And lov nr. 551 of May 2019, drafted against the consolidation of
// March 2019, calls § 69, stk. 2, what the September text prints as stk. 3, a subsection having been
// put in front of it in between; its changes are made where that text prints the words.
func TestAktivlovensRangesAndCitedNumbering(t *testing.T) {
	a, err := corpusArchive(t)
	if err != nil {
		t.Fatal(err)
	}
	doc, _, ok := a.Open("A20190098129")
	if !ok {
		t.Fatal("not in the archive")
	}
	c := doc.Model().Content
	for _, n := range []string{"35", "56"} {
		loc, ok := c.Find(lexdoc.Address{Recognised: true, Paragraph: n})
		if !ok || strings.Contains(loc.Provision.Text(), "(Ophævet)") {
			t.Errorf("§ %s found as %q", n, loc.Provision.Text())
		}
	}
	v, err := a.LawAsOf("A20190098129", time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	// § 13 a, stk. 3, of lov nr. 1000 of 2015, printed though it never took effect, went with its
	// repeal by lov nr. 551 of 2019; the consolidation of 2021 prints four subsections.
	if got, ok := v.ProvisionText("§ 13 a, stk. 3"); !ok || !strings.Contains(got, "skal være aktivt jobsøgende. Personen skal ved første jobsamtale") {
		t.Errorf(`"§ 13 a, stk. 3" = %q`, got)
	}
	for _, u := range v.Unresolved {
		if u.Edit.Source == "A20190055130" && (u.Edit.Address.Paragraph == "69" || u.Edit.Address.Paragraph == "69k") {
			t.Errorf("lov nr. 551 of 2019, nr. %s, refused at %s: %s", u.Edit.Number, u.Edit.Address, u.Reason)
		}
	}
}
