// =============================================================================
// File: internal/app/frame_test.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// Tests for the repaint guard. Two things need pinning and they pull in
// opposite directions: motion that changes nothing must NOT cost a frame, and
// motion that changes something must. A regression in either direction is
// invisible in normal use — one shows up as flicker inside herdr, the other
// as a hover highlight that only lights up when you click.
//
// TestPaint_UnchangedFrameCostsNoBytes is the measurement from
// docs/research/2026-09-02/04-flicker.md reproduced against Vincent's real
// painters: it drives an actual terminfo screen over a fake tty and counts the
// escape sequences a frame puts on the wire.

package app

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/theme"
)

// motionApp builds an App with one real file open and paints it once, so the
// frame-key baseline matches what is on screen before the test feeds motion.
func motionApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	writeFileT(t, path, "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n")

	a := newTestApp(t, dir)
	a.openFile(path)
	a.paint()
	return a
}

// motionAt builds the pure-motion event a terminal in any-motion mode
// (xterm 1003) sends when the pointer crosses a cell: no buttons, no
// modifiers.
func motionAt(x, y int) *tcell.EventMouse {
	return tcell.NewEventMouse(x, y, tcell.ButtonNone, tcell.ModNone)
}

// TestHandleEventForFrame_MotionOverEditorSkipsRepaint is the whole point of
// the guard. Fifty motion reports over the editor pane — what the pointer
// crossing it actually generates — must produce zero repaints and leave the
// screen byte-identical.
func TestHandleEventForFrame_MotionOverEditorSkipsRepaint(t *testing.T) {
	a := motionApp(t)
	scr := a.screen.(tcell.SimulationScreen)

	before, w, h := scr.GetContents()
	snapshot := append([]tcell.SimCell(nil), before...)
	framesBefore := a.frames

	ex, ey, _, _ := a.editorRect()
	for i := 0; i < 50; i++ {
		// Same cell repeatedly, then a walk across the row: both are
		// "nothing observable changed" as far as the screen is concerned.
		x := ex + i%8
		if a.handleEventForFrame(motionAt(x, ey+2)) {
			t.Fatalf("motion #%d at (%d,%d) asked for a repaint", i, x, ey+2)
		}
	}

	if a.frames != framesBefore {
		t.Errorf("frames went %d -> %d across 50 motion events, want no change",
			framesBefore, a.frames)
	}
	after, w2, h2 := scr.GetContents()
	if w != w2 || h != h2 {
		t.Fatalf("screen resized under the test: %dx%d -> %dx%d", w, h, w2, h2)
	}
	for i := range snapshot {
		if !sameCell(snapshot[i], after[i]) {
			t.Fatalf("cell %d (col %d, row %d) changed: %q/%v -> %q/%v",
				i, i%w, i/w, snapshot[i].Runes, snapshot[i].Style,
				after[i].Runes, after[i].Style)
		}
	}
}

// sameCell compares two simulated cells on the two things a viewer can see.
func sameCell(a, b tcell.SimCell) bool {
	if a.Style != b.Style || len(a.Runes) != len(b.Runes) {
		return false
	}
	for i := range a.Runes {
		if a.Runes[i] != b.Runes[i] {
			return false
		}
	}
	return true
}

// TestHandleEventForFrame_GitPanelHoverRepaints is the other half: moving
// between two rows of the Changes panel changes the highlighted row, so it
// must repaint. Guarding too aggressively here is what would leave hover
// dead.
func TestHandleEventForFrame_GitPanelHoverRepaints(t *testing.T) {
	_, a := panelApp(t)
	a.paint()

	if len(a.lastGitPanelRows) < 2 {
		t.Fatalf("fixture drew %d panel rows, need at least 2", len(a.lastGitPanelRows))
	}
	px, _, _, _ := a.gitPanelRect()
	rowA := a.lastGitPanelRows[0].y
	rowB := a.lastGitPanelRows[1].y

	// Land on the first row. That is a change (hover was -1), so it draws.
	if !a.handleEventForFrame(motionAt(px+1, rowA)) {
		t.Fatal("moving onto a panel row did not ask for a repaint")
	}
	a.paint()
	if a.gitPanelHover != rowA {
		t.Fatalf("hover is %d, want row %d", a.gitPanelHover, rowA)
	}

	// Same row again: nothing changed.
	if a.handleEventForFrame(motionAt(px+2, rowA)) {
		t.Error("motion within the same panel row asked for a repaint")
	}

	// Different row: must repaint.
	if !a.handleEventForFrame(motionAt(px+1, rowB)) {
		t.Fatalf("moving from row %d to row %d did not ask for a repaint", rowA, rowB)
	}
	if a.gitPanelHover != rowB {
		t.Fatalf("hover is %d, want row %d", a.gitPanelHover, rowB)
	}
}

