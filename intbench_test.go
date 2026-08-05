package simdjson

// appendInt is 16.3% of the base struct encode. Is it actually fast?
//
// It uses a two-digit table with magic division, which is what sonic's
// i64toa.c does. strconv.AppendInt is tuned assembly-free Go that the standard
// library has had many eyes on. If ours is not clearly ahead, the 16.3% is not
// an opportunity and the search moves elsewhere.

import (
	"strconv"
	"testing"
)

var intCases = []struct {
	name string
	v    int64
}{
	{"1digit", 7},
	{"2digit", 42},
	{"4digit", 1234},
	{"6digit", 123456},
	{"9digit", 123456789},
	{"12digit", 123456789012},
	{"18digit", 123456789012345678},
	{"negative", -123456789},
	{"zero", 0},
	// twitter's shape: tweet and user ids are 18-19 digits.
	{"tweetid", 505874924095815681},
}

func BenchmarkAppendIntOurs(b *testing.B) {
	dst := make([]byte, 0, 64)
	for _, c := range intCases {
		v := c.v
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				dst = appendInt(dst[:0], v)
			}
		})
	}
}

func BenchmarkAppendIntStrconv(b *testing.B) {
	dst := make([]byte, 0, 64)
	for _, c := range intCases {
		v := c.v
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				dst = strconv.AppendInt(dst[:0], v, 10)
			}
		})
	}
}
