// =============================================================================
// File: internal/app/branchpicker_test.go
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
)

// branchApp builds an App over a repo with three branches, checked out on
// main. Two of them carry a commit of their own so the list has something
// to sort; which of the two sorts first is left to git, because the two
// commits land in the same second and no test should depend on that.
func branchApp(t *testing.T) (string, *App) {
	t.Helper()
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "a.txt"), "main\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed")

	gitRun(t, dir, "checkout", "-q", "-b", "feature/alpha")
	writeFileT(t, filepath.Join(dir, "a.txt"), "alpha\n")
	gitRun(t, dir, "commit", "-qam", "alpha")

	gitRun(t, dir, "checkout", "-q", "-b", "feature/beta")
	writeFileT(t, filepath.Join(dir, "a.txt"), "beta\n")
	gitRun(t, dir, "commit", "-qam", "beta")

	gitRun(t, dir, "checkout", "-q", "main")

	a := newTestApp(t, dir)
	a.gitPanelShown = true
	a.refreshGitStatus()
	return dir, a
}

// pickerRowNames renders the picker's current rows as a list of names, with
// the current branch marked, so an assertion reads like the screen does.
func pickerRowNames(a *App) []string {
	out := make([]string, 0, len(a.branchPicker.rows))
	for _, r := range a.branchPicker.rows {
		name := r.name
		if r.current {
			name = "*" + name
		}
		out = append(out, name)
	}
	return out
}

// -----------------------------------------------------------------------------
// Opening
// -----------------------------------------------------------------------------

// TestOpenBranchPicker_ListsCurrentBranchFirst pins the ordering rule.
// for-each-ref sorts by commit date, which usually but not always puts the
// checked-out branch first; pinning it means the list always opens with a
// row that says where you are.
func TestOpenBranchPicker_ListsCurrentBranchFirst(t *testing.T) {
	_, a := branchApp(t)

	a.openBranchPicker()

	if !a.branchPicker.open {
		t.Fatalf("picker did not open: %q", a.statusMsg)
	}
	got := pickerRowNames(a)
	if len(got) != 3 {
		t.Fatalf("rows = %v, want three branches", got)
	}
	if got[0] != "*main" {
		t.Errorf("row 0 = %q, want the marked current branch", got[0])
	}
	// The other two follow, in whatever order git gave them. Their exact
	// order is NOT asserted: the fixture commits them within the same
	// second, and --sort=-committerdate ties break on refname, so pinning
	// it here would be pinning how fast the test ran. The sort flag itself
	// is pinned in gitwrite_test.go, where it is a property of the command
	// rather than of the clock.
	rest := got[1] + " " + got[2]
	if !strings.Contains(rest, "feature/alpha") || !strings.Contains(rest, "feature/beta") {
		t.Errorf("rows = %v, want both feature branches after the current one", got)
	}
	if a.branchPicker.selected != 0 {
		t.Errorf("selected = %d, want row 0 highlighted on open", a.branchPicker.selected)
	}
}

// TestOpenBranchPicker_RefusesWhileATabIsDirty is the guard that gates the
// LIST rather than the checkout: a checkout rewrites files under an open
// buffer, and saying so when the user asks for the list is clearer than
// saying it after they have chosen a branch.
func TestOpenBranchPicker_RefusesWhileATabIsDirty(t *testing.T) {
	dir, a := branchApp(t)
	a.openFile(filepath.Join(dir, "a.txt"))
	tab := a.activeTabPtr()
	if tab == nil {
		t.Fatal("no tab opened")
	}
	tab.InsertString("edited\n")

	a.openBranchPicker()

	if a.branchPicker.open {
		t.Error("a dirty tab must refuse to open the picker")
	}
	if !strings.Contains(a.statusMsg, "Save or discard") {
		t.Errorf("flash = %q", a.statusMsg)
	}
}

// TestOpenBranchPicker_RefusesOutsideARepo keeps Esc b harmless where there
// is nothing to switch.
func TestOpenBranchPicker_RefusesOutsideARepo(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.refreshGitStatus()

	a.openBranchPicker()

	if a.branchPicker.open {
		t.Error("a non-repo must not open the picker")
	}
	if !strings.Contains(a.statusMsg, "Not a git repository") {
		t.Errorf("flash = %q", a.statusMsg)
	}
}

