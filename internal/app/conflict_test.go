// =============================================================================
// File: internal/app/conflict_test.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// Tests for conflict.go — the Overwrite / Reload / Show diff / Cancel
// prompt a save runs into when the file changed on disk underneath it.
// The fixture is conflictFixture from app_test.go: a dirty tab whose file
// has been rewritten and whose Mtime is backdated so one reconcile pass
// records the conflict.

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// conflictedApp returns an app with one conflicted tab, ready for the
// prompt's buttons to be fired.
func conflictedApp(t *testing.T, diskContent string) (*App, string) {
	t.Helper()
	a, target := conflictFixture(t, diskContent)
	a.reconcileOpenTabsWithDisk(pollNow(a))
	if !a.tabs[0].Conflict {
		t.Fatal("fixture should have produced a conflict")
	}
	return a, target
}

// TestConflictPrompt_ButtonsAndSafeDefault pins the row itself: four
// buttons, Cancel first because that is where focus starts and an
// accidental Enter must not lose anybody's work.
func TestConflictPrompt_ButtonsAndSafeDefault(t *testing.T) {
	a, _ := conflictedApp(t, "from the agent\n")

	a.openConflictPrompt(0)

	want := []string{"Cancel", "Show diff", "Reload", "Overwrite"}
	if len(a.dirtyButtons) != len(want) {
		t.Fatalf("got %d buttons, want %d", len(a.dirtyButtons), len(want))
	}
	for i, label := range want {
		if a.dirtyButtons[i].label != label {
			t.Errorf("button %d: got %q, want %q", i, a.dirtyButtons[i].label, label)
		}
	}
	if a.dirtyHover != 0 {
		t.Fatalf("focus should start on Cancel, got %d", a.dirtyHover)
	}
	if !strings.Contains(a.dirtyMessage, "note.txt") {
		t.Fatalf("the prompt should name the file; got %q", a.dirtyMessage)
	}
}

