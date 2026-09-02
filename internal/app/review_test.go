// =============================================================================
// File: internal/app/review_test.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// Nothing here runs the herdr binary: the send tests inject review.Run.
// A test that actually shelled out would stage a review batch in whichever
// agent pane happened to be open on the machine running the suite.
// =============================================================================

package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/diff"
	"github.com/chasereyn/vincent/internal/editor"
	"github.com/chasereyn/vincent/internal/review"
)

// reviewDiffText is a two-hunk diff with an in-place edit in the first hunk
// and a pure addition in the second. Two hunks so the hunk index is
// actually exercised; the trailing empty element is the newline git emits.
var reviewDiffText = strings.Join([]string{
	"@@ -10,3 +10,3 @@",
	" alpha",
	"-bravo one",
	"+bravo two",
	" charlie",
	"@@ -40,2 +40,3 @@",
	" delta",
	"+echo",
	"",
}, "\n")

// reviewRows is reviewDiffText parsed, which is what a diff tab holds.
func reviewRows() []diff.Row { return diff.Parse(reviewDiffText) }

// newTestDiffTab builds a diff tab over the fixture, for tests that only
// need a diff tab to exist and do not care which file it is of.
func newTestDiffTab() *editor.Tab {
	return editor.NewDiffTab("sample.go", reviewRows())
}

// reviewApp builds an App with one diff tab open over dir/sample.go, the
// Changes panel shown, and row lo..hi of the diff selected — the state a
// reviewer is in the moment before they press Esc r.
func reviewApp(t *testing.T, lo, hi int) (*App, *editor.Tab) {
	t.Helper()
	dir := t.TempDir()
	a := newTestApp(t, dir)
	tab := editor.NewDiffTab(filepath.Join(a.rootDir, "sample.go"), reviewRows())
	a.tabs = append(a.tabs, tab)
	a.activeTab = 0
	tab.Anchor = editor.Position{Line: lo}
	tab.Cursor = editor.Position{Line: hi}
	return a, tab
}

// fakeHerdr installs a review.Runner for one test and records the argv it
// is handed. list is the JSON `herdr agent list` replies with; sendErr, if
// non-nil, is what `pane send-text` fails with.
func fakeHerdr(t *testing.T, list string, sendErr error) *[][]string {
	t.Helper()
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "wE")
	t.Setenv("HERDR_PANE_ID", "wE:p1")

	calls := &[][]string{}
	prevRun, prevLog := review.Run, review.Logf
	review.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "agent list"):
			return []byte(list), nil
		case strings.HasPrefix(joined, "pane send-text") && sendErr != nil:
			return nil, sendErr
		}
		return nil, nil
	}
	review.Logf = func(string, ...any) {}
	t.Cleanup(func() { review.Run, review.Logf = prevRun, prevLog })
	return calls
}

// oneAgentJSON is an `agent list` reply with exactly one eligible pane, so
// the send path skips the picker.
const oneAgentJSON = `{"result":{"agents":[
{"agent":"claude","agent_status":"working","pane_id":"wE:p2","tab_id":"wE:t1","workspace_id":"wE","name":"the agent"}
]}}`

// twoAgentJSON is an `agent list` reply with two eligible panes, which is
// what forces the picker open.
const twoAgentJSON = `{"result":{"agents":[
{"agent":"claude","agent_status":"working","pane_id":"wE:p2","workspace_id":"wE","name":"first"},
{"agent":"claude","agent_status":"idle","pane_id":"wE:p3","workspace_id":"wE","name":"second"}
]}}`

// -----------------------------------------------------------------------------
// Anchoring
// -----------------------------------------------------------------------------

// TestCommentableRange_SkipsChrome pins that a selection dragged across a
// hunk boundary shrinks to the real lines inside it instead of being
// refused over the elision row in the middle.
func TestCommentableRange_SkipsChrome(t *testing.T) {
	rows := reviewRows()
	// Row 4 is the gap between the two hunks.
	if rows[4].Kind != diff.KindGap {
		t.Fatalf("fixture changed: row 4 is %v, expected the hunk gap", rows[4].Kind)
	}
	lo, hi, ok := commentableRange(rows, 3, 5)
	if !ok || lo != 3 || hi != 5 {
		t.Fatalf("range = %d..%d ok=%v, want 3..5", lo, hi, ok)
	}
	// A selection of nothing but the gap has nothing to comment on.
	if _, _, ok := commentableRange(rows, 4, 4); ok {
		t.Error("a gap-only selection should not be commentable")
	}
}

