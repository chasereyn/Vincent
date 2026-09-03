// =============================================================================
// File: internal/finder/index_test.go
// Author: Spicer Matthews <spicer@cloudmanic.com>
// Created: 2026-04-30
// Copyright: 2026 Cloudmanic, LLC. All rights reserved.
// =============================================================================

package finder

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

// TestBuildIndex_FallbackWalk pins the non-git path: a plain
// directory tree (no .git, no .gitignore) gets walked and every
// regular file shows up, sorted, with hardcoded ignores honoured.
func TestBuildIndex_FallbackWalk(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "main.go", "package main")
	mustWrite(t, dir, "internal/app/app.go", "package app")
	mustWrite(t, dir, "node_modules/react/index.js", "// vendored")
	mustWrite(t, dir, ".DS_Store", "junk")

	paths, viaGit, err := BuildIndex(dir)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if viaGit {
		t.Fatal("expected fallback path for non-git tempdir, got git")
	}
	want := []string{"internal/app/app.go", "main.go"}
	if !sliceEqual(paths, want) {
		t.Fatalf("paths: got %v, want %v", paths, want)
	}
}

// TestBuildIndex_FallbackHonoursGitignore is the .gitignore-in-
// fallback contract: even without git installed, a project root
// .gitignore should still mask out files. Without this the user
// would get build artefacts and secret env files spamming results
// in any non-git workspace.
func TestBuildIndex_FallbackHonoursGitignore(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "main.go", "package main")
	mustWrite(t, dir, "secret.env", "KEY=...")
	mustWrite(t, dir, "build/output.bin", "x")
	mustWrite(t, dir, ".gitignore", "*.env\nbuild/\n")

	paths, _, err := BuildIndex(dir)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	want := []string{".gitignore", "main.go"}
	if !sliceEqual(paths, want) {
		t.Fatalf("paths: got %v, want %v", paths, want)
	}
}

// TestBuildIndex_GitFastPath confirms the fast path runs when the
// directory is a real git repo. We `git init` the tempdir, drop
// in two tracked-ish files, and assert the index reports `viaGit`
// true and contains both entries. If git is missing on the host
// the test skips — CI with git installed always exercises this.
func TestBuildIndex_GitFastPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	mustWrite(t, dir, "main.go", "package main")
	mustWrite(t, dir, "README.md", "# hi")
	mustWrite(t, dir, ".gitignore", "ignored.txt\n")
	mustWrite(t, dir, "ignored.txt", "should not appear")

	paths, viaGit, err := BuildIndex(dir)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if !viaGit {
		t.Fatal("expected git fast path for git repo, got fallback")
	}
	want := []string{".gitignore", "README.md", "main.go"}
	if !sliceEqual(paths, want) {
		t.Fatalf("paths: got %v, want %v", paths, want)
	}
}

// TestBuildIndex_RootIsRepoUnchanged pins the Phase 8b contract that when
// rootDir is itself a git repo, BuildIndex takes the exact single-repo
// path it always did — even when a NESTED directory also has its own
// .git (a submodule or vendored clone). repos.Discover folds that into
// the single outer repo, so the git fast path should see and report the
// nested repo's tracked files exactly as `git ls-files` from the outer
// root would (a submodule's checked-out files show up as ordinary paths
// unless it's genuinely unregistered, which this test doesn't need to
// resolve — the point is BuildIndex never treats it as a second root to
// prefix separately).
func TestBuildIndex_RootIsRepoUnchanged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	mustWrite(t, dir, "main.go", "package main")
	nested := filepath.Join(dir, "vendor", "sub")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if out, err := exec.Command("git", "-C", nested, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init nested: %v: %s", err, out)
	}

	paths, viaGit, err := BuildIndex(dir)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if !viaGit {
		t.Fatal("expected git fast path for a repo root")
	}
	// Byte-identical to calling buildIndexGit(dir) directly: no
	// "vendor/sub/..." prefix rewriting, because Discover(dir) reports
	// only dir itself as a repo.
	want, _ := buildIndexGit(dir)
	if !sliceEqual(paths, want) {
		t.Fatalf("paths: got %v, want %v (buildIndexGit directly)", paths, want)
	}
}

