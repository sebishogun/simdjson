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

**What did transfer** is a fact rather than a technique, and half of what was
first written here about it was wrong.

sonic's `Valid` says in its own source that it "does not check for the invalid
UTF-8 characters". That reads like an admission of doing less work, and it is
not: `encoding/json.Valid` does not check either, and neither does this one,
because this one is checked against `encoding/json` for the same answer on the
same input. Asked directly — a lone continuation byte, a truncated sequence, an
overlong form, a surrogate, all inside a string — sonic, `encoding/json` and
this package return `true` for every one of them. Same work.

What is left of the difference is real and is enough: sonic's is a hand-written
assembly state machine, built for amd64 and arm64 and nothing else. It is 1.25x
this one, which is portable Go running on six architectures.

**The rule.** Three changes, each fewer instructions than what it replaced, each
slower. Instruction counts do not predict this code any more; branch behaviour
and the length distribution of real strings do. Measure, interleaved, or do not
change it.

## skip costs 79 against a budget of 80

Whitespace skipping is 40% of `Valid` on twitter.json, which is 27% whitespace,
and the obvious improvement is sitting right there: 46% of the whitespace runs
outside strings are exactly one byte — the space after a colon — and answering
those with a comparison instead of a mask load, a shift and a bit scan should be
free.

	if i+1 < len(d.data) && d.data[i+1] > ' ' {
		return i + 1
	}

6% slower. The branch is reached only by the 7% of calls that meet whitespace at
all, so it cannot cost 6% by executing. It costs it by existing:

	before  can inline (*Doc).skip with cost 79 ... budget 80
	after   cannot inline (*Doc).skip: function too complex: cost 100

`skip` is called once or twice per token, several hundred thousand times per
document, and losing the inline turns every one of those into a call. One unit
of headroom under the budget.

The file already said this, from the last time: "Doing that instead pushes skip
past the inlining budget, and losing the inline costs far more than the two
loads it saves." It was right, and it was about a different change, and the
number was not written down.

It is now. **`skip` is closed.** Nothing can be added to it — not a fast path,
not a check, not a comment-worth of code. Making whitespace skipping cheaper has
to mean calling `skip` less often or making `skipRun` cheaper, and neither is
the same problem.

## The escape scan resisted seven attempts, and here is the shape of all of them

`cleanRun` is 44% of a stdlib-compatible encode and has not moved. Every attempt
to move it is below, with what each one predicted and what it measured, because
the pattern across them says more than any one of them.

	1  vector kernel over every string          4% slower
	2  vector kernel, after the ASCII fast      14% slower
	   path left only long strings
	3  goccy's one-mask scan in cleanRun        2.5% slower
	4  goccy's one-mask scan in the fast path   3% slower
	5  single-load le64str                      3.5% slower
	6  exact control test in cleanRun           2.5% slower
	7  exact control test, retried after the    wash
	   profile changed

Attempt 2 was made because attempt 1's explanation predicted it would work.
Attempt 7 was made because attempt 6's explanation turned out to be wrong --
the claim was that `0xE2` ends the word loop on Japanese text, and Japanese is
`E3 81 82`, so what actually ended it was the masked control test reading `0x81`
as `0x01`. Fixing that was still a wash, because by then the ASCII fast path
had cut the string before its first non-ASCII byte and what reached `cleanRun`
was too short for a word loop to matter.

**The through-line.** Every one of these makes the scan cheaper per byte. None
of them makes it cheaper per string, and the strings are eleven bytes. The
median string in twitter.json is eleven bytes; in citm_catalog.json it is eight,
and citm has four escapes in 221 KB. There is no amortisation to be had.

What did work, twice, was removing work rather than speeding it up: answering
the escape question and the UTF-8 question with one table pass (13%), and not
rescanning the prefix that pass had already proved clean (3%). And what worked
outside the scan was removing calls: leaf kinds written in the struct loop
instead of dispatched to (3%), and again for slice elements.

An eighth, in the other inner loop, came out the same way. `number` is 47% of
validating canada.json and scans digits one byte at a time; canada's numbers are
nineteen bytes and mostly digits, so eight-at-a-time should halve it:

	digits = below(0x3A) &^ below(0x30), all eight or break

	                byte loop   SWAR
	canada Valid     1,339 us   1,417   6% slower
	canada Parse     1,568      1,671   7% slower

A short, perfectly predicted byte loop that the compiler already understands is
hard to beat with anything that has setup.

**That conclusion was wrong, and this is the correction.** Eight-at-a-time is
worth 16% on Valid and 12% on Parse. What lost here was not the width. It was
`all eight or break` — a break leaves the byte loop to walk from the start of
the block, and a *wider* block breaks *earlier* relative to what it covers, so
widening while keeping the tail hands the tail more work, not less. canada's
fractions are six or seven digits: four-at-a-time breaks with two or three bytes
left, eight-at-a-time breaks with seven.

The mask already holds the answer. `TrailingZeros64` of it is the first
non-digit byte exactly, so there is no tail to walk:

	                            4-wide, break   8-wide, exact position
	canada Valid                  1,103 us        923      16% faster
	canada Parse (gate)           1,313           1,156    12% faster

Only bit 7 of each byte survives the mask, so the lowest set bit is 8k+7 for the
first non-digit byte k. A non-digit always sets its own bit — 0x76 plus ten or
more carries into it, and the `| x` catches bytes at 0x80 and above whose sum
wraps past it — and a digit never sets an earlier byte's, because carrying out of
a byte needs 0x8A there and a digit is nine at most.

**The rule.** A rejected idea is rejected in the form it was tried. This one was
recorded as "eight-at-a-time loses" when what it showed was "eight-at-a-time
*with a byte-loop tail* loses", and the tail was the part worth removing. Two
years of that entry would have kept anyone from trying it again.

**How much was ever available.** After the eighth loss, the two forms of
`cleanRun` were measured against each other on their own, instead of through an
encode:

	shape         seven tests   one mask
	ascii-140         55.2 ns     48.1     1.15x
	jp-150            53.0        51.4     1.03x
	ascii-11           4.25        5.31     0.80x

The best case is 15% and the common case is 25% worse — and the common case is
the case, because the median string is eleven bytes. That is the whole of the
in-situ 2.5%, and it is a *bound* rather than another failure: `cleanRun` is 44%
of an encode, so even winning every long string at 1.15x is about 2% of
`MarshalTo`. The 1.77x is not in this loop and no amount of work on it will
find the 1.77x.

The same measurement corrected a second belief. `cleanRun` runs at 2.5 GB/s on
Japanese text and 2.5 GB/s on ASCII — the same rate. The story that its word
loop was breaking on continuation bytes and falling into the byte loop, which
was the reason for attempts 6 and 7, was wrong: the word loop was working the
whole time, and seven tests that short-circuit beat seventeen operations that
do not.

**The ninth attempt is the one worth keeping.** The kernel had only ever been
measured on plain ASCII. Measured instead on what actually reaches `cleanRun` —
strings with something above ASCII in them, which are the long ones — it is
clearly ahead:

	                cleanRun   kernel
	jp-150            53.1 ns    38.2   1.4x
	mixed-150         42.2       16.2   2.6x
	jp-400           133.9       27.0   5.0x

So it was integrated the way the earlier failure suggested: once per string in
`appendQuoted`, before the run-at-a-time loop, instead of once per run inside
it. A string with nothing to escape — which is most of them — then costs one
kernel call and a memmove.

	MarshalTo   65.4 65.1 65.3 us -> 71.8 71.5 71.5   10% slower

**That is the finding.** A replacement that is 1.4x to 5x faster on exactly the
inputs it will see, called once per string instead of once per run, integrated
at the point the previous post-mortem pointed at, still lost 10%. Nine attempts,
nine losses, and this one had every argument in its favour and a microbenchmark
on the right population to back it.

Isolated speedups in this code do not survive integration, and that is now a
demonstrated property rather than a suspicion. The next person to look at
`cleanRun` should not start from a microbenchmark.

