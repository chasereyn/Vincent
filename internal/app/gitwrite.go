// =============================================================================
// File: internal/app/gitwrite.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// No upstream ancestor. spice-edit never wrote to a repository; the read
// side it did have lives in gitentries.go / gitstatus.go, and this file is
// deliberately separate from both.
// =============================================================================

// gitwrite.go is every git command Vincent runs that CHANGES something:
// commit-all, push, and checkout. Three, and no more — "four writes, all
// blunt" is a non-negotiable (see CLAUDE.md), and the fourth is saving a
// text buffer, which is not git's business.
//
// Two rules shape the file.
//
// EVERY WRITE GOES THROUGH A gitRunner. The runner is a function value, so
// a test can inject a fake and assert on the argv without a real remote,
// a real network, or a real credential prompt. Nothing here calls exec
// directly except runGitWrite itself.
//
// NO GIT_OPTIONAL_LOCKS=0. The read path sets it (gitCmd in gitentries.go)
// so polling never contends with the agent's `git add` for
// `.git/index.lock`. Here it would be wrong: every command is a write that
// genuinely needs the index lock. When the lock is already held we surface
// one sentence and stop — there is deliberately NO retry loop, because the
// thing holding the lock is usually an agent mid-write and a retry loop
// would race it.
//
// GIT_TERMINAL_PROMPT=0 is set on all of them so a repo wanting a username
// fails in milliseconds instead of blocking on a terminal that is in raw
// mode and cannot show git's prompt anyway. exec.CommandContext caps the
// rest: a hung SSH connection cannot leave the "Pushing…" flag stuck
// forever.

package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/chasereyn/vincent/internal/review"
)

const (
	// gitWriteTimeout bounds a local write (add / commit / checkout /
	// for-each-ref). Generous rather than tight: a pre-commit hook is a
	// legitimate reason for `git commit` to take seconds, and these run
	// on the UI thread. It exists so a wedged hook cannot hang Vincent
	// forever, not to police slow hooks.
	gitWriteTimeout = 30 * time.Second

	// gitPushTimeout bounds a push, which talks to a network. Sixty
	// seconds because a big push over a slow link is real, and because
	// the failure mode we are actually guarding against — a hung SSH
	// handshake — never resolves at all.
	gitPushTimeout = 60 * time.Second

	// indexLockSentence is the one message a locked index gets. Named
	// because it is asserted on in tests and because it is the only git
	// failure Vincent explains rather than merely reports: the cause is
	// almost always the agent whose work you are reviewing, and "try
	// again in a moment" is genuinely the right advice.
	indexLockSentence = "git index is locked (an agent is probably mid-write). Try again in a moment."
)

// errDetachedHEAD is what the push worker reports instead of a branch name
// when HEAD is not on a branch. A sentinel rather than a string so
// applyGitPush can tell it apart from git's own failures, which get the
// stderr treatment.
var errDetachedHEAD = errors.New("detached HEAD")

// gitRunner runs one git command in dir and returns its stdout, stderr and
// error separately.
//
// A function value rather than a direct exec call so the whole write side
// is testable: a fake runner records the argv and answers without touching
// a repository, a remote, or a credential helper. Every operation below
// takes one as its first argument for the same reason.
type gitRunner func(ctx context.Context, dir string, args ...string) (stdout, stderr string, err error)

// runGitWrite is the production gitRunner: `git -C <dir> <args...>` with
// the environment every Vincent write wants. See the file comment for why
// GIT_TERMINAL_PROMPT is set and GIT_OPTIONAL_LOCKS deliberately is not.
//
// stdout and stderr are captured separately because they play different
// roles: stdout is what a flash quotes (a short SHA), stderr is what the
// log keeps verbatim.
func runGitWrite(ctx context.Context, dir string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	return out.String(), errOut.String(), err
}

// gitWriter returns the runner this App should use — the injected one in a
// test, the real one in production. Every write path calls this rather than
// reading the field, so a nil field can never reach exec.
func (a *App) gitWriter() gitRunner {
	if a.gitWriteRunner != nil {
		return a.gitWriteRunner
	}
	return runGitWrite
}

// -----------------------------------------------------------------------------
// The operations
// -----------------------------------------------------------------------------

