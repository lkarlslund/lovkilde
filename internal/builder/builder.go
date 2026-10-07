// Package builder compiles the XML archive into the artifact a server reads: each document's
// model, each amending act's changes, and the catalogue (see internal/compiled). It is the one
// place the publisher's XML becomes the artifact; nothing that serves imports it.
package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/lkarlslund/retsinformation/internal/archive"
	"github.com/lkarlslund/retsinformation/internal/artifactdir"
	"github.com/lkarlslund/retsinformation/internal/compiled"
	"github.com/lkarlslund/retsinformation/internal/crawl"
	"github.com/lkarlslund/retsinformation/internal/lexdania"
	"github.com/lkarlslund/retsinformation/internal/parallel"
	"github.com/lkarlslund/retsinformation/internal/progress"
)

// Options says what to build from and where to put it.
type Options struct {
	// XMLDir is the archive of canonical XML the artifact is compiled from.
	XMLDir string
	// DataDir is where the artifact is installed and made current (see internal/artifactdir),
	// unless Out names a file to write instead.
	DataDir string
	Out     string
	// CollectionRecord is the file the collection pass publishes itself to; the artifact records
	// that pass as how current its text is (compiled.ArtifactHeader.Collection).
	CollectionRecord string
	// Report receives the one-line summary of what was built.
	Report io.Writer
}

