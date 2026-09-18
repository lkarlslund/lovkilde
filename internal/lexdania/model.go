package lexdania

import "github.com/lkarlslund/retsinformation/internal/lexdoc"

// Model converts a parsed document into the mirror's own representation.
//
// This is the seam between the register's markup and the mirror's concepts, and it is
// deliberately translation-only: every judgement here (document family, whether it is an
// amendment, whether it is a bill) is produced by the code that already produces it, so
// the two representations cannot disagree while both are in use. Moving those judgements
// into lexdoc is the point of the following stages; until then, one implementation and
// one adapter is what keeps a parity test honest rather than circular.
//
// Two fields are left empty on purpose. Identity.Medium and its publication sequence
// belong to whoever located the document — a parsed file does not know its shelf — and
// Cite.Present is a claim about the archive, not about this document. Both are filled by
// the archive-level conversion, which is the only place that knows them.
func (d *Document) Model() *lexdoc.Document {
	acc := d.Accession()
	out := &lexdoc.Document{
		Identity: lexdoc.Identity{
			Accession: d.Meta.Accession,
			Identifier: lexdoc.Identifier{
				Full:       acc.Full,
				Scheme:     acc.Scheme,
				Rank:       acc.Rank,
				Year:       acc.Year,
				Number:     acc.Number,
				TypeCode:   acc.TypeCode,
				Session:    acc.Session,
				BillLetter: acc.BillLetter,
				WellFormed: acc.WellFormed,
			},
			Type:                 d.Meta.DocumentType,
			Family:               d.TypeFamily(),
			Class:                d.TypeClass(),
			Title:                d.Meta.Title,
			PopularTitle:         d.Meta.PopularTitle,
			Ministry:             d.Meta.Ministry,
			Rank:                 d.Meta.Rank,
			JournalNumber:        d.Meta.JournalNumber,
			ParliamentarySession: d.Meta.ParliamentarySession,
			Status:               lexdoc.Status(d.Meta.Status),
			Subjects:             d.Meta.Subjects,
			EUReferences:         d.Meta.EUReferences,
			Concerns:             cites(d.Meta.Concerns()),
			AmendedBy:            cites(d.Meta.Changes()),
			Validity:             periods(d.Meta.Submissions()),
			Amendment:            d.IsAmendment(),
			Bill:                 d.IsBill(),
		},
		Provenance: lexdoc.Provenance{
			Source:  modelSource(d.Source),
			Sidecar: d.Sidecar,
			Schema:  d.SchemaLocation,
		},
		Diagnostics: lexdoc.Diagnostics{Warnings: d.Warnings()},
	}
	out.Content = contentOf(d.Root)

	// A date the register wrote but nobody can read is a finding, not a zero: a zero
	// time sorts like 1 January year 1 and would silently order a version chain wrong.
	if raw := d.Meta.DiesSigni; raw != "" {
		if t, ok := ParseDate(raw); ok {
			out.Identity.Dates.Signed = t
		} else {
			out.Diagnostics.Warnings = append(out.Diagnostics.Warnings, "signature date "+raw+" does not parse")
		}
	}
	if raw := d.Meta.DiesEdicti; raw != "" {
		if t, ok := ParseDate(raw); ok {
			out.Identity.Dates.Promulgated = t
		} else {
			out.Diagnostics.Warnings = append(out.Diagnostics.Warnings, "promulgation date "+raw+" does not parse")
		}
	}
	out.Identity.Dates.AnnouncedIn = d.Meta.AnnouncedIn
	return out
}

// cites turns resolved anchors into links to other documents.
func cites(refs []Reference) []lexdoc.Cite {
	if len(refs) == 0 {
		return nil
	}
	out := make([]lexdoc.Cite, 0, len(refs))
	for _, r := range refs {
		c := lexdoc.Cite{Accession: r.Accn, Title: r.Text}
		if r.Effective != "" {
			t, ok := ParseDate(r.Effective)
			if !ok {
				c.Title = c.Title + " [effective date " + r.Effective + " does not parse]"
			} else {
				c.Effective = t
			}
		}
		out = append(out, c)
	}
	return out
}

// periods turns a document's own validity anchors into spans. The register attaches no
// free text to these, so Period.Note stays empty here rather than being filled with a
// field that means something else.
func periods(refs []Reference) []lexdoc.Period {
	if len(refs) == 0 {
		return nil
	}
	out := make([]lexdoc.Period, 0, len(refs))
	for _, r := range refs {
		var p lexdoc.Period
		if r.StartDate != "" {
			if t, ok := ParseDate(r.StartDate); ok {
				p.From = t
			}
		}
		if r.EndDate != "" {
			if t, ok := ParseDate(r.EndDate); ok {
				p.To = t
			}
		}
		out = append(out, p)
	}
	return out
}

func modelSource(s TextSource) lexdoc.Source {
	switch s {
	case TextFromXML:
		return lexdoc.SourceXML
	case TextFromHTML:
		return lexdoc.SourceHTMLPayload
	case TextFromNone:
		return lexdoc.SourceNone
	default:
		return lexdoc.SourceUnknown
	}
}