**The rule.** Nine attempts at making an inner loop faster, nine losses, in
two different loops. The loops are not the problem, and the ninth measurement
proved it properly by bounding what was there to win. Count what a loop is
called on, and measure the replacement in isolation before threading it through
a program — when the answer is "eleven bytes, a few hundred thousand times",
the work to remove is the call, not the loop.

## Fusing passes wins only when the passes are memory-bound

Two fusions, same shape, opposite results.

**The one that worked.** `JSONCopyRun` does the escape scan and the copy in one
pass. 18% off `MarshalTo`. The copy has to happen regardless and is memory
traffic; folding the scan into it adds one comparison per block to a loop that
was already waiting on memory.

**The one that did not.** `JSONQuote` adds UTF-8 validation to the same loop, so
an encoder's three questions — anything to escape, is it valid, copy it —
become one pass instead of three. Measured against the two-pass version it
replaces:

	              MarshalTo
	two passes    58.5 58.8 58.5
	fused         64.8 63.9 64.1     9% slower

Also tried with the short-string fast path kept in front of it, which is the
arrangement that fixed an earlier version of this mistake. Still 9% slower.

The difference is what the extra work costs against what it saves. The escape
scan is one comparison per block. The UTF-8 classifier is three lane-crossing
shuffles and a dozen comparisons per block. The strings are around 150 bytes,
so after the first pass they are in L1 — and a second pass over L1 is cheaper
than doubling the instructions in the first.

`simd.ValidUTF8` and `simd.JSONCopyRun` are each a tight vector loop over data
in cache. Two of those beat one loop doing both.

**The rule.** Count the instructions the fusion adds per block against the
memory traffic it removes. If the data is already in L1 — and for a string it
is — there is no traffic to remove and the arithmetic is all cost.

## A second index builder for small documents does not beat the first

Below about a kilobyte this loses to fastjson — 147 ns against 39 on 64 bytes —
and the reason looked obvious. The vector index costs the same few passes
whatever the document is: five mask passes, a word pass to resolve the strings,
a pass to extract and pair the brackets. Seven passes to describe sixty-four
bytes. One scalar pass ought to win easily.

It does not. Filling the same index in one pass, measured against the vector one
on the same bytes:

	bytes    one pass   vector
	   51       84.3     85.8    1.02x
	  185      305.3    201.2    0.66
	  497      824.1    230.0    0.28
	 1013     1,684     284.1    0.17

A wash at fifty bytes and worse everywhere above it. Tightening the loop — a
class table instead of a chain of comparisons, and setting the in-string bits a
range at a time when a string closes instead of a bit at a time as the bytes go
past — made it *slower* again, 106 ns at fifty-one bytes.

The premise was wrong. After the portable mask builders were rewritten word at a
time, `buildIndexWhole` costs 86 ns for fifty-one bytes, and that is most of the
147 ns a 64-byte `Parse` takes. It is not seven expensive passes; it is seven
cheap ones, and a scalar loop over the same bytes is not cheaper than five
vector calls plus two word passes even at that size.

Getting the two builders to agree was most of the work, and it turned up three
things worth knowing about the vector index — the whitespace mask covers the
whole document including inside strings, escapes are resolved before strings so
a backslash suppresses a quote and nothing else, and a trailing backslash is
only an error inside a string. All three came from the fuzzer that compared
them.

**The rule.** "Seven passes must be worse than one" is a statement about passes,
and what matters is what a pass costs. Measure the thing you are trying to beat
before writing the thing that beats it.

## A 10% regression with no code change at all

`canada.json` lost 9% on `Parse` and 14% on `Valid` between simd.go v1.7.1 and
v1.8.0. The bisect landed on the commit that bumped the dependency, and the
obvious reading — a kernel got slower — is wrong in every particular.

**What the dependency bump changed.** v1.8.0 is v1.7.1 plus one kernel,
`JSONCopyRun`. `Parse` and `Valid` never call it. All four mask kernels are
byte-identical between the two tags:

	maskBits       SAME  2278c7e5
	maskBitsLess   SAME  29feccbb
	maskBitsAny    SAME  fd14ea38
	maskBitsAny4   SAME  01c4adc1

The regression is present with `GOSIMD=avx512`, `avx2` *and* `sse2` forced, so
it is not a dispatch decision either.

**What the counters said.** Two test binaries built from identical simdjson
source, differing only in the dependency version, on the same 2,000 iterations:

	                        v1.7.1          v1.8.0        delta
	ns/op                1,180,335       1,337,931       +13.4%
	cycles              11.83 G         13.50 G          +14.1%
	instructions        80.494951 G     80.495057 G      +0.0001%
	branches            22.548230 G     22.548255 G      +0.0001%
	branch-misses           23.06 M         23.04 M       -0.06%
	L1-icache-misses         472 K           483 K        +2.2%
	stalled-frontend         0.572 G         1.404 G     +145%

The same instruction stream. The same branches, predicted the same way. No
instruction-cache problem worth the name. IPC fell from 6.81 to 5.96 and
frontend stalls went up two and a half times.

**Where.** `perf annotate` puts the cost on `simdjson.go:455`, the loop that
scans the fraction digits of a number, and the disassembly of that loop in the
two binaries is byte-for-byte the same instructions at different addresses:

	v1.7.1                                v1.8.0
	c60d9a: inc    %rdi                   c63b3a: inc    %rdi
	c60d9d: nopl   (%rax)                 c63b3d: nopl   (%rax)
	c60da0: cmp    %rcx,%rdi              c63b40: cmp    %rcx,%rdi
	c60da3: jge    c60db2                 c63b43: jge    c63b52
	c60da5: movzbl (%rdx,%rdi,1),%esi     c63b45: movzbl (%rdx,%rdi,1),%esi
	c60da9: add    $0xffffffd0,%esi       c63b49: add    $0xffffffd0,%esi
	c60dac: cmp    $0x9,%sil              c63b4c: cmp    $0x9,%sil
	c60db0: jbe    c60d9a                 c63b50: jbe    c63b3a

Adding the kernel moved every symbol after it by 0x2da0 bytes, and 0x2da0 mod
64 is 32. So every loop in the binary kept its alignment mod 32 and had its
alignment mod 64 flipped. This one went from offset 26 in its 64-byte line,
where its 24 bytes fit entirely inside one line, to offset 58, where they
straddle two. It runs once per digit byte, over about 1.6 million of them.

The compiler's `nopl` is aligning the loop *body* to 32 — `c60da0` and `c63b40`
are both 32-aligned. The branch target is the `inc` three bytes earlier, and
three bytes is the whole difference.

**There is no fix from Go source.** Nothing here is a mistake to correct: the
code is the same code, the compiler did what it was asked, and the address it
landed at is a property of everything linked before it. Shrinking the loop was
considered and there is nothing to shrink — 24 bytes is `inc`, a bounds check, a
load, and a two-instruction range test, and the table-lookup alternative encodes
to the same size while adding a dependent load. A non-inlined digit-run helper
would shrink `number` from 1408 bytes to a few hundred, and at 444,000 calls per
iteration against 5.9 M cycles per iteration the call overhead alone is more
than a third of the benchmark.

Go PGO does not apply: it reads `default.pgo` from the main package, so a
library cannot ship one that reaches the programs that import it.

**What this is for.** It cost most of a day to find, and the answer at the end of
it was "nothing changed". Next time a benchmark moves and the diff does not
explain it, the first two commands are

	perf stat -e cycles,instructions,branch-misses,stalled-cycles-frontend ./old.test ...
	perf stat -e cycles,instructions,branch-misses,stalled-cycles-frontend ./new.test ...

and if the instruction counts match to five figures, stop reading the diff.

**The rule.** A benchmark is a measurement of a binary, not of a change. Two
builds of identical source can differ by 14% for reasons no line of the diff
mentions. Before attributing a regression to a change, prove the instruction
count moved.

### Postscript: it was recoverable, and the fix was one keyword

The entry above concluded there was no fix from Go source. That was wrong, and
the reason it was wrong is worth as much as the diagnosis.

