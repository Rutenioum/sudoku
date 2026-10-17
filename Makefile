# Binary name
BINARY_NAME=sudoku
MAIN_PATH=./cmd/sudoku-tunnel

# Build flags
# -s: Omit the symbol table and debug information
# -w: Omit the DWARF symbol table
LDFLAGS=-ldflags "-s -w"

.PHONY: all build clean vet test test-race bench bench-downlink interop perfcmp wan pprof-sudoku build-all help

default: build

# Build the binary for the current OS/ARCH
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p bin
	go build $(LDFLAGS) -o bin/$(BINARY_NAME) $(MAIN_PATH)
	@echo "Build complete: bin/$(BINARY_NAME)"

vet:
	go vet ./...

# Run tests
test:
	@echo "Running tests..."
	go test -v ./...

# Same checks as CI on Linux and macOS
test-race:
	go test -count=1 -shuffle=on -race ./...

# Process-level compatibility against a released binary, both client/server directions:
#   make interop SUDOKU_REF_BIN=/path/to/sudoku-v0.5.1
interop:
	@test -n "$(SUDOKU_REF_BIN)" || { echo "set SUDOKU_REF_BIN to a released sudoku binary"; exit 1; }
	SUDOKU_REF_BIN=$(SUDOKU_REF_BIN) go test ./tests -run TestInteropWithReference -count=1 -v -timeout 25m

# Throughput, CPU and memory against a released binary (run on an otherwise idle machine):
#   make perfcmp SUDOKU_REF_BIN=/path/to/sudoku-v0.5.1
perfcmp:
	@test -n "$(SUDOKU_REF_BIN)" || { echo "set SUDOKU_REF_BIN to a released sudoku binary"; exit 1; }
	SUDOKU_REF_BIN=$(SUDOKU_REF_BIN) go test ./tests -run TestPerfCompare -count=1 -v -timeout 60m

# 200ms-RTT lossy WAN simulations (a few minutes)
wan:
	SUDOKU_WAN_SIM=1 go test ./tests -run WAN -count=1 -v -timeout 30m

# Run benchmarks (no tests)
bench:
	@echo "Running benchmarks..."
	go test -run '^$$' -bench . -benchmem ./...

# Concurrent downlink matrix, as summarized in CI
bench-downlink:
	./scripts/ci-downlink-benchmark-summary.sh

# Generate pprof profiles for the Sudoku obfs layer
pprof-sudoku:
	@echo "Generating pprof profiles for ./pkg/obfs/sudoku..."
	@rm -f cpu.out mem.out
	go test -run '^$$' -bench BenchmarkSudoku -benchmem -cpuprofile cpu.out -memprofile mem.out ./pkg/obfs/sudoku
	@echo "Profiles written: cpu.out, mem.out"

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@rm -rf bin
	@go clean

# Cross-compile for common platforms locally (useful for quick checks)
build-all: clean
	@echo "Building for multiple platforms..."
	@mkdir -p bin
	# Linux
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-amd64 $(MAIN_PATH)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-arm64 $(MAIN_PATH)
	# Windows
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-windows-amd64.exe $(MAIN_PATH)
	# macOS (Darwin)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-amd64 $(MAIN_PATH)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-arm64 $(MAIN_PATH)
	@echo "All builds complete."

# Show help
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  build       Build for current OS (default)"
	@echo "  vet         Run go vet"
	@echo "  test        Run unit tests"
	@echo "  test-race   Run the CI test command with the race detector"
	@echo "  bench       Run benchmarks (benchmem)"
	@echo "  bench-downlink Run the CI downlink throughput matrix"
	@echo "  interop     Cross-version compatibility against SUDOKU_REF_BIN"
	@echo "  perfcmp     Performance comparison against SUDOKU_REF_BIN"
	@echo "  wan         Run the WAN simulations (SUDOKU_WAN_SIM=1)"
	@echo "  pprof-sudoku Generate cpu/mem profiles for Sudoku bench"
	@echo "  clean       Remove bin directory"
	@echo "  build-all   Cross-compile for Linux, Windows, and Darwin (AMD64/ARM64)"
