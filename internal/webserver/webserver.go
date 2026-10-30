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
//	/doc              one document: table of contents, text, related acts
//	/browse           a listing by type and year
//	/status           what is served and how current it is; plain text for curl
//	/settings    how often the server checks for a newer dataset and software, "check now",
//	                  and the optional API key
//	/access           where a browser gives the API key, while one is set; nothing else answers
//	                  without it
//	/eula          the terms of use, which a browser accepts before any other page answers
//	/mcp              the MCP endpoint, so one port serves both audiences; it asks for the terms
//	                  through its own tool
//
// There is no /api. The machine-readable forms are what the MCP tools return; a page asks the
// Go methods directly.
package webserver

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/lkarlslund/lovkilde/internal/apikey"
	"github.com/lkarlslund/lovkilde/internal/checks"
	"github.com/lkarlslund/lovkilde/internal/disclaimer"
	"github.com/lkarlslund/lovkilde/internal/mcpserver"
	"github.com/lkarlslund/lovkilde/internal/updates"
)

// pageSize is how many browse rows one load carries; scrolling to the end brings the next as
// many. It is a screenful of scanning for a person, not a limit on the dataset: the total is
// always shown.
const pageSize = 100

// Handler is one server's web front end. It holds no state of its own.
type Handler struct {
	srv  *mcpserver.Server
	tmpl map[string]*template.Template

	// Checks are the server's scheduled checks, shown and set on the settings page; nil serves
	// no settings. Updates is what they found, and the updates started from the settings page:
	// while a newer program or newer law texts are published, every page says so. Nothing is
	// downloaded until someone asks.
	Checks  *checks.Checks
	Updates Updater

	// Key is the optional API key, set on the settings page; while one is set, nothing but the
	// key page answers without it. Exposed says the server listens beyond this machine, which
	// puts a warning next to the settings link for as long as no key is set.
	Key     *apikey.Key
	Exposed bool
}

// New returns the handler for a server that already has its archive, index, and artifact.
func New(srv *mcpserver.Server) *Handler {
	h := &Handler{srv: srv}
	h.tmpl = parseTemplates(template.FuncMap{"softwareUpdate": h.softwareUpdate, "exposedWithoutKey": h.exposedWithoutKey})
	return h
}

// softwareUpdate is the notice every page shows while a newer program or newer law texts are
// published, or while the published texts need a newer program, or nil.
func (h *Handler) softwareUpdate() *softwareNotice {
	if h.Updates == nil {
		return nil
	}
	n := &softwareNotice{}
	if h.Updates.Can(updates.App) {
		st := h.Updates.State(updates.App)
		n.URL = h.Updates.SoftwareURL()
		if st.Check.Last.Available {
			n.Behind, n.Version, n.Downloaded, n.Fetching = st.Check.Last.Newer, st.Check.Last.Version, st.Check.Last.Downloaded, st.Running
		}
	}
	if h.Updates.Can(updates.Dataset) {
		st := h.Updates.State(updates.Dataset)
		n.Data = st.Check.Last.Upgrade
		if st.Check.Last.Available {
			n.Texts, n.TextsFetching = st.Check.Last.Version, st.Running
			n.TextsNew = true
		}
	}
	if !n.Data && n.Behind == 0 && !n.TextsNew {
		return nil
	}
	return n
}

// Updater is what the pages ask of the server's updates (internal/updates).
type Updater interface {
	Can(what string) bool
	State(what string) updates.State
	Start(ctx context.Context, what string) (string, error)
	SoftwareURL() string
}

type softwareNotice struct {
	Behind     int
	Version    string // the newer program's name, rNNNNN
	Downloaded bool   // the newer program is downloaded
	Fetching   bool   // the newer program is being downloaded
	Data       bool   // the published texts need a newer program
	URL        string

	TextsNew      bool   // newer law texts are published
	Texts         string // when they were published
	TextsFetching bool   // they are being downloaded
}

