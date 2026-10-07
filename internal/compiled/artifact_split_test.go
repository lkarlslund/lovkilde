package compiled

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// TestCompilingOutOfOrderWritesTheSameFile pins the split the parallel build rests on.
//
// A build now compiles documents on worker goroutines and writes their frames here in
// mirror order. That is only safe if compiling carries no writer state: no position, no
// scratch buffer, no table order. So this test compiles the documents in reverse and
// writes them forwards, and requires the result to be the same artifact the serial
// writer produces — same frame sizes at the same offsets, not merely readable.
func TestCompilingOutOfOrderWritesTheSameFile(t *testing.T) {
	const n = 7
	var docs []ArtifactDoc
	for i := range n {
		d := fixtureDocument()
		acc := fmt.Sprintf("A202600000%02d", i)
		d.Identity.Accession = acc
		docs = append(docs, ArtifactDoc{Accession: acc, Document: d})
	}

	serial := filepath.Join(t.TempDir(), "serial.bin")
	if err := writeArtifactFile(t, serial, "serial", docs); err != nil {
		t.Fatal(err)
	}

	split := filepath.Join(t.TempDir(), "split.bin")
	if err := writeSplitArtifact(t, split, "split", docs); err != nil {
		t.Fatal(err)
	}

	a, err := OpenArtifact(serial)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenArtifact(split)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// The layout, not just the contents. Absolute offsets cannot be compared across two
	// files: the header carries the time it was written, so its compressed frame differs
	// by a few bytes and shifts every document after it. What must be identical is each
	// frame's own size and its distance from the first document — a compile step that
	// quietly depended on writer state would change one of those.
	if !slices.Equal(a.order, b.order) {
		t.Fatalf("the two writers listed the documents differently: %v vs %v", a.order, b.order)
	}
	baseA, baseB := uint64(0), uint64(0)
	for i, d := range docs {
		ra, ok := a.Ref(d.Accession)
		if !ok {
			t.Fatalf("%s is not in the serial artifact", d.Accession)
		}
		rb, ok := b.Ref(d.Accession)
		if !ok {
			t.Fatalf("%s is not in the split artifact", d.Accession)
		}
		if i == 0 {
			baseA, baseB = ra.Offset, rb.Offset
		}
		if ra.Length != rb.Length || ra.Raw != rb.Raw || ra.Offset-baseA != rb.Offset-baseB {
			t.Errorf("%s is laid out differently: serial {off %d len %d raw %d} vs split {off %d len %d raw %d}",
				d.Accession, ra.Offset, ra.Length, ra.Raw, rb.Offset, rb.Length, rb.Raw)
		}
	}

	for _, d := range docs {
		got, err := b.Document(d.Accession)
		if err != nil {
			t.Fatalf("%s: %v", d.Accession, err)
		}
		want, err := a.Document(d.Accession)
		if err != nil {
			t.Fatalf("%s serial: %v", d.Accession, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s came back differently:\n got %s\nwant %s", d.Accession, brief(got), brief(want))
		}
		if g, w := got.Content.Markdown(), d.Document.Content.Markdown(); g != w {
			t.Errorf("%s markdown changed through the split writer:\n got:\n%s\nwant:\n%s", d.Accession, g, w)
		}
	}
}

// writeSplitArtifact compiles every document first, in reverse, then writes the frames
// forwards — the shape of the real build, with the scheduling made deterministic so the
// test fails on a dependency rather than on a race.
func writeSplitArtifact(t *testing.T, path, source string, docs []ArtifactDoc) error {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	wr, err := NewWriter(f, source)
	if err != nil {
		f.Close()
		return err
	}
	frames := make([]CompiledFrame, len(docs))
	for i := len(docs) - 1; i >= 0; i-- {
		frames[i], err = wr.Compile(docs[i].Accession, docs[i].Document)
		if err != nil {
			f.Close()
			return err
		}
	}
	for _, fr := range frames {
		if err := wr.WriteFrame(fr); err != nil {
			f.Close()
			return err
		}
	}
	wr.SetCatalogue([]byte("[]")) // no archive: an empty catalogue
	if err := wr.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