// TestBuildIndex_MultiRepoRoot is the Phase 8b case: rootDir is a plain
// folder holding two git repos plus a loose file that belongs to neither.
// Each repo's files should show up prefixed with the repo's own folder
// name, and the loose file should show up unprefixed via the leftover
// walk — all merged into one sorted slice.
func TestBuildIndex_MultiRepoRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	for _, repo := range []string{"repoA", "repoB"} {
		repoDir := filepath.Join(dir, repo)
		if err := os.MkdirAll(repoDir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", repo, err)
		}
		if out, err := exec.Command("git", "-C", repoDir, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init %s: %v: %s", repo, err, out)
		}
		mustWrite(t, repoDir, "main.go", "package main")
	}
	mustWrite(t, dir, "notes.txt", "loose file, no repo owns this")

	paths, viaGit, err := BuildIndex(dir)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if !viaGit {
		t.Fatal("expected at least one repo to use the git fast path")
	}
	want := []string{"notes.txt", "repoA/main.go", "repoB/main.go"}
	if !sliceEqual(paths, want) {
		t.Fatalf("paths: got %v, want %v", paths, want)
	}
}

// TestBuildIndex_MultiRepoSkipsRepoDirsInWalk guards against the easy bug
// in the multi-root path: the leftover walk must prune each discovered
// repo's directory entirely rather than descending into it, or a repo's
// files would appear twice — once correctly prefixed by the git-fast-path
// build, once again unprefixed (or wrongly prefixed) by the walk.
func TestBuildIndex_MultiRepoSkipsRepoDirsInWalk(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if out, err := exec.Command("git", "-C", repoDir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	mustWrite(t, repoDir, "main.go", "package main")

	paths, _, err := BuildIndex(dir)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	want := []string{"repo/main.go"}
	if !sliceEqual(paths, want) {
		t.Fatalf("paths: got %v, want %v (repo/main.go should appear exactly once)", paths, want)
	}
}

// TestBuildIndex_ManyReposConcurrent exercises the bounded worker pool
// with more repos than maxConcurrentRepoIndexBuilds, so the semaphore
// actually has to queue work rather than hand every repo its own
// goroutine slot immediately. Mostly a deadlock/race guard — go test
// -race is what would actually catch a bug here — plus a correctness
// check that every repo's file made it into the merged result.
func TestBuildIndex_ManyReposConcurrent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	const n = 20
	want := make([]string, 0, n)
	for i := 0; i < n; i++ {
		name := "repo" + itoaForTest(i)
		repoDir := filepath.Join(dir, name)
		if err := os.MkdirAll(repoDir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if out, err := exec.Command("git", "-C", repoDir, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init %s: %v: %s", name, err, out)
		}
		mustWrite(t, repoDir, "file.go", "package main")
		want = append(want, name+"/file.go")
	}

	paths, _, err := BuildIndex(dir)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if !sliceEqual(paths, want) {
		t.Fatalf("paths: got %v, want %v", paths, want)
	}
}

// itoaForTest is a tiny local int->string helper so
// TestBuildIndex_ManyReposConcurrent doesn't need to import strconv just
// to build twenty directory names.
func itoaForTest(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestBuildIndex_EmptyRootRejected is a small contract guard:
// passing "" should return a useful error, not silently scan the
// caller's CWD. Otherwise a bug in the caller could blast every
// file under their home directory into the finder.
func TestBuildIndex_EmptyRootRejected(t *testing.T) {
	if _, _, err := BuildIndex(""); err == nil {
		t.Fatal("expected error for empty rootDir")
	}
}

// mustWrite is a tiny test helper that creates parent directories
// and writes content to a file under root. Pulled out so each test
// reads as the scenario it's pinning down rather than mkdir+write
// boilerplate.
func mustWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(body), 0644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// sliceEqual checks two string slices for deep equality. Pulled
// into the test file so the assertion in each test reads as the
// rule it's pinning down ("got these paths, want these paths")
// instead of a reflect.DeepEqual call.
func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}
