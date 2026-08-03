package simdjson

import (
	"math"
	"math/rand"
	"strconv"
	"testing"
)

func TestAppendIntMatchesStrconv(t *testing.T) {
	vals := []int64{
		0, 1, -1, 9, -9, 10, -10, 99, 100, 999, 1000, 9999, 10000,
		math.MaxInt64, math.MinInt64, math.MaxInt32, math.MinInt32,
		math.MaxInt64 - 1, math.MinInt64 + 1,
	}
	r := rand.New(rand.NewSource(1))
	for _, max := range []int64{10, 100, 10000, 1 << 20, 1 << 31, 1 << 62} {
		for i := 0; i < 4000; i++ {
			v := r.Int63n(max)
			vals = append(vals, v, -v)
		}
	}
	for _, v := range vals {
		got := string(appendInt(nil, v))
		want := strconv.FormatInt(v, 10)
		if got != want {
			t.Fatalf("appendInt(%d) = %q, strconv %q", v, got, want)
		}
	}
}

func TestAppendUintMatchesStrconv(t *testing.T) {
	vals := []uint64{
		0, 1, 9, 10, 99, 100, 999, 1000, 9999, 10000,
		math.MaxUint64, math.MaxUint64 - 1, math.MaxUint32, 1 << 63,
	}
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 20000; i++ {
		vals = append(vals, r.Uint64()>>(uint(i)%64))
	}
	for _, v := range vals {
		got := string(appendUint(nil, v))
		want := strconv.FormatUint(v, 10)
		if got != want {
			t.Fatalf("appendUint(%d) = %q, strconv %q", v, got, want)
		}
	}
}

func FuzzAppendIntMatchesStrconv(f *testing.F) {
	for _, v := range []int64{0, 1, -1, math.MaxInt64, math.MinInt64, 1234567890} {
		f.Add(v)
	}
	f.Fuzz(func(t *testing.T, v int64) {
		if got, want := string(appendInt(nil, v)), strconv.FormatInt(v, 10); got != want {
			t.Fatalf("appendInt(%d) = %q, strconv %q", v, got, want)
		}
		u := uint64(v)
		if got, want := string(appendUint(nil, u)), strconv.FormatUint(u, 10); got != want {
			t.Fatalf("appendUint(%d) = %q, strconv %q", u, got, want)
		}
	})
}
