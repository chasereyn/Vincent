// =============================================================================
// File: internal/app/reviewlog.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// No upstream equivalent. spice-edit had nothing that talked to another
// process and so nothing that needed a log.
// =============================================================================

// reviewlog.go points the review package's failure log at a file instead
// of stderr. A raw-mode TUI owns the whole terminal, so anything written
// to stderr lands on top of the screen and sits there until the next
// repaint. herdr's JSON error envelopes are exactly the kind of thing we
// want to keep for debugging and never want to see painted over a diff.

package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chasereyn/vincent/internal/config"
	"github.com/chasereyn/vincent/internal/review"
)

// reviewLogName is the file under ~/.config/vincent/ that receives the
// verbatim herdr failures. One file, append-only, never rotated: a send
// fails rarely enough that the file stays small, and when it does fail
// the full envelope is what you want to read.
const reviewLogName = "herdr.log"

// installReviewLog redirects review.Logf into ~/.config/vincent/herdr.log.
// It runs once per App and is deliberately forgiving: if the directory or
// the file cannot be opened, failures are discarded rather than written
// to stderr, because a log line over the UI is worse than no log line.
// The status bar still gets the one-sentence version either way.
func installReviewLog() {
	cfg := config.DefaultPath()
	if cfg == "" {
		review.Logf = func(string, ...any) {}
		return
	}
	dir := filepath.Dir(cfg) // same directory as config.json, XDG-aware
	if err := os.MkdirAll(dir, 0o755); err != nil {
		review.Logf = func(string, ...any) {}
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, reviewLogName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		review.Logf = func(string, ...any) {}
		return
	}
	review.Logf = func(format string, args ...any) {
		fmt.Fprintf(f, format+"\n", args...)
	}
}
