package simdjson

// The sort has to produce exactly encoding/json's order, because the bytes it
// produces are compared against encoding/json's byte for byte. A sort that is
// faster and disagrees anywhere is not a sort.
//
// So it is checked against sort.Strings on the shapes a radix sort gets wrong:
// keys that are prefixes of each other, keys sharing long prefixes, empty
// keys, bytes above 0x7f, and enough of them to reach every branch -- the
// insertion tail, the partition loop, the ninther, and the heapsort escape.

import (
	stdjson "encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"sort"
	"strings"
	"testing"
)

// sorted runs the sort and returns the keys in the order it produced.
func sortedKeys(keys []string) []string {
	p := make([]pair[int], len(keys))
	for i, k := range keys {
		p[i] = pair[int]{k: k, v: i}
	}
	sortPairs(p)
	out := make([]string, len(p))
	for i := range p {
		out[i] = p[i].k
	}
	return out
}

// checkSort fails unless the sort agrees with sort.Strings and carried each
// value along with its key.
func checkSort(t *testing.T, name string, keys []string) {
	t.Helper()
	p := make([]pair[int], len(keys))
	for i, k := range keys {
		p[i] = pair[int]{k: k, v: i}
	}
	sortPairs(p)

	want := slices.Clone(keys)
	sort.Strings(want)
	for i := range p {
		if p[i].k != want[i] {
			t.Errorf("%s: at %d got %q, want %q\n got: %q\nwant: %q",
				name, i, p[i].k, want[i], sortedKeys(keys), want)
			return
		}
		// The value must still be the one that came in with this key.
		if keys[p[i].v] != p[i].k {
			t.Errorf("%s: at %d key %q carries value %d, which belongs to %q",
				name, i, p[i].k, p[i].v, keys[p[i].v])
			return
		}
	}
}

func TestSortPairs(t *testing.T) {
	cases := map[string][]string{
		"empty":     {},
		"one":       {"a"},
		"two":       {"b", "a"},
		"reversed":  {"e", "d", "c", "b", "a"},
		"sorted":    {"a", "b", "c", "d", "e"},
		"identical": {"aa", "ab", "ac", "ad"},
		// A key that is a prefix of another is where byteAt's -1 sentinel
		// earns its place: the shorter must sort first.
		"prefixes": {"id_str", "id", "in_reply_to", "i", "", "idx"},
		"emptyKey": {"", "a", ""},
		// The real shape: twitter.json's status object.
		"twitter": {
			"metadata", "created_at", "id", "id_str", "text", "source",
			"truncated", "in_reply_to_status_id", "in_reply_to_status_id_str",
			"in_reply_to_user_id", "in_reply_to_user_id_str",
			"in_reply_to_screen_name", "user", "geo", "coordinates", "place",
			"contributors", "retweet_count", "favorite_count", "entities",
			"favorited", "retweeted", "lang", "possibly_sensitive",
		},
		// Every key identical up to the last byte: the partition advances one
		// byte at a time and this is the worst of it.
		"longCommonPrefix": {
			strings.Repeat("x", 64) + "c",
			strings.Repeat("x", 64) + "a",
			strings.Repeat("x", 64) + "d",
			strings.Repeat("x", 64) + "b",
		},
		"highBytes": {"\xff", "\x80", "\x7f", "a", "\xc3\xa9", "\xc3\xa8"},
		"digits":    {"10", "9", "1", "100", "2", "20"},
	}
	for name, keys := range cases {
		checkSort(t, name, keys)
	}

	// Sizes across every threshold: the insertion tail at 11, the ninther at
	// 40, and well past both.
	for _, n := range []int{2, 3, 10, 11, 12, 13, 39, 40, 41, 64, 100, 1000} {
		keys := make([]string, n)
		for i := range keys {
			keys[i] = fmt.Sprintf("key_%06d", i)
		}
		rand.New(rand.NewSource(int64(n))).Shuffle(n, func(i, j int) {
			keys[i], keys[j] = keys[j], keys[i]
		})
		checkSort(t, fmt.Sprintf("shuffled/%d", n), keys)
	}
}

