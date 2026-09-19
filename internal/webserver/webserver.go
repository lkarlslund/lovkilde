// Package webserver serves a human being over the answers the MCP server gives.
//
// It owns no data and no logic. Documents are opened through mcpserver — the same path the
// tools take — so a page and an answer cannot disagree about what a provision says. Two things
// are true only of a browser and so live here: the content arrives as the HTML the renderer
// built (escaped where it was built, not here), and the links between things are followed
// rather than quoted.
//
// # Routes
//
//	/                 search box, facets, what the server holds
//	/search           results, each linking to the provision it matched
//	/dok              one document: table of contents, text, related acts
//	/browse           a listing by type and year
//	/status           plain text, for curl
//	/mcp              the MCP endpoint, so one port serves both audiences
//
// There is no /api. The machine-readable forms are what the MCP tools return; a page asks the
// Go methods directly.
package webserver

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lkarlslund/retsinformation/internal/mcpserver"
)

// pageSize is how many browse rows one page carries. It is a page's worth of scanning for a
// person, not a limit on the dataset: the total is always shown.
const pageSize = 200

// Handler is one server's web front end. It holds no state of its own.
type Handler struct {
	srv  *mcpserver.Server
	tmpl map[string]*template.Template
}

// New returns the handler for a server that already has its archive, index, and artifact.
func New(srv *mcpserver.Server) *Handler {
	return &Handler{srv: srv, tmpl: parseTemplates()}
}

// ServeHTTP routes the paths documented on the package.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/":
		h.index(w, r)
	case "/search":
		h.search(w, r)
	case "/dok":
		h.document(w, r)
	case "/browse":
		h.browse(w, r)
	case "/status":
		h.status(w, r)
	case "/mcp":
		h.srv.MCPHandler().ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// ExecuteTemplate on "layout", not Execute on the entry: parseTemplates keeps one
	// template set per page, and its map value is the *content* template that set.New
	// returned. Calling Execute on it renders the block alone — no doctype, no head, no
	// stylesheet, no header or footer — which is how a fully written style sheet ended up
	// never reaching a browser. TestEveryPageCarriesItsLayout keeps that from being invisible.
	if err := h.tmpl[name].ExecuteTemplate(w, "layout", data); err != nil {
		// The header is already out, so say it in the body too rather than fail silently.
		fmt.Fprintf(w, "\n<p class=bad>Fejl under visning: %v", err)
	}
}

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	// Facets come from a one-row browse, which walks the archive's metadata in memory: the
	// counts are what the listing can actually filter by, not a separate promise.
	facets := h.srv.Browse(mcpserver.BrowseRequest{Limit: 1})
	h.render(w, "index", map[string]any{
		"PageTitle": "Søg i dansk lovgivning",
		"Status":    h.srv.Status(),
		"Search":    h.srv.HasSearch(),
		"Kinds":     facets.Kinds,
		"Years":     facets.Years,
		"Total":     facets.Total,
		"Queries":   indexQueries,
	})
}

// indexQueries are examples worth one click each, and each one is an operator rather than a word:
// a front page whose examples find nothing teaches a reader that the search is broken. All three
// were run against the whole mirror before being written here — "forældrepension" and "æresgæld"
// were two that were dropped for returning nothing at all, which is a corpus fact and not a bug,
// and not something to hand a first-time reader.
var indexQueries = []struct{ Query, Label string }{
	{`"præstegæld" AND skifte`, "to ord, samme lov"},
	{`"ubetinget råderet"`, "netop den talemåde"},
	{`pension AND bolig -barn`, "kræv ét ord, udeluk et andet"},
}

// searchPage is the search form with its results.
type searchPage struct {
	// PageTitle is the browser tab's text. It is not called Title because browsePage.Title
	// is the reader's own title query, and a template that reads one word for two things
	// eventually reads the wrong one.
	PageTitle string
	Query     string
	Kind      string
	Since     string
	Until     string
	Hits      []hitView
	Total     int
	Mirror    string
	Kinds     []mcpserver.Facet
	Warn      []string
	HasQuery  bool
	NoIndex   bool
}

