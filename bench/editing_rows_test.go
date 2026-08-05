package bench

import (
	"encoding/json"
	"testing"

	ours "github.com/sebishogun/simdjson"
	"github.com/tidwall/sjson"
)

// Editing rows: replace one value in a document without decoding the rest.
// Outputs are cross-checked semantically (both splice, but sjson may differ
// in whitespace around the spliced value, so equality is checked on the
// decoded documents, not the bytes).
func BenchmarkSetPathRow(b *testing.B) {
	data := loadCorpus(b, "twitter")
	cases := []struct {
		name  string
		path  string
		spath string
		val   any
	}{
		{"shallow", "search_metadata.count", "search_metadata.count", 42},
		{"deep", "statuses.50.user.screen_name", "statuses.50.user.screen_name", "replaced_name"},
	}
	for _, c := range cases {
		og, err := ours.SetPath(data, c.path, c.val)
		if err != nil {
			b.Fatal(err)
		}
		sg, err := sjson.SetBytes(data, c.spath, c.val)
		if err != nil {
			b.Fatal(err)
		}
		var a, s any
		if err := json.Unmarshal(og, &a); err != nil {
			b.Fatal(err)
		}
		if err := json.Unmarshal(sg, &s); err != nil {
			b.Fatal(err)
		}
		af, _ := json.Marshal(a)
		sf, _ := json.Marshal(s)
		if string(af) != string(sf) {
			b.Fatalf("%s: edits disagree", c.name)
		}
		b.Run(c.name+"/ours", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := ours.SetPath(data, c.path, c.val); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(c.name+"/sjson", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := sjson.SetBytes(data, c.spath, c.val); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