// TestHandleEventForFrame_ExpiredFlashRepaints pins the time-dependent half.
// The status flash lapses on a clock nothing posts an event for, so the frame
// key has to be compared against the last PAINTED frame rather than against
// the state just before the event — otherwise both sides read "expired", the
// guard skips, and the stale message sits on screen until something else
// happens.
func TestHandleEventForFrame_ExpiredFlashRepaints(t *testing.T) {
	a := motionApp(t)
	a.flash("hello")
	a.paint()

	ex, ey, _, _ := a.editorRect()
	if a.handleEventForFrame(motionAt(ex+1, ey+1)) {
		t.Error("motion while the flash is still live asked for a repaint")
	}

	// Expire it the way the clock would.
	a.statusUntil = a.statusUntil.Add(-2 * statusFlashFor)
	if !a.handleEventForFrame(motionAt(ex+2, ey+1)) {
		t.Fatal("motion after the flash expired did not ask for a repaint")
	}
}

// TestHandleEventForFrame_ArmedLeaderRepaints is the same argument for the
// Esc-leader hint, which also disappears on a timer.
func TestHandleEventForFrame_ArmedLeaderRepaints(t *testing.T) {
	a := motionApp(t)
	a.handleKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if !a.leaderArmed() {
		t.Fatal("Esc did not arm the leader")
	}
	a.paint()

	ex, ey, _, _ := a.editorRect()
	if a.handleEventForFrame(motionAt(ex+1, ey+1)) {
		t.Error("motion while the leader is armed asked for a repaint")
	}
	a.lastEscape = a.lastEscape.Add(-2 * doubleEscMs)
	if !a.handleEventForFrame(motionAt(ex+2, ey+1)) {
		t.Fatal("motion after the leader expired did not ask for a repaint")
	}
}

// TestHandleEventForFrame_NonMotionAlwaysRepaints guards the deliberate
// asymmetry: only pure motion gets the cheap test. A wheel notch, a click, a
// key, and a resize all repaint unconditionally, because tracking every state
// change they can cause is exactly the bookkeeping that rots.
func TestHandleEventForFrame_NonMotionAlwaysRepaints(t *testing.T) {
	a := motionApp(t)
	ex, ey, _, _ := a.editorRect()

	cases := []struct {
		name string
		ev   tcell.Event
	}{
		{"wheel", tcell.NewEventMouse(ex+1, ey+1, tcell.WheelDown, tcell.ModNone)},
		{"click", tcell.NewEventMouse(ex+1, ey+1, tcell.Button1, tcell.ModNone)},
		{"key", tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone)},
		{"resize", tcell.NewEventResize(a.width, a.height)},
		{"treeRefresh", &treeRefreshEvent{}},
	}
	for _, tc := range cases {
		a.paint()
		if !a.handleEventForFrame(tc.ev) {
			t.Errorf("%s event did not ask for a repaint", tc.name)
		}
	}
}

