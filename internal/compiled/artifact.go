package compiled

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// The artifact is the mirror's own compiled form: internal/lexdoc documents as JSON, one
// independently decodable zstd frame per document, in the Zstandard Seekable Format so the
// result is still a plain zstd stream that standard tools read.
//
// It exists because the serving path re-parses the publisher's XML for every lookup and again for
// every search snippet. The codec bench (codec_bench_test.go, testdata/codec-bench-20260918.txt)
// settled the encoder and the container: JSON because stdlib and CBOR round-trip nil against empty
// faithfully while gob and the JSON v2 experiment do not, and level 3 container compression.
//
// The bench's size numbers were measured on cDoc, a struct assembled for the codec comparison, and
// they said this form would be bigger than the XML: 79.5 MB against 59.9 MB raw over 1,000
// documents. Measured on the model itself (testdata/artifact-20260918.txt, same 1,000 documents),
// that turns out to have been a property of cDoc rather than of the model: the model's JSON is
// 28.5 MB raw and 5.6 MB compressed, where the XML is 59.9 MB raw and 5.7 MB compressed. cDoc
// copied a section's run text out for every section it belonged to, which the model does not do:
// one provision holds one segment slice and its subsections and conditions are spans over it.
//
// What the artifact is argued for remains speed rather than size — 122 microseconds to read and
// decode a document against 691 to parse it, 5.7x on the same sample — but it does not cost size
// either, and the difference between those two claims is a measurement rather than a hope.
const (
	// artifactVersion is the format pin. A file from another build is refused rather than
	// misread: the frame layout is self-describing only up to a point, and a shifted read of a
	// document table produces offsets that look entirely plausible.
	artifactVersion = 1

	// maxFrameBytes is the boundary the container itself enforces: the seek table records each
	// frame's compressed and uncompressed size as a uint32, so a frame of 4 GiB or more cannot be
	// described at all. Checking it where the cast happens is the only place it can be checked —
	// see frame().
	maxFrameBytes = 1 << 32

	// maxDocumentJSON is this build's own refusal, not a format limit. A lookup decodes a document
	// whole, so this number is also the memory one answer can cost, which is why it is not simply
	// the container's limit. Measured over the whole mirror (191,429 documents, 6.9 GiB of model
	// JSON): the largest single document is 42.2 MiB, twenty documents exceed 8 MiB, and 417 more
	// fall between 1 and 8 MiB — so this bound is roughly three times the largest thing the register
	// publishes, and a model that grew past it is a bug in the model rather than a law. Refusing it
	// names the accession instead of the server spending a tenth of a gigabyte on one answer.
	maxDocumentJSON = 1 << 27

	// From the Zstandard Seekable Format, version 0.1.0: the seek table is a skippable frame
	// whose content ends with these, so a reader can find it in the last bytes of a file.
	skippableFrameMagic = 0x184D2A5E
	seekableMagic       = 0x8F92EAB1

	// seekableFooter is Number_Of_Frames, Seek_Table_Descriptor and Seekable_Magic_Number.
	seekableFooter = 4 + 1 + 4
)

// documentSizeBands are the sizes the build counts documents in, with the last band holding
// everything at or above it. The bound above is a claim about the corpus, so the build reports the
// distribution that justifies it rather than a lone maximum: "documents are far smaller than 8 MiB"
// was written down as a fact and turned out to be false for the mirror's own largest documents and
// for the artifact's own document table.
var documentSizeBands = []int{1 << 13, 1 << 16, 1 << 20, 8 << 20, 64 << 20}

// ArtifactHeader is the artifact's first frame: what it holds and which build wrote it.
//
// It does not carry a document count. The count is the length of the document table, which is the
// only place it can be written once known, and a second copy of it in the header would be a claim
// that can disagree with the table. What the header owns is the format pin and the provenance of
// the build.
type ArtifactHeader struct {
	Version     int       `json:"version"`
	Codec       string    `json:"codec"`
	Compression string    `json:"compression"`
	Created     time.Time `json:"created"`
	Source      string    `json:"source"`

	// Build and Modified name the source the document model was compiled from. The payload is the
	// JSON form of this repository's Go types, so a file from another revision decodes into today's
	// struct with yesterday's meaning: see build.go.
	Build    string `json:"build,omitempty"`
	Modified bool   `json:"modified,omitempty"`
}

