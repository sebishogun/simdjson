package simdjson_test

import (
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/sebishogun/simdjson"
)

type mInner struct {
	X int    `json:"x"`
	Y string `json:"y,omitempty"`
}

type mEmbedded struct {
	E1 int `json:"e1"`
	E2 int
}

type mWide struct {
	mEmbedded
	Str     string         `json:"str"`
	Num     int            `json:"num"`
	Big     int64          `json:"big"`
	U       uint32         `json:"u"`
	F       float64        `json:"f"`
	F32     float32        `json:"f32"`
	B       bool           `json:"b"`
	Ptr     *mInner        `json:"ptr"`
	Slice   []int          `json:"slice"`
	StrSl   []string       `json:"strsl"`
	Arr     [3]int         `json:"arr"`
	Map     map[string]int `json:"map"`
	MapAny  map[string]any `json:"mapany"`
	MapInt  map[int]string `json:"mapint"`
	Nested  mInner         `json:"nested"`
	Any     any            `json:"any"`
	Bytes   []byte         `json:"bytes"`
	Quoted  int            `json:"quoted,string"`
	Omit    string         `json:"omit,omitempty"`
	OmitInt int            `json:"omitint,omitempty"`
	Skipped int            `json:"-"`
	NoTag   string
	unexp   int
}

// Byte-identical output, which is what a drop-in replacement has to mean.
func TestMarshalMatchesStdlib(t *testing.T) {
	vals := []any{
		mWide{},
		mWide{Str: "hello", Num: 42, B: true, F: 1.5},
		mWide{Str: "\u00e9\n\t\"quoted\" <b>&</b>"},
		mWide{Ptr: &mInner{X: 1, Y: "z"}},
		mWide{Slice: []int{1, 2, 3}, Arr: [3]int{9, 8, 7}},
		mWide{StrSl: []string{"a", `b"c`, "\\", "\u2028line"}},
		mWide{Map: map[string]int{"b": 2, "a": 1, "c": 3}},
		mWide{MapInt: map[int]string{3: "c", 1: "a", 2: "b"}},
		mWide{MapAny: map[string]any{"z": []any{1.0, nil, true}, "a": "x"}},
		mWide{Nested: mInner{X: 5}},
		mWide{Any: map[string]any{"deep": []any{1.0, 2.0, map[string]any{"k": true}}}},
		mWide{Bytes: []byte("hello")},
		mWide{Quoted: 123},
		mWide{Omit: "kept", OmitInt: 7},
		mWide{mEmbedded: mEmbedded{E1: 1, E2: 2}},
		mWide{NoTag: "v"},
		mWide{F: 1e21, F32: 3.4e38},
		mWide{F: 1e-7, Big: 9007199254740993},
		map[string]any{"a": 1.0, "b": "two"},
		[]any{1.0, "a", nil, true},
		"plain",
		42,
		3.14,
		true,
		nil,
		[]int(nil),
		map[string]int(nil),
	}
	for i, v := range vals {
		got, gotErr := simdjson.Marshal(v)
		want, wantErr := json.Marshal(v)
		if (gotErr == nil) != (wantErr == nil) {
			t.Errorf("case %d: simdjson err = %v, stdlib err = %v", i, gotErr, wantErr)
			continue
		}
		if wantErr != nil {
			continue
		}
		if string(got) != string(want) {
			t.Errorf("case %d:\n  simdjson = %s\n  stdlib   = %s", i, got, want)
		}
	}
}

// Strings are where the escaping lives, so they get their own sweep.
func TestMarshalStringsMatchStdlib(t *testing.T) {
	cases := []string{
		"", "plain", `has "quotes"`, `back\slash`, "tab\there", "nl\nhere",
		"cr\rhere", "ctrl\x00\x01\x1f", "<html>&amp;</html>", "\u2028\u2029",
		"\u00e9\u00e8\u00ea", "\U0001F600", "mixed \"a\" <b> &c\n\x07",
		string([]byte{0xff, 0xfe}), "ends with backslash\\",
		strings.Repeat("a", 100) + `"` + strings.Repeat("b", 100),
		strings.Repeat("\u00e9", 50),
	}
	for _, s := range cases {
		got, err := simdjson.Marshal(s)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		want, _ := json.Marshal(s)
		if string(got) != string(want) {
			t.Errorf("%q:\n  simdjson = %s\n  stdlib   = %s", s, got, want)
		}
	}
}

