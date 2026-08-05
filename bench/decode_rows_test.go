package bench

import (
	"encoding/json"
	"strings"
	"testing"

	sonic "github.com/bytedance/sonic"
	goccy "github.com/goccy/go-json"
	jsoniter "github.com/json-iterator/go"
	ours "github.com/sebishogun/simdjson"
	segjson "github.com/segmentio/encoding/json"
)

// citm's natural struct: the heavy parts are performances and seatCategory
// maps of small objects.
type citmDoc struct {
	AreaNames map[string]string `json:"areaNames"`
	Events    map[string]struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"events"`
	Performances []struct {
		ID        int64  `json:"id"`
		EventID   int64  `json:"eventId"`
		Start     int64  `json:"start"`
		VenueCode string `json:"venueCode"`
		Prices    []struct {
			Amount         int64 `json:"amount"`
			SeatCategoryID int64 `json:"seatCategoryId"`
		} `json:"prices"`
		SeatCategories []struct {
			SeatCategoryID int64 `json:"seatCategoryId"`
			Areas          []struct {
				AreaID int64 `json:"areaId"`
			} `json:"areas"`
		} `json:"seatCategories"`
	} `json:"performances"`
}

// jsoniterStd is the configuration that matches encoding/json's semantics;
// the "fastest" config skips validation and is not comparable.
var jsoniterStd = jsoniter.ConfigCompatibleWithStandardLibrary

func benchAll(b *testing.B, data []byte, mk func() any) {
	run := func(name string, f func([]byte, any) error) {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				v := mk()
				if err := f(data, v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	run("ours", func(d []byte, v any) error { return ours.Unmarshal(d, v) })
	run("sonic", func(d []byte, v any) error { return sonic.ConfigStd.Unmarshal(d, v) })
	run("goccy", func(d []byte, v any) error { return goccy.Unmarshal(d, v) })
	run("stdlib", func(d []byte, v any) error { return json.Unmarshal(d, v) })
	run("jsoniter", func(d []byte, v any) error { return jsoniterStd.Unmarshal(d, v) })
	run("segmentio", func(d []byte, v any) error { return segjson.Unmarshal(d, v) })
}

func BenchmarkUnmarshalCitmStruct(b *testing.B) {
	data := loadCorpus(b, "citm")
	var a, c citmDoc
	if err := ours.Unmarshal(data, &a); err != nil {
		b.Fatal(err)
	}
	if err := json.Unmarshal(data, &c); err != nil {
		b.Fatal(err)
	}
	if len(a.Performances) != len(c.Performances) || len(a.AreaNames) != len(c.AreaNames) ||
		a.Performances[0].ID != c.Performances[0].ID {
		b.Fatal("decoders disagree")
	}
	benchAll(b, data, func() any { return &citmDoc{} })
}

func BenchmarkUnmarshalFloatSlice(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; sb.Len() < 2<<20; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("-65.613616999999977,43.420273000000009")
	}
	sb.WriteString("]")
	data := []byte(sb.String())
	var a, c []float64
	if err := ours.Unmarshal(data, &a); err != nil {
		b.Fatal(err)
	}
	if err := json.Unmarshal(data, &c); err != nil {
		b.Fatal(err)
	}
	if len(a) != len(c) || a[0] != c[0] || a[len(a)-1] != c[len(c)-1] {
		b.Fatal("decoders disagree")
	}
	benchAll(b, data, func() any { return new([]float64) })
}
