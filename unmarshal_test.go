package simdjson_test

import (
	"encoding/json"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/sebishogun/simdjson"
)

type inner struct {
	X int    `json:"x"`
	Y string `json:"y,omitempty"`
}

type embedded struct {
	E1 int `json:"e1"`
	E2 int
}

type wide struct {
	embedded
	Str     string         `json:"str"`
	Num     int            `json:"num"`
	Big     int64          `json:"big"`
	U       uint32         `json:"u"`
	F       float64        `json:"f"`
	F32     float32        `json:"f32"`
	B       bool           `json:"b"`
	Ptr     *inner         `json:"ptr"`
	Slice   []int          `json:"slice"`
	StrSl   []string       `json:"strsl"`
	Arr     [3]int         `json:"arr"`
	Map     map[string]int `json:"map"`
	MapAny  map[string]any `json:"mapany"`
	MapInt  map[int]string `json:"mapint"`
	Nested  inner          `json:"nested"`
	Any     any            `json:"any"`
	Bytes   []byte         `json:"bytes"`
	Quoted  int            `json:"quoted,string"`
	QuotedB bool           `json:"qb,string"`
	Skipped int            `json:"-"`
	NoTag   string
	Deep    map[string][]inner `json:"deep"`
	unexp   int
}

// Every field of a wide struct, decoded by both, compared. Any place the two
// disagree is a place this package would silently produce different data from
// the standard library.
func TestUnmarshalMatchesStdlib(t *testing.T) {
	docs := []string{
		`{}`,
		`{"str":"hello","num":42,"b":true,"f":1.5}`,
		`{"str":"\u00e9\n\t","num":-7,"big":9007199254740993,"u":4294967295}`,
		`{"ptr":{"x":1,"y":"z"}}`,
		`{"ptr":null,"slice":null,"map":null,"any":null,"nested":null}`,
		`{"slice":[1,2,3],"arr":[9,8,7,6],"arr2":[1]}`,
		`{"arr":[1]}`,
		`{"strsl":["a","b\"c"]}`,
		`{"map":{"a":1,"b":2},"mapint":{"1":"one","2":"two"}}`,
		`{"mapany":{"a":[1,{"b":null}],"c":"d"}}`,
		`{"nested":{"x":5},"any":{"deep":[1,2,{"k":true}]}}`,
		`{"bytes":"aGVsbG8="}`,
		`{"quoted":"123","qb":"true"}`,
		`{"e1":1,"E2":2}`,
		`{"NoTag":"v"}`,
		`{"notag":"case-insensitive"}`,
		`{"STR":"upper"}`,
		`{"-":5,"Skipped":9}`,
		`{"unknown":1,"str":"kept"}`,
		`{"f32":3.4028235e38,"f":1e308}`,
		`{"deep":{"k":[{"x":1},{"x":2}]}}`,
		`{"num":0,"f":0,"b":false,"str":""}`,
	}
	for _, doc := range docs {
		var got, want wide
		gotErr := simdjson.Unmarshal([]byte(doc), &got)
		wantErr := json.Unmarshal([]byte(doc), &want)
		if (gotErr == nil) != (wantErr == nil) {
			t.Errorf("%s\n  simdjson err = %v\n  stdlib   err = %v", doc, gotErr, wantErr)
			continue
		}
		if wantErr != nil {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s\n  simdjson = %+v\n  stdlib   = %+v", doc, got, want)
		}
	}
}

// The interface{} case has its own shape rules — every number becomes a
// float64, every object a map[string]any — and getting them wrong is invisible
// until someone type-asserts.
func TestUnmarshalAnyMatchesStdlib(t *testing.T) {
	docs := []string{
		`null`, `true`, `false`, `0`, `-1.5e10`, `"s"`, `"\ud83d\ude00"`,
		`[]`, `{}`, `[1,"two",{"three":[4,null]}]`,
		`{"a":{"b":{"c":[1,2,3]}}}`,
		`[[[[[1]]]]]`,
		`{"big":123456789012345678901234567890}`,
	}
	for _, doc := range docs {
		var got, want any
		gotErr := simdjson.Unmarshal([]byte(doc), &got)
		wantErr := json.Unmarshal([]byte(doc), &want)
		if (gotErr == nil) != (wantErr == nil) {
			t.Errorf("%s: simdjson err = %v, stdlib err = %v", doc, gotErr, wantErr)
			continue
		}
		if wantErr == nil && !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n  simdjson = %#v\n  stdlib   = %#v", doc, got, want)
		}
	}
}

