package simdjson

// The contract under test: for every grammar-valid JSON number,
// parseFloat64Fast either declines or returns the bit-exact
// strconv.ParseFloat answer. A decline is never wrong; an accept must be
// identical down to the sign of zero.

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

// checkFloatAgainstStrconv is the single oracle both the test and the fuzzer
// use.
func checkFloatAgainstStrconv(t interface {
	Helper()
	Fatalf(string, ...any)
}, s string) {
	t.Helper()
	f, ok := parseFloat64Fast([]byte(s))
	if !ok {
		return // declined; strconv owns it
	}
	want, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("%q: fast accepted what strconv errors on (%v)", s, err)
	}
	if math.Float64bits(f) != math.Float64bits(want) {
		t.Fatalf("%q: fast %v (%016x) != strconv %v (%016x)",
			s, f, math.Float64bits(f), want, math.Float64bits(want))
	}
}

func TestParseFloatFastAgainstStrconv(t *testing.T) {
	fixed := []string{
		"0", "-0", "0.0", "-0.0e5", "0e999", "-0E-999",
		"1", "-1", "12", "123456789", "9007199254740992", "9007199254740993",
		"1e0", "1e1", "1e-1", "1e22", "1e-22", "1e23", "1e-23",
		"1.5", "-2.25", "3.141592653589793", "2.718281828459045",
		"-65.613616999999977", "43.420273000000009", // canada's shape
		"1e308", "-1e308", "1.7976931348623157e308", "1e-308",
		"2.2250738585072014e-308",                // smallest normal
		"1e-309", "5e-324", "4.9e-324", "1e-323", // subnormals: must decline or match
		"1e309", "-1e309", "1e999", "1e-999", // over/underflow: strconv errors or rounds
		"0.1", "0.2", "0.3", "0.000123", "0.00000000000000000001234",
		"123456789012345678901234567890", "1.2345678901234567890123e10",
		"7.2057594037927933e16", "7.2057594037927933e+16",
		"5e-20", "67e14", "985e15", "55895e-16", // Lemire paper edge family
		"8.988465674311579e307", "4.4501477170144023e-308",
		"2.446494580089078e-296", "1.00000000000000011102230246251565404236316680908203125",
	}
	for _, s := range fixed {
		if !validJSONNumber([]byte(s)) {
			continue
		}
		checkFloatAgainstStrconv(t, s)
	}

	// Grammar-driven random sweep: mantissa lengths 1..25 with a dot at every
	// position, exponents -350..350.
	rng := rand.New(rand.NewSource(47))
	digits := func(n int) string {
		var b strings.Builder
		b.WriteByte('1' + byte(rng.Intn(9)))
		for i := 1; i < n; i++ {
			b.WriteByte('0' + byte(rng.Intn(10)))
		}
		return b.String()
	}
	for round := 0; round < 200000; round++ {
		n := 1 + rng.Intn(25)
		d := digits(n)
		var b strings.Builder
		if rng.Intn(2) == 1 {
			b.WriteByte('-')
		}
		if dot := rng.Intn(n + 1); dot > 0 && dot < n {
			b.WriteString(d[:dot])
			b.WriteByte('.')
			b.WriteString(d[dot:])
		} else {
			b.WriteString(d)
		}
		if rng.Intn(2) == 1 {
			fmt.Fprintf(&b, "e%+d", rng.Intn(701)-350)
		}
		s := b.String()
		if !validJSONNumber([]byte(s)) {
			t.Fatalf("generator produced invalid number %q", s)
		}
		checkFloatAgainstStrconv(t, s)
	}

	// Bit-pattern round trips: every float64 printed by strconv must come
	// back identical.
	for round := 0; round < 100000; round++ {
		bits := rng.Uint64()
		f := math.Float64frombits(bits)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		s := strconv.FormatFloat(f, 'g', -1, 64)
		s = strings.TrimPrefix(s, "+")
		if !validJSONNumber([]byte(s)) {
			continue // e.g. "1e+308" is valid JSON; skip anything that isn't
		}
		checkFloatAgainstStrconv(t, s)
	}
}

// TestParseFloatFastCoverage pins that the fast path actually takes the
// corpus shapes it was built for — a silent 100% fallback would pass every
// differential while buying nothing.
func TestParseFloatFastCoverage(t *testing.T) {
	for _, s := range []string{"-65.613616999999977", "1.5", "3.141592653589793", "12", "0.000123"} {
		if _, ok := parseFloat64Fast([]byte(s)); !ok {
			t.Fatalf("%q: declined; the corpus shape must take the fast path", s)
		}
	}
}

func FuzzParseFloatFast(f *testing.F) {
	f.Add("-65.613616999999977")
	f.Add("9007199254740993")
	f.Add("5e-324")
	f.Add("1.7976931348623157e308")
	f.Add("0.00000000000000000001234")
	f.Fuzz(func(t *testing.T, s string) {
		if !validJSONNumber([]byte(s)) {
			return
		}
		checkFloatAgainstStrconv(t, s)
	})
}