// TestConflictPrompt_CancelLeavesEverythingAlone is the escape hatch: the
// buffer, the file, and the conflict all survive untouched, so the user can
// go and look before deciding.
func TestConflictPrompt_CancelLeavesEverythingAlone(t *testing.T) {
	a, target := conflictedApp(t, "from the agent\n")
	a.openConflictPrompt(0)

	a.dirtyHover = 0
	a.dirtyActivate()

	if a.dirtyOpen {
		t.Fatal("Cancel should dismiss the prompt")
	}
	if !a.tabs[0].Conflict || !a.tabs[0].Dirty {
		t.Fatal("Cancel must not resolve the conflict")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "from the agent\n" {
		t.Fatalf("Cancel must not write; disk holds %q", got)
	}
}

// TestConflictPrompt_OverwriteWritesTheBufferAndClearsConflict is the
// "my version wins" path. It goes through SaveOverwrite, which is the only
// way past Save's refusal.
func TestConflictPrompt_OverwriteWritesTheBufferAndClearsConflict(t *testing.T) {
	a, target := conflictedApp(t, "from the agent\n")
	buffer := a.tabs[0].Buffer.String()
	a.openConflictPrompt(0)

	a.dirtyHover = 3 // Overwrite
	a.dirtyActivate()

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != buffer {
		t.Fatalf("disk should hold the buffer; got %q want %q", got, buffer)
	}
	if a.tabs[0].Conflict {
		t.Fatal("Overwrite should clear the conflict")
	}
	if a.tabs[0].Dirty {
		t.Fatal("Overwrite should clear the dirty flag")
	}
}

// TestConflictPrompt_ReloadTakesTheDiskVersion is the "their version wins"
// path: the buffer becomes what is on disk and both flags drop.
func TestConflictPrompt_ReloadTakesTheDiskVersion(t *testing.T) {
	a, target := conflictedApp(t, "from the agent\n")
	a.openConflictPrompt(0)

	a.dirtyHover = 2 // Reload
	a.dirtyActivate()

	if got := a.tabs[0].Buffer.String(); got != "from the agent\n" {
		t.Fatalf("buffer after Reload: %q", got)
	}
	if a.tabs[0].Dirty || a.tabs[0].Conflict {
		t.Fatalf("Reload should clear both flags (dirty=%v conflict=%v)",
			a.tabs[0].Dirty, a.tabs[0].Conflict)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "from the agent\n" {
		t.Fatalf("Reload must not write; disk holds %q", got)
	}
}

// TestBufferVsDiskRows_ShowsBothSides is the Show diff computation. The
// user's unsaved line has to appear as a deletion and the agent's as an
// addition, because that is the question being asked: did the two touch
// the same lines?
func TestBufferVsDiskRows_ShowsBothSides(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("from the agent\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rows, ok := bufferVsDiskRows(target, "mine\n")
	if !ok {
		t.Fatal("two different files should produce a diff")
	}
	var texts []string
	for _, r := range rows {
		texts = append(texts, r.Text)
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "mine") || !strings.Contains(joined, "from the agent") {
		t.Fatalf("diff should carry both sides; got:\n%s", joined)
	}
}

// TestBufferVsDiskRows_IdenticalReportsNothing keeps the "no differences"
// case out of the diff tab.
func TestBufferVsDiskRows_IdenticalReportsNothing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("same\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, ok := bufferVsDiskRows(target, "same\n"); ok {
		t.Fatal("identical content should report no diff")
	}
}

// TestBufferVsDiskRows_WorksOutsideAGitRepo is the reason bufferVsDiskRows
// stopped shelling out to `git diff --no-index`: a conflict can happen on a
// file that was never inside a repo at all — herdr's working directory,
// scratch notes, anything opened by absolute path — and the diff still has
// to render. t.TempDir() here is deliberately NOT inside the vincent repo
// and has no .git of its own, so a regression back to a git shell-out would
// fail this the same way it used to fail for real.
func TestBufferVsDiskRows_WorksOutsideAGitRepo(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "scratch.txt")
	if err := os.WriteFile(target, []byte("on disk\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rows, ok := bufferVsDiskRows(target, "in the buffer\n")
	if !ok {
		t.Fatal("a file outside any git repo should still diff")
	}
	var texts []string
	for _, r := range rows {
		texts = append(texts, r.Text)
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "in the buffer") || !strings.Contains(joined, "on disk") {
		t.Fatalf("diff should carry both sides; got:\n%s", joined)
	}
}

// TestConflictPrompt_ShowDiffOpensAFrozenDiffTab walks the button. The tab
// has to be titled distinctly (two diffs of one file are otherwise
// indistinguishable) and marked frozen, so the reconcile loop leaves it
// alone instead of swapping in the ordinary git diff. Show diff is
// in-process now (diff.Unified, not a git shell-out), so this needs no
// requireGit guard.
func TestConflictPrompt_ShowDiffOpensAFrozenDiffTab(t *testing.T) {
	a, _ := conflictedApp(t, "from the agent\n")
	a.openConflictPrompt(0)

	a.dirtyHover = 1 // Show diff
	a.dirtyActivate()

	if len(a.tabs) != 2 {
		t.Fatalf("expected a second tab for the diff, got %d", len(a.tabs))
	}
	dt := a.tabs[1]
	if !dt.IsDiff() {
		t.Fatal("the new tab should be a diff")
	}
	if !dt.DiffFrozen {
		t.Fatal("a buffer-vs-disk diff must be frozen")
	}
	if dt.DisplayName() != "note.txt (buffer vs disk)" {
		t.Fatalf("tab title: %q", dt.DisplayName())
	}
	if a.activeTab != 1 {
		t.Fatalf("the diff should be focused; activeTab=%d", a.activeTab)
	}
	// The file tab keeps its conflict — looking is not deciding.
	if !a.tabs[0].Conflict {
		t.Fatal("Show diff must not resolve the conflict")
	}
}

// TestReconcileDiffTab_LeavesAFrozenDiffAlone is the other half of that:
// the reconcile pass must not re-run the diff over a frozen tab, or the
// buffer-vs-disk comparison silently becomes the file's git diff while the
// user is reading it.
func TestReconcileDiffTab_LeavesAFrozenDiffAlone(t *testing.T) {
	a, _ := conflictedApp(t, "from the agent\n")
	a.openBufferVsDisk(0)

	dt := a.tabs[1]
	before := dt.Buffer.String()
	dt.Mtime = dt.Mtime.Add(-time.Hour) // force the "disk is newer" branch

	a.reconcileOpenTabsWithDisk(pollNow(a))

	if got := dt.Buffer.String(); got != before {
		t.Fatalf("a frozen diff should be untouched;\nbefore: %q\nafter:  %q", before, got)
	}
}
