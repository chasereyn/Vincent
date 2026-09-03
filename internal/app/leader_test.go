// =============================================================================
// File: internal/app/leader_test.go
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
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/filetree"
)

// TestLeaderActionFor_AllBindingsResolve walks the binding table and
// verifies every entry returns a non-nil action. Catches accidentally
// dropping a method reference when the table is reshuffled.
func TestLeaderActionFor_AllBindingsResolve(t *testing.T) {
	for _, b := range leaderBindings() {
		if leaderActionFor(b.key) == nil {
			t.Errorf("binding %q resolved to nil", b.key)
		}
	}
}

// TestLeaderActionFor_UnboundReturnsNil pins down the contract that
// leaderActionFor reports a miss with nil so handleKey can distinguish
// "leader fired" from "key was unbound — fall through".
func TestLeaderActionFor_UnboundReturnsNil(t *testing.T) {
	if leaderActionFor('k') != nil {
		t.Fatal("'k' should not be a leader binding (no editor action mapped)")
	}
}

// TestHandleKey_LeaderSave saves the active tab via Esc, s. The buffer
// is dirtied before the leader fires so the assertion is meaningful:
// a successful save flips the dirty flag back to false.
func TestHandleKey_LeaderSave(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "t.txt")
	if err := os.WriteFile(target, []byte(""), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	a.handleKey(keyEv(tcell.KeyRune, 'x')) // dirty the buffer
	if !a.activeTabPtr().Dirty {
		t.Fatal("expected dirty buffer before save")
	}

	a.handleKey(keyEv(tcell.KeyEsc, 0))
	a.handleKey(keyEv(tcell.KeyRune, 's'))

	if a.activeTabPtr().Dirty {
		t.Fatal("Esc-s should have saved the buffer (dirty still true)")
	}
}

// TestHandleKey_LeaderUndoRedo round-trips an edit through Esc-u and
// Esc-U. Pins down both bindings at once and the fact that the leader
// state resets between sequences (we re-arm Esc each time).
func TestHandleKey_LeaderUndoRedo(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "t.txt")
	if err := os.WriteFile(target, []byte(""), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)
	a.handleKey(keyEv(tcell.KeyRune, 'a'))

	a.handleKey(keyEv(tcell.KeyEsc, 0))
	a.handleKey(keyEv(tcell.KeyRune, 'u'))
	if a.activeTabPtr().Buffer.Lines[0] != "" {
		t.Fatalf("Esc-u should have undone the insert, got %q", a.activeTabPtr().Buffer.Lines[0])
	}

	a.handleKey(keyEv(tcell.KeyEsc, 0))
	a.handleKey(keyEv(tcell.KeyRune, 'U'))
	if a.activeTabPtr().Buffer.Lines[0] != "a" {
		t.Fatalf("Esc-r should have redone the insert, got %q", a.activeTabPtr().Buffer.Lines[0])
	}
}

// TestHandleKey_LeaderToggleSidebar flips sidebarShown via Esc-t. The
// toggle is the simplest leader action with no preconditions, so it's
// the most stable smoke test that the dispatch wiring is intact.
func TestHandleKey_LeaderToggleSidebar(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	before := a.sidebarShown
	a.handleKey(keyEv(tcell.KeyEsc, 0))
	a.handleKey(keyEv(tcell.KeyRune, 'f'))
	if a.sidebarShown == before {
		t.Fatalf("Esc-f should toggle sidebar (still %v)", a.sidebarShown)
	}
}

