package lexdania

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The full-corpus scans exist to find bugs, not to assert. They are opt-in because
// they read every byte of the archive, and they report histograms rather than
// failing: a new rare element or an unclassified change wording is information, and
// the interesting cases are the rare ones.
//
//	RETSIN_FULL_SCAN=1 go test -timeout 4h -run TestScanAll ./internal/lexdania/ -v
//
// The crawl writes concurrently, so a file can be read half-written. Parse failures
// are counted and reported by cause, never fatal.
const fullScanEnv = "RETSIN_FULL_SCAN"

func fullScanFiles(t *testing.T) []string {
	if os.Getenv(fullScanEnv) == "" {
		t.Skipf("set %s=1 to scan the whole archive", fullScanEnv)
	}
	root, err := filepath.Abs(corpusDir(t))
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // a directory being written while we walk it
		}
		if !d.IsDir() && strings.HasSuffix(p, ".xml") {
			files = append(files, p)
		}
		return nil
	})
	if len(files) == 0 {
		t.Skip("no files")
	}
	return files
}

// fanOut runs fn over files on several goroutines and merges the results.
func fanOut[T any, R any](files []T, workers int, fn func(T) R, merge func(R)) {
	workers = max(workers, 1)
	var mu sync.Mutex
	var wg sync.WaitGroup
	ch := make(chan T)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range ch {
				r := fn(f)
				mu.Lock()
				merge(r)
				mu.Unlock()
			}
		}()
	}
	for _, f := range files {
		ch <- f
	}
	close(ch)
	wg.Wait()
}