// Every permutation of a small set, so no ordering of the input is left
// untried at the sizes the insertion tail handles.
func TestSortPairsPermutations(t *testing.T) {
	base := []string{"a", "ab", "b", "", "ba", "abc"}
	var perm func([]string, int)
	count := 0
	perm = func(s []string, i int) {
		if i == len(s) {
			count++
			checkSort(t, fmt.Sprintf("perm/%v", s), slices.Clone(s))
			return
		}
		for j := i; j < len(s); j++ {
			s[i], s[j] = s[j], s[i]
			perm(s, i+1)
			s[i], s[j] = s[j], s[i]
		}
	}
	perm(slices.Clone(base), 0)
	if count != 720 {
		t.Fatalf("tried %d permutations, want 720", count)
	}
}

// Random keys, random alphabets. A small alphabet makes shared prefixes common,
// which is the case the radix partitioning exists for and the one where an
// off-by-one in the depth would show.
func TestSortPairsRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, alphabet := range []string{"ab", "abc", "abcdefghij", "\x00\x01\xfe\xff"} {
		for trial := 0; trial < 200; trial++ {
			n := 1 + rng.Intn(60)
			keys := make([]string, 0, n)
			seen := map[string]bool{}
			for len(keys) < n {
				l := rng.Intn(8)
				var sb strings.Builder
				for i := 0; i < l; i++ {
					sb.WriteByte(alphabet[rng.Intn(len(alphabet))])
				}
				if k := sb.String(); !seen[k] {
					seen[k] = true
					keys = append(keys, k)
				}
			}
			checkSort(t, fmt.Sprintf("random/%q/%d", alphabet, trial), keys)
		}
	}
}

// The introsort escape. maxDepth is 2*ceil(lg(n+1)) and the only way to reach
// heapPairs from the outside is an input that keeps partitioning badly, so it
// is called directly as well -- otherwise it is code no test has run.
func TestHeapPairs(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for _, n := range []int{0, 1, 2, 3, 17, 64, 257} {
		keys := make([]string, n)
		for i := range keys {
			keys[i] = fmt.Sprintf("k%05d", rng.Intn(1000000))
		}
		p := make([]pair[int], len(keys))
		for i, k := range keys {
			p[i] = pair[int]{k: k, v: i}
		}
		heapPairs(p)
		want := slices.Clone(keys)
		sort.Strings(want)
		for i := range p {
			if p[i].k != want[i] {
				t.Fatalf("heapPairs n=%d: at %d got %q, want %q", n, i, p[i].k, want[i])
			}
		}
	}

	// And through radixQsort with maxDepth exhausted, which is the path that
	// actually calls it.
	keys := make([]string, 200)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%05d", rng.Intn(1000000))
	}
	p := make([]pair[int], len(keys))
	for i, k := range keys {
		p[i] = pair[int]{k: k, v: i}
	}
	radixQsort(p, 0, 0, nil)
	want := slices.Clone(keys)
	sort.Strings(want)
	for i := range p {
		if p[i].k != want[i] {
			t.Fatalf("radixQsort maxDepth=0: at %d got %q, want %q", i, p[i].k, want[i])
		}
	}
}

// Keys sharing a long prefix, which is what JSON keys are.
//
// This is the shape that sent the sort into its introsort escape: every level
// that only advanced the radix used to spend a unit of the depth budget, and an
// eleven-byte common prefix spent eleven of the fourteen available at n=64. The
// escape hatch for adversarial input fired on the ordinary case. Sorting is
// still correct when that happens -- heapsort is a sort -- so only a benchmark
// or a counter shows it, which is why BenchmarkSortPrefixed is here too.
func TestSortPairsSharedPrefixes(t *testing.T) {
	for _, prefix := range []string{
		"", "f_", "field_name_",
		"a_very_long_common_field_name_prefix_",
		strings.Repeat("x", 200),
	} {
		for _, n := range []int{12, 16, 41, 64, 256, 1000} {
			keys := make([]string, n)
			for i := range keys {
				// Scattered, so the input is not already ordered.
				keys[i] = fmt.Sprintf("%s%06d", prefix, (i*7919)%n)
			}
			checkSort(t, fmt.Sprintf("prefix=%d/n=%d", len(prefix), n), keys)
		}
	}

	// A prefix longer than the depth budget could ever be, with the keys
	// differing only in their last byte.
	long := strings.Repeat("k", 500)
	keys := make([]string, 64)
	for i := range keys {
		keys[i] = long + string(rune('A'+i))
	}
	checkSort(t, "500-byte prefix", keys)
}

