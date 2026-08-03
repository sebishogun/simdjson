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
