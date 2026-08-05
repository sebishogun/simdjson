package simdjson

// The gate on the parallel index: bit-identity with the serial WINDOWED path,
// which is the path it replaces -- parallel only runs above parallelMinBytes,
// where serial means windowed. (buildIndexWhole and buildIndexWindowed already
// order multi-error inputs differently from each other, because one runs its
// string pass over the whole document and the other per window; the parallel
// path mirrors the windowed ordering, so that is the comparison.)
//
// Success must match on pos, match, inStr, wsw, wsCount and noWS. Failure must
// match on the error string. Both are checked over crafted boundary cases and
// random soup, with the tunables forced so a few-hundred-KB document crosses
// many segment boundaries.

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// forceParallel shrinks the thresholds so every test document goes parallel,
// and restores them.
func forceParallel(t *testing.T) {
	t.Helper()
	oldMin, oldSeg := parallelMinBytes, parallelSegBytes
	parallelMinBytes, parallelSegBytes = 1, 1
	t.Cleanup(func() { parallelMinBytes, parallelSegBytes = oldMin, oldSeg })
}

// compareIndexes runs both paths on the same bytes and requires identical
// results. It returns whether the input was valid, so callers can count.
func compareIndexes(t *testing.T, name string, data []byte, validate bool) bool {
	return compareIndexesOpt(t, name, data, validate, false)
}

// compareIndexesOpt with allowDecline for input where the boundary snap can
// legitimately fail -- a decline is correct behaviour there, not a miss, and
// the serial path handles the document.
func compareIndexesOpt(t *testing.T, name string, data []byte, validate, allowDecline bool) bool {
	return compareIndexesMode(t, name, data, validate, false, allowDecline)
}

// compareIndexesMode also covers Valid's masks-only mode, where pos and match
// are not built and the comparison is the masks, the whitespace accounting and
// the errors.
func compareIndexesMode(t *testing.T, name string, data []byte, validate, noBrackets, allowDecline bool) bool {
	t.Helper()
	sIx, sErr := buildIndexWindowed(data, &index{}, validate, noBrackets, false)
	pIx, pErr, ok := buildIndexParallel(data, &index{}, validate, noBrackets)
	if !ok {
		if allowDecline {
			return sErr == nil
		}
		t.Fatalf("%s: parallel path declined input of %d bytes", name, len(data))
	}
	if (sErr == nil) != (pErr == nil) {
		t.Fatalf("%s: serial err %v, parallel err %v", name, sErr, pErr)
	}
	if sErr != nil {
		if sErr.Error() != pErr.Error() {
			t.Fatalf("%s: serial err %q, parallel err %q", name, sErr, pErr)
		}
		return false
	}
	if noBrackets {
		if len(pIx.pos) != 0 || len(pIx.match) != 0 {
			t.Fatalf("%s: masks-only built %d/%d brackets", name, len(pIx.pos), len(pIx.match))
		}
	}
	if len(sIx.pos) != len(pIx.pos) {
		t.Fatalf("%s: pos length %d vs %d", name, len(sIx.pos), len(pIx.pos))
	}
	for i := range sIx.pos {
		if sIx.pos[i] != pIx.pos[i] {
			t.Fatalf("%s: pos[%d] = %d vs %d", name, i, sIx.pos[i], pIx.pos[i])
		}
		if sIx.match[i] != pIx.match[i] {
			t.Fatalf("%s: match[%d] (pos %d) = %d vs %d",
				name, i, sIx.pos[i], sIx.match[i], pIx.match[i])
		}
	}
	nw := (len(data) + 63) / 64
	for w := 0; w < nw; w++ {
		if sIx.inStr[w] != pIx.inStr[w] {
			t.Fatalf("%s: inStr[%d] = %#x vs %#x", name, w, sIx.inStr[w], pIx.inStr[w])
		}
	}
	if validate {
		if sIx.wsCount != pIx.wsCount || sIx.noWS != pIx.noWS {
			t.Fatalf("%s: wsCount/noWS = %d/%v vs %d/%v",
				name, sIx.wsCount, sIx.noWS, pIx.wsCount, pIx.noWS)
		}
		for w := 0; w < nw; w++ {
			if sIx.wsw[w] != pIx.wsw[w] {
				t.Fatalf("%s: wsw[%d] differs", name, w)
			}
		}
	}
	return true
}

