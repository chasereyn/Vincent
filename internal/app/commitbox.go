// =============================================================================
// File: internal/app/commitbox.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// The box's shape is review.go's inline composer (composerLines): a
// bordered box with the kind on the top border and the keys on the bottom
// one. Reused rather than reinvented so the two text fields in Vincent
// look like the same control.
// =============================================================================

// commitbox.go is the Esc c commit message box: a single-line field in the
// Changes panel footer, and the `git add -A && git commit -m` behind it.
//
// It sits exactly where Zed puts its commit box — bottom of the git panel,
// under the repo / branch row — and it STACKS ABOVE the review block
// rather than replacing it. That is deliberate. Zed's footer ends in
// "describe this change and commit it"; Vincent's ends in "describe this
// change and hand it back"; and a reviewer who has just written six notes
// and then decides to commit a typo fix of their own should not have to
// choose which of the two the panel is currently for.
//
// Three refusals, all before the box opens, because each one means the
// commit would either do nothing or lose something:
//
//   - Not a repo, or nothing changed. `git commit` with an empty index
//     fails with a five-line message; refusing up front is one sentence.
//   - A dirty text tab. `git add -A` would stage the version on DISK,
//     which is not the version the reviewer is looking at, and the commit
//     would silently exclude their unsaved edit. Save or discard first.
//
// The fourth refusal — an empty message — happens on Enter and leaves the
// box open, because the fix is to type something rather than to start over.
//
// A FAILED COMMIT KEEPS THE MESSAGE. Same consume-on-success rule the
// review batch has (see review.go): the reviewer wrote those words, and a
// locked index is not a reason to make them write them again.

package app

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/review"
)

const (
	// commitBoxHeight is how many rows the box occupies: top border, the
	// text field, bottom border. Same three-row shape as the review
	// composer, for the same reason — a modeless box with no buttons has
	// to name its keys somewhere.
	commitBoxHeight = 3

	// commitBoxFieldX is the column, within the box, where editable text
	// starts — just past the "│ " left border. Mirrors composerFieldX.
	commitBoxFieldX = 2

	// commitFlashMessageMax is how much of the message the success flash
	// quotes back. Enough to recognise which commit it was, short enough
	// to leave the SHA visible in a one-row status bar.
	commitFlashMessageMax = 40
)

// commitBoxHit is the draw-time snapshot of where the box landed, so a
// click is tested against real geometry rather than against footer
// arithmetic recomputed in the click handler. Same record-during-draw
// discipline as gitPanelRowRect and reviewRowRect — and for the same
// reason: two copies of the layout drift the first time the footer grows a
// row.
//
// drawn is false whenever the box is closed or the footer was clamped too
// short to paint it, which is the one state a click handler must not
// guess at.
type commitBoxHit struct {
	drawn       bool
	top, bottom int // inclusive screen rows the box occupies
	textY       int // the editable row
	fieldX      int // first column of the text field
	fieldW      int
}

// -----------------------------------------------------------------------------
// Open / close
// -----------------------------------------------------------------------------

// openCommitBox is the Esc c leader action: arm the commit message box.
//
// It opens the Changes panel if it was closed, because the box lives in
// that panel's footer and a keypress that puts focus somewhere invisible
// is a broken keybinding. Opening the panel also refreshes the snapshot,
// which is what makes the "nothing to commit" check below answer about the
// repo as it is now rather than as the last tick saw it.
//
// Pressing Esc c while the box is already open is a no-op beyond
// re-checking the guards: the message survives, which is what you want
// after a failed commit.
func (a *App) openCommitBox() {
	if a.tree == nil {
		a.flash("Commit isn't available in single-file mode")
		return
	}
	if !a.gitSnap.IsRepo {
		a.flash("Not a git repository")
		return
	}
	if !a.gitPanelShown {
		a.gitPanelShown = true
		a.gitPanelHover = -1
		a.reflowPanels()
		a.refreshGitStatus()
	}
	if len(a.gitSnap.Entries) == 0 {
		a.flash("Nothing to commit — no changes")
		return
	}
	if n := a.dirtyTabCount(); n > 0 {
		a.flash("Save or discard " + dirtyTabCount(n) + " first")
		return
	}
	a.commitOpen = true
	a.commitCursor = len(a.commitValue)
}

