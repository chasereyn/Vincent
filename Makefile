# =============================================================================
# File: Makefile
# Copyright: 2026 Chase Reynolds. All rights reserved.
#
# Derived from spice-edit's Makefile (Copyright 2026 Cloudmanic, LLC, MIT).
# The website targets are gone — Vincent has no site to build.
# =============================================================================

BINARY := vincent

.PHONY: run build install build-linux build-mac test test-race test-short coverage tidy clean help

# help is the default target so `make` with no args prints what's available.
help:
	@echo "Vincent — read-only, mouse-first terminal client for reviewing agent code"
	@echo ""
	@echo "  make run          Run against the current directory."
	@echo "  make build        Build the binary into ./bin/$(BINARY)."
	@echo "  make install      go install into \$$GOPATH/bin."
	@echo "  make build-linux  Cross-compile a static linux/amd64 binary."
	@echo "  make build-mac    Cross-compile a static darwin/arm64 binary."
	@echo "  make test         Run the full suite."
	@echo "  make test-race    Run with -race (needs cgo; CI parity)."
	@echo "  make test-short   Skip slow tests — quick iteration loop."
	@echo "  make coverage     Generate coverage.out + coverage.html."
	@echo "  make tidy         Run 'go mod tidy'."
	@echo "  make clean        Remove ./bin and coverage artifacts."

# run starts Vincent via 'go run' against the current directory. Quickest
# path for development; for real use prefer 'make build' and the binary.
run:
	go run .

# build produces a single binary at ./bin/$(BINARY).
build:
	mkdir -p bin
	go build -o bin/$(BINARY) .

# install puts the binary on PATH via the Go toolchain rather than
# /usr/local/bin, which needs sudo on macOS and does not exist on Windows.
install:
	go install .

# build-linux cross-compiles a fully static linux/amd64 binary.
build-linux:
	mkdir -p bin
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags='-s -w' -o bin/$(BINARY)-linux-amd64 .

# build-mac cross-compiles for Apple Silicon. Present because this repo is
# developed on Windows today and moves to macOS later — verifying the
# cross-compile keeps working is the point of having the target.
build-mac:
	mkdir -p bin
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags='-s -w' -o bin/$(BINARY)-darwin-arm64 .

# test runs the full suite. Deliberately WITHOUT -race: the race detector
# requires cgo, and this machine builds with CGO_ENABLED=0 (no C compiler,
# which is also what keeps the binary static). Running `make test` here
# would otherwise fail with "-race requires cgo" rather than telling you
# anything about the code.
test:
	go test ./...

# test-race is the CI-parity target. CI runners have a C compiler, so
# .github/workflows/test.yml runs this on all three platforms. Locally on
# Windows it needs `scoop install mingw` first.
test-race:
	go test -race ./...

# test-short is the quick local loop: skip anything tagged slow, no race
# detector. Use this while writing tests.
test-short:
	go test -short ./...

# coverage produces a profile across every package plus an HTML report.
coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"
	@go tool cover -func=coverage.out | tail -n 1

# tidy keeps go.mod / go.sum in sync with what's actually imported.
tidy:
	go mod tidy

# clean removes build artifacts and coverage output.
clean:
	rm -rf bin coverage.out coverage.html
