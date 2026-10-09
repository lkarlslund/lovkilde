// Package xet downloads files from Hugging Face's Xet storage, reading the chunks a file shares
// with files already on disk from those files instead of downloading them.
//
// Xet stores a file as content-defined chunks of about 64 KiB, packed into xorbs, and describes it
// as a list of terms: runs of chunks in a xorb. Two versions of a file that differ in a few places
// share most of their chunks, so most of the new version's terms name runs of the old version's
// xorbs. A file on disk is mapped to its chunks by cutting it the way Xet cuts files (Cut) and
// laying the cuts over its own terms; whatever the new file needs from a mapped run is copied from
// disk, and only the rest is fetched.
//
// The protocol is specified at https://huggingface.co/docs/xet. Only downloading is implemented:
// the hf command uploads.
package xet

import (
	"io"
)

const (
	minChunk = 8 << 10
	maxChunk = 128 << 10
	// boundaryMask has 16 bits set, so a boundary is found every 64 KiB on average.
	boundaryMask = uint64(0xffff) << 48
)

// Cut splits what r holds into Xet's content-defined chunks and calls emit with the length of
// each, in order. The boundaries are the ones Xet stores the same bytes under.
func Cut(r io.Reader, emit func(n int)) error {
	buf := make([]byte, 1<<20)
	var h uint64
	size := 0
	for {
		n, err := r.Read(buf)
		for _, b := range buf[:n] {
			h = h<<1 + gearTable[b]
			size++
			if size >= minChunk && (size >= maxChunk || h&boundaryMask == 0) {
				emit(size)
				size, h = 0, 0
			}
		}
		if err == io.EOF {
			if size > 0 {
				emit(size)
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
}
