# Things that were tried and did not work

Each entry is something that looked right, was built, was measured, and lost.
They are here because the reasoning that produced them is reasoning that will
recur, and because a rejected idea with no record is an idea that gets rebuilt.

The house rule they all come from: **measure before believing, and profile
before optimising.** Every entry below was a theory that survived until it met a
number.

---

## 1. Windowing stage one to keep the document in cache

**The idea.** Stage one made one vector pass over the whole document to find the
interesting bytes, then a scalar pass over the positions it found to sort them
by class. The scalar pass reads `data[p]` for every match. On a 1.17 MB document
the vector pass has long since evicted the beginning of the input by the time
the scalar pass gets there, so every one of those reads should miss.

The fix that follows is obvious and had already worked once, in simdcsv, where
indexing one record at a time beat indexing the whole buffer: process the input
in a cache-sized window, so the bytes are still resident for the second pass.

**What happened.** It was implemented, and then the window size was swept from
4 KiB to 64 MiB — the largest of which is the whole document, i.e. no windowing
at all.

```
chunk 4 KiB     scan 1755014 ns    parse 2843307 ns
chunk 8 KiB     scan 1756978       parse 2818644
chunk 16 KiB    scan 1767091       parse 2839976
chunk 32 KiB    scan 1759836       parse 2837283
chunk 64 KiB    scan 1770534       parse 2808739
chunk 256 KiB   scan 1768639       parse 2822566
chunk 64 MiB    scan 1755725       parse 2806092
```

Flat. Not "a small win", not "within noise of a small win" — a factor of sixteen
thousand in window size changed nothing.

**Why.** `perf stat` on the same benchmark:

```
5,953,585,499  cycles
34,709,484,291 instructions      -> IPC 5.83
7,986,283,587  branches
513,922        branch-misses     -> 0.006%
228,846,557    cache-references
2,538,085      cache-misses      -> 1.1%
```

Nothing was stalling. The machine was retiring 5.83 instructions per cycle,
close to its issue width, with essentially no mispredictions and essentially no
misses. The code was not waiting for memory; it was executing 72 instructions
per byte of input. A locality fix cannot help a program that is not
memory-bound, and no amount of reasoning about cache lines was going to reveal
that — one `perf stat` did.

**What was done instead.** The representation changed. See the package comment
in `structural.go`: offset lists became bitmasks, and the scalar-step-per-match
work became bit arithmetic over sixty-four bytes at a time. Scan went from
1.76 ms to 370 µs.

**What to take from it.** A parameter sweep that comes out flat is a disproof,
not an inconclusive result. And "this is the same shape as a thing that worked
before" is a hypothesis, not evidence — simdcsv's per-record scan won for
reasons that had to be re-established here, and were not.

---

## 2. Indexing every token start, the way C++ simdjson does

**The idea.** C++ simdjson does not index only `{}[]:,`. It also indexes
*pseudo-structural characters*: the opening quote of every string and the first
byte of every number and literal — anything that begins a token. That is what
lets its stage two step from one token to the next by incrementing an index,
never scanning bytes for the next non-space, and it is also how a stray `x` in
`{"a":1 x}` becomes visible to the grammar instead of sitting in a stretch of
bytes nobody looks at.

Here, stage two was spending 15% of Parse in `skipSpace` walking whitespace byte
by byte, and 25% in the string-end lookup. Both are exactly what a token index
removes. The masks needed for it are nearly free: token starts are
`ops | (quote & inString) | (scalars & follows)` where `follows` is the
whitespace-or-operator mask shifted by one.

**What happened.** It was built — the whitespace mask, the boundary arithmetic,
the class table extended to strings and scalars, each string's end recorded into
the `match` array while its mask word was still in a register, a cursor over the
index replacing `skipSpace`, and a `scalarEnd` check to catch the `1x` case that
the index alone cannot see.

It is 2.7× slower.

```
                 structural only      every token start
Scan+Get              377 µs               1003 µs
Parse+Get            1670 µs               3029 µs
```

**Why.** The document has 230,011 structural characters and 490,019 candidate
bytes; adding string starts and scalar starts took the index from 230 K entries
to about 410 K. `pos` is `[]int32`, `cls` is `[]uint8`, `match` is `[]int32` —
so the index grew from roughly 2.1 MB to 3.6 MB, against a 1.17 MB document.

The profile is unambiguous about the consequence. `Doc.tok`, the cursor, was
24% of Parse, and the line that cost was the cursor's own bounds test:

