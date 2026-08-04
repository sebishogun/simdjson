// Command benchcheck compares a benchmark run against a stored baseline and
// fails when something got slower.
//
// Every performance number in this repository was measured once by hand. That
// is how they were arrived at and it is also how they rot: a kernel rewrite
// that costs 30% looks exactly like one that costs nothing until somebody
// re-runs the benchmark and remembers what it used to say. This makes the
// remembering the machine's job.
//
//	go test -run '^$' -bench . -count 6 ./... > new.txt
//	go run ./tools/benchcheck -baseline testdata/bench/amd64.txt new.txt
//
// The comparison is deliberately crude — the median of each benchmark's
// samples, against a percentage threshold — rather than benchstat's
// distributional test. benchstat answers "is this difference real", which
// needs enough samples to be confident and reports "~" when it is not. A gate
// has to answer "should this merge", and for that a slow-but-certain 40%
// regression and a noisy-but-likely 40% regression deserve the same answer.
//
// Noise is handled by taking the minimum of each benchmark's samples rather
// than the median — see parse for why that is the right estimator and not
// merely the optimistic one — and then by a percentage threshold on top.
//
// The threshold alone was tried first and is not enough. It assumed a
// run-to-run spread of 1-3%, and at 6 to 15 ns/op the spread against a median
// exceeds 100%.
//
// The threshold is 8%, which is tight, and the reason it can be is that the
// minimum estimator and the gate's choice of benchmarks between them leave very
// little noise to absorb. Two full passes recorded back to back on an idle
// machine, sixteen samples each, disagreed by 0.1% to 1.9% — every benchmark in
// the gate is above 61 us, where the per-sample spread that makes a nanosecond
// benchmark unusable has averaged out. 25% was the first value here and it was
// wrong for the thing the gate exists to catch: the regression that prompted it
// was 10% on canada's parse, and it would have passed.
//
// The corollary is that this threshold belongs to this gate. Point benchcheck
// at a set of benchmarks that includes short ones and it will need a looser
// one, or per-benchmark thresholds — see wideThreshold, which is where the
// three benchmarks that need one are listed with what they measured.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// wideThreshold holds the benchmarks whose own run-to-run spread is wider than
// the default threshold, and what each one is allowed instead.
//
// Two full passes recorded back to back on an idle machine had eleven of the
// fourteen agreeing within 0.6%. These three disagreed by 4.3%, 6.2% and 8.2%.
// They are the three that go through reflect and allocate per value, which is
// the difference: their timing includes whichever garbage collections happened
// to land inside the measurement.
//
// Neither uniform answer is any good. Holding these to 8% fails the gate on
// noise, and a gate that cries wolf gets switched off. Holding all fourteen to
// 18% lets canada's parse quietly lose 10%, which is the exact regression the
// gate was built to catch.
//
// Each value is about twice the spread that was measured for that benchmark.
// If one of these is made to allocate less, re-measure and tighten it — a
// stale exemption is a hole.
var wideThreshold = map[string]float64{
	"BenchmarkGateStream/Encode": 18, // measured 8.2%
	"BenchmarkGateStream/Decode": 15, // measured 6.2%
	"BenchmarkGateUnmarshal":     12, // measured 4.3%
}

