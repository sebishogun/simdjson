package bench

import (
	"encoding/json"
	"testing"

	"github.com/bytedance/sonic"
	gojson "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
)

// Do the encoders actually produce the same bytes? A benchmark against one that
// does less is not a benchmark.
func TestEncodersAgree(t *testing.T) {
	type doc struct {
		S string  `json:"s"`
		N float64 `json:"n"`
		U string  `json:"u"`
	}
	v := doc{S: `a<b>c&d "quoted" \slash`, N: 1e21, U: "café  line"}

	std, _ := json.Marshal(v)
	got := map[string][]byte{}
	got["ours"], _ = ours.Marshal(v)
	got["goccy"], _ = gojson.Marshal(v)
	got["sonic"], _ = sonic.Marshal(v)
	got["sonic-std"], _ = sonic.ConfigStd.Marshal(v)

	t.Logf("stdlib: %s", std)
	for _, name := range []string{"ours", "goccy", "sonic", "sonic-std"} {
		if string(got[name]) == string(std) {
			t.Logf("%-6s IDENTICAL", name)
		} else {
			t.Logf("%-6s DIFFERS: %s", name, got[name])
		}
	}
}
