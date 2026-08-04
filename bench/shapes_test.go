package bench

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"strings"
	"testing"

	sonic "github.com/bytedance/sonic"
	goccy "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
	fastjson "github.com/valyala/fastjson"
)

// Shapes, not sizes. Every one of these is about a megabyte, so what differs
// between them is structure: how deep, how wide, how much of the bytes are
// strings, how many escapes, how much whitespace, how much of it is ASCII.
func shapes() []struct {
	name string
	data []byte
} {
	var out []struct {
		name string
		data []byte
	}
	add := func(name string, b []byte) {
		out = append(out, struct {
			name string
			data []byte
		}{name, b})
	}
	const target = 1 << 20

	// Deeply nested: one path down, no breadth. The recursion depth a parser
	// has to survive.
	var deep bytes.Buffer
	depth := 500
	for i := 0; i < depth; i++ {
		deep.WriteString(`{"a":`)
	}
	deep.WriteString(`1`)
	for i := 0; i < depth; i++ {
		deep.WriteString(`}`)
	}
	one := deep.String()
	var deepAll bytes.Buffer
	deepAll.WriteByte('[')
	for deepAll.Len() < target {
		if deepAll.Len() > 1 {
			deepAll.WriteByte(',')
		}
		deepAll.WriteString(one)
	}
	deepAll.WriteByte(']')
	add("deep-nested", deepAll.Bytes())

	// Wide objects: many keys in one object, which is the case a field lookup
	// has to be fast at.
	var wide bytes.Buffer
	wide.WriteByte('{')
	for i := 0; wide.Len() < target; i++ {
		if i > 0 {
			wide.WriteByte(',')
		}
		fmt.Fprintf(&wide, `"field_number_%d":%d`, i, i)
	}
	wide.WriteByte('}')
	add("wide-object", wide.Bytes())

	// Long strings: most of the bytes are inside string literals.
	var long bytes.Buffer
	long.WriteByte('[')
	for i := 0; long.Len() < target; i++ {
		if i > 0 {
			long.WriteByte(',')
		}
		fmt.Fprintf(&long, `"%s"`, strings.Repeat("abcdefghij", 200))
	}
	long.WriteByte(']')
	add("long-strings", long.Bytes())

	// Escape-heavy: every string is mostly backslashes, which is the path that
	// cannot be a memmove.
	var esc bytes.Buffer
	esc.WriteByte('[')
	for i := 0; esc.Len() < target; i++ {
		if i > 0 {
			esc.WriteByte(',')
		}
		esc.WriteString(`"` + strings.Repeat(`a\"b\\c\nd\te`, 20) + `"`)
	}
	esc.WriteByte(']')
	add("escape-heavy", esc.Bytes())

	// Non-ASCII: the UTF-8 validator's real workload.
	var uni bytes.Buffer
	uni.WriteByte('[')
	for i := 0; uni.Len() < target; i++ {
		if i > 0 {
			uni.WriteByte(',')
		}
		fmt.Fprintf(&uni, `"%s"`, strings.Repeat("日本語のテキストです、これは。", 8))
	}
	uni.WriteByte(']')
	add("non-ascii", uni.Bytes())

	// Numbers only: no strings at all, so the number parser is the whole job.
	var nums bytes.Buffer
	nums.WriteByte('[')
	for i := 0; nums.Len() < target; i++ {
		if i > 0 {
			nums.WriteByte(',')
		}
		fmt.Fprintf(&nums, "%d.%d", i, i%1000)
	}
	nums.WriteByte(']')
	add("numbers", nums.Bytes())

	// Booleans and nulls: the shortest possible values, so the per-value
	// overhead is the whole cost.
	var lits bytes.Buffer
	lits.WriteByte('[')
	for i := 0; lits.Len() < target; i++ {
		if i > 0 {
			lits.WriteByte(',')
		}
		switch i % 3 {
		case 0:
			lits.WriteString("true")
		case 1:
			lits.WriteString("false")
		default:
			lits.WriteString("null")
		}
	}
	lits.WriteByte(']')
	add("literals", lits.Bytes())

	// Pretty-printed: the same document as wide-object, indented, so most of
	// the bytes are whitespace.
	var pretty bytes.Buffer
	_ = stdjson.Indent(&pretty, wide.Bytes(), "", "    ")
	add("pretty-printed", pretty.Bytes())

	// Empty containers: nothing but structure.
	var empties bytes.Buffer
	empties.WriteByte('[')
	for i := 0; empties.Len() < target; i++ {
		if i > 0 {
			empties.WriteByte(',')
		}
		empties.WriteString(`{"a":{},"b":[],"c":[{}]}`)
	}
	empties.WriteByte(']')
	add("empty-containers", empties.Bytes())

	return out
}

func BenchmarkShapes(b *testing.B) {
	for _, s := range shapes() {
		data := s.data
		// Prove every library agrees the document is valid before timing it.
		if !stdjson.Valid(data) {
			b.Fatalf("%s: generated invalid JSON", s.name)
		}
		b.Run(s.name+"/ours-Parse", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			var p ours.Parser
			for i := 0; i < b.N; i++ {
				if _, err := p.Parse(data); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(s.name+"/ours-Scan", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			var p ours.Parser
			for i := 0; i < b.N; i++ {
				if _, err := p.Scan(data); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(s.name+"/fastjson", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			var p fastjson.Parser
			for i := 0; i < b.N; i++ {
				if _, err := p.ParseBytes(data); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(s.name+"/ours-Valid", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				if !ours.Valid(data) {
					b.Fatal("invalid")
				}
			}
		})
		b.Run(s.name+"/sonic-Valid", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				if !sonic.Valid(data) {
					b.Fatal("invalid")
				}
			}
		})
		b.Run(s.name+"/goccy-Valid", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				if !goccy.Valid(data) {
					b.Fatal("invalid")
				}
			}
		})
		b.Run(s.name+"/stdlib-Valid", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				if !stdjson.Valid(data) {
					b.Fatal("invalid")
				}
			}
		})
	}
}

// The same shapes through Unmarshal into any, which is the path with no type
// information to help it.
func BenchmarkShapesUnmarshalAny(b *testing.B) {
	for _, s := range shapes() {
		if s.name == "deep-nested" {
			continue // 500 levels into any is a stack test, not a speed test
		}
		data := s.data
		b.Run(s.name+"/ours", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				var v any
				if err := ours.Unmarshal(data, &v); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(s.name+"/goccy", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				var v any
				if err := goccy.Unmarshal(data, &v); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(s.name+"/sonic", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				var v any
				if err := sonic.ConfigStd.Unmarshal(data, &v); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(s.name+"/stdlib", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				var v any
				if err := stdjson.Unmarshal(data, &v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
