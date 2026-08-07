package main

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name             string
		family, corpus   string
		library          string
		wantFamily, want string
	}{
		{"BenchmarkUnmarshalCitmStruct/sonic", "unmarshal", "citm", "sonic", "unmarshal", "citm"},
		{"BenchmarkUnmarshalCitmStruct/ours", "unmarshal", "citm", "this", "unmarshal", "citm"},
		{"BenchmarkCorpus/twitter/ours-Parse", "parse", "twitter", "this", "parse", "twitter"},
		{"BenchmarkCorpus/canada/goccy-Valid", "validate", "canada", "goccy", "validate", "canada"},
		{"BenchmarkCorpus/citm/fastjson", "parse", "citm", "fastjson", "parse", "citm"},
		{"BenchmarkStreamShapes/tweet-2KB/ours", "streaming", "tweet-2KB", "this", "streaming", "tweet-2KB"},
		{"BenchmarkMarshalStruct/ours", "marshal", "", "this", "marshal", ""},
		{"BenchmarkSMLMarshal/medium/ours", "marshal", "medium", "this", "marshal", "medium"},
		{"BenchmarkShapeValid/twitter/ours", "validate", "twitter", "this", "validate", "twitter"},
		{"BenchmarkWidgetGet/widget/ours", "other", "widget", "this", "other", "widget"},
	}
	for _, c := range cases {
		got := classify(c.name)
		if got.Family != c.wantFamily || got.Corpus != c.want || got.Library != c.library {
			t.Errorf("classify(%q) = {%s, %s, %s}, want {%s, %s, %s}",
				c.name, got.Family, got.Corpus, got.Library,
				c.wantFamily, c.want, c.library)
		}
	}
}

func TestClassifyUnknownLibrary(t *testing.T) {
	got := classify("BenchmarkUnmarshalCitmStruct/somethingnew")
	if got.Library != "other" {
		t.Fatalf("unknown library = %q, want other", got.Library)
	}
}
