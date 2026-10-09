package xet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pierrec/lz4/v4"
)

func randomBytes(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func cut(t *testing.T, b []byte) []int {
	t.Helper()
	var sizes []int
	if err := Cut(bytes.NewReader(b), func(n int) { sizes = append(sizes, n) }); err != nil {
		t.Fatal(err)
	}
	return sizes
}

// Chunks are cut by content: within the size bounds, and an insertion disturbs only the chunks
// around it. (The boundaries were checked once against the protocol's reference file.)
func TestCutIsContentDefined(t *testing.T) {
	a := randomBytes(1, 2<<20)
	sizes := cut(t, a)
	total := 0
	for i, n := range sizes {
		total += n
		if n > maxChunk || n < minChunk && i != len(sizes)-1 {
			t.Errorf("chunk %d is %d bytes", i, n)
		}
	}
	if total != len(a) {
		t.Fatalf("the chunks cover %d of %d bytes", total, len(a))
	}
	b := append(append(append([]byte{}, a[:1<<20]...), randomBytes(2, 100)...), a[1<<20:]...)
	shared := map[string]bool{}
	off := 0
	for _, n := range sizes {
		shared[string(a[off:off+n])] = true
		off += n
	}
	changed := 0
	off = 0
	for _, n := range cut(t, b) {
		if !shared[string(b[off:off+n])] {
			changed++
		}
		off += n
	}
	if changed > 2 {
		t.Errorf("100 inserted bytes changed %d chunks", changed)
	}
}

// split is byte grouping, the inverse of regroup, as the uploader does it.
func split(b []byte) []byte {
	var g [4][]byte
	for j, c := range b {
		g[j%4] = append(g[j%4], c)
	}
	return bytes.Join(g[:], nil)
}

func serialize(t *testing.T, scheme byte, data []byte) []byte {
	t.Helper()
	stored := data
	if scheme != schemeNone {
		in := data
		if scheme == schemeBG4LZ4 {
			in = split(data)
		}
		var buf bytes.Buffer
		w := lz4.NewWriter(&buf)
		if _, err := w.Write(in); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		stored = buf.Bytes()
	}
	h := []byte{0, byte(len(stored)), byte(len(stored) >> 8), byte(len(stored) >> 16), scheme, byte(len(data)), byte(len(data) >> 8), byte(len(data) >> 16)}
	return append(h, stored...)
}

func TestEveryCompressionSchemeReadsBack(t *testing.T) {
	data := bytes.Repeat([]byte("§ 1. Stk. 2, nr. 3 "), 263) // 4997 bytes: not a multiple of 4
	for _, scheme := range []byte{schemeNone, schemeLZ4, schemeBG4LZ4} {
		got, err := readChunk(bytes.NewReader(serialize(t, scheme, data)))
		if err != nil || !bytes.Equal(got, data) {
			t.Errorf("scheme %d: %d bytes, %v", scheme, len(got), err)
		}
	}
	if _, err := readChunk(bytes.NewReader(serialize(t, 7, data))); err == nil {
		t.Error("an unknown scheme was read")
	}
}

// fakeXet is the Hub, the CAS and the xorb store in one server. Each stored file's new chunks go
// into a xorb of their own; chunks seen before are referred to where they are.
type fakeXet struct {
	t      *testing.T
	srv    *httptest.Server
	files  map[string]string // name → hash
	recons map[string]reconstruction
	xorbs  map[string][]byte  // serialized
	offs   map[string][]int64 // chunk start offsets in the serialized xorb, and its end
	known  map[string]chunkID // chunk content → where it is
	mu     sync.Mutex
	asked  map[string][]string // xorb → Range headers asked for
}

func newFakeXet(t *testing.T) *fakeXet {
	f := &fakeXet{t: t, files: map[string]string{}, recons: map[string]reconstruction{}, xorbs: map[string][]byte{}, offs: map[string][]int64{}, known: map[string]chunkID{}, asked: map[string][]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeXet) store(name string, content []byte) {
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	sum = sha256.Sum256([]byte(name))
	xorb := hex.EncodeToString(sum[:])
	var rec reconstruction
	off := 0
	for _, n := range cut(f.t, content) {
		c := content[off : off+n]
		off += n
		id, ok := f.known[string(c)]
		if !ok {
			id = chunkID{xorb, len(f.offs[xorb])}
			f.known[string(c)] = id
			f.offs[xorb] = append(f.offs[xorb], int64(len(f.xorbs[xorb])))
			f.xorbs[xorb] = append(f.xorbs[xorb], serialize(f.t, byte(id.index%2), c)...) // none and LZ4
		}
		if k := len(rec.Terms) - 1; k >= 0 && rec.Terms[k].Xorb == id.xorb && rec.Terms[k].Range.End == id.index {
			rec.Terms[k].Range.End++
			rec.Terms[k].Unpacked += int64(n)
		} else {
			rec.Terms = append(rec.Terms, term{Xorb: id.xorb, Unpacked: int64(n), Range: chunkRange{id.index, id.index + 1}})
		}
	}
	f.files[name], f.recons[hash] = hash, rec
}

// bound is where chunk i of xorb starts, or its end.
func (f *fakeXet) bound(xorb string, i int) int64 {
	if i == len(f.offs[xorb]) {
		return int64(len(f.xorbs[xorb]))
	}
	return f.offs[xorb][i]
}

func (f *fakeXet) serve(w http.ResponseWriter, r *http.Request) {
	switch p := r.URL.Path; {
	case strings.HasPrefix(p, "/datasets/o/n/resolve/main/"):
		hash, ok := f.files[strings.TrimPrefix(p, "/datasets/o/n/resolve/main/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Xet-Hash", hash)
		w.WriteHeader(http.StatusFound)
	case p == "/api/datasets/o/n/xet-read-token/main":
		json.NewEncoder(w).Encode(map[string]any{"accessToken": "xt", "exp": 1 << 40, "casUrl": f.srv.URL})
	case strings.HasPrefix(p, "/v2/reconstructions/"):
		if r.Header.Get("Authorization") != "Bearer xt" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		rec, ok := f.recons[strings.TrimPrefix(p, "/v2/reconstructions/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		rec.Xorbs = map[string][]fetch{}
		for _, t := range rec.Terms {
			fe := rec.Xorbs[t.Xorb]
			if fe == nil {
				fe = []fetch{{URL: f.srv.URL + "/xorb/" + t.Xorb}}
			}
			var rg = struct {
				Chunks chunkRange `json:"chunks"`
				Bytes  struct {
					Start int64 `json:"start"`
					End   int64 `json:"end"`
				} `json:"bytes"`
			}{Chunks: t.Range}
			rg.Bytes.Start, rg.Bytes.End = f.bound(t.Xorb, t.Range.Start), f.bound(t.Xorb, t.Range.End)-1
			fe[0].Ranges = append(fe[0].Ranges, rg)
			rec.Xorbs[t.Xorb] = fe
		}
		json.NewEncoder(w).Encode(rec)
	case strings.HasPrefix(p, "/xorb/"):
		xorb := f.xorbs[strings.TrimPrefix(p, "/xorb/")]
		f.mu.Lock()
		f.asked[strings.TrimPrefix(p, "/xorb/")] = append(f.asked[strings.TrimPrefix(p, "/xorb/")], r.Header.Get("Range"))
		f.mu.Unlock()
		var ranges [][2]int64
		for s := range strings.SplitSeq(strings.TrimPrefix(r.Header.Get("Range"), "bytes="), ",") {
			a, b, _ := strings.Cut(s, "-")
			x, _ := strconv.ParseInt(a, 10, 64)
			y, _ := strconv.ParseInt(b, 10, 64)
			ranges = append(ranges, [2]int64{x, y})
		}
		if len(ranges) == 1 {
			w.WriteHeader(http.StatusPartialContent)
			w.Write(xorb[ranges[0][0] : ranges[0][1]+1])
			return
		}
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		for _, rg := range ranges {
			pw, _ := mw.CreatePart(textproto.MIMEHeader{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", rg[0], rg[1], len(xorb))}})
			pw.Write(xorb[rg[0] : rg[1]+1])
		}
		mw.Close()
		w.Header().Set("Content-Type", "multipart/byteranges; boundary="+mw.Boundary())
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body.Bytes())
	default:
		http.NotFound(w, r)
	}
}

// A new version of a file on disk is assembled from the old one, and only the xorb holding what
// changed is fetched; without the old one, everything is, in one request per xorb.
func TestADownloadFetchesOnlyWhatIsNotOnDisk(t *testing.T) {
	x := newFakeXet(t)
	old := randomBytes(1, 3<<19)
	updated := append(append(append([]byte{}, old[:700_000]...), randomBytes(2, 3000)...), old[700_000:]...)
	x.store("old", old)
	x.store("new", updated)
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old")
	if err := os.WriteFile(oldPath, old, 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Hub{Repo: "o/n", BaseURL: x.srv.URL}
	ctx := context.Background()
	oldHash, err := h.FileHash(ctx, "old")
	if err != nil {
		t.Fatal(err)
	}
	newHash, _ := h.FileHash(ctx, "new")
	sum := sha256.Sum256([]byte("old"))
	oldXorb := hex.EncodeToString(sum[:])

	dst := filepath.Join(dir, "new")
	st, err := h.Download(ctx, newHash, dst, []Have{{oldPath, oldHash}})
	if got, _ := os.ReadFile(dst); err != nil || !bytes.Equal(got, updated) {
		t.Fatalf("download: %d bytes, %v", len(got), err)
	}
	if len(x.asked[oldXorb]) != 0 || st.Reused < int64(len(old))-2*maxChunk || st.Fetched > 3*maxChunk || len(st.Ignored) != 0 {
		t.Errorf("with the old file on disk: %+v, the old xorb asked for %v", st, x.asked[oldXorb])
	}

	x.asked = map[string][]string{}
	st, err = h.Download(ctx, newHash, dst, nil)
	if got, _ := os.ReadFile(dst); err != nil || !bytes.Equal(got, updated) {
		t.Fatalf("download from nothing: %d bytes, %v", len(got), err)
	}
	if st.Reused != 0 || len(x.asked[oldXorb]) != 1 || !strings.Contains(x.asked[oldXorb][0], ",") {
		t.Errorf("from nothing: %+v; the old xorb asked for %v, want its two runs in one request", st, x.asked[oldXorb])
	}

	// A file on disk that is not what its hash names is left alone, and the download goes on.
	st, err = h.Download(ctx, newHash, dst, []Have{{oldPath, newHash}})
	if got, _ := os.ReadFile(dst); err != nil || !bytes.Equal(got, updated) || len(st.Ignored) != 1 || st.Reused != 0 {
		t.Errorf("with a mislabelled file on disk: %+v, %v", st, err)
	}
}