// TestRangeAddress_DeletionsOnlyUseTheOldSide pins the side rule: a range
// of nothing but deletions addresses the old file, because those lines do
// not exist any more and a new-side number would point somewhere else.
func TestRangeAddress_DeletionsOnlyUseTheOldSide(t *testing.T) {
	rows := reviewRows()
	side, start, end := rangeAddress(rows, 1, 1) // the "-bravo one" row
	if side != review.SideOld || start != 11 || end != 11 {
		t.Fatalf("deletion address = side %v %d..%d, want old 11..11", side, start, end)
	}
	// The same rows plus the addition below them address the new file.
	side, start, end = rangeAddress(rows, 1, 2)
	if side != review.SideNew || start != 11 || end != 11 {
		t.Fatalf("mixed address = side %v %d..%d, want new 11..11", side, start, end)
	}
	// A context range spans its new-side numbers.
	side, start, end = rangeAddress(rows, 0, 3)
	if side != review.SideNew || start != 10 || end != 12 {
		t.Fatalf("context address = side %v %d..%d, want new 10..12", side, start, end)
	}
}

// TestHunkIndexFor_CountsElisions pins that a note in the second hunk
// records hunk 1, not hunk 0.
func TestHunkIndexFor_CountsElisions(t *testing.T) {
	rows := reviewRows()
	if got := hunkIndexFor(rows, 1); got != 0 {
		t.Errorf("first hunk index = %d, want 0", got)
	}
	if got := hunkIndexFor(rows, 6); got != 1 {
		t.Errorf("second hunk index = %d, want 1", got)
	}
}

// TestSnippetFor_KeepsDiffPrefixes pins the anchor's exact text: every line
// keeps the +, - or space that says what happened to it, and chrome rows
// contribute nothing.
func TestSnippetFor_KeepsDiffPrefixes(t *testing.T) {
	got := snippetFor(reviewRows(), 0, 3)
	want := strings.Join([]string{" alpha", "-bravo one", "+bravo two", " charlie"}, "\n")
	if got != want {
		t.Fatalf("snippet =\n%q\nwant\n%q", got, want)
	}
}

// TestAnchorRowFor_FindsAndMissesCorrectly pins both halves of the
// never-rebase policy: the marker finds its row when the line is still in
// the diff, and reports -1 rather than picking a neighbour when it is not.
func TestAnchorRowFor_FindsAndMissesCorrectly(t *testing.T) {
	rows := reviewRows()
	if got := anchorRowFor(rows, review.Comment{Side: review.SideNew, End: 11}); got != 2 {
		t.Errorf("new-side anchor row = %d, want 2", got)
	}
	if got := anchorRowFor(rows, review.Comment{Side: review.SideOld, End: 11}); got != 1 {
		t.Errorf("old-side anchor row = %d, want 1", got)
	}
	if got := anchorRowFor(rows, review.Comment{Side: review.SideNew, End: 999}); got != -1 {
		t.Errorf("missing anchor = %d, want -1", got)
	}
}

// -----------------------------------------------------------------------------
// The composer
// -----------------------------------------------------------------------------

// TestOpenReviewComposer_RequiresADiff pins that the composer refuses to
// open over a plain file tab, and says so rather than doing nothing.
func TestOpenReviewComposer_RequiresADiff(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.openReviewComposer()
	if a.composerOpen {
		t.Fatal("composer should not open with no diff tab")
	}
	if !strings.Contains(a.statusMsg, "diff") {
		t.Errorf("flash = %q, want a mention of opening a diff", a.statusMsg)
	}
}

