# simdjson

**JSON parsing for Go that finds the whole document's structure in a few vector
passes, then walks that instead of the bytes.** Built on
[simd.go](https://github.com/sebishogun/simd).

**No cgo, and it runs the same on amd64, arm64, riscv64, s390x, ppc64le and
loong64.** The established Go port,
[minio/simdjson-go](https://github.com/minio/simdjson-go), is amd64 with AVX2
and hand-written assembly — and on amd64 it is faster than this, by 1.3–1.8×.
The trade this package makes is portability for speed, and
[the numbers say so plainly](#against-miniosimdjson-go-which-is-faster).

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

## Numbers

Zen 5, worse of two runs of six, against `encoding/json` unmarshalling into
`map[string]any` and reading the same field:

| | encoding/json | `Scan` |
|---|---|---|
| get 1 field, 100 items | 0.116 ms | **7.97×** |
| get 1 field, 1,000 items | 0.999 ms | **6.77×** |
| get 1 field, 10,000 items | 11.77 ms | **7.83×** |

### Against minio/simdjson-go

[minio/simdjson-go](https://github.com/minio/simdjson-go) is the established Go
port of this idea, in hand-written AVX2 assembly. Same machine, same documents,
worse of two runs of six at a load average under 2:

| get one field | encoding/json | minio | this | vs stdlib | vs minio |
|---|---|---|---|---|---|
| 100 items | 0.116 ms | 0.026 ms | 0.015 ms | **7.97×** | **1.78×** |
| 1,000 items | 0.999 ms | 0.225 ms | 0.148 ms | **6.77×** | **1.52×** |
| 10,000 items | 11.77 ms | 2.090 ms | 1.504 ms | **7.83×** | **1.39×** |

Parse alone, 2,000 items: minio 0.425 ms, `Scan` 0.298 ms — **1.43×**.

**It was the other way round until v0.2.0.** The first release made one vector
pass per structural character — six of them — plus quotes and backslashes, then
merged eight ascending lists. minio computes the same thing in one fused pass
over 64-byte chunks, using a carry-less multiply to turn quote parity into a
prefix-XOR, and it was 1.3–1.8× faster.

Measuring that is what produced the fix. `simd.IndexAll` takes a single byte, so
six delimiters meant six reads of the document; simd.go v1.3.0 added
`IndexAllAny`, which takes a set and is 2.6–2.9× six separate calls. Eight
passes and a merge became one pass and a filter.

**minio is still amd64-only.** It requires AVX2 and refuses to run without it.
This runs on amd64, arm64, riscv64, s390x, ppc64le and loong64 from one source,
because the kernels are generated per target rather than written by hand — and
it is now faster on the architecture minio was written for.

Two things in that table are worth reading carefully.

**`Scan` is 6.8–8.0×** the standard library across every size, which is the case
this package is for: values out of a document without decoding the rest.

**`Parse` is not faster at all.** Validating every value costs about what
`encoding/json` costs, and this does it *and* builds an index. If you need the
validation you are better off with the standard library; if you produced the
bytes, `Scan` is the point.

**Walking everything is 0.45×.** The standard library decodes in one fused pass;
this indexes and then navigates. Reaching into a document is what an index buys,
and reading all of it is what it does not.

## How it works

Two stages, which is the design simdjson introduced.

**Stage one** finds every structural character — `{ } [ ] : ,` — in one vector
pass each, and works out which quotes really open and close strings rather than
being escaped. A conventional parser reads a byte and branches on what it is,
which is a dependent and unpredictable branch per byte. This makes a handful of
branch-free passes instead.

**Stage two** walks those positions. A megabyte document might have fifty
thousand structural characters, so the second stage sees fifty thousand items
rather than a million bytes.

The difficulty is entirely in stage one. A `{` inside a string is text, and a
`"` preceded by an odd number of backslashes does not close anything — in
`"a\\"` the quote follows two backslashes and does close the string, while in
`"a\"` it follows one and does not. Both are resolved before any position is
interpreted.

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
a stream of payloads wants. Reuse cuts allocation by about 625× — 999 KB to 1.6
KB per parse — and about 4% of the time, because Go's allocator was already
handing back warm memory. The allocation is worth removing; do not expect the
clock to move much.

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
| [**simd.go**](https://github.com/sebishogun/simd) | 463 operations over slices, bytes and text. The kernels everything else is built from. |
| [**simdblas**](https://github.com/sebishogun/simdblas) | A BLAS backend for gonum. One `blas64.Use` call and `mat`, `stat` and `optimize` run on it. |
| [**simdcsv**](https://github.com/sebishogun/simdcsv) | CSV reading on one vector scan per record. |
| [**simdvec**](https://github.com/sebishogun/simdvec) | Embedding search whose whole index scan is one matrix-vector product. |

## License

MIT — see [LICENSE](LICENSE). Depends on
[simd.go](https://github.com/sebishogun/simd) (MIT).