// closeCommitBox drops focus without touching the message. Esc means
// "not now", not "forget what I typed" — reopening with Esc c brings the
// words back, which matters most in the case the box is most likely to be
// dismissed from: a commit that just failed.
func (a *App) closeCommitBox() {
	a.commitOpen = false
}

// clearCommitBox closes the box and empties it. Only the success path
// calls this: once the commit landed, the message describes a commit that
// exists and re-offering it would invite committing it twice.
func (a *App) clearCommitBox() {
	a.commitOpen = false
	a.commitValue = nil
	a.commitCursor = 0
	a.commitScroll = 0
}

// dirtyTabCount renders a dirty-tab count with the right plural. "1 dirty
// tabs" is the kind of small wrongness that makes a tool feel unfinished —
// see noteCount in review.go, which exists for the same reason.
func dirtyTabCount(n int) string {
	if n == 1 {
		return "1 dirty tab"
	}
	return fmt.Sprintf("%d dirty tabs", n)
}

// -----------------------------------------------------------------------------
// Commit
// -----------------------------------------------------------------------------

// submitCommit is Enter in the box: stage everything and commit it.
//
// The dirty-tab guard is re-checked here as well as at open, because the
// box can sit armed while the reviewer types into a file — and `git add -A`
// staging the on-disk version of a buffer they have edited is the one
// outcome that would lose work rather than merely fail.
//
// On success the panel is refreshed twice over: refreshGitStatus so the
// Changes list empties on THIS frame rather than up to ten seconds later,
// and startGitPoll so the open tabs' gutter markers and diff tabs catch up
// on the next event. The immediate one is the one the user sees; the poll
// is the one that fixes the diffs.
func (a *App) submitCommit() {
	if !a.commitOpen {
		return
	}
	msg := strings.TrimSpace(string(a.commitValue))
	if msg == "" {
		a.flash("Type a commit message first")
		return
	}
	if n := a.dirtyTabCount(); n > 0 {
		a.flash("Save or discard " + dirtyTabCount(n) + " first")
		return
	}

	run := a.gitWriter()
	if _, stderr, err := gitCommitAll(run, a.rootDir, msg); err != nil {
		review.Logf("git commit: %v\n%s", err, stderr)
		a.flash(gitFailureSentence("Commit", stderr, err))
		return
	}
	// Best-effort: a commit that landed but whose SHA we could not read
	// back is still a commit, and saying so without the SHA beats
	// reporting a failure that did not happen.
	sha, _, err := gitHeadShort(run, a.rootDir)
	if err != nil || sha == "" {
		sha = "HEAD"
	}
	a.clearCommitBox()
	a.flash(fmt.Sprintf("Committed %s: %s", sha, trimRunes(msg, commitFlashMessageMax)))
	a.refreshGitStatus()
	a.startGitPoll()
}

// -----------------------------------------------------------------------------
// Keyboard
// -----------------------------------------------------------------------------

// handleCommitKey owns the keyboard while the box is armed.
//
// Routed in handleKey ahead of the Esc-leader table AND ahead of the
// review composer: while a text field has focus Esc has one job, and Esc c
// pressed over a half-written note has to be able to take the keyboard and
// hand it back. The note's own state survives untouched either way.
//
// Everything that is not Esc or Enter goes to editRunes — the same
// single-line field the prompt modal and the composer use, so caret
// movement and backspace behave identically in all three.
func (a *App) handleCommitKey(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyEsc:
		a.closeCommitBox()
		return
	case tcell.KeyEnter:
		a.submitCommit()
		return
	}
	a.commitValue, a.commitCursor, _ = editRunes(a.commitValue, a.commitCursor, ev)
}

// -----------------------------------------------------------------------------
// Mouse
// -----------------------------------------------------------------------------