// pad returns filler that is a valid JSON string member of exactly n bytes,
// for positioning later content at chosen offsets. n must be at least 8.
func pad(n int) string {
	return `"p":"` + strings.Repeat("x", n-8) + `",`
}

func TestParallelIndexMatchesSerial(t *testing.T) {
	forceParallel(t)
	seg := chunkBytes // one window per segment under the forced tunables

	cases := map[string]string{
		// Ordinary shapes crossing several segments.
		"array of objects": `[` + strings.Repeat(`{"k":"v","n":[1,2,{"d":"x"}]},`, 12000)[:12000*30-1] + `]`,
		// One string spanning multiple whole segments: parity is 1 across
		// several boundaries, which is the bit phase one exists to compute.
		"huge string": `{` + pad(3*seg) + `"z":[1]}`,
		// Brackets inside that huge string must not index.
		"huge string of brackets": `{"k":"` + strings.Repeat(`[{]}`, seg) + `"}`,
		// Deep nesting across all segments: everything pairs cross-segment.
		"deep nesting": strings.Repeat(`[`, 2*seg) + `1` + strings.Repeat(`]`, 2*seg),
		// Nesting that opens in segment 0 and closes far away, with local
		// pairs between.
		"wide then deep": `[` + strings.Repeat(`{"a":[1]},`, 3*seg/10) + `[` +
			strings.Repeat(`2,`, seg) + `3]]`,
		// Escapes: a wall of backslash pairs crossing the first boundary, with
		// enough ordinary document after it that the snap lands and later
		// boundaries survive.
		"backslash wall": `{"k":"` + strings.Repeat(`\\`, seg/2) + `",` +
			strings.Repeat(`"a":[1,{"b":"c"}],`, 3*seg/18) + `"z":1}`,
		// An escaped quote exactly at a window boundary region.
		"escaped quotes": `{"k":"` + strings.Repeat(`a\"`, seg) + `"}`,
		// Whitespace-heavy, for wsCount and noWS.
		"pretty": `[` + strings.Repeat("\n\t[ 1 , {\"k\" : \"v\"} ] ,", 20000) + "1]",
		"no ws":  `[` + strings.Repeat(`{"a":1},`, 40000) + `2]`,
	}
	for name, doc := range cases {
		for _, validate := range []bool{false, true} {
			compareIndexes(t, fmt.Sprintf("%s validate=%v", name, validate), []byte(doc), validate)
		}
	}
}

func TestParallelIndexErrors(t *testing.T) {
	forceParallel(t)
	seg := chunkBytes
	mk := func(parts ...string) []byte { return []byte(strings.Join(parts, "")) }

	cases := map[string][]byte{
		"ctl in string, late segment":  mk(`{`, pad(3*seg), `"k":"a`, "\x01", `b"}`),
		"invalid escape far in":        mk(`{`, pad(2*seg), `"k":"a\qb"}`),
		"short u escape at end":        mk(`{`, pad(2*seg), `"k":"\u00`),
		"unterminated string":          mk(`{`, pad(2*seg), `"k":"open`),
		"ends in backslash":            mk(`{`, pad(2*seg), `"k":"a\`),
		"unterminated container":       mk(`[`, pad(2*seg), `"k":1`),
		"extra close, cross segment":   mk(`[`, pad(2*seg), `"k":1]]`),
		"mismatched, cross segment":    mk(`[`, pad(2*seg), `"k":1}`),
		"mismatched, same segment":     mk(`[{]`, pad(2*seg), `"k":1`),
		"close with nothing open":      mk(`]`, pad(2*seg), `"k":1`),
		"deep unbalanced":              mk(strings.Repeat(`[`, seg), `1`, strings.Repeat(`]`, seg/2)),
		"ctl late vs bracket early":    mk(`[}`, pad(3*seg), `"k":"a`, "\x02", `"`),
		"bracket early vs ctl earlier": mk(`{"a":"`, "\x03", `"`, `}`, `]`, pad(2*seg)),
	}
	for name, doc := range cases {
		for _, validate := range []bool{false, true} {
			compareIndexes(t, fmt.Sprintf("%s validate=%v", name, validate), doc, validate)
		}
	}
}

// Random soup: most of it is invalid, which is the point -- the two paths must
// agree on WHICH error, under the windowed ordering, not merely that there is
// one.
func TestParallelIndexRandom(t *testing.T) {
	forceParallel(t)
	atoms := []string{
		`{`, `}`, `[`, `]`, `"`, `\`, `\\`, `\"`, `"s"`, `123`, `,`, `:`,
		` `, "\n", "\t", `a`, `é`, "\x00", "\x1f", `A`, `\q`,
		`{"k":"v"}`, `[1,2]`, `"a\\"`, `"\\\"",`,
	}
	rng := rand.New(rand.NewSource(11))
	valid := 0
	for trial := 0; trial < 120; trial++ {
		var b bytes.Buffer
		target := 150_000 + rng.Intn(200_000)
		for b.Len() < target {
			b.WriteString(atoms[rng.Intn(len(atoms))])
		}
		if compareIndexesOpt(t, fmt.Sprintf("soup %d", trial), b.Bytes(), trial%2 == 0, true) {
			valid++
		}
	}
	t.Logf("%d of 120 soups indexed clean", valid)
}

