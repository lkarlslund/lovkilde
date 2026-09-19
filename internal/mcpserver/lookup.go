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

	// Provenance is "xml", "html" or "none" — lexdania.TextSource, widened to a
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

	Text      string
	Truncated bool
	Warnings  []string

	// HTML is the same content as Text, marked up for a browser: the same walk in lexdoc, with
	// an id per citable thing so an address can be linked to. It is escaped where it is built,
	// not here, and it is not what the MCP tools serve — those answer in Text. Empty when the
	// answer was built by the as-of path, which still renders through the tree engine.
	HTML string

	// Contents is the document's own table of contents, with the ids HTML gave them.
	Contents []ContentsEntry

	// Anchor is where HTML put the provision this request addressed, empty when the address
	// named nothing or no address was given.
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
		Language:  req.Language,
	}

	if req.AsOf != "" {
		return s.lookupAsOf(req, entry, res)
	}

	m, path, err := s.model(entry)
	if err != nil {
		return nil, err
	}
	res.Path = path
	res.provenance(m)
	if w := s.artifactStaleness(); w != "" {
		res.Warnings = append(res.Warnings, w)
	}
	res.noteAmendments(m, s.horizon().Now)

	// The text comes from the model, so an answer can say what the thing it found *is*: a
	// chapter, a provision, one subsection of it. The flat section list cannot tell those
	// apart — it carries whatever the publisher's elements happened to be — and its
	// subsection addressing is off by one in places, which is a citation error waiting to be
	// quoted.
	addr := lexdoc.ParseAddress(req.Address)
	if req.Address != "" {
		body, ok := m.Content.MarkdownProvisions(addr)
		if !ok {
			return nil, fmt.Errorf("%s: nothing matches %q among %d provisions. Addresses are read in Danish citation form (§ 5, stk. 2); entry-into-force and transitional material is deliberately not addressable because its numbering restarts",
				entry.Accession, req.Address, len(m.Content.Provisions()))
		}
		res.Text = body
		if wantHTML {
			res.HTML, _ = m.Content.HTMLProvisions(addr)
			res.Anchor = m.Content.AnchorFor(addr)
		}
	} else {
		res.Text = m.Content.Markdown()
		if wantHTML {
			res.HTML = m.Content.HTML()
		}
	}
	if wantHTML {
		for _, line := range m.Content.Contents() {
			res.Contents = append(res.Contents, ContentsEntry{ID: line.ID, Label: line.Label, Level: line.Level})
		}
	}
	res.Warnings = append(res.Warnings, m.Diagnostics.Warnings...)
	if !wantHTML {
		res.truncate(req.MaxChars)
	}
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
	res.provenance(view.Doc.Model())

	if req.Address != "" {
		sec := view.Section(req.Address)
		if sec == nil {
			return nil, fmt.Errorf("%s as of %s: nothing matches %q", entry.Accession, res.AsOf, req.Address)
		}
		res.Text = lexdania.DisplayText(sec.Node)
	} else {
		res.Text = view.Text()
	}
	res.warnChain(view)
	res.truncate(req.MaxChars)
	return res, nil
}

// warnChain states that a consolidated text is not the whole answer, and which of the two
// reasons applies. Naming the wrong one is not cosmetic: "not in the mirror" sends the reader to
// collect more documents, while a refused edit is our own limitation that collecting more
// documents cannot repair. BR18 has the second and used to be reported as the first.
func (r *LookupResult) warnChain(view *lexdania.View) {
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
	if r.RefusedEdits == 0 {
		return
	}
	if danish {
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"%d af ændringerne i kæden kunne ikke placeres i teksten. Hvor en af dem angår denne bestemmelse, kan teksten stadig vise den tidligere ordlyd; spørg med address= for at se én bestemmelse ad gangen.",
			r.RefusedEdits))
		return
	}
	r.Warnings = append(r.Warnings, fmt.Sprintf(
		"%d of the changes made by this law's amending acts could not be placed in the text, so where one of them applied, this text may still show the older wording. Refused because: %s",
		r.RefusedEdits, refusalReasons(view.Unresolved)))
}

// refusalReasons says in one line why changes were refused, most frequent class first. A count
// alone only says the text may be stale; the classes say whom to distrust — an address that no
// longer resolves is a provision the chain moved, while wording no pattern recognised is a kind
// of change the engine has never placed.
//
// Reasons are grouped with any quoted address removed, because "§ 21, stk. 2" not found" and
// "§ 523, stk. 2" not found" are one class of failure wearing two addresses.
func refusalReasons(refused []lexdania.UnresolvedEdit) string {
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
		switch {
		case r.AsOf != "":
			// This answer is already the text as amended, which is what the sentence recommends.
		case strings.EqualFold(r.Language, "da"):
			r.AmendmentsNote = fmt.Sprintf(
				"Ifølge dokumentets egne metadata ændres det af %s. Teksten her er denne accession, som den er publiceret; spørg med as_of=%s for den tekst, der gjaldt på det tidspunkt.",
				r.Amendments, now.Format("2006-01-02"))
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
func (s *Server) model(entry lexdania.Entry) (*lexdoc.Document, string, error) {
	if s.docs != nil {
		m, err := s.docs.Document(entry.Accession)
		if err != nil {
			return nil, "", fmt.Errorf("%s is indexed but not in the compiled artifact (%s): %w. Rebuild it with `retsin artifact build`",
				entry.Accession, s.docs.Artifact().Path(), err)
		}
		return m, s.docs.Artifact().Path(), nil
	}
	doc, path, ok := s.archive.Open(entry.Accession)
	if !ok {
		return nil, "", fmt.Errorf("%s is indexed but could not be read from %s: the file may be mid-write, re-run", entry.Accession, entry.Path)
	}
	return doc.Model(), path, nil
}

// provenance records which text this is, in the words a client will read. It reads the model's own
// provenance rather than the reader's, because the same answer is served from the artifact and from
// a parse, and the distinction that matters to a reader — whose text is this — is a property of the
// document, not of how it was read.
func (r *LookupResult) provenance(m *lexdoc.Document) {
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
		return lexdania.Entry{}, fmt.Errorf("no document given: pass an ELI path (eli/lta/2019/1399) or an accession (B20190139905)")
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
