package bench

import (
	"encoding/json"
	"testing"

	"github.com/bytedance/sonic"
	gojson "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
)

// What each encoder does with a string that is not valid UTF-8. encoding/json
// replaces the bad bytes with U+FFFD; anything that does not is skipping the
// validation, which is most of what encoding a unicode-heavy document costs.
func TestInvalidUTF8Encoding(t *testing.T) {
	type doc struct {
		S string `json:"s"`
	}
	v := doc{S: "ok" + string([]byte{0xff, 0xfe}) + "end"}

	std, _ := json.Marshal(v)
	t.Logf("stdlib     %q", std)
	o, _ := ours.Marshal(v)
	t.Logf("ours       %q  same=%v", o, string(o) == string(std))
	g, _ := gojson.Marshal(v)
	t.Logf("goccy      %q  same=%v", g, string(g) == string(std))
	s1, _ := sonic.ConfigStd.Marshal(v)
	t.Logf("sonic-std  %q  same=%v", s1, string(s1) == string(std))
	s2, _ := sonic.Marshal(v)
	t.Logf("sonic-dflt %q  same=%v", s2, string(s2) == string(std))
}
