package bench

import (
	"encoding/json"
	"reflect"
	"testing"
)

// How many escapes does a string actually contain?
//
// The proposed kernel writes escapes inline instead of stopping at each one and
// returning to Go. That is worth something only in proportion to how often it
// stops. A corpus whose strings have no escapes at all would see no change,
// and the kernel would be weeks of work for nothing -- which is what the last
// three "obvious" changes turned out to be.
func TestEscapeDensity(t *testing.T) {
	data := loadCorpus(t, "twitter")
	var v tSearch
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	hist := map[int]int{}
	var strings, totalEsc, totalBytes int
	var walk func(reflect.Value)
	walk = func(rv reflect.Value) {
		switch rv.Kind() {
		case reflect.String:
			s := rv.String()
			strings++
			totalBytes += len(s)
			n := 0
			for i := 0; i < len(s); i++ {
				c := s[i]
				if c < 0x20 || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' || c == 0xE2 {
					n++
				}
			}
			totalEsc += n
			switch {
			case n == 0:
				hist[0]++
			case n == 1:
				hist[1]++
			case n <= 3:
				hist[3]++
			case n <= 10:
				hist[10]++
			default:
				hist[99]++
			}
		case reflect.Struct:
			for i := 0; i < rv.NumField(); i++ {
				walk(rv.Field(i))
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < rv.Len(); i++ {
				walk(rv.Index(i))
			}
		}
	}
	walk(reflect.ValueOf(v))
	pct := func(n int) float64 { return 100 * float64(n) / float64(strings) }
	t.Logf("%d strings, %d bytes, %d escape bytes (%.2f%% of bytes)",
		strings, totalBytes, totalEsc, 100*float64(totalEsc)/float64(totalBytes))
	t.Logf("  0 escapes:    %4d strings (%.1f%%)  <- kernel returns once, nothing to gain", hist[0], pct(hist[0]))
	t.Logf("  1 escape:     %4d strings (%.1f%%)", hist[1], pct(hist[1]))
	t.Logf("  2-3 escapes:  %4d strings (%.1f%%)", hist[3], pct(hist[3]))
	t.Logf("  4-10:         %4d strings (%.1f%%)", hist[10], pct(hist[10]))
	t.Logf("  11+:          %4d strings (%.1f%%)", hist[99], pct(hist[99]))
	t.Logf("round-trips saved per string, average: %.2f", float64(totalEsc)/float64(strings))
}
