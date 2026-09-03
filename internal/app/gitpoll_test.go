// =============================================================================
// File: internal/app/gitpoll_test.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// Tests for the background git refresh. These drive real `git` against a
// real repo in t.TempDir, the way diffview_test.go does — the whole point of
// the poller is what happens when git is slow or contended, and a mock would
// verify none of it.
//
// The event round trip is tested end to end (post from the worker, receive on
// the screen's queue, apply on the main goroutine) because the three rules
// that matter are all about that boundary: nothing written from the worker,
// one poll in flight, and results for a vanished tab dropped.

package app

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/editor"
	"github.com/chasereyn/vincent/internal/filetree"
)

// awaitGitPoll pumps the screen's event queue until the poller's event turns
// up, and returns it. Anything else in the queue is handled on the way past
// so the app stays consistent, exactly as the real loop would.
func awaitGitPoll(t *testing.T, a *App) gitPollResult {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ev := a.screen.PollEvent()
		if ev == nil {
			t.Fatal("screen closed before the poll came back")
		}
		if poll, ok := ev.(*gitPollEvent); ok {
			return poll.res
		}
		a.handleEvent(ev)
	}
	t.Fatal("timed out waiting for a gitPollEvent")
	return gitPollResult{}
}

// TestStartGitPoll_DeliversTheRepoState is the round trip: a repo with a
// modified tracked file and an untracked one, polled on a worker goroutine,
// arriving on the event queue with both entries and the branch.
func TestStartGitPoll_DeliversTheRepoState(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "tracked.txt"), "one\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed")
	writeFileT(t, filepath.Join(dir, "tracked.txt"), "changed\n")
	writeFileT(t, filepath.Join(dir, "fresh.txt"), "new\n")

	a := newTestApp(t, dir)
	if !a.startGitPoll() {
		t.Fatal("startGitPoll declined to start with nothing in flight")
	}
	res := awaitGitPoll(t, a)

	if !res.snap.IsRepo {
		t.Fatal("poll came back saying the temp dir is not a repo")
	}
	if res.snap.Branch != "main" {
		t.Errorf("branch = %q, want %q", res.snap.Branch, "main")
	}
	got := map[string]gitEntry{}
	for _, e := range res.snap.Entries {
		got[e.Rel] = e
	}
	if e, ok := got["tracked.txt"]; !ok {
		t.Errorf("tracked.txt missing from %v", sortedKeys(got))
	} else if e.Kind != filetree.GitChangeModified || e.Untracked {
		t.Errorf("tracked.txt = kind %v untracked %v, want modified and tracked",
			e.Kind, e.Untracked)
	}
	if e, ok := got["fresh.txt"]; !ok {
		t.Errorf("fresh.txt missing from %v", sortedKeys(got))
	} else if !e.Untracked {
		t.Errorf("fresh.txt = kind %v untracked %v, want untracked", e.Kind, e.Untracked)
	}

	// And applying it must land the same facts in the UI state the panel
	// and the tree render from.
	a.applyGitPoll(res)
	if a.gitBranch != "main" {
		t.Errorf("a.gitBranch = %q after apply, want %q", a.gitBranch, "main")
	}
	if len(a.gitSnap.Entries) != len(res.snap.Entries) {
		t.Errorf("panel has %d entries, poll delivered %d",
			len(a.gitSnap.Entries), len(res.snap.Entries))
	}
	if len(a.tree.DirtyFiles) == 0 {
		t.Error("the tree's dirty-file map is empty after applying the poll")
	}
}

// TestStartGitPoll_OnlyOneInFlight pins the guard. Two ticks arriving while
// a slow repo is still being read must not become two workers — that is how
// a stall turns into a fork storm.
func TestStartGitPoll_OnlyOneInFlight(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	a := newTestApp(t, dir)

	if !a.startGitPoll() {
		t.Fatal("the first poll did not start")
	}
	if a.startGitPoll() {
		t.Fatal("a second poll started while the first was in flight")
	}
	res := awaitGitPoll(t, a)
	a.applyGitPoll(res)

	if a.gitPollBusy {
		t.Error("gitPollBusy is still set after the result was applied")
	}
	if !a.startGitPoll() {
		t.Error("the poller stayed shut after a completed refresh")
	}
	awaitGitPoll(t, a)
}

