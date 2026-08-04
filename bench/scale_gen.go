package bench

// The documents BenchmarkScale needs, built on demand.
//
// They used to be read from a hardcoded home directory and the benchmark
// skipped when they were not there, which meant the size sweep ran on exactly
// one machine and silently ran short even on that one: the 8 MB file was
// missing and nothing said so.
//
// They are generated instead, from the vendored twitter.json, into the user
// cache directory. The first run pays for it; later runs find them. Half a
// gigabyte of JSON is not something to put in a module zip.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// scaleSizes is the sweep. A structural index is memory proportional to the
// input, so the question is where -- if anywhere -- it stops fitting and the
// approach falls off. Anything measured only at a megabyte is measured entirely
// inside cache.
var scaleSizes = []int{1 << 20, 8 << 20, 16 << 20, 32 << 20, 64 << 20, 512 << 20}

func scaleDir(tb testing.TB) string {
	tb.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		tb.Fatalf("user cache dir: %v", err)
	}
	dir := filepath.Join(base, "simdjson-bench")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}

// scaleDoc returns a document of about sz bytes: twitter.json's statuses array,
// repeated. Same shape at every size, so the sweep varies one thing.
func scaleDoc(tb testing.TB, sz int) []byte {
	tb.Helper()
	path := filepath.Join(scaleDir(tb), fmt.Sprintf("arr_%d.json", sz))
	if data, err := os.ReadFile(path); err == nil && len(data) >= sz {
		return data
	}

	var src struct {
		Statuses []json.RawMessage `json:"statuses"`
	}
	if err := json.Unmarshal(loadCorpus(tb, "twitter"), &src); err != nil {
		tb.Fatalf("twitter.json: %v", err)
	}
	if len(src.Statuses) == 0 {
		tb.Fatal("twitter.json has no statuses")
	}

	var b bytes.Buffer
	b.Grow(sz + 1<<16)
	b.WriteString(`{"statuses":[`)
	for i := 0; b.Len() < sz; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(src.Statuses[i%len(src.Statuses)])
	}
	b.WriteString(`]}`)

	// Written through a temporary so a killed run does not leave a truncated
	// file that the next one reads as valid.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o644); err != nil {
		tb.Fatalf("write %s: %v", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		tb.Fatalf("rename %s: %v", path, err)
	}
	return b.Bytes()
}
