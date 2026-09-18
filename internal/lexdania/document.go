package lexdania

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Document is one canonical Lex Dania document.
//
// Root is the complete, losslessly-decoded tree. Everything else is a projection
// computed from it at parse time, so callers can use typed fields for the parts we
// understand and still reach Root for the parts we do not.
type Document struct {
	Root *Node

	// SchemaLocation is Dokument@SchemaLocation: the versioned published XSD this
	// instance claims to validate against. It is present on roughly a third of
	// documents (677 of 1,812 sampled), so an empty value is normal, not an error.
	SchemaLocation string

	// Meta is the register metadata block, which is where identity, status and the
	// version links live. The document body carries no identity information.
	Meta Meta

	// Body is every child of Dokument except Meta, in source order. Which elements
	// appear here depends on document type; the sampled corpus uses TitelGruppe,
	// DokumentIndhold, DokumentHoved, TitelGrupper, UnderskriftGruppe, Bilag,
	// Bemaerkninger, SkriftligFremsaettelse, Fremsaetter and Udkast.
	Body []*Node

	// Source is where the text of this document came from: its own XML, a sidecar
	// payload recovered by LoadWithBody, or nowhere. Documents parsed straight from
	// bytes have no sidecar to speak of, so Parse leaves this empty and only
	// LoadWithBody sets it.
	Source TextSource

	// Sidecar is the payload LoadWithBody recovered Body from, when Source is
	// TextFromHTML. It is kept so that a caller can show its work.
	Sidecar string

	warnings []string
}

// Load reads and parses a stored document.
func Load(path string) (*Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return doc, nil
}

func newDocument(root *Node) (*Document, error) {
	d := &Document{Root: root, SchemaLocation: root.Attrs["SchemaLocation"]}
	meta := root.Elem("Meta")
	if meta == nil {
		return nil, fmt.Errorf("lexdania: <Dokument> has no <Meta>, which every canonical document carries")
	}
	d.Meta = parseMeta(meta)
	for _, c := range root.Elems {
		if c.Name != "Meta" {
			d.Body = append(d.Body, c)
		}
	}
	if d.Meta.Accession == "" {
		return nil, fmt.Errorf("lexdania: document has no AccessionNumber")
	}
	d.warnings = append(d.warnings, d.Meta.Warnings...)
	if len(d.Meta.Rest) > 0 {
		names := make([]string, 0, len(d.Meta.Rest))
		for _, n := range d.Meta.Rest {
			names = append(names, n.Name)
		}
		d.warnings = append(d.warnings, "unmapped Meta children: "+strings.Join(names, ", "))
	}
	return d, nil
}

// Status is the register's own verdict on whether a document is current.
//
// This is the field that identifies current law. The two-digit suffix of an
// accession number is NOT a version counter, which was tested rather than assumed:
// 1,086 accession bases in the archive hold more than one accession, but 1,082 of
// those are ft/fob parliamentary items numbering each sitting, leaving only 4 law
// bases. "Highest suffix wins" is therefore not a valid way to find current law.
type Status string

const (
	StatusValid    Status = "Valid"
	StatusHistoric Status = "Historic"
	StatusUnknown  Status = ""
)

// IsHistoric reports whether the register marks the document as repealed, expired
// or otherwise superseded.
func (s Status) IsHistoric() bool { return s == StatusHistoric }

// Reference is one resolved anchor in Meta.
//
// The schema expresses links indirectly: an element carries id= to become an
// anchor, and sibling value elements carry REFid= to attach themselves to it:
//
//	<Concerns id="concerns_1"/>
//	<Ref_Accn REFid="concerns_1">A20050052630</Ref_Accn>
//	<Ref_Af REFid="concerns_1">2005-06-24</Ref_Af>
//	<Ref_Text REFid="concerns_1">Lov om fødevarer</Ref_Text>
//
// Resolving these is the single most valuable thing this package does, because the
// anchors are the version graph: Concerns means "this document concerns that law"
// and Change means "this later document amended me, effective Ref_Af".
type Reference struct {
	Kind string // anchor element name: Concerns, Change, DateOfSubmit, Sign, Sub-Sign, …
	ID   string // the id= value

	Accn      string // Ref_Accn: the accession of the referenced document
	Effective string // Ref_Af: the date the referenced document takes effect for this one
	Text      string // Ref_Text: the referenced document's title

	StartDate string // StartDate for this anchor (period of validity)
	EndDate   string // EndDate for this anchor

	Signature string // Signature for this anchor (signing authority)

	// Values holds any other value element attached to this anchor, keyed by
	// element name, so a value we have no field for is still reachable.
	Values map[string]string
}

