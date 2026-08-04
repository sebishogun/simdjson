package bench

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sebishogun/simd"
)

// Where does a vector scan for "first byte needing a JSON escape" beat a word
// loop? The word loop does eight bytes per iteration with no call; the kernel
// does sixty-four but is a call away.
func BenchmarkEscapeScan(b *testing.B) {
	for _, n := range []int{16, 32, 64, 128, 256, 1024} {
		s := strings.Repeat("abcdefgh", n/8+1)[:n]
		b.Run(fmt.Sprintf("n=%d/word", n), func(b *testing.B) {
			for b.Loop() {
				sinkN2 = wordScan(s)
			}
		})
		b.Run(fmt.Sprintf("n=%d/kernel", n), func(b *testing.B) {
			for b.Loop() {
				sinkN2 = simd.IndexAny(s, "\"\\<>&")
			}
		})
	}
}

var sinkN2 int

func wordScan(s string) int {
	const (
		lo = 0x0101010101010101
		hi = 0x8080808080808080
	)
	i := 0
	for ; i+8 <= len(s); i += 8 {
		w := uint64(s[i]) | uint64(s[i+1])<<8 | uint64(s[i+2])<<16 | uint64(s[i+3])<<24 |
			uint64(s[i+4])<<32 | uint64(s[i+5])<<40 | uint64(s[i+6])<<48 | uint64(s[i+7])<<56
		if a := w &^ hi; (a-lo*0x20)&^a&hi != 0 {
			break
		}
		for _, c := range []byte{'"', '\\', '<', '>', '&', 0xE2} {
			x := w ^ (lo * uint64(c))
			if (x-lo)&^x&hi != 0 {
				return i
			}
		}
	}
	return i
}
