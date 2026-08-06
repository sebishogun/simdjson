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

// BenchmarkStreamShapes varies the one axis the ndjson bench holds fixed:
// record size. A log line is a hundred bytes, a tweet is two kilobytes, an
// exported row with embedded blobs is tens of kilobytes, and the batching
// machinery -- buffer growth, index amortization, prefetch -- has different
// work to do at each. Records are real tweets repeated to size, streamed as
// newline-delimited values and decoded into any.
func BenchmarkStreamShapes(b *testing.B) {
	tw := loadCorpus(b, "twitter")
	doc, err := ours.Parse(tw)
	if err != nil {
		b.Fatal(err)
	}
	statuses := doc.Root().Get("statuses")
	var recs [][]byte
	for i := 0; i < statuses.Len(); i++ {
		recs = append(recs, append([]byte(nil), statuses.Index(i).Raw()...))
	}
	if len(recs) == 0 {
		b.Fatal("no statuses")
	}
	mk := func(target, per int) []byte {
		var buf bytes.Buffer
		for i := 0; buf.Len() < target; i++ {
			if per == 1 {
				buf.Write(recs[i%len(recs)])
			} else {
				buf.WriteByte('[')
				for j := 0; j < per; j++ {
					if j > 0 {
						buf.WriteByte(',')
					}
					buf.Write(recs[(i+j)%len(recs)])
				}
				buf.WriteByte(']')
			}
			buf.WriteByte('\n')
		}
		return buf.Bytes()
	}
	streams := []struct {
		name string
		data []byte
	}{
		{"tweet-2KB", mk(6<<20, 1)},
		{"batch-50KB", mk(6<<20, 24)},
	}
	for _, s := range streams {
		data := s.data
		b.Run(s.name+"/ours", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				d := ours.NewDecoder(bytes.NewReader(data))
				for {
					var v any
					if err := d.Decode(&v); err != nil {
						if err == io.EOF {
							break
						}
						b.Fatal(err)
					}
				}
			}
		})
		b.Run(s.name+"/stdlib", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				d := stdjson.NewDecoder(bytes.NewReader(data))
				for {
					var v any
					if err := d.Decode(&v); err != nil {
						if err == io.EOF {
							break
						}
						b.Fatal(err)
					}
				}
			}
		})
		b.Run(s.name+"/sonic", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				d := sonic.ConfigStd.NewDecoder(bytes.NewReader(data))
				for {
					var v any
					if err := d.Decode(&v); err != nil {
						if err == io.EOF {
							break
						}
						b.Fatal(err)
					}
				}
			}
		})
		b.Run(s.name+"/goccy", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				d := goccy.NewDecoder(bytes.NewReader(data))
				for {
					var v any
					if err := d.Decode(&v); err != nil {
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
