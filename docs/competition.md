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
| 10 GB in one piece | **this** | 833 MB/s | — | C++ simdjson caps a document at 4 GB |

`encoding/json/v2`, new in Go 1.26 behind `GOEXPERIMENT=jsonv2`, is **not** a
threat yet: 686 µs to unmarshal twitter against v1's 834 and this package's 322,
and identical to v1 on marshal. When it becomes the default every Go program
gets it for free, so it is the one to watch, but it is 2.1× behind today.

## minio/simdjson-go — the same design, done differently

The direct competitor: also two-stage, also SIMD, also Go. It is slower than
this on all three corpora, and it does four things better.

**Delta offsets, not absolute ones.** It stores the *distance from the previous
structural character* in a `uint32`
(`parsed_json.go:74`, `flatten_bits_amd64.s:26`). A delta never exceeds the gap
between two structural characters, so the width of the integer never bounds the
size of the document. This package stores absolute `int32` positions, which is
why it refuses anything over 2 GiB — a limit that is not inherent, it is a
representation choice, and it is the wrong one. **This is the most valuable
thing in this document.**

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
shift, one load. This package used a `sync.Map`, which was 10% of a stream
decode before it was cached per-Decoder — and that cache is a workaround for the
wrong data structure.

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

In order of value:

1. **Delta-encoded structural positions.** Removes the 2 GiB limit outright,
   without the 1.4× index that `int64` absolutes would cost. minio proves it
   works. This is a real defect in this package's design and it now has a known
   fix.
2. **An array-indexed type cache** for the compiled encoders and decoders,
   instead of `sync.Map`.
3. **Fuse the five mask passes into one.** The kernels are generated from C, so
   this is one kernel that computes four masks, not four kernels called four
   times — five dispatches and five passes become one.
4. **Lazy scalars in `Parse`.** fastjson's whole advantage on twitter. It would
   change what `Parse` means here, so it needs thought rather than adoption.

Not reachable without changing what this package is: sonic's JIT, and the
hand-written assembly both it and minio use — this one generates its assembly
from C ahead of time for six architectures, which is the trade.
