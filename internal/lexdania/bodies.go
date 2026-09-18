package lexdania

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// SidecarExt is the extension of the site payload stored beside a stored document:
// data/xml/lta/1995/498.json next to data/xml/lta/1995/498.xml.
//
// It lives here rather than in the crawler because both layers need the same answer:
// the crawler writes the sidecar, and this package has to find it again when it opens
// the document. The dependency runs the other way (crawl imports lexdania), so a
// constant defined in the crawler would be unreachable from the reader.
const SidecarExt = ".json"

// TextSource records where the text of a document came from.
//
// The distinction is not cosmetic. Canonical <DokumentIndhold> is the register's own
// structured text; HTML-derived text is ours, recovered from the site's rendering by
// the reader in htmlbody.go, and it carries that reader's residual loss
// (internal/html-fidelity.md). A tool that quotes statutory text to a lawyer has to
// be able to say which of the two it is quoting, so the provenance travels with the
// document instead of being a property of the caller.
type TextSource string

const (
	// TextFromXML means the body is the document's own <DokumentIndhold>.
	TextFromXML TextSource = "xml"
	// TextFromHTML means the body was recovered from a sidecar payload.
	TextFromHTML TextSource = "html"
	// TextFromNone means the archive has metadata for this document but no text: not
	// every document has one, and claiming otherwise would make a missing body read as
	// an empty law rather than as a gap.
	TextFromNone TextSource = "none"
)

// SidecarPath is the path of the payload stored beside an XML document, or "" when the
// path could not have one.
func SidecarPath(path string) string {
	if !strings.HasSuffix(path, ".xml") {
		return ""
	}
	return strings.TrimSuffix(path, ".xml") + SidecarExt
}

// LoadWithBody reads a stored document and, when its XML carries no text, attaches the
// text recovered from the site payload stored beside it.
//
// Only about 2,100 of the 101,727 stored HTML bodies have a canonical body to check
// against, so the reader cannot be validated against ground truth on the rest and has
// to be trusted with its provenance visible. Documents whose XML does carry a body are
// never touched by the sidecar: the publisher's own structured text always wins.
func LoadWithBody(path string) (*Document, error) {
	doc, err := Load(path)
	if err != nil {
		return nil, err
	}
	doc.Source = TextFromXML
	if doc.hasXMLBody() {
		return doc, nil
	}
	doc.Source = TextFromNone

	sidecar := SidecarPath(path)
	if sidecar == "" {
		return doc, nil
	}
	raw, err := os.ReadFile(sidecar)
	if err != nil {
		// Absent sidecar is the ordinary case for the 89,667 documents with a body and
		// for anything the body queue has not reached: it is a gap, not a failure.
		return doc, nil
	}
	markup, err := PayloadMarkup(raw)
	if err != nil {
		doc.warnings = append(doc.warnings, fmt.Sprintf("sidecar %s: %v", sidecar, err))
		return doc, nil
	}
	body, err := ParseHTMLBody(markup)
	if err != nil {
		doc.warnings = append(doc.warnings, fmt.Sprintf("sidecar %s: %v", sidecar, err))
		return doc, nil
	}
	if body.Root == nil || strings.TrimSpace(body.Root.PlainText()) == "" {
		// The site answers with a heading and nothing else for some documents.
		doc.warnings = append(doc.warnings, fmt.Sprintf("sidecar %s carries no text", sidecar))
		return doc, nil
	}
	doc.attachBody(body.Root, sidecar)
	return doc, nil
}

// hasXMLBody reports whether the XML itself carries text. <DokumentIndhold> is the
// body element; other Dokument children (TitelGruppe, UnderskriftGruppe, Bilag) carry
// front matter, signatures and appendices, which are not the text of the law.
//
// The check has to walk the subtree. Text() joins only a node's own runs, so a body
// whose prose sits inside <Paragraf> and <Stk> children — which is every real body —
// would read as empty, and the sidecar would replace the register's own text.
func (d *Document) hasXMLBody() bool {
	body := d.Root.Elem("DokumentIndhold")
	return body != nil && strings.TrimSpace(body.PlainText()) != ""
}

// attachBody adds a recovered body to the document as though it had been parsed from
// the XML, so that every downstream projection sees one kind of body.
func (d *Document) attachBody(body *Node, sidecar string) {
	// A Node interleaves text and children: len(Runs) is len(Elems)+1, so appending a
	// child without the (empty) text run before it would break the invariant that
	// checkInvariant asserts, and the reader's own tests rely on.
	promotePrintedParagraphNumbers(body)
	d.Root.Elems = append(d.Root.Elems, body)
	d.Root.Runs = append(d.Root.Runs, "")
	d.Body = append(d.Body, body)
	d.Source = TextFromHTML
	d.Sidecar = sidecar
}

// PayloadMarkup extracts the documentHtml markup from a raw site payload.
//
// The crawler stores the whole response rather than the extracted field, because the
// rest of the payload (isHistorical, editorialNotes, alternativeMedia) is information
// that the XML does not carry. The response is an array holding one object, which is
// the shape the site itself consumes.
func PayloadMarkup(raw []byte) (string, error) {
	var docs []struct {
		DocumentHTML string `json:"documentHtml"`
	}
	if err := json.Unmarshal(raw, &docs); err != nil {
		return "", fmt.Errorf("payload is not the site's document array: %w", err)
	}
	if len(docs) == 0 {
		return "", fmt.Errorf("payload array is empty")
	}
	return docs[0].DocumentHTML, nil
}
