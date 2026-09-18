package lexdania

import (
	"testing"

	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// TestModelIdentity checks the conversion on a document whose contents are known, so a
// corpus test cannot be the only thing holding the mapping together.
func TestModelIdentity(t *testing.T) {
	doc := mustParse(t, fixtureLaw)
	m := doc.Model()

	if m.Identity.Accession != "A20050000129" {
		t.Errorf("Accession = %q", m.Identity.Accession)
	}
	if got, want := m.Identity.PrimaryTitle(), doc.Title(); got != want {
		t.Errorf("PrimaryTitle = %q, want %q", got, want)
	}
	if !m.Identity.IsHistoric() {
		t.Error("the fixture is marked Historic and the model lost it")
	}
	if m.Identity.Type != doc.Meta.DocumentType || m.Identity.Family != doc.TypeFamily() || m.Identity.Class != doc.TypeClass() {
		t.Errorf("type triple = %q/%q/%q, want %q/%q/%q",
			m.Identity.Type, m.Identity.Family, m.Identity.Class,
			doc.Meta.DocumentType, doc.TypeFamily(), doc.TypeClass())
	}
	if m.Identity.IsAmendment() != doc.IsAmendment() || m.Identity.IsBill() != doc.IsBill() {
		t.Errorf("judgements = amendment %v bill %v, want %v/%v",
			m.Identity.IsAmendment(), m.Identity.IsBill(), doc.IsAmendment(), doc.IsBill())
	}
	if m.Provenance.Schema != doc.SchemaLocation {
		t.Errorf("Schema = %q, want %q", m.Provenance.Schema, doc.SchemaLocation)
	}
	if m.Provenance.Source != lexdoc.SourceUnknown && m.Provenance.Source != lexdoc.SourceXML {
		t.Errorf("Source = %q for a document parsed straight from XML", m.Provenance.Source)
	}

	// A shelf the reader cannot know must stay unknown: inventing an ELI path would put
	// a wrong citation in a rendered answer.
	if m.Identity.ELIPath() != "" {
		t.Errorf("ELIPath = %q for a document whose shelf nobody stated", m.Identity.ELIPath())
	}
	m.Identity.Medium = "lta"
	m.Identity.PublishedYear, m.Identity.PublishedNumber = 2005, 129
	if got, want := m.Identity.ELIPath(), "eli/lta/2005/129"; got != want {
		t.Errorf("ELIPath = %q, want %q", got, want)
	}

	if len(m.Diagnostics.Warnings) < len(doc.Warnings()) {
		t.Errorf("the model dropped warnings: %d, the reader raised %d", len(m.Diagnostics.Warnings), len(doc.Warnings()))
	}
}

// TestModelUnreadableDateIsReported is the reason dates are typed: a zero time is a
// confident wrong fact, and a chain sorted on it silently puts versions in the wrong order.
func TestModelUnreadableDateIsReported(t *testing.T) {
	doc := mustParse(t, fixtureLaw)
	doc.Meta.DiesSigni = "2005-13-45"
	m := doc.Model()
	if !m.Identity.Dates.Signed.IsZero() {
		t.Errorf("an unparseable signature date became %v", m.Identity.Dates.Signed)
	}
	var found bool
	for _, w := range m.Diagnostics.Warnings {
		if w == "signature date 2005-13-45 does not parse" {
			found = true
		}
	}
	if !found {
		t.Errorf("the unreadable date was not reported; warnings = %v", m.Diagnostics.Warnings)
	}
}

// TestModelParityOnCorpus is the migration guarantee: for every field the model claims
// today, it must agree with the reader it was built from. It is deliberately a comparison
// against the old path rather than against expectations, because during a migration the
// old path is the definition of correct; once the judgements move into lexdoc this test
// becomes the thing that proves the move changed nothing.
func TestModelParityOnCorpus(t *testing.T) {
	dir := corpusDir(t)
	files := sampleFiles(t, dir, 400)

	var parsed, amended, bills, historic, withCites, withWarnings int
	for _, f := range files {
		doc, err := Load(f)
		if err != nil {
			continue // a concurrently-written archive can catch a half-written file
		}
		parsed++
		m := doc.Model()

		if m.Identity.Accession != doc.Meta.Accession {
			t.Errorf("%s: accession %q, want %q", f, m.Identity.Accession, doc.Meta.Accession)
		}
		if m.Identity.PrimaryTitle() != doc.Title() {
			t.Errorf("%s: title %q, want %q", f, m.Identity.PrimaryTitle(), doc.Title())
		}
		if m.Identity.Type != doc.Meta.DocumentType {
			t.Errorf("%s: type %q, want %q", f, m.Identity.Type, doc.Meta.DocumentType)
		}
		if m.Identity.Family != doc.TypeFamily() || m.Identity.Class != doc.TypeClass() {
			t.Errorf("%s: family/class %q/%q, want %q/%q", f,
				m.Identity.Family, m.Identity.Class, doc.TypeFamily(), doc.TypeClass())
		}
		if m.Identity.IsAmendment() != doc.IsAmendment() {
			t.Errorf("%s: amendment judgement disagrees", f)
		}
		if m.Identity.IsBill() != doc.IsBill() {
			t.Errorf("%s: bill judgement disagrees", f)
		}
		if m.Identity.Status != lexdoc.Status(doc.Meta.Status) {
			t.Errorf("%s: status %q, want %q", f, m.Identity.Status, doc.Meta.Status)
		}
		if m.Provenance.Schema != doc.SchemaLocation || m.Provenance.Sidecar != doc.Sidecar {
			t.Errorf("%s: provenance lost schema or sidecar", f)
		}
		if got, want := len(m.Identity.Concerns), len(doc.Meta.Concerns()); got != want {
			t.Errorf("%s: %d concerns, want %d", f, got, want)
		}
		if got, want := len(m.Identity.AmendedBy), len(doc.Meta.Changes()); got != want {
			t.Errorf("%s: %d amendments known, want %d", f, got, want)
		}
		if got, want := len(m.Diagnostics.Warnings), len(doc.Warnings()); got < want {
			t.Errorf("%s: model carries %d warnings, reader raised %d", f, got, want)
		}

		if m.Identity.IsAmendment() {
			amended++
		}
		if m.Identity.IsBill() {
			bills++
		}
		if m.Identity.IsHistoric() {
			historic++
		}
		if len(m.Identity.Concerns) > 0 || len(m.Identity.AmendedBy) > 0 {
			withCites++
		}
		if len(m.Diagnostics.Warnings) > 0 {
			withWarnings++
		}
	}

	if parsed < 100 {
		t.Fatalf("only %d of %d sampled documents parsed; the comparison is not meaningful", parsed, len(files))
	}
	t.Logf("compared %d documents: %d amendments, %d bills, %d historic, %d with version links, %d with diagnostics",
		parsed, amended, bills, historic, withCites, withWarnings)
}
