// =============================================================================
// File: internal/app/diffview.go
// Author: Chase Reynolds
// Created: 2026-08-15
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// diffview.go is the app-side half of Vincent's inline diff viewer: shelling
// out to git for the diff text, opening it as a tab, and keeping it fresh
// while an agent keeps writing to the file underneath.
//
// Two shell-outs, in order. `git diff HEAD` covers everything git already
// knows about — staged, unstaged, and both at once — which is the right
// default for review, because what a reviewer wants to see is the whole
// delta from the last commit regardless of what happens to be staged.
// Untracked files produce nothing from that, so they fall through to
// `git diff --no-index` against the null device, which renders a new file as
// one long addition. Splitting staged from unstaged is phase 3's job, once
// there's a git panel to hang the distinction off.
//
// Everything here degrades the way gitstatus.go does: any git failure means
// no diff and a flash message, never a crash and never a retry loop.

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chasereyn/vincent/internal/diff"
	"github.com/chasereyn/vincent/internal/editor"
	"github.com/chasereyn/vincent/internal/filetree"
)

// loadDiffRows returns the parsed diff for path, and whether git had
// anything to say about it at all.
//
// ok=false means "no changes" — an unmodified tracked file, a path outside
// the repo, or git not being available. The caller flashes that; it is not
// an error state.
func loadDiffRows(rootDir, path string) ([]diff.Row, bool) {
	if rootDir == "" || path == "" {
		return nil, false
	}
	if out := gitDiffOutput(rootDir, "diff", diffBase(rootDir), "--", path); out != "" {
		return diff.Parse(out), true
	}
	// Nothing against HEAD means one of two very different things: the file
	// is tracked and clean, or it is untracked and git therefore refused to
	// diff it at all. Only the second gets the --no-index fallback —
	// running it unconditionally would render every clean file in the repo
	// as one enormous addition.
	if gitTracks(rootDir, path) {
		return nil, false
	}
	// The null device is spelled NUL on Windows and /dev/null elsewhere;
	// os.DevNull gets that right and git accepts both.
	if out := gitDiffOutput(rootDir, "diff", "--no-index", "--", os.DevNull, path); out != "" {
		return diff.Parse(out), true
	}
	return nil, false
}

// gitTracks reports whether git has path in its index.
//
// `ls-files --error-unmatch` is the cheap, scriptable way to ask: it exits
// non-zero for an untracked path and prints nothing useful either way, so
// only the exit status is read. A git failure for any other reason answers
// "tracked", which routes to the safe outcome — reporting no changes rather
// than rendering a whole file as new.
func gitTracks(rootDir, path string) bool {
	return gitCmd(rootDir, "ls-files", "--error-unmatch", "--", path).Run() == nil
}

// emptyTreeSHA is git's well-known hash of the empty tree. It is a constant
// of the object format, not something a particular repo has to contain, so
// git resolves it even in a repository with no objects at all.
const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// diffBase returns the revision to diff the working tree against: HEAD
// normally, and the empty tree in a repo with no commits yet.
//
// Without this, `git diff HEAD` fails outright ("bad revision 'HEAD'") in a
// freshly initialised repo and every file in it reports "No changes" — a
// dead end with nothing on screen explaining why. Against the empty tree,
// the first staged file reads as one long addition, which is exactly what it
// is. Agents get pointed at brand-new scratch repos often enough to be worth
// the one extra shell-out.
func diffBase(rootDir string) string {
	if err := gitCmd(rootDir, "rev-parse", "--verify", "-q", "HEAD").Run(); err == nil {
		return "HEAD"
	}
	return emptyTreeSHA
}

// gitDiffOutput runs a git command in rootDir and returns its stdout,
// ignoring the exit status.
//
// Ignoring it is deliberate, not lazy: `git diff --no-index` exits 1 when
// the files differ, which is the case we actually want. Since the only
// signal we need is "did anything come back on stdout", the exit code
// carries no information we would act on.
func gitDiffOutput(rootDir string, args ...string) string {
	out, _ := gitCmd(rootDir, args...).Output()
	return string(out)
}

