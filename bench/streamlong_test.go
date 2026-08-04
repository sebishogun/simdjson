package bench

import (
	"bytes"
	stdjson "encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"

	ours "github.com/sebishogun/simdjson"
)

type lrow struct {
	ID   int    `json:"id"`
	Body string `json:"body"`
}

// A stream whose records are mostly one long string: a log line, a document
// body, a base64 blob. The framing scan has nothing to find for the whole
// length of the field, which is the case a vector scan exists for.
func BenchmarkStreamDecodeLong(b *testing.B) {
	for _, n := range []int{64, 512, 4096} {
		var buf bytes.Buffer
		e := stdjson.NewEncoder(&buf)
		for i := 0; i < 200000/(n/32+1); i++ {
			e.Encode(lrow{ID: i, Body: strings.Repeat("abcdefgh", n/8)})
		}
		data := buf.Bytes()
		b.Run("field="+strconv.Itoa(n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				d := ours.NewDecoder(bytes.NewReader(data))
				for {
					var r lrow
					if err := d.Decode(&r); err != nil {
						if err == io.EOF {
							break
						}
						b.Fatal(err)
					}
				}
			}
		})
	}
}
