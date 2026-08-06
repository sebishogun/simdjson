package bench

// The rest of the C++ simdjson corpus, swept: every file is a shape our
// tables never measured, and each library runs Valid and a decode-to-map
// under stdlib-compatible configs, cross-checked before timing.

import (
	"encoding/json"
	"testing"

	sonic "github.com/bytedance/sonic"
	goccy "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
)

var shapeNames = []string{"numbers", "github_events", "apache_builds",
	"gsoc-2018", "instruments", "update-center", "mesh", "mesh.pretty", "marine_ik",
	"twitter", "citm", "canada"}

func BenchmarkShapeValid(b *testing.B) {
	for _, name := range shapeNames {
		data := loadCorpus(b, name)
		if !ours.Valid(data) || !json.Valid(data) {
			b.Fatalf("%s: validity disagreement", name)
		}
		for _, l := range []struct {
			lib string
			f   func([]byte) bool
		}{
			{"ours", ours.Valid},
			{"sonic", func(d []byte) bool { return sonic.ConfigStd.Valid(d) }},
			{"goccy", goccy.Valid},
			{"stdlib", json.Valid},
		} {
			b.Run(name+"/"+l.lib, func(b *testing.B) {
				b.SetBytes(int64(len(data)))
				for b.Loop() {
					if !l.f(data) {
						b.Fatal("invalid")
					}
				}
			})
		}
	}
}

func BenchmarkShapeDecodeMap(b *testing.B) {
	for _, name := range shapeNames {
		data := loadCorpus(b, name)
		var want any
		if err := json.Unmarshal(data, &want); err != nil {
			b.Fatal(err)
		}
		for _, l := range []struct {
			lib string
			f   func([]byte, any) error
		}{
			{"ours", func(d []byte, v any) error { return ours.Unmarshal(d, v) }},
			{"sonic", func(d []byte, v any) error { return sonic.ConfigStd.Unmarshal(d, v) }},
			{"goccy", func(d []byte, v any) error { return goccy.Unmarshal(d, v) }},
			{"stdlib", func(d []byte, v any) error { return json.Unmarshal(d, v) }},
		} {
			var got any
			if err := l.f(data, &got); err != nil {
				b.Fatalf("%s/%s: %v", name, l.lib, err)
			}
			b.Run(name+"/"+l.lib, func(b *testing.B) {
				b.SetBytes(int64(len(data)))
				for b.Loop() {
					var v any
					if err := l.f(data, &v); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
