package simdjson

// validUTF8 picks between unicode/utf8.Valid and simd.ValidUTF8 by length, so
// the two have to agree on every input. If they ever disagreed, the bug would
// present as a string being accepted or rejected according to how long it is,
// which is about the worst shape a bug can have.
//
// The interesting inputs are all near the boundary and all malformed in ways
// the two might treat differently: a truncated multi-byte sequence at the very
// end, a continuation byte with no lead, an overlong encoding, a surrogate, and
// a codepoint above U+10FFFF.

import (
	"math/rand"
	"testing"
	"unicode/utf8"

	"github.com/sebishogun/simd"
)

func TestValidUTF8AgreesAcrossTheGate(t *testing.T) {
	bad := [][]byte{
		{0x80},                   // continuation with no lead
		{0xC2},                   // truncated two-byte
		{0xE0, 0xA0},             // truncated three-byte
		{0xF0, 0x9F, 0x92},       // truncated four-byte
		{0xC0, 0xAF},             // overlong '/'
		{0xE0, 0x80, 0xAF},       // overlong, three bytes
		{0xF0, 0x80, 0x80, 0xAF}, // overlong, four bytes
		{0xED, 0xA0, 0x80},       // surrogate D800
		{0xF4, 0x90, 0x80, 0x80}, // above U+10FFFF
		{0xFE}, {0xFF},
	}
	good := [][]byte{
		[]byte("a"), []byte("é"), []byte("中"), []byte("🙂"),
		{0xF4, 0x8F, 0xBF, 0xBF}, // U+10FFFF, the last legal one
	}

	// Every length either side of the gate, with the awkward bytes placed at
	// the front, in the middle and at the very end -- the end being where a
	// vector pass and a scalar one are most likely to differ.
	for _, n := range []int{0, 1, 31, 32, 62, 63, 64, 65, 66, 95, 96, 127, 128, 129, 255, 256} {
		for _, tail := range append(append([][]byte{}, bad...), good...) {
			for _, place := range []string{"head", "middle", "tail"} {
				b := make([]byte, 0, n+len(tail))
				switch place {
				case "head":
					b = append(b, tail...)
					for len(b) < n {
						b = append(b, 'x')
					}
				case "middle":
					for len(b) < n/2 {
						b = append(b, 'x')
					}
					b = append(b, tail...)
					for len(b) < n {
						b = append(b, 'x')
					}
				case "tail":
					for len(b) < n {
						b = append(b, 'x')
					}
					b = append(b, tail...)
				}
				want := utf8.Valid(b)
				if got := validUTF8(b); got != want {
					t.Fatalf("validUTF8 %v, utf8.Valid %v: n=%d place=%s bytes=% x",
						got, want, n, place, b)
				}
				// And the kernel directly, so a disagreement is attributed to
				// the right one of the two.
				if got := simd.ValidUTF8(b); got != want {
					t.Fatalf("simd.ValidUTF8 %v, utf8.Valid %v: n=%d place=%s bytes=% x",
						got, want, n, place, b)
				}
			}
		}
	}
}

func FuzzValidUTF8Gate(f *testing.F) {
	f.Add([]byte("hello"))
	f.Add([]byte("日本語のテキストで、六十四バイトを超える長さのものを用意する必要がある"))
	f.Add([]byte{0xED, 0xA0, 0x80})
	f.Fuzz(func(t *testing.T, b []byte) {
		if got, want := validUTF8(b), utf8.Valid(b); got != want {
			t.Fatalf("validUTF8 %v, utf8.Valid %v for % x", got, want, b)
		}
	})
}

// Random inputs at the sizes the gate switches on, which the seeded fuzz corpus
// will not reach on its own.
func TestValidUTF8GateRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 20000; trial++ {
		n := rng.Intn(200)
		b := make([]byte, n)
		for i := range b {
			// Weighted towards the bytes that start and continue sequences,
			// so most inputs are near-valid rather than obviously not.
			switch rng.Intn(4) {
			case 0:
				b[i] = byte(rng.Intn(0x80))
			case 1:
				b[i] = byte(0x80 + rng.Intn(0x40))
			case 2:
				b[i] = byte(0xC0 + rng.Intn(0x20))
			default:
				b[i] = byte(rng.Intn(256))
			}
		}
		if got, want := validUTF8(b), utf8.Valid(b); got != want {
			t.Fatalf("validUTF8 %v, utf8.Valid %v for % x", got, want, b)
		}
	}
}
