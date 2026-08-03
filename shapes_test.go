package simdjson

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Shapes, not sizes.
//
// The three files everyone benchmarks against — twitter, citm, canada — are
// three points in a space with many more dimensions than size. Between them
// they cover "strings and objects", "objects and whitespace" and "numbers", and
// nothing else: no deep nesting, no wide objects, no long strings, no escapes
// to speak of, no non-ASCII outside a few tweets, no empty containers.
//
// A library tuned on three documents is fast on three documents. These are each
// about a megabyte and differ only in structure, so what they measure is the
// part of the cost that comes from shape.

const shapeTarget = 1 << 20

func shapeCorpus() map[string][]byte {
	m := map[string][]byte{}

	// One path straight down, no breadth: how much a level of nesting costs,
	// and whether the descent survives 500 of them.
	var one bytes.Buffer
	const depth = 500
	for i := 0; i < depth; i++ {
		one.WriteString(`{"a":`)
	}
	one.WriteString(`1`)
	for i := 0; i < depth; i++ {
		one.WriteByte('}')
	}
	m["deep-nested"] = repeatInArray(one.String())

	// Many keys in one object: the case a field lookup has to be fast at, and
	// the one where a linear scan over field names stops being acceptable.
	var wide bytes.Buffer
	wide.WriteByte('{')
	for i := 0; wide.Len() < shapeTarget; i++ {
		if i > 0 {
			wide.WriteByte(',')
		}
		fmt.Fprintf(&wide, `"field_number_%d":%d`, i, i)
	}
	wide.WriteByte('}')
	m["wide-object"] = wide.Bytes()

	// Most of the bytes inside string literals, where the index is building
	// masks over bytes nothing will ever look at.
	m["long-strings"] = repeatInArray(`"` + strings.Repeat("abcdefghij", 200) + `"`)

	// Strings that are mostly escapes: the decode path that cannot be a copy.
	m["escape-heavy"] = repeatInArray(`"` + strings.Repeat(`a\"b\\c\nd\te`, 20) + `"`)

	// Text that is not ASCII, which is what a UTF-8 validator is for.
	m["non-ascii"] = repeatInArray(`"` + strings.Repeat("日本語のテキストです、これは。", 8) + `"`)

	// No strings at all, so the number scanner is the whole job.
	var nums bytes.Buffer
	nums.WriteByte('[')
	for i := 0; nums.Len() < shapeTarget; i++ {
		if i > 0 {
			nums.WriteByte(',')
		}
		fmt.Fprintf(&nums, "%d.%d", i, i%1000)
	}
	nums.WriteByte(']')
	m["numbers"] = nums.Bytes()

	// The shortest values there are, so per-value overhead is the whole cost.
	var lits bytes.Buffer
	lits.WriteByte('[')
	for i := 0; lits.Len() < shapeTarget; i++ {
		if i > 0 {
			lits.WriteByte(',')
		}
		lits.WriteString([...]string{"true", "false", "null"}[i%3])
	}
	lits.WriteByte(']')
	m["literals"] = lits.Bytes()

	// Mostly whitespace, which is what a document written for a human is.
	var pretty bytes.Buffer
	if err := stdjson.Indent(&pretty, m["wide-object"], "", "    "); err == nil {
		m["pretty-printed"] = pretty.Bytes()
	}

	// Nothing but structure: every bracket is a bracket the index must pair.
	m["empty-containers"] = repeatInArray(`{"a":{},"b":[],"c":[{}]}`)

	return m
}

func repeatInArray(unit string) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	for b.Len() < shapeTarget {
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		b.WriteString(unit)
	}
	b.WriteByte(']')
	return b.Bytes()
}

// Every shape must give encoding/json's answer, for every entry point. A shape
// that is only benchmarked and never checked is a shape where being fast proves
// nothing.
func TestShapesMatchStdlib(t *testing.T) {
	for name, data := range shapeCorpus() {
		t.Run(name, func(t *testing.T) {
			if !stdjson.Valid(data) {
				t.Fatalf("the generator produced invalid JSON")
			}
			if !Valid(data) {
				t.Error("Valid says no and encoding/json says yes")
			}
			if _, err := Parse(data); err != nil {
				t.Errorf("Parse: %v", err)
			}
			var got, want bytes.Buffer
			if err := Compact(&got, data); err != nil {
				t.Fatalf("Compact: %v", err)
			}
			if err := stdjson.Compact(&want, data); err != nil {
				t.Fatalf("stdlib Compact: %v", err)
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Error("Compact differs from encoding/json")
			}
			got.Reset()
			want.Reset()
			if err := Indent(&got, data, "", " "); err != nil {
				t.Fatalf("Indent: %v", err)
			}
			if err := stdjson.Indent(&want, data, "", " "); err != nil {
				t.Fatalf("stdlib Indent: %v", err)
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Error("Indent differs from encoding/json")
			}
			if name == "deep-nested" {
				return // 500 levels into an any is a stack test, not this test
			}
			var g, w any
			if err := Unmarshal(data, &g); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if err := stdjson.Unmarshal(data, &w); err != nil {
				t.Fatalf("stdlib Unmarshal: %v", err)
			}
			gb, _ := stdjson.Marshal(g)
			wb, _ := stdjson.Marshal(w)
			if !bytes.Equal(gb, wb) {
				t.Error("Unmarshal into any differs from encoding/json")
			}
		})
	}
}

func BenchmarkShapes(b *testing.B) {
	for name, data := range shapeCorpus() {
		b.Run(name+"/Scan", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			var p Parser
			for i := 0; i < b.N; i++ {
				if _, err := p.Scan(data); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/Parse", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			var p Parser
			for i := 0; i < b.N; i++ {
				if _, err := p.Parse(data); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/Valid", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				if !Valid(data) {
					b.Fatal("invalid")
				}
			}
		})
		b.Run(name+"/Compact", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			var buf bytes.Buffer
			for i := 0; i < b.N; i++ {
				buf.Reset()
				if err := Compact(&buf, data); err != nil {
					b.Fatal(err)
				}
			}
		})
		if name == "deep-nested" {
			continue
		}
		b.Run(name+"/UnmarshalAny", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				var v any
				if err := Unmarshal(data, &v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