The loop was 24 bytes because it contained **two** branches, not one:

	c64c40: cmp %rcx,%rdi        the signed test the source asked for
	c64c43: jge ...
	...                          and, on the entry path, an unsigned bounds check

`for j < len(b) && isDigit(b[j])` with `j` an `int` cannot be compiled to one
check, because nothing tells the compiler `j` is not negative. It emits the
signed comparison the source wrote *and* the unsigned bounds check that indexing
requires. Go's alignment pass then spends three more bytes on a `nopl` to put
the loop's condition on a 32-byte boundary — which is what leaves the branch
target, the increment, three bytes earlier and in the previous cache line.

Making the cursor a `uint` removes the second branch, and with it the padding:

	b := d.data
	n := uint(len(b))
	j := uint(i)
	for j < n && isDigit(b[j]) { j++ }

21 bytes, one branch, no padding. Counters on Valid/canada, 1,500 iterations:

	                        pre-regression   regressed        uint cursor
	cycles                      8.875 G       10.233 G          8.886 G
	instructions               60.415 G       60.732 G         59.922 G
	stalled-cycles-frontend     0.592 G        1.439 G          0.823 G

	Valid/canada             1,193,793 ns   1,349,157 ns    1,171,138 ns
	Parse/canada             1,436,472 ns   1,561,474 ns    1,408,494 ns

Fewer instructions than the version before the regression ever happened, because
the double check was always there and the misalignment is only what made it
visible.

An intermediate attempt is worth recording because it looked right and was not.
Re-slicing at each digit run — `j += digitRun(b[j:])` — gets the same one-branch
loop, since a counter starting at a literal zero is provably non-negative too.
It recovered the frontend entirely (0.526 G stalls) and then gave most of it
back: building a fresh slice header at three sites cost **3.07 billion**
instructions across the document, 63.49 G against 60.41. The loop shape was
bought and paid for again in setup. The `uint` gets the shape for nothing.

**The rule, revised.** "No fix from source" is a claim about the search, not
about the code, and it should be made only after looking at *why* the
instructions are what they are. The disassembly had said 24 bytes and two
branches from the beginning. Counting the branches and asking what the second
one was for is the step that was skipped.

## The type cache did not want replacing, and the reason it looked like it did

Reading goccy turned up a real technique: it finds the minimum and maximum
`rtype` address through `go:linkname reflect.typelinks` and indexes a flat array
by `(typeptr - base) >> shift`, where this package used a `sync.Map` keyed by
`reflect.Type`. A `sync.Map` keyed by an interface hashes that interface on
every lookup, which is an indirect call to the type's hash function before any
comparison happens. Once per program that is nothing; once per value in a
stream it is not, and the note in `docs/competition.md` put it at 10% of a
stream decode.

Two replacements were written and both are slower.

**A generic cache**, `map[uintptr]F` behind an `atomic.Pointer`, keyed on the
type pointer taken from the interface's second word — no `linkname`, no
runtime internals, a lookup that is an atomic load and an integer map probe.
Unmarshal **+4.7%**.

**The same thing written out three times** without generics, on the theory that
a `map[uintptr]F` with `F` a type parameter loses the compiler's specialised
integer-key access. Unmarshal **+3.8%**. So it was not generics.

What it is, most likely, is the key itself:

	func typeKey(t reflect.Type) uintptr {
		return uintptr((*(*[2]unsafe.Pointer)(unsafe.Pointer(&t)))[1])
	}

There is no way to read an interface's data word without taking the address of
the interface, and taking the address of a parameter forces it to the stack. A
store and a load, per lookup, to save a hash.

**But the number was stale, and that is the part worth keeping.** The 10% was
measured before the per-Decoder and per-`encodeState` caches were added — the
ones that remember the last type and skip the lookup entirely. Profiling
Unmarshal now puts the whole of `sync.Map.Load` at **2.76%**, not 10%. The
workaround had already collected most of the prize, so every replacement was
competing for 2.76% and each of them spent more than that getting there.

`sync.Map` is a `HashTrieMap` as of Go 1.26, and for a map holding one entry —
which is what a benchmark that unmarshals one type has — it is a pointer chase
and hard to beat.

**The rule.** A technique read out of another library comes with a number
attached, and the number is about that library. Before adopting it, re-measure
the thing it is supposed to fix *here*, in the state the code is in now. Two
implementations were written against a figure that a previous change had
already invalidated.

## How much of a benchmark is the linker: about 8%

Entry 13 showed a 14% regression caused by nothing but an address change. The
obvious follow-up question is how big that effect is in general, and it is
answerable directly: build the same source several times with differing amounts
of dead code and watch a benchmark that the dead code cannot possibly affect.

Four builds, differing only in 0, 7, 14 or 21 unexported functions that nothing
calls:

	no padding          501,741 ns
	7 dead functions    491,382
	14 dead functions   504,024
	21 dead functions   532,253

**8.3% between the best and the worst, from code that never executes.**

This is the noise floor under every number in this repository, and it is much
larger than the run-to-run spread the gate was tuned against — 0.1% to 1.9% for
most of the gate, 4.5% for `GateUnmarshal`. A benchmark's absolute value is
partly a fact about where the linker put things.

What it changes:

- A regression smaller than this band is not evidence on its own. The gate's
  per-benchmark thresholds already reflect it, which is why `GateUnmarshal` is
  allowed 12% and not 8%.
- The way to tell a real change from a layout change is the instruction count,
  not more benchmark runs. Two builds retiring the same instructions in
  different time did not differ in any way the source explains.
- It cuts both ways. A change that *appears* to win by 8% may have won a
  lottery, and re-measuring it after an unrelated commit is the check.

The immediate case: a whole-number fast path in `appendFloat` measured
MarshalTo -10% and Unmarshal +8.8%. Counters settled it — MarshalTo retires
82.02 G instructions against 89.75 G, so that is real work removed, while
Unmarshal retires 90.867 G against 90.876 G and spends the difference entirely
in frontend stalls. One is a change and the other is an address.

**The rule.** Before believing a benchmark moved, count instructions. Before
believing it did not, remember it can move 8% for free.

## Fusing the five mask calls in Go, for documents too small to pay for them

A 36-byte document is four and a half words, and classifying it costs five calls
into simd — `MaskBits` twice, `MaskBitsAny` twice, `MaskBitsLess` once. Each is a
generic dispatch, a threshold check that sends it to the portable path anyway,
and a loop of four iterations. Profiling a 36-byte `Parse` puts 35% in those
five calls, and `simd.MaskBitsAny[go.shape.[]uint8]` alone at 14%.

So: do the same arithmetic, in the same order, word at a time, for all five
masks in one loop. Not a scalar index builder — that is entry 12 and it lost for
different reasons — just the five calls collapsed into the loop that was already
running.

It is slower at every size it applies to. Interleaved, two passes, minimum:

	bytes   five calls   one loop
	   15         87.4      118.3   +35%
	   29        106.2      126.7   +19%
	   36        116.6      134.7   +16%
	   50        135.2      158.0   +17%
	   64        121.9      119.9    -2%

**The interesting part is that a first measurement said the opposite.** Sweeping
the threshold and comparing against numbers taken from an *earlier build* showed
36 bytes going from 138.8 ns to 117.9 — a 15% win, consistent across four
threshold values. Every one of those numbers came from a separately compiled
binary, and the entry above this one measured that exact mistake at 8.3%.

The A/B is one binary against one binary, interleaved, and it says the opposite.

Why it loses is not mysterious once believed: eleven SWAR operations per word
against a compare and a predicate store. The dispatch overhead is real and it is
smaller than eleven operations per word, even at four words. There is no size at
which this wins, because the fixed cost it removes is smaller than the variable
cost it adds at the very first word.

**The rule.** A threshold sweep against remembered numbers is not a measurement.
Build both, run them alternately, and compare within one pass — and be most
suspicious when a sweep is *consistent*, because consistency across builds is
what a layout difference looks like.

## Deriving the bracket kind from masks, and an A/B script that lied

