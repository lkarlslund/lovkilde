package main

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lkarlslund/retsinformation/internal/builder"
)

// runCLI runs a command tree with args, its output discarded.
func runCLI(t *testing.T, root *cobra.Command, args ...string) error {
	t.Helper()
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	return root.Execute()
}

// buildArtifact compiles the XML archive under dir/xml and installs the artifact in dir, as
// `lovkilde-update artifact build` does. The reader's tests need real artifacts; the program that
// serves them does not build them, so only its tests reach the builder.
func buildArtifact(t *testing.T, dir string) {
	t.Helper()
	if _, err := builder.Artifact(context.Background(), builder.Options{XMLDir: dir + "/xml", DataDir: dir}); err != nil {
		t.Fatalf("artifact build: %v", err)
	}
}

// The commands a server needs, and none that collect or compile.
func TestTheReaderCommandTree(t *testing.T) {
	have := map[string]bool{}
	for _, c := range newRootCmd().Commands() {
		have[c.Name()] = true
	}
	for _, name := range []string{"serve", "mcp", "index", "search", "artifact"} {
		if !have[name] {
			t.Errorf("command %q missing from the tree", name)
		}
	}
	for _, name := range []string{"fetch", "update", "harvest", "inventory"} {
		if have[name] {
			t.Errorf("the reader offers %q, which is lovkilde-update's", name)
		}
	}
}

// The server reads an artifact and nothing else, so the binary must not contain the code that
// collects documents or reads the publisher's XML: if it did, a fallback that reads them could
// come back without anyone deciding it should. Those packages are lovkilde-update's. Only the
// binary is checked; tests may parse XML fixtures to build the artifacts they serve.
func TestTheServerLinksNoCollectionOrXML(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	const internal = "github.com/lkarlslund/retsinformation/internal/"
	forbidden := map[string]bool{}
	for _, p := range []string{"lexdania", "builder", "crawl", "store", "harvest", "docapi", "sitemap", "rihttp", "limiter", "archive"} {
		forbidden[internal+p] = true
	}
	for _, dep := range strings.Fields(string(out)) {
		if forbidden[dep] {
			t.Errorf("lovkilde links %s", strings.TrimPrefix(dep, internal))
		}
	}
}
