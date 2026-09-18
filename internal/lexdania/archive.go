package lexdania

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Entry is one document as recorded in the archive's file index.
type Entry struct {
	Path         string
	Kind         string // the medium directory: lta, ltb, ltc, mt, retsinfo, ft, fob, accn
	Accession    string
	Year         int
	Number       int
	DiesSigni    string
	DocumentType string
	Status       Status
	Title        string
}

// ELIPath is the canonical ELI path for this document, e.g. eli/lta/2025/331.
func (e Entry) ELIPath() string { return fmt.Sprintf("eli/%s/%d/%d", e.Kind, e.Year, e.Number) }

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
	Root    string
	entries []Entry

	byAccn   map[string]int
	byNumber map[int][]int // publication number → entry indexes; date disambiguates
	scanned  time.Time

	// Skipped counts files that could not be indexed, e.g. a document still being
	// written while the archive is scanned. It is reported rather than ignored
	// because a partial index silently under-reports amendment chains.
	Skipped int
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

	a := &Archive{Root: root, byAccn: map[string]int{}, byNumber: map[int][]int{}, scanned: time.Now()}

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
		e, ok := readHead(path)
		if !ok {
			a.Skipped++
			return nil
		}
		a.add(e)
		return nil
	})
	if err != nil && ctx.Err() == nil {
		return nil, err
	}
	return a, nil
}

func (a *Archive) add(e Entry) {
	// The archive can hold the same accession twice: xml/accn/<rank>/<year>/… is
	// byte-identical to the medium-path copy, which is why accessions are the
	// deduplication key in the first place. Keep the medium path, since it is the
	// canonical ELI resource and yields a usable ELIPath.
	if prev, ok := a.byAccn[e.Accession]; ok {
		if a.entries[prev].Kind != "accn" || e.Kind == "accn" {
			return
		}
		a.entries[prev] = e
		return
	}
	a.byAccn[e.Accession] = len(a.entries)
	a.byNumber[e.Number] = append(a.byNumber[e.Number], len(a.entries))
	a.entries = append(a.entries, e)
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

func submatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// Len is the number of distinct documents indexed.
func (a *Archive) Len() int { return len(a.entries) }

// Entries returns the index sorted by kind, year and number.
func (a *Archive) Entries() []Entry {
	out := append([]Entry(nil), a.entries...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Year != out[j].Year {
			return out[i].Year < out[j].Year
		}
		return out[i].Number < out[j].Number
	})
	return out
}

// Find returns the index entry for an accession.
func (a *Archive) Find(accession string) (Entry, bool) {
	i, ok := a.byAccn[accession]
	if !ok {
		return Entry{}, false
	}
	return a.entries[i], true
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

// Resolve finds documents a preamble hint refers to: "bekendtgørelse nr. 1399 af 12.
// december 2019". Number alone is ambiguous across years, so date and year are used
// to narrow the candidates.
func (a *Archive) Resolve(h TargetHint) []Entry {
	var out []Entry
	for _, i := range a.byNumber[h.Number] {
		e := a.entries[i]
		if !h.Date.IsZero() {
			signed, ok := ParseDate(e.DiesSigni)
			if ok && !signed.IsZero() {
				if !signed.Equal(h.Date) {
					continue
				}
			} else if e.Year != h.Date.Year() {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// Versions lists the amendment chain of a stored law, marking which amending
// documents are already present in the archive.
//
// Not-found is expected and not an error: while a crawl is in progress, a chain can
// name documents that have not been fetched yet. Reporting Found=false lets a caller
// tell "no such version" apart from "not archived yet", which is the difference
// between an incomplete answer and a wrong one.
func (a *Archive) Versions(accession string) ([]Version, error) {
	doc, _, ok := a.Open(accession)
	if !ok {
		return nil, fmt.Errorf("lexdania: %s is not in the archive", accession)
	}
	vs := doc.Versions()
	for i := range vs {
		if e, found := a.Find(vs[i].Accession); found {
			vs[i].Found = true
			vs[i].Path = e.Path
		}
	}
	return vs, nil
}

// LawAsOf reconstructs a stored law as it stood at a date.
func (a *Archive) LawAsOf(accession string, asOf time.Time) (*View, error) {
	doc, path, ok := a.Open(accession)
	if !ok {
		return nil, fmt.Errorf("lexdania: %s is not in the archive (%s)", accession, a.Root)
	}
	v := LawAsOf(doc, asOf, a.Open)
	v.Path = path
	return v, nil
}