// TestApplyGitPoll_DropsResultsForClosedTabs is the other rule. A tab closed
// while its read was in flight must not be resurrected, and — the part that
// would actually crash — nothing in the apply path may index the result map
// as if every entry still had a tab.
func TestApplyGitPoll_DropsResultsForClosedTabs(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	kept := filepath.Join(dir, "kept.txt")
	closed := filepath.Join(dir, "closed.txt")
	writeFileT(t, kept, "one\n")
	writeFileT(t, closed, "two\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed")

	a := newTestApp(t, dir)
	a.openFile(kept)
	a.openFile(closed)
	if len(a.tabs) != 2 {
		t.Fatalf("expected 2 open tabs, got %d", len(a.tabs))
	}

	req := a.buildGitPollRequest()
	if len(req.targets) != 2 {
		t.Fatalf("request covers %d paths, want 2", len(req.targets))
	}
	res := runGitPoll(req)

	// The user closes the second tab before the poll lands, and the file it
	// was showing changes on disk. Nothing about it may be applied.
	a.closeTab(1)
	writeFileT(t, closed, "rewritten by an agent\n")

	a.applyGitPoll(res)

	if len(a.tabs) != 1 {
		t.Fatalf("apply changed the tab count to %d, want 1", len(a.tabs))
	}
	if a.tabs[0].Path != kept {
		t.Errorf("surviving tab is %q, want %q", a.tabs[0].Path, kept)
	}
}

// TestBuildGitPollRequest_OnePathTwoTabs is the trap a path-keyed result map
// sets. A file and its diff are two tabs sharing one Path and they want
// different reads off it; collapsing them to one read silently stops
// refreshing whichever tab was opened second.
func TestBuildGitPollRequest_OnePathTwoTabs(t *testing.T) {
	requireGit(t)
	dir, a := diffTestRepo(t)
	target := filepath.Join(dir, "tracked.txt")

	a.openFile(target)
	a.openDiff(target)

	req := a.buildGitPollRequest()
	if len(req.targets) != 1 {
		t.Fatalf("request covers %d paths, want 1 (the file and its diff share it)",
			len(req.targets))
	}
	got := req.targets[0]
	if !got.text || !got.diff {
		t.Fatalf("target = %+v, want both the text and the diff read", got)
	}

	res := runGitPoll(req)
	file := res.files[target]
	if file.lines == nil {
		t.Error("no gutter markers came back for the file tab")
	}
	if !file.rowsOK || len(file.rows) == 0 {
		t.Error("no diff rows came back for the diff tab")
	}
}

// TestBuildGitPollRequest_SkipsImagesAndUntitled keeps the poller from
// forking git for tabs that could never use the answer.
func TestBuildGitPollRequest_SkipsImagesAndUntitled(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.tabs = []*editor.Tab{
		{Path: "", Buffer: editor.NewBuffer("")},
		{Path: filepath.Join(a.rootDir, "pic.png"), Buffer: editor.NewBuffer(""), Mode: "image"},
	}
	if got := a.buildGitPollRequest(); len(got.targets) != 0 {
		t.Errorf("request covers %d paths, want none: %+v", len(got.targets), got.targets)
	}
}

// TestRunGitPoll_NonRepoDegrades checks the failure shape every git helper in
// Vincent shares: a directory that is not a repo produces an empty result,
// not an error and not a panic.
func TestRunGitPoll_NonRepoDegrades(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "loose.txt")
	writeFileT(t, path, "hello\n")

	res := runGitPoll(gitPollRequest{
		rootDir: dir,
		tree:    true,
		targets: []gitPollTarget{{path: path, repo: dir, spec: path, text: true}},
	})
	if res.snap.IsRepo {
		t.Error("a bare temp dir reported as a git repo")
	}
	if len(res.snap.Entries) != 0 {
		t.Errorf("non-repo produced %d entries", len(res.snap.Entries))
	}
	file, ok := res.files[path]
	if !ok {
		t.Fatal("no entry for the polled path")
	}
	if file.missing || file.statErr {
		t.Errorf("stat of an existing file reported missing=%v statErr=%v",
			file.missing, file.statErr)
	}
}

