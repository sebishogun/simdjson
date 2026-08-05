package simdjson

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestLeafSliceMatchesStdlib pins the one-pass leaf-slice decoder to
// encoding/json across allocation reuse, growth boundaries, emptiness and
// null.
func TestLeafSliceMatchesStdlib(t *testing.T) {
	mkN := func(n int) string {
		var sb strings.Builder
		sb.WriteString("[")
		for i := 0; i < n; i++ {
			if i > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, "%d.25", i)
		}
		sb.WriteString("]")
		return sb.String()
	}
	// Growth boundaries around the initial 8 and each doubling.
	for _, n := range []int{0, 1, 7, 8, 9, 15, 16, 17, 31, 32, 33, 100} {
		in := mkN(n)
		var got, want []float64
		if err := Unmarshal([]byte(in), &got); err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if err := json.Unmarshal([]byte(in), &want); err != nil {
			t.Fatalf("n=%d stdlib: %v", n, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("n=%d: %v != %v", n, got, want)
		}
		if (got == nil) != (want == nil) {
			t.Fatalf("n=%d: nil-ness differs: ours %v stdlib %v", n, got == nil, want == nil)
		}
	}
	// Reuse: a big backing array shrinks by len, stale tail invisible.
	got := make([]float64, 50)
	want := make([]float64, 50)
	for i := range got {
		got[i], want[i] = 9, 9
	}
	if err := Unmarshal([]byte(`[1.5,2.5]`), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`[1.5,2.5]`), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reuse: %v != %v", got, want)
	}
	// null and strings and ints.
	var ns []int64
	if err := Unmarshal([]byte(`null`), &ns); err != nil || ns != nil {
		t.Fatalf("null: %v %v", ns, err)
	}
	var ss []string
	if err := Unmarshal([]byte(`["a","b",null,"c"]`), &ss); err != nil {
		t.Fatal(err)
	}
	var wss []string
	_ = json.Unmarshal([]byte(`["a","b",null,"c"]`), &wss)
	if !reflect.DeepEqual(ss, wss) {
		t.Fatalf("strings: %v != %v", ss, wss)
	}
	// Errors agree on existence.
	for _, bad := range []string{`[1,]`, `[1,,2]`, `[1 2]`, `[1`, `["x"]`} {
		var a []float64
		gerr := Unmarshal([]byte(bad), &a)
		var b []float64
		serr := json.Unmarshal([]byte(bad), &b)
		if (gerr == nil) != (serr == nil) {
			t.Fatalf("%q: ours err=%v, stdlib err=%v", bad, gerr, serr)
		}
	}
}