// TestOpenBranchPicker_ReportsAListingFailure covers a git that could not
// answer — a corrupt refs directory, or git gone from PATH between the
// snapshot and the keypress. One sentence, no modal over nothing.
func TestOpenBranchPicker_ReportsAListingFailure(t *testing.T) {
	_, a := branchApp(t)
	f := newFakeGit(map[string]fakeGitReply{
		"for-each-ref": {stderr: "fatal: not a git repository", err: errors.New("exit 128")},
	})
	a.gitWriteRunner = f.run

	a.openBranchPicker()

	if a.branchPicker.open {
		t.Error("a failed listing must not open an empty picker")
	}
	if !strings.Contains(a.statusMsg, "Branch list failed") {
		t.Errorf("flash = %q", a.statusMsg)
	}
}

// TestAnyModalOpen_CountsTheBranchPicker is the routing invariant. A modal
// missing from this predicate takes keystrokes while the app below it also
// reacts to them.
func TestAnyModalOpen_CountsTheBranchPicker(t *testing.T) {
	_, a := branchApp(t)
	a.openBranchPicker()
	if !a.anyModalOpen() {
		t.Error("anyModalOpen must count the branch picker")
	}
	a.closeAllModals()
	if a.branchPicker.open {
		t.Error("closeAllModals must close the branch picker")
	}
}

// -----------------------------------------------------------------------------
// Filtering
// -----------------------------------------------------------------------------