// ServeHTTP routes the paths documented on the package.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == accessPath {
		h.access(w, r)
		return
	}
	if !h.authorized(r) {
		h.requireKey(w, r)
		return
	}
	switch r.URL.Path {
	case termsPath:
		h.terms(w, r)
		return
	case "/mcp":
		h.srv.MCPHandler().ServeHTTP(w, r)
		return
	}
	if !h.acceptedTerms(r) {
		h.requireTerms(w, r)
		return
	}
	switch r.URL.Path {
	case "/":
		h.index(w, r)
	case "/search":
		h.search(w, r)
	case "/doc":
		h.document(w, r)
	case "/browse":
		h.browse(w, r)
	case "/status":
		h.status(w, r)
	case settingsPath:
		h.settings(w, r)
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
		"PageTitle":     "Søg i dansk lovgivning",
		"Status":        h.srv.StatusDA(),
		"Search":        h.srv.HasSearch(),
		"SearchPending": h.srv.SearchPending(),
		"Kinds":         kindToggles(facets.Kinds, nil),
		"Years":         facets.Years,
		"Total":         facets.Total,
		"Queries":       indexQueries,
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
	Since     string
	Until     string

	// Kinds and Statuses are the toggles, each knowing whether it is on. The counts come from
	// the archive's metadata rather than the index, so they describe the mirror, not the
	// subset a text query could reach.
	Kinds    []toggle
	Statuses []toggle
	Sources  []toggle

	// Within narrows to one document, and WithinTitle names it on the page.
	Within      string
	WithinTitle string
	AllLink     string // the same search without the document limit

	Hits     []hitView
	Cited    []citedView // documents the query cites by number, above the ranked hits
	Total    int
	From     int
	To       int
	More     string // the next page's address, or "" at the end
	Filtered bool   // a filter beyond the words is on
	Mirror   string
	Warn     []string
	HasQuery bool
	NoIndex  bool
}

// toggle is one filter choice shown as a switch: the value the form sends, the words the reader
// sees, how many documents carry it, and whether it is on.
type toggle struct {
	Value string
	Label string
	Hint  string
	Count int
	On    bool
}

// citedView is a document the query names by its number.
type citedView struct {
	Title  string
	Label  string // "LOV nr. 1399 af 2019"
	Link   string
	Status string
}

type hitView struct {
	N          int
	Title      string
	DocLink    string
	Address    string
	Snippet    string
	Provenance string
	Link       string
	Requested  int
	Covered    int
	Gap        string
	ELI        string
}