// TestIsPureMotion covers the classifier on its own, including the wheel
// bits — tcell folds those into the button mask, and letting one through the
// skip path would silently eat a scroll.
func TestIsPureMotion(t *testing.T) {
	cases := []struct {
		name string
		ev   tcell.Event
		want bool
	}{
		{"motion", motionAt(3, 4), true},
		{"motion with shift", tcell.NewEventMouse(3, 4, tcell.ButtonNone, tcell.ModShift), true},
		{"button1", tcell.NewEventMouse(3, 4, tcell.Button1, tcell.ModNone), false},
		{"wheel up", tcell.NewEventMouse(3, 4, tcell.WheelUp, tcell.ModNone), false},
		{"wheel down", tcell.NewEventMouse(3, 4, tcell.WheelDown, tcell.ModNone), false},
		{"wheel left", tcell.NewEventMouse(3, 4, tcell.WheelLeft, tcell.ModNone), false},
		{"key", tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone), false},
		{"resize", tcell.NewEventResize(10, 10), false},
	}
	for _, tc := range cases {
		if got := isPureMotion(tc.ev); got != tc.want {
			t.Errorf("%s: isPureMotion = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRun_CoalescesMotionBurst drives the real loop. Eight motion reports and
// a quit arrive as one queued burst; the loop must handle all nine and paint
// exactly once for the lot, on top of the frame Run paints before it starts
// polling.
func TestRun_CoalescesMotionBurst(t *testing.T) {
	a := motionApp(t)
	a.frames = 0

	ex, ey, _, _ := a.editorRect()
	for i := 0; i < 8; i++ {
		if err := a.screen.PostEvent(motionAt(ex+i, ey+1)); err != nil {
			t.Fatalf("post motion %d: %v", i, err)
		}
	}
	if err := a.screen.PostEvent(&quitEvent{}); err != nil {
		t.Fatalf("post quit: %v", err)
	}

	if err := a.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	// One for Run's opening paint, one for the drained burst (the quit
	// event in it is not motion, so it earns a frame).
	if a.frames != 2 {
		t.Errorf("frames = %d, want 2 (opening paint + one coalesced burst)", a.frames)
	}
}

// TestRun_KeyInMotionBurstIsStillHandled is the risk the drain loop
// introduces: swallowing input. A key buried between motion reports must
// still take effect, in order.
func TestRun_KeyInMotionBurstIsStillHandled(t *testing.T) {
	a := motionApp(t)
	ex, ey, _, _ := a.editorRect()

	post := func(ev tcell.Event) {
		t.Helper()
		if err := a.screen.PostEvent(ev); err != nil {
			t.Fatalf("post: %v", err)
		}
	}
	post(motionAt(ex+1, ey+1))
	post(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	post(motionAt(ex+2, ey+1))
	post(&quitEvent{})

	if err := a.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !a.leaderArmed() {
		t.Error("the Esc buried in the motion burst never armed the leader")
	}
}

// -----------------------------------------------------------------------------
// Byte-count harness
// -----------------------------------------------------------------------------

// countingTty is a tcell.Tty that swallows input and counts every byte tcell
// writes. It is the only way to see what a frame actually costs: a
// SimulationScreen never encodes anything, so it can prove the *contents* are
// identical but not that no escape sequences went out.
type countingTty struct {
	mu     sync.Mutex
	n      int
	closed chan struct{}
}

// newCountingTty returns a tty ready to hand to tcell.
func newCountingTty() *countingTty {
	return &countingTty{closed: make(chan struct{})}
}

// Write records the byte count and discards the data.
func (c *countingTty) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.n += len(p)
	c.mu.Unlock()
	return len(p), nil
}

// Read blocks until Close, which is what tcell's input loop expects of a tty
// with nobody typing at it.
func (c *countingTty) Read(p []byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

// Close wakes the reader so tcell's input goroutine can exit.
func (c *countingTty) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

// Start satisfies tcell.Tty. There is no real terminal state to save.
func (c *countingTty) Start() error { return nil }

// Stop satisfies tcell.Tty.
func (c *countingTty) Stop() error { return nil }

// Drain satisfies tcell.Tty.
func (c *countingTty) Drain() error { return nil }

// NotifyResize satisfies tcell.Tty. The size never changes here.
func (c *countingTty) NotifyResize(func()) {}

// WindowSize pins the harness at 200x50 — the geometry the flicker report
// measured, so the numbers are comparable to it.
func (c *countingTty) WindowSize() (tcell.WindowSize, error) {
	return tcell.WindowSize{Width: 200, Height: 50, PixelWidth: 0, PixelHeight: 0}, nil
}

// count reads the running total.
func (c *countingTty) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// reset zeroes the running total between frames.
func (c *countingTty) reset() {
	c.mu.Lock()
	c.n = 0
	c.mu.Unlock()
}

// TestPaint_UnchangedFrameCostsNoBytes measures Vincent's real painters over
// a real terminfo screen, which is the claim the flicker report makes and the
// one worth defending: a frame nothing changed in must not reach the wire.
//
// The numbers land in the test log so a future change to the painters can be
// compared against them rather than argued about.
func TestPaint_UnchangedFrameCostsNoBytes(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	tty := newCountingTty()
	scr, err := tcell.NewTerminfoScreenFromTty(tty)
	if err != nil {
		t.Skipf("no terminfo for xterm-256color on this host: %v", err)
	}
	if err := scr.Init(); err != nil {
		t.Skipf("terminfo screen init: %v", err)
	}
	// Close the tty BEFORE Fini. tcell's Fini waits for its input goroutine,
	// which is parked in countingTty.Read, and Drain is a no-op here — so
	// finalising first deadlocks the test rather than failing it.
	defer func() {
		_ = tty.Close()
		scr.Fini()
	}()
	scr.SetStyle(tcell.StyleDefault.
		Background(theme.Default().BG).
		Foreground(theme.Default().Text))

	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	writeFileT(t, path, sampleSource())

	a := newTestApp(t, dir)
	a.screen = scr
	a.width, a.height = scr.Size()
	a.openFile(path)

	// First frame: everything is new, so this is what a real repaint costs.
	tty.reset()
	a.paint()
	firstFrame := tty.count()

	// Second frame: identical content, painted anyway. This is the number
	// the report measured at 56,619 bytes for the fill-then-glyph idiom.
	tty.reset()
	a.paint()
	repaintBytes := tty.count()

	// Third "frame": a pure-motion event over the editor, through the guard
	// the way Run does it. Nothing changed, so nothing is drawn at all.
	tty.reset()
	ex, ey, _, _ := a.editorRect()
	if a.handleEventForFrame(motionAt(ex+4, ey+4)) {
		a.paint()
	}
	motionBytes := tty.count()

	t.Logf("bytes/frame at %dx%d: first paint %d, identical repaint %d, guarded motion event %d",
		a.width, a.height, firstFrame, repaintBytes, motionBytes)

	if motionBytes != 0 {
		t.Errorf("a pure-motion event that changed nothing put %d bytes on the wire, want 0",
			motionBytes)
	}
	if firstFrame == 0 {
		t.Error("the first paint wrote nothing — the harness is not measuring anything")
	}
	// An identical repaint measured 12,828 bytes before the painters were
	// made idempotent and screen.Clear() came out of draw(); it measures 343
	// after. The ceiling here is deliberately loose — the point is to fail
	// loudly if someone reintroduces a fill-then-glyph pass or a Clear(),
	// which puts the number back into five figures, not to pin an exact
	// byte count that a theme tweak would break.
	const repaintCeiling = 4096
	if repaintBytes > repaintCeiling {
		t.Errorf("an identical repaint cost %d bytes, want under %d — a painter is "+
			"probably filling its rect before writing glyphs again, or draw() got "+
			"its screen.Clear() back",
			repaintBytes, repaintCeiling)
	}
}

// sampleSource is a few hundred lines of plausible Go so the editor pane is
// full of distinct Chroma styles. A blank file would understate the frame
// cost, because the cheapest cell to re-transmit is one that matches its
// neighbour's style.
func sampleSource() string {
	var b bytes.Buffer
	b.WriteString("package sample\n\nimport \"fmt\"\n\n")
	for i := 0; i < 300; i++ {
		b.WriteString("// doc comment explaining why helper exists at all\n")
		b.WriteString("func helper(n int) string {\n")
		b.WriteString("\tif n > 42 {\n")
		b.WriteString("\t\treturn fmt.Sprintf(\"big %d and then some trailing text\", n)\n")
		b.WriteString("\t}\n\treturn \"small\"\n}\n\n")
	}
	return b.String()
}

// TestCountingTty_ReadUnblocksOnClose pins the one piece of the harness that
// can hang a test run: tcell's input goroutine sits in Read forever unless
// Close wakes it.
func TestCountingTty_ReadUnblocksOnClose(t *testing.T) {
	tty := newCountingTty()
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 8)
		_, err := tty.Read(buf)
		done <- err
	}()
	if err := tty.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatalf("read returned %v, want io.EOF", err)
	}
}
