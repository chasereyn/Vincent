// =============================================================================
// File: internal/app/gitwrite_test.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// fakeGit is a scripted gitRunner: it records every argv it was handed and
// answers from a table keyed by the first argument. Anything not in the
// table succeeds with empty output, which keeps a test that cares about one
// command from having to describe the other four.
type fakeGit struct {
	calls   [][]string
	replies map[string]fakeGitReply
}

// fakeGitReply is one scripted answer.
type fakeGitReply struct {
	stdout string
	stderr string
	err    error
}

// run is the gitRunner the fake hands to the code under test.
func (f *fakeGit) run(_ context.Context, _ string, args ...string) (string, string, error) {
	f.calls = append(f.calls, args)
	if r, ok := f.replies[args[0]]; ok {
		return r.stdout, r.stderr, r.err
	}
	return "", "", nil
}

// argv renders the recorded calls as one string per call, so an assertion
// can name the command it expected without indexing into a slice of slices.
func (f *fakeGit) argv() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// newFakeGit builds a fake with a reply table.
func newFakeGit(replies map[string]fakeGitReply) *fakeGit {
	if replies == nil {
		replies = map[string]fakeGitReply{}
	}
	return &fakeGit{replies: replies}
}

// bareOrigin creates a bare repo in its own temp dir and wires it up as
// `origin` of repo. A real remote on the local filesystem is the only way
// to test a push without a network — and without one, "push" is the single
// operation here that no test would cover.
func bareOrigin(t *testing.T, repo string) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "--bare")
	gitRun(t, repo, "remote", "add", "origin", dir)
	return dir
}

// -----------------------------------------------------------------------------
// The scripted argv — what each operation actually asks git to do
// -----------------------------------------------------------------------------

// TestGitCommitAll_StagesEverythingThenCommits pins the two-command shape.
// "Commit all" means tracked AND untracked in one gesture, which is what
// `add -A` buys and what CLAUDE.md asked for by name.
func TestGitCommitAll_StagesEverythingThenCommits(t *testing.T) {
	f := newFakeGit(nil)
	if _, _, err := gitCommitAll(f.run, "/repo", "fix the thing"); err != nil {
		t.Fatalf("gitCommitAll: %v", err)
	}
	want := []string{"add -A", "commit -m fix the thing"}
	got := f.argv()
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestGitCommitAll_StopsWhenStagingFails is the short-circuit. Committing
// after a half-finished `add` would produce a commit nobody described.
func TestGitCommitAll_StopsWhenStagingFails(t *testing.T) {
	f := newFakeGit(map[string]fakeGitReply{
		"add": {stderr: "fatal: pathspec broke", err: errors.New("exit 128")},
	})
	if _, stderr, err := gitCommitAll(f.run, "/repo", "msg"); err == nil {
		t.Fatal("a failed add must fail the whole commit")
	} else if !strings.Contains(stderr, "pathspec broke") {
		t.Errorf("stderr = %q, want the add's stderr", stderr)
	}
	if len(f.calls) != 1 {
		t.Errorf("ran %v, want the add alone", f.argv())
	}
}

// TestGitPush_CreatesTheUpstreamWhenThereIsNone covers the branch a
// reviewer pushes most often: one an agent just made. The @{u} probe
// failing is git's own way of saying "no upstream".
func TestGitPush_CreatesTheUpstreamWhenThereIsNone(t *testing.T) {
	f := newFakeGit(map[string]fakeGitReply{
		"rev-parse": {err: errors.New("exit 128")},
	})
	if _, _, err := gitPush(f.run, "/repo", "feature/x"); err != nil {
		t.Fatalf("gitPush: %v", err)
	}
	got := f.argv()
	if len(got) != 2 || got[1] != "push -u origin feature/x" {
		t.Errorf("argv = %v, want the @{u} probe then `push -u origin feature/x`", got)
	}
}

// TestGitPush_PlainPushWhenTheBranchTracksSomething is the other half. `-u`
// unconditionally would rewrite the upstream of a branch already tracking
// something else, which a review client has no business doing quietly.
func TestGitPush_PlainPushWhenTheBranchTracksSomething(t *testing.T) {
	f := newFakeGit(map[string]fakeGitReply{
		"rev-parse": {stdout: "origin/main\n"},
	})
	if _, _, err := gitPush(f.run, "/repo", "main"); err != nil {
		t.Fatalf("gitPush: %v", err)
	}
	got := f.argv()
	if len(got) != 2 || got[1] != "push" {
		t.Errorf("argv = %v, want the @{u} probe then a bare `push`", got)
	}
}

// TestGitBranches_ReadsForEachRef pins both the command and the parse. The
// command matters as much as the output: `git branch` decorates the current
// branch with "* ", and parsing that is how you end up with a branch called
// "*".
func TestGitBranches_ReadsForEachRef(t *testing.T) {
	f := newFakeGit(map[string]fakeGitReply{
		"for-each-ref": {stdout: "main\nfeature/x\n\n  spaced  \n"},
	})
	names, _, err := gitBranches(f.run, "/repo")
	if err != nil {
		t.Fatalf("gitBranches: %v", err)
	}
	want := []string{"main", "feature/x", "spaced"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("name %d = %q, want %q", i, names[i], want[i])
		}
	}
	argv := f.argv()
	if len(argv) != 1 || !strings.HasPrefix(argv[0], "for-each-ref --sort=-committerdate") {
		t.Errorf("argv = %v, want a committerdate-sorted for-each-ref", argv)
	}
}

