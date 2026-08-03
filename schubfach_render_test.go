package simdjson

import (
	stdjson "encoding/json"
	"math"
	"math/rand"
	"testing"
)

// renderShortest is what appendFloat would call: pick the format the way
// encoding/json does, then render.
func renderShortest(f float64) string {
	abs := math.Abs(f)
	expFormat := abs != 0 && (abs < 1e-6 || abs >= 1e21)
	d := schubfach(math.Float64bits(abs))
	return string(appendShortest(nil, math.Signbit(f), d, expFormat))
}

func checkRender(t *testing.T, f float64) {
	t.Helper()
	if f == 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return
	}
	got := renderShortest(f)
	w, err := stdjson.Marshal(f)
	if err != nil {
		return
	}
	if got != string(w) {
		t.Errorf("%v (bits %#016x): schubfach %q, encoding/json %q",
			f, math.Float64bits(f), got, w)
	}
}

func TestRenderMatchesStdlib(t *testing.T) {
	for _, f := range []float64{
		1, 2, 3, 1.5, 2.5, 0.5, 100, 1e6, 1e7, 1e20, 1e21, 1e22,
		1e-5, 1e-6, 1e-7, 1e-8, 0.1, 0.3, 2.675,
		math.MaxFloat64, math.SmallestNonzeroFloat64,
		math.Pi, math.E, 1.0 / 3.0,
		123456789012345, 1234567890123456789,
		0.000001, 0.0000001, 9.999999e20, 1.0000001e21,
	} {
		checkRender(t, f)
		checkRender(t, -f)
	}
}

func TestRenderRandom(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for i := 0; i < 200000; i++ {
		checkRender(t, math.Float64frombits(r.Uint64()))
	}
}

func TestRenderRealistic(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	for i := 0; i < 100000; i++ {
		switch i % 5 {
		case 0:
			checkRender(t, float64(r.Intn(1000000))/100)
		case 1:
			checkRender(t, float64(r.Intn(1000000))/1000)
		case 2:
			checkRender(t, r.NormFloat64())
		case 3:
			checkRender(t, float64(r.Intn(1<<30))*1.5)
		case 4:
			checkRender(t, r.Float64()*math.Pow(10, float64(r.Intn(40)-20)))
		}
	}
}

func FuzzRenderMatchesStdlib(f *testing.F) {
	for _, v := range []uint64{math.Float64bits(1), math.Float64bits(1e21), math.Float64bits(1e-7)} {
		f.Add(v)
	}
	f.Fuzz(func(t *testing.T, bits uint64) {
		v := math.Float64frombits(bits)
		if v == 0 || math.IsInf(v, 0) || math.IsNaN(v) {
			return
		}
		got := renderShortest(v)
		w, err := stdjson.Marshal(v)
		if err != nil {
			return
		}
		if got != string(w) {
			t.Fatalf("%v (bits %#016x): schubfach %q, encoding/json %q", v, bits, got, w)
		}
	})
}
