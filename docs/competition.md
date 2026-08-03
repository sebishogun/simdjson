# What the other libraries do, and where they beat this one

Read from source rather than from their READMEs, in August 2026, at the versions
in this module's cache. Every claim below has a file and a function behind it.

The point of the exercise was not to admire them. It was to find the specific
things they do that this does not, and decide which are reachable from portable
Go with no cgo and no run-time code generation.

## Where each one leads

| operation | leader | this | margin | why they lead |
|---|---|---|---|---|
| parse a 631 KB document | fastjson 238 µs | 251 µs | 1.05× | decodes nothing — records extents and defers |
| parse 1.7 MB / 2.25 MB | **this** | 689 / 1,592 µs | 1.02× / 1.26× | the index amortises over a bigger document |
| index without validating | **this** | 61 µs | 3.9× | `Scan` is masks only |
| unmarshal into a struct | **this** | 390 µs | 1.02× over goccy | compiled decoder over an index |
| marshal | sonic 36 µs | 76 µs | 2.1× | JIT to machine code + assembly quote |
| one field from a big document | gjson | — | up to 1000× | scans to the field and stops; keeps nothing |
| validate | sonic | 3,637 MB/s | 1.02× ours on twitter | hand-written assembly state machine |
| 10 GB in one piece | **this** | 833 MB/s | — | `SIMDJSON_MAXSIZE_BYTES` caps a C++ document at 4 GB − 1 |

`encoding/json/v2`, new in Go 1.26 behind `GOEXPERIMENT=jsonv2`, is **not** a
threat yet: 686 µs to unmarshal twitter against v1's 834 and this package's 322,
and identical to v1 on marshal. When it becomes the default every Go program
gets it for free, so it is the one to watch, but it is 2.1× behind today.

## minio/simdjson-go — the same design, done differently

The direct competitor: also two-stage, also SIMD, also Go. It is slower than
this on all three corpora, and it does four things better.

**Delta offsets, not absolute ones.** It stores the *distance from the previous
structural character* in a `uint32`. `flatten_bits_amd64.s:47` writes
`MOVL ZEROS, (DI)(INDEX*4)` where `ZEROS` is the trailing-zero count plus one —
a gap, not an offset — and `stage2_build_tape_amd64.go:42` puts it back together
with a running sum, `idx = idx_in + uint64(indexes[index])`. A delta never
exceeds the gap between two structural characters, so the width of the integer
never bounds the size of the document.

**It does not port to this package as it stands, and the reason is worth
writing down.** minio's stage two walks the index strictly forward, so a running
sum is free. This package *navigates*: `matchBracket` (`simdjson.go:792`) takes
an opening bracket's byte offset, gallops and then bisects over `pos` to find
its entry, and then reads `pos[match[k]]` — an entry an arbitrary distance
ahead. Both of those need random access to an ordered array, and neither
survives delta encoding. So the delta is not a fix to copy: minio gets it for
free from a design this package deliberately does not share, and pays for it by
being unable to jump.

What removes the limit here without giving up the jump is a **base per chunk**:
keep `pos` as `int32` relative to `chunkBase[c]`, with a handful of 64-bit bases
and the entry index where each chunk starts. Binary search still works — over
the chunk table first, then within the chunk — and a document under 2 GiB has
exactly one chunk with base zero, which is the code that runs today. See #113.
Note also that `match` holds *entry indices* as `int32`, so it caps the number
of brackets at 2^31 independently of the byte count.

**One fused pass, not five.** A single hand-written loop
(`find_structural_bits_amd64.s:49`) computes the backslash, quote, whitespace
and structural masks together and flattens them to indices without materialising
per-64-byte results. This package makes five separate guarded kernel calls
(`MaskBits` ×2, `MaskBitsAny` ×2, `MaskBitsLess`), each with its own dispatch
and its own pass over the input.

**Prefix-XOR by carry-less multiply.** `VPCLMULQDQ` against an all-ones constant
turns the escape-parity prefix scan into one instruction. This package does it
with a word-at-a-time loop in Go.

**Stage two on another goroutine.** Above 8 KiB (`parse_json_amd64.go:75`) stage
two runs concurrently with stage one, fed through a channel whose capacity is
the flow control. This package runs them in sequence.

Its NDJSON path batches 10 MiB across `GOMAXPROCS/2` goroutines
(`simdjson_amd64.go:116`).

## C++ simdjson — the original, and what it does that this does not

