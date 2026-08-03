package simdjson

// The benchmarks the gate watches.
//
// Every one is an operation somebody actually calls, on a document somebody
// actually has, and they are here rather than in the comparison harness because
// a gate has to run without the competing libraries installed. What they are
// for is not finding out how fast this is — that is what the harness is for —
// but noticing when it stops being that fast.

import (
	"bytes"
	"os"
	"testing"
)

func gateCorpus(b *testing.B, name string) []byte {
	b.Helper()
	data, err := os.ReadFile("/tmp/" + name + ".json")
	if err != nil {
		b.Skipf("%s not available", name)
	}
	return data
}

var gateNames = []string{"twitter", "citm", "canada"}

func BenchmarkGateParse(b *testing.B) {
	for _, name := range gateNames {
		data := gateCorpus(b, name)
		b.Run(name, func(b *testing.B) {
			var p Parser
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				if _, err := p.Parse(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkGateScan(b *testing.B) {
	for _, name := range gateNames {
		data := gateCorpus(b, name)
		b.Run(name, func(b *testing.B) {
			var p Parser
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				if _, err := p.Scan(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkGateValid(b *testing.B) {
	for _, name := range gateNames {
		data := gateCorpus(b, name)
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				if !Valid(data) {
					b.Fatal("invalid")
				}
			}
		})
	}
}

type gateRow struct {
	ID   int64    `json:"id"`
	Text string   `json:"text"`
	Tags []string `json:"tags"`
	N    float64  `json:"n"`
	OK   bool     `json:"ok"`
}

func gateValue() []gateRow {
	out := make([]gateRow, 2000)
	for i := range out {
		out[i] = gateRow{
			ID: int64(i), Text: "a tweet-ish string with some length to it",
			Tags: []string{"alpha", "beta"}, N: float64(i) * 1.5, OK: i%2 == 0,
		}
	}
	return out
}

func BenchmarkGateMarshal(b *testing.B) {
	v := gateValue()
	b.Run("Marshal", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := Marshal(v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("MarshalTo", func(b *testing.B) {
		buf := make([]byte, 0, 1<<20)
		for i := 0; i < b.N; i++ {
			var err error
			if buf, err = MarshalTo(buf[:0], v); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkGateUnmarshal(b *testing.B) {
	src, err := Marshal(gateValue())
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		var out []gateRow
		if err := Unmarshal(src, &out); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGateStream(b *testing.B) {
	var buf bytes.Buffer
	e := NewEncoder(&buf)
	for _, r := range gateValue() {
		if err := e.Encode(r); err != nil {
			b.Fatal(err)
		}
	}
	data := buf.Bytes()
	b.Run("Decode", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for i := 0; i < b.N; i++ {
			d := NewDecoder(bytes.NewReader(data))
			for {
				var r gateRow
				if err := d.Decode(&r); err != nil {
					break
				}
			}
		}
	})
	b.Run("Encode", func(b *testing.B) {
		rows := gateValue()
		b.SetBytes(int64(len(data)))
		for i := 0; i < b.N; i++ {
			var out bytes.Buffer
			out.Grow(len(data))
			enc := NewEncoder(&out)
			for j := range rows {
				if err := enc.Encode(rows[j]); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
}