func topN(m map[string]int, n int) []string {
	all := make([]kvPair, 0, len(m))
	for k, v := range m {
		all = append(all, kvPair{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	return formatKV(all, n)
}

func rarest(m map[string]int, n int) []string {
	all := make([]kvPair, 0, len(m))
	for k, v := range m {
		all = append(all, kvPair{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v < all[j].v
		}
		return all[i].k < all[j].k
	})
	return formatKV(all, n)
}

type kvPair struct {
	k string
	v int
}

func formatKV(all []kvPair, n int) []string {
	out := make([]string, 0, min(n, len(all)))
	for _, e := range all[:min(n, len(all))] {
		out = append(out, fmtCount(e.v, e.k))
	}
	return out
}

func fmtCount(v int, k string) string { return strconv.Itoa(v) + "\t" + k }

// readFileHead reads the first bytes of a file, enough to tell an HTML payload from
// truncated XML when a parse fails.
func readFileHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, n)
	m, _ := io.ReadFull(f, b)
	return b[:m], nil
}

// scanAgg is everything worth counting while reading every document once.
type scanAgg struct {
	docs, bytes, parseFail, htmlFail, otherFail, invariantBad int
	parseFailSample                                           []string
	unmappedMeta                                              map[string]int
	metaWarnings                                              map[string]int
	accScheme                                                 map[string]int
	accBad                                                    map[string]int
	rootKids                                                  map[string]int
	elements                                                  map[string]int
	provisionLike                                             map[string]int
	zeroSectionDocsWithParagraf                               []string
	duplicateNumbers                                          map[string]int
	stkTextNoStk                                              []string
	nrTextNoNr                                                []string
	changes, changesUnknown                                   int
	unhandled, approximate                                    map[string]int
	unhandledTotal, unhandledDocs                             int
	unknownKindProse                                          map[string]int
	unrecognisedAddress                                       map[string]int
	badDates                                                  map[string]int
	docTypes                                                  map[string]int
	bills, amenders                                           int
	editsByKind, unknownKind, noTarget, unresolvedByReason    map[string]int
	appliedSamples                                            map[string]int
	applied, chains, chainsSkipped                            int
	addrTotal, addrFound, addrMissed                          int
	addrInsertion                                             int
	addrForeign                                               int
	addrAddressless                                           int
	addrMissedSample                                          []string
	addrMissedByKind                                          map[string]int

	// Second-pass checks: each is a hypothesis that a shape is not being recognised,
	// stated so that the raw text of the document decides.
	exitusNoStk       int
	exitusNoStkSample []string
	afsnitRoman       int
	afsnitRomanSample []string
	notaNr            int
	indledningNr      int
	renumberDocs      int
	renumberEdits     int
	dupByKind         map[string]int
	dupSample         []string
	paraAncestor      map[string]int
	appendixDocs      int
	appendixSections  int
	collidingSections int
	pktRefs           int
	// handledUnits counts the shapes the library now has a mechanism for, taken from
	// Edit.Kind and Address rather than from the prose, so the number moves when the
	// library moves rather than when this file's regexes do.
	handledUnits map[string]int
	// residueSamples keeps a few verbatim definitions per refusal bucket: the counts say
	// how much is left, only the wording says whether a fix is one regex or a new model.
	residueSamples map[string][]string
}

func newScanAgg() *scanAgg {
	return &scanAgg{
		unmappedMeta: map[string]int{}, metaWarnings: map[string]int{},
		accScheme: map[string]int{}, accBad: map[string]int{},
		rootKids: map[string]int{}, elements: map[string]int{},
		provisionLike: map[string]int{}, duplicateNumbers: map[string]int{},
		unknownKindProse: map[string]int{}, unrecognisedAddress: map[string]int{},
		badDates: map[string]int{}, docTypes: map[string]int{},
		dupByKind: map[string]int{}, paraAncestor: map[string]int{},
		editsByKind: map[string]int{}, unknownKind: map[string]int{},
		noTarget: map[string]int{}, unresolvedByReason: map[string]int{},
		appliedSamples: map[string]int{}, addrMissedByKind: map[string]int{},
		unhandled: map[string]int{}, approximate: map[string]int{}, handledUnits: map[string]int{}, residueSamples: map[string][]string{},
	}
}

// merge folds one per-file aggregate into the total. Written out explicitly: a map
// cannot be compared, so there is no clever generic version worth having here.
func (a *scanAgg) merge(b *scanAgg) {
	a.docs += b.docs
	a.bytes += b.bytes
	a.parseFail += b.parseFail
	a.htmlFail += b.htmlFail
	a.otherFail += b.otherFail
	a.invariantBad += b.invariantBad
	a.changes += b.changes
	a.changesUnknown += b.changesUnknown
	a.unhandledTotal += b.unhandledTotal
	a.unhandledDocs += b.unhandledDocs
	for k, v := range b.unhandled {
		a.unhandled[k] += v
	}
	for k, v := range b.handledUnits {
		a.handledUnits[k] += v
	}
	for k, v := range b.residueSamples {
		if len(a.residueSamples[k]) < 40 { // generous so the distinct list stays varied
			a.residueSamples[k] = append(a.residueSamples[k], v...)
		}
	}
	for k, v := range b.approximate {
		a.approximate[k] += v
	}
	a.bills += b.bills
	a.amenders += b.amenders
	a.parseFailSample = append(a.parseFailSample, b.parseFailSample...)
	a.zeroSectionDocsWithParagraf = append(a.zeroSectionDocsWithParagraf, b.zeroSectionDocsWithParagraf...)
	a.stkTextNoStk = append(a.stkTextNoStk, b.stkTextNoStk...)
	a.nrTextNoNr = append(a.nrTextNoNr, b.nrTextNoNr...)
	a.exitusNoStk += b.exitusNoStk
	a.afsnitRoman += b.afsnitRoman
	a.notaNr += b.notaNr
	a.indledningNr += b.indledningNr
	a.renumberDocs += b.renumberDocs
	a.renumberEdits += b.renumberEdits
	a.pktRefs += b.pktRefs
	a.exitusNoStkSample = append(a.exitusNoStkSample, b.exitusNoStkSample...)
	a.afsnitRomanSample = append(a.afsnitRomanSample, b.afsnitRomanSample...)
	a.dupSample = append(a.dupSample, b.dupSample...)
	for k, v := range b.dupByKind {
		a.dupByKind[k] += v
	}
	for k, v := range b.paraAncestor {
		a.paraAncestor[k] += v
	}
	a.applied += b.applied
	a.chainsSkipped += b.chainsSkipped
	a.addrTotal += b.addrTotal
	a.addrAddressless += b.addrAddressless
	a.addrInsertion += b.addrInsertion
	a.addrForeign += b.addrForeign
	for k, v := range b.addrMissedByKind {
		a.addrMissedByKind[k] += v
	}
	a.addrFound += b.addrFound
	a.addrMissed += b.addrMissed
	a.addrMissedSample = append(a.addrMissedSample, b.addrMissedSample...)
	a.chains += b.chains
	for k, v := range b.editsByKind {
		a.editsByKind[k] += v
	}
	for k, v := range b.unknownKind {
		a.unknownKind[k] += v
	}
	for k, v := range b.noTarget {
		a.noTarget[k] += v
	}
	for k, v := range b.unresolvedByReason {
		a.unresolvedByReason[k] += v
	}
	for k, v := range b.appliedSamples {
		a.appliedSamples[k] += v
	}
	a.appendixDocs += b.appendixDocs
	a.appendixSections += b.appendixSections
	a.collidingSections += b.collidingSections
	for _, pair := range []struct {
		dst, src map[string]int
	}{
		{a.unmappedMeta, b.unmappedMeta}, {a.metaWarnings, b.metaWarnings},
		{a.accScheme, b.accScheme}, {a.accBad, b.accBad},
		{a.rootKids, b.rootKids}, {a.elements, b.elements},
		{a.provisionLike, b.provisionLike}, {a.duplicateNumbers, b.duplicateNumbers},
		{a.unknownKindProse, b.unknownKindProse}, {a.unrecognisedAddress, b.unrecognisedAddress},
		{a.badDates, b.badDates}, {a.docTypes, b.docTypes},
	} {
		for k, v := range pair.src {
			pair.dst[k] += v
		}
	}
}

// TestScanAllDocuments reads every document and reports every structural surprise.
func TestScanAllDocuments(t *testing.T) {
	files := fullScanFiles(t)
	root, err := filepath.Abs(corpusDir(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("scanning %d files", len(files))

	total := newScanAgg()
	fanOut(files, min(runtime.NumCPU(), 12), func(path string) *scanAgg {
		a := newScanAgg()
		if info, err := os.Stat(path); err == nil {
			a.bytes = int(info.Size())
		}
		doc, err := Load(path)
		if err != nil {
			a.parseFail++
			head, _ := readFileHead(path, 200)
			if IsHTMLPayload(head) {
				a.htmlFail++
			} else {
				a.otherFail++
				if len(a.parseFailSample) < 8 {
					a.parseFailSample = append(a.parseFailSample, path+": "+err.Error())
				}
			}
			return a
		}
		a.docs++
		a.docTypes[doc.Meta.DocumentType]++
		if doc.IsBill() {
			a.bills++
		}
		if doc.IsAmendment() && !doc.IsBill() {
			a.amenders++
		}
		if err := doc.Root.checkInvariant(""); err != nil {
			a.invariantBad++
		}
		for _, w := range doc.Warnings() {
			a.metaWarnings[classifyWarning(w)]++
		}
		for _, n := range doc.Meta.Rest {
			a.unmappedMeta[n.Name]++
		}
		acc := doc.Accession()
		a.accScheme[acc.Scheme+"|rank="+strings.ToLower(doc.Meta.Rank)]++
		if !acc.WellFormed {
			a.accBad[acc.Full+"  in "+filepath.Base(filepath.Dir(path))]++
		}
		for _, c := range doc.Root.Elems {
			a.rootKids[c.Name]++
		}
		noteElements(doc.Root, nil, a)

		secs := doc.Sections()
		if len(paragraphs(secs)) == 0 && a.elements["Paragraf"] > 0 {
			a.zeroSectionDocsWithParagraf = append(a.zeroSectionDocsWithParagraf, path)
		}
		seen := map[string]bool{}
		dups := 0
		var bodyNumbers, appendixNumbers []string
		for _, s := range secs {
			if s.Kind != "paragraf" || s.Parent != nil {
				continue
			}
			key := s.Number + s.Litra
			if s.Appendix {
				appendixNumbers = append(appendixNumbers, key)
			} else {
				bodyNumbers = append(bodyNumbers, key)
				if seen[key] {
					dups++
				}
			}
			seen[key] = true
		}
		// Entry-into-force quotations keep the number they had in their own amending
		// act, so they collide with the law's numbering constantly. Counting them with
		// the body's own duplicates hid what was left unexplained.
		if len(appendixNumbers) > 0 {
			a.appendixDocs++
			a.appendixSections += len(appendixNumbers)
			inBody := map[string]bool{}
			for _, n := range bodyNumbers {
				inBody[n] = true
			}
			for _, n := range appendixNumbers {
				if inBody[n] {
					a.collidingSections++
				}
			}
		}
		if dups > 0 {
			// Large amendment acts legitimately restart block numbering for each law
			// they touch, so duplicates only mean anything with the class attached.
			cls := "law"
			if doc.IsBill() {
				cls = "bill"
			} else if doc.IsAmendment() {
				cls = "amender"
			}
			a.dupByKind[cls+"|"+filepath.Base(filepath.Dir(path))]++
			if cls == "law" && len(a.dupSample) < 20 {
				a.dupSample = append(a.dupSample, strconv.Itoa(dups)+"\t"+path)
			}
		}
		// A paragraph whose subsections are written as repeated <Exitus> with no <Stk>
		// wrapper cannot be numbered stk. 1, 2, 3 — check whether that shape exists.
		if n := exitusWithoutStk(doc.Root); n > 0 {
			a.exitusNoStk += n
			if len(a.exitusNoStkSample) < 10 {
				a.exitusNoStkSample = append(a.exitusNoStkSample, path)
			}
		}
		if n := afsnitWithRomanNumber(doc.Root); n > 0 {
			a.afsnitRoman += n
			if len(a.afsnitRomanSample) < 10 {
				a.afsnitRomanSample = append(a.afsnitRomanSample, path)
			}
		}
		a.notaNr += countNamed(doc.Root, "Nota", "Fodnote")
		a.indledningNr += countNamed(doc.Root, "Indledning")
		// The text mentions a construct the tree never produced: a strong hint that a
		// shape is unrecognised rather than that the law lacks it.
		if len(secs) > 0 {
			text := doc.Root.PlainText()
			if len(stkSections(secs)) == 0 && strings.Contains(text, "stk.") && len(a.stkTextNoStk) < 3 {
				a.stkTextNoStk = append(a.stkTextNoStk, path)
			}
			if len(nrSections(secs)) == 0 && strings.Contains(text, "nr.") && len(a.nrTextNoNr) < 3 {
				a.nrTextNoNr = append(a.nrTextNoNr, path)
			}
		}
		renumberHere, unhandledHere := 0, 0
		for _, e := range doc.Amendments() {
			a.changes++
			a.editsByKind[string(e.Kind)]++
			if isRenumbering(e.Definition) {
				a.renumberEdits++
				renumberHere++
			}
			if strings.Contains(e.Address.Raw, "pkt.") {
				a.pktRefs++
			}
			for _, unit := range handledUnits(e) {
				a.handledUnits[unit]++
			}
			if why := unhandledReason(e); why != "" {
				a.residueSamples[why] = append(a.residueSamples[why], firstWords(e.Definition, 9))
				a.unhandled[why]++
				a.unhandledTotal++
				unhandledHere++
			} else if why := approxPosition(e); why != "" {
				a.approximate[why]++
			}
			switch {
			case e.Kind == EditUnknown:
				a.changesUnknown++
				a.unknownKindProse[Squeeze(e.Definition)]++
				// Grouped by opening words: the quoted text after them differs every
				// time, which buries the pattern in a histogram of one-offs.
				a.unknownKind[firstWords(e.Definition, 3)]++
			case e.Address.Raw == "":
				a.noTarget[Squeeze(e.Definition)]++
			case !e.Address.Recognised:
				a.unrecognisedAddress[Squeeze(e.Address.Raw)]++
			}
		}
		if renumberHere > 0 {
			a.renumberDocs++
		}
		if unhandledHere > 0 {
			a.unhandledDocs++
		}
		for _, r := range doc.Meta.References {
			if r.Effective != "" && len(r.Effective) <= 40 {
				if _, ok := ParseDate(r.Effective); !ok {
					a.badDates[r.Effective]++
				}
			}
		}
		return a
	}, func(a *scanAgg) { total.merge(a) })

	t.Logf("docs %d (%d GB) | parse failures %d (html %d, other %d) | invariant violations %d",
		total.docs, total.bytes/1_000_000_000, total.parseFail, total.htmlFail, total.otherFail, total.invariantBad)
	t.Logf("bills %d | amending acts %d | numbered changes %d | unclassified %d (%.2f%%)",
		total.bills, total.amenders, total.changes, total.changesUnknown,
		100*float64(total.changesUnknown)/max(1, float64(total.changes)))

	report := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		t.Logf("--- %s (%d) ---", title, len(lines))
		for _, l := range lines {
			t.Logf("  %s", l)
		}
	}
	report("unmapped Meta children", topN(total.unmappedMeta, 25))
	report("Meta warnings by class", topN(total.metaWarnings, 20))
	report("accession scheme|rank", topN(total.accScheme, 25))
	report("malformed accessions", topN(total.accBad, 20))
	report("root children", topN(total.rootKids, 25))
	report("provision-like elements (carry Explicatus)", topN(total.provisionLike, 25))
	report("DocumentType", topN(total.docTypes, 25))
	report("edits by kind", topN(total.editsByKind, 14))
	t.Logf("of %d numbered changes, %d (%.2f%%) cannot be applied as written, in %d documents",
		total.changes, total.unhandledTotal, 100*float64(total.unhandledTotal)/math.Max(1, float64(total.changes)),
		total.unhandledDocs)
	report("CANNOT APPLY, by reason (first match wins, so the buckets are disjoint)", topN(total.unhandled, 20))
	report("applied but positioned approximately", topN(total.approximate, 10))
	report("unknown kind by first words", topN(total.unknownKind, 25))
	report("edits that name no target", topN(total.noTarget, 25))
	report("UNCLASSIFIED change wording", topN(total.unknownKindProse, 40))
	report("addresses NOT recognised", topN(total.unrecognisedAddress, 40))
	report("effective dates that did not parse", topN(total.badDates, 20))
	report("rarest elements", rarest(total.elements, 40))
	t.Logf("appendix material: %d documents, %d provisions, %d of them numbered like a provision of the law itself", total.appendixDocs, total.appendixSections, total.collidingSections)
	report("provision ancestors (grandparent>parent >> element)", topN(total.paraAncestor, 30))

	report("docs with <Paragraf> but no sections", total.zeroSectionDocsWithParagraf)
	report("text says stk. but no stk sections (confounded by citation text)", total.stkTextNoStk)
	report("duplicate provision numbers by class|dir", topN(total.dupByKind, 20))
	report("duplicate numbers in non-amending documents", total.dupSample)
	report("Paragraf with repeated Exitus but no Stk", total.exitusNoStkSample)
	report("Afsnit with roman printed numbers", total.afsnitRomanSample)
	report("address units now reachable, counted from the library's own parse", topN(total.handledUnits, 12))
	for reason := range total.unhandled {
		report("refusal samples: "+reason, dedupe(total.residueSamples[reason], 10))
	}
	t.Logf("structural hypotheses: Paragraf repeated-Exitus-no-Stk = %d | afsnit roman = %d | "+
		"nota/fodnote elements = %d | indledning elements = %d | renumbering edits = %d in %d docs | "+
		"addresses citing pkt. = %d",
		total.exitusNoStk, total.afsnitRoman, total.notaNr, total.indledningNr,
		total.renumberEdits, total.renumberDocs, total.pktRefs)
	// Address resolution against the real target laws needs the whole archive indexed,
	// so it runs after the per-file pass rather than inside it.
	if ar, err := Scan(context.Background(), root); err != nil {
		t.Logf("target phase skipped: %v", err)
	} else {
		scanTargets(ar, total)
		t.Logf("addresses resolved against the published text of their target law: %d of %d (%.2f%%), %d missed, "+
			"%d address-less instructions excluded, "+
			"%d insertion addresses excluded because they name a position that does not exist yet, "+
			"%d changes belonging to other laws excluded",
			total.addrFound, total.addrTotal,
			100*float64(total.addrFound)/math.Max(1, float64(total.addrTotal)),
			total.addrMissed, total.addrAddressless, total.addrInsertion, total.addrForeign)
		report("missed addresses by edit kind", topN(total.addrMissedByKind, 8))
		report("addresses that do not resolve in their target law", total.addrMissedSample)
	}
	report("parse failure samples", total.parseFailSample)
}

// noteElements records element names and which elements carry an Explicatus, i.e.
// which ones look like a numbered provision.
func noteElements(n *Node, parent *Node, a *scanAgg) {
	a.elements[n.Name]++
	if parent != nil {
		for _, c := range n.Elems {
			if paragraphNames[c.Name] {
				a.paraAncestor[parent.Name+">"+n.Name+" >> "+c.Name]++
			}
		}
	}
	hasExplicatus := false
	for _, c := range n.Elems {
		if c.Name == "Explicatus" {
			hasExplicatus = true
		}
		noteElements(c, n, a)
	}
	if hasExplicatus {
		a.provisionLike[n.Name]++
	}
}

// paragraphNames mirrors the paragraphish list in sections.go so the ancestor
// histogram answers "where do provisions actually live?".
var paragraphNames = map[string]bool{
	"Paragraf": true, "TilParagraf": true, "AendringCentreretParagraf": true,
	"IkraftCentreretParagraf": true,
}

func paragraphs(secs []*Section) []*Section  { return byKind(secs, "paragraf") }
func stkSections(secs []*Section) []*Section { return byKind(secs, "stk") }
func nrSections(secs []*Section) []*Section  { return byKind(secs, "nr") }

func byKind(secs []*Section, kind string) []*Section {
	var out []*Section
	for _, s := range secs {
		if s.Kind == kind {
			out = append(out, s)
		}
	}
	return out
}

func classifyWarning(w string) string {
	if i := strings.IndexByte(w, ':'); i > 0 {
		return w[:i]
	}
	if i := strings.IndexByte(w, ' '); i > 0 {
		return w[:i]
	}
	return w
}

// exitusWithoutStk counts Paragraf elements carrying two or more Exitus children and
// no Stk wrapper: subsections that exist in the text but cannot be numbered.
func exitusWithoutStk(n *Node) int {
	out := 0
	for _, c := range n.Elems {
		if c.Name == "Paragraf" {
			exitus, stk := 0, 0
			for _, g := range c.Elems {
				switch g.Name {
				case "Exitus":
					exitus++
				case "Stk":
					stk++
				}
			}
			if stk == 0 && exitus >= 2 {
				out++
			}
		}
		out += exitusWithoutStk(c)
	}
	return out
}

// afsnitWithRomanNumber counts Afsnit elements whose printed number is roman, e.g.
// "Afsnit IX", which an arabic-only address parser cannot match.
func afsnitWithRomanNumber(n *Node) int {
	out := 0
	for _, c := range n.Elems {
		if c.Name == "Afsnit" {
			if e := c.Elem("Explicatus").TextOrNull(); strings.ContainsAny(e, "IVXLCDM") {
				out++
			}
		}
		out += afsnitWithRomanNumber(c)
	}
	return out
}

func countNamed(n *Node, names ...string) int {
	out := 0
	for _, c := range n.Elems {
		for _, nm := range names {
			if c.Name == nm {
				out++
			}
		}
		out += countNamed(c, names...)
	}
	return out
}

// isRenumbering matches the Danish "X bliver herefter Y" renumbering instruction,
// which is a distinct edit kind rather than an unknown one.
func isRenumbering(def string) bool {
	low := strings.ToLower(def)
	return strings.Contains(low, "bliver herefter")
}

// firstWords returns the opening words of a change, which is the shape worth grouping
// unclassified changes by; the quoted text after them differs every time.
// dedupe keeps the first n distinct entries, so a sample reports wording *shapes*
// rather than ten copies of whichever refusal happened to be commonest.
func dedupe(in []string, n int) []string {
	seen := make(map[string]bool, n)
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= n {
			break
		}
	}
	return out
}

func firstWords(s string, n int) string {
	words := strings.Fields(Squeeze(s))
	if len(words) > n {
		words = words[:n]
	}
	return strings.Join(words, " ")
}

// scanTargets measures whether an amendment's address can be located in the law it
// changes — the instrument that matters for consolidation, because an address that
// cannot be found cannot be applied however well the rest works.
//
// Two things make the sampling non-obvious. Chains are assembled from the *law's*
// <Change> anchors: only about 1 in 70 amending acts declares the reverse anchor, so
// starting from amenders finds almost nothing. And each address is resolved against the
// text as it stood immediately before that amendment took effect, not against the
// consolidated act as published — a consolidated act already contains its own
// amendments, so resolving against it reports "not found" for well-formed changes.
//
// Modern law kinds only: entry order starts at 1852, where chains are re-accessions of
// century-old acts rather than modern amending acts.
func scanTargets(ar *Archive, total *scanAgg) {
	const maxLaws = 60
	entries := ar.Entries()
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Year > entries[j].Year })

	laws := 0
	for _, e := range entries {
		if laws >= maxLaws {
			return
		}
		switch e.Kind {
		case "lta", "ltb", "ltc", "mt":
		default:
			continue
		}
		if e.Year < 2010 {
			continue
		}
		law, _, ok := ar.Open(e.Accession)
		if !ok || law == nil || law.IsAmendment() || law.IsBill() {
			// Amending acts are filed in the same kinds as the laws they change, and
			// their own §§ are framing paragraphs: resolving a target-law address
			// against those measures nothing.
			continue
		}
		vs, err := ar.Versions(e.Accession)
		if err != nil || len(vs) == 0 {
			continue
		}
		// A consolidated act already contains the amendments that were in force when it
		// was published, so only amendments dated after the act's own date are still
		// pending — and those are the ones whose addresses refer to the text as published.
		// Resolving an already-merged amendment against the consolidated text measures
		// the archive's bookkeeping, not the parser.
		pending, hasPending := pendingAmender(ar, law, vs)
		if !hasPending {
			continue
		}
		laws++
		secs := law.Sections()
		for _, ch := range pending.Amendments() {
			if !ch.Address.Recognised {
				continue
			}
			// A multi-law amendment act repeats its block structure per law it touches,
			// so its changes address the *other* laws as well. The same target filter the
			// consolidator uses has to apply here too, or every change aimed at a
			// different law counts as an addressing failure.
			if !targetsLaw(ch.Target, law.Meta, law.Accession()) {
				total.addrForeign++
				continue
			}
			// An insertion names the position the text is to occupy ("indsættes som
			// stk. 12"), which by definition does not exist yet, so it cannot resolve.
			// Counting those as failures would understate every real addressing bug.
			if ch.Kind == EditInsertText || ch.Kind == EditInsertProvision {
				total.addrInsertion++
				continue
			}
			// Three shapes address the document rather than a provision: a global replace
			// spans the whole text, a renumbering is satisfied by counting subsections, and
			// a territorial note changes no text at all. None of them can "resolve" to a
			// section, so counting them as misses would make the addressing rate move with
			// the corpus's taste in wording instead of with addressing bugs.
			if ch.Address.Everywhere || ch.Kind == EditNonOperative ||
				ch.Kind == EditRenumber || ch.Address.Preamble || ch.Address.DocTitle {
				total.addrAddressless++
				continue
			}
			total.addrTotal++
			if Find(secs, ch.Address) != nil {
				total.addrFound++
				continue
			}
			total.addrMissed++
			total.addrMissedByKind[string(ch.Kind)]++
			if len(total.addrMissedSample) < 40 {
				total.addrMissedSample = append(total.addrMissedSample,
					fmt.Sprintf("%s | %s", ch.Address, firstWords(ch.Definition, 6)))
			}
		}
	}
}

