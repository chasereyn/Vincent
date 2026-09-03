// =============================================================================
// File: internal/app/find_test.go
// Author: Spicer Matthews <spicer@cloudmanic.com>
// Created: 2026-04-30
// Copyright: 2026 Cloudmanic, LLC. All rights reserved.
// =============================================================================

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/editor"
)

// seedFindApp opens a tab with content seeded for find tests so each
// test can focus on the behaviour under test rather than fixture setup.
func seedFindApp(t *testing.T, content string) *App {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	return a
}

// TestOpenFind_OpensBarEmpty drops the user into a focused find bar
// with an empty input. Pre-fill from a prior query is intentionally
// not done — closing the bar already clears find state, so each Esc-f
// is a fresh search.
func TestOpenFind_OpensBarEmpty(t *testing.T) {
	a := seedFindApp(t, "foo bar foo")
	a.openFind()
	if !a.findOpen {
		t.Fatal("openFind did not flip findOpen")
	}
	if len(a.findValue) != 0 {
		t.Fatalf("input should be empty, got %q", string(a.findValue))
	}
}

// TestOpenFind_NoTabIsNoOp guards against opening the bar when there's
// no text tab to search. Without this, the bar would float over an
// empty editor with nothing to highlight.
func TestOpenFind_NoTabIsNoOp(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.openFind()
	if a.findOpen {
		t.Fatal("openFind should be a no-op with no tab")
	}
}

// TestHandleFindKey_TypingLiveSearches drives the per-keystroke handler
// the way a user would: type "foo", and the active tab's match list
// should be populated and the cursor should sit on the first match.
func TestHandleFindKey_TypingLiveSearches(t *testing.T) {
	a := seedFindApp(t, "foo bar foo")
	a.openFind()
	for _, r := range "foo" {
		a.handleFindKey(keyEv(tcell.KeyRune, r))
	}
	tab := a.activeTabPtr()
	if len(tab.FindMatches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(tab.FindMatches))
	}
	if tab.Cursor != (editor.Position{Line: 0, Col: 0}) {
		t.Fatalf("cursor should snap to first match, got %+v", tab.Cursor)
	}
}

// TestHandleFindKey_EnterAdvances simulates Enter inside the bar — it
// should jump to the next match, with wrap-around.
func TestHandleFindKey_EnterAdvances(t *testing.T) {
	a := seedFindApp(t, "foo\nfoo\nfoo")
	a.openFind()
	for _, r := range "foo" {
		a.handleFindKey(keyEv(tcell.KeyRune, r))
	}
	tab := a.activeTabPtr()
	a.handleFindKey(keyEv(tcell.KeyEnter, 0))
	if tab.FindIndex != 1 {
		t.Fatalf("expected FindIndex=1 after Enter, got %d", tab.FindIndex)
	}
	if tab.Cursor.Line != 1 {
		t.Fatalf("cursor should be on line 1, got %+v", tab.Cursor)
	}
}

// TestHandleFindKey_ShiftEnterGoesBack pins down the Shift-Enter -> prev
// behaviour. Enter then Shift-Enter from the first match should leave
// us back at the first match.
func TestHandleFindKey_ShiftEnterGoesBack(t *testing.T) {
	a := seedFindApp(t, "foo\nfoo\nfoo")
	a.openFind()
	for _, r := range "foo" {
		a.handleFindKey(keyEv(tcell.KeyRune, r))
	}
	a.handleFindKey(keyEv(tcell.KeyEnter, 0))
	// Shift+Enter — keyEv default is ModNone, so build it directly.
	a.handleFindKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModShift))
	if a.activeTabPtr().FindIndex != 0 {
		t.Fatalf("Shift-Enter should walk back, got idx=%d", a.activeTabPtr().FindIndex)
	}
}

