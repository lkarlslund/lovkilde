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

	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// DefaultMaxChars bounds one answer. An agent that asks for "the whole law" and gets
// 400 kB of it has not been helped, it has been evicted from its own context.
const DefaultMaxChars = 20000

// LookupRequest names a document, and optionally one provision inside it.
type LookupRequest struct {
	// Document is an ELI path (eli/lta/2019/1399, or the full site URL), a medium
	// path without the eli prefix (lta/2019/1399), or an accession (A20191399070).
	Document string
	// Address is a provision in Danish citation form: "§ 267, stk. 3 nr. 2",
	// "kapitel 5", "bilag 1", "indledningen".
	Address string
	// AsOf is a YYYY-MM-DD date: the text as amended on that day, built from the
	// amendment chain rather than the single stored document.
	AsOf string
	// MaxChars truncates the text; zero means DefaultMaxChars.
	MaxChars int
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

	// Provenance is "xml", "html" or "none" — lexdania.TextSource, widened to a
	// string so a client sees a plain value.
	Provenance     string
	ProvenanceNote string

	// Mirror is the dataset line: the date asked, the mirror's collection horizon and
	// the index build. It is what "current" means in this answer, stated as dates
	// rather than as a claim.
	Mirror        string
	AsOf          string
	AppliedEdits  int
	ChainComplete bool

	// Amendments names the later documents this one's metadata points at: the
	// register's own statement, not the mirror's guess.
	Amendments string

	Text      string
	Truncated bool
	Warnings  []string
}

// Lookup answers one request against the archive.
func (s *Server) Lookup(req LookupRequest) (*LookupResult, error) {
	entry, err := s.find(req.Document)
	if err != nil {
		return nil, err
	}
	res := &LookupResult{
		Mirror:    s.horizon().Line(),
		Citation:  citation(entry),
		ELIURL:    s.siteBase + "/" + entry.ELIPath(),
		Accession: entry.Accession,
		Kind:      entry.Kind,
		Year:      entry.Year,
		Number:    entry.Number,
		DiesSigni: entry.DiesSigni,
		Status:    string(entry.Status),
	}

	if req.AsOf != "" {
		return s.lookupAsOf(req, entry, res)
	}

	doc, path, ok := s.archive.Open(entry.Accession)
	if !ok {
		return nil, fmt.Errorf("%s is indexed but could not be read from %s: the file may be mid-write, re-run", entry.Accession, entry.Path)
	}
	res.Path = path
	res.provenance(doc, s.archive.Root)
	res.noteAmendments(doc.Model(), s.horizon().Now)

	// The text comes from the model, so an answer can say what the thing it found *is*: a
	// chapter, a provision, one subsection of it. The flat section list cannot tell those
	// apart — it carries whatever the publisher's elements happened to be — and its
	// subsection addressing is off by one in places, which is a citation error waiting to be
	// quoted.
	m := doc.Model()
	if req.Address != "" {
		body, ok := m.Content.MarkdownProvisions(lexdoc.ParseAddress(req.Address))
		if !ok {
			return nil, fmt.Errorf("%s: nothing matches %q among %d provisions. Addresses are read in Danish citation form (§ 5, stk. 2); entry-into-force and transitional material is deliberately not addressable because its numbering restarts",
				entry.Accession, req.Address, len(m.Content.Provisions()))
		}
		res.Text = body
	} else {
		res.Text = m.Content.Markdown()
	}
	res.Warnings = append(res.Warnings, doc.Warnings()...)
	res.truncate(req.MaxChars)
	return res, nil
}