// TestOpenReviewComposer_FreezesTheAnchor pins what the composer captures
// at open: the file, the side, the line range, the owning hunk, and the
// verbatim snippet.
func TestOpenReviewComposer_FreezesTheAnchor(t *testing.T) {
	a, _ := reviewApp(t, 1, 2)
	a.openReviewComposer()

	if !a.composerOpen {
		t.Fatal("composer should be open")
	}
	if a.composerFile != "sample.go" {
		t.Errorf("file = %q, want sample.go", a.composerFile)
	}
	if a.composerSide != review.SideNew || a.composerStart != 11 || a.composerEnd != 11 {
		t.Errorf("range = side %v %d..%d, want new 11..11",
			a.composerSide, a.composerStart, a.composerEnd)
	}
	if a.composerHunk != 0 {
		t.Errorf("hunk = %d, want 0", a.composerHunk)
	}
	if want := "-bravo one\n+bravo two"; a.composerSnippet != want {
		t.Errorf("snippet = %q, want %q", a.composerSnippet, want)
	}
	if a.composerEdit != -1 {
		t.Errorf("composerEdit = %d, want -1 for a new note", a.composerEdit)
	}
}

// TestComposer_TypeCycleAndSave walks the whole gesture: type, Tab to a
// kind, Enter to save. The saved comment must carry the frozen anchor, and
// the composer must close.
func TestComposer_TypeCycleAndSave(t *testing.T) {
	a, _ := reviewApp(t, 2, 2)
	a.openReviewComposer()

	for _, r := range "wrong check" {
		a.handleKey(keyEv(tcell.KeyRune, r))
	}
	a.handleKey(keyEv(tcell.KeyTab, 0)) // None -> Issue
	if a.composerKind != review.KindIssue {
		t.Fatalf("kind = %v, want Issue after one Tab", a.composerKind)
	}
	a.handleKey(keyEv(tcell.KeyEnter, 0))

	if a.composerOpen {
		t.Fatal("Enter should close the composer")
	}
	if a.reviewBatch.Len() != 1 {
		t.Fatalf("batch has %d notes, want 1", a.reviewBatch.Len())
	}
	c := a.reviewBatch.Comments[0]
	if c.Text != "wrong check" || c.Kind != review.KindIssue {
		t.Errorf("comment = %+v, want the typed text and Issue", c)
	}
	if c.Snippet != "+bravo two" {
		t.Errorf("snippet = %q, want the frozen diff line", c.Snippet)
	}
}

// TestComposer_EscCancelsWithoutSaving pins that Esc inside the composer
// means "abandon this note", not "arm the leader".
func TestComposer_EscCancelsWithoutSaving(t *testing.T) {
	a, _ := reviewApp(t, 2, 2)
	a.openReviewComposer()
	a.handleKey(keyEv(tcell.KeyRune, 'x'))
	a.handleKey(keyEv(tcell.KeyEsc, 0))

	if a.composerOpen {
		t.Fatal("Esc should close the composer")
	}
	if a.reviewBatch.Len() != 0 {
		t.Fatalf("batch has %d notes, want none", a.reviewBatch.Len())
	}
	if a.leaderArmed() {
		t.Error("Esc inside the composer must not arm the leader")
	}
}

// TestComposer_EmptySaveDiscards pins that Enter on an empty new note
// discards it rather than storing a snippet with no words attached.
func TestComposer_EmptySaveDiscards(t *testing.T) {
	a, _ := reviewApp(t, 2, 2)
	a.openReviewComposer()
	a.handleKey(keyEv(tcell.KeyEnter, 0))

	if a.reviewBatch.Len() != 0 {
		t.Fatal("an empty note should not be saved")
	}
	if a.composerOpen {
		t.Fatal("composer should have closed")
	}
}

