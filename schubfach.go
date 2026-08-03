package simdjson

// Shortest-representation float formatting, by the Schubfach algorithm.
//
// This is 31% of marshalling a struct full of numbers, and it is the last large
// item in the encoder. Go's strconv uses Ryu, which is correctly-rounded
// shortest and therefore produces exactly the same digits; the difference is
// only how fast they are produced. sonic uses Schubfach, compiled from C to
// per-ISA assembly (native/f64toa.c:288, citing Drachennest). Nothing about the
// algorithm needs SIMD — it is a table of powers of ten and a 128-bit multiply,
// which is math/bits.Mul64 — so the whole of it is reachable from portable Go.
//
// # What "shortest" means, and why it is not obvious
//
// A float64 is one of 2^64 values, and the decimal written for it must read
// back as that exact value and no other. Many decimals do: the float nearest
// 0.3 is read back correctly from "0.3", "0.30", "0.2999999999999999889…" and
// a great many others. The shortest is wanted, and where two are equally short
// the one nearest the true value.
//
// The set that reads back correctly is an interval — every decimal strictly
// between the midpoint below and the midpoint above, with the endpoints
// included or not depending on whether the significand is even. Schubfach finds
// the shortest decimal in that interval by scaling both endpoints by a power of
// ten and looking for a multiple of a power of ten between them.
//
// # Layout
//
//	decimal        the result: digits and a base-ten exponent
//	schubfach      the algorithm
//	pow10Sig       the table, 128 bits per entry
//
// The output is checked against strconv on every value the tests can reach,
// because "the same digits" is the entire contract: a float formatter that is
// faster and disagrees anywhere is not a float formatter.

import "math/bits"

// decimal is a value written as sig * 10^exp.
//
// sig keeps its trailing zeros: 1.0 comes back as 10^16 with an exponent of
// -16. Stripping them belongs to whatever renders the digits, which is where
// the decimal point has to be placed anyway.
type decimal struct {
	sig uint64
	exp int32
}

const (
	f64SigBits  = 52
	f64ExpBits  = 11
	f64ExpBias  = 1075 // 1023 + 52
	f64MaxExp   = 1 << f64ExpBits
	f64SigMask  = 1<<f64SigBits - 1
	f64HiddenIn = 1 << f64SigBits
)

// schubfach returns the shortest decimal that reads back as the float64 whose
// bits are given, for a finite non-zero value.
//
// Transcribed from the reference rather than derived: sonic ships Drachennest's
// C (native/f64toa.c:288), which is the Schubfach paper's algorithm, and the
// four places a from-memory version went wrong were all here. They are marked.
func schubfach(bitsIn uint64) decimal {
	rsig := bitsIn & f64SigMask
	rexp := int32((bitsIn >> f64SigBits) & (f64MaxExp - 1))

	var c uint64
	var q int32
	if rexp != 0 {
		c = rsig | f64HiddenIn
		q = rexp - f64ExpBias
	} else {
		c = rsig
		q = 1 - f64ExpBias
	}

	even := c&1 == 0
	// The lower boundary is closer when the significand is the smallest in its
	// binade and there is a binade below.
	irregular := rsig == 0 && rexp > 1

	cbl := 4*c - 2
	if irregular {
		cbl = 4*c - 1
	}
	cb := 4 * c
	cbr := 4*c + 2

	// The irregular term is conditional. Applying it always -- which reads as
	// "k is floor(log10(3/4 * 2^q))" -- is wrong for every regular value.
	k := q * 1262611
	if irregular {
		k -= 524031
	}
	k >>= 22

	h := q + ((-k * 1741647) >> 19) + 1
	phi, plo := pow10Sig(-k)
	vbl := roundOdd(phi, plo, cbl<<uint(h))
	vb := roundOdd(phi, plo, cb<<uint(h))
	vbr := roundOdd(phi, plo, cbr<<uint(h))

	lower := vbl
	upper := vbr
	if !even {
		lower++
		upper--
	}

	s := vb / 4
	if s >= 10 {
		// Everything here is in quarters, so the shorter candidate's
		// neighbours are 40*sp and 40*sp+40. Writing 10*sp instead -- forgetting
		// that the interval is scaled by four -- makes every value come out with
		// seventeen digits.
		sp := s / 10
		upInside := lower <= 40*sp
		wpInside := 40*sp+40 <= upper
		if upInside != wpInside {
			return decimal{sp + boolToU64(wpInside), k + 1}
		}
		// When both are inside, this scale cannot decide and the one below
		// does. Returning here is wrong.
	}

	uInside := lower <= 4*s
	wInside := 4*s+4 <= upper
	if uInside != wInside {
		return decimal{s + boolToU64(wInside), k}
	}

	// Both ends inside: take the nearer, ties to even. The midpoint is 4*s+2,
	// not 8*s+4.
	mid := 4*s + 2
	roundUp := vb > mid || (vb == mid && s&1 != 0)
	return decimal{s + boolToU64(roundUp), k}
}

