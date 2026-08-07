package main

import (
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
