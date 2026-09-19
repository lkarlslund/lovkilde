package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lkarlslund/retsinformation/internal/compiled"
	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
	"github.com/lkarlslund/retsinformation/internal/search"
)

// Server is the archive behind an MCP endpoint. It holds an index and nothing else:
// no store, no writer, no client. If it can be changed through this server, it is not
// a mirror any more.
type Server struct {
	archive  *lexdania.Archive
	byELI    map[string][]lexdania.Entry
	kinds    countByKind
	siteBase string
	version  string
	started  time.Time

	// index is the built full-text index, or nil. Search is not advertised when it is
	// nil, because a tool that cannot answer is worse than no tool.
	index *search.Index

	// docs is the compiled artifact the document text is read from, when one was given. Without it
	// every lookup re-parses the publisher's XML, which answers correctly and slowly, so the
	// status line says which of the two a client is being served from rather than leaving it to be
	// guessed from how long the answer took.
	docs *compiled.Cache

	// MaxChars caps every answer when the request does not ask for a different cap.
	// Zero keeps the package default.
	MaxChars int

	// SweepStampPath is the file the collection pass publishes its horizon to. Empty
	// means the server says its horizon is unknown rather than inventing one from
	// file mtimes, which a bulk crawl makes meaningless.
	SweepStampPath string

	// nowFunc answers "what time is it" for the horizon. It is a field rather than a call to
	// time.Now so a test can pin the clock: several of this package's claims are about a date
	// measured from today, and a test that hard-codes today is correct for exactly one day —
	// which is how two horizon tests came to disagree with each other the moment the date moved.
	nowFunc func() time.Time

	// mcpHandler is the built HTTP transport, made once on first use. See MCPHandler for why it
	// has to be the same one every time, and why the building is guarded rather than checked.
	mcpOnce    sync.Once
	mcpHandler http.Handler
}

