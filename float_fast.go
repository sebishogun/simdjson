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
	"encoding/binary"
	"math"
	"math/bits"
)

// pow10Exact holds the powers of ten a float64 represents exactly. Products
// and quotients against these round once, which is Clinger's fast path.
var pow10Exact = [23]float64{
	1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10, 1e11,
	1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22,
}

// Float-parse statuses from parseFloat64At.
const (
	floatBadSyntax = 0 // not a number here; the caller reports its kind error
	floatParsed    = 1 // f is the bit-exact answer, end is one past the number
	floatFallback  = 2 // grammar-valid to end, but strconv owns the rounding
)

// parseFloat64At parses AND validates the JSON number at data[pos] in one
// pass, replacing the number() walk that used to run first — the profile had
// the two passes as 140ms and 40ms of a 380ms decode. The grammar is
// number()'s, reject for reject: -? (0|[1-9][0-9]*) (.[0-9]+)?
// ([eE][+-]?[0-9]+)?, trailing junk left for the caller's separator check,
// and a leading zero admits no more integer digits. The fuzz holds the two
// walks to identical accept/reject/end on arbitrary bytes.
//
// The fraction — the long digit run in real data — moves eight digits per
// step: an unaligned load, one is-all-digits test, and the three-multiply
// fold, exactly parse_eight_digits_unrolled from the Lemire paper.
//
// The cursor is a uint for the same bounds-check-elimination reason as
// number(); see the comment there and wrong.md entry 13.
func parseFloat64At(data []byte, pos int) (f float64, end int, status int) {
	b := data
	n := uint(len(b))
	j := uint(pos)
	neg := false
	if j < n && b[j] == '-' {
		neg = true
		j++
	}
	if j >= n {
		return 0, 0, floatBadSyntax
	}
	var man uint64
	nd, exp10 := 0, 0
	trunc := false
	switch {
	case b[j] == '0':
		j++
	case b[j] >= '1' && b[j] <= '9':
		man = uint64(b[j] - '0')
		nd = 1
		j++
		for j < n && b[j] >= '0' && b[j] <= '9' {
			if nd < 19 {
				man = man*10 + uint64(b[j]-'0')
				nd++
			} else {
				exp10++
				trunc = true
			}
			j++
		}
	default:
		return 0, 0, floatBadSyntax
	}
	if j < n && b[j] == '.' {
		j++
		if j >= n || b[j] < '0' || b[j] > '9' {
			return 0, 0, floatBadSyntax
		}
		if man == 0 {
			// Leading fraction zeros scale the exponent and cost no digit
			// budget: 0.000123 is 123e-6 with three significant digits.
			for j < n && b[j] == '0' {
				exp10--
				j++
			}
		}
		for j+8 <= n && nd+8 <= 19 {
			v := binary.LittleEndian.Uint64(b[j:])
			t := v - 0x3030303030303030
			if (t|(t+0x7676767676767676))&0x8080808080808080 != 0 {
				break
			}
			t = (t * 2561) >> 8 & 0x00FF00FF00FF00FF
			t = (t * 6553601) >> 16 & 0x0000FFFF0000FFFF
			t = (t * 42949672960001) >> 32
			man = man*100000000 + t
			nd += 8
			exp10 -= 8
			j += 8
		}
		for j < n && b[j] >= '0' && b[j] <= '9' {
			if nd < 19 {
				if man == 0 && b[j] == '0' {
					exp10--
				} else {
					man = man*10 + uint64(b[j]-'0')
					nd++
					exp10--
				}
			} else {
				trunc = true
			}
			j++
		}
	}
	if j < n && (b[j] == 'e' || b[j] == 'E') {
		j++
		esign := 1
		if j < n && (b[j] == '+' || b[j] == '-') {
			if b[j] == '-' {
				esign = -1
			}
			j++
		}
		if j >= n || b[j] < '0' || b[j] > '9' {
			return 0, 0, floatBadSyntax
		}
		e := 0
		for j < n && b[j] >= '0' && b[j] <= '9' {
			if e < 10000 {
				e = e*10 + int(b[j]-'0')
			}
			j++
		}
		exp10 += esign * e
	}
	end = int(j)

	if man == 0 {
		// All digits were zeros. The exponent cannot rescue a zero, and the
		// sign survives: "-0.0e5" is negative zero, as strconv has it.
		if neg {
			return math.Copysign(0, -1), end, floatParsed
		}
		return 0, end, floatParsed
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
		return f, end, floatParsed
	}

	f1, ok1 := eiselLemire64(man, exp10, neg)
	if !ok1 {
		return 0, end, floatFallback
	}
	if trunc {
		// Dropped digits mean man is a truncation. If man and man+1 land on
		// the same float64 the dropped tail cannot matter; otherwise strconv
		// owns the long division.
		f2, ok2 := eiselLemire64(man+1, exp10, neg)
		if !ok2 || f1 != f2 {
			return 0, end, floatFallback
		}
	}
	return f1, end, floatParsed
}

// parseFloat64Fast parses a whole grammar-valid JSON number. ok=false means
// "use strconv" — never a wrong answer.
func parseFloat64Fast(s []byte) (float64, bool) {
	f, end, status := parseFloat64At(s, 0)
	return f, status == floatParsed && end == len(s)
}

// parseIntAt parses and validates an INTEGER JSON number at data[pos] in one
// pass — the walk decInt64 ran through number() and the re-scan it then paid
// strconv.ParseInt for, together. citm is int64s wall to wall, and every id
// in every API response is this shape. The grammar is number()'s, and any
// doubt is a fallback, never an answer: a '.', an 'e', more than eighteen
// digits, or a value outside the caller's bit range all return floatFallback
// and the old numAt+strconv path reproduces its exact behavior, errors
// included. Eighteen digits cannot overflow a uint64 accumulator, which is
// what keeps the fast loop free of checks.
func parseIntAt(data []byte, pos int, min, max int64) (n int64, end int, status int) {
	b := data
	nn := uint(len(b))
	j := uint(pos)
	neg := false
	if j < nn && b[j] == '-' {
		neg = true
		j++
	}
	if j >= nn {
		return 0, 0, floatBadSyntax
	}
	var man uint64
	switch {
	case b[j] == '0':
		j++
	case b[j] >= '1' && b[j] <= '9':
		man = uint64(b[j] - '0')
		j++
		start := j
		for j < nn && b[j] >= '0' && b[j] <= '9' {
			man = man*10 + uint64(b[j]-'0')
			j++
		}
		if j-start >= 18 {
			return 0, 0, floatFallback
		}
	default:
		return 0, 0, floatBadSyntax
	}
	if j < nn && (b[j] == '.' || b[j] == 'e' || b[j] == 'E') {
		// Float-shaped. The old path owns the error text.
		return 0, 0, floatFallback
	}
	n = int64(man)
	if neg {
		n = -n
	}
	if n < min || n > max {
		return 0, 0, floatFallback
	}
	return n, int(j), floatParsed
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
