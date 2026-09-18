package compiled

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
	"github.com/lkarlslund/retsinformation/internal/lexdoc"
)

// Artifact is an open artifact, read-only.
//
// Reads use ReadAt at the offsets the document table recorded, so a lookup costs one seek, one
// frame, and one JSON decode: no other document is touched, and nothing is held open per read.
// os.File.ReadAt is a positional read, so concurrent lookups do not contend over a file offset.
type Artifact struct {
	path   string
	file   *os.File
	Header ArtifactHeader
	refs   map[string]DocRef
	order  []string

	decoder *zstd.Decoder
}

// OpenArtifact reads the artifact's header and document table and refuses anything this build
// cannot read.
func OpenArtifact(path string) (*Artifact, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	size := st.Size()
	if size < seekableFooter {
		f.Close()
		return nil, fmt.Errorf("%s: %d bytes, too small to be an artifact", path, size)
	}
	a := &Artifact{path: path, file: f, refs: map[string]DocRef{}}
	tail := make([]byte, seekableFooter)
	if _, err := f.ReadAt(tail, size-seekableFooter); err != nil {
		a.Close()
		return nil, err
	}
	if got := binary.LittleEndian.Uint32(tail[5:9]); got != seekableMagic {
		a.Close()
		return nil, fmt.Errorf("%s: no seek table in the last bytes (magic %#x): not a retsin artifact", path, got)
	}
	frames := int(binary.LittleEndian.Uint32(tail[:4]))
	descriptor := tail[4]
	if descriptor&0x80 != 0 {
		a.Close()
		return nil, fmt.Errorf("%s: seek table carries frame checksums, which this build neither writes nor verifies", path)
	}
	if frames < 3 {
		a.Close()
		return nil, fmt.Errorf("%s: seek table names %d frames; an artifact holds a header, a document and a table at least", path, frames)
	}
	// The skippable frame holding the seek table: magic, content length, entries, footer. Reading
	// its header confirms the file is the length the seek table claims, rather than a truncated
	// write that cumulates into offsets pointing past the end.
	content := frames*8 + seekableFooter
	head := make([]byte, 8)
	if _, err := f.ReadAt(head, size-int64(content)-8); err != nil {
		a.Close()
		return nil, fmt.Errorf("%s: seek table header is not where its size says: %w", path, err)
	}
	if got := binary.LittleEndian.Uint32(head[:4]); got != skippableFrameMagic {
		a.Close()
		return nil, fmt.Errorf("%s: expected a skippable frame holding the seek table, found %#x: not a retsin artifact", path, got)
	}
	if got := int(binary.LittleEndian.Uint32(head[4:8])); got != content {
		a.Close()
		return nil, fmt.Errorf("%s: seek table frame is %d bytes, seek table math says %d: the file and its table disagree", path, got, content)
	}
	entries := make([]byte, frames*8)
	if _, err := f.ReadAt(entries, size-int64(content)); err != nil {
		a.Close()
		return nil, err
	}
	offsets := make([]uint64, frames+1)
	for i := 0; i < frames; i++ {
		offsets[i+1] = offsets[i] + uint64(binary.LittleEndian.Uint32(entries[i*8:]))
	}
	if int64(offsets[frames]) != size-int64(content)-8 {
		a.Close()
		return nil, fmt.Errorf("%s: frame sizes end at %d, the file says content ends at %d: the artifact is truncated or the table is wrong",
			path, offsets[frames], size-int64(content)-8)
	}

	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		a.Close()
		return nil, err
	}
	a.decoder = decoder

	if err := a.readJSONFrame(offsets[0], offsets[1], &a.Header); err != nil {
		a.Close()
		return nil, err
	}
	if a.Header.Version != artifactVersion {
		// Refusing is the only honest option: the frames themselves are plain zstd, so a
		// different layout would still decode, and a document table read under the wrong
		// assumption would return offsets that look entirely plausible.
		a.Close()
		return nil, fmt.Errorf("%s: artifact format version %d, this build reads and writes %d — rebuild the artifact from the mirror",
			path, a.Header.Version, artifactVersion)
	}
	// The document table is the last content frame, the header the first, and the documents are
	// whatever is between them: the frame count from the seek table locates all three, so there is
	// no second index that can disagree about where the table is.
	if frames < 3 {
		a.Close()
		return nil, fmt.Errorf("%s: %d content frames; a header, at least one document and a table make three", path, frames)
	}
	var refs []DocRef
	if err := a.readJSONFrame(offsets[frames-1], offsets[frames], &refs); err != nil {
		a.Close()
		return nil, err
	}
	if len(refs) != frames-2 {
		a.Close()
		return nil, fmt.Errorf("%s: %d content frames but the table lists %d documents: a document is missing from the table, or the table describes frames that are not documents",
			path, frames, len(refs))
	}
	// Cross-check the two indexes against each other. The seek table says where the frames are, the
	// document table says where the documents are, and documents are the frames between the header
	// and the table: document i must be exactly frame i+1. A build that wrote one index without the
	// other is refused here rather than served as half a mirror.
	for i, r := range refs {
		if want, end := offsets[i+1], offsets[i+2]; r.Offset != want || r.Offset+r.Length != end {
			a.Close()
			return nil, fmt.Errorf("%s: %s recorded as bytes %d..%d, the frame table says frame %d is %d..%d",
				path, r.Accession, r.Offset, r.Offset+r.Length, i+1, want, end)
		}
		if r.Length == 0 || r.Offset+r.Length > offsets[frames] {
			a.Close()
			return nil, fmt.Errorf("%s: %s claims bytes %d..%d, past the end of the content frames at %d",
				path, r.Accession, r.Offset, r.Offset+r.Length, offsets[frames])
		}
		a.refs[r.Accession] = r
		a.order = append(a.order, r.Accession)
	}
	return a, nil
}