// DocRef is where one document sits in the artifact.
type DocRef struct {
	Accession string `json:"accession"`
	Offset    uint64 `json:"offset"` // byte offset of the document's own frame
	Length    uint64 `json:"length"` // compressed bytes of that frame
	Raw       uint64 `json:"raw"`    // JSON bytes it decodes to
}

// Writer builds an artifact, document by document.
//
// Streaming rather than a slice of documents, because a whole-corpus build cannot hold the
// documents: 191,393 models is gigabytes, while the frames leave one at a time. The writer keeps
// only the sizes it needs for the seek table and the document table.
type Writer struct {
	w       *countingWriter
	enc     *zstd.Encoder
	header  ArtifactHeader
	sizes   [][2]uint32
	refs    []DocRef
	scratch []byte

	// Size accounting for the build report: what the documents cost to hold decompressed, how that
	// is distributed, and the largest one. See documentSizeBands.
	largest   int
	rawBytes  uint64
	bandCount []int64

	done bool
}

// NewWriter writes the header frame and returns the writer.
func NewWriter(w io.Writer, source string) (*Writer, error) {
	return newWriter(w, source, artifactVersion)
}

// newWriter takes the version explicitly so a test can write an artifact from another build and
// prove the reader refuses it. Callers get NewWriter, which pins it.
func newWriter(w io.Writer, source string, version int) (*Writer, error) {
	// EncodeAll rather than a streaming writer, because the frame boundary is the layout: Flush
	// ends a block and keeps the frame open, so a "frame" per document measured with Flush is
	// really one frame with n blocks, which cannot be sought to or decoded on its own. EncodeAll
	// writes a complete frame per call, which is what the seek table describes.
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	wr := &Writer{
		w:   &countingWriter{w: w},
		enc: enc,
		header: ArtifactHeader{
			Version:     version,
			Codec:       "encoding/json of internal/lexdoc.Document",
			Compression: "zstd level 3, one frame per document",
			Created:     time.Now().UTC().Truncate(time.Second),
			Source:      source,
		},
	}
	if stamp, ok := CurrentBuild(); ok {
		wr.header.Build, wr.header.Modified = stamp.Revision, stamp.Modified
	}
	payload, err := json.Marshal(wr.header)
	if err != nil {
		enc.Close()
		return nil, err
	}
	if _, _, err := wr.frame(payload); err != nil {
		enc.Close()
		return nil, err
	}
	return wr, nil
}

// CompiledFrame is one document prepared for the file: the model's JSON and the zstd
// frame that JSON compresses to.
//
// The split exists because a build's time goes almost entirely into these two
// operations — marshal the model, compress one frame — while the file itself is a
// sequential byte stream. Compiling touches no writer state, so it runs on workers;
// writing keeps the order the seek table describes.
type CompiledFrame struct {
	Accession  string
	JSON       []byte
	Compressed []byte
}

// Compile marshals and compresses one document without writing it anywhere.
//
// Safe for several goroutines against one Writer: zstd's EncodeAll is documented as
// callable concurrently, and nothing here reads or advances the writer's position, its
// tables, or its scratch buffer. The two size guards run here so an oversized document
// is refused before its bytes are handed to the writer.
func (wr *Writer) Compile(accession string, doc *lexdoc.Document) (CompiledFrame, error) {
	if accession == "" {
		return CompiledFrame{}, errors.New("document has no accession: the artifact's table would have a hole in it")
	}
	if doc == nil {
		return CompiledFrame{}, fmt.Errorf("%s has no document: refusing to record it as present with nothing behind it", accession)
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return CompiledFrame{}, fmt.Errorf("%s: %w", accession, err)
	}
	if len(payload) >= maxDocumentJSON {
		return CompiledFrame{}, fmt.Errorf("%s: %d bytes of JSON, over this build's %d byte document bound. The document is served by decoding it whole, so this is the memory one answer would cost; if the mirror really holds a document this large, the bound is the thing to raise, not the file to skip",
			accession, len(payload), maxDocumentJSON)
	}
	if err := frameSizeFits("uncompressed", len(payload)); err != nil {
		return CompiledFrame{}, err
	}
	compressed := wr.enc.EncodeAll(payload, nil)
	if err := frameSizeFits("compressed", len(compressed)); err != nil {
		return CompiledFrame{}, err
	}
	return CompiledFrame{Accession: accession, JSON: payload, Compressed: compressed}, nil
}

