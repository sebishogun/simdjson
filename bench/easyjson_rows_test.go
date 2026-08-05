package bench

// The codegen-class rows. Every call's error is checked and every decode is
// cross-checked against encoding/json before a number counts — the published
// comparison this replaces did neither, and its easyjson column was one
// object marshaled and an unchecked instant error unmarshaled (see the
// research notes in the repo history). easyjson matches keys
// case-sensitively, unlike stdlib v1; the corpora exercise exact-case keys,
// so the rows are comparable.

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/mailru/easyjson"
	ours "github.com/sebishogun/simdjson"
)

func BenchmarkEasyjson(b *testing.B) {
	twitter := loadCorpus(b, "twitter")
	canada := loadCorpus(b, "canada")

	var ej ejSearch
	if err := easyjson.Unmarshal(twitter, &ej); err != nil {
		b.Fatal(err)
	}
	var std tSearch
	if err := json.Unmarshal(twitter, &std); err != nil {
		b.Fatal(err)
	}
	if len(ej.Statuses) != len(std.Statuses) ||
		ej.Statuses[0].Text != std.Statuses[0].Text ||
		ej.Statuses[0].User.ScreenName != std.Statuses[0].User.ScreenName {
		b.Fatal("easyjson and stdlib disagree on twitter")
	}

	b.Run("unmarshal-twitter", func(b *testing.B) {
		b.SetBytes(int64(len(twitter)))
		for b.Loop() {
			var v ejSearch
			if err := easyjson.Unmarshal(twitter, &v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("unmarshal-twitter-ours", func(b *testing.B) {
		// tSearch, not ejSearch: the generated UnmarshalJSON on the ej types
		// captures ANY drop-in library into easyjson's own code path, so the
		// row would measure easyjson through our plumbing. Identical tags,
		// no methods.
		b.SetBytes(int64(len(twitter)))
		for b.Loop() {
			var v tSearch
			if err := ours.Unmarshal(twitter, &v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("unmarshal-canada", func(b *testing.B) {
		var chk, chk2 ejCanada
		if err := easyjson.Unmarshal(canada, &chk); err != nil {
			b.Fatal(err)
		}
		if err := json.Unmarshal(canada, &chk2); err != nil {
			b.Fatal(err)
		}
		if !reflect.DeepEqual(chk, chk2) {
			b.Fatal("easyjson and stdlib disagree on canada")
		}
		b.SetBytes(int64(len(canada)))
		for b.Loop() {
			var v ejCanada
			if err := easyjson.Unmarshal(canada, &v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("unmarshal-canada-ours", func(b *testing.B) {
		b.SetBytes(int64(len(canada)))
		for b.Loop() {
			var v canadaFC
			if err := ours.Unmarshal(canada, &v); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("marshal-twitter", func(b *testing.B) {
		out, err := easyjson.Marshal(&ej)
		if err != nil {
			b.Fatal(err)
		}
		wantOut, err := json.Marshal(&std)
		if err != nil {
			b.Fatal(err)
		}
		if string(out) != string(wantOut) {
			b.Fatal("easyjson marshal output differs from encoding/json")
		}
		b.SetBytes(int64(len(out)))
		for b.Loop() {
			if _, err := easyjson.Marshal(&ej); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("marshal-twitter-ours", func(b *testing.B) {
		out, err := ours.Marshal(&std)
		if err != nil {
			b.Fatal(err)
		}
		b.SetBytes(int64(len(out)))
		for b.Loop() {
			if _, err := ours.Marshal(&std); err != nil {
				b.Fatal(err)
			}
		}
	})
}
