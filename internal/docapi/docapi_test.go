package docapi

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"
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

// The site answers HTTP 200 with its own web page for URIs that have no XML
// representation. That must be recognisable as such, or the crawler treats a
// permanent property of the resource as a transient failure and retries it.
func TestParseXMLMetaDetectsHTMLPayload(t *testing.T) {
	pages := []string{
		"<html><body>Den fulde tekst</body></html>",
		"<!DOCTYPE html><html lang=\"da\">...</html>",
		"\n\n  <HTML>\n",
	}
	for _, p := range pages {
		if !IsHTMLPayload([]byte(p)) {
			t.Errorf("IsHTMLPayload(%q) = false, want true", p[:min(len(p), 30)])
		}
		if _, err := ParseXMLMeta([]byte(p)); !errors.Is(err, ErrNotXML) {
			t.Errorf("ParseXMLMeta on HTML gave %v, want ErrNotXML", err)
		}
	}

	// Real canonical XML must not be caught by the check, including when it is
	// preceded by whitespace or an XML declaration.
	good, err := os.ReadFile(filepath.Join("testdata", "ken-afg.xml"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if IsHTMLPayload(good) {
		t.Error("canonical ELI XML misclassified as HTML")
	}
	if IsHTMLPayload([]byte("<?xml version=\"1.0\"?>\n<Dokument><Meta><AccessionNumber>A1</AccessionNumber></Meta></Dokument>")) {
		t.Error("XML declaration misclassified as HTML")
	}
}

// TextLen decides whether a payload carries a document or just a wrapper, so the
// classification of 29k body-less laws rests on it.
func TestTextLenCountsVisibleText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"heading", `<div class="bjelke">Den fulde tekst</div>`, utf8.RuneCountInString("Den fulde tekst")},
		{"two paragraphs", `<p>§ 1.</p><p>tekst</p>`, utf8.RuneCountInString("§ 1. tekst")},
		{"inline span keeps its text", `<span class="ParagrafNr">§ 1.</span> Det er formålet`, utf8.RuneCountInString("§ 1. Det er formålet")},
		{"comment is not text", "<p>a<!-- k -->b</p>", utf8.RuneCountInString("a b")},
		{"entities decoded", "An &aelig;ble", utf8.RuneCountInString("An æble")},
		{"nbsp is a space", "a&nbsp;b", utf8.RuneCountInString("a b")},
		{"escaped markup is text", "&lt;p&gt;", 3},
		{"whitespace collapses", "a   \n\t  b", 3},
		{"no text at all", `<div class="bjelke"></div><p>  </p>`, 0},
		{"invisible marks are not text", "\u200b\u200c\u200d\ufeff\u00ad", 0},
	}
	for _, tc := range cases {
		if got := TextLen(tc.in); got != tc.want {
			t.Errorf("%s: TextLen(%q) = %d, want %d", tc.name, tc.in, got, tc.want)
		}
	}
}
