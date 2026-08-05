package simdjson

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

// TestCompiledArrayMatchesStdlib pins the compiled [N]T decoder to
// encoding/json on every rule that differs from slices: excess discarded,
// missing zeroed, null leaves the value unchanged.
func TestCompiledArrayMatchesStdlib(t *testing.T) {
	type inner struct {
		P [2]float64 `json:"p"`
	}
	cases := []struct {
		name string
		in   string
		mk   func() any
	}{
		{"exact", `[1.5,-2.25]`, func() any { return &[2]float64{} }},
		{"short", `[7]`, func() any { return &[3]int{9, 9, 9} }},
		{"empty", `[]`, func() any { return &[2]string{"a", "b"} }},
		{"excess", `[1,2,3,4,5]`, func() any { return &[3]int{} }},
		{"null", `null`, func() any { return &[2]float64{3.5, 4.5} }},
		{"strings", `["x","y",null]`, func() any { return &[3]string{"a", "b", "c"} }},
		{"bytes", `[1,2,255]`, func() any { return &[3]uint8{} }},
		{"nested", `[[1,2],[3,4]]`, func() any { return &[2][2]float64{} }},
		{"inStruct", `{"p":[-65.613617,43.420273]}`, func() any { return &inner{} }},
		{"structElems", `[{"p":[1,2]},{"p":[3,4]}]`, func() any { return &[2]inner{} }},
		{"elemNull", `[null,8]`, func() any { return &[2]int{5, 6} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, want := tc.mk(), tc.mk()
			gerr := Unmarshal([]byte(tc.in), got)
			werr := json.Unmarshal([]byte(tc.in), want)
			if (gerr == nil) != (werr == nil) {
				t.Fatalf("error mismatch: ours %v, stdlib %v", gerr, werr)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("value mismatch:\n ours   %+v\n stdlib %+v", got, want)
			}
		})
	}

	// Errors: excess elements must still be malformed-checked, and a wrong
	// element type errors like stdlib does (position aside).
	for _, bad := range []string{`[1,2,3,x]`, `["s"]`, `[1,2`, `[1,,2]`} {
		var a [3]int
		if err := Unmarshal([]byte(bad), &a); err == nil {
			var b [3]int
			if serr := json.Unmarshal([]byte(bad), &b); serr != nil {
				t.Fatalf("%q: ours accepted what stdlib rejects (%v)", bad, serr)
			}
		}
	}

	// Randomized cross-check over lengths 0..6 into [4]float64.
	for n := 0; n <= 6; n++ {
		in := "["
		for i := 0; i < n; i++ {
			if i > 0 {
				in += ","
			}
			in += fmt.Sprintf("%d.%d", i, i)
		}
		in += "]"
		got, want := [4]float64{9, 9, 9, 9}, [4]float64{9, 9, 9, 9}
		if err := Unmarshal([]byte(in), &got); err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if err := json.Unmarshal([]byte(in), &want); err != nil {
			t.Fatalf("n=%d stdlib: %v", n, err)
		}
		if got != want {
			t.Fatalf("n=%d: %v != %v", n, got, want)
		}
	}
}
