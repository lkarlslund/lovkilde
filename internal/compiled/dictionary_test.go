package compiled

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/klauspost/compress/dict"

	"github.com/lkarlslund/retsinformation/internal/compiled/dictionary"
)

// TestAnArtifactCarriesItsOwnDictionary: documents are compressed against a dictionary, and the
// reader decodes them with the one the file carries, not the one this build embeds. An artifact
// written with another dictionary — an older build's, or a retrained one — must read back the
// same, and its header must name the dictionary it holds.
func TestAnArtifactCarriesItsOwnDictionary(t *testing.T) {
	doc := fixtureDocument()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	// Varied samples: the library's builder crashes on identical ones.
	var samples [][]byte
	for i := 0; i < 64; i++ {
		s := append([]byte(fmt.Sprintf(`{"n":%d,"pad":"%s",`, i, strings.Repeat(string(rune('a'+i%26)), i*7))), raw[1:]...)
		samples = append(samples, s)
	}
	other, err := dict.BuildZstdDict(samples, dict.Options{MaxDictSize: 4 << 10, HashBytes: 6, ZstdDictID: 4242})
	if err != nil {
		t.Fatal(err)
	}

	for name, d := range map[string][]byte{"embedded": dictionary.Current, "another": other} {
		path := filepath.Join(t.TempDir(), "artifact.bin")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		wr, err := newWriter(f, "test", artifactVersion, d, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := wr.Add("A20260000001", doc); err != nil {
			t.Fatal(err)
		}
		wr.SetCatalogue([]byte("[]")) // no archive: an empty catalogue
		if err := wr.Close(); err != nil {
			t.Fatal(err)
		}
		f.Close()

		a, err := OpenArtifact(path)
		if err != nil {
			t.Fatalf("%s dictionary: %v", name, err)
		}
		want, _ := describeDictionary(d)
		if a.Header.Dictionary != want {
			t.Errorf("%s dictionary: header names %+v, want %+v", name, a.Header.Dictionary, want)
		}
		back, err := a.Document("A20260000001")
		if err != nil {
			t.Fatalf("%s dictionary: %v", name, err)
		}
		if !reflect.DeepEqual(back, doc) {
			t.Errorf("%s dictionary: the document did not come back unchanged", name)
		}
		a.Close()
	}

	if _, err := newWriter(nil, "test", artifactVersion, []byte("not a dictionary at all"), nil); err == nil {
		t.Error("a writer accepted bytes that are not a zstd dictionary")
	}
}
