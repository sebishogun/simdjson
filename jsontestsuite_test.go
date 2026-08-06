package simdjson

// The JSONTestSuite (github.com/nst/JSONTestSuite, MIT), 318 parsing cases:
// y_ files must be accepted, n_ files rejected, i_ files are implementation-
// defined. The bar here is strict AGREEMENT WITH encoding/json on every
// file including the i_ class -- the drop-in claim is agreement, not RFC
// exegesis -- for both Valid and Unmarshal-into-any error presence.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONTestSuiteAgreesWithStdlib(t *testing.T) {
	files, err := filepath.Glob("testdata/jsontestsuite/*.json")
	if err != nil || len(files) < 300 {
		t.Fatalf("suite files: %d, %v", len(files), err)
	}
	var checked, disagreeValid, disagreeDecode int
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(f)
		checked++

		sv := json.Valid(data)
		ov := Valid(data)
		if sv != ov {
			disagreeValid++
			t.Errorf("%s: Valid: ours=%v stdlib=%v", name, ov, sv)
		}
		var oa, sa any
		oerr := Unmarshal(data, &oa)
		serr := json.Unmarshal(data, &sa)
		if (oerr == nil) != (serr == nil) {
			disagreeDecode++
			t.Errorf("%s: Unmarshal err: ours=%v stdlib=%v", name, oerr, serr)
		}
		// The y_/n_ prefixes double-check stdlib itself hasn't drifted from
		// the suite's own expectations where they are unambiguous.
		if strings.HasPrefix(name, "y_") && !sv {
			t.Logf("note: stdlib rejects y_ case %s", name)
		}
		if strings.HasPrefix(name, "n_") && sv {
			t.Logf("note: stdlib accepts n_ case %s", name)
		}
	}
	t.Logf("%d files, %d Valid disagreements, %d decode disagreements",
		checked, disagreeValid, disagreeDecode)
}
