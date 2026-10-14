package compiled

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/lkarlslund/lovkilde/internal/lexdoc"
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

	// decoder reads the plain frames (header, dictionary, table); docDecoder the documents,
	// against the artifact's own dictionary.
	decoder    *zstd.Decoder
	docDecoder *zstd.Decoder

	// catalogue is where the catalogue frame sits: what the documents are to one another, read
	// by the library a server answers from (see lexdania.CatalogueRecord).
	catalogue [2]uint64
}

// Catalogue is the catalogue frame's payload: JSON the library layer defines, kept opaque here.
func (a *Artifact) Catalogue() ([]byte, error) {
	return a.decodeWith(a.docDecoder, a.catalogue[0], a.catalogue[1])
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
		return nil, fmt.Errorf("%s: no seek table in the last bytes (magic %#x): not a lovkilde artifact", path, got)
	}
	frames := int(binary.LittleEndian.Uint32(tail[:4]))
	descriptor := tail[4]
	if descriptor&0x80 != 0 {
		a.Close()
		return nil, fmt.Errorf("%s: seek table carries frame checksums, which this build neither writes nor verifies", path)
	}
	if frames < 4 {
		a.Close()
		return nil, fmt.Errorf("%s: seek table names %d frames; an artifact holds a header, a dictionary, a catalogue and a table at least", path, frames)
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
		return nil, fmt.Errorf("%s: expected a skippable frame holding the seek table, found %#x: not a lovkilde artifact", path, got)
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

	// The header and the dictionary are plain frames: a reader has to open them before it has
	// the dictionary the documents need.
	plain, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		a.Close()
		return nil, err
	}
	a.decoder = plain

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
	// The dictionary is the second frame, and it must be the one the header names: documents
	// compressed against another dictionary decode to garbage or not at all, and which of the two
	// is luck.
	dict, err := a.frameBytes(offsets[1], offsets[2])
	if err != nil {
		a.Close()
		return nil, fmt.Errorf("%s: dictionary frame: %w", path, err)
	}
	if got, err := describeDictionary(dict); err != nil || got != a.Header.Dictionary {
		a.Close()
		return nil, fmt.Errorf("%s: the dictionary frame is not the dictionary the header names (%+v, header says %+v, %v)", path, got, a.Header.Dictionary, err)
	}
	withDict, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderDicts(dict))
	if err != nil {
		a.Close()
		return nil, fmt.Errorf("%s: dictionary: %w", path, err)
	}
	a.docDecoder = withDict

	// The document table is the last content frame, the header and dictionary the first two, and
	// the documents are whatever is between them: the frame count from the seek table locates them
	// all, so there is no second index that can disagree about where the table is.
	var refs []DocRef
	if err := a.readJSONFrame(offsets[frames-1], offsets[frames], &refs); err != nil {
		a.Close()
		return nil, err
	}
	// The catalogue is the frame before the table: the documents are the frames between the
	// dictionary and it.
	a.catalogue = [2]uint64{offsets[frames-2], offsets[frames-1]}
	if len(refs) != frames-4 {
		a.Close()
		return nil, fmt.Errorf("%s: %d content frames but the table lists %d documents: a document is missing from the table, or the table describes frames that are not documents",
			path, frames, len(refs))
	}
	// Cross-check the two indexes against each other. The seek table says where the frames are, the
	// document table says where the documents are, and documents are the frames between the
	// dictionary and the table: the i-th document of this file must be exactly frame i+2. A build
	// that wrote one index without the other is refused here rather than served as half a mirror.
	for i, r := range refs {
		if want, end := offsets[i+2], offsets[i+3]; r.Offset != want || r.Offset+r.Length != end {
			a.Close()
			return nil, fmt.Errorf("%s: %s recorded as bytes %d..%d, the frame table says frame %d is %d..%d",
				path, r.Accession, r.Offset, r.Offset+r.Length, i+2, want, end)
		}
		if r.Length == 0 || r.Offset+r.Length > offsets[frames-2] {
			a.Close()
			return nil, fmt.Errorf("%s: %s claims bytes %d..%d, past the end of the content frames at %d",
				path, r.Accession, r.Offset, r.Offset+r.Length, offsets[frames-2])
		}
		a.refs[r.Accession] = r
		a.order = append(a.order, r.Accession)
	}
	return a, nil
}

// Frame returns a document's compressed frame as it is stored, for a build that carries the
// document over without compiling it again. The frame is compressed against Header.Dictionary.
func (a *Artifact) Frame(accession string) (CompiledFrame, error) {
	r, ok := a.refs[accession]
	if !ok {
		return CompiledFrame{}, fmt.Errorf("%s: %s is not in this artifact", a.path, accession)
	}
	raw := make([]byte, r.Length)
	if _, err := a.file.ReadAt(raw, int64(r.Offset)); err != nil {
		return CompiledFrame{}, fmt.Errorf("%s: %s: %w", a.path, accession, err)
	}
	return CompiledFrame{Accession: accession, Raw: int(r.Raw), Compressed: raw}, nil
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
	raw, err := a.Payload(accession)
	if err != nil {
		return nil, err
	}
	var doc lexdoc.Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %s: %w", a.path, accession, err)
	}
	return &doc, nil
}

// Payload returns a document's decompressed JSON.
//
// Split out because the byte-bounded cache in front of this reader caches exactly these bytes: a
// decoded document is mutable, so it cannot be shared between concurrent requests, while a payload
// can be and its length is exactly what it costs to keep.
func (a *Artifact) Payload(accession string) ([]byte, error) {
	r, ok := a.refs[accession]
	if !ok {
		return nil, fmt.Errorf("%s: %s is not in this artifact (%d documents, built %s): rebuild the artifact from the mirror",
			a.path, accession, len(a.order), a.Header.Created.Format("2006-01-02"))
	}
	raw, err := a.decodeWith(a.docDecoder, r.Offset, r.Offset+r.Length)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", a.path, accession, err)
	}
	return raw, nil
}

// frameBytes reads and decompresses exactly the bytes between two offsets.
func (a *Artifact) frameBytes(from, to uint64) ([]byte, error) {
	return a.decodeWith(a.decoder, from, to)
}

// decodeWith reads the bytes between two offsets and decompresses them with dec.
func (a *Artifact) decodeWith(dec *zstd.Decoder, from, to uint64) ([]byte, error) {
	if to <= from {
		return nil, fmt.Errorf("frame from %d to %d is empty", from, to)
	}
	raw := make([]byte, to-from)
	if _, err := a.file.ReadAt(raw, int64(from)); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	plain, err := dec.DecodeAll(raw, nil)
	if err != nil {
		return nil, fmt.Errorf("frame at %d: %w", from, err)
	}
	return plain, nil
}

// readJSONFrame decodes exactly the bytes between two offsets and unmarshals them.
func (a *Artifact) readJSONFrame(from, to uint64, out any) error {
	plain, err := a.frameBytes(from, to)
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, out)
}

func (a *Artifact) Close() error {
	if a.docDecoder != nil {
		a.docDecoder.Close()
	}
	if a.decoder != nil {
		a.decoder.Close()
	}
	return a.file.Close()
}

// Identity names this artifact for whatever is derived from it: the search index records it, and
// a server compares it with the artifact it serves. It is the build's random stamp, so two builds
// are never mistaken for each other even from the same corpus in the same second.
func (a *Artifact) Identity() string {
	if a.Header.Stamp == "" {
		return "artifact " + a.Header.Created.UTC().Format(time.RFC3339Nano)
	}
	return "artifact " + a.Header.Stamp
}
