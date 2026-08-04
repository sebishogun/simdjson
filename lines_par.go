package simdjson

// Reading line-delimited JSON on more than one core.
//
// NDJSON is the one shape that splits safely: a newline outside a string ends a
// record, and JSON forbids a literal newline inside a string, so any newline in
// the input really is a boundary. No scanning for context, no speculation, no
// repair.
//
// The design is minio's — chunks read on one goroutine, parsed on several,
// delivered in order — with the things its implementation gets wrong fixed. See
// docs/parallelism.md for where each piece came from.
//
//   - minio spawns a goroutine per chunk. This uses a fixed pool, so a fast
//     producer cannot start ten thousand goroutines.
//   - minio's chunk errors carry no position: the chunk is a recycled copy and
//     its offset in the stream was never recorded, so in the one path built for
//     enormous inputs an error cannot say where it happened. Every chunk here
//     carries its absolute start, and an error is rebased through both the
//     record's position in the chunk and the chunk's position in the stream.
//   - minio never stops. After the first error the reader keeps reading and the
//     workers keep parsing, and if the consumer stops ranging — which its own
//     documentation says to do — the forwarder parks forever. Here the first
//     error, or a false from the callback, cancels the reader and the workers
//     drain.
//
// Order is kept the way minio keeps it, which is the good part of its design: a
// channel of channels. The reader pushes a result channel into the queue before
// it hands the chunk to a worker, so the queue is in input order by
// construction, and the consumer takes futures off it and blocks on each in
// turn. Chunks are parsed out of order and delivered in order.

import (
	"bufio"
	"io"
	"runtime"
	"sync"
)

// parChunk is the unit of work: a run of whole records, and where it started.
//
// buf and ix come from the free list and go back to it when the consumer has
// finished with the Doc built over them. Without that this allocated a megabyte
// and an index per chunk -- 30.6 MB per pass over a 200,000-record stream
// against the sequential path's 1.29 -- which is bounded live memory and
// unbounded garbage.
type parChunk struct {
	data []byte
	base int64
	buf  *parBuf
}

// parBuf is a chunk's reusable storage: the bytes read into it and the index
// built over them.
type parBuf struct {
	data []byte
	ix   *index
}

// parResult is one chunk, indexed, and where each record starts in it.
//
// One index for the whole chunk, not one per record. That is the difference
// between this being worth doing and not. A Doc per record would be an
// allocation per record — which is what made the first ForEachLine 1.8x slower
// than the loop it replaced — and splitting on the worker only to parse again
// on the consumer would do the work twice and parallelise none of it. The chunk
// is indexed once, on a worker, and the consumer turns offsets into Values
// without parsing anything.
type parResult struct {
	doc  *Doc
	offs []int
	base int64
	err  error
	buf  *parBuf // returned to the free list once the consumer is done
}

// lineWorkers is how many goroutines index chunks.
//
// GOMAXPROCS/2, which is minio's choice, capped at four — and the cap is where
// the measurement put it, not a guess. Throughput against GOMAXPROCS, 200,000
// records:
//
//	GOMAXPROCS    2      4      8     16     32
//	MB/s        649  1,185  1,180  1,185  1,153
//
// Two workers saturate it and the rest is contention. The reason is that the
// parallelism is in the indexing while the consumer still constructs every
// Value and runs the callback on one goroutine, so the consumer is the wall and
// no number of workers moves it.
//
// The cap matters because memory is workers times the chunk size: sixteen
// workers is sixteen megabytes in flight for the throughput four gets.
func lineWorkers() int {
	n := runtime.GOMAXPROCS(0) / 2
	if n < 1 {
		return 1
	}
	if n > 4 {
		return 4
	}
	return n
}

// parChunkBytes is how much input a chunk holds before it is cut at the next
// newline.
//
// 1 MiB, which is C++ simdjson's DEFAULT_BATCH_SIZE. minio uses 10 MiB, which
// makes each worker's footprint ten times larger without a measurement behind
// it.
const parChunkBytes = 1 << 20

type parJob struct {
	chunk parChunk
	out   chan parResult
}

