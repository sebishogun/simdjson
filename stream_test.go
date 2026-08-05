package simdjson

import (
	"bytes"
	stdjson "encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

// oneByteReader hands back a single byte per Read, so every refill boundary in
// the decoder falls in a different place. A stream decoder that only works when
// the whole input arrives at once works on every test that does not do this.
type oneByteReader struct {
	s string
	i int
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	p[0] = r.s[r.i]
	r.i++
	return 1, nil
}

var streamInputs = []string{
	`{"a":1}`,
	`{"a":1}{"b":2}`,
	"{\"a\":1}\n{\"b\":2}\n{\"c\":3}\n",
	`  {"a":1}   {"b":2}  `,
	`1 2 3`,
	`"x" "y"`,
	`true false null`,
	`[1,2,3][4,5,6]`,
	`{"s":"has } and ] and \" inside"}{"t":2}`,
	`{"s":"trailing backslash pair \\\\"}`,
	`[{"a":[{"b":[[[]]]}]}]`,
	`{"nested":{"deep":{"deeper":[1,2,{"x":"y"}]}}}` + "\n" + `[]`,
	``,
	`   `,
	`{"a":1}garbage`,
	`{"a":`,
	`[1,2`,
	`"unterminated`,
}

// decodeAll reads every value the decoder will give, as RawMessage, and reports
// the values and the error that stopped it.
func decodeAll(dec interface{ Decode(any) error }) ([]string, error) {
	var out []string
	for {
		var raw stdjson.RawMessage
		err := dec.Decode(&raw)
		if err != nil {
			return out, err
		}
		out = append(out, string(raw))
	}
}

func TestDecoderMatchesStdlib(t *testing.T) {
	for _, in := range streamInputs {
		for _, chunked := range []bool{false, true} {
			var ourR, stdR io.Reader
			if chunked {
				ourR, stdR = &oneByteReader{s: in}, &oneByteReader{s: in}
			} else {
				ourR, stdR = strings.NewReader(in), strings.NewReader(in)
			}
			got, gErr := decodeAll(NewDecoder(ourR))
			want, wErr := decodeAll(stdjson.NewDecoder(stdR))

			// The values decoded before the stream stopped must agree exactly.
			if len(got) != len(want) {
				t.Errorf("Decode(%q, chunked=%v) got %d values %q, want %d %q",
					in, chunked, len(got), got, len(want), want)
				continue
			}
			for i := range got {
				var g, w bytes.Buffer
				// Compare compacted: both are raw slices of the input, so
				// interior whitespace is whatever the input had.
				if err := stdjson.Compact(&g, []byte(got[i])); err != nil {
					t.Fatalf("our value %q is not JSON: %v", got[i], err)
				}
				_ = stdjson.Compact(&w, []byte(want[i]))
				if g.String() != w.String() {
					t.Errorf("Decode(%q) value %d = %s, want %s", in, i, g.String(), w.String())
				}
			}
			// And both must stop, on EOF or on an error.
			if (gErr == io.EOF) != (wErr == io.EOF) {
				t.Errorf("Decode(%q, chunked=%v) stopped with %v, stdlib with %v",
					in, chunked, gErr, wErr)
			}
			if gErr == nil {
				t.Errorf("Decode(%q) never stopped", in)
			}
		}
	}
}

func TestDecoderInto(t *testing.T) {
	type row struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}
	const in = "{\"name\":\"a\",\"n\":1}\n{\"name\":\"b\",\"n\":2}\n{\"name\":\"c\",\"n\":3}\n"
	dec := NewDecoder(strings.NewReader(in))
	var got []row
	for {
		var r row
		if err := dec.Decode(&r); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("Decode: %v", err)
			}
			break
		}
		got = append(got, r)
	}
	want := []row{{"a", 1}, {"b", 2}, {"c", 3}}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDecoderMoreAndOffset(t *testing.T) {
	const in = `{"a":1} {"b":2}`
	dec := NewDecoder(strings.NewReader(in))
	sdec := stdjson.NewDecoder(strings.NewReader(in))
	for i := 0; ; i++ {
		gm, wm := dec.More(), sdec.More()
		if gm != wm {
			t.Fatalf("value %d: More() = %v, stdlib %v", i, gm, wm)
		}
		if !gm {
			break
		}
		var g, w stdjson.RawMessage
		if err := dec.Decode(&g); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if err := sdec.Decode(&w); err != nil {
			t.Fatalf("stdlib Decode: %v", err)
		}
		if got, want := dec.InputOffset(), sdec.InputOffset(); got != want {
			t.Errorf("value %d: InputOffset() = %d, stdlib %d", i, got, want)
		}
	}
}

func TestDecoderBuffered(t *testing.T) {
	const in = `{"a":1}rest of the stream`
	dec := NewDecoder(strings.NewReader(in))
	var raw stdjson.RawMessage
	if err := dec.Decode(&raw); err != nil {
		t.Fatal(err)
	}
	rest, _ := io.ReadAll(dec.Buffered())
	if string(rest) != "rest of the stream" {
		t.Errorf("Buffered() = %q", rest)
	}
}