// TestHandleKey_LeaderFind binds Esc-/ to the find bar. Slash is the
// find gesture in less, vim, and most pagers, and it freed f for the file
// panel toggle.
func TestHandleKey_LeaderFind(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	if err := os.WriteFile(target, []byte("one\ntwo"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)

	a.handleKey(keyEv(tcell.KeyEsc, 0))
	a.handleKey(keyEv(tcell.KeyRune, '/'))

	if !a.findOpen {
		t.Fatal("Esc-/ should open the find bar")
	}
}

// TestHandleKey_LeaderSelectAll binds Esc-a to Tab.SelectAll, which had
// no caller before the 2026-09-02 key rework.
func TestHandleKey_LeaderSelectAll(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	if err := os.WriteFile(target, []byte("one\ntwo"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)

	a.handleKey(keyEv(tcell.KeyEsc, 0))
	a.handleKey(keyEv(tcell.KeyRune, 'a'))

	if !a.hasSelection() {
		t.Fatal("Esc-a should select the whole buffer")
	}
}

// TestHandleKey_LeaderQuit sets a.quit via Esc-q. We test this directly
// rather than through Run() so we don't have to drive the event loop —
// the quit flag is what Run() polls each tick.
func TestHandleKey_LeaderQuit(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.handleKey(keyEv(tcell.KeyEsc, 0))
	a.handleKey(keyEv(tcell.KeyRune, 'q'))
	if !a.quit {
		t.Fatal("Esc-q should set a.quit = true")
	}
}

// TestHandleKey_LeaderFoldAll pins Esc z. Reviewing agent work expands the
// tree behind your back — every file opened from the Changes panel or the
// finder calls Tree.Reveal — so this is the key that gets the sidebar's
// shape back, and it has to work with the tree at any depth.
func TestHandleKey_LeaderFoldAll(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(nested, "deep.txt")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target) // openFile reveals, so both levels are now expanded

	var expanded []string
	var walk func(n *filetree.Node)
	walk = func(n *filetree.Node) {
		for _, c := range n.Children {
			if c.IsDir && c.Expanded {
				expanded = append(expanded, c.Name)
			}
			walk(c)
		}
	}
	walk(a.tree.Root)
	if len(expanded) < 2 {
		t.Fatalf("fixture: expected two open levels, got %v", expanded)
	}

	a.handleKey(keyEv(tcell.KeyEsc, 0))
	a.handleKey(keyEv(tcell.KeyRune, 'z'))

	expanded = nil
	walk(a.tree.Root)
	if len(expanded) != 0 {
		t.Fatalf("Esc z should have folded every folder, still open: %v", expanded)
	}
	if a.statusMsg == "" {
		t.Fatal("the fold should flash — a silent tree jump reads as a glitch")
	}
}

// TestMenuCollapseTree_SingleFileModeFlashes covers the nil-tree guard.
// Esc z reaches the action regardless of mode, and dereferencing a nil
// tree is the crash it exists to prevent.
func TestMenuCollapseTree_SingleFileModeFlashes(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.tree = nil
	a.menuCollapseTree()
	if a.statusMsg == "" {
		t.Fatal("expected a flash explaining there is no explorer to fold")
	}
}

// TestHandleKey_LeaderUnboundFallsThrough is the regression test for the
// "stray Esc shouldn't swallow the next keystroke" property: pressing
// Esc and then an unbound letter must still deliver that letter to the
// active tab. Without the fall-through, an accidental Esc tap would
// silently eat the user's next character.
func TestHandleKey_LeaderUnboundFallsThrough(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "t.txt")
	if err := os.WriteFile(target, []byte(""), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)

	// 'k' rather than the 'z' this test used to press: z became fold-all,
	// and 'n' became New File — both since bound, so 'k' is what's left
	// genuinely unbound.
	a.handleKey(keyEv(tcell.KeyEsc, 0))
	a.handleKey(keyEv(tcell.KeyRune, 'k'))

	if got := a.activeTabPtr().Buffer.Lines[0]; got != "k" {
		t.Fatalf("unbound key after Esc should reach the editor, got %q", got)
	}
}

