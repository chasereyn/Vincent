// =============================================================================
// File: internal/editor/diffoverlay_test.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package editor

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/diff"
	"github.com/chasereyn/vincent/internal/theme"
)

// overlayTab builds the standard diff fixture with one two-line overlay
// hung under diff row 1, and paints it. Returns the tab and screen so a
// test can assert on either.
func overlayTab(t *testing.T, w, h int) (*Tab, tcell.SimulationScreen) {
	t.Helper()
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	t.Cleanup(scr.Fini)
	scr.SetSize(w, h)

	tab := NewDiffTab("sample.go", diff.Parse(diffFixture))
	tab.SetDiffOverlays([]DiffOverlay{{Row: 1, Lines: []DiffOverlayLine{
		{Text: "┌─ note ─┐", FG: tcell.ColorWhite, BG: tcell.ColorBlack, Caret: NoCaret},
		{Text: "│ typed  │", FG: tcell.ColorWhite, BG: tcell.ColorBlack, Caret: 3},
	}}})
	tab.Render(scr, theme.Default(), 0, 0, w, h)
	scr.Show()
	return tab, scr
}

// TestDiffOverlay_PushesRowsDown pins the core promise of an inline box: it
// takes real screen rows, and the diff row that used to be under the anchor
// now appears below the overlay instead of being covered by it.
func TestDiffOverlay_PushesRowsDown(t *testing.T) {
	tab, scr := overlayTab(t, 40, 10)

	// The overlay hangs UNDER diff row 1, so screen row 1 is still that
	// diff row and screen rows 2-3 are the overlay's own two lines.
	if got := rowText(t, scr, 1); !strings.Contains(got, "bravo one") {
		t.Fatalf("screen row 1 = %q, want the anchor diff row", got)
	}
	if got := rowText(t, scr, 2); got != "┌─ note ─┐" {
		t.Errorf("screen row 2 = %q, want the overlay's first line", got)
	}
	if got := rowText(t, scr, 3); got != "│ typed  │" {
		t.Errorf("screen row 3 = %q, want the overlay's second line", got)
	}
	// The row that would have been at screen row 2 without the overlay is
	// now two rows further down, still visible rather than hidden.
	if got := rowText(t, scr, 4); !strings.Contains(got, "bravo two") {
		t.Errorf("screen row 4 = %q, want the pushed-down diff row", got)
	}
	// Scrolling is still measured in diff rows: the overlay must not have
	// moved the viewport.
	if tab.ScrollY != 0 {
		t.Errorf("ScrollY = %d, overlays must not scroll the viewport", tab.ScrollY)
	}
}

// TestDiffOverlay_FillsFullWidth pins that an overlay row paints its
// background across the whole pane. A background that stops at the end of
// the text reads as a smear over the diff rather than as a panel in it.
func TestDiffOverlay_FillsFullWidth(t *testing.T) {
	_, scr := overlayTab(t, 40, 10)
	if got := cellBG(t, scr, 39, 2); got != tcell.ColorBlack {
		t.Errorf("far-right cell of an overlay row has bg %v, want black", got)
	}
}

// TestDiffOverlay_ShowsCaret pins that a Caret offset becomes a real
// terminal cursor. Without it the composer looks dead while typing works.
func TestDiffOverlay_ShowsCaret(t *testing.T) {
	_, scr := overlayTab(t, 40, 10)
	x, y, visible := scr.GetCursor()
	if !visible {
		t.Fatal("overlay with a Caret should show the terminal cursor")
	}
	if x != 3 || y != 3 {
		t.Errorf("cursor at (%d,%d), want (3,3)", x, y)
	}
}

// TestDiffOverlay_NoCaretHidesCursor pins the default: a diff has no
// insertion point, so with no composer open the cursor stays hidden.
func TestDiffOverlay_NoCaretHidesCursor(t *testing.T) {
	_, scr := newDiffScreen(t, 40, 10)
	if _, _, visible := scr.GetCursor(); visible {
		t.Error("a diff with no overlay caret should hide the cursor")
	}
}

// TestDiffHitAt_ReportsOverlayLineAndColumn pins the click mapping: the two
// screen rows below the anchor resolve to overlay lines 0 and 1 of that
// row, and the x offset comes back as a rune column.
func TestDiffHitAt_ReportsOverlayLineAndColumn(t *testing.T) {
	tab, _ := overlayTab(t, 40, 10)

	hit, ok := tab.DiffHitAt(0, 1, 40, 10)
	if !ok || hit.Row != 1 || hit.Overlay != NoOverlay {
		t.Fatalf("row 1 hit = %+v ok=%v, want diff row 1 itself", hit, ok)
	}
	hit, ok = tab.DiffHitAt(5, 3, 40, 10)
	if !ok || hit.Row != 1 || hit.Overlay != 1 || hit.Col != 5 {
		t.Fatalf("row 3 hit = %+v ok=%v, want row 1 overlay 1 col 5", hit, ok)
	}
	// A click past the end of an overlay line clamps to its length rather
	// than reporting a column that does not exist.
	hit, _ = tab.DiffHitAt(38, 2, 40, 10)
	if hit.Col != 10 {
		t.Errorf("clamped col = %d, want 10 (length of the border line)", hit.Col)
	}
}

// TestDiffHitAt_BelowContentIsAMiss pins that clicking empty space under
// the last row reports no hit, so a stray click cannot select a row that
// isn't there.
func TestDiffHitAt_BelowContentIsAMiss(t *testing.T) {
	tab, _ := overlayTab(t, 40, 20)
	// Six diff rows plus two overlay rows = eight occupied screen rows.
	if hit, ok := tab.DiffHitAt(0, 12, 40, 20); ok {
		t.Fatalf("hit below content = %+v, want a miss", hit)
	}
}

// TestDiffHitTest_OverlayRowSnapsToAnchor pins that a drag passing over an
// overlay keeps extending the selection at the anchor row instead of
// stalling — the app claims real composer clicks before this path runs.
func TestDiffHitTest_OverlayRowSnapsToAnchor(t *testing.T) {
	tab, _ := overlayTab(t, 40, 10)
	pos, ok := tab.HitTest(7, 3, 40, 10)
	if !ok {
		t.Fatal("overlay row should still hit-test")
	}
	if pos.Line != 1 || pos.Col != 0 {
		t.Errorf("pos = %+v, want line 1 col 0", pos)
	}
}

// TestDiffSelectedRows_NormalisesDirection pins that dragging up a diff
// selects the same rows as dragging down it.
func TestDiffSelectedRows_NormalisesDirection(t *testing.T) {
	tab, _ := overlayTab(t, 40, 10)
	tab.Anchor = Position{Line: 4}
	tab.Cursor = Position{Line: 1}
	lo, hi := tab.DiffSelectedRows()
	if lo != 1 || hi != 4 {
		t.Fatalf("range = %d..%d, want 1..4", lo, hi)
	}
	if !tab.diffRowSelected(2) || tab.diffRowSelected(5) {
		t.Error("diffRowSelected disagrees with DiffSelectedRows")
	}
}
