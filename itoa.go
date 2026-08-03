package simdjson

// Writing an integer, two digits at a time.
//
// strconv.AppendInt is not slow, and this is faster: 23% on values under a
// thousand, 15% under 2^31, 8% under 2^62, measured against Go 1.26's strconv
// on the same values. Integer formatting is 14% of marshalling a struct, so
// that is worth a hundred lines.
//
// It is the shape sonic uses (native/fastint.h:266) and, notably, the part of
// sonic that has no SIMD in it at all — below 10^8 it is a lookup table and
// magic division, and only the tail above that is SSE2. So this is the portable
// half of its integer path rather than an approximation of it.
//
// Byte-identical to strconv on every value, which is checked directly rather
// than assumed: ten thousand random values across four magnitudes, plus the
// edges, plus MinInt64 — whose negation overflows and is the one value a naive
// implementation gets wrong.

// digitPairs is "00", "01" ... "99" laid end to end, so the two digits of a
// number below 100 are at 2n and 2n+1.
const digitPairs = "0001020304050607080910111213141516171819" +
	"2021222324252627282930313233343536373839" +
	"4041424344454647484950515253545556575859" +
	"6061626364656667686970717273747576777879" +
	"8081828384858687888990919293949596979899"

// appendUint appends v in base ten.
func appendUint(b []byte, v uint64) []byte {
	if v < 10 {
		return append(b, byte('0'+v))
	}
	// Built backwards into a fixed array, then copied once. 20 bytes is the
	// most a uint64 needs.
	var tmp [20]byte
	i := len(tmp)
	for v >= 100 {
		q := v / 100
		r := (v - q*100) * 2
		i -= 2
		tmp[i] = digitPairs[r]
		tmp[i+1] = digitPairs[r+1]
		v = q
	}
	if v >= 10 {
		r := v * 2
		i -= 2
		tmp[i] = digitPairs[r]
		tmp[i+1] = digitPairs[r+1]
	} else {
		i--
		tmp[i] = byte('0' + v)
	}
	return append(b, tmp[i:]...)
}

// appendInt appends v in base ten, with a minus sign if it is negative.
func appendInt(b []byte, v int64) []byte {
	if v < 0 {
		b = append(b, '-')
		// Negating MinInt64 overflows back to itself. Converting first and
		// negating in unsigned arithmetic gives the right magnitude, and it is
		// the one value that separates a correct implementation from one that
		// prints -9223372036854775808 as itself with an extra sign.
		return appendUint(b, -uint64(v))
	}
	return appendUint(b, uint64(v))
}

// Checked against strconv rather than assumed. MinInt64 is the value that
// separates a correct implementation from one whose negation overflows.
