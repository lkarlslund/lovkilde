package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

// The server's contract is that an answer identifies itself: which document it is,
// where the text came from, and how current the mirror was when it was read. These
// tests drive Lookup directly rather than over a transport, because the protocol
// plumbing belongs to the SDK and the claims above belong to us.

// meta writes the identity block. Year and number have to follow the path the file is
// stored under: the index reads identity from the file rather than the path, so a
// fixture that lies about one of them tests a document that cannot exist.
func meta(acc, doctype, title string, year, number int) string {
	return `<Meta><AccessionNumber>` + acc + `</AccessionNumber><Year>` + strconv.Itoa(year) +
		`</Year><Number>` + strconv.Itoa(number) + `</Number>` +
		`<DocumentType>` + doctype + `</DocumentType><Status>Valid</Status>` +
		`<DiesSigni>1999-01-01</DiesSigni><DocumentTitle>` + title + `</DocumentTitle></Meta>`
}

const (
	accHTML  = "A19990001X"
	accCanon = "A19980002X"
	accEmpty = "A19970003X"
)

// sidecarWithHTML wraps markup in the array shape the site answers with, encoded so
// the quotes inside the markup survive as JSON escapes.
func sidecarWithHTML(markup string) string {
	raw, err := json.Marshal([]struct {
		DocumentHTML string `json:"documentHtml"`
	}{{DocumentHTML: markup}})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// newTestServer lays out three stored documents in the archive's own shape: one whose
// text is canonical, one whose text only exists in a sidecar, and one with no text.
// Those three states are what every answer has to distinguish.
func newTestServer(t *testing.T) *Server {
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lta/1999/1.xml", "<Dokument>"+meta(accHTML, "Bekendtgørelse", "Testbekendtgørelse om intet", 1999, 1)+"</Dokument>")
	// Built with a encoder-free literal on purpose: the escaping here is the same
	// double-escaping that bit the first version of this fixture, where one quote was
	// escaped and its partner was not, the payload did not decode, and the server
	// correctly reported the document as having no text.
	// The numbers have to arrive as their own spans, which is how the payloads are
	// written: "paragrafnr" and "stknr" are the classes the reader turns into an
	// Explicatus marker, and without a marker a paragraph has no number to address.
	// The paragraph's own prose is unmarked and is stykke 1 by Danish practice, so the
	// first marked stykke is numbered 2 -- a fixture that marked it 1 would be asking
	// the reader to reproduce a shape the corpus does not write.
	write("lta/1999/1.json", sidecarWithHTML(`<p class="Paragraf"><span class="ParagrafNr">§ 1.</span> Bekendtgørelsen finder anvendelse fra ...</p><p class="Stk2"><span class="StkNr">Stk. 2.</span> Tekst læst ud af HTML.</p>`))
	write("lta/1998/2.xml", "<Dokument>"+meta(accCanon, "Lov", "Testlov", 1998, 2)+"<DokumentIndhold><Paragraf>"+
		"<Explicatus>§ 1.</Explicatus><Stk><Explicatus>Stk. 1.</Explicatus>KANONISK TEKST</Stk></Paragraf></DokumentIndhold></Dokument>")
	write("lta/1997/3.xml", "<Dokument>"+meta(accEmpty, "Bekendtgørelse", "Tom dokument", 1997, 3)+"</Dokument>")

	a, err := lexdania.Scan(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if a.Len() != 3 {
		t.Fatalf("indexed %d documents, want 3 (skipped %d)", a.Len(), a.Skipped)
	}
	return New(a, "https://www.retsinformation.dk/", "test")
}

func TestLookupIdentifiesEveryFormOfTheArgument(t *testing.T) {
	s := newTestServer(t)
	for _, in := range []string{
		"eli/lta/1998/2",
		"lta/1998/2",
		"https://www.retsinformation.dk/eli/lta/1998/2",
		"https://www.retsinformation.dk/eli/lta/1998/2/xml",
		accCanon,
		strings.ToLower(accCanon),
	} {
		res, err := s.Lookup(LookupRequest{Document: in})
		if err != nil {
			t.Errorf("Lookup(%q): %v", in, err)
			continue
		}
		if res.Accession != accCanon {
			t.Errorf("Lookup(%q) resolved %q, want %q", in, res.Accession, accCanon)
		}
		if res.ELIURL != "https://www.retsinformation.dk/eli/lta/1998/2" {
			t.Errorf("Lookup(%q) ELIURL = %q", in, res.ELIURL)
		}
	}
}

// The three text states have to come back distinguishable, because "no text" and "text
// we reconstructed" call for opposite things from the caller.
func TestLookupReportsProvenanceForEveryTextState(t *testing.T) {
	s := newTestServer(t)
	cases := []struct {
		doc, wantProvenance, wantInNote string
	}{
		{"lta/1998/2", "xml", "canonical ELI XML"},
		{"lta/1999/1", "html", "reconstruction"},
		{"lta/1997/3", "none", "no text at all"},
	}
	for _, tc := range cases {
		res, err := s.Lookup(LookupRequest{Document: tc.doc})
		if err != nil {
			t.Errorf("Lookup(%q): %v", tc.doc, err)
			continue
		}
		if res.Provenance != tc.wantProvenance {
			t.Errorf("Lookup(%q) provenance = %q, want %q", tc.doc, res.Provenance, tc.wantProvenance)
		}
		if !strings.Contains(res.ProvenanceNote, tc.wantInNote) {
			t.Errorf("Lookup(%q) note = %q, want it to say %q", tc.doc, res.ProvenanceNote, tc.wantInNote)
		}
		if res.Mirror == "" || !strings.Contains(res.Mirror, "3 documents") {
			t.Errorf("Lookup(%q) mirror = %q: an answer has to say how current the mirror was", tc.doc, res.Mirror)
		}
	}
	// The HTML answer names the file it came from, so a reader can go and check.
	res, err := s.Lookup(LookupRequest{Document: "lta/1999/1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ProvenanceNote, "1.json") {
		t.Errorf("the HTML answer does not name its payload: %q", res.ProvenanceNote)
	}
}

func TestLookupAddressSelectsOneProvision(t *testing.T) {
	s := newTestServer(t)
	res, err := s.Lookup(LookupRequest{Document: "lta/1998/2", Address: "§ 1, stk. 1"})
	if err != nil {
		t.Fatalf("Lookup by address: %v", err)
	}
	if !strings.Contains(res.Text, "KANONISK TEKST") {
		t.Errorf("addressed text = %q", res.Text)
	}
	if strings.Contains(res.Text, "Testlov") {
		t.Errorf("addressed text includes the whole document: %q", res.Text)
	}
	if _, err := s.Lookup(LookupRequest{Document: "lta/1998/2", Address: "§ 99"}); err == nil {
		t.Error("an address with no matching provision has to be an error, not an empty answer")
	} else if !strings.Contains(err.Error(), "nothing matches") {
		t.Errorf("unhelpful error for an unmatched address: %v", err)
	}
}

// Addressing has to work on recovered text as well as on the register's own, because
// most of the mirror has nothing else. A body that can be read but not cited is the
// failure the census counted 255,828 times, so this is the assertion that keeps the
// tool honest about what it can point at.
func TestLookupAddressesTextRecoveredFromHTML(t *testing.T) {
	s := newTestServer(t)
	res, err := s.Lookup(LookupRequest{Document: "lta/1999/1", Address: "§ 1, stk. 2"})
	if err != nil {
		t.Fatalf("an HTML-derived body has to be addressable: %v", err)
	}
	if res.Provenance != "html" {
		t.Errorf("provenance = %q, want html", res.Provenance)
	}
	if !strings.Contains(res.Text, "Tekst læst ud af HTML.") {
		t.Errorf("addressed text = %q", res.Text)
	}
	if strings.Contains(res.Text, "anvendelse") {
		t.Errorf("the stykke answer includes its paragraph's own text: %q", res.Text)
	}
}

func TestLookupRejectsWhatItCannotResolve(t *testing.T) {
	s := newTestServer(t)
	for _, tc := range []struct{ in, want string }{
		{"", "no document given"},
		{"lta/1998/999", "not in the mirror"},
		{"A19999999X", "not in the mirror"},
	} {
		_, err := s.Lookup(LookupRequest{Document: tc.in})
		if err == nil {
			t.Errorf("Lookup(%q) succeeded, want an error", tc.in)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Lookup(%q) error = %q, want it to mention %q", tc.in, err, tc.want)
		}
	}
}

// An answer larger than the caller's context is not a favour. The truncation has to
// hold the limit and stay renderable, which on Danish text means not slicing a å in
// half.
func TestLookupTruncatesWithoutBreakingText(t *testing.T) {
	s := newTestServer(t)
	long := strings.Repeat("Rædselsfuld sædelse. ", 4000)
	dir := s.archive.Root
	path := filepath.Join(dir, "lta", "1996", "4.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lta", "1996", "4.xml"),
		[]byte("<Dokument>"+meta("A19960004X", "Bekendtgørelse", "Lang", 1996, 4)+"</Dokument>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(sidecarWithHTML("<p>"+long+"</p>")), 0o644); err != nil {
		t.Fatal(err)
	}
	reopened, err := lexdania.Scan(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	s = New(reopened, "https://www.retsinformation.dk", "test")

	res, err := s.Lookup(LookupRequest{Document: "lta/1996/4", MaxChars: 101})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Error("a 101-character answer to a long document has to say it was truncated")
	}
	if len(res.Text) > 101 {
		t.Errorf("text length %d exceeds the requested 101", len(res.Text))
	}
	if !utf8.ValidString(res.Text) {
		t.Errorf("truncation split a multi-byte character: %q", res.Text[len(res.Text)-8:])
	}
	if !strings.Contains(res.String(), "truncated") {
		t.Error("the rendered answer does not mention the truncation")
	}
}

func TestLookupAsOfBuildsAConsolidatedView(t *testing.T) {
	s := newTestServer(t)
	res, err := s.Lookup(LookupRequest{Document: "lta/1998/2", AsOf: "2010-01-01"})
	if err != nil {
		t.Fatalf("as_of lookup: %v", err)
	}
	if res.AsOf != "2010-01-01" {
		t.Errorf("AsOf = %q, want the date back", res.AsOf)
	}
	if !strings.Contains(res.Text, "KANONISK TEKST") {
		t.Errorf("consolidated text = %q", res.Text)
	}
	// The view is a copy of the document, and provenance has to ride along on it.
	if res.Provenance != "xml" {
		t.Errorf("consolidated provenance = %q, want xml", res.Provenance)
	}
	if _, err := s.Lookup(LookupRequest{Document: "lta/1998/2", AsOf: "i går"}); err == nil {
		t.Error("a date the parser cannot read has to be an error, not an unconsolidated answer")
	}
}

// The rendered form is what a client's model actually reads.
func TestLookupResultRendersAnIdentifyingHeader(t *testing.T) {
	s := newTestServer(t)
	res, err := s.Lookup(LookupRequest{Document: "lta/1999/1", Address: "§ 1, stk. 2"})
	if err != nil {
		t.Fatal(err)
	}
	text := res.String()
	for _, want := range []string{
		"## ",                    // a heading, not a bare line: the consumer renders markdown
		"Text source:",           // label and value are checked apart so a renderer that
		"html",                   // drops either half cannot pass while still looking complete
		"Dataset:",               // the answer states which dates it speaks for
		"3 documents",            //
		"### Text",               //
		"Tekst læst ud af HTML.", // this fixture's provision is HTML-derived, so this is the text
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered answer is missing %q:\n%s", want, text)
		}
	}
	if strings.HasPrefix(text, "KANONISK") {
		t.Errorf("answer starts with raw statute text instead of a markdown heading:\n%s", text)
	}
	// The text is the model's own rendering, so it is shown as markdown rather than inside a
	// code fence: the answer exists to show which piece is a chapter and which a provision,
	// and a fence is how that used to be thrown away.
	if strings.Contains(text, "```") {
		t.Errorf("the model's markdown is fenced off from the client:\n%s", text)
	}
}

// Statute text that could close the answer's formatting still gets fenced. The rendering is
// markdown now, which makes this guard easier to lose and no less necessary: a law that quotes a
// code fence would otherwise leak the rest of itself into the answer as markup.
func TestLookupFencesTextThatCouldBreakOut(t *testing.T) {
	s := newTestServer(t)
	res, err := s.Lookup(LookupRequest{Document: "lta/1998/2"})
	if err != nil {
		t.Fatal(err)
	}
	res.Text = "§ 1. Tekst\n\n```\nif (a) { }\n```\n"
	rendered := res.String()
	if !strings.Contains(rendered, "````") {
		t.Errorf("text carrying a fence was not put in a longer one:\n%s", rendered)
	}
	if strings.Contains(textBlock("Stk. 1. Almindelig tekst."), "```") {
		t.Error("ordinary statute text is still being fenced")
	}
}

// The server's own status is a coverage claim, so the only time it may quote is the
// index file's build stamp. The first version of this test asked for the current
// year and passed because the server printed its own start time under the label
// "index built": a freshly started process looked like a freshly built index, which
// is the exact way a stale index claims to be current.
func TestServerStatus(t *testing.T) {
	s := newTestServer(t)
	i := mustIndex(t)
	i.Built = time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC).Unix()
	s.UseIndex(i)
	st := s.Status()
	if !strings.Contains(st, "3 documents") || !strings.Contains(st, "lta=3") {
		t.Errorf("Status = %q, want counts and media", st)
	}
	if !strings.Contains(st, "index built 2020-03-04") {
		t.Errorf("Status = %q, want the index file's own build stamp", st)
	}
	if strings.Contains(st, time.Now().Format("2006-01-02")) {
		t.Errorf("Status = %q quotes today, which is how a process start passes for an index time", st)
	}
	if got := newTestServer(t).Status(); !strings.Contains(got, "index built undated") {
		t.Errorf("a server with no index claims an index time: %q", got)
	}
}
