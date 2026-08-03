# How the others use more than one core, and where this one should

Read from source: minio/simdjson-go v0.4.5, sonic v1.15.2, the C++ simdjson
4.6.4 amalgamated header, plus fastjson, goccy/go-json and gjson to confirm they
do nothing. Every claim has a file and a line.

The reason to care: this package indexes at 3,637 MB/s, unmarshals in memory at
about 1,960 MB/s, and streams 10 GB of NDJSON at 956 MB/s. Stage one is not the
bottleneck in that last number — it is roughly eleven times faster than the
number it appears in. Everything below is about which core does what.

## Who does anything at all

| library | parallelism inside one call |
|---|---|
| minio/simdjson-go | **yes** — two separate mechanisms |
| C++ simdjson | **yes** — one worker thread, on by default |
| sonic | **no.** Every `go func` in the repository is in a `_test.go` |
| fastjson, goccy, gjson | **no.** Zero goroutines in production code |

sonic is worth stating plainly because it is easy to mistake for concurrency: it
has `sync.Pool` everywhere (`internal/encoder/vars/stack.go:42`,
`internal/decoder/jitdec/pools.go:44`) and it is safe to call from many
goroutines. Neither is parallelism. Its streaming decoder is
`json.NewDecoder(r)` — literally the standard library
(`decoder/decoder_compat.go:188`).

## minio, mechanism one: a chunk per goroutine

`ParseNDStream`, `simdjson_amd64.go:116`. Read up to 10 MiB, extend to the next
newline, hand the chunk to a fresh goroutine.

	const tmpSize = 10 << 20
	conc := (runtime.GOMAXPROCS(0) + 1) / 2
	queue := make(chan chan Stream, conc)

The clever part is how order survives. `queue` is a channel **of channels** — a
future per chunk. The reader pushes a result channel into `queue` *before*
spawning the goroutine that will fill it (`:177`), and a forwarder goroutine
drains `queue` strictly FIFO, blocking on each future in turn (`:138`). So
chunks are parsed out of order and delivered in order, and the queue's capacity
is the whole of the backpressure: the reader blocks at `queue <- result` once
`conc` chunks are in flight.

Splitting is scan-first, not speculate-and-repair (`:163`): after reading
10 MiB it calls `buf.ReadBytes('\n')` and appends, so a chunk always ends on a
newline and a record never straddles one. That is sound for NDJSON because an
unescaped newline inside a string is invalid JSON, which its own stage one
already rejects. The cost is that a stream with no newlines grows a chunk
without bound.

**Two things it gets wrong, which are the interesting part.**

*No cancellation.* After the first error is delivered it sets `end = true`
(`:143`) but the reader keeps reading and the workers keep parsing the rest of
the input. Worse, if the consumer stops ranging — which is exactly what its own
documentation tells you to do after an error — the forwarder parks forever on
`res <- i` (`:145`), holding `conc`+2 goroutines and `conc` × 10 MiB.

*No byte offsets.* A chunk error is `fmt.Errorf("parsing input: %w", parseErr)`
(`:196`) against a recycled copy whose absolute position in the stream was never
recorded. So in the one path designed for enormous inputs, an error cannot say
where it happened.

## minio, mechanism two: stage two on its own goroutine

`parseMessage`, `parse_json_amd64.go:74`. Above 8 KiB, stage two runs
concurrently with stage one on a single document:

	if len(pj.Message) > 8<<10 {
	    go func() { ... pj.unifiedMachine() ... }()
	    ...
	    pj.findStructuralIndices()
	    wg.Wait()

The handoff is a ring of sixteen index buffers (`parsed_json.go:73`) and a
channel whose capacity is deliberately `indexSlots-2 = 14`, with the reason in
the source: the sender blocks while the consumer still holds the slot it is
working on, so the ring is never overwritten in use. Stage one takes a slot with
`atomic.AddUint64(&pj.buffersOffset, 1)` (`stage1_find_marks_amd64.go:72`) and
terminates with a sentinel `indexChan{index: -1}`. Single producer, single
consumer, FIFO channel — order and shutdown are trivial by construction.

Ninety-six kilobytes of ring, and a hard ceiling of two-way overlap.

## C++ simdjson: exactly one worker, and it works one batch ahead

`struct stage1_worker` (`simdjson.h:6262`) holds one `std::thread`, a mutex, a
condition variable, and **a second complete parser** (`:6533`). The worker runs
stage one of the *next* batch while the main thread runs stage two of the
current one, and the handoff is a swap (`:10002`):

	worker->finish();
	std::swap(*parser, stage1_thread_parser);
	error = stage1_thread_error;

It is on by default (`bool threaded{true}`, `:6137`), enabled whenever the
compiler says threads exist (`SIMDJSON_THREADS_ENABLED`, `:343`). Batches are
`DEFAULT_BATCH_SIZE = 1000000` (`:5152`) and a document straddling a batch
boundary is not split — its structural indexes are withheld and it is re-scanned
from its start in the next batch (`streaming_partial`, `:3597`).

Memory is two parsers, flat, whatever the core count. That is the whole design.

## Nobody parallelises stage one

Checked in all four codebases and it is not there. The carry chain —
backslash parity, in-string state, the pseudo-structural predecessor — flows
left to right through every 64-byte block and nobody breaks it. minio threads
`prev_iter_ends_odd_backslash`, `prev_iter_inside_quote` and
`prev_iter_ends_pseudo_pred` through its loop
(`stage1_find_marks_amd64.go:44`); its sixteen-buffer ring is pipeline handoff,
not parallel stage one. C++ runs one stage one per batch on one thread.

Nobody speculates both carry states and picks, and nobody processes blocks
independently and fixes up. There is no precedent to copy, which is worth
knowing before treating it as an obvious win.

## Where it belongs here

The numbers say where, and they say it is not stage one. Stage one is already
the fastest thing in the library at 3,637 MB/s; splitting it across cores means
breaking a sequential carry chain for the stage with the least headroom. The
streaming bottleneck is stage two, the per-record overhead, and the chunk
copies.

So, in order:

1. **Per-chunk parallelism in the streaming Decoder** — minio's shape, with the
   newline-boundary scan, which is correct and nearly free. Two changes to what
   minio did: a **fixed worker pool** rather than a goroutine per chunk, and the
   two things it lacks — the **absolute chunk-start offset** carried through so
   an error points into the original stream, and **cancellation**, so the first
   delivered error stops the reader and lets in-flight work drain instead of
   parsing another nine gigabytes.
2. **Stage two overlapping stage one for a single in-memory document**, behind a
   threshold around 8-16 KiB. Cheap, sound, bounded by a small ring, and it is
   the one design both minio and C++ simdjson converged on independently.
3. **Stage one internally** — not until stage one is the measured bottleneck,
   which it is not.

One limit on all of this: minio pays head-of-line blocking to keep order —
one slow chunk stalls every later result — and nothing in the field does
out-of-order completion with reordering. That is available and untried, and it
is also more machinery than the problem has yet been shown to need.
