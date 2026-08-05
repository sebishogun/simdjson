package simdjson

// Parallel Indent: the last text transform still serial at scale. The writer's
// whole cross-segment state is two values -- the nesting depth and the
// pending-newline flag an opened container carries until something turns up
// inside it -- and every newline's width is linear in depth. So phase one runs
// a counting twin of the writer per segment FOR BOTH POSSIBLE pending seeds
// (depth only scales widths; pending steers control flow, and a boundary can
// land mid-pending), a serial fold picks each segment's real seed and lays out
// exact offsets, and phase two runs the real writer per segment into its slot.
//
// Segments split src at arbitrary byte offsets: a boundary inside a string
// resumes through the in-string mask branch and writes the remainder verbatim;
// inside a number or literal, the carried pending=false makes the resumed run
// pass through untouched; inside whitespace, the skip loop just continues.
// Byte-identity with the serial writer is held by the differential across
// exactly those landings.

import (
	"bytes"
	"runtime"
	"sync"
)

// indentSeg is one segment's phase-one summary, per pending seed.
type indentSeg struct {
	lo, hi int
	// Per seed s (0: pending=false, 1: pending=true):
	constB  [2]int // bytes independent of entry depth
	depthNL [2]int // sum over newlines of the LOCAL depth at emission
	nNL     [2]int // newlines emitted
	dOut    [2]int // depth delta
	pOut    [2]bool
	// Resolved by the fold:
	seed   int
	off    int
	dIn    int
	outLen int
}

// indentCount is the counting twin of writeIndent's loop over src[lo:hi],
// from relative depth zero and the given pending seed.
func indentCount(src []byte, ix *index, lo, hi int, prefixLen, indentLen int, pending bool) (constB, depthNL, nNL, dOut int, pOut bool) {
	nlConst := 1 + prefixLen
	depth := 0
	nl := func() {
		constB += nlConst
		depthNL += depth
		nNL++
	}
	for i := lo; i < hi; {
		if ix.inStr[i>>6]&(1<<(uint(i)&63)) != 0 {
			if pending {
				nl()
				pending = false
			}
			j := stringRunEnd(ix, i, hi)
			constB += j - i
			i = j
			continue
		}
		c := src[i]
		i++
		switch c {
		case ' ', '\t', '\n', '\r':
			for i < hi && isJSONSpace[src[i]] {
				i++
			}
		case '{', '[':
			if pending {
				nl()
			}
			depth++
			pending = true
			constB++
		case '}', ']':
			depth--
			if pending {
				pending = false
			} else {
				nl()
			}
			constB++
		case ',':
			constB++
			nl()
		case ':':
			constB += 2
		default:
			if pending {
				nl()
				pending = false
			}
			j := i
			for j < hi && !indentBreak[src[j]] {
				j++
			}
			constB += j - (i - 1)
			i = j
		}
	}
	_ = indentLen
	return constB, depthNL, nNL, depth, pending
}

// indentWriteSeg is writeIndent's loop over src[lo:hi] writing into out at o,
// with absolute entry depth and pending carried in. It returns one past the
// last byte written.
func indentWriteSeg(out []byte, o int, src []byte, ix *index, lo, hi int,
	prefix, indent string, depth int, pending bool) int {
	pad := make([]byte, 0, 1+len(prefix)+16*len(indent))
	pad = append(append(pad, '\n'), prefix...)
	head := len(pad)
	newline := func(d int) {
		if len(indent) == 0 {
			o += copy(out[o:], pad[:head])
			return
		}
		for (len(pad)-head)/len(indent) < d {
			pad = append(pad, indent...)
		}
		o += copy(out[o:], pad[:head+d*len(indent)])
	}
	for i := lo; i < hi; {
		if ix.inStr[i>>6]&(1<<(uint(i)&63)) != 0 {
			if pending {
				newline(depth)
				pending = false
			}
			j := stringRunEnd(ix, i, hi)
			o += copy(out[o:], src[i:j])
			i = j
			continue
		}
		c := src[i]
		i++
		switch c {
		case ' ', '\t', '\n', '\r':
			for i < hi && isJSONSpace[src[i]] {
				i++
			}
		case '{', '[':
			if pending {
				newline(depth)
			}
			depth++
			pending = true
			out[o] = c
			o++
		case '}', ']':
			depth--
			if pending {
				pending = false
			} else {
				newline(depth)
			}
			out[o] = c
			o++
		case ',':
			out[o] = c
			o++
			newline(depth)
		case ':':
			out[o] = ':'
			out[o+1] = ' '
			o += 2
		default:
			if pending {
				newline(depth)
				pending = false
			}
			j := i
			for j < hi && !indentBreak[src[j]] {
				j++
			}
			o += copy(out[o:], src[i-1:j])
			i = j
		}
	}
	return o
}

// indentParallel lays out src[:end] across workers into dst, then copies the
// tail through as the serial writer does. It reports whether it ran.
func indentParallel(dst *bytes.Buffer, src []byte, ix *index, prefix, indent string, end int) bool {
	if end < parallelMinBytes {
		return false
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > parallelMaxProcs {
		workers = parallelMaxProcs
	}
	if workers < 2 {
		return false
	}
	segs := make([]indentSeg, 0, workers)
	per := (end + workers - 1) / workers
	for lo := 0; lo < end; lo += per {
		segs = append(segs, indentSeg{lo: lo, hi: min(lo+per, end)})
	}

	// Phase one: both pending seeds per segment, in parallel.
	var wg sync.WaitGroup
	for i := range segs {
		wg.Add(1)
		go func(sg *indentSeg) {
			defer wg.Done()
			for seed := 0; seed < 2; seed++ {
				c, dn, n, d, p := indentCount(src, ix, sg.lo, sg.hi,
					len(prefix), len(indent), seed == 1)
				sg.constB[seed], sg.depthNL[seed], sg.nNL[seed] = c, dn, n
				sg.dOut[seed], sg.pOut[seed] = d, p
			}
		}(&segs[i])
	}
	wg.Wait()

	// The fold: real seeds, entry depths, exact offsets.
	depth, pending, off := 0, false, 0
	for i := range segs {
		sg := &segs[i]
		seed := 0
		if pending {
			seed = 1
		}
		sg.seed, sg.dIn, sg.off = seed, depth, off
		sg.outLen = sg.constB[seed] + (sg.depthNL[seed]+depth*sg.nNL[seed])*len(indent)
		off += sg.outLen
		depth += sg.dOut[seed]
		pending = sg.pOut[seed]
	}
	total := off + (len(src) - end)

	dst.Grow(total)
	out := dst.AvailableBuffer()[:total]

	// Phase two: the real writer per segment into its slot.
	for i := range segs {
		wg.Add(1)
		go func(sg *indentSeg) {
			defer wg.Done()
			o := indentWriteSeg(out, sg.off, src, ix, sg.lo, sg.hi,
				prefix, indent, sg.dIn, sg.seed == 1)
			if o != sg.off+sg.outLen {
				// The count and the writer are twins; disagreement is a bug,
				// and writing on would corrupt a neighbour's slot.
				panic("simdjson: indent count/write mismatch")
			}
		}(&segs[i])
	}
	wg.Wait()
	copy(out[off:], src[end:])
	dst.Write(out)
	return true
}
