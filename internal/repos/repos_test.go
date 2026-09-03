// =============================================================================
// File: internal/repos/repos_test.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package repos

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// mkdirs creates every listed directory under root, failing the test on
// error. Paths use forward slashes and are joined per platform.
func mkdirs(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDiscover_RootIsRepoReturnsOnlyRoot pins the single-repo case: a
// personal project opened at its own root must behave exactly as before
// phase 8, so nested .git folders inside it are ignored.
func TestDiscover_RootIsRepoReturnsOnlyRoot(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, ".git", "vendor/dep/.git", "sub/.git")
	got := Discover(root)
	want := []string{filepath.Clean(root)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Discover = %v, want %v", got, want)
	}
}

// TestDiscover_FolderOfReposFindsEachOnce is the RP-Repos case: a flat
// folder of repos, one level down, sorted, and nothing found inside a
// repo once it is found.
func TestDiscover_FolderOfReposFindsEachOnce(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "beta/.git", "alpha/.git", "alpha/inner/.git", "notes", "node_modules/pkg/.git")
	got := Discover(root)
	want := []string{filepath.Join(root, "alpha"), filepath.Join(root, "beta")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Discover = %v, want %v", got, want)
	}
}

// TestDiscover_TwoLevelsAndNoDeeper checks the depth limit: a repo under
// root/group/repo is found, root/a/b/repo is not.
func TestDiscover_TwoLevelsAndNoDeeper(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "group/repo/.git", "a/b/deep/.git")
	got := Discover(root)
	want := []string{filepath.Join(root, "group", "repo")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Discover = %v, want %v", got, want)
	}
}

// TestDiscover_NoReposIsEmptyNotNil keeps callers from having to nil-check.
func TestDiscover_NoReposIsEmptyNotNil(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "docs", "src")
	got := Discover(root)
	if got == nil || len(got) != 0 {
		t.Fatalf("Discover = %#v, want empty non-nil", got)
	}
}

// TestDiscover_WorktreeGitFileCounts covers a linked worktree, whose .git
// is a file rather than a directory.
func TestDiscover_WorktreeGitFileCounts(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "wt")
	if err := os.WriteFile(filepath.Join(root, "wt", ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Discover(root); len(got) != 1 || got[0] != filepath.Join(root, "wt") {
		t.Fatalf("Discover = %v, want the worktree", got)
	}
}

// TestOwnerAndRel pin prefix matching: the longest repo wins, a sibling
// with a shared name prefix does not match, and Rel uses forward slashes.
func TestOwnerAndRel(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "alpha")
	ab := filepath.Join(root, "alphabet")
	repos := []string{a, ab}

	file := filepath.Join(ab, "src", "x.go")
	if got := Owner(repos, file); got != ab {
		t.Fatalf("Owner = %q, want %q (prefix must respect the separator)", got, ab)
	}
	if got := Rel(repos, file); got != "src/x.go" {
		t.Fatalf("Rel = %q, want src/x.go", got)
	}
	if got := Owner(repos, a); got != a {
		t.Fatalf("Owner of a repo root = %q, want itself", got)
	}
	if got := Owner(repos, filepath.Join(root, "other", "f")); got != "" {
		t.Fatalf("Owner outside every repo = %q, want empty", got)
	}
	if got := Rel(repos, filepath.Join(root, "other", "f")); got != "" {
		t.Fatalf("Rel outside every repo = %q, want empty", got)
	}
}