// pendingAmender returns the earliest amendment dated after the consolidated act's own
// signing date, i.e. the first change whose text has not been merged into it yet. Only
// that one is used: a later pending amendment may address text an earlier pending one
// rewrote, which would show up as a miss that is really correct behaviour.
func pendingAmender(ar *Archive, law *Document, vs []Version) (*Document, bool) {
	signed, ok := ParseDate(law.Meta.DiesSigni)
	if !ok {
		return nil, false
	}
	var best *Version
	for i, v := range vs {
		if !v.Found || v.Effective.IsZero() || !v.Effective.After(signed) {
			continue
		}
		if best == nil || v.Effective.Before(best.Effective) {
			best = &vs[i]
		}
	}
	if best == nil {
		return nil, false
	}
	doc, _, ok := ar.Open(best.Accession)
	if !ok || doc == nil || !doc.IsAmendment() || doc.IsBill() {
		return nil, false
	}
	return doc, true

}

// timeYears keeps the "as of" date past every date in the archive without hard-coding
// a year the crawl might overtake.
const timeYears = 365 * 24 * time.Hour

// handledUnits names the shapes the library now has a mechanism for. It reads Edit.Kind
// and Address, which is what the engine actually acts on; matching the definition text
// here instead would keep reporting shapes as unhandled after they had become applicable,
// which is how the previous table overstated the residue for the five shapes below.
func handledUnits(e Edit) []string {
	var out []string
	switch e.Kind {
	case EditNonOperative:
		out = append(out, "territorial/validity note (recorded, no text change)")
	case EditReplaceEverywhere:
		out = append(out, "replace everywhere in the act")
	case EditRenumber:
		out = append(out, "renumbering instruction (checked against the text)")
	}
	switch {
	case e.Address.Preamble:
		out = append(out, "preamble address")
	case e.Address.DocTitle:
		out = append(out, "document title address")
	case e.Address.Afsnit != 0:
		out = append(out, "afsnit address, roman numerals included")
	}
	return out
}

