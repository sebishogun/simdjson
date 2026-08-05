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
`encoding.TextMarshaler` and `encoding.TextUnmarshaler`, `UseNumber`, `DisallowUnknownFields`, and the `omitempty`,
`omitzero` and `,string` tags. Every one is checked against `encoding/json` by
a fuzzer that demands the same bytes and the same error-or-not, not merely the
same meaning.

`Decoder.Token` too — a cursor over the syntax rather than over the values —
checked against the standard library's token stream by the same fuzzer.

## Where it stands

Two passes of eight samples on an idle machine, minimum of each. Every number
below appeared in both within 1.6% — the one that did not is called out where it
appears. The competitors run in the same process on the same bytes, and
[bench/](bench) is the harness, so all of it can be re-run.

**Parsing.** A document in, a navigable and validated structure out:

| | this | fastjson | minio | |
|---|---|---|---|---|
| twitter, 1.17 MB | **219 µs** | 232 µs | 305 µs | **1.06×** |
| citm, 1.73 MB | **592 µs** | 736 µs | 664 µs | **1.12×** |
| canada, 2.25 MB | **1,130 µs** | 1,910 µs | 5,569 µs | **1.69×** |

`Scan`, the index without the grammar descent, is 52 / 188 / 318 µs on the same
three. It is not the same operation; see below.

**Validating**, against sonic, which is the only other library doing it with
vector instructions:

| | this | sonic | encoding/json | |
|---|---|---|---|---|
| twitter | **153 µs** | 174 µs | 1,251 µs | **1.14×** |
| citm | **394 µs** | 441 µs | 3,173 µs | **1.12×** |
| canada | **891 µs** | 978 µs | 4,153 µs | **1.10×** |

**Into Go values**, twitter into a struct:

| | this | goccy | sonic | encoding/json |
|---|---|---|---|---|
| `Unmarshal` → struct | **329 µs** | 336 µs | 410 µs | 2,645 µs |

**Out of Go values.** Here sonic leads, and it is worth being exact about where:

| | this | sonic | goccy | encoding/json |
|---|---|---|---|---|
| `Marshal`, a decoded document, sorted keys | **769 µs** | 810 µs | 1,742 µs | 2,175 µs |
| `Marshal`, a struct | 57 µs | **27–33 µs** | 88 µs | 110 µs |
| `Marshal`, `map[string]struct`, 256 entries | 28 µs | **21 µs** | 38 µs | 58 µs |

The two rows sonic wins are the same problem seen twice: escaping strings.
Escaping costs 15.0 µs on top of a 35.0 µs base here, and sonic does escaping
*and* UTF-8 validation inside its whole 27 µs. Its `quote.c` reserves worst-case
output space and writes escapes inline in one vector pass; this package's kernel
stops at each byte needing an escape and returns to Go for it. Six attempts at closing
that in Go are recorded in [docs/wrong.md](docs/wrong.md); the sixth is in and
worth 5%, and the five before it were regressions or nothing — including one
identical to the sixth but for which side of a branch took a function call.

sonic's own two passes differed by 19% on the struct row, where every other
number here agreed within 1.6%, which is why that cell is a range.

**Which sonic**: `sonic.ConfigStd` throughout, which sorts map keys, escapes HTML and
validates strings. `sonic.Marshal` does none of the three and is faster for it;
thirty calls on the same map give five different outputs. Both are in the
harness, the second labelled not comparable.

**Text in, text out**, against `encoding/json`, MB/s:

| | twitter | citm | canada | vs stdlib |
|---|---|---|---|---|
| `Valid` | 3,954 | 4,326 | 2,026 | **4.0–8.0×** |
| `Compact` | 1,457 | 1,895 | 1,702 | **3.9–5.2×** |
| `Indent` | 1,046 | 1,074 | 612 | **2.2–3.1×** |

`Valid` is 15–24× goccy's. Against sonic's it is ahead on twitter, level on
citm and 1.34× behind on canada — which is 2.25 MB of floating-point numbers
and 24 bytes of whitespace, and is the shape that gets the least out of an
index.

**Streaming**, 50,000 newline-delimited records, 6.5 MB:

| | this | goccy | sonic | encoding/json |
|---|---|---|---|---|
| `Decoder` | **11.7 ms** | 12.6 ms | 13.1 ms | 37.8 ms |
| `Encoder` | **6.7 ms** | 7.0 ms | 10.7 ms | 10.0 ms |

The index is built per buffer rather than per value — per value it was 54 ms,
slower than everything here — and the buffer is indexed in **partial mode**,
which treats a value cut in half by the end of the buffer as a fact to report
rather than an error: it indexes what is there and says how far that goes.
Before that, a scalar pass had to find where the last whole value ended before
the vector index was allowed to read the same bytes, which was 17% of a decode.

