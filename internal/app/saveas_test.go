// =============================================================================
// File: internal/app/saveas_test.go
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chasereyn/vincent/internal/editor"
)

// TestMenuSaveAs_PrefillsCurrentPath checks the prompt opens prefilled
// with wherever the tab is currently saved.
func TestMenuSaveAs_PrefillsCurrentPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "foo.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)

	a.menuSaveAs()
	if !a.promptOpen {
		t.Fatal("menuSaveAs should open the prompt")
	}
	if got := string(a.promptValue); got != target {
		t.Fatalf("prompt should prefill the current path, got %q want %q", got, target)
	}
}

// TestMenuSaveAs_RefusesReadOnlyTab checks a diff tab is refused with a
// flash and never gets a prompt — the same guard every other mutator on
// Tab carries, restated at the app layer.
func TestMenuSaveAs_RefusesReadOnlyTab(t *testing.T) {
	dir := t.TempDir()
	a := newTestApp(t, dir)
	a.tabs = []*editor.Tab{editor.NewDiffTab(filepath.Join(dir, "f.txt"), nil)}
	a.activeTab = 0

	a.menuSaveAs()
	if a.promptOpen {
		t.Fatal("menuSaveAs should refuse a read-only tab, not open the prompt")
	}
	if a.statusMsg == "" {
		t.Fatal("refusing should flash a message")
	}
}

// TestSaveActiveTabAs_WritesNewFileDirectly checks the no-conflict path:
// a target that doesn't exist yet is written straight away, no confirm.
func TestSaveActiveTabAs_WritesNewFileDirectly(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(oldPath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(oldPath)

	newPath := filepath.Join(dir, "new.txt")
	a.saveActiveTabAs(newPath)

	if a.confirmOpen {
		t.Fatal("a non-existent target should not trigger the overwrite confirm")
	}
	got, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("read new path: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("new file contents = %q", got)
	}
	if a.activeTabPtr().Path != newPath {
		t.Fatalf("active tab should now point at the new path, got %q", a.activeTabPtr().Path)
	}
}

// TestSaveActiveTabAs_AsksBeforeOverwriting checks the confirm-then-write
// path: an existing target opens the Yes/No modal instead of writing
// immediately, and the write only happens after confirmYes.
func TestSaveActiveTabAs_AsksBeforeOverwriting(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(oldPath, []byte("mine"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	existingPath := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(existingPath, []byte("theirs"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(oldPath)

	a.saveActiveTabAs(existingPath)
	if !a.confirmOpen {
		t.Fatal("an existing target should open the overwrite confirm")
	}
	got, _ := os.ReadFile(existingPath)
	if string(got) != "theirs" {
		t.Fatalf("nothing should be written before confirming, got %q", got)
	}

	a.confirmYes()
	got, err := os.ReadFile(existingPath)
	if err != nil {
		t.Fatalf("read after confirm: %v", err)
	}
	if string(got) != "mine" {
		t.Fatalf("confirming should overwrite with the buffer, got %q", got)
	}
	if a.activeTabPtr().Path != existingPath {
		t.Fatalf("tab should now point at the overwritten path, got %q", a.activeTabPtr().Path)
	}
}

// TestSaveActiveTabAs_DeclineLeavesEverythingAlone checks confirmCancel:
// declining the overwrite must leave both files and the tab untouched.
func TestSaveActiveTabAs_DeclineLeavesEverythingAlone(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(oldPath, []byte("mine"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	existingPath := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(existingPath, []byte("theirs"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(oldPath)

	a.saveActiveTabAs(existingPath)
	a.confirmCancel()

	got, _ := os.ReadFile(existingPath)
	if string(got) != "theirs" {
		t.Fatalf("declining should leave the existing file alone, got %q", got)
	}
	if a.activeTabPtr().Path != oldPath {
		t.Fatalf("declining should leave the tab pointed at its old path, got %q", a.activeTabPtr().Path)
	}
}

// TestWriteTabAs_RefreshesActiveFolderAndFlashes checks the surrounding
// UI bookkeeping writeTabAs is responsible for beyond the write itself.
func TestWriteTabAs_RefreshesActiveFolderAndFlashes(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(oldPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(oldPath)

	newPath := filepath.Join(sub, "new.txt")
	a.writeTabAs(a.activeTab, newPath)

	if a.activeFolder != sub {
		t.Fatalf("activeFolder = %q, want %q", a.activeFolder, sub)
	}
	if a.tree.ActiveFile != newPath {
		t.Fatalf("tree.ActiveFile = %q, want %q", a.tree.ActiveFile, newPath)
	}
	if a.statusMsg == "" {
		t.Fatal("writeTabAs should flash a confirmation")
	}
}