// TestComposer_BackspaceOnEmptyDeletesAnExistingNote pins the delete
// gesture: reopen a note, clear it, and one more Backspace removes it.
func TestComposer_BackspaceOnEmptyDeletesAnExistingNote(t *testing.T) {
	a, _ := reviewApp(t, 2, 2)
	a.openReviewComposer()
	a.handleKey(keyEv(tcell.KeyRune, 'x'))
	a.handleKey(keyEv(tcell.KeyEnter, 0))
	if a.reviewBatch.Len() != 1 {
		t.Fatalf("setup: batch has %d notes, want 1", a.reviewBatch.Len())
	}

	a.openReviewCommentForEdit(0)
	if a.composerEdit != 0 {
		t.Fatalf("composerEdit = %d, want 0", a.composerEdit)
	}
	a.handleKey(keyEv(tcell.KeyBackspace2, 0)) // clears the "x"
	a.handleKey(keyEv(tcell.KeyBackspace2, 0)) // deletes the note

	if a.reviewBatch.Len() != 0 {
		t.Fatalf("batch has %d notes, want the note deleted", a.reviewBatch.Len())
	}
	if a.composerOpen {
		t.Error("deleting the note should close the composer")
	}
}

// TestComposer_EditUpdatesInPlace pins that reopening a note and saving
// replaces it rather than appending a second one, and that the frozen
// anchor survives the edit.
func TestComposer_EditUpdatesInPlace(t *testing.T) {
	a, _ := reviewApp(t, 2, 2)
	a.openReviewComposer()
	a.handleKey(keyEv(tcell.KeyRune, 'a'))
	a.handleKey(keyEv(tcell.KeyEnter, 0))
	before := a.reviewBatch.Comments[0]

	a.openReviewCommentForEdit(0)
	a.handleKey(keyEv(tcell.KeyRune, 'b'))
	a.handleKey(keyEv(tcell.KeyEnter, 0))

	if a.reviewBatch.Len() != 1 {
		t.Fatalf("batch has %d notes, want the one note updated", a.reviewBatch.Len())
	}
	after := a.reviewBatch.Comments[0]
	if after.Text != "ab" {
		t.Errorf("text = %q, want %q", after.Text, "ab")
	}
	if after.Snippet != before.Snippet || after.Start != before.Start || after.Side != before.Side {
		t.Errorf("editing a note re-anchored it: %+v -> %+v", before, after)
	}
}

// TestComposer_RendersInlineUnderTheAnchor is the drawing test: the box
// occupies real rows in the diff, right under the line it is about, and
// carries the kind, the range and the key hints.
func TestComposer_RendersInlineUnderTheAnchor(t *testing.T) {
	a, tab := reviewApp(t, 2, 2)
	a.openReviewComposer()
	a.cycleComposerKind() // Issue
	a.composerValue = []rune("duplicates isTransient")
	a.composerCursor = len(a.composerValue)
	a.draw()
	a.screen.(tcell.SimulationScreen).Show()

	_, ey, _, _ := a.editorRect()
	// The anchor is diff row 2 and the diff opened at row 0, so the box
	// starts on the screen row just below it.
	row := ey + (2 - tab.ScrollY) + 1
	top := panelRowText(t, a, row)
	if !strings.Contains(top, "┌─ [ISSUE] L11") {
		t.Errorf("composer top border = %q, want the kind and line", strings.TrimRight(top, " "))
	}
	body := panelRowText(t, a, row+1)
	if !strings.Contains(body, "duplicates isTransient") {
		t.Errorf("composer text row = %q, want the typed note", strings.TrimRight(body, " "))
	}
	foot := panelRowText(t, a, row+2)
	if !strings.Contains(foot, "Tab kind · Enter save · Esc cancel") {
		t.Errorf("composer footer = %q, want the key hints", strings.TrimRight(foot, " "))
	}
	// The box must PUSH the diff down, not cover it: the row that follows
	// the anchor is still on screen, three rows lower.
	after := panelRowText(t, a, row+3)
	if !strings.Contains(after, "charlie") {
		t.Errorf("row below the box = %q, want the pushed-down diff row", strings.TrimRight(after, " "))
	}
}

// TestComposer_ShowsACaret pins that the inline box gets a real terminal
// cursor. A text field with no visible caret reads as broken.
func TestComposer_ShowsACaret(t *testing.T) {
	a, _ := reviewApp(t, 2, 2)
	a.openReviewComposer()
	a.composerValue = []rune("abc")
	a.composerCursor = 3
	a.draw()
	a.screen.(tcell.SimulationScreen).Show()

	if _, _, visible := a.screen.(tcell.SimulationScreen).GetCursor(); !visible {
		t.Fatal("the composer should show the terminal caret")
	}
}

