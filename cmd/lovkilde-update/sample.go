// The sample command: pulling the ground truth the HTML reader is measured against.
//
// It is deliberately not part of fetch. fetch mutates the mirror -- queue rows,
// archive files -- and is the thing a daily cron runs; this reads the archive,
// spends requests on a hand-picked list, and writes next to it. Mixing the two
// would put measurement scaffolding on the same code path as the product.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lkarlslund/lovkilde/internal/crawl"
	"github.com/lkarlslund/lovkilde/internal/limiter"
)

// sampleSubdir is where the manifest and payloads live, beside the archive rather
// than inside it.
const sampleSubdir = "paired-truth"

func sampleCmd(open openFunc) *cobra.Command {
	var plan, get, listStrata, overwrite bool
	var quotas []string
	var seed int64
	var out, manifest, dir string
	var limit int
	var kinds []string
	cmd := &cobra.Command{
		Use:   "sample",
		Short: "Fetch paired documents: canonical XML against the site's own HTML",
		Long: `Measure the HTML body reader against ground truth.

The reference is the canonical <DokumentIndhold> already in the archive, not a
third party's transcription of it. Two representations published by the same
publisher from the same source can disagree usefully; an aggregator's text can
only agree with itself. So the documents sampled here are the ones whose XML DOES
carry the text -- the opposite population to "fetch --bodies".

Two phases, because reading the archive costs CPU and the second phase costs
requests. Plan reads the archive and writes a manifest of what it would fetch;
fetch works off that manifest and can be interrupted at any point:

  lovkilde-update sample --plan --quota centreret=300 --quota random=500 \
                --seed 7 --out data/paired-truth/manifest.tsv
  lovkilde-update sample --fetch --manifest data/paired-truth/manifest.tsv --limit 30

The payload of each sampled document is written beside nothing: it goes to
--dir in the archive's own layout (data/paired-truth/lta/1942/542.json), never
into data/xml. A sidecar next to a document that already has canonical XML would
make the loader's rule ambiguous in precisely the files used to test it.

A quota is stratum=count. --strata lists the strata and what each one is for;
targeted strata are filled before the random one, so the scarce shapes cannot
lose a contention with the largest bucket. A quota the corpus cannot fill is
reported as a shortfall rather than quietly rounded down.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !plan && !get && !listStrata {
				return fmt.Errorf("say what to do: --strata, --plan, or --fetch")
			}
			if plan && get {
				return fmt.Errorf("--plan and --fetch are separate runs, so the sample can be reviewed before requests are spent")
			}
			if listStrata {
				for _, s := range crawl.Strata() {
					fmt.Printf("%-10s %s\n", s.Name, s.Why)
				}
				return nil
			}
			a, lim, err := open(cmd)
			if err != nil {
				return err
			}
			defer a.Close()
			kinds = cleanList(kinds)
			base := filepath.Join(a.cfg.DataDir, sampleSubdir)
			if plan {
				if out == "" {
					out = filepath.Join(base, "manifest.tsv")
				}
				parsed, err := crawl.ParseQuotas(quotas)
				if err != nil {
					return err
				}
				return runWith(a, lim, func(ctx context.Context) error {
					return a.samplePlan(ctx, crawl.SamplePlanOptions{
						Quotas: parsed, Seed: seed, Kinds: kinds, Out: out,
					})
				})
			}
			if manifest == "" {
				manifest = filepath.Join(base, "manifest.tsv")
			}
			if dir == "" {
				dir = base
			}
			return runWith(a, lim, func(ctx context.Context) error {
				return a.sampleFetch(ctx, lim, crawl.SampleFetchOptions{
					Manifest: manifest, Dir: dir, Limit: limit, Overwrite: overwrite,
				})
			})
		},
	}
	cmd.Flags().BoolVar(&listStrata, "strata", false, "list the strata and what each one exists to measure, requesting nothing")
	cmd.Flags().BoolVar(&plan, "plan", false, "read the archive and write a manifest: stratified, seeded, zero requests")
	cmd.Flags().BoolVar(&get, "fetch", false, "fetch the payloads listed in a manifest, paced like any other document request")
	cmd.Flags().StringSliceVar(&quotas, "quota", nil,
		"how many documents to take from a stratum, e.g. centreret=300 (repeatable). "+
			"Use --strata for the names and what each one means")
	cmd.Flags().Int64Var(&seed, "seed", 0,
		"seed for the shuffle; 0 picks one and logs it at debug level. The seed is written into the manifest, so a plan can be reproduced")
	cmd.Flags().StringVar(&out, "out", "",
		"manifest to write with --plan (default: <data-dir>/"+sampleSubdir+"/manifest.tsv)")
	cmd.Flags().StringVar(&manifest, "manifest", "",
		"manifest to read with --fetch (default: <data-dir>/"+sampleSubdir+"/manifest.tsv)")
	cmd.Flags().StringVar(&dir, "dir", "",
		"directory for fetched payloads (default: <data-dir>/"+sampleSubdir+"), mirroring the archive layout")
	cmd.Flags().IntVar(&limit, "limit", 0, "stop after N manifest rows (useful for a trial run)")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false,
		"re-fetch rows whose payload is already on disk; by default they are counted and skipped, which is what makes an interrupted sample resumable")
	cmd.Flags().StringSliceVar(&kinds, "kind", nil, "with --plan, only sample these ELI kinds (repeatable)")
	return cmd
}

// samplePlan runs the read-only pass and prints what the sample looks like, because
// a manifest nobody inspects is a request budget spent on an unknown distribution.
func (a *app) samplePlan(ctx context.Context, opt crawl.SamplePlanOptions) error {
	rep, err := a.crawl.PlanBodySample(ctx, opt)
	if err != nil {
		return err
	}
	slog.Info("sample planned", "scanned", rep.Scanned, "with_xml_body", rep.WithXMLBody,
		"documents", rep.Chosen, "seed", rep.Seed, "manifest", rep.Manifest)
	for _, name := range crawl.StratumNames() {
		got, ok := rep.Got[name]
		if !ok {
			continue
		}
		if short := rep.Short[name]; short > 0 {
			slog.Warn("stratum quota unfillable from this corpus", "stratum", name,
				"taken", got, "short", short)
			continue
		}
		slog.Info("stratum", "stratum", name, "taken", got)
	}
	dist := make([]string, 0, len(rep.Kinds))
	for k, n := range rep.Kinds {
		dist = append(dist, fmt.Sprintf("%s=%d", k, n))
	}
	// Sorted, because a map's iteration order would make the same plan print two
	// different distributions and a log line you cannot diff against itself is
	// noise.
	sort.Strings(dist)
	slog.Info("sample kinds", "distribution", strings.Join(dist, " "))
	return nil
}

// sampleFetch spends the requests, on the document bucket's pacing.
func (a *app) sampleFetch(ctx context.Context, lim *limiter.Limiter, opt crawl.SampleFetchOptions) error {
	rows, err := crawl.ReadSampleManifest(opt.Manifest)
	if err != nil {
		return err
	}
	total := int64(len(rows))
	if opt.Limit > 0 && opt.Limit < len(rows) {
		total = int64(opt.Limit)
	}
	bar := newFetchBar(lim, progressModeValue, total, "sample")
	defer bar.Finish()
	var barWarned bool
	st, err := a.crawl.FetchBodySample(ctx, crawl.SampleFetchOptions{
		Manifest: opt.Manifest, Dir: opt.Dir, Limit: opt.Limit, Overwrite: opt.Overwrite,
		OnProgress: func(s crawl.SampleStats) {
			if err := bar.Set(int64(s.Attempted)); err != nil && !barWarned {
				barWarned = true
				slog.Warn("progress bar not updating", "err", err)
			}
			if bar.Enabled() {
				bar.Describe(pacingLabel(lim, "sample"))
			}
		},
	})
	if err != nil {
		return err
	}
	slog.Info("sample on disk", "stored", st.Stored, "already_held", st.Held,
		"without_text", st.Empty, "failed", st.Failed, "dir", opt.Dir)
	return nil
}
