// =============================================================================
// File: internal/app/diffview_test.go
// Author: Chase Reynolds
// Created: 2026-08-15
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// These tests drive real `git` against a real repo rather than mocking the
// shell-out. Diff parsing has its own unit tests in internal/diff; what is
// worth verifying here is the part that would break silently against a real
// git — untracked-file handling, the double shell-out order, and the tab
// bookkeeping around a diff.

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chasereyn/vincent/internal/diff"
	"github.com/chasereyn/vincent/internal/editor"
)

// diffTestRepo builds a repo with one committed file, then modifies it and
// adds an untracked one. Returns the repo root and an App rooted at it.
func diffTestRepo(t *testing.T) (string, *App) {
	t.Helper()
	requireGit(t)
	dir := initRepo(t)

	writeFileT(t, filepath.Join(dir, "tracked.txt"), "alpha\nbravo\ncharlie\n")
	gitRun(t, dir, "add", "tracked.txt")
	gitRun(t, dir, "commit", "-q", "-m", "seed")

	writeFileT(t, filepath.Join(dir, "tracked.txt"), "alpha\nBRAVO\ncharlie\n")
	writeFileT(t, filepath.Join(dir, "untracked.txt"), "fresh\n")

	return dir, newTestApp(t, dir)
}

// TestLoadDiffRows_ModifiedTrackedFile is the ordinary case: git diff HEAD
// has something to say, so the untracked fallback never runs.
func TestLoadDiffRows_ModifiedTrackedFile(t *testing.T) {
	dir, _ := diffTestRepo(t)

	rows, ok := loadDiffRows(dir, filepath.Join(dir, "tracked.txt"))
	if !ok {
		t.Fatal("modified tracked file produced no diff")
	}
	added, deleted := diff.Stats(rows)
	if added != 1 || deleted != 1 {
		t.Errorf("stats = +%d −%d, want +1 −1", added, deleted)
	}
	// The context lines around the change must survive, or the reviewer has
	// no idea where in the file they are.
	if got := diff.MaxLineNo(rows); got != 3 {
		t.Errorf("MaxLineNo = %d, want 3 — context lines are missing", got)
	}
}

// TestLoadDiffRows_UntrackedFileFallsBackToNoIndex covers the case a plain
// `git diff HEAD` silently returns nothing for. A brand-new file an agent
// just wrote is precisely the thing a reviewer most wants to see, so an
// empty result there would be the worst possible failure mode.
func TestLoadDiffRows_UntrackedFileFallsBackToNoIndex(t *testing.T) {
	dir, _ := diffTestRepo(t)

	rows, ok := loadDiffRows(dir, filepath.Join(dir, "untracked.txt"))
	if !ok {
		t.Fatal("untracked file produced no diff — the --no-index fallback did not fire")
	}
	added, deleted := diff.Stats(rows)
	if added != 1 || deleted != 0 {
		t.Errorf("stats = +%d −%d, want +1 −0 (a new file is all additions)", added, deleted)
	}
	if rows[0].Kind != diff.KindAdded || rows[0].Text != "fresh" {
		t.Errorf("row 0 = %+v, want the file's only line as an addition", rows[0])
	}
}

// TestLoadDiffRows_CleanFileReportsNothing pins the "no changes" signal. It
// is not an error — the caller flashes it — so it must be distinguishable
// from a diff that happens to be empty.
func TestLoadDiffRows_CleanFileReportsNothing(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "clean.txt"), "unchanged\n")
	gitRun(t, dir, "add", "clean.txt")
	gitRun(t, dir, "commit", "-q", "-m", "seed")

	if _, ok := loadDiffRows(dir, filepath.Join(dir, "clean.txt")); ok {
		t.Error("a committed, unmodified file should report no diff")
	}
}

// TestLoadDiffRows_StagedChangesAreIncluded documents the deliberate choice
// to diff against HEAD rather than the index: what a reviewer wants is the
// whole delta since the last commit, regardless of what an agent happened to
// stage. Splitting the two is phase 3's job.
func TestLoadDiffRows_StagedChangesAreIncluded(t *testing.T) {
	dir, _ := diffTestRepo(t)
	gitRun(t, dir, "add", "tracked.txt")

	rows, ok := loadDiffRows(dir, filepath.Join(dir, "tracked.txt"))
	if !ok {
		t.Fatal("staged change produced no diff")
	}
	if added, deleted := diff.Stats(rows); added != 1 || deleted != 1 {
		t.Errorf("stats = +%d −%d, want the staged change to still show", added, deleted)
	}
}

