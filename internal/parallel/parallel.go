// Package parallel runs independent work over an index range from worker goroutines.
package parallel

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
)

// For runs visit over the indices 0..n-1 from worker goroutines and blocks until
// every one is done. Workers scale with GOMAXPROCS, so GOMAXPROCS=1 is the serial
// path and a measurement at 1 is the same code rather than a different one.
//
// Work is split into disjoint contiguous chunks and each result is written back to
// the slot its index owns, so no mutex is taken anywhere: a bug shows up as a wrong
// count, not as a race. Callers that need the original order keep it by reading the
// filled slice back in index order — the order visit runs in is not defined.
//
// Chunking rather than a shared queue is deliberate for this repository's workloads:
// the mirror is a directory tree, so a chunk reads neighbours whose metadata the
// kernel just cached, and there is no contention point to measure against.
func For(ctx context.Context, n int, visit func(i int)) {
	if n <= 0 {
		return
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > n {
		workers = n
	}
	if workers < 1 {
		workers = 1
	}
	// Pieces are contiguous and roughly a sixteenth of a worker's share, and workers
	// take them from a counter rather than being handed one big range. Contiguity is
	// worth keeping — the mirror is a sorted path list, so a piece reads neighbours the
	// kernel just cached — but one big range per worker means one worker that drew the
	// long documents finishes last and everyone else idles.
	const piecesPerWorker = 16
	pieces := workers * piecesPerWorker
	if pieces > n {
		pieces = n
	}
	piece := (n + pieces - 1) / pieces
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				lo := int(next.Add(int64(piece))) - int(piece)
				if lo >= n {
					return
				}
				hi := min(lo+piece, n)
				if ctx.Err() != nil {
					return
				}
				for i := lo; i < hi; i++ {
					visit(i)
				}
			}
		}()
	}
	wg.Wait()
}