// TestHandleKey_LeaderTimesOut verifies the leader window expires:
// after doubleEscMs has passed since the last Esc, a bound letter must
// reach the editor as a normal keystroke instead of firing the action.
func TestHandleKey_LeaderTimesOut(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "t.txt")
	if err := os.WriteFile(target, []byte(""), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	a.openFile(target)

	a.handleKey(keyEv(tcell.KeyEsc, 0))
	// Backdate the Esc timestamp past the leader window so the next 's'
	// is treated as a plain keystroke rather than Save.
	a.lastEscape = time.Now().Add(-2 * doubleEscMs)
	a.handleKey(keyEv(tcell.KeyRune, 's'))

	if got := a.activeTabPtr().Buffer.Lines[0]; got != "s" {
		t.Fatalf("expired leader window: 's' should insert literally, got %q", got)
	}
}

// TestHandleKey_EscEscCancelsLeader pins the 2026-09-02 rework: a second
// Esc while the leader is armed cancels it rather than opening the menu.
// The menu is Esc m now.
func TestHandleKey_EscEscCancelsLeader(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.handleKey(keyEv(tcell.KeyEsc, 0))
	if !a.leaderArmed() {
		t.Fatal("first Esc should arm the leader")
	}
	a.handleKey(keyEv(tcell.KeyEsc, 0))
	if a.cheatsheetOpen {
		t.Fatal("Esc-Esc must not open an overlay")
	}
	if a.leaderArmed() {
		t.Fatal("second Esc should cancel the armed leader")
	}
}

// TestHandleKey_LeaderCheatsheet binds Esc-? to the key table, and pins
// that a following Esc closes it rather than arming the leader again. That
// second half is the whole reason the cheatsheet is routed ahead of the Esc
// branch in handleKey.
func TestHandleKey_LeaderCheatsheet(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.handleKey(keyEv(tcell.KeyEsc, 0))
	a.handleKey(keyEv(tcell.KeyRune, '?'))
	if !a.cheatsheetOpen {
		t.Fatal("Esc-? should open the cheatsheet")
	}
	a.handleKey(keyEv(tcell.KeyEsc, 0))
	if a.cheatsheetOpen {
		t.Fatal("Esc with the cheatsheet open should close it")
	}
}

// TestHandleKey_LeaderReviewComposer pins the rebinding itself: Esc-r on a
// diff opens the composer rather than redoing an edit. Without this the
// swap could silently regress to Redo and nobody would notice until a
// review note failed to open.
func TestHandleKey_LeaderReviewComposer(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.tabs = append(a.tabs, newTestDiffTab())
	a.activeTab = 0

	a.handleKey(keyEv(tcell.KeyEsc, 0))
	a.handleKey(keyEv(tcell.KeyRune, 'r'))

	if !a.composerOpen {
		t.Fatal("Esc-r on a diff should open the review composer")
	}
}

// TestLeaderActionForKey_EnterSendsReview pins the named-key half of the
// leader table. Enter is not a rune, so it needs its own lookup — and the
// send action must be reachable from it.
func TestLeaderActionForKey_EnterSendsReview(t *testing.T) {
	if leaderActionForKey(tcell.KeyEnter) == nil {
		t.Fatal("Esc-Enter should be bound")
	}
	if leaderActionForKey(tcell.KeyTab) != nil {
		t.Fatal("Tab should not be a named-key leader binding")
	}
	for _, b := range leaderKeyBindings() {
		if leaderActionForKey(b.key) == nil {
			t.Errorf("named binding %v resolved to nil", b.key)
		}
	}
}

// TestHandleKey_LeaderEnterSendsReview drives the named-key leader through
// handleKey itself, which is the part that could regress silently: the rune
// table is consulted first, and an early return there would leave Esc-Enter
// dead with every unit test on leaderActionForKey still green.
func TestHandleKey_LeaderEnterSendsReview(t *testing.T) {
	a := newTestApp(t, t.TempDir())

	a.handleKey(keyEv(tcell.KeyEsc, 0))
	a.handleKey(keyEv(tcell.KeyEnter, 0))

	// With an empty batch the action's own precondition fires, which is
	// exactly the observable proof that it ran at all.
	if !strings.Contains(a.statusMsg, "No review notes") {
		t.Fatalf("Esc-Enter should have reached sendReview; flash = %q", a.statusMsg)
	}
}