// TestParallelDeclines: input the boundary snap cannot fix falls back to the
// serial path rather than being wrong.
func TestParallelDeclines(t *testing.T) {
	forceParallel(t)
	all := bytes.Repeat([]byte{'\\'}, 4*chunkBytes)
	if _, _, ok := buildIndexParallel(all, &index{}, false, false); ok {
		t.Fatal("parallel path accepted a document that is one backslash run")
	}
	// And through the public entry point the serial path answers.
	_, err := Parse(all)
	if err == nil {
		t.Fatal("Parse accepted a document of backslashes")
	}
}

// TestParallelIsUsed pins the mechanism: above the thresholds the parallel
// path must actually run, or every number attached to it is fiction. It
// cannot be observed from the output -- that is the whole contract -- so the
// segment planner is asked directly.
func TestParallelIsUsed(t *testing.T) {
	if b := parallelSegments(make([]byte, 64<<20)); b == nil {
		t.Skip("one usable core; the parallel path is off here")
	} else if len(b) < 3 {
		t.Fatalf("64 MB planned only %d segments", len(b)-1)
	}

}

// Random VALID documents, since the soup above almost never produces one: the
// success path has to agree on pos, match and inStr over varied shapes, not
// only on the crafted cases.
func TestParallelIndexRandomValid(t *testing.T) {
	forceParallel(t)
	rng := rand.New(rand.NewSource(23))
	var emit func(b *bytes.Buffer, depth, budget int) int
	strAtoms := []string{
		`plain`, `with \"quote\"`, `back\\slash`, `unié`, `tab\there`,
		`long ` + strings.Repeat("x", 300), `éé日本`, ``, `a/b\/c`,
	}
	emit = func(b *bytes.Buffer, depth, budget int) int {
		switch k := rng.Intn(7); {
		case k == 0 && depth < 12:
			b.WriteByte('[')
			n := rng.Intn(6)
			for i := 0; i < n && budget > 0; i++ {
				if i > 0 {
					b.WriteByte(',')
				}
				budget = emit(b, depth+1, budget-2)
			}
			b.WriteByte(']')
			return budget - 2
		case k == 1 && depth < 12:
			b.WriteByte('{')
			n := rng.Intn(5)
			for i := 0; i < n && budget > 0; i++ {
				if i > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(b, `"k%d":`, i)
				budget = emit(b, depth+1, budget-8)
			}
			b.WriteByte('}')
			return budget - 2
		case k == 2:
			s := strAtoms[rng.Intn(len(strAtoms))]
			b.WriteString(`"` + s + `"`)
			return budget - len(s) - 2
		case k == 3:
			fmt.Fprintf(b, "%d", rng.Int63()-rng.Int63())
			return budget - 12
		case k == 4:
			b.WriteString("  ")
			fmt.Fprintf(b, "%g", rng.NormFloat64())
			return budget - 14
		case k == 5:
			b.WriteString("true")
			return budget - 4
		default:
			b.WriteString("null")
			return budget - 4
		}
	}
	valid := 0
	for trial := 0; trial < 60; trial++ {
		var b bytes.Buffer
		b.WriteByte('[')
		budget := 150_000 + rng.Intn(150_000)
		first := true
		for budget > 0 {
			if !first {
				b.WriteByte(',')
			}
			first = false
			budget = emit(&b, 0, budget)
		}
		b.WriteByte(']')
		if compareIndexes(t, fmt.Sprintf("valid %d", trial), b.Bytes(), trial%2 == 0) {
			valid++
		}
	}
	if valid != 60 {
		t.Fatalf("only %d of 60 generated documents were valid; the generator is broken", valid)
	}
}

