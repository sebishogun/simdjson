package bench

import (
	"encoding/json"
	"testing"

	"github.com/bytedance/sonic"
	gojson "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
)

func BenchmarkMarshalStruct(b *testing.B) {
	data := loadCorpus(b, "twitter")
	var v tSearch
	if err := json.Unmarshal(data, &v); err != nil {
		b.Fatal(err)
	}
	// The encoders must agree before any of the numbers mean anything.
	a, err := ours.Marshal(v)
	if err != nil {
		b.Fatal(err)
	}
	c, err := json.Marshal(v)
	if err != nil {
		b.Fatal(err)
	}
	if string(a) != string(c) {
		b.Fatalf("output differs:\n ours=%.200s\n std =%.200s", a, c)
	}
	b.Logf("encoded size %d bytes", len(a))

	// Std.Marshal and Marshal are the same operation and must cost the same.
	// They did not: Options.Marshal copied the finished buffer where the
	// package-level Marshal encodes straight into the one it returns.
	b.Run("ours-Std.Marshal", func(b *testing.B) {
		b.SetBytes(int64(len(a)))
		for b.Loop() {
			if _, err := ours.Std.Marshal(v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ours", func(b *testing.B) {
		b.SetBytes(int64(len(a)))
		for b.Loop() {
			if _, err := ours.Marshal(v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ours-MarshalTo", func(b *testing.B) {
		buf := make([]byte, 0, len(a))
		b.SetBytes(int64(len(a)))
		for b.Loop() {
			var err error
			buf, err = ours.MarshalTo(buf[:0], v)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	// The options, one at a time, all through MarshalTo so the allocation is
	// held constant. "ours" allocates and "ours-Fast" does not, so the gap
	// between those two is the options AND the allocation together, which is
	// not a decomposition of anything.
	for _, o := range []struct {
		name string
		opt  ours.Options
	}{
		{"opts=none", ours.Options{SortMapKeys: true}},
		{"opts=html", ours.Options{SortMapKeys: true, EscapeHTML: true}},
		{"opts=validate", ours.Options{SortMapKeys: true, ValidateStrings: true}},
		{"opts=both", ours.Options{SortMapKeys: true, EscapeHTML: true, ValidateStrings: true}},
	} {
		opt := o.opt
		b.Run("ours-"+o.name, func(b *testing.B) {
			buf := make([]byte, 0, len(a))
			b.SetBytes(int64(len(a)))
			for b.Loop() {
				var err error
				buf, err = opt.MarshalTo(buf[:0], v)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	b.Run("ours-Fast", func(b *testing.B) {
		buf := make([]byte, 0, len(a))
		b.SetBytes(int64(len(a)))
		for b.Loop() {
			var err error
			buf, err = ours.Fast.MarshalTo(buf[:0], v)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("stdlib", func(b *testing.B) {
		b.SetBytes(int64(len(a)))
		for b.Loop() {
			if _, err := json.Marshal(v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("goccy", func(b *testing.B) {
		b.SetBytes(int64(len(a)))
		for b.Loop() {
			if _, err := gojson.Marshal(v); err != nil {
				b.Fatal(err)
			}
		}
	})
	// sonic's default config does not HTML-escape and passes U+2028 through, so
	// it is not producing the same bytes. ConfigStd is its stdlib-compatible
	// mode and is the one to compare against.
	b.Run("sonic-std", func(b *testing.B) {
		s, err := sonic.ConfigStd.Marshal(v)
		if err != nil {
			b.Fatal(err)
		}
		if string(s) != string(c) {
			b.Fatal("sonic ConfigStd still differs from encoding/json")
		}
		b.SetBytes(int64(len(a)))
		for b.Loop() {
			if _, err := sonic.ConfigStd.Marshal(v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("sonic-default-NOT-comparable", func(b *testing.B) {
		b.SetBytes(int64(len(a)))
		for b.Loop() {
			if _, err := sonic.Marshal(v); err != nil {
				b.Fatal(err)
			}
		}
	})
}
