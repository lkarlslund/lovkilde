// Package docapi fetches the actual text of a legal document.
//
// Two representations are available and they serve different purposes:
//
//   - ELI accession XML (GET {site}/eli/accn/{accession}/xml): the machine-readable
//     canonical form, validated against a versioned published XSD
//     (http://www.retsinformation.dk/offentlig/xml/schemas/...). This is the
//     archive format we store for every document.
//   - Site document JSON (POST {site}/api/document/eli/{path}, body {}): the
//     representation the website itself uses. It carries the rendered documentHtml
//     plus rich structured metadata (title, ministry, classification, PDF
//     availability, editorial notes, references). It is fetched per document, not
//     per change: the body backfill (internal/crawl/bodies.go) requests it only for
//     documents whose canonical XML has no <DokumentIndhold>, and records the outcome
//     in Badger, so the volume is bounded by the corpus rather than by the update
//     rate. It is the site's own endpoint, not the third-party API, and does not
//     draw on that request budget.
package docapi

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	stdhtml "html"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/lkarlslund/retsinformation/internal/config"
	"github.com/lkarlslund/retsinformation/internal/limiter"
	"github.com/lkarlslund/retsinformation/internal/rihttp"
)

// API fetches documents from the site origin.
type API struct {
	site   string
	client *rihttp.Client
}

// New returns a document client.
func New(cfg config.Config, client *rihttp.Client) *API {
	return &API{site: strings.TrimSuffix(cfg.SiteBase, "/"), client: client}
}

// XMLURL returns the canonical ELI XML URL for an accession number.
func (a *API) XMLURL(accession string) string {
	return fmt.Sprintf("%s/eli/accn/%s/xml", a.site, accession)
}

// JSONURL returns the site's document API URL for an ELI resource path such as
// "eli/retsinfo/2026/10019".
func (a *API) JSONURL(eliPath string) string {
	return fmt.Sprintf("%s/api/document/%s", a.site, strings.TrimPrefix(eliPath, "/"))
}

// FetchXML downloads the canonical XML for an accession number.
func (a *API) FetchXML(ctx context.Context, accession string) ([]byte, error) {
	return a.client.GetJSON(ctx, limiter.BucketDocument, a.XMLURL(accession), "application/xml")
}

// FetchXMLURL downloads canonical XML from a fully-formed representation URL,
// which is how the sitemap-driven crawl requests documents: appending /xml to an
// ELI resource path serves the canonical document for every ELI kind.
func (a *API) FetchXMLURL(ctx context.Context, xmlURL string) ([]byte, error) {
	return a.client.GetJSON(ctx, limiter.BucketDocument, xmlURL, "application/xml")
}

// FetchJSON downloads the site's JSON representation for an ELI path.
func (a *API) FetchJSON(ctx context.Context, eliPath string) ([]byte, error) {
	return a.client.PostJSON(ctx, limiter.BucketDocument, a.JSONURL(eliPath), []byte("{}"))
}

// Document is the site's JSON representation of a document.
type Document struct {
	ID                       int    `json:"id"`
	AccessionNumber          string `json:"accessionNumber"`
	Title                    string `json:"title"`
	DocumentTypeID           string `json:"documentTypeId"`
	ShortName                string `json:"shortName"`
	DocumentHTML             string `json:"documentHtml"`
	IsHistorical             bool   `json:"isHistorical"`
	IsReprint                bool   `json:"isReprint"`
	Ressort                  string `json:"ressort"`
	GeografiskDaekningID     int    `json:"geografiskDaekningId"`
	RetsinfoKlassifikationID int    `json:"retsinfoKlassifikationId"`
	EditorialNotes           []any  `json:"editorialNotes"`
	AlternativeMedia         []any  `json:"alternativeMedia"`
	Metadata                 []struct {
		DisplayName  string `json:"displayName"`
		DisplayValue string `json:"displayValue"`
	} `json:"metadata"`
	PDFLink struct {
		IsPDFAvailable bool `json:"isPdfAvailable"`
	} `json:"pdfLink"`
}