func TestMarshalFuzzyAgainstStdlib(t *testing.T) {
	r := rand.New(rand.NewPCG(53, 59))
	atoms := []string{"", "a", `"`, `\`, "\n", "\x00", "<", ">", "&", "\u00e9",
		"\u2028", "\U0001F600", string([]byte{0x80}), "z"}
	for trial := 0; trial < 4000; trial++ {
		var v mWide
		var sb strings.Builder
		for i := 0; i < r.IntN(6); i++ {
			sb.WriteString(atoms[r.IntN(len(atoms))])
		}
		v.Str = sb.String()
		v.Num = r.IntN(1000) - 500
		v.F = float64(r.IntN(1000)) / 7
		v.B = r.IntN(2) == 0
		if r.IntN(2) == 0 {
			v.Slice = []int{r.IntN(10), r.IntN(10)}
		}
		if r.IntN(2) == 0 {
			v.Map = map[string]int{"k": r.IntN(10), "a": 1}
		}
		if r.IntN(2) == 0 {
			v.Ptr = &mInner{X: r.IntN(10)}
		}
		got, gotErr := simdjson.Marshal(v)
		want, wantErr := json.Marshal(v)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("simdjson err = %v, stdlib err = %v", gotErr, wantErr)
		}
		if wantErr == nil && string(got) != string(want) {
			t.Fatalf("\n  simdjson = %s\n  stdlib   = %s", got, want)
		}
	}
}

// Round-trip: what we encode, we and the standard library both decode back.
func TestMarshalRoundTrips(t *testing.T) {
	v := mWide{Str: "hi \"there\"\n", Num: 7, F: 2.5, B: true,
		Slice: []int{1, 2}, Map: map[string]int{"a": 1}, Ptr: &mInner{X: 3}}
	b, err := simdjson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var back mWide
	if err := simdjson.Unmarshal(b, &back); err != nil {
		t.Fatalf("our decode of our encode: %v", err)
	}
	var stdBack mWide
	if err := json.Unmarshal(b, &stdBack); err != nil {
		t.Fatalf("stdlib decode of our encode: %v", err)
	}
}

// FuzzMarshalAgainstStdlib holds the encoder to byte-identical output. Escaping
// is where the two can differ silently: an invalid UTF-8 byte written raw looks
// the same when printed as the \ufffd the standard library writes, and is not
// the same bytes. That is how the first version of the escape scanner was
// caught.
func FuzzMarshalAgainstStdlib(f *testing.F) {
	for _, s := range []string{
		"", "plain", `"`, `\`, "\n", "\x00", "<>&", "\u2028\u2029",
		"\u00e9", "\U0001F600", string([]byte{0xff}), "a\x7fb",
	} {
		f.Add(s, 0, 1.5, true)
	}
	f.Fuzz(func(t *testing.T, s string, n int, fl float64, b bool) {
		v := mWide{Str: s, Num: n, F: fl, B: b, StrSl: []string{s}, NoTag: s}
		got, gotErr := simdjson.Marshal(v)
		want, wantErr := json.Marshal(v)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("simdjson err = %v, stdlib err = %v", gotErr, wantErr)
		}
		if wantErr != nil {
			return
		}
		if string(got) != string(want) {
			t.Fatalf("\n  simdjson = %s\n  stdlib   = %s", got, want)
		}
		// And a bare string, where the escaping stands alone.
		gs, _ := simdjson.Marshal(s)
		ws, _ := json.Marshal(s)
		if string(gs) != string(ws) {
			t.Fatalf("string %q:\n  simdjson = %s\n  stdlib   = %s", s, gs, ws)
		}
	})
}
