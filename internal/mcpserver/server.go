package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
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

	// MaxChars caps every answer when the request does not ask for a different cap.
	// Zero keeps the package default.
	MaxChars int

	// SweepStampPath is the file the collection pass publishes its horizon to. Empty
	// means the server says its horizon is unknown rather than inventing one from
	// file mtimes, which a bulk crawl makes meaningless.
	SweepStampPath string
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
	return fmt.Sprintf("%d documents in %s (media: %s), index built %s",
		s.archive.Len(), s.archive.Root, s.kinds, built)
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
	return srv.Run(ctx, &mcp.StdioTransport{})
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
