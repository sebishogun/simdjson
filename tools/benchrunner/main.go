// Command benchrunner measures every benchmark in a compiled test binary, one
// process per benchmark, in a shuffled order, and writes the minimum of each
// benchmark's samples as JSON.
//
// The comparison harness in bench/ is a separate module — its benchmarks pit
// this library against sonic, goccy, stdlib and the rest, and the runner must
// not pull those rivals into its own module graph — so the runner takes a
// prebuilt binary:
//
//	cd bench && go test -c -o /tmp/bench.test .
//	cd tools && go run ./benchrunner -bench-bin /tmp/bench.test \
//	    -count 8 -shuffle-seed 1 -out /tmp/bench-run.json
//
// One process per benchmark is what makes the measurement honest: a benchmark
// that allocates a lot forces the next benchmark in the same process to run
// against a warmed-up heap, and one goroutine that never stops — an encoder
// gone wrong, a stream never closed — would otherwise contaminate everything
// after it. The shuffled order is so that an unlucky neighbour, thermal drift
// or a stray background job does not systematically favour or hurt any one
// benchmark; -shuffle-seed makes the order reproducible.
//
// The recorded value is the minimum of the samples, not the median. Benchmark
// interference is one-sided — a frequency drop, a migration or a noisy
// neighbour can only make a run slower — so the minimum is the
// maximum-likelihood estimate of the true cost, the same estimator the
// benchcheck gate uses.
//
// # Discovery
//
// Discovery is per parent benchmark, never one global run: one slow row
// (BenchmarkScale/512MB/goccy-Valid takes ~56 minutes at 1x) used to hold the
// whole binary hostage. First -test.list '^Benchmark' (instant, no
// execution) names the top-level benchmarks; then each parent runs once with
// -test.benchtime 1x and its own -test.timeout of -max-discover-sec seconds.
// The runner enforces that timeout itself — go test's -test.timeout does not
// interrupt benchmarks, it is disarmed before they run — by killing the
// process with a context deadline. A parent killed by the deadline is skipped
// as a whole; a parent that completes is checked sub-benchmark by
// sub-benchmark, and any sub whose measured 1x time exceeds the threshold is
// skipped individually. Every skip lands in the JSON's "skipped" array with a
// reason, and -include-slow turns the skips off and discovery's timeout off —
// slow rows then take as long as they take.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sebishogun/simd"
)

type run struct {
	Machine   string         `json:"machine"`
	GoVersion string         `json:"goVersion"`
	Tier      string         `json:"tier"`
	Date      string         `json:"date"`
	Benches   []bench        `json:"benches"`
	Skipped   []skippedEntry `json:"skipped"`
}

// bench is one benchmark's result. Mbps is the MB/s of the same line whose
// ns/op was the minimum; it is 0 for benchmarks that never called SetBytes.
// A benchmark that produced no ns/op line at all — skipped, or failed before
// measuring — carries an error instead of numbers.
type bench struct {
	Name  string  `json:"name"`
	NsMin float64 `json:"nsMin,omitempty"`
	Mbps  float64 `json:"mbps,omitempty"`
	Error string  `json:"error,omitempty"`
}

