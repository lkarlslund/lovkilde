package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lkarlslund/lovkilde/internal/eula"
)

type fakeUpdater struct{ note, started string }

func (f *fakeUpdater) Note() string                 { return f.note }
func (f *fakeUpdater) Check(context.Context) string { return "Programmet: nyeste" }
func (f *fakeUpdater) Start(_ context.Context, what string) (string, error) {
	f.started = what
	return "hentes nu", nil
}

// An answer carries the update note as a block of its own, and the update tool starts what it is
// asked for.
func TestAnswersNoteAnUpdateTheUserCanStart(t *testing.T) {
	s := newTestServer(t)
	dir := t.TempDir()
	if err := eula.Open(dir).Accept(); err != nil {
		t.Fatal(err)
	}
	s.EULA = eula.Open(dir)
	f := &fakeUpdater{note: "Note for the assistant: Updates are available."}
	s.Updates = f

	res, _, _ := s.lookupTool(context.Background(), nil, lookupInput{Document: accCanon})
	if res.IsError || len(res.Content) != 2 || res.Content[1].(*mcp.TextContent).Text != f.note {
		t.Errorf("the answer does not end with the note: %+v", res.Content)
	}
	f.note = ""
	if res, _, _ := s.lookupTool(context.Background(), nil, lookupInput{Document: accCanon}); len(res.Content) != 1 {
		t.Errorf("an answer carries a note with no update published: %d blocks", len(res.Content))
	}

	res, _, _ = s.updateTool(context.Background(), nil, updateInput{Action: "dataset"})
	if res.IsError || f.started != "dataset" || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "hentes nu") {
		t.Errorf("update dataset: started %q, %+v", f.started, res.Content)
	}
}
