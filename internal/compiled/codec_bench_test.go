package compiled

import (
	"bytes"
	"encoding/gob"
	stdjson "encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	jsonv2 "github.com/go-json-experiment/json"
	"github.com/klauspost/compress/zstd"
	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

// TestCodecBench measures how a compiled document should be stored, on real documents,
// before any of it replaces the parse-on-read path. Run it with:
//
//	RETSIN_CODEC_BENCH=1 go test ./internal/compiled -run TestCodecBench -v
//
// The question it answers is narrow but load-bearing: the serving path currently parses
// the publisher's XML on every lookup, and the fix is to parse once at compile time and
// store the result. That only pays if the stored result is small enough to hold on disk
// compressed, quick enough to decode, and faithful enough that a decoded document still
// answers address lookups and reports the warnings the parser raised.
//
// The stored shape is deliberately NOT lexdania.Document: Document.warnings is
// unexported, and Section.Runs is a []textRun whose fields are unexported too, so every
// encoder in Go would silently write an empty value for both — the compiled form would
// lose warnings, which lookup returns to the caller, and lose the run splices that
// amendments are written back with. Section.Parent and Container are also cyclic
// pointers, which gob handles badly by design. cDoc is the same information with
// exported fields and indices instead of pointers, so the encoders are competing on
// payload, not on whether they can represent it at all.
func TestCodecBench(t *testing.T) {
	if os.Getenv("RETSIN_CODEC_BENCH") == "" {
		t.Skip("set RETSIN_CODEC_BENCH=1 to run; it reads the mirror and takes about a minute")
	}
	root := os.Getenv("RETSIN_DATA_DIR")
	if root == "" {
		root = filepath.Join("..", "..", "data")
	}
	want := 1000
	if v := os.Getenv("RETSIN_CODEC_DOCS"); v != "" {
		fmt.Sscanf(v, "%d", &want)
	}

	sample := sampleDocuments(t, root, want)
	if len(sample) < 20 {
		t.Fatalf("only %d documents parsed from %s; the sample is not representative", len(sample), root)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# compiled-document codec bench\n# generated: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "# reproduce: RETSIN_CODEC_BENCH=1 RETSIN_CODEC_DOCS=%d go test ./internal/compiled -run TestCodecBench -v\n", len(sample))
	fmt.Fprintf(&b, "# sample: %d documents, %.1f MB of publisher XML, from %s\n\n",
		len(sample), float64(totalXML(sample))/1e6, filepath.Base(root))

	// What the current path costs, so a compiled dataset has something to beat.
	var parse, section time.Duration
	docs := make([]*lexdania.Document, 0, len(sample))
	for _, s := range sample {
		start := time.Now()
		doc, err := lexdania.Parse(s.xml)
		if err != nil {
			t.Fatalf("parse %s: %v", s.path, err)
		}
		parse += time.Since(start)
		start = time.Now()
		doc.Sections()
		section += time.Since(start)
		docs = append(docs, doc)
	}
	fmt.Fprintf(&b, "## the parse this would replace\n")
	fmt.Fprintf(&b, "  lexdania.Parse:  %v total, %v per document\n", parse.Round(time.Microsecond), (parse / time.Duration(len(docs))).Round(time.Microsecond))
	fmt.Fprintf(&b, "  Sections():      %v total, %v per document\n", section.Round(time.Microsecond), (section / time.Duration(len(docs))).Round(time.Microsecond))
	fmt.Fprintf(&b, "  extrapolated to 191393 documents: parse %v\n\n", (parse/time.Duration(len(docs)))*191393)

	// The baseline that needs no new format at all: keep the XML and compress it.
	xmlBlob := make([]byte, 0, totalXML(sample))
	for _, s := range sample {
		xmlBlob = append(xmlBlob, s.xml...)
	}
	xmlZ := mustZstd(t, xmlBlob)
	fmt.Fprintf(&b, "## baseline: store the XML itself\n")
	fmt.Fprintf(&b, "  raw %8.1f MB   zstd %8.1f MB  (%.1f%% of raw)\n\n",
		mb(float64(len(xmlBlob))), mb(float64(len(xmlZ))), 100*float64(len(xmlZ))/float64(len(xmlBlob)))

	// The compiled shape every codec then encodes.
	compiled := make([]cDoc, 0, len(docs))
	for _, d := range docs {
		compiled = append(compiled, compile(d))
	}
	compiledMB := 0.0
	for i := range compiled {
		compiledMB += mb(float64(compiled[i].approxSize()))
	}
	fmt.Fprintf(&b, "## the compiled form (exported fields, indices not pointers)\n")
	fmt.Fprintf(&b, "  %.1f MB of Go strings and slices in memory, before any encoding.\n", compiledMB)
	fmt.Fprintf(&b, "  nodes %d, sections %d, warnings %d across %d documents\n\n",
		countNodes(compiled), countSections(compiled), countWarnings(compiled), len(compiled))

	fmt.Fprintf(&b, "## encoders\n")
	fmt.Fprintf(&b, "%-22s %10s %10s %10s %10s %10s %8s\n", "codec", "per-doc", "stream", "+zstd", "encode", "decode", "faith")
	for _, c := range []struct {
		name   string
		encode func(*bytes.Buffer, []cDoc) error
		decode func([]byte, int) ([]cDoc, error)
	}{
		{"gob stream", encodeGob, decodeGob},
		{"encoding/json", encodeStdJSON, decodeStdJSON},
		{"go-json-experiment", encodeJSONv2, decodeJSONv2},
		{"cbor", encodeCBOR, decodeCBOR},
	} {
		var perDoc int
		var buf bytes.Buffer
		start := time.Now()
		if err := c.encode(&buf, compiled); err != nil {
			t.Fatalf("%s encode: %v", c.name, err)
		}
		enc := time.Since(start)
		stream := buf.Bytes()

		start = time.Now()
		back, err := c.decode(stream, len(compiled))
		if err != nil {
			t.Fatalf("%s decode: %v", c.name, err)
		}
		dec := time.Since(start)

		faithful := true
		for i := range compiled {
			if !reflect.DeepEqual(compiled[i], back[i]) {
				faithful = false
				break
			}
		}
		// per-doc is what a seekable layout pays: each document is its own record, so
		// an encoder that amortises type names over a stream loses that advantage.
		for i := range compiled {
			var one bytes.Buffer
			if err := c.encode(&one, compiled[i:i+1]); err != nil {
				t.Fatalf("%s single encode: %v", c.name, err)
			}
			perDoc += one.Len()
		}
		z := mustZstd(t, stream)
		fmt.Fprintf(&b, "%-22s %9.1f MB %9.1f MB %9.1f MB %9v %10v %8v\n",
			c.name, mb(float64(perDoc)), mb(float64(len(stream))), mb(float64(len(z))),
			enc.Round(time.Millisecond), dec.Round(time.Millisecond), faithful)
	}

	// The reason the compiled form is pointer-free, stated as a measurement rather than
	// as folklore about gob.
	if err := gobTry(docs); err != nil {
		fmt.Fprintf(&b, "\n## why indices instead of the parsed struct\n")
		fmt.Fprintf(&b, "  gob over lexdania.Document as it stands: %v\n", err)
		fmt.Fprintf(&b, "  (Section.Parent and Section.Container are cyclic pointers; Document.warnings\n")
		fmt.Fprintf(&b, "   and textRun's fields are unexported, so every encoder would drop them silently.)\n")
	}

	report := filepath.Join("testdata", "codec-bench-"+time.Now().Format("20060102")+".txt")
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + b.String() + "\nreport written to " + report)
}

// gobTry encodes the live parsed documents, including their parent pointers, and returns
// whatever the encoder makes of that. A nil return means gob coped, which would be news.
func gobTry(docs []*lexdania.Document) error {
	defer func() {
		// A cyclic structure can overflow the stack rather than return an error, and a
		// benchmark harness has no business taking the test binary down for it.
		_ = recover()
	}()
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	for _, d := range docs {
		if err := enc.Encode(d); err != nil {
			return err
		}
	}
	return nil
}

type sampleDoc struct {
	path string
	xml  []byte
	doc  *lexdania.Document
}

// sampleDocuments walks the mirror deterministically: kinds in order, years ascending,
// and a fixed stride so the same N documents come back on every run. Random sampling
// would make two runs of the benchmark incomparable, which is the one thing a harness
// that exists to settle an argument must never do.
func sampleDocuments(t *testing.T, root string, want int) []sampleDoc {
	t.Helper()
	// The mirror is data/xml/<kind>/<year>/<n>.xml, so the walk is three levels and
	// every directory name is data, not configuration.
	kinds, err := os.ReadDir(filepath.Join(root, "xml"))
	if err != nil {
		t.Skipf("no mirror at %s: %v", root, err)
	}
	var files []string
	for _, k := range kinds {
		if !k.IsDir() {
			continue
		}
		years, err := os.ReadDir(filepath.Join(root, "xml", k.Name()))
		if err != nil {
			continue
		}
		for _, y := range years {
			if !y.IsDir() {
				continue
			}
			entries, err := os.ReadDir(filepath.Join(root, "xml", k.Name(), y.Name()))
			if err != nil {
				continue
			}
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".xml") {
					files = append(files, filepath.Join(root, "xml", k.Name(), y.Name(), e.Name()))
				}
			}
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		t.Skipf("no XML found under %s", root)
	}
	stride := max(len(files)/want, 1)
	out := make([]sampleDoc, 0, want)
	for i := 0; i < len(files) && len(out) < want; i += stride {
		raw, err := os.ReadFile(files[i])
		if err != nil {
			continue
		}
		if _, err := lexdania.Parse(raw); err != nil {
			continue // documents the parser refuses are not the serving path's problem
		}
		out = append(out, sampleDoc{path: files[i], xml: raw})
	}
	return out
}