func main() {
	baseline := flag.String("baseline", "", "the stored benchmark output to compare against")
	threshold := flag.Float64("threshold", 8, "percent slower before a benchmark fails")
	update := flag.Bool("update", false, "overwrite the baseline with the new run instead of comparing")
	verbose := flag.Bool("v", false, "print every benchmark's delta, not only the ones that failed")
	// -agree asks a different question: not "did this change make something
	// slower" but "do two runs of the SAME code produce the same numbers". A
	// baseline is only worth recording if they do, and the ordinary comparison
	// cannot answer it -- it fires in one direction, so a run that came out
	// faster passes silently. Recording a baseline once, two runs disagreed by
	// 10.8% and 12.3% on the two allocation-heavy benchmarks and this tool
	// reported no regressions, because the second run was the faster one.
	agree := flag.Bool("agree", false,
		"compare two runs of the same code: fail on a difference in either direction")
	flag.Parse()

	// Whether -threshold was given rather than defaulted. The exemptions in
	// wideThreshold only loosen, and the comment on the switch below has always
	// said a tighter flag must beat them -- but the code only ever compared
	// magnitudes, so `-threshold 1` raised the limit back to 12 or 15 for the
	// three benchmarks in the table and reported nothing. Found by testing
	// -agree at a threshold under the exemptions and getting a pass on two runs
	// that differ by 9.7% and 10.9%.
	strictAsked := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "threshold" {
			strictAsked = true
		}
	})
	maxLoad := flag.Float64("maxload", 4, "refuse to run when the one-minute load average is above this")

	if err := requireQuiet(*maxLoad); err != nil {
		fmt.Fprintln(os.Stderr, "benchcheck:", err)
		os.Exit(2)
	}

	if *baseline == "" || flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: benchcheck -baseline FILE [-threshold PCT] [-update] NEW")
		os.Exit(2)
	}
	newPath := flag.Arg(0)

	if *update {
		data, err := os.ReadFile(newPath)
		if err != nil {
			fatal(err)
		}
		if err := os.WriteFile(*baseline, redactCPU(data), 0o644); err != nil {
			fatal(err)
		}
		fmt.Printf("baseline updated from %s\n", newPath)
		return
	}

	base, err := parse(*baseline)
	if err != nil {
		// The first run on a new architecture lands here, because baselines
		// are per-GOARCH and only the one they were recorded on exists. That
		// is a thing to do, not a thing that went wrong, so say which.
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr,
				"benchcheck: no baseline at %s.\n\n"+
					"Baselines are per-architecture and this machine has none yet, which is\n"+
					"expected the first time the benchmarks run on a new GOARCH. Record one\n"+
					"on an otherwise idle machine:\n\n"+
					"    make bench-update\n\n"+
					"Then `make bench-check` compares against it. Read\n"+
					"testdata/bench/README.md first — a baseline recorded on a busy or\n"+
					"thermally degraded machine is worse than no baseline, because every\n"+
					"later comparison inherits it.\n", *baseline)
			os.Exit(1)
		}
		fatal(err)
	}
	cur, err := parse(newPath)
	if err != nil {
		fatal(err)
	}

	var regressed, improved, missing, all []string
	names := make([]string, 0, len(base))
	for n := range base {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, n := range names {
		c, ok := cur[n]
		if !ok {
			// A benchmark in the baseline with no line in the run is a failure,
			// not a note. It is either a rename, which silently drops that
			// benchmark's history, or a skip — and a skipped benchmark is the
			// one way this tool can report success for a run that measured
			// nothing. Both deserve to stop the gate and be looked at.
			missing = append(missing, n)
			continue
		}
		b := base[n]
		if b == 0 {
			continue
		}
		delta := (c - b) / b * 100
		// The per-benchmark exemption only ever loosens. A flag tighter than an
		// entry in the table is a deliberate ask for a stricter run and has to
		// win, or -threshold 1 would silently do nothing for three of fourteen.
		lim := *threshold
		if w, ok := wideThreshold[n]; ok && w > lim && !strictAsked {
			lim = w
		}
		if *verbose {
			note := ""
			if lim != *threshold {
				note = fmt.Sprintf("  (limit %.0f%%)", lim)
			}
			all = append(all,
				fmt.Sprintf("  %-52s %9.2f -> %9.2f ns/op  %+6.1f%%%s", n, b, c, delta, note))
		}
		switch {
		case *agree && (delta > lim || delta < -lim):
			// Either direction is a disagreement, and per benchmark: an
			// aggregate pass hides one row differing by 12%.
			regressed = append(regressed,
				fmt.Sprintf("  %-52s %9.2f vs %9.2f ns/op  %+6.1f%%  (limit ±%.0f%%)", n, b, c, delta, lim))
		case *agree:
			// Nothing: in agree mode a smaller number is not good news.
		case delta > lim:
			regressed = append(regressed,
				fmt.Sprintf("  %-52s %9.2f -> %9.2f ns/op  %+6.1f%%  (limit %.0f%%)", n, b, c, delta, lim))
		case delta < -lim:
			improved = append(improved,
				fmt.Sprintf("  %-52s %9.2f -> %9.2f ns/op  %+6.1f%%", n, b, c, delta))
		}
	}

	what := "compared against"
	if *agree {
		what = "checked for agreement with"
	}
	fmt.Printf("%d benchmarks %s %s (threshold %.0f%%)\n",
		len(base), what, *baseline, *threshold)
	if len(all) > 0 {
		fmt.Printf("\n%s\n", strings.Join(all, "\n"))
	}
	if len(improved) > 0 {
		fmt.Printf("\n%d faster:\n%s\n", len(improved), strings.Join(improved, "\n"))
	}
	if len(missing) > 0 {
		fmt.Printf("\n%d MISSING from this run (renamed, removed, or skipped):\n  %s\n",
			len(missing), strings.Join(missing, "\n  "))
	}
	if len(regressed) > 0 {
		label := "SLOWER"
		if *agree {
			label = "DISAGREE between the two runs"
		}
		fmt.Printf("\n%d %s:\n%s\n", len(regressed), label, strings.Join(regressed, "\n"))
	}
	if len(regressed) > 0 || len(missing) > 0 {
		if len(regressed) > 0 && *agree {
			fmt.Fprintln(os.Stderr, "\nbenchcheck: the two runs disagree, so neither is a "+
				"baseline. Something was using the machine -- see docs/wrong.md entries 21 "+
				"and 23 -- or these benchmarks need more samples.")
		} else if len(regressed) > 0 {
			fmt.Fprintln(os.Stderr, "\nbenchcheck: regressions above the threshold; "+
				"re-run to rule out noise, then either fix them or update the baseline "+
				"with -update and say why in the commit.")
		}
		if len(missing) > 0 {
			fmt.Fprintln(os.Stderr, "\nbenchcheck: benchmarks in the baseline did not "+
				"run. If they were renamed, update the baseline; if they skipped, the "+
				"gate measured nothing and the run is not a pass.")
		}
		os.Exit(1)
	}
	fmt.Println("\nno regressions")
}