The extraction loop reads `data[p]` once per bracket to decide which of `{}[]`
it is. That is a scattered load into a document far larger than L1, and the
counters agreed it was a load and not a branch: 145,000 L1 misses per Scan of
citm against 2,364 branch misses, with the switch at 920 ms of a 10 s profile.

Two extra masks — `{[` for open-versus-close and `[]` for curly-versus-square —
give both bits from two *sequential* words per sixty-four bytes of document,
replacing one scattered byte per bracket.

It is 32% slower.

	Scan/twitter, three runs each, alone
	  before   59,126   59,769   59,906
	  after    78,177   78,365   79,030

Two more passes over the whole document cost far more than the scattered loads
they remove, and the reason is the ratio: a mask pass touches every byte, while
the loads it saves happen only at brackets — about one byte in twenty. Trading
1.7 MB of sequential reads for 85,000 scattered ones is a bad trade even though
scattered reads are individually dearer.

**The A/B script reported it as a 24% improvement.** The pattern used all
session pipes two labelled runs into awk, sorts, and compares. `NEW` sorts
before `OLD`, so the first field is the new number and the second is the old
one — and this particular copy printed them in that order while labelling the
columns the other way round. Every figure came out with the sign flipped.

It was caught because the "before" number did not match the recorded baseline:
the script said the old binary ran Scan/twitter in 77,889 ns where the gate's
baseline said 59,178. A number that should have been familiar and was not.

**The rule.** An A/B harness needs a control. Run the unmodified binary alone
and check it against the recorded baseline before believing anything the harness
says about the modified one — and if the two disagree, the harness is wrong
until proven otherwise. A measurement tool is code, and it is the one piece of
code whose bugs are invisible in its own output.

## Whitespace skipping is a quarter of a parse and near its floor

After the fused mask kernel, `Parse` on citm_catalog.json spends 25% of its time
stepping over whitespace — `skip` 25.5% cumulative, `skipRun` 13.9%,
`skipRunAcross` 4.6% — against 4.6% in the whole vector stage. The document is
71% whitespace, four spaces of indentation at a time.

That looks like the obvious next thing and it is mostly already done. The run is
a bit scan over the whitespace mask, so a run of any length costs the same as a
run of one; `noWS` proves the machine-generated case has none at all; and
`skipRun` caches the last mask word it read.

Disassembling `skipRun` says where the rest goes, and it is not one thing:

	4.40%  push %rbp                      the prologue -- it is a call
	2.41%  mov 0x18(%rax),%rdx            loading d.ix
	7.63%  test %rdx,%rdx                 the nil check on it
	4.76%  cmp %rbx,0xe8(%rdx); jne       the cached-word check
	3.75%  mov 0xa0(%rax),%rsi            the mask slice header

Ten percent of it is reaching the cache through `d.ix`, so moving those two
fields to `Doc` should pay. Measured, control first: Parse/citm 602,295 →
599,060, Parse/twitter 221,943 → 220,647, Valid/citm 412,943 → **417,487**. A
wash — about 1% better where there is little whitespace and about 1% worse where
there is a lot, because `Doc` is read by everything and sixteen more bytes of it
cost more than the indirection saves.

**The rest is call overhead spread thin.** `skipRun` cannot be inlined into
`skip`: `skip` sits at cost 79 against a budget of 80, and a single-byte fast
path that pushed it over cost 6%. So the floor is one call per token, and citm
has a quarter of a million of them.

What would actually remove it is not making the skip cheaper but not skipping:
indexing token *starts* rather than bracket positions, which is what C++
simdjson's pseudo-structural characters are. That was tried and is entry 2 —
slower here, because extracting the extra positions costs more than the scan it
saves.

**The rule.** A number being large is not the same as a number being available.
Twenty-five percent spread across a quarter of a million calls, each doing eight
operations, has no single place to attack; the profile says where the time is
and the disassembly says there is nothing in it to remove.

**A correction.** The comment that put these two fields on the index claimed
Doc placement cost 4.8%. It came from the A/B script with the inverted columns
(entry 16). The direction survived re-measurement and the magnitude did not.

## A size limit the code did not honour, and the off-by-one that nearly hid it

The library documents a 2 GiB limit on `Parse` and `Scan`, set by the int32 the
bracket positions are stored in. The real limit was half that, and crossing it
was a panic rather than an error.

The bracket stack packed two things into one int32: the entry index shifted left
one, with the opening kind — brace or bracket — in the low bit, so a closing
bracket could check that it matched without a second look at the input. That is
a good trick and it costs one bit. The cost was never accounted for: the index
gets 31 bits, not 32, so it overflows the sign at 2^30 entries and the pop's
`o >> 1` comes back negative.

2^30 entries fit in 1.5 GiB of `[[],[],...,[]]`, which is inside the limit the
library says it accepts. It panicked with `index out of range [-1073741823]`.

**The off-by-one.** The first attempt at a repro used `N = 2^29` inner pairs,
which is `2N+2 = 2^30+2` entries, and it parsed correctly. Only *openings* go on
the stack, and the last opening is at `2N-1 = 2^30-1` — one short. `N = 2^29+100`
panics. An entry count above the threshold is not the same as a *stack push*
above it, and a repro that gets that wrong reads as proof the bug is not there.

**The fix is the smallest array.** `stack` is one entry per level of nesting,
not one per bracket, so it is orders of magnitude smaller than `pos` and `match`
and int64 costs nothing measurable — 0.9% on Valid/canada against a control,
inside the 8.3% layout floor. Widening `pos` was never the answer: the index is
already 0.93x the document.

With the stack widened, an entry index is bounded by the document length and
fits an int32 for exactly the reason a position does, so 2 GiB is now the limit
in fact and not only in the comment.

**Rejected: 4 GiB.** Making `pos` and `match` unsigned would double it, which is
what C++ simdjson does. Not taken. It is a signedness change spread across the
binary search and every consumer of `match`, in the hottest code here, and the
answer above the limit does not change with the number — it is `Decoder`, which
streams and has none. A 2x capability gain is not worth a class of sign bug in
`matchBracket`.

**The rule.** A constant that names a limit is a claim about the code, and the
code has to be run at that size to know whether the claim is true. This one was
wrong for the whole life of the library because nothing in the test suite
allocated a gigabyte. The tests that check it now are gated on an environment
variable and skipped by default, which is the price of having them at all.

## The comparison sort was 18% of an encode, and that was not the floor

Not a rejection. A correction to a number recorded here, and the reason it was
wrong.

Sorting a decoded document's map keys was `sort.Slice`, which swaps through
reflect. Replacing it with `slices.SortFunc` over a concrete slice was measured
at 18% of marshalling a decoded document, and that 18% is real. What went with
it was the assumption that a comparison sort was the shape of the answer and
only the swap had been wrong.

It was not. `slices.SortFunc` still pays two costs a comparison sort cannot
avoid:

  - The comparator is a function value. Every one of the n log n comparisons is
    an indirect call the compiler cannot see through, and the body is small
    enough that the call is a large fraction of it.
  - Each comparison walks the keys from byte zero. JSON keys in one object share
    prefixes constantly -- twitter.json's status objects hold `id`, `id_str`,
    `in_reply_to_status_id`, `in_reply_to_status_id_str`, `in_reply_to_user_id`,
    `in_reply_to_user_id_str`, `in_reply_to_screen_name` -- so the same prefix is
    re-walked on every comparison that touches those keys.

A three-way radix quicksort has neither. It partitions on one byte at a time, so
a prefix is examined once for a whole subarray rather than once per comparison,
and the byte is read inline with no call.

	twitter, marshal a decoded document        sorted      sort alone
	slices.SortFunc                            986,932 ns    418,158
	three-way radix quicksort                  796,355       217,850

The sort itself is 1.92x, the whole encode is 19.3%, and the encode goes from
1.20x behind sonic to 1.05x ahead. On citm_catalog the sort nearly vanishes:
sorted is 4.6% above unsorted, where it used to be 41%.

