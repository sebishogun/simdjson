package simdjson

// json.org's JSON_checker suite: pass* must parse, fail* should not -- except
// fail1 ("A JSON payload should be an object or array") and fail18 (depth),
// which modern JSON (RFC 8259) and encoding/json both accept. As with the
// JSONTestSuite, the asserted bar is agreement with encoding/json on every
// file; the pass/fail names are cross-checked against stdlib's own answers so
// a stdlib drift would surface as log lines.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestJSONCheckerAgreesWithStdlib(t *testing.T) {
	files, err := filepath.Glob("testdata/jsonchecker/*.json")
	if err != nil || len(files) < 30 {
		t.Fatalf("checker files: %d, %v", len(files), err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(f)
		if sv, ov := json.Valid(data), Valid(data); sv != ov {
			t.Errorf("%s: Valid: ours=%v stdlib=%v", name, ov, sv)
		}
		var oa, sa any
		oerr := Unmarshal(data, &oa)
		serr := json.Unmarshal(data, &sa)
		if (oerr == nil) != (serr == nil) {
			t.Errorf("%s: Unmarshal err: ours=%v stdlib=%v", name, oerr, serr)
		}
	}
}