// commitBoxClick places the caret when the click landed in the box's text
// field, and reports whether the box owned the click at all.
//
// Every row of the box is claimed, borders included: falling through would
// let a click on the border reach the Changes row that happens to share
// that y, which reads as the panel doing something random.
func (a *App) commitBoxClick(x, y int) bool {
	box := a.lastCommitBox
	if !box.drawn || y < box.top || y > box.bottom {
		return false
	}
	if y != box.textY {
		return true
	}
	target := a.commitScroll + x - box.fieldX
	if target < 0 {
		target = 0
	}
	if target > len(a.commitValue) {
		target = len(a.commitValue)
	}
	a.commitCursor = target
	return true
}

// -----------------------------------------------------------------------------
// Draw
// -----------------------------------------------------------------------------

// commitBoxRows is how many footer rows the box wants: three when armed,
// none otherwise. gitPanelFooterH adds it to the branch row and the review
// block, so the box growing into the footer shortens the Changes list by
// exactly its own height and nothing overlaps.
func (a *App) commitBoxRows() int {
	if !a.commitOpen {
		return 0
	}
	return commitBoxHeight
}

// drawCommitBox paints the box at (x, y) within a budget of rows, records
// its hit-test rect, and returns how many rows it actually used.
//
// rows can be smaller than commitBoxHeight in a short terminal, in which
// case the box draws what fits, top-down: the border and the field are
// worth more than the key hints, and a field the user cannot see is worse
// than a hint they cannot read. Returning the count is what lets the review
// block below start at the right row without duplicating the arithmetic.
func (a *App) drawCommitBox(x, y, w, rows int) int {
	a.lastCommitBox = commitBoxHit{}
	if !a.commitOpen || rows <= 0 || w <= 0 {
		return 0
	}
	th := a.theme
	border := tcell.StyleDefault.Background(th.SidebarBG).Foreground(th.ReviewBorder)
	textStyle := tcell.StyleDefault.Background(th.SidebarBG).Foreground(th.Text)

	// The box spans the panel's text column: one cell of left pad, like
	// every other panel row, out to the right edge.
	boxW := w - 1
	if boxW < 12 {
		boxW = 12
	}
	fieldW := boxW - 4
	if fieldW < 1 {
		fieldW = 1
	}
	a.commitScroll = scrollWindow(a.commitCursor, a.commitScroll, fieldW)

	lines := []struct {
		text  string
		style tcell.Style
	}{
		{fitBorder("┌─ Commit all ", boxW, "┐"), border},
		{"", textStyle}, // filled in below; the field needs the window
		{fitBorder("└─ Enter commit · Esc cancel ", boxW, "┘"), border},
	}
	visible := runeWindow(a.commitValue, a.commitScroll, fieldW)
	lines[1].text = "│ " + visible + strings.Repeat(" ", fieldW-len([]rune(visible))) + " │"

	used := 0
	for i, line := range lines {
		if used >= rows {
			break
		}
		cy := y + i
		drawClipped(a.screen, x+1, cy, w-1, line.text, line.style)
		used++
	}

	a.lastCommitBox = commitBoxHit{
		drawn:  true,
		top:    y,
		bottom: y + used - 1,
		textY:  y + 1,
		fieldX: x + 1 + commitBoxFieldX,
		fieldW: fieldW,
	}
	// The caret is recorded rather than shown, because the editor pane
	// paints after the panel and its own ShowCursor/HideCursor would
	// overwrite ours. draw() replays it at the end of the frame — see
	// showCommitCaret.
	a.commitCaretX = a.lastCommitBox.fieldX + a.commitCursor - a.commitScroll
	a.commitCaretY = a.lastCommitBox.textY
	return used
}

// showCommitCaret puts the terminal cursor in the box's text field. Called
// from draw() late in the frame, after the editor pane has had its say:
// tab.Render calls ShowCursor or HideCursor for the buffer's own caret, and
// whoever calls last wins.
//
// Nothing happens when the field's caret scrolled out of view or the box
// was clamped away, which leaves the editor's own cursor showing — the
// honest answer, since in that state there is no visible field to point at.
func (a *App) showCommitCaret() {
	box := a.lastCommitBox
	if !a.commitOpen || !box.drawn || box.textY > box.bottom {
		return
	}
	if a.commitCaretX < box.fieldX || a.commitCaretX >= box.fieldX+box.fieldW {
		return
	}
	a.screen.ShowCursor(a.commitCaretX, a.commitCaretY)
}
