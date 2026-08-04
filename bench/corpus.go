// Package bench measures this library against the other Go JSON libraries.
//
// It is a separate module so the competitors are not dependencies of simdjson
// itself. Nothing here is imported by the library; `go get
// github.com/sebishogun/simdjson` does not pull sonic, goccy, gjson or the
// rest. Run it with `make bench-vs` from the parent directory.
//
// It exists because docs/competition.md makes claims with numbers in them, and
// a claim nobody can re-run is a claim nobody can check. Every row of that
// table should have a benchmark here behind it.
//
// The only replace directive points simdjson at the parent directory, so this
// measures the working tree. simd resolves to the version the parent requires;
// to measure a local simd tree, add a replace for it yourself rather than
// committing a path that only exists on one machine.
package bench

import (
	"compress/gzip"
	"io"
	"os"
	"sync"
	"testing"
)

// The corpus every JSON parser is measured on. Three documents chosen because
// they stress different things:
//
//	twitter.json   631 KB   strings and unicode escapes
//	citm_catalog   1.73 MB  objects and keys
//	canada.json    2.25 MB  almost entirely floating-point numbers
//
// The synthetic documents the rest of these benchmarks use are regular, have no
// escapes and few numbers, which is exactly the shape that flatters a
// structural index. These do not.
var corpus = []string{"twitter", "citm", "canada"}

var corpusCache sync.Map // name -> []byte

// loadCorpus reads one of the vendored corpora.
//
// From the parent module's testdata, gzipped, and Fatal rather than Skip if it
// is missing. This used to read /tmp/<name>.json and skip when it was not
// there, which is how the harness came to be measured on a machine that
// happened to have them: a benchmark that skips writes no line, and a
// comparison with no line for the competitor reads as a comparison that was
// run. Every number in docs/competition.md was produced that way, and only
// stayed true because the files were still in /tmp.
func loadCorpus(tb testing.TB, name string) []byte {
	tb.Helper()
	if v, ok := corpusCache.Load(name); ok {
		return v.([]byte)
	}
	path := "../testdata/bench/corpus/" + name + ".json.gz"
	f, err := os.Open(path)
	if err != nil {
		tb.Fatalf("corpus %s: %v", path, err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		tb.Fatalf("corpus %s: %v", path, err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		tb.Fatalf("corpus %s: %v", path, err)
	}
	corpusCache.Store(name, data)
	return data
}
