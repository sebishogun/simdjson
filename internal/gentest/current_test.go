package gentest

// The committed generated file has to match what the generator produces now.
//
// Without this, a change to structgen that alters its output is invisible: the
// committed file keeps compiling and keeps passing the differential, because
// the differential tests the COMMITTED code and not the generator. The two
// would drift until someone regenerated and got a surprise diff.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGeneratedFileIsCurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs the generator, which shells out to go list")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "gen.go")
	if b, err := structgen(t, out, "Outer"); err != nil {
		t.Fatalf("running structgen: %v\n%s", err, b)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("simdjson_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("simdjson_gen.go is out of date; run go generate ./internal/gentest\n"+
			"--- committed ---\n%s\n--- generated ---\n%s", want, got)
	}
}

// TestGeneratorDeclines: every shape in Declined is one structgen must refuse.
// If it starts accepting one, the differential has to cover it first --
// accepting silently is how wrong JSON ships.
func TestGeneratorDeclines(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs the generator")
	}
	b, err := structgen(t, filepath.Join(t.TempDir(), "gen.go"), "Declined")
	if err == nil {
		t.Fatalf("structgen accepted Declined; it must refuse it:\n%s", b)
	}
	if !contains(string(b), "declined Declined") {
		t.Errorf("structgen failed but not by declining:\n%s", b)
	}
}

// structgen runs the generator over this package.
//
// tools/ is its own module, so the generator is BUILT there and then RUN from
// here. Running it there instead resolves this package's import path in the
// tools module, where it is not a dependency, and the source importer fails.
func structgen(t *testing.T, out, types string) ([]byte, error) {
	t.Helper()
	here, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "structgen")
	build := exec.Command("go", "build", "-o", bin, "./structgen")
	build.Dir = filepath.Join(here, "..", "..", "tools")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building structgen: %v\n%s", err, b)
	}
	cmd := exec.Command(bin, "-dir", here, "-o", out, "-types", types)
	cmd.Dir = here
	return cmd.CombinedOutput()
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
