package lexdania

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

func TestParseIdentity(t *testing.T) {
	doc := mustParse(t, fixtureLaw)

	if got := doc.Meta.Accession; got != "A20050000129" {
		t.Errorf("Accession = %q", got)
	}
	if got := doc.Meta.Title; got != "Bekendtgørelse af lov om anpartsselskaber" {
		t.Errorf("Title = %q", got)
	}
	if doc.Meta.Year != 2005 || doc.Meta.Number != 1 {
		t.Errorf("Year/Number = %d/%d", doc.Meta.Year, doc.Meta.Number)
	}
	if doc.Meta.Status != StatusHistoric {
		t.Errorf("Status = %q", doc.Meta.Status)
	}
	if doc.SchemaLocation == "" {
		t.Error("SchemaLocation not read from Dokument attribute")
	}
	// Meta is excluded from Body: it is identity, not content.
	if want := []string{"TitelGruppe", "DokumentIndhold", "UnderskriftGruppe", "Bilag"}; !equalNames(doc.Body, want) {
		t.Errorf("Body = %v, want %v", names(doc.Body), want)
	}
	if len(doc.Meta.Subjects) != 2 || len(doc.Meta.EUReferences) != 1 {
		t.Errorf("repeated Meta elements: subjects=%v eu=%v", doc.Meta.Subjects, doc.Meta.EUReferences)
	}
}

func equalNames(got []*Node, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i, n := range got {
		if n.Name != want[i] {
			return false
		}
	}
	return true
}

func names(ns []*Node) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Name
	}
	return out
}

// TestMixedContentInvariant guards the interleave rule everything else depends on.
// Without it, text split by formatting elements reassembles in the wrong order and
// quote matching silently fails.
func TestMixedContentInvariant(t *testing.T) {
	doc := mustParse(t, fixtureLaw)
	if err := doc.Root.checkInvariant(""); err != nil {
		t.Fatal(err)
	}

	// The § 2 stk 1 sentence is stored as three Char runs with an italic run in the
	// middle; it must come back as one sentence, in order.
	sec := Find(doc.Sections(), ParseAddress("§ 2, stk. 1"))
	if sec == nil {
		t.Fatal("§ 2 stk. 1 not found")
	}
	want := "Selskabet skal anmode om tilladelse før erhvervelse af egne anparter."
	if got := Squeeze(sec.Node.Elem("Exitus").PlainText()); got != want {
		t.Errorf("reassembled sentence =\n  %q\nwant\n  %q", got, want)
	}
}

func TestReferenceResolution(t *testing.T) {
	doc := mustParse(t, fixtureLaw)

	changes := doc.Meta.Changes()
	if len(changes) != 2 {
		t.Fatalf("Changes = %d, want 2", len(changes))
	}
	if changes[0].Accn != "B20060000205" || changes[0].Effective != "2006-07-01" {
		t.Errorf("change 1 = %+v", changes[0])
	}
	if changes[0].Text == "" {
		t.Error("Ref_Text not attached to Change anchor")
	}

	concerns := doc.Meta.Concerns()
	if len(concerns) != 1 || concerns[0].Accn != "A20040099929" {
		t.Errorf("Concerns = %+v", concerns)
	}

	subs := doc.Meta.Submissions()
	if len(subs) != 1 || subs[0].StartDate != "2005-04-01" || subs[0].EndDate != "2015-01-01" {
		t.Errorf("Submissions = %+v", subs)
	}

	// Signature attaches to a Sign anchor; the value must not be lost just because
	// the element holding it is empty.
	var signed string
	for _, r := range doc.Meta.References {
		if r.Kind == "Sign" {
			signed = r.Signature
		}
	}
	if signed != "HANS HOLGER ARNESEN" {
		t.Errorf("Sign anchor signature = %q", signed)
	}

	// Nothing here should have been left unmapped or warned about.
	if len(doc.Meta.Rest) != 0 {
		t.Errorf("unmapped Meta children: %v", names(doc.Meta.Rest))
	}
	if len(doc.Warnings()) != 0 {
		t.Errorf("warnings = %v", doc.Warnings())
	}
}

