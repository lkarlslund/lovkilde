// Package mcpserver serves the mirror to agents over the Model Context Protocol.
//
// Two decisions shape it. The server is read-only: an agent with write access to a
// legal mirror has a much shorter path to a wrong answer than to a right one, and the
// mirror is a cache of someone else's register rather than a place to edit. And every
// answer says where its text came from, because the archive's text is a mixture — the
// register's own structured body for some documents, our reader's reconstruction from
// the site's HTML for most of the rest — and those two are not equally quotable. The
// measured difference is in the README: 84.9 % of paired documents reproduce the
// canonical text byte for byte, which is good enough to search and read, and not good
// enough to quote without saying which one it was.
package mcpserver

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lkarlslund/retsinformation/internal/chain"
	"github.com/lkarlslund/retsinformation/internal/disclaimer"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// DefaultMaxChars bounds one answer. An agent that asks for "the whole law" and gets
// 400 kB of it has not been helped, it has been evicted from its own context.
const DefaultMaxChars = 20000

// LookupRequest names a document, and optionally one provision inside it.
type LookupRequest struct {
	// Document is an ELI path (eli/lta/2019/1399, or the full site URL), a medium
	// path without the eli prefix (lta/2019/1399), or an accession (B20190139905).
	Document string
	// Address is a provision in Danish citation form: "§ 267, stk. 3 nr. 2",
	// "kapitel 5", "bilag 1", "indledningen".
	Address string
	// AsOf is a YYYY-MM-DD date: the text as amended on that day, built from the
	// amendment chain rather than the single stored document.
	AsOf string
	// MaxChars truncates the text; zero means DefaultMaxChars.
	MaxChars int

	// Language picks the language of the prose the server composes itself — the amendment
	// note and the warnings about an incomplete chain. The reading page asks for Danish;
	// empty means English, which is the MCP contract. Data the register supplied (titles,
	// accessions, dates) is never translated, and neither is the internal wording of a
	// refusal reason.
	Language string
}

// LookupResult is an answer: what the document is, where the text came from, and the
// text itself.
type LookupResult struct {
	Citation  string
	ELIURL    string
	Accession string
	Kind      string
	Year      int
	Number    int
	DiesSigni string
	Status    string
	Path      string

	// Provenance is "xml", "html" or "none" — lexdoc.Source, widened to a
	// string so a client sees a plain value.
	Provenance     string
	ProvenanceNote string

	// Mirror is the dataset line: the date asked, the mirror's collection horizon and
	// the index build. It is what "current" means in this answer, stated as dates
	// rather than as a claim.
	Mirror string
	AsOf   string

	// Language is the request's, carried so the prose this type composes for itself comes back
	// in the language the reader asked for.
	Language string

	// ChainActs counts the amending acts that take effect by AsOf, AppliedActs counts those
	// whose text the mirror held and applied, and RefusedEdits counts the individual changes
	// inside them that could not be placed. The three answer different questions: a chain can
	// arrive whole and still be partly unplaced, which is exactly what BR18 does.
	ChainActs    int
	AppliedActs  int
	RefusedEdits int

	// MissingActs are the accessions of amending acts that take effect by AsOf and are not in
	// the mirror. The list is empty almost always, so a reader shown only "incomplete" without
	// it is being pointed at a gap in the engine and told it is a gap in the archive.
	MissingActs []string

	// ChainComplete is whether nothing was refused and nothing was missing. It is the summary;
	// the fields above are the reasons.
	ChainComplete bool

	// Amendments names the later documents this one's metadata points at: the
	// register's own statement, not the mirror's guess.
	Amendments string

	// AmendmentsNote is that statement as a sentence in the request's language, so the Danish
	// reading page does not have to recognise an English warning and throw it away.
	AmendmentsNote string

	// ConsolidateOn is the date a reading page offers the consolidated text for, when the
	// document has been amended since it was published.
	ConsolidateOn string

	Text      string
	Truncated bool
	Warnings  []string

	// HTML is the same content as Text, marked up for a browser: the same walk in lexdoc, with
	// an id per citable thing so an address can be linked to. It is escaped where it is built,
	// not here, and it is not what the MCP tools serve — those answer in Text.
	HTML string

	// Contents is the document's own table of contents, with the ids HTML gave them.
	Contents []ContentsEntry

	// Anchor is where HTML put the provision this request addressed, empty when the address
	// named nothing or no address was given.
	Anchor string

	// RefusedChanges are the changes of the chain the consolidated text does not include, each
	// with the act's own wording: a reader told only that "3 changes were refused" cannot tell
	// whether the provision in front of them is one, nor what it should read. Those concerning the
	// address asked for come first, marked.
	RefusedChanges []RefusedChange
}