// TestGitCurrentBranch_DetachedHeadIsEmpty is why the push path does not
// read gitSnapshot.Branch: loadGitBranch reports a short SHA when detached,
// and a SHA cannot be pushed by name.
func TestGitCurrentBranch_DetachedHeadIsEmpty(t *testing.T) {
	f := newFakeGit(map[string]fakeGitReply{"rev-parse": {stdout: "HEAD\n"}})
	branch, _, err := gitCurrentBranch(f.run, "/repo")
	if err != nil {
		t.Fatalf("gitCurrentBranch: %v", err)
	}
	if branch != "" {
		t.Errorf("branch = %q, want empty for a detached HEAD", branch)
	}
}

// TestGitCheckout_IsPlain keeps the write blunt: no -B, no --force, no
// stash. Git's own refusal when a file would be clobbered is the answer we
// want to surface, not one to work around.
func TestGitCheckout_IsPlain(t *testing.T) {
	f := newFakeGit(nil)
	if _, _, err := gitCheckout(f.run, "/repo", "main"); err != nil {
		t.Fatalf("gitCheckout: %v", err)
	}
	if got := f.argv(); len(got) != 1 || got[0] != "checkout main" {
		t.Errorf("argv = %v, want a plain `checkout main`", got)
	}
}

// -----------------------------------------------------------------------------
// Failure sentences
// -----------------------------------------------------------------------------

// TestGitFailureSentence_IndexLockGetsItsOwnSentence pins the one git
// failure Vincent explains rather than reports. The cause is nearly always
// the agent whose work is being reviewed, and there is deliberately no
// retry loop racing it.
func TestGitFailureSentence_IndexLockGetsItsOwnSentence(t *testing.T) {
	stderr := "fatal: Unable to create '/repo/.git/index.lock': File exists.\n"
	if got := gitFailureSentence("Commit", stderr, errors.New("exit 128")); got != indexLockSentence {
		t.Errorf("sentence = %q, want %q", got, indexLockSentence)
	}
}

// TestGitFailureSentence_SkipsHintsAndPrefixes proves the status bar gets
// the useful line. git's hint: block is the longest and least actionable
// part of a typical failure, and "fatal: " is noise in a one-row bar.
func TestGitFailureSentence_SkipsHintsAndPrefixes(t *testing.T) {
	stderr := "\nhint: Updates were rejected because the tip is behind\n" +
		"error: failed to push some refs to 'origin'\n"
	got := gitFailureSentence("Push", stderr, nil)
	if got != "Push failed: failed to push some refs to 'origin'" {
		t.Errorf("sentence = %q", got)
	}
}

