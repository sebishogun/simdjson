package simdjson

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// TestMarshalIndentMatchesStdlib pins the trusted path (indentTrusted) byte-for-byte to
// encoding/json.MarshalIndent across value shapes and indent variants —
// including the shapes whose layout has edge cases: empty containers, deep
// nesting, escapes, and every scalar kind.
func TestMarshalIndentTrustedMatchesStdlib(t *testing.T) {
	type inner struct {
		S []int          `json:"s"`
		M map[string]int `json:"m,omitempty"`
	}
	type rec struct {
		Name  string  `json:"name"`
		N     float64 `json:"n"`
		OK    bool    `json:"ok"`
		Tags  []string
		In    inner
		Empty []int
		Nil   *rec `json:"nil"`
	}
	rng := rand.New(rand.NewSource(53))
	values := []any{
		map[string]any{},
		[]any{},
		[]any{1, "two", nil, true, []any{}, map[string]any{}},
		rec{Name: "a \"quoted\" name\nline", N: -0.5, Tags: []string{"x"},
			In: inner{S: []int{1, 2, 3}, M: map[string]int{"k": 1}}},
		map[string]any{"deep": []any{[]any{[]any{[]any{"end"}}}}},
		"just a string with é and \t",
		3.14159, nil, true,
	}
	for i := 0; i < 100; i++ {
		var sb strings.Builder
		fmt.Fprintf(&sb, `{"i":%d,"s":"v%d","a":[%d,%d],"o":{"x":%g}}`,
			i, rng.Intn(100), rng.Intn(10), rng.Intn(10), rng.Float64())
		var m any
		if err := json.Unmarshal([]byte(sb.String()), &m); err != nil {
			t.Fatal(err)
		}
		values = append(values, m)
	}
	variants := [][2]string{{"", "  "}, {"", "\t"}, {">>", " "}, {"", ""}, {"p", ""}}
	for _, v := range values {
		for _, pv := range variants {
			want, werr := json.MarshalIndent(v, pv[0], pv[1])
			got, gerr := MarshalIndent(v, pv[0], pv[1])
			if (werr == nil) != (gerr == nil) {
				t.Fatalf("%T %q: error mismatch %v vs %v", v, pv, gerr, werr)
			}
			if string(got) != string(want) {
				t.Fatalf("%T prefix=%q indent=%q:\n ours %s\n std  %s", v, pv[0], pv[1], got, want)
			}
		}
	}
}
