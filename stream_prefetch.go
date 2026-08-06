package simdjson

// One-batch-ahead preparation for the streaming Decoder.
//
// While the caller drains a batch, a background task prepares the next one
// from bytes ALREADY in the buffer: skip the gap, build the partial-mode
// index, scanRoot, and — when the Value staging is armed — validate it too.
// On exhaustion the mainline swaps the prepared batch in and pays none of
// that; stage one leaves the critical path the way C++ simdjson's worker
// thread takes it off, except nothing here touches the io.Reader. The
// background task never reads: a slow stream just leaves the prefetch idle,
// and the reader-facing semantics — what is read, when, Buffered(),
// InputOffset() — are byte-for-byte those of the serial path.
//
// Lifecycle. The prepared batch aliases d.buf, and fill is the only thing
// that moves d.buf (growth and compaction), so fill's entry is the one join
// point that drops a prefetch; the swap in load is the one that uses it. A
// swap is taken only when the cursor sits exactly where the preparation
// started — a Token walk, single-value mode, an error path, anything at all
// that moves differently just fails the guard and loads serially, wasting
// the background work and nothing else. Background errors are never
// surfaced: a batch that fails to prepare is left for the serial path to
// load, so the caller sees exactly the serial error from exactly the serial
// position.

import "sync"

// streamPrefetchMin is the fewest unclaimed buffered bytes worth preparing.
// A var for the tests.
var streamPrefetchMin = 4 << 10

// streamAheadChunk caps a top-level batch when the buffer holds at least
// twice this much, so one buffer spans several batches and the background
// task always has the next one to prepare. Uncapped, a batch runs to the end
// of everything buffered and the prefetch starves — it measured within noise
// of plain staging on a 64 MB in-memory stream, because the material for
// batch k+1 only ever arrived after batch k was exhausted.
var streamAheadChunk = 1 << 20

// topWindow is where the top-level index stops looking: everything buffered,
// or one ahead-chunk of it when enough is buffered that capping leaves the
// prefetch real work. Serial mode — prefetch disabled — always sees
// everything, so nothing about the old path changes.
func (d *Decoder) topWindow() int {
	if rem := len(d.buf) - d.off; rem >= 2*streamAheadChunk && rem >= streamPrefetchMin {
		return d.off + streamAheadChunk
	}
	return len(d.buf)
}

type prefetch struct {
	wg   sync.WaitGroup
	busy bool // a task was launched and has not been joined
	// Written by the task, read after wg.Wait.
	ok    bool
	start int // absolute offset the batch begins at, after the gap
	doc   *Doc
	data  []byte
	elems []topExtent // validation staging for the batch, when armed
	bad   int
}

// pfJoin waits out any in-flight preparation. Call before reading the result
// or before anything moves d.buf.
func (d *Decoder) pfJoin() {
	if d.pf.busy {
		d.pf.wg.Wait()
		d.pf.busy = false
	}
}

// pfDrop discards any preparation, joined first.
func (d *Decoder) pfDrop() {
	d.pfJoin()
	d.pf.ok = false
}

// pfLaunch starts preparing the next top-level batch, if the buffer holds
// enough past the current one to be worth it. Called with a batch just
// installed; the task reads only d.buf[nb:] and fields captured here.
func (d *Decoder) pfLaunch() {
	d.pfDrop()
	if len(d.tstack) > 0 || d.single {
		return
	}
	nb := d.base + len(d.data)
	if len(d.buf)-nb < streamPrefetchMin {
		return
	}
	validate := d.valStreak >= streamStageStreak
	if d.pfIx == nil {
		d.pfIx, _ = indexPool.Get().(*index)
	}
	ix := d.pfIx
	buf := d.buf
	useNumber, disallow := d.useNumber, d.disallowUnknown
	d.pf.busy = true
	d.pf.wg.Add(1)
	go func() {
		defer d.pf.wg.Done()
		d.pf.ok = false
		i := nb
		for i < len(buf) && isJSONSpace[buf[i]] {
			i++
		}
		if i >= len(buf) || !canStartValue[buf[i]] {
			return
		}
		hi := len(buf)
		if hi-i >= 2*streamAheadChunk {
			hi = i + streamAheadChunk
		}
		nix, err := buildIndexMode(buf[i:hi], ix, true, false, true)
		d.pfIx = nix
		if err != nil || nix.safeEnd == 0 {
			return
		}
		if nix.partErr != nil && nix.partErrAt < nix.safeEnd {
			return
		}
		data := buf[i : i+nix.safeEnd]
		doc, err := scanRootCapped(data, nix)
		if err != nil {
			return
		}
		doc.strictSkip = true
		doc.useNumber, doc.disallowUnknown = useNumber, disallow
		d.pf.start, d.pf.doc, d.pf.data = i, doc, data
		d.pf.elems, d.pf.bad = d.pf.elems[:0], 0
		if validate {
			if elems, ok := enumerateBatchValues(nix, data, streamStageMinElems); ok &&
				len(data) >= streamStageMinBytes {
				d.pf.elems, d.pf.bad = stageValidateElems(doc, elems)
			}
		}
		d.pf.ok = true
	}()
}

// pfTake installs the prepared batch when it begins exactly at the cursor.
// It reports whether it did; the caller returns into deliveries on true.
func (d *Decoder) pfTake() bool {
	if !d.pf.busy && !d.pf.ok {
		return false
	}
	d.pfJoin()
	if !d.pf.ok || d.pf.start != d.off || len(d.tstack) > 0 || d.single {
		d.pf.ok = false
		return false
	}
	d.pf.ok = false
	// The live doc's index buffer becomes the next preparation's scratch.
	d.pfIx, d.ix = d.ix, d.pfIx
	d.doc, d.data = d.pf.doc, d.pf.data
	d.base, d.cur = d.pf.start, 0
	d.stElems = append(d.stElems[:0], d.pf.elems...)
	d.stNext, d.stBad = 0, d.pf.bad
	d.pfLaunch()
	return true
}