// parse reads `go test -bench` output and returns the minimum ns/op per
// benchmark.
//
// This was the median, on the reasoning that a gate wants the typical case
// rather than the luckiest one. That is wrong here, and the argument against
// it is not statistical taste but the shape of the noise: **benchmark
// interference is one-sided**. A frequency drop, a migration, an interrupt or
// a noisy neighbour can only ever make a run slower. Nothing makes a kernel
// finish in less time than it takes. So the distribution is the true cost plus
// a non-negative contaminant, and the minimum is the maximum-likelihood
// estimate of the true cost while the median is the true cost plus however
// much contamination reached the middle sample.
//
// That is not theoretical. Two consecutive runs of make bench-check on an idle
// machine, same commit and same binary, reported sixteen regressions and then
// five, with zero overlap — every one of them a transient that reached the
// median of a six-sample run. At 6 to 15 ns/op, which is where the small
// elementwise kernels live, a single scheduling event is larger than the
// measurement.
//
// The minimum removes exactly that contaminant and nothing else. It cannot
// hide a real regression, because a real regression raises every sample
// including the best one.
func parse(path string) (map[string]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	samples := map[string][]float64{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// A result line is: Name-P  iterations  value  ns/op  [more]
		if len(fields) < 4 || !strings.HasPrefix(fields[0], "Benchmark") {
			continue
		}
		var ns float64
		for i := 2; i+1 < len(fields); i++ {
			if fields[i+1] != "ns/op" {
				continue
			}
			v, err := strconv.ParseFloat(fields[i], 64)
			if err != nil {
				continue
			}
			ns = v
			break
		}
		if ns == 0 {
			continue
		}
		// Strip the -P suffix so a run on a machine with a different core
		// count still matches the baseline.
		name := fields[0]
		if i := strings.LastIndex(name, "-"); i > 0 {
			if _, err := strconv.Atoi(name[i+1:]); err == nil {
				name = name[:i]
			}
		}
		samples[name] = append(samples[name], ns)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(samples))
	for n, s := range samples {
		sort.Float64s(s)
		out[n] = s[0]
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no benchmark results found", path)
	}
	return out, nil
}

// redactCPU drops go test's cpu line from a baseline.
//
// The baseline is committed, and that line names the model of whatever machine
// recorded it. Nothing here reads it -- the parser takes only lines beginning
// with "Benchmark" -- so it is identifying detail with no purpose in a public
// repository. goos, goarch and pkg stay: those change what the numbers mean.
func redactCPU(b []byte) []byte {
	out := b[:0:0]
	for _, line := range bytes.SplitAfter(b, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("cpu:")) {
			continue
		}
		out = append(out, line...)
	}
	return out
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "benchcheck:", err)
	os.Exit(1)
}