// gitCommitAll stages everything and commits it in one gesture:
// `git add -A` then `git commit -m <message>`.
//
// Tracked and untracked together, no staging step, no partial commit. That
// is the blunt write CLAUDE.md asks for: anything finer belongs to lazygit
// one pane over. A failure in the `add` short-circuits, because committing
// after a half-finished stage would produce a commit nobody asked for.
func gitCommitAll(run gitRunner, dir, message string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitWriteTimeout)
	defer cancel()
	if out, errOut, err := run(ctx, dir, "add", "-A"); err != nil {
		return out, errOut, err
	}
	return run(ctx, dir, "commit", "-m", message)
}

// gitHeadShort returns the short SHA of HEAD, for the "Committed abc1234"
// flash. Read after the commit rather than parsed out of `git commit`'s
// own output: that output is localised and its format is not a promise.
func gitHeadShort(run gitRunner, dir string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitWriteTimeout)
	defer cancel()
	out, errOut, err := run(ctx, dir, "rev-parse", "--short", "HEAD")
	return strings.TrimSpace(out), errOut, err
}

// gitCurrentBranch returns the checked-out branch name, or "" when HEAD is
// detached.
//
// `rev-parse --abbrev-ref HEAD` prints the literal string "HEAD" when
// detached, which is the whole reason this is not read off
// gitSnapshot.Branch: loadGitBranch falls back to a short SHA there, and a
// SHA is indistinguishable from a branch called by its SHA. Pushing needs
// to know the difference.
func gitCurrentBranch(run gitRunner, dir string) (branch, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitWriteTimeout)
	defer cancel()
	out, errOut, err := run(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", errOut, err
	}
	name := strings.TrimSpace(out)
	if name == "HEAD" {
		return "", errOut, nil
	}
	return name, errOut, nil
}

// gitPush pushes branch to origin, creating the upstream link when the
// branch does not have one.
//
// The probe is `rev-parse --abbrev-ref --symbolic-full-name @{u}`, which
// exits non-zero exactly when there is no upstream. Running `git push -u
// origin <branch>` unconditionally would work too, but it also rewrites
// the upstream of a branch that already tracks something else, and a
// review client has no business doing that quietly.
func gitPush(run gitRunner, dir, branch string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitPushTimeout)
	defer cancel()
	if _, _, err := run(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err != nil {
		return run(ctx, dir, "push", "-u", "origin", branch)
	}
	return run(ctx, dir, "push")
}

// gitCheckout switches the working tree to branch.
//
// Plain `git checkout <branch>` — no -B, no --force, no stash. If the
// working tree would be clobbered git refuses and says so, and that
// refusal is the right answer: Vincent's own guard (no dirty tabs) covers
// the editor's unsaved work, and git's covers the repository's.
func gitCheckout(run gitRunner, dir, branch string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitWriteTimeout)
	defer cancel()
	return run(ctx, dir, "checkout", branch)
}

// gitBranches lists local branch names, most recently committed first.
//
// `for-each-ref refs/heads` rather than `git branch`: the latter's output
// is a UI (it decorates the current branch with "* " and colours it), and
// parsing a UI is how you end up with a branch called "*". The sort is
// committerdate descending because the branch you want next is nearly
// always one you touched recently.
func gitBranches(run gitRunner, dir string) (branches []string, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitWriteTimeout)
	defer cancel()
	out, errOut, err := run(ctx, dir, "for-each-ref", "--sort=-committerdate", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, errOut, err
	}
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			branches = append(branches, name)
		}
	}
	return branches, errOut, nil
}

// -----------------------------------------------------------------------------
// Failure reporting
// -----------------------------------------------------------------------------

// gitFailureSentence turns a git failure into ONE plain sentence for the
// status bar. action is the verb the user just performed ("Commit",
// "Push", "Checkout").
//
// The rule is herdr-reviewr's, applied to git: log the full envelope, show
// a sentence. git's stderr is often five lines of hint: text naming remote
// refs and config keys the reviewer never typed, and painting that into a
// one-row status bar truncates it into nonsense. The verbatim text goes to
// ~/.config/vincent/herdr.log (see reviewlog.go) where it can be read.
func gitFailureSentence(action, stderr string, err error) string {
	if strings.Contains(stderr, "index.lock") {
		return indexLockSentence
	}
	detail := gitStderrSummary(stderr)
	if detail == "" && err != nil {
		detail = err.Error()
	}
	if detail == "" {
		return action + " failed"
	}
	return action + " failed: " + detail
}

