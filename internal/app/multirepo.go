// =============================================================================
// File: internal/app/multirepo.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// No upstream ancestor. spice-edit assumed one root and one repo, and every
// git call in it ran in the root directory.
// =============================================================================

// multirepo.go is the registry that answers "which repository does this
// belong to". Every git call in Vincent now goes through it.
//
// Why it exists: at work the root is ~/Developer/RP-Repos, a FLAT FOLDER OF
// REPOS, and the root itself is not one. Running `git -C <root> status`
// there reports nothing at all, so the Changes panel was empty, the branch
// row was blank, and the three writes had nothing to write to. At home the
// root IS a repo and everything already worked. Both cases have to work, and
// the second one has to keep working byte for byte — which is why almost
// every function here has an early return for it.
//
// THE SINGLE-REPO SHORT CIRCUIT. When the root is itself a repo (or when
// discovery found nothing, which is also how "the root is a folder INSIDE a
// repo" arrives here — repos.Discover only looks downward), repoFor and
// activeRepo both return a.rootDir verbatim and gitPathFor hands git the
// absolute path, exactly as the pre-phase-8a code did. Nothing downstream
// can tell the difference, and the regression tests in multirepo_test.go
// pin that. Only when there is a real registry of two or more repos, or one
// repo below a non-repo root, does the owner lookup engage.
//
// TWO PATHS, NOT TWO PARSERS. The per-repo status read is the SAME
// loadGitSnapshot that gitentries.go always used, run once per repo and
// merged; there is deliberately no second porcelain parser. See
// loadReposSnapshot.

package app

import (
	"path/filepath"
	"sync"

	"github.com/chasereyn/vincent/internal/repos"
)

// repoStatusWorkers bounds how many `git status` runs the poll worker has
// in flight at once.
//
// Four, not one and not len(repos). One serialises a folder of twenty
// repos into twenty round trips through the filesystem; unbounded forks
// twenty gits at once on a machine that is already running an agent, which
// is the load this whole file's caller (gitpoll.go) exists to keep off the
// UI. Four saturates a single spinning answer without becoming the reason
// the machine is slow.
const repoStatusWorkers = 4

// -----------------------------------------------------------------------------
// The registry
// -----------------------------------------------------------------------------

// repoRoot is the root as an ABSOLUTE path.
//
// a.rootDir keeps whatever the user typed, which is very often "." (see
// pathops.go). repos.Owner compares path prefixes, so a registry built from
// "." would own nothing and every lookup would answer "not in a repo". The
// tree's root is already absolutised by filetree.New, so prefer it and fall
// back to absolutising rootDir in single-file mode.
func (a *App) repoRoot() string {
	if a.tree != nil && a.tree.Root != nil {
		return a.tree.Root.Path
	}
	return absolutePathFor(a.rootDir)
}

// refreshRepos rebuilds the repo registry from the current root.
//
// Called from refreshGitStatus and from refreshTreeNow — that second one is
// the ten-second tick, so a repo cloned into the folder while Vincent is
// open appears without a restart. It is a filesystem walk of at most
// repos.MaxDepth levels, no forks, which is the same order of cost as the
// tree rescan that runs beside it on the same tick.
//
// Single-file mode gets an empty registry on purpose: there is no tree to
// colour, and the one open file wants git to walk UP from its directory,
// which is exactly what an empty registry makes repoFor fall back to.
func (a *App) refreshRepos() {
	if a.tree == nil {
		a.repos = nil
		return
	}
	a.repos = repos.Discover(a.repoRoot())
}

// rootIsRepo reports whether the root directory is itself the only
// repository — the at-home case, and the one that must not change.
func (a *App) rootIsRepo() bool {
	return len(a.repos) == 1 && a.repos[0] == a.repoRoot()
}

// singleRepoMode reports whether git should simply be run in the root, the
// way it was before phase 8a.
//
// Two conditions, one answer. The root IS the repo, or discovery found
// nothing — and that second case covers a root that is a SUBFOLDER of a
// repo, where the right thing is still to let git walk up from the root
// rather than to declare the whole tree untracked.
func (a *App) singleRepoMode() bool {
	return len(a.repos) == 0 || a.rootIsRepo()
}

