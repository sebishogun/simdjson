# simdjson

**JSON for Go that finds the whole document's structure in a few vector passes,
then walks that instead of the bytes.** Built on
[simd.go](https://github.com/sebishogun/simd).

A drop-in for `encoding/json` — `Marshal`, `Unmarshal`, `Decoder`, `Encoder`,
`Valid`, `Compact`, `Indent` — and, underneath, an index you can navigate
without decoding anything.

**No cgo, and it runs the same on amd64, arm64, riscv64, s390x, ppc64le and
loong64.** The established Go port,
[minio/simdjson-go](https://github.com/minio/simdjson-go), is amd64 with AVX2
and hand-written assembly. This is 1.4–1.7× faster than it on amd64 and also
runs on the other five architectures.

[gjson](https://github.com/tidwall/gjson) is still faster at pulling one field
out of a document you already trust — by about 2× when both validate, and by a
thousand-fold when neither does. It is a different operation: gjson keeps
nothing, so reading the same document twice costs twice. Reading a document once
this is 1.90× faster than `gjson.Valid`, and past a few hundred queries against
one document it wins outright.
[The numbers say all of it plainly](#the-comparison-that-is-actually-like-for-like).

```
go get github.com/sebishogun/simdjson
```

```go
doc, err := simdjson.Parse(data)
if err != nil {
	return err
}

name := doc.Get("user", "name").String()
age  := doc.Get("user", "age").Int()

doc.Get("items").ForEach(func(v simdjson.Value) bool {
	total += v.Key("score").Float()
	return true
})
```

## It is also a drop-in for `encoding/json`

`Marshal`, `Unmarshal`, `Valid`, `Compact`, `Indent`, `MarshalIndent`,
`HTMLEscape`, `NewDecoder`, `NewEncoder`, `RawMessage`, `Marshaler`,
`TextMarshaler`, `UseNumber`, `DisallowUnknownFields`, and the `omitempty`,
`omitzero` and `,string` tags. Every one is checked against `encoding/json` by
a fuzzer that demands the same bytes and the same error-or-not, not merely the
same meaning.

Not implemented: `Decoder.Token`, which is a cursor over the syntax rather than
over the values. It says so rather than half-working.

## Where it stands

One machine, minimum of four, and every number below appeared twice in two
separate passes — anything the two passes disagreed about is not here. The
competitors are measured in the same process on the same bytes.

**Parsing.** A document in, a navigable and validated structure out:

| 1 MB–2.3 MB document | this | fastjson | minio | |
|---|---|---|---|---|
| twitter, 1.17 MB | 237 µs | 237 µs | 315 µs | parity |
| citm, 1.73 MB | 636 µs | 711 µs | 692 µs | **1.12×** |
| canada, 2.25 MB | 1,372 µs | 1,972 µs | 5,609 µs | **1.44×** |

`Scan`, the index without the grammar descent, is 57 µs / 207 µs / 345 µs on the
same three — 4.1× fastjson on twitter. It is not the same operation; see below.

**Into and out of Go values**, twitter into a struct and back:

| | this | goccy | sonic | encoding/json |
|---|---|---|---|---|
| `Unmarshal` → struct | **390 µs** | 398 µs | 443 µs | 2,705 µs |
| `Marshal` | **90 µs** | 111 µs | — | 129 µs |
| `MarshalTo`, caller's buffer | 69 µs | — | 38 µs | — |
| `Fast` options | 46 µs | — | 28 µs | — |

Marshal is the one place another library is clearly ahead: sonic's
`ConfigStd` is 1.85× this on `MarshalTo`. sonic's *default* config is 28 µs and
is not comparable to any of these — it does not escape HTML and passes U+2028
through raw, so it does not produce what `encoding/json` produces.

**Text in, text out**, against `encoding/json`, MB/s:

| | twitter | citm | canada | vs stdlib |
|---|---|---|---|---|
| `Valid` | 2,834 | 3,166 | 1,902 | **5.3–6.0×** |
| `Compact` | 1,457 | 1,895 | 1,702 | **3.9–5.2×** |
| `Indent` | 1,046 | 1,074 | 612 | **2.2–3.1×** |

`Valid` is 15× goccy's and 1.25× behind sonic's.

**Streaming**, 50,000 newline-delimited records, 6.5 MB:

| | this | goccy | sonic | encoding/json |
|---|---|---|---|---|
| `Decoder` | 15.0 ms | **13.1 ms** | 13.6 ms | 38.5 ms |
| `Encoder` | 9.0 ms | **6.9 ms** | 10.0 ms | 10.4 ms |

goccy is ahead on both. The index is built per buffer rather than per value —
per value it was 54 ms, slower than everything — and what is left is a framing
pass that has to find where the last whole value ends before the index can be
built at all. See `docs/wrong.md`.

**Nine shapes, not three files.** twitter, citm and canada cover
strings-and-objects, objects-and-whitespace and numbers, and nothing else.
`shapes_test.go` adds deep nesting, wide objects, long strings, escape-heavy
strings, non-ASCII, bare numbers, bare literals, pretty-printed and empty
containers — each about a megabyte, each checked against `encoding/json`
through every entry point before it is timed. `Scan` holds 12.3–12.5 GB/s on
every one of them except the two that are nothing but brackets.

## Numbers, including the ones that are bad

### Against other index-building parsers, this wins

[minio/simdjson-go](https://github.com/minio/simdjson-go), the established Go
port, in hand-written AVX2. Both validate. Worse of two runs of six for this
package, better of two for the others, at load under 2:

| get one field | encoding/json | minio | `Parse` | vs stdlib | vs minio |
|---|---|---|---|---|---|
| 100 items | 0.095 ms | 0.025 ms | 0.0145 ms | **6.57×** | **1.73×** |
| 1,000 items | 0.951 ms | 0.221 ms | 0.142 ms | **6.68×** | **1.55×** |
| 10,000 items | 9.62 ms | 2.030 ms | 1.449 ms | **6.64×** | **1.40×** |

`Scan`, which builds the same index and skips validation, is 3.8 µs / 36 µs /
371 µs on the same three documents. That is 5.5× minio at 10,000 items, but it
is not the same operation — minio validates and `Scan` does not, so the table
above is the comparison to read.

### Every parser that parses, on the same document

10,000 items, 1.17 MB, one field read out. Everything here walks and checks the
whole document; the lazy scanners are the section below. Worse of two runs of
six at load under 2:

| | 10,000 items | |
|---|---|---|
| [valyala/fastjson](https://github.com/valyala/fastjson) | 1.335 ms | **1.09× faster than this** |
| **this — `Parse`** | **1.455 ms** | |
| [minio/simdjson-go](https://github.com/minio/simdjson-go) | 2.066 ms | 1.42× |
| [bytedance/sonic](https://github.com/bytedance/sonic) | 5.773 ms | 3.97× |
| `encoding/json` | 9.522 ms | 6.54× |
| [goccy/go-json](https://github.com/goccy/go-json) | 11.788 ms | 8.10× |

**fastjson is ahead**, by 9%, and it is the one to beat. It builds a value tree
into a reusable arena rather than an index, so navigation afterwards is a
pointer walk where this is a lookup into a position array — a different trade,
and on this benchmark a slightly better one. It is also amd64-and-everything
scalar Go, which is worth saying plainly: the vector passes here buy the
structural index, and the index is not yet where most of `Parse` goes.

### Against lazy scanners, it still loses

[gjson](https://github.com/tidwall/gjson) and
[jsonparser](https://github.com/buger/jsonparser) do not parse the document.
They scan for the path and stop at the first match.

That makes the obvious comparison unfair in gjson's favour, because `gjson.Get`
is not the same operation as `Parse`. It does not validate, and it will answer
from a document that is not JSON:

| input | `gjson.Get` returns | actually valid |
|---|---|---|
| `{"a" 1}` — no colon | `"1"` | no |
| `{"a":1` — unterminated | `"1"` | no |
| `{"a":01}` — invalid number | `"01"` | no |

So the comparison has to put validation on both sides. On a 10,000-item
document:

| | gjson | this | |
|---|---|---|---|
| **both validating** — `gjson.Valid`+`Get` against `Parse`+`Get` | 694 µs | 1.449 ms | gjson **2.09×** |
| neither validating — `gjson.Get` against `Scan`+`Get` | 53.3 ns | 371 µs | gjson 6,960× |

**Correcting the comparison moves it by two orders of magnitude and does not
reverse it.** gjson is faster at getting a field out of a document, whether or
not either side checks the document first.

The gap on the validating row was 13.9× and is now 2.09×. What closed it was
stage one: bitmasks instead of offset lists, which took the scan from 1.76 ms to
371 µs, and string validation done once over the whole document with masks
instead of a byte walk per string. What is left is one thing:

**The grammar walk still reads bytes.** `Parse` costs 1.449 ms where `Scan`
costs 371 µs, so proving the document well-formed is 1.08 ms of it. The
recursive descent steps through the input skipping whitespace a byte at a time,
while the structural index beside it already records where every brace, colon
and comma is.

The obvious fix — index every token start, so stepping to the next token is an
array read — was built and is 2.7× *slower*, because it makes the index bigger
than the document. That is written up in [docs/wrong.md](docs/wrong.md); it is
the second-most useful thing in this repository.

Container extents no longer cost anything: stage one pairs every bracket in one
stack pass over the class array, so finding where an object ends is a lookup
rather than a depth-counting walk. `Index(j)` is still linear in j, but each
step is now a lookup instead of a walk over the subtree it is skipping.

### The comparison that is actually like-for-like

`gjson.Get` and `Parse`+`Get` are not the same operation, and neither the
13.9× nor the 2.09× above says much on its own. Two comparisons that do.

**Reading the whole document once.** `gjson.Valid` walks 1.17 MB with a switch
per byte and returns a bool. `Scan` walks the same bytes with vector compares
and returns a structural index:

| | time | what is left afterwards |
|---|---|---|
| `gjson.Valid` | 711 µs | a `bool` |
| **`Scan`** | **374 µs** | a reusable index — **1.90×** |
| `Parse` | 1,453 µs | that index, plus the grammar proved |

**Asking a document more than one question.** gjson keeps nothing, so every
`Get` rescans from byte zero. This indexes once. Reading `items.N.score` for N
across a 10,000-item document:

| queries | gjson | this | |
|---|---|---|---|
| 1 | 365 ns | 375 µs | gjson 1,027× |
| 10 | 3.1 µs | 382 µs | gjson 123× |
| 100 | 187 µs | 484 µs | gjson 2.5× |
| 1,000 | 17.3 ms | **10.7 ms** | **1.62×** |

The crossover is a few hundred queries per document. Both are quadratic here —
gjson rescans to reach element N, and `Index(j)` walks j elements — but each of
this package's steps is a lookup where gjson's is a byte scan.

### So which one

**gjson or jsonparser** for pulling a field or two out of a document you
produced yourself. That is what they are built for and nothing here approaches
them at it.

**This** when the document comes from outside and has to be checked, when you
will ask it more than a few hundred questions, or when the target is not amd64.

Worth being clear about what gjson does that this does not: a path language with
wildcards, `#(age>45)` filters, about fifteen modifiers, JSON Lines and custom
modifiers. This has `Get(path...)`, `Key`, `Index` and `ForEach`. Also worth
being clear about what it does not do — its own README says the `Get*` functions
"expect that the json is well-formed" and that bad JSON "may return back
unexpected results", which is the difference the validating row above is
measuring.

## How it works

Two stages, which is the design simdjson introduced.

**Stage one** classifies the document with vector compares and answers
everything that follows with bit arithmetic. Three passes produce a bitmask each
— one bit per input byte — for the quotes, the backslashes and the six
structural characters. A conventional parser reads a byte and branches on what
it is, which is a dependent and unpredictable branch per byte. This has no
per-byte branch at all.

**Stage two** walks the surviving positions. A megabyte document might have
fifty thousand structural characters, so the second stage sees fifty thousand
items rather than a million bytes.

The difficulty is entirely in stage one. A `{` inside a string is text, and a
`"` preceded by an odd number of backslashes does not close anything — in
`"a\\"` the quote follows two backslashes and does close the string, while in
`"a\"` it follows one and does not. Both are resolved before any position is
interpreted, and both are resolved as arithmetic over sixty-four bytes at a
time:

- **which quotes are escaped** — adding the odd-length backslash-run starts back
  into the backslash mask propagates a carry through each run and lands it one
  past the run's end, which turns "the parity of this run" into a single add;
- **which bytes are inside a string** — an inclusive prefix XOR of the surviving
  quote mask, six shift-and-xor steps per word, with the parity carried into the
  next word by sign-extending its top bit;
- **which structural characters survive** — an and-not.

None of that costs anything per match, which is the point. The version before
it built *lists of offsets* instead, one per character class. That is the
natural thing to build on a `simd.IndexAll` primitive and it is the wrong
representation: this document is about 40% structural characters, so the offset
list came out four times the size of the document it described, and every
question asked afterwards cost a scalar step per entry.

Replacing it was worth 4.8× on stage one. Two other things were tried first and
are recorded in [docs/wrong.md](docs/wrong.md) — windowing the input to keep it
in cache, which a sweep from 4 KiB to 64 MiB showed changed nothing, and
indexing every token start the way C++ simdjson's pseudo-structural characters
do, which is 2.7× slower here because it makes the index bigger than the
document.

## Parse or Scan

**`Parse` validates.** It checks every value against JSON's grammar and rejects
exactly what `encoding/json` rejects. Use it for anything from outside.

**`Scan` does not.** It builds the index and identifies the root, and skips the
recursive descent that proves the parts you never look at are well-formed.
Malformed input then gives wrong answers rather than errors — nothing reads out
of bounds and nothing panics, but the result is not to be trusted. Use it when
you produced the bytes.

Validation is most of the cost, and skipping work you did not ask for is the
whole reason a structural index exists.

**`Parser`** reuses its index between documents, which is what a server handling
a stream of payloads wants. Reuse cuts allocation by about 3,170× — 1,008 KB to
318 B per parse — and 17% of the time: 345 µs against 286 µs on a 230 KB
document. The allocation is the part worth removing.

## Correctness

Defined as agreeing with `encoding/json`, and tested that way: hand-written
cases, 2000 randomised documents built from atoms chosen to collide (structure
inside strings, escaped quotes, escaped backslashes, surrogate pairs), and
**fuzzing — 49 million executions, clean**.

The fuzzer found four real bugs in its first three minutes, none of which the
hand-written tests caught:

| input | bug |
|---|---|
| `{"":"\x82"}` | invalid UTF-8 returned raw; `encoding/json` coerces it to U+FFFD |
| `{"":{"":[{"\x00":0}]}}` | raw control character in a string, which JSON forbids |
| `{"":"\0"}` | invalid escape accepted — `unquote` returned a false flag the parse path ignored |
| `{"":10.}` | `strconv.ParseFloat` accepts `10.`; JSON's grammar does not |

It then found a fifth failure that was **in the test**: `1E700` is valid JSON
that does not fit a `float64`. Comparing against `Unmarshal`, which converts,
made a conversion limit look like a syntax rule. The oracle for accept-or-reject
is `json.Valid`.

```
go test ./...
go test -run '^$' -fuzz FuzzAgainstStdlib -fuzztime 60s
```

## What this is not

**Not a replacement for `encoding/json`.** No struct unmarshalling, no tags, no
interfaces, no streaming, no encoding. If you want a Go value, use the standard
library.

**Not faster at everything.** `Parse`, which validates every value, costs about
what `encoding/json` costs and builds an index on top; use it for untrusted
input and `Scan` when you produced the bytes. Walking a whole document is slower
than the standard library's single fused decode. Reaching *into* a document is
what an index buys; reading all of it is what it does not.

**Not zero-copy for strings with escapes.** A string with no backslash is
returned without copying out of the document; one with an escape is decoded into
a new string.

## Status

Early, and measured on amd64 only. The `simd` package underneath is verified on
amd64 and arm64 NEON and under emulation elsewhere.


## The rest of the family

All built on [simd.go](https://github.com/sebishogun/simd), which generates its
kernels once from C and ships them as committed assembly for nine instruction
sets — so none of these needs cgo, and none is amd64-only.

| | |
|---|---|
| [**simd.go**](https://github.com/sebishogun/simd) | 467 operations over slices, bytes and text. The kernels everything else is built from. |
| [**simdblas**](https://github.com/sebishogun/simdblas) | A BLAS backend for gonum. One `blas64.Use` call and `mat`, `stat` and `optimize` run on it. |
| [**simdcsv**](https://github.com/sebishogun/simdcsv) | CSV reading on one vector scan per record. |
| [**simdvec**](https://github.com/sebishogun/simdvec) | Embedding search whose whole index scan is one matrix-vector product. |

## License

MIT — see [LICENSE](LICENSE). Depends on
[simd.go](https://github.com/sebishogun/simd) (MIT).
