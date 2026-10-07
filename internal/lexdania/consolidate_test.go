package lexdania

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAddress(t *testing.T) {
	cases := []struct {
		in         string
		par        string
		stk        int
		nr         int
		chapter    int
		bilag      int
		rangeTo    string
		heading    bool
		recognised bool
	}{
		{in: "I § 267, stk. 1, ændres …", par: "267", stk: 1, recognised: true},
		{in: "§ 5, stk. 2, nr. 3, ophæves", par: "5", stk: 2, nr: 3, recognised: true},
		{in: "i §§ 259-266", par: "259", rangeTo: "266", recognised: true},
		{in: "I lovens § 12 er …", par: "12", recognised: true},
		{in: "jf. bekendtgørelsens § 8 a, stk. 3", par: "8a", stk: 3, recognised: true},
		{in: "kapitel 4 ophæves", chapter: 4, recognised: true},
		{in: "i bilag 1 ændres", bilag: 1, recognised: true},
		{in: "overskriften for § 12 ændres", par: "12", heading: true, recognised: true},
		{in: "§§ 280-282", par: "280", rangeTo: "282", recognised: true},
		{in: "Bekendtgørelsen træder i kraft den 1. januar 2020", recognised: false},
		{in: "som affattet i lovbekendtgørelsen", recognised: false},
	}
	for _, c := range cases {
		got := ParseAddress(c.in)
		if got.Paragraph != c.par || got.Stk != c.stk || got.Nr != c.nr ||
			got.Chapter != c.chapter || got.Bilag != c.bilag || got.RangeTo != c.rangeTo ||
			got.Heading != c.heading || got.Recognised != c.recognised {
			t.Errorf("ParseAddress(%q) = %+v", c.in, got)
		}
	}
}