**Two things this is not.** It is not a faster comparison -- `cmpString` was
already `strings.Compare` written out to avoid the call, and it is now deleted,
because the radix sort never compares two whole keys in the partition loop at
all. And it is not codegen: sonic reaches the same place with the same
algorithm, in plain Go, called from its JIT rather than compiled by it. That was
worth reading before starting, because it settled that the gap was reachable
without a code generator.

**The rule.** A measured improvement to a thing is not evidence that the thing
is the right shape. `sort.Slice` -> `slices.SortFunc` was 18% and made the next
question "which comparison sort", when the answer was that a comparison sort was
paying for an ordering property -- a total order on whole keys -- that emitting
sorted JSON does not need.

## A control taken from the first benchmark says nothing about the last

The rule from entry 16 is that an A/B harness needs a control: re-measure
something the change cannot touch and check it reproduces its recorded value. It
caught an inverted script once and a noisy machine twice. Here it passed and the
measurement was still wrong.

Re-recording the gate baseline after the number-scanner change, the control was
`GateParse/twitter` -- the first benchmark the gate runs, and one the change
cannot reach. It came out at 219,462 against a recorded 219,921, matching to
0.2% at a load average of 0.72. On that basis the baseline was updated.

`GateValid/canada` in the same file read 2,266,582 ns. It had just been measured
at 917,263.

The benchmark run had started quiet. `make verify` and `make fuzz` were started
while it was still going, and thirty-two fuzz workers took the machine. Twitter
had already finished; canada had not. The control was clean because it ran
before the interference, and every benchmark after it was garbage -- and a
baseline recorded from that file would have locked a 2.2 ms floor under a 917 µs
benchmark, so a real regression to twice the correct time would have passed the
gate silently.

**The rule.** A control has to be exposed to the same conditions as the thing it
validates, and "the same conditions" includes *when*. One measured at the start
of a run only certifies the start of the run. Either interleave it, or check
every benchmark the change cannot touch rather than one, or do not run anything
else for the duration -- which is the actual fix here, since the interference was
self-inflicted.

`benchcheck` already refuses to record above a load average of 4 and had
refused this file once, on an earlier attempt, with "a benchmark taken under
load is not a slow benchmark, it is no benchmark". It could not refuse the
second time: by then the load average had decayed, and the damage was in a file
written minutes earlier. A guard on the machine at write time does not see what
the machine was doing at measure time.

## Validating the document once instead of per skipped field

Unmarshal sets `strictSkip`, so a field the struct does not name is proved
well-formed by a grammar descent rather than stepped over with a bracket
lookup. That descent is 25.4% of unmarshalling twitter.json into a struct,
measured by turning it off — which is incorrect, and is exactly why it is only
an experiment:

	descent (correct)                 355,662 ns
	no validation of skipped fields   265,262      the floor, 1.28x past goccy

So 90 us is on the table, and it cannot simply be dropped: the index proves
brackets balanced and strings terminated, but not that numbers are well-formed
or that colons and commas are where the grammar wants them.

The obvious replacement is the mask walk. `Valid` already chooses between the
two and the comment there records the mask walk as 33% faster on twitter-shaped
whitespace, so proving the whole document once up front and then skipping with
a bracket lookup should be cheaper than descending most of it.

	descent per skipped field    355,662 ns
	validTokens once, then skip  396,971      11.6% slower

The 33% does not transfer. The mask walk costs about 132 us over the whole
document; the descent costs 90 us over the roughly 70% of it the struct skips.
Per byte they are within 2% of each other. What made the mask walk look faster
in `Valid` was everything else that differs between those two paths, not the
token loop.

**Which also kills the bounded version.** The plan behind this measurement was a
`validRange(start, end)` — the same state machine stopped at a subtree — so that
skipped fields got the fast validator instead of the descent. That is about a
hundred and eighty lines of delicate state machine, and it would have been worth
roughly 2%, not the 8% the 33% figure implied. The four-line whole-document
version answered the same question for nothing.

**The rule.** A ratio measured between two whole code paths is not a ratio
between the loops inside them. Before building the fast one into a second place,
check the cheap version of the same idea — it costs an afternoon less and
answers it.

## Doing it again, three hours after writing it down

Entry 21 says: do not run anything else while a benchmark runs, because a
control measured before the interference certifies nothing after it. Recording
the gate baseline again, the procedure was two independent runs with the two
agreeing as the control -- the usual control being unavailable, since that
change touched Parse, Scan and Valid on every corpus and left nothing untouched
to re-measure.

Two of fifteen disagreed:

	                      run 1     run 2
	GateStream/Decode   516,649   466,298   10.8%
	GateUnmarshal       460,652   410,322   12.3%

Both are the allocation-heavy ones, and both moved the way contention moves
them, because `gofmt -l` and `go vet` were run on the bench module during run 1.
The rule was followed except for the part where it was not: a vet in a different
module felt like it did not count, and the scheduler does not know what a module
is.

Run 3, with nothing else touching the machine, agrees with run 2 across every
benchmark within 1.4% and most within 0.8% -- including those two. The baseline
is the minimum over runs 2 and 3, sixteen samples.

**And benchcheck said the disagreeing runs were fine**, for two reasons, and
the first one I got wrong before checking. It compares in one direction, so a
run that came out *faster* is never a regression -- but that is not why these
passed. `GateUnmarshal` and `GateStream/Decode` are two of the three benchmarks
in `wideThreshold`, exempted at 12% and 15% because they allocate per value and
their timing includes whichever collections landed inside it. Measured from the
baseline the disagreements are 10.9% and 9.7%, inside their own exemptions. The
exemptions were doing exactly what they were written to do, and they are wide
enough to swallow a contaminated run.

So `-agree` was added: same comparison, both directions, per benchmark. And
testing it found a second defect, in code whose comment already described the
right behaviour:

	lim := *threshold
	if w, ok := wideThreshold[n]; ok && w > lim {
		lim = w
	}

The comment above it reads "A flag tighter than an entry in the table is a
deliberate ask for a stricter run and has to win, or -threshold 1 would silently
do nothing for three of fourteen." The code compares magnitudes only, so the
exemption wins whenever it is larger -- and `-threshold 1` did silently do
nothing for those three, which is the thing the comment says must not happen. It
now applies the exemption only when the threshold was not given explicitly.

**The rule.** When the question is "do these two runs agree", a threshold check
that only fires downward is the wrong instrument -- and per-benchmark exemptions
sized for noise are, by construction, sized to hide interference too. Also: a
comment describing what the code should do is not evidence that it does. That
one had been right and wrong in the same paragraph for as long as it existed,
and only writing a test that depended on it found out.

## Arguing about runtime internals instead of measuring what they were worth

The compiled map encoder allocated three times per entry. goccy allocates twice
for a whole map of any size, and it gets there with `//go:linkname` into
`runtime.mapiterinit`, `reflect.mapiterkey`, `reflect.mapiternext` and
`reflect.mapiterelem` -- walking the map's iterator with raw pointers, never
building a reflect.Value.

The question was whether to do the same, and the case against it was being
assembled before the case for it had a number attached. Two facts had already
turned up, and both cut against the caution:

  - Go ships `runtime/linkname_shim.go`, whose header reads "Legacy
    //go:linkname compatibility shims. The functions below are unused by the
    toolchain, and exist only for compatibility with existing //go:linkname use
    in the ecosystem." When Go 1.24 replaced maps with Swiss tables, the team
    kept these working on purpose, emulating a `hiter` layout that no longer
    exists.
  - goccy's `map112.go` / `map113.go` split shows the failure mode when one does
    break: `reflect.mapitervalue` became `reflect.mapiterelem`, and the fix was
    two files and a build tag.

None of that decided anything, because the missing number was how much the
technique was worth here. With plain reflect:

	n=64 struct        allocs      ns
	before                194  12,273
	after                   4   7,235
	goccy (linkname)        2   8,218
	sonic                   3   5,866

Four allocations, flat in n, against goccy's two. What linkname would buy from
here is one or two allocations per map, against thirty microseconds of encoding
at n=256 -- nothing. And sonic is still 1.23x ahead while allocating three
times, which says its remaining lead is not allocation at all, so the technique
would not have closed that either.

