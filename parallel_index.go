package simdjson

// A parallel stage one, for whole documents past parallelMinBytes.
//
// The measured ceiling (parallel_ceiling_test.go) is 10.9x at sixteen threads
// on 64 MB, and the profile of serial Scan at that size says why a masks-only
// split would waste it: the Go word loop -- escape math, prefix XOR, and the
// bracket walk -- is about two thirds of the time, the vector kernels less
// than a fifth. So the brackets are paired in parallel too, per segment, with
// a serial merge for the pairs that cross.
//
// THE CONTRACT IS BIT-IDENTITY WITH THE SERIAL PATH: same pos, same match,
// same inStr, same wsCount and noWS, and on bad input the same error the
// serial loop would have produced, chosen by the serial loop's own ordering.
// TestParallelIndexMatchesSerial holds it to that.
//
// Three cross-segment dependencies exist, and two are removed by construction:
//
//   - The escape carry (a backslash run straddling a boundary) and the escape
//     TARGET carry (a backslash at the last bit aiming at the next word):
//     segment boundaries are advanced, in whole windows, until the byte before
//     the boundary is not a backslash. Both carries are then zero at every
//     boundary. Real JSON hits a backslash before a 64 KiB-aligned boundary
//     about once per couple of hundred boundaries, so the snap almost never
//     moves anything; input that defeats it entirely falls back to the serial
//     path.
//
//   - The in-string parity is one bit per segment and cannot be removed, so it
//     is computed: phase one runs the kernels and the escape arithmetic per
//     segment in parallel and reports each segment's quote parity and its
//     bracket count under a carry-in of zero; a serial prefix XOR then fixes
//     every segment's carry-in. Flipping the carry inverts inStr for the whole
//     segment exactly, so the count under a carry-in of one is
//     totalStructural - countAtZero -- no second pass needed to size the
//     output.
//
// Phase two re-runs the full window loop per segment with the known carry-in,
// writing pos, match and inStr at global offsets. Brackets pair against a
// local stack; a close with no local open is exported rather than an error,
// and phase three walks the segments in order pairing exports against the
// opens the earlier segments left -- the standard parallel-parentheses merge,
// O(what actually crosses).
//
// Phase one repeats the kernel and escape work that phase two does. That is
// deliberate: caching the masks instead would mean writing and re-reading five
// full-length arrays -- the exact memory traffic the windowed serial path
// exists to avoid. The duplicated work is under half of one pass, so the
// ceiling drops from P to about P/1.4, and the measured result is what counts.

import (
	"encoding/binary"
	"math/bits"
	"runtime"
	"sync"

	"github.com/sebishogun/simd"
)

// Tunables, as variables so the differential test can force many small
// segments onto small documents. Shipped values: a segment per 4 MB, at most
// sixteen workers, nothing parallel under 8 MB. The ceiling measurements put
// the turnover at sixteen threads and showed 1-2 MB documents gaining little;
// under the minimum the serial path runs unchanged.
var (
	parallelMinBytes = 8 << 20
	parallelSegBytes = 4 << 20
	parallelMaxProcs = 16
)

// segErr orders an error the way the serial loop would have found it: by
// window, then by pass within the window -- the serial code finishes a
// window's string/escape pass before its bracket pass, so a control character
// late in a window outranks a bracket mismatch early in the same window --
// then by byte position within the pass.
type segErr struct {
	win  int32
	pass int8
	pos  int32
	err  error
}

func (e *segErr) before(o *segErr) bool {
	if e.win != o.win {
		return e.win < o.win
	}
	if e.pass != o.pass {
		return e.pass < o.pass
	}
	return e.pos < o.pos
}

// segSummary is what phase one learns about a segment.
type segSummary struct {
	parity  uint64 // quote parity: ^0 if the segment flips in-string state
	count0  int    // brackets outside strings, under carry-in 0
	totalSt int    // all structural bits, either parity
}

// segResult is what phase two produces for a segment.
type segResult struct {
	err *segErr
	// opens is the residual local stack: brackets opened here and not closed
	// here, as global pos-index<<1|kind, bottom first.
	opens []int64
	// closes are brackets closed here with no local open, in order.
	closes []segClose
	// prevLeadOut and wsCount/anyWS feed the post-loop checks.
	prevLeadOut uint64
	wsCount     int
	anyWS       uint64
}

