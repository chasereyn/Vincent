// =============================================================================
// File: internal/app/commitbox_test.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/review"
)

// commitApp builds an App over a repo with one modified tracked file — the
// smallest fixture where a commit is a legal thing to ask for.
func commitApp(t *testing.T) (string, *App) {
	t.Helper()
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "a.txt"), "one\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed")
	writeFileT(t, filepath.Join(dir, "a.txt"), "changed\n")

	a := newTestApp(t, dir)
	a.gitPanelShown = true
	a.refreshGitStatus()
	return dir, a
}

// typeInto feeds a string into the commit box one keystroke at a time, the
// way the event loop does. Typing rather than assigning commitValue is what
// makes the test cover handleCommitKey and the caret.
func typeInto(a *App, text string) {
	for _, r := range text {
		a.handleCommitKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
}

// -----------------------------------------------------------------------------
// Opening — and the three refusals
// -----------------------------------------------------------------------------

// TestOpenCommitBox_OpensTheChangesPanel pins the "focus must be visible"
// rule: the box lives in that panel's footer, so Esc c with the panel
// closed has to bring the panel back or the keystroke arms an invisible
// field.
func TestOpenCommitBox_OpensTheChangesPanel(t *testing.T) {
	_, a := commitApp(t)
	a.gitPanelShown = false

	a.openCommitBox()

	if !a.gitPanelShown {
		t.Error("Esc c must open the Changes panel it draws into")
	}
	if !a.commitOpen {
		t.Fatalf("box did not arm: %q", a.statusMsg)
	}
}

// TestOpenCommitBox_RefusesWithNothingToCommit keeps the failure a sentence
// rather than git's five-line "nothing to commit" essay.
func TestOpenCommitBox_RefusesWithNothingToCommit(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "a.txt"), "one\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed")

	a := newTestApp(t, dir)
	a.gitPanelShown = true
	a.refreshGitStatus()

	a.openCommitBox()

	if a.commitOpen {
		t.Error("a clean repo must not arm the commit box")
	}
	if !strings.Contains(a.statusMsg, "Nothing to commit") {
		t.Errorf("flash = %q", a.statusMsg)
	}
}

// TestOpenCommitBox_RefusesWhileATabIsDirty is the guard that exists to
// prevent losing work, not merely to prevent an error: `git add -A` would
// stage the version on DISK, silently committing something other than what
// the reviewer is looking at.
func TestOpenCommitBox_RefusesWhileATabIsDirty(t *testing.T) {
	dir, a := commitApp(t)
	a.openFile(filepath.Join(dir, "a.txt"))
	tab := a.activeTabPtr()
	if tab == nil {
		t.Fatal("no tab opened")
	}
	tab.InsertString("edited\n")
	if !tab.Dirty {
		t.Fatal("fixture failed to make the tab dirty")
	}

	a.openCommitBox()

	if a.commitOpen {
		t.Error("a dirty tab must refuse the commit box")
	}
	if !strings.Contains(a.statusMsg, "Save or discard") || !strings.Contains(a.statusMsg, "1 dirty tab") {
		t.Errorf("flash = %q, want the save-or-discard refusal naming one dirty tab", a.statusMsg)
	}
}

// TestOpenCommitBox_RefusesOutsideARepo covers the leader key's harmless
// case, which for a write is the one that matters most.
func TestOpenCommitBox_RefusesOutsideARepo(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.refreshGitStatus()

	a.openCommitBox()

	if a.commitOpen {
		t.Error("a non-repo must not arm the commit box")
	}
	if !strings.Contains(a.statusMsg, "Not a git repository") {
		t.Errorf("flash = %q", a.statusMsg)
	}
}

// -----------------------------------------------------------------------------
// Committing
// -----------------------------------------------------------------------------

// TestSubmitCommit_RefusesAnEmptyMessageAndKeepsTheBox pins the fourth
// refusal. The fix for "you typed nothing" is to type something, so the box
// stays where the caret already is.
func TestSubmitCommit_RefusesAnEmptyMessageAndKeepsTheBox(t *testing.T) {
	_, a := commitApp(t)
	a.openCommitBox()
	typeInto(a, "   ")

	a.submitCommit()

	if !a.commitOpen {
		t.Error("an empty message must leave the box open")
	}
	if !strings.Contains(a.statusMsg, "commit message") {
		t.Errorf("flash = %q, want it to ask for a message", a.statusMsg)
	}
}

// TestSubmitCommit_CommitsAndClearsTheBox is the happy path end to end,
// against a real git: the commit lands, the flash names its short SHA and
// the message, and the box empties so the same words cannot be committed
// twice.
func TestSubmitCommit_CommitsAndClearsTheBox(t *testing.T) {
	dir, a := commitApp(t)
	a.openCommitBox()
	typeInto(a, "tidy the thing")

	a.submitCommit()

	if a.commitOpen || len(a.commitValue) != 0 {
		t.Errorf("box should be closed and empty, got open=%v value=%q", a.commitOpen, string(a.commitValue))
	}
	if subject := gitOut(t, dir, "log", "-1", "--format=%s"); subject != "tidy the thing" {
		t.Errorf("HEAD subject = %q", subject)
	}
	sha := gitOut(t, dir, "rev-parse", "--short", "HEAD")
	if !strings.Contains(a.statusMsg, sha) || !strings.Contains(a.statusMsg, "tidy the thing") {
		t.Errorf("flash = %q, want it to name %q and the message", a.statusMsg, sha)
	}
	// The Changes list must be right on this frame, not ten seconds later.
	if len(a.gitSnap.Entries) != 0 {
		t.Errorf("panel still lists %d changes after the commit", len(a.gitSnap.Entries))
	}
}