// TestSavedNote_RendersAMarkerAndReopensOnClick pins the second surface:
// a saved note draws one compact row under its anchor, and clicking that
// row reopens it for editing.
func TestSavedNote_RendersAMarkerAndReopensOnClick(t *testing.T) {
	a, tab := reviewApp(t, 2, 2)
	a.openReviewComposer()
	a.cycleComposerKind()
	for _, r := range "note text" {
		a.handleKey(keyEv(tcell.KeyRune, r))
	}
	a.handleKey(keyEv(tcell.KeyEnter, 0))
	a.draw()
	a.screen.(tcell.SimulationScreen).Show()

	_, ey, _, _ := a.editorRect()
	row := ey + (2 - tab.ScrollY) + 1
	marker := panelRowText(t, a, row)
	if !strings.Contains(marker, "▍ [ISSUE] note text") {
		t.Fatalf("marker row = %q, want the compact note marker", strings.TrimRight(marker, " "))
	}
	if len(a.lastMarkerRefs) != 1 {
		t.Fatalf("recorded %d marker refs, want 1", len(a.lastMarkerRefs))
	}

	// Click it. The press is in editor-local coordinates.
	if !a.reviewDiffPress(tab, 4, row-ey) {
		t.Fatal("a click on the marker should be claimed by the review layer")
	}
	if !a.composerOpen || a.composerEdit != 0 {
		t.Fatalf("clicking a marker should reopen note 0 (open=%v edit=%d)",
			a.composerOpen, a.composerEdit)
	}
}

// TestReviewDiffPress_MovesTheCaretInsideTheBox pins that a click in the
// composer's text row repositions the caret instead of moving the diff's
// selection out from under the box.
func TestReviewDiffPress_MovesTheCaretInsideTheBox(t *testing.T) {
	a, tab := reviewApp(t, 2, 2)
	a.openReviewComposer()
	a.composerValue = []rune("abcdefgh")
	a.composerCursor = 8
	a.draw()

	textRow := (2 - tab.ScrollY) + 1 + composerTextLine
	if !a.reviewDiffPress(tab, composerFieldX+3, textRow) {
		t.Fatal("a click in the text row should be claimed")
	}
	if a.composerCursor != 3 {
		t.Errorf("caret = %d, want 3", a.composerCursor)
	}
	if tab.Cursor.Line != 2 {
		t.Errorf("diff selection moved to line %d; the click belonged to the box", tab.Cursor.Line)
	}
}

// TestReviewDiffPress_HunkGapIsNotSelectable pins that the elision between
// hunks cannot be selected. It is not a line of anybody's file, so parking
// a selection on it would be the start of a note about nothing.
func TestReviewDiffPress_HunkGapIsNotSelectable(t *testing.T) {
	a, tab := reviewApp(t, 0, 0)
	if tab.DiffRows[4].Kind != diff.KindGap {
		t.Fatalf("fixture changed: row 4 is %v", tab.DiffRows[4].Kind)
	}
	if !a.reviewDiffPress(tab, 8, 4) {
		t.Fatal("a click on the hunk gap should be claimed and dropped")
	}
	if tab.Cursor.Line != 0 {
		t.Errorf("selection moved to line %d; the gap is not selectable", tab.Cursor.Line)
	}
	// A real line still falls through to the normal cursor placement.
	if a.reviewDiffPress(tab, 8, 2) {
		t.Error("a click on a commentable row should NOT be claimed")
	}
}

// TestComposer_FollowsARefreshedDiff pins that the box stays under the line
// it is about when the agent writes again underneath it. The anchor is
// re-found from the frozen side and line; the note itself is not rebased.
func TestComposer_FollowsARefreshedDiff(t *testing.T) {
	a, tab := reviewApp(t, 2, 2)
	a.openReviewComposer()
	startedAt := a.composerStart

	// Re-parse the same hunk with two extra context lines above it, so
	// every row index shifts down by two.
	shifted := strings.Join([]string{
		"@@ -8,5 +8,5 @@",
		" zero",
		" one",
		" alpha",
		"-bravo one",
		"+bravo two",
		" charlie",
		"",
	}, "\n")
	tab.SetDiffRows(diff.Parse(shifted))
	a.draw()

	if a.composerRow != 4 {
		t.Errorf("composer row = %d, want 4 (the moved +bravo two row)", a.composerRow)
	}
	if a.composerStart != startedAt {
		t.Errorf("composer start = %d, want %d — the note must not be rebased",
			a.composerStart, startedAt)
	}
}

