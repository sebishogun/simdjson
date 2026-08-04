package bench

import (
	"bytes"
	stdjson "encoding/json"
	"io"
	"strconv"
	"testing"

	sonic "github.com/bytedance/sonic"
	goccy "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
)

type srow struct {
	ID    int      `json:"id"`
	Name  string   `json:"name"`
	Tags  []string `json:"tags"`
	Score float64  `json:"score"`
}

// ndjson builds a newline-delimited stream: the shape a log file or an export
// arrives in, and the reason a streaming decoder exists.
func ndjson(n int) []byte {
	var b bytes.Buffer
	e := stdjson.NewEncoder(&b)
	for i := 0; i < n; i++ {
		e.Encode(srow{
			ID:    i,
			Name:  "record-" + strconv.Itoa(i),
			Tags:  []string{"alpha", "beta", "gamma"},
			Score: float64(i) * 1.5,
		})
	}
	return b.Bytes()
}

func BenchmarkStreamDecode(b *testing.B) {
	data := ndjson(50000)
	b.Run("ours", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for i := 0; i < b.N; i++ {
			d := ours.NewDecoder(bytes.NewReader(data))
			for {
				var r srow
				if err := d.Decode(&r); err != nil {
					if err == io.EOF {
						break
					}
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("stdlib", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for i := 0; i < b.N; i++ {
			d := stdjson.NewDecoder(bytes.NewReader(data))
			for {
				var r srow
				if err := d.Decode(&r); err != nil {
					if err == io.EOF {
						break
					}
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("goccy", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for i := 0; i < b.N; i++ {
			d := goccy.NewDecoder(bytes.NewReader(data))
			for {
				var r srow
				if err := d.Decode(&r); err != nil {
					if err == io.EOF {
						break
					}
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("sonic", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for i := 0; i < b.N; i++ {
			d := sonic.ConfigStd.NewDecoder(bytes.NewReader(data))
			for {
				var r srow
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

func BenchmarkStreamEncode(b *testing.B) {
	rows := make([]srow, 50000)
	for i := range rows {
		rows[i] = srow{ID: i, Name: "record-" + strconv.Itoa(i),
			Tags: []string{"alpha", "beta", "gamma"}, Score: float64(i) * 1.5}
	}
	run := func(name string, mk func(io.Writer) interface{ Encode(any) error }) {
		b.Run(name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				var buf bytes.Buffer
				e := mk(&buf)
				for j := range rows {
					if err := e.Encode(rows[j]); err != nil {
						b.Fatal(err)
					}
				}
				b.SetBytes(int64(buf.Len()))
			}
		})
	}
	run("ours", func(w io.Writer) interface{ Encode(any) error } { return ours.NewEncoder(w) })
	run("stdlib", func(w io.Writer) interface{ Encode(any) error } { return stdjson.NewEncoder(w) })
	run("goccy", func(w io.Writer) interface{ Encode(any) error } { return goccy.NewEncoder(w) })
	run("sonic", func(w io.Writer) interface{ Encode(any) error } { return sonic.ConfigStd.NewEncoder(w) })
}
