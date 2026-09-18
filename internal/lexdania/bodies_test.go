package lexdania

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The loader rule these tests exist to keep is narrow: the register's own text always
// wins, a sidecar only ever fills a document whose XML has no text, and whatever ends
// up in the tree says which of the two it is.

const stubXML = `<Dokument><Meta><AccessionNumber>A19990001X</AccessionNumber>` +
	`<Year>1999</Year><Number>1</Number><DocumentType>Bekendtgørelse</DocumentType>` +
	`<Status>Gældende</Status><DocumentTitle>Testbekendtgørelse</DocumentTitle></Meta></Dokument>`

const bodyXML = `<Dokument><Meta><AccessionNumber>A19990001X</AccessionNumber>` +
	`<Year>1999</Year><Number>1</Number><DocumentType>Bekendtgørelse</DocumentType>` +
	`<Status>Gældende</Status><DocumentTitle>Testbekendtgørelse</DocumentTitle></Meta>` +
	`<DokumentIndhold><Rubrica>KANONISK TEKST</Rubrica></DokumentIndhold></Dokument>`

// sidecar wraps markup in the shape the site answers with, because that is what the
// crawler stores: the whole payload, not the extracted field.
func sidecar(t *testing.T, markup string) string {
	raw, err := json.Marshal([]struct {
		DocumentHTML string `json:"documentHtml"`
	}{{DocumentHTML: markup}})
	if err != nil {
		t.Fatalf("build sidecar: %v", err)
	}
	return string(raw)
}

