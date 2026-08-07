package main

import (
	"bufio"
	"bytes"
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
)

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
		for i := 0; i+1 < len(fields); i++ {
			if fields[i+1] != "ns/op" {
				continue
			}
			v, perr := strconv.ParseFloat(fields[i], 64)
			if perr != nil {
				continue
			}
			m := 0.0
			if i+3 < len(fields) && fields[i+3] == "MB/s" {
				if f, perr := strconv.ParseFloat(fields[i+2], 64); perr == nil {
					m = f
				}
			}
			if !found || v < best {
				best, mbps, found = v, m, true
			}
			break
		}
	}
	if !found {
		return 0, 0, fmt.Errorf("no ns/op result for this benchmark")
	}
	return best, mbps, nil
}

// discoverNames runs the benchmarks and returns every benchmark name,
// GOMAXPROCS suffix stripped, deduplicated and sorted.
//
// Discovery is done by running with -test.benchtime 1x, not by -test.list:
// the names that matter here are the sub-benchmarks (BenchmarkFoo/bar), which
// only exist once the parent benchmark runs. -test.list sees only the parent.
func discoverNames(out []byte) ([]string, error) {
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 || !strings.HasPrefix(fields[0], "Benchmark") {
			continue
		}
		seen[stripSuffix(fields[0])] = true
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("discovery run produced no Benchmark lines")
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
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
