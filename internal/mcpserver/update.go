package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Updater is what the update tool and the note on every answer ask of the program running the
// server (internal/updates); nil serves neither.
type Updater interface {
	// Note is a line for the assistant while an update is published or under way, "" otherwise.
	Note() string
	// Check asks now whether newer versions are published.
	Check(ctx context.Context) string
	// Start downloads and puts to use a newer version of "app" or "dataset".
	Start(ctx context.Context, what string) (string, error)
}

// updateTool is the tool updates are checked for and started through.
const updateTool = "update"

// updateInput is the update tool's argument object; see lookupInput for why the tag is the
// description.
type updateInput struct {
	Action string `json:"action" jsonschema:"'check' asks whether a newer program or newer law texts are published (the server also checks daily); 'app' downloads the newer program; 'dataset' downloads the newer law texts. Use 'app' and 'dataset' only when the user has asked for that update"`
}

func (s *Server) updateTool(ctx context.Context, req *mcp.CallToolRequest, in updateInput) (*mcp.CallToolResult, any, error) {
	if r := s.gate(req); r != nil {
		return r, nil, nil
	}
	if in.Action == "check" {
		return s.withNote(textResult(false, s.Updates.Check(ctx))), nil, nil
	}
	text, err := s.Updates.Start(ctx, in.Action)
	if err != nil {
		return textResult(true, "lovkilde: "+err.Error()), nil, nil
	}
	return textResult(false, text), nil, nil
}

// withNote adds the update note to an answer, as a block of its own after the answer's text.
func (s *Server) withNote(r *mcp.CallToolResult) *mcp.CallToolResult {
	if s.Updates == nil || r.IsError {
		return r
	}
	if note := s.Updates.Note(); note != "" {
		r.Content = append(r.Content, &mcp.TextContent{Text: note})
	}
	return r
}