// searchPageSize is how many hits one load brings; scrolling brings the next as many.
const searchPageSize = 20

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	q := strings.TrimSpace(v.Get("q"))
	kinds := nonEmpty(v["kind"])
	statuses := nonEmpty(v["status"])
	req := mcpserver.SearchRequest{
		Query:    q,
		Kinds:    kinds,
		Statuses: statuses,
		Since:    yearOf(v.Get("since")),
		Until:    yearOf(v.Get("until")),
		Within:   strings.TrimSpace(v.Get("in")),
		Offset:   offsetOf(v.Get("offset")),
		Limit:    searchPageSize,
		Language: "da",
	}
	req.Sources = nonEmpty(v["source"])
	facets := h.srv.Browse(mcpserver.BrowseRequest{Limit: 1})
	page := searchPage{
		Query:     q,
		Since:     v.Get("since"),
		Until:     v.Get("until"),
		Kinds:     kindToggles(facets.Kinds, kinds),
		Statuses:  statusToggles(facets.Statuses, statuses),
		Sources:   sourceToggles(h.srv.SourceFacets(), req.Sources),
		Within:    req.Within,
		HasQuery:  q != "",
		PageTitle: "Søgning",
	}
	page.Filtered = len(kinds) > 0 || len(statuses) > 0 || len(req.Sources) > 0 || req.Since > 0 || req.Until > 0 || req.Within != ""
	if req.Within != "" {
		page.WithinTitle = req.Within
		if title, ok := h.srv.Citation(req.Within); ok {
			page.WithinTitle = title
		}
		all := url.Values{}
		for key, vals := range v {
			if key != "in" && key != "offset" && key != "del" {
				all[key] = vals
			}
		}
		page.AllLink = "/search?" + all.Encode()
	}
	if q != "" {
		page.PageTitle = "Søgning efter " + q
	}
	fragment := v.Get("del") == "1"
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
	page.Total = res.Total
	for _, c := range res.Cited {
		label := fmt.Sprintf("nr. %d af %d", c.Number, c.Year)
		if t := strings.Fields(c.DocumentType); len(t) > 0 {
			label = t[0] + " " + label
		}
		page.Cited = append(page.Cited, citedView{Title: c.Title, Label: label, Link: documentLink(c.Reference, ""), Status: c.Status})
	}
	page.Warn = res.Warnings
	page.From = res.Offset + 1
	page.To = res.Offset + len(res.Hits)
	if res.Next > 0 {
		next := url.Values{}
		for key, vals := range v {
			if key != "offset" && key != "del" {
				next[key] = vals
			}
		}
		next.Set("offset", strconv.Itoa(res.Next))
		page.More = "/search?" + next.Encode()
	}
	for i, hit := range res.Hits {
		ref := hit.Reference
		if ref == "" {
			// Reference is the unique handle: a proposal stores every stage under one ELI,
			// so a link has to carry the accession when there is no ELI of its own.
			ref = hit.ELI
		}
		page.Hits = append(page.Hits, hitView{
			N:          res.Offset + i + 1,
			DocLink:    documentLink(ref, ""),
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
	if fragment {
		h.renderFragment(w, "search", "hits", page)
		return
	}
	h.render(w, "search", page)
}

// renderFragment writes one named block of a page without its layout: the next screenful of a
// list, which the page's script appends where the previous one ended.
func (h *Handler) renderFragment(w http.ResponseWriter, page, block string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl[page].ExecuteTemplate(w, block, data); err != nil {
		fmt.Fprintf(w, "\n<p class=bad>Fejl under visning: %v", err)
	}
}

// kindNames are the Danish names of the register's media, so a toggle says what it holds
// rather than a code only the register's own staff would read.
var kindNames = map[string][2]string{
	"lta":      {"Love og bekendtgørelser", "Lovtidende A"},
	"ltb":      {"Bevillinger", "Lovtidende B"},
	"ltc":      {"Traktater og aftaler", "Lovtidende C"},
	"mt":       {"Vejledninger og skrivelser", "Ministerialtidende"},
	"ft":       {"Lovforslag", "Folketingstidende"},
	"fob":      {"Ombudsmandens udtalelser", "Folketingets Ombudsmand"},
	"retsinfo": {"Øvrige dokumenter", "cirkulærer, afgørelser m.m."},
	"accn":     {"Afgørelser", "ankenævn"},
}

// kindName is the toggle's words for a medium, falling back to the code for one not named.
func kindName(kind string) string {
	if n, ok := kindNames[kind]; ok {
		return n[0]
	}
	return kind
}

// kindOrder puts the media a reader most often means first: law, then the bills that make it,
// then guidance; a medium not listed follows, as the archive counted it.
var kindOrder = []string{"lta", "ft", "mt", "ltc", "retsinfo", "accn", "fob", "ltb"}

func kindToggles(facets []mcpserver.Facet, on []string) []toggle {
	out := make([]toggle, 0, len(facets))
	facets = slices.Clone(facets)
	rank := func(kind string) int {
		if i := slices.Index(kindOrder, kind); i >= 0 {
			return i
		}
		return len(kindOrder)
	}
	slices.SortStableFunc(facets, func(a, b mcpserver.Facet) int { return rank(a.Name) - rank(b.Name) })
	for _, f := range facets {
		t := toggle{Value: f.Name, Label: kindName(f.Name), Count: f.Count, On: slices.Contains(on, f.Name)}
		if n, ok := kindNames[f.Name]; ok {
			t.Hint = n[1]
		}
		out = append(out, t)
	}
	return out
}

func statusToggles(facets []mcpserver.Facet, on []string) []toggle {
	out := make([]toggle, 0, len(facets))
	for _, f := range facets {
		value := f.Name
		if value == "" {
			value = "unknown"
		}
		selected := slices.ContainsFunc(on, func(s string) bool { return statusShort(s) == statusShort(value) })
		out = append(out, toggle{Value: value, Label: statusShort(value), Count: f.Count, On: selected})
	}
	return out
}

func sourceToggles(facets []mcpserver.Facet, on []string) []toggle {
	out := make([]toggle, 0, len(facets))
	for _, f := range facets {
		out = append(out, toggle{Value: f.Name, Label: provenanceLabel(f.Name), Hint: provenanceExplained(f.Name), Count: f.Count, On: slices.Contains(on, f.Name)})
	}
	return out
}

// nonEmpty drops the blank values a form sends for an unticked choice.
func nonEmpty(values []string) []string {
	var out []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
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
	AmendmentsNote string
	Body           template.HTML
	PlainText      string
	Contents       []contentsLink
	Anchor         string
	Addr           string
	Groups         []relatedGroupView
	FullLink       string

	// Ref is the document's handle for a form that reloads it, DateTool whether the page offers
	// to show the text as of a chosen date, and OriginalLink the page without that date.
	Ref          string
	DateTool     bool
	OriginalLink string

	// Refused are the changes a consolidated text could not take in, and ConsolidateLink the
	// page with the text as amended, offered when the document has been amended since.
	Refused         []refusedView
	AsOf            string
	ConsolidateLink string
	ConsolidateOn   string
}

// refusedView is one change the consolidated text leaves out, as the page shows it.
type refusedView struct {
	ID       string // the list entry's own anchor, which a provision's marker links to
	Act      string
	ActLink  string
	Number   string
	Target   string
	InForce  string
	Reason   string
	Wording  string
	Concerns bool
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
		Document: ref, Address: addr, AsOf: q.Get("as_of"), Language: "da",
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
	page.AmendmentsNote = res.AmendmentsNote
	page.AsOf = res.AsOf
	page.Ref = orString(res.Accession, res.ELIURL)
	// A date is worth offering where amendments are known to exist: on anything else every
	// date shows the same text, and a tool that never changes anything teaches distrust.
	page.DateTool = res.AmendmentsNote != "" || res.AsOf != "" || len(res.RefusedChanges) > 0
	page.OriginalLink = documentLink(page.Ref, addr)
	if res.ConsolidateOn != "" {
		page.ConsolidateOn = res.ConsolidateOn
		page.ConsolidateLink = documentLink(orString(res.Accession, res.ELIURL), addr) + "&as_of=" + url.QueryEscape(res.ConsolidateOn)
	}
	page.PageTitle = res.Citation
	if addr != "" {
		// A tab that says only the law's title is useless with twenty provision tabs open.
		page.PageTitle = addr + " — " + res.Citation
	}
	// The renderer escapes everything it takes from the document on the way in, so the body is
	// marked safe as a whole; TestHTMLSaysWhatPlainTextSays and
	// TestHTMLEscapesTextThatLooksLikeMarkup are what that rests on.
	body := res.HTML
	marked := map[string][]string{}
	for i, c := range res.RefusedChanges {
		v := refusedView{
			ID:       fmt.Sprintf("ikke-indarbejdet-%d", i+1),
			Act:      orString(c.ActTitle, c.Act),
			Number:   strings.TrimSuffix(c.Number, "."),
			Target:   c.Target,
			InForce:  c.InForce,
			Reason:   refusalReasonDA(c.Reason),
			Wording:  c.Wording,
			Concerns: c.Concerns,
		}
		if c.Act != "" {
			v.ActLink = documentLink(c.Act, "")
		}
		page.Refused = append(page.Refused, v)
		if c.Anchor != "" {
			marked[c.Anchor] = append(marked[c.Anchor], v.ID)
		}
	}
	page.Body = template.HTML(markRefusedHTML(body, marked))
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

// markRefusedHTML puts a note at the head of each provision a refused change names, linking to
// the change in the list: a reader of § 5 learns there that § 5 may not read as the law does.
// The anchors are the renderer's own ids, and the note is markup this package writes.
func markRefusedHTML(body string, marked map[string][]string) string {
	for anchor, ids := range marked {
		open := fmt.Sprintf("<section id=%q>", anchor)
		at := strings.Index(body, open)
		if at < 0 {
			continue
		}
		links := make([]string, len(ids))
		for i, id := range ids {
			links[i] = fmt.Sprintf(`<a href="#%s">nr. %s</a>`, id, strings.TrimPrefix(id, "ikke-indarbejdet-"))
		}
		note := `<p class="refused-mark">En ændring af denne bestemmelse er ikke indarbejdet (se ` + strings.Join(links, ", ") +
			` under »Ændringer, der ikke er indarbejdet«). Bestemmelsen kan stadig vise den tidligere ordlyd.</p>`
		body = body[:at+len(open)] + note + body[at+len(open):]
	}
	return body
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

	// Status is the register's label the list was narrowed to, TypeCode the publisher-code
	// substring, and Statuses the counts beside the choice.
	Status   string
	TypeCode string
	Statuses []toggle
	Kinds    []toggle
	Years    []mcpserver.Facet
	More     string // the next screenful's address, or "" at the end
	From     int
	To       int
}

type browseRow struct {
	Link   string
	Title  string
	Kind   string
	Year   string
	Number string
	Type   string

	// Status is the register's own claim about the document: "Valid", "Historic", or ""
	// when its payload does not say.
	Status string
}

func (h *Handler) browse(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := mcpserver.BrowseRequest{
		Kind:     q.Get("kind"),
		Year:     yearOf(q.Get("year")),
		Title:    q.Get("title"),
		Status:   q.Get("status"),
		TypeCode: q.Get("type"),
		Offset:   offsetOf(q.Get("offset")),
		Limit:    pageSize,
	}
	res := h.srv.Browse(req)
	page := browsePage{
		Total:    res.Total,
		Offset:   res.Offset,
		Kind:     res.Kind,
		Year:     yearText(res.Year),
		Title:    res.Title,
		Status:   res.Status,
		TypeCode: req.TypeCode,
		Statuses: statusToggles(res.Statuses, nonEmpty([]string{res.Status})),
		Kinds:    kindToggles(res.Kinds, nonEmpty([]string{res.Kind})),
		Years:    res.Years,
		From:     min(res.Offset+1, max(res.Total, 1)),
		To:       min(res.Offset+len(res.Items), res.Total),
	}
	page.PageTitle = "Alle dokumenter"
	switch {
	case res.Kind != "":
		page.PageTitle = kindName(res.Kind)
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
			Status: item.Status,
		})
	}
	if next := res.Offset + len(res.Items); next < res.Total {
		page.More = browseQueryArgs(res.Kind, res.Year, res.Title, res.Status, res.TypeCode, next)
	}
	if q.Get("del") == "1" {
		h.renderFragment(w, "browse", "rows", page)
		return
	}
	h.render(w, "browse", page)
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	if !wantsHTML(r) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, h.srv.StatusDA())
		fmt.Fprintf(w, "\n%s\n", disclaimer.ShortDanish)
		return
	}
	facets := h.srv.Browse(mcpserver.BrowseRequest{Limit: 1})
	h.render(w, "status", map[string]any{
		"PageTitle": "Status",
		"Info":      h.srv.StatusInfo(),
		"Kinds":     kindToggles(facets.Kinds, nil),
		"Checks":    h.checkViews(),
		"Short":     disclaimer.ShortDanish,
		"Terms":     disclaimer.Terms,
	})
}

// wantsHTML says whether the request comes from a browser rather than curl: /status answers a
// browser with a page and everything else with plain text.
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// documentLink is the one way a link to a provision gets built, so every link in the interface
// carries its address the same way.
func documentLink(ref, addr string) string {
	values := url.Values{}
	values.Set("ref", ref)
	if addr != "" {
		values.Set("addr", addr)
	}
	return "/doc?" + values.Encode()
}

func browseQueryArgs(kind string, year int, title, status, typeCode string, offset int) string {
	values := url.Values{}
	if kind != "" {
		values.Set("kind", kind)
	}
	if status != "" {
		values.Set("status", status)
	}
	if typeCode != "" {
		values.Set("type", typeCode)
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