// Path is the file this artifact was opened from, for error messages and the provenance line.
func (a *Artifact) Path() string { return a.path }

// Documents lists the accessions the artifact holds, in build order.
func (a *Artifact) Documents() []string { return a.order }

// Ref reports where a document is without reading it.
func (a *Artifact) Ref(accession string) (DocRef, bool) {
	r, ok := a.refs[accession]
	return r, ok
}

// Document reads one document back.
//
// A document the artifact does not hold is an error, not a nil: the artifact's coverage claim is a
// new way to be quietly incomplete, and an answer that came back empty would look like a law that
// does not exist rather than a dataset that does not hold it.
func (a *Artifact) Document(accession string) (*lexdoc.Document, error) {
	r, ok := a.refs[accession]
	if !ok {
		return nil, fmt.Errorf("%s: %s is not in this artifact (%d documents, built %s): rebuild the artifact from the mirror",
			a.path, accession, len(a.order), a.Header.Created.Format("2006-01-02"))
	}
	var doc lexdoc.Document
	if err := a.readJSONFrame(r.Offset, r.Offset+r.Length, &doc); err != nil {
		return nil, fmt.Errorf("%s: %s: %w", a.path, accession, err)
	}
	return &doc, nil
}

// readJSONFrame decodes exactly the bytes between two offsets and unmarshals them.
func (a *Artifact) readJSONFrame(from, to uint64, out any) error {
	if to <= from {
		return fmt.Errorf("frame from %d to %d is empty", from, to)
	}
	raw := make([]byte, to-from)
	if _, err := a.file.ReadAt(raw, int64(from)); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	plain, err := a.decoder.DecodeAll(raw, nil)
	if err != nil {
		return fmt.Errorf("frame at %d: %w", from, err)
	}
	if err := json.Unmarshal(plain, out); err != nil {
		return fmt.Errorf("frame at %d: %w", from, err)
	}
	return nil
}

func (a *Artifact) Close() error {
	if a.decoder != nil {
		a.decoder.Close()
	}
	return a.file.Close()
}
