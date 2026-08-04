# Everything that has to pass before a commit, and the lanes that are easy to
# forget.
#
# The cross lane is here because this package reads masks that assembly kernels
# wrote, and reads them with binary.LittleEndian regardless of what the machine
# is. That is correct — the kernels write a bit per byte in a fixed order — and
# it is exactly the kind of correct that stops being true after a refactor and
# is invisible on amd64. s390x is big-endian and takes 0.4 seconds to ask.

GO ?= go
DOCKER ?= docker

.PHONY: all verify test test-race test-tiers test-purego test-cross fuzz bench fmt fmt-check vet

all: verify ## The default

verify: fmt-check vet vet-vs test test-race test-tiers test-purego ## Everything short of the cross lane and the gate

test: ## Run the suite
	$(GO) test ./...

test-race: ## The suite under the race detector
	$(GO) test -race ./...

test-tiers: ## Once per instruction-set tier simd.go can dispatch to
	@for t in scalar sse2 avx2 avx512; do \
		printf '%-10s ' "$$t"; \
		GOSIMD=$$t $(GO) test -count=1 ./... || exit 1; \
	done

test-purego: ## Against simd.go's portable reference
	$(GO) test -count=1 -tags purego ./...

# The tests skipped by -short are the ones that build nine megabyte documents
# or twenty thousand stream records. They are worth their seconds on a real
# machine and are minutes under emulation, which is the only place -short is
# passed.
test-cross: ## arm64, s390x and ppc64le under docker + qemu
	@for p in linux/arm64 linux/s390x linux/ppc64le; do \
		echo "--- $$p"; \
		$(DOCKER) run --rm --platform $$p -v "$(PWD)":/src -w /src \
			-e GOFLAGS=-buildvcs=false -e CGO_ENABLED=0 golang:1.26 \
			sh -c 'go test -short -vet=off -timeout 1200s ./...' || exit 1; \
	done

# ---------------------------------------------------------------- performance
#
# Every number in the README was measured once by hand, which is how they were
# arrived at and also how they rot: a change that costs 10% looks exactly like
# one that costs nothing until somebody re-runs the benchmark and remembers what
# it used to say. A 10% regression in canada's parse shipped and sat there for
# four commits before it was noticed by accident. This makes the remembering the
# machine's job.
#
# The benchmarks are in bench_gate_test.go, in this package, so the gate runs
# without the competing libraries installed. They need the corpora in /tmp and
# skip without them.

BENCH_BASELINE = testdata/bench/$(shell $(GO) env GOARCH).txt
BENCH_COUNT   ?= 6
BENCH_OUT     ?= /tmp/simdjson-bench-$(shell $(GO) env GOARCH).txt

.PHONY: bench-run bench-check bench-update bench-vs bench-vs-test vet-vs

bench-run: ## Run the gate benchmarks and write the raw output
	$(GO) test -run '^$$' -bench 'BenchmarkGate' -count $(BENCH_COUNT) . > $(BENCH_OUT)

bench-check: bench-run ## Benchmark and fail on anything slower than the baseline
	cd tools && $(GO) run ./benchcheck -baseline ../$(BENCH_BASELINE) $(BENCH_OUT)

bench-update: bench-run ## Re-record the baseline. Say why in the commit message.
	cd tools && $(GO) run ./benchcheck -update -baseline ../$(BENCH_BASELINE) $(BENCH_OUT)
	@echo "baseline updated: $(BENCH_BASELINE)"

FUZZTIME ?= 60s

# Every one of these compares against encoding/json and demands the same bytes
# and the same error-or-not, which is the only standard worth holding a JSON
# library to.
fuzz: ## Fuzz each differential against encoding/json for FUZZTIME each
	@for f in FuzzAgainstStdlib FuzzUnmarshalAgainstStdlib FuzzMarshalAgainstStdlib \
	          FuzzTextOpsAgainstStdlib FuzzDecoderAgainstStdlib; do \
		printf '%-32s ' "$$f"; \
		$(GO) test -run '^$$' -fuzz $$f -fuzztime $(FUZZTIME) . 2>&1 | tail -1 || exit 1; \
	done

bench: ## Every benchmark once
	$(GO) test -run '^$$' -bench . -benchmem ./...

# The comparison against the other Go JSON libraries. A separate module, so
# `go get` of this one does not pull sonic, goccy, gjson and the rest, and so
# their versions move independently of ours.
#
# This is where docs/competition.md's numbers come from. It used to live in a
# temporary directory on one machine, read its corpora from /tmp and skip when
# they were missing -- so the table was reproducible by nobody, including on the
# machine that produced it once the files aged out.
VS_COUNT ?= 8
VS_BENCH ?= .

bench-vs: ## Measure against sonic, goccy, gjson, fastjson, minio and the stdlib
	cd bench && $(GO) test -run '^$$' -bench '$(VS_BENCH)' -count $(VS_COUNT) .

bench-vs-test: ## The agreement checks: do the libraries produce the same bytes
	cd bench && $(GO) test ./...

vet-vs: ## go vet the comparison module
	cd bench && $(GO) vet ./...
	@out=$$(cd bench && gofmt -l .); if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; exit 1; fi

fmt: ## Format
	$(GO) fmt ./...

fmt-check: ## Fail if anything is not gofmt-clean
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; exit 1; fi

vet: ## go vet
	$(GO) vet ./...