// TestPollFile_MissingVsError pins the distinction the reconciler leans on:
// "the file is gone" is a state to react to, while any other stat failure
// means we learned nothing and the tab must be left alone.
func TestPollFile_MissingVsError(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "never-existed.txt")

	got := pollFile(gitPollTarget{path: gone, repo: dir, spec: gone, text: true})
	if !got.missing {
		t.Error("a nonexistent path did not come back as missing")
	}
	if got.statErr {
		t.Error("a nonexistent path came back as a stat error too")
	}
	if !got.mtime.IsZero() {
		t.Errorf("mtime = %v for a missing file, want the zero time", got.mtime)
	}
}

// TestGitPollEvent_RoutesThroughHandleEvent makes sure the event is wired
// into the main loop's switch. Without the case the poller runs, posts, and
// nothing ever happens — which looks exactly like a repo that stopped
// changing.
func TestGitPollEvent_RoutesThroughHandleEvent(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "fresh.txt"), "new\n")

	a := newTestApp(t, dir)
	a.gitPollBusy = true // pretend a poll is out
	res := runGitPoll(a.buildGitPollRequest())

	a.handleEvent(&gitPollEvent{when: time.Now(), res: res})

	if a.gitPollBusy {
		t.Error("handleEvent did not clear the in-flight flag")
	}
	if len(a.gitSnap.Entries) == 0 {
		t.Error("handleEvent did not stamp the snapshot onto the panel")
	}
}

// TestRefreshTreeNow_StartsAPollInsteadOfBlocking is the behaviour change
// itself: the 10s tick must hand the git work to the poller rather than
// forking on the goroutine that is supposed to be reading the mouse.
func TestRefreshTreeNow_StartsAPollInsteadOfBlocking(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	a := newTestApp(t, dir)

	a.handleEvent(&treeRefreshEvent{when: time.Now()})
	if !a.gitPollBusy {
		t.Fatal("the tick did not start a background poll")
	}

	// Drain it so the goroutine is not left posting into a closed screen.
	res := awaitGitPoll(t, a)
	a.applyGitPoll(res)
}

// TestAwaitGitPollHelperSeesOtherEvents is a guard on the test helper: the
// finder rebuild posts its own event, and a helper that treated the first
// event as the poll's would be flaky rather than wrong.
func TestAwaitGitPollHelperSeesOtherEvents(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	a := newTestApp(t, dir)

	if err := a.screen.PostEvent(tcell.NewEventResize(a.width, a.height)); err != nil {
		t.Fatalf("post resize: %v", err)
	}
	if !a.startGitPoll() {
		t.Fatal("poll did not start")
	}
	res := awaitGitPoll(t, a)
	a.applyGitPoll(res)
	if a.gitPollBusy {
		t.Error("still busy after applying the result")
	}
}

// TestStartGitPoll_ProceedsAfterAStuckPoll is the deadlock guard on the
// only auto-refresh in the app.
//
// gitPollBusy is cleared in exactly one place — applyGitPoll, reached by the
// worker's PostEvent. A PostEvent that fails on a full queue, or a worker
// that dies inside a git read, therefore used to leave the flag true for the
// rest of the session and silently stop the ten-second refresh: the panel
// just quietly stopped telling the truth. After gitPollStuckAfter the guard
// stops believing the in-flight poll and launches anyway.
func TestStartGitPoll_ProceedsAfterAStuckPoll(t *testing.T) {
	requireGit(t)
	a := newTestApp(t, initRepo(t))

	a.gitPollBusy = true
	a.gitPollStartedAt = time.Now()
	if a.startGitPoll() {
		t.Fatal("a poll that just launched must still block the next one")
	}

	a.gitPollStartedAt = time.Now().Add(-gitPollStuckAfter - time.Second)
	if !a.startGitPoll() {
		t.Error("a poll older than gitPollStuckAfter must not block the next one")
	}
	if time.Since(a.gitPollStartedAt) > time.Minute {
		t.Error("startGitPoll must restamp gitPollStartedAt when it launches")
	}
	awaitGitPoll(t, a)
}

// pollNow runs one git poll synchronously and returns its per-file results,
// so tests that drive reconcileOpenTabsWithDisk directly can hand it the
// same data the background poller would. It exists because the phase-6a
// conflict tests were written against the old no-argument signature and
// the phase-4 poller landed in the same merge.
func pollNow(a *App) map[string]gitPollFile {
	return runGitPoll(a.buildGitPollRequest()).files
}