// RefusedChange is one change the engine could not place, as the act states it.
type RefusedChange struct {
	Act      string // the amending act's accession
	ActTitle string
	ActELI   string
	Number   string // the change's number in the act: "2."
	Target   string // the place it names, as read: "§ 5, stk. 1"
	InForce  string // the day it takes effect, when later than the act's signing
	// Wording is the change as the act prints it: its sentence, and the new text it brings.
	Wording string
	Reason  string
	// Concerns is set when the change names the provision the request addressed.
	Concerns bool
	// Anchor is the id a page gave the provision the change names, "" when it names none the
	// page shows.
	Anchor string
}

// ContentsEntry is one line of a document's table of contents.
type ContentsEntry struct {
	ID    string
	Label string
	Level int
}

// Lookup answers one request against the archive, as an MCP client gets it: text, and no
// markup. The browser's rendering is deliberately not built here — it is a second render of
// the same document, and an agent that never receives it should not pay for it. Use
// LookupPage where the HTML and table of contents are wanted.
func (s *Server) Lookup(req LookupRequest) (*LookupResult, error) {
	return s.lookup(req, false)
}

// LookupPage answers the same request and also builds what a page needs: the HTML rendering,
// the document's own table of contents, and the anchor the addressed provision was given. One
// read of the file, one walk of the model, both outputs.
func (s *Server) LookupPage(req LookupRequest) (*LookupResult, error) {
	return s.lookup(req, true)
}

// lookup answers one request. wantHTML decides the one thing that differs between the two public
// shapes: a page gets the rendering and the table of contents, an answer gets text alone.
//
// The character cap applies to the answer and not to the page. It exists to keep a reply inside an
// agent's context, and applying it here produced a page whose HTML held the whole document while
// its text said "Svaret er afkortet" — measured on 202223L00057, whose page is 12.5 MB. A page is
// bounded by what the publisher published; that is a deliberate choice, and the document's own size
// is the number it is bounded by, not a cap borrowed from the tool.
func (s *Server) lookup(req LookupRequest, wantHTML bool) (*LookupResult, error) {
	danish := strings.EqualFold(req.Language, "da")
	entry, err := s.find(req.Document, danish)
	if err != nil {
		return nil, err
	}
	res := &LookupResult{
		Mirror:    s.horizon().LineIn(danish),
		Citation:  citation(entry),
		ELIURL:    s.siteBase + "/" + entry.ELIPath(),
		Accession: entry.Accession,
		Kind:      entry.Kind,
		Year:      entry.Year,
		Number:    entry.Number,
		DiesSigni: entry.DiesSigni,
		Status:    string(entry.Status),
		Language:  req.Language,
	}

	if req.AsOf != "" {
		return s.lookupAsOf(req, entry, res, wantHTML)
	}

	m, path, err := s.model(entry)
	if err != nil {
		return nil, err
	}
	res.Path = path
	res.provenance(m)
	res.noteAmendments(m, s.horizon().Now)

	if err := res.render(&m.Content, entry.Accession, req.Address, wantHTML); err != nil {
		return nil, err
	}
	for _, w := range m.Diagnostics.Warnings {
		if danish {
			w = diagnosticDA(w)
		}
		res.Warnings = append(res.Warnings, w)
	}
	if !wantHTML {
		res.truncate(req.MaxChars)
	}
	return res, nil
}

// render fills the answer's text, and for a page its HTML and contents, from one document's
// content: the stored one, or a consolidated one. Both paths render the same way, so an as-of
// answer cites "§ 5, stk. 2" exactly as an ordinary one does.
//
// The text comes from the model, so an answer can say what the thing it found *is*: a chapter, a
// provision, one subsection of it.
func (r *LookupResult) render(c *lexdoc.Content, accession, address string, wantHTML bool) error {
	addr := lexdoc.ParseAddress(address)
	if address != "" {
		body, ok := c.MarkdownProvisions(addr)
		if !ok {
			if strings.EqualFold(r.Language, "da") {
				return fmt.Errorf("intet i %s svarer til %q. Adresser skrives som en dansk henvisning (§ 5, stk. 2); ikrafttrædelses- og overgangsbestemmelser kan ikke adresseres, fordi deres nummerering begynder forfra", accession, address)
			}
			if r.AsOf != "" {
				return fmt.Errorf("%s as of %s: nothing matches %q", accession, r.AsOf, address)
			}
			return fmt.Errorf("%s: nothing matches %q among %d provisions. Addresses are read in Danish citation form (§ 5, stk. 2); entry-into-force and transitional material is deliberately not addressable because its numbering restarts",
				accession, address, len(c.Provisions()))
		}
		r.Text = body
		if wantHTML {
			r.HTML, _ = c.HTMLProvisions(addr)
			r.Anchor = c.AnchorFor(addr)
		}
	} else {
		r.Text = c.Markdown()
		if wantHTML {
			r.HTML = c.HTML()
		}
	}
	if wantHTML {
		for _, line := range c.Contents() {
			r.Contents = append(r.Contents, ContentsEntry{ID: line.ID, Label: line.Label, Level: line.Level})
		}
	}
	return nil
}