// TestSectionsExcludesProposedText guards a subtle corruption: an amending act embeds
// replacement provisions in <AendringNyTekst>, and those <Paragraf> elements carry
// their own localId values. Counting them as the amender's own provisions makes an
// amendment appear to contain the law it amends, and every later address lookup then
// resolves against proposed text instead of enacted text.
func TestSectionsExcludesProposedText(t *testing.T) {
	amender := mustParse(t, fixtureAmender2)
	secs := amender.Sections()

	for _, s := range secs {
		if s.RawNumber == "§ 4." || s.Number == "4" {
			t.Errorf("proposed § 4 from AendringNyTekst counted as the amender's own provision: %+v", s)
		}
	}
	if len(secs) == 0 {
		t.Fatal("amending act should still expose its own centreret paragraphs")
	}
	if secs[0].Number != "1" {
		t.Errorf("amender's own paragraph number = %q, want 1", secs[0].Number)
	}

	// The base law's top-level units are the document title, kapitel + 3 paragrafer +
	// bilag. Subsections are in the same flat list but parented, so count only the
	// roots. The title is in the list on purpose: "Lovens titel affattes således:" is a
	// real instruction that needs something to point at.
	base := mustParse(t, fixtureLaw)
	var roots []string
	for _, s := range base.Sections() {
		if s.Parent == nil {
			roots = append(roots, s.Kind+":"+s.Number)
		}
	}
	if want := []string{"titel:", "kapitel:1", "paragraf:1", "paragraf:2", "paragraf:3", "bilag:1"}; !equalStrings(roots, want) {
		t.Errorf("base top-level sections = %v, want %v", roots, want)
	}
	if len(base.Sections()) <= len(roots) {
		t.Errorf("subsection sections missing from the flat list: %d total, %d roots", len(base.Sections()), len(roots))
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestSubsectionNumberingIsPositional(t *testing.T) {
	doc := mustParse(t, fixtureLaw)
	secs := doc.Sections()

	one := Find(secs, ParseAddress("§ 2, stk. 1"))
	if one == nil {
		t.Fatal("§ 2, stk. 1 not found")
	}
	two := Find(secs, ParseAddress("§ 2, stk. 2"))
	if two == nil {
		t.Fatal("§ 2, stk. 2 not found")
	}
	if one.Match == two.Match {
		t.Error("both subsections resolved to the same text")
	}
	if !strings.Contains(one.Match, "anmode om tilladelse") {
		t.Errorf("stk 1 content = %q", one.Match)
	}
	if !strings.Contains(two.Match, "Erhvervsstyrelsen") {
		t.Errorf("stk 2 content = %q", two.Match)
	}
}

// TestRestatedEditIsCounted covers the edit that finds its quoted passage and writes back text
// identical to what was already there: an amendment that restates existing law. It stays
// applied, but the outcome has to be able to say it changed nothing, because the alternative is
// an engine that reports amendments applied while leaving the law alone — which is what it did,
// undetectably, until the run list was fixed.
func TestRestatedEditIsCounted(t *testing.T) {
	doc := mustParse(t, fixtureLaw)
	c, out := applyToModel(doc, Edit{Kind: EditReplaceText, Address: ParseAddress("§ 2, stk. 1"),
		Old: "Selskabet", New: "Selskabet"})
	if !out.Applied {
		t.Fatalf("a passage that is present was not applied: %s", out.Reason)
	}
	if !out.NoOp {
		t.Error("an edit that changed nothing was not reported as a no-op")
	}
	before := provisionText(t, c, "§ 2, stk. 1")

	// And the same address with real new text must not be counted as a no-op.
	out = c.Apply(modelEdits([]Edit{{Kind: EditReplaceText, Address: ParseAddress("§ 2, stk. 1"),
		Old: "anmode om tilladelse før", New: "søge om tilladelse før"}})[0])
	if !out.Applied || out.NoOp {
		t.Errorf("an edit that changed text: %+v", out)
	}
	if provisionText(t, c, "§ 2, stk. 1") == before {
		t.Error("a real edit left the provision unchanged")
	}
}

func TestAmendmentExtraction(t *testing.T) {
	one := mustParse(t, fixtureAmender1)
	edits := one.Amendments()
	if len(edits) != 2 {
		t.Fatalf("edits = %d, want 2", len(edits))
	}
	if edits[0].Kind != EditReplaceText {
		t.Errorf("edit 1 kind = %q, want %q", edits[0].Kind, EditReplaceText)
	}
	if QuoteStrip(edits[0].Old) != "anmode om tilladelse før" {
		t.Errorf("edit 1 Old = %q", edits[0].Old)
	}
	if edits[0].New != "søge om tilladelse før" {
		t.Errorf("edit 1 New = %q", edits[0].New)
	}
	if !edits[0].Address.Recognised || edits[0].Address.Paragraph != "2" || edits[0].Address.Stk != 1 {
		t.Errorf("edit 1 address = %+v", edits[0].Address)
	}
	if edits[1].Kind != EditDeleteProvision {
		t.Errorf("edit 2 kind = %q, want %q", edits[1].Kind, EditDeleteProvision)
	}

	two := mustParse(t, fixtureAmender2)
	var inserted *Edit
	for i := range two.Amendments() {
		e := two.Amendments()[i]
		if e.Kind == EditInsertProvision {
			inserted = &two.Amendments()[i]
		}
	}
	if inserted == nil {
		t.Fatalf("no insertion found in %+v", two.Amendments())
	}
	if len(inserted.NewNodes) == 0 {
		t.Error("insertion lost its structured new text")
	}

	st := Coverage(two.Amendments())
	if st.Total != 2 || st.UnrecognisedAddress != 0 {
		t.Errorf("coverage = %v", st)
	}
}

func TestVersionsOrderedByEffectiveDate(t *testing.T) {
	doc := mustParse(t, fixtureLaw)
	vs := doc.Versions()
	if len(vs) != 2 {
		t.Fatalf("versions = %d", len(vs))
	}
	if !vs[0].Effective.Before(vs[1].Effective) {
		t.Errorf("versions not ordered by effective date: %v", vs)
	}
	if vs[0].Accession != "B20060000205" || vs[1].Accession != "B20070000305" {
		t.Errorf("versions = %+v", vs)
	}

	// Ordering must not depend on document order in Meta: swap the two anchors and the
	// result must come back in the same date order.
	swapped := strings.Replace(fixtureLaw,
		"<Change id=\"change_1\" />\n    <Ref_Accn REFid=\"change_1\">B20060000205</Ref_Accn>\n    <Ref_Af REFid=\"change_1\">2006-07-01</Ref_Af>",
		"<Change id=\"change_1\" />\n    <Ref_Accn REFid=\"change_1\">B20070000305</Ref_Accn>\n    <Ref_Af REFid=\"change_1\">2007-01-01</Ref_Af>", 1)
	swapped = strings.Replace(swapped,
		"<Ref_Accn REFid=\"change_2\">B20070000305</Ref_Accn>\n    <Ref_Af REFid=\"change_2\">2007-01-01</Ref_Af>",
		"<Ref_Accn REFid=\"change_2\">B20060000205</Ref_Accn>\n    <Ref_Af REFid=\"change_2\">2006-07-01</Ref_Af>", 1)
	reordered := mustParse(t, swapped).Versions()
	if reordered[0].Accession != "B20060000205" {
		t.Errorf("swap of Meta order changed version order: %+v", reordered)
	}
}

// writeArchive lays fixtures out the way the archive stores them.
func writeArchive(t *testing.T, files map[string]string) *Archive {
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a, err := Scan(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestArchiveScanAndResolve(t *testing.T) {
	a := writeArchive(t, map[string]string{
		"lta/2005/1.xml": fixtureLaw,
		"lta/2006/2.xml": fixtureAmender1,
		"lta/2007/3.xml": fixtureAmender2,
	})
	if a.Len() != 3 {
		t.Fatalf("indexed %d documents, want 3 (Skipped=%d)", a.Len(), a.Skipped)
	}
	e, ok := a.Find("A20050000129")
	if !ok {
		t.Fatal("base law not indexed by accession")
	}
	if e.Kind != "lta" || e.Number != 1 || e.Year != 2005 {
		t.Errorf("entry = %+v", e)
	}
	if got := e.ELIPath(); got != "eli/lta/2005/1" {
		t.Errorf("ELIPath = %q", got)
	}

	// Resolve a preamble hint the way consolidation does it: number plus signing date.
	got := a.Resolve(TargetHint{Number: 1, Date: time.Date(2005, 3, 1, 0, 0, 0, 0, time.UTC)})
	if len(got) != 1 || got[0].Accession != "A20050000129" {
		t.Errorf("Resolve = %+v", got)
	}
	// Same number, wrong date, must not match.
	if got := a.Resolve(TargetHint{Number: 1, Date: time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)}); len(got) != 0 {
		t.Errorf("Resolve matched a document signed on a different date: %+v", got)
	}

	vs, err := a.Versions("A20050000129")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		if !v.Found {
			t.Errorf("chain document %s should be present in this archive", v.Accession)
		}
	}
}

func TestViewAsOfStages(t *testing.T) {
	a := writeArchive(t, map[string]string{
		"lta/2005/1.xml": fixtureLaw,
		"lta/2006/2.xml": fixtureAmender1,
		"lta/2007/3.xml": fixtureAmender2,
	})

	asOf := func(y int, m time.Month, d int) *View {
		v, err := a.LawAsOf("A20050000129", time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	// Before any amendment.
	v := asOf(2005, 6, 1)
	if text := v.Text(); !strings.Contains(text, "anmode om") {
		t.Error("pre-amendment text should still read 'anmode om'")
	}
	if len(v.Applied) != 0 {
		t.Errorf("applied %d edits before the first effective date", len(v.Applied))
	}

	// After the textual amendment only.
	v = asOf(2006, 8, 1)
	if text := v.Text(); !strings.Contains(text, "søge om") {
		t.Error("text amendment not applied by 2006-08-01")
	}
	if text := v.Text(); !strings.Contains(text, "Loven træder i kraft straks") {
		t.Error("§ 3 should still exist on 2006-08-01")
	}
	if v.SkippedForeign != 1 {
		t.Errorf("SkippedForeign = %d, want 1 (the change aimed at law nr. 999)", v.SkippedForeign)
	}
	if len(v.Unresolved) != 0 {
		t.Errorf("unexpected unresolved edits: %+v", v.Unresolved)
	}
	if !v.Trustworthy() {
		t.Errorf("view should be complete; %v", v.Unresolved)
	}

	// After both amendments: § 3 repealed, § 4 inserted.
	v = asOf(2007, 6, 1)
	if text := v.Text(); strings.Contains(text, "Loven træder i kraft straks") {
		t.Error("§ 3 should have been repealed by 2007-06-01")
	}
	if text := v.Text(); !strings.Contains(text, "henvisning i erhvervsregistret") {
		t.Error("inserted § 4 missing by 2007-06-01")
	}
	if has(v, "§ 3") {
		t.Error("repealed § 3 is still addressable")
	}
	if !has(v, "§ 4") {
		t.Error("inserted § 4 is not addressable")
	}
	if !v.Trustworthy() {
		t.Errorf("view should be complete: %+v", v.Unresolved)
	}
	if err := v.Content.Validate(); err != nil {
		t.Errorf("content broken by structural edits: %v", err)
	}
}

// TestViewDoesNotMutateBase guards the caching hazard: the same Document is handed to
// every view of every date, so a view that edited in place would make results depend
// on call order.
func TestViewDoesNotMutateBase(t *testing.T) {
	doc := mustParse(t, fixtureLaw)
	before := doc.Root.PlainText()

	open := func(accn string) (*Document, string, bool) {
		switch accn {
		case "B20060000205":
			return mustParse(t, fixtureAmender1), "a", true
		case "B20070000305":
			return mustParse(t, fixtureAmender2), "b", true
		}
		return nil, "", false
	}
	v := LawAsOf(doc, time.Date(2009, 1, 1, 0, 0, 0, 0, time.UTC), open)
	if len(v.Applied) < 2 {
		t.Fatalf("expected at least 2 applied edits, got %d (%+v)", len(v.Applied), v.Unresolved)
	}
	if doc.Root.PlainText() != before {
		t.Error("LawAsOf mutated the caller's document")
	}
}

func TestMissingChainDocumentIsReported(t *testing.T) {
	a := writeArchive(t, map[string]string{"lta/2005/1.xml": fixtureLaw})
	v, err := a.LawAsOf("A20050000129", time.Date(2009, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if v.Trustworthy() {
		t.Error("a view whose chain documents are missing must not be presented as complete")
	}
	// A missing document is a gap in what we hold, not a change we refused: the two have
	// different remedies, so they are reported apart.
	if got := v.MissingChain(); len(got) != 2 {
		t.Errorf("missing chain = %+v, want both amenders", got)
	}
	if len(v.Unresolved) != 0 {
		t.Errorf("a missing document was reported as a refused change: %+v", v.Unresolved)
	}
}

// TestSilentChainVersionIsNotTrustworthy covers the shape most of the pre-2010 archive arrives
// in. Those documents' XML carries metadata and chain anchors but no body, which lives in the
// HTML payload instead, so a change document parsed this way yields no changes at all.
//
// The receipt the engine used to give for that was indistinguishable from a clean one: applied 0,
// refused 0, nothing missing, trustworthy. B20000072305 shows what that is worth: eight amending
// acts, all eight found, applied 0, refused 0, and 44 provisions standing exactly as promulgated.
// A refusal says we would not guess. Silence says there was nothing to read, and only one of the
// two is a fact a reader can act on.
func TestSilentChainVersionIsNotTrustworthy(t *testing.T) {
	a := writeArchive(t, map[string]string{
		"lta/2005/1.xml": fixtureLaw,
		"lta/2006/2.xml": fixtureBodylessAmender,
	})
	v, err := a.LawAsOf("A20050000129", time.Date(2009, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Applied) != 0 {
		t.Fatalf("a bodyless amender should produce no edits, got %d", len(v.Applied))
	}
	for _, u := range v.Unresolved {
		if strings.Contains(u.Reason, "B20060000205") {
			t.Errorf("the unread amender was refused rather than counted silent: %+v", u)
		}
	}
	if len(v.Silent) != 1 || v.Silent[0].Accession != "B20060000205" {
		t.Errorf("silent = %+v, want the one amender we could not read", v.Silent)
	}
	if v.Trustworthy() {
		t.Error("a chain whose amending acts yielded nothing must not be presented as complete")
	}
	if !strings.Contains(v.String(), "1 read with no result") {
		t.Errorf("the receipt does not say the amender was read with no result: %s", v)
	}
}

// TestForeignOnlyAmenderIsNotSilent keeps the two apart: an act that changes other laws is a
// complete answer about this one, while an act we could not read is not.
func TestForeignOnlyAmenderIsNotSilent(t *testing.T) {
	a := writeArchive(t, map[string]string{
		"lta/2005/1.xml": fixtureLaw,
		"lta/2006/2.xml": fixtureForeignAmender,
	})
	v, err := a.LawAsOf("A20050000129", time.Date(2009, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Silent) != 0 {
		t.Errorf("an amender whose changes aim at another law is answered, not unread: %+v", v.Silent)
	}
	if v.SkippedForeign == 0 {
		t.Error("the foreign changes should still be counted")
	}
}

func TestUnresolvableAddressIsRefused(t *testing.T) {
	doc := mustParse(t, fixtureLaw)
	_, out := applyToModel(doc, Edit{Kind: EditDeleteProvision, Address: ParseAddress("§ 999"), Number: "1."})
	if out.Applied || out.Reason == "" {
		t.Errorf("an edit with an unfindable address must be refused with a reason: %+v", out)
	}
}

func TestUnknownKindIsRefused(t *testing.T) {
	doc := mustParse(t, fixtureLaw)
	_, out := applyToModel(doc, Edit{Kind: EditUnknown, Address: ParseAddress("§ 1"), Prose: "some wording we cannot parse"})
	if out.Applied {
		t.Errorf("unknown kind must be refused: %+v", out)
	}
}

// TestForeignTargetNotApplied is the highest-consequence guard in this package: a
// change written for another law must never be applied to the law being
// reconstructed, because the result is plausible text that is simply wrong.
func TestForeignTargetNotApplied(t *testing.T) {
	amender := mustParse(t, fixtureAmender1)
	edits := amender.Amendments()
	base := mustParse(t, fixtureLaw)

	if targetsLaw(edits[1].Target, base.Meta, base.Accession()) {
		t.Errorf("change aimed at %q was accepted for law %q", edits[1].Target.Raw, base.Meta.Accession)
	}
	if !targetsLaw(edits[0].Target, base.Meta, base.Accession()) {
		t.Errorf("change aimed at the base law itself was rejected: %+v", edits[0].Target)
	}
}

// TestInsertAfterProvisionRepealedInSameAct covers the shape Danish amendatory acts
// actually use: "Efter § 3 indsættes en ny § 4" appearing in the same act that
// repeals § 3. Refusing that would silently lose enacted law, so the insertion is
// anchored positionally and the view says so.
func TestInsertAfterProvisionRepealedInSameAct(t *testing.T) {
	a := writeArchive(t, map[string]string{
		"lta/2005/1.xml": fixtureLaw,
		"lta/2007/3.xml": fixtureAmender2,
	})
	v, err := a.LawAsOf("A20050000129", time.Date(2008, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if has(v, "§ 3") {
		t.Error("§ 3 should be repealed")
	}
	inserted, ok := v.ProvisionText("§ 4")
	if !ok {
		t.Fatalf("insertion was lost; unresolved = %+v", v.Unresolved)
	}
	if !strings.Contains(inserted, "henvisning i erhvervsregistret") {
		t.Errorf("inserted § 4 content = %q", inserted)
	}

	anchored := false
	for _, ap := range v.Applied {
		if ap.Edit.Kind == EditInsertProvision && strings.Contains(ap.Note, "positionally") {
			anchored = true
		}
	}
	if !anchored {
		t.Errorf("the positional fallback should be recorded in the applied note: %+v", v.Applied)
	}

	// Order must still be sane: the new provision sits after § 2, not before § 1.
	var nums []string
	for _, ref := range v.Content.Provisions() {
		nums = append(nums, ref.Provision.Number)
	}
	if len(nums) != 3 || nums[0] != "1" || nums[1] != "2" || nums[2] != "4" {
		t.Errorf("provision order after repeal+insert = %v, want [1 2 4]", nums)
	}
}

func TestStructuralEditsKeepInvariants(t *testing.T) {
	doc := mustParse(t, fixtureLaw)
	c, out := applyToModel(doc, Edit{Kind: EditDeleteProvision, Address: ParseAddress("§ 3")})
	if !out.Applied {
		t.Fatalf("§ 3 repeal refused: %s", out.Reason)
	}
	// Insert § 4 after § 2, using markup extracted from a real amender.
	amender := mustParse(t, fixtureAmender2)
	var ins *Edit
	for _, e := range amender.Amendments() {
		if e.Kind == EditInsertProvision {
			ins = &e
		}
	}
	if ins == nil {
		t.Fatal("fixture lost its insertion")
	}
	ins.Address = ParseAddress("§ 2")
	if out := c.Apply(modelEdits([]Edit{*ins})[0]); !out.Applied {
		t.Fatalf("§ 4 insertion refused: %s", out.Reason)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("structure broken by structural edits: %v", err)
	}
	if _, ok := c.Find(ParseAddress("§ 3")); ok {
		t.Error("§ 3 survived repeal")
	}
	provisionText(t, c, "§ 4")
	// § 1 and § 2 must still be intact, i.e. the repeal removed exactly one provision.
	provisionText(t, c, "§ 1")
	provisionText(t, c, "§ 2")
}

// has reports whether an address resolves in a consolidated law.
func has(v *View, addr string) bool {
	_, ok := v.ProvisionText(addr)
	return ok
}