Read at 4.6.4, from the amalgamated header in `/usr/include/simdjson.h` and by
disassembling `libsimdjson.so.33.0.0` where the header only declares a thing.
Line numbers are that header; generic code is duplicated once per ISA and the
first occurrence is cited.

**It has a size limit too, and a smaller one.**
`constexpr size_t SIMDJSON_MAXSIZE_BYTES = 0xFFFFFFFF` (3273), enforced as
`if (capacity > SIMDJSON_MAXSIZE_BYTES) { return CAPACITY; }` (14984). Its
`structural_indexes` are `uint32_t` (3751) and *absolute*, so a document is
capped at 4 GB − 1. It is documented, not apologised for. This package's 2 GiB
is half of that and, unlike theirs, has `Decoder` and `Token` behind it — 10 GB
at 956 and 833 MB/s in under 20 MB of heap.

**Pseudo-structural bytes.** Their index records the seven structural
characters *and* the first non-whitespace byte after each one, which is where a
number or literal begins. Stage two therefore never scans for a token's start:
a token is `[index[i], index[i+1])`, and the end of a scalar is checked with one
table lookup, `is_not_structural_or_whitespace(*p)` (20703, tables at
12665-66). This package indexes brackets only and finds scalar starts by
skipping whitespace — and `docs/wrong.md` has the measurement showing that
indexing every token start was *slower* here, because the extra positions cost
more to extract than the scan they save.

**UTF-8 is a separate pass for them too.** No fused symbol exists in the
library: `nm -D` gives `simdjson::validate_utf8`, per-arch
`implementation::validate_utf8`, and `dom_parser_implementation::stage1` as
three separate entry points, and disassembling `parse` at `0xca90` shows it call
`stage1` and then tail-jump to `stage2` with no validator between them. The
haswell validator is its own AVX2 body (`vpshufb`/`vptest`, the lookup-table
DFA). That is the same answer this package arrived at by measurement when
fusing validation into the copy loop cost 9%.

**On-Demand keeps nothing.** `iterate` runs stage one and returns
(72586-87); "the call to iterate does not parse and validate the whole document"
(64852). The whole of its state is a cursor — `token_iterator` is
`const uint8_t *buf; token_position _position;` (64033-34) — plus a
`_string_buf_loc` scratch pointer for unescaping (64084-127). Values are parsed
when asked. This is fastjson's idea with an index under it.

**Numbers are Eisel-Lemire and scalar.** `compute_float_64` (20133) with the
Clinger fast path (20143), a 128-bit multiply against `power_of_five_128`
(20252), the `0x1FF` exactness probe (20268) and round-to-even (20356).
Digit accumulation is `while (parse_digit(*p, i)) { p++; }` (20679) — no SIMD.
Go's `strconv.ParseFloat` has had Eisel-Lemire since 1.16, so this is one of the
few places the standard library already hands over the state of the art.

**Batching.** `DEFAULT_BATCH_SIZE = 1000000` (5152), minimum 32 (5161), with
`stage1_mode::streaming_partial` and `streaming_final` (3602) doing what
partial-mode indexing does here, and the next batch starting at
`batch_start + structural_indexes[n_structural_indexes]` (70937) — bytes, not
counts, which is the same conclusion reached here after count-based batching
measured 20% worse.

**What cannot be copied.** `SIMDJSON_PADDING = 64` (3283): the indexer reads up
to 64 bytes past the end of the input and the caller must guarantee they are
allocated. Go slices panic instead, so the same trick needs a padded copy and
`unsafe`, and `-race`/`checkptr` object to the over-read. Their runtime
dispatch across haswell/icelake/westmere/arm64 in one binary is the same problem
this package solves by generating per-ISA assembly ahead of time.

## fastjson — why it wins on twitter without any SIMD

It builds a value tree and decodes **nothing**:

- a string value stores the raw bytes and a `typeRawString` tag
  (`parser.go:132`); unescaping happens on access, and only after an
  `IndexByte` for a backslash finds one;
- a number stores its text (`parser.go:426`) and is converted on access;
- object keys stay escaped until something asks for them (`parser.go:511`).

The values live in a bump-allocated arena reused between parses
(`parser.go:67`), so a whole parse is **two allocations**: one to copy the input,
one for an error that usually is not made.

That is the entire trick, and it is why it wins on twitter — 18,099 strings that
this package validates and it does not look at. It costs elsewhere: a mandatory
`memcpy` of the input on every parse, object lookup that is a linear scan of a
`[]kv`, and a hard depth limit of 300.

## goccy/go-json — the fastest thing in pure Go

