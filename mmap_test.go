package simdjson

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOpenFile(t *testing.T) {
	p := writeTemp(t, "doc.json", []byte(`{"a":{"b":[1,2,3]},"s":"hello"}`))
	for _, validate := range []bool{true, false} {
		m, err := OpenFile(p, validate)
		if err != nil {
			t.Fatalf("OpenFile(validate=%v): %v", validate, err)
		}
		if got := m.Doc().Path("s").String(); got != "hello" {
			t.Errorf("validate=%v: s = %q", validate, got)
		}
		if got := m.Doc().Path("a.b.2").Int(); got != 3 {
			t.Errorf("validate=%v: a.b.2 = %d", validate, got)
		}
		if len(m.Bytes()) != 31 {
			t.Errorf("Bytes() length = %d", len(m.Bytes()))
		}
		if err := m.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		// Close twice must not fault or error.
		if err := m.Close(); err != nil {
			t.Errorf("second Close: %v", err)
		}
	}
}

func TestOpenFileErrors(t *testing.T) {
	if _, err := OpenFile(filepath.Join(t.TempDir(), "nope.json"), true); err == nil {
		t.Error("missing file gave no error")
	}
	if _, err := OpenFile(writeTemp(t, "empty.json", nil), true); err == nil {
		t.Error("empty file gave no error")
	}
	if _, err := OpenFile(writeTemp(t, "bad.json", []byte(`{`)), true); err == nil {
		t.Error("malformed file gave no error")
	}
	// Scan does not validate the whole document, so a bad tail is not its
	// problem -- but a document that does not even index is.
	if _, err := OpenFile(writeTemp(t, "bad2.json", []byte(`{"a":`)), false); err == nil {
		t.Error("truncated file gave no error from the non-validating path")
	}
}

// A string taken with String survives Close; one taken with StringNoCopy does
// not, and the doc comment says so. Only the first is asserted -- reading the
// second after Close is a fault, not a test.
func TestOpenFileStringOutlivesClose(t *testing.T) {
	p := writeTemp(t, "doc.json", []byte(`{"s":"kept"}`))
	m, err := OpenFile(p, true)
	if err != nil {
		t.Fatal(err)
	}
	s := m.Doc().Path("s").String()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if s != "kept" {
		t.Errorf("string did not survive Close: %q", s)
	}
}

func TestOpenFileRealCorpus(t *testing.T) {
	f, err := os.Open("testdata/bench/corpus/twitter.json.gz")
	if err != nil {
		t.Skip(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	p := writeTemp(t, "twitter.json", data)

	m, err := OpenFile(p, true)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if n := m.Doc().Path("search_metadata.count").Int(); n != 100 {
		t.Errorf("search_metadata.count = %d, want 100", n)
	}
	if s := m.Doc().Path("statuses.0.user.screen_name").String(); s == "" {
		t.Error("statuses.0.user.screen_name is empty")
	}
}