type segClose struct {
	k      int32 // global pos index
	pos    int32 // byte position
	win    int32 // window index within the segment, for error ranking
	square bool  // ']' rather than '}'
}

// parallelSegments picks the boundaries, or reports that it cannot.
func parallelSegments(data []byte) []int {
	workers := runtime.GOMAXPROCS(0)
	if workers > parallelMaxProcs {
		workers = parallelMaxProcs
	}
	if byLen := len(data) / parallelSegBytes; byLen < workers {
		workers = byLen
	}
	if workers < 2 {
		return nil
	}
	win := chunkBytes
	segLen := (len(data)/workers/win + 1) * win
	bounds := make([]int, 0, workers+1)
	bounds = append(bounds, 0)
	for b := segLen; b < len(data); b += segLen {
		// Snap forward in whole windows until the byte before the boundary is
		// not a backslash, so the escape carries are zero by construction.
		snapped := b
		for snapped < len(data) && data[snapped-1] == '\\' {
			snapped += win
		}
		// A boundary the snap pushed past the end is dropped, not fatal: the
		// next nominal boundary may land after the backslash run and still
		// split the document. Only when NO interior boundary survives does the
		// whole path decline.
		if snapped >= len(data) {
			continue
		}
		if snapped > bounds[len(bounds)-1] {
			bounds = append(bounds, snapped)
		}
	}
	if len(bounds) < 2 {
		return nil
	}
	return append(bounds, len(data))
}

// buildIndexParallel indexes data across segments. ok reports whether it ran;
// when false the caller uses the serial path and nothing has been touched.
func buildIndexParallel(data []byte, ix *index, validate bool) (*index, error, bool) {
	bounds := parallelSegments(data)
	if bounds == nil {
		return nil, nil, false
	}
	nseg := len(bounds) - 1
	win := chunkBytes
	nw := (len(data) + 63) / 64

	// Phase one: per-segment parity and counts, in parallel.
	sums := make([]segSummary, nseg)
	var wg sync.WaitGroup
	for s := 0; s < nseg; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			sums[s] = summarizeSegment(data[bounds[s]:bounds[s+1]], win)
		}(s)
	}
	wg.Wait()

	// The serial stitch: carry-in parity and pos base per segment.
	carry := make([]uint64, nseg)
	kbase := make([]int, nseg)
	var par uint64
	total := 0
	for s := 0; s < nseg; s++ {
		carry[s] = par
		kbase[s] = total
		c := sums[s].count0
		if par != 0 {
			c = sums[s].totalSt - c
		}
		total += c
		par ^= sums[s].parity
	}

	// Output storage, sized exactly. inStr keeps its one-word sentinel.
	if cap(ix.inStr) < nw+1 {
		ix.inStr = make([]uint64, nw+1)
	}
	ix.inStr = ix.inStr[:nw+1]
	ix.inStr[nw] = 0
	if validate {
		if cap(ix.wsw) < nw {
			ix.wsw = make([]uint64, nw)
		}
		ix.wsw = ix.wsw[:nw]
	} else {
		ix.wsw = nil
	}
	if cap(ix.pos) < total || cap(ix.match) < total {
		ix.pos = make([]int32, total)
		ix.match = make([]int32, total)
	}
	pos, match := ix.pos[:total], ix.match[:total]

	// Phase two: the full window loop per segment, in parallel.
	res := make([]segResult, nseg)
	for s := 0; s < nseg; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			res[s] = fillSegment(data, bounds[s], bounds[s+1], win, carry[s],
				kbase[s], pos, match, ix.inStr, ix.wsw, validate)
		}(s)
	}
	wg.Wait()

	// Phase three: merge, in segment order, with the serial error ordering.
	var stack []int64
	if cap(ix.stack) > 0 {
		stack = ix.stack[:0]
	}
	wsCount := 0
	var anyWS, prevLead uint64
	for s := 0; s < nseg; s++ {
		r := &res[s]
		// Resolve this segment's exported closes against the opens the
		// earlier segments left, collecting the first cross error by rank.
		var crossErr *segErr
		for i := range r.closes {
			c := &r.closes[i]
			if len(stack) == 0 {
				crossErr = &segErr{win: c.win, pass: 1, pos: c.pos,
					err: errAt("unbalanced brackets", int(c.pos))}
				break
			}
			o := stack[len(stack)-1]
			if (o&1 == 1) != c.square {
				crossErr = &segErr{win: c.win, pass: 1, pos: c.pos,
					err: errAt("mismatched brackets", int(c.pos))}
				break
			}
			stack = stack[:len(stack)-1]
			oi := o >> 1
			match[oi] = c.k
			match[c.k] = int32(oi)
		}
		// The segment's own first error and the cross error compete under the
		// serial ordering; whichever the serial loop would have hit first
		// wins.
		if r.err != nil && (crossErr == nil || r.err.before(crossErr)) {
			return nil, r.err.err, true
		}
		if crossErr != nil {
			return nil, crossErr.err, true
		}
		stack = append(stack, r.opens...)
		wsCount += r.wsCount
		anyWS |= r.anyWS
		if s == nseg-1 {
			prevLead = r.prevLeadOut
		}
	}
	// The post-loop checks, in the serial order.
	if par != 0 {
		return nil, errAt("unterminated string", len(data)-1), true
	}
	if validate {
		if prevLead != 0 {
			return nil, errSyntax("string ends in a backslash"), true
		}
		ix.noWS = anyWS == 0
		ix.wsCount = wsCount
	}
	if len(stack) != 0 {
		return nil, errAt("unterminated container", len(data)-1), true
	}
	ix.pos, ix.match, ix.stack = pos, match, stack
	return ix, nil, true
}

