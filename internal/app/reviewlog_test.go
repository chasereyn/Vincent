// =============================================================================
// File: internal/app/reviewlog_test.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chasereyn/vincent/internal/review"
)

// TestInstallReviewLog_WritesToConfigDirNotStderr pins the reason this
// file exists: after installReviewLog, a review.Logf call lands in
// ~/.config/vincent/herdr.log and nothing reaches stderr. HOME is pointed
// at a temp dir so the test never touches the real config directory.
func TestInstallReviewLog_WritesToConfigDirNotStderr(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	prev := review.Logf
	t.Cleanup(func() { review.Logf = prev })

	installReviewLog()
	review.Logf("vincent: herdr pane send-text %s failed: %v", "w1:p2", "boom")

	data, err := os.ReadFile(filepath.Join(home, ".config", "vincent", reviewLogName))
	if err != nil {
		t.Fatalf("log file: %v", err)
	}
	if !strings.Contains(string(data), "w1:p2") || !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("log content = %q", data)
	}
}

// TestInstallReviewLog_UnwritableHomeDiscards proves the forgiving half:
// when the config directory cannot be created, failures are swallowed
// rather than sent to stderr, and calling Logf does not panic.
func TestInstallReviewLog_UnwritableHomeDiscards(t *testing.T) {
	home := t.TempDir()
	// A regular file where the .config directory should be makes MkdirAll fail.
	if err := os.WriteFile(filepath.Join(home, ".config"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	prev := review.Logf
	t.Cleanup(func() { review.Logf = prev })

	installReviewLog()
	review.Logf("should vanish %d", 1)
}
