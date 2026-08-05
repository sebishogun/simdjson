package bench

// Guards that keep a mislabeled row from ever being published.
//
// sonic silently falls back to encoding/json off amd64 and outside its
// supported Go range (golang/go#71672 broke 1.24.0 outright) — a benchmark
// run there measures the standard library and labels it sonic. minio's
// simdjson-go requires AVX2 and has no fallback, so its failure mode is loud
// and needs no guard. The Go-version half of sonic's gate cannot be asserted
// robustly from here (the range moves with sonic releases); the architecture
// half can, and the version trap is documented where the pin lives.

import (
	"runtime"
	"testing"

	msj "github.com/minio/simdjson-go"
)

func TestCompetitorRowsAreWhatTheyClaim(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Fatalf("bench is amd64-only: sonic would silently fall back to encoding/json on %s and the sonic rows would be stdlib mislabeled", runtime.GOARCH)
	}
	if !msj.SupportedCPU() {
		t.Fatal("minio/simdjson-go does not support this CPU; its rows would fail rather than mislead, but the run is not the published configuration")
	}
}
