package simdjson

import (
	stdjson "encoding/json"
	"math"
	"testing"
)

// appendFloat takes a shortcut for whole numbers, and the shortcut is only
// right where the exact integer is also the shortest decimal that round-trips.
// That holds below 2^53 for a float64 and below 2^24 for a float32, and the
// interesting cases are all at those edges — plus negative zero, which is a
// whole number that must not print as one.
func TestFloatMatchesStdlib(t *testing.T) {
	vals := []float64{
		0, math.Copysign(0, -1), 1, -1, 3, -3, 1.5, -1.5,
		1e14, -1e14, 1e15, -1e15, 1e15 + 1, 1<<53 - 1, 1 << 53,
		1e20, 1e21, 1e-6, 1e-7, 123456789012345, 999999999999999,
		math.SmallestNonzeroFloat64, math.MaxFloat64,
	}
	for i := 0; i < 3000; i++ {
		vals = append(vals, float64(i)*1.5, float64(i)*0.1, float64(-i))
	}
	for _, v := range vals {
		got, err := Marshal(v)
		if err != nil {
			t.Fatalf("%v: %v", v, err)
		}
		want, err := stdjson.Marshal(v)
		if err != nil {
			t.Fatalf("%v: stdlib %v", v, err)
		}
		if string(got) != string(want) {
			t.Errorf("float64(%v) = %s, stdlib %s", v, got, want)
		}
	}

	// 123456792 is the case that says the bound has to be 2^24 and not 2^53:
	// it is a whole number, it is exactly a float32, and the shortest decimal
	// that round-trips to it is 123456790.
	f32 := []float32{0, 1, 3, 1.5, 1 << 23, 1<<24 - 1, 1 << 24, 1<<24 + 2, 123456792, 1e20, 16777216}
	for i := 0; i < 3000; i++ {
		f32 = append(f32, float32(i)*1.5, float32(i)*3, float32(i)*1e6)
	}
	for _, v := range f32 {
		got, err := Marshal(v)
		if err != nil {
			t.Fatalf("%v: %v", v, err)
		}
		want, err := stdjson.Marshal(v)
		if err != nil {
			t.Fatalf("%v: stdlib %v", v, err)
		}
		if string(got) != string(want) {
			t.Errorf("float32(%v) = %s, stdlib %s", v, got, want)
		}
	}
}