// WriteFrame appends one compiled frame at the writer's current position and records it
// in the tables. Frames are taken in the order the file is to hold them.
func (wr *Writer) WriteFrame(f CompiledFrame) error {
	if wr.done {
		return errors.New("artifact writer is already closed")
	}
	if len(f.JSON) == 0 {
		return fmt.Errorf("%s: refusing to write a frame with no payload", f.Accession)
	}
	start := wr.w.n
	if _, err := wr.w.Write(f.Compressed); err != nil {
		return err
	}
	compressed := wr.w.n - start
	wr.sizes = append(wr.sizes, [2]uint32{uint32(compressed), uint32(len(f.JSON))})
	wr.recordSize(len(f.JSON))
	wr.refs = append(wr.refs, DocRef{Accession: f.Accession, Offset: start, Length: compressed, Raw: uint64(len(f.JSON))})
	return nil
}

// Add writes one document as its own frame.
//
// Every document is its own frame because that is what makes a lookup read one document: the
// server seeks to a frame, decodes it, and never touches the rest. It also means a corrupt frame
// costs one document rather than the file.
//
// A build that compiles several documents at once uses Compile and WriteFrame instead; this is
// the same two steps in the order a single-threaded caller wants them.
func (wr *Writer) Add(accession string, doc *lexdoc.Document) error {
	if wr.done {
		return errors.New("artifact writer is already closed")
	}
	f, err := wr.Compile(accession, doc)
	if err != nil {
		return err
	}
	return wr.WriteFrame(f)
}

// recordSize keeps what the build report needs about one document's compiled size.
func (wr *Writer) recordSize(n int) {
	if n > wr.largest {
		wr.largest = n
	}
	wr.rawBytes += uint64(n)
	if wr.bandCount == nil {
		wr.bandCount = make([]int64, len(documentSizeBands)+1)
	}
	for i, band := range documentSizeBands {
		if n < band {
			wr.bandCount[i]++
			return
		}
	}
	wr.bandCount[len(documentSizeBands)]++
}

// SizeDistribution reports the document-size histogram in the same order as the bands it was
// counted against: band sizes, counts per band with the last holding everything at or above the
// last band, the largest single document, and the total decompressed bytes.
func (wr *Writer) SizeDistribution() (bands []int, counts []int64, largest int, total uint64) {
	counts = append(counts, wr.bandCount...)
	return documentSizeBands, counts, wr.largest, wr.rawBytes
}

// Documents is how many documents the writer has taken so far, for progress reporting.
func (wr *Writer) Documents() int { return len(wr.refs) }

// Close writes the document table and the seek table. Nothing may be written to the underlying
// writer afterwards, which is why the seek table has to be written here rather than by the caller.
func (wr *Writer) Close() error {
	if wr.done {
		return nil
	}
	wr.done = true
	if len(wr.refs) == 0 {
		// An empty artifact is how a build with a broken source path would quietly claim the whole
		// mirror. Refusing is the only answer that cannot be mistaken for coverage.
		wr.enc.Close()
		return errors.New("no documents compiled: refusing to finish an empty artifact")
	}
	table, err := json.Marshal(wr.refs)
	if err != nil {
		wr.enc.Close()
		return err
	}
	// The table is bounded by frame()'s own limit rather than a second number of its own: it grows
	// with the mirror — about a hundred bytes of JSON per document, so the current 191,429 documents
	// make roughly 21 MB — and the only boundary that is real for it is the one the seek table
	// imposes on every frame. The first build refused it anyway, against the 8 MiB it had decided a
	// document's size was, which is how a whole-mirror artifact turned out never to have been
	// buildable at all.
	if _, _, err := wr.frame(table); err != nil {
		wr.enc.Close()
		return err
	}
	if err := wr.enc.Close(); err != nil {
		return err
	}
	return writeSeekTable(wr.w, wr.sizes)
}