// summarizeSegment is phase one: kernels and escape arithmetic only, reporting
// the segment's quote parity and bracket counts. Carries start at zero, which
// the boundary snap guarantees is right.
func summarizeSegment(seg []byte, win int) segSummary {
	quote := make([]byte, win/8)
	esc := make([]byte, win/8)
	structural := make([]byte, win/8)
	var sum segSummary
	var prevEsc, strCarry uint64
	for base := 0; base < len(seg); base += win {
		end := base + win
		if end > len(seg) {
			end = len(seg)
		}
		chunk := seg[base:end]
		cnw := (len(chunk) + 63) / 64
		simd.MaskBits(quote, chunk, '"')
		simd.MaskBits(esc, chunk, '\\')
		simd.MaskBitsAny(structural, chunk, structSet)
		clear(quote[simd.MaskLen(len(chunk)) : cnw*8])
		clear(esc[simd.MaskLen(len(chunk)) : cnw*8])
		clear(structural[simd.MaskLen(len(chunk)) : cnw*8])
		for w := 0; w < cnw; w++ {
			off := w * 8
			bs := binary.LittleEndian.Uint64(esc[off:])
			escaped := escapedMask(bs, &prevEsc)
			q := binary.LittleEndian.Uint64(quote[off:]) &^ escaped
			x := q
			x ^= x << 1
			x ^= x << 2
			x ^= x << 4
			x ^= x << 8
			x ^= x << 16
			x ^= x << 32
			in := x ^ strCarry
			strCarry = uint64(int64(in) >> 63)
			st := binary.LittleEndian.Uint64(structural[off:])
			sum.totalSt += bits.OnesCount64(st)
			sum.count0 += bits.OnesCount64(st &^ in)
		}
	}
	sum.parity = strCarry
	return sum
}