// clock is the server's idea of now: time.Now, unless a test pinned it.
func (s *Server) clock() time.Time {
	if s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

// New indexes the archive for serving. Scanning 191,393 documents takes about a second
// warm, so the index is rebuilt at startup rather than cached: the archive's own rule
// is that its index is derived from the files and can be thrown away at any time, and
// a cache would be a second answer to "what is in the mirror".
func New(a *lexdania.Archive, siteBase, version string) *Server {
	s := &Server{
		archive:  a,
		byELI:    make(map[string][]lexdania.Entry, a.Len()),
		kinds:    countByKind{},
		siteBase: strings.TrimSuffix(siteBase, "/"),
		version:  version,
		started:  time.Now(),
	}
	for _, e := range a.Entries() {
		// Keyed without the eli/ segment, which is how callers write the path after
		// copying it from a URL; find() strips whichever form it was given.
		//
		// A slice, not a single entry, because an ELI path is not unique. A
		// folketingssag stores each stage of a bill under its own accession, and they
		// all derive the same eli/ft/<year>/<number>: 202012L00054, 202013L00054 and
		// 20201BB00054 are all eli/ft/2020/54, with different text. A map of one entry
		// per ELI would silently answer for whichever file was scanned last.
		key := strings.TrimPrefix(e.ELIPath(), "eli/")
		s.byELI[key] = append(s.byELI[key], e)
		s.kinds.add(e.Kind)
	}
	for _, es := range s.byELI {
		// Deterministic order, so an ambiguous path lists its candidates the same way
		// on every run rather than in map order.
		sort.Slice(es, func(i, j int) bool { return es[i].Accession < es[j].Accession })
	}
	return s
}

// ambiguous reports every document stored under the same ELI path, and says whether
// the path actually identifies one document.
func (s *Server) ambiguous(eli string) []lexdania.Entry {
	es := s.byELI[strings.TrimPrefix(eli, "eli/")]
	if len(es) < 2 {
		return nil
	}
	return es
}

// Status is what the server knows about the mirror it is serving, for the initialize
// instructions and for the lookup header.
func (s *Server) Status() string {
	built := "undated"
	if s.index != nil && s.index.Built > 0 {
		built = time.Unix(s.index.Built, 0).Format(time.RFC3339)
	}
	return fmt.Sprintf("%d documents in %s (media: %s), index built %s, %s",
		s.archive.Len(), s.archive.Root, s.kinds, built, s.textSource())
}

// textSource names where a document's text is read from, in the words an operator reads in a log
// line and a client reads in a search answer. Serving by parse is allowed but not invisible: it is
// the difference between a lookup costing a seek and a decompression and one costing an XML parse,
// and a server that said nothing about it would be a server people run slowly for years.
func (s *Server) textSource() string {
	if s.docs == nil {
		return "text by parsing XML (run `retsin artifact build` and restart to serve from the compiled artifact)"
	}
	art := s.docs.Artifact()
	return fmt.Sprintf("text from the compiled artifact at %s (built %s)", art.Path(), art.Header.Created.Format("2006-01-02 15:04:05Z07:00"))
}

// DefaultArtifactCacheBytes is how much payload the serving cache may hold. The bound is in bytes
// because that is what a cache costs: a document's compressed payload is its own size, so the
// budget is arithmetic rather than a guess about struct widths.
const DefaultArtifactCacheBytes = 64 << 20

// UseArtifact serves document text out of a compiled artifact instead of parsing the publisher's
// XML per read.
//
// The artifact has to be this build's own. Its payload is the JSON form of the model's Go types, so
// a file from another revision decodes into today's struct with yesterday's meaning and answers
// questions about a law with fields that no longer say what they said. The refusal therefore names
// which side is stale, since the operator can rebuild either one and only the artifact's timestamp
// says which needs it.
func (s *Server) UseArtifact(path string, cacheBytes int64) error {
	if cacheBytes <= 0 {
		cacheBytes = DefaultArtifactCacheBytes
	}
	c, err := compiled.OpenCached(path, cacheBytes)
	if err != nil {
		return err
	}
	if err := c.Artifact().CheckCurrentBuild(); err != nil {
		c.Close()
		return err
	}
	// The other way an artifact can be wrong: right build, old data. The collection pass publishes
	// when it last pulled, so a server that booted against an artifact from before that would be
	// answering from a corpus that no longer exists.
	if h := s.horizon(); h.Known {
		if err := c.Artifact().CheckNotOlderThan(h.PulledAt, "the last recorded collection pass"); err != nil {
			c.Close()
			return err
		}
	}
	s.docs = c
	return nil
}

// artifactStaleness is the same check as the one at startup, re-asked per answer: a server that
// booted against a current artifact will still be running tomorrow night when the crawl finishes,
// and from that moment its compiled text describes a mirror it no longer holds. That is not a
// reason to stop answering — the text it holds is still the register's text for the documents it
// holds — but it is a reason to say so in the answer that is affected rather than in a log line
// nobody reads.
func (s *Server) artifactStaleness() string {
	if s.docs == nil {
		return ""
	}
	art := s.docs.Artifact()
	h := s.horizon()
	if !h.Known || !art.Header.Created.Before(h.PulledAt) {
		return ""
	}
	return fmt.Sprintf("the compiled text this answer is read from was built %s, before the last collection pass (%s), so documents that arrived since are not in it; rebuild the artifact with `retsin artifact build`",
		art.Header.Created.Format("2006-01-02 15:04:05Z07:00"), h.PulledAt.Format("2006-01-02 15:04:05Z07:00"))
}

// unitSource is where a search hit's text comes from: the artifact when serving has one, and the
// XML reader otherwise. The index itself stores no text either way.
func (s *Server) unitSource() search.Documents {
	if s.docs != nil {
		return artifactDocs{cache: s.docs}
	}
	return search.XMLDocuments{}
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

// lookupInput is the tool's argument object. The jsonschema tags are what a client
// sees, so they are written for someone who has never read this repository.
type lookupInput struct {
	Document string `json:"document" jsonschema:"required" jsonschema_description:"ELI path (eli/lta/2019/1399, or its full URL) or accession number (A20191399070)"`
	Address  string `json:"address,omitempty" jsonschema_description:"one provision, in Danish citation form: '§ 267, stk. 3 nr. 2', 'kapitel 5', 'bilag 2', 'indledningen'. Omit for the whole document."`
	AsOf     string `json:"as_of,omitempty" jsonschema_description:"YYYY-MM-DD: consolidate the amendment chain and return the text as amended on that date, instead of the single stored version"`
	MaxChars int    `json:"max_chars,omitempty" jsonschema_description:"truncate the text at this many characters; default 20000"`
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
		Name:    "retsin",
		Version: s.version,
	}, &mcp.ServerOptions{
		Instructions: "Lookup Danish law from a local mirror of retsininformation.dk. " +
			"Every answer names where its text came from: 'xml' is the register's own " +
			"structured body, 'html' is text our reader recovered from the site's HTML " +
			"payload, and 'none' means the mirror has metadata but no text. Say which one " +
			"a quotation came from. " + s.horizon().Policy(),
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name: "lookup",
		Description: "Read one document from the mirror: metadata plus text, optionally one " +
			"provision, optionally consolidated to a date. Answers report text provenance " +
			"(xml, html or none) and the mirror's index time.",
	}, s.lookupTool)
	if s.index != nil {
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

// searchInput is the search tool's argument object.
type searchInput struct {
	Query string `json:"query" jsonschema:"required" jsonschema_description:"Danish words to search for, e.g. 'miljøbeskyttelse pligt'. Inflected forms and compounds are matched; æ/ø/å are distinct letters and are not folded. Operators: 'a AND b' requires both, '+a' requires one, '-a' and 'NOT a' exclude, 'a OR b' unions, and 'a b' in double quotes requires both words in the same provision"`
	Kind  string `json:"kind,omitempty" jsonschema_description:"limit to one medium: lta, ltb, ltc, mt, retsinfo, ft, fob, accn"`
	Since int    `json:"since,omitempty" jsonschema_description:"only documents published from this year"`
	Until int    `json:"until,omitempty" jsonschema_description:"only documents published up to this year"`

	// Source and NoNear are the two ways to ask for less work rather than more: an
	// xml-only search reads fewer documents, and an ungraded one skips the text reads that
	// proximity needs.
	Source string `json:"source,omitempty" jsonschema_description:"'xml' for the register's own structured text only, 'html' for reader-recovered text"`
	Limit  int    `json:"limit,omitempty" jsonschema_description:"maximum hits, default 10, cap 50"`

	NoNear bool `json:"no_near,omitempty" jsonschema_description:"rank from the index alone and skip the text reads that measure word distance"`
}

func (s *Server) searchTool(_ context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, any, error) {
	res, err := s.Search(SearchRequest{
		Query:  in.Query,
		Kind:   in.Kind,
		Since:  in.Since,
		Until:  in.Until,
		Source: in.Source,
		Limit:  in.Limit,
		NoNear: in.NoNear,
	})
	if err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "retsin: " + err.Error()}},
		}, nil, nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: res.String()}},
	}, nil, nil
}

func (s *Server) lookupTool(_ context.Context, _ *mcp.CallToolRequest, in lookupInput) (*mcp.CallToolResult, any, error) {
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
			Content: []mcp.Content{&mcp.TextContent{Text: "retsin: " + err.Error()}},
		}, nil, nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: res.String()}},
	}, nil, nil
}
