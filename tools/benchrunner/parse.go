package main

import (
	"bufio"
	"bytes"
	"fmt"
	"math/rand/v2"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// skippedEntry records a benchmark that discovery or the -bench filter left
// out of the run, and why.
type skippedEntry struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

const (
	reasonExceeds  = "exceeds -max-discover-sec at 1x"
	reasonFiltered = "filtered by -bench"
	reasonNoResult = "no result at 1x"
)

// listBenchmarks parses `-test.list '^Benchmark'` output: one plain top-level
// benchmark name per line, no execution involved.
func listBenchmarks(out []byte) ([]string, error) {
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "Benchmark") {
			seen[line] = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// parseTimes returns every benchmark's ns/op from one per-parent 1x discovery
// run, keyed by the GOMAXPROCS-suffix-stripped name.
func parseTimes(out []byte) (map[string]float64, error) {
	times := map[string]float64{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 || !strings.HasPrefix(fields[0], "Benchmark") {
			continue
		}
		ns, ok := firstNs(fields)
		if !ok {
			continue
		}
		times[stripSuffix(fields[0])] = ns
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return times, nil
}

// planParent decides which of a parent's discovered sub-benchmarks run.
//
// A parent whose discovery process was killed by the timeout is skipped as a
// whole — its slow rows never even got measured, so the sub-benchmark names
// are unknown and one skipped entry with the parent's name is all there is to
// record. A parent that completed but has sub-benchmarks whose measured 1x
// time exceeds the threshold keeps its fast sub-benchmarks and skips the slow
// ones individually. With includeSlow nothing is skipped; the caller is
// expected to have run discovery without a timeout.
func planParent(parent string, times map[string]float64, timedOut bool, maxSec int, includeSlow bool) (names []string, skipped []skippedEntry) {
	if timedOut && !includeSlow {
		return nil, []skippedEntry{{parent, reasonExceeds}}
	}
	if len(times) == 0 {
		return nil, []skippedEntry{{parent, reasonNoResult}}
	}
	for _, n := range sortedKeys(times) {
		if !includeSlow && maxSec > 0 && times[n] > float64(maxSec)*1e9 {
			skipped = append(skipped, skippedEntry{
				n, fmt.Sprintf("%s (measured %.1fs)", reasonExceeds, times[n]/1e9),
			})
			continue
		}
		names = append(names, n)
	}
	return names, skipped
}

// filterBench splits discovered names into those matching the -bench regex
// and those left out of the run set.
func filterBench(names []string, re *regexp.Regexp) (kept []string, skipped []skippedEntry) {
	for _, n := range names {
		if re.MatchString(n) {
			kept = append(kept, n)
			continue
		}
		skipped = append(skipped, skippedEntry{n, reasonFiltered})
	}
	return kept, skipped
}

func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// resultLines returns the result lines for one benchmark from `go test -bench`
// output. A result line starts with the benchmark name and a -P GOMAXPROCS
// suffix; the suffix is stripped and the name compared. Lines for other
// benchmarks, the goos/goarch/ok headers and --- FAIL blocks never match.
func resultLines(name string, out []byte) []string {
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 || !strings.HasPrefix(fields[0], "Benchmark") {
			continue
		}
		if stripSuffix(fields[0]) == name {
			lines = append(lines, sc.Text())
		}
	}
	return lines
}

// minSample returns the minimum ns/op over the given result lines and the
// MB/s value of that same line.
//
// The minimum is the estimator the gate uses: benchmark interference is
// one-sided — a frequency drop, a migration or a noisy neighbour can only make
// a run slower — so the minimum is the maximum-likelihood estimate of the true
// cost. The MB/s column exists only when the benchmark called SetBytes; lines
// without it contribute MB/s 0.
func minSample(lines []string) (ns, mbps float64, err error) {
	best := 0.0
	found := false
	for _, line := range lines {
		fields := strings.Fields(line)
		v, ok := firstNs(fields)
		if !ok {
			continue
		}
		// MB/s comes right after ns/op when the benchmark called SetBytes.
		m := 0.0
		for i := 0; i+1 < len(fields); i++ {
			if fields[i+1] == "ns/op" && i+3 < len(fields) && fields[i+3] == "MB/s" {
				if f, perr := strconv.ParseFloat(fields[i+2], 64); perr == nil {
					m = f
				}
				break
			}
		}
		if !found || v < best {
			best, mbps, found = v, m, true
		}
	}
	if !found {
		return 0, 0, fmt.Errorf("no ns/op result for this benchmark")
	}
	return best, mbps, nil
}

// firstNs returns the value preceding the first ns/op token in a result
// line's fields.
func firstNs(fields []string) (float64, bool) {
	for i := 0; i+1 < len(fields); i++ {
		if fields[i+1] != "ns/op" {
			continue
		}
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

// discoverNames returns every benchmark name in `go test -bench` output,
// GOMAXPROCS suffix stripped, deduplicated and sorted.
//
// This was the discovery mechanism before per-parent discovery existed. It
// still serves as the fallback for binaries whose -test.list cannot see
// benchmarks — a whole binary at -test.benchtime 1x in one process, slow
// rows included, which is exactly why it is only a fallback.
func discoverNames(out []byte) ([]string, error) {
	times, err := parseTimes(out)
	if err != nil {
		return nil, err
	}
	if len(times) == 0 {
		return nil, fmt.Errorf("discovery run produced no Benchmark lines")
	}
	return sortedKeys(times), nil
}

// stripSuffix removes the trailing -<GOMAXPROCS> from a benchmark name, the
// same way benchcheck does: a "-" followed by digits at the end of the name.
func stripSuffix(name string) string {
	if i := strings.LastIndex(name, "-"); i > 0 {
		if _, err := strconv.Atoi(name[i+1:]); err == nil {
			return name[:i]
		}
	}
	return name
}

// shuffleNames shuffles names in place with a PCG generator seeded from seed,
// so the same seed always produces the same order. With seed 0 the caller is
// expected to have substituted a time-derived seed.
func shuffleNames(names []string, seed uint64) {
	r := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
	r.Shuffle(len(names), func(i, j int) {
		names[i], names[j] = names[j], names[i]
	})
}