```
110ms   if k < len(pos) && int(pos[k]) >= i && (k == 0 || int(pos[k-1]) < i) {
```

Two loads from a 1.6 MB array that is being streamed alongside the document, the
class array and the match array. `lowerBound`, the binary-search fallback the
cursor exists to avoid, took no measurable time at all — the fallback was never
the problem. Walking the index was simply more expensive than walking the
whitespace, because the index had become bigger than the thing it indexed.

**What was done.** Reverted to indexing the six structural characters only.
`skipSpace` is a byte loop again and the string end comes from the in-string
bitmask.

**What to take from it.** This is the same mistake as the offset lists in the
package comment, in a new costume: a representation larger than the input is
paid for on every access, and the saving has to beat that before anything else.
C++ simdjson can afford pseudo-structural characters because its tape is the
only thing stage two touches and its whole design is built around that; bolting
the index onto a stage two that still walks bytes gets the cost without the
benefit. Copying a design's *component* is not copying its design.

---

## 3. Fewer vector passes over the document

**The idea.** Stage one needed the positions of eight bytes: the six structural
characters plus the quote and the backslash. Eight is exactly what
`simd.IndexAllAny` takes, so one pass finding all of them and a scalar loop
splitting the results by class should beat three separate passes.

**What happened.** It measured slower — 2.07 ms against 1.71 ms for the
three-pass version. The combined pass returns one list that then has to be split
by reading `data[p]` for every position found, which on a document of strings is
a couple of hundred thousand reads back into the input.

**What to take from it.** Instruction count per pass is not the cost; what the
next stage has to do with the result is. This one was superseded anyway — the
bitmask form does one pass *and* needs no split, because a mask per class is
free where a list per class is not.

---

## 4. `\_\_builtin\_bit\_cast` is required for the mask kernel

Not a rejected idea so much as a trap that cost a compile-and-read. The
bitmask kernel in `simd` needs a vector comparison turned into one bit per lane.
The portable-looking spelling is a loop:

```c
u64 bits = 0;
for (int j = 0; j < 64; j++) bits |= (u64)(hit[j] != 0) << j;
```

LLVM does not recognise that as a movemask. It compiles to a stack spill of the
vector and sixty-four scalar loads, with a frame and a saved base pointer.
`__builtin_bit_cast(unsigned long long, mask)` on a `_Bool` ext_vector compiles
to two instructions per sixty-four bytes:

```
vpcmpeqb (%rsi,%r8), %zmm0, %k0
kmovq    %k0, (%rdi,%r10,8)
```

Both variants were compiled and read before either was committed to.

## The set decides whether a vector scan pays, not the length

The escape scan is the encode: 45% of a `MarshalTo` once UTF-8 validation stopped
being 42% of it. `simd.IndexAnyOrLess` does that scan 3.9x faster at 64 bytes
and 8.7x at 4096, measured on a string with nothing to escape. Putting it behind
a length threshold on both paths gave:

	           Fast          MarshalTo
	before     65.9 - 69.0   81.0 - 82.3
	after      47.0 - 47.7   83.7 - 84.5

Half of it a 30% win and the other half a 4% loss, from the same call.

