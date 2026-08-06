# Shuffled Per-Process Benchmarks and README Charts — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Defend the benchmark numbers against order and process-state effects (shuffled order, one process per benchmark), and render the comparison against rival libraries as charts in the README and docs.

**Architecture:** A `tools/benchrunner` that spawns the comparison harness (`bench/`, a separate Go module) once per benchmark in seeded-random order and emits a JSON snapshot; a `tools/benchchart` that renders ratio-to-us and raw-MB/s SVG charts from that JSON via gonum/plot (dependency confined to `tools/go.mod`); `-shuffle=on` on the existing gate Makefile targets in both simdjson and GO_SIMD.

**Tech Stack:** Go (go 1.26 in tools), gonum.org/v1/plot (tools module only), the existing `go test -bench` output format, `tools/benchcheck`'s parse conventions.

**Design doc:** `docs/plans/2026-08-06-shuffled-bench-and-charts-design.md` (committed, `35d6f84`).

---

### Task 1: Shuffle the gate order

**Files:**
- Modify: `Makefile` (simdjson) — `bench-run` (~line 69), `bench-agree` (~line 88)
- Modify: `/home/sebishogun/Work/Development/GO_SIMD/Makefile` — `bench-run` (~line 383)

**Step 1: Add `-shuffle=on` to the simdjson gate targets**

In simdjson `Makefile`, change both `$(GO) test -run '^$$' -bench 'BenchmarkGate' -count $(BENCH_COUNT) .` invocations (in `bench-run` and `bench-agree`) to include `-shuffle=on` after `-count $(BENCH_COUNT)`.

**Step 2: Same for GO_SIMD**

In `/home/sebishogun/Work/Development/GO_SIMD/Makefile` `bench-run` (line ~383, `$(BENCH_PIN) $(GO) test -run '^$$' -bench . -count $(BENCH_COUNT) $(PKG)`), add `-shuffle=on` before `$(PKG)`.

**Step 3: Verify benchcheck is order-independent**

Run: `make bench-check` in simdjson
Expected: PASS (same baseline; benchcheck parses by name, order does not matter). If the machine is busy and it fails on the 8% threshold, note it and re-run on an idle machine — do not change the threshold.

**Step 4: Commit**

```bash
git -C /home/sebishogun/Work/Development/simdjson add Makefile && git -C /home/sebishogun/Work/Development/simdjson commit -m "bench: shuffle the gate's benchmark order per pass"
git -C /home/sebishogun/Work/Development/GO_SIMD add Makefile && git -C /home/sebishogun/Work/Development/GO_SIMD commit -m "bench: shuffle the gate's benchmark order per pass"
```

---

### Task 2: `tools/benchrunner` — one process per benchmark, shuffled

**Files:**
- Create: `tools/benchrunner/main.go`
- Create: `tools/benchrunner/parse.go`
- Create: `tools/benchrunner/parse_test.go`
- Test: `tools/benchrunner/parse_test.go`

The tool: flags `-bench-bin` (path to compiled bench test binary), `-count` (default 8), `-shuffle-seed` (default 0 = time-based), `-out` (JSON path). It lists benchmark names via `go test -list .` on the binary (or `-bench . -benchtime 1x` dry run — prefer `-list`, it exists since Go 1.20: `go test -list 'Benchmark' -run '^$'` prints benchmark names without running), shuffles them, and for each spawns `-run '^$' -bench '^<name>$' -count N`, parses the min ns/op, and writes JSON:

```json
{
  "machine": "hostname",
  "goVersion": "go1.26.2",
  "tier": "avx512",
  "date": "2026-08-06",
  "benches": [{"name": "BenchmarkUnmarshalCitmStruct/sonic", "nsMin": 970000}]
}
```

`tier` comes from running `go run github.com/sebishogun/simdjson ./cmd/simdinfo`-equivalent — simplest: `go run` a tiny inline program importing the library and printing `simd.Tier()`. If that is awkward from the tools module (it would need simdjson as a dependency), fall back to recording `runtime.GOARCH` and a `tier: unknown` field and let benchchart take the tier as a flag. Prefer the direct import: add `github.com/sebishogun/simdjson` as a tools-module dependency for the tier probe only.

**Step 1: Write the failing parser test**

```go
// parse_test.go
func TestParseMin(t *testing.T) {
	out := `BenchmarkUnmarshalCitmStruct/sonic-32    2145    970000 ns/op   ...