func main() {
	bin := flag.String("bench-bin", "", "path to the compiled bench test binary")
	count := flag.Int("count", 8, "samples per benchmark (passed as -test.count)")
	benchtime := flag.String("benchtime", "1s", "wall-clock budget per sample "+
		"(passed as -test.benchtime). The suite's wall time is roughly "+
		"count × benchtime × benchmarks; the gate's own numbers use the "+
		"default 1s, but 250ms keeps the same eight samples at a quarter of "+
		"the wall time for the big harness")
	seedFlag := flag.Uint64("shuffle-seed", 0, "seed for the execution order; 0 = time-based")
	outPath := flag.String("out", "", "JSON output path")
	cwd := flag.String("cwd", "", "working directory the bench binary runs in; "+
		"the harness reads its corpora relative to it (bench/ for this repo). "+
		"Defaults to the runner's own directory, which is a footgun: launch "+
		"through the Makefile or pass -cwd explicitly")
	maxSec := flag.Int("max-discover-sec", 10,
		"per-parent discovery timeout in seconds; 0 = no timeout and nothing is skipped")
	includeSlow := flag.Bool("include-slow", false,
		"run discovery without the timeout and keep rows slower than -max-discover-sec")
	benchFilter := flag.String("bench", ".*", "only run discovered benchmarks whose name matches this regex")
	rowSec := flag.Int("max-row-sec", 300,
		"wall-clock cap per measured row; a row that outlives it is recorded "+
			"as an error and the run continues. 0 disables. The per-op slow-row "+
			"threshold cannot catch a row whose ops are fast but whose harness "+
			"interplay is not -- sonic's cold-start JIT ran one row 53 minutes.")
	flag.Parse()

	if *bin == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "usage: benchrunner -bench-bin BIN -out JSON "+
			"[-cwd DIR] [-count N] [-shuffle-seed S] [-max-discover-sec SEC] [-include-slow] [-bench RE]")
		os.Exit(2)
	}
	if *count < 1 {
		fmt.Fprintln(os.Stderr, "benchrunner: -count must be at least 1")
		os.Exit(2)
	}
	benchRe, err := regexp.Compile(*benchFilter)
	if err != nil {
		fatal(fmt.Errorf("bad -bench regex: %v", err))
	}

	// Phase 1: top-level benchmark names, no execution.
	// Every child process runs in -cwd: the harness reads its corpora via
	// relative paths, and a runner launched from the wrong directory quietly
	// discovers nothing. The make target passes -cwd ../bench.
	binCmd := func(ctx context.Context, args ...string) *exec.Cmd {
		c := exec.CommandContext(ctx, *bin, args...)
		c.Dir = *cwd
		return c
	}

	fmt.Printf("listing benchmarks in %s\n", *bin)
	lout, lerr := binCmd(context.Background(), "-test.list", "^Benchmark").CombinedOutput()
	if lerr != nil {
		fmt.Fprintf(os.Stderr, "benchrunner: -test.list: %v\n", lerr)
	}
	parents, err := listBenchmarks(lout)
	if err != nil {
		fatal(err)
	}
	if len(parents) == 0 {
		// Some binaries' -test.list cannot see benchmarks. Fall back to the
		// old one-process global 1x run, which is exactly the thing per-parent
		// discovery exists to avoid — slow rows included — so it is a last
		// resort and says so.
		fmt.Fprintln(os.Stderr, "benchrunner: -test.list found no benchmarks; "+
			"falling back to one global 1x discovery run")
		dout, derr := binCmd(context.Background(),
			"-test.run", "^$", "-test.bench", ".", "-test.benchtime", "1x").CombinedOutput()
		if derr != nil {
			fmt.Fprintf(os.Stderr, "benchrunner: discovery: %v\n", derr)
		}
		parents, err = discoverNames(dout)
		if err != nil {
			fatal(err)
		}
	}

	// Phase 2: each parent at 1x, bounded by -max-discover-sec.
	var skipped []skippedEntry
	times := map[string]float64{}
	for i, parent := range parents {
		fmt.Printf("discover [%d/%d] %s\n", i+1, len(parents), parent)
		args := []string{
			"-test.run", "^$",
			"-test.bench", "^" + regexp.QuoteMeta(parent) + "($|/)",
			"-test.benchtime", "1x",
		}
		ctx := context.Background()
		cancel := func() {}
		if *includeSlow {
			args = append(args, "-test.timeout", "0s")
		} else {
			args = append(args, "-test.timeout", strconv.Itoa(*maxSec)+"s")
			if *maxSec > 0 {
				ctx, cancel = context.WithTimeout(ctx, time.Duration(*maxSec)*time.Second)
			}
		}
		out, rerr := binCmd(ctx, args...).CombinedOutput()
		cancel()
		// The deadline is detected via ctx.Err, not errors.Is on rerr: when
		// the context kills the process, exec's Wait reports "signal:
		// killed" and prefers that over the context error, so
		// errors.Is(rerr, context.DeadlineExceeded) is false. Checking the
		// context itself must also survive cancel() above, which turns an
		// unfired context into context.Canceled.
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
		if timedOut {
			fmt.Fprintf(os.Stderr, "benchrunner: %s: discovery exceeded %ds\n", parent, *maxSec)
		}
		if rerr != nil && len(out) == 0 && !timedOut {
			skipped = append(skipped, skippedEntry{parent, "discovery process failed: " + rerr.Error()})
			continue
		}
		pt, perr := parseTimes(out)
		if perr != nil {
			fatal(perr)
		}
		names, skip := planParent(parent, pt, timedOut, *maxSec, *includeSlow)
		for _, n := range names {
			times[n] = pt[n]
		}
		skipped = append(skipped, skip...)
	}

	// The -bench filter decides which discovered names actually run.
	toRun, filtered := filterBench(sortedKeys(times), benchRe)
	skipped = append(skipped, filtered...)
	sort.Slice(skipped, func(i, j int) bool {
		return skipped[i].Name < skipped[j].Name
	})

	seed := *seedFlag
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	shuffleNames(toRun, seed)
	fmt.Printf("%d benchmarks in the run set; order seed %d (reproduce with -shuffle-seed %d); %d skipped\n",
		len(toRun), seed, seed, len(skipped))

	benches := make([]bench, 0, len(toRun))
	for i, name := range toRun {
		fmt.Printf("[%d/%d] %s\n", i+1, len(toRun), name)
		rctx := context.Background()
		rcancel := func() {}
		if *rowSec > 0 {
			rctx, rcancel = context.WithTimeout(rctx, time.Duration(*rowSec)*time.Second)
		}
		out, rerr := binCmd(rctx,
			"-test.run", "^$",
			"-test.bench", "^"+regexp.QuoteMeta(name)+"$",
			"-test.count", strconv.Itoa(*count),
			"-test.benchtime", *benchtime,
			"-test.shuffle", "on").CombinedOutput()
		rowTimedOut := errors.Is(rctx.Err(), context.DeadlineExceeded)
		rcancel()
		if rowTimedOut {
			// Partial samples that made it out before the cap still count:
			// the minimum of what was measured is a real minimum. Only a row
			// with no complete sample at all becomes an error below.
			fmt.Fprintf(os.Stderr, "benchrunner: %s: row exceeded %ds, using what it printed\n", name, *rowSec)
		} else if rerr != nil {
			fmt.Fprintf(os.Stderr, "benchrunner: %s: %v\n", name, rerr)
		}
		ns, mbps, perr := minSample(resultLines(name, out))
		if perr != nil {
			// A skipped benchmark (or one that failed before measuring) writes
			// no result line. It is recorded as an error, not a number, and
			// the run continues: this is the one failure mode that is
			// legitimate — the corpus missing, a rival library skipping its
			// own benchmark — and a missing line must not masquerade as a
			// zero ns/op in the JSON.
			fmt.Fprintf(os.Stderr, "benchrunner: %s: no ns/op result: %v\n", name, perr)
			benches = append(benches, bench{Name: name, Error: "no ns/op result"})
			continue
		}
		benches = append(benches, bench{Name: name, NsMin: ns, Mbps: mbps})
	}

	r := run{
		Machine:   hostname(),
		GoVersion: goVersion(),
		Tier:      simd.Tier(),
		Date:      time.Now().Format("2006-01-02"),
		Benches:   benches,
		Skipped:   skipped,
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		fatal(err)
	}
	data = append(data, '\n')
	if dir := filepath.Dir(*outPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fatal(err)
		}
	}
	if err := os.WriteFile(*outPath, data, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %s: %d benchmarks, %d without a result, %d skipped\n",
		*outPath, len(benches), len(benches)-countResults(benches), len(skipped))
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

// goVersion is `go version`'s output, so a later gate can tell which toolchain
// a run came from.
func goVersion() string {
	if out, err := exec.Command("go", "version").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return runtime.Version()
}

func countResults(b []bench) int {
	n := 0
	for _, x := range b {
		if x.Error == "" {
			n++
		}
	}
	return n
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "benchrunner:", err)
	os.Exit(1)
}