The two paths differ only in their set. `Fast` looks for `"` and `\`, which
almost never appear, so one call covers the whole string. The stdlib-compatible
path adds `<`, `>`, `&` and `0xE2` -- `0xE2` because it leads U+2028 and U+2029,
which encoding/json escapes. But `0xE2` also leads U+2014 and U+2026, the em
dash and the ellipsis, and the whole of the U+2000 punctuation block. On a
document of tweets the scan stops every few bytes, no run is long enough to
cover the call, and the fixed cost is paid over and over.

The obvious repair does not work either. Take `0xE2` out of the set and rule out
the two line terminators once per string with two substring searches, which are
fast and almost always fail:

	           Fast          MarshalTo
	v10        46.1 - 46.6   95.1 - 95.6

Two extra passes over every string cost far more than the stops they avoid --
17% worse than doing nothing. Shipped is the split: the kernel on the two-byte
set, the word loop on the six-byte one. Fast 66-69 to 46-48, MarshalTo 81-82 to
77-78.

**The rule.** A vector scan's cost is per call, and a set whose members are
common turns one call into many. Judge a scan kernel by how long its runs are
on real data, not by its throughput on a string with no matches in it -- that
benchmark measures the one case the caller does not have.

## A structural index per value is the wrong unit for a stream

The first streaming Decoder called Unmarshal per value, which builds an index
per value: five vector passes, a pool fetch, and mask buffers, for a
hundred-byte record. It was the slowest of the four libraries measured -- 54 ms
against goccy's 13 and encoding/json's 39 -- by the design that makes this
package fast everywhere else.

An index costs about the same whether one value reads it or six hundred do. So
the buffer, not the value, is the unit: index as many whole values as are
present and decode them all from it. 54 ms to 24.

Then the framing scan -- finding where the last whole value ends, which has to
happen before the index because an index over half a value is an error rather
than a partial answer -- was 27% of the decode, five times the index it exists
to protect. It called simd.IndexAny per hop, and the hops in a record of a
hundred bytes are a dozen bytes long. The kernel's own length guard cannot help:
it sees 64 KiB of buffer remaining and takes the vector path to find something
three bytes away. A bounded scalar run first, kernel after: 24 ms to 16.6.

**The rule.** A per-call cost is amortised by the work in one call, not by the
size of the buffer that call was handed. Both mistakes here were the same
mistake -- choosing a strategy by how much data existed rather than by how much
of it the operation would actually touch.

## The escape kernel loses on the HTML set twice, for different reasons

The first time, over every string in the document, it was 4% slower and the
explanation was that the set contains `0xE2` -- which leads U+2014 and U+2026
and the whole U+2000 block -- so the scan stops at every dash and ellipsis and
no run is long enough to cover the call.

That explanation predicted the kernel would win once the population changed. It
did change: the ASCII fast path in `appendQuoted` now answers for 95% of the
strings, leaving only the ones with something above ASCII in them, which are the
long ones -- 30% of the bytes in 5% of the strings, around 150 apiece.

Same call, same threshold, a population that is now entirely long strings:

	              MarshalTo
	word loop     68.9  69.2  69.1
	kernel        78.8  78.8  79.6

14% worse. Whatever the first explanation was measuring, it was not the whole
reason, and the prediction it made was wrong.

Measured against `cleanRun` on its own the kernel is 3.9x at 64 bytes and 7x at
256, so the loss is not in the scan. What is left is the shape of the call:
`scan` holding a call cannot be inlined, and `appendBody` calls it once per run.

**The rule.** A theory that explains a measurement is not the same as a theory
that predicts the next one. This one explained the first result and failed the
experiment it suggested; the word loop stays, and the reason it stays is the
measurement, not the story about `0xE2`.

## Three techniques taken from the fast libraries, none of which transferred

Read sonic and goccy to find what they do that this does not. Three things,
each tried and each measured worse here.

**One mask instead of six tests.** goccy's escape scan, which it took from
segmentio/encoding, folds every check into a single word and takes one branch:

	mask := n | (n - lo*0x20) | ((n^lo*'"')-lo) | ((n^lo*'\\')-lo) |
	        ((n^lo*'<')-lo) | ((n^lo*'>')-lo) | ((n^lo*'&')-lo)
	if mask & msb != 0 { ... }

The terms are deliberately inexact -- the `&^ n` that makes a has-a-byte test
exact is left out -- because a false positive only falls into a byte loop that
gives the exact answer. Fewer operations, one branch instead of seven.

In `cleanRun` it was 2.5% slower. The reason is the ASCII fast path in
`appendQuoted`, which already answers for 95% of strings: what reaches
`cleanRun` is the strings with something above ASCII in them, where the first
term breaks immediately and computing the other six is waste. Folding tests
helps when they must all be evaluated; short-circuiting helps when the first
one usually fires. Our population had already been sorted into the second kind.

**The same mask in the fast path**, where the words really are almost all
clean, was 3% slower. The median string in these documents is eleven bytes, so
one word iteration runs and the loop setup is the cost; a table lookup per byte
has no setup.

**One load instead of eight.** `le64str` compiled to 139 bytes -- eight loads,
seven shifts and a bounds check per byte. Slicing to a provable eight bytes
first makes the load-combining rule fire and the function drops to 43 bytes.
It was 3.5% slower, three runs, consistently. The likely cause is the slice
bound the caller guarantees and the compiler cannot see, checked once per
iteration where the previous form's checks were folded away.

**What did transfer** is a fact rather than a technique: sonic's `Valid` says in
its own source that it "does not check for the invalid UTF-8 characters", and it
is a hand-written assembly state machine on amd64 and arm64 only. It is 1.25x
this one and it is not doing the same work or running in the same places.

**The rule.** Three changes, each fewer instructions than what it replaced, each
slower. Instruction counts do not predict this code any more; branch behaviour
and the length distribution of real strings do. Measure, interleaved, or do not
change it.