// lookupAsOf builds the consolidated view instead of reading one stored document.
func (s *Server) lookupAsOf(req LookupRequest, entry chain.Entry, res *LookupResult, wantHTML bool) (*LookupResult, error) {
	asOf, ok := lexdoc.ParseDate(req.AsOf)
	if !ok || asOf.IsZero() {
		if strings.EqualFold(req.Language, "da") {
			return nil, fmt.Errorf("%q er ikke en dato; skriv ÅÅÅÅ-MM-DD", req.AsOf)
		}
		return nil, fmt.Errorf("as_of %q is not a date; use YYYY-MM-DD", req.AsOf)
	}
	view, err := s.lib().LawAsOf(entry.Accession, asOf)
	if err != nil {
		return nil, err
	}
	res.Path = view.Path
	res.AsOf = asOf.Format("2006-01-02")
	res.Language = req.Language
	res.ChainActs = len(view.Chain)
	res.AppliedActs = view.AppliedCount()
	res.RefusedEdits = len(view.Unresolved)
	for _, m := range view.MissingChain() {
		res.MissingActs = append(res.MissingActs, m.Accession)
	}
	res.ChainComplete = view.Trustworthy()
	if strings.EqualFold(req.Language, "da") {
		// The reading page needs to say what this text *is* when it is a derived one. The MCP
		// answer already prints the same facts as its own line; only the page had nothing.
		res.AmendmentsNote = fmt.Sprintf(
			"Teksten er konsolideret som den %s efter %d af %d ændringsbekendtgørelser.",
			res.AsOf, res.AppliedActs, res.ChainActs)
	}
	res.provenance(view.Doc)
	if err := res.render(view.Content, entry.Accession, req.Address, wantHTML); err != nil {
		return nil, err
	}
	res.warnChain(view)
	res.RefusedChanges = s.refusedChanges(view.Unresolved, req.Address)
	res.Text = markRefused(res.Text, res.RefusedChanges)
	if wantHTML {
		for i := range res.RefusedChanges {
			if a := lexdoc.ParseAddress(res.RefusedChanges[i].Target); a.Paragraph != "" {
				res.RefusedChanges[i].Anchor = view.Content.AnchorFor(lexdoc.Address{Recognised: true, Paragraph: a.Paragraph})
			}
		}
	}
	if !wantHTML {
		res.truncate(req.MaxChars)
	}
	return res, nil
}

// maxRefusedWording caps one refused change's wording in an answer; a change rewriting a whole
// annex would otherwise crowd out the text it was refused from.
const maxRefusedWording = 1500

// refusedChanges lists the changes the consolidated text leaves out, with the act's wording, those
// naming the requested address first.
func (s *Server) refusedChanges(refused []lexdoc.UnresolvedEdit, address string) []RefusedChange {
	asked := lexdoc.ParseAddress(address)
	var here, rest []RefusedChange
	for _, u := range refused {
		e := u.Edit
		rc := RefusedChange{Act: e.Source, Number: strings.TrimSpace(e.Number), Target: e.Address.String(), Reason: u.Reason}
		if entry, ok := s.lib().Find(e.Source); ok {
			rc.ActTitle = citation(entry)
			rc.ActELI = s.siteBase + "/" + entry.ELIPath()
		}
		if !e.InForce.IsZero() && e.InForce.Year() < 9999 {
			rc.InForce = e.InForce.Format("2006-01-02")
		}
		wording := strings.TrimSpace(e.Definition)
		if p := strings.TrimSpace(e.Prose); len(p) > len(wording) {
			wording = p // the act's whole paragraph, its new text with it
		} else if t := e.BroughtText(); t != "" && !strings.Contains(wording, t) {
			wording += " »" + t + "«"
		}
		if r := []rune(wording); len(r) > maxRefusedWording {
			wording = string(r[:maxRefusedWording]) + " …"
		}
		rc.Wording = wording
		if address != "" && concerns(e, asked) {
			rc.Concerns = true
			here = append(here, rc)
			continue
		}
		rest = append(rest, rc)
	}
	return append(here, rest...)
}