// TestHandleFindKey_EscClearsHighlights pins the close gesture: Esc
// closes the bar AND wipes the tab's match list so the highlights
// disappear with the UI. Leaving them painted after the bar closes is
// the kind of "did anything happen?" surprise we want to avoid.
func TestHandleFindKey_EscClearsHighlights(t *testing.T) {
	a := seedFindApp(t, "foo bar foo")
	a.openFind()
	for _, r := range "foo" {
		a.handleFindKey(keyEv(tcell.KeyRune, r))
	}
	a.handleFindKey(keyEv(tcell.KeyEsc, 0))
	if a.findOpen {
		t.Fatal("Esc should close the find bar")
	}
	tab := a.activeTabPtr()
	if tab.FindQuery != "" || tab.FindMatches != nil || tab.FindIndex != -1 {
		t.Fatalf("Esc should clear all find state, got %+v", tab)
	}
}

// TestHandleFindKey_BackspaceLiveUpdates removes a character from the
// input and confirms matches re-resolve. Without this, deleting the
// query would leave stale highlights painted in the editor.
func TestHandleFindKey_BackspaceLiveUpdates(t *testing.T) {
	a := seedFindApp(t, "foo bar foox")
	a.openFind()
	for _, r := range "foox" {
		a.handleFindKey(keyEv(tcell.KeyRune, r))
	}
	tab := a.activeTabPtr()
	if len(tab.FindMatches) != 1 {
		t.Fatalf("setup expected 1 match for 'foox', got %d", len(tab.FindMatches))
	}
	a.handleFindKey(keyEv(tcell.KeyBackspace, 0))
	if len(tab.FindMatches) != 2 {
		t.Fatalf("after backspace should match 'foo' (2x), got %d", len(tab.FindMatches))
	}
}

// TestEditorRect_ShrinksWhenFindOpen pins down the layout contract: the
// editor body is one row shorter while the find bar is up (and two rows
// shorter once the replace row is revealed). Without this the bar would
// paint over the bottom row of code.
func TestEditorRect_ShrinksWhenFindOpen(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	_, _, _, hClosed := a.editorRect()
	a.findOpen = true
	_, _, _, hOpen := a.editorRect()
	if hOpen != hClosed-1 {
		t.Fatalf("editor height didn't shrink by one row: closed=%d open=%d", hClosed, hOpen)
	}
	a.findReplaceVisible = true
	_, _, _, hReplaceOpen := a.editorRect()
	if hReplaceOpen != hClosed-2 {
		t.Fatalf("editor height didn't shrink by two rows with replace visible: closed=%d open=%d", hClosed, hReplaceOpen)
	}
}

// TestHasFindable_ImageTabIsFalse keeps the menu's Find row disabled on
// image tabs — there's nothing to search inside an image.
func TestHasFindable_ImageTabIsFalse(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	if a.hasFindable() {
		t.Fatal("no tab should not be findable")
	}
}

// TestCounterText_Variants pins the three rendered states of the
// counter so a future refactor can't quietly drop "no results" or the
// blank no-query state.
func TestCounterText_Variants(t *testing.T) {
	a := seedFindApp(t, "foo bar foo")
	a.openFind()
	if got := a.findCounterText(); got != "" {
		t.Fatalf("empty input should yield blank counter, got %q", got)
	}
	for _, r := range "foo" {
		a.handleFindKey(keyEv(tcell.KeyRune, r))
	}
	if got := a.findCounterText(); got != "1 of 2" {
		t.Fatalf("counter for 2 matches should be '1 of 2', got %q", got)
	}
	for _, r := range "zzz" {
		a.handleFindKey(keyEv(tcell.KeyRune, r))
	}
	if got := a.findCounterText(); got != "no results" {
		t.Fatalf("zero hits should yield 'no results', got %q", got)
	}
}

// TestCloseAllModals_ClosesFindBar guards against a regression where
// opening another modal could leave the find bar focused underneath.
func TestCloseAllModals_ClosesFindBar(t *testing.T) {
	a := seedFindApp(t, "foo")
	a.openFind()
	a.closeAllModals()
	if a.findOpen {
		t.Fatal("closeAllModals should close the find bar")
	}
}

