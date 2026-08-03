package simdjson

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The four text operations are checked the same way the rest of the package is:
// against encoding/json, on the same input, demanding the same answer. For
// Valid that is a bool; for the other three it is the exact bytes, because
// "compacted" and "indented" are not judgement calls — callers diff them.

var textCases = []string{
	``, ` `, `null`, `true`, `1`, `-0.5e10`, `"x"`, `""`,
	`{}`, `[]`, `{ }`, `[ ]`, `[[]]`, `{"a":{}}`, `{"a":[]}`,
	`{"a":1}`, ` { "a" : 1 } `, "{\n\t\"a\": [1, 2, 3]\n}",
	`[1,2,3]`, `[{"a":1},{"b":2}]`, `{"a":{"b":{"c":[1,{"d":null}]}}}`,
	`{"s":" spaces  inside \" a string \\ "}`,
	`{"s":"tab\tnewline\nbrace{bracket[comma,colon:"}`,
	`{"unicode":"é é 日本語 🎉"}`,
	`{"html":"<script>&amp;</script>"}`,
	"{\"lineterm\":\"  \"}",
	// Invalid, where the answer is an error and an untouched destination.
	`{`, `}`, `[`, `{"a"}`, `{"a":}`, `{"a":1,}`, `[1,]`, `{}extra`,
	`01`, `1.`, `+1`, `"unterminated`, `tru`, `nul`, `[1 2]`, `{"a" 1}`,
	`"\q"`, "\"\x01\"", `{"a":1}{"b":2}`,
}

func TestValidMatchesStdlib(t *testing.T) {
	for _, s := range textCases {
		if got, want := Valid([]byte(s)), json.Valid([]byte(s)); got != want {
			t.Errorf("Valid(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestCompactMatchesStdlib(t *testing.T) {
	for _, s := range textCases {
		var got, want bytes.Buffer
		gErr := Compact(&got, []byte(s))
		wErr := json.Compact(&want, []byte(s))
		if (gErr != nil) != (wErr != nil) {
			t.Errorf("Compact(%q) error = %v, stdlib = %v", s, gErr, wErr)
			continue
		}
		if wErr == nil && got.String() != want.String() {
			t.Errorf("Compact(%q) = %q, want %q", s, got.String(), want.String())
		}
	}
}

func TestIndentMatchesStdlib(t *testing.T) {
	for _, pre := range []string{"", "  ", "\t"} {
		for _, ind := range []string{"", " ", "  ", "\t"} {
			for _, s := range textCases {
				var got, want bytes.Buffer
				gErr := Indent(&got, []byte(s), pre, ind)
				wErr := json.Indent(&want, []byte(s), pre, ind)
				if (gErr != nil) != (wErr != nil) {
					t.Errorf("Indent(%q,%q,%q) error = %v, stdlib = %v", s, pre, ind, gErr, wErr)
					continue
				}
				if wErr == nil && got.String() != want.String() {
					t.Errorf("Indent(%q,%q,%q) =\n%q\nwant\n%q", s, pre, ind, got.String(), want.String())
				}
			}
		}
	}
}

func TestHTMLEscapeMatchesStdlib(t *testing.T) {
	for _, s := range textCases {
		var got, want bytes.Buffer
		HTMLEscape(&got, []byte(s))
		json.HTMLEscape(&want, []byte(s))
		if got.String() != want.String() {
			t.Errorf("HTMLEscape(%q) = %q, want %q", s, got.String(), want.String())
		}
	}
}

func TestMarshalIndentMatchesStdlib(t *testing.T) {
	vals := []any{
		map[string]any{"a": 1, "b": []any{1, 2}},
		[]any{},
		map[string]any{},
		struct {
			A int      `json:"a"`
			B []string `json:"b"`
		}{1, []string{"x", "y"}},
	}
	for _, v := range vals {
		got, gErr := MarshalIndent(v, "", "  ")
		want, wErr := json.MarshalIndent(v, "", "  ")
		if (gErr != nil) != (wErr != nil) {
			t.Errorf("MarshalIndent(%v) error = %v, stdlib = %v", v, gErr, wErr)
			continue
		}
		if wErr == nil && !bytes.Equal(got, want) {
			t.Errorf("MarshalIndent(%v) =\n%s\nwant\n%s", v, got, want)
		}
	}
}

func FuzzTextOpsAgainstStdlib(f *testing.F) {
	for _, s := range textCases {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if got, want := Valid(data), json.Valid(data); got != want {
			t.Fatalf("Valid(%q) = %v, want %v", data, got, want)
		}

		var got, want bytes.Buffer
		gErr := Compact(&got, data)
		wErr := json.Compact(&want, data)
		if (gErr != nil) != (wErr != nil) {
			t.Fatalf("Compact(%q) error = %v, stdlib = %v", data, gErr, wErr)
		}
		if wErr == nil && !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("Compact(%q) = %q, want %q", data, got.Bytes(), want.Bytes())
		}

		got.Reset()
		want.Reset()
		gErr = Indent(&got, data, ">", " ")
		wErr = json.Indent(&want, data, ">", " ")
		if (gErr != nil) != (wErr != nil) {
			t.Fatalf("Indent(%q) error = %v, stdlib = %v", data, gErr, wErr)
		}
		if wErr == nil && !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("Indent(%q) =\n%q\nwant\n%q", data, got.Bytes(), want.Bytes())
		}

		got.Reset()
		want.Reset()
		HTMLEscape(&got, data)
		json.HTMLEscape(&want, data)
		if !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("HTMLEscape(%q) = %q, want %q", data, got.Bytes(), want.Bytes())
		}
	})
}