// TestGitFailureSentence_FallsBackToTheError covers a git that died without
// saying anything — a timeout, or a binary that is not there. Reporting
// "Push failed" with no reason at all would be worse than the exit status.
func TestGitFailureSentence_FallsBackToTheError(t *testing.T) {
	got := gitFailureSentence("Checkout", "", errors.New("signal: killed"))
	if got != "Checkout failed: signal: killed" {
		t.Errorf("sentence = %q", got)
	}
}

// -----------------------------------------------------------------------------
// Against a real git
// -----------------------------------------------------------------------------

// TestRunGitWrite_CommitsForReal exercises the production runner end to
// end. The scripted tests above pin the argv; this one pins that the argv
// actually does what we think in a repo, including that `add -A` picks up
// an untracked file.
func TestRunGitWrite_CommitsForReal(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "fresh.txt"), "new\n")

	if _, stderr, err := gitCommitAll(runGitWrite, dir, "seed the repo"); err != nil {
		t.Fatalf("gitCommitAll: %v\n%s", err, stderr)
	}
	subject := gitOut(t, dir, "log", "-1", "--format=%s")
	if subject != "seed the repo" {
		t.Errorf("subject = %q, want the message we passed", subject)
	}
	if status := gitOut(t, dir, "status", "--porcelain"); status != "" {
		t.Errorf("status = %q, want a clean tree after commit-all", status)
	}
	sha, _, err := gitHeadShort(runGitWrite, dir)
	if err != nil || sha == "" {
		t.Fatalf("gitHeadShort: %q %v", sha, err)
	}
	if len(sha) > 12 || strings.ContainsAny(sha, " \n") {
		t.Errorf("short sha = %q, want a bare abbreviated hash", sha)
	}
}

// TestGitPush_AgainstABareOrigin is the only test that pushes. The remote
// is a bare repo in a second temp dir, so there is no network and no
// credential prompt anywhere in it.
func TestGitPush_AgainstABareOrigin(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "a.txt"), "one\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed")
	origin := bareOrigin(t, dir)

	// No upstream yet, so this must take the `push -u origin main` path.
	if _, stderr, err := gitPush(runGitWrite, dir, "main"); err != nil {
		t.Fatalf("first push: %v\n%s", err, stderr)
	}
	if got := gitOut(t, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); got != "origin/main" {
		t.Errorf("upstream = %q, want origin/main — the -u did not stick", got)
	}
	if got := gitOut(t, origin, "rev-parse", "HEAD"); got != gitOut(t, dir, "rev-parse", "HEAD") {
		t.Error("origin's HEAD does not match ours after the push")
	}

	// A second push now has an upstream and must take the bare-push path.
	writeFileT(t, filepath.Join(dir, "a.txt"), "two\n")
	gitRun(t, dir, "commit", "-qam", "second")
	if _, stderr, err := gitPush(runGitWrite, dir, "main"); err != nil {
		t.Fatalf("second push: %v\n%s", err, stderr)
	}
	if got := gitOut(t, origin, "rev-parse", "HEAD"); got != gitOut(t, dir, "rev-parse", "HEAD") {
		t.Error("origin did not receive the second commit")
	}
}

// TestGitCheckout_SwitchesForReal proves the checkout moves HEAD and the
// working tree, which is what the branch picker's tab reload depends on.
func TestGitCheckout_SwitchesForReal(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "a.txt"), "main\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed")
	gitRun(t, dir, "checkout", "-q", "-b", "other")
	writeFileT(t, filepath.Join(dir, "a.txt"), "other\n")
	gitRun(t, dir, "commit", "-qam", "other")

	if _, stderr, err := gitCheckout(runGitWrite, dir, "main"); err != nil {
		t.Fatalf("gitCheckout: %v\n%s", err, stderr)
	}
	if got := gitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Errorf("branch = %q, want main", got)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if body := string(raw); body != "main\n" {
		t.Errorf("a.txt = %q, want the main version back on disk", body)
	}
}

// -----------------------------------------------------------------------------
// Push, at the App level
// -----------------------------------------------------------------------------

