package simdjson

// Where does the vector copy-and-scan overtake the scalar one?
//
// kernelScanMin is 64 and was not arrived at by measuring this. The struct
// encode spends 21.3% of itself in scalar scanning (cleanRun and plainASCIIRun)
// against 3.6% in the vector kernel, which says most strings never reach the
// kernel. The strings that matter are field values -- tweet text, names, URLs --
// not keys, so the distribution here is tens to hundreds of bytes.

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func quoteInputs() []struct {
	name string
	s    string
} {
	var out []struct {
		name string
		s    string
	}
	for _, n := range []int{8, 16, 24, 32, 48, 64, 96, 140, 256, 512} {
		ascii := strings.Repeat("abcdefghij", (n/10)+1)[:n]
		out = append(out, struct {
			name string
			s    string
		}{fmt.Sprintf("ascii/%d", n), ascii})
	}
	// Non-ASCII, which is where the UTF-8 check and the tail path run.
	//
	// Cut on a rune boundary. Truncating mid-rune makes the input invalid, and
	// then this measures appendQuotedInvalid's sanitize path instead -- which is
	// how the first version of this benchmark reported jp/140 at 295 ns against
	// jp/96 at 19.5, fifteen times worse for one and a half times the bytes.
	jp := strings.Repeat("名前前田あゆみ", 40)
	for _, n := range []int{24, 48, 96, 144, 258} {
		s := jp[:n]
		if !utf8.ValidString(s) {
			panic("cut mid-rune: " + fmt.Sprint(n))
		}
		out = append(out, struct {
			name string
			s    string
		}{fmt.Sprintf("jp/%d", len(s)), s})
	}
	// And a realistic mix: mostly ASCII with one non-ASCII run, which is what a
	// tweet actually looks like.
	for _, n := range []int{48, 96, 144} {
		s := "@user Hello there " + jp[:n]
		out = append(out, struct {
			name string
			s    string
		}{fmt.Sprintf("mixed/%d", len(s)), s})
	}
	return out
}

func BenchmarkAppendQuoted(b *testing.B) {
	dst := make([]byte, 0, 4096)
	for _, in := range quoteInputs() {
		s := in.s
		b.Run(in.name, func(b *testing.B) {
			b.SetBytes(int64(len(s)))
			for b.Loop() {
				dst = appendQuoted(dst[:0], s)
			}
		})
	}
}
