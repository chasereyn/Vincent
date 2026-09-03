// =============================================================================
// File: internal/app/gitstatus_test.go
// Author: Spicer Matthews <spicer@cloudmanic.com>
// Created: 2026-04-30
// Copyright: 2026 Cloudmanic, LLC. All rights reserved.
// =============================================================================

// Tests for gitstatus.go. The pure helpers (dirtyFolderSet, pathInside)
// are exercised in isolation
// with synthetic input — no subprocess needed. The shell-out flow
// (loadGitStatus end-to-end) is exercised against a real `git init`'d
// repo in a t.TempDir, and skipped when git isn't on PATH so the test
// suite still runs in a stripped-down container.

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/chasereyn/vincent/internal/editor"
	"github.com/chasereyn/vincent/internal/filetree"
)

// TestLoadGitLineChanges_IncludesStagedChanges compares the worktree with HEAD,
// so staging a file does not remove its gutter markers.
func TestLoadGitLineChanges_IncludesStagedChanges(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	path := filepath.Join(repo, "a.txt")
	writeFileT(t, path, "one\ntwo\nthree\n")
	gitRun(t, repo, "add", "a.txt")
	gitRun(t, repo, "commit", "-m", "init")
	writeFileT(t, path, "one\nchanged\nthree\nfour\n")
	gitRun(t, repo, "add", "a.txt")

	changes := loadGitLineChanges(repo, "a.txt")
	if len(changes) == 0 {
		t.Fatal("staged changes should produce gutter markers")
	}
	if got := changes[1]; got != editor.GitLineModified {
		t.Fatalf("line 2 marker = %v, want modified", got)
	}
}

// TestLoadGitBranch_NotARepo confirms the helper degrades quietly when
// the directory isn't a git work tree — empty string, no panic, no
// stderr noise reaching the editor.
func TestLoadGitBranch_NotARepo(t *testing.T) {
	if got := loadGitBranch(t.TempDir()); got != "" {
		t.Fatalf("non-repo branch = %q, want empty", got)
	}
	if got := loadGitBranch(""); got != "" {
		t.Fatalf("empty rootDir branch = %q, want empty", got)
	}
}

// TestLoadGitBranch_OnBranch checks the happy path — a fresh repo
// checked out on `main` returns "main".
func TestLoadGitBranch_OnBranch(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	if got := loadGitBranch(repo); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
}

// TestLoadGitBranch_TracksRename confirms a rename of the current
// branch is reflected on the next call — this is the whole point of
// the 10s tick: the user's checkout state is allowed to change behind
// the editor's back.
func TestLoadGitBranch_TracksRename(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	writeFileT(t, filepath.Join(repo, "a.txt"), "x")
	gitRun(t, repo, "add", "a.txt")
	gitRun(t, repo, "commit", "-m", "init")
	gitRun(t, repo, "branch", "-m", "main", "feat/something")
	if got := loadGitBranch(repo); got != "feat/something" {
		t.Fatalf("after rename branch = %q, want feat/something", got)
	}
}

// TestLoadGitBranch_DetachedHEAD asserts the symbolic-ref fallback
// kicks in: when HEAD is detached at a commit, the helper returns a
// short SHA instead of an empty string, so the status bar still shows
// *something* useful instead of vanishing mid-rebase.
func TestLoadGitBranch_DetachedHEAD(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	writeFileT(t, filepath.Join(repo, "a.txt"), "x")
	gitRun(t, repo, "add", "a.txt")
	gitRun(t, repo, "commit", "-m", "init")
	gitRun(t, repo, "checkout", "-q", "--detach", "HEAD")

	got := loadGitBranch(repo)
	if got == "" {
		t.Fatal("detached HEAD branch came back empty; expected a short SHA")
	}
	if got == "main" {
		t.Fatalf("detached HEAD reported branch name %q; expected SHA", got)
	}
	if len(got) > 12 || len(got) < 4 {
		t.Fatalf("detached HEAD output %q doesn't look like a short SHA", got)
	}
}

