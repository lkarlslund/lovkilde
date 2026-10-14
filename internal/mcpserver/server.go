package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lkarlslund/lovkilde/internal/chain"
	"github.com/lkarlslund/lovkilde/internal/compiled"
	"github.com/lkarlslund/lovkilde/internal/disclaimer"
	"github.com/lkarlslund/lovkilde/internal/eula"
	"github.com/lkarlslund/lovkilde/internal/lexdoc"
	"github.com/lkarlslund/lovkilde/internal/library"
	"github.com/lkarlslund/lovkilde/internal/search"
)

// Server is the archive behind an MCP endpoint. It holds an index and nothing else:
// no store, no writer, no client. If it can be changed through this server, it is not
// a mirror any more.
type Server struct {
	// base is the catalogue of the library the server was made with, or nil when it answers from
	// an artifact alone, which brings its own (see corpus). A server is made without one; a test
	// passes one, to serve fixtures without building an artifact.
	base     *catalogueView
	siteBase string
	version  string
	started  time.Time

	// index is the built full-text index, or nil. Search is not advertised when it is
	// nil, because a tool that cannot answer is worse than no tool.
	indexBuilding atomic.Bool

	// served is the artifact and index answered from, swapped whole (SwapCorpus).
	served atomic.Pointer[corpus]

	// MaxChars caps every answer when the request does not ask for a different cap.
	// Zero keeps the package default.
	MaxChars int

	// nowFunc answers "what time is it" for the horizon. It is a field rather than a call to
	// time.Now so a test can pin the clock: several of this package's claims are about a date
	// measured from today, and a test that hard-codes today is correct for exactly one day —
	// which is how two horizon tests came to disagree with each other the moment the date moved.
	nowFunc func() time.Time

	// mcpHandler is the built HTTP transport, made once on first use. See MCPHandler for why it
	// has to be the same one every time, and why the building is guarded rather than checked.
	mcpOnce    sync.Once
	mcpHandler http.Handler

	// terms is which MCP sessions have been shown the terms of use; see termsTool.
	terms terms

	// EULA is whether the terms are accepted on this server, through the website or the MCP
	// tool; nil keeps acceptance to each MCP session.
	EULA *eula.Store
}