The three commits that did it are ordinary reflect: hoist the key's
TextMarshaler test to the type (`k.Interface()` boxes, and whether a type
implements an interface is not a per-value question), fill one reused Value with
`SetIterKey` instead of allocating one per entry, and hold every value in a
single `reflect.MakeSlice` sized by `rv.Len()`.

That last one carried a cost nobody would look for. `ptrOf` allocates and copies
whenever its argument is not addressable, and a Value from `MapIter.Value` is
not addressable -- `copyVal` does not set `flagAddr`. So every value cost two
allocations, not one: the box, and then a second copy to get a pointer to it.
Slice elements are addressable, so putting the values in a slice removed both.

**The rule.** "Depends on unstable internals" is a statement about risk, and
risk is only half of a decision. The other half is what the technique is worth,
and that is a measurement. Here it was worth approximately zero, which is a
better reason to skip it than the one that was about to be written down -- and
if it had been worth 40%, the shim and the build-tag precedent say the risk was
smaller than the hedge implied.

## A 3.8x microbenchmark that was worth nothing

Quoting is 54% of marshalling a struct, and `appendQuoted` called
`plainASCIIRun` first. For a string of printable ASCII that returns the whole
length and the early return fires -- so the vector kernel never ran on ASCII at
all. Two passes, a word-loop scan and then a memmove, where the kernel does one
fused pass that copies while it scans.

Routing strings of 64 bytes or more through the kernel instead:

	appendQuoted, ASCII    before   after
	96 bytes                 29.0    12.6    2.3x
	140                      41.0    20.0    2.1x
	256                      74.0    19.5    3.8x
	512                     127.4    33.5    3.8x

On the actual encode it was worth nothing. BenchmarkMarshalStruct went from
59,248 to 60,334 ns, which is inside the 1% the controls drifted, and the map
and decoded-document rows moved 0.3%.

The strings in the benchmark say why:

	short, under 64 bytes    829 strings              unaffected
	long and ASCII           124 strings   9,677 B    the only case this touches
	long and non-ASCII       248 strings  54,449 B    already used the kernel

A non-ASCII string was already reaching the kernel through the tail path. What
the change actually improved was 124 strings holding 12.8% of the string bytes
-- and it made the other 87% slightly worse, because it validates UTF-8 over the
whole string where the old path validated only the tail after the ASCII prefix.
3.8x on an eighth of the data, minus a little on the rest, is zero.

**The rule.** A microbenchmark measures a function; it does not measure how
often that function is the one being called, or on what. Before believing a
speedup, count what fraction of the real input reaches the path that got faster
-- that count took one test and would have predicted this result without
writing the change at all.

The 54% figure was not wrong, and that is the trap. Quoting really is half the
encode. But almost all of it was already on the fast path, and the profile
cannot show that, because a profile attributes time to functions rather than to
the inputs that drove them.

## A measurement in a comment that was wrong, and the reasoning under it

`cleanRun` tested for control bytes by clearing each byte's high bit first,
which turns 0x8D into 0x0D and calls it a control character -- wrong for 0x80
through 0x9F, the commonest UTF-8 continuation bytes. Changing it to the exact
test looked like a bug fix and was committed as one.

It was not a bug. `cleanRunOpts`, twenty lines below, already used the exact
test, under a comment explaining why `cleanRun` deliberately did not:

	It is worth an operation here and not in cleanRun, and the difference is
	the rest of the set. ... cleanRun also looks for 0xE2, which stops it at
	that text anyway, so there the extra operation buys nothing and costs
	2.5%. Measured both ways on both paths.

So a documented, measured decision was overturned on a 2.4% reading whose own
commit message admitted the controls had drifted 1% and sonic's 7%. That is the
wrong way round: a recorded measurement is evidence, and beating it needs better
evidence, not a noisier number in the other direction.

Re-measured properly -- three passes of twelve samples each way, with the Fast
path as a built-in control because it goes through `cleanRunOpts` and not
through `cleanRun`:

	                masked   exact
	Marshal         60,204  58,327   -3.1%
	MarshalTo       53,216  51,452   -3.3%
	Fast            34,902  35,000   +0.3%   control, untouched

The exact test wins, by more than the 2.4% originally claimed, and the control
confirms nothing else moved. So the change stays -- but it stays because of this
measurement, not the one it was committed on.

**Why the old number was wrong**, which matters more than which way it went. The
justification was that `cleanRun`'s 0xE2 probe stops the loop on non-ASCII text
regardless. `hasByte(w, 0xE2)` matches one byte value, and Japanese is E3 81 82
and its neighbours -- there is no 0xE2 in it. The probe never fires on CJK. What
was stopping the loop was the masked control test itself, mistaking continuation
bytes for controls. The comment described a mechanism that does not occur on the
corpus it was measured against.

**The rule.** When a comment records a measurement that contradicts what you are
about to do, the disagreement is the finding. Re-measure both, with a control,
before touching either -- and if the old number turns out wrong, correct the
comment rather than silently leaving it to argue against the code beneath it.

## Using the fused kernel on the continuation after an escape

The base encode's profile shows two kernels running over the same strings:
`jsonCopyRunAVX512` at 12.6% and `indexAnyOrLessAVX512` at 10.5%. The reason is
the loop shape. `JSONCopyRun` copies while it scans and stops at the first byte
needing an escape; after that the loop scans the next run with a separate call
and then copies it with `append`. Two passes over bytes the kernel could have
done in one, and after an escape near the front of a tweet there can be a lot of
them left.

Continuing with the kernel instead:

	                          control   change
	MarshalStruct/ours         59,295   60,536   +2.1%
	MarshalStruct/MarshalTo    52,152   54,440   +4.4%
	twitter decoded, sorted   789,481  816,442   +3.4%
	MarshalStruct/Fast         35,946   35,405   -1.5%

Slower, on everything except the path that does not use it.

The runs after an escape are short. A tweet has a newline or a quote every few
dozen bytes, so what the kernel gets handed is usually well under the length
where it repays its call -- and each continuation also pays a `growTo` capacity
check that the single up-front call paid once. The two-kernel profile was real
and the inference from it was not: `indexAnyOrLessAVX512` is not a redundant
second scan of the same bytes, it is scanning what is left, and the reason there
is a lot of it is that the string is long, not that the work is duplicated.

**The rule, and it is the fourth time today.** A profile shows two functions
doing similar work; that is not evidence they are doing the *same* work. The
check is cheap -- how long are the runs each one gets -- and it is the same check
that would have predicted the other three: how often is the path that got faster
actually taken, and on what.

## The floor was slower than the thing it was supposed to be a floor for

Nothing in the profile accounted for sonic's lead on struct encoding, and the
one structural difference left was that sonic compiles the field sequence to
machine code at run time while this makes an encodeFn call per field. That fits
a deficit spread thinly with no hotspot, and it is the kind of explanation that
survives because it cannot be checked by looking harder at the same profile.

So it was measured. A hand-written encoder for the exact struct: same fields,
same order, no reflection, no dispatch, no options consulted -- what perfect
codegen would emit -- checked byte-identical to encoding/json so it is doing the
same job.

	hand-written, no dispatch    61,311 ns
	ours, MarshalTo, Std         52,300
	ours, no options             35,189
	sonic ConfigStd              31,650

The floor is 17% SLOWER than the thing it was meant to bound. Per-field dispatch
is not the gap, and a code generator -- which was the next thing on the list --
would have been weeks of work to arrive somewhere worse than where the code
already is.

It is slower because it uses strconv.AppendInt and a byte-at-a-time escape scan,
and this package's own primitives beat both. Which is the actual finding: the
dispatch overhead is real but smaller than what the primitives buy back, so the
compiled-closure design is not what is costing anything.

**That last sentence was wrong, and this is the part worth reading.** "The
primitives beat the floor's primitives" says nothing about dispatch; it says the
experiment held two things different at once and attributed the difference to
the wrong one. The floor was rebuilt in the main package so it could call this
package's own appendInt and appendQuoted, leaving the field sequence being
straight-line rather than a loop as the only remaining difference:

	hand-written, our primitives   41,394 ns
	compiled encoder               50,530

