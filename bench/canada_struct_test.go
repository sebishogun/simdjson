package bench

import (
	"encoding/json"
	"testing"

	sonic "github.com/bytedance/sonic"
	goccy "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
)

type canadaFC struct {
	Type     string `json:"type"`
	Features []struct {
		Type       string `json:"type"`
		Properties struct {
			Name string `json:"name"`
		} `json:"properties"`
		Geometry struct {
			Type        string         `json:"type"`
			Coordinates [][][2]float64 `json:"coordinates"`
		} `json:"geometry"`
	} `json:"features"`
}

func BenchmarkUnmarshalCanadaStruct(b *testing.B) {
	data := loadCorpus(b, "canada")
	var a, c canadaFC
	if err := ours.Unmarshal(data, &a); err != nil {
		b.Fatal(err)
	}
	if err := json.Unmarshal(data, &c); err != nil {
		b.Fatal(err)
	}
	if len(a.Features) != len(c.Features) ||
		len(a.Features[0].Geometry.Coordinates) != len(c.Features[0].Geometry.Coordinates) ||
		a.Features[0].Geometry.Coordinates[0][0] != c.Features[0].Geometry.Coordinates[0][0] {
		b.Fatal("decoders disagree")
	}
	run := func(name string, f func([]byte, any) error) {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				var v canadaFC
				if err := f(data, &v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	run("ours", func(d []byte, v any) error { return ours.Unmarshal(d, v) })
	run("sonic", func(d []byte, v any) error { return sonic.ConfigStd.Unmarshal(d, v) })
	run("goccy", func(d []byte, v any) error { return goccy.Unmarshal(d, v) })
	run("stdlib", func(d []byte, v any) error { return json.Unmarshal(d, v) })
}
