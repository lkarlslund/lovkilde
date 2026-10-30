// The serve command: one port, two audiences.
//
// The same loaded server answers a person at / and an agent at /mcp. That is the point of the
// command rather than an accident of routing: a browser and a model reading the same mirror
// should not be able to get different text, different provenance, or a different idea of what
// "current" means, and the only reliable way to guarantee that is one process with one archive,
// one index, and one artifact.
//
// Like "mcp", this command reads the artifact and nothing else: no state store, no XML.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"time"

	"github.com/spf13/cobra"

	"github.com/lkarlslund/lovkilde/internal/apikey"
	"github.com/lkarlslund/lovkilde/internal/mcpserver"
	"github.com/lkarlslund/lovkilde/internal/updates"
	"github.com/lkarlslund/lovkilde/internal/webserver"
)

// defaultServeAddr binds to localhost. A readable interface over a legal corpus is a thing
// people will want to put behind a reverse proxy, and the safe default is the one that does not
// appear on the network by itself: an operator who wants that says so with --addr.
const defaultServeAddr = "127.0.0.1:8787"

// serveShutdown is how long in-flight requests get after an interrupt. It is short because the
// server is read-only: nothing is corrupted by cutting it off, and an operator who pressed
// Ctrl-C should not wait for a large document to finish rendering.
const serveShutdown = 5 * time.Second

