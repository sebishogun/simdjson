# The gate's baselines and the documents it measures them on

`make bench-check` runs the benchmarks in `bench_gate_test.go` and compares them
against the baseline for the machine's `GOARCH`. It exists because every
performance number in this repository was measured once by hand, and a change
that costs 10% looks exactly like one that costs nothing until somebody
re-measures. A 10% regression in canada's parse shipped and sat for four commits
before it was noticed by accident.

## The three documents

`corpus/` holds them gzipped: 676 KB for all three, decompressed once per test
binary. They are the standard set — the same three files simdjson, sonic,
minio/simdjson-go, fastjson and goccy all publish numbers on — which is what
makes a number from here comparable to a number from there.

| file | size | what it is | what it stresses |
|---|---|---|---|
| `twitter.json` | 632 KB | a Twitter search API response, Japanese and English | 18,099 strings, most of the bytes inside quotes, non-ASCII throughout |
| `citm_catalog.json` | 1.7 MB | a ticketing system's venue and event catalogue | deep nesting, many small objects, **71% whitespace** |
| `canada.json` | 2.3 MB | GeoJSON, the outline of Canada | almost entirely numbers — 111,126 coordinate pairs, nested arrays, no strings to speak of |

Between them they cover the three ways a JSON document can be shaped, which is
why a change can be 5% faster on one and 10% slower on another. All three are
originally from the simdjson project's test corpus and are widely mirrored;
`citm` and `canada` also appear in the nativejson-benchmark suite.

What they do *not* cover: small documents. `bench_gate_test.go` has no 64-byte
case, and that is where this library is furthest behind fastjson. If small-input
work starts, the gate needs a benchmark for it first — and a looser threshold,
see below.

## Recording a baseline

    make bench-update

Then say in the commit message *why* the baseline moved. A baseline that gets
updated without an explanation is a gate that has been switched off.

Record on an idle machine. `benchcheck` refuses above a one-minute load average
of 4 and that check is a floor, not a standard: a baseline recorded on a busy or
thermally throttled machine is worse than no baseline, because every later
comparison inherits it and the gate then reports success for real regressions.

Two full passes, back to back, and take the minimum across both. The baseline
committed here is 16 samples per benchmark for that reason. Two passes recorded
this way on an idle machine disagreed by 0.1% to 1.9%.

## Why the threshold is 8%

It was 25%, which is useless for the thing the gate was built to catch: the
canada regression was 10% and would have passed.

8% works here because of what the gate measures and how. The estimator is the
minimum of a benchmark's samples, not the median — a noisy neighbour can only
ever make a run slower, so the minimum is the closest thing to the machine's
real capability and not merely the optimistic reading. And every benchmark in
the gate is above 61 µs, where the per-sample spread that makes a nanosecond
benchmark unusable has already averaged out.

Three of the fourteen do not get 8%, and the reason is worth knowing before
adding a benchmark here. Across the two recorded passes, eleven agreed within
0.6% and these three did not:

| benchmark | measured spread | its limit |
|---|---|---|
| `GateStream/Encode` | 8.2% | 18% |
| `GateStream/Decode` | 6.2% | 15% |
| `GateUnmarshal` | 4.5% | 12% |

They are the three that go through reflect and allocate per value, so their
timing includes whichever garbage collections landed inside the measurement.
Each limit is about twice its measured spread; the table is `wideThreshold` in
`tools/benchcheck/main.go`. If one of them is made to allocate less, re-measure
and tighten it — a stale exemption is a hole in the gate.

Neither uniform answer works. 8% for all fourteen fails on Encode's own noise,
and a gate that cries wolf gets switched off. 18% for all fourteen lets canada's
parse quietly lose 10%.

### The floor under all of this is code layout, and it is about 8%

Adding dead code changes these numbers. Four builds of the same source, the only
difference being 0, 7, 14 or 21 unexported functions that nothing calls:

| build | `GateUnmarshal`, minimum of three |
|---|---|
| no padding | 501,741 |
| 7 dead functions | **491,382** |
| 14 dead functions | 504,024 |
| 21 dead functions | **532,253** |

**8.3%, from code that never runs.** Adding a function moves every symbol after
it, and a hot loop that fitted inside one 64-byte fetch line at one address
straddles two at another — `docs/wrong.md` entry 13 has the disassembly of a
case where that cost 14% with an identical instruction stream.

Three things follow. Any single benchmark number here is partly an accident of
where the linker put things. A regression inside that band is not evidence of
anything on its own. And the way to tell the difference is to count
instructions: if two builds retire the same number and one takes longer, no
change caused it.

	perf stat -e cycles,instructions,stalled-cycles-frontend ./old.test -test.bench X -test.benchtime 8000x
	perf stat -e cycles,instructions,stalled-cycles-frontend ./new.test -test.bench X -test.benchtime 8000x

This is why the thresholds above are what they are, and it is the reason to be
suspicious of a tighter one rather than pleased with it.

Adding a *short* benchmark breaks the assumption differently: at 6 to 15 ns/op
the spread against a median exceeds 100%. Such a benchmark needs its own entry
in that table, not a looser global threshold.

## Baselines are per-architecture

`amd64.txt` is the only one recorded. `arm64.txt` and the rest do not exist and
`benchcheck` says so plainly rather than passing vacuously, which is the same
reason a benchmark that skips is now a failure: a gate that reports success for
a run in which nothing was measured is worse than no gate.
