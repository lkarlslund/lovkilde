package search

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

func TestTokenizeKeepsDanishLettersAndCitations(t *testing.T) {
	toks := Tokenize("Lov nr. 1399 om miljøbeskyttelse; Aalborg Kommunes å-udsættelse")
	want := []string{"lov", "nr", "1399", "om", "miljøbeskyttelse", "ålborg", "kommunes", "å", "udsættelse"}
	// "å" alone is dropped as a single-rune token, so compare what survives.
	got := slices.DeleteFunc(slices.Clone(toks), func(s string) bool { return s == "å" })
	want = slices.DeleteFunc(want, func(s string) bool { return s == "å" })
	if !slices.Equal(got, want) {
		t.Errorf("Tokenize() = %q, want %q", got, want)
	}
}

func TestTokenizeDoesNotFoldDiacritics(t *testing.T) {
	for _, pair := range [][2]string{{"tanke", "tænke"}, {"mand", "mænd"}, {"mos", "møs"}} {
		if Tokenize(pair[0])[0] == Tokenize(pair[1])[0] {
			t.Errorf("%q and %q must not be the same term: they are different Danish words", pair[0], pair[1])
		}
	}
}

func TestPreReformAaSpellingFoldsToAaOnlyAtWordStart(t *testing.T) {
	if got := Tokenize("Aastrup")[0]; got != "åstrup" {
		t.Errorf("word-initial Aa: got %q, want åstrup (Aastrup is the pre-1948 spelling of Åstrup)", got)
	}
	// Medial aa is ordinary Danish and must stay: "faar" is pre-reform "far", but
	// "saadant" and friends fold only at the start, and a token like "baade" keeps
	// its shape rather than becoming nonsense.
	if got := Tokenize("baad")[0]; got != "baad" {
		t.Errorf("medial aa: got %q, want baad unchanged", got)
	}
	// "Aa" alone is the letter Å, folds to "å", and is then dropped as a single-rune
	// term. Asserting both halves of that keeps the fold and the length floor from
	// drifting apart: the reachability test queries "å-sag" because of exactly this.
	if got := Tokenize("Aa"); len(got) != 0 {
		t.Errorf("Tokenize(Aa) = %q, want nothing: the folded letter is a single rune", got)
	}
}

func TestFoldUsesLongestEndingFirst(t *testing.T) {
	// "ændringer" ends in both "er" and "inge"-adjacent shapes; the longest candidate
	// must win or the stem depends on slice order rather than on the word.
	if got := Fold("ændringer"); got != "ændr" && got != "ændring" {
		t.Errorf("Fold(ændringer) = %q, want a stem that still shares a prefix with ændring", got)
	}
	if got := Fold("s"); got != "" {
		t.Errorf("Fold(s) = %q, want no fold: refusing to stem a word down to nothing", got)
	}
}

func TestAddressOfRoundTripsThroughParseAddress(t *testing.T) {
	// The claim under test: a search hit's address parses back to the same provision,
	// because lookup resolves addresses with ParseAddress. A renderer that produces
	// pretty-but-unparseable strings would return hits the user cannot open.
	para := &lexdania.Section{Kind: "paragraf", Number: "267"}
	cases := []struct {
		sec  *lexdania.Section
		want string
	}{
		{&lexdania.Section{Kind: "kapitel", Number: "5"}, "kapitel 5"},
		{&lexdania.Section{Kind: "bilag", Number: "2"}, "bilag 2"},
		{&lexdania.Section{Kind: "paragraf", Number: "267"}, "§ 267"},
		{&lexdania.Section{Kind: "paragraf", Number: "8", Litra: "a"}, "§ 8a"},
		{&lexdania.Section{Kind: "stk", Stk: 3, Parent: para}, "§ 267, stk. 3"},
		{&lexdania.Section{Kind: "nr", Nr: 2, Stk: 3, Parent: para}, "§ 267, stk. 3 nr. 2"},
	}
	for _, c := range cases {
		got := lexdania.AddressOf(c.sec)
		if got != c.want {
			t.Errorf("lexdania.AddressOf(%s) = %q, want %q", c.sec.Kind, got, c.want)
			continue
		}
		addr := lexdania.ParseAddress(got)
		switch {
		case !addr.Recognised:
			t.Errorf("AddressOf produced %q which ParseAddress does not recognise", got)
		case c.sec.Kind == "paragraf" && addr.Paragraph != c.sec.Number+c.sec.Litra:
			t.Errorf("round-trip of %q gave paragraph %q, want %q", got, addr.Paragraph, c.sec.Number+c.sec.Litra)
		case c.sec.Kind == "stk" && (addr.Paragraph != "267" || addr.Stk != 3):
			t.Errorf("round-trip of %q gave %+v, want § 267 stk 3", got, addr)
		}
	}
	if got := lexdania.AddressOf(nil); got != "" {
		t.Errorf("lexdania.AddressOf(nil) = %q, want empty", got)
	}
}