// lookupAsOf builds the consolidated view instead of reading one stored document.
func (s *Server) lookupAsOf(req LookupRequest, entry lexdania.Entry, res *LookupResult) (*LookupResult, error) {
	asOf, ok := lexdania.ParseDate(req.AsOf)
	if !ok || asOf.IsZero() {
		return nil, fmt.Errorf("as_of %q is not a date; use YYYY-MM-DD", req.AsOf)
	}
	view, err := s.archive.LawAsOf(entry.Accession, asOf)
	if err != nil {
		return nil, err
	}
	res.Path = view.Path
	res.AsOf = asOf.Format("2006-01-02")
	res.AppliedEdits = view.AppliedCount()
	res.ChainComplete = view.Trustworthy()
	res.provenance(view.Doc, s.archive.Root)

	if req.Address != "" {
		sec := view.Section(req.Address)
		if sec == nil {
			return nil, fmt.Errorf("%s as of %s: nothing matches %q", entry.Accession, res.AsOf, req.Address)
		}
		res.Text = lexdania.DisplayText(sec.Node)
	} else {
		res.Text = view.Text()
	}
	if !res.ChainComplete {
		res.Warnings = append(res.Warnings,
			"the amendment chain is incomplete: at least one amending document named by the chain is not in the mirror, so this text does not include its edits. The chain, including what is missing, is listed in the consolidation report")
	}
	if len(view.Unresolved) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%d edits in the chain could not be placed in the text; they are listed in the consolidation report, not applied", len(view.Unresolved)))
	}
	res.truncate(req.MaxChars)
	return res, nil
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
			if i == maxNamedAmendments {
				parts = append(parts, fmt.Sprintf("and %d more", len(past)-i))
				break
			}
			field := l.cite.Accession
			if !l.cite.Effective.IsZero() {
				field += " eff. " + l.cite.Effective.Format("2006-01-02")
			}
			parts = append(parts, field)
		}
		r.Amendments = strings.Join(parts, ", ")
		if r.AsOf == "" {
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

// provenance records which text this is, in the words a client will read.
func (r *LookupResult) provenance(doc *lexdania.Document, mirrorRoot string) {
	switch doc.Source {
	case lexdania.TextFromHTML:
		r.Provenance = string(lexdania.TextFromHTML)
		r.ProvenanceNote = "recovered from the site's HTML payload (" + doc.Sidecar + "). " +
			"This is our reader's reconstruction rather than the register's structured text: " +
			"it matched the canonical body byte for byte in 84.9% of the paired sample, so it is fine to read and search, " +
			"and has to be identified as HTML-derived when quoted."
	case lexdania.TextFromNone:
		r.Provenance = string(lexdania.TextFromNone)
		r.ProvenanceNote = "the mirror holds this document's metadata but no text at all: " +
			"neither a canonical body nor an HTML payload. Fetch the body before relying on an answer about its contents."
	default:
		r.Provenance = string(doc.Source)
		if r.Provenance == "" {
			r.Provenance = string(lexdania.TextFromXML)
		}
		r.ProvenanceNote = "the register's own <DokumentIndhold>, decoded from canonical ELI XML"
	}
	if r.Path == "" {
		r.Path = doc.Sidecar
	}
	_ = mirrorRoot
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
		fmt.Fprintf(&b, "- **As of %s:** %d amending documents applied, chain %s\n", r.AsOf, r.AppliedEdits, chain)
	}
	fmt.Fprintf(&b, "- **Text source:** `%s` — %s\n", r.Provenance, r.ProvenanceNote)
	fmt.Fprintf(&b, "- **Dataset:** %s\n", r.Mirror)
	if r.Amendments != "" {
		fmt.Fprintf(&b, "- **Later documents naming this one (per its own metadata):** %s\n", r.Amendments)
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "> **Warning:** %s\n", w)
	}
	if r.Path != "" {
		fmt.Fprintf(&b, "- **Stored at:** `%s`\n", r.Path)
	}

	fmt.Fprintf(&b, "\n### Text\n\n%s\n", textBlock(r.Text))
	if r.Truncated {
		fmt.Fprintf(&b, "\n*[truncated at %d characters; ask again with a larger max_chars or a narrower address]*\n", DefaultMaxChars)
	}
	return b.String()
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
func (s *Server) find(doc string) (lexdania.Entry, error) {
	id := strings.TrimSpace(doc)
	if id == "" {
		return lexdania.Entry{}, fmt.Errorf("no document given: pass an ELI path (eli/lta/2019/1399) or an accession (A20191399070)")
	}
	if i := strings.Index(id, "retsinformation.dk"); i >= 0 {
		id = id[i+len("retsinformation.dk"):]
	}
	id = strings.Trim(id, "/")
	id = strings.TrimPrefix(id, "eli/")
	id = strings.TrimSuffix(id, "/xml")

	if !strings.Contains(id, "/") {
		acc := strings.ToUpper(id)
		if e, ok := s.archive.Find(acc); ok {
			return e, nil
		}
		return lexdania.Entry{}, fmt.Errorf("accession %q is not in the mirror (%d documents indexed)", acc, s.archive.Len())
	}

	candidates := s.byELI[id]
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
		return lexdania.Entry{}, fmt.Errorf("%q matches %d documents in the mirror, not one: %s. Re-ask with one of those accession numbers — folketingssager store every stage of a bill under the same ELI path",
			id, len(candidates), strings.Join(names, ", "))
	}
	return lexdania.Entry{}, fmt.Errorf("%q is not in the mirror (%d documents indexed). The path is <medium>/<year>/<number>, e.g. lta/2019/1399", id, s.archive.Len())
}

// citation renders the document the way it is cited in Danish practice.
func citation(e lexdania.Entry) string {
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
