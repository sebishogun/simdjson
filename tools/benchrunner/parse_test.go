package main

import (
	"regexp"
	"strings"
	"testing"
)

// benchOutput is real `go test -bench` output in shape: result lines are
// Name-P iterations value ns/op [MB/s], and lines without a SetBytes call have
// no MB/s column at all.
const benchOutput = `goos: linux
goarch: amd64
pkg: github.com/sebishogun/simdjson/bench
cpu: AMD RYZEN AI MAX+ 395 w/ Radeon 8060S
BenchmarkUnmarshalCitmStruct/sonic-32         1   1050000 ns/op   1700.1 MB/s
BenchmarkUnmarshalCitmStruct/sonic-32         2    970000 ns/op   1839.4 MB/s
BenchmarkUnmarshalCitmStruct/sonic-32         3   1010000 ns/op   1766.3 MB/s
BenchmarkAccessShape/early/gjson-32           1      8436 ns/op
BenchmarkGetField/n=1000/ours-32              1     62839 ns/op   1813.36 MB/s
ok  	github.com/sebishogun/simdjson/bench	0.412s
`

func TestParseMin(t *testing.T) {
	lines := resultLines("BenchmarkUnmarshalCitmStruct/sonic", []byte(benchOutput))
	if len(lines) != 3 {
		t.Fatalf("resultLines: got %d lines, want 3", len(lines))
	}
	ns, mbps, err := minSample(lines)
	if err != nil {
		t.Fatal(err)
	}
	if ns != 970000 {
		t.Errorf("ns = %v, want 970000 (the minimum sample)", ns)
	}
	if mbps != 1839.4 {
		t.Errorf("mbps = %v, want 1839.4 (the MB/s of the minimum's own line)", mbps)
	}
}

func TestParseNoBytes(t *testing.T) {
	lines := resultLines("BenchmarkAccessShape/early/gjson", []byte(benchOutput))
	ns, mbps, err := minSample(lines)
	if err != nil {
		t.Fatal(err)
	}
	if ns != 8436 {
		t.Errorf("ns = %v, want 8436", ns)
	}
	if mbps != 0 {
		t.Errorf("mbps = %v, want 0 (no MB/s column on this line)", mbps)
	}
}

func TestParseMissing(t *testing.T) {
	lines := resultLines("BenchmarkNeverRan", []byte(benchOutput))
	if len(lines) != 0 {
		t.Fatalf("resultLines: got %d lines, want 0", len(lines))
	}
	if _, _, err := minSample(lines); err == nil {
		t.Fatal("minSample of no lines succeeded, want an error")
	}
}