// TestSubmitCommit_IndexLockKeepsTheMessage is the failure Vincent expects
// most: the agent being reviewed is mid-`git add`. The sentence is the
// explanatory one, there is no retry, and the words the reviewer typed are
// still there to send again.
func TestSubmitCommit_IndexLockKeepsTheMessage(t *testing.T) {
	_, a := commitApp(t)
	f := newFakeGit(map[string]fakeGitReply{
		"add": {
			stderr: "fatal: Unable to create '/repo/.git/index.lock': File exists.",
			err:    errors.New("exit 128"),
		},
	})
	a.gitWriteRunner = f.run
	a.openCommitBox()
	typeInto(a, "keep me")

	a.submitCommit()

	if a.statusMsg != indexLockSentence {
		t.Errorf("flash = %q, want %q", a.statusMsg, indexLockSentence)
	}
	if string(a.commitValue) != "keep me" {
		t.Errorf("message = %q, want it kept for a retry", string(a.commitValue))
	}
	if !a.commitOpen {
		t.Error("a failed commit must leave the box open")
	}
	// No retry loop: exactly one `add`, and no `commit` after it failed.
	if got := f.argv(); len(got) != 1 || got[0] != "add -A" {
		t.Errorf("argv = %v, want a single add and no retry", got)
	}
}

// TestSubmitCommit_ReportsAPlainSentenceOnFailure covers the generic case —
// a rejected commit hook, say. One sentence on screen; the envelope goes to
// the log.
func TestSubmitCommit_ReportsAPlainSentenceOnFailure(t *testing.T) {
	_, a := commitApp(t)
	f := newFakeGit(map[string]fakeGitReply{
		"commit": {
			stderr: "hint: some hint\nerror: pre-commit hook refused\n",
			err:    errors.New("exit 1"),
		},
	})
	a.gitWriteRunner = f.run
	a.openCommitBox()
	typeInto(a, "nope")

	a.submitCommit()

	if a.statusMsg != "Commit failed: pre-commit hook refused" {
		t.Errorf("flash = %q", a.statusMsg)
	}
}

// TestDirtyTabCount_Pluralises guards the small wrongness rule. "1 dirty
// tabs" is exactly the kind of thing that makes a tool feel unfinished.
func TestDirtyTabCount_Pluralises(t *testing.T) {
	if got := dirtyTabCount(1); got != "1 dirty tab" {
		t.Errorf("one = %q", got)
	}
	if got := dirtyTabCount(3); got != "3 dirty tabs" {
		t.Errorf("three = %q", got)
	}
}

// -----------------------------------------------------------------------------
// Keyboard
// -----------------------------------------------------------------------------

// TestHandleCommitKey_EscClosesButKeepsTheText pins the difference between
// Esc and a successful commit. Esc means "not now"; the words survive so
// Esc c brings them back.
func TestHandleCommitKey_EscClosesButKeepsTheText(t *testing.T) {
	_, a := commitApp(t)
	a.openCommitBox()
	typeInto(a, "half written")

	a.handleCommitKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))

	if a.commitOpen {
		t.Error("Esc must close the box")
	}
	if string(a.commitValue) != "half written" {
		t.Errorf("value = %q, want it preserved across the close", string(a.commitValue))
	}
	a.openCommitBox()
	if a.commitCursor != len("half written") {
		t.Errorf("cursor = %d, want the end of the preserved text", a.commitCursor)
	}
}

// TestHandleKey_CommitBoxOwnsTheKeyboard proves the routing: while the box
// is armed, 'q' is a character rather than the quit leader, and Esc belongs
// to the box.
func TestHandleKey_CommitBoxOwnsTheKeyboard(t *testing.T) {
	_, a := commitApp(t)
	a.openCommitBox()

	a.handleKey(tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone))
	if a.quit {
		t.Fatal("a keystroke in the commit box must not reach the leader table")
	}
	if string(a.commitValue) != "q" {
		t.Errorf("value = %q, want the typed character", string(a.commitValue))
	}
}

// -----------------------------------------------------------------------------
// Layout and drawing
// -----------------------------------------------------------------------------