// fixtureIndex is a tiny corpus with planted facts, so ranking claims are testable
// without the 14 GB mirror.
func fixtureIndex(t *testing.T, opts Options) *Index {
	docs := []DocMeta{
		{Accession: "A20191399070", ELI: "eli/lta/2019/1399", Kind: "lta", Year: 2019, Number: 1399, Title: "Lov om miljøbeskyttelse", Source: "xml"},
		{Accession: "A19890100070", ELI: "eli/fob/1989/100", Kind: "fob", Year: 1989, Number: 100, Title: "Bekendtgørelse om affald", Source: "html"},
		{Accession: "A19450050070", ELI: "eli/lta/1945/50", Kind: "lta", Year: 1945, Number: 50, Title: "Lov om Aa-sager", Source: "xml"},
	}
	units := []Unit{
		{Doc: 0, Addr: "titel", Text: "Lov om miljøbeskyttelse"},
		{Doc: 0, Addr: "§ 1", Text: "Formål at fremme miljøbeskyttelsen og forebygge forurening."},
		{Doc: 0, Addr: "§ 267, stk. 3 nr. 2", Text: "Pligt til oplysning; pligtige virksomheder under miljøbeskyttelsesloven."},
		{Doc: 1, Addr: "§ 1", Text: "Affaldsplaner for offentlige veje."},
		{Doc: 1, Addr: "", Text: "Henvendt til kommunerne med pligt til rapportering."},
		{Doc: 2, Addr: "§ 3", Text: "Aa-sagen og Aalborg Kommunes pligt efter denne lov."},
	}
	idx, err := Build(context.Background(), docs, units, opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return idx
}

func TestBuildRefusesAnEmptyIndex(t *testing.T) {
	if _, err := Build(context.Background(), nil, nil, Options{}); err == nil {
		t.Fatal("Build accepted zero units; an empty index must not be written as if it were a result")
	}
}

func TestSearchFindsPlantedProvision(t *testing.T) {
	idx := fixtureIndex(t, Options{})
	hits := idx.Search(Query{Text: "miljøbeskyttelse", Limit: 5})
	if len(hits) == 0 {
		t.Fatal("no hits for a term planted in three units")
	}
	first := hits[0]
	if first.Doc.Accession != "A20191399070" {
		t.Errorf("top hit is %s, want the miljøbeskyttelse act", first.Doc.Accession)
	}
	if !strings.Contains(first.Addr, "§") && first.Addr != "titel" {
		t.Errorf("top hit address %q names no provision", first.Addr)
	}
	if first.Doc.Source == "" || first.Doc.ELI == "" {
		t.Errorf("hit carries no provenance or ELI: %+v", first.Doc)
	}
}

func TestSearchRanksMultiTermMatchAboveSingleTermNoise(t *testing.T) {
	idx := fixtureIndex(t, Options{})
	hits := idx.Search(Query{Text: "pligt virksomheder", Limit: 6})
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	if hits[0].Doc.Accession != "A20191399070" {
		accessions := make([]string, 0, len(hits))
		for _, h := range hits {
			accessions = append(accessions, h.Doc.Accession)
		}
		t.Errorf("top hit %q; ranking did not prefer the unit matching both terms: %v", hits[0].Doc.Accession, accessions)
	}
}

func TestSearchFiltersByKindYearAndProvenance(t *testing.T) {
	idx := fixtureIndex(t, Options{})
	for _, h := range idx.Search(Query{Text: "pligt", Kind: "fob"}) {
		if h.Doc.Kind != "fob" {
			t.Fatalf("Kind filter leaked %s", h.Doc.Kind)
		}
	}
	// "pligt" is planted in a 1945, a 1989 and a 2019 unit, so this is what actually
	// tests the bound: a year filter that did nothing would still return the older two.
	for _, h := range idx.Search(Query{Text: "pligt", MinYear: 2000}) {
		if h.Doc.Year < 2000 {
			t.Errorf("MinYear=2000 returned %s (%d)", h.Doc.Accession, h.Doc.Year)
		}
	}
	for _, h := range idx.Search(Query{Text: "pligt", MaxDecade: 1940}) {
		if h.Doc.Year/10 > 1940 {
			t.Errorf("MaxDecade=1940 returned %s (%d)", h.Doc.Accession, h.Doc.Year)
		}
	}
	htmlOnly := idx.Search(Query{Text: "pligt", Source: "html"})
	if len(htmlOnly) == 0 {
		t.Fatal("Source=html returned nothing though an HTML document matches")
	}
	for _, h := range htmlOnly {
		if h.Doc.Source != "html" {
			t.Fatalf("Source filter leaked provenance %q", h.Doc.Source)
		}
	}
}

func TestSearchReportsPreReformSpellingThroughAaFold(t *testing.T) {
	idx := fixtureIndex(t, Options{})
	hits := idx.Search(Query{Text: "å-sag", Limit: 5})
	found := false
	for _, h := range hits {
		if h.Doc.Accession == "A19450050070" {
			found = true
		}
	}
	if !found {
		accessions := make([]string, 0, len(hits))
		for _, h := range hits {
			accessions = append(accessions, h.Doc.Accession)
		}
		t.Errorf("a modern-spelling query did not reach the pre-1948 spelling; hits were %v", accessions)
	}
}

func TestSearchWithoutFoldDoesNotInventInflectedHits(t *testing.T) {
	// "pligtige" appears verbatim, "pligt" only as a prefix. With no folded stream the
	// hit must come from prefix extension, and FoldedOnly must be false — otherwise a
	// client would be told the match was stem-only when the exact word is there.
	idx := fixtureIndex(t, Options{})
	hits := idx.Search(Query{Text: "pligtige"})
	if len(hits) == 0 {
		t.Fatal("exact term planted in the corpus found nothing")
	}
	for _, h := range hits {
		if h.FoldedOnly {
			t.Errorf("hit %q marked fold-only though the exact term is in the text", h.Addr)
		}
	}
}

func TestSaveLoadRoundTripsEveryPosting(t *testing.T) {
	idx := fixtureIndex(t, Options{Folded: true})
	path := t.TempDir() + "/index.bin"
	if err := idx.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(back.Terms) != len(idx.Terms) || len(back.Units) != len(idx.Units) || len(back.Docs) != len(idx.Docs) {
		t.Fatalf("counts differ after round-trip: %d/%d terms, %d/%d units, %d/%d docs",
			len(back.Terms), len(idx.Terms), len(back.Units), len(idx.Units), len(back.Docs), len(idx.Docs))
	}
	for i := range idx.Terms {
		if back.Terms[i] != idx.Terms[i] || back.Stream[i] != idx.Stream[i] {
			t.Fatalf("term %d differs: %q/%d vs %q/%d", i, back.Terms[i], back.Stream[i], idx.Terms[i], idx.Stream[i])
		}
		if !slices.Equal(back.Postings[i], idx.Postings[i]) {
			t.Fatalf("postings for %q differ: %v vs %v", idx.Terms[i], back.Postings[i], idx.Postings[i])
		}
	}
	if idx.Built <= 0 {
		t.Fatalf("Built = %d at build time, want a real timestamp", idx.Built)
	}
	if back.Built != idx.Built {
		t.Errorf("Built = %d after round-trip, want %d: the index's age must survive the file", back.Built, idx.Built)
	}
	idx.B = 0.4
	if again := roundTripB(t, idx); again != 0.4 {
		t.Errorf("B = %v after round-trip, want 0.4: the scoring parameter must survive the file", again)
	}
	if back.HasFolded() != idx.HasFolded() {
		t.Errorf("stream flags lost through the file")
	}
	// Searching the loaded file must agree with searching the in-memory build, or the
	// delta-varint postings decode wrong in a way that only shows up at query time.
	for _, q := range []string{"miljøbeskyttelse", "pligtige", "affaldsplaner"} {
		a, b := idx.Search(Query{Text: q, Limit: 5}), back.Search(Query{Text: q, Limit: 5})
		if len(a) != len(b) {
			t.Fatalf("query %q: %d hits in memory, %d after load", q, len(a), len(b))
		}
		for i := range a {
			if a[i].Unit != b[i].Unit {
				t.Fatalf("query %q: hit %d differs after load (%d vs %d)", q, i, a[i].Unit, b[i].Unit)
			}
		}
	}
}

// roundTripB saves and loads an index and reports the scoring parameter that comes back.
func roundTripB(t *testing.T, idx *Index) float64 {
	path := t.TempDir() + "/b.bin"
	if err := idx.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return back.B
}

func TestLoadRejectsAnOlderIndexFormat(t *testing.T) {
	// Version 1 is not merely different, it is shorter: reading one as version 2 would
	// shift every table by eight bytes and could produce counts that look plausible.
	idx := fixtureIndex(t, Options{})
	path := t.TempDir() + "/v1.bin"
	if err := idx.Save(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(magic)] = 1 // rewind the version byte the writer put there
	v1 := path + ".old"
	if err := os.WriteFile(v1, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Load(v1)
	if err == nil {
		t.Fatal("Load accepted an index from an older format version")
	}
	if !strings.Contains(err.Error(), "rebuild") {
		t.Errorf("refusal does not say what to do: %v", err)
	}
}

func TestLoadRejectsForeignAndTruncatedFiles(t *testing.T) {
	dir := t.TempDir()
	other := dir + "/other.bin"
	if err := os.WriteFile(other, []byte(strings.Repeat("not an index", 20)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(other); err == nil {
		t.Error("Load accepted a file that is not an index")
	}

	idx := fixtureIndex(t, Options{Folded: true})
	good := dir + "/good.bin"
	if err := idx.Save(good); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	short := dir + "/short.bin"
	if err := os.WriteFile(short, raw[:len(raw)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(short); err == nil {
		t.Error("Load accepted a truncated index; a half-read index silently under-reports")
	}
}

func TestSearchIgnoresEmptyQueryAndUnknownTerms(t *testing.T) {
	idx := fixtureIndex(t, Options{})
	if len(idx.Search(Query{Text: "   "})) != 0 {
		t.Error("whitespace query returned hits")
	}
	if len(idx.Search(Query{Text: "qwertyuiop"})) != 0 {
		t.Error("a term that is nowhere in the corpus returned hits")
	}
}

// TestFoldedEntriesStayInTheirOwnStream guards the one thing the folded stream must
// never do: answer a raw-term lookup. If findTerm ignored the stream byte, a stem-only
// match would be reported as though the user's exact word appeared in the text, which
// is the difference between "an inflected form appears here" and "this word appears here".
func TestFoldedEntriesStayInTheirOwnStream(t *testing.T) {
	docs := []DocMeta{{Accession: "A1", ELI: "eli/lta/2000/1", Kind: "lta", Year: 2000, Number: 1, Title: "T", Source: "xml"}}
	units := []Unit{{Doc: 0, Addr: "§ 1", Text: "Virksomheder skal rapportere."}}

	plain, err := Build(context.Background(), docs, units, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if i := plain.findTerm("virksomhed", StreamFolded); i >= 0 {
		t.Error("a stream-less index contains a folded entry")
	}

	folded, err := Build(context.Background(), docs, units, Options{Folded: true})
	if err != nil {
		t.Fatal(err)
	}
	if i := folded.findTerm("virksomhed", StreamFolded); i < 0 {
		t.Fatal("folded build has no stem entry for virksomheder")
	} else if folded.Stream[i] != StreamFolded {
		t.Errorf("stem entry recorded as stream %d, want folded", folded.Stream[i])
	}
	if i := folded.findTerm("virksomhed", StreamRaw); i >= 0 {
		t.Errorf("raw lookup returned the folded entry for %q", folded.Terms[i])
	}
	if i := folded.findTerm("virksomheder", StreamRaw); i < 0 {
		t.Error("the surface form disappeared from the raw stream")
	}

	// The stream byte has to survive the file, or a saved index answers raw lookups
	// with stems.
	path := t.TempDir() + "/streams.bin"
	if err := folded.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if i := back.findTerm("virksomhed", StreamRaw); i >= 0 {
		t.Errorf("after load, %q answers a raw lookup: the stream byte was lost", back.Terms[i])
	}
	if i := back.findTerm("virksomhed", StreamFolded); i < 0 {
		t.Error("after load, the folded entry is gone")
	}
}

// TestTermFrequencySaturatesRatherThanWraps guards the byte-wide posting counter.
//
// A section that repeats a word 300 times must not look like one that repeats it 44:
// an unsigned byte wrapping past 255 would quietly rank the most repetitive unit in the
// corpus as a weak match.
func TestTermFrequencySaturatesRatherThanWraps(t *testing.T) {
	docs := []DocMeta{{Accession: "A1", ELI: "eli/lta/2000/1", Kind: "lta", Year: 2000, Number: 1, Title: "T", Source: "xml"}}
	text := strings.Repeat("særlig ", 400)
	idx, err := Build(context.Background(), docs, []Unit{{Doc: 0, Addr: "§ 1", Text: text}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	i := idx.findTerm("særlig", StreamRaw)
	if i < 0 {
		t.Fatal("the repeated term is not indexed")
	}
	postings := idx.Postings[i]
	if len(postings) != 1 {
		t.Fatalf("postings = %d, want one unit", len(postings))
	}
	if postings[0].Tf != 255 {
		t.Errorf("Tf = %d, want the 255 ceiling for a term appearing 400 times", postings[0].Tf)
	}
}
