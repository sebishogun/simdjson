package simdjson

// Sorting a map's keys, which encoding/json always does and which is 42% of
// marshalling a decoded document.
//
// # Why not slices.SortFunc
//
// It was slices.SortFunc with a string comparison, and that is two costs. The
// comparator is a function value, so every one of the n log n comparisons is an
// indirect call the compiler cannot see through. And each of those calls then
// compares two whole strings, walking a common prefix that the *previous*
// comparison already walked -- JSON keys in one object share prefixes
// constantly ("id", "id_str", "in_reply_to_status_id", "in_reply_to_user_id").
//
// A three-way radix quicksort has neither. It partitions on one byte at a
// time, so a prefix is examined once for the whole subarray rather than once
// per comparison, and the byte is read inline with no call at all. This is
// Bentley and Sedgewick's Quick3string; sonic uses the same algorithm for the
// same reason, which is what the 1.67x difference in sort time was.
//
// # Why generic over the value
//
// There are two pair types -- the reflect encoder holds a reflect.Value, the
// map[string]any encoder holds an any -- and a sort shared between them must
// not reintroduce the indirection it exists to remove. A generic over the value
// type is stenciled per GC shape, so p[i].k is a direct field load and the swap
// is a direct move in both instantiations. A shared sort taking a key accessor,
// or a constraint with a key() method, would put the call back.
//
// # Layout
//
//	pair        one entry, key and value together
//	sortPairs   the entry point
//	radixQsort  the algorithm
//	insertPairs / heapPairs   the small and the pathological cases
//
// Correctness is not negotiable here: the output bytes have to match
// encoding/json exactly, so the order has to. sortmap_test.go compares this
// against sort.Strings on adversarial key sets -- shared prefixes, keys that
// are prefixes of each other, empty keys, non-ASCII, and every permutation of
// small sets -- and the marshal fuzzer covers it end to end.

import "math/bits"

// pair is one map entry, held while the keys are sorted.
//
// Key and value travel together rather than sorting the keys alone and looking
// each value up afterwards. That second hash was 10% of marshalling a decoded
// document, for something the range already had in hand.
type pair[V any] struct {
	k string
	v V
}

// sortPairs orders entries by key, as encoding/json does: by bytes, not by
// runes or by any collation.
func sortPairs[V any](p []pair[V]) {
	if len(p) < 2 {
		return
	}
	radixQsort(p, 0, 2*bits.Len(uint(len(p)+1)))
}

// radixQsort sorts p by the bytes from d onward.
//
// Every key in p is known to share the same first d bytes, which is what makes
// this cheaper than a comparison sort: that prefix is never looked at again.
func radixQsort[V any](p []pair[V], d, maxDepth int) {
	for len(p) > 11 {
		// Three-way partition on byte d: less, equal, greater. The equal group
		// is the one that advances to d+1, and it is why a shared prefix costs
		// one pass rather than one pass per comparison.
		pv := pivotByte(p, d)
		lt, i, gt := 0, 0, len(p)
		for i < gt {
			switch c := byteAt(p[i].k, d); {
			case c < pv:
				p[lt], p[i] = p[i], p[lt]
				i++
				lt++
			case c > pv:
				gt--
				p[i], p[gt] = p[gt], p[i]
			default:
				i++
			}
		}

		// A level that split nothing only moved the radix along, and that must
		// not be charged to the introsort budget.
		//
		// The budget exists for pivots that split badly, and a level where every
		// key shares the byte has not split at all -- it has advanced d, which is
		// guaranteed progress, because keys are finite and d running past the
		// longest one ends the loop. Charging it was a real defect rather than a
		// tuning question: JSON keys in one object share prefixes constantly, and
		// with a budget of 2*ceil(log2(n+1)) -- 14 at n=64 -- an eleven-byte
		// common prefix spent eleven of it before any partitioning began. The
		// escape hatch for adversarial input was firing on the ordinary case.
		//
		//	shared prefix   n=16   n=64   n=256    heapPairs calls
		//	none               0      0       0
		//	2 bytes            0      0       0
		//	11 bytes           1      2       1
		//	37 bytes           1      1       1
		if lt == 0 && gt == len(p) {
			if pv < 0 {
				// Every key ended at d and they share the first d bytes, so
				// they are all the same string. Nothing left to order.
				break
			}
			// The whole group shares byte d, so partitioning again at d+1 would
			// read all n keys to learn the same thing. Find where the group
			// stops agreeing and jump straight there.
			//
			// A partition costs one byte read per key per level, so a p-byte
			// common prefix costs p*n of them -- and JSON keys share prefixes by
			// construction. The scan below reads eight bytes at a time and does
			// no swapping, so the same prefix costs about p*n/8 with a much
			// smaller constant.
			//
			//	n=256   prefix=0    2,807 ns
			//	        prefix=11   4,742      +69% before this
			//	        prefix=64  13,899     +395%
			d = commonPrefixEnd(p, d+1)
			continue
		}

		// Introsort's escape. Three-way radix partitioning degrades on inputs
		// built to defeat the pivot choice, and a JSON encoder takes its keys
		// from whatever the caller decoded, which can be an attacker.
		if maxDepth == 0 {
			heapPairs(p)
			return
		}
		maxDepth--

		// Recurse into the two smaller parts and loop on the largest, so the
		// stack is bounded by log n however the partition falls.
		if pv < 0 {
			// The equal group is the keys that ended at d. They share the
			// first d bytes and have nothing after, so they are the same
			// string -- and a map has each key once, so there is at most one.
			// Nothing to sort there.
			if lt > len(p)-gt {
				radixQsort(p[gt:], d, maxDepth)
				p = p[:lt]
			} else {
				radixQsort(p[:lt], d, maxDepth)
				p = p[gt:]
			}
			continue
		}
		switch lo, eq, hi := lt, gt-lt, len(p)-gt; {
		case lo >= eq && lo >= hi:
			radixQsort(p[lt:gt], d+1, maxDepth)
			radixQsort(p[gt:], d, maxDepth)
			p = p[:lt]
		case eq >= hi:
			radixQsort(p[:lt], d, maxDepth)
			radixQsort(p[gt:], d, maxDepth)
			p, d = p[lt:gt], d+1
		default:
			radixQsort(p[:lt], d, maxDepth)
			radixQsort(p[lt:gt], d+1, maxDepth)
			p = p[gt:]
		}
	}
	insertPairs(p, d)
}

