package bench

// The Small/Medium/Large rows: the fixtures every Go JSON README descends
// from, run under this harness's protocol — stdlib-compatible configurations
// only, outputs cross-checked against encoding/json before any number
// counts, allocations reported. Published tables for these fixtures often
// use lossy configs (jsoniter ConfigFastest, sonic default); those numbers
// are not comparable with these and the difference is deliberate.

import (
	"encoding/json"
	"reflect"
	"testing"

	sonic "github.com/bytedance/sonic"
	goccy "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
	segjson "github.com/segmentio/encoding/json"
)

func smlCases() []struct {
	name string
	data []byte
	mk   func() any
} {
	return []struct {
		name string
		data []byte
		mk   func() any
	}{
		{"small", smallFixture, func() any { return &SmallPayload{} }},
		{"medium", mediumFixture, func() any { return &MediumPayload{} }},
		{"large", largeFixture, func() any { return &LargePayload{} }},
	}
}

func BenchmarkSMLUnmarshal(b *testing.B) {
	libs := []struct {
		name string
		f    func([]byte, any) error
	}{
		{"ours", func(d []byte, v any) error { return ours.Unmarshal(d, v) }},
		{"sonic", func(d []byte, v any) error { return sonic.ConfigStd.Unmarshal(d, v) }},
		{"goccy", func(d []byte, v any) error { return goccy.Unmarshal(d, v) }},
		{"segmentio", func(d []byte, v any) error { return segjson.Unmarshal(d, v) }},
		{"jsoniter", func(d []byte, v any) error { return jsoniterStd.Unmarshal(d, v) }},
		{"stdlib", func(d []byte, v any) error { return json.Unmarshal(d, v) }},
	}
	for _, c := range smlCases() {
		want := c.mk()
		if err := json.Unmarshal(c.data, want); err != nil {
			b.Fatal(err)
		}
		for _, l := range libs {
			got := c.mk()
			if err := l.f(c.data, got); err != nil {
				b.Fatalf("%s/%s: %v", c.name, l.name, err)
			}
			if !reflect.DeepEqual(got, want) {
				b.Fatalf("%s/%s: decode disagrees with encoding/json", c.name, l.name)
			}
			b.Run(c.name+"/"+l.name, func(b *testing.B) {
				b.SetBytes(int64(len(c.data)))
				b.ReportAllocs()
				for b.Loop() {
					v := c.mk()
					if err := l.f(c.data, v); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkSMLMarshal(b *testing.B) {
	libs := []struct {
		name string
		f    func(any) ([]byte, error)
	}{
		{"ours", func(v any) ([]byte, error) { return ours.Marshal(v) }},
		{"sonic", func(v any) ([]byte, error) { return sonic.ConfigStd.Marshal(v) }},
		{"goccy", func(v any) ([]byte, error) { return goccy.Marshal(v) }},
		{"segmentio", func(v any) ([]byte, error) { return segjson.Marshal(v) }},
		{"jsoniter", func(v any) ([]byte, error) { return jsoniterStd.Marshal(v) }},
		{"stdlib", func(v any) ([]byte, error) { return json.Marshal(v) }},
	}
	for _, c := range smlCases() {
		v := c.mk()
		if err := json.Unmarshal(c.data, v); err != nil {
			b.Fatal(err)
		}
		want, err := json.Marshal(v)
		if err != nil {
			b.Fatal(err)
		}
		for _, l := range libs {
			out, err := l.f(v)
			if err != nil {
				b.Fatalf("%s/%s: %v", c.name, l.name, err)
			}
			if string(out) != string(want) {
				b.Fatalf("%s/%s: output differs from encoding/json", c.name, l.name)
			}
			b.Run(c.name+"/"+l.name, func(b *testing.B) {
				b.SetBytes(int64(len(want)))
				b.ReportAllocs()
				for b.Loop() {
					if _, err := l.f(v); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
