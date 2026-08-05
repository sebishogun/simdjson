package simdjson

// A float parser for bytes the grammar already delimited.
//
// strconv.ParseFloat was 46% of decoding canada.json into structs, and 330ms
// of its 440 was readFloat — re-scanning digits this package's number()
// already walked and validated. The bytes handed to decFloat64 are a whole,
// grammar-valid JSON number, so this parses them in one pass — mantissa,
// dot, exponent — and converts with the Eisel-Lemire algorithm (Lemire,
// "Number Parsing at a Gigabyte per Second", 2021; the same algorithm behind
// strconv's own fast path and fast_float).
//
// ok=false NEVER means a wrong answer; it means "use strconv". The fallbacks:
// more than 19 significant digits (with a man/man+1 agreement escape),
// exponents outside the table, results in the subnormal or overflow ranges
// (strconv's error contract lives there), and the algorithm's own rounding
// ambiguities. The differential fuzz holds every ok=true bit-identical to
// strconv.

import (
	"math"
	"math/bits"
)

// pow10Exact holds the powers of ten a float64 represents exactly. Products
// and quotients against these round once, which is Clinger's fast path.
var pow10Exact = [23]float64{
	1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10, 1e11,
	1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22,
}

// parseFloat64Fast parses a grammar-valid JSON number. The grammar is
// number()'s: -? (0|[1-9][0-9]*) (.[0-9]+)? ([eE][+-]?[0-9]+)? — no
// whitespace, no junk, at least one integer digit.
func parseFloat64Fast(s []byte) (f float64, ok bool) {
	i := 0
	neg := false
	if s[0] == '-' {
		neg = true
		i = 1
	}
	var man uint64
	nd, exp10 := 0, 0
	trunc := false
	for ; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			break
		}
		if nd < 19 {
			if man == 0 && c == '0' {
				continue // integer part is a lone 0; skip without spending budget
			}
			man = man*10 + uint64(c-'0')
			nd++
		} else {
			exp10++
			trunc = true
		}
	}
	if i < len(s) && s[i] == '.' {
		i++
		for ; i < len(s); i++ {
			c := s[i]
			if c < '0' || c > '9' {
				break
			}
			if nd < 19 {
				if man == 0 && c == '0' {
					exp10-- // leading fraction zero: scales, costs no budget
					continue
				}
				man = man*10 + uint64(c-'0')
				nd++
				exp10--
			} else {
				trunc = true
			}
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		esign := 1
		if s[i] == '+' {
			i++
		} else if s[i] == '-' {
			esign = -1
			i++
		}
		e := 0
		for ; i < len(s); i++ {
			if e < 10000 {
				e = e*10 + int(s[i]-'0')
			}
		}
		exp10 += esign * e
	}
	if man == 0 {
		// All digits were zeros. The exponent cannot rescue a zero, and the
		// sign survives: "-0.0e5" is negative zero, as strconv has it.
		if neg {
			return math.Copysign(0, -1), true
		}
		return 0, true
	}

	// Clinger: an exactly-representable mantissa times an exactly-
	// representable power of ten rounds once. This is most numbers people
	// write; canada's 17-digit coordinates skip it.
	if !trunc && man < 1<<53 && exp10 >= -22 && exp10 <= 22 {
		f = float64(man)
		if exp10 > 0 {
			f *= pow10Exact[exp10]
		} else if exp10 < 0 {
			f /= pow10Exact[-exp10]
		}
		if neg {
			f = -f
		}
		return f, true
	}

	f1, ok1 := eiselLemire64(man, exp10, neg)
	if !ok1 {
		return 0, false
	}
	if trunc {
		// Dropped digits mean man is a truncation. If man and man+1 land on
		// the same float64 the dropped tail cannot matter; otherwise strconv
		// owns the long division.
		f2, ok2 := eiselLemire64(man+1, exp10, neg)
		if !ok2 || f1 != f2 {
			return 0, false
		}
	}
	return f1, true
}

// eiselLemire64 converts man × 10^exp10 to the nearest float64 via one (or
// two) 64×64→128 multiplies against the normalized lemire10Tab. ok=false is
// "not provably correctly rounded here" — ambiguity, subnormal, overflow —
// and sends the caller to strconv.
func eiselLemire64(man uint64, exp10 int, neg bool) (float64, bool) {
	if exp10 < lemire10Min || exp10 > lemire10Max {
		return 0, false
	}
	clz := bits.LeadingZeros64(man)
	man <<= uint(clz)
	// floor(exp10 * log2(10)) == 217706*exp10 >> 16 over the table's range.
	retExp2 := uint64(217706*exp10>>16+64+1023) - uint64(clz)

	po := &lemire10Tab[exp10-lemire10Min]
	xHi, xLo := bits.Mul64(man, po[0])
	if xHi&0x1FF == 0x1FF && xLo+man < man {
		yHi, yLo := bits.Mul64(man, po[1])
		mergedHi, mergedLo := xHi, xLo+yHi
		if mergedLo < xLo {
			mergedHi++
		}
		if mergedHi&0x1FF == 0x1FF && mergedLo+1 == 0 && yLo+man < man {
			return 0, false
		}
		xHi, xLo = mergedHi, mergedLo
	}

	msb := xHi >> 63
	retMantissa := xHi >> (msb + 9)
	retExp2 -= 1 ^ msb

	// Half-way pattern: rounding direction not provable from the product.
	if xLo == 0 && xHi&0x1FF == 0 && retMantissa&3 == 1 {
		return 0, false
	}
	retMantissa += retMantissa & 1
	retMantissa >>= 1
	if retMantissa>>53 > 0 {
		retMantissa >>= 1
		retExp2 += 1
	}
	// Subnormal underflow and overflow carry strconv's error contract, so
	// both go back to strconv rather than being approximated here.
	if retExp2-1 >= 0x7FF-1 {
		return 0, false
	}
	retBits := retExp2<<52 | retMantissa&0x000FFFFFFFFFFFFF
	if neg {
		retBits |= 0x8000000000000000
	}
	return math.Float64frombits(retBits), true
}