// commonPrefixEnd returns the first byte position at or after d where the keys
// in p stop agreeing.
//
// Every key already shares everything before d, so this only has to find where
// that run ends. It is bounded by the shortest key: a key that ends is a key
// that differs from a longer one, and byteAt's -1 sentinel makes that ordering
// work, so the run cannot continue past it.
//
// Checked against every key rather than against the first and last. Two keys
// agreeing to byte k says nothing about a third between them, and getting that
// wrong would sort by a prefix nobody shares -- silently, since the result
// would still be a permutation.
func commonPrefixEnd[V any](p []pair[V], d int) int {
	// The shortest key bounds the answer, and it is cheaper to find than to
	// re-test len() inside the byte loop for every key.
	end := len(p[0].k)
	for i := 1; i < len(p); i++ {
		if n := len(p[i].k); n < end {
			end = n
		}
	}
	ref := p[0].k
	for d < end {
		// Eight bytes at a time while every key still agrees with the first.
		if d+8 <= end {
			w := le64(ref, d)
			ok := true
			for i := 1; i < len(p); i++ {
				if le64(p[i].k, d) != w {
					ok = false
					break
				}
			}
			if ok {
				d += 8
				continue
			}
		}
		c := ref[d]
		for i := 1; i < len(p); i++ {
			if p[i].k[d] != c {
				return d
			}
		}
		d++
	}
	return d
}

// le64 reads eight bytes of s at i as one word. The caller has checked that
// i+8 <= len(s).
func le64(s string, i int) uint64 {
	_ = s[i+7]
	return uint64(s[i]) | uint64(s[i+1])<<8 | uint64(s[i+2])<<16 | uint64(s[i+3])<<24 |
		uint64(s[i+4])<<32 | uint64(s[i+5])<<40 | uint64(s[i+6])<<48 | uint64(s[i+7])<<56
}

// insertPairs is the small case. Eleven is where the partitioning stops paying
// for its pivot.
func insertPairs[V any](p []pair[V], d int) {
	for i := 1; i < len(p); i++ {
		for j := i; j > 0 && lessFrom(p[j].k, p[j-1].k, d); j-- {
			p[j], p[j-1] = p[j-1], p[j]
		}
	}
}

// heapPairs is the escape, and it compares whole keys rather than keys from d.
// The prefix before d is equal across p, so the two orders are the same one.
func heapPairs[V any](p []pair[V]) {
	n := len(p)
	for i := (n - 1) / 2; i >= 0; i-- {
		siftPairs(p, i, n)
	}
	for i := n - 1; i > 0; i-- {
		p[0], p[i] = p[i], p[0]
		siftPairs(p, 0, i)
	}
}

func siftPairs[V any](p []pair[V], root, hi int) {
	for {
		child := 2*root + 1
		if child >= hi {
			return
		}
		if child+1 < hi && p[child].k < p[child+1].k {
			child++
		}
		if p[root].k >= p[child].k {
			return
		}
		p[root], p[child] = p[child], p[root]
		root = child
	}
}

// pivotByte chooses the byte to partition on: median of three, or Tukey's
// ninther once there are enough keys to pay for it.
func pivotByte[V any](p []pair[V], d int) int {
	n := len(p)
	m := n >> 1
	if n > 40 {
		t := n / 8
		return medianOf3(
			medianOf3(byteAt(p[0].k, d), byteAt(p[t].k, d), byteAt(p[2*t].k, d)),
			medianOf3(byteAt(p[m-t].k, d), byteAt(p[m].k, d), byteAt(p[m+t].k, d)),
			medianOf3(byteAt(p[n-1-2*t].k, d), byteAt(p[n-1-t].k, d), byteAt(p[n-1].k, d)))
	}
	return medianOf3(byteAt(p[0].k, d), byteAt(p[m].k, d), byteAt(p[n-1].k, d))
}

func medianOf3(i, j, k int) int {
	if i > j {
		i, j = j, i
	}
	if k < i {
		return i
	}
	if k > j {
		return j
	}
	return k
}

// byteAt is s[i] with -1 past the end, so that a key which is a prefix of
// another sorts before it without a length test in the partition loop.
func byteAt(s string, i int) int {
	if i < len(s) {
		return int(s[i])
	}
	return -1
}

// lessFrom compares two keys from byte d, the bytes before it being equal.
func lessFrom(a, b string, d int) bool {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := d; i < n; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}