// repoFor returns the directory a git command about path must run in.
//
// In single-repo mode that is a.rootDir verbatim, which is what every
// caller passed before this file existed. Otherwise it is the repo that
// owns path, or "" when no repo does — and "" is a real answer the callers
// act on: no diff, no gutter markers, and a flash when a diff is asked
// for. A file sitting beside the repos in the root folder is exactly that
// case.
func (a *App) repoFor(path string) string {
	if a.singleRepoMode() {
		return a.rootDir
	}
	return repos.Owner(a.repos, absolutePathFor(path))
}

// gitPathFor returns the directory a git command about path runs in and the
// pathspec to give it.
//
// The pathspec is repo-relative in multi-repo mode because that is the form
// a command run with `-C <repo>` is guaranteed to accept; in single-repo
// mode it stays the absolute path the old callers passed, so the argv is
// unchanged. dir == "" means no repo owns path and the caller must not
// shell out at all.
func (a *App) gitPathFor(path string) (dir, spec string) {
	if a.singleRepoMode() {
		return a.rootDir, path
	}
	abs := absolutePathFor(path)
	owner := repos.Owner(a.repos, abs)
	if owner == "" {
		return "", ""
	}
	return owner, repos.Rel(a.repos, abs)
}

// activeRepo is the repo the three git writes and the footer's branch row
// act on. The precedence is deliberate and is pinned by test:
//
//  1. Single-repo mode — the root, and nothing else to decide.
//  2. The repo owning the ACTIVE TAB's file. What is on screen is what the
//     reviewer thinks they are working in, and a commit going anywhere else
//     is the failure this ordering exists to prevent.
//  3. The repo of the last Changes-panel row they clicked. Sticky, not
//     hover-driven: a pointer drifting over another repo's row must never
//     re-aim an armed commit box.
//  4. The first repo with changes. In a review session that is nearly
//     always the one repo the agent has been writing to.
//  5. The first repo, alphabetically, so the answer is never empty when a
//     repo exists at all.
//
// Returns "" only when there are no repos and no root.
func (a *App) activeRepo() string {
	if a.singleRepoMode() {
		return a.rootDir
	}
	if tab := a.activeTabPtr(); tab != nil && tab.Path != "" {
		if owner := a.repoFor(tab.Path); owner != "" {
			return owner
		}
	}
	if a.gitPanelRepo != "" && repos.Owner(a.repos, a.gitPanelRepo) != "" {
		return a.gitPanelRepo
	}
	for _, snap := range a.gitSnap.Repos {
		if len(snap.Entries) > 0 {
			return snap.Root
		}
	}
	if len(a.repos) > 0 {
		return a.repos[0]
	}
	return ""
}

// activeRepoSnapshot returns the `git status` snapshot for activeRepo().
//
// In single-repo mode that is the whole snapshot, unchanged. In multi-repo
// mode it is the matching member of gitSnap.Repos — matched on Root, which
// is why loadReposSnapshot reports each repo under the path DISCOVERY knew
// it by rather than under git's own resolved toplevel: on macOS those differ
// by /var versus /private/var and a prefix match would silently never hit.
//
// A repo with no entries still has a snapshot, so the footer keeps showing
// its branch when there is nothing to commit.
func (a *App) activeRepoSnapshot() gitSnapshot {
	if len(a.gitSnap.Repos) == 0 {
		return a.gitSnap
	}
	active := a.activeRepo()
	for _, snap := range a.gitSnap.Repos {
		if snap.Root == active {
			return snap
		}
	}
	return gitSnapshot{}
}

// activeRepoName is the repo name the panel footer and the commit box show.
func (a *App) activeRepoName() string {
	if snap := a.activeRepoSnapshot(); snap.RepoName != "" {
		return snap.RepoName
	}
	return filepath.Base(a.repoRoot())
}

// branchLabel is the branch the status bar and the panel footer show: the
// ACTIVE repo's, not the root's.
//
// Single-repo mode returns a.gitBranch, which is the field applyGitSnapshot
// has always stamped and the one the status-bar tests set directly — the
// label is unchanged there in every respect. Multi-repo mode reads it off
// the active repo's snapshot PER FRAME instead of caching it, because it
// changes when the user switches to a tab in another repo, and that is not
// a git event: a cached value would sit wrong for up to ten seconds.
// frame.go's frameKey reads this too, so the switch repaints.
func (a *App) branchLabel() string {
	if len(a.gitSnap.Repos) == 0 {
		return a.gitBranch
	}
	return a.activeRepoSnapshot().Branch
}

