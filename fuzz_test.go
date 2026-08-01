package simdjson

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Fuzzing against encoding/json. A parser's failures are the inputs nobody
// writes down, and the two properties worth asserting are cheap to state:
// this must accept exactly what the standard library accepts, and when both
// accept, the value must be the same.
func FuzzAgainstStdlib(f *testing.F) {
	for _, s := range []string{
		`{"a":1}`, `[1,2,3]`, `"x"`, `null`, `{"a":"\u00e9"}`,
		`{"a":"}{"}`, `{"a":"\\"}`, `{"nested":{"deep":[{"x":1}]}}`,
		`{`, `[1,`, `"`, `tru`, `{"a":}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		// json.Valid is the oracle for accept-or-reject, not Unmarshal.
		// Unmarshal also converts, and it rejects 1E700 — which is valid JSON
		// that does not fit a float64. That is a conversion limit rather than a
		// syntax rule, and comparing against it made the fuzzer report a bug
		// that was in this test.
		valid := json.Valid(data)
		doc, gotErr := Parse(data)
		if valid != (gotErr == nil) {
			t.Fatalf("input %q: json.Valid=%v, simdjson err=%v", data, valid, gotErr)
		}
		if !valid {
			return
		}
		var want any
		if json.Unmarshal(data, &want) != nil {
			return // representable as JSON, not as a Go value
		}
		got := toGo(doc.Root())
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("input %q:\n got %#v\nwant %#v", data, got, want)
		}
	})
}