// fillSegment is phase two: the serial window loop, verbatim in structure, for
// one segment with a known string carry-in. It writes inStr, wsw, pos and
// match at global offsets and never returns early -- the first error is
// recorded with its serial rank and processing stops, so the merge can decide
// which segment's error the serial loop would have hit first.
//
// It mirrors buildIndexWindowed line for line where they overlap, and the
// differential test is what keeps the two from drifting.
func fillSegment(data []byte, lo, hi, win int, strCarry uint64,
	kBase int, pos, match []int32, inStr []uint64, wsw []uint64,
	validate bool) segResult {

	quote := make([]byte, win/8)
	esc := make([]byte, win/8)
	structural := make([]byte, win/8)
	var ctl, ws []byte
	if validate {
		ctl = make([]byte, win/8)
		ws = make([]byte, win/8)
	}
	var r segResult
	var prevEsc, prevLead uint64
	k := kBase
	winIdx := int32(-1)
	for base := lo; base < hi; base += win {
		winIdx++
		end := base + win
		if end > hi {
			end = hi
		}
		chunk := data[base:end]
		cnw := (len(chunk) + 63) / 64
		simd.MaskBits(quote, chunk, '"')
		simd.MaskBits(esc, chunk, '\\')
		simd.MaskBitsAny(structural, chunk, structSet)
		if validate {
			simd.MaskBitsLess(ctl, chunk, 0x20)
			simd.MaskBitsAny(ws, chunk, wsSet)
		}
		clear(quote[simd.MaskLen(len(chunk)) : cnw*8])
		clear(esc[simd.MaskLen(len(chunk)) : cnw*8])
		clear(structural[simd.MaskLen(len(chunk)) : cnw*8])
		if validate {
			clear(ctl[simd.MaskLen(len(chunk)) : cnw*8])
			clear(ws[simd.MaskLen(len(chunk)) : cnw*8])
		}
		wbase := base / 64

		for w := 0; w < cnw; w++ {
			off := w * 8
			bs := binary.LittleEndian.Uint64(esc[off:])
			escaped := escapedMask(bs, &prevEsc)
			q := binary.LittleEndian.Uint64(quote[off:]) &^ escaped
			x := q
			x ^= x << 1
			x ^= x << 2
			x ^= x << 4
			x ^= x << 8
			x ^= x << 16
			x ^= x << 32
			in := x ^ strCarry
			strCarry = uint64(int64(in) >> 63)
			inStr[wbase+w] = in

			if validate {
				if binary.LittleEndian.Uint64(ctl[off:])&in != 0 {
					r.err = &segErr{win: winIdx, pass: 0, pos: int32(base + w*64),
						err: errSyntax("control character in string")}
					return r
				}
				wsword := binary.LittleEndian.Uint64(ws[off:])
				wsw[wbase+w] = wsword
				outWS := wsword &^ in
				r.anyWS |= outWS
				r.wsCount += bits.OnesCount64(outWS)

				leaders := bs & in &^ escaped
				target := leaders<<1 | prevLead
				prevLead = leaders >> 63
				for t := target; t != 0; t &= t - 1 {
					pp := base + w*64 + bits.TrailingZeros64(t)
					if pp >= len(data) {
						r.err = &segErr{win: winIdx, pass: 0, pos: int32(pp),
							err: errSyntax("string ends in a backslash")}
						return r
					}
					switch data[pp] {
					case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
					case 'u':
						if pp+4 >= len(data) {
							r.err = &segErr{win: winIdx, pass: 0, pos: int32(pp),
								err: errSyntax("short \\u escape")}
							return r
						}
						for j := 1; j <= 4; j++ {
							if !isHex(data[pp+j]) {
								r.err = &segErr{win: winIdx, pass: 0, pos: int32(pp),
									err: errSyntax("invalid \\u escape")}
								return r
							}
						}
					default:
						r.err = &segErr{win: winIdx, pass: 0, pos: int32(pp),
							err: errSyntax("invalid escape")}
						return r
					}
				}
			}
		}

		for w := 0; w < cnw; w++ {
			st := binary.LittleEndian.Uint64(structural[w*8:]) &^ inStr[wbase+w]
			bpos := int32(base + w*64)
			for st != 0 {
				p := bpos + int32(bits.TrailingZeros64(st))
				pos[k] = p
				switch data[p] {
				case '{':
					r.opens = append(r.opens, int64(k)<<1)
				case '[':
					r.opens = append(r.opens, int64(k)<<1|1)
				case '}', ']':
					if len(r.opens) == 0 {
						r.closes = append(r.closes, segClose{
							k: int32(k), pos: p, win: winIdx, square: data[p] == ']'})
					} else {
						o := r.opens[len(r.opens)-1]
						if (o&1 == 1) != (data[p] == ']') {
							r.err = &segErr{win: winIdx, pass: 1, pos: p,
								err: errAt("mismatched brackets", int(p))}
							return r
						}
						r.opens = r.opens[:len(r.opens)-1]
						oi := o >> 1
						match[oi] = int32(k)
						match[k] = int32(oi)
					}
				}
				k++
				st &= st - 1
			}
		}
	}
	r.prevLeadOut = prevLead
	return r
}
