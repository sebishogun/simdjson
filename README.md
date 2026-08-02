# simdjson

**JSON parsing for Go that finds the whole document's structure in a few vector
passes, then walks that instead of the bytes.** Built on
[simd.go](https://github.com/sebishogun/simd).

**No cgo, and it runs the same on amd64, arm64, riscv64, s390x, ppc64le and
loong64.** The established Go port,
[minio/simdjson-go](https://github.com/minio/simdjson-go), is amd64 with AVX2
and hand-written assembly. This is 1.4–1.7× faster than it on amd64 and also
runs on the other five architectures.

It is still slower than [gjson](https://github.com/tidwall/gjson) at pulling a
field out of a document, by about 2×, and
[the numbers say so plainly](#against-lazy-scanners-it-still-loses).

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

**Use gjson or jsonparser** unless you need `encoding/json`-grade validation, an
architecture minio does not support, or the whole document indexed once and
queried many times.

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