func totalXML(s []sampleDoc) int {
	n := 0
	for _, d := range s {
		n += len(d.xml)
	}
	return n
}

func mb(f float64) float64 { return f / 1e6 }

func mustZstd(t *testing.T, b []byte) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	return enc.EncodeAll(b, nil)
}

// cDoc is the compiled representation: the same information a parsed Document carries,
// with exported fields so nothing is dropped on the way to disk, and indices so the
// parent/child graph survives without cycles.
type cDoc struct {
	// Meta is carried as parsed. Its Raw field is nil here on purpose: nothing outside
	// the parser reads it, and keeping it would store the metadata block twice.
	Meta           lexdania.Meta
	Nodes          []cNode
	Root           int
	Body           []int
	Sections       []cSection
	Warnings       []string // Document's own, which are unexported in the parse struct
	SchemaLocation string
	Source         string
	Sidecar        string
}

type cNode struct {
	Name  string
	Space string
	Attrs map[string]string
	Runs  []string
	Elems []int
}

type cSection struct {
	Node      int
	Container int
	Parent    int // -1 when there is none
	Kind      string
	Number    string
	RawNumber string
	Stk       int
	Nr        int
	Litra     string
	Appendix  bool
	Match     string
	Runs      []cRun
}

type cRun struct {
	Node int
	Idx  int
}