**Its type cache is an array, not a map.** It finds the minimum and maximum
`rtype` address via `go:linkname reflect.typelinks`, then indexes a flat array by
`(typeptr - base) >> shift` (`decoder/compile_norace.go:12`). One subtract, one
shift, one load. This package uses a `sync.Map`, which was 10% of a stream
decode before it was cached per-Decoder.

That reads like the cache is a workaround for the wrong data structure, and it
was written here as such. It is not: with the per-Decoder cache in place the
`sync.Map` lookup is 2.76% of an Unmarshal, and two replacements — a generic
`map[uintptr]F` behind an atomic pointer, and the same written out concretely —
came in 4.7% and 3.8% *slower*. The key cannot be computed without taking the
address of an interface, which spills it. `docs/wrong.md` has the detail.

Its escape scan is the SWAR mask this package measured and rejected
(`encoder/string.go:414`), which is a fair reminder that the same technique can
be right for one caller and wrong for another: goccy has no ASCII fast path in
front of it, so its population is every string, not the 5% that are not ASCII.

Strings decode in place (`string.go:395`, `dst <= src`) and clean strings alias
the input with no copy at all.

## gjson — the one this cannot beat at its own game

`Get` does not validate (`gjson.go:2126`, in its own words) and stops the moment
it finds the field (`gjson.go:1291`). Skipping a value it does not want is a
depth counter driven by a table — `depth += int(c) - 2` (`gjson.go:1185`) — with
an eight-bytes-at-a-time quote scan and a backslash-parity check to avoid
mistaking `\"` for a close.

It keeps nothing, so the second query costs the same as the first. That is the
trade: this package is behind on one field and ahead from a few hundred queries
onward, and `docs/lazy-paths.md` has the measurement.

## What to take

In order of value, which is not the order they were found in, and which changed
once the profile was read rather than guessed at.

**What `Scan` actually spends, on citm and twitter, by line:**

| | ms of 10,140 | share |
|---|---|---|
| the six shift-XOR steps of the prefix scan | 1,150 | 11.3% |
| `switch data[p]`, one dependent load per bracket | 800 | 7.9% |
| popcount of the structural mask | 880 | 8.7% |
| structural load and and-not, second pass | 780 | 7.7% |
| **every vector mask kernel put together** | **2,050** | **20.2%** |

The kernels are a fifth of it. The Go word loop is 72%. That is the opposite of
what "fuse the vector passes" assumes, and it is why the list below starts
somewhere else.

1. **Prefix-XOR eight words at a time.** 11.3% of `Scan` goes on six sequential
   shift-XOR steps per word. minio does the same thing in one `VPCLMULQDQ`;
   that instruction is not portable, but the chains for different words are
   independent, so a `u64x8` does eight words' worth in six vector ops and the
   cross-word carry reduces to a prefix XOR over the top bits alone. About 1.75
   ops per word against 12. See #116.
2. **Fuse the five mask passes into one.** Every `Parse`, `Scan`, `Valid`,
   `Compact` and `Indent` pays for five passes over the document and five
   dispatches; minio pays for one. The kernels here are generated from C, so
   this is one kernel that writes four masks, not four kernels called four
   times. Worth roughly two thirds of that 20.2%, and it also makes #117 cheap.
3. ~~**An array-indexed type cache**~~ — *tried, measured worse, see
   `docs/wrong.md`.* The 10% figure below was measured before the per-Decoder
   cache existed. Profiling now puts the whole `sync.Map` lookup at **2.76%** of
   an Unmarshal, and both replacements written for it cost more than that.
4. **A chunk base for the index**, removing the 2 GiB limit while keeping the
   binary search that delta encoding would cost. Smaller than it first looked:
   C++ simdjson caps at 4 GB and calls it a documented limit, and the answer
   here above 2 GiB is already `Decoder` and `Token`.
5. **Lazy scalars in `Parse`.** fastjson's whole advantage on twitter, and
   C++ simdjson's On-Demand mode is the same idea with an index under it. It
   would change what `Parse` means here, so it needs thought rather than
   adoption.

Two things this package already does that the leaders confirm rather than
contradict: keeping UTF-8 validation as a separate pass, which C++ simdjson also
does and which was measured here at 9% when fused; and bounding batches by bytes
rather than by count, which `DEFAULT_BATCH_SIZE` also does and which measured
20% worse the other way.

Not reachable without changing what this package is: sonic's JIT, and the
hand-written assembly both it and minio use — this one generates its assembly
from C ahead of time for six architectures, which is the trade.
