package simdjson

// Parse's grammar descent across workers, completing the family's symmetry:
// the same element extents that shard Valid's walk and Unmarshal's decode
// shard the validating descent, and a Doc for a clean document is then
// assembled directly -- root Value from the index, navigating set -- without
// any serial walk at all.
//
// The exactness policy is Unmarshal's: commit only when every element
// validates and ends exactly at its extent. Anything else reruns the serial
// finish, which owns the error and its position literally.

import (
	"runtime"
	"sync"
)

// walkTopParallel validates a root array's elements across workers, ranges
// byte-balanced, and returns one past the root's closing bracket. ok=false
// means a worker found an element wanting -- or the split declined -- and the
// caller's serial walk owns whatever is wrong.
func walkTopParallel(data []byte, ix *index, elems []topExtent, rootClose int) (int, bool) {
	maxw := runtime.GOMAXPROCS(0)
	if maxw > parallelMaxProcs {
		maxw = parallelMaxProcs
	}
	ranges := splitTopWork(elems, maxw)
	if ranges == nil {
		return 0, false
	}
	fail := make([]bool, len(ranges))
	var wg sync.WaitGroup
	for w, r := range ranges {
		lo, hi := r[0], r[1]
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			my := *ix
			my.wsW, my.wsX = -1, 0
			pd := &Doc{data: data, ix: &my, inStr: my.inStr, noWS: my.noWS, wsw: my.wsw}
			for i := lo; i < hi; i++ {
				end, err := pd.validateValue(int(elems[i].startB))
				if err != nil || end != int(elems[i].endB) {
					fail[w] = true
					return
				}
			}
		}(w, lo, hi)
	}
	wg.Wait()
	for _, f := range fail {
		if f {
			return 0, false
		}
	}
	return int(ix.pos[rootClose]) + 1, true
}

var parseParallelMinElems = 2

// finishParallel builds the Doc for a large root array of containers, or
// declines.
func finishParallel(data []byte, ix *index) (*Doc, bool) {
	elems, rootClose, shaped := enumerateTopContainers(ix, data, parseParallelMinElems)
	if !shaped {
		return nil, false
	}
	if _, ok := walkTopParallel(data, ix, elems, rootClose); !ok {
		return nil, false
	}
	d := &Doc{data: data, ix: ix, inStr: ix.inStr, noWS: ix.noWS, wsw: ix.wsw}
	d.root = Value{d: d, kind: Array, start: int(ix.pos[0]), end: int(ix.pos[rootClose]) + 1}
	d.navigating = true
	return d, true
}
