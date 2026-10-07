package search

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// Save writes the index as one immutable file.
//
// The caller writes to a temp name and renames, so a reader never sees a half-built
// index: the archive's own rule is that derived data is either complete or absent.
//
// Layout is three tables behind a header — documents, units, then the sorted term
// dictionary with its postings inline after each term. Sorted terms mean a prefix
// query is one binary search plus a linear run, which is what makes Danish compound
// matching free at query time.
func (idx *Index) Save(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	// Written through a buffer straight to the file: assembling the whole file in memory
	// first cost its size again, plus the copies a growing buffer leaves behind.
	w := bufio.NewWriterSize(f, 1<<20)
	if err := idx.write(w); err != nil {
		f.Close()
		return err
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (idx *Index) write(buf *bufio.Writer) error {
	buf.WriteString(magic)
	buf.WriteByte(formatVersion) // bump whenever the header or table layout changes
	var hdr [8]byte
	binary.LittleEndian.PutUint64(hdr[:], idx.Flags)
	buf.Write(hdr[:])
	binary.LittleEndian.PutUint64(hdr[:], uint64(math.Float64bits(idx.avgUnitLen)))
	buf.Write(hdr[:])
	binary.LittleEndian.PutUint64(hdr[:], uint64(math.Float64bits(idx.B)))
	buf.Write(hdr[:])
	binary.LittleEndian.PutUint64(hdr[:], uint64(idx.Built))
	buf.Write(hdr[:])
	putString(buf, idx.Source)
	putUvarint(buf, uint64(len(idx.Docs)))
	putUvarint(buf, uint64(len(idx.Units)))
	putUvarint(buf, uint64(len(idx.Terms)))

	for _, d := range idx.Docs {
		putString(buf, d.Accession)
		putString(buf, d.ELI)
		putString(buf, d.Kind)
		putString(buf, d.Title)
		putString(buf, d.Source)
		putString(buf, d.Path)
		putString(buf, d.Status)
		putString(buf, d.TypeCode)
		putUvarint(buf, uint64(d.Year))
		putUvarint(buf, uint64(d.Number))
	}

	for _, u := range idx.Units {
		putUvarint(buf, uint64(u.Doc))
		putUvarint(buf, uint64(u.Len))
		putString(buf, u.Addr)
	}

	var prev uint32
	for i, term := range idx.Terms {
		putString(buf, term)
		buf.WriteByte(idx.Stream[i])
		postings := idx.Postings[i]
		putUvarint(buf, uint64(len(postings)))
		prev = 0
		for _, p := range postings {
			putUvarint(buf, uint64(p.Unit-prev))
			buf.WriteByte(p.Tf)
			prev = p.Unit
		}
	}
	return nil
}

// Load reads an index file back.
//
// Terms are copied out of the buffer rather than aliased to it, which costs roughly
// the size of the dictionary in extra RAM and buys a buffer that can be released.
// Whether that is the right trade at full-corpus size is a measured question, not an
// assumption; the number is in the measurement report.
func Load(path string) (*Index, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r := &reader{buf: raw}
	if got := string(r.bytes(len(magic))); got != magic {
		return nil, fmt.Errorf("%s: not a retsin search index (magic %q)", path, got)
	}
	if v := r.byte(); v != formatVersion {
		// Refusing is the only honest option. An older file is not a different layout
		// so much as the same layout shifted by eight bytes, and a shifted read of a
		// self-describing table can produce counts that look entirely plausible.
		return nil, fmt.Errorf("%s: index format version %d, this build writes %d — rebuild with `retsin index build`", path, v, formatVersion)
	}
	idx := &Index{}
	idx.Flags = r.uint64()
	idx.avgUnitLen = math.Float64frombits(r.uint64())
	idx.B = math.Float64frombits(r.uint64())
	idx.Built = int64(r.uint64())
	idx.Source = r.str()
	// B is read as written, including a literal zero: zero is this corpus's measured
	// default, so "no length normalisation" is a real setting rather than an absent one.
	// Substituting a default here would silently re-rank an index built with the
	// opposite choice.

	ndocs := int(r.uvarint())
	nunits := int(r.uvarint())
	nterms := int(r.uvarint())
	if err := r.err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	idx.Docs = make([]DocMeta, ndocs)
	for i := range idx.Docs {
		idx.Docs[i].Accession = r.str()
		idx.Docs[i].ELI = r.str()
		idx.Docs[i].Kind = r.str()
		idx.Docs[i].Title = r.str()
		idx.Docs[i].Source = r.str()
		idx.Docs[i].Path = r.str()
		idx.Docs[i].Status = r.str()
		idx.Docs[i].TypeCode = r.str()
		idx.Docs[i].Year = int(r.uvarint())
		idx.Docs[i].Number = int(r.uvarint())
	}
	idx.Units = make([]UnitMeta, nunits)
	for i := range idx.Units {
		idx.Units[i].Doc = int(r.uvarint())
		idx.Units[i].Len = int(r.uvarint())
		idx.Units[i].Addr = r.str()
	}
	idx.Terms = make([]string, nterms)
	idx.Stream = make([]byte, nterms)
	idx.Postings = make([][]Posting, nterms)
	for i := range idx.Terms {
		idx.Terms[i] = r.str()
		idx.Stream[i] = r.byte()
		n := int(r.uvarint())
		postings := make([]Posting, n)
		var prev uint32
		for j := range postings {
			prev += uint32(r.uvarint())
			postings[j] = Posting{Unit: prev, Tf: r.byte()}
		}
		idx.Postings[i] = postings
	}
	if err := r.err(); err != nil {
		return nil, fmt.Errorf("%s: truncated index: %w", path, err)
	}
	return idx, nil
}

// Flags records which token streams the file contains.
func (idx *Index) HasFolded() bool   { return idx.Flags&1 != 0 }
func (idx *Index) HasReversed() bool { return idx.Flags&2 != 0 }

func putUvarint(buf *bufio.Writer, v uint64) {
	var scratch [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(scratch[:], v)
	buf.Write(scratch[:n])
}

func putString(buf *bufio.Writer, s string) {
	putUvarint(buf, uint64(len(s)))
	buf.WriteString(s)
}

// reader is a cursor over the file. Errors accumulate and are reported once, because
// checking after every field would bury the format in conditionals.
type reader struct {
	buf  []byte
	at   int
	bad  bool
	name string
}

func (r *reader) err() error {
	if r.bad {
		return fmt.Errorf("ran past the end of the index at offset %d", r.at)
	}
	return nil
}

func (r *reader) bytes(n int) []byte {
	if n < 0 || r.at+n > len(r.buf) {
		r.bad = true
		return r.buf[r.at:]
	}
	out := r.buf[r.at : r.at+n]
	r.at += n
	return out
}

func (r *reader) byte() byte {
	if r.at >= len(r.buf) {
		r.bad = true
		return 0
	}
	v := r.buf[r.at]
	r.at++
	return v
}

func (r *reader) uvarint() uint64 {
	v, n := binary.Uvarint(r.buf[r.at:])
	if n <= 0 {
		r.bad = true
		return 0
	}
	r.at += n
	return v
}

func (r *reader) uint64() uint64 {
	if r.at+8 > len(r.buf) {
		r.bad = true
		return 0
	}
	v := binary.LittleEndian.Uint64(r.buf[r.at:])
	r.at += 8
	return v
}

func (r *reader) str() string {
	n := int(r.uvarint())
	return string(r.bytes(n))
}