// Meta is the register metadata block, with every child element observed in the
// corpus given a home. Fields are populated from Raw, which stays the lossless
// source.
//
// Two kinds of field appear below: scalars, and the reference layer. Repeated
// elements (Subject, EuReferences) become slices.
type Meta struct {
	Raw *Node

	DocumentType string // e.g. "Lovbekendtgørelse", "Ændringsbekendtgørelse", or a raw code
	Rank         string // rangering: A, B, C …  NOT the ELI medium
	Accession    string // AccessionNumber, the natural deduplication key
	DocumentID   string // DocumentId, e.g. "BG000194"
	UniqueID     string // UniqueDocumentId, e.g. "31483"
	Title        string // DocumentTitle — the element is DocumentTitle, not Titel
	PopularTitle string

	Year     int // year of signature
	Number   int // publication number within the year
	Ministry string
	Status   Status

	DiesSigni   string // date of signature
	DiesEdicti  string // date of promulgation
	AnnouncedIn string // the medium, e.g. "Lovtidende A"

	JournalNumber               string
	AdministrativeAuthority     string
	PlaceOfSignature            string
	DateOfHistoricMark          string
	NumberOfTheAct              string
	ParliamentarySession        string
	IntroducedBy                string
	SpokesmenDuringFirstReading string
	Voting                      string

	// StandingCommittees records which committee a bill was referred to, e.g.
	// "Efter 1.beh henvist til beh i Udvalget for Forretningsordenen (UFO)".
	// It appears on ~6 % of documents (all of them bills in ft/) and was initially
	// unmapped, which is how the corpus test surfaced it.
	StandingCommittees string

	// EditorialNote is an editorial remark, seen once in 2,145 sampled documents.
	EditorialNote string

	DateOfIntroduction  string
	DateOfFirstReading  string
	DateOfSecondReading string
	DateOfThirdReading  string

	ExceptedFromPublication string
	Republished             string
	RepublishedUniqueID     string // RepublishedUniqueDocumentId
	RepublishedHeader       string
	RepublishedNote         string

	Subjects     []string
	EUReferences []string // CELEX identifiers, e.g. 31988L0388

	// References is the resolved anchor layer, in document order.
	References []Reference

	// Rest holds Meta children we have no field for. It is not a junk drawer by
	// accident: a non-empty Rest is how a schema change becomes visible instead of
	// being silently dropped.
	Rest []*Node

	// Warnings records projection problems we could not represent, currently only
	// REFid= values with no matching anchor. Callers consolidating text should
	// surface these: a dropped reference can mean a dropped amendment.
	Warnings []string
}