The floor is 18% FASTER. Per-field dispatch IS a cost, the first floor could not
have shown it, and "codegen would not help" did not follow from that run. See
floor_test.go and task #146.

It is not the encodeFn calls: the leaf set covers string, int64/int, uint64/uint,
bool and float64, so in this struct only the nested User and the Statuses slice
reach f.fn -- 101 calls against roughly 2,100 field writes. It is the loop
itself. Per field: load &fields[i], test f.simple, test omitAll, compute
unsafe.Add(p, f.offset), switch on f.leaf. Generated code has the offsets and
key bytes as constants and does none of it.

So the rule below stands, but with a second clause: measuring the floor was the
right call, and a floor is only a floor if the ONLY thing it changes is the
thing under test. Two variables in one experiment is how a measurement produces
a confident wrong answer instead of no answer.

**Where the gap is instead**, from sonic's native/quote.c:

	if (*dn >= nb * MAX_ESCAPED_BYTES) {
	    *dn = memcchr_quote_unsafe(sp, nb, dp, tab);
	    return nb;
	}

It reserves six bytes of output per input byte and then runs a vector pass that
writes the escapes inline with no per-byte bounds check. simd.JSONCopyRun stops
at the first byte needing an escape and returns to Go, which emits it and calls
back. Five escapes in a tweet is five round-trips against one call. Our escaping
costs 17,452 ns on top of a 35,189 base; sonic's escaping and UTF-8 validation
are both inside its 31,650 total.

**The rule.** An explanation that fits the evidence and cannot be tested by more
of the same evidence is where to spend a measurement, not where to start
building. This one cost one benchmark and killed a multi-week direction. The
same measurement also produced the real answer, because knowing the floor tells
you which side of it to look on.

## Five attempts at appendQuoted, five regressions

The escaping path is 17,266 ns on top of a 35,189 ns base, and sonic does
escaping and UTF-8 validation inside 29,542 ns total. So it is the gap, and it
resisted five separate attempts:

	route long ASCII strings through the kernel     0%      (12.8% of bytes)
	continue with the kernel after each escape      +2..4%
	exact control test in cleanRun                  -3.1%   kept
	escapes interrupt the ASCII run, split fn       +6.9%
	the same, without the double scan               +5.3%

The last one is the instructive pair. `plainASCIIRun` returns early only when it
consumes the whole string, so one newline in an otherwise-ASCII string fell
through to `simd.ValidUTF8` -- a full validation pass over bytes all under 0x80
and valid by construction. Isolated, fixing that is worth a lot:

	escapes in 512 bytes    0      1      2      4      8     16     32
	before              131.8  195.4  202.1  218.7  240.1  262.7  301.7
	after               128.9  137.8  140.0  124.1  133.8  158.6  197.1

The first escape went from 63.6 ns to 8. And the encode got 5.3% slower.

The first version also re-scanned the ASCII prefix in the function it bailed
into, which cost 7-9% on its own and was a plain mistake. Fixing that left 5.3%
that is not a mistake: it is the cost of splitting appendQuoted in two, so the
non-ASCII path -- the long strings, 84.8% of the bytes -- goes through a call
that used to be straight-line code.

**What this says about the function.** appendQuoted is tuned to within a few
percent by its shape, not only its algorithm, and the shape is load-bearing: an
early return that the common case hits without a branch, and one function so
the compiler keeps it together. Every change that improved a case improved a
minority of the input and paid for it on the majority.

**The rule.** When a function has been optimised to the point where its
structure is the optimisation, a change that is locally better needs to be
measured on the whole before it is believed -- and "locally better" here meant
43% on a microbenchmark, five times, while the thing that matters got worse.

## The sixth attempt was the fifth attempt with the call on the other side

The escaping path took six tries. The fifth and the sixth are the same
optimisation -- an escape interrupts the ASCII run instead of ending it, so a
string that is all ASCII never pays for the UTF-8 validation pass -- with the
same isolated numbers:

	escapes in 512 bytes    0      1      2      4      8     16     32
	before              131.8  195.4  202.1  218.7  240.1  262.7  301.7
	fifth               128.9  137.8  140.0  124.1  133.8  158.6  197.1
	sixth               133.1  154.2  147.0  130.3  139.6  163.6  207.7

The fifth is the better of the two in isolation. On the encode:

	fifth    52,455 -> 55,644   +6.1%
	sixth    52,589 -> 50,008   -4.9%

The difference is which side of the branch got the function call. The fifth put
the non-ASCII path behind one, and non-ASCII strings are the long ones -- 248 of
1201, holding 84.8% of the bytes. The sixth leaves that path exactly where it
was, straight-line, and puts the ASCII-with-escapes case behind the call: 25% of
strings, and the short ones.

**What distinguished them was not the microbenchmark.** The fifth looks better
there. What said it would lose was that `jp/48` and `jp/96` -- non-ASCII, and
untouched by the change in principle -- had moved. In the sixth they did not:
42.31 against a 42.8 baseline, 19.89 against 19.6.

**The rule.** In a function tuned to within a few percent by its shape, the
optimisation is not the only variable; where the compiler is allowed to keep the
code together is another, and it can be larger. When measuring one, watch the
paths the change is not supposed to touch -- if they moved, the shape changed,
and the shape is what will decide the result.

## The quote kernel, built and then not used

sonic's lead on struct encoding came down to one mechanism: its `quote.c`
reserves worst-case output space -- six bytes per input byte, every byte
becoming \u00XX -- and then runs a bounds-check-free vector pass that writes
escapes inline. `simd.JSONCopyRun` stops at the first byte needing an escape and
returns, so the caller emits it and calls again.

So the kernel was built. `simd_json_quote`, emitted for amd64, arm64 and
riscv64; refused by the emission check on loong64, s390x and ppc64le because
clang allocates registers the Go runtime owns there, which is that check working
as designed. Tested against a loop written from encoding/json's rules over every
byte value, every escape shorthand, U+2028 and U+2029 and their lead-byte
neighbours, truncated sequences, 3,200 cases with an escape at every position
across a vector block boundary, and 3,000 random inputs. It is correct.

It made the encoder slower.

	                       control   change
	Marshal, struct         58,011   73,974   +27.5%
	MarshalTo, struct       51,501   53,424    +3.7%
	twitter decoded        797,747  806,834    +1.1%
	control goccy           92,334   93,057    +0.8%

The split between the two struct rows is the explanation. `MarshalTo` writes
into a buffer the caller already owns, so the reservation is usually free.
`Marshal` allocates one per call sized from a hint the pool carries, and asking
for six times the input immediately reallocates it -- every call, on a buffer
that then never uses five sixths of what it asked for.

**The reservation is the technique.** It is what removes the per-byte space
check, and it is also what an append-based encoder cannot afford: sonic writes
into a buffer it sized up front, and this writes into one that grows. The two
designs are not interchangeable at the point where the kernel is called, and no
amount of tuning the kernel changes that -- the cost is in the caller.

A first attempt made it worse still by reserving unconditionally: a 144-byte
non-ASCII string with nothing at all to escape went from 34.3 ns to 44.7,
because it paid for output space it never wrote. Reserving only after an escape
is known to exist fixed that and left the numbers above.

**The kernel stays in simd.** It is correct, it is tested, and a caller with a
pre-sized destination -- which is most callers of a quoting primitive -- gets
what it promises. It is simdjson that cannot use it, and that is a fact about
simdjson's buffer strategy rather than about the kernel.

**The rule.** A mechanism copied from another implementation carries its
preconditions with it. sonic's quoting is fast because of the reservation, and
the reservation is affordable because of how sonic allocates. Reading the first
half and not the second is how a correct kernel ends up 27% slower in place.

## Fusing validation into the copy, which the profile said was 44%

The Std encode walks every string three times: plainASCIIRun for the clean ASCII
prefix, validUTF8String over the tail, then JSONCopyRun over that same tail for
escapes. Profiled, those are 10.7%, 16.0% and 22.6% of a struct encode. sonic
walks it once.