// setGitPanelRepo records which repo a clicked Changes row belonged to.
// See activeRepo, rule 3, for why this is a click and not a hover.
func (a *App) setGitPanelRepo(repo string) {
	a.gitPanelRepo = repo
}

// -----------------------------------------------------------------------------
// Status for every repo
// -----------------------------------------------------------------------------

// loadReposSnapshot reads `git status` for every repo under root and merges
// the results into one snapshot.
//
// The single-repo cases short-circuit to plain loadGitSnapshot, which is
// what makes "the root is a repo" and "the root is inside a repo" behave
// exactly as they did before this function existed.
//
// The merged snapshot is a genuine aggregate, not a pick: Entries is every
// repo's entries concatenated, so `Changes (N)` counts the whole folder and
// DirtyFiles() colours a file in any repo. Root is the folder itself and
// Branch is empty, because the folder has neither — the footer asks
// activeRepoSnapshot for those instead.
//
// Runs on the poll worker (gitpoll.go). It touches no App state, which is
// what makes the bounded fan-out below safe.
func loadReposSnapshot(root string, repoPaths []string) gitSnapshot {
	if len(repoPaths) == 0 {
		return loadGitSnapshot(root)
	}
	if len(repoPaths) == 1 && repoPaths[0] == filepath.Clean(root) {
		return loadGitSnapshot(root)
	}

	snaps := loadRepoSnapshotsConcurrently(repoPaths)

	merged := gitSnapshot{
		Root:     filepath.Clean(root),
		RepoName: filepath.Base(filepath.Clean(root)),
		Entries:  []gitEntry{},
		Repos:    []gitSnapshot{},
	}
	for _, snap := range snaps {
		if !snap.IsRepo {
			// A .git that git itself will not accept — a half-cloned
			// directory, a broken worktree pointer. Drop it rather than
			// listing a repo whose every read failed.
			continue
		}
		merged.IsRepo = true
		merged.Repos = append(merged.Repos, snap)
		merged.Entries = append(merged.Entries, snap.Entries...)
	}
	return merged
}

// loadRepoSnapshotsConcurrently reads every repo's status on at most
// repoStatusWorkers goroutines and returns the snapshots in the order the
// paths came in.
//
// Results are written into a pre-sized slice at a fixed index rather than
// appended from the goroutines, so the order is the caller's and there is
// no shared mutable state beyond one slot per worker.
func loadRepoSnapshotsConcurrently(repoPaths []string) []gitSnapshot {
	out := make([]gitSnapshot, len(repoPaths))
	sem := make(chan struct{}, repoStatusWorkers)
	var wg sync.WaitGroup
	for i, path := range repoPaths {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = loadSnapshotAs(path, path)
		}(i, path)
	}
	wg.Wait()
	return out
}

// repoBranchMap is the folder-path -> branch map the file tree decorates
// repo folders from. Empty in single-repo mode, where the panel footer
// already names the one branch and repeating it on the root row would be
// noise.
func (a *App) repoBranchMap() map[string]string {
	if len(a.gitSnap.Repos) == 0 {
		return nil
	}
	out := make(map[string]string, len(a.gitSnap.Repos))
	for _, snap := range a.gitSnap.Repos {
		if snap.Branch != "" {
			out[snap.Root] = snap.Branch
		}
	}
	return out
}

// dirtyTabCountIn counts unsaved tabs inside repo.
//
// The commit and checkout guards use this rather than dirtyTabCount
// because `git add -A` in alpha cannot stage anything in beta, and a dirty
// buffer in an unrelated repo blocking the commit is a refusal the user
// cannot act on without closing work they still want.
//
// In single-repo mode repoFor answers a.rootDir for EVERY path, including
// one outside the root, so this counts exactly what dirtyTabCount counted
// before — untitled buffers included. In multi-repo mode an untitled
// buffer belongs to no repo and blocks nothing, which is right: there is
// no file on disk for `add -A` to stage the wrong version of.
func (a *App) dirtyTabCountIn(repo string) int {
	n := 0
	for _, tab := range a.tabs {
		if tab == nil || !tab.Dirty {
			continue
		}
		if a.repoFor(tab.Path) == repo {
			n++
		}
	}
	return n
}
