package bench

import (
	"encoding/json"
	"testing"

	sonic "github.com/bytedance/sonic"
	goccy "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
	segjson "github.com/segmentio/encoding/json"
)

func BenchmarkMarshalIndentRow(b *testing.B) {
	data := loadCorpus(b, "twitter")
	var v tSearch
	if err := json.Unmarshal(data, &v); err != nil {
		b.Fatal(err)
	}
	want, err := json.MarshalIndent(&v, "", "  ")
	if err != nil {
		b.Fatal(err)
	}
	got, err := ours.MarshalIndent(&v, "", "  ")
	if err != nil {
		b.Fatal(err)
	}
	if string(got) != string(want) {
		b.Fatalf("outputs differ at %d vs %d bytes", len(got), len(want))
	}
	run := func(name string, f func() ([]byte, error)) {
		b.Run(name, func(b *testing.B) {
			out, err := f()
			if err != nil {
				b.Fatal(err)
			}
			if string(out) != string(want) {
				b.Fatalf("%s differs from encoding/json", name)
			}
			b.SetBytes(int64(len(out)))
			for b.Loop() {
				if _, err := f(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	run("ours", func() ([]byte, error) { return ours.MarshalIndent(&v, "", "  ") })
	run("sonic", func() ([]byte, error) { return sonic.ConfigStd.MarshalIndent(&v, "", "  ") })
	run("goccy", func() ([]byte, error) { return goccy.MarshalIndent(&v, "", "  ") })
	run("segmentio", func() ([]byte, error) { return segjson.MarshalIndent(&v, "", "  ") })
	run("stdlib", func() ([]byte, error) { return json.MarshalIndent(&v, "", "  ") })
}