// concerns reports a change naming the provision an address asks for: the same §, and the same
// subsection where both name one; an annex by its number.
func concerns(e lexdoc.Edit, asked lexdoc.Address) bool {
	places := lexdoc.ParseAddresses(e.Definition)
	places = append(places, e.Address)
	for _, a := range places {
		switch {
		case asked.Paragraph != "" && strings.EqualFold(a.Paragraph, asked.Paragraph):
			if asked.Stk == 0 || a.Stk == 0 || a.Stk == asked.Stk {
				return true
			}
		case asked.Bilag != 0 && a.Bilag == asked.Bilag:
			return true
		}
	}
	return false
}

// warnChain states that a consolidated text is not the whole answer, and which of the two
// reasons applies. Naming the wrong one is not cosmetic: "not in the mirror" sends the reader to
// collect more documents, while a refused edit is our own limitation that collecting more
// documents cannot repair. BR18 has the second and used to be reported as the first.
func (r *LookupResult) warnChain(view *chain.View) {
	danish := strings.EqualFold(r.Language, "da")
	if len(r.MissingActs) > 0 {
		if danish {
			r.Warnings = append(r.Warnings, fmt.Sprintf(
				"%d af de %d ændringsbekendtgørelser, der træder i kraft inden %s, findes ikke i spejlet (%s). Deres ændringer indgår ikke i teksten, og ingen bestemmelse er markeret som berørt.",
				len(r.MissingActs), r.ChainActs, r.AsOf, strings.Join(r.MissingActs, ", ")))
		} else {
			r.Warnings = append(r.Warnings, fmt.Sprintf(
				"%d of the %d amending acts that take effect by %s are not in the mirror (%s), so their changes are absent from this text, and no provision is marked as affected",
				len(r.MissingActs), r.ChainActs, r.AsOf, strings.Join(r.MissingActs, ", ")))
		}
	}
	if n := len(view.Pending); n > 0 {
		first := view.Pending[0].InForce
		for _, e := range view.Pending {
			if e.InForce.Before(first) {
				first = e.InForce
			}
		}
		if danish {
			r.Warnings = append(r.Warnings, fmt.Sprintf(
				"%d ændringer i love, der er vedtaget inden %s, træder først i kraft senere (den første den %s) og indgår ikke i teksten.",
				n, r.AsOf, first.Format("2006-01-02")))
		} else {
			r.Warnings = append(r.Warnings, fmt.Sprintf(
				"%d changes made by acts already passed by %s take effect later (the first on %s) and are not in this text.",
				n, r.AsOf, first.Format("2006-01-02")))
		}
	}
	if r.RefusedEdits == 0 {
		return
	}
	if danish {
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"%d af ændringerne i kæden kunne ikke indarbejdes i teksten. De står med deres ordlyd under »Ændringer, der ikke er indarbejdet«, og en bestemmelse, de angår, er markeret: den kan stadig vise den tidligere ordlyd.",
			r.RefusedEdits))
		return
	}
	r.Warnings = append(r.Warnings, fmt.Sprintf(
		"%d of the changes made by this law's amending acts could not be placed in the text, so where one of them applied, this text may still show the older wording; each is listed with its wording under \"Changes not placed in this text\". Refused because: %s",
		r.RefusedEdits, refusalReasons(view.Unresolved)))
}

// refusalReasons says in one line why changes were refused, most frequent class first. A count
// alone only says the text may be stale; the classes say whom to distrust — an address that no
// longer resolves is a provision the chain moved, while wording no pattern recognised is a kind
// of change the engine has never placed.
//
// Reasons are grouped with any quoted address removed, because "§ 21, stk. 2" not found" and
// "§ 523, stk. 2" not found" are one class of failure wearing two addresses.
func refusalReasons(refused []lexdoc.UnresolvedEdit) string {
	counts := map[string]int{}
	for _, u := range refused {
		r := u.Reason
		for {
			from := strings.IndexByte(r, '"')
			if from < 0 {
				break
			}
			to := strings.IndexByte(r[from+1:], '"')
			if to < 0 {
				break
			}
			r = r[:from] + r[from+to+2:]
		}
		r = strings.Join(strings.Fields(strings.Trim(r, " ;.,")), " ")
		if r == "" {
			r = "no reason recorded"
		}
		counts[r]++
	}
	type reason struct {
		text string
		n    int
	}
	pairs := make([]reason, 0, len(counts))
	for r, n := range counts {
		pairs = append(pairs, reason{r, n})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].n != pairs[j].n {
			return pairs[i].n > pairs[j].n
		}
		return pairs[i].text < pairs[j].text
	})
	parts := make([]string, 0, 3)
	for i, p := range pairs {
		if i >= 3 {
			parts = append(parts, fmt.Sprintf("%d further reasons", len(pairs)-3))
			break
		}
		parts = append(parts, fmt.Sprintf("%d× %s", p.n, p.text))
	}
	return strings.Join(parts, "; ")
}

