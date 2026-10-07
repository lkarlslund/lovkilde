// Package lexdoc is the mirror's own representation of a Danish legal document.
//
// It exists because everything above the parser has so far worked in the publisher's
// markup. A document was a tree of <Paragraf> and <Stk> nodes; a provision's number came
// from a localId attribute or was parsed out of an Explicatus element; entry-into-force
// material was modelled as an "appendix" only because the publisher nests it under an
// element whose numbering restarts; and the HTML reader synthesised XML element names so
// that downstream code would accept what it had read. Making the publisher's schema the
// mirror's data model costs three things, and all three have been paid:
//
//   - Every concept has to be re-derived on every read, because the stored shape is
//     markup and the meaning is a projection over it.
//   - The HTML reader has to forge XML to be understood, which is a mapping table plus a
//     standing invitation to confuse the two sources.
//   - The compiled dataset would have stored the publisher's vocabulary — 136 element
//     names, 34 attribute names, mixed-content interleaving — instead of the mirror's
//     understanding of the same document.
//
// So this package holds the concepts: what a document is, what its provisions are, when
// it says things take effect. lexdania stays what its name already implies, the reader of
// the register's XML, and hands over through Document.Model(). The HTML reader gets the
// same seam when it stops synthesising element names.
//
// Nothing in here carries publisher markup: no element names, no attribute maps, no raw
// payload. Where a fact could only be expressed as text — the document type as the
// register spelled it — it is kept as the fact, and the judgement derived from it is kept
// separately so the reasoning can move here without the data changing.
package lexdoc

import (
	"strconv"
	"time"
)

// Document is one legal document as the mirror understands it.
//
// Provision structure, inline content and amendments are added by the stages that follow;
// identity, provenance and diagnostics are here because every one of them is already
// needed by the reader, the index and the serving path.
type Document struct {
	Identity    Identity
	Provenance  Provenance
	Diagnostics Diagnostics

	// Content is what the document says: its provisions, in the mirror's own structure.
	Content Content

	// Changes is what this document does to other laws, when it amends any: its change lists read
	// into edits, each naming the law it is aimed at, and its own entry-into-force provisions. It is
	// read once, when the document is compiled into the artifact, from the register's change markup
	// where there is some and from the text where there is not; a law's state on a date is its own
	// model with the Changes of the acts its metadata names applied (see lexdania.Chains).
	Changes *Changes `json:",omitempty"`
}

// Changes is an amending document's patch: its edits, in the order it lists them, and when its
// parts take effect as its own provisions say.
type Changes struct {
	Edits   []Edit
	InForce InForce
}

// Identity is what a document is, as distinct from what it says.
type Identity struct {
	// Accession is the register's natural key, e.g. A2005005263LTA. It is the same
	// string in every representation, so it is a fact rather than markup even though
	// its encoding is the publisher's.
	Accession string

	// Identifier is Accession decoded. It is kept beside it rather than in its place,
	// because the corpus contains accessions that do not decode, and those have to be
	// reported rather than normalised away.
	Identifier Identifier

	// Medium, PublishedYear and PublishedNumber are how the register publishes the
	// document: the shelf (lta, ltb, ltc, mt, retsinfo, ft, fob) and the sequence on
	// it. A document read straight from a file does not know its shelf, so these are
	// filled by whoever located the document, and ELIPath stays empty until all three
	// are known. A wrong ELI is worse than none: it is quoted as a citation.
	Medium          string
	PublishedYear   int
	PublishedNumber int

	// Type is the register's own document type, spelled however it was spelled: a
	// Danish name ("Lovbekendtgørelse") or an internal code with the family glued to a
	// class letter ("BEK Æ#LOKDOK05"). Family and Class are the two judgements derived
	// from it, and both spellings have to survive the derivation or roughly a third of
	// amending acts go unnoticed.
	Type         string
	Family       string
	Class        string
	Title        string
	PopularTitle string

	Ministry             string
	Rank                 string
	JournalNumber        string
	ParliamentarySession string

	Status Status
	Dates  Dates

	Subjects     []string
	EUReferences []string

	// Concerns and AmendedBy are the version graph. They are facts about the document,
	// not about the archive: Concerns are the acts this document is about, and AmendedBy
	// are the later documents that changed this one and from when. Whether a cited act
	// is actually present in the mirror is a separate claim, held on the Cite.
	Concerns  []Cite
	AmendedBy []Cite

	// Validity is the periods during which the document itself was in force, which the
	// register attaches to its own anchors rather than to a field.
	Validity []Period

	// Amendment and Bill are judgements, not data: this document exists to change
	// another one, or it is a parliamentary proposal that never necessarily became law.
	// Consolidating a law must not apply a proposal, so the second one is load-bearing.
	Amendment bool
	Bill      bool
}

