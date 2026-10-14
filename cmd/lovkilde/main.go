// Command lovkilde serves Danish legislation from a compiled artifact: the MCP endpoint, the
// reading pages, full-text search and laws as of a date.
//
// An artifact is everything it reads. It does not talk to retsinformation.dk and does not
// read the publisher's XML; collecting the documents and compiling the artifact is command
// lovkilde-update's work, which may run on another machine. The search index is cut from the
// artifact here, and never distributed.
//
// All settings are flags and there is deliberately no config file or environment layer.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/lkarlslund/lovkilde/internal/cli"
	"github.com/lkarlslund/lovkilde/internal/config"
	"github.com/lkarlslund/lovkilde/internal/disclaimer"
)

// version is reported by --version and identifies the tool.
const version = "0.1"

// gate serializes progress frames and log records, which share stderr (cli.Gate).
var gate = cli.Gate

// prof is the run's profiler, set by --cpuprofile and --memprofile.
var prof = &cli.Profiler{}

func main() {
	err := newRootCmd().Execute()
	prof.Finish()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			os.Exit(1)
		}
		fmt.Fprintf(gate.LogWriter(), "error: %v\n", err)
		holdWindow()
		os.Exit(1)
	}
}

// holdWindow keeps the console window open after an error when the program was started with no
// arguments on Windows, which is what a double-click does: the window would otherwise close
// before the error could be read.
func holdWindow() {
	if runtime.GOOS != "windows" || len(os.Args) > 1 {
		return
	}
	fmt.Fprint(gate.LogWriter(), "Tryk Enter for at lukke.")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

// configFunc resolves the validated flags without opening any storage.
type configFunc func() (config.Config, error)

func newRootCmd() *cobra.Command {
	cfg := config.Default()
	var logLevel string

	root := &cobra.Command{
		Use:     "lovkilde",
		Short:   "Serve Danish legislation from a compiled artifact",
		Version: version,
		Long: `Serve Danish legislation from a compiled artifact: an MCP endpoint and reading pages,
lookup by ELI path or accession, laws as of a date, and full-text search.

` + disclaimer.ShortEnglish + `

` + disclaimer.ConsolidatedEnglish + `

  lovkilde serve        HTTP: the reading pages and the MCP endpoint at /mcp; opens the
                      browser unless --no-browser. "lovkilde" alone does the same
  lovkilde mcp          MCP over stdio
  lovkilde search       full-text search from the command line
  lovkilde index build  cut the search index from the artifact (serve does this itself)
  lovkilde artifact check
                      say whether this binary can serve the artifact on disk
  lovkilde artifact download
                      install the published artifact (serve does this when there is none)

The artifact is everything this reads: the catalogue, each document's model and each
amending act's changes. It is made by lovkilde-update, which collects the documents from
retsinformation.dk and compiles them; this program never contacts the site.

Data layout:
  <data-dir>/artifact.current               the current artifact's file name
  <data-dir>/artifacts/artifact-*.lkz       artifacts: the current one and the one before
  <data-dir>/artifacts/search-*.lkiz        the index cut from each
  <data-dir>/artifact.checked               when the published artifact was last looked for`,
		SilenceUsage: true,
		// main prints the error once, through the gate that keeps it off a progress frame.
		SilenceErrors: true,
		Args:          cobra.NoArgs,
	}
	pf := root.PersistentFlags()
	pf.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "data directory holding the artifacts")
	pf.StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")
	prof.Register(root)
	root.PersistentPreRunE = func(*cobra.Command, []string) error { return prof.Start() }

	resolve := func() (config.Config, error) {
		if err := cfg.Validate(); err != nil {
			return cfg, err
		}
		cli.SetLogger(logLevel)
		return cfg, nil
	}
	// With no command, lovkilde serves: the program someone downloads and starts should show
	// them the website.
	serve := serveCmd(resolve)
	root.RunE = serve.RunE
	root.Flags().AddFlagSet(serve.Flags())
	root.AddCommand(
		mcpCmd(resolve),
		serve,
		indexCmd(resolve),
		artifactCmd(resolve),
		searchCmd(resolve),
	)
	return root
}