// TestLoadDiffRows_RepoWithNoCommits covers the fresh-repo case that plain
// `git diff HEAD` fails outright on. Without the empty-tree base every file
// in a just-initialised repo would report "No changes" — a dead end with
// nothing on screen explaining why.
func TestLoadDiffRows_RepoWithNoCommits(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "first.txt"), "hello\n")
	gitRun(t, dir, "add", "first.txt")

	rows, ok := loadDiffRows(dir, filepath.Join(dir, "first.txt"))
	if !ok {
		t.Fatal("staged file in a commitless repo produced no diff")
	}
	if added, deleted := diff.Stats(rows); added != 1 || deleted != 0 {
		t.Errorf("stats = +%d −%d, want +1 −0", added, deleted)
	}
}

// TestOpenDiff_OpensASeparateTabFromTheFile is the tab-bookkeeping contract:
// a file and its diff coexist, each with its own scroll position, and
// opening one never lands on the other.
func TestOpenDiff_OpensASeparateTabFromTheFile(t *testing.T) {
	dir, a := diffTestRepo(t)
	target := filepath.Join(dir, "tracked.txt")

	a.openFile(target)
	a.openDiff(target)

	if len(a.tabs) != 2 {
		t.Fatalf("got %d tabs, want the file and its diff", len(a.tabs))
	}
	if tab := a.activeTabPtr(); tab == nil || !tab.IsDiff() {
		t.Fatal("openDiff should focus the diff tab")
	}

	// Re-opening the file must go back to the file, not to the diff that
	// shares its path.
	a.openFile(target)
	if tab := a.activeTabPtr(); tab == nil || tab.IsDiff() {
		t.Fatal("openFile landed on the diff tab")
	}
	if len(a.tabs) != 2 {
		t.Errorf("got %d tabs after reopening the file, want 2", len(a.tabs))
	}
}

// TestOpenDiff_ReusesAndRefreshesTheTab proves a second openDiff re-runs git
// instead of showing a stale snapshot. In an agent workflow the file has
// usually moved on since the tab was opened.
func TestOpenDiff_ReusesAndRefreshesTheTab(t *testing.T) {
	dir, a := diffTestRepo(t)
	target := filepath.Join(dir, "tracked.txt")

	a.openDiff(target)
	first := a.activeTabPtr()
	if _, deleted := first.DiffStats(); deleted != 1 {
		t.Fatalf("initial diff has %d deletions, want 1", deleted)
	}

	// The agent writes again — two lines changed now instead of one.
	writeFileT(t, target, "ALPHA\nBRAVO\ncharlie\n")
	a.openDiff(target)

	if len(a.tabs) != 1 {
		t.Fatalf("got %d tabs, want the diff tab reused", len(a.tabs))
	}
	if a.activeTabPtr() != first {
		t.Error("openDiff replaced the tab instead of refreshing it")
	}
	if _, deleted := first.DiffStats(); deleted != 2 {
		t.Errorf("refreshed diff has %d deletions, want 2", deleted)
	}
}

// TestOpenDiff_CleanFileFlashesInsteadOfOpening keeps an empty tab from
// appearing for a file with nothing to review.
func TestOpenDiff_CleanFileFlashesInsteadOfOpening(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "clean.txt"), "unchanged\n")
	gitRun(t, dir, "add", "clean.txt")
	gitRun(t, dir, "commit", "-q", "-m", "seed")

	a := newTestApp(t, dir)
	a.openDiff(filepath.Join(dir, "clean.txt"))

	if len(a.tabs) != 0 {
		t.Errorf("got %d tabs, want none for a clean file", len(a.tabs))
	}
	if !strings.Contains(a.statusMsg, "No changes") {
		t.Errorf("statusMsg = %q, want a 'No changes' flash", a.statusMsg)
	}
}

// TestOpenDiff_OpensOnTheFirstChange checks the framing: a diff should not
// open on the leading context git includes for orientation.
func TestOpenDiff_OpensOnTheFirstChange(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	target := filepath.Join(dir, "long.txt")

	var b strings.Builder
	for i := 0; i < 80; i++ {
		b.WriteString("line\n")
	}
	writeFileT(t, target, b.String())
	gitRun(t, dir, "add", "long.txt")
	gitRun(t, dir, "commit", "-q", "-m", "seed")
	writeFileT(t, target, b.String()+"appended\n")

	a := newTestApp(t, dir)
	a.openDiff(target)

	tab := a.activeTabPtr()
	if tab == nil || !tab.IsDiff() {
		t.Fatal("expected a diff tab")
	}
	if tab.DiffRows[tab.Cursor.Line].Kind != diff.KindAdded {
		t.Errorf("opened on row %d (%v), want the first addition",
			tab.Cursor.Line, tab.DiffRows[tab.Cursor.Line].Kind)
	}
}