// Artifact compiles the archive and returns the path of the file written.
func Artifact(ctx context.Context, o Options) (string, error) {
	// Without --out the artifact goes into the artifacts directory under its own name
	// and becomes current (see internal/artifactdir); with --out it is just a file.
	if o.Report == nil {
		o.Report = io.Discard
	}
	out := o.Out
	install := out == ""
	if install {
		out = filepath.Join(artifactdir.Dir(o.DataDir), "artifact-building"+artifactdir.Ext)
	}
	start := time.Now()
	slog.Info("scanning the mirror", "dir", o.XMLDir)
	a, err := lexdania.Scan(ctx, o.XMLDir)
	if err != nil {
		return "", err
	}
	// The archive's last collection pass is how current this text is; the server quotes it
	// from the artifact, which is what it serves. A record that will not parse is an error
	// rather than an artifact that claims nothing: it is the operator's state to fix.
	// Without a record path — a test's or a measurement's build — the artifact records no pass.
	var pass *compiled.Collection
	if o.CollectionRecord != "" {
		rec, found, err := crawl.ReadSweepRecord(o.CollectionRecord)
		switch {
		case err != nil:
			return "", fmt.Errorf("reading the collection record: %w", err)
		case found:
			pass = &compiled.Collection{PulledAt: rec.PulledAt, IssuesKnown: rec.IssuesKnown,
				Owed: rec.Owed, Stalled: rec.Stalled, Digest: rec.Digest}
		default:
			slog.Warn("no collection pass recorded: the artifact will say the mirror was never pulled", "looked_in", o.CollectionRecord)
		}
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	tmp := out + artifactdir.PartExt
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	wr, err := compiled.NewWriterAfter(f, o.XMLDir, pass)
	if err != nil {
		f.Close()
		return "", err
	}

	// Which entries make it into the file is decided here, in entry order: the
	// mirror can hold one accession twice, and which copy wins has to be the same
	// answer every build, so the filter cannot run on workers.
	var want []string
	entryOf := map[string]lexdania.Entry{}
	seen := map[string]bool{}
	var noAccession, duplicates int
	for _, e := range a.Entries() {
		if e.Accession == "" {
			noAccession++
			continue
		}
		if seen[e.Accession] {
			duplicates++
			continue
		}
		want = append(want, e.Accession)
		entryOf[e.Accession] = e
		seen[e.Accession] = true
	}

	// Open, model, marshal and compress on workers; write here, in order.
	//
	// A build spends its time in exactly those four steps — 191,429 documents
	// parsed, their models marshalled, one zstd frame per document — and none of
	// them touch the writer. The file itself is a byte stream whose offsets are the
	// seek table, so the writing stays single-threaded and in mirror order. Chunks
	// hand over on a channel, which means the file is being written while the next
	// chunk compiles.
	const chunk = 512
	// The phases are timed separately because they answer different questions:
	// compile is how long the corpus takes to parse, model, marshal and compress;
	// write is how long the file takes to leave the process; loop is the wall time
	// of the two running against each other. Compile and write overlap, so the
	// first two do not add up to the third, and the gap between them is what the
	// parallelism bought.
	var compileNanos, writeNanos int64
	tLoop := time.Now()
	type ready struct {
		frames   []compiled.CompiledFrame
		missing  int
		failed   string
		firstErr error
	}
	// One catalogue record per document, written by the worker that compiled it.
	records := make([]lexdania.CatalogueRecord, len(want))
	batches := make(chan ready, 1)
	go func() {
		defer close(batches)
		for lo := 0; lo < len(want); lo += chunk {
			hi := min(lo+chunk, len(want))
			batch := ready{frames: make([]compiled.CompiledFrame, hi-lo)}
			errs := make([]error, hi-lo)
			opened := make([]bool, hi-lo)
			tCompile := time.Now()
			parallel.For(ctx, hi-lo, func(i int) {
				acc := want[lo+i]
				doc, path, ok := a.Open(acc)
				if !ok {
					return
				}
				opened[i] = true
				// The accession comes from the entry rather than the document: the entry is where
				// the mirror found it, and a file whose own metadata disagrees about its key is
				// filed under the address the archive gave it. The model carries the act's
				// changes and the catalogue what chains need of it (lexdania.ArtifactRecords).
				m, rec := lexdania.ArtifactRecords(entryOf[acc], doc, path)
				records[lo+i] = rec
				batch.frames[i], errs[i] = wr.Compile(acc, m)
			})
			compileNanos += int64(time.Since(tCompile))
			for i := range opened {
				if !opened[i] {
					batch.missing++
				}
			}
			// The first failing document by file order, not by whichever worker
			// noticed first: the error names one document and the reader should get
			// the same name from the same broken mirror.
			for i := range errs {
				if errs[i] != nil {
					batch.firstErr, batch.failed = errs[i], want[lo+i]
					break
				}
			}
			batches <- batch
			if batch.firstErr != nil {
				return
			}
		}
	}()

	var unreadable int
	for batch := range batches {
		if batch.firstErr != nil {
			f.Close()
			// The half-written file is 400 MB of nothing on a build that died this way once
			// already; leaving it is a second problem the same failure has to cause.
			os.Remove(tmp)
			return "", fmt.Errorf("%s: %w", batch.failed, batch.firstErr)
		}
		unreadable += batch.missing
		tWrite := time.Now()
		for _, fr := range batch.frames {
			if err := wr.WriteFrame(fr); err != nil {
				f.Close()
				os.Remove(tmp)
				return "", fmt.Errorf("%s: %w", fr.Accession, err)
			}
		}
		writeNanos += int64(time.Since(tWrite))
	}
	loop := time.Since(tLoop)
	tTables := time.Now()
	// The catalogue lists every entry of the archive, in the order the scan found them —
	// the order the consolidation indexes inherit — with what was read of each document.
	read := make(map[string]lexdania.CatalogueRecord, len(want))
	for i, acc := range want {
		if records[i].Readable {
			read[acc] = records[i]
		}
	}
	var catalogue []lexdania.CatalogueRecord
	for _, e := range a.ScanOrder() {
		rec := read[e.Accession]
		rec.Entry = e
		catalogue = append(catalogue, rec)
	}
	payload, err := json.Marshal(catalogue)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	wr.SetCatalogue(payload)
	if err := wr.Close(); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if install {
		// Installed under the name its own header gives it, and made current only
		// now that it is complete and opens.
		if out, err = artifactdir.Install(o.DataDir, tmp); err != nil {
			return "", err
		}
		if removed, err := artifactdir.Prune(o.DataDir); err != nil {
			slog.Warn("could not prune old artifacts", "err", err)
		} else if len(removed) > 0 {
			slog.Info("pruned artifacts no longer kept", "removed", removed)
		}
	} else if err := os.Rename(tmp, out); err != nil {
		return "", err
	}
	tables := time.Since(tTables)
	st, err := os.Stat(out)
	if err != nil {
		return "", err
	}

	opened, err := compiled.OpenArtifact(out)
	if err != nil {
		return "", err
	}
	defer opened.Close()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	// The size distribution is printed because the bound on a document's size is a claim
	// about this corpus, and a claim of that kind was previously asserted at 8 MiB without
	// ever having been counted — which is what made a whole-mirror build impossible.
	bands, counts, largest, rawTotal := wr.SizeDistribution()
	slog.Info("artifact built",
		"path", out,
		"documents", wr.Documents(),
		"bytes", st.Size(),
		"model_json", archive.SizeHint(int64(rawTotal)),
		"largest_document_json", archive.SizeHint(int64(largest)),
		"document_sizes", sizeHistogram(bands, counts),
		"entries without an accession", noAccession,
		"duplicate accessions", duplicates,
		"unreadable", unreadable,
		"elapsed", time.Since(start).Round(time.Millisecond),
		"loop", loop.Round(time.Millisecond),
		"compile", time.Duration(compileNanos).Round(time.Millisecond),
		"write", time.Duration(writeNanos).Round(time.Millisecond),
		"tables", tables.Round(time.Millisecond),
		"heap_in_use_mb", float64(ms.HeapInuse)/1e6,
		"rss", progress.PeakRSS(),
	)
	// The coverage claim is the number a reader of an answer cannot see for themselves, so
	// it is printed with what was left out rather than as a bare count.
	fmt.Fprintf(o.Report, "%s: %d documents, %.1f MB, built %s by %s, %s; model JSON %s, largest document %s (%s); left out: %d without an accession, %d duplicate, %d unreadable\n",
		out, wr.Documents(), float64(st.Size())/1e6,
		opened.Header.Created.Format(time.RFC3339), stampedAs(opened.Header),
		time.Since(start).Round(time.Millisecond),
		archive.SizeHint(int64(rawTotal)), archive.SizeHint(int64(largest)), sizeHistogram(bands, counts),
		noAccession, duplicates, unreadable)
	return out, nil
}

// sizeHistogram renders the compiled document sizes as one log field, e.g.
// "<8 KiB:152310 <64 KiB:31000 <1 MiB:6012 <8 MiB:990 <64 MiB:13 >=64 MiB:4". The last band is
// open at the top because it is the one that matters: it holds the documents the old bound refused.
func sizeHistogram(bands []int, counts []int64) string {
	parts := make([]string, 0, len(counts))
	for i, c := range counts {
		if i < len(bands) {
			parts = append(parts, fmt.Sprintf("<%s:%d", archive.SizeHint(int64(bands[i])), c))
			continue
		}
		parts = append(parts, ">="+archive.SizeHint(int64(bands[len(bands)-1]))+":"+strconv.FormatInt(c, 10))
	}
	return strings.Join(parts, " ")
}

// stampedAs names the build that wrote an artifact the way the refusal names it, so the line an
// operator reads after a build matches the line they read after a server refuses the file.
func stampedAs(h compiled.ArtifactHeader) string {
	stamp := h.Build
	if len(stamp) > 12 {
		stamp = stamp[:12]
	}
	if stamp == "" {
		return "an unstamped build"
	}
	if h.Modified {
		return stamp + "-dirty"
	}
	return stamp
}