// parseMeta projects the Meta element into typed fields and resolves the
// anchor/REFid indirection.
func parseMeta(meta *Node) Meta {
	m := Meta{Raw: meta}

	// Pass 1: anchors. Refs can only be attached once every anchor exists, because
	// nothing in the schema guarantees the anchor precedes its values (it happens to
	// do so in every document sampled, and the parser must not depend on that).
	anchors := map[string]int{}
	for _, c := range meta.Elems {
		if id, ok := c.Attr("id"); ok && id != "" {
			anchors[id] = len(m.References)
			m.References = append(m.References, Reference{Kind: c.Name, ID: id, Values: map[string]string{}})
		}
	}

	// Pass 2: values and scalars.
	for _, c := range meta.Elems {
		if refid, ok := c.Attr("REFid"); ok && refid != "" {
			i, found := anchors[refid]
			if !found {
				// A value pointing at a nonexistent anchor means our reference
				// layer is incomplete for this document. Say so instead of
				// dropping it, since dropping it would hide missing version links.
				m.Rest = append(m.Rest, c)
				m.Warnings = append(m.Warnings,
					fmt.Sprintf("%s REFid=%q has no matching anchor", c.Name, refid))
				continue
			}
			r := &m.References[i]
			if r.Values == nil {
				r.Values = map[string]string{}
			}
			v := c.Text()
			switch c.Name {
			case "Ref_Accn":
				r.Accn = v
			case "Ref_Af":
				r.Effective = v
			case "Ref_Text":
				r.Text = v
			case "StartDate":
				r.StartDate = v
			case "EndDate":
				r.EndDate = v
			case "Signature":
				r.Signature = v
			default:
				r.Values[c.Name] = v
			}
			continue
		}

		v := strings.TrimSpace(c.PlainText())
		switch c.Name {
		case "DocumentType":
			m.DocumentType = v
		case "Rank":
			m.Rank = v
		case "AccessionNumber":
			m.Accession = v
		case "DocumentId":
			m.DocumentID = v
		case "UniqueDocumentId":
			m.UniqueID = v
		case "DocumentTitle":
			m.Title = v
		case "PopularTitle":
			m.PopularTitle = v
		case "Year":
			m.Year = atoi(v)
		case "Number":
			m.Number = atoi(v)
		case "Ministry":
			m.Ministry = v
		case "Status":
			m.Status = Status(v)
		case "DiesSigni":
			m.DiesSigni = v
		case "DiesEdicti":
			m.DiesEdicti = v
		case "AnnouncedIn":
			m.AnnouncedIn = v
		case "JournalNumber":
			m.JournalNumber = v
		case "AdministrativeAuthority":
			m.AdministrativeAuthority = v
		case "PlaceOfSignature":
			m.PlaceOfSignature = v
		case "DateOfHistoricMark":
			m.DateOfHistoricMark = v
		case "NumberOfTheAct":
			m.NumberOfTheAct = v
		case "ParliamentarySession":
			m.ParliamentarySession = v
		case "IntroducedBy":
			m.IntroducedBy = v
		case "SpokesmenDuringFirstReading":
			m.SpokesmenDuringFirstReading = v
		case "Voting":
			m.Voting = v
		case "SubmittedToTheFollowingStandingCommittees":
			m.StandingCommittees = v
		case "EditorialNote":
			m.EditorialNote = v
		case "DateOfIntroduction":
			m.DateOfIntroduction = v
		case "DateOfFirstReading":
			m.DateOfFirstReading = v
		case "DateOfSecondReading":
			m.DateOfSecondReading = v
		case "DateOfThirdReading":
			m.DateOfThirdReading = v
		case "ExceptedFromPublication":
			m.ExceptedFromPublication = v
		case "Republished":
			m.Republished = v
		case "RepublishedUniqueDocumentId":
			m.RepublishedUniqueID = v
		case "RepublishedHeader":
			m.RepublishedHeader = v
		case "RepublishedNote":
			m.RepublishedNote = v
		case "Subject":
			m.Subjects = append(m.Subjects, v)
		case "EuReferences":
			m.EUReferences = append(m.EUReferences, v)
		default:
			// An anchor element carries no value of its own and is already
			// represented in References, so it must not be counted as unmapped.
			if _, isAnchor := anchors[c.Attrs["id"]]; isAnchor {
				continue
			}
			m.Rest = append(m.Rest, c)
		}
	}

	// A Change or Concerns anchor with no Ref_Accn is a broken link: the register
	// said a relationship exists but did not say to which document. Staying quiet
	// here would truncate version chains without leaving any trace.
	for _, r := range m.References {
		if (r.Kind == "Change" || r.Kind == "Concerns") && r.Accn == "" {
			m.Warnings = append(m.Warnings,
				fmt.Sprintf("%s anchor %q has no Ref_Accn", r.Kind, r.ID))
		}
	}
	return m
}

// Concerns returns the laws or acts this document concerns — normally the law an
// amending act is making changes to.
//
// Coverage is genuinely partial and that is expected: measured on 70 sampled
// amending acts, only 1 declared a Concerns anchor, while about 22 % of laws carry
// a Change anchor naming the act that amended them. Build version chains from
// Changes, not from Concerns.
func (m *Meta) Concerns() []Reference { return m.refsOfKind("Concerns") }

