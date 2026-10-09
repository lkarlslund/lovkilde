package xet

import (
	"context"
	"fmt"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// fetchers is how many xorb ranges are downloaded at once.
const fetchers = 4

// Have is a file on disk that is also in Xet storage, under Hash: the chunks it shares with a
// download are copied from it rather than fetched.
type Have struct {
	Path string
	Hash string
}

// Stats says where a download's bytes came from.
type Stats struct {
	Fetched int64 // bytes received from Xet storage
	Reused  int64 // bytes of the file copied from files on disk
	// Ignored is why a Have was not read from: it could not be mapped onto its chunks.
	Ignored []error
}

// chunkID is a chunk in a xorb.
type chunkID struct {
	xorb  string
	index int
}

// span is where a chunk's data is on disk.
type span struct {
	f   *os.File
	off int64
	n   int64
}

// Download writes the file stored under hash to dst, copying every chunk it shares with have
// from disk and fetching only the others. The caller verifies what was written: a chunk is
// checked against the length the reconstruction gives its run, not against its hash.
func (h *Hub) Download(ctx context.Context, hash, dst string, have []Have) (Stats, error) {
	var st Stats
	rec, err := h.reconstruction(ctx, hash)
	if err != nil {
		return st, err
	}
	local := map[chunkID]span{}
	for _, hv := range have {
		f, err := os.Open(hv.Path)
		if err != nil {
			st.Ignored = append(st.Ignored, err)
			continue
		}
		defer f.Close()
		if err := h.mapFile(ctx, f, hv.Hash, local); err != nil {
			st.Ignored = append(st.Ignored, fmt.Errorf("%s: %w", hv.Path, err))
		}
	}

	spool, err := os.MkdirTemp(filepath.Dir(dst), ".xet-")
	if err != nil {
		return st, err
	}
	defer os.RemoveAll(spool)
	fetched, n, err := h.fetchMissing(ctx, rec, local, spool)
	st.Fetched = n
	for _, f := range fetched.files {
		defer f.Close()
	}
	if err != nil {
		return st, err
	}

	out, err := os.Create(dst)
	if err != nil {
		return st, err
	}
	for _, t := range rec.Terms {
		var got int64
		for i := t.Range.Start; i < t.Range.End; i++ {
			id := chunkID{t.Xorb, i}
			s, ok := local[id]
			if ok {
				st.Reused += s.n
			} else if s, ok = fetched.spans[id]; !ok {
				err = fmt.Errorf("chunk %d of xorb %s was neither on disk nor fetched", i, t.Xorb)
				break
			}
			if _, err = io.Copy(out, io.NewSectionReader(s.f, s.off, s.n)); err != nil {
				break
			}
			got += s.n
		}
		if err == nil && got != t.Unpacked {
			err = fmt.Errorf("chunks %d–%d of xorb %s hold %d bytes, the reconstruction says %d", t.Range.Start, t.Range.End, t.Xorb, got, t.Unpacked)
		}
		if err != nil {
			break
		}
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
	}
	return st, err
}

// mapFile records where on disk each chunk of the file stored under hash is, f being that file:
// f is cut into chunks and the cuts are laid over the file's terms in order. A file that is not
// what hash names does not fit its terms, and nothing of it is recorded.
func (h *Hub) mapFile(ctx context.Context, f *os.File, hash string, into map[chunkID]span) error {
	rec, err := h.reconstruction(ctx, hash)
	if err != nil {
		return err
	}
	var sizes []int64
	if err := Cut(f, func(n int) { sizes = append(sizes, int64(n)) }); err != nil {
		return err
	}
	found := map[chunkID]span{}
	var off int64
	next := 0
	for _, t := range rec.Terms {
		var sum int64
		for i := t.Range.Start; i < t.Range.End; i++ {
			if next == len(sizes) {
				return fmt.Errorf("has fewer chunks than %s", hash)
			}
			found[chunkID{t.Xorb, i}] = span{f, off, sizes[next]}
			off += sizes[next]
			sum += sizes[next]
			next++
		}
		if sum != t.Unpacked {
			return fmt.Errorf("its chunks do not fit the terms of %s", hash)
		}
	}
	if next != len(sizes) {
		return fmt.Errorf("has more chunks than %s", hash)
	}
	maps.Copy(into, found)
	return nil
}

// spooled is the chunks fetched, decompressed into spool files.
type spooled struct {
	mu    sync.Mutex
	spans map[chunkID]span
	files []*os.File
}

// fetchMissing downloads every range of rec's xorbs that holds a chunk the file needs and local
// does not have, and returns where each fetched chunk was spooled and how many bytes came over
// the network.
func (h *Hub) fetchMissing(ctx context.Context, rec *reconstruction, local map[chunkID]span, dir string) (*spooled, int64, error) {
	need := map[chunkID]bool{}
	for _, t := range rec.Terms {
		for i := t.Range.Start; i < t.Range.End; i++ {
			if _, ok := local[chunkID{t.Xorb, i}]; !ok {
				need[chunkID{t.Xorb, i}] = true
			}
		}
	}
	type job struct {
		xorb string
		f    fetch
	}
	var jobs []job
	for xorb, fs := range rec.Xorbs {
		for _, f := range fs {
			// Only the ranges holding a needed chunk; the signed URL serves any of its ranges.
			keep := f
			keep.Ranges = nil
			for _, r := range f.Ranges {
				for i := r.Chunks.Start; i < r.Chunks.End; i++ {
					if need[chunkID{xorb, i}] {
						keep.Ranges = append(keep.Ranges, r)
						break
					}
				}
			}
			if len(keep.Ranges) > 0 {
				jobs = append(jobs, job{xorb, keep})
			}
		}
	}

	sp := &spooled{spans: map[chunkID]span{}}
	var total int64
	var totalMu sync.Mutex
	for range min(fetchers, len(jobs)) {
		f, err := os.CreateTemp(dir, "spool-")
		if err != nil {
			return sp, 0, err
		}
		sp.files = append(sp.files, f)
	}
	work := make(chan job)
	g, gctx := errgroup.WithContext(ctx)
	for _, f := range sp.files {
		g.Go(func() error {
			for j := range work {
				n, err := h.fetchRanges(gctx, j.xorb, j.f, f, sp)
				totalMu.Lock()
				total += n
				totalMu.Unlock()
				if err != nil {
					return err
				}
			}
			return nil
		})
	}
	g.Go(func() error {
		defer close(work)
		for _, j := range jobs {
			select {
			case work <- j:
			case <-gctx.Done():
				return gctx.Err()
			}
		}
		return nil
	})
	return sp, total, g.Wait()
}

// fetchRanges downloads f's ranges of xorb in one request, retrying a failed one, and spools
// their chunks into spool.
func (h *Hub) fetchRanges(ctx context.Context, xorb string, f fetch, spool *os.File, sp *spooled) (int64, error) {
	var total int64
	var err error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			case <-ctx.Done():
				return total, ctx.Err()
			}
		}
		var n int64
		n, err = h.fetchOnce(ctx, xorb, f, spool, sp)
		total += n
		if err == nil || ctx.Err() != nil {
			break
		}
	}
	return total, err
}