BenchmarkUnmarshalCitmStruct/sonic-32    2146    985000 ns/op
`
	got := minNS(out, "BenchmarkUnmarshalCitmStruct/sonic")
	if got != 970000 {
		t.Fatalf("min = %d, want 970000", got)
	}
}
```

Test that a name missing from output returns an error, and that the shuffle is seeded (same seed → same order).

**Step 2: Run to verify it fails**

Run: `go test ./tools/benchrunner/ -run TestParseMin -v`
Expected: FAIL (no `minNS` function).

**Step 3: Implement**

`parse.go`: reuse the same line convention as `tools/benchcheck/load.go` (read it first — match its field splitting and its minimum-of-samples logic, but return only the min). `main.go`: seed handling (`rand.New(rand.NewSource(seed))` — math/rand/v2), per-benchmark `exec.Command`, parse, aggregate, JSON encode.

**Step 4: Run tests to verify they pass**

Run: `go test ./tools/benchrunner/...`
Expected: PASS. Also `gofmt -l .` empty and `go vet ./tools/benchrunner/...` clean.

**Step 5: Commit**

```bash
git add tools/benchrunner && git commit -m "tools: benchrunner, one process per benchmark in shuffled order"
```

---

### Task 3: Wire `bench-suite` into the Makefile

**Files:**
- Modify: `Makefile` (simdjson) — near `bench-vs` (~line 144)

**Step 1: Add the target**

```make
BENCH_BIN    ?= /tmp/simdjson-bench.test
BENCH_SNAP   ?= docs/bench/compare-$(shell date +%F).json

bench-suite: ## Build the comparison harness, run one process per benchmark
	cd bench && $(GO) test -c -o $(BENCH_BIN) .
	cd tools && $(GO) run ./benchrunner -bench-bin $(BENCH_BIN) -count $(VS_COUNT) -out ../$(BENCH_SNAP)
```

(`VS_COUNT` already exists in the Makefile — check its value, default 8.)

**Step 2: Verify**

Run: `make bench-suite`
Expected: builds, runs every benchmark once per process, writes `docs/bench/compare-<date>.json`. The full suite takes several minutes; that is expected.

**Step 3: Commit**

```bash
git add Makefile && git commit -m "bench: bench-suite target runs the harness per-process"
```

---

### Task 4: `tools/benchchart` — ratio and throughput charts

**Files:**
- Create: `tools/benchchart/main.go`
- Create: `tools/benchchart/map.go` (name → {family, corpus, library} table)
- Create: `tools/benchchart/map_test.go`
- Modify: `tools/go.mod` (add `gonum.org/v1/plot`)

**Step 1: Add the dependency**

Run: `cd tools && go get gonum.org/v1/plot@latest && go mod tidy`
Expected: `tools/go.mod` gains gonum/plot; simdjson's own go.mod is untouched.

**Step 2: Write the failing mapping test**

The naming convention (verified in `bench/decode_rows_test.go`): sub-bench suffixes are `ours`, `sonic`, `goccy`, `stdlib`, `jsoniter`, `segmentio` (others: `fastjson`, `minio`, `gjson`, `sjson`, `easyjson`, `jsonv2`). `ours` maps to library `this`.

```go
func TestClassify(t *testing.T) {
	got, ok := classify("BenchmarkUnmarshalCitmStruct/sonic")
	if !ok || got.Family != "unmarshal" || got.Corpus != "citm" || got.Library != "sonic" {
		t.Fatalf("classify = %v, %v", got, ok)
	}
}
```

Table covers at minimum the five charted families — parse (`BenchmarkCorpus`, `BenchmarkScale`), validate (`BenchmarkShapeValid`), unmarshal (`BenchmarkUnmarshal*`, `BenchmarkSMLUnmarshal`, `BenchmarkColdStartUnmarshal`), marshal (`BenchmarkMarshal*`, `BenchmarkSMLMarshal`), streaming (`BenchmarkStream*`, `BenchmarkReadme`) — everything else is `family: "other"` (still carried in JSON, not charted).

**Step 3: Run to verify it fails**

Run: `go test ./tools/benchchart/ -run TestClassify -v`
Expected: FAIL.

**Step 4: Implement the charts**

