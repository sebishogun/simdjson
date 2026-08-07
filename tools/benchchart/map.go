package main

import "strings"

// A benchmark name becomes {family, corpus, library}. The harness names
// sub-benchmarks by library (bench/decode_rows_test.go runs "ours", "sonic",
// "goccy", "stdlib", "jsoniter", "segmentio"; other files add fastjson, minio,
// gjson, sjson, easyjson, jsonv2) and sometimes by operation
// (BenchmarkCorpus/twitter/ours-Parse). The classifier reads the operation
// suffix first — it is the most reliable signal — then the parent prefix.

type row struct {
	Family  string
	Corpus  string
	Library string
}

var knownOps = map[string]string{
	"Parse":  "parse",
	"Scan":   "index",
	"Valid":  "validate",
	"Decode": "streaming",
	"Encode": "streaming",
}

var knownLibraries = map[string]string{
	"ours":      "this",
	"sonic":     "sonic",
	"goccy":     "goccy",
	"stdlib":    "stdlib",
	"jsoniter":  "jsoniter",
	"segmentio": "segmentio",
	"fastjson":  "fastjson",
	"minio":     "minio",
	"gjson":     "gjson",
	"sjson":     "sjson",
	"easyjson":  "easyjson",
	"jsonv2":    "jsonv2",
}

// parentPrefixes maps a top-level benchmark prefix to the family its rows
// belong to, unless the operation suffix says otherwise.
var parentPrefixes = []struct{ prefix, family string }{
	{"BenchmarkUnmarshal", "unmarshal"},
	{"BenchmarkMarshal", "marshal"},
	{"BenchmarkStream", "streaming"},
	{"BenchmarkShapeValid", "validate"},
	{"BenchmarkShapeDecodeMap", "unmarshal"},
	{"BenchmarkColdStartUnmarshal", "unmarshal"},
	{"BenchmarkSMLUnmarshal", "unmarshal"},
	{"BenchmarkSMLMarshal", "marshal"},
	{"BenchmarkReadme", "streaming"},
	{"BenchmarkCorpus", "parse"},
	{"BenchmarkScale", "parse"},
}

func classify(name string) row {
	rest := strings.TrimPrefix(name, "Benchmark")
	segs := strings.Split(rest, "/")
	last := segs[len(segs)-1]

	// The operation suffix, if any: "ours-Parse" is library ours, op Parse.
	var op, libPart string
	if i := strings.LastIndexByte(last, '-'); i >= 0 {
		op = last[i+1:]
		libPart = last[:i]
	} else {
		libPart = last
	}
	lib, ok := knownLibraries[libPart]
	if !ok {
		lib = "other"
	}

	// The corpus is the path segment between the parent and the library.
	corpus := ""
	if len(segs) >= 3 && segs[0] != "" {
		corpus = segs[len(segs)-2]
	}
	if corpus == libPart {
		corpus = ""
	}

	// Corpus names in the parent itself: BenchmarkUnmarshalCitmStruct.
	fam := ""
	for _, p := range parentPrefixes {
		if strings.HasPrefix(name, p.prefix) {
			fam = p.family
			break
		}
	}
	if c, ok := knownOps[op]; ok {
		fam = c
	}
	if fam == "" {
		fam = "other"
	}
	if corpus == "" {
		for _, c := range []string{"Twitter", "Citm", "Canada", "FloatSlice", "Small", "Medium", "Large"} {
			if strings.Contains(segs[0], c) {
				corpus = strings.ToLower(c)
				break
			}
		}
	}
	return row{Family: fam, Corpus: corpus, Library: lib}
}
