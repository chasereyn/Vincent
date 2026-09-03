// =============================================================================
// File: internal/app/shutdown_test.go
// Author: Chase Reynolds
// Created: 2026-08-15
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package app

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// TestQuitEvent_SetsTheQuitFlag pins the wake-up path the signal watcher
// uses. The watcher must not write a.quit from its goroutine — every write
// to UI state belongs on the main thread — so it posts this event and
// handleEvent does the write.
func TestQuitEvent_SetsTheQuitFlag(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	if a.quit {
		t.Fatal("a fresh app is already quitting")
	}
	a.handleEvent(&quitEvent{when: time.Now()})
	if !a.quit {
		t.Error("a quitEvent did not set the quit flag")
	}
}

// TestSignalWatch_StopIsIdempotent covers the normal-exit path. Close calls
// stopSignalWatch, and Close is reachable more than once (a deferred call
// plus the watcher's own hard-exit path), so a second call must not panic on
// a channel that is already closed.
func TestSignalWatch_StopIsIdempotent(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.startSignalWatch()
	if a.signalStop == nil {
		t.Fatal("startSignalWatch registered nothing")
	}
	a.stopSignalWatch()
	a.stopSignalWatch() // must not panic on an already-closed channel.
	if a.signalStop != nil {
		t.Error("signalStop should be nil once stopped")
	}
}

// TestRunLoop_ExitsOnQuitEvent proves the posted event actually breaks the
// loop rather than merely setting a flag nothing reads. This is the half
// that makes the graceful shutdown path real: without it the watcher's
// grace period would always expire and every exit would be the hard one.
func TestRunLoop_ExitsOnQuitEvent(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	scr := a.screen.(tcell.SimulationScreen)

	done := make(chan error, 1)
	go func() { done <- a.Run() }()

	// Let Run reach PollEvent before waking it.
	time.Sleep(50 * time.Millisecond)
	if err := scr.PostEvent(&quitEvent{when: time.Now()}); err != nil {
		t.Fatalf("post: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after a quitEvent — the loop is wedged")
	}
}

// TestLeaderArmed_WindowIsGenerous guards the fix for Esc-q not quitting.
// The old 500ms window is a typist's reflex; Vincent is read with one hand
// on the mouse, and a leader that silently expires reads as a broken
// keybinding rather than as a missed deadline.
func TestLeaderArmed_WindowIsGenerous(t *testing.T) {
	if doubleEscMs < time.Second {
		t.Errorf("doubleEscMs = %v, want at least a second", doubleEscMs)
	}

	a := newTestApp(t, t.TempDir())
	if a.leaderArmed() {
		t.Error("leader reports armed before any Esc")
	}
	a.lastEscape = time.Now()
	if !a.leaderArmed() {
		t.Error("leader should be armed immediately after Esc")
	}
	a.lastEscape = time.Now().Add(-2 * doubleEscMs)
	if a.leaderArmed() {
		t.Error("leader should have expired")
	}
}

// TestStatusBar_ShowsTheLeaderHint proves the armed window is visible. It
// was invisible state before: press Esc, nothing appears to happen, and
// whether the next key is a command or a keystroke depends on a timer you
// cannot see.
//
// The screen is widened well past any real terminal because the hint is
// generated from the whole leader table and truncated to fit: at the
// fixture's default 120 the tail (w close · q quit · ? keys) legitimately
// falls off the end. Phases 3b, 6b and 7 together added six keys and the
// full hint is now well past 240 runes. The number is not a claim about a
// terminal; it is a width at which "every binding reached the bar" is a
// claim the bar can satisfy at all, and the next test covers what happens
// when it cannot.
func TestStatusBar_ShowsTheLeaderHint(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	scr := a.screen.(tcell.SimulationScreen)
	scr.SetSize(320, 40)
	a.width, a.height = scr.Size()
	a.lastEscape = time.Now()
	a.draw()
	scr.Show()

	cells, w, _ := scr.GetContents()
	_, sy, _, _ := a.statusRect()
	row := make([]rune, 0, w)
	for x := 0; x < w; x++ {
		c := cells[sy*w+x]
		if len(c.Runes) == 0 {
			row = append(row, ' ')
			continue
		}
		row = append(row, c.Runes[0])
	}
	got := string(row)
	for _, want := range []string{"Esc", "d diff", "q quit", "? keys"} {
		if !strings.Contains(got, want) {
			t.Errorf("status bar = %q, want it to mention %q", got, want)
		}
	}
}

// TestStatusBar_LeaderHintTruncatesWithEllipsis covers the narrow case the
// test above deliberately steps around: the hint is longer than a 120-cell
// bar, so it has to be cut with a visible ellipsis rather than silently
// clipped mid-word or drawn over the branch label.
func TestStatusBar_LeaderHintTruncatesWithEllipsis(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.lastEscape = time.Now()
	a.draw()
	scr := a.screen.(tcell.SimulationScreen)
	scr.Show()

	cells, w, _ := scr.GetContents()
	_, sy, _, _ := a.statusRect()
	row := make([]rune, 0, w)
	for x := 0; x < w; x++ {
		c := cells[sy*w+x]
		if len(c.Runes) == 0 {
			row = append(row, ' ')
			continue
		}
		row = append(row, c.Runes[0])
	}
	got := string(row)

	if len([]rune(leaderHint())) <= w {
		t.Skip("leader table now fits in 120 cells — nothing to truncate")
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("truncated hint should end in an ellipsis: %q", got)
	}
	// The head is what has to survive: review bindings lead the table
	// precisely so the tail is what gets dropped.
	if !strings.Contains(got, "Esc — d diff · e open file · r note") {
		t.Fatalf("truncation ate the head of the table: %q", got)
	}
}
