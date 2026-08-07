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
// Discovery runs the binary with -test.benchtime 1x and reads every
// Benchmark...-N line. This is a run, not -test.list, because the meaningful
// units are sub-benchmarks (BenchmarkFoo/bar), which only come into existence
// when the parent benchmark runs; -test.list sees only the parent.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sebishogun/simd"
)

type run struct {
	Machine   string  `json:"machine"`
	GoVersion string  `json:"goVersion"`
	Tier      string  `json:"tier"`
	Date      string  `json:"date"`
	Benches   []bench `json:"benches"`
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
	seedFlag := flag.Uint64("shuffle-seed", 0, "seed for the execution order; 0 = time-based")
	outPath := flag.String("out", "", "JSON output path")
	flag.Parse()

	if *bin == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "usage: benchrunner -bench-bin BIN -out JSON [-count N] [-shuffle-seed S]")
		os.Exit(2)
	}
	if *count < 1 {
		fmt.Fprintln(os.Stderr, "benchrunner: -count must be at least 1")
		os.Exit(2)
	}

	fmt.Printf("discovering benchmarks in %s\n", *bin)
	dout, derr := exec.Command(*bin,
		"-test.run", "^$", "-test.bench", ".", "-test.benchtime", "1x").CombinedOutput()
	if derr != nil {
		fmt.Fprintf(os.Stderr, "benchrunner: discovery: %v\n", derr)
	}
	names, err := discoverNames(dout)
	if err != nil {
		fatal(err)
	}

	seed := *seedFlag
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	shuffleNames(names, seed)
	fmt.Printf("%d benchmarks; order seed %d (reproduce with -shuffle-seed %d)\n",
		len(names), seed, seed)

	benches := make([]bench, 0, len(names))
	for i, name := range names {
		fmt.Printf("[%d/%d] %s\n", i+1, len(names), name)
		out, rerr := exec.Command(*bin,
			"-test.run", "^$",
			"-test.bench", "^"+regexp.QuoteMeta(name)+"$",
			"-test.count", strconv.Itoa(*count),
			"-test.shuffle", "on").CombinedOutput()
		if rerr != nil {
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
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(*outPath, data, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %s: %d benchmarks, %d without a result\n",
		*outPath, len(benches), len(benches)-countResults(benches))
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
