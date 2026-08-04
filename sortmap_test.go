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
	radixQsort(p, 0, 0)
	want := slices.Clone(keys)
	sort.Strings(want)
	for i := range p {
		if p[i].k != want[i] {
			t.Fatalf("radixQsort maxDepth=0: at %d got %q, want %q", i, p[i].k, want[i])
		}
	}
}
