package mcpserver

import (
	"testing"

	"github.com/lkarlslund/lovkilde/internal/eula"
)

// Terms accepted on the server, on the website or in an earlier session, open every MCP session.
func TestTermsAcceptedOnTheServerOpenEverySession(t *testing.T) {
	s := newTestServer(t)
	dir := t.TempDir()
	s.EULA = eula.Open(dir)
	if s.gate(nil) == nil {
		t.Fatal("a session answered before the terms were accepted")
	}
	if err := eula.Open(dir).Accept(); err != nil {
		t.Fatal(err)
	}
	s.EULA = eula.Open(dir)
	if r := s.gate(nil); r != nil {
		t.Errorf("a session was refused after the terms were accepted on the server: %v", r.Content)
	}
}