// TestGitPanelFooter_CommitBoxStacksAboveTheReviewBlock is the layout claim
// phase 3b makes: both surfaces are live at once, the box on top, and
// neither paints over the other. This is the test that would catch the
// footer's two halves disagreeing about where the boundary is.
func TestGitPanelFooter_CommitBoxStacksAboveTheReviewBlock(t *testing.T) {
	_, a := commitApp(t)
	a.reviewBatch.Comments = []review.Comment{
		{File: "a.txt", Start: 1, End: 1, Text: "a note"},
	}
	a.openCommitBox()
	typeInto(a, "wip")
	a.draw()
	a.screen.(tcell.SimulationScreen).Show()

	px, _, _, h := a.gitPanelRect()
	rows := make([]string, h)
	for y := 0; y < h; y++ {
		rows[y] = strings.TrimSpace(panelRowText(t, a, y)[px:])
	}

	rowOf := func(want string) int {
		for y, r := range rows {
			if strings.Contains(r, want) {
				return y
			}
		}
		t.Fatalf("panel is missing %q:\n%s", want, strings.Join(rows, "\n"))
		return -1
	}
	branchY := rowOf("⑂ ")
	topY := rowOf("Commit all")
	textY := rowOf("wip")
	footY := rowOf("Enter commit")
	reviewY := rowOf("Review (1)")

	if !(branchY < topY && topY < textY && textY < footY && footY < reviewY) {
		t.Errorf("footer order is branch=%d box=%d/%d/%d review=%d, want them in that order:\n%s",
			branchY, topY, textY, footY, reviewY, strings.Join(rows, "\n"))
	}
	if topY+1 != textY || textY+1 != footY {
		t.Errorf("the box is not three contiguous rows: %d, %d, %d", topY, textY, footY)
	}
	if got := a.lastCommitBox; !got.drawn || got.top != topY || got.bottom != footY || got.textY != textY {
		t.Errorf("recorded box = %+v, want it to match the drawn rows %d..%d", got, topY, footY)
	}
	// The footer grows by exactly the box's height and the Changes list
	// gives up exactly that much — which is what keeps the box from
	// evicting more of the list than it occupies.
	withBox, listWithBox := a.gitPanelFooterH(), a.gitPanelListH()
	a.closeCommitBox()
	withoutBox, listWithoutBox := a.gitPanelFooterH(), a.gitPanelListH()
	if withBox-withoutBox != commitBoxHeight {
		t.Errorf("footer height with box = %d, without = %d, want a %d-row difference",
			withBox, withoutBox, commitBoxHeight)
	}
	if listWithoutBox-listWithBox != commitBoxHeight {
		t.Errorf("Changes list lost %d rows to the box, want %d",
			listWithoutBox-listWithBox, commitBoxHeight)
	}
}

// TestCommitBoxClick_PlacesTheCaretAndAbsorbsTheBorders keeps the mouse
// honest. Vincent is mouse-first, so clicking into the message has to work;
// and a click on the border must not fall through to whatever Changes row
// shares that y.
func TestCommitBoxClick_PlacesTheCaretAndAbsorbsTheBorders(t *testing.T) {
	_, a := commitApp(t)
	a.openCommitBox()
	typeInto(a, "abcdef")
	a.draw()

	box := a.lastCommitBox
	if !box.drawn {
		t.Fatal("the box did not draw")
	}
	if !a.commitBoxClick(box.fieldX+2, box.textY) {
		t.Fatal("a click in the text row must be claimed")
	}
	if a.commitCursor != 2 {
		t.Errorf("cursor = %d, want 2 — the column that was clicked", a.commitCursor)
	}
	if !a.commitBoxClick(box.fieldX, box.top) {
		t.Error("a click on the top border must be absorbed, not passed on")
	}
	if a.commitBoxClick(box.fieldX, box.top-1) {
		t.Error("a click above the box must not be claimed")
	}
}

// TestMenuToggleGitPanel_DropsTheCommitBoxFocus is the invisible-focus
// trap: the box draws inside the panel, so hiding the panel with the box
// armed would swallow every keystroke with no caret to explain why. The
// text still survives, because Esc g is not "discard what I typed".
func TestMenuToggleGitPanel_DropsTheCommitBoxFocus(t *testing.T) {
	_, a := commitApp(t)
	a.openCommitBox()
	typeInto(a, "still here")

	a.menuToggleGitPanel()

	if a.gitPanelShown {
		t.Fatal("fixture: the panel should have closed")
	}
	if a.commitOpen {
		t.Error("hiding the panel must drop the commit box's focus")
	}
	if a.lastCommitBox.drawn {
		t.Error("the stale hit rect must go with it")
	}
	if string(a.commitValue) != "still here" {
		t.Errorf("value = %q, want the message kept", string(a.commitValue))
	}
}

// TestCommitBox_ClosedBoxTakesNoRows is the "off" state of the layout: no
// rows, nothing recorded, and therefore no click target.
func TestCommitBox_ClosedBoxTakesNoRows(t *testing.T) {
	_, a := commitApp(t)
	a.draw()

	if a.commitBoxRows() != 0 {
		t.Errorf("closed box wants %d rows", a.commitBoxRows())
	}
	if a.lastCommitBox.drawn {
		t.Error("a closed box must not record a hit rect")
	}
	if a.commitBoxClick(0, 0) {
		t.Error("a closed box must claim no clicks")
	}
}
