package mcpserver

import (
	"context"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lkarlslund/retsinformation/internal/disclaimer"
)

// termsTool is the tool a session accepts the terms through. Every other tool answers only with
// the terms until it has: first the model fetches them and shows them to the user, then, once the
// user has said yes, it calls again to accept.
const termsTool = "vilkaar"

// termsInput is the terms tool's argument object; see lookupInput for why the tag is the
// description.
type termsInput struct {
	Accept bool `json:"accepterer,omitempty" jsonschema:"true only after the user has read the terms this tool returned and said themselves that they accept them; omit to fetch the terms"`
}

// terms is which sessions have been shown the terms and which have accepted them. A session is
// keyed by its id; the stdio transport's one session has the empty id.
type terms struct {
	mu       sync.Mutex
	shown    map[string]bool
	accepted map[string]bool
}

func (t *terms) state(id string) (shown, accepted bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.shown[id], t.accepted[id]
}

func (t *terms) mark(id string, accepted bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.shown == nil {
		t.shown, t.accepted = map[string]bool{}, map[string]bool{}
	}
	t.shown[id] = true
	if accepted {
		t.accepted[id] = true
	}
}

func sessionID(req *mcp.CallToolRequest) string {
	if req == nil || req.Session == nil {
		return ""
	}
	return req.Session.ID()
}

const termsInstructions = "Show the user these terms in full, in Danish and word for word, and ask whether they " +
	"accept them. Only when the user has answered that they accept, call the tool " + termsTool +
	" again with accepterer: true. Never accept on the user's behalf. Until then no other tool answers.\n\n"

func textResult(isError bool, text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: isError, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func (s *Server) termsTool(_ context.Context, req *mcp.CallToolRequest, in termsInput) (*mcp.CallToolResult, any, error) {
	id := sessionID(req)
	shown, accepted := s.terms.state(id)
	switch {
	case accepted:
		return textResult(false, "Vilkårene er allerede accepteret i denne session. Alle værktøjer kan bruges."), nil, nil
	case !in.Accept:
		s.terms.mark(id, false)
		return textResult(false, termsInstructions+disclaimer.TermsText()), nil, nil
	case !shown:
		return textResult(true, "The terms have not been shown in this session yet. Call "+termsTool+
			" without arguments, show the user the terms, and accept only once they have.\n\n"+disclaimer.TermsText()), nil, nil
	}
	s.terms.mark(id, true)
	return textResult(false, "Vilkårene er accepteret for denne session (version "+disclaimer.TermsVersion+
		"). Alle værktøjer kan nu bruges. Husk: teksterne er uofficielle og kan indeholde fejl."), nil, nil
}

// gate answers in place of a tool whose session has not accepted the terms, or nil when it has.
func (s *Server) gate(req *mcp.CallToolRequest) *mcp.CallToolResult {
	if _, accepted := s.terms.state(sessionID(req)); accepted {
		return nil
	}
	return textResult(true, "The user has not accepted the terms of use in this session. Call the tool "+
		termsTool+" without arguments to fetch them, show them to the user, and call it with "+
		"accepterer: true once the user has accepted. No other tool answers until then.")
}