// gitStderrSummary picks the one line of git's stderr worth showing: the
// first that is neither blank nor a "hint:" continuation, with git's own
// "fatal: " / "error: " prefix stripped and the result capped.
//
// The hint lines are skipped on purpose. They are the longest part of a
// typical failure and the least useful in a status bar — "hint: Updates
// were rejected because…" is three lines of advice about a fix Vincent
// cannot perform anyway.
func gitStderrSummary(stderr string) string {
	for _, raw := range strings.Split(stderr, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "hint:") {
			continue
		}
		line = strings.TrimPrefix(line, "fatal: ")
		line = strings.TrimPrefix(line, "error: ")
		return trimRunes(line, gitSentenceMax)
	}
	return ""
}

// gitSentenceMax caps a quoted stderr line. The status bar is one row and
// shares it with the branch and cursor readouts, so a long line has to be
// cut somewhere; cutting it here means the cut is visible (trimRunes adds
// an ellipsis) rather than happening silently at the edge of the screen.
const gitSentenceMax = 90

// -----------------------------------------------------------------------------
// Push — Esc P
// -----------------------------------------------------------------------------

// gitPushEvent is the custom tcell event the push worker posts when it
// comes back. Same pattern as gitPollEvent: the worker only shells out and
// hands plain values to the main loop, which does every write to UI state.
type gitPushEvent struct {
	when   time.Time
	branch string
	stderr string
	err    error
}

// When satisfies the tcell.Event interface.
func (e *gitPushEvent) When() time.Time { return e.when }

// pushBranch is the Esc P leader action: `git push` on a worker goroutine.
//
// A push is the one git write here that talks to a network, so it is the
// one that cannot run on the UI thread — a slow remote would freeze the
// pointer mid-review, which is exactly the stall gitpoll.go was built to
// end. The flag refuses a second push while one is in flight; the 60-second
// context inside gitPush is what guarantees the flag comes back even if
// the remote never answers.
//
// The branch is resolved on the worker, not here, because telling a branch
// from a detached HEAD costs a fork (see gitCurrentBranch) and forks on the
// UI thread are what this whole pattern exists to avoid.
func (a *App) pushBranch() {
	if a.tree == nil {
		a.flash("Push isn't available in single-file mode")
		return
	}
	if !a.gitSnap.IsRepo {
		a.flash("Not a git repository")
		return
	}
	if a.pushing {
		a.flash("Already pushing…")
		return
	}
	a.pushing = true
	a.flash("Pushing…")

	run := a.gitWriter()
	// The active repo, resolved HERE on the UI thread and captured by
	// value. The worker must not read a.repos or a.tabs, and the answer
	// must not change while the push is in flight.
	dir := a.activeRepo()
	scr := a.screen
	go func() {
		branch, stderr, err := gitCurrentBranch(run, dir)
		switch {
		case err != nil:
			// Could not even work out where we are — report that rather
			// than pushing something unidentified.
		case branch == "":
			err = errDetachedHEAD
		default:
			_, stderr, err = gitPush(run, dir, branch)
		}
		// A failed post means the screen is gone (we are shutting down).
		// The flag dies with the process, so there is nothing to undo.
		_ = scr.PostEvent(&gitPushEvent{when: time.Now(), branch: branch, stderr: stderr, err: err})
	}()
}

// applyGitPush turns a finished push into a flash. Main goroutine only,
// reached from handleEvent.
//
// The flag is cleared first, before anything that could fail, so a push
// can never leave Esc P refusing forever.
func (a *App) applyGitPush(e *gitPushEvent) {
	a.pushing = false
	switch {
	case errors.Is(e.err, errDetachedHEAD):
		a.flash("HEAD is detached — check out a branch first")
	case e.err != nil:
		review.Logf("git push: %v\n%s", e.err, e.stderr)
		a.flash(gitFailureSentence("Push", e.stderr, e.err))
	default:
		a.flash("Pushed " + e.branch + " to origin")
	}
}