// TestDiscoverNames checks that every Benchmark...-N line is collected, the
// GOMAXPROCS suffix is stripped, duplicates collapse, and the result is sorted.
func TestDiscoverNames(t *testing.T) {
	out := `goos: linux
BenchmarkGetField/n=1000/gjson-32         1   24225 ns/op   4703.82 MB/s
BenchmarkGetField/n=1000/gjson-32         1   24000 ns/op   4700.10 MB/s
BenchmarkUnmarshalCitmStruct/sonic-32     1  1460924 ns/op    78.00 MB/s
BenchmarkUnmarshalCitmStruct/sonic-4      1  1500000 ns/op    76.10 MB/s
BenchmarkBaz-16                           1     8436 ns/op
ok  	github.com/sebishogun/simdjson/bench	1.0s
`
	names, err := discoverNames([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"BenchmarkBaz",
		"BenchmarkGetField/n=1000/gjson",
		"BenchmarkUnmarshalCitmStruct/sonic",
	}
	if strings.Join(names, "\n") != strings.Join(want, "\n") {
		t.Errorf("discoverNames = %q, want %q", names, want)
	}
}

func TestShuffleSeeded(t *testing.T) {
	names := []string{
		"BenchmarkA", "BenchmarkB/one", "BenchmarkB/two", "BenchmarkC",
		"BenchmarkD", "BenchmarkE", "BenchmarkF", "BenchmarkG",
	}
	first := append([]string{}, names...)
	shuffleNames(first, 7)
	second := append([]string{}, names...)
	shuffleNames(second, 7)
	if strings.Join(first, "\n") != strings.Join(second, "\n") {
		t.Fatalf("same seed produced different orders:\n%v\n%v", first, second)
	}
	if strings.Join(first, "\n") == strings.Join(names, "\n") {
		t.Fatalf("seed 7 left the list in order; pick another seed")
	}
	other := append([]string{}, names...)
	shuffleNames(other, 8)
	if strings.Join(first, "\n") == strings.Join(other, "\n") {
		t.Fatalf("seeds 7 and 8 produced the same order; pick another seed")
	}
}

// --- per-parent discovery ---

func TestListBenchmarks(t *testing.T) {
	out := `BenchmarkGetField
BenchmarkUnmarshalCitmStruct
BenchmarkScale
ok  	github.com/sebishogun/simdjson/bench	0.006s
`
	names, err := listBenchmarks([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"BenchmarkGetField", "BenchmarkScale", "BenchmarkUnmarshalCitmStruct"}
	if strings.Join(names, "\n") != strings.Join(want, "\n") {
		t.Errorf("listBenchmarks = %q, want %q", names, want)
	}
}

func TestParseTimes(t *testing.T) {
	out := `goos: linux
goarch: amd64
BenchmarkScale/1MB/ours-Parse-32   1  535285 ns/op  1960.29 MB/s
BenchmarkScale/1MB/ours-Scan-32    1  251658 ns/op  4169.60 MB/s
BenchmarkScale/8MB/goccy-Valid-32  1  701709854 ns/op  11.96 MB/s
ok  	github.com/sebishogun/simdjson/bench	0.9s
`
	times, err := parseTimes([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(times) != 3 {
		t.Fatalf("parseTimes: got %d entries, want 3", len(times))
	}
	if times["BenchmarkScale/8MB/goccy-Valid"] != 701709854 {
		t.Errorf("goccy-Valid time = %v, want 701709854", times["BenchmarkScale/8MB/goccy-Valid"])
	}
}

// TestPlanParentTimeout: a parent whose discovery process was killed by the
// timeout is skipped as a whole — its sub-benchmarks are never seen, so a
// single skipped entry with the parent's name is all there is to record.
func TestPlanParentTimeout(t *testing.T) {
	times := map[string]float64{"BenchmarkScale/1MB/ours-Parse": 5e5}
	names, skipped := planParent("BenchmarkScale", times, true, 10, false)
	if len(names) != 0 {
		t.Errorf("names = %q, want none", names)
	}
	if len(skipped) != 1 || skipped[0].Name != "BenchmarkScale" ||
		skipped[0].Reason != "exceeds -max-discover-sec at 1x" {
		t.Errorf("skipped = %+v, want one entry: BenchmarkScale / exceeds -max-discover-sec at 1x", skipped)
	}
}

// TestPlanParentTimeoutIncludeSlow: with -include-slow the timeout is never
// armed, so a timed-out parent is a contradiction; the completed subs stay.
func TestPlanParentTimeoutIncludeSlow(t *testing.T) {
	times := map[string]float64{"BenchmarkScale/1MB/ours-Parse": 5e5}
	names, skipped := planParent("BenchmarkScale", times, true, 10, true)
	if len(skipped) != 0 {
		t.Errorf("skipped = %+v, want none with -include-slow", skipped)
	}
	if strings.Join(names, "\n") != "BenchmarkScale/1MB/ours-Parse" {
		t.Errorf("names = %q, want BenchmarkScale/1MB/ours-Parse", names)
	}
}

// TestPlanParentSubThreshold: a sub-benchmark whose measured 1x time exceeds
// the threshold is skipped individually; fast sub-benchmarks of the same
// parent stay.
func TestPlanParentSubThreshold(t *testing.T) {
	times := map[string]float64{
		"BenchmarkScale/1MB/ours-Parse":   5e5,
		"BenchmarkScale/64MB/goccy-Valid": 44.3e9,
	}
	names, skipped := planParent("BenchmarkScale", times, false, 10, false)
	if strings.Join(names, "\n") != "BenchmarkScale/1MB/ours-Parse" {
		t.Errorf("names = %q, want only the fast sub", names)
	}
	if len(skipped) != 1 || skipped[0].Name != "BenchmarkScale/64MB/goccy-Valid" ||
		skipped[0].Reason != "exceeds -max-discover-sec at 1x (measured 44.3s)" {
		t.Errorf("skipped = %+v, want 64MB/goccy-Valid with its measured time", skipped)
	}
}

func TestPlanParentNoResult(t *testing.T) {
	names, skipped := planParent("BenchmarkFoo", map[string]float64{}, false, 10, false)
	if len(names) != 0 {
		t.Errorf("names = %q, want none", names)
	}
	if len(skipped) != 1 || skipped[0].Name != "BenchmarkFoo" ||
		skipped[0].Reason != "no result at 1x" {
		t.Errorf("skipped = %+v, want BenchmarkFoo / no result at 1x", skipped)
	}
}

// TestPlanParentNoThreshold: -max-discover-sec 0 disables the threshold, so
// nothing is skipped on measured time.
func TestPlanParentNoThreshold(t *testing.T) {
	times := map[string]float64{
		"BenchmarkScale/1MB/ours-Parse":   5e5,
		"BenchmarkScale/64MB/goccy-Valid": 44.3e9,
	}
	names, skipped := planParent("BenchmarkScale", times, false, 0, false)
	if len(skipped) != 0 {
		t.Errorf("skipped = %+v, want none with maxSec 0", skipped)
	}
	if strings.Join(names, "\n") != "BenchmarkScale/1MB/ours-Parse\nBenchmarkScale/64MB/goccy-Valid" {
		t.Errorf("names = %q, want both subs", names)
	}
}

func TestBenchFilter(t *testing.T) {
	names := []string{
		"BenchmarkUnmarshalCitmStruct/ours",
		"BenchmarkUnmarshalCitmStruct/sonic",
		"BenchmarkScale/64MB/ours-Parse",
	}
	kept, skipped := filterBench(names, regexp.MustCompile(`BenchmarkUnmarshalCitmStruct`))
	if strings.Join(kept, "\n") != "BenchmarkUnmarshalCitmStruct/ours\nBenchmarkUnmarshalCitmStruct/sonic" {
		t.Errorf("kept = %q, want the two citm subs", kept)
	}
	if len(skipped) != 1 || skipped[0].Name != "BenchmarkScale/64MB/ours-Parse" ||
		skipped[0].Reason != "filtered by -bench" {
		t.Errorf("skipped = %+v, want BenchmarkScale/64MB/ours-Parse / filtered by -bench", skipped)
	}
}