// Identifier is a decoded accession number.
type Identifier struct {
	Full string

	// Scheme is "accession" for the gazette encoding used by laws, orders, circulars
	// and rulings, "bill" for the parliamentary encoding, and "" when the value matched
	// neither. Both forms are 12 characters, which is why length cannot tell them apart.
	Scheme     string
	Rank       string
	Year       int
	Number     int
	TypeCode   string // kept as text: "R1" in G198050837R1 is not the number 1
	Session    string // bill scheme
	BillLetter string // bill scheme

	WellFormed bool
}

// Base returns the identifier without the two-character type code, which is what the
// same document is cited as elsewhere. Bill identifiers have no such suffix, and a
// malformed one is returned unchanged rather than truncated into a different document.
func (a Identifier) Base() string {
	if !a.WellFormed || a.Scheme == SchemeBill {
		return a.Full
	}
	return a.Full[:10]
}

// The two identifier encodings found in the register's accession numbers.
const (
	SchemeAccession = "accession"
	SchemeBill      = "bill"
)

// IsBill reports a parliamentary bill, report or motion rather than a promulgated act.
func (a Identifier) IsBill() bool { return a.Scheme == SchemeBill }

// Dates are the moments a legal document claims about itself. They are typed because
// almost everything that compares documents compares these; where the register wrote a
// date that does not parse, the failure is a diagnostic, not a zero date pretending to
// be a fact.
type Dates struct {
	Signed      time.Time // dies signi
	Promulgated time.Time // dies edicti
	AnnouncedIn string    // the medium it appeared in, e.g. "Lovtidende A"
}

// Cite is a link to another document with the facts that make the link usable.
type Cite struct {
	Accession string
	Effective time.Time
	Title     string

	// Present says whether the cited document was found in the mirror. It is filled by
	// whoever looked, not by the reader of one document: "the register says this act was
	// changed on this date" and "we have the changing act" are different claims, and a
	// consolidation has to be able to say which one it is standing on.
	Present bool
}

// Period is a span during which something was in force. An empty bound means the
// register did not say, not that it is unbounded.
type Period struct {
	From time.Time
	To   time.Time
	Note string
}

// Status is the register's own claim about whether the document still stands.
type Status string

const (
	StatusValid    Status = "Valid"
	StatusHistoric Status = "Historic"
	StatusUnknown  Status = ""
)

// IsHistoric reports the register's mark of repeal, expiry or supersession.
func (s Status) IsHistoric() bool { return s == StatusHistoric }

// Provenance says where the text of this document came from.
//
// The distinction is not decorative: most of the corpus carries its prose in the
// register's XML, but a large minority has an empty body in the XML and gets its text
// from the site's own HTML payload. A consolidated view assembled from both has to be
// able to say so, because the two representations have different failure modes and the
// reader deserves to know which one a provision came from.
type Provenance struct {
	Source Source

	// Path is the mirror's own file, filled by whoever located the document.
	Path    string
	Sidecar string

	// Schema is the versioned published schema the instance claims to validate
	// against. It is present on roughly a third of documents, so empty is normal.
	Schema string
}

// Source is where a document's text came from.
type Source string

const (
	SourceXML         Source = "xml"  // the register's canonical body
	SourceHTMLPayload Source = "html" // the site's rendered payload
	SourceNone        Source = "none" // metadata only, no text found in either
	SourceUnknown     Source = ""
)

// Diagnostics is everything the reader noticed that is not the document: projection
// problems, text it could not place, classes outside its mapping table.
//
// The rule this field enforces is that reading is allowed to be incomplete but never
// silently so. A document that parsed and lost something has to look different from a
// document that parsed completely, or "this document is handled" becomes an untestable
// claim.
type Diagnostics struct {
	Warnings []string
}

// PrimaryTitle is the title to show: the register's own, falling back to the popular
// one, which is the only title some acts carry.
func (id Identity) PrimaryTitle() string {
	if id.Title != "" {
		return id.Title
	}
	return id.PopularTitle
}

// IsAmendment reports whether the document exists to change another document.
func (id Identity) IsAmendment() bool { return id.Amendment }

// IsBill reports a parliamentary proposal rather than promulgated law.
func (id Identity) IsBill() bool { return id.Bill }

// IsHistoric reports the register's supersession mark.
func (id Identity) IsHistoric() bool { return id.Status.IsHistoric() }

// ELIPath is the register's own citation path for this document, or empty when the
// shelf or sequence is not known.
func (id Identity) ELIPath() string {
	if id.Medium == "" || id.PublishedYear == 0 || id.PublishedNumber == 0 {
		return ""
	}
	return "eli/" + id.Medium + "/" + strconv.Itoa(id.PublishedYear) + "/" + strconv.Itoa(id.PublishedNumber)
}