// TestOpenDiffContext_OffersTheReviewActions pins the right-click path, and
// that it mirrors the menu rather than replacing it.
func TestOpenDiffContext_OffersTheReviewActions(t *testing.T) {
	a, tab := reviewApp(t, 2, 2)
	ex, ey, _, _ := a.editorRect()
	if !a.openDiffContext(tab, ex+8, ey+2) {
		t.Fatal("right-click on a diff row should open a context menu")
	}
	if !a.contextOpen || len(a.contextItems) == 0 {
		t.Fatal("context menu should be open with items")
	}
	if a.contextItems[0].label != "Add review note" {
		t.Errorf("first item = %q, want Add review note", a.contextItems[0].label)
	}
	// Activating it must work with no tree node behind the menu.
	a.contextHover = 0
	a.contextActivate()
	if !a.composerOpen {
		t.Error("the context item should open the composer")
	}
}

// -----------------------------------------------------------------------------
// The panel footer
// -----------------------------------------------------------------------------

// TestReviewBlockRows_GrowsWithTheBatch pins the footer's height budget,
// which the Changes list above it borrows its rows from.
func TestReviewBlockRows_GrowsWithTheBatch(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	if got := a.reviewBlockRows(); got != 1 {
		t.Errorf("empty block = %d rows, want 1", got)
	}
	a.reviewBatch.Comments = make([]review.Comment, 2)
	if got := a.reviewBlockRows(); got != 5 {
		t.Errorf("2-note block = %d rows, want 5 (header + 2 + send + copy)", got)
	}
	a.reviewBatch.Comments = make([]review.Comment, maxReviewFooterRows+3)
	want := 1 + maxReviewFooterRows + 1 + 2
	if got := a.reviewBlockRows(); got != want {
		t.Errorf("overflowing block = %d rows, want %d", got, want)
	}
}

// TestGitPanelFooterH_NeverEvictsTheList pins the clamp: a long review in
// a short terminal must still leave the panel header and one list row
// standing, and the block's own rows must stay inside the budget it got.
func TestGitPanelFooterH_NeverEvictsTheList(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.gitPanelShown = true
	a.screen.(tcell.SimulationScreen).SetSize(120, 12)
	a.width, a.height = a.screen.Size()
	a.reflowPanels()
	a.reviewBatch.Comments = make([]review.Comment, 8)
	for i := range a.reviewBatch.Comments {
		a.reviewBatch.Comments[i] = review.Comment{File: "a.go", Start: i + 1, End: i + 1, Text: "note"}
	}

	_, _, _, h := a.gitPanelRect()
	footer := a.gitPanelFooterH()
	if footer > h-gitPanelHeaderRows-1 {
		t.Fatalf("footer wants %d of %d rows, leaving no list", footer, h)
	}
	if a.gitPanelListH() < 1 {
		t.Fatalf("list height = %d, want at least one row", a.gitPanelListH())
	}

	a.draw()
	a.screen.(tcell.SimulationScreen).Show()
	rows := footer - gitPanelBranchRows
	for _, r := range a.lastReviewRows {
		if r.y < h-rows || r.y >= h {
			t.Errorf("recorded row y=%d outside the block's %d rows at the panel bottom (h=%d)", r.y, rows, h)
		}
	}
}

