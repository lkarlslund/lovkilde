package search

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lkarlslund/lovkilde/internal/lexdoc"
	"github.com/lkarlslund/lovkilde/internal/parallel"
)

// CutVersion names how documents are cut into units and counted (DocUnits, Units, Tokenize).
// It is part of what an index records as its source, so a server rebuilds an index cut by older
// code even when the artifact is the same: bump it whenever a change to those would give a
// different index from the same artifact.
const CutVersion = 1

// SourceFor is the source an index cut from an artifact records: the artifact's identity and the
// cut. A server compares it with its own artifact and this build's cut.
func SourceFor(artifactIdentity string) string {
	return artifactIdentity + " cut " + strconv.Itoa(CutVersion)
}

// DocUnits is everything one document contributes to the index: a unit for its title, then
// the units its model holds (see modelunits.go).
//
// A title unit exists because searching for a law by name — "miljøbeskyttelsesloven",
// "ejendomsvurderingsafgiftsloven" — is the single most common legal query, and without it
// the index can only find a statute whose title it never indexed.
func DocUnits(id int, meta DocMeta, m *lexdoc.Document) []Unit {
	var units []Unit
	if strings.TrimSpace(meta.Title) != "" {
		units = append(units, Unit{Doc: id, Addr: "titel", Text: meta.Title})
	}
	return append(units, Units(id, meta, m)...)
}

// MetaOf is what the index records about a document, read from its model alone: the shelf,
// sequence, type, status and title the register gave it, and where its text came from. It is
// what lets an index be built from the compiled artifact without the mirror it came from.
func MetaOf(m *lexdoc.Document) DocMeta {
	id := m.Identity
	return DocMeta{
		Accession: id.Accession,
		ELI:       id.ELIPath(),
		Kind:      id.Medium,
		Year:      id.PublishedYear,
		Number:    id.PublishedNumber,
		// The register's own whitespace, collapsed: titles arrive with doubled spaces
		// ("Inspektion af  Statsfængslet"), which is noise in a listing and made a title
		// look different from the same title printed in the document.
		Title:    strings.Join(strings.Fields(id.Title), " "),
		Source:   string(m.Provenance.Source),
		Path:     m.Provenance.Path,
		Status:   string(id.Status),
		TypeCode: id.Type,
	}
}

// streamBatch is how many documents are read, cut and counted before their postings are
// added. Their text is dropped with the batch, so this, not the corpus, bounds how much
// provision text is alive at once.
const streamBatch = 1024

// BuildStream indexes n documents that load returns one at a time, from workers, and keeps
// none of their text: each batch is read, cut into units and counted, its postings added, and
// the text let go. Document and unit ids follow the order of i, whatever the worker count, so
// the file is the same at any parallelism. A document load reports as absent takes no id.
//
// Build, which takes every unit's text up front, held the whole corpus' provision text — 5.4
// of the 10 GB a full build had live at its peak — until the last unit was counted.
func BuildStream(ctx context.Context, n int, load func(i int) (DocMeta, []Unit, bool), opts Options) (*Index, error) {
	b := newBuilder(opts)
	type unit struct {
		addr  string
		runes int
		text  string
	}
	type doc struct {
		meta  DocMeta
		units []unit
		ok    bool
	}
	var totalLen uint64
	for lo := 0; lo < n; lo += streamBatch {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hi := min(lo+streamBatch, n)
		got := make([]doc, hi-lo)
		parallel.For(ctx, hi-lo, func(k int) {
			meta, units, ok := load(lo + k)
			if !ok {
				return
			}
			d := &got[k]
			d.meta, d.ok = meta, true
			d.units = make([]unit, len(units))
			for j, u := range units {
				d.units[j] = unit{addr: u.Addr, runes: utf8.RuneCountInString(u.Text), text: u.Text}
			}
		})
		// Ids in document order, here and not on the workers.
		base := make([]int, len(got))
		for k := range got {
			d := &got[k]
			if !d.ok {
				continue
			}
			docID := len(b.docs)
			b.docs = append(b.docs, d.meta)
			base[k] = len(b.units)
			for _, u := range d.units {
				b.units = append(b.units, UnitMeta{Doc: docID, Addr: u.addr, Len: u.runes})
				totalLen += uint64(u.runes)
			}
		}
		parallel.For(ctx, len(got), func(k int) {
			d := &got[k]
			for j, u := range d.units {
				b.addUnit(uint32(base[k]+j), u.text, opts.Folded)
			}
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(b.units) == 0 {
		return nil, errors.New("no units to index: refusing to build an empty index")
	}
	idx := &Index{Docs: b.docs, Units: b.units, Flags: opts.Flags(), B: bm25B, Built: time.Now().Unix(), avgUnitLen: float64(totalLen) / float64(len(b.units))}
	idx.collect(b, opts)
	return idx, nil
}