// The payoff, measured through the public entry points at the size the path
// exists for. The serial rows force the threshold out of reach; nothing else
// differs.
func BenchmarkParallelScan(b *testing.B) {
	one := gateCorpus(b, "canada")
	var data []byte
	for len(data) < 64<<20 {
		data = append(data, one...)
	}
	data = data[:len(one)*28]
	var p Parser
	for _, mode := range []struct {
		name string
		min  int
	}{{"serial", 1 << 62}, {"parallel", 8 << 20}} {
		b.Run("Scan/"+mode.name, func(b *testing.B) {
			old := parallelMinBytes
			parallelMinBytes = mode.min
			defer func() { parallelMinBytes = old }()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := p.Scan(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	// Valid needs a document that is one value; wrap the repeats in an array.
	doc := append([]byte{'['}, bytes.Join(splitRepeats(data, len(one)), []byte(","))...)
	doc = append(doc, ']')
	for _, mode := range []struct {
		name string
		min  int
	}{{"serial", 1 << 62}, {"parallel", 8 << 20}} {
		b.Run("Valid/"+mode.name, func(b *testing.B) {
			old := parallelMinBytes
			parallelMinBytes = mode.min
			defer func() { parallelMinBytes = old }()
			b.SetBytes(int64(len(doc)))
			for b.Loop() {
				if !Valid(doc) {
					b.Fatal("invalid")
				}
			}
		})
	}
}

func splitRepeats(data []byte, one int) [][]byte {
	var out [][]byte
	for len(data) >= one {
		out = append(out, data[:one])
		data = data[one:]
	}
	return out
}

// TestParallelMasksOnly is the differential for Valid's mode: no brackets are
// built, unbalanced brackets are NOT an index error there -- the grammar walk
// owns that -- and everything else must still match word for word.
func TestParallelMasksOnly(t *testing.T) {
	forceParallel(t)
	seg := chunkBytes
	cases := map[string]string{
		"ordinary":      `[` + strings.Repeat(`{"k":"v","n":[1,2]},`, 3*seg/20) + `1]`,
		"huge string":   `{` + pad(3*seg) + `"z":1}`,
		"unbalanced ok": strings.Repeat(`[`, seg) + `1`, // masks-only must NOT error
		"pretty":        `[` + strings.Repeat("\n\t[ 1 , {\"k\" : \"v\"} ] ,", 9000) + "1]",
		"ctl in string": `{` + pad(2*seg) + `"k":"a` + "\x01" + `"}`,
		"bad escape":    `{` + pad(2*seg) + `"k":"a\qb"}`,
		"unterm string": `{` + pad(2*seg) + `"k":"open`,
	}
	for name, doc := range cases {
		for _, validate := range []bool{false, true} {
			compareIndexesMode(t, fmt.Sprintf("%s validate=%v", name, validate),
				[]byte(doc), validate, true, false)
		}
	}
	// And end to end: Valid itself, parallel against forced-serial.
	rng := rand.New(rand.NewSource(41))
	atoms := []string{`{"a":1}`, `[1,2]`, `"s"`, `,`, `[`, `]`, ` `, `3`}
	for trial := 0; trial < 40; trial++ {
		var b bytes.Buffer
		b.WriteByte('[')
		for b.Len() < 100_000+rng.Intn(100_000) {
			b.WriteString(atoms[rng.Intn(len(atoms))])
		}
		b.WriteString(`1]`)
		doc := b.Bytes()
		got := Valid(doc)
		old := parallelMinBytes
		parallelMinBytes = 1 << 62
		want := Valid(doc)
		parallelMinBytes = old
		if got != want {
			t.Fatalf("soup %d: parallel Valid %v, serial %v", trial, got, want)
		}
	}
}