// TextLen reports how much visible text a documentHtml payload carries, counting
// runes after tags are removed, entities decoded and whitespace collapsed.
//
// It exists to classify a payload without parsing it: the body backfill needs to
// know whether a document came back with prose or with nothing but a heading and a
// table shell, and it needs to record that number once per document so the census
// ("how many of the 29k body-less laws actually have text?") is a store scan rather
// than a re-read of gigabytes of payloads.
//
// Tags are stripped before entities are decoded, so a document that quotes markup
// as escaped text is counted as text rather than stripped again.
func TextLen(markup string) int {
	var b strings.Builder
	depth := 0
	for _, r := range markup {
		switch {
		case r == '<':
			depth++
		case depth > 0 && r == '>':
			depth--
			b.WriteRune(' ')
		case depth == 0:
			b.WriteRune(r)
		}
	}
	text := stdhtml.UnescapeString(b.String())
	// Zero-width and soft-hyphen marks are invisible, so they are not text. They
	// occur in this corpus (see internal/lexdania's matchRunes), and a count that
	// called them characters would over-report an empty document as having one.
	text = strings.NewReplacer("\u00ad", "", "\u200b", "", "\u200c", "", "\u200d", "", "\ufeff", "").Replace(text)
	// Runes, not bytes: Danish text is mostly multi-byte, and a count used to
	// classify documents has to mean the same thing in every language.
	return utf8.RuneCountInString(strings.Join(strings.Fields(text), " "))
}

// ParseJSON decodes the site document payload, which is a one-element array.
func ParseJSON(raw []byte) (*Document, error) {
	var docs []Document
	if err := json.Unmarshal(raw, &docs); err != nil {
		return nil, fmt.Errorf("decode document json: %w", err)
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("document json payload was empty")
	}
	return &docs[0], nil
}

// Meta is the subset of information we read out of the canonical XML to key the
// manifest. Field tags are LEAF names because the parent binds the Meta element:
// writing "Meta>AccessionNumber" here would make the decoder look for
// Dokument/Meta/Meta/AccessionNumber, and Go reports such a mismatch by leaving
// the field empty rather than returning an error.
type Meta struct {
	DocumentType string `xml:"DocumentType"`
	Rank         string `xml:"Rank"`
	Accession    string `xml:"AccessionNumber"`
	DocumentID   string `xml:"DocumentId"`
	Title        string `xml:"Titel"`
}

// ErrNotXML marks a payload that is an HTML page rather than canonical XML.
//
// Some catalogue URIs have no XML representation: requesting /xml on them answers
// HTTP 200 with the website's own page. That is a permanent property of the
// resource, not a transient fault, so callers must not spend the polite request
// budget retrying it. xml.Unmarshal alone cannot express the difference — it
// reports "expected element type <Dokument> but have <html>" the same way it
// would report a genuinely corrupt document.
var ErrNotXML = errors.New("server returned HTML where ELI XML was expected")

// IsHTMLPayload reports whether a response body is a web page rather than XML.
func IsHTMLPayload(raw []byte) bool {
	s := strings.ToLower(strings.TrimSpace(string(raw)))
	return strings.HasPrefix(s, "<!doctype html") || strings.HasPrefix(s, "<html")
}

// ParseXMLMeta extracts identity metadata from canonical ELI XML without
// validating the whole document against the XSD.
func ParseXMLMeta(raw []byte) (*Meta, error) {
	var m struct {
		XMLName xml.Name `xml:"Dokument"`
		Meta    Meta     `xml:"Meta"`
	}
	if IsHTMLPayload(raw) {
		return nil, fmt.Errorf("%w (%d bytes)", ErrNotXML, len(raw))
	}
	if err := xml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode eli xml: %w", err)
	}
	if m.Meta.Accession == "" {
		return nil, fmt.Errorf("eli xml has no AccessionNumber")
	}
	return &m.Meta, nil
}

var accnFromHref = regexp.MustCompile(`/eli/accn/([^/]+)`)

// AccessionFromHref extracts an accession number from an ELI accession URL such
// as https://www.retsinformation.dk/eli/accn/W20261001925/xml.
func AccessionFromHref(href string) string {
	m := accnFromHref.FindStringSubmatch(href)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// PathFromELIURL turns a canonical ELI resource URL into the path the site
// document API expects, e.g.
// https://www.retsinformation.dk/eli/retsinfo/2026/10019 -> eli/retsinfo/2026/10019
func PathFromELIURL(u string) string {
	i := strings.Index(u, "/eli/")
	if i < 0 {
		return ""
	}
	p := u[i+1:]
	// Strip any representation suffix (e.g. ".rdfa") and a trailing slash.
	p = strings.TrimSuffix(p, "/")
	if j := strings.LastIndex(p, "/"); j >= 0 {
		if dot := strings.LastIndex(p[j:], "."); dot > 0 {
			p = p[:j+dot]
		}
	}
	return p
}