func boolToU64(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// roundOdd multiplies the scaled significand by the 128-bit power of ten and
// keeps the high word, setting the low bit when anything was truncated.
//
// The condition is that the discarded word exceeded one, not that it existed.
// Setting the bit unconditionally makes every comparison below think the value
// is inexact and produces the maximum number of digits every time.
func roundOdd(ghi, glo, cp uint64) uint64 {
	xhi, _ := bits.Mul64(cp, glo)
	ylo, yhiCarry := bits.Add64(mulLo(cp, ghi), xhi, 0)
	yhi := mulHi(cp, ghi) + yhiCarry
	return yhi | boolToU64(ylo > 1)
}

func mulHi(a, b uint64) uint64 { h, _ := bits.Mul64(a, b); return h }
func mulLo(a, b uint64) uint64 { _, l := bits.Mul64(a, b); return l }

// appendShortest writes f the way encoding/json does, using the digits
// Schubfach produced.
//
// The format rule is encoding/json's and not strconv's 'g': decimal while the
// magnitude is in [1e-6, 1e21), scientific outside it, and an exponent written
// without a leading zero -- "1e+21", not "1e+21" from 'g' which would be
// "1e+21" anyway but "1e-07" where json wants "1e-07"... the one rewrite that
// matters is that json emits e-07 as e-07 and 'g' agrees, while the *choice* of
// format differs. See appendFloat, which owns the rule; this only renders.
//
// neg is passed separately because the sign is not in the digits.
func appendShortest(b []byte, neg bool, d decimal, expFormat bool) []byte {
	// Digits, trailing zeros removed. The significand comes back at a fixed
	// scale, so 1.0 arrives as 10^16 and every zero of it has to go.
	var tmp [24]byte
	digits := appendUint(tmp[:0], d.sig)
	exp := int(d.exp)
	for len(digits) > 1 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		exp++
	}
	if neg {
		b = append(b, '-')
	}
	// point is where the decimal point goes relative to the first digit: the
	// value is 0.digits * 10^point.
	point := exp + len(digits)

	if expFormat {
		b = append(b, digits[0])
		if len(digits) > 1 {
			b = append(b, '.')
			b = append(b, digits[1:]...)
		}
		b = append(b, 'e')
		e := point - 1
		if e < 0 {
			b = append(b, '-')
			e = -e
		} else {
			b = append(b, '+')
		}
		if e < 10 {
			// encoding/json writes a single-digit exponent with no padding:
			// 1e+21 rather than 1e+021. strconv's 'e' pads to two, which is why
			// the standard library rewrites it afterwards.
			return append(b, byte('0'+e))
		}
		return appendUint(b, uint64(e))
	}

	switch {
	case point <= 0:
		// 0.000ddd
		b = append(b, '0', '.')
		for i := 0; i < -point; i++ {
			b = append(b, '0')
		}
		return append(b, digits...)
	case point >= len(digits):
		// ddd000
		b = append(b, digits...)
		for i := len(digits); i < point; i++ {
			b = append(b, '0')
		}
		return b
	default:
		// dd.ddd
		b = append(b, digits[:point]...)
		b = append(b, '.')
		return append(b, digits[point:]...)
	}
}
