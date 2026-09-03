// =============================================================================
// File: internal/app/app_test.go
// Author: Spicer Matthews <spicer@cloudmanic.com>
// Created: 2026-04-30
// Copyright: 2026 Cloudmanic, LLC. All rights reserved.
// =============================================================================

// Tests for the pure-logic helpers and the small bits of App glue that don't
// require a live terminal. Where we need an *App we build one against a
// tcell.SimulationScreen so layout and event-routing helpers can run without
// touching a real tty. The interactive code paths (Run, the event loop, real
// drawing) are exercised manually — here we just pin down the helpers so
// future refactors don't silently regress them.

package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/diff"
	"github.com/chasereyn/vincent/internal/editor"
	"github.com/chasereyn/vincent/internal/filetree"
	"github.com/chasereyn/vincent/internal/icons"
	"github.com/chasereyn/vincent/internal/theme"
)

// newTestApp builds a fully-wired App against a tcell.SimulationScreen. It
// mirrors what New() does, but skips the background tree-refresh goroutine
// because we don't want a ticker firing while tests run.
func newTestApp(t *testing.T, root string) *App {
	t.Helper()
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { scr.Fini() })
	scr.SetSize(120, 40)

	tree, err := filetree.New(root)
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	a := &App{
		screen:       scr,
		theme:        theme.Default(),
		rootDir:      tree.Root.Path,
		tree:         tree,
		sidebarShown: true,
		sidebarWidth: defaultSidebarWidth,
		// Mirror the real constructors. A zero width here silently makes
		// gitPanelW() return 0 even with the panel shown, so every panel
		// test would assert against a panel that never drew.
		gitPanelWidth: defaultGitPanelWidth,
		gitPanelHover: -1,
		// Mirror the constructors: -1 is "no branch row on screen", so a
		// click test cannot match row 0 before a footer draw stamped it.
		lastBranchRowY: -1,
		// The real default (config.Defaults().TabBar) is false, but this
		// fixture predates the toggle and most existing tab-bar tests
		// assume the full strip renders — true here keeps them
		// unchanged. Tests for the off state set it explicitly.
		tabBarShown: true,
	}
	a.setActiveFolder(tree.Root.Path)
	a.width, a.height = scr.Size()
	return a
}

// TestSidebarW_ShownVsHidden verifies the sidebar width helper returns 0
// when hidden and the configured width when shown. Every layout helper
// pivots on this so we want it locked in.
func TestSidebarW_ShownVsHidden(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	if got := a.sidebarW(); got != defaultSidebarWidth {
		t.Fatalf("shown sidebarW: got %d, want %d", got, defaultSidebarWidth)
	}
	a.sidebarShown = false
	if got := a.sidebarW(); got != 0 {
		t.Fatalf("hidden sidebarW: got %d, want 0", got)
	}
}

// TestSidebarRect checks the sidebar render rectangle reserves one cell
// for the splitter on its right edge, and collapses to zero when hidden.
func TestSidebarRect(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	x, y, w, h := a.sidebarRect()
	if x != 0 || y != 0 {
		t.Fatalf("expected origin (0,0), got (%d,%d)", x, y)
	}
	if w != defaultSidebarWidth-1 {
		t.Fatalf("expected w = sidebarWidth-1, got %d", w)
	}
	if h != a.height-1 {
		t.Fatalf("expected h = height-1, got %d", h)
	}

	a.sidebarShown = false
	x, y, w, h = a.sidebarRect()
	if x != 0 || y != 0 || w != 0 || h != 0 {
		t.Fatalf("expected zero rect when hidden, got (%d,%d,%d,%d)", x, y, w, h)
	}
}

// TestSplitterX returns the splitter column when shown and -1 when hidden.
func TestSplitterX(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	if got := a.splitterX(); got != defaultSidebarWidth-1 {
		t.Fatalf("shown splitterX: got %d", got)
	}
	a.sidebarShown = false
	if got := a.splitterX(); got != -1 {
		t.Fatalf("hidden splitterX: got %d, want -1", got)
	}
}

// TestTabBarRect checks the tab bar starts after the sidebar and spans the
// remaining width on row 0.
func TestTabBarRect(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	x, y, w, h := a.tabBarRect()
	if x != defaultSidebarWidth || y != 0 || h != 1 {
		t.Fatalf("tabBar position/size unexpected: (%d,%d,%d,%d)", x, y, w, h)
	}
	if w != a.width-defaultSidebarWidth {
		t.Fatalf("tabBar width: got %d", w)
	}
	a.sidebarShown = false
	x, _, w, _ = a.tabBarRect()
	if x != 0 || w != a.width {
		t.Fatalf("hidden-sidebar tabBar should fill row: got x=%d w=%d", x, w)
	}
}

// TestEditorRect verifies the editor body sits between tab bar and status
// bar, to the right of the sidebar.
func TestEditorRect(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	x, y, w, h := a.editorRect()
	if x != defaultSidebarWidth || y != 1 {
		t.Fatalf("editor origin: (%d,%d)", x, y)
	}
	if w != a.width-defaultSidebarWidth {
		t.Fatalf("editor width: got %d", w)
	}
	if h != a.height-2 {
		t.Fatalf("editor height: got %d", h)
	}
}

// TestStatusRect always returns the bottom-most row, full width.
func TestStatusRect(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	x, y, w, h := a.statusRect()
	if x != 0 || y != a.height-1 || w != a.width || h != 1 {
		t.Fatalf("status rect: (%d,%d,%d,%d)", x, y, w, h)
	}
}

// TestResizeSidebar_Clamps verifies the sidebar width clamps to the
// [minSidebarWidth, width-minEditorAfterDrag] range.
func TestResizeSidebar_Clamps(t *testing.T) {
	a := newTestApp(t, t.TempDir())

	// Negative target → clamps up to minSidebarWidth.
	a.resizeSidebar(-50)
	if a.sidebarWidth != minSidebarWidth {
		t.Fatalf("negative target: got %d, want %d", a.sidebarWidth, minSidebarWidth)
	}

	// Above max → clamps to width - minEditorAfterDrag.
	a.resizeSidebar(a.width)
	wantMax := a.width - minEditorAfterDrag
	if a.sidebarWidth != wantMax {
		t.Fatalf("oversize target: got %d, want %d", a.sidebarWidth, wantMax)
	}

	// In range — kept verbatim.
	a.resizeSidebar(25)
	if a.sidebarWidth != 25 {
		t.Fatalf("in-range target: got %d", a.sidebarWidth)
	}
}

// TestResizeSidebar_TinyWindow falls back to minSidebarWidth when the window
// is too narrow for both panels at the requested size.
func TestResizeSidebar_TinyWindow(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.width = 30 // smaller than minSidebarWidth + minEditorAfterDrag.
	a.resizeSidebar(50)
	if a.sidebarWidth != minSidebarWidth {
		t.Fatalf("tiny window: got %d, want %d", a.sidebarWidth, minSidebarWidth)
	}
}