// clock is the server's idea of now: time.Now, unless a test pinned it.
func (s *Server) clock() time.Time {
	if s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

// New makes a server. It answers once it is given an artifact (UseArtifact); a library passed here
// answers until then, and is what tests serve fixtures from.
func New(a chain.Library, siteBase, version string) *Server {
	s := &Server{
		siteBase: strings.TrimSuffix(siteBase, "/"),
		version:  version,
		started:  time.Now(),
	}
	if a != nil {
		s.base = newCatalogueView(a)
	}
	return s
}

// catalogueView is a library with the lookups a server needs over its catalogue, built once per
// library: documents by ELI path, and the count by medium the status line prints.
type catalogueView struct {
	lib   chain.Library
	byELI map[string][]chain.Entry
	kinds countByKind
}

func newCatalogueView(lib chain.Library) *catalogueView {
	c := &catalogueView{lib: lib, byELI: make(map[string][]chain.Entry, lib.Len()), kinds: countByKind{}}
	for _, e := range lib.Entries() {
		// Keyed without the eli/ segment, which is how callers write the path after
		// copying it from a URL; find() strips whichever form it was given.
		//
		// A slice, not a single entry, because an ELI path is not unique. A
		// folketingssag stores each stage of a bill under its own accession, and they
		// all derive the same eli/ft/<year>/<number>: 202012L00054, 202013L00054 and
		// 20201BB00054 are all eli/ft/2020/54, with different text. A map of one entry
		// per ELI would silently answer for whichever file was scanned last.
		key := strings.TrimPrefix(e.ELIPath(), "eli/")
		c.byELI[key] = append(c.byELI[key], e)
		c.kinds.add(e.Kind)
	}
	for _, es := range c.byELI {
		// Deterministic order, so an ambiguous path lists its candidates the same way
		// on every run rather than in map order.
		sort.Slice(es, func(i, j int) bool { return es[i].Accession < es[j].Accession })
	}
	return c
}

// cat is the catalogue answered from: the artifact's when one is served, else the library's the
// server was made with.
func (s *Server) cat() *catalogueView {
	if c := s.served.Load(); c != nil && c.cat != nil {
		return c.cat
	}
	if s.base != nil {
		return s.base
	}
	return emptyCatalogue
}

// lib is the library answered from; see cat.
func (s *Server) lib() chain.Library { return s.cat().lib }

// emptyCatalogue answers for a server that has been given nothing to serve yet.
var emptyCatalogue = newCatalogueView(emptyLibrary{})

type emptyLibrary struct{}

func (emptyLibrary) Len() int                        { return 0 }
func (emptyLibrary) Entries() []chain.Entry          { return nil }
func (emptyLibrary) Location() string                { return "nothing" }
func (emptyLibrary) Find(string) (chain.Entry, bool) { return chain.Entry{}, false }
func (emptyLibrary) Versions(a string) ([]chain.Version, error) {
	return nil, fmt.Errorf("%s is not in the library", a)
}
func (emptyLibrary) LawAsOf(a string, _ time.Time) (*chain.View, error) {
	return nil, fmt.Errorf("%s is not in the library", a)
}
func (emptyLibrary) Model(string) (*lexdoc.Document, string, bool) { return nil, "", false }

// ambiguous reports every document stored under the same ELI path, and says whether
// the path actually identifies one document.
func (s *Server) ambiguous(eli string) []chain.Entry {
	es := s.cat().byELI[strings.TrimPrefix(eli, "eli/")]
	if len(es) < 2 {
		return nil
	}
	return es
}

// Status is what the server knows about the mirror it is serving, for the initialize
// instructions and for the lookup header.
func (s *Server) Status() string {
	index := "no search index"
	switch ix := s.idx(); {
	case ix != nil && ix.Built > 0:
		index = "index built " + time.Unix(ix.Built, 0).Format(time.RFC3339)
	case ix != nil:
		index = "index built at an unrecorded time"
	case s.SearchPending():
		index = "search index being built"
	}
	return fmt.Sprintf("%d documents in %s (media: %s), %s, %s",
		s.lib().Len(), s.lib().Location(), s.cat().kinds, index, s.textSource())
}

// StatusDA is Status for the Danish reading pages.
func (s *Server) StatusDA() string {
	index := "intet søgeindeks"
	switch ix := s.idx(); {
	case ix != nil && ix.Built > 0:
		index = "søgeindeks bygget " + time.Unix(ix.Built, 0).Format("2006-01-02 15:04")
	case ix != nil:
		index = "søgeindeks uden byggetidspunkt"
	case s.SearchPending():
		index = "søgeindekset bygges"
	}
	return fmt.Sprintf("%d dokumenter i spejlet (typer: %s), %s, %s",
		s.lib().Len(), s.cat().kinds, index, s.textSourceDA())
}

// textSourceDA is textSource in Danish.
func (s *Server) textSourceDA() string {
	if s.cache() == nil {
		return "teksten læses fra " + s.lib().Location()
	}
	return fmt.Sprintf("teksten læses fra den kompilerede fil (bygget %s)", s.cache().Artifact().Header.Created.Local().Format("2006-01-02 15:04"))
}

// textSource names where a document's text is read from, in the words an operator reads in a log
// line and a client reads in a search answer.
func (s *Server) textSource() string {
	if s.cache() == nil {
		return "text from " + s.lib().Location()
	}
	art := s.cache().Artifact()
	return fmt.Sprintf("text from the compiled artifact at %s (built %s)", art.Path(), art.Header.Created.Format("2006-01-02 15:04:05Z07:00"))
}

// DefaultArtifactCacheBytes is how much payload the serving cache may hold. The bound is in bytes
// because that is what a cache costs: a document's compressed payload is its own size, so the
// budget is arithmetic rather than a guess about struct widths.
const DefaultArtifactCacheBytes = 64 << 20

// UseArtifact serves a compiled artifact: its catalogue, its documents' models and its acts'
// changes are everything an answer is made from.
//
// The artifact has to be this build's own. Its payload is the JSON form of the model's Go types, so
// a file from another revision decodes into today's struct with yesterday's meaning and answers
// questions about a law with fields that no longer say what they said. The refusal therefore names
// which side is stale, since the operator can rebuild either one and only the artifact's timestamp
// says which needs it.
func (s *Server) UseArtifact(path string, cacheBytes int64) error {
	p, err := s.PrepareArtifact(path, cacheBytes)
	if err != nil {
		return err
	}
	s.SwapCorpus(p, s.idx())
	return nil
}

// PrepareArtifact opens an artifact and makes the checks serving it needs, without serving it:
// the document model must be this build's. A server that is answering from one artifact prepares
// the next this way.
func (s *Server) PrepareArtifact(path string, cacheBytes int64) (*Prepared, error) {
	if cacheBytes <= 0 {
		cacheBytes = DefaultArtifactCacheBytes
	}
	c, err := compiled.OpenCached(path, cacheBytes)
	if err != nil {
		return nil, err
	}
	if err := c.Artifact().CheckModel(); err != nil {
		c.Close()
		return nil, err
	}
	// The artifact's catalogue is the library answers come from: lookups, browsing and laws as of
	// a date. An artifact whose catalogue is empty — a test's small build — leaves the catalogue to
	// the library the server was made with.
	lib, err := library.Open(c)
	if err != nil {
		c.Close()
		return nil, err
	}
	p := &Prepared{Cache: c}
	switch {
	case lib.Len() > 0:
		p.cat = newCatalogueView(lib)
	case s.base == nil:
		c.Close()
		return nil, fmt.Errorf("%s has no catalogue, so there is nothing to look a document up in: rebuild it with `lovkilde-update artifact build`", path)
	}
	return p, nil
}

// corpus is what the server answers from: the artifact text is read from and the index cut from
// it. It is replaced whole, so no request sees one artifact's text with another's index.
type corpus struct {
	docs  *compiled.Cache
	index *search.Index
	cat   *catalogueView // the artifact's catalogue, or nil to answer from the server's library
}

// Prepared is an artifact opened and checked for serving: its document cache and the library its
// catalogue makes.
type Prepared struct {
	Cache *compiled.Cache
	cat   *catalogueView
}

// Artifact is the prepared artifact itself.
func (p *Prepared) Artifact() *compiled.Artifact { return p.Cache.Artifact() }

// Close releases a prepared artifact that will not be served after all.
func (p *Prepared) Close() error { return p.Cache.Close() }

// SwapCorpus makes an artifact, its catalogue and its index the ones served, together. The
// artifact replaced is closed a minute later, when the requests that were reading it have finished.
func (s *Server) SwapCorpus(p *Prepared, index *search.Index) {
	old := s.served.Swap(&corpus{docs: p.Cache, index: index, cat: p.cat})
	if old != nil && old.docs != nil && old.docs != p.Cache {
		retired := old.docs
		time.AfterFunc(time.Minute, func() { retired.Close() })
	}
}

// cache is the artifact text is served from, or nil before one is given.
func (s *Server) cache() *compiled.Cache {
	if c := s.served.Load(); c != nil {
		return c.docs
	}
	return nil
}

// ArtifactIdentity names the artifact text is served from (compiled.Artifact.Identity), or "" before
// one is given: what an index built for this server must record as its source.
func (s *Server) ArtifactIdentity() string {
	if s.cache() == nil {
		return ""
	}
	return s.cache().Artifact().Identity()
}

// indexMismatch says when the search index was built from a different artifact than the one
// serving text. The two then disagree about which provisions exist: a hit can name an address
// the served text no longer has, or miss one it does. Answers still come, with this said.
func (s *Server) indexMismatch(danish bool) string {
	if s.cache() == nil || s.idx() == nil || !strings.HasPrefix(s.idx().Source, "artifact ") {
		return ""
	}
	art := s.cache().Artifact()
	if s.idx().Source == search.SourceFor(art.Identity()) {
		return ""
	}
	if danish {
		return fmt.Sprintf("søgeindekset er bygget fra en anden kompileret fil (%s) end den, teksten læses fra (%s): byg indekset igen med `lovkilde index build`",
			strings.TrimPrefix(s.idx().Source, "artifact "), art.Header.Created.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf("the search index was built from a different compiled artifact (%s) than the one text is served from (%s): rebuild the index with `lovkilde index build`",
		strings.TrimPrefix(s.idx().Source, "artifact "), art.Header.Created.UTC().Format(time.RFC3339))
}

// unitSource is where a search hit's text comes from: the artifact served, or the library the
// server was made with before one is. The index itself stores no text either way.
func (s *Server) unitSource() search.Documents {
	if s.cache() != nil {
		return artifactDocs{cache: s.cache()}
	}
	return libraryDocs{lib: s.lib()}
}

// libraryDocs reads a hit's document from a library.
type libraryDocs struct{ lib chain.Library }

func (l libraryDocs) Document(meta search.DocMeta) (*lexdoc.Document, error) {
	m, _, ok := l.lib.Model(meta.Accession)
	if !ok {
		return nil, fmt.Errorf("%s is not readable in %s", meta.Accession, l.lib.Location())
	}
	return m, nil
}

// artifactDocs reads a hit's document out of the compiled artifact. The cache hands out a freshly
// decoded model per read — it caches payloads, not documents — so two searches running at once
// cannot edit each other's copy of a law while grading proximity.
type artifactDocs struct{ cache *compiled.Cache }

func (a artifactDocs) Document(meta search.DocMeta) (*lexdoc.Document, error) {
	if meta.Accession == "" {
		return nil, fmt.Errorf("hit for %s carries no accession, so the artifact has no key to look it up by", meta.ELI)
	}
	return a.cache.Document(meta.Accession)
}

// lookupInput is the tool's argument object. The jsonschema tag value *is* the
// description a client sees: github.com/google/jsonschema-go reads that one tag and no
// other, and derives "required" from the absence of omitempty. Writing a description
// anywhere else drops it in silence, which TestToolSchemasDescribeEveryArgument is here
// to make impossible. These strings are written for someone who has never read this
// repository, and every example in them is an address this mirror answers.
type lookupInput struct {
	Document string `json:"document" jsonschema:"ELI path (eli/lta/2019/1399, or its full URL) or accession number (B20190139905)"`
	Address  string `json:"address,omitempty" jsonschema:"one provision, in Danish citation form: '§ 5', '§ 5, stk. 2', a numbered item ('nr. 3'), 'kapitel 5', 'bilag 2', 'indledningen'. Omit for the whole document."`
	AsOf     string `json:"as_of,omitempty" jsonschema:"YYYY-MM-DD: consolidate the amendment chain and return the text as amended on that date, instead of the single stored version"`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"truncate the text at this many characters; default 20000"`
}

// Serve connects the server to stdin/stdout and stays there until the client closes
// the pipe. Everything this process writes to stdout is protocol; logs must go to
// stderr, which is why the command that calls this keeps its logger on stderr.
func (s *Server) Serve(ctx context.Context) error {
	return s.mcpServer().Run(ctx, &mcp.StdioTransport{})
}

// mcpServer builds the protocol server and its tool list. Both transports — the stdio
// pipe and the HTTP endpoint — ask for the same one, so a client that connects over the
// network cannot be offered a different archive than a client that was spawned onto a
// pipe. Which tools exist is still decided by what this instance actually has: search is
// added only when an index is loaded.
func (s *Server) mcpServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "lovkilde",
		Version: s.version,
	}, &mcp.ServerOptions{
		Instructions: "Lookup Danish law from an unofficial mirror of retsinformation.dk. " +
			"Before anything else, call the tool " + termsTool + " to fetch the terms of use, show them to the " +
			"user in full, and call it again with accept: true only once the user has accepted them; " +
			"no other tool answers before that. " +
			disclaimer.ShortEnglish + " " + disclaimer.ConsolidatedEnglish + " " +
			"Pass these warnings on to the user with any answer built on this text. " +
			"Every answer names where its text came from: 'xml' is the register's own " +
			"structured body, 'html' is text our reader recovered from the site's HTML " +
			"payload, and 'none' means the mirror has metadata but no text. Say which one " +
			"a quotation came from. " + s.horizon().Policy(),
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name: termsTool,
		Description: "The terms of use (vilkår), which the user must accept before any other tool answers. " +
			"Call without arguments to fetch them, show them to the user in full, and call again with " +
			"accept: true only once the user has said they accept. Never accept on the user's behalf.",
	}, s.termsTool)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "lookup",
		Description: "Read one document from the mirror: metadata plus text, optionally one " +
			"provision, optionally consolidated to a date. Answers report text provenance " +
			"(xml, html or none) and the mirror's index time. A consolidated answer lists " +
			"every amending change it could not place under \"Changes not placed in this " +
			"text\", each with its act and wording, and marks the provisions they concern; " +
			"a provision so marked may not read as the law does on that date.",
	}, s.lookupTool)
	// Offered while the index is being built too: the tool list is fixed when a client connects,
	// and a server that started a minute ago will have search a minute from now.
	if s.idx() != nil || s.indexBuilding.Load() {
		mcp.AddTool(srv, &mcp.Tool{
			Name: "search",
			Description: "Full-text search over the mirror. Returns provisions, not documents: " +
				"each hit names its ELI path, section address, and the text source of that " +
				"document, and the address can be passed straight back to lookup. " +
				"Plain words combine with coverage-weighted OR and every hit reports how many of " +
				"the words it answers, so use AND when a word must be present: '+ord AND andet " +
				"NOT tredje', or '-ord' to exclude. A quoted span requires its words in one " +
				"provision, and hits then report how many words apart they sat.",
		}, s.searchTool)
	}
	return srv
}

// MCPHandler serves the same tools over the streamable HTTP transport, so a client that
// connects to a URL is served the same archive as one spawned onto a pipe. One protocol
// server is shared by every request, which is what the SDK expects: it keeps no per-request
// state, and running stateless would break clients that hold a session open.
//
// The endpoint is read-only for the same reason the stdio one is: every tool here answers a
// question about someone else's register.
//
// The handler is built once and shared, because a stateful streamable handler keeps its session
// map in itself: hand out a new one per request and the server answers "initialize" happily, then
// answers "session not found" to the very next request from that same client. That failure is
// worth the memo, since it arrives at the client's first tool call rather than at connect time.
// The build is guarded by a Once rather than tested for nil, because a serving process answers
// requests in parallel and two handlers built in the same instant would split the sessions
// between them.
//
// It is built on first use rather than in New because the tool list depends on what the instance
// has been given by then: search exists only if an index was loaded.
func (s *Server) MCPHandler() http.Handler {
	s.mcpOnce.Do(func() {
		// One protocol server behind one handler: the tool list is decided once, by what this
		// instance actually has, so a client on the network cannot be offered a different archive or
		// a different tool set than a client on a pipe.
		proto := s.mcpServer()
		s.mcpHandler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return proto }, nil)
	})
	return s.mcpHandler
}

// searchInput is the search tool's argument object. Same rule as lookupInput: the
// jsonschema tag value is the description a client gets.
type searchInput struct {
	Query string `json:"query" jsonschema:"Danish words to search for, e.g. 'miljøbeskyttelse pligt'. Inflected forms and compounds are matched; æ/ø/å are distinct letters and are not folded. Operators: 'a AND b' requires both, '+a' requires one, '-a' and 'NOT a' exclude, 'a OR b' unions, and two words in single quotes require both in the same provision"`
	Kind  string `json:"kind,omitempty" jsonschema:"limit to one medium: lta, ltb, ltc, mt, retsinfo, ft, fob, accn"`
	Since int    `json:"since,omitempty" jsonschema:"only documents published from this year"`
	Until int    `json:"until,omitempty" jsonschema:"only documents published up to this year"`

	// Source and NoNear are the two ways to ask for less work rather than more: an
	// xml-only search reads fewer documents, and an ungraded one skips the text reads that
	// proximity needs.
	Source string `json:"source,omitempty" jsonschema:"'xml' for the register's own structured text only, 'html' for reader-recovered text"`

	// Status is the register's own label, and the description says whose claim it is,
	// because "only what is in force" is exactly the question a model gets wrong by
	// assuming the answer means the law has not been touched since the crawl.
	Status string `json:"status,omitempty" jsonschema:"only documents the register labels 'Valid' (current), 'Historic' (repealed or superseded), or 'unknown' (the payload says nothing). The register's own claim, not a verification that nothing newer has changed it"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum hits, default 10, cap 50"`
	Offset int    `json:"offset,omitempty" jsonschema:"start further down the ranked list, e.g. the 'next offset' a previous answer gave"`

	NoNear bool `json:"no_near,omitempty" jsonschema:"rank from the index alone and skip the text reads that measure word distance"`
}

func (s *Server) searchTool(_ context.Context, req *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, any, error) {
	if r := s.gate(req); r != nil {
		return r, nil, nil
	}
	res, err := s.Search(SearchRequest{
		Query:  in.Query,
		Kind:   in.Kind,
		Since:  in.Since,
		Until:  in.Until,
		Source: in.Source,
		Status: in.Status,
		Limit:  in.Limit,
		Offset: in.Offset,
		NoNear: in.NoNear,
	})
	if err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "lovkilde: " + err.Error()}},
		}, nil, nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: res.String()}},
	}, nil, nil
}

func (s *Server) lookupTool(_ context.Context, req *mcp.CallToolRequest, in lookupInput) (*mcp.CallToolResult, any, error) {
	if r := s.gate(req); r != nil {
		return r, nil, nil
	}
	maxChars := in.MaxChars
	if maxChars <= 0 {
		maxChars = s.MaxChars
	}
	res, err := s.Lookup(LookupRequest{
		Document: in.Document,
		Address:  in.Address,
		AsOf:     in.AsOf,
		MaxChars: maxChars,
	})
	if err != nil {
		// A tool error rather than a protocol error: the client's model reads this and
		// corrects its own request, which is what the answer is for.
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "lovkilde: " + err.Error()}},
		}, nil, nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: res.String()}},
	}, nil, nil
}