// frame compresses one payload as one complete zstd frame and records its sizes.
func (wr *Writer) frame(payload []byte) (start, compressed uint64, err error) {
	// The container's real boundary is checked here, at the cast into the seek table's uint32
	// sizes, rather than as a smaller figure assumed elsewhere about what a document's size happens
	// to be. Refusing before encoding keeps a frame the table could not describe out of the file.
	if err := frameSizeFits("uncompressed", len(payload)); err != nil {
		return 0, 0, err
	}
	start = wr.w.n
	wr.scratch = wr.enc.EncodeAll(payload, wr.scratch[:0])
	if _, err = wr.w.Write(wr.scratch); err != nil {
		return 0, 0, err
	}
	compressed = wr.w.n - start
	if err := frameSizeFits("compressed", int(compressed)); err != nil {
		return 0, 0, err
	}
	wr.sizes = append(wr.sizes, [2]uint32{uint32(compressed), uint32(len(payload))})
	return start, compressed, nil
}

// frameSizeFits reports whether a frame of n bytes can be described by the seek table at all. Its
// two sizes per frame are uint32, so 4 GiB is the limit and reaching it means the artifact is not
// the right container for whatever produced this.
func frameSizeFits(what string, n int) error {
	if uint64(n) >= maxFrameBytes {
		return fmt.Errorf("%s frame of %d bytes cannot be described by the seek table, whose sizes are uint32: %d is the largest representable",
			what, n, maxFrameBytes-1)
	}
	return nil
}

// Header reports what the writer put in the header frame.
func (wr *Writer) Header() ArtifactHeader { return wr.header }

// Refs reports where each document landed, in the order added.
func (wr *Writer) Refs() []DocRef { return wr.refs }

// ArtifactDoc is one document going into the artifact.
type ArtifactDoc struct {
	Accession string
	Document  *lexdoc.Document
}

// Build writes docs as an artifact to w and reports the header and table it wrote, so a caller can
// record where documents are without reading the file back. It exists for tests and small builds;
// a whole-corpus build streams through Writer, because the documents do not fit in memory.
func Build(w io.Writer, source string, docs []ArtifactDoc) (*ArtifactHeader, []DocRef, error) {
	wr, err := newWriter(w, source, artifactVersion)
	if err != nil {
		return nil, nil, err
	}
	for _, d := range docs {
		if err := wr.Add(d.Accession, d.Document); err != nil {
			return nil, nil, err
		}
	}
	if err := wr.Close(); err != nil {
		return nil, nil, err
	}
	return &wr.header, wr.refs, nil
}

// countingWriter is how the writer learns where each frame landed.
type countingWriter struct {
	w io.Writer
	n uint64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += uint64(n)
	return n, err
}

// writeSeekTable writes the seek table's skippable frame in the Zstandard Seekable Format,
// version 0.1.0: skippable magic, the content length, one entry per frame with its compressed and
// uncompressed size, then the footer whose magic number is the last thing in the file. Entries are
// sizes rather than offsets, so a reader cumulates them.
//
// The checksum flag is left clear. The frames are independent and every read is verified against
// the document table's own length, so the table would carry a second, weaker copy of a guarantee
// the container already makes.
func writeSeekTable(w io.Writer, sizes [][2]uint32) error {
	content := make([]byte, 0, len(sizes)*8+seekableFooter)
	var scratch [8]byte
	for _, s := range sizes {
		binary.LittleEndian.PutUint32(scratch[:4], s[0])
		binary.LittleEndian.PutUint32(scratch[4:8], s[1])
		content = append(content, scratch[:4]...)
		content = append(content, scratch[4:8]...)
	}
	binary.LittleEndian.PutUint32(scratch[:4], uint32(len(sizes)))
	content = append(content, scratch[:4]...)
	content = append(content, 0) // Seek_Table_Descriptor: no checksums, no reserved bits set
	binary.LittleEndian.PutUint32(scratch[:4], seekableMagic)
	content = append(content, scratch[:4]...)

	var head [8]byte
	binary.LittleEndian.PutUint32(head[:4], skippableFrameMagic)
	binary.LittleEndian.PutUint32(head[4:8], uint32(len(content)))
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	_, err := w.Write(content)
	return err
}
