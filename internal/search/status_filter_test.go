package search

import "testing"

// TestStatusFilterSelectsWhatTheRegisterSays pins the three buckets a reader can choose, the
// Danish spellings that mean them, and above all that the documents the register says nothing
// about are reachable by name. A status filter that folds unlabelled documents into "historic"
// would quietly turn "only current law" into "only law the register bothered to label", and the
// two would come back with the same-looking result.
func TestStatusFilterSelectsWhatTheRegisterSays(t *testing.T) {
	idx, err := Build(
		[]DocMeta{
			{Accession: "A20200001X", ELI: "eli/lta/2020/1", Kind: "lta", Year: 2020, Number: 1, Title: "Gældende lov", Source: "xml", Status: "Valid", TypeCode: "LOKDOK01"},
			{Accession: "A19990002X", ELI: "eli/lta/1999/2", Kind: "lta", Year: 1999, Number: 2, Title: "Ophævet lov", Source: "xml", Status: "Historic", TypeCode: "LOKDOK01"},
			{Accession: "A18850003X", ELI: "eli/lta/1885/3", Kind: "lta", Year: 1885, Number: 3, Title: "Ulabelset dokument", Source: "xml", Status: "", TypeCode: "KEN#LOKDOK23"},
		},
		[]Unit{
			{Doc: 0, Addr: "§ 1", Text: "pligt i den gældende tekst"},
			{Doc: 1, Addr: "§ 1", Text: "pligt i den ophævede tekst"},
			{Doc: 2, Addr: "§ 1", Text: "pligt i det ulabelset dokument"},
		},
		Options{})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string][]string{
		"valid":     {"A20200001X"},
		"gældende":  {"A20200001X"},
		"Valid":     {"A20200001X"},
		"historic":  {"A19990002X"},
		"historisk": {"A19990002X"},
		"unknown":   {"A18850003X"},
		"ukendt":    {"A18850003X"},
		"":          {"A20200001X", "A19990002X", "A18850003X"},
	}
	for status, wantDocs := range want {
		got := map[string]bool{}
		for _, h := range idx.Search(Query{Text: "pligt", Status: status, Limit: 10}) {
			got[h.Doc.Accession] = true
		}
		if len(got) != len(wantDocs) {
			t.Errorf("status %q returned %d documents, want %d (%v)", status, len(got), len(wantDocs), wantDocs)
			continue
		}
		for _, acc := range wantDocs {
			if !got[acc] {
				t.Errorf("status %q did not return %s", status, acc)
			}
		}
	}
}

// TestTypeCodeFilterIsAPrefixBecauseTheMirrorHasNoNameTable: the codes are the publisher's, so
// the filter matches a prefix and nothing pretends to know what a code means.
func TestTypeCodeFilterIsAPrefix(t *testing.T) {
	idx, err := Build(
		[]DocMeta{
			{Accession: "A1", Kind: "lta", Year: 2020, Source: "xml", TypeCode: "LOKDOK01"},
			{Accession: "A2", Kind: "lta", Year: 2020, Source: "xml", TypeCode: "KEN#LOKDOK23"},
		},
		[]Unit{
			{Doc: 0, Addr: "§ 1", Text: "pligt her"},
			{Doc: 1, Addr: "§ 1", Text: "pligt der"},
		},
		Options{})
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]int{"": 2, "LOKDOK": 2, "KEN#": 1, "KEN#LOKDOK23": 1, "X": 0}
	for prefix, want := range codes {
		hits := idx.Search(Query{Text: "pligt", TypeCode: prefix, Limit: 10})
		if len(hits) != want {
			t.Errorf("type-code prefix %q returned %d hits, want %d", prefix, len(hits), want)
		}
	}
}
