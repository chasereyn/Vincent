// =============================================================================
// File: internal/editor/diffview_test.go
// Author: Chase Reynolds
// Created: 2026-08-15
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package editor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/diff"
	"github.com/chasereyn/vincent/internal/theme"
)

// diffFixture is a one-hunk diff with an in-place edit (so there is a word
// range to tint) plus an unpaired addition. Assembled from a slice because
// the leading space on a context line must survive whitespace trimming.
var diffFixture = strings.Join([]string{
	"@@ -10,3 +10,4 @@",
	" alpha",
	"-bravo one",
	"+bravo two",
	"+charlie",
	" delta",
	"",
}, "\n")

// newDiffScreen builds a diff tab and paints it into a simulation screen of
// the given size, returning both so a test can assert on cells.
func newDiffScreen(t *testing.T, w, h int) (*Tab, tcell.SimulationScreen) {
	t.Helper()
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	t.Cleanup(scr.Fini)
	scr.SetSize(w, h)

	tab := NewDiffTab("sample.go", diff.Parse(diffFixture))
	tab.Render(scr, theme.Default(), 0, 0, w, h)
	scr.Show()
	return tab, scr
}

// rowText reads back one rendered row as a string, trailing blanks trimmed.
func rowText(t *testing.T, scr tcell.SimulationScreen, row int) string {
	t.Helper()
	cells, w, _ := scr.GetContents()
	var b strings.Builder
	for x := 0; x < w; x++ {
		c := cells[row*w+x]
		if len(c.Runes) == 0 {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(c.Runes[0])
	}
	return strings.TrimRight(b.String(), " ")
}

// cellBG returns the background colour of one rendered cell.
func cellBG(t *testing.T, scr tcell.SimulationScreen, x, row int) tcell.Color {
	t.Helper()
	cells, w, _ := scr.GetContents()
	_, bg, _ := cells[row*w+x].Style.Decompose()
	return bg
}

// TestNewDiffTab_IsReadOnlyAndParallelToRows pins the two structural
// invariants everything else leans on: the tab reports itself read-only, and
// its buffer runs line-for-line parallel to the diff rows (which is what
// lets scrolling, clamping, and the find bar work on a diff unchanged).
func TestNewDiffTab_IsReadOnlyAndParallelToRows(t *testing.T) {
	rows := diff.Parse(diffFixture)
	tab := NewDiffTab("sample.go", rows)

	if !tab.IsDiff() {
		t.Error("IsDiff() = false")
	}
	if !tab.ReadOnly() {
		t.Error("ReadOnly() = false — a diff must never accept edits")
	}
	if tab.IsImage() {
		t.Error("IsImage() = true on a diff tab")
	}
	if got := tab.Buffer.LineCount(); got != len(rows) {
		t.Fatalf("buffer has %d lines for %d diff rows", got, len(rows))
	}
	for i, r := range rows {
		if tab.Buffer.Lines[i] != r.Text {
			t.Errorf("line %d = %q, want %q", i, tab.Buffer.Lines[i], r.Text)
		}
	}
}

// TestDiffTab_RefusesEveryMutation is the most important test in this file.
// A diff tab carries the REAL FILE'S Path, so any mutation path that stays
// open on one is a route to writing diff text over the user's source. Each
// entry point is checked at the Tab level rather than at the menu, because
// the menu is not the only caller — leader keys reach several of these
// directly.
func TestDiffTab_RefusesEveryMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	const original = "package main\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tab := NewDiffTab(path, diff.Parse(diffFixture))
	before := strings.Join(tab.Buffer.Lines, "\n")

	tab.InsertRune('x')
	tab.InsertString("hello")
	tab.Backspace()
	tab.Delete()
	tab.SelectAll()
	tab.DeleteSelection()
	if changed, _ := tab.ToggleLineComment(); changed {
		t.Error("ToggleLineComment mutated a diff tab")
	}
	if got := strings.Join(tab.Buffer.Lines, "\n"); got != before {
		t.Errorf("diff buffer was mutated:\n got %q\nwant %q", got, before)
	}

	// Save must refuse rather than write the diff over sample.go.
	if err := tab.Save(); err == nil {
		t.Error("Save() on a diff tab returned nil — it should refuse")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(onDisk) != original {
		t.Fatalf("SAVE OVERWROTE THE SOURCE FILE: %q", string(onDisk))
	}

	// The history actions must stay disabled too: a diff's buffer is
	// replaced wholesale on refresh, which a naive CanRevert reads as
	// "the user has edits".
	if tab.CanUndo() || tab.CanRedo() || tab.CanRevert() {
		t.Errorf("history enabled on a diff tab: undo=%v redo=%v revert=%v",
			tab.CanUndo(), tab.CanRedo(), tab.CanRevert())
	}
	tab.SetDiffRows(diff.Parse("@@ -1,1 +1,1 @@\n-a\n+b\n"))
	if tab.CanRevert() {
		t.Error("CanRevert() true after a diff refresh — revert would desync the rows")
	}
}

// TestDiffTab_DisplayNameMarksTheDiff proves a file and its diff are
// distinguishable in the tab bar. They share a Path and can be open at the
// same time, so the name is the only thing telling them apart.
func TestDiffTab_DisplayNameMarksTheDiff(t *testing.T) {
	tab := NewDiffTab("/tmp/sample.go", diff.Parse(diffFixture))
	if got := tab.DisplayName(); got != "sample.go ±" {
		t.Errorf("DisplayName = %q, want %q", got, "sample.go ±")
	}
}

// TestRenderDiff_DrawsBothGuttersAndMarkers checks the row prefix: old and
// new line numbers side by side, a blank in the column where the row does
// not exist, and the ± marker. This is the whole point of an inline diff —
// being able to read which line of which file you are looking at.
func TestRenderDiff_DrawsBothGuttersAndMarkers(t *testing.T) {
	_, scr := newDiffScreen(t, 40, 10)

	// Row 0 is context: present on both sides, no marker.
	if got, want := rowText(t, scr, 0), "10 10   alpha"; got != want {
		t.Errorf("context row = %q, want %q", got, want)
	}
	// Row 1 is the deletion: an old number, a blank new column, a '-'.
	if got, want := rowText(t, scr, 1), "11    - bravo one"; got != want {
		t.Errorf("deleted row = %q, want %q", got, want)
	}
	// Row 2 is the addition: blank old column, a new number, a '+'.
	if got, want := rowText(t, scr, 2), "   11 + bravo two"; got != want {
		t.Errorf("added row = %q, want %q", got, want)
	}
}

// TestRenderDiff_TintsFullRowWidth is a deliberate anti-regression. A tint
// that stops at end-of-line makes a short changed line look like a different
// kind of change from a long one, which is exactly the false signal a review
// tool must not emit.
func TestRenderDiff_TintsFullRowWidth(t *testing.T) {
	th := theme.Default()
	_, scr := newDiffScreen(t, 40, 10)

	// Far past the end of "bravo one" — still inside the deletion's tint.
	if got := cellBG(t, scr, 38, 1); got != th.DiffDelBG {
		t.Errorf("deleted row trailing cell bg = %v, want DiffDelBG %v", got, th.DiffDelBG)
	}
	if got := cellBG(t, scr, 38, 2); got != th.DiffAddBG {
		t.Errorf("added row trailing cell bg = %v, want DiffAddBG %v", got, th.DiffAddBG)
	}
	// A context row keeps the editor background — unless it is the cursor
	// row, which row 0 is on a freshly built tab, so check row 4 instead.
	if got := cellBG(t, scr, 38, 4); got != th.BG {
		t.Errorf("context row bg = %v, want BG %v", got, th.BG)
	}
}

// TestRenderDiff_WordTintCoversOnlyTheChangedRun checks the darker
// character-level tint. "bravo one" -> "bravo two" shares the prefix
// "bravo " and nothing at the end, so exactly "one" / "two" should carry the
// word colour and the shared prefix should stay on the plain row tint.
func TestRenderDiff_WordTintCoversOnlyTheChangedRun(t *testing.T) {
	th := theme.Default()
	tab, scr := newDiffScreen(t, 40, 10)

	gutter := diffGutterCells(tab.DiffRows)
	// "bravo one": b-r-a-v-o-space are shared, "one" is not.
	for i, want := range map[int]tcell.Color{
		0: th.DiffDelBG,     // 'b' — shared prefix
		5: th.DiffDelBG,     // ' ' — shared prefix
		6: th.DiffDelWordBG, // 'o' — changed
		8: th.DiffDelWordBG, // 'e' — changed
	} {
		if got := cellBG(t, scr, gutter+i, 1); got != want {
			t.Errorf("deleted row col %d bg = %v, want %v", i, got, want)
		}
	}
	// The unpaired addition ("charlie") replaced nothing, so no part of it
	// is a "changed middle".
	for i := 0; i < 7; i++ {
		if got := cellBG(t, scr, gutter+i, 3); got != th.DiffAddBG {
			t.Errorf("unpaired addition col %d bg = %v, want plain DiffAddBG", i, got)
		}
	}
}

// TestRenderDiff_HidesTheCaret confirms a diff has a selected row rather
// than an insertion point. A blinking caret in a read-only view invites the
// user to type into something that will not accept it.
func TestRenderDiff_HidesTheCaret(t *testing.T) {
	_, scr := newDiffScreen(t, 40, 10)
	if x, y, visible := scr.GetCursor(); visible {
		t.Errorf("cursor shown at (%d, %d) on a diff tab", x, y)
	}
}

// TestRenderDiff_GapBetweenHunks proves the elision marker is drawn, so two
// unrelated regions of a file are never silently butted together.
func TestRenderDiff_GapBetweenHunks(t *testing.T) {
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer scr.Fini()
	scr.SetSize(40, 10)

	tab := NewDiffTab("x.go", diff.Parse("@@ -1,1 +1,1 @@\n one\n@@ -90,1 +90,1 @@\n two\n"))
	tab.Render(scr, theme.Default(), 0, 0, 40, 10)
	scr.Show()

	if got := rowText(t, scr, 1); !strings.Contains(got, "⋯") {
		t.Errorf("row between hunks = %q, want an elision marker", got)
	}
	// The hunk after the gap must resume at its own line numbers, not
	// continue counting from the first hunk.
	if got := rowText(t, scr, 2); !strings.HasPrefix(got, "90 90") {
		t.Errorf("post-gap row = %q, want it to resume at line 90", got)
	}
}

// TestDiffHitTest_MapsClicksToRows covers the click path phase 2's comment
// composer will hang off. Gutter clicks resolve to their row rather than
// being a dead zone: in a read-only view there is no reason for a line
// number to be unclickable.
func TestDiffHitTest_MapsClicksToRows(t *testing.T) {
	tab, _ := newDiffScreen(t, 40, 10)
	gutter := diffGutterCells(tab.DiffRows)

	pos, ok := tab.HitTest(gutter+2, 2, 40, 10)
	if !ok {
		t.Fatal("click on a diff row was not resolved")
	}
	if pos.Line != 2 {
		t.Errorf("clicked row = %d, want 2", pos.Line)
	}
	if pos.Col != 2 {
		t.Errorf("clicked col = %d, want 2", pos.Col)
	}

	// A gutter click lands on column zero of the same row.
	pos, ok = tab.HitTest(1, 3, 40, 10)
	if !ok || pos.Line != 3 || pos.Col != 0 {
		t.Errorf("gutter click = (%d, %d) ok=%v, want row 3 col 0", pos.Line, pos.Col, ok)
	}

	// Below the last row there is nothing to hit.
	if _, ok := tab.HitTest(gutter, 9, 40, 10); ok {
		t.Error("click past the last diff row should not resolve")
	}
}

// TestScrollToRow_FramesTheChange checks that jumping to a row leaves
// context visible above it. Landing a change on the top line of the pane
// hides the lines that tell you whether the change is correct.
func TestScrollToRow_FramesTheChange(t *testing.T) {
	rows := diff.Parse("@@ -1,60 +1,60 @@\n" + strings.Repeat(" ctx\n", 50) + "-old\n+new\n")
	tab := NewDiffTab("x.go", rows)

	tab.ScrollToRow(50, 30)
	if tab.Cursor.Line != 50 {
		t.Errorf("cursor = %d, want the target row 50", tab.Cursor.Line)
	}
	if tab.ScrollY != 40 {
		t.Errorf("ScrollY = %d, want 40 (a third of a 30-row viewport above)", tab.ScrollY)
	}

	// Near the top there is nothing to scroll back to, so it clamps.
	tab.ScrollToRow(2, 30)
	if tab.ScrollY != 0 {
		t.Errorf("ScrollY = %d near the top, want 0", tab.ScrollY)
	}

	// Out-of-range rows are ignored rather than scrolling into the void.
	before := tab.ScrollY
	tab.ScrollToRow(9999, 30)
	if tab.ScrollY != before {
		t.Errorf("ScrollY moved to %d for an out-of-range row", tab.ScrollY)
	}
}

// TestSetDiffRows_KeepsScrollPosition covers the live-refresh path: when the
// agent writes to the file again, the diff updates underneath the reviewer
// without yanking them back to the top of it.
func TestSetDiffRows_KeepsScrollPosition(t *testing.T) {
	long := diff.Parse("@@ -1,60 +1,60 @@\n" + strings.Repeat(" ctx\n", 50) + "-old\n+new\n")
	tab := NewDiffTab("x.go", long)
	tab.ScrollY = 30

	tab.SetDiffRows(diff.Parse(diffFixture))

	if tab.ScrollY != 30 {
		t.Errorf("ScrollY = %d after refresh, want it preserved at 30", tab.ScrollY)
	}
	if got := tab.Buffer.LineCount(); got != 5 {
		t.Errorf("buffer has %d lines after refresh, want 5", got)
	}
	// The cursor must be clamped into the new, much shorter diff — leaving
	// it past the end would index out of range on the next render.
	if tab.Cursor.Line >= tab.Buffer.LineCount() {
		t.Errorf("cursor line %d is past the refreshed buffer", tab.Cursor.Line)
	}
	if !tab.StyleStale {
		t.Error("StyleStale = false after a refresh — the old highlighting would be reused")
	}
}

// TestDiffStats surfaces the counts the status bar leads with.
func TestDiffStats(t *testing.T) {
	tab := NewDiffTab("x.go", diff.Parse(diffFixture))
	added, deleted := tab.DiffStats()
	if added != 2 || deleted != 1 {
		t.Errorf("DiffStats = +%d −%d, want +2 −1", added, deleted)
	}
}

// TestDiffGutterCells_GrowsWithLineNumbers keeps the two number columns wide
// enough for the largest line in the diff. Too narrow and a four-digit line
// number is silently clipped to three.
func TestDiffGutterCells_GrowsWithLineNumbers(t *testing.T) {
	small := diff.Parse("@@ -1,1 +1,1 @@\n one\n")
	big := diff.Parse("@@ -12000,1 +12000,1 @@\n one\n")

	// Floor of two digits per column: 2*2 + 4.
	if got := diffGutterCells(small); got != 8 {
		t.Errorf("small gutter = %d, want 8", got)
	}
	// Five digits per column: 5*2 + 4.
	if got := diffGutterCells(big); got != 14 {
		t.Errorf("wide gutter = %d, want 14", got)
	}
}

// TestNewDiffTab_EmptyDiffStillHasALine guards the degenerate case. Enough
// surrounding code assumes a buffer has at least one line that an empty one
// would panic somewhere far from here.
func TestNewDiffTab_EmptyDiffStillHasALine(t *testing.T) {
	tab := NewDiffTab("x.go", nil)
	if tab.Buffer.LineCount() < 1 {
		t.Fatal("an empty diff must still have one buffer line")
	}
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer scr.Fini()
	scr.SetSize(20, 5)
	tab.Render(scr, theme.Default(), 0, 0, 20, 5) // must not panic.
}