So simd_json_copy_valid was built: simd_json_copy_run with the classifier from
simd_valid_utf8 folded into the same block loop, returning the count or a
negative value when what it copied was not valid UTF-8. It needs no extra output
space, which is what killed the quote kernel before it -- it still stops at
escapes rather than writing them.

	                       control   change
	Marshal, struct         58,711   60,094   +2.4%
	MarshalTo, struct       51,884   52,671   +1.5%
	twitter decoded        806,363  805,375   -0.1%
	control goccy           92,954   93,196   +0.3%

Slightly worse. The 16% is real and it is not available, for two reasons the
profile cannot show:

  - It is concentrated in the 248 strings of 1201 that hold non-ASCII. For the
    other 953, validUTF8String is answered by plainASCIIRun having consumed the
    whole string, and never runs at all.
  - Inside the kernel, an all-ASCII block already skips the classifier -- the
    `(v | prev) & 0x80` test is the same one simd_valid_utf8 uses. So fusing
    saves the call and the second walk, not the checking. What it adds is
    carrying `prev` and `err` across every block, including the ones that
    skipped.

Net: a saving on a quarter of the strings against a cost on all of them.

**The kernel stays in simd.** It is correct -- checked against every byte value,
malformed sequences of every shape at every offset across a block boundary,
5,000 random inputs and 5,000 valid ones -- and a caller that genuinely needs
both answers over the same bytes gets them in one pass. simdjson is not that
caller, because plainASCIIRun has usually answered both before the kernel is
reached.

**The rule, again and more sharply.** A profile line is a cost, not an
opportunity, and the difference is which inputs produce it. 16% spent in
validation across 1201 strings is not 16% available if 953 of them never enter
the function. Counting that took one test and it was not run first.

## An array element with a leading zero became two elements

Streaming `[01]` with Token and Decode returned two elements, `0` and `1`, and
no error. `[01,2]` returned three. encoding/json returns the `0` and then stops
with "expected comma after array element".

This is worse than a missing error. Invalid input did not merely get accepted;
it produced MORE data than it contained, and a caller ranging over elements got
a value that is not in the document.

Decode and Value consume the comma in front of an element after the first, and
neither required one to be there:

	if c == ',' {
		d.off++
	}

Not a comma, carry on. So `01` decoded the `0`, found no comma, shrugged, and
decoded the `1` as the next element. Token had the check the whole time -- see
`errAt("expected ',' after element")` in token.go -- which is why the bug was
invisible from the Token side and only reachable by decoding elements.

**Found by a test written to check something else.** The window path in
loadWindow needed cases where a batch boundary lands in an awkward place, so
one of them was an array holding a malformed number. It failed at every window
size including the one where the new code cannot run, which is what said the
bug was older than the change under test.

**The rule.** A differential test that only compares the happy path compares
the half that was already working. `[01]` is four bytes; the case cost nothing
to write, and nothing in the suite covered it because every streaming test used
valid input. Feed the malformed thing to both sides and check the count, not
just the values -- a decoder that invents an element passes any test that only
looks at the elements it does return.

## A truncated array hung the decoder forever

`[1,2` streamed with Token and Decode never returned. Neither did `[1` or
`[{"a":1},2`. Not slow -- a spin, with no bound and no allocation, on four
bytes of malformed input. Anything decoding untrusted JSON with a Decoder had
an unbounded hang reachable by truncating an array after a number.

loadOne:

	end, ok := d.valueEnd(d.buf, d.off)
	if ok { ... }
	if d.err != nil {
		if d.err == io.EOF && end > d.off && d.buf[d.off] != '"' && ... {
			continue
		}

Nothing in that loop changes state when `ok` is false. peek does not advance
past a non-space byte and valueEnd is pure, so `continue` re-ran exactly the
same computation and took the same branch, forever.

The intent was right and only the control flow was wrong. A number whose end
the buffer cannot prove, with the reader at EOF, IS a complete number -- the end
of the input is the end of the value, and encoding/json hands back the 1 and the
2 from `[1,2` before it stops. The fix is to accept the value ending there, not
to loop hoping for input that is not coming.

The first attempt at the fix returned io.ErrUnexpectedEOF instead, on the
reasoning that a container that never closed is an error. That is true and it is
not this value's error: it belongs to the read AFTER the last good value, which
is the rule safeEnd already follows everywhere else. The differential caught it
immediately -- one element back where the stdlib gives two.

**Nothing found it for as long as it existed.** Not the suite, not seven fuzz
targets. FuzzDecoderAgainstStdlib decodes at the top level, where a different
loader handles framing; FuzzTokenAgainstStdlib reads tokens and never decodes an
element. loadBatch runs only with a container open, so the entire batched
element path -- the one the README recommends for documents too large to hold --
had no fuzzer over it. `[1,2` was already sitting in streamInputs; it was fed to
Decode, which never enters that path, and never to Token-then-Decode, which
only does.

**The rule.** Ask which code a fuzz target can actually reach, not which
package it lives in. Seven green targets and a suite that passed said nothing
about a function none of them called. The test that found this was written to
check something else and found it in its first run, because it was the first
thing to call that code with input designed to break it.

## A microbenchmark said 1.6x and the encode did not move

plainASCIIRun is a byte loop over a 256-entry table and was 18.1% flat of the
struct encode with dispatch removed -- the largest single item in that profile.
cleanRun beside it does the same job eight bytes at a time with SWAR. So the
obvious change was to make plainASCIIRun match cleanRun.

It is 1.5x SLOWER, at every length measured:

	bytes      4      8     12     16     24     32     64    128
	SWAR    2.27   3.38   4.23   6.21   9.14  11.90  23.57  46.63
	byte    1.22   2.01   2.81   3.58   5.20   6.74  13.06  25.71

and 1.50x slower over all 18,099 strings in twitter.json. The stopping set has
five members, so a word test needs five hasByte chains at four operations each
plus an exact below-0x20 test: twenty-five-odd operations per eight bytes,
against eight L1 lookups whose branch is taken every time until the byte that
ends the run and therefore predicts perfectly. No length is long enough to turn
that around.

**Then the same test was run on cleanRun, which ships as SWAR**, and it lost by
more -- six stopping bytes rather than five:

	bytes      8     16     32     64    128    512   4096   corpus
	SWAR    3.54   6.80  13.08  25.53  50.76  202.0   1592   203566
	byte    1.98   3.59   6.80  13.10  25.99  124.1  842.9   125893

1.9x at 4 KB. So cleanRun was rewritten as a byte loop, which by the
microbenchmark should have been worth about 3% of the encode.

**It was worth nothing.** Interleaved A/B, minimum of nine runs each:

	                            byte loop     SWAR
	DispatchFloor/compiled         51016     50506
	DispatchFloor/handwritten      41151     41305
	GateMarshal/Marshal           150166    148276
	GateMarshal/MarshalTo         140746    136247
	GateStream/Encode             214392    211665

Every difference is inside the 8.3% code-layout noise floor and the sign is not
even consistent. Reverted.

**Why the microbenchmark lied.** It fed cleanRun whole corpus strings, and
cleanRun never sees those. appendQuoted takes clean strings on its own fast path
through plainASCIIRun; cleanRun is reached only from appendBody, on the tail
AFTER an escape has been found, and those tails are short. Below eight bytes the
SWAR loop runs zero word iterations and falls straight into the same byte loop
it was being compared against -- so on its real inputs the two are the same
code, and the profile's 8.9% is time neither version can avoid.

**The rule.** A microbenchmark measures the function on the inputs you chose,
and the profile tells you the function is hot, and neither tells you the two are
the same inputs. Before optimising a hot function, find out what it is actually
called with. Here that was one question -- who calls cleanRun and with what --
and the answer invalidated both the microbenchmark and the plan built on it.

Keeping the SWAR was then the right call for the same reason the change was
wrong: there is no evidence to move shipped code either way, and "the simpler
one also wins a benchmark that does not apply" is not evidence.
