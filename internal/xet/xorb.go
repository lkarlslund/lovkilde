package xet

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/pierrec/lz4/v4"
)

// The compression schemes a chunk in a xorb is stored under.
const (
	schemeNone = 0
	schemeLZ4  = 1
	// schemeBG4LZ4 is LZ4 over the chunk's bytes regrouped by their position modulo 4.
	schemeBG4LZ4 = 2
)

// readChunk reads one serialized chunk from a xorb — an 8-byte header and its stored bytes — and
// returns its data.
func readChunk(r io.Reader) ([]byte, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	if hdr[0] != 0 {
		return nil, fmt.Errorf("xorb chunk version %d, not 0", hdr[0])
	}
	stored := int(hdr[1]) | int(hdr[2])<<8 | int(hdr[3])<<16
	size := int(hdr[5]) | int(hdr[6])<<8 | int(hdr[7])<<16
	if size > maxChunk || stored > 2*maxChunk {
		return nil, fmt.Errorf("a xorb chunk of %d bytes stored in %d: larger than a chunk can be", size, stored)
	}
	data := make([]byte, stored)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, unexpected(err)
	}
	var out []byte
	switch hdr[4] {
	case schemeNone:
		out = data
	case schemeLZ4, schemeBG4LZ4:
		out = make([]byte, size)
		zr := lz4.NewReader(bytes.NewReader(data))
		if _, err := io.ReadFull(zr, out); err != nil {
			return nil, fmt.Errorf("a xorb chunk's LZ4 frame: %w", unexpected(err))
		}
		if n, _ := zr.Read(make([]byte, 1)); n != 0 {
			return nil, fmt.Errorf("a xorb chunk's LZ4 frame holds more than its %d bytes", size)
		}
		if hdr[4] == schemeBG4LZ4 {
			out = regroup(out)
		}
	default:
		return nil, fmt.Errorf("xorb chunk compression scheme %d is not known", hdr[4])
	}
	if len(out) != size {
		return nil, fmt.Errorf("a xorb chunk holds %d bytes, its header says %d", len(out), size)
	}
	return out, nil
}

// regroup undoes byte grouping: g holds the bytes at positions 0, 4, 8, …, then those at 1, 5,
// 9, …, and so on; the groups of a length not divisible by 4 are one longer from the first.
func regroup(g []byte) []byte {
	n := len(g)
	split, rem := n/4, n%4
	var start [4]int
	for k := 1; k < 4; k++ {
		start[k] = start[k-1] + split
		if k-1 < rem {
			start[k]++
		}
	}
	out := make([]byte, n)
	for j := range out {
		out[j] = g[start[j%4]+j/4]
	}
	return out
}

func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
