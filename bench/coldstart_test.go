package bench

// Cold start: what the FIRST operation on a never-seen type costs. Every
// iteration builds a fresh type with reflect.StructOf, so each decode pays
// the library's per-type setup -- sonic's JIT compile, goccy's codec build,
// our compiled-decoder construction. Published tables Pretouch this away;
// servers meet it on every new payload shape after deploy.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	sonic "github.com/bytedance/sonic"
	goccy "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
)

var coldDoc = []byte(`{"f0":1,"f1":"two","f2":3.5,"f3":true,"f4":[1,2,3]}`)

func coldType(i int) reflect.Type {
	return reflect.StructOf([]reflect.StructField{
		{Name: "F0", Type: reflect.TypeOf(int(0)), Tag: reflect.StructTag(fmt.Sprintf(`json:"f0" x:"%d"`, i))},
		{Name: "F1", Type: reflect.TypeOf(""), Tag: `json:"f1"`},
		{Name: "F2", Type: reflect.TypeOf(float64(0)), Tag: `json:"f2"`},
		{Name: "F3", Type: reflect.TypeOf(false), Tag: `json:"f3"`},
		{Name: "F4", Type: reflect.TypeOf([]int(nil)), Tag: `json:"f4"`},
	})
}

func BenchmarkColdStartUnmarshal(b *testing.B) {
	libs := []struct {
		name string
		f    func([]byte, any) error
	}{
		{"ours", func(d []byte, v any) error { return ours.Unmarshal(d, v) }},
		{"sonic", func(d []byte, v any) error { return sonic.ConfigStd.Unmarshal(d, v) }},
		{"goccy", func(d []byte, v any) error { return goccy.Unmarshal(d, v) }},
		{"stdlib", func(d []byte, v any) error { return json.Unmarshal(d, v) }},
	}
	for _, l := range libs {
		b.Run(l.name, func(b *testing.B) {
			i := 0
			for b.Loop() {
				v := reflect.New(coldType(i)).Interface()
				i++
				if err := l.f(coldDoc, v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