// TestParseGitDiffLines maps unified hunk ranges to zero-based gutter rows.
func TestParseGitDiffLines(t *testing.T) {
	diff := []byte("@@ -2,0 +3,2 @@\n+a\n+b\n@@ -8,2 +10,2 @@\n-old\n+new\n@@ -20,2 +21,0 @@\n-old\n")
	got := parseGitDiffLines(diff)
	if got[2] != editor.GitLineAdded || got[3] != editor.GitLineAdded {
		t.Fatalf("added markers wrong: %v", got)
	}
	if got[9] != editor.GitLineModified || got[10] != editor.GitLineModified {
		t.Fatalf("modified markers wrong: %v", got)
	}
	if got[21] != editor.GitLineDeleted {
		t.Fatalf("deleted marker wrong: %v", got)
	}
}

// TestDirtyFolderSet_RollsUpToRoot verifies that each dirty file paints
// every ancestor folder up to (and including) the project root, so a
// collapsed branch still shows the user there's a change inside.
func TestDirtyFolderSet_RollsUpToRoot(t *testing.T) {
	// Paths are joined rather than written as POSIX literals so the walk
	// is exercised with the platform's own separator.
	root := filepath.Join(string(filepath.Separator) + "proj")
	leaf := filepath.Join(root, "a", "b", "c", "leaf.txt")
	dirty := map[string]filetree.GitChangeKind{
		leaf:                              filetree.GitChangeModified,
		filepath.Join(root, "x", "y.txt"): filetree.GitChangeModified,
	}
	got := dirtyFolderSet(dirty, root)

	want := []string{
		root,
		filepath.Join(root, "a"),
		filepath.Join(root, "a", "b"),
		filepath.Join(root, "a", "b", "c"),
		filepath.Join(root, "x"),
	}
	for _, w := range want {
		if got[w] == filetree.GitChangeNone {
			t.Errorf("expected %q to be marked dirty; got %v", w, sortedKeys(got))
		}
	}
	// The leaf file path itself isn't a folder, must not appear here.
	if got[leaf] != filetree.GitChangeNone {
		t.Error("dirtyFolderSet should not contain file paths")
	}
}

// TestDirtyFolderSet_StopsAtRoot proves the walk stops at root rather
// than continuing all the way to "/", so a sibling project directory
// or the user's home directory can't be marked dirty by us.
func TestDirtyFolderSet_StopsAtRoot(t *testing.T) {
	sep := string(filepath.Separator)
	parent := filepath.Join(sep + "proj")
	root := filepath.Join(parent, "inner")
	dirty := map[string]filetree.GitChangeKind{
		filepath.Join(root, "a", "b.txt"): filetree.GitChangeModified,
	}
	got := dirtyFolderSet(dirty, root)
	for _, ancestor := range []string{parent, sep, filepath.Join(sep + "home")} {
		if got[ancestor] != filetree.GitChangeNone {
			t.Errorf("walk escaped root: %q should not be marked", ancestor)
		}
	}
	if got[root] == filetree.GitChangeNone {
		t.Error("root itself should be marked when something inside is dirty")
	}
	if got[filepath.Join(root, "a")] == filetree.GitChangeNone {
		t.Error("intermediate folder should be marked")
	}
}