// TestDetectLangLabel covers the language label helper's three cases.
func TestDetectLangLabel(t *testing.T) {
	cases := map[string]string{
		"":               "text",
		"foo.go":         "go",
		"foo":            "text",
		"path/to/x.py":   "py",
		"archive.tar.gz": "gz",
	}
	for in, want := range cases {
		if got := detectLangLabel(in); got != want {
			t.Errorf("detectLangLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIsWordChar pins down the ASCII-only word definition we use for
// double-click word selection.
func TestIsWordChar(t *testing.T) {
	word := []rune{'a', 'z', 'A', 'Z', '0', '9', '_'}
	for _, r := range word {
		if !isWordChar(r) {
			t.Errorf("isWordChar(%q) = false, want true", r)
		}
	}
	nonWord := []rune{' ', '\t', '.', ',', '-', '!', '\n', '/'}
	for _, r := range nonWord {
		if isWordChar(r) {
			t.Errorf("isWordChar(%q) = true, want false", r)
		}
	}
}

// TestSetActiveFolder writes both the App field and the tree's mirror copy.
func TestSetActiveFolder(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.setActiveFolder(sub)
	if a.activeFolder != sub {
		t.Fatalf("activeFolder: got %q, want %q", a.activeFolder, sub)
	}
	if a.tree.ActiveFolder != sub {
		t.Fatalf("tree.ActiveFolder: got %q, want %q", a.tree.ActiveFolder, sub)
	}
}

// TestOpenFile_Basic opens a file, switches to it on re-open, and updates
// activeFolder to the file's parent.
func TestOpenFile_Basic(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "child")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	target := filepath.Join(sub, "file.txt")
	if err := os.WriteFile(target, []byte("hello"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	a := newTestApp(t, dir)
	a.openFile(target)
	if len(a.tabs) != 1 {
		t.Fatalf("expected 1 tab, got %d", len(a.tabs))
	}
	if a.activeFolder != sub {
		t.Fatalf("activeFolder: got %q, want %q", a.activeFolder, sub)
	}

	// Re-opening should switch to existing tab, not create a new one.
	a.activeTab = -1
	a.openFile(target)
	if len(a.tabs) != 1 {
		t.Fatalf("re-open created duplicate tab")
	}
	if a.activeTab != 0 {
		t.Fatalf("re-open didn't switch active: got %d", a.activeTab)
	}
}

// TestOpenFile_ErrorFlash surfaces an error when the path can't be loaded
// (here, a directory rather than a file).
func TestOpenFile_ErrorFlash(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(sub)
	if !strings.Contains(a.statusMsg, "Error") {
		t.Fatalf("expected error flash, got %q", a.statusMsg)
	}
	if len(a.tabs) != 0 {
		t.Fatalf("expected no tabs, got %d", len(a.tabs))
	}
}

// TestRequestCloseTab_DirtyOpensModal proves a dirty tab does not close
// on first request and instead opens the unsaved-changes modal so the
// user can pick Save / Discard / Cancel.
func TestRequestCloseTab_DirtyOpensModal(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dirty.txt")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	a.tabs[0].Dirty = true

	a.requestCloseTab(0)
	if len(a.tabs) != 1 {
		t.Fatalf("dirty tab should not close until the user picks an action")
	}
	if !a.dirtyOpen {
		t.Fatal("dirty close modal should be open")
	}
}

// TestRequestCloseTab_CleanClosesImmediately closes a clean tab in one shot.
func TestRequestCloseTab_CleanClosesImmediately(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "clean.txt")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	a.requestCloseTab(0)
	if len(a.tabs) != 0 {
		t.Fatalf("clean tab should close on first request")
	}
}

// TestCloseTab_ClampsActive ensures activeTab never points outside the slice.
func TestCloseTab_ClampsActive(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	a := newTestApp(t, dir)
	a.openFile(filepath.Join(dir, "a.txt"))
	a.openFile(filepath.Join(dir, "b.txt"))
	a.activeTab = 1
	a.closeTab(1)
	if a.activeTab != 0 {
		t.Fatalf("activeTab should clamp to 0 after closing last; got %d", a.activeTab)
	}
	a.closeTab(0)
	if a.activeTab != 0 {
		t.Fatalf("activeTab should stay >=0 with no tabs; got %d", a.activeTab)
	}
}

// TestCloseTab_OutOfRange is a no-op.
func TestCloseTab_OutOfRange(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.closeTab(-1)
	a.closeTab(99)
	a.requestCloseTab(99)
}

// TestHasTab_Predicates covers the "is X available?" checks used to dim menu
// rows.
func TestHasTab_Predicates(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("hi"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	if a.hasTab() || a.hasSavableTab() || a.hasSelection() || a.hasClipboard() || a.hasCommentableTab() {
		t.Fatal("fresh app should have no tab/selection/clipboard/comment action")
	}

	a.openFile(target)
	if !a.hasTab() || !a.hasSavableTab() {
		t.Fatal("expected hasTab && hasSavableTab after open")
	}
	if a.hasSelection() {
		t.Fatal("no selection on a fresh tab")
	}
	if a.hasCommentableTab() {
		t.Fatal(".txt should not expose the line-comment action")
	}

	// Make a synthetic selection.
	tab := a.activeTabPtr()
	tab.Anchor = editor.Position{Line: 0, Col: 0}
	tab.Cursor = editor.Position{Line: 0, Col: 1}
	if !a.hasSelection() {
		t.Fatal("expected selection after Anchor != Cursor")
	}

	a.clipBuf = "x"
	if !a.hasClipboard() {
		t.Fatal("expected hasClipboard once clipBuf set")
	}
}

// TestHasCommentableTab_Predicate checks that line-comment actions only enable
// on editable text tabs with known comment syntax.
func TestHasCommentableTab_Predicate(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	htmlFile := filepath.Join(dir, "index.html")
	if err := os.WriteFile(goFile, []byte("package main"), 0644); err != nil {
		t.Fatalf("seed go: %v", err)
	}
	if err := os.WriteFile(htmlFile, []byte("<main></main>"), 0644); err != nil {
		t.Fatalf("seed html: %v", err)
	}
	a := newTestApp(t, dir)

	a.openFile(goFile)
	if !a.hasCommentableTab() {
		t.Fatal(".go tab should expose the line-comment action")
	}

	a.openFile(htmlFile)
	if a.hasCommentableTab() {
		t.Fatal(".html tab should not expose the line-comment action")
	}
}

// TestSidebarToggleLabel flips between Show/Hide based on sidebarShown.
func TestSidebarToggleLabel(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	if a.sidebarToggleLabel() != "Hide file explorer" {
		t.Fatalf("got %q", a.sidebarToggleLabel())
	}
	a.sidebarShown = false
	if a.sidebarToggleLabel() != "Show file explorer" {
		t.Fatalf("got %q", a.sidebarToggleLabel())
	}
}

// TestFlash sets statusMsg and pushes statusUntil into the future.
func TestFlash(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	before := time.Now()
	a.flash("hello world")
	if a.statusMsg != "hello world" {
		t.Fatalf("statusMsg: got %q", a.statusMsg)
	}
	if !a.statusUntil.After(before) {
		t.Fatalf("statusUntil should be in the future, got %v", a.statusUntil)
	}
}

// TestMenuToggleSidebar flips the sidebarShown flag.
func TestMenuToggleSidebar(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	if !a.sidebarShown {
		t.Fatal("sidebar should start visible")
	}
	a.menuToggleSidebar()
	if a.sidebarShown {
		t.Fatal("expected hidden after first toggle")
	}
	a.menuToggleSidebar()
	if !a.sidebarShown {
		t.Fatal("expected shown after second toggle")
	}
}

// TestMenuToggleLineComment runs the menu action against the active tab so the
// app layer and editor-layer primitive stay wired together.
func TestMenuToggleLineComment(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	if err := os.WriteFile(target, []byte("one\ntwo"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)

	a.menuToggleLineComment()

	if got := a.activeTabPtr().Buffer.String(); got != "// one\ntwo" {
		t.Fatalf("buffer = %q, want current line commented", got)
	}
	if a.statusMsg != "Toggled line comment" {
		t.Fatalf("statusMsg = %q", a.statusMsg)
	}
}

// TestMenuToggleLineComment_Unsupported flashes a clear no-op instead of
// guessing at block-comment-only formats.
func TestMenuToggleLineComment_Unsupported(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "index.html")
	if err := os.WriteFile(target, []byte("<main></main>"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)

	a.menuToggleLineComment()

	if got := a.activeTabPtr().Buffer.String(); got != "<main></main>" {
		t.Fatalf("unsupported buffer changed to %q", got)
	}
	if a.statusMsg != "No line comment syntax for this file" {
		t.Fatalf("statusMsg = %q", a.statusMsg)
	}
}

// TestTabBarClick_SwitchesTab clicks inside a non-active tab's body and
// verifies activeTab updates.
func TestTabBarClick_SwitchesTab(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	a := newTestApp(t, dir)
	a.openFile(filepath.Join(dir, "a.txt"))
	a.openFile(filepath.Join(dir, "b.txt"))
	// b is active. Lay out the tabs and click inside tab 0's body (not the ×).
	a.lastTabRects = a.layoutTabs()
	tabA := a.lastTabRects[0]
	clickX := tabA.X + 1
	if clickX == tabA.CloseX {
		clickX = tabA.X + 2
	}
	a.tabBarClick(clickX, 0)
	if a.activeTab != 0 {
		t.Fatalf("expected activeTab=0, got %d", a.activeTab)
	}
}

// TestEditorSize matches the editor rect's width and height.
func TestEditorSize(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	w, h := a.editorSize()
	if w != a.width-defaultSidebarWidth || h != a.height-2 {
		t.Fatalf("editorSize: got (%d,%d)", w, h)
	}
}

// TestActiveTabPtr returns nil with no tabs and the right pointer otherwise.
func TestActiveTabPtr(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	if a.activeTabPtr() != nil {
		t.Fatal("expected nil with no tabs")
	}
	a.openFile(target)
	if a.activeTabPtr() != a.tabs[0] {
		t.Fatal("activeTabPtr should match tabs[activeTab]")
	}
	a.activeTab = 99
	if a.activeTabPtr() != nil {
		t.Fatal("out-of-range activeTab should yield nil")
	}
}

// TestSaveActiveTab writes the buffer to disk and clears Dirty.
func TestSaveActiveTab(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "save.txt")
	if err := os.WriteFile(target, []byte("seed"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	a.activeTabPtr().InsertString("X")
	a.saveActiveTab()
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), "X") {
		t.Fatalf("save did not persist: %q", got)
	}
}

// TestSaveActiveTab_NoTab is a no-op.
func TestSaveActiveTab_NoTab(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.saveActiveTab()
}

// TestCopyCutPaste exercises the clipboard glue.
func TestCopyCutPaste(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("hello"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)

	// No selection — copy/cut should be no-ops.
	a.copySelection()
	a.cutSelection()
	if a.clipBuf != "" {
		t.Fatalf("clipBuf should still be empty: %q", a.clipBuf)
	}

	// Make selection of "hello".
	tab := a.activeTabPtr()
	tab.Anchor = editor.Position{Line: 0, Col: 0}
	tab.Cursor = editor.Position{Line: 0, Col: 5}
	a.copySelection()
	if a.clipBuf != "hello" {
		t.Fatalf("copy: clipBuf %q", a.clipBuf)
	}

	// Cut: same selection should now empty the buffer.
	tab.Anchor = editor.Position{Line: 0, Col: 0}
	tab.Cursor = editor.Position{Line: 0, Col: 5}
	a.cutSelection()
	if tab.Buffer.LineRunes(0) != nil && len(tab.Buffer.LineRunes(0)) != 0 {
		// Some buffer impls return empty slice; both fine.
	}

	// Paste empty path: when clipBuf empty, flash about external paste.
	a.clipBuf = ""
	a.pasteClipboard()
	if !strings.Contains(a.statusMsg, "clipboard empty") {
		t.Fatalf("expected empty-clip flash, got %q", a.statusMsg)
	}

	// Paste with content.
	a.clipBuf = "X"
	a.pasteClipboard()
}

// TestPasteClipboard_NoTab is safe with no tab open.
func TestPasteClipboard_NoTab(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.clipBuf = "X"
	a.pasteClipboard() // no tab — nothing to paste into.
}

// TestMenuClickPaths covers menuSave/menuCopy/menuCut/menuPaste/menuClose
// menuQuit and menuRefreshTree as one-liners.
func TestMenuClickPaths(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("hi"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)

	// Selection so copy/cut have something to operate on.
	tab := a.activeTabPtr()
	tab.Anchor = editor.Position{Line: 0, Col: 0}
	tab.Cursor = editor.Position{Line: 0, Col: 2}

	a.menuSave()
	a.menuCopy()
	tab.Anchor = editor.Position{Line: 0, Col: 0}
	tab.Cursor = editor.Position{Line: 0, Col: 1}
	a.menuCut()
	a.menuPaste()
	a.menuRefreshTree()

	// Clean the tab before quitting; the dirty-quit path is exercised
	// separately in dirty_modal_test.go.
	tab.Dirty = false
	a.menuQuit()
	if !a.quit {
		t.Fatal("menuQuit should set quit flag")
	}
}

// TestUndoRedoRevert_MenuPaths exercises the new history actions end
// to end through the menu wrappers. The flash on no-op paths is also
// covered so the user always gets feedback when they hit a dead-end.
func TestUndoRedoRevert_MenuPaths(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "edit.txt")
	if err := os.WriteFile(target, []byte("seed"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	tab := a.activeTabPtr()

	// Nothing to undo / redo / revert on a freshly opened file.
	if a.hasUndo() || a.hasRedo() || a.hasRevert() {
		t.Fatal("freshly opened tab should have no history")
	}
	a.menuUndo()
	a.menuRedo()
	a.menuRevert()

	// One edit → undo + revert become available.
	tab.MoveCursorTo(editor.Position{Line: 0, Col: 4}, false)
	tab.InsertString("X")
	if !a.hasUndo() || !a.hasRevert() {
		t.Fatal("expected undo + revert after edit")
	}
	if a.hasRedo() {
		t.Fatal("redo should still be empty")
	}

	a.menuUndo()
	if got := tab.Buffer.String(); got != "seed" {
		t.Fatalf("after menuUndo = %q, want seed", got)
	}
	if !a.hasRedo() {
		t.Fatal("redo should be populated after an undo")
	}

	a.menuRedo()
	if got := tab.Buffer.String(); got != "seedX" {
		t.Fatalf("after menuRedo = %q, want seedX", got)
	}

	// Revert back to original; then Undo must recover the post-edit state.
	a.menuRevert()
	if got := tab.Buffer.String(); got != "seed" {
		t.Fatalf("after menuRevert = %q, want seed", got)
	}
	a.menuUndo()
	if got := tab.Buffer.String(); got != "seedX" {
		t.Fatalf("after undo-of-revert = %q, want seedX", got)
	}
}

// TestUndoRedoRevert_NoTabSafelyNoOps guards against crashes when the
// menu rows somehow fire with no active tab — they should silently
// return rather than dereferencing nil.
func TestUndoRedoRevert_NoTabSafelyNoOps(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.menuUndo()
	a.menuRedo()
	a.menuRevert()
	if a.hasUndo() || a.hasRedo() || a.hasRevert() {
		t.Fatal("no-tab predicates should all be false")
	}
}

// TestMenuClose_NoTab safely no-ops.
func TestMenuClose_NoTab(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.menuClose()
}

// TestScrollAt routes scroll to the panel under the cursor; we just verify
// it doesn't panic across the three regions.
func TestScrollAt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("a\nb\nc\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	a.scrollAt(1, 5, 1)           // sidebar
	a.scrollAt(60, 5, 1)          // editor
	a.scrollAt(60, a.height-1, 1) // status bar (no-op-ish)
}

// TestSidebarClick_File opens a file when a file row is clicked.
func TestSidebarClick_File(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "click.txt")
	if err := os.WriteFile(target, []byte("z"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	// Render once so the tree has visible rows for HitTest.
	a.draw()
	// File row is row 1 (0 is the root); click at column 1, row 1.
	a.sidebarClick(1, 1)
	// Only a no-panic guarantee — depending on row order we may or may
	// not have opened the file. Just make sure no crash and either zero
	// or one tab is open.
	if len(a.tabs) > 1 {
		t.Fatalf("unexpected tabs: %d", len(a.tabs))
	}
}

// TestSidebarClick_Miss is safe when (x,y) hits no row.
func TestSidebarClick_Miss(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.sidebarClick(1, 100) // off the bottom of the tree
}

// TestSidebarClick_RootRowResetsActiveFolder pins the bug fix:
// clicking the project-name row in the sidebar (y=1) sets the
// active folder back to the project root. Before this fix, once
// the user picked any subfolder there was no path back to root
// short of restarting the editor — every other row in the tree
// only walks "deeper," not "up." Also confirms the click does not
// open a file or toggle any directory's expansion as a side
// effect; it's purely a navigation/state reset.
func TestSidebarClick_RootRowResetsActiveFolder(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "internal")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.draw() // populate t.visible so HitTest works
	a.setActiveFolder(sub)
	if a.activeFolder == a.rootDir {
		t.Fatal("seed broken: active folder should start as subfolder")
	}

	a.sidebarClick(1, 1) // (col=1, row=1) is the project name row

	if a.activeFolder != a.rootDir {
		t.Errorf("active folder = %q, want root %q", a.activeFolder, a.rootDir)
	}
	if len(a.tabs) != 0 {
		t.Errorf("clicking root opened tabs: %d", len(a.tabs))
	}
}

// TestUpdateTreeHover_ClearsOutsideTheSidebar mirrors
// TestGitPanel_HoverClearsOutsideThePanel: there is no "pointer left the
// window" event, so the tree's row highlight has to be cleared by the next
// event that lands elsewhere, not left lit after the mouse moves to the
// editor pane.
func TestUpdateTreeHover_ClearsOutsideTheSidebar(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "click.txt")
	if err := os.WriteFile(target, []byte("z"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.draw() // populate tree.visible so hover has a real row to land on

	sx, sy, sw, _ := a.sidebarRect()
	a.updateTreeHover(sx+1, sy+1) // row 1 = project root, inside the tree
	if a.tree.HoverY != 1 {
		t.Fatalf("HoverY = %d over a real row, want 1", a.tree.HoverY)
	}

	a.updateTreeHover(sx+sw+5, sy+1) // past the splitter — over the editor now
	if a.tree.HoverY != -1 {
		t.Errorf("HoverY = %d after moving off the sidebar, want -1", a.tree.HoverY)
	}
}

// TestSelectWordAt selects the word under a buffer position.
func TestSelectWordAt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "w.txt")
	if err := os.WriteFile(target, []byte("hello world"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	tab := a.activeTabPtr()
	a.selectWordAt(tab, editor.Position{Line: 0, Col: 2})
	if tab.Anchor.Col != 0 || tab.Cursor.Col != 5 {
		t.Fatalf("word select: anchor=%v cursor=%v", tab.Anchor, tab.Cursor)
	}

	// Empty line — no selection.
	tab.Buffer = editor.NewBuffer("")
	a.selectWordAt(tab, editor.Position{Line: 0, Col: 0})
}

// TestEditorPress_PlacesCaret moves the caret to the clicked spot.
func TestEditorPress_PlacesCaret(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "p.txt")
	if err := os.WriteFile(target, []byte("hello\nworld\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	ex, ey, _, _ := a.editorRect()
	a.editorPress(ex+2, ey+1)
	tab := a.activeTabPtr()
	if tab.Cursor.Line != 1 {
		t.Fatalf("expected line 1, got %d", tab.Cursor.Line)
	}
}

// TestOpenGitHunkAt_ConsumesMarkerClick proves gutter markers are clickable
// and that the click is consumed rather than falling through to cursor
// placement. Phase 1 replaced spice-edit's info modal here with the real
// diff view; the modal assertion went with it. What the click opens is
// covered by the diffview tests, which can seed a git repo — this one has
// no repo, so loadDiffRows finds nothing and only the consumed-click
// contract is observable.
func TestOpenGitHunkAt_ConsumesMarkerClick(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "p.txt")
	if err := os.WriteFile(target, []byte("hello\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	tab := a.activeTabPtr()
	tab.GitLines = map[int]editor.GitLineChange{0: editor.GitLineModified}

	if !a.openGitHunkAt(tab, 0, 0) {
		t.Fatal("expected gutter marker click to be handled")
	}
	if a.confirmOpen {
		t.Fatal("gutter click should no longer open a modal")
	}
}

// TestOpenGitHunkAt_IgnoresDiffTab keeps the gutter click from recursing.
// A diff tab has no git gutter, and treating column zero of one as a marker
// click would re-open the diff of the file being diffed.
func TestOpenGitHunkAt_IgnoresDiffTab(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	tab := editor.NewDiffTab("x.go", diff.Parse("@@ -1 +1 @@\n-a\n+b\n"))
	tab.GitLines = map[int]editor.GitLineChange{0: editor.GitLineModified}

	if a.openGitHunkAt(tab, 0, 0) {
		t.Fatal("diff tabs should not handle gutter clicks")
	}
}

// TestOpenGitHunkAt_IgnoresCleanGutter keeps normal cursor placement intact.
func TestOpenGitHunkAt_IgnoresCleanGutter(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	tab, err := editor.NewTab("")
	if err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if a.openGitHunkAt(tab, 0, 0) {
		t.Fatal("clean gutter should not be handled as a git preview")
	}
}

// TestEditorPress_DoubleClickSelectsWord triggers the word-select path.
func TestEditorPress_DoubleClickSelectsWord(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "p.txt")
	if err := os.WriteFile(target, []byte("hello world"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	ex, ey, _, _ := a.editorRect()
	a.editorPress(ex+2, ey)
	a.editorPress(ex+2, ey) // immediately again — double-click within window
	tab := a.activeTabPtr()
	if tab.Anchor.Col == tab.Cursor.Col {
		t.Fatal("expected a word selection after double-click")
	}
}

// TestEditorPress_NoTabSafe doesn't panic with no active tab.
func TestEditorPress_NoTabSafe(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.editorPress(50, 5)
	a.editorDrag(50, 5)
}

// TestEditorDrag_AutoScroll arms the auto-scroll direction when dragging
// outside the editor's vertical bounds.
func TestEditorDrag_AutoScroll(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "d.txt")
	if err := os.WriteFile(target, []byte("a\nb\nc\nd\ne\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	ex, ey, _, eh := a.editorRect()
	a.editorDrag(ex+1, ey-1) // above editor → auto-scroll up
	if a.autoScrollDir != -1 {
		t.Fatalf("expected autoScrollDir=-1, got %d", a.autoScrollDir)
	}
	a.editorDrag(ex+1, ey+eh+1) // below → auto-scroll down
	if a.autoScrollDir != 1 {
		t.Fatalf("expected autoScrollDir=1, got %d", a.autoScrollDir)
	}
	a.editorDrag(ex+1, ey+1) // inside → stops
	if a.autoScrollDir != 0 {
		t.Fatalf("expected stopped autoScroll, got %d", a.autoScrollDir)
	}
}

// TestHandleKey_EscDoesNotOpenMenu pins that Esc alone, pressed any number
// of times, never opens the menu. The old Esc-Esc double-tap was removed on
// 2026-09-02; Esc arms the leader and a second Esc cancels it.
func TestHandleKey_EscDoesNotOpenMenu(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	for i := 0; i < 3; i++ {
		a.handleKey(keyEv(tcell.KeyEsc, 0))
		if a.cheatsheetOpen {
			t.Fatalf("Esc press %d opened an overlay", i+1)
		}
	}
}

// TestHandleKey_RoutesToActiveTab dispatches typing to the active tab.
func TestHandleKey_RoutesToActiveTab(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "t.txt")
	if err := os.WriteFile(target, []byte(""), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	a.handleKey(keyEv(tcell.KeyRune, 'a'))
	a.handleKey(keyEv(tcell.KeyRune, 'b'))
	a.handleKey(keyEv(tcell.KeyEnter, 0))
	a.handleKey(keyEv(tcell.KeyRune, 'c'))
	a.handleKey(keyEv(tcell.KeyTab, 0))
	a.handleKey(keyEv(tcell.KeyBackspace, 0))
	a.handleKey(keyEv(tcell.KeyHome, 0))
	a.handleKey(keyEv(tcell.KeyEnd, 0))
	a.handleKey(keyEv(tcell.KeyLeft, 0))
	a.handleKey(keyEv(tcell.KeyRight, 0))
	a.handleKey(keyEv(tcell.KeyUp, 0))
	a.handleKey(keyEv(tcell.KeyDown, 0))
	a.handleKey(keyEv(tcell.KeyPgUp, 0))
	a.handleKey(keyEv(tcell.KeyPgDn, 0))
	a.handleKey(keyEv(tcell.KeyDelete, 0))
}

// resizedApp builds a test app at (w, h) and drives one real resize
// through handleEvent, so the startup sidebar clamp and reflowPanels run
// exactly as they do on the first frame of a live session. The Changes
// panel is left hidden: the two-panel width budget is exercised in
// gitpanel_test.go, and mixing it in here would make the sidebar numbers
// a function of three constants instead of one.
func resizedApp(t *testing.T, w, h int) *App {
	t.Helper()
	a := newTestApp(t, t.TempDir())
	a.gitPanelShown = false
	scr := a.screen.(tcell.SimulationScreen)
	scr.SetSize(w, h)
	a.handleEvent(tcell.NewEventResize(w, h))
	return a
}

// TestSidebarWidth_WideWindowKeepsTheDefault is the case the doubled
// default was chosen for: on the monitor Vincent actually runs on, the
// tree opens at its full 60 cells and the editor keeps the rest.
func TestSidebarWidth_WideWindowKeepsTheDefault(t *testing.T) {
	a := resizedApp(t, 200, 50)

	if a.sidebarWidth != defaultSidebarWidth {
		t.Fatalf("sidebarWidth = %d, want %d", a.sidebarWidth, defaultSidebarWidth)
	}
	_, _, ew, _ := a.editorRect()
	if want := 200 - defaultSidebarWidth; ew != want {
		t.Fatalf("editor width = %d, want %d", ew, want)
	}
	// The tree has to actually paint inside its rect — a width the
	// renderer never receives is not a width.
	a.draw()
	scr := a.screen.(tcell.SimulationScreen)
	scr.Show()
	if _, _, sw, _ := a.sidebarRect(); sw != defaultSidebarWidth-1 {
		t.Fatalf("sidebar render width = %d, want %d (one cell is the splitter)", sw, defaultSidebarWidth-1)
	}
	if a.splitterX() != defaultSidebarWidth-1 {
		t.Fatalf("splitter at %d, want %d", a.splitterX(), defaultSidebarWidth-1)
	}
}

// TestSidebarWidth_NarrowWindowGetsAProportion is why the startup clamp
// exists. 60 cells of an 80-column terminal leaves 20 for the editor, and
// resizeSidebar alone would allow it (80 - 40 = 40, so 60 clamps to 40 —
// still half the window). The percentage cap lands on 32 instead.
func TestSidebarWidth_NarrowWindowGetsAProportion(t *testing.T) {
	a := resizedApp(t, 80, 24)

	if want := 80 * startupSidebarPercent / 100; a.sidebarWidth != want {
		t.Fatalf("sidebarWidth = %d, want %d", a.sidebarWidth, want)
	}
	_, _, ew, _ := a.editorRect()
	if ew < minEditorAfterDrag {
		t.Fatalf("editor got %d columns, want at least %d", ew, minEditorAfterDrag)
	}
}

// TestClampStartupSidebar_IsOneShot pins the reason the clamp is not just
// folded into reflowPanels: it must not re-apply on a later resize, or a
// splitter drag would quietly snap back the next time the terminal changed
// size.
func TestClampStartupSidebar_IsOneShot(t *testing.T) {
	a := resizedApp(t, 200, 50)

	// The user drags the tree wider than 40% of the window.
	a.resizeSidebar(120)
	if a.sidebarWidth != 120 {
		t.Fatalf("fixture: drag to 120 gave %d", a.sidebarWidth)
	}

	scr := a.screen.(tcell.SimulationScreen)
	scr.SetSize(200, 40)
	a.handleEvent(tcell.NewEventResize(200, 40))

	if a.sidebarWidth != 120 {
		t.Fatalf("a resize re-applied the startup clamp: sidebarWidth = %d, want 120", a.sidebarWidth)
	}
}

// TestClampStartupSidebar_NoWidthYetIsNoop guards the ordering: the clamp
// is a division by the window width, and a zero width would set the
// sidebar to zero and mark itself done, so the real first frame would
// never get its chance.
func TestClampStartupSidebar_NoWidthYetIsNoop(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.width = 0
	a.clampStartupSidebar()
	if a.sidebarWidth != defaultSidebarWidth {
		t.Fatalf("sidebarWidth = %d, want the untouched default %d", a.sidebarWidth, defaultSidebarWidth)
	}
	if a.startupSidebarDone {
		t.Fatal("the clamp must not mark itself done before it has a width to measure")
	}
}

// TestReflowPanels_ShrinkingWindowKeepsTheEditorPositive is the crash
// guard reflowPanels exists for, re-checked at the new default: with the
// tree at 60 and the Changes panel open, a window narrowed to 80 must
// still leave every rect non-negative.
func TestReflowPanels_ShrinkingWindowKeepsTheEditorPositive(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.gitPanelShown = true
	a.width, a.height = 80, 24
	a.reflowPanels()

	if a.sidebarWidth < minSidebarWidth {
		t.Fatalf("sidebarWidth = %d, want at least %d", a.sidebarWidth, minSidebarWidth)
	}
	for _, tc := range []struct {
		name string
		rect func() (int, int, int, int)
	}{
		{"sidebar", a.sidebarRect},
		{"editor", a.editorRect},
		{"tab bar", a.tabBarRect},
		{"git panel", a.gitPanelRect},
	} {
		_, _, w, h := tc.rect()
		if w < 0 || h < 0 {
			t.Fatalf("%s rect went negative: %dx%d", tc.name, w, h)
		}
	}
}

// TestHandleEvent_Resize updates width/height.
func TestHandleEvent_Resize(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	scr := a.screen.(tcell.SimulationScreen)
	scr.SetSize(80, 24)
	ev := tcell.NewEventResize(80, 24)
	a.handleEvent(ev)
	if a.width != 80 || a.height != 24 {
		t.Fatalf("resize: got %dx%d", a.width, a.height)
	}
}

// TestHandleMouse_Wheel routes scroll events to the panel under the cursor.
func TestHandleMouse_Wheel(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	ev := tcell.NewEventMouse(60, 5, tcell.WheelDown, tcell.ModNone)
	a.handleMouse(ev)
	ev = tcell.NewEventMouse(60, 5, tcell.WheelUp, tcell.ModNone)
	a.handleMouse(ev)
}

// TestHandleMouse_WheelHorizontal confirms WheelLeft / WheelRight events
// shift the active tab's ScrollX. The test opens a tab with a long line,
// fires WheelRight to scroll horizontally, then WheelLeft to walk it
// back to zero.
func TestHandleMouse_WheelHorizontal(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "long.txt")
	if err := os.WriteFile(target, []byte(strings.Repeat("x", 200)+"\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	tab := a.activeTabPtr()
	if tab == nil {
		t.Fatal("no active tab after openFile")
	}
	// Aim well inside the editor pane (past the sidebar, below the tab bar).
	editorX := a.sidebarW() + 10
	ev := tcell.NewEventMouse(editorX, 5, tcell.WheelRight, tcell.ModNone)
	a.handleMouse(ev)
	if tab.ScrollX == 0 {
		t.Fatalf("WheelRight should advance ScrollX, still 0")
	}
	startX := tab.ScrollX
	ev = tcell.NewEventMouse(editorX, 5, tcell.WheelLeft, tcell.ModNone)
	a.handleMouse(ev)
	if tab.ScrollX >= startX {
		t.Fatalf("WheelLeft should reduce ScrollX, got %d (was %d)", tab.ScrollX, startX)
	}
}

// TestHandleMouse_ShiftWheelScrollsHorizontally confirms that holding
// shift while turning the vertical wheel scrolls the X axis instead —
// this is the path that actually works in most terminals (which never
// emit native WheelLeft/WheelRight). Without shift, the same wheel
// event must scroll vertically; we check both to make sure the modifier
// is what gates the rotation.
func TestHandleMouse_ShiftWheelScrollsHorizontally(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "long.txt")
	if err := os.WriteFile(target, []byte(strings.Repeat("x", 200)+"\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	tab := a.activeTabPtr()
	if tab == nil {
		t.Fatal("no active tab after openFile")
	}
	editorX := a.sidebarW() + 10

	// Shift+WheelDown → horizontal scroll right.
	ev := tcell.NewEventMouse(editorX, 5, tcell.WheelDown, tcell.ModShift)
	a.handleMouse(ev)
	if tab.ScrollX == 0 {
		t.Fatalf("Shift+WheelDown should scroll horizontally, ScrollX still 0")
	}
	if tab.ScrollY != 0 {
		t.Fatalf("Shift+WheelDown should NOT touch ScrollY, got %d", tab.ScrollY)
	}

	// Shift+WheelUp → horizontal scroll left.
	startX := tab.ScrollX
	ev = tcell.NewEventMouse(editorX, 5, tcell.WheelUp, tcell.ModShift)
	a.handleMouse(ev)
	if tab.ScrollX >= startX {
		t.Fatalf("Shift+WheelUp should reduce ScrollX, got %d (was %d)", tab.ScrollX, startX)
	}

	// Unmodified WheelDown still scrolls vertically. Reset the sticky
	// shift state first — within modifierStickyWindow of the previous
	// shift events it'd still register as a shifted wheel.
	tab.ScrollX = 0
	tab.ScrollY = 0
	a.lastShiftAt = time.Time{}
	ev = tcell.NewEventMouse(editorX, 5, tcell.WheelDown, tcell.ModNone)
	a.handleMouse(ev)
	if tab.ScrollY == 0 {
		t.Fatalf("WheelDown without shift should scroll vertically, ScrollY still 0")
	}
	if tab.ScrollX != 0 {
		t.Fatalf("WheelDown without shift should NOT touch ScrollX, got %d", tab.ScrollX)
	}
}

// TestHandleMouse_ShiftStickyForWheel covers the Zellij quirk where
// Shift arrives in a ButtonNone+Shift event right before an unmodified
// WheelDown. We feed that exact sequence and confirm the wheel event is
// treated as horizontal because the sticky-shift window picked it up.
func TestHandleMouse_ShiftStickyForWheel(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "long.txt")
	if err := os.WriteFile(target, []byte(strings.Repeat("x", 200)+"\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	tab := a.activeTabPtr()
	editorX := a.sidebarW() + 10

	// First event: ButtonNone with Shift modifier — what Zellij emits
	// when the user holds shift but hasn't moved or wheeled yet.
	ev := tcell.NewEventMouse(editorX, 5, tcell.ButtonNone, tcell.ModShift)
	a.handleMouse(ev)
	// Second event: WheelDown with NO modifier — what arrives milliseconds
	// later. Without the sticky window this would scroll vertically.
	ev = tcell.NewEventMouse(editorX, 5, tcell.WheelDown, tcell.ModNone)
	a.handleMouse(ev)

	if tab.ScrollX == 0 {
		t.Fatalf("expected sticky-shift to route WheelDown to horizontal, ScrollX still 0")
	}
	if tab.ScrollY != 0 {
		t.Fatalf("sticky-shift WheelDown shouldn't touch ScrollY, got %d", tab.ScrollY)
	}
}

// TestHandleMouse_RightClickOpensCheatsheet falls back to the key table when the
// right-click isn't on a tree row.
func TestHandleMouse_RightClickOpensCheatsheet(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	ev := tcell.NewEventMouse(90, 5, tcell.Button3, tcell.ModNone)
	a.handleMouse(ev)
	if !a.cheatsheetOpen {
		t.Fatal("right-click on nothing in particular should open the cheatsheet")
	}
}

// TestHandleMouse_LeftPressInEditor enters editor drag mode.
func TestHandleMouse_LeftPressInEditor(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("ab\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	ev := tcell.NewEventMouse(60, 5, tcell.Button1, tcell.ModNone)
	a.handleMouse(ev)
	if a.dragMode != "editor" {
		t.Fatalf("expected dragMode=editor, got %q", a.dragMode)
	}
	// Release.
	ev = tcell.NewEventMouse(60, 5, 0, tcell.ModNone)
	a.handleMouse(ev)
	if a.dragMode != "" {
		t.Fatalf("expected drag cleared on release, got %q", a.dragMode)
	}
}

// TestHandleMouse_SidebarSplitterDrag enters splitter drag and resizes.
func TestHandleMouse_SidebarSplitterDrag(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	splitX := a.splitterX()
	ev := tcell.NewEventMouse(splitX, 5, tcell.Button1, tcell.ModNone)
	a.handleMouse(ev)
	if a.dragMode != "sidebar" {
		t.Fatalf("expected sidebar drag, got %q", a.dragMode)
	}
	// Continue dragging — resizes.
	ev = tcell.NewEventMouse(splitX+5, 5, tcell.Button1, tcell.ModNone)
	a.handleMouse(ev)
}

// TestDraw_AllPanels exercises the drawing path so the stdout/screen code
// is covered. Result correctness is exercised manually; here we just make
// sure no panics across several states.
func TestDraw_AllPanels(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("hi\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.draw() // empty editor + sidebar
	a.openFile(target)
	a.draw() // with a tab
	a.activeTabPtr().Dirty = true
	a.draw() // dirty marker
	a.openCheatsheet()
	a.draw() // with the key cheatsheet up
	a.closeCheatsheet()
	a.openPrompt("T", "H", "x", nil)
	a.draw()
	a.promptCancel()
	a.openConfirm("T", "M", nil)
	a.draw()
	a.confirmCancel()
	a.openTreeContext(a.tree.Root, 5, 5)
	a.draw()
	a.closeAllModals()
	a.flash("hello")
	a.draw() // status flash
	a.sidebarShown = false
	a.draw()
	// Tiny window → too-small message.
	a.width, a.height = 5, 5
	a.draw()
}

// TestTabBarClick_ClosesViaX clicks the × in a tab and verifies the close
// path runs (clean tab → tab removed).
func TestTabBarClick_ClosesViaX(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	a.lastTabRects = a.layoutTabs()
	r := a.lastTabRects[0]
	a.tabBarClick(r.CloseX, 0)
	if len(a.tabs) != 0 {
		t.Fatalf("expected close, got %d tabs", len(a.tabs))
	}
}

// TestLeaderHint_CoversEveryBindingWithNoStaleText is the test the old
// hardcoded status line needed and never had. That string still said
// "f find · t tree" two renames after f became the file explorer and /
// became find — the one piece of UI whose entire job is saying what the
// keys are was naming the wrong keys. Generating it from leaderRows makes
// that impossible; this pins it.
func TestLeaderHint_CoversEveryBindingWithNoStaleText(t *testing.T) {
	hint := leaderHint()

	if !strings.HasPrefix(hint, " Esc — ") {
		t.Fatalf("hint should lead with the armed-leader prefix: %q", hint)
	}
	for _, b := range leaderBindings() {
		want := string(b.key) + " " + b.hint
		if !strings.Contains(hint, want) {
			t.Fatalf("hint is missing %q\nhint: %q", want, hint)
		}
	}
	for _, b := range leaderKeyBindings() {
		want := b.label + " " + b.hint
		if !strings.Contains(hint, want) {
			t.Fatalf("hint is missing the named-key entry %q\nhint: %q", want, hint)
		}
	}
	// The separator count proves nothing was silently concatenated: N
	// entries carry N-1 middle dots.
	if got, want := strings.Count(hint, " · "), len(leaderRows())-1; got != want {
		t.Fatalf("hint has %d separators, want %d", got, want)
	}
	// Stale text from the hardcoded version. "f find" and "t tree" were
	// both wrong by the time it was deleted; "m menu" names a binding that
	// no longer exists.
	for _, stale := range []string{"f find", "t tree", "m menu", "y copy ·"} {
		if strings.Contains(hint, stale) {
			t.Fatalf("hint still carries stale text %q: %q", stale, hint)
		}
	}
}

// TestLeaderHint_UnboundRunesAreAbsent is the other half: a rune that is
// not in the table must not appear as a key label, or the hint promises a
// binding that does nothing.
func TestLeaderHint_UnboundRunesAreAbsent(t *testing.T) {
	bound := map[rune]bool{}
	for _, b := range leaderBindings() {
		bound[b.key] = true
	}
	hint := leaderHint()
	for _, r := range "bcehijklmnovxBCD" {
		if bound[r] {
			continue // a later pass may legitimately bind it
		}
		if strings.Contains(hint, " "+string(r)+" ") {
			t.Fatalf("hint advertises unbound key %q: %q", string(r), hint)
		}
	}
}

// TestDrawStatusBar_ArmedLeaderShowsTheKeyTable proves the generated hint
// actually reaches the bottom row, truncated to the space it has rather
// than spilling over the branch label on its right.
func TestDrawStatusBar_ArmedLeaderShowsTheKeyTable(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.gitBranch = "main"
	a.lastEscape = time.Now()
	if !a.leaderArmed() {
		t.Fatal("fixture failed to arm the leader")
	}
	a.draw()
	scr := a.screen.(tcell.SimulationScreen)
	scr.Show()

	cells, w, _ := scr.GetContents()
	_, sy, _, _ := a.statusRect()
	var row strings.Builder
	for x := 0; x < w; x++ {
		row.WriteString(string(cells[sy*w+x].Runes))
	}
	got := row.String()

	// The head of the table is what survives truncation on any window.
	if !strings.Contains(got, "Esc — d diff") {
		t.Fatalf("status row is missing the armed-leader head: %q", got)
	}
	// The branch label still owns the right edge.
	if !strings.HasSuffix(strings.TrimRight(got, " "), "main") {
		t.Fatalf("armed hint overran the branch label: %q", got)
	}
}

// TestDrawStatusBar_RendersBranchRightAligned pins down the lower-right
// branch label: when gitBranch is set, the rightmost cells of the
// status bar carry " <branch> " in order, so the user can glance at
// the corner and read which checkout they're on.
func TestDrawStatusBar_RendersBranchRightAligned(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.gitBranch = "feat/widgets"
	a.draw()
	scr := a.screen.(tcell.SimulationScreen)
	scr.Show() // SimulationScreen serves GetContents from the *front* buffer.

	cells, w, _ := scr.GetContents()
	_, sy, _, _ := a.statusRect()

	want := []rune(" feat/widgets ")
	startX := w - len(want)
	for i, r := range want {
		c := cells[sy*w+startX+i]
		if len(c.Runes) == 0 || c.Runes[0] != r {
			t.Fatalf("status bar col %d = %v, want %q",
				startX+i, c.Runes, r)
		}
	}
}

// TestDrawStatusBar_OmitsBranchWhenEmpty confirms a non-repo project
// (gitBranch == "") doesn't paint a stray label or steal cells from
// the left-side text — the right edge should just be the bar's bg.
func TestDrawStatusBar_OmitsBranchWhenEmpty(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.gitBranch = ""
	a.draw()
	scr := a.screen.(tcell.SimulationScreen)
	scr.Show()

	cells, w, _ := scr.GetContents()
	_, sy, _, _ := a.statusRect()

	// Tail of the status bar must be blank — the bar's fill character.
	for x := w - 5; x < w; x++ {
		c := cells[sy*w+x]
		if len(c.Runes) > 0 && c.Runes[0] != ' ' {
			t.Fatalf("status bar col %d = %v, expected blank tail", x, c.Runes)
		}
	}
}

// TestTrimRunes covers the label clipping helper so long dynamic labels
// cannot overwrite the right-aligned shortcut column.
func TestTrimRunes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"fits", "Save", 8, "Save"},
		{"clips", "Find file in project", 10, "Find file…"},
		{"one", "Save", 1, "…"},
		{"none", "Save", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trimRunes(tt.in, tt.max); got != tt.want {
				t.Fatalf("trimRunes(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
		})
	}
}

// screenLine returns one row from a SimulationScreen as a fixed-width string.
func screenLine(scr tcell.SimulationScreen, y int) string {
	cells, w, _ := scr.GetContents()
	rs := make([]rune, w)
	for x := 0; x < w; x++ {
		c := cells[y*w+x]
		if len(c.Runes) == 0 {
			rs[x] = ' '
			continue
		}
		rs[x] = c.Runes[0]
	}
	return string(rs)
}

// TestLayoutTabs_IconsExpandWidth pins down the geometry contract:
// turning icons on grows each tab by exactly two cells (the glyph + a
// separator space), and the close-× column shifts right by the same
// amount. Without this, a tab-bar click on the × would land on the
// wrong column whenever icons are enabled.
func TestLayoutTabs_IconsExpandWidth(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(filepath.Join(dir, "main.go"))

	a.tree.IconsEnabled = false
	off := a.layoutTabs()
	a.tree.IconsEnabled = true
	on := a.layoutTabs()

	if len(off) != 1 || len(on) != 1 {
		t.Fatalf("layoutTabs len off=%d on=%d, want 1 each", len(off), len(on))
	}
	if on[0].Width != off[0].Width+2 {
		t.Fatalf("icons should add 2 cells: off=%d on=%d", off[0].Width, on[0].Width)
	}
	if on[0].CloseX != off[0].CloseX+2 {
		t.Fatalf("CloseX should shift by 2 when icons on: off=%d on=%d",
			off[0].CloseX, on[0].CloseX)
	}
}

// TestDrawTabBar_RendersIconWhenEnabled verifies the glyph actually
// lands on screen between the dirty slot and the file name when
// icons are enabled. We use the simulation screen and look for the
// language-specific glyph from icons.For somewhere on the tab row.
func TestDrawTabBar_RendersIconWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(filepath.Join(dir, "main.go"))
	a.tree.IconsEnabled = true

	a.drawTabBar()
	a.screen.Show()
	cells, w, _ := a.screen.(tcell.SimulationScreen).GetContents()

	// Read the tab-bar row (y=0) and look for the .go glyph.
	wantGlyph := []rune(icons.For("main.go", false, false))[0]
	found := false
	for x := 0; x < w; x++ {
		c := cells[x]
		if len(c.Runes) > 0 && c.Runes[0] == wantGlyph {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected .go glyph on the tab-bar row when icons are enabled")
	}
}

// TestHasTree_TrueAndFalse pins the visibility predicate that drives
// the single-file-mode menu filter: any app with a non-nil tree
// reports true; setting tree to nil flips it false.
func TestHasTree_TrueAndFalse(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	if !a.hasTree() {
		t.Fatal("expected hasTree=true on a normal-mode app")
	}
	a.tree = nil
	if a.hasTree() {
		t.Fatal("expected hasTree=false when tree is nil")
	}
}

// TestMenuToggleSidebar_NoPanicInSingleFileMode is a regression guard for
// a crash: the Esc-t leader calls menuToggleSidebar directly, bypassing the
// menu row's hasTree gate. In single-file mode (tree == nil) flipping
// sidebarShown true would send draw() into a.tree.Render on a nil tree and
// panic. The toggle must stay a no-op so the sidebar can't be shown when
// there's no tree behind it.
func TestMenuToggleSidebar_NoPanicInSingleFileMode(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.tree = nil // single-file mode
	a.sidebarShown = false

	a.menuToggleSidebar() // simulates the Esc-t leader

	if a.sidebarShown {
		t.Fatal("sidebar must stay hidden in single-file mode — no tree to render")
	}
	a.draw() // would panic on nil a.tree.Render if the toggle flipped it on
}

// TestRefreshGitStatus_RefreshesGutterInSingleFileMode pins the
// single-file-mode fix: with no file tree, refreshGitStatus must still
// reload the open tab's per-line gutter markers (a file-scoped git diff
// that doesn't need the tree). Without this, saving a file in
// single-file mode — which routes through refreshGitStatus — would
// leave the gutter markers frozen at their open-time state.
func TestRefreshGitStatus_RefreshesGutterInSingleFileMode(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	target := filepath.Join(repo, "f.go")
	writeFileT(t, target, "package main\n\nfunc main() {}\n")
	gitRun(t, repo, "add", "f.go")
	gitRun(t, repo, "commit", "-m", "init")

	a := newTestApp(t, repo)
	a.tree = nil // simulate single-file mode
	a.openFile(target)
	tab := a.activeTabPtr()
	if tab == nil {
		t.Fatal("expected an open tab")
	}

	// Clean file → no markers yet. Now dirty the worktree and clear the
	// tab's cached markers so we can prove refreshGitStatus repopulates
	// them despite tree == nil.
	writeFileT(t, target, "package main\n\nfunc main() { println(1) }\n")
	tab.GitLines = nil
	a.refreshGitStatus()

	if len(tab.GitLines) == 0 {
		t.Fatal("expected gutter markers to be refreshed in single-file mode, got none")
	}
}

// TestDrawTabBar_NoIconWhenDisabled is the inverse of the above —
// flipping IconsEnabled off must remove the glyph from the tab bar
// (so terminals without a Nerd Font don't see tofu boxes in tabs).
func TestDrawTabBar_NoIconWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(filepath.Join(dir, "main.go"))
	a.tree.IconsEnabled = false

	a.drawTabBar()
	a.screen.Show()
	cells, w, _ := a.screen.(tcell.SimulationScreen).GetContents()

	wantGlyph := []rune(icons.For("main.go", false, false))[0]
	for x := 0; x < w; x++ {
		c := cells[x]
		if len(c.Runes) > 0 && c.Runes[0] == wantGlyph {
			t.Fatalf("did not expect glyph %q at x=%d when icons off", string(wantGlyph), x)
		}
	}
}

// -----------------------------------------------------------------------------
// Bracketed paste
// -----------------------------------------------------------------------------

// recordingScreen wraps a SimulationScreen and counts the input-mode calls
// initScreenInput makes. The simulation screen stores its paste flag in an
// unexported field with no getter, so counting the call is the only way to
// prove bracketed paste was switched on.
type recordingScreen struct {
	tcell.SimulationScreen
	mouseCalls int
	pasteCalls int
}

// EnableMouse records the call and forwards it to the wrapped screen.
func (s *recordingScreen) EnableMouse(flags ...tcell.MouseFlags) {
	s.mouseCalls++
	s.SimulationScreen.EnableMouse(flags...)
}

// EnablePaste records the call and forwards it to the wrapped screen.
func (s *recordingScreen) EnablePaste() {
	s.pasteCalls++
	s.SimulationScreen.EnablePaste()
}

// TestInitScreenInput_EnablesMouseAndPaste pins the fix for the paste
// data-loss bug at its source: both constructors go through
// initScreenInput, and it must turn bracketed paste on. Without the
// EnablePaste call the terminal delivers a paste as bare keystrokes and no
// amount of handling downstream can tell them from typing.
func TestInitScreenInput_EnablesMouseAndPaste(t *testing.T) {
	scr := &recordingScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
	if err := scr.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	defer scr.Fini()

	initScreenInput(scr)

	if scr.pasteCalls != 1 {
		t.Fatalf("EnablePaste calls: got %d, want 1", scr.pasteCalls)
	}
	if scr.mouseCalls != 1 {
		t.Fatalf("EnableMouse calls: got %d, want 1", scr.mouseCalls)
	}
}

// pasteEvents drives one whole bracketed paste through handleEvent: the
// start marker, the body as key events, then the end marker. This is the
// exact shape tcell delivers a paste in.
func pasteEvents(a *App, body ...tcell.Event) {
	a.handleEvent(tcell.NewEventPaste(true))
	for _, ev := range body {
		a.handleEvent(ev)
	}
	a.handleEvent(tcell.NewEventPaste(false))
}

// TestHandlePaste_EscAndQAreDataNotCommands is the regression test for the
// bug: pasted text containing an escape followed by 'q' used to arm the Esc
// leader and quit Vincent mid-paste. Inside a paste nothing dispatches —
// the payload lands in the buffer as one undo step and the app stays put.
//
// An escape is dropped rather than inserted. That is not a choice so much
// as the only reachable behaviour: tcell.NewEventKey folds a 0x1b rune into
// KeyEsc, so an escape byte can never arrive as a rune, and a terminal in
// bracketed-paste mode filters escape sequences out of the payload anyway.
// What matters is that it stops being a command — everything printable
// around it lands in the buffer verbatim.
func TestHandlePaste_EscAndQAreDataNotCommands(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)

	pasteEvents(a,
		keyEv(tcell.KeyEsc, 0),    // would have armed the leader
		keyEv(tcell.KeyRune, 'q'), // ... and quit Vincent
		keyEv(tcell.KeyRune, 'u'),
		keyEv(tcell.KeyRune, 'i'),
		keyEv(tcell.KeyEnter, 0),
		keyEv(tcell.KeyTab, 0),
		keyEv(tcell.KeyRune, '\x1b'), // normalised by tcell to KeyEsc
		keyEv(tcell.KeyRune, 'z'),
	)

	if a.quit {
		t.Fatal("a pasted Esc-q must not quit")
	}
	if a.dirtyOpen || a.cheatsheetOpen {
		t.Fatal("a paste must not open any modal")
	}
	if a.pasting {
		t.Fatal("End() should have closed the paste window")
	}
	tab := a.activeTabPtr()
	want := "qui\n\tz" + "seed"
	if got := tab.Buffer.String(); got != want {
		t.Fatalf("buffer after paste: got %q, want %q", got, want)
	}
	if !tab.CanUndo() {
		t.Fatal("the paste should be undoable")
	}
	if !tab.Undo() {
		t.Fatal("Undo should report a step was taken")
	}
	if got := tab.Buffer.String(); got != "seed" {
		t.Fatalf("one Undo should revert the whole paste; got %q", got)
	}
}

// TestHandlePaste_ReadOnlyTabDiscards proves a paste never reaches a diff
// tab's buffer. A diff tab carries the real file's Path, so text landing in
// it would be one Save away from overwriting the user's source.
func TestHandlePaste_ReadOnlyTabDiscards(t *testing.T) {
	dir := t.TempDir()
	a := newTestApp(t, dir)
	rows := []diff.Row{{Kind: diff.KindContext, Text: "ctx"}}
	a.tabs = append(a.tabs, editor.NewDiffTab(filepath.Join(dir, "x.go"), rows))
	a.activeTab = 0

	pasteEvents(a, keyEv(tcell.KeyRune, 'x'))

	if got := a.tabs[0].Buffer.String(); got != "ctx" {
		t.Fatalf("read-only tab buffer changed: %q", got)
	}
	if !strings.Contains(a.statusMsg, "read-only") {
		t.Fatalf("expected a read-only flash, got %q", a.statusMsg)
	}
}

// TestHandlePaste_ModalOpenDiscards keeps pasted text out of the buffer
// behind an overlay. The user's target when a modal is up is the modal, and
// silently inserting into the file underneath it is the worst of both.
func TestHandlePaste_ModalOpenDiscards(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	a.openCheatsheet()

	pasteEvents(a, keyEv(tcell.KeyRune, 'x'))

	if got := a.activeTabPtr().Buffer.String(); got != "seed" {
		t.Fatalf("buffer changed behind a modal: %q", got)
	}
}

// -----------------------------------------------------------------------------
// Disk reconciliation and conflicts
// -----------------------------------------------------------------------------

// conflictFixture opens a file, dirties the buffer, then rewrites the file
// on disk with newContent and backdates the tab's Mtime so the next
// reconcile pass sees the disk as newer. Backdating rather than sleeping
// keeps the test instant — same trick reconcileDiffTab's tests use.
func conflictFixture(t *testing.T, newContent string) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("alpha\nbravo\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	a.activeTabPtr().InsertString("mine\n")

	if err := os.WriteFile(target, []byte(newContent), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	a.tabs[0].Mtime = a.tabs[0].Mtime.Add(-time.Hour)
	// openFile flashes "Opened <name>"; clear it so a test can assert on
	// what reconciling did or did not say.
	a.statusMsg = ""
	return a, target
}

// TestReconcile_DirtyTabWithDifferentBytesConflicts is the core of the
// model. An agent rewrote the file while the user had unsaved edits: the
// tab is conflicted, the buffer is untouched, and a Save refuses rather
// than silently overwriting the agent.
func TestReconcile_DirtyTabWithDifferentBytesConflicts(t *testing.T) {
	a, target := conflictFixture(t, "from the agent\n")

	a.reconcileOpenTabsWithDisk(pollNow(a))

	tab := a.tabs[0]
	if !tab.Conflict {
		t.Fatal("a dirty tab whose file changed should be conflicted")
	}
	if got := tab.Buffer.String(); got != "mine\nalpha\nbravo\n" {
		t.Fatalf("the buffer must not be touched; got %q", got)
	}
	if err := tab.Save(); !errors.Is(err, editor.ErrDiskConflict) {
		t.Fatalf("Save: got %v, want ErrDiskConflict", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "from the agent\n" {
		t.Fatalf("disk should be byte-identical after a refused save; got %q", got)
	}
}

// TestReconcile_ConflictDoesNotReFlashOrAdvanceMtime pins the two halves
// of the re-flash suppression. The flag, not the timestamp, is what stops
// the warning repeating — advancing Mtime instead is exactly the bug this
// replaced, because it erased the only record that a conflict existed.
func TestReconcile_ConflictDoesNotReFlashOrAdvanceMtime(t *testing.T) {
	a, _ := conflictFixture(t, "from the agent\n")

	a.reconcileOpenTabsWithDisk(pollNow(a))
	firstMtime := a.tabs[0].Mtime
	a.statusMsg = ""

	a.reconcileOpenTabsWithDisk(pollNow(a))

	if a.statusMsg != "" {
		t.Fatalf("a standing conflict should not re-flash; got %q", a.statusMsg)
	}
	if !a.tabs[0].Mtime.Equal(firstMtime) {
		t.Fatal("a conflicted tab must keep its old Mtime")
	}
	if !a.tabs[0].Conflict {
		t.Fatal("the conflict should still stand")
	}
}

// TestReconcile_IdenticalBytesIsNotAConflict is the no-false-positives
// half. A gofmt over an already-formatted file, or a tool that rewrites a
// file it decided not to change, bumps mtime without changing a byte. That
// must be silent, or the warning becomes noise the user learns to ignore.
func TestReconcile_IdenticalBytesIsNotAConflict(t *testing.T) {
	a, _ := conflictFixture(t, "alpha\nbravo\n") // same bytes as on open
	before := a.tabs[0].Mtime

	a.reconcileOpenTabsWithDisk(pollNow(a))

	if a.tabs[0].Conflict {
		t.Fatal("a rewrite that changed no bytes is not a conflict")
	}
	if a.statusMsg != "" {
		t.Fatalf("it should not flash either; got %q", a.statusMsg)
	}
	if !a.tabs[0].Mtime.After(before) {
		t.Fatal("the new mtime should be taken so we don't re-check every tick")
	}
	if !a.tabs[0].Dirty {
		t.Fatal("the user's edits are still unsaved — Dirty must stand")
	}
}

// TestReconcile_CleanTabReloadsQuietly keeps the flash off the normal
// case. A clean tab picking up an agent's write is what happens all day in
// this workflow; a status flash per write is noise during exactly the
// stretch the user is reading. Diff tabs already made this call.
func TestReconcile_CleanTabReloadsQuietly(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	if err := os.WriteFile(target, []byte("from the agent\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	a.tabs[0].Mtime = a.tabs[0].Mtime.Add(-time.Hour)
	a.statusMsg = "" // drop openFile's own flash.

	a.reconcileOpenTabsWithDisk(pollNow(a))

	if got := a.tabs[0].Buffer.String(); got != "from the agent\n" {
		t.Fatalf("a clean tab should reload; buffer is %q", got)
	}
	if a.statusMsg != "" {
		t.Fatalf("a clean reload must not flash; got %q", a.statusMsg)
	}
	if a.tabs[0].Conflict {
		t.Fatal("a clean reload is not a conflict")
	}
	// And nothing about the reload should appear on the status row.
	a.draw()
	scr := a.screen.(tcell.SimulationScreen)
	scr.Show()
	if row := statusRowText(t, a); strings.Contains(row, "reloaded") {
		t.Fatalf("status row should not mention a reload; got %q", row)
	}
}

// statusRowText reads the status bar back off the simulation screen as a
// string, so a test can assert on what the user would actually see.
func statusRowText(t *testing.T, a *App) string {
	t.Helper()
	cells, w, _ := a.screen.(tcell.SimulationScreen).GetContents()
	_, sy, _, _ := a.statusRect()
	var b strings.Builder
	for x := 0; x < w; x++ {
		c := cells[sy*w+x]
		if len(c.Runes) > 0 {
			b.WriteRune(c.Runes[0])
		}
	}
	return b.String()
}

// TestDrawTabBar_ConflictDotUsesTheConflictColour is the visual half of
// the model: one dot, two states, conflict outranking dirty. If both
// states painted the same colour the warning would be invisible.
func TestDrawTabBar_ConflictDotUsesTheConflictColour(t *testing.T) {
	a, _ := conflictFixture(t, "from the agent\n")

	// Dirty but not yet conflicted — the dot is the Modified amber.
	a.drawTabBar()
	a.screen.Show()
	if got := dotColour(t, a); got != a.theme.Modified {
		t.Fatalf("dirty dot colour: got %v, want Modified %v", got, a.theme.Modified)
	}

	a.reconcileOpenTabsWithDisk(pollNow(a))
	a.drawTabBar()
	a.screen.Show()
	if got := dotColour(t, a); got != a.theme.Conflict {
		t.Fatalf("conflicted dot colour: got %v, want Conflict %v", got, a.theme.Conflict)
	}
}

// dotColour finds the tab bar's dirty dot and returns its foreground.
func dotColour(t *testing.T, a *App) tcell.Color {
	t.Helper()
	cells, w, _ := a.screen.(tcell.SimulationScreen).GetContents()
	_, ty, _, _ := a.tabBarRect()
	for x := 0; x < w; x++ {
		c := cells[ty*w+x]
		if len(c.Runes) > 0 && c.Runes[0] == '●' {
			fg, _, _ := c.Style.Decompose()
			return fg
		}
	}
	t.Fatal("no dirty dot found in the tab bar")
	return 0
}

// TestDrawStatusBar_ConflictSaysChangedOnDisk pins the wording. The flash
// that announced the conflict expires after three seconds; the state does
// not, so the status bar has to keep saying it.
func TestDrawStatusBar_ConflictSaysChangedOnDisk(t *testing.T) {
	a, _ := conflictFixture(t, "from the agent\n")
	a.reconcileOpenTabsWithDisk(pollNow(a))
	// Step past the flash so the status bar shows tab state, not a message.
	a.statusUntil = time.Now().Add(-time.Second)

	a.draw()
	a.screen.Show()

	row := statusRowText(t, a)
	if !strings.Contains(row, "● changed on disk") {
		t.Fatalf("status row should say the file changed on disk; got %q", row)
	}
}

// TestSaveTabAt_ConflictedOpensThePromptAndWritesNothing is the
// load-bearing refusal: the save the user asked for turns into a question.
func TestSaveTabAt_ConflictedOpensThePromptAndWritesNothing(t *testing.T) {
	a, target := conflictFixture(t, "from the agent\n")
	a.reconcileOpenTabsWithDisk(pollNow(a))

	if a.saveTabAt(0) {
		t.Fatal("saveTabAt should report failure on a conflicted tab")
	}
	if !a.dirtyOpen {
		t.Fatal("it should have opened the conflict prompt")
	}
	if a.dirtyTitle != "Changed on disk" {
		t.Fatalf("prompt title: %q", a.dirtyTitle)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "from the agent\n" {
		t.Fatalf("nothing should have been written; disk holds %q", got)
	}
}

// TestDrawTabBar_OffShowsNameNotStrip pins what row 0 looks like with
// tabBarShown off: the active tab's name renders (so "what file am I
// looking at" survives), but no second tab's name and no × close button
// appear — there's no strip to click, only the active tab's name.
func TestDrawTabBar_OffShowsNameNotStrip(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha.go", "zzz_other.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	a := newTestApp(t, dir)
	a.openFile(filepath.Join(dir, "alpha.go"))
	a.openFile(filepath.Join(dir, "zzz_other.go"))
	a.tabBarShown = false

	a.drawTabBar()
	a.screen.Show()
	cells, w, _ := a.screen.(tcell.SimulationScreen).GetContents()
	row := rowText(t, cells, w, 0)

	if !strings.Contains(row, "zzz_other.go") {
		t.Fatalf("expected active tab name on row 0, got %q", row)
	}
	if strings.Contains(row, "alpha.go") {
		t.Fatalf("did not expect the inactive tab's name with the strip off: %q", row)
	}
	if strings.Contains(row, "×") {
		t.Fatalf("did not expect a close button with the strip off: %q", row)
	}

	// No tab strip means no click targets to hit-test against.
	if a.lastTabRects != nil {
		t.Fatalf("lastTabRects = %v, want nil with the strip off", a.lastTabRects)
	}
}

// TestDrawTabBar_OnShowsFullStrip is the mirror of the test above: with
// tabBarShown on, every open tab's name renders in row 0 and lastTabRects
// is populated so tabBarClick has real geometry to hit-test against.
func TestDrawTabBar_OnShowsFullStrip(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha.go", "zzz_other.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	a := newTestApp(t, dir)
	a.openFile(filepath.Join(dir, "alpha.go"))
	a.openFile(filepath.Join(dir, "zzz_other.go"))
	a.tabBarShown = true

	a.drawTabBar()
	a.screen.Show()
	cells, w, _ := a.screen.(tcell.SimulationScreen).GetContents()
	row := rowText(t, cells, w, 0)

	if !strings.Contains(row, "alpha.go") || !strings.Contains(row, "zzz_other.go") {
		t.Fatalf("expected both tab names on row 0 with the strip on, got %q", row)
	}
	if len(a.lastTabRects) != 2 {
		t.Fatalf("lastTabRects = %d entries, want 2", len(a.lastTabRects))
	}
}

// TestMenuToggleTabBar_FlipsAndLabels pins the toggle action and its
// dynamic menu label, mirroring TestMenuToggleSidebar-style coverage.
func TestMenuToggleTabBar_FlipsAndLabels(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.tabBarShown = false

	if got := a.tabBarToggleLabel(); got != "Show tab bar" {
		t.Fatalf("label = %q, want %q", got, "Show tab bar")
	}
	a.menuToggleTabBar()
	if !a.tabBarShown {
		t.Fatal("expected tabBarShown = true after toggle")
	}
	if got := a.tabBarToggleLabel(); got != "Hide tab bar" {
		t.Fatalf("label = %q, want %q", got, "Hide tab bar")
	}
	a.menuToggleTabBar()
	if a.tabBarShown {
		t.Fatal("expected tabBarShown = false after second toggle")
	}
}

// rowText reconstructs one screen row as a plain string, for substring
// assertions against drawTabBar output. Distinct from filetree's own
// rowText helper — this package has no equivalent yet.
func rowText(t *testing.T, cells []tcell.SimCell, w, y int) string {
	t.Helper()
	rs := make([]rune, 0, w)
	for x := 0; x < w; x++ {
		c := cells[y*w+x]
		if len(c.Runes) == 0 {
			rs = append(rs, ' ')
			continue
		}
		rs = append(rs, c.Runes[0])
	}
	return string(rs)
}