func (h *Hub) fetchOnce(ctx context.Context, xorb string, f fetch, spool *os.File, sp *spooled) (int64, error) {
	ranges := make([]string, len(f.Ranges))
	for i, r := range f.Ranges {
		ranges[i] = strconv.FormatInt(r.Bytes.Start, 10) + "-" + strconv.FormatInt(r.Bytes.End, 10)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", "bytes="+strings.Join(ranges, ","))
	resp, err := h.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("xorb %s: %s", xorb, resp.Status)
	}
	body := &counter{r: resp.Body}
	if len(f.Ranges) == 1 {
		return body.n, spoolRange(xorb, f.Ranges[0].Chunks, body, spool, sp)
	}
	mt, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/byteranges" {
		return 0, fmt.Errorf("xorb %s: %d ranges answered as %q", xorb, len(f.Ranges), resp.Header.Get("Content-Type"))
	}
	// The parts come in the order the ranges were asked for.
	mr := multipart.NewReader(body, params["boundary"])
	for i, r := range f.Ranges {
		p, err := mr.NextPart()
		if err != nil {
			return body.n, fmt.Errorf("xorb %s: %d ranges asked for, %d answered: %w", xorb, len(f.Ranges), i, err)
		}
		if err := spoolRange(xorb, r.Chunks, p, spool, sp); err != nil {
			return body.n, err
		}
	}
	return body.n, nil
}

// spoolRange reads the chunks r holds, decompressed, onto the end of spool.
func spoolRange(xorb string, chunks chunkRange, r io.Reader, spool *os.File, sp *spooled) error {
	for i := chunks.Start; i < chunks.End; i++ {
		data, err := readChunk(r)
		if err != nil {
			return fmt.Errorf("chunk %d of xorb %s: %w", i, xorb, unexpected(err))
		}
		off, err := spool.Seek(0, io.SeekEnd)
		if err != nil {
			return err
		}
		if _, err := spool.Write(data); err != nil {
			return err
		}
		sp.mu.Lock()
		sp.spans[chunkID{xorb, i}] = span{spool, off, int64(len(data))}
		sp.mu.Unlock()
	}
	if n, _ := io.Copy(io.Discard, r); n != 0 {
		return fmt.Errorf("xorb %s: %d bytes after chunk %d", xorb, n, chunks.End-1)
	}
	return nil
}

type counter struct {
	r io.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