Allocation is lower than goccy's too: 9.5 MB in 150,183 allocations against
12.9 MB in 306,525, for the same 6.5 MB of input.

**Small documents are the one size where this is the wrong tool.** The index
costs the same few passes whether the document is 64 bytes or a megabyte, and
below about a kilobyte that fixed cost is the whole cost:

| | this | fastjson | encoding/json |
|---|---|---|---|
| 64 B | 123 ns | 40 ns | 76 ns |
| 200 B | 276 ns | 103 ns | 233 ns |
| 2 KB | **890 ns** | 1,080 ns | 3,437 ns |
| 20 KB | **7,951 ns** | 10,882 ns | 24,271 ns |

The crossover is between 200 bytes and 2 KB. Below it, use `encoding/json`; it
is what that package is good at, and reaching for a vector unit to read a
64-byte config file is not a trade that pays.

**At a gigabyte and past it.** Real documents — twitter, citm and canada —
repeated to size, with a deliberate best and worst case for the range:

| 1 GB, one piece | `Scan` | `Valid` | `Parse` | `encoding/json.Valid` |
|---|---|---|---|---|
| best case: minified ASCII, long values | 7,302 MB/s | 5,859 | 5,143 | 583 |
| worst case: nothing but brackets and escapes | 1,166 | 1,497 | 707 | 471 |

Six times between the two for `Scan`, which is the real spread — a single
number for a JSON parser is an average over shapes that differ by that much.

**Past 2 GiB, one piece does not work at all.** A bracket position is an
`int32`, so the index cannot describe a larger document, and `Parse` now says so
instead of panicking with a negative index — which is what it did until a 10 GB
file was put through it. `int64` positions would take the index from 0.93× the
document to past 1.4× and cost every ordinary parse to serve a size that has a
better answer.

That answer is `Decoder`, and it has no limit. Ten gigabytes of tweets,
2.93 M records of about 3.4 KB each — `go test ./bench -run TestHuge -huge
-huge-bytes 10e9`:

| 10 GB | decoded into | time | throughput | peak heap |
|---|---|---|---|---|
| line-delimited | `Value` | 3.89 s | **2,570 MB/s** | 7.5 MB |
| line-delimited | struct, 4 fields | 5.15 s | 1,943 | 8.3 MB |
| line-delimited | `map[string]any` | 29.93 s | 334 | 8.8 MB |
| one array | `Value` | 6.85 s | 1,460 | 7.7 MB |
| one array | struct, 4 fields | 8.31 s | 1,203 | 8.0 MB |
| one array | `map[string]any` | 33.20 s | 301 | 8.1 MB |
| one array, 300 M small elements | struct, 3 fields | 29.92 s | 334 | 8.4 MB |

Under nine megabytes for ten gigabytes, because nothing is ever held whole.

**The decode target decides the throughput, not the parser.** `Value` builds no
Go value and is the parser's own rate; `map[string]any` allocates a map and an
interface per field and is 7.7× slower on identical bytes. A streaming figure
quoted without naming what it decoded into cannot be reproduced, which is why
each row here names one.

A single enormous *array* works too, which C++ simdjson does not do at all — it
caps a document at 4 GB for the same reason this one caps at 2 GiB, structural
indices that are 32 bits wide. Read the opening bracket with [`Decoder.Token`]
and then decode the elements. It is currently 1.76× slower than the same
records line-delimited: finding where each element ends is a byte scan that
duplicates what the structural index already computes, and it is half the run.

The elements are indexed in batches rather than one at a time, which is what
C++ simdjson's `parse_many` does and for the same reason: stage one is a fixed
cost per call and amortising it over a batch is most of what makes streaming
fast. The batch is bounded by **bytes, not by count** — an element of a
megabyte fills one by itself, a hundred-byte record shares one with six hundred
others. Batching by count instead was 20% slower on the large-element array,
because it meant indexing several megabytes to save a cost measured in
microseconds.

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

**Not unlimited in document size.** `Parse` and `Scan` index a document in one
piece and cap at 2 GiB, because a bracket position is an int32 and the index is
already 0.93x the document; int64 positions would take it past 1.4x and charge
every ordinary parse for a size that has a better answer. That answer is
`Decoder`, which streams in 64 KiB buffers and has no limit — including one
huge top-level array, read with `Token` for the opening bracket and then `More`
and `Decode`, exactly as the standard library does it. Over the cap you get an
error naming the way out, not a panic, which was not true until it was tested at
that size: [docs/wrong.md](docs/wrong.md).

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