// noteAmendments records what this document's own metadata says about later
// documents, which is how a mirror answers "is this the text in force?" without
// guessing at anything. The Change anchors are data in the register's payload: they
// name the accessions that amend this document and the date each takes effect, so
// the answer carries dates the mirror holds rather than a forecast of what may or
// may not have happened since the mirror was crawled.
// The reference is read through the model, because the publisher's own name for a reference type
// ("Change") is not something an answer should be matching on: it is one spelling in one format,
// and the model already decided which references amend the document and from when.
func (r *LookupResult) noteAmendments(m *lexdoc.Document, now time.Time) {
	type later struct {
		cite lexdoc.Cite
	}
	var past, future []later
	for _, ref := range m.Identity.AmendedBy {
		if ref.Accession == "" {
			continue
		}
		item := later{cite: ref}
		// An amendment whose payload carries no date cannot be placed on a timeline,
		// so it is treated as applicable: the register says something changed and the
		// mirror cannot say when, which is the client's problem to know about.
		if ref.Effective.IsZero() || !ref.Effective.After(now) {
			past = append(past, item)
		} else {
			future = append(future, item)
		}
	}
	if len(past) > 0 {
		parts := make([]string, 0, len(past)+1)
		for i, l := range past {
			danish := strings.EqualFold(r.Language, "da")
			if i == maxNamedAmendments {
				if danish {
					parts = append(parts, fmt.Sprintf("og %d flere", len(past)-i))
				} else {
					parts = append(parts, fmt.Sprintf("and %d more", len(past)-i))
				}
				break
			}
			field := l.cite.Accession
			if !l.cite.Effective.IsZero() {
				if danish {
					field += " fra " + l.cite.Effective.Format("2006-01-02")
				} else {
					field += " eff. " + l.cite.Effective.Format("2006-01-02")
				}
			}
			parts = append(parts, field)
		}
		r.Amendments = strings.Join(parts, ", ")
		switch {
		case r.AsOf != "":
			// This answer is already the text as amended, which is what the sentence recommends.
		case strings.EqualFold(r.Language, "da"):
			r.AmendmentsNote = fmt.Sprintf(
				"Ifølge dokumentets egne metadata er det siden ændret af %s. Teksten her er dokumentet, som det blev offentliggjort.",
				r.Amendments)
			r.ConsolidateOn = now.Format("2006-01-02")
		default:
			r.Warnings = append(r.Warnings, fmt.Sprintf(
				"%d document(s) named in this document's own metadata amend it on or before %s. The text above is this accession as published; ask again with as_of=%s for the text as amended on that date.",
				len(past), now.Format("2006-01-02"), now.Format("2006-01-02")))
		}
	}
	if len(future) > 0 {
		if len(past) == 0 {
			r.Amendments = fmt.Sprintf("%d, all effective after %s (earliest %s) -- as of %s this accession is the operative text in this dataset",
				len(future), now.Format("2006-01-02"),
				future[0].cite.Effective.Format("2006-01-02"), now.Format("2006-01-02"))
		}
	}
}

// maxNamedAmendments bounds how many accessions an answer names before summarising;
// a bill with a hundred sittings should not be served as a hundred words.
const maxNamedAmendments = 6

// model reads the document an answer is made of.
//
// With an artifact configured the read is a seek, a decompression and a decode of the model the
// builder compiled — the publisher's XML is what a *builder* reads, not what a lookup reads. Without
// one it parses, and says so through Status. A document the artifact does not hold is an error
// naming the remedy: the artifact's coverage is a claim about the mirror, and an answer that quietly
// came back empty would turn a stale file into a law with nothing in it.
func (s *Server) model(entry chain.Entry) (*lexdoc.Document, string, error) {
	if s.cache() != nil {
		m, err := s.cache().Document(entry.Accession)
		if err != nil {
			return nil, "", fmt.Errorf("%s is indexed but not in the compiled artifact (%s): %w. Rebuild it with `lovkilde-update artifact build`",
				entry.Accession, s.cache().Artifact().Path(), err)
		}
		return m, s.cache().Artifact().Path(), nil
	}
	m, path, ok := s.lib().Model(entry.Accession)
	if !ok {
		return nil, "", fmt.Errorf("%s is indexed but could not be read from %s: the file may be mid-write, re-run", entry.Accession, entry.Path)
	}
	return m, path, nil
}