// TestOpenDiffAtLine_LandsOnTheClickedLine covers the git-gutter click: a
// change bar next to line N in the file must land on that change in the
// diff, not at the top of it.
func TestOpenDiffAtLine_LandsOnTheClickedLine(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	target := filepath.Join(dir, "long.txt")

	var before strings.Builder
	for i := 0; i < 60; i++ {
		before.WriteString("line\n")
	}
	writeFileT(t, target, before.String())
	gitRun(t, dir, "add", "long.txt")
	gitRun(t, dir, "commit", "-q", "-m", "seed")

	// Change line 50 (one-based), leaving everything else alone.
	lines := strings.Split(before.String(), "\n")
	lines[49] = "CHANGED"
	writeFileT(t, target, strings.Join(lines, "\n"))

	a := newTestApp(t, dir)
	a.openDiffAtLine(target, 49) // zero-based line 49 == file line 50.

	tab := a.activeTabPtr()
	if tab == nil || !tab.IsDiff() {
		t.Fatal("expected a diff tab")
	}
	row := tab.DiffRows[tab.Cursor.Line]
	if row.New != 50 {
		t.Errorf("landed on a row for new line %d, want 50", row.New)
	}
}

// TestReconcileDiffTab_TracksTheFile is the live-update path. It is what
// makes Vincent usable next to a running agent: the diff you are reading
// updates in place as the agent writes, without losing your place.
func TestReconcileDiffTab_TracksTheFile(t *testing.T) {
	dir, a := diffTestRepo(t)
	target := filepath.Join(dir, "tracked.txt")

	a.openDiff(target)
	tab := a.activeTabPtr()
	tab.ScrollY = 1

	// Nothing changed on disk — reconcile must be a no-op even though the
	// poller handed it fresh rows, or a tab you had scrolled would be
	// rebuilt under you every ten seconds for nothing.
	rowsBefore := len(tab.DiffRows)
	a.reconcileDiffTab(tab, pollDiffTab(a, tab))
	if len(tab.DiffRows) != rowsBefore {
		t.Errorf("reconcile changed a diff whose file was untouched")
	}

	// Now the agent writes. Roll the mtime back so the change is visible
	// regardless of the filesystem's timestamp granularity — on Windows
	// two writes inside the same tick can share an mtime.
	writeFileT(t, target, "ALPHA\nBRAVO\nCHARLIE\n")
	tab.Mtime = tab.Mtime.Add(-time.Hour)
	a.reconcileDiffTab(tab, pollDiffTab(a, tab))

	if _, deleted := tab.DiffStats(); deleted != 3 {
		t.Errorf("refreshed diff has %d deletions, want 3", deleted)
	}
	if tab.ScrollY != 1 {
		t.Errorf("ScrollY = %d after refresh, want it preserved at 1", tab.ScrollY)
	}
}

// TestReconcileDiffTab_SurvivesADeletedFile checks the case the plain-file
// reconcile treats as a warning. For a diff, a file disappearing is just
// another diff — an all-deletions one — and must not mark the tab dirty or
// flash "deleted on disk" at a tab that has nothing to save.
func TestReconcileDiffTab_SurvivesADeletedFile(t *testing.T) {
	dir, a := diffTestRepo(t)
	target := filepath.Join(dir, "tracked.txt")

	a.openDiff(target)
	tab := a.activeTabPtr()

	if err := os.Remove(target); err != nil {
		t.Fatalf("remove: %v", err)
	}
	a.reconcileDiffTab(tab, pollDiffTab(a, tab))

	if tab.Dirty {
		t.Error("a diff tab must never be marked dirty — there is nothing to save")
	}
	if _, deleted := tab.DiffStats(); deleted != 3 {
		t.Errorf("deleted file shows %d deletions, want all 3 lines", deleted)
	}
}

// TestHasDiffableTab is the menu predicate. It stays true for any tab with a
// path, because deciding otherwise would mean running git on every menu
// repaint just to grey out a row.
func TestHasDiffableTab(t *testing.T) {
	dir, a := diffTestRepo(t)

	if a.hasDiffableTab() {
		t.Error("no tabs open, but View diff reported applicable")
	}
	a.openFile(filepath.Join(dir, "tracked.txt"))
	if !a.hasDiffableTab() {
		t.Error("an open file should be diffable")
	}
}

// pollDiffTab performs the reads the background poller would have performed
// for one diff tab, synchronously. The reconcile path takes its stat and its
// diff rows as data now (see gitpoll.go), so a test has to supply them —
// doing it through the real pollFile keeps these tests exercising the actual
// git reads rather than a hand-built fixture.
func pollDiffTab(a *App, tab *editor.Tab) gitPollFile {
	return pollFile(a.rootDir, gitPollTarget{path: tab.Path, diff: true})
}
