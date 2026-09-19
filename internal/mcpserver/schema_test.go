package mcpserver

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The argument descriptions are the only thing a client that has never seen this
// repository reads before calling a tool, and they can vanish without any error: the
// schema library reads the value of a struct tag named "jsonschema" as the description,
// and ignores a tag named anything else. Writing the guidance in a tag with a plausible
// name therefore produced a schema where every argument was undocumented, and one
// argument was documented as the word "required". Asserting on the emitted schema is the
// only check that sees this, because the Go source still reads fine either way.
func TestToolSchemasDescribeEveryArgument(t *testing.T) {
	srv := newTestServer(t)
	endpoint := httptest.NewServer(srv.MCPHandler())
	defer endpoint.Close()

	session, _ := mcpPost(t, endpoint.URL, "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "schema-test", "version": "0"},
		},
	})
	if session == "" {
		t.Fatal("initialize returned no session id, so the tool list below is not a real client view")
	}
	mcpPost(t, endpoint.URL, session, map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized",
	})
	_, body := mcpPost(t, endpoint.URL, session, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	})

	tools := listedTools(t, body)
	if len(tools) == 0 {
		t.Fatal("tools/list returned no tools")
	}
	for _, tool := range tools {
		props, _ := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
		if len(props) == 0 {
			t.Fatalf("%s: no input properties in the emitted schema", tool["name"])
		}
		for name, raw := range props {
			prop, _ := raw.(map[string]any)
			desc, _ := prop["description"].(string)
			switch {
			case desc == "":
				t.Errorf("%s.%s: no description reached the client; the guidance has to be the value of the jsonschema struct tag", tool["name"], name)
			case desc == "required":
				t.Errorf("%s.%s: documented as the word %q, which is what a jsonschema:\"required\" tag does: the tag value is the description, and required comes from omitempty", tool["name"], name, "required")
			case len(desc) < 20:
				t.Errorf("%s.%s: description is %q, too short to tell a client anything", tool["name"], name, desc)
			}
		}
	}

	// The cues worth their bytes: the one field with no other cue is the document
	// reference, and the address forms are Danish abbreviations an English-speaking model
	// cannot guess.
	lookup := toolByName(t, tools, "lookup")
	lookupProps := lookup["inputSchema"].(map[string]any)["properties"].(map[string]any)
	for _, want := range []string{"eli/lta", "accession"} {
		if !strings.Contains(descOf(lookupProps, "document"), want) {
			t.Errorf("lookup.document does not mention %q; a client has to learn the reference forms from somewhere", want)
		}
	}
	if !strings.Contains(descOf(lookupProps, "address"), "stk.") {
		t.Error("lookup.address does not show a stykke address, the form most answers need")
	}
	if req, _ := lookup["inputSchema"].(map[string]any)["required"].([]any); len(req) != 1 || req[0] != "document" {
		t.Errorf("lookup.required = %v, want only document required", req)
	}
}

// tool is one entry from a tools/list response.
type toolEntry = map[string]any

func toolByName(t *testing.T, tools []toolEntry, name string) toolEntry {
	for _, tool := range tools {
		if tool["name"] == name {
			return tool
		}
	}
	t.Fatalf("tool %q is not offered", name)
	return nil
}

func descOf(props map[string]any, name string) string {
	prop, _ := props[name].(map[string]any)
	desc, _ := prop["description"].(string)
	return desc
}

// mcpPost sends one JSON-RPC message the way the streamable transport wants it and
// returns the session id and the decoded reply. The transport answers with server-sent
// events, so the payload is whatever line follows "data: ".
func mcpPost(t *testing.T, url, session string, payload map[string]any) (string, map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST %v: status %d", payload["method"], resp.StatusCode)
	}
	var out map[string]any
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &out); err != nil {
			t.Fatalf("decoding reply to %v: %v", payload["method"], err)
		}
	}
	return resp.Header.Get("Mcp-Session-Id"), out
}

func listedTools(t *testing.T, reply map[string]any) []toolEntry {
	result, _ := reply["result"].(map[string]any)
	raw, _ := result["tools"].([]any)
	tools := make([]toolEntry, 0, len(raw))
	for _, item := range raw {
		tool, ok := item.(toolEntry)
		if !ok {
			t.Fatalf("tool entry is %T", item)
		}
		tools = append(tools, tool)
	}
	return tools
}
