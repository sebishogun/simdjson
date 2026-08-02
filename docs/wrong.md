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
