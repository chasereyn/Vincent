// =============================================================================
// File: internal/app/gitpoll.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// gitpoll.go moves the 10-second refresh off the UI thread. It has no
// upstream ancestor; the reads it performs came out of app.go's
// refreshTreeNow and refreshGitStatus, and out of diffview.go's
// reconcileDiffTab.
//
// Why: the tick used to run on the main goroutine, and it forks git several
// times — `rev-parse --show-toplevel`, `symbolic-ref HEAD`, `status
// --porcelain`, then a `git diff` per open tab, plus one more per open diff
// tab. On a repo an agent is actively writing to, any of those can block on
// the filesystem or on git's own locking, and while it blocks the event loop
// is not polling: the pointer stops moving, clicks queue up, and the whole
// app reads as hung. That is precisely the window Vincent is used in, so it
// is not a rare case.
//
// The split is the same pattern as the auto-scroll and finder-rebuild
// goroutines: the worker only READS, and hands its results to the main loop
// as a custom tcell event. Nothing in here touches App from the worker
// goroutine, which is what makes it safe under -race:
//
//   - startGitPoll builds a plain-value request on the UI thread (root dir
//     plus a snapshot of which tabs are open and what mtime each one last
//     knew), hands it to the worker, and returns.
//   - runGitPoll is a free function. It has no App, by construction — that
//     is cheaper to keep honest than a comment saying "don't touch a.".
//   - applyGitPoll runs back on the UI thread from handleEvent and does
//     every write.
//
// Two rules worth stating because getting them wrong is subtle:
//
//   - ONE POLL IN FLIGHT. If the previous one has not come back, the tick is
//     skipped. Otherwise a slow repo stacks up workers, each forking git,
//     and the pile makes the thing it is measuring worse.
//   - RESULTS FOR A CLOSED TAB ARE DROPPED. applyGitPoll walks the live tab
//     list and looks each tab up in the result, never the other way round,
//     so a tab closed (or opened) mid-poll simply is not in the join.

package app

import (
	"os"
	"time"

	"github.com/chasereyn/vincent/internal/diff"
	"github.com/chasereyn/vincent/internal/editor"
)

// gitPollEvent is the custom tcell event the poller posts when a refresh
// comes back. handleEvent turns it into UI state via applyGitPoll.
type gitPollEvent struct {
	when time.Time
	res  gitPollResult
}

// When satisfies the tcell.Event interface.
func (e *gitPollEvent) When() time.Time { return e.when }

// gitPollTarget is one path the worker must read, and which reads it wants.
// Deliberately not a *editor.Tab — handing the worker a pointer into live UI
// state is the bug this whole file exists to avoid.
//
// Both flags can be set at once. A file and its diff are two tabs sharing
// one Path (see diffview.go), and they want different reads off it: the file
// tab wants gutter markers, the diff tab wants parsed diff rows. Collapsing
// them to one read per path would silently stop refreshing whichever tab
// happened to be opened second.
type gitPollTarget struct {
	path string
	text bool
	diff bool
}

// gitPollRequest is everything one refresh needs, by value.
type gitPollRequest struct {
	rootDir string
	tree    bool // false in single-file mode: no whole-repo status to read
	targets []gitPollTarget
}

// gitPollFile is one tab's worth of read-only results.
//
// missing and statErr are kept apart on purpose: a file that is gone is a
// real state the reconciler reacts to (flash once, mark the tab), while a
// permission error or any other stat failure means "we learned nothing" and
// the tab must be left exactly as it was.
type gitPollFile struct {
	missing bool
	statErr bool
	mtime   time.Time

	// lines is the gutter-marker map for a text tab.
	lines map[int]editor.GitLineChange

	// rows is the parsed diff for a diff tab, and rowsOK is git's answer to
	// "was there anything to diff at all" — an unmodified file reports no
	// rows, which is not the same as an empty diff.
	rows   []diff.Row
	rowsOK bool
}

// gitPollResult is one completed refresh.
type gitPollResult struct {
	snap  gitSnapshot
	files map[string]gitPollFile
}

// startGitPoll kicks off a background refresh, unless one is already in
// flight. Runs on the UI thread; returns whether it actually launched, which
// is what the tests assert the in-flight guard on.
func (a *App) startGitPoll() bool {
	if a.gitPollBusy {
		return false
	}
	req := a.buildGitPollRequest()
	scr := a.screen
	a.gitPollBusy = true
	go func() {
		res := runGitPoll(req)
		// A failed post means the screen is gone (we are shutting down) or
		// tcell's queue is full. Either way there is nothing useful to do
		// with the result, and gitPollBusy dies with the process.
		_ = scr.PostEvent(&gitPollEvent{when: time.Now(), res: res})
	}()
	return true
}

// buildGitPollRequest snapshots the UI state the worker needs, one target
// per distinct path. UI thread only — this is the one place that reads
// a.tabs on behalf of the poller.
func (a *App) buildGitPollRequest() gitPollRequest {
	req := gitPollRequest{rootDir: a.rootDir, tree: a.tree != nil}
	at := map[string]int{} // path -> index into req.targets
	for _, tab := range a.tabs {
		if tab == nil || tab.Path == "" || tab.IsImage() {
			continue
		}
		idx, seen := at[tab.Path]
		if !seen {
			req.targets = append(req.targets, gitPollTarget{path: tab.Path})
			idx = len(req.targets) - 1
			at[tab.Path] = idx
		}
		if tab.IsDiff() {
			req.targets[idx].diff = true
		} else {
			req.targets[idx].text = true
		}
	}
	return req
}

// runGitPoll performs every read one refresh needs. It runs on a worker
// goroutine and takes no App, so there is no UI state within reach.
func runGitPoll(req gitPollRequest) gitPollResult {
	res := gitPollResult{files: make(map[string]gitPollFile, len(req.targets))}
	if req.tree && req.rootDir != "" {
		// The single `git status` parse both the tree and the Changes panel
		// derive from. See gitentries.go.
		res.snap = loadGitSnapshot(req.rootDir)
	}
	for _, target := range req.targets {
		res.files[target.path] = pollFile(req.rootDir, target)
	}
	return res
}

// pollFile stats one path and runs whichever git reads its tabs want.
func pollFile(rootDir string, target gitPollTarget) gitPollFile {
	out := gitPollFile{}
	info, err := os.Stat(target.path)
	switch {
	case err == nil:
		out.mtime = info.ModTime()
	case os.IsNotExist(err):
		out.missing = true
	default:
		out.statErr = true
	}
	if target.text {
		out.lines = loadGitLineChanges(rootDir, target.path)
	}
	if target.diff {
		out.rows, out.rowsOK = loadDiffRows(rootDir, target.path)
	}
	return out
}

// applyGitPoll turns a completed refresh into UI state. Main goroutine only.
//
// The busy flag is cleared first so a poll that somehow panics inside the
// appliers cannot wedge the poller shut for the rest of the session.
func (a *App) applyGitPoll(res gitPollResult) {
	a.gitPollBusy = false
	a.applyGitSnapshot(res.snap)
	a.reconcileOpenTabsWithDisk(res.files)
}