// provenance records which text this is, in the words a client will read. It reads the model's own
// provenance rather than the reader's, because the same answer is served from the artifact and from
// a parse, and the distinction that matters to a reader — whose text is this — is a property of the
// document, not of how it was read.
func (r *LookupResult) provenance(m *lexdoc.Document) {
	defer func() {
		if strings.EqualFold(r.Language, "da") {
			r.ProvenanceNote = provenanceNoteDA(r.Provenance, m.Provenance.Sidecar)
		}
	}()
	switch m.Provenance.Source {
	case lexdoc.SourceHTMLPayload:
		r.Provenance = string(lexdoc.SourceHTMLPayload)
		r.ProvenanceNote = "recovered from the site's HTML payload (" + m.Provenance.Sidecar + "). " +
			"This is our reader's reconstruction rather than the register's structured text: " +
			"it matched the canonical body byte for byte in 84.9% of the paired sample, so it is fine to read and search, " +
			"and has to be identified as HTML-derived when quoted."
	case lexdoc.SourceNone:
		r.Provenance = string(lexdoc.SourceNone)
		r.ProvenanceNote = "the mirror holds this document's metadata but no text at all: " +
			"neither a canonical body nor an HTML payload. Fetch the body before relying on an answer about its contents."
	default:
		r.Provenance = string(m.Provenance.Source)
		if r.Provenance == "" {
			r.Provenance = string(lexdoc.SourceXML)
		}
		// The register's own name for its body element is not a word an answer needs: it is one
		// spelling in one format, and what a reader needs to know is that this is the structured
		// text the register publishes rather than a reconstruction.
		r.ProvenanceNote = "the register's own structured body, decoded from canonical ELI XML"
	}
	if r.Path == "" {
		r.Path = m.Provenance.Sidecar
	}
}

func (r *LookupResult) truncate(max int) {
	if max <= 0 {
		max = DefaultMaxChars
	}
	if len(r.Text) <= max {
		return
	}
	// Cut on a rune boundary: this corpus is Danish, and slicing the middle of an å
	// produces a text the client cannot render.
	cut := max
	for cut > 0 && !utf8Start(r.Text[cut]) {
		cut--
	}
	r.Text = r.Text[:cut]
	r.Truncated = true
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// String renders the answer as the text a client is given: a header that identifies
// the source, then the text.
func (r *LookupResult) String() string {
	// Markdown, because the consumer is a model rendering this in a chat: headings and
	// emphasis survive a copy-paste, and a fenced block keeps statute text from being
	// reinterpreted as markup by whatever renders the answer next.
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", r.Citation)
	fmt.Fprintf(&b, "- **ELI:** %s\n", r.ELIURL)
	fmt.Fprintf(&b, "- **Accession:** `%s`\n", r.Accession)
	fmt.Fprintf(&b, "- **Status:** %s · **%d/%d** · **kind:** %s\n", r.Status, r.Year, r.Number, r.Kind)
	if r.DiesSigni != "" {
		fmt.Fprintf(&b, "- **Dies signi:** %s\n", r.DiesSigni)
	}
	if r.AsOf != "" {
		chain := "complete"
		if !r.ChainComplete {
			chain = "**INCOMPLETE**"
		}
		fmt.Fprintf(&b, "- **As of %s:** %d of %d amending acts applied, %d changes refused, chain %s\n",
			r.AsOf, r.AppliedActs, r.ChainActs, r.RefusedEdits, chain)
	}
	fmt.Fprintf(&b, "- **Text source:** `%s` — %s\n", r.Provenance, r.ProvenanceNote)
	fmt.Fprintf(&b, "- **Dataset:** %s\n", r.Mirror)
	if r.Amendments != "" {
		fmt.Fprintf(&b, "- **Later documents naming this one (per its own metadata):** %s\n", r.Amendments)
	}
	notice := disclaimer.ShortEnglish
	if r.AsOf != "" {
		notice = disclaimer.ConsolidatedEnglish + " " + disclaimer.ShortEnglish
	}
	fmt.Fprintf(&b, "> **Disclaimer:** %s\n", notice)
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "> **Warning:** %s\n", w)
	}
	if r.Path != "" {
		fmt.Fprintf(&b, "- **Stored at:** `%s`\n", r.Path)
	}

	r.writeRefused(&b)
	fmt.Fprintf(&b, "\n### Text\n\n%s\n", textBlock(r.Text))
	if r.Truncated {
		fmt.Fprintf(&b, "\n*[truncated at %d characters; ask again with a larger max_chars or a narrower address]*\n", DefaultMaxChars)
	}
	return b.String()
}