// TestHandleFindKey_TabRevealsAndFocusesReplace pins the reveal gesture:
// the replace row starts hidden, and the first Tab both shows it and
// moves focus onto it.
func TestHandleFindKey_TabRevealsAndFocusesReplace(t *testing.T) {
	a := seedFindApp(t, "foo")
	a.openFind()
	if a.findReplaceVisible {
		t.Fatal("replace row should start hidden")
	}
	a.handleFindKey(keyEv(tcell.KeyTab, 0))
	if !a.findReplaceVisible {
		t.Fatal("Tab should reveal the replace row")
	}
	if !a.findReplaceFocus {
		t.Fatal("Tab should focus the replace field on first press")
	}
	a.handleFindKey(keyEv(tcell.KeyTab, 0))
	if !a.findReplaceVisible {
		t.Fatal("a second Tab should not hide the row again")
	}
	if a.findReplaceFocus {
		t.Fatal("a second Tab should move focus back to the find field")
	}
}

// TestHandleFindKey_TypingRoutesToFocusedField checks that once the
// replace field has focus, printable runes land there instead of in the
// find query (and don't disturb it).
func TestHandleFindKey_TypingRoutesToFocusedField(t *testing.T) {
	a := seedFindApp(t, "foo bar")
	a.openFind()
	for _, r := range "foo" {
		a.handleFindKey(keyEv(tcell.KeyRune, r))
	}
	a.handleFindKey(keyEv(tcell.KeyTab, 0)) // focus replace
	for _, r := range "baz" {
		a.handleFindKey(keyEv(tcell.KeyRune, r))
	}
	if got := string(a.findValue); got != "foo" {
		t.Fatalf("find field should be untouched by replace typing, got %q", got)
	}
	if got := string(a.findReplaceValue); got != "baz" {
		t.Fatalf("replace field = %q, want %q", got, "baz")
	}
}

// TestHandleFindKey_EnterOnReplaceFieldReplacesAndAdvances drives the
// whole UI path: type a query, reveal + focus replace, type a
// replacement, press Enter, and check both the buffer and that the bar
// stayed open with focus still on the replace field for the next one.
func TestHandleFindKey_EnterOnReplaceFieldReplacesAndAdvances(t *testing.T) {
	a := seedFindApp(t, "foo foo foo")
	a.openFind()
	for _, r := range "foo" {
		a.handleFindKey(keyEv(tcell.KeyRune, r))
	}
	a.handleFindKey(keyEv(tcell.KeyTab, 0))
	for _, r := range "X" {
		a.handleFindKey(keyEv(tcell.KeyRune, r))
	}
	a.handleFindKey(keyEv(tcell.KeyEnter, 0))

	tab := a.activeTabPtr()
	if got := tab.Buffer.Lines[0]; got != "X foo foo" {
		t.Fatalf("buffer = %q, want %q", got, "X foo foo")
	}
	if !a.findOpen {
		t.Fatal("a single replace should not close the bar")
	}
	if !a.findReplaceFocus {
		t.Fatal("focus should stay on the replace field so Enter can repeat")
	}
}

// TestHandleFindKey_EnterOnReplaceField_RefusedOnDiffTab checks the
// app-layer gate: replacing must be refused (with a flash, and no change
// to the rendered diff) even though the same tab is perfectly findable.
func TestHandleFindKey_EnterOnReplaceField_RefusedOnDiffTab(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	tab := editor.NewDiffTab("", nil)
	tab.Buffer = editor.NewBuffer("foo bar")
	a.tabs = []*editor.Tab{tab}
	a.activeTab = 0

	a.openFind()
	for _, r := range "foo" {
		a.handleFindKey(keyEv(tcell.KeyRune, r))
	}
	a.handleFindKey(keyEv(tcell.KeyTab, 0))
	a.handleFindKey(keyEv(tcell.KeyRune, 'X'))
	a.handleFindKey(keyEv(tcell.KeyEnter, 0))

	if got := tab.Buffer.Lines[0]; got != "foo bar" {
		t.Fatalf("a diff tab must not be rewritten, got %q", got)
	}
	if a.statusMsg == "" {
		t.Fatal("refusing the replace should flash a message")
	}
}

