# =============================================================================
# File: Makefile
# Copyright: 2026 Chase Reynolds. All rights reserved.
#
# Derived from spice-edit's Makefile (Copyright 2026 Cloudmanic, LLC, MIT).
# The website targets are gone — Vincent has no site to build.
# =============================================================================

# -----------------------------------------------------------------------------
# Windows shell resolution. Must come before anything that runs $(shell ...).
# -----------------------------------------------------------------------------
#
# GNU make on Windows uses cmd.exe unless it finds a POSIX shell on PATH.
# In a PowerShell session it does not: Git ships sh.exe under usr/bin, but
# only Git's cmd/ directory is on PATH. Every recipe here is POSIX, so the
# first one fails with the memorable
#
#     mkdir -p bin
#     A subdirectory or file -p already exists.
#
# and it only reproduces from PowerShell — from Git Bash sh.exe IS on PATH
# and everything works, which is a good way to ship this bug without seeing
# it.
#
# Rather than rewrite every recipe in cmd syntax, point make at Git's own
# shell. PATH matters too: make skips SHELL entirely for commands with no
# shell metacharacters, running them via CreateProcess, so mkdir / cp / rm
# have to be findable as executables in their own right.
#
# The candidates are spelled in 8.3 short form (PROGRA~1) because make
# cannot handle a SHELL path containing spaces.
ifeq ($(OS),Windows_NT)
POSIX_SH_CANDIDATES := \
	C:/PROGRA~1/Git/usr/bin/sh.exe \
	C:/PROGRA~2/Git/usr/bin/sh.exe \
	$(subst \,/,$(LOCALAPPDATA))/Programs/Git/usr/bin/sh.exe \
	$(subst \,/,$(USERPROFILE))/scoop/apps/git/current/usr/bin/sh.exe
POSIX_SH := $(firstword $(foreach c,$(POSIX_SH_CANDIDATES),$(wildcard $(c))))
ifneq ($(POSIX_SH),)
SHELL := $(POSIX_SH)
export PATH := $(patsubst %/,%,$(dir $(POSIX_SH)));$(PATH)
else
$(warning No POSIX shell found, so make will fall back to cmd.exe and every)
$(warning recipe here will fail. Run make from Git Bash instead, or install)
$(warning Git for Windows so that usr/bin/sh.exe exists.)
endif
endif

# NOTE: every @echo in this file is plain ASCII on purpose. make writes UTF-8
# bytes straight to the console, and the Windows console is cp1252 by default
# — an em-dash comes out as mojibake. Comments can use whatever they like;
# anything PRINTED cannot.

# GOEXE is ".exe" on Windows and empty everywhere else. Without it the
# Windows build produces an extensionless file that IS a valid PE binary but
# that PowerShell silently refuses to run — Windows only executes what
# PATHEXT lists, and there is no error message when it doesn't.
EXE := $(shell go env GOEXE)
BINARY := vincent$(EXE)

# HOME is not set in a PowerShell session — Windows uses USERPROFILE — so a
# bare $(HOME)/.local/bin resolves to "/.local/bin" there, and the install
# fails somewhere far from the cause. Git Bash DOES set HOME, so this only
# breaks in the shell the previous version was never tested in.
ifeq ($(HOME),)
HOME_DIR := $(subst \,/,$(USERPROFILE))
else
HOME_DIR := $(HOME)
endif

# INSTALL_DIR is where `make install` puts the binary. ~/.local/bin rather
# than GOPATH/bin because that is what is actually on PATH on this machine.
# Override on the command line if yours differs: make install INSTALL_DIR=...
INSTALL_DIR ?= $(HOME_DIR)/.local/bin

.PHONY: run build install build-linux build-mac test test-race test-short coverage tidy clean help

# help is the default target so `make` with no args prints what's available.
help:
	@echo "Vincent - read-only, mouse-first terminal client for reviewing agent code"
	@echo ""
	@echo "  make run          Run against the current directory."
	@echo "  make build        Build the binary into ./bin/$(BINARY)."
	@echo "  make install      Build and copy to $(INSTALL_DIR)."
	@echo "  make build-linux  Cross-compile a static linux/amd64 binary."
	@echo "  make build-mac    Cross-compile a static darwin/arm64 binary."
	@echo "  make test         Run the full suite."
	@echo "  make test-race    Run with -race (needs cgo; CI parity)."
	@echo "  make test-short   Skip slow tests - quick iteration loop."
	@echo "  make coverage     Generate coverage.out + coverage.html."
	@echo "  make tidy         Run 'go mod tidy'."
	@echo "  make clean        Remove ./bin and coverage artifacts."

# run starts Vincent via 'go run' against the current directory. Quickest
# path for development; for real use prefer 'make build' and the binary.
run:
	go run .

# bin is an order-only prerequisite of every build target: make creates it
# once, only when missing. This avoids `mkdir -p`, whose -p flag cmd.exe
# reads as a directory NAME rather than a flag.
bin:
	mkdir bin

# build produces a single binary at ./bin/$(BINARY).
build: | bin
	go build -o bin/$(BINARY) .

# install builds and copies the binary to INSTALL_DIR (~/.local/bin by
# default). NOT `go install`: that lands in GOPATH/bin, which is not on PATH
# on this machine, so the command would appear to succeed and then `vincent`
# would still be "not recognised".
install: build
	@mkdir -p "$(INSTALL_DIR)"
	@cp bin/$(BINARY) "$(INSTALL_DIR)/$(BINARY)" 2>/dev/null || { \
		echo ""; \
		echo "Could not replace $(INSTALL_DIR)/$(BINARY)."; \
		echo "Windows locks a running executable - if Vincent is open, quit it"; \
		echo "(Esc q) and run 'make install' again. The new build is already at"; \
		echo "bin/$(BINARY) either way."; \
		echo ""; \
		exit 1; }
	@echo "Installed $(INSTALL_DIR)/$(BINARY)"
	@echo "Run 'vincent' from any directory."

# build-linux cross-compiles a fully static linux/amd64 binary.
build-linux: | bin
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags='-s -w' -o bin/$(BINARY)-linux-amd64 .

# build-mac cross-compiles for Apple Silicon. Present because this repo is
# developed on Windows today and moves to macOS later — verifying the
# cross-compile keeps working is the point of having the target.
build-mac: | bin
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