// Sorting keys that share a prefix must not cost more than sorting keys that do
// not. If the depth budget is ever charged for advancing the radix again, the
// prefixed rows here go several times slower than the bare one while every test
// still passes.
func BenchmarkSortPrefixed(b *testing.B) {
	for _, prefix := range []string{"", "field_name_", strings.Repeat("x", 64)} {
		for _, n := range []int{16, 64, 256} {
			src := make([]pair[int], n)
			for i := range src {
				src[i] = pair[int]{k: fmt.Sprintf("%s%06d", prefix, (i*7919)%n), v: i}
			}
			work := make([]pair[int], n)
			b.Run(fmt.Sprintf("prefix=%d/n=%d", len(prefix), n), func(b *testing.B) {
				for b.Loop() {
					copy(work, src)
					sortPairs(work)
				}
			})
		}
	}
}

// commonPrefixEnd is checked against every key in the group, not the first and
// last, and it must stop at the shortest. Both are silent failures if wrong:
// the result stays a permutation, it is just ordered by a prefix nobody shares.
func TestCommonPrefixEnd(t *testing.T) {
	mk := func(keys ...string) []pair[int] {
		p := make([]pair[int], len(keys))
		for i, k := range keys {
			p[i] = pair[int]{k: k, v: i}
		}
		return p
	}
	cases := []struct {
		name string
		keys []string
		d    int
		want int
	}{
		{"all equal", []string{"abc", "abc", "abc"}, 0, 3},
		{"differ at 0", []string{"abc", "xbc"}, 0, 0},
		{"differ at 2", []string{"abc", "abx"}, 0, 2},
		// The middle key is where a first-and-last comparison goes wrong: the
		// ends agree to byte 3 and the one between them does not.
		{"middle differs", []string{"aaaa", "aaba", "aaaa"}, 0, 2},
		{"short key bounds it", []string{"aaaaaaaaaaaa", "aa", "aaaaaaaaaaaa"}, 0, 2},
		{"one empty", []string{"aaa", "", "aaa"}, 0, 0},
		// Past the eight-byte word path, with the difference inside a word.
		{"word path then differ", []string{
			"aaaaaaaaaaaaaaaaXb", "aaaaaaaaaaaaaaaaXc"}, 0, 17},
		{"word path exact", []string{
			"aaaaaaaabbbbbbbb", "aaaaaaaabbbbbbbb"}, 0, 16},
		{"start past zero", []string{"zzabc", "zzabx"}, 2, 4},
		{"long shared", []string{
			strings.Repeat("k", 100) + "a", strings.Repeat("k", 100) + "b"}, 0, 100},
	}
	for _, c := range cases {
		if got := commonPrefixEnd(mk(c.keys...), c.d); got != c.want {
			t.Errorf("%s: commonPrefixEnd(%q, %d) = %d, want %d", c.name, c.keys, c.d, got, c.want)
		}
	}
}