// Random documents built from atoms chosen to collide with the struct above,
// decoded by both. This is the check that catches what the hand-written cases
// did not think of.
func TestUnmarshalFuzzyAgainstStdlib(t *testing.T) {
	keys := []string{"str", "num", "big", "u", "f", "f32", "b", "ptr", "slice",
		"strsl", "arr", "map", "mapany", "mapint", "nested", "any", "bytes",
		"quoted", "qb", "e1", "E2", "NoTag", "notag", "STR", "unknown", "deep"}
	vals := []string{`1`, `-1`, `0`, `1.5`, `"x"`, `""`, `true`, `false`, `null`,
		`[]`, `[1,2]`, `["a"]`, `{}`, `{"x":1}`, `{"a":1}`, `"123"`, `"true"`,
		`{"x":1,"y":"s"}`, `[{"x":1}]`, `"aGk="`, `1e3`, `-0.0`}
	r := rand.New(rand.NewPCG(97, 101))
	for trial := 0; trial < 6000; trial++ {
		var sb strings.Builder
		sb.WriteByte('{')
		for i := 0; i < r.IntN(5); i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteByte('"')
			sb.WriteString(keys[r.IntN(len(keys))])
			sb.WriteString(`":`)
			sb.WriteString(vals[r.IntN(len(vals))])
		}
		sb.WriteByte('}')
		doc := sb.String()

		var got, want wide
		gotErr := simdjson.Unmarshal([]byte(doc), &got)
		wantErr := json.Unmarshal([]byte(doc), &want)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%s\n  simdjson err = %v\n  stdlib   err = %v", doc, gotErr, wantErr)
		}
		if wantErr == nil && !reflect.DeepEqual(got, want) {
			t.Fatalf("%s\n  simdjson = %+v\n  stdlib   = %+v", doc, got, want)
		}
	}
}

func TestUnmarshalRejectsNonPointer(t *testing.T) {
	var w wide
	if err := simdjson.Unmarshal([]byte(`{}`), w); err == nil {
		t.Error("decoding into a non-pointer must fail")
	}
	if err := simdjson.Unmarshal([]byte(`{}`), nil); err == nil {
		t.Error("decoding into nil must fail")
	}
	var p *wide
	if err := simdjson.Unmarshal([]byte(`{}`), p); err == nil {
		t.Error("decoding into a nil pointer must fail")
	}
}

// Decode on a Value is Unmarshal for part of a document, which is the point of
// having an index at all.
func TestDecodeSubValue(t *testing.T) {
	doc := []byte(`{"meta":{"page":1},"items":[{"x":1},{"x":2}]}`)
	d, err := simdjson.Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	var it []inner
	if err := d.Get("items").Decode(&it); err != nil {
		t.Fatal(err)
	}
	if len(it) != 2 || it[0].X != 1 || it[1].X != 2 {
		t.Fatalf("got %+v", it)
	}
}

// FuzzUnmarshalAgainstStdlib holds the decoder to encoding/json's behaviour on
// arbitrary input: the same document must be accepted or rejected by both, and
// when accepted must produce a value reflect.DeepEqual to the standard
// library's. Every disagreement found while writing this decoder — nil versus
// empty slices, null on a `,string` field, []byte accepting both a base64
// string and an array — was found this way rather than by reading the docs.
func FuzzUnmarshalAgainstStdlib(f *testing.F) {
	for _, s := range []string{
		`{}`, `{"str":"a","num":1}`, `{"slice":[]}`, `{"qb":null}`,
		`{"bytes":[1,2]}`, `{"bytes":"aGk="}`, `{"arr":[1,2,3,4]}`,
		`{"mapint":{"1":"a"}}`, `{"e1":1,"E2":2}`, `{"ptr":{"x":1}}`,
		`{"any":{"a":[1,null,true]}}`, `{"quoted":"7"}`, `{"f32":1e39}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, doc string) {
		var got, want wide
		gotErr := simdjson.Unmarshal([]byte(doc), &got)
		wantErr := json.Unmarshal([]byte(doc), &want)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%q\n  simdjson err = %v\n  stdlib   err = %v", doc, gotErr, wantErr)
		}
		if wantErr != nil {
			return
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%q\n  simdjson = %+v\n  stdlib   = %+v", doc, got, want)
		}

		var gotAny, wantAny any
		gotErr = simdjson.Unmarshal([]byte(doc), &gotAny)
		wantErr = json.Unmarshal([]byte(doc), &wantAny)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%q into any\n  simdjson err = %v\n  stdlib err = %v", doc, gotErr, wantErr)
		}
		if wantErr == nil && !reflect.DeepEqual(gotAny, wantAny) {
			t.Fatalf("%q into any\n  simdjson = %#v\n  stdlib   = %#v", doc, gotAny, wantAny)
		}
	})
}
