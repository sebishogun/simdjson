package simdjson

import (
	stdjson "encoding/json"
	"math"
	"testing"
)

// The reflect-free path must produce exactly what the reflect path and
// encoding/json produce. Round-tripping decoded documents is the case it
// exists for, so that is what is checked.
func TestEncodeAnyMatchesStdlib(t *testing.T) {
	docs := []string{
		`{}`, `[]`, `null`, `true`, `false`, `1`, `-2.5`, `1e3`, `"s"`, `""`,
		`{"a":1}`, `{"b":2,"a":1}`, `{"z":1,"a":2,"m":3}`,
		`[1,"two",true,null,3.5]`,
		`{"nested":{"deep":{"deeper":[1,2,{"x":null}]}}}`,
		`{"unicode":"日本語","esc":"a\"b\\c\nd","slash":"a/b"}`,
		`{"html":"<script>&</script>"}`,
		`{"big":123456789012345,"neg":-0.0001,"exp":1e21,"tiny":1e-7}`,
		`{"arr":[[],[[]],[[[]]]]}`,
		`{"":"empty key","k":""}`,
		`[{"a":1},{"a":2},{"a":3}]`,
	}
	for _, in := range docs {
		var v any
		if err := stdjson.Unmarshal([]byte(in), &v); err != nil {
			t.Fatalf("seed %s: %v", in, err)
		}
		got, err := Marshal(v)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		want, err := stdjson.Marshal(v)
		if err != nil {
			t.Fatalf("%s: stdlib %v", in, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s\n got %s\nwant %s", in, got, want)
		}
	}
}

// UseNumber decodes numbers as json.Number, which the fast path has to write as
// its own digits rather than reformatting them.
func TestEncodeAnyNumber(t *testing.T) {
	for _, in := range []string{
		`{"n":1}`, `{"n":1.0}`, `{"n":1e100}`, `{"n":123456789012345678901234567890}`,
		`{"n":-0}`, `{"n":0.1000}`,
	} {
		d := NewDecoder(newStringReader(in))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		got, err := Marshal(v)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		sd := stdjson.NewDecoder(newStringReader(in))
		sd.UseNumber()
		var sv any
		if err := sd.Decode(&sv); err != nil {
			t.Fatal(err)
		}
		want, _ := stdjson.Marshal(sv)
		if string(got) != string(want) {
			t.Errorf("%s\n got %s\nwant %s", in, got, want)
		}
	}
}

// Not-recognised types must still reach the compiled encoders.
func TestEncodeAnyFallsBack(t *testing.T) {
	type inner struct {
		X int `json:"x"`
	}
	vals := []any{
		inner{1},
		&inner{2},
		[]inner{{3}, {4}},
		map[string]inner{"k": {5}},
		map[string]int{"a": 1, "b": 2},
		[]string{"a", "b"},
		[2]int{1, 2},
		map[int]string{1: "a"},
		uint64(math.MaxUint64),
		float32(1.5),
		[]byte("bytes"),
	}
	for _, v := range vals {
		got, err := Marshal(v)
		if err != nil {
			t.Fatalf("%T: %v", v, err)
		}
		want, err := stdjson.Marshal(v)
		if err != nil {
			t.Fatalf("%T: stdlib %v", v, err)
		}
		if string(got) != string(want) {
			t.Errorf("%T\n got %s\nwant %s", v, got, want)
		}
	}
}

// Infinities and NaN inside an interface must be refused, as encoding/json does.
func TestEncodeAnyRejectsNonFinite(t *testing.T) {
	for _, v := range []any{
		math.Inf(1), math.Inf(-1), math.NaN(),
		map[string]any{"k": math.NaN()},
		[]any{1.0, math.Inf(1)},
	} {
		if _, err := Marshal(v); err == nil {
			t.Errorf("%v was accepted", v)
		}
		if _, err := stdjson.Marshal(v); err == nil {
			t.Errorf("stdlib accepted %v -- the test is wrong", v)
		}
	}
}

// Deeply nested maps exercise the shared key buffer's stack discipline: a
// nested map must not tread on the keys of the map containing it.
func TestEncodeAnyNestedKeyBuffer(t *testing.T) {
	build := func(depth int) any {
		var v any = map[string]any{"leaf": 1.0}
		for i := 0; i < depth; i++ {
			v = map[string]any{"z": v, "a": 1.0, "m": "x"}
		}
		return v
	}
	for _, depth := range []int{1, 2, 5, 20, 100} {
		v := build(depth)
		got, err := Marshal(v)
		if err != nil {
			t.Fatalf("depth %d: %v", depth, err)
		}
		want, err := stdjson.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("depth %d\n got %s\nwant %s", depth, got, want)
		}
	}
}

// Both encodings are supported and both must be right. Sorted is byte-identical
// to encoding/json; unsorted is valid JSON carrying exactly the same value, and
// is the only thing that can be asserted about it, because Go randomises map
// iteration on purpose.
func TestEncodeAnyBothOrderings(t *testing.T) {
	docs := []string{
		`{"z":1,"a":2,"m":3}`,
		`{"nested":{"z":1,"a":{"y":2,"b":3}},"top":[{"q":1,"p":2}]}`,
		`{"":0,"k":"v","n":null,"t":true,"f":1.5}`,
		`{"日本":1,"esc\"key":2,"a/b":3}`,
	}
	unsorted := Std
	unsorted.SortMapKeys = false

	for _, in := range docs {
		var v any
		if err := stdjson.Unmarshal([]byte(in), &v); err != nil {
			t.Fatal(err)
		}

		// Sorted: byte for byte.
		got, err := Marshal(v)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		want, _ := stdjson.Marshal(v)
		if string(got) != string(want) {
			t.Errorf("sorted %s\n got %s\nwant %s", in, got, want)
		}

		// Unsorted: valid, and the same value.
		un, err := unsorted.Marshal(v)
		if err != nil {
			t.Fatalf("%s unsorted: %v", in, err)
		}
		if !Valid(un) {
			t.Errorf("unsorted %s produced invalid JSON: %s", in, un)
		}
		var back any
		if err := stdjson.Unmarshal(un, &back); err != nil {
			t.Errorf("unsorted %s does not re-read: %v (%s)", in, err, un)
			continue
		}
		// Re-encoding what came back, sorted, must equal the sorted encoding —
		// which is a value comparison that does not care about key order.
		norm, _ := stdjson.Marshal(back)
		if string(norm) != string(want) {
			t.Errorf("unsorted %s lost or changed something\n got %s\nwant %s", in, norm, want)
		}
		// And unsorted output must have the same length as sorted: same keys,
		// same values, same escaping, only a different order.
		if len(un) != len(got) {
			t.Errorf("unsorted %s is %d bytes, sorted is %d", in, len(un), len(got))
		}
	}
}

// The fallback path has to honour SortMapKeys too, since a map[string]int does
// not go through encodeStringMap.
func TestEncodeReflectMapBothOrderings(t *testing.T) {
	m := map[string]int{"z": 1, "a": 2, "m": 3, "b": 4}
	got, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := stdjson.Marshal(m)
	if string(got) != string(want) {
		t.Errorf("sorted\n got %s\nwant %s", got, want)
	}
	o := Std
	o.SortMapKeys = false
	un, err := o.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !Valid(un) || len(un) != len(got) {
		t.Errorf("unsorted reflect map = %s", un)
	}
	var back map[string]int
	if err := stdjson.Unmarshal(un, &back); err != nil || len(back) != 4 || back["z"] != 1 {
		t.Errorf("unsorted reflect map round trip = %v, %v", back, err)
	}
}