// writeRefused lists the changes the text leaves out, ahead of the text so a truncated answer
// still carries them: what each act says, where it aimed, and why it was not placed.
func (r *LookupResult) writeRefused(b *strings.Builder) {
	if len(r.RefusedChanges) == 0 {
		return
	}
	here := 0
	for _, c := range r.RefusedChanges {
		if c.Concerns {
			here++
		}
	}
	fmt.Fprintf(b, "\n### Changes not placed in this text (%d)\n\n", len(r.RefusedChanges))
	switch {
	case here == 1:
		fmt.Fprintf(b, "**1 of them concerns the provision asked for**, so its text below may not read as the law does on %s. ", r.AsOf)
	case here > 1:
		fmt.Fprintf(b, "**%d of them concern the provision asked for**, so its text below may not read as the law does on %s. ", here, r.AsOf)
	}
	fmt.Fprintf(b, "The text below is the law with every other change made; these were not, and each is quoted as its act prints it.\n\n")
	for i, c := range r.RefusedChanges {
		mark := ""
		if c.Concerns {
			mark = " — **concerns the provision asked for**"
		}
		act := c.Act
		if c.ActTitle != "" {
			act = fmt.Sprintf("%s (`%s`)", c.ActTitle, c.Act)
		}
		fmt.Fprintf(b, "%d. **%s, change %s** → %s%s\n", i+1, act, c.Number, orDash(c.Target), mark)
		if c.InForce != "" {
			fmt.Fprintf(b, "   - **In force:** %s\n", c.InForce)
		}
		if c.ActELI != "" {
			fmt.Fprintf(b, "   - **Act:** %s\n", c.ActELI)
		}
		fmt.Fprintf(b, "   - **Not placed because:** %s\n", c.Reason)
		fmt.Fprintf(b, "   - **Wording:** %s\n", strings.ReplaceAll(c.Wording, "\n", " "))
	}
}

// markRefused puts a line under each provision's label in the text naming the changes to it that
// were not placed, by their number in the list: someone reading § 5 learns there, and not only in
// a list above, that § 5 may not read as the law does.
func markRefused(text string, refused []RefusedChange) string {
	if len(refused) == 0 {
		return text
	}
	byLabel := map[string][]string{}
	var order []string
	for i, c := range refused {
		a := lexdoc.ParseAddress(c.Target)
		if a.Paragraph == "" {
			continue
		}
		label := provisionLabel(a.Paragraph)
		if _, seen := byLabel[label]; !seen {
			order = append(order, label)
		}
		byLabel[label] = append(byLabel[label], fmt.Sprint(i+1))
	}
	if len(order) == 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines)+len(order))
	done := map[string]bool{}
	for _, l := range lines {
		out = append(out, l)
		key := strings.Join(strings.Fields(strings.TrimLeft(l, "#* ")), " ")
		key = strings.TrimRight(key, "*")
		if items, ok := byLabel[key]; ok && !done[key] {
			done[key] = true
			out = append(out, "", fmt.Sprintf("> **Change not placed here:** see %s under \"Changes not placed in this text\"; this provision may not read as the law does.", itemList(items)), "")
		}
	}
	return strings.Join(out, "\n")
}

// provisionLabel is how the text prints a provision's label: "5" is "§ 5.", "3a" is "§ 3 a.".
func provisionLabel(paragraph string) string {
	n := strings.TrimRight(paragraph, "abcdefghijklmnopqrstuvwxyzæøå")
	if l := paragraph[len(n):]; l != "" {
		return "§ " + n + " " + l + "."
	}
	return "§ " + n + "."
}