// TestDirtyFolderSet_EmptyInput returns an empty (non-nil) map so
// callers can safely range over the result without nil-checking.
func TestDirtyFolderSet_EmptyInput(t *testing.T) {
	got := dirtyFolderSet(nil, "/anywhere")
	if got == nil {
		t.Fatal("expected non-nil empty map")
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}

// TestRebaseGitPaths_NormalizesTreeRootCasing keeps git and filetree path keys
// aligned on case-insensitive filesystems where cwd casing may drift.
func TestRebaseGitPaths_NormalizesTreeRootCasing(t *testing.T) {
	// Paths are built with filepath.Join rather than POSIX literals:
	// rebaseGitPaths rejoins with the platform separator, so hardcoded
	// "/a/b" literals fail on Windows even though the behaviour under
	// test is correct.
	sep := string(filepath.Separator)
	upper := filepath.Join(sep+"Users", "fatih", "Documents", "Projeler", "vincent")
	lower := filepath.Join(sep+"Users", "fatih", "documents", "projeler", "vincent")
	dirty := map[string]filetree.GitChangeKind{
		filepath.Join(upper, "internal", "app", "app.go"): filetree.GitChangeModified,
	}
	rebased := rebaseGitPaths(dirty, lower)
	want := filepath.Join(lower, "internal", "app", "app.go")
	if rebased[want] != filetree.GitChangeModified {
		t.Fatalf("rebased path missing: got %v want key %q", rebased, want)
	}
}

// TestRebaseGitPaths_DoesNotMoveRepoPathsUnderSubdirRoot protects launches
// rooted at a subdirectory: only descendants of that tree root are rebased.
func TestRebaseGitPaths_DoesNotMoveRepoPathsUnderSubdirRoot(t *testing.T) {
	// See the note in TestRebaseGitPaths_NormalizesTreeRootCasing on why
	// these are joined rather than written as POSIX literals.
	sep := string(filepath.Separator)
	repo := filepath.Join(sep+"repo", "internal")
	inside := filepath.Join(repo, "app", "app.go")
	outside := filepath.Join(repo, "editor", "tab.go")
	dirty := map[string]filetree.GitChangeKind{
		inside:  filetree.GitChangeModified,
		outside: filetree.GitChangeModified,
	}
	rebased := rebaseGitPaths(dirty, filepath.Join(repo, "app"))
	if rebased[inside] != filetree.GitChangeModified {
		t.Fatalf("descendant path should stay under subdir root, got %v", rebased)
	}
	if rebased[outside] != filetree.GitChangeModified {
		t.Fatalf("outside path should remain unchanged, got %v", rebased)
	}
}

// TestPathInside covers the core ancestry check used by dirtyFolderSet.
// Beyond the obvious matches, the prefix-trick trap ("/foo/bar" is NOT
// inside "/foo/ba") is the regression we care most about.
func TestPathInside(t *testing.T) {
	cases := []struct {
		candidate, root string
		want            bool
	}{
		{"/foo", "/foo", true},
		{"/foo/bar", "/foo", true},
		{"/foo/bar/baz", "/foo", true},
		{"/foo/ba", "/foo/bar", false},
		{"/foo/bar", "/foo/ba", false}, // string-prefix would lie here
		{"/sibling", "/foo", false},
		{"/", "/foo", false},
	}
	for _, tc := range cases {
		if got := pathInside(tc.candidate, tc.root); got != tc.want {
			t.Errorf("pathInside(%q, %q) = %v, want %v", tc.candidate, tc.root, got, tc.want)
		}
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// requireGit skips the calling test when git isn't on PATH. The encoding
// helpers don't need it; only the end-to-end flow does.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
}

// initRepo creates a fresh git repo in t.TempDir and configures a local
// committer identity so commits in the test don't depend on the host's
// global git config.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	initRepoAt(t, dir)
	// On macOS the temp dir lives under /var, which is a symlink to
	// /private/var. git resolves the real path; rev-parse --show-toplevel
	// will report /private/var/... — tests use the same dir variable so
	// they compare the *resolved* path to itself. Force resolution here.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}
	return resolved
}

// initRepoAt is initRepo's body for a directory the caller already made —
// which is what the multi-repo fixtures need, since they build several
// repos inside ONE t.TempDir rather than one repo per temp dir. It does no
// symlink resolution: the caller resolved the parent once and every path
// below it is already physical.
func initRepoAt(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "Test User")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	// macOS 'git init' may print a default-branch hint; force a stable name
	// so the tests work the same on every host.
	gitRun(t, dir, "checkout", "-q", "-b", "main")
}

// gitRun invokes git in cwd. Fails the test on non-zero exit so a broken
// fixture doesn't masquerade as a code bug.
func gitRun(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, cwd, err, out)
	}
}

// writeFileT writes content to path with sensible perms, failing the test
// on any IO error. (Named writeFileT to avoid colliding with the helper
// of the same name in modals_test.go.)
func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// sortedKeys returns the keys of m in lexicographic order — handy when
// printing diff context inside test failures.
func sortedKeys[K comparable](m map[string]K) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