// ForEachLineReaderParallel is [ForEachLineReader] across several goroutines.
//
// fn is called on the calling goroutine, once per record, in input order. The
// parallelism is in the indexing, not in the callback, so fn needs no locking
// and sees records in the order they appeared.
//
// The Value passed to fn is valid for the duration of the call.
//
// Memory is bounded by the number of workers times the chunk size, whatever the
// length of the input.
//
// An error stops the reader, drains the workers and is returned; so does fn
// returning false, without an error. The records before an error in the same
// chunk are still delivered — they are still good, and dropping them would
// silently truncate at a chunk boundary. Errors carry the byte offset in the
// whole stream, not in the chunk they were found in.
func ForEachLineReaderParallel(r io.Reader, fn func(Value) bool) error {
	workers := lineWorkers()
	if workers == 1 {
		return ForEachLineReader(r, fn)
	}

	queue := make(chan chan parResult, workers)
	jobs := make(chan parJob, workers)
	// One buffer per in-flight chunk, and there are at most as many in flight
	// as the queue is deep. Buffered so a return never blocks the consumer.
	free := make(chan *parBuf, workers+2)
	for i := 0; i < workers+2; i++ {
		free <- &parBuf{}
	}

	var cancelOnce sync.Once
	cancel := make(chan struct{})
	stop := func() { cancelOnce.Do(func() { close(cancel) }) }

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				j.out <- splitChunk(j.chunk)
				close(j.out)
			}
		}()
	}

	go func() {
		defer close(jobs)
		defer close(queue)
		br := bufio.NewReaderSize(r, parChunkBytes)
		var base int64
		for {
			var buf *parBuf
			select {
			case buf = <-free:
			case <-cancel:
				return
			}
			chunk, err := readChunk(br, buf, parChunkBytes)
			if len(chunk) > 0 {
				out := make(chan parResult, 1)
				select {
				case queue <- out:
				case <-cancel:
					return
				}
				select {
				case jobs <- parJob{parChunk{chunk, base, buf}, out}:
				case <-cancel:
					// The future is already in the queue and no worker will
					// ever fill it, so close it or the consumer's drain waits
					// on it forever. This is the deadlock minio has, found here
					// by running the cancellation test under -race three times.
					close(out)
					return
				}
				base += int64(len(chunk))
			}
			if err != nil {
				if err != io.EOF {
					// Through a future of its own, so it arrives in stream
					// order after everything read before it.
					out := make(chan parResult, 1)
					select {
					case queue <- out:
						out <- parResult{err: err}
						close(out)
					case <-cancel:
					}
				}
				return
			}
		}
	}()

	var ret error
	for out := range queue {
		res, ok := <-out
		if !ok {
			continue
		}
		done := false
		for _, off := range res.offs {
			v, _, err := res.doc.value(off)
			if err != nil {
				ret, done = rebase(err, res.base), true
				break
			}
			if !fn(v) {
				done = true
				break
			}
		}
		if !done && res.err != nil {
			ret, done = res.err, true
		}
		// The Doc built over this chunk is dead the moment the callback has
		// seen the last value in it, so the buffer and its index go back.
		if res.buf != nil {
			select {
			case free <- res.buf:
			default:
			}
		}
		if done {
			stop()
			break
		}
	}
	// Drain, so the reader and the workers can finish rather than park.
	for out := range queue {
		<-out
	}
	wg.Wait()
	return ret
}

// splitChunk indexes a chunk once and records where each value in it begins.
//
// Partial mode is what allows an index over a chunk holding many top-level
// values; an index is otherwise for exactly one. The index is not pooled
// between chunks, because the Doc built over it is handed to the consumer and
// outlives the worker's next job. One index per megabyte is not the allocation
// worth chasing.
func splitChunk(c parChunk) parResult {
	var reuse *index
	if c.buf != nil {
		reuse = c.buf.ix
	}
	ix, err := buildIndexMode(c.data, reuse, true, false, true)
	if c.buf != nil {
		c.buf.ix = ix
	}
	if err != nil {
		return parResult{base: c.base, err: rebase(err, c.base), buf: c.buf}
	}
	d := &Doc{data: c.data, ix: ix, inStr: ix.inStr, noWS: ix.noWS, wsw: ix.wsw}
	d.navigating = true

	var offs []int
	for i := d.skip(0); i < len(c.data); {
		v, end, verr := d.value(i)
		if verr == nil {
			verr = v.validate()
		}
		if verr != nil {
			// c.base alone: the index was built over the chunk, so the
			// error's offset is already relative to it. Adding the record's
			// offset too overshoots by exactly that, which the stream-offset
			// test catches to the byte.
			return parResult{doc: d, offs: offs, base: c.base,
				err: rebase(verr, c.base), buf: c.buf}
		}
		offs = append(offs, i)
		i = d.skip(end)
	}
	return parResult{doc: d, offs: offs, base: c.base, buf: c.buf}
}

// rebase moves a SyntaxError's offset onto the whole stream. minio does not do
// this, and so cannot say where an error in a ten gigabyte file happened.
func rebase(err error, base int64) error {
	se, ok := err.(*SyntaxError)
	if !ok {
		return err
	}
	return &SyntaxError{msg: se.msg, Offset: se.Offset + base}
}

// readChunk reads about n bytes and then to the end of the line, so a chunk
// never cuts a record in half.
func readChunk(br *bufio.Reader, pb *parBuf, n int) ([]byte, error) {
	buf := pb.data[:0]
	if cap(buf) < n+4096 {
		buf = make([]byte, 0, n+4096)
	}
	for {
		chunk, err := br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if err == bufio.ErrBufferFull {
			continue
		}
		pb.data = buf
		if err != nil {
			return buf, err
		}
		if len(buf) >= n {
			return buf, nil
		}
	}
}