// And against a brute-force answer on random groups, since the word path and
// the byte path have to agree everywhere they overlap.
func TestCommonPrefixEndRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for trial := 0; trial < 5000; trial++ {
		n := 1 + rng.Intn(6)
		shared := rng.Intn(30)
		base := make([]byte, shared)
		for i := range base {
			base[i] = byte('a' + rng.Intn(3))
		}
		keys := make([]string, n)
		for i := range keys {
			tail := make([]byte, rng.Intn(6))
			for j := range tail {
				tail[j] = byte('a' + rng.Intn(3))
			}
			keys[i] = string(base) + string(tail)
		}
		p := make([]pair[int], n)
		for i, k := range keys {
			p[i] = pair[int]{k: k, v: i}
		}
		// Brute force: the first position where any key differs from the first,
		// bounded by the shortest.
		want := len(keys[0])
		for _, k := range keys {
			if len(k) < want {
				want = len(k)
			}
		}
		for i := 0; i < want; i++ {
			same := true
			for _, k := range keys {
				if k[i] != keys[0][i] {
					same = false
					break
				}
			}
			if !same {
				want = i
				break
			}
		}
		if got := commonPrefixEnd(p, 0); got != want {
			t.Fatalf("commonPrefixEnd(%q) = %d, want %d", keys, got, want)
		}
	}
}

// permSets returns k distinct shufflings of n keys of one shape.
//
// A sort benchmark that reuses ONE permutation measures a trained branch
// predictor, not a sort. See BenchmarkSortPermutations and docs/wrong.md: the
// same input measured 2,807 ns reusing one permutation and 7,909 ns cycling
// through sixty-four, a factor of 2.8. A real map walk produces a different
// order on every call, because Go randomises map iteration.
func permSets(n, k int) [][]pair[int] {
	base := make([]pair[int], n)
	for i := range base {
		base[i] = pair[int]{k: fmt.Sprintf("field_name_%04d", i), v: i}
	}
	rng := rand.New(rand.NewSource(1))
	out := make([][]pair[int], k)
	for i := range out {
		out[i] = append([]pair[int](nil), base...)
		rng.Shuffle(n, func(a, c int) { out[i][a], out[i][c] = out[i][c], out[i][a] })
	}
	return out
}

// BenchmarkSortPermutations is the honest version, and the copyonly rows are
// the floor to subtract: the loop has to restore the input each iteration.
func BenchmarkSortPermutations(b *testing.B) {
	const k = 64
	for _, n := range []int{16, 64, 256, 1024} {
		ps := permSets(n, k)
		buf := make([]pair[int], n)
		b.Run(fmt.Sprintf("n=%d/sort", n), func(b *testing.B) {
			i := 0
			for b.Loop() {
				copy(buf, ps[i%k])
				i++
				sortPairs(buf)
			}
		})
		b.Run(fmt.Sprintf("n=%d/copyonly", n), func(b *testing.B) {
			i := 0
			for b.Loop() {
				copy(buf, ps[i%k])
				i++
			}
		})
	}
}

// One permutation against many, so the trap has a number attached to it in the
// tree rather than only in docs/wrong.md.
func BenchmarkSortPredictorTrap(b *testing.B) {
	const n, k = 256, 64
	ps := permSets(n, k)
	buf := make([]pair[int], n)
	b.Run("one-permutation", func(b *testing.B) {
		for b.Loop() {
			copy(buf, ps[0])
			sortPairs(buf)
		}
	})
	b.Run("many-permutations", func(b *testing.B) {
		i := 0
		for b.Loop() {
			copy(buf, ps[i%k])
			i++
			sortPairs(buf)
		}
	})
}

