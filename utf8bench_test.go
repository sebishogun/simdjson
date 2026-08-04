package simdjson

// Where does simd.ValidUTF8 overtake unicode/utf8.Valid? The strings a decoder
// sees are keys and field values -- tens of bytes -- and a non-inlinable call
// into a kernel is about 1.4ns before it does anything.

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sebishogun/simd"
)

func BenchmarkUTF8Crossover(b *testing.B) {
	// Non-ASCII, because the all-ASCII case never reaches either of these.
	unit := "名前前田あゆみ"
	for _, n := range []int{8, 16, 24, 32, 48, 64, 96, 128, 192, 256, 512} {
		var sb strings.Builder
		for sb.Len() < n {
			sb.WriteString(unit)
		}
		s := []byte(sb.String()[:n])
		// Trim to a rune boundary so both agree it is valid.
		for len(s) > 0 && !utf8.Valid(s) {
			s = s[:len(s)-1]
		}
		b.Run("stdlib/"+itoaBench(len(s)), func(b *testing.B) {
			for b.Loop() {
				if !utf8.Valid(s) {
					b.Fatal("invalid")
				}
			}
		})
		b.Run("simd/"+itoaBench(len(s)), func(b *testing.B) {
			for b.Loop() {
				if !simd.ValidUTF8(s) {
					b.Fatal("invalid")
				}
			}
		})
	}
}

func itoaBench(n int) string { return string(appendInt(nil, int64(n))) }