// TestDrawGitPanelReview_ListsNotesAndActions walks the drawn footer and
// then clicks the rows it recorded.
func TestDrawGitPanelReview_ListsNotesAndActions(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.gitPanelShown = true
	a.reviewBatch.Comments = []review.Comment{
		{File: "a.go", Start: 4, End: 4, Text: "first note"},
		{File: "b.go", Start: 9, End: 9, Text: "second note", Stale: true},
	}
	a.draw()
	a.screen.(tcell.SimulationScreen).Show()

	px, _, _, h := a.gitPanelRect()
	var panel []string
	for y := 0; y < h; y++ {
		panel = append(panel, strings.TrimSpace(panelRowText(t, a, y)[px:]))
	}
	joined := strings.Join(panel, "\n")
	for _, want := range []string{"Review (2)", "a.go:4 · first note", "(stale)", "Send to agent", "Copy"} {
		if !strings.Contains(joined, want) {
			t.Errorf("footer is missing %q:\n%s", want, joined)
		}
	}

	// The recorded rects are what clicks are tested against.
	var sendY, commentY int
	for _, r := range a.lastReviewRows {
		switch r.kind {
		case "send":
			sendY = r.y
		case "comment":
			if r.idx == 0 {
				commentY = r.y
			}
		}
	}
	if sendY == 0 || commentY == 0 {
		t.Fatalf("footer recorded no rects: %+v", a.lastReviewRows)
	}
	if !a.reviewPanelClick(sendY) {
		t.Error("the send row should claim its click")
	}
	if !a.reviewPanelClick(commentY) {
		t.Error("a note row should claim its click")
	}
}

// -----------------------------------------------------------------------------
// Handoff
// -----------------------------------------------------------------------------

// TestSendReview_OneAgentSendsAndConsumes pins the happy path: the argv is
// send-text then focus, and the batch clears only after the send succeeded.
func TestSendReview_OneAgentSendsAndConsumes(t *testing.T) {
	calls := fakeHerdr(t, oneAgentJSON, nil)
	a := newTestApp(t, t.TempDir())
	a.reviewBatch.Comments = []review.Comment{{File: "a.go", Start: 1, End: 1, Text: "fix"}}

	a.sendReview()

	if a.reviewBatch.Len() != 0 {
		t.Fatalf("batch has %d notes, want it consumed on success", a.reviewBatch.Len())
	}
	if !strings.Contains(a.statusMsg, "Sent 1 note to the agent") {
		t.Errorf("flash = %q, want the sent confirmation", a.statusMsg)
	}
	if len(*calls) != 3 {
		t.Fatalf("made %d herdr calls, want list + send-text + focus: %v", len(*calls), *calls)
	}
	if got := strings.Join((*calls)[1][:4], " "); got != "herdr pane send-text wE:p2" {
		t.Errorf("send argv = %q", got)
	}
	if !strings.Contains((*calls)[1][4], "Please address these review comments.") {
		t.Errorf("payload does not look like a rendered batch: %q", (*calls)[1][4])
	}
}

// TestSendReview_FailedSendKeepsTheBatch is the other half of
// consume-on-success: a closed pane must not eat the review.
func TestSendReview_FailedSendKeepsTheBatch(t *testing.T) {
	fakeHerdr(t, oneAgentJSON, errors.New("pane closed"))
	a := newTestApp(t, t.TempDir())
	a.reviewBatch.Comments = []review.Comment{{File: "a.go", Start: 1, End: 1, Text: "fix"}}

	a.sendReview()

	if a.reviewBatch.Len() != 1 {
		t.Fatalf("batch has %d notes, want it kept after a failed send", a.reviewBatch.Len())
	}
	if !strings.Contains(a.statusMsg, "review kept") {
		t.Errorf("flash = %q, want it to say the review was kept", a.statusMsg)
	}
	if strings.Contains(a.statusMsg, "pane closed") {
		t.Errorf("herdr's own error text leaked into the status bar: %q", a.statusMsg)
	}
}

