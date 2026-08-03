package simdjson

import (
	"math"
	"math/rand"
	"strconv"
	"testing"
)

// schubfachDigits renders a decimal as the shortest digit string plus exponent,
// which is what strconv's 'e' format with precision -1 also produces. Comparing
// there rather than after formatting isolates the algorithm from the layout.
func schubfachShortest(f float64) (string, int) {
	d := schubfach(math.Float64bits(f))
	s := string(appendUint(nil, d.sig))
	e := int(d.exp)
	// Schubfach returns the significand at a fixed scale, trailing zeros and
	// all -- 1.0 comes back as 10^16 with an exponent of -16, not as 1 with an
	// exponent of 0. Stripping them is the renderer's job, and sonic does it
	// with ctz10 (f64toa.c:48). Doing it here is what makes this comparable to
	// strconv's shortest form.
	for len(s) > 1 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
		e++
	}
	// strconv reports the exponent of the first digit; e is of the last.
	return s, e + len(s) - 1
}

func strconvShortest(f float64) (string, int) {
	s := strconv.FormatFloat(math.Abs(f), 'e', -1, 64)
	// "d.dddde±dd"
	mant, exp := s, 0
	for i := 0; i < len(s); i++ {
		if s[i] == 'e' {
			mant = s[:i]
			e, _ := strconv.Atoi(s[i+1:])
			exp = e
			break
		}
	}
	digits := ""
	for i := 0; i < len(mant); i++ {
		if mant[i] != '.' {
			digits += string(mant[i])
		}
	}
	// Strip trailing zeros, which strconv does not emit for shortest anyway.
	for len(digits) > 1 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
	}
	return digits, exp
}

func checkOne(t *testing.T, f float64) {
	t.Helper()
	if f == 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return
	}
	gs, ge := schubfachShortest(math.Abs(f))
	ws, we := strconvShortest(f)
	if gs != ws || ge != we {
		t.Errorf("%v (bits %#016x): schubfach %se%d, strconv %se%d",
			f, math.Float64bits(f), gs, ge, ws, we)
	}
}

func TestSchubfachAgainstStrconv(t *testing.T) {
	for _, f := range []float64{
		1, 2, 3, 10, 100, 1e10, 1e100, 1e-10, 1e-100,
		0.1, 0.2, 0.3, 1.5, 2.5, 1.25, 2.675,
		1.0 / 3.0, 2.0 / 3.0,
		math.MaxFloat64, math.SmallestNonzeroFloat64,
		math.Pi, math.E, math.Sqrt2,
		9007199254740992, 9007199254740993,
		1e15, 1e16, 1e17, 1e21, 1e22, 1e23,
		4.9406564584124654e-324, 2.2250738585072014e-308,
		2.2250738585072011e-308, // the classic strtod bug
		123456789012345678000,
	} {
		checkOne(t, f)
		checkOne(t, -f)
	}
}

func TestSchubfachRandom(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 200000; i++ {
		f := math.Float64frombits(r.Uint64())
		checkOne(t, f)
	}
}

// Values with few significant digits, which is what real JSON holds and where a
// shortest-representation bug is most likely to be visible.
func TestSchubfachRealistic(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 100000; i++ {
		switch i % 4 {
		case 0:
			checkOne(t, float64(r.Intn(1000000))/100)
		case 1:
			checkOne(t, float64(r.Intn(1000000))/1000)
		case 2:
			checkOne(t, r.NormFloat64()*float64(r.Intn(1000)+1))
		case 3:
			checkOne(t, float64(r.Intn(1<<30))*1.5)
		}
	}
}

// Every exponent, at the boundaries of each binade, where the rounding interval
// changes shape.
func TestSchubfachBinades(t *testing.T) {
	for e := 1; e < 2047; e++ {
		for _, sig := range []uint64{0, 1, 2, f64SigMask - 1, f64SigMask, 1 << 51} {
			checkOne(t, math.Float64frombits(uint64(e)<<52|sig))
		}
	}
	// Subnormals.
	for _, sig := range []uint64{1, 2, 3, 1 << 20, f64SigMask} {
		checkOne(t, math.Float64frombits(sig))
	}
}

func FuzzSchubfachAgainstStrconv(f *testing.F) {
	for _, v := range []uint64{
		math.Float64bits(1), math.Float64bits(0.1), math.Float64bits(1e100),
		math.Float64bits(math.MaxFloat64), 1,
	} {
		f.Add(v)
	}
	f.Fuzz(func(t *testing.T, bits uint64) {
		v := math.Float64frombits(bits)
		if v == 0 || math.IsInf(v, 0) || math.IsNaN(v) {
			return
		}
		gs, ge := schubfachShortest(math.Abs(v))
		ws, we := strconvShortest(v)
		if gs != ws || ge != we {
			t.Fatalf("%v (bits %#016x): schubfach %se%d, strconv %se%d", v, bits, gs, ge, ws, we)
		}
	})
}