`main.go`: read the JSON, group by family; for each family draw:
- ratio chart: x = library (ordered by ratio ascending, `this` pinned first), y = log-scaled ratio `other.nsMin / ours.nsMin`, reference line at 1.0, labels with the ratio;
- throughput chart: y = MB/s. `SetBytes` in the harness makes MB/s computable: `bytes = nsMin * MB/s / 1e9` — instead read `B/op` from the output? The runner only records nsMin; extend the runner JSON to also capture `bytes` (from the `MB/s` column, recompute `bytes = ns * MBps / 1e3`) or capture the `MB/s` value directly. Simplest: record `mbps` (the value in the MB/s column of the min sample's line) alongside `nsMin`.

Render with `plot.New`, `plotter.BarChart` (or `plotter.Lines` for the log ratio), `vg.SVGWriter` into `docs/figures/<family>-ratio.svg` and `docs/figures/<family>-throughput.svg`. Footnote: machine, goVersion, tier, date from the JSON.

**Step 5: Run tests, generate sample charts from the Task 3 snapshot**

Run: `go test ./tools/benchchart/... && go run ./tools/benchchart -in ../docs/bench/compare-*.json -out ../docs/figures`
Expected: SVG files appear; open one to eyeball (read the file, check it has `<svg` and text labels).

**Step 6: Commit**

```bash
git add tools/benchchart tools/go.mod tools/go.sum docs/figures
git commit -m "tools: benchchart renders ratio and throughput charts from the snapshot"
```

---

### Task 5: Makefile `bench-charts` / `bench-all`

**Files:**
- Modify: `Makefile` (simdjson)

**Step 1: Add targets**

```make
bench-charts: ## Render the figures from the latest snapshot
	cd tools && $(GO) run ./benchchart -in ../docs/bench/compare-*.json -out ../docs/figures

bench-all: bench-suite bench-charts ## Full measurement + render
```

**Step 2: Verify and commit**

Run: `make bench-charts` — regenerates figures, no diff if inputs unchanged (deterministic).
Commit: `git add Makefile && git commit -m "bench: bench-charts and bench-all targets"`.

---

### Task 6: Real run, snapshot and figures committed

**Step 1: Quiet machine, full run**

Wait for `uptime` load < 1. Run: `make bench-all`
Expected: fresh `docs/bench/compare-<today>.json` + figures.

**Step 2: Check the charts are honest**

Read the generated ratio SVGs: rows where we lose (sonic marshal ~0.5, goccy citm, sjson edit, gjson single-field) must show bars below 1.0. If a family's chart is missing a library because a benchmark name fell into `other`, fix the table.

**Step 3: Commit**

```bash
git add docs/bench docs/figures
git commit -m "docs: benchmark snapshot and figures, measured per-process in shuffled order"
```

---

### Task 7: README and competition.md

**Files:**
- Modify: `README.md` — Performance section (starts ~line 137)
- Modify: `docs/competition.md` — after the table (~line 34)

**Step 1: README**

After the existing tables, add the five ratio charts with relative paths (`docs/figures/parse-ratio.svg`, ...). Add a "How these were measured" paragraph: one process per benchmark, randomized order, minimum of `VS_COUNT` samples, idle machine, tier named from the snapshot (avx512), date. Keep the existing tables — charts show shape, tables carry the exact numbers.

**Step 2: competition.md**

Add the raw-throughput figures for the record, with a sentence that the full list of measured libraries is in the snapshot JSON.

**Step 3: Verify and commit**

Run: `make verify` (or at least `go build ./... && go test ./... -count=1` in the root module and `cd bench && go test ./...` for the harness module).
Commit: `git add README.md docs/competition.md && git commit -m "docs: charts in the README and competition record, with method"`.

---

### Task 8: Cross-check the record

**Step 1: Confirm the methodology claims**

- `docs/plans/2026-08-06-shuffled-bench-and-charts-design.md` committed (`35d6f84`).
- Every claim in the README paragraph matches what the runner actually did (verify by reading `tools/benchrunner/main.go`).

**Step 2: GO_SIMD README, if it claims benchmark numbers**

Check `/home/sebishogun/Work/Development/GO_SIMD/README.md` for a performance section; if it cites numbers, add a one-line pointer to the simdjson method paragraph rather than duplicating charts (GO_SIMD benches kernels, not libraries).

**Step 3: Final verification and commit**

Run: `go vet ./tools/...` and `make bench-check` (gate still green with shuffle).
Commit any residue: `git add -A && git commit -m "docs: record the benchmarking method"`.