// TestBranchPicker_FiltersAsYouType pins the fuzzy filter: "fb" has to find
// feature/beta, the way the root picker's does, and a query matching nothing
// leaves no row highlighted rather than pointing at a branch the user did
// not ask for.
func TestBranchPicker_FiltersAsYouType(t *testing.T) {
	_, a := branchApp(t)
	a.openBranchPicker()

	for _, r := range "fb" {
		a.handleBranchPickerKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	got := pickerRowNames(a)
	if len(got) != 1 || got[0] != "feature/beta" {
		t.Errorf("rows for %q = %v, want feature/beta alone", "fb", got)
	}

	for _, r := range "zzz" {
		a.handleBranchPickerKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	if len(a.branchPicker.rows) != 0 {
		t.Errorf("rows for a nonsense query = %v, want none", pickerRowNames(a))
	}
	if a.branchPicker.selected != rootPickerNoSelection {
		t.Errorf("selected = %d, want nothing highlighted with no matches", a.branchPicker.selected)
	}
	// Enter with nothing highlighted is a no-op, not a guess.
	a.handleBranchPickerKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if !a.branchPicker.open {
		t.Error("Enter with no match must leave the picker up")
	}
}

// TestBranchPicker_EmptyQueryRestoresTheFullList covers backspacing out of
// a filter, which is how anybody who mistypes gets back.
func TestBranchPicker_EmptyQueryRestoresTheFullList(t *testing.T) {
	_, a := branchApp(t)
	a.openBranchPicker()
	a.handleBranchPickerKey(tcell.NewEventKey(tcell.KeyRune, 'z', tcell.ModNone))
	if len(a.branchPicker.rows) != 0 {
		t.Fatalf("fixture: %v", pickerRowNames(a))
	}
	a.handleBranchPickerKey(tcell.NewEventKey(tcell.KeyBackspace2, 0, tcell.ModNone))
	if got := pickerRowNames(a); len(got) != 3 || got[0] != "*main" {
		t.Errorf("rows = %v, want the full current-first list back", got)
	}
}

// -----------------------------------------------------------------------------
// Checking out
// -----------------------------------------------------------------------------

// TestBranchPickerActivate_ChecksOutAndRefreshes is the whole gesture: pick
// a branch, the working tree moves, the panel's footer says so on this
// frame, and the open tabs are handed to the poller to reload.
func TestBranchPickerActivate_ChecksOutAndRefreshes(t *testing.T) {
	dir, a := branchApp(t)
	a.openBranchPicker()
	// Row 1 is feature/beta (current-first ordering, then recency).
	a.branchPicker.selected = 1
	want := a.branchPicker.rows[1].name

	a.branchPickerActivate()

	if a.branchPicker.open {
		t.Error("a successful checkout must close the picker")
	}
	if !strings.Contains(a.statusMsg, "Checked out "+want) {
		t.Errorf("flash = %q, want it to name %q", a.statusMsg, want)
	}
	if got := gitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != want {
		t.Errorf("HEAD is on %q, want %q", got, want)
	}
	if a.gitSnap.Branch != want {
		t.Errorf("panel footer still says %q, want %q — the refresh did not run", a.gitSnap.Branch, want)
	}
	// The tab reload is the poller's job, not a second reload path here.
	if !a.gitPollBusy {
		t.Error("a checkout must kick a git poll so open tabs reload from disk")
	}
}

// TestCheckoutBranch_KeepsThePickerUpOnFailure mirrors pickRoot: git
// refusing because a file would be clobbered is something the user can act
// on, and closing the modal would make them reopen it to do so.
func TestCheckoutBranch_KeepsThePickerUpOnFailure(t *testing.T) {
	_, a := branchApp(t)
	a.openBranchPicker()
	f := newFakeGit(map[string]fakeGitReply{
		"checkout": {
			stderr: "error: Your local changes would be overwritten\nhint: commit them\n",
			err:    errors.New("exit 1"),
		},
	})
	a.gitWriteRunner = f.run

	a.checkoutBranch("feature/beta")

	if !a.branchPicker.open {
		t.Error("a failed checkout must leave the picker open")
	}
	if a.statusMsg != "Checkout failed: Your local changes would be overwritten" {
		t.Errorf("flash = %q", a.statusMsg)
	}
}

// TestBranchRowClick_OpensThePicker is the mouse-first path. Zed makes the
// repo / branch row its switcher, and the house rule says no action lives
// behind only one of keyboard or mouse.
func TestBranchRowClick_OpensThePicker(t *testing.T) {
	_, a := branchApp(t)
	a.draw()

	if a.lastBranchRowY < 0 {
		t.Fatal("the footer did not record a branch row")
	}
	a.gitPanelClick(a.width-2, a.lastBranchRowY)

	if !a.branchPicker.open {
		t.Errorf("clicking the branch row must open the picker (flash %q)", a.statusMsg)
	}
}

// -----------------------------------------------------------------------------
// Drawing and the mouse
// -----------------------------------------------------------------------------

// TestDrawBranchPicker_MarksCurrentAndRecordsRows walks the drawn modal and
// then clicks a row it recorded, which is the only way to know the paint and
// the hit test agree.
func TestDrawBranchPicker_MarksCurrentAndRecordsRows(t *testing.T) {
	_, a := branchApp(t)
	a.openBranchPicker()
	a.draw()
	a.screen.(tcell.SimulationScreen).Show()

	mx, my, mw, mh := a.branchPickerModalRect()
	var lines []string
	for y := my; y < my+mh; y++ {
		lines = append(lines, modalRowText(t, a, y, mx, mw))
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"Switch branch", branchCurrentMark + "main", "feature/beta", "enter checks out"} {
		if !strings.Contains(joined, want) {
			t.Errorf("modal is missing %q:\n%s", want, joined)
		}
	}

	if len(a.branchPicker.rowRects) != 3 {
		t.Fatalf("recorded %d row rects, want 3", len(a.branchPicker.rowRects))
	}
	// A wheel notch moves the window, not the highlight — the wheel is for
	// looking, the click is for choosing.
	before := a.branchPicker.selected
	a.handleBranchPickerMouse(mx+2, my+5, tcell.WheelDown)
	if a.branchPicker.selected != before {
		t.Errorf("the wheel moved the highlight from %d to %d", before, a.branchPicker.selected)
	}
	// A click outside dismisses.
	a.handleBranchPickerMouse(0, a.height-1, tcell.Button1)
	if a.branchPicker.open {
		t.Error("a click outside the modal must close it")
	}
}

// TestBranchPickerMouse_HoverFollowsThePointer pins the mouse-first
// behaviour the root picker and the finder both have: you can scrub the
// list without clicking.
func TestBranchPickerMouse_HoverFollowsThePointer(t *testing.T) {
	_, a := branchApp(t)
	a.openBranchPicker()
	a.draw()

	mx, _, _, _ := a.branchPickerModalRect()
	last := a.branchPicker.rowRects[2]
	a.handleBranchPickerMouse(mx+2, last.y, tcell.ButtonNone)
	if a.branchPicker.selected != last.index {
		t.Errorf("selected = %d after hovering row %d", a.branchPicker.selected, last.index)
	}
}

// modalRowText reads w cells of screen row y starting at column x. Rune-
// indexed rather than byte-sliced off panelRowText: a modal is drawn over
// the file tree, whose indent guides are multibyte, so a byte offset into
// the rendered row is not the column it looks like.
func modalRowText(t *testing.T, a *App, y, x, w int) string {
	t.Helper()
	runes := []rune(panelRowText(t, a, y))
	if x >= len(runes) {
		return ""
	}
	end := x + w
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[x:end])
}

// TestOrderBranchRows_DetachedHeadMarksNothing is the honest rendering of
// "you are not on any of these": gitSnapshot.Branch holds a short SHA when
// detached, which matches no branch name.
func TestOrderBranchRows_DetachedHeadMarksNothing(t *testing.T) {
	rows := orderBranchRows([]string{"main", "other"}, "a1b2c3d")
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.current {
			t.Errorf("%q must not be marked current when HEAD is detached", r.name)
		}
	}
	if rows[0].name != "main" {
		t.Errorf("order changed: %q first, want git's own order preserved", rows[0].name)
	}
}