// compile flattens a parsed document. Node indices are assigned in walk order, so the
// graph is one array and a list of child indices rather than a forest of pointers.
func compile(d *lexdania.Document) cDoc {
	out := cDoc{Meta: d.Meta, SchemaLocation: d.SchemaLocation, Sidecar: d.Sidecar, Source: string(d.Source)}
	out.Meta.Raw = nil
	out.Warnings = d.Warnings()

	index := map[*lexdania.Node]int{}
	var add func(*lexdania.Node) int
	add = func(n *lexdania.Node) int {
		if n == nil {
			return -1
		}
		if i, ok := index[n]; ok {
			return i
		}
		i := len(out.Nodes)
		out.Nodes = append(out.Nodes, cNode{})
		cn := cNode{Name: n.Name, Space: n.Space, Runs: n.Runs}
		if len(n.Attrs) > 0 {
			cn.Attrs = map[string]string{}
			for k, v := range n.Attrs {
				cn.Attrs[k] = v
			}
		}
		cn.Elems = make([]int, 0, len(n.Elems))
		index[n] = i
		out.Nodes[i] = cn
		for _, e := range n.Elems {
			cn.Elems = append(cn.Elems, add(e))
		}
		out.Nodes[i] = cn
		return i
	}
	out.Root = add(d.Root)
	for _, b := range d.Body {
		out.Body = append(out.Body, add(b))
	}

	secs := d.Sections()
	pos := map[*lexdania.Section]int{}
	for i, s := range secs {
		pos[s] = i
	}
	for _, s := range secs {
		cs := cSection{
			Node:      index[s.Node],
			Kind:      s.Kind,
			Number:    s.Number,
			RawNumber: s.RawNumber,
			Stk:       s.Stk,
			Nr:        s.Nr,
			Litra:     s.Litra,
			Appendix:  s.Appendix,
			Match:     s.Match,
			Parent:    -1,
		}
		if s.Container != nil {
			cs.Container = index[s.Container]
		}
		if s.Parent != nil {
			cs.Parent = pos[s.Parent]
		}
		out.Sections = append(out.Sections, cs)
	}
	return out
}

