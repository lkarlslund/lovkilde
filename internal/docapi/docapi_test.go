package docapi

import (
	"os"
	"path/filepath"
	"testing"
)

// Fixtures are genuine responses captured from three different LexDania schemas
// (AFG, BEKH, FOB). They exist because a wrong XML field tag does not fail:
// encoding/xml silently leaves mismatched fields empty, so a parser bug shows up
// as "every document has no accession" rather than as an error.
func TestParseXMLMetaRealFixtures(t *testing.T) {
	cases := []struct {
		file       string
		wantAcc    string
		wantDocID  string
		wantRank   string
		wantDocTyp string
	}{
		{"testdata/ken-afg.xml", "W20261001925", "DU000200", "W", "KEN#LOKDOK23"},
		{"testdata/bek-bekh.xml", "B20260078905", "DU000157", "B", "BEK H#LOKDOK04"},
		{"testdata/fob-lok53.xml", "O19800010070", "AX023137", "O", "FOU H#LOKDOK53"},
	}
	for _, c := range cases {
		raw, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatalf("read fixture %s: %v", c.file, err)
		}
		m, err := ParseXMLMeta(raw)
		if err != nil {
			t.Errorf("%s: ParseXMLMeta: %v", c.file, err)
			continue
		}
		if m.Accession != c.wantAcc {
			t.Errorf("%s: Accession = %q, want %q", c.file, m.Accession, c.wantAcc)
		}
		if m.DocumentID != c.wantDocID {
			t.Errorf("%s: DocumentID = %q, want %q", c.file, m.DocumentID, c.wantDocID)
		}
		if m.Rank != c.wantRank {
			t.Errorf("%s: Rank = %q, want %q", c.file, m.Rank, c.wantRank)
		}
		if m.DocumentType != c.wantDocTyp {
			t.Errorf("%s: DocumentType = %q, want %q", c.file, m.DocumentType, c.wantDocTyp)
		}
	}
}

// The archive keys files on the ELI URL, but deduplication and harvest
// resolution key on accession; both must be extracted or documents are refetched
// forever.
func TestParseXMLMetaRejectsGarbage(t *testing.T) {
	if _, err := ParseXMLMeta([]byte("<html>not a document</html>")); err == nil {
		t.Error("non-Dokument XML should be rejected rather than stored as a document")
	}
	if _, err := ParseXMLMeta([]byte("<Dokument><Meta><Rank>X</Rank></Meta></Dokument>")); err == nil {
		t.Error("XML without an accession should be rejected")
	}
}

func TestAccessionFromHref(t *testing.T) {
	cases := map[string]string{
		"http://retsinformation.dk/eli/accn/A20240039929/xml":      "A20240039929",
		"https://www.retsinformation.dk/eli/accn/W20261001925/xml": "W20261001925",
		"https://www.retsinformation.dk/eli/retsinfo/2026/10019":   "",
		"": "",
	}
	for href, want := range cases {
		if got := AccessionFromHref(href); got != want {
			t.Errorf("AccessionFromHref(%s) = %q, want %q", href, got, want)
		}
	}
}

func TestPathFromELIURL(t *testing.T) {
	cases := map[string]string{
		"https://www.retsinformation.dk/eli/retsinfo/2026/10019": "eli/retsinfo/2026/10019",
		"https://www.retsinformation.dk/eli/lta/2026/789":        "eli/lta/2026/789",
		"https://www.retsinformation.dk/eli/lta/2026/789.rdfa":   "eli/lta/2026/789",
		"https://www.retsinformation.dk/eli/accn/A20240039929":   "eli/accn/A20240039929",
		"https://example.com/nope":                               "",
	}
	for in, want := range cases {
		if got := PathFromELIURL(in); got != want {
			t.Errorf("PathFromELIURL(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestParseJSONSiteDocument(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "site-document.json"))
	if err != nil {
		t.Skip("site JSON fixture not present")
	}
	d, err := ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if d.AccessionNumber == "" || d.DocumentHTML == "" {
		t.Errorf("site document missing content: %+v", d)
	}
}