// itemList names list items: "item 2", "items 2 and 5".
func itemList(items []string) string {
	if len(items) == 1 {
		return "item " + items[0]
	}
	return "items " + strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// provenanceNoteDA says in Danish where a document's text came from.
func provenanceNoteDA(source, sidecar string) string {
	switch source {
	case string(lexdoc.SourceHTMLPayload):
		return "hentet fra registerets html-side (" + sidecar + "). Det er vores læsning, ikke registerets strukturerede tekst: " +
			"den stemte tegn for tegn med registerets tekst i 84,9 % af stikprøven, så den kan læses og søges i, " +
			"men skal angives som læst fra html, når den citeres."
	case string(lexdoc.SourceNone):
		return "spejlet har dokumentets metadata, men ingen tekst: hverken registerets strukturerede tekst eller en html-side."
	}
	return "læst fra registerets egen strukturerede XML"
}

// diagnosticDA puts the reader's notes about a document into Danish.
func diagnosticDA(w string) string {
	for en, da := range map[string]string{
		"signature date ":    "underskriftsdatoen ",
		"promulgation date ": "kundgørelsesdatoen ",
	} {
		if rest, ok := strings.CutPrefix(w, en); ok {
			return da + strings.TrimSuffix(rest, " does not parse") + " kan ikke læses"
		}
	}
	return w
}

// orDash is a value, or a dash where there is none.
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// textBlock places the text in the answer.
//
// It used to be fenced always. That stopped being right when the text became the model's own
// rendering: the answer's whole job is to show which part is a chapter and which is a provision,
// and a fence turns that back into a wall of characters. Statute text still gets fenced if it
// carries a fence of its own, because a fence that closes early leaks the rest of a law into the
// answer as markup.
func textBlock(text string) string {
	if strings.Contains(text, "```") {
		return fenced(text)
	}
	return text
}

// fenced puts text in a code fence, lengthening the fence if the text contains one.
//
// Statute text is not supposed to contain triple backticks, and it does not matter
// whether it does: a fence that closes early would leak the rest of a law into the
// answer as markup.
func fenced(text string) string {
	fence := "```"
	if strings.Contains(text, "```") {
		fence = "````"
	}
	return fence + "\n" + text + "\n" + fence
}

// find resolves the document argument. It accepts the forms a caller is most likely
// to have in hand: the ELI path from a URL, that path without the eli prefix, or the
// accession number the register keys everything by.
func (s *Server) find(doc string, danish bool) (chain.Entry, error) {
	id := strings.TrimSpace(doc)
	if id == "" {
		if danish {
			return chain.Entry{}, fmt.Errorf("der er ikke peget på noget dokument: angiv en ELI-sti (eli/lta/2019/1399) eller et accessionsnummer (B20190139905)")
		}
		return chain.Entry{}, fmt.Errorf("no document given: pass an ELI path (eli/lta/2019/1399) or an accession (B20190139905)")
	}
	if i := strings.Index(id, "retsinformation.dk"); i >= 0 {
		id = id[i+len("retsinformation.dk"):]
	}
	id = strings.Trim(id, "/")
	id = strings.TrimPrefix(id, "eli/")
	id = strings.TrimSuffix(id, "/xml")

	if !strings.Contains(id, "/") {
		acc := strings.ToUpper(id)
		if e, ok := s.lib().Find(acc); ok {
			return e, nil
		}
		if danish {
			return chain.Entry{}, fmt.Errorf("%s findes ikke i spejlet (%d dokumenter)", acc, s.lib().Len())
		}
		return chain.Entry{}, fmt.Errorf("accession %q is not in the mirror (%d documents indexed)", acc, s.lib().Len())
	}

	candidates := s.cat().byELI[id]
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if len(candidates) > 1 {
		// Not a bug to paper over: the stages of a bill genuinely differ in text, so
		// picking one and presenting it as "the document" would be a confident wrong
		// answer. Listing the accessions turns it into a choice the caller can make.
		names := make([]string, 0, len(candidates))
		for _, e := range candidates {
			names = append(names, fmt.Sprintf("%s (%s)", e.Accession, e.DocumentType))
		}
		if danish {
			return chain.Entry{}, fmt.Errorf("%s svarer til %d dokumenter i spejlet, ikke ét: %s. Vælg et af accessionsnumrene — en folketingssag gemmer alle et lovforslags stadier under samme ELI-sti",
				id, len(candidates), strings.Join(names, ", "))
		}
		return chain.Entry{}, fmt.Errorf("%q matches %d documents in the mirror, not one: %s. Re-ask with one of those accession numbers — folketingssager store every stage of a bill under the same ELI path",
			id, len(candidates), strings.Join(names, ", "))
	}
	if danish {
		return chain.Entry{}, fmt.Errorf("%s findes ikke i spejlet (%d dokumenter). Stien er <medie>/<år>/<nummer>, f.eks. lta/2019/1399", id, s.lib().Len())
	}
	return chain.Entry{}, fmt.Errorf("%q is not in the mirror (%d documents indexed). The path is <medium>/<year>/<number>, e.g. lta/2019/1399", id, s.lib().Len())
}

// citation renders the document the way it is cited in Danish practice.
func citation(e chain.Entry) string {
	if e.Title != "" {
		return e.Title
	}
	return fmt.Sprintf("%s nr. %d af %s", e.DocumentType, e.Number, e.DiesSigni)
}

// countByKind backs the status text; it is computed once at startup.
type countByKind map[string]int

func (c countByKind) add(kind string) { c[kind]++ }

// String sorts the media: the same mirror should render the same status string in
// every answer, or a client that caches or diffs answers sees change that did not
// happen. Map order is exactly the kind of noise that looks like a data change.
func (c countByKind) String() string {
	kinds := make([]string, 0, len(c))
	for kind := range c {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		parts = append(parts, kind+"="+strconv.Itoa(c[kind]))
	}
	return strings.Join(parts, " ")
}

// Citation names a document from the archive's metadata alone, without reading its text: the
// title a page puts beside a filter that keeps a search inside that document.
func (s *Server) Citation(ref string) (string, bool) {
	e, err := s.find(ref, true)
	if err != nil {
		return "", false
	}
	return citation(e), true
}
