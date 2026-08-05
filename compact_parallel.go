package simdjson

// Parallel Compact for large documents: the strip-copy was the last serial
// pass in it -- the index and the validation already ride the parallel
// machinery -- and it has the family's classic two-phase shape. Phase one
// counts each segment's output bytes straight off the masks the index
// already built (a kept byte is one the whitespace mask does not claim or the
// in-string mask does), a prefix sum places every segment, and phase two runs
// the ordinary word-run copier per segment into its slot. Byte-identity with
// the serial copier is by construction -- same masks, same runs, same bytes --
// and the differential holds it anyway.
//
// The output lands in dst.AvailableBuffer, filled in parallel and then
// dst.Write'n, which for a buffer writing its own spare capacity is the
// documented in-place append.

import (
	"bytes"
	"math/bits"
	"runtime"
	"sync"
)

// compactParallel writes the compact form of src into dst using nseg
// segments, or reports that one worker is enough.
func compactParallel(dst *bytes.Buffer, src []byte, ix *index) bool {
	if len(src) < parallelMinBytes {
		return false
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > parallelMaxProcs {
		workers = parallelMaxProcs
	}
	if workers < 2 {
		return false
	}
	nw := (len(src) + 63) / 64
	wordsPer := (nw + workers - 1) / workers
	if wordsPer == 0 {
		return false
	}

	// Phase one: output bytes per segment, from the masks alone.
	type seg struct{ loW, hiW, outOff, outLen int }
	segs := make([]seg, 0, workers)
	for lo := 0; lo < nw; lo += wordsPer {
		hi := min(lo+wordsPer, nw)
		segs = append(segs, seg{loW: lo, hiW: hi})
	}
	var wg sync.WaitGroup
	for i := range segs {
		wg.Add(1)
		go func(s *seg) {
			defer wg.Done()
			n := 0
			for w := s.loW; w < s.hiW; w++ {
				keep := ^(ix.wsw[w] &^ ix.inStr[w])
				if rem := len(src) - w*64; rem < 64 {
					keep &= 1<<uint(rem) - 1
				}
				n += bits.OnesCount64(keep)
			}
			s.outLen = n
		}(&segs[i])
	}
	wg.Wait()
	total := 0
	for i := range segs {
		segs[i].outOff = total
		total += segs[i].outLen
	}

	dst.Grow(total)
	out := dst.AvailableBuffer()[:total]

	// Phase two: the serial copier's word-run loop, per segment, into its
	// slot.
	for i := range segs {
		wg.Add(1)
		go func(s *seg) {
			defer wg.Done()
			o := s.outOff
			for w := s.loW; w < s.hiW; w++ {
				base := w * 64
				n := len(src) - base
				if n > 64 {
					n = 64
				}
				keep := ^(ix.wsw[w] &^ ix.inStr[w])
				if n < 64 {
					keep &= 1<<uint(n) - 1
				}
				if keep == 1<<uint(n)-1 || (n == 64 && keep == ^uint64(0)) {
					o += copy(out[o:], src[base:base+n])
					continue
				}
				for keep != 0 {
					t := bits.TrailingZeros64(keep)
					l := bits.TrailingZeros64(^(keep >> uint(t)))
					if t+l >= n {
						o += copy(out[o:], src[base+t:base+n])
						break
					}
					o += copy(out[o:], src[base+t:base+t+l])
					keep &^= (1<<uint(l) - 1) << uint(t)
				}
			}
		}(&segs[i])
	}
	wg.Wait()
	dst.Write(out)
	return true
}