// TestOrphanRefIsReported proves that a broken link is surfaced rather than dropped.
// Renaming the anchor's id splits the relationship in two: the anchor survives with no
// accession, and its value elements point at nothing. Both halves are reported,
// because either one alone would silently truncate a version chain.
func TestOrphanRefIsReported(t *testing.T) {
	broken := strings.Replace(fixtureLaw,
		"<Concerns id=\"concerns_1\" />", "<Concerns id=\"concerns_gone\" />", 1)
	doc := mustParse(t, broken)

	concerns := doc.Meta.Concerns()
	if len(concerns) != 1 {
		t.Fatalf("Concerns anchors = %d, want 1", len(concerns))
	}
	if concerns[0].Accn != "" {
		t.Errorf("anchor kept an accession it should not have: %+v", concerns[0])
	}

	var brokenAnchor, orphanedValues bool
	for _, w := range doc.Warnings() {
		switch {
		case strings.Contains(w, "no Ref_Accn"):
			brokenAnchor = true
		case strings.Contains(w, "no matching anchor"):
			orphanedValues = true
		}
	}
	if !brokenAnchor {
		t.Error("anchor without an accession was not reported")
	}
	if !orphanedValues {
		t.Error("Ref_* elements pointing at a removed anchor were not reported")
	}
}

func TestAccessionParsing(t *testing.T) {
	cases := []struct {
		in     string
		rank   string
		year   int
		number int
		code   string
		ok     bool
	}{
		{"A20250033129", "A", 2025, 331, "29", true},
		{"B19800001405", "B", 1980, 14, "05", true},
		{"W20261001925", "W", 2026, 10019, "25", true},
		// The type code is alphanumeric for retsinfo documents; a numeric parse
		// turned G198050837R1 into type code 1 and dropped the R.
		{"G198050837R1", "G", 1980, 50837, "R1", true},
		{"A2025", "", 0, 0, "", false},
		{"202500331290", "", 0, 0, "", false},
		{"", "", 0, 0, "", false},
	}
	for _, c := range cases {
		got := ParseAccession(c.in)
		if got.WellFormed != c.ok {
			t.Errorf("ParseAccession(%q).WellFormed = %v, want %v", c.in, got.WellFormed, c.ok)
			continue
		}
		if !c.ok {
			continue
		}
		if got.Rank != c.rank || got.Year != c.year || got.Number != c.number || got.TypeCode != c.code {
			t.Errorf("ParseAccession(%q) = %+v", c.in, got)
		}
	}
	if got := ParseAccession("A20250033129").Base(); got != "A202500331" {
		t.Errorf("Base() = %q", got)
	}
}

// TestBillIdentifierScheme covers the second <AccessionNumber> scheme, found by the
// corpus test: every ft/ document carries a parliamentary bill id instead of a
// gazette accession, and both are exactly 12 characters. Treating the bill form as
// malformed made a whole document class look corrupt.
func TestBillIdentifierScheme(t *testing.T) {
	cases := []struct {
		in      string
		scheme  string
		year    int
		session string
		letter  string
		number  int
	}{
		{"201013L00173", SchemeBill, 2010, "13", "L", 173},
		{"19531CB00161", SchemeBill, 1953, "1", "CB", 161},
		{"19951DA00029", SchemeBill, 1995, "1", "DA", 29},
		{"20211XX00420", SchemeBill, 2021, "1", "XX", 420},
		{"198512K00002", SchemeBill, 1985, "12", "K", 2},
		// Recent bills number the bill with 4 digits instead of 5, so the widths
		// inside a bill id genuinely vary and cannot be sliced by fixed offsets.
		{"200423LA0079", SchemeBill, 2004, "23", "LA", 79},
		{"202523LA0012", SchemeBill, 2025, "23", "LA", 12},
		{"A18521100030", SchemeAccession, 1852, "", "", 11000},
	}
	for _, c := range cases {
		got := ParseAccession(c.in)
		if !got.WellFormed || got.Scheme != c.scheme {
			t.Errorf("ParseAccession(%q) = %+v, want scheme %q", c.in, got, c.scheme)
			continue
		}
		if got.Year != c.year || got.Session != c.session || got.BillLetter != c.letter || got.Number != c.number {
			t.Errorf("ParseAccession(%q) = %+v", c.in, got)
		}
	}
	// A bill has no type code to strip, so it must not be grouped with anything.
	if got := ParseAccession("201013L00173").Base(); got != "201013L00173" {
		t.Errorf("bill Base() = %q, want the whole id", got)
	}
	if !ParseAccession("201013L00173").IsBill() || ParseAccession("A18521100030").IsBill() {
		t.Error("IsBill() must distinguish the two schemes")
	}
}