func serveCmd(resolve configFunc) *cobra.Command {
	var (
		o         serveOptions
		addr      string
		verbose   bool
		noBrowser bool
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the mirror as a website and an MCP endpoint on one port (the default command)",
		Long: `Serve the local mirror over HTTP: a search box, readable documents, and the MCP
endpoint at /mcp. This is what "lovkilde" with no command does. Once the server listens, it
opens the website in the browser; --no-browser does not.

  lovkilde serve
  curl 'http://127.0.0.1:8787/search?q=for%C3%A6ldrepension'
  curl 'http://127.0.0.1:8787/doc?ref=B20190139905&addr=%C2%A7+5,+stk.+2'

Both audiences get the same answers from the same loaded data. A page shows a provision, its
address, and where its text came from, exactly as the lookup tool does; a link from a search
result carries the provision's address, so the thing that matched is the thing that opens.
Documents link to their own table of contents, to the register's own ELI page, and to the later
documents their own metadata names as changing them — with anything the mirror does not hold
marked as missing rather than shown as a broken promise.

The page's HTML is the same walk that produces the markdown an agent reads, with document text
escaped where it is built: a provision containing "<script>" arrives as text about a script, not
as a script. There is no JavaScript, no build step, and no asset pipeline.

--index/--artifact mean what they mean for "mcp". The search index is the server's own: when it
is missing, unreadable or built from another artifact, the server rebuilds it from the artifact
in the background (about a minute) and saves it; lookups and browsing answer meanwhile, and the
site says search is on its way. A missing artifact is downloaded from --release, and a newer one
offered every --check-every, and installed when the user asks on the settings page or through the
tool update; without one the server does not start, and one that is present but
unreadable stops it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Most of a server's heap is the index and the catalogue, which live as long as it does,
			// so the default of collecting when the heap has doubled lets a search of common words
			// leave the process a gigabyte larger. Collecting at half again costs no measurable
			// search time. GOGC set by whoever runs the server wins.
			if os.Getenv("GOGC") == "" {
				debug.SetGCPercent(50)
			}
			o.Restarts = true
			srv, upd, err := openServingServer(cmd.Context(), resolve, o)
			if err != nil {
				return err
			}
			cfg, err := resolve()
			if err != nil {
				return err
			}
			go upd.Run(cmd.Context())
			web := webserver.New(srv)
			web.Checks, web.Updates = upd.Checks(), upd
			web.Key, web.Exposed = apikey.Open(cfg.DataDir), exposedAddr(addr)
			ctx, stop := context.WithCancel(cmd.Context())
			defer stop()
			service, err := startServer(ctx, addr, web, verbose)
			if err != nil {
				return err
			}
			fmt.Fprintf(gate.LogWriter(), "lovkilde %s serving %s at http://%s/ (MCP endpoint at /mcp)\n", version, srv.Status(), service.Addr())
			// A server that restarted as a newer version has its browser tab already.
			if restarted := os.Getenv(restartedEnv) != ""; !noBrowser && !restarted {
				if err := openBrowser(browserURL(service.Addr())); err != nil && !errors.Is(err, errNoDisplay) {
					fmt.Fprintf(gate.LogWriter(), "could not open a browser: %v\n", err)
				}
			}
			os.Unsetenv(restartedEnv)
			// Started from Explorer on Windows, Lovkilde sits in the notification area.
			removeTray := startTray(ctx, browserURL(service.Addr()), cfg.DataDir, stop)
			defer removeTray()
			// A newer build kept in the data directory — fetched when the user asked, or by hand —
			// replaces this server once it has stopped listening.
			newer := make(chan string, 1)
			if web.Updates.Can(updates.App) {
				go func() {
					if path := watchProgram(ctx, cfg.DataDir, followInterval); path != "" {
						newer <- path
						stop()
					}
				}()
			}
			if err := service.Wait(); err != nil {
				return err
			}
			select {
			case path := <-newer:
				removeTray()
				if err := restartAs(path); err != nil {
					if errors.Is(err, errServiceRestart) {
						return err
					}
					return fmt.Errorf("could not restart as the newer version in %s: %w", path, err)
				}
			default:
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", defaultServeAddr,
		"address to listen on; localhost by default, because a public legal mirror is a decision, not a default")
	cmd.Flags().IntVar(&o.MaxChars, "max-chars", 0,
		fmt.Sprintf("cap every MCP answer at this many characters (default %d); web pages are not capped",
			mcpserver.DefaultMaxChars))
	cmd.Flags().StringVar(&o.Index, "index", "",
		"where the search index is kept (default: beside the artifact, named for its stamp); built from the artifact when it is missing or from another artifact")
	cmd.Flags().StringVar(&o.Artifact, "artifact", "",
		"serve this artifact file and no other (default: the current one, followed as new ones are installed)")
	cmd.Flags().IntVar(&o.ArtifactCacheMB, "artifact-cache-mb", mcpserver.DefaultArtifactCacheBytes>>20,
		"megabytes of artifact payloads the serving cache may hold")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false,
		"do not open the website in the browser once the server listens")
	cmd.Flags().BoolVar(&verbose, "verbose-requests", false,
		"log every request line; off by default because a public page's traffic is a list of other people's questions")
	addFetchFlags(cmd, &o)
	return cmd
}

// softwareRepo is where the published builds of this program are: its releases, rNNNNN.
const softwareRepo = "lkarlslund/lovkilde"

// exposedAddr says whether a listen address reaches beyond this machine: anything but a loopback
// address does, including every interface (":8787", "0.0.0.0:8787") and a name that does not
// resolve to loopback alone.
func exposedAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback()
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return true
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return true
		}
	}
	return false
}

// httpService is a running HTTP server this process can wait for and report honestly.
type httpService struct {
	srv  *http.Server
	addr string
	done chan struct{}
	err  error
}

// Addr is the address actually bound, which is what the log line reports.
func (h *httpService) Addr() string { return h.addr }

// Wait returns when the listener stops: after a shutdown, with nil, or with the error that
// killed it, such as a TLS misconfiguration nobody noticed at bind time.
func (h *httpService) Wait() error {
	<-h.done
	return h.err
}

// startServer binds and serves, and shuts down when the context ends.
//
// It returns once the listener is up, so a caller can print the address it actually got: a
// server that logs a port it failed to bind is how an operator spends an hour on a stale
// process.
func startServer(ctx context.Context, addr string, handler http.Handler, logRequests bool) (*httpService, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}
	httpSrv := &http.Server{
		Handler: handler,
		// ReadHeaderTimeout is not decoration: a serving process that waits forever on a
		// half-sent request line is a process with a free resource leak.
		ReadHeaderTimeout: 10 * time.Second,
	}
	if logRequests {
		httpSrv.Handler = logRequestsHandler(httpSrv.Handler)
	}
	service := &httpService{srv: httpSrv, addr: ln.Addr().String(), done: make(chan struct{})}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serveShutdown)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
	go func() {
		defer close(service.done)
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			service.err = err
			fmt.Fprintf(gate.LogWriter(), "http: %v\n", err)
		}
	}()
	return service, nil
}

// logRequestsHandler writes one line per request to the gated log writer, so a request line
// cannot land in the middle of a progress frame.
func logRequestsHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		fmt.Fprintf(gate.LogWriter(), "%s %s %d %s\n", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

// statusRecorder remembers the status code a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