type hitView struct {
	Title      string
	Address    string
	Snippet    string
	Provenance string
	Link       string
	Requested  int
	Covered    int
	Gap        string
	ELI        string
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	req := mcpserver.SearchRequest{
		Query: q,
		Kind:  r.URL.Query().Get("kind"),
		Since: yearOf(r.URL.Query().Get("since")),
		Until: yearOf(r.URL.Query().Get("until")),
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	page := searchPage{
		Query:     q,
		Kind:      req.Kind,
		Since:     r.URL.Query().Get("since"),
		Until:     r.URL.Query().Get("until"),
		HasQuery:  q != "",
		Kinds:     h.srv.Browse(mcpserver.BrowseRequest{Limit: 1}).Kinds,
		PageTitle: "Søgning",
	}
	if q != "" {
		page.PageTitle = "Søgning efter " + q
	}
	res, err := h.srv.Search(req)
	if err != nil {
		// A server without an index answers with the reason, and the page offers browsing
		// instead of an empty result list.
		page.NoIndex = !h.srv.HasSearch()
		page.Warn = []string{err.Error()}
		h.render(w, "search", page)
		return
	}
	page.Mirror = res.Mirror
	page.Total = len(res.Hits)
	page.Warn = res.Warnings
	for _, hit := range res.Hits {
		ref := hit.Reference
		if ref == "" {
			// Reference is the unique handle: a proposal stores every stage under one ELI,
			// so a link has to carry the accession when there is no ELI of its own.
			ref = hit.ELI
		}
		page.Hits = append(page.Hits, hitView{
			Title:      hit.Citation,
			Address:    hit.Address,
			Snippet:    hit.Snippet,
			Provenance: hit.Provenance,
			Link:       documentLink(ref, hit.Address),
			Requested:  hit.Requested,
			Covered:    hit.Covered,
			Gap:        gapText(hit.Gap),
			ELI:        hit.ELI,
		})
	}
	h.render(w, "search", page)
}

// gapText says the measured distance in words rather than showing -1 for "not graded".
func gapText(gap int) string {
	switch {
	case gap < 0:
		return ""
	case gap == 0:
		return "samme bestemmelse"
	default:
		return fmt.Sprintf("%d ord fra hinanden", gap)
	}
}

// documentPage is one document as a page.
type documentPage struct {
	// PageTitle is the browser tab's text: the document's own citation, with the address
	// in front when the reader arrived at one provision.
	PageTitle      string
	Citation       string
	Accession      string
	ELI            string
	Kind           string
	Year           string
	Status         string
	Provenance     string
	ProvenanceNote string
	Mirror         string
	Warnings       []string
	Body           template.HTML
	PlainText      string
	Contents       []contentsLink
	Anchor         string
	Addr           string
	Groups         []relatedGroupView
	FullLink       string
}

// contentsLink is one line of the table of contents with the address of the page that holds it.
//
// A narrowed view — a reader who arrived at one provision — carries the whole document's contents,
// because a provision without its law's shape is a fragment with no context. That means most of
// those links do not point into the page being read, so they are given the URL of the page that
// does hold them. A contents list whose links land on the current page and find nothing is the
// specific way a legal reader is misled about where a rule is, and the real mirror showed 522 of
// them on the first afternoon this was checked.
type contentsLink struct {
	ID    string
	Label string
	Level int
	Href  string
}

type relatedGroupView struct {
	Name  string
	Items []relatedItemView
}

type relatedItemView struct {
	Label string
	Href  string
	Held  bool
	Date  string
}

func (h *Handler) document(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ref := q.Get("ref")
	addr := q.Get("addr")
	if ref == "" {
		http.Error(w, "et dokument skal peges på med ?ref=…", http.StatusBadRequest)
		return
	}
	// No character cap: a page shows the document whole, which is the difference between this path
	// and the tool's (see mcpserver.Server.lookup).
	res, err := h.srv.LookupPage(mcpserver.LookupRequest{
		Document: ref, Address: addr, AsOf: q.Get("as_of"),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	page := documentPage{
		Citation:       res.Citation,
		Accession:      res.Accession,
		ELI:            res.ELIURL,
		Kind:           res.Kind,
		Year:           strconv.Itoa(res.Year),
		Status:         res.Status,
		Provenance:     res.Provenance,
		ProvenanceNote: res.ProvenanceNote,
		Mirror:         res.Mirror,
		Warnings:       res.Warnings,
		Anchor:         res.Anchor,
		Addr:           addr,
		FullLink:       documentLink(orString(res.Accession, res.ELIURL), ""),
	}
	page.PageTitle = res.Citation
	if addr != "" {
		// A tab that says only the law's title is useless with twenty provision tabs open.
		page.PageTitle = addr + " — " + res.Citation
	}
	// The renderer escapes everything it takes from the document on the way in, so the body is
	// marked safe as a whole; TestHTMLSaysWhatPlainTextSays and
	// TestHTMLEscapesTextThatLooksLikeMarkup are what that rests on.
	page.Body = template.HTML(res.HTML)
	if res.HTML == "" {
		// The as-of view is still built by the amendment engine, which has no HTML mode, so it
		// is shown as text rather than pretending to be marked up.
		page.PlainText = res.Text
	}
	anchorBase := ""
	if addr != "" {
		// The narrowed page holds one part, so its contents list has to link forward to the page
		// that holds the rest.
		anchorBase = page.FullLink
	}
	for _, line := range res.Contents {
		page.Contents = append(page.Contents, contentsLink{
			ID: line.ID, Label: line.Label, Level: line.Level, Href: anchorBase + "#" + line.ID,
		})
	}
	for _, g := range h.related(res) {
		view := relatedGroupView{Name: g.Name}
		for _, item := range g.Items {
			view.Items = append(view.Items, relatedItemView{
				Label: item.Title,
				Href:  documentLink(item.Reference, ""),
				Held:  item.Held,
				Date:  item.Effective,
			})
		}
		if len(view.Items) > 0 {
			page.Groups = append(page.Groups, view)
		}
	}
	h.render(w, "document", page)
}

// related asks for the same law's other links, keyed on what the lookup identified. An
// accession is preferred because it is unique; the ELI URL is what a document without one has.
func (h *Handler) related(res *mcpserver.LookupResult) []mcpserver.RelatedGroup {
	if res.Accession != "" {
		if groups, err := h.srv.Related(res.Accession); err == nil {
			return groups
		}
	}
	groups, err := h.srv.Related(res.ELIURL)
	if err != nil {
		return nil
	}
	return groups
}

// browsePage is the listing with its filters and page links.
type browsePage struct {
	// PageTitle is the browser tab's text; Title below is the reader's own title query.
	PageTitle string
	Items     []browseRow
	Total     int
	Offset    int
	Kind      string
	Year      string
	Title     string
	Kinds     []mcpserver.Facet
	Years     []mcpserver.Facet
	Pages     []pageLink
	From      int
	To        int
}

type browseRow struct {
	Link   string
	Title  string
	Kind   string
	Year   string
	Number string
	Type   string
}

type pageLink struct {
	Label   string
	Href    string
	Current bool
}

func (h *Handler) browse(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := mcpserver.BrowseRequest{
		Kind:   q.Get("kind"),
		Year:   yearOf(q.Get("year")),
		Title:  q.Get("title"),
		Offset: offsetOf(q.Get("offset")),
		Limit:  pageSize,
	}
	res := h.srv.Browse(req)
	page := browsePage{
		Total:  res.Total,
		Offset: res.Offset,
		Kind:   res.Kind,
		Year:   yearText(res.Year),
		Title:  res.Title,
		Kinds:  res.Kinds,
		Years:  res.Years,
		Pages:  pages(res),
		From:   min(res.Offset+1, max(res.Total, 1)),
		To:     min(res.Offset+len(res.Items), res.Total),
	}
	page.PageTitle = "Alle dokumenter"
	switch {
	case res.Kind != "":
		page.PageTitle = "Dokumenter af typen " + res.Kind
	case res.Year != 0:
		page.PageTitle = "Dokumenter fra " + yearText(res.Year)
	}
	for _, item := range res.Items {
		page.Items = append(page.Items, browseRow{
			Link:   documentLink(item.Reference, ""),
			Title:  item.Title,
			Kind:   item.Kind,
			Year:   strconv.Itoa(item.Year),
			Number: strconv.Itoa(item.Number),
			Type:   item.DocumentType,
		})
	}
	h.render(w, "browse", page)
}

func pages(res *mcpserver.BrowseResult) []pageLink {
	if res.Total <= pageSize {
		return nil
	}
	out := []pageLink{{
		Label:   fmt.Sprintf("1–%d", min(pageSize, res.Total)),
		Href:    browseQueryArgs(res.Kind, res.Year, res.Title, 0),
		Current: res.Offset == 0,
	}}
	for start := pageSize; start < res.Total; start += pageSize {
		out = append(out, pageLink{
			Label:   fmt.Sprintf("%d–%d", start+1, min(start+pageSize, res.Total)),
			Href:    browseQueryArgs(res.Kind, res.Year, res.Title, start),
			Current: start == res.Offset,
		})
	}
	return out
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, h.srv.Status())
}

// documentLink is the one way a link to a provision gets built, so every link in the interface
// carries its address the same way.
func documentLink(ref, addr string) string {
	values := url.Values{}
	values.Set("ref", ref)
	if addr != "" {
		values.Set("addr", addr)
	}
	return "/dok?" + values.Encode()
}

func browseQueryArgs(kind string, year int, title string, offset int) string {
	values := url.Values{}
	if kind != "" {
		values.Set("kind", kind)
	}
	if year > 0 {
		values.Set("year", strconv.Itoa(year))
	}
	if title != "" {
		values.Set("title", title)
	}
	if offset > 0 {
		values.Set("offset", strconv.Itoa(offset))
	}
	return "/browse?" + values.Encode()
}

// yearOf reads a year field, treating anything unparseable as "not filtered" rather than as an
// error: a half-typed year in a form is a person still working, not a bad request.
func yearOf(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1000 || n > 9999 {
		return 0
	}
	return n
}

func offsetOf(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func yearText(year int) string {
	if year == 0 {
		return ""
	}
	return strconv.Itoa(year)
}

func orString(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}