// TestSortBucketPath exercises the counting pass, which only runs at bucketMin
// keys and above -- every other sort test in this file is below it, so none of
// them touched this code.
//
// Shapes chosen for what a bucket sort gets wrong: keys that end exactly at a
// bucket boundary (bucket zero), a group that is entirely one repeated key, a
// long shared prefix so the prefix skip runs, keys differing only in the last
// byte, and lengths straddling the eight-byte step in commonPrefixEnd.
func TestSortBucketPath(t *testing.T) {
	shapes := []struct {
		name string
		gen  func(i int) string
	}{
		{"shared prefix + digits", func(i int) string { return fmt.Sprintf("field_name_%04d", i) }},
		{"no shared prefix", func(i int) string { return fmt.Sprintf("%04d_field", i) }},
		{"all identical", func(i int) string { return "same" }},
		{"prefixes of each other", func(i int) string { return strings.Repeat("a", i%40) }},
		{"empty and not", func(i int) string {
			if i%3 == 0 {
				return ""
			}
			return fmt.Sprintf("k%d", i%7)
		}},
		{"last byte only", func(i int) string { return "aaaaaaaaaaaaaaaaaaaa" + string(rune('a'+i%26)) }},
		{"straddles 8 bytes", func(i int) string { return "abcdefgh"[:1+i%8] + fmt.Sprintf("%d", i) }},
		{"high bytes", func(i int) string { return string([]byte{byte(i), byte(i >> 8), 0xff, 0x00}) }},
		{"long", func(i int) string { return strings.Repeat("x", 200) + fmt.Sprintf("%05d", i) }},
	}
	rng := rand.New(rand.NewSource(9))
	for _, sh := range shapes {
		for _, n := range []int{95, 96, 97, 128, 255, 256, 257, 1000, 4096} {
			p := make([]pair[int], n)
			want := make([]string, n)
			for i := range p {
				k := sh.gen(i)
				p[i] = pair[int]{k: k, v: i}
				want[i] = k
			}
			rng.Shuffle(n, func(a, b int) { p[a], p[b] = p[b], p[a] })
			sortPairs(p)
			sort.Strings(want)
			for i := range p {
				if p[i].k != want[i] {
					t.Fatalf("%s n=%d: at %d got %q, want %q", sh.name, n, i, p[i].k, want[i])
				}
			}
			// The values must still name their own keys: a sort that orders the
			// keys correctly while detaching them from their values produces
			// valid-looking JSON with every value under the wrong name.
			for i := range p {
				if got := sh.gen(p[i].v); got != p[i].k {
					t.Fatalf("%s n=%d: entry %d has key %q but value %d, whose key is %q",
						sh.name, n, i, p[i].k, p[i].v, got)
				}
			}
		}
	}
}

// TestNestedSameTypeMaps covers the values slice being reused from the pooled
// encodeState. A map nested inside a map of the SAME type would otherwise share
// that slice with its parent, and because the parent reads it during the write
// loop -- after the child has run -- the result is an object whose names all
// carry the wrong values. Valid JSON, wrong document, and no test would notice
// unless it compared values rather than shape.
func TestNestedSameTypeMaps(t *testing.T) {
	type node map[string]any
	build := func(depth, width int) node {
		var rec func(d int) node
		rec = func(d int) node {
			m := node{}
			for i := 0; i < width; i++ {
				k := fmt.Sprintf("key_%04d", i)
				if d > 0 {
					m[k] = rec(d - 1)
				} else {
					m[k] = fmt.Sprintf("leaf_%d_%d", d, i)
				}
			}
			return m
		}
		return rec(depth)
	}
	// width^(depth+1) nodes, so the pairs are chosen to stay small while still
	// crossing bucketMin at the widest.
	for _, c := range []struct{ width, depth int }{
		{2, 3}, {5, 2}, {40, 1}, {100, 1}, {300, 1}, {8, 2},
	} {
		{
			width, depth := c.width, c.depth
			v := build(depth, width)
			got, err := Marshal(v)
			if err != nil {
				t.Fatalf("width=%d depth=%d: %v", width, depth, err)
			}
			want, err := stdjson.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("width=%d depth=%d:\n got %s\nwant %s", width, depth,
					truncate(string(got)), truncate(string(want)))
			}
		}
	}
	// Reuse across successive Marshal calls through the same pooled state, with
	// different sizes, so a cached slice from a bigger map cannot leak values
	// into a smaller one.
	for _, n := range []int{300, 2, 150, 1, 400} {
		m := node{}
		for i := 0; i < n; i++ {
			m[fmt.Sprintf("k_%04d", i)] = i
		}
		got, err := Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := stdjson.Marshal(m)
		if string(got) != string(want) {
			t.Fatalf("n=%d:\n got %s\nwant %s", n, truncate(string(got)), truncate(string(want)))
		}
	}
}

func truncate(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