// Changes returns the later documents that amended this one, each with the date it
// takes effect. This is the authoritative forward edge of the version chain.
func (m *Meta) Changes() []Reference { return m.refsOfKind("Change") }

// Submissions returns the document's own periods of validity, which the schema
// attaches to a DateOfSubmit anchor rather than to the document directly. A
// document can carry more than one period.
func (m *Meta) Submissions() []Reference { return m.refsOfKind("DateOfSubmit") }

func (m *Meta) refsOfKind(kind string) []Reference {
	var out []Reference
	for _, r := range m.References {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// AccessionInfo is a parsed accession number.
//
// The shape is one rank letter, a four-digit year, a five-digit publication number
// and a two-digit type code: A20250033129 is rank A, year 2025, number 331, code 29.
//
// Note what cannot be derived from it. The ELI medium (lta, ltb, retsinfo, …) is
// not the rank: B-ranked bekendtgørelser live under the lta/ path because their
// AnnouncedIn medium is Lovtidende A, and Rank B is a Retsinformation rangering.
// Guessing a path from the accession therefore produces wrong answers, which is why
// Archive resolves accessions by indexing files instead.
type AccessionInfo struct {
	Full string
	// Scheme distinguishes the two identifier encodings found in <AccessionNumber>.
	// "accession" is the canonical gazette id used by laws, orders, circulars and
	// rulings; "bill" is the parliamentary bill id used by ft/ documents.
	// "" means the value matched neither and is left as-is.
	Scheme string
	Rank   string
	Year   int
	Number int
	// TypeCode is the two-character gazette suffix. It is kept as text because it is
	// not always numeric: retsinfo documents carry alphanumeric codes such as the
	// "R1" in G198050837R1, which a numeric parse silently turned into 1.
	TypeCode   string
	Session    string // bill scheme: Folketing session the bill was tabled in
	BillLetter string // bill scheme: bill series letter, e.g. "L", "K", "CB", "XX"

	WellFormed bool
}

// The identifier encodings found in <AccessionNumber>. Both are always 12
// characters, which is why a length check alone cannot tell them apart.
//
//	accession: rank + year(4) + number(5) + type code(2)
//	           A18521100030, and the code may be alphanumeric: G198050837R1
//	bill:      year(4) + session(1-2) + series(1-2) + number(4-5)
//	           198512K00032, 19531CB00161, 200423LA0079
//
// The widths inside a bill id are not fixed, so only the boundaries that the data
// actually shows are claimed. The bill form was found by the corpus test: every ft/
// document (Lovforslag, Betænkning, Beslutningsforslag) uses it, and rejecting it as
// malformed made an entire document class look corrupt.
const (
	SchemeAccession = "accession"
	SchemeBill      = "bill"
)

var (
	reCanonicalAccession = regexp.MustCompile(`^([A-Z])(\d{4})(\d{5})([A-Z0-9]\d)$`)
	reBillID             = regexp.MustCompile(`^(\d{4})(\d{1,2})([A-Z]{1,2})(\d{4,5})$`)
)

// ParseAccession decodes an accession number without failing on odd input, because
// the corpus is data and a malformed accession should be reported, not fatal.
func ParseAccession(s string) AccessionInfo {
	a := AccessionInfo{Full: s}
	if m := reCanonicalAccession.FindStringSubmatch(s); m != nil {

		a.Scheme = SchemeAccession
		a.Rank = m[1]
		a.Year = atoi(m[2])
		a.Number = atoi(m[3])
		a.TypeCode = m[4]
		a.WellFormed = true
		return a
	}
	if m := reBillID.FindStringSubmatch(s); m != nil {
		a.Scheme = SchemeBill
		a.Year = atoi(m[1])
		a.Session = m[2]
		a.BillLetter = m[3]
		a.Number = atoi(m[4])
		a.WellFormed = true
		return a
	}
	return a
}

// IsBill reports whether this identifier names a parliamentary bill or report
// rather than a promulgated act. Bills propose changes but are not themselves law,
// so a consolidator must never apply one.
func (a AccessionInfo) IsBill() bool { return a.Scheme == SchemeBill }

// Base returns the accession with its type code removed. It is a grouping key only:
// see AccessionInfo for why the suffix is not a version number. Bill ids carry no
// type code, so a bill groups with itself.
func (a AccessionInfo) Base() string {
	if !a.WellFormed || a.Scheme == SchemeBill {
		return a.Full
	}
	return a.Full[:10]
}

// Warnings returns everything questionable found while projecting this document,
// combining Meta's findings with the document-level ones.
func (d *Document) Warnings() []string { return d.warnings }

func (d *Document) Accession() AccessionInfo { return ParseAccession(d.Meta.Accession) }

// Title returns the document title, falling back to the popular title.
func (d *Document) Title() string {
	if d.Meta.Title != "" {
		return d.Meta.Title
	}
	return d.Meta.PopularTitle
}

// TypeFamily returns the leading document-family token of DocumentType, e.g. LBK,
// BEK, LOV or KEN.
//
// DocumentType is inconsistent in the source: it holds either a Danish name
// ("Lovbekendtgørelse", "Ændringsbekendtgørelse") or a raw internal code with the
// family glued to a class code ("BEK Æ#LOKDOK05", "LOV Æ#LOKDOK02", "KEN#LOKDOK23").
// Both forms have to be handled or amending acts are missed entirely — a filter on
// the substring "ændr" silently skips every "BEK Æ#LOKDOK05" document, which is
// roughly a third of the amending acts in lta.
func (d *Document) TypeFamily() string {
	family, _ := splitCode(d.Meta.DocumentType)
	return family
}

// TypeClass returns the single-letter class from a code-style DocumentType: "Æ" for
// amendments, "H" for the main/head document, "I" for entry-into-force documents.
// It returns "" for documents whose type is a name rather than a code.
func (d *Document) TypeClass() string {
	_, code := splitCode(d.Meta.DocumentType)
	return code
}

// IsAmendment reports whether this document's purpose is to change another
// document, judged from both spellings of the type.
func (d *Document) IsAmendment() bool {
	if d.TypeClass() == "Æ" {
		return true
	}
	return strings.Contains(strings.ToLower(d.Meta.DocumentType), "ændr")
}

// IsBill reports whether this is a parliamentary bill, report or motion rather than
// a promulgated act: Lovforslag, Betænkning, Beslutningsforslag and their LSV/LSF/
// BSF/BTL/SF code spellings. Bills contain proposed amendment text and are archived
// alongside the acts they became, so a consolidator that trusts DocumentType alone
// would apply a proposal that never became law.
//
// Two independent signals are used because DocumentType is spelled inconsistently:
// the bill identifier scheme (measured: every ft/ document sampled carries one) and
// the type name or parliament series code.
func (d *Document) IsBill() bool {
	if d.Accession().IsBill() {
		return true
	}
	t := strings.ToLower(d.Meta.DocumentType)
	if strings.Contains(t, "forslag") || strings.Contains(t, "betænkning") {
		return true
	}
	family, _ := splitCode(d.Meta.DocumentType)
	switch strings.ToUpper(strings.TrimSuffix(family, ".L")) {
	case "LSV", "LSF", "BSF", "BTL", "SF":
		return true
	}
	return false
}

// splitCode separates "BEK Æ#LOKDOK05" into "BEK" and "Æ", and reports the name
// form unchanged.
func splitCode(t string) (family, class string) {
	i := strings.IndexByte(t, ' ')
	if i < 0 {
		return t, ""
	}
	family = t[:i]
	code := strings.TrimSpace(t[i+1:])
	if j := strings.Index(code, "#"); j >= 0 {
		return family, code[:j]
	}
	return family, code
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// ErrNotXML marks bytes that are an HTML page rather than canonical XML. Some
// catalogue URIs answer HTTP 200 with the website's own page when /xml is
// requested, and a crawler must record that as a permanent property of the
// resource rather than a parse failure worth retrying.
var ErrNotXML = errors.New("payload is HTML, not ELI XML")

// IsHTMLPayload reports whether a body is a web page rather than XML.
func IsHTMLPayload(raw []byte) bool {
	s := strings.ToLower(strings.TrimSpace(string(raw)))
	return strings.HasPrefix(s, "<!doctype html") || strings.HasPrefix(s, "<html")
}

func bytesReader(data []byte) *bytes.Reader { return bytes.NewReader(data) }