func (c cDoc) approxSize() int {
	n := len(c.Sidecar) + len(c.SchemaLocation) + len(c.Source)
	for _, s := range c.Warnings {
		n += len(s)
	}
	for _, nd := range c.Nodes {
		n += len(nd.Name) + len(nd.Space)
		for _, r := range nd.Runs {
			n += len(r)
		}
		for k, v := range nd.Attrs {
			n += len(k) + len(v)
		}
		n += 8 * len(nd.Elems)
	}
	for _, s := range c.Sections {
		n += len(s.Kind) + len(s.Number) + len(s.RawNumber) + len(s.Litra) + len(s.Match)
	}
	return n
}

func countNodes(docs []cDoc) int {
	n := 0
	for _, d := range docs {
		n += len(d.Nodes)
	}
	return n
}

func countSections(docs []cDoc) int {
	n := 0
	for _, d := range docs {
		n += len(d.Sections)
	}
	return n
}

func countWarnings(docs []cDoc) int {
	n := 0
	for _, d := range docs {
		n += len(d.Warnings)
	}
	return n
}

// The four encoders, each behind the same two functions so the loop above stays about
// payload sizes rather than about API differences.

func encodeGob(buf *bytes.Buffer, docs []cDoc) error {
	enc := gob.NewEncoder(buf)
	for i := range docs {
		if err := enc.Encode(&docs[i]); err != nil {
			return err
		}
	}
	return nil
}

func decodeGob(b []byte, n int) ([]cDoc, error) {
	dec := gob.NewDecoder(bytes.NewReader(b))
	out := make([]cDoc, 0, n)
	for i := 0; i < n; i++ {
		var d cDoc
		if err := dec.Decode(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func encodeStdJSON(buf *bytes.Buffer, docs []cDoc) error {
	for i := range docs {
		b, err := stdjson.Marshal(docs[i])
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return nil
}

func decodeStdJSON(b []byte, n int) ([]cDoc, error) {
	var out []cDoc
	dec := stdjson.NewDecoder(bytes.NewReader(b))
	for i := 0; i < n; i++ {
		var d cDoc
		if err := dec.Decode(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func encodeJSONv2(buf *bytes.Buffer, docs []cDoc) error {
	for i := range docs {
		if err := jsonv2.MarshalWrite(buf, docs[i]); err != nil {
			return err
		}
		buf.WriteByte('\n')
	}
	return nil
}

func decodeJSONv2(b []byte, n int) ([]cDoc, error) {
	lines := bytes.Split(bytes.TrimRight(b, "\n"), []byte{'\n'})
	out := make([]cDoc, 0, n)
	for _, line := range lines {
		var d cDoc
		if err := jsonv2.Unmarshal(line, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if len(out) != n {
		return nil, fmt.Errorf("jsonv2: decoded %d documents, wanted %d", len(out), n)
	}
	return out, nil
}

func encodeCBOR(buf *bytes.Buffer, docs []cDoc) error {
	for i := range docs {
		b, err := cbor.Marshal(docs[i])
		if err != nil {
			return err
		}
		buf.Write(b)
	}
	return nil
}

func decodeCBOR(b []byte, n int) ([]cDoc, error) {
	var out []cDoc
	dec := cbor.NewDecoder(bytes.NewReader(b))
	for i := 0; i < n; i++ {
		var d cDoc
		if err := dec.Decode(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}
