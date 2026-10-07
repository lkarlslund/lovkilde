package lexdania

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lkarlslund/retsinformation/internal/chain"
	"github.com/lkarlslund/retsinformation/internal/parallel"
)

// Archive is a read-only index over a directory of stored documents.
//
// It indexes by reading the Meta block of each file rather than trusting the path,
// because the path cannot be derived from the accession: the ELI medium is not the
// rank. B-ranked bekendtgørelser live under lta/ because their AnnouncedIn medium is
// Lovtidende A, while Rank is Retsinformation's own rangering. Guessing a path from
// an accession would therefore be wrong often enough to matter, and wrong silently.
//
// It also keeps the archive's own rule: nothing here is mutable state. The index is
// derived from files and can be rebuilt at any time.
type Archive struct {
	Root string
	*chain.Catalog
	scanned time.Time

	// Skipped counts files that could not be indexed, e.g. a document still being
	// written while the archive is scanned. It is reported rather than ignored
	// because a partial index silently under-reports amendment chains.
	Skipped int

	chainsOnce sync.Once
	chains     *Chains
}

// chain is the archive's consolidation, reading documents from its files.
func (a *Archive) chain() *Chains {
	a.chainsOnce.Do(func() {
		a.chains = chain.NewChainsFor(a.Catalog,
			func(accession string) (*Law, bool) {
				doc, path, ok := a.Open(accession)
				if !ok {
					return chain.NewLaw(chain.Meta{}, path, nil), false
				}
				return doc.Law(path), true
			},
			func(accession string) (*ActRecord, bool) {
				doc, path, ok := a.Open(accession)
				if !ok {
					return &ActRecord{Path: path}, false
				}
				return readAct(doc, path), true
			},
			func(accession string) []WaitingChange {
				doc, _, ok := a.Open(accession)
				if !ok {
					return nil
				}
				return WaitingNotes(doc.textLines())
			})
	})
	return a.chains
}

var (
	reAccession = regexp.MustCompile(`<AccessionNumber>([^<]*)`)
	reYear      = regexp.MustCompile(`<Year>([^<]*)`)
	reNumber    = regexp.MustCompile(`<Number>([^<]*)`)
	reDiesSigni = regexp.MustCompile(`<DiesSigni>([^<]*)`)
	reDocType   = regexp.MustCompile(`<DocumentType>([^<]*)`)
	reStatus    = regexp.MustCompile(`<Status>([^<]*)`)
	reTitle     = regexp.MustCompile(`(?s)<DocumentTitle>([^<]*)`)
)

// headBytes is how much of each file is read while indexing. Identity fields live at
// the start of Meta, and reading whole files here would turn indexing a large
// archive into a multi-gigabyte job for information contained in the first
// kilobytes. Titles can be long, so a title cut off at this limit is simply shorter.
const headBytes = 6 << 10

// Scan builds the index. The archive may be growing while this runs — the crawl
// writes files concurrently — so a file that vanishes or fails mid-read is counted
// in Skipped and does not abort the scan.
func Scan(ctx context.Context, root string) (*Archive, error) {
	// The root is checked before walking. Inside the walk a missing file is normal —
	// the crawl writes while this runs, and a vanished entry is counted in Skipped —
	// but a root that is not a directory means the walk would visit nothing and
	// return an empty index that looks like a successful scan. An empty archive is
	// what every downstream count then reports as "no documents", which is the worst
	// possible way to learn the path was wrong.
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		if err != nil {
			return nil, fmt.Errorf("mirror root %s: %w", root, err)
		}
		return nil, fmt.Errorf("mirror root %s is not a directory", root)
	}

	a := &Archive{Root: root, Catalog: chain.NewCatalog(), scanned: time.Now()}

	// The walk collects paths; reading the heads happens afterwards in parallel.
	// readHead is a 6 KB read plus a handful of regexp matches, the mirror holds
	// 191,429 such files, and done one at a time the scan spends its time waiting on
	// the kernel rather than working. Splitting the path list into contiguous chunks
	// and writing each result back to its own slot keeps the order the walk produced,
	// which matters: add() below runs in walk order, so byAccn, byNumber and Entries()
	// come out identical to the serial scan's, byte for byte, at any worker count.
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			a.Skipped++
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".xml") {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil && ctx.Err() == nil {
		return nil, err
	}

	entries := make([]Entry, len(paths))
	got := make([]bool, len(paths))
	parallel.For(ctx, len(paths), func(i int) {
		entries[i], got[i] = readHead(paths[i])
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	for i := range paths {
		if !got[i] {
			a.Skipped++
			continue
		}
		a.Add(entries[i])
	}
	return a, nil
}

// readHead extracts identity fields from the start of a file.
func readHead(path string) (Entry, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Entry{}, false
	}
	defer f.Close()

	buf := make([]byte, headBytes)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return Entry{}, false
	}
	head := string(buf[:n])

	acc := submatch(reAccession, head)
	if acc == "" {
		return Entry{}, false
	}
	rel, _ := filepath.Rel(filepath.Dir(filepath.Dir(filepath.Dir(path))), path)
	kind := strings.Split(filepath.ToSlash(rel), "/")[0]

	return Entry{
		Path:         path,
		Kind:         kind,
		Accession:    acc,
		Year:         atoi(submatch(reYear, head)),
		Number:       atoi(submatch(reNumber, head)),
		DiesSigni:    submatch(reDiesSigni, head),
		DocumentType: submatch(reDocType, head),
		Status:       Status(submatch(reStatus, head)),
		Title:        Squeeze(submatch(reTitle, head)),
	}, true
}

// submatch copies what it extracts: a substring of the file's head would keep the whole head
// alive for as long as the entry lives, which is the life of the archive. Holding 191,589 heads
// for a few short fields each was 650 MB of an artifact build's live heap, and the same in every
// server.
func submatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.Clone(strings.TrimSpace(m[1]))
}

// Open parses a document by accession, attaching text from a sidecar payload when the
// stored XML has none. It satisfies Opener, so the consolidation engine can be driven
// from the archive or from a fixture map in tests.
func (a *Archive) Open(accession string) (*Document, string, bool) {
	e, ok := a.Find(accession)
	if !ok {
		return nil, "", false
	}
	doc, err := LoadWithBody(e.Path)
	if err != nil {
		return nil, e.Path, false
	}
	return doc, e.Path, true
}

// Versions lists the amendment chain of a stored law, marking which amending
// documents are already present in the archive.
//
// Not-found is expected and not an error: while a crawl is in progress, a chain can
// name documents that have not been fetched yet. Reporting Found=false lets a caller
// tell "no such version" apart from "not archived yet", which is the difference
// between an incomplete answer and a wrong one.
func (a *Archive) Versions(accession string) ([]Version, error) { return a.chain().Versions(accession) }

// LawAsOf reconstructs a stored law as it stood at a date.
func (a *Archive) LawAsOf(accession string, asOf time.Time) (*View, error) {
	return a.chain().LawAsOf(accession, asOf)
}

// LawAdoptedAsOf reconstructs a stored law as amended by every act signed by a date, the parts
// not yet in force included; see LawAdoptedAsOf.
func (a *Archive) LawAdoptedAsOf(accession string, asOf time.Time) (*View, error) {
	return a.chain().LawAdoptedAsOf(accession, asOf)
}

// LawBefore reconstructs a stored law as the amending act met it; see Chains.LawBefore.
func (a *Archive) LawBefore(accession, act string) (*View, error) {
	return a.chain().LawBefore(accession, act)
}