// unhandledReason answers the question "could this change be applied to the archived
// text at all?", and the answer comes from what the library does with the edit. Anything
// it has a mechanism for returns "" and drops out of the table, so what is left is the
// residue rather than a list of shapes this file has not been updated to notice.
//
// The buckets are disjoint: the first match wins.
func unhandledReason(e Edit) string {
	low := strings.ToLower(e.Definition)
	if len(handledUnits(e)) > 0 {
		return ""
	}
	// A preamble or title rewrite only works when the kind can write text; a change that
	// names the preamble but cannot be classified stays in the residue.
	if (e.Address.Preamble || e.Address.DocTitle) &&
		(e.Kind == EditReplaceProvision || e.Kind == EditReplaceText) {
		return ""
	}
	if e.Address.Afsnit != 0 && e.Kind != EditUnknown {
		return ""
	}
	switch {
	case strings.Contains(low, "affattes som bilag"):
		return "annex taken from the amending act into the law"
	case strings.Contains(low, "fodnote"):
		// "Fodnoten til lovens titel affattes således" is about a footnote attached to the
		// title, which has no representation in the model. Claiming the title instead
		// would rewrite the wrong text with confidence, so the address deliberately
		// refuses it and the change stays here.
		return "footnote to the document title, which is not an addressable unit"
	case strings.Contains(low, "pkt.") && e.Kind == EditInsertText:
		// Placement is the whole instruction: "indsættes som 2. pkt." says *which
		// sentence* to become. Splicing at the end of the subsection is not that.
		return "insertion at a numbered sentence (pkt.), which the model cannot locate"
	case e.Kind == EditUnknown:
		return "wording we do not classify"
	case e.Address.Raw == "":
		return "change names no provision"
	case !e.Address.Recognised:
		return "address we cannot parse"
	}
	return ""
}

// approxPosition names the changes that do get applied but whose exact placement is a
// guess. Counting them separately keeps them out of the "cannot apply" total without
// pretending they are exact.
func approxPosition(e Edit) string {
	if strings.Contains(strings.ToLower(e.Definition), "pkt.") {
		// A quoted passage locates itself even inside a sentence, so the edit lands in
		// the right words; only a sentence-level *insertion* is genuinely misplaced.
		return "quoted passage inside a sentence (pkt.): the words are right, the sentence boundary is not recorded"
	}
	if e.Kind == EditInsertProvision {
		return "new provision inserted at an address: position within a run of insertions is approximate"
	}
	return ""
}
