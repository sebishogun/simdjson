# Shuffled per-process benchmarking and README charts

Date: 2026-08-06. Status: approved.

## Why

Two gaps in how this repository produces performance numbers, both raised from
the numbers themselves:

1. **Benchmark order and process state are not defended.** The gate and the
   comparison harness run every benchmark in one process in source order, so
   warm caches, branch predictors and thermal state carry over between
   benchmarks. Nothing randomizes order, and nothing isolates one benchmark
   from the next.
2. **The README and docs show tables, not shapes.** Sonic renders charts of
   its benchmark rows; this repository has the data but no figures. The
   transparency requirement: every library actually benchmarked must appear in
   the figures, including the rows where another library wins.

## Design

### The runner — `tools/benchrunner`

Runs the comparison harness (`bench/`, a separate module) with one process per
benchmark, in randomized order:

- `cd bench && go test -c -o /tmp/bench.test .` once; then spawn the binary
  once per benchmark with `-run '^$' -bench '^Name$' -count 8`.
- Order is shuffled with a seeded PRNG; `-shuffle-seed N` reproduces a run.
- Parse the ns/op lines; take the minimum per benchmark (the estimator the
  gate already uses — layout noise is one-sided, so the minimum converges to
  the true code speed).
- Emit JSON: `{machine, goVersion, tier, date, benches: [{name, nsMin}]}`.
  `tier` is `simd.Tier()` from the library (avx512/avx2/...), because the
  numbers are only meaningful relative to the tier they were measured on.
- Library and corpus attribution come from the benchmark names, which the
  harness already encodes as sub-benchmarks (`BenchmarkUnmarshalCitmStruct/
  sonic`). The chart tool maps name → {operation, corpus, library} through a
  table with an "other" fallback.

### The gate — `-shuffle=on`

The CI gate gets the cheap half with no new machinery: `-shuffle=on` on the
`bench-run`, `bench-check` and `bench-agree` Makefile targets. `go test`'s
built-in shuffle randomizes top-level benchmark order within the single
process. `tools/benchcheck` parses by name and is order-independent, so the
baseline file stays valid unchanged. Same one-line addition to the GO_SIMD
Makefile gate targets for a consistent methodology claim.

### The chart tool — `tools/benchchart`

- Adds `gonum.org/v1/plot` to `tools/go.mod` only. The library's own module
  graph never grows; users of the library download nothing new.
- Reads the runner's JSON, renders SVG into `docs/figures/` (SVG diffable and
  GitHub-renderable).
- Chart 1 — **ratio-to-us**, one per operation family (parse, validate,
  unmarshal, marshal, streaming): x = library, y = time ÷ our time, log
  scale, reference line at 1.0. Rows we lose show as bars below 1.0 — the
  honest shape.
- Chart 2 — **raw throughput** in MB/s, grouped bars per corpus, for the
  record.
- Footnote on every chart: machine, tier, go version, date, taken from the
  JSON.

### Placement and workflow

- Committed snapshot `docs/bench/compare-<date>.json`, regenerable.
- `make bench-suite` — run the runner. `make bench-charts` — render charts.
  `make bench-all` — both.
- README Performance section: the five ratio charts plus a short "how these
  were measured" paragraph (per-process, shuffled, minima of 8, idle machine,
  tier named).
- `docs/competition.md`: the raw MB/s charts.

## Verification

- Runner: unit test against a fixture of `go test -bench` output.
- Gate: `make bench-check` passes with `-shuffle=on` (order independence is
  benchcheck's existing property).
- Charts: regenerated from a real run and committed; README renders them.
