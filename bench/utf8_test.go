package bench

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sebishogun/simd"
)

// Where does a vector UTF-8 validator start beating the standard library's?
// The strings an encoder meets are tens to hundreds of bytes, and a
// non-inlinable call costs before it does anything.
func BenchmarkValidUTF8(b *testing.B) {
	for _, n := range []int{16, 32, 64, 128, 256, 1024, 8192} {
		ascii := strings.Repeat("a", n)
		uni := strings.Repeat("café ", n/6+1)[:n]
		for _, tc := range []struct {
			name string
			s    string
		}{{"ascii", ascii}, {"unicode", uni}} {
			b.Run(fmt.Sprintf("n=%d/%s/stdlib", n, tc.name), func(b *testing.B) {
				for b.Loop() {
					sinkB = utf8.ValidString(tc.s)
				}
			})
			b.Run(fmt.Sprintf("n=%d/%s/simd", n, tc.name), func(b *testing.B) {
				for b.Loop() {
					sinkB = simd.ValidUTF8(tc.s)
				}
			})
		}
	}
}

var sinkB bool