func TestEncoderMatchesStdlib(t *testing.T) {
	vals := []any{
		map[string]any{"a": 1},
		[]int{1, 2, 3},
		"a string with <html> & \"quotes\"",
		nil,
		struct {
			X int    `json:"x"`
			Y string `json:"y"`
		}{1, "two"},
	}
	for _, escape := range []bool{true, false} {
		for _, ind := range []string{"", "  ", "\t"} {
			var got, want bytes.Buffer
			ge, we := NewEncoder(&got), stdjson.NewEncoder(&want)
			ge.SetEscapeHTML(escape)
			we.SetEscapeHTML(escape)
			if ind != "" {
				ge.SetIndent("", ind)
				we.SetIndent("", ind)
			}
			for _, v := range vals {
				if err := ge.Encode(v); err != nil {
					t.Fatalf("Encode(%v): %v", v, err)
				}
				if err := we.Encode(v); err != nil {
					t.Fatalf("stdlib Encode(%v): %v", v, err)
				}
			}
			if got.String() != want.String() {
				t.Errorf("escape=%v indent=%q:\n got %q\nwant %q", escape, ind, got.String(), want.String())
			}
		}
	}
}

// A round trip through both halves, on a stream long enough to cross the
// decoder's refill boundary several times.
func TestStreamRoundTrip(t *testing.T) {
	type row struct {
		ID   int    `json:"id"`
		Text string `json:"text"`
	}
	n := 20000
	if testing.Short() {
		n = 500 // still several refills, which is what this is testing
	}
	var enc bytes.Buffer
	e := NewEncoder(&enc)
	var want []row
	for i := 0; i < n; i++ {
		r := row{ID: i, Text: strings.Repeat("x", i%97)}
		want = append(want, r)
		if err := e.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	dec := NewDecoder(bytes.NewReader(enc.Bytes()))
	for i := 0; ; i++ {
		var got row
		if err := dec.Decode(&got); err != nil {
			if err == io.EOF {
				if i != len(want) {
					t.Fatalf("read %d rows, wrote %d", i, len(want))
				}
				break
			}
			t.Fatalf("row %d: %v", i, err)
		}
		if got != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, got, want[i])
		}
	}
}

func FuzzDecoderAgainstStdlib(f *testing.F) {
	for _, s := range streamInputs {
		f.Add([]byte(s))
	}
	// Each input goes through at three batch sizes. At the shipped 64 KB the
	// window path in loadWindow needs 64 KB of lookahead before it runs at all,
	// and a fuzzer's inputs are bytes, not kilobytes -- so this target covered
	// every streaming path EXCEPT that one until the sizes were varied.
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, chunk := range []int{0, 8, 64} {
			old := streamChunk
			if chunk != 0 {
				streamChunk = chunk
			}
			got, gErr := decodeAll(NewDecoder(bytes.NewReader(data)))
			streamChunk = old

			want, wErr := decodeAll(stdjson.NewDecoder(bytes.NewReader(data)))
			if len(got) != len(want) {
				t.Fatalf("chunk=%d: got %d values %q, want %d %q",
					chunk, len(got), got, len(want), want)
			}
			for i := range got {
				var g, w bytes.Buffer
				if err := stdjson.Compact(&g, []byte(got[i])); err != nil {
					t.Fatalf("chunk=%d: value %d %q is not JSON: %v", chunk, i, got[i], err)
				}
				_ = stdjson.Compact(&w, []byte(want[i]))
				if !bytes.Equal(g.Bytes(), w.Bytes()) {
					t.Fatalf("chunk=%d: value %d = %s, want %s", chunk, i, g.Bytes(), w.Bytes())
				}
			}
			if (gErr == io.EOF) != (wErr == io.EOF) {
				t.Fatalf("chunk=%d: stopped with %v, stdlib with %v", chunk, gErr, wErr)
			}
		}
	})
}

func TestDecoderUseNumberMatchesStdlib(t *testing.T) {
	const in = `{"a":1,"b":2.5,"c":1e400,"d":[3,4.0],"e":12345678901234567890}`
	for _, use := range []bool{false, true} {
		g := NewDecoder(strings.NewReader(in))
		w := stdjson.NewDecoder(strings.NewReader(in))
		if use {
			g.UseNumber()
			w.UseNumber()
		}
		var gv, wv any
		gErr := g.Decode(&gv)
		wErr := w.Decode(&wv)
		if (gErr != nil) != (wErr != nil) {
			t.Fatalf("UseNumber=%v: error %v, stdlib %v", use, gErr, wErr)
		}
		if wErr != nil {
			continue
		}
		gb, _ := stdjson.Marshal(gv)
		wb, _ := stdjson.Marshal(wv)
		if !bytes.Equal(gb, wb) {
			t.Errorf("UseNumber=%v:\n got %s\nwant %s", use, gb, wb)
		}
	}
}

func TestDecoderDisallowUnknownFieldsMatchesStdlib(t *testing.T) {
	type small struct {
		A int `json:"a"`
		B int `json:"b"`
	}
	inputs := []string{
		`{"a":1,"b":2}`,
		`{"a":1,"c":3}`,
		`{"c":3}`,
		`{"A":1}`,
		`{"a":1,"\u0063":3}`,
		`{}`,
	}
	for _, disallow := range []bool{false, true} {
		for _, in := range inputs {
			var gv, wv small
			g := NewDecoder(strings.NewReader(in))
			w := stdjson.NewDecoder(strings.NewReader(in))
			if disallow {
				g.DisallowUnknownFields()
				w.DisallowUnknownFields()
			}
			gErr := g.Decode(&gv)
			wErr := w.Decode(&wv)
			if (gErr != nil) != (wErr != nil) {
				t.Errorf("disallow=%v %s: error %v, stdlib %v", disallow, in, gErr, wErr)
				continue
			}
			if gErr != nil {
				if gErr.Error() != wErr.Error() {
					t.Errorf("disallow=%v %s: %q, stdlib %q", disallow, in, gErr, wErr)
				}
				continue
			}
			if gv != wv {
				t.Errorf("disallow=%v %s: got %+v, want %+v", disallow, in, gv, wv)
			}
		}
	}
}