// TestBillsAreNotAmendments guards the most dangerous classification error available
// here: a bill ("Lovforslag som vedtaget", "Betænkning over lovforslag") carries
// amendment markup that looks exactly like an enacted act's, and applying one would
// insert law that was never passed.
func TestBillsAreNotAmendments(t *testing.T) {
	billTypes := []string{
		"Lovforslag som fremsat", "Lovforslag som vedtaget", "Betænkning  over lovforslag",
		"Beslutningsforslag som fremsat", "LSV L#LOKDOK27", "LSF L#LOKDOK25",
		"BSF B#LOKDOK41", "BTL L#LOKDOK32", "SF.L#LOKDOK28",
	}
	for _, dt := range billTypes {
		d := &Document{Meta: Meta{DocumentType: dt, Accession: "201013L00173"}}
		if !d.IsBill() {
			t.Errorf("DocumentType %q should be a bill", dt)
		}
	}
	notBills := []string{"Lovbekendtgørelse", "BEK Æ#LOKDOK05", "LOV Æ#LOKDOK02", "Ændringslov", "Cirkulære (1 sp)"}
	for _, dt := range notBills {
		d := &Document{Meta: Meta{DocumentType: dt, Accession: "A20100011030"}}
		if d.IsBill() {
			t.Errorf("DocumentType %q should not be a bill", dt)
		}
	}
}