// TestHandleFindKey_AltEnterReplacesAll drives Replace All through the
// key handler: Alt+Enter should fire regardless of which field has
// focus, since it acts on the whole match list rather than "the current
// one".
func TestHandleFindKey_AltEnterReplacesAll(t *testing.T) {
	a := seedFindApp(t, "foo foo foo")
	a.openFind()
	for _, r := range "foo" {
		a.handleFindKey(keyEv(tcell.KeyRune, r))
	}
	a.handleFindKey(keyEv(tcell.KeyTab, 0))
	for _, r := range "X" {
		a.handleFindKey(keyEv(tcell.KeyRune, r))
	}
	a.handleFindKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModAlt))

	tab := a.activeTabPtr()
	if got := tab.Buffer.Lines[0]; got != "X X X" {
		t.Fatalf("buffer = %q, want %q", got, "X X X")
	}
	if !tab.Undo() {
		t.Fatal("Replace All through the bar should still be one undo step")
	}
	if got := tab.Buffer.Lines[0]; got != "foo foo foo" {
		t.Fatalf("Undo after Replace All = %q, want original restored", got)
	}
}

// TestFindBarHeight_GrowsWithReplaceRow pins the layout contract the
// research doc called out: findBarHeight is what editorRect and
// findBarRect both consult, so revealing the replace row grows the bar
// (and shrinks the editor) by exactly one row.
func TestFindBarHeight_GrowsWithReplaceRow(t *testing.T) {
	a := seedFindApp(t, "foo")
	a.openFind()
	if got := a.findBarHeight(); got != 1 {
		t.Fatalf("closed replace row: findBarHeight() = %d, want 1", got)
	}
	a.handleFindKey(keyEv(tcell.KeyTab, 0))
	if got := a.findBarHeight(); got != 2 {
		t.Fatalf("open replace row: findBarHeight() = %d, want 2", got)
	}
	_, _, _, h := a.findBarRect()
	if h != 2 {
		t.Fatalf("findBarRect height = %d, want 2", h)
	}
}

// TestDrawFindBar_RendersBothRowsOnSimulationScreen is the drawing test:
// once the replace row is visible, both the "Find:" and "Replace:"
// labels must actually land on the screen, on two different rows.
func TestDrawFindBar_RendersBothRowsOnSimulationScreen(t *testing.T) {
	a := seedFindApp(t, "foo")
	a.openFind()
	a.handleFindKey(keyEv(tcell.KeyTab, 0)) // reveal + focus the replace row

	a.drawFindBar()
	a.screen.Show()
	scr := a.screen.(tcell.SimulationScreen)
	_, by, _, h := a.findBarRect()
	if h != 2 {
		t.Fatalf("bar height = %d, want 2", h)
	}
	findRow := screenLine(scr, by)
	replaceRow := screenLine(scr, by+1)
	if !strings.Contains(findRow, "Find:") {
		t.Fatalf("find row = %q, want it to contain %q", findRow, "Find:")
	}
	if !strings.Contains(replaceRow, "Replace:") {
		t.Fatalf("replace row = %q, want it to contain %q", replaceRow, "Replace:")
	}
}

// TestDrawFindBar_OnlyOneRowWhenReplaceHidden guards against the replace
// row bleeding onto screen (or the editor being shrunk for it) before
// Tab has ever been pressed.
func TestDrawFindBar_OnlyOneRowWhenReplaceHidden(t *testing.T) {
	a := seedFindApp(t, "foo")
	a.openFind()

	a.drawFindBar()
	a.screen.Show()
	scr := a.screen.(tcell.SimulationScreen)
	_, by, _, h := a.findBarRect()
	if h != 1 {
		t.Fatalf("bar height = %d, want 1", h)
	}
	findRow := screenLine(scr, by)
	if !strings.Contains(findRow, "Find:") {
		t.Fatalf("find row = %q, want it to contain %q", findRow, "Find:")
	}
}