// TestPushBranch_RefusesASecondWhileOneIsInFlight is the flag's whole job.
// Two pushes racing the same remote is the kind of thing that produces a
// non-fast-forward failure the user did not cause.
func TestPushBranch_RefusesASecondWhileOneIsInFlight(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.gitSnap = gitSnapshot{IsRepo: true, Branch: "main"}
	a.pushing = true

	a.pushBranch()
	if !strings.Contains(a.statusMsg, "Already pushing") {
		t.Errorf("flash = %q, want the already-pushing refusal", a.statusMsg)
	}
}

// TestPushBranch_RefusesOutsideARepo keeps the leader key harmless
// everywhere. Esc P is one keystroke away from Esc p, so its no-op case has
// to be a sentence rather than a stack trace.
func TestPushBranch_RefusesOutsideARepo(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.pushBranch()
	if a.pushing {
		t.Error("a non-repo must not arm the pushing flag")
	}
	if !strings.Contains(a.statusMsg, "Not a git repository") {
		t.Errorf("flash = %q", a.statusMsg)
	}
}

// TestApplyGitPush_ClearsTheFlagOnEveryPath is the deadlock guard. If any
// outcome left pushing true, Esc P would refuse for the rest of the session.
func TestApplyGitPush_ClearsTheFlagOnEveryPath(t *testing.T) {
	cases := []struct {
		name  string
		event gitPushEvent
		want  string
	}{
		{"success", gitPushEvent{branch: "main"}, "Pushed main to origin"},
		{"detached", gitPushEvent{err: errDetachedHEAD}, "detached"},
		{"rejected", gitPushEvent{
			err:    errors.New("exit 1"),
			stderr: "hint: fetch first\nerror: failed to push some refs\n",
		}, "failed to push some refs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t, t.TempDir())
			a.pushing = true
			ev := tc.event
			a.applyGitPush(&ev)
			if a.pushing {
				t.Error("pushing must be false once the result is applied")
			}
			if !strings.Contains(a.statusMsg, tc.want) {
				t.Errorf("flash = %q, want it to mention %q", a.statusMsg, tc.want)
			}
		})
	}
}

// TestPushBranch_RunsOnAWorkerAndReportsBack drives the whole push path
// the way the event loop does: the action forks a goroutine, the goroutine
// posts an event, and the main loop applies it. This is the test that gives
// the race detector something to look at — the push worker is the only new
// goroutine phase 3b adds.
func TestPushBranch_RunsOnAWorkerAndReportsBack(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.gitSnap = gitSnapshot{IsRepo: true, Branch: "main"}
	f := newFakeGit(map[string]fakeGitReply{"rev-parse": {stdout: "main\n"}})
	a.gitWriteRunner = f.run

	a.pushBranch()
	if !a.pushing {
		t.Fatal("pushBranch must arm the in-flight flag before returning")
	}
	if !strings.Contains(a.statusMsg, "Pushing") {
		t.Errorf("flash = %q, want the in-flight message", a.statusMsg)
	}

	deadline := time.Now().Add(15 * time.Second)
	for a.pushing && time.Now().Before(deadline) {
		ev := a.screen.PollEvent()
		if ev == nil {
			t.Fatal("screen closed before the push came back")
		}
		a.handleEvent(ev)
	}
	if a.pushing {
		t.Fatal("timed out waiting for a gitPushEvent")
	}
	if a.statusMsg != "Pushed main to origin" {
		t.Errorf("flash = %q", a.statusMsg)
	}
}

// TestGitWriter_DefaultsToTheRealRunner proves the injection point fails
// safe: an App nobody wired a fake into still gets a working runner rather
// than a nil call.
func TestGitWriter_DefaultsToTheRealRunner(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	if a.gitWriter() == nil {
		t.Fatal("gitWriter must never return nil")
	}
	f := newFakeGit(nil)
	a.gitWriteRunner = f.run
	if _, _, err := a.gitWriter()(context.Background(), "/repo", "status"); err != nil {
		t.Fatalf("injected runner: %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("the injected runner was not used: %v", f.argv())
	}
}

// -----------------------------------------------------------------------------
// small shared helpers
// -----------------------------------------------------------------------------

// gitOut runs git in cwd and returns its trimmed stdout, failing the test
// on a non-zero exit. The read-side twin of gitRun in gitstatus_test.go.
func gitOut(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, cwd, err)
	}
	return strings.TrimSpace(string(out))
}
