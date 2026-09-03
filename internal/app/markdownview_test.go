// =============================================================================
// File: internal/app/markdownview_test.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// Tests for the app-side half of the markdown viewer: opening a .md file
// rendered by default, the Esc-m toggle, disk reconciliation, and the
// status bar's rendered/raw label.

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestOpenFile_MarkdownOpensRendered proves a .md file opens in rendered
// mode by default — editor.NewTab's own dispatch, exercised through the
// app's real open path rather than asserted against the editor package
// directly.
func TestOpenFile_MarkdownOpensRendered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "README.md")
	if err := os.WriteFile(path, []byte("# Title\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(path)

	tab := a.activeTabPtr()
	if tab == nil {
		t.Fatal("expected an active tab after opening README.md")
	}
	if !tab.IsMarkdown() {
		t.Fatal("a .md file should open rendered by default")
	}
	if !tab.ReadOnly() {
		t.Fatal("a rendered markdown tab must be read-only")
	}
}

// TestMenuToggleMarkdownView_SwapsRenderedAndRaw exercises the Esc-m path
// end to end through the app layer: same tab slot, same active index,
// Mode flips both ways.
func TestMenuToggleMarkdownView_SwapsRenderedAndRaw(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte("# Notes\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(path)
	idx := a.activeTab

	a.menuToggleMarkdownView()
	if a.activeTab != idx {
		t.Fatalf("active tab index changed: got %d, want %d", a.activeTab, idx)
	}
	if a.tabs[idx].IsMarkdown() {
		t.Fatal("expected raw mode after one toggle")
	}
	if a.tabs[idx].Path != path {
		t.Fatal("Path must survive the toggle")
	}

	a.menuToggleMarkdownView()
	if !a.tabs[idx].IsMarkdown() {
		t.Fatal("expected rendered mode after a second toggle")
	}
}

// TestMenuToggleMarkdownView_NonMarkdownTabIsNoOp proves the leader
// binding does nothing harmful when the active tab isn't markdown at
// all — no panic, no mode change.
func TestMenuToggleMarkdownView_NonMarkdownTabIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(path)

	a.menuToggleMarkdownView() // must not panic
	if a.tabs[0].IsMarkdown() {
		t.Fatal("a .go tab must never become markdown")
	}
}

// TestMenuToggleMarkdownView_NoActiveTabIsNoOp proves the nil-tab guard:
// pressing Esc m with no tab open does nothing.
func TestMenuToggleMarkdownView_NoActiveTabIsNoOp(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.menuToggleMarkdownView() // must not panic
}

// markdownReconcileFixture seeds a repo with one markdown file, opens it
// (rendered), and returns the app plus the file's path — mirroring
// conflictFixture's shape in app_test.go for the equivalent diff-tab
// reconcile tests.
func markdownReconcileFixture(t *testing.T, initial string) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(target, []byte(initial), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	a.statusMsg = ""
	return a, target
}

// TestReconcileMarkdownTab_PicksUpAgentWrite proves the rendered tab
// re-renders the new content when the file changes on disk — the
// markdown counterpart to reconcileDiffTab's own "tracks a running
// agent" behaviour.
func TestReconcileMarkdownTab_PicksUpAgentWrite(t *testing.T) {
	a, target := markdownReconcileFixture(t, "# One\n")
	tab := a.activeTabPtr()
	if got := tab.MarkdownSource; got != "# One\n" {
		t.Fatalf("MarkdownSource = %q, want %q", got, "# One\n")
	}

	if err := os.WriteFile(target, []byte("# Two\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	// Backdate Mtime the same way conflictFixture does, so the poll's
	// mtime reads as newer without a real filesystem-timestamp sleep.
	tab.Mtime = tab.Mtime.Add(-time.Hour)

	a.reconcileOpenTabsWithDisk(pollNow(a))

	if got := tab.MarkdownSource; got != "# Two\n" {
		t.Fatalf("MarkdownSource after reconcile = %q, want %q", got, "# Two\n")
	}
	if !tab.IsMarkdown() {
		t.Fatal("reconcile must not change Mode")
	}
}

// TestReconcileMarkdownTab_UnchangedFileIsQuiet proves an unrelated
// reconcile tick (nothing changed on disk) leaves the tab and the status
// message alone — no flash, no re-render churn.
func TestReconcileMarkdownTab_UnchangedFileIsQuiet(t *testing.T) {
	a, _ := markdownReconcileFixture(t, "# One\n")
	tab := a.activeTabPtr()
	before := tab.MarkdownSource

	a.reconcileOpenTabsWithDisk(pollNow(a))

	if tab.MarkdownSource != before {
		t.Fatal("an unchanged file must not trigger a re-render")
	}
	if a.statusMsg != "" {
		t.Fatalf("unexpected flash on a no-op reconcile: %q", a.statusMsg)
	}
}

// TestReconcileMarkdownTab_DeletedFlashesOnce proves a deletion is
// announced exactly once, not re-flashed on every subsequent tick — the
// same DiskGone latch reconcileOpenTabsWithDisk's text-tab path uses.
func TestReconcileMarkdownTab_DeletedFlashesOnce(t *testing.T) {
	a, target := markdownReconcileFixture(t, "# One\n")
	tab := a.activeTabPtr()

	if err := os.Remove(target); err != nil {
		t.Fatalf("remove: %v", err)
	}
	a.reconcileOpenTabsWithDisk(pollNow(a))
	if !tab.DiskGone {
		t.Fatal("expected DiskGone after the file was removed")
	}
	if !strings.Contains(a.statusMsg, "deleted on disk") {
		t.Fatalf("status message = %q, want it to mention the deletion", a.statusMsg)
	}

	a.statusMsg = ""
	a.reconcileOpenTabsWithDisk(pollNow(a))
	if a.statusMsg != "" {
		t.Fatalf("second tick re-flashed: %q", a.statusMsg)
	}
}

// TestDrawStatusBar_MarkdownShowsRenderedOrRaw pins the status text the
// phase spec asks for by name: "rendered" for the markdown tab, "raw" for
// the same file toggled to its editable text view.
func TestDrawStatusBar_MarkdownShowsRenderedOrRaw(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(path, []byte("# Title\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(path)
	a.statusMsg = "" // the "Opened <file>" flash would otherwise win.
	a.statusUntil = time.Time{}

	a.draw()
	a.screen.Show()
	if got := screenText(a); !strings.Contains(got, "rendered") {
		t.Fatalf("status bar = %q, want it to mention %q", got, "rendered")
	}

	a.menuToggleMarkdownView()
	a.draw()
	a.screen.Show()
	if got := screenText(a); !strings.Contains(got, "raw") {
		t.Fatalf("status bar after toggle = %q, want it to mention %q", got, "raw")
	}
}