// TestBillInChainIsNotApplied proves the consolidator refuses to apply a bill that
// appears in a law's version chain, even though its markup is indistinguishable
// from an enacted amendment's.
func TestBillInChainIsNotApplied(t *testing.T) {
	// The accession is left alone so the chain still resolves: the point is that the
	// bill guard fires on the document's own type, not on whether it can be found.
	bill := strings.Replace(fixtureAmender1,
		"<DocumentType>BEK Æ#LOKDOK05</DocumentType>",
		"<DocumentType>Lovforslag som fremsat</DocumentType>", 1)
	a := writeArchive(t, map[string]string{
		"lta/2005/1.xml":  fixtureLaw,
		"ft/2006/205.xml": bill,
	})
	v, err := a.LawAsOf("A20050000129", time.Date(2008, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if v.SkippedBills == 0 {
		t.Fatalf("the bill should have been skipped as a proposal; unresolved = %+v", v.Unresolved)
	}
	if strings.Contains(v.Text(), "søge om tilladelse før") {
		t.Fatal("a bill's proposed text was applied to the consolidated law")
	}
}

// TestAmendmentTypeSpellings guards the bug where filtering amendments on the word
// "ændr" misses every document whose type is a raw code. "BEK Æ#LOKDOK05" is an
// amending act, and it is about a third of them in lta.
func TestAmendmentTypeSpellings(t *testing.T) {
	doc := mustParse(t, fixtureAmender1)
	if !doc.IsAmendment() {
		t.Errorf("BEK Æ#LOKDOK05 not recognised as an amendment (family %q class %q)",
			doc.TypeFamily(), doc.TypeClass())
	}
	if doc.TypeFamily() != "BEK" || doc.TypeClass() != "Æ" {
		t.Errorf("family/class = %q/%q", doc.TypeFamily(), doc.TypeClass())
	}

	base := mustParse(t, fixtureLaw)
	if base.IsAmendment() {
		t.Error("a Lovbekendtgørelse should not be classified as an amendment")
	}

	doc2 := mustParse(t, fixtureAmender2)
	if !doc2.IsAmendment() {
		t.Error("Ændringsbekendtgørelse should be an amendment")
	}
}

func TestParseDanishDates(t *testing.T) {
	want := time.Date(2019, 12, 11, 0, 0, 0, 0, time.UTC)
	for _, in := range []string{"2019-12-11", "11. december 2019", "11. december 2019."} {
		got, ok := ParseDate(in)
		if !ok || !got.Equal(want) {
			t.Errorf("ParseDate(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	if _, ok := ParseDate("11. dec 2019"); ok {
		t.Error("abbreviated month should not parse as a date")
	}
}

// TestTargetHintParsesDanishMonth is the guard for a bug found while writing this
// package: a numeric-month regex silently produced an empty TargetHint, and an empty
// hint is accepted as "target unknown", which would have applied a change meant for
// another law to the law being reconstructed.
// TestTargetHintParsesCompoundInstrument is the guard for a contamination bug found by the
// model/tree parity gate: the instrument reference was matched only at a word boundary, so
// "jf. lovbekendtgørelse nr. 295 af …" — the ordinary way of citing a consolidated law — yielded
// an empty hint. An empty hint is accepted for every law, which put an election act's changes
// (§ 96, § 100) into the municipal government act that has neither paragraph.
func TestTargetHintParsesCompoundInstrument(t *testing.T) {
	got := parseTarget("I lov om kommunale og regionale valg, jf. lovbekendtgørelse nr. 295 af 7. marts 2022, foretages følgende ændringer:")
	if got.Number != 295 {
		t.Fatalf("Number = %d, want 295 (hint %q)", got.Number, got.Raw)
	}
	want := time.Date(2022, 3, 7, 0, 0, 0, 0, time.UTC)
	if !got.Date.Equal(want) {
		t.Errorf("Date = %v, want %v", got.Date, want)
	}

	// The hint must then be able to say no. The base here is bekendtgørelse nr. 1329, whose
	// changes these are not.
	styrelsesloven := Meta{Number: 1329}
	if targetsLaw(got, styrelsesloven, AccessionInfo{}) {
		t.Error("an election act's changes were accepted for bekendtgørelse nr. 1329")
	}
	electionLaw := Meta{Number: 295}
	if !targetsLaw(got, electionLaw, AccessionInfo{}) {
		t.Error("the election act's own changes were rejected")
	}

	for _, form := range []string{
		"I anordning nr. 44 af 22. februar 2021 om …",
		"I forordning nr. 712 af 3. april 2020 …",
		"I cirkulære nr. 12 af 9. oktober 1998 …",
		"I lov nr. 102 af 24. januar 2024 …",
	} {
		if h := parseTarget(form); h.Empty() {
			t.Errorf("hint for %q came back empty", form)
		}
	}
}

func TestTargetHintParsesDanishMonth(t *testing.T) {
	got := parseTarget("I bekendtgørelse nr. 1399 af 12. december 2019 om bygningsreglement 2018 (BR18), som senest ændret ved bekendtgørelse nr. 1397 af 2. december 2024, foretages følgende ændringer:")
	if got.Number != 1399 {
		t.Errorf("Number = %d, want 1399", got.Number)
	}
	want := time.Date(2019, 12, 12, 0, 0, 0, 0, time.UTC)
	if !got.Date.Equal(want) {
		t.Errorf("Date = %v, want %v", got.Date, want)
	}

	amender := mustParse(t, fixtureAmender1)
	edits := amender.Amendments()
	if len(edits) < 2 {
		t.Fatalf("got %d edits, want one per amending paragraph", len(edits))
	}
	for _, e := range edits {
		if e.Target.Empty() {
			t.Errorf("edit %s lost its target hint: preamble unparseable", e.Number)
		}
	}
}

func TestHTMLPayloadRejected(t *testing.T) {
	if !IsHTMLPayload([]byte("<!DOCTYPE html>\n<html><body>hi</body></html>")) {
		t.Error("HTML not detected")
	}
	if _, err := Parse([]byte("<!DOCTYPE html><html></html>")); err == nil {
		t.Error("HTML payload should not parse as a document")
	}
}

// TestUnknownElementSurvives proves the reason for the generic tree: an element this
// package has never seen is still reachable and still counted, instead of vanishing.
func TestUnknownElementSurvives(t *testing.T) {
	src := strings.Replace(fixtureLaw,
		"<PopularTitle>Anpartsselskabsloven</PopularTitle>",
		"<FremtidigUkendt type=\"x\"><UnderUnknown value=\"7\"/></FremtidigUkendt>", 1)
	doc := mustParse(t, src)

	kept := false
	for _, n := range doc.Meta.Rest {
		if n.Name == "FremtidigUkendt" {
			kept = true
			if n.Elem("UnderUnknown") == nil {
				t.Error("nested unknown child lost")
			}
		}
	}
	if !kept {
		t.Fatal("unknown Meta element was dropped")
	}
	stats := doc.Root.Stats()
	if stats["FremtidigUkendt"] == nil || stats["FremtidigUkendt"].Seen != 1 {
		t.Errorf("Stats does not report the unknown element: %v", stats["FremtidigUkendt"])
	}
	warned := false
	for _, w := range doc.Warnings() {
		if strings.Contains(w, "FremtidigUkendt") {
			warned = true
		}
	}
	if !warned {
		t.Error("unmapped children should also produce a warning")
	}
}

// TestEncodingRoundTrip checks the parser sees the same tree as encoding/xml for a
// document containing entities and non-ASCII text, so nothing is silently mangled.
func TestEncodingRoundTrip(t *testing.T) {
	src := strings.Replace(fixtureLaw,
		"<Char>Skema over krav.</Char>",
		"<Char>Æøå &amp; § 12 \u00bbcite\u00ab</Char>", 1)
	doc := mustParse(t, src)

	text := doc.Root.PlainText()
	for _, want := range []string{"Æøå & § 12 »cite«"} {
		if !strings.Contains(text, want) {
			t.Errorf("entity/encoding round-trip lost %q", want)
		}
	}
	// Cross-check against the standard decoder's view of the same bytes.
	var std struct {
		XMLName xml.Name
		Meta    struct{ AccessionNumber string }
	}
	if err := xml.Unmarshal([]byte(src), &std); err != nil {
		t.Fatal(err)
	}
	if std.Meta.AccessionNumber != doc.Meta.Accession {
		t.Errorf("standard decoder sees %q, we see %q", std.Meta.AccessionNumber, doc.Meta.Accession)
	}
}

// A number is its document's within the year; the date an act cites with it is sometimes the day of
// publication or a slip, and the change is still the document's. A number from another year, or a
// date months away, is another document.
func TestATargetsDateSlipIsTolerated(t *testing.T) {
	order := Meta{Number: 1398, DiesSigni: "2013-12-12"}
	near := lexdoc.ParseTarget("I bekendtgørelse nr. 1398 af 1. december 2013 om udlægning af områder foretages følgende ændringer:")
	if !targetsLaw(near, order, AccessionInfo{}) {
		t.Error("the order's own change rejected over a date eleven days off")
	}
	far := lexdoc.ParseTarget("I bekendtgørelse nr. 1398 af 1. juni 2013 om noget andet foretages følgende ændringer:")
	if targetsLaw(far, order, AccessionInfo{}) {
		t.Error("a date six months away taken for the same document")
	}
	other := lexdoc.ParseTarget("I bekendtgørelse nr. 1398 af 12. december 2012 om noget andet foretages følgende ændringer:")
	if targetsLaw(other, order, AccessionInfo{}) {
		t.Error("the same number from another year taken for the same document")
	}
}