// writePair lays out a stored document and its payload the way the archive does.
func writePair(t *testing.T, xmlBody, markup string) string {
	dir := t.TempDir()
	path := filepath.Join(dir, "lta", "1999", "1.xml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(xmlBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if markup != "" {
		if err := os.WriteFile(strings.TrimSuffix(path, ".xml")+".json", []byte(markup), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestSidecarPath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"data/xml/lta/1995/498.xml", "data/xml/lta/1995/498.json"},
		{"data/xml/lta/1995/498", ""},
		{"data/xml/lta/1995/498.json", ""},
		{"", ""},
	} {
		if got := SidecarPath(tc.in); got != tc.want {
			t.Errorf("SidecarPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLoadWithBodyAttachesSidecarText(t *testing.T) {
	path := writePair(t, stubXML, sidecar(t, `<p class="Paragraf">§ 1.</p><p class="Stk2">Stk. 1. Teksten fra HTML.</p>`))
	doc, err := LoadWithBody(path)
	if err != nil {
		t.Fatalf("LoadWithBody: %v", err)
	}
	if doc.Source != TextFromHTML {
		t.Errorf("Source = %q, want %q", doc.Source, TextFromHTML)
	}
	if doc.Sidecar != path[:len(path)-len(".xml")]+".json" {
		t.Errorf("Sidecar = %q, want the payload path", doc.Sidecar)
	}
	if got := strings.Join(strings.Fields(doc.Root.PlainText()), " "); !strings.Contains(got, "Teksten fra HTML.") {
		t.Errorf("sidecar text did not reach the tree: %q", got)
	}
	if err := doc.Root.checkInvariant(path); err != nil {
		t.Errorf("attaching a body broke the mixed-content invariant: %v", err)
	}
	// The point of attaching at all: the recovered text has to be addressable, not
	// merely present.
	var sections []*Section
	for _, s := range doc.Sections() {
		if strings.Contains(s.Node.Text(), "Teksten fra HTML.") {
			sections = append(sections, s)
		}
	}
	if len(sections) == 0 {
		t.Errorf("recovered text produced no section, so it cannot be addressed\ntree:\n%s",
			dumpNode(doc.Root, ""))
	}
	if len(doc.Warnings()) != 0 {
		t.Errorf("a clean attach reported warnings: %v", doc.Warnings())
	}
	// Body is the projection most callers reach for, so the recovered body has to be
	// in it and not only somewhere under Root.
	if len(doc.Body) != 1 || doc.Body[0].Name != "DokumentIndhold" {
		t.Errorf("Body = %+v, want the one recovered body", doc.Body)
	}
}

// An empty body element is a formatting artefact, not the register's text. Refusing
// the payload there would report a document with no text at all.
func TestWhitespaceOnlyBodyDoesNotBlockTheSidecar(t *testing.T) {
	whitespace := strings.Replace(bodyXML, "<Rubrica>KANONISK TEKST</Rubrica>", "  \n\t ", 1)
	path := writePair(t, whitespace, sidecar(t, `<p>Tekst fra HTML.</p>`))
	doc, err := LoadWithBody(path)
	if err != nil {
		t.Fatalf("LoadWithBody: %v", err)
	}
	if doc.Source != TextFromHTML {
		t.Errorf("Source = %q, want %q: an empty <DokumentIndhold> is not text", doc.Source, TextFromHTML)
	}
	if !strings.Contains(doc.Root.PlainText(), "Tekst fra HTML.") {
		t.Errorf("the payload was refused: %q", doc.Root.PlainText())
	}
}

// The register's own text outranks anything we recovered. Without this rule a document
// would silently swap its authoritative text for ours the moment a payload existed
// beside it, which is exactly the failure mode the whole pipeline is built to avoid.
func TestCanonicalBodyIsNeverReplacedByASidecar(t *testing.T) {
	path := writePair(t, bodyXML, sidecar(t, `<p class="Paragraf">§ 1.</p><p>Tekst fra HTML</p>`))
	doc, err := LoadWithBody(path)
	if err != nil {
		t.Fatalf("LoadWithBody: %v", err)
	}
	if doc.Source != TextFromXML {
		t.Errorf("Source = %q, want %q: canonical text outranks a payload", doc.Source, TextFromXML)
	}
	if doc.Sidecar != "" {
		t.Errorf("Sidecar = %q, want empty: the payload must not be read at all", doc.Sidecar)
	}
	got := strings.Join(strings.Fields(doc.Root.PlainText()), " ")
	if !strings.Contains(got, "KANONISK TEKST") {
		t.Errorf("canonical body disappeared: %q", got)
	}
	if strings.Contains(got, "Tekst fra HTML") {
		t.Errorf("HTML text was attached alongside the canonical body: %q", got)
	}
	if n := len(doc.Root.ElemsNamed("DokumentIndhold")); n != 1 {
		t.Errorf("DokumentIndhold count = %d, want 1\ntree:\n%s", n, dumpNode(doc.Root, ""))
	}
}

// A document with neither a body nor a payload is a gap, and has to stay one: the
// archive holds thousands of metadata-only records, and a caller must be able to tell
// "no text exists here" from "text exists and is empty".
func TestLoadWithBodyWithoutSidecarStaysTextless(t *testing.T) {
	path := writePair(t, stubXML, "")
	doc, err := LoadWithBody(path)
	if err != nil {
		t.Fatalf("a missing payload is not a load failure: %v", err)
	}
	if doc.Source != TextFromNone {
		t.Errorf("Source = %q, want %q", doc.Source, TextFromNone)
	}
	if len(doc.Warnings()) != 0 {
		t.Errorf("a missing payload produced warnings: %v", doc.Warnings())
	}
	if doc.Meta.Accession != "A19990001X" {
		t.Errorf("Meta lost while looking for a body: %+v", doc.Meta)
	}
}

// A payload that cannot be read is reported on the document and leaves it metadata-only.
func TestLoadWithBodyUndecodableSidecar(t *testing.T) {
	path := writePair(t, stubXML, `this is not the site's array`)
	doc, err := LoadWithBody(path)
	if err != nil {
		t.Fatalf("a broken payload must not fail the document: %v", err)
	}
	if doc.Source != TextFromNone {
		t.Errorf("Source = %q, want %q", doc.Source, TextFromNone)
	}
	var found bool
	for _, w := range doc.Warnings() {
		if strings.Contains(w, ".json") {
			found = true
		}
	}
	if !found {
		t.Errorf("the unreadable payload is not named in the warnings: %v", doc.Warnings())
	}
}

// The site answers with a heading and nothing else for some documents; that is the
// document's state, not a body we recovered.
func TestLoadWithBodySidecarWithoutText(t *testing.T) {
	path := writePair(t, stubXML, sidecar(t, `<div class="sitebanner"></div><p>   </p>`))
	doc, err := LoadWithBody(path)
	if err != nil {
		t.Fatalf("LoadWithBody: %v", err)
	}
	if doc.Source != TextFromNone {
		t.Errorf("Source = %q, want %q for a payload carrying no prose", doc.Source, TextFromNone)
	}
	if len(doc.Warnings()) == 0 {
		t.Error("an empty payload has to be reported, or it reads as a document with no text anywhere")
	}
}

// Provenance has to survive the copy every consolidated view is built from.
func TestCloneKeepsTextProvenance(t *testing.T) {
	path := writePair(t, stubXML, sidecar(t, `<p>Tekst.</p>`))
	doc, err := LoadWithBody(path)
	if err != nil {
		t.Fatal(err)
	}
	clone := doc.Clone()
	if clone.Source != doc.Source || clone.Sidecar != doc.Sidecar {
		t.Errorf("clone lost provenance: %q %q vs %q %q", clone.Source, clone.Sidecar, doc.Source, doc.Sidecar)
	}
}

// Archive.Open is the seam the consolidation engine and any lookup tool use, so the
// pairing has to happen there and not only for direct callers.
func TestArchiveOpenPairsSidecarText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lta", "1999", "1.xml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(stubXML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lta", "1999", "1.json"),
		[]byte(sidecar(t, `<p class="Paragraf">§ 1.</p><p>Åbnet via arkivet.</p>`)), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := Scan(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	doc, _, ok := a.Open("A19990001X")
	if !ok {
		t.Fatal("Open could not find the document it indexed")
	}
	if doc.Source != TextFromHTML {
		t.Errorf("Source = %q, want %q: Open has to pair the sidecar", doc.Source, TextFromHTML)
	}
	if !strings.Contains(doc.Root.PlainText(), "Åbnet via arkivet.") {
		t.Errorf("Open returned metadata without the text beside it:\n%s", dumpNode(doc.Root, ""))
	}
}

func TestPayloadMarkup(t *testing.T) {
	got, err := PayloadMarkup([]byte(`[{"documentHtml":"<p>x</p>"}]`))
	if err != nil || got != "<p>x</p>" {
		t.Errorf("PayloadMarkup = %q, %v; want the markup", got, err)
	}
	if _, err := PayloadMarkup([]byte(`[]`)); err == nil {
		t.Error("an empty payload array has to be an error, not empty markup")
	}
	if _, err := PayloadMarkup([]byte(`{"documentHtml":"<p>x</p>"}`)); err == nil {
		t.Error("an object where the site answers with an array has to be an error")
	}
	got, err = PayloadMarkup([]byte(`[{"id":7}]`))
	if err != nil || got != "" {
		t.Errorf("PayloadMarkup without the field = %q, %v; want empty and no error", got, err)
	}
}