// openDiff opens (or re-focuses) the inline diff for path.
//
// A file and its diff are two separate tabs sharing one Path — you review
// the diff, then click back to the file for surrounding context, and both
// keep their own scroll position. That is why the tab search below matches
// on mode as well as path.
func (a *App) openDiff(path string) {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	rows, ok := loadDiffRows(a.rootDir, path)
	if !ok {
		a.flash(fmt.Sprintf("No changes in %s", filepath.Base(path)))
		return
	}

	for i, t := range a.tabs {
		if t.IsDiff() && !t.DiffFrozen && t.Path == path {
			// Re-diff on re-open rather than showing what the file looked
			// like the last time this tab was focused. In an agent
			// workflow the file has usually moved on since.
			//
			// A frozen diff (the conflict prompt's buffer-vs-disk view) is
			// skipped: it answers a different question about the same file
			// and must not be recycled into the git diff.
			t.SetDiffRows(rows)
			a.activeTab = i
			return
		}
	}

	t := editor.NewDiffTab(path, rows)
	_, h := a.editorSize()
	t.ScrollToRow(diff.FirstChangedRow(rows), h)
	a.tabs = append(a.tabs, t)
	a.activeTab = len(a.tabs) - 1
	// No success flash: the status bar already shows "Diff · +N −M · file"
	// for as long as the tab is focused, so a flash would say the same
	// thing twice and then expire.
}

// openDiffAtLine opens the diff for path and scrolls to the row showing the
// given zero-based line of the file on disk. Used by the editor's git-gutter
// click, so clicking a change bar next to line 400 lands on that change in
// the diff instead of at the top of it.
func (a *App) openDiffAtLine(path string, line int) {
	a.openDiff(path)
	tab := a.activeTabPtr()
	if tab == nil || !tab.IsDiff() {
		return
	}
	row := diff.RowForNewLine(tab.DiffRows, line+1)
	if row < 0 {
		return
	}
	_, h := a.editorSize()
	tab.ScrollToRow(row, h)
}

// reconcileDiffTab re-runs the diff behind an open diff tab when the file
// underneath it has changed, preserving the scroll position. Called from the
// disk-reconcile loop — which in the workflow Vincent exists for means the
// agent just wrote to the file you are reviewing.
//
// It is quiet on purpose. A flash per write would be constant noise during
// exactly the stretch you are trying to read the diff.
//
// A tab whose file has gone entirely clean keeps showing its last diff
// rather than emptying out: an agent reverting its own change should not
// silently erase the thing you were reading.
//
// The stat and the `git diff` both happened on the poller's goroutine and
// arrive in polled; this function only decides and writes, so it stays on
// the main goroutine. The poller reads the diff for every open diff tab
// rather than trying to predict which ones moved — a wasted `git diff` in
// the background costs nothing anyone can see, and the alternative is the
// worker second-guessing tab state it does not own.
func (a *App) reconcileDiffTab(t *editor.Tab, polled gitPollFile) {
	if t.DiffFrozen {
		// Not this file's git diff — today, a buffer-vs-disk comparison
		// the conflict prompt opened. Re-running the ordinary diff over it
		// would replace what the reviewer asked to see.
		return
	}
	// A missing file is not a reason to bail — a deletion is a diff too —
	// but a zero mtime from the failed stat must not read as "unchanged".
	mtime := time.Time{}
	switch {
	case polled.statErr:
		return // transient stat error; try again next tick.
	case polled.missing:
		if t.DiskGone {
			return // already reconciled this deletion.
		}
	default:
		mtime = polled.mtime
		if !mtime.After(t.Mtime) {
			return
		}
	}
	t.DiskGone = mtime.IsZero()
	t.Mtime = mtime

	if !polled.rowsOK {
		return
	}
	t.SetDiffRows(polled.rows)
}

// -----------------------------------------------------------------------------
// Menu / leader entry points
// -----------------------------------------------------------------------------

// menuViewDiff opens the diff for whatever the active tab is showing. On a
// diff tab it re-runs the diff, which doubles as a manual refresh.
func (a *App) menuViewDiff() {
	tab := a.activeTabPtr()
	if tab == nil || tab.Path == "" {
		return
	}
	a.openDiff(tab.Path)
}

// hasDiffableTab is the menu predicate for View diff: any tab backed by a
// real path. Whether that path actually has changes is only knowable by
// running git, which is too expensive to do on every menu repaint — so the
// row stays enabled and a clean file flashes "No changes" instead.
func (a *App) hasDiffableTab() bool {
	tab := a.activeTabPtr()
	return tab != nil && tab.Path != ""
}

// ctxViewDiff is the file-tree context-menu action: diff the clicked file
// without opening it first. Folders are ignored — a folder-wide diff is a
// different view, and phase 3's git panel is where it belongs.
func ctxViewDiff(a *App, n *filetree.Node) {
	if n == nil || n.IsDir {
		return
	}
	a.openDiff(n.Path)
}
