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

	// frameSplitSize is the seekable format's own frame boundary. Documents are far smaller, so
	// hitting it means something has gone wrong with one document rather than with the layout,
	// and a frame over the boundary would make the sizes in the seek table wrong.
	frameSplitSize = 8 << 20

	// From the Zstandard Seekable Format, version 0.1.0: the seek table is a skippable frame
	// whose content ends with these, so a reader can find it in the last bytes of a file.
	skippableFrameMagic = 0x184D2A5E
	seekableMagic       = 0x8F92EAB1

	// seekableFooter is Number_Of_Frames, Seek_Table_Descriptor and Seekable_Magic_Number.
	seekableFooter = 4 + 1 + 4
)

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
	done    bool
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

// Add writes one document as its own frame.
//
// Every document is its own frame because that is what makes a lookup read one document: the
// server seeks to a frame, decodes it, and never touches the rest. It also means a corrupt frame
// costs one document rather than the file.
func (wr *Writer) Add(accession string, doc *lexdoc.Document) error {
	if wr.done {
		return errors.New("artifact writer is already closed")
	}
	if accession == "" {
		return errors.New("document has no accession: the artifact's table would have a hole in it")
	}
	if doc == nil {
		return fmt.Errorf("%s has no document: refusing to record it as present with nothing behind it", accession)
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("%s: %w", accession, err)
	}
	if len(payload) >= frameSplitSize {
		return fmt.Errorf("%s: %d bytes of JSON, over the seekable format's %d byte frame boundary",
			accession, len(payload), frameSplitSize)
	}
	start, compressed, err := wr.frame(payload)
	if err != nil {
		return err
	}
	wr.refs = append(wr.refs, DocRef{Accession: accession, Offset: start, Length: compressed, Raw: uint64(len(payload))})
	return nil
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
	if len(table) >= frameSplitSize {
		wr.enc.Close()
		return fmt.Errorf("document table is %d bytes, over the frame boundary", len(table))
	}
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
	start = wr.w.n
	wr.scratch = wr.enc.EncodeAll(payload, wr.scratch[:0])
	if _, err = wr.w.Write(wr.scratch); err != nil {
		return 0, 0, err
	}
	compressed = wr.w.n - start
	wr.sizes = append(wr.sizes, [2]uint32{uint32(compressed), uint32(len(payload))})
	return start, compressed, nil
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