// TestSendReview_TwoAgentsOpensThePicker pins that an ambiguous target asks
// rather than guessing, and that picking sends and consumes.
func TestSendReview_TwoAgentsOpensThePicker(t *testing.T) {
	calls := fakeHerdr(t, twoAgentJSON, nil)
	a := newTestApp(t, t.TempDir())
	a.reviewBatch.Comments = []review.Comment{{File: "a.go", Start: 1, End: 1, Text: "fix"}}

	a.sendReview()

	if !a.pickerOpen {
		t.Fatal("two agents should open the picker")
	}
	if len(a.pickerTargets) != 2 {
		t.Fatalf("picker has %d targets, want 2", len(a.pickerTargets))
	}
	if len(*calls) != 1 {
		t.Fatalf("picker should not have sent anything yet: %v", *calls)
	}
	a.draw()
	a.screen.(tcell.SimulationScreen).Show()

	a.pickerHover = 1
	a.handlePickerKey(keyEv(tcell.KeyEnter, 0))
	if a.pickerOpen {
		t.Error("picking should close the picker")
	}
	if got := strings.Join((*calls)[1][:4], " "); got != "herdr pane send-text wE:p3" {
		t.Errorf("sent to the wrong pane: %q", got)
	}
	if a.reviewBatch.Len() != 0 {
		t.Error("a successful pick should consume the batch")
	}
}

// TestSendReview_EmptyBatchSaysSo pins that Esc-Enter with nothing pending
// explains itself instead of silently doing nothing.
func TestSendReview_EmptyBatchSaysSo(t *testing.T) {
	fakeHerdr(t, oneAgentJSON, nil)
	a := newTestApp(t, t.TempDir())
	a.sendReview()
	if !strings.Contains(a.statusMsg, "No review notes") {
		t.Errorf("flash = %q, want the empty-batch message", a.statusMsg)
	}
}

// TestSendReview_NoAgentFallsBackWithoutConsuming pins the fallback: zero
// candidates means the clipboard, and the batch is KEPT — the reviewer has
// the text but has not pasted it anywhere yet.
func TestSendReview_NoAgentFallsBackWithoutConsuming(t *testing.T) {
	fakeHerdr(t, `{"result":{"agents":[]}}`, nil)
	a := newTestApp(t, t.TempDir())
	a.reviewBatch.Comments = []review.Comment{{File: "a.go", Start: 1, End: 1, Text: "fix"}}

	a.sendReview()

	if a.reviewBatch.Len() != 1 {
		t.Fatalf("batch has %d notes, want it kept on the clipboard fallback", a.reviewBatch.Len())
	}
	// The clipboard write itself needs a tty, which a test run has no
	// business assuming. Either outcome is fine; what must be true is
	// that the reason reached the status bar.
	if !strings.Contains(a.statusMsg, "no agent") && !strings.Contains(a.statusMsg, "Copy failed") {
		t.Errorf("flash = %q, want the no-agent reason", a.statusMsg)
	}
}

// -----------------------------------------------------------------------------
// Staleness
// -----------------------------------------------------------------------------

// TestMarkStaleComments_FlagsAndUnflags pins that staleness follows the
// changeset in both directions, and that line numbers are never touched.
func TestMarkStaleComments_FlagsAndUnflags(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.reviewBatch.Comments = []review.Comment{
		{File: "a.go", Start: 12, End: 12, Text: "one"},
		{File: "gone.go", Start: 3, End: 3, Text: "two"},
	}

	a.markStaleComments(gitSnapshot{IsRepo: true, Entries: []gitEntry{{Rel: "a.go"}}})
	if a.reviewBatch.Comments[0].Stale {
		t.Error("a.go is in the changeset; its note should not be stale")
	}
	if !a.reviewBatch.Comments[1].Stale {
		t.Error("gone.go left the changeset; its note should be stale")
	}
	if a.reviewBatch.Comments[1].Start != 3 {
		t.Error("staleness must never rebase a line number")
	}

	a.markStaleComments(gitSnapshot{IsRepo: true, Entries: []gitEntry{{Rel: "a.go"}, {Rel: "gone.go"}}})
	if a.reviewBatch.Comments[1].Stale {
		t.Error("a file back in the changeset should clear the stale flag")
	}
}

// TestMarkStaleComments_IgnoresANonRepoSnapshot pins the degrade path: a
// failed or non-repo status must not flag an entire review stale.
func TestMarkStaleComments_IgnoresANonRepoSnapshot(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.reviewBatch.Comments = []review.Comment{{File: "a.go", Start: 1, End: 1}}
	a.markStaleComments(gitSnapshot{})
	if a.reviewBatch.Comments[0].Stale {
		t.Error("a non-repo snapshot should mark nothing stale")
	}
}
