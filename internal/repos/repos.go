// =============================================================================
// File: internal/repos/repos.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// No upstream equivalent. spice-edit assumed one root, one repo.
// =============================================================================

// Package repos finds the git repositories under a root folder and says
// which one owns a path. It exists because Vincent's root is not always a
// repo: at work the root is a flat folder of company repos, and every git
// operation — status, diff, commit, push, checkout — has to run in the
// repo that owns the file rather than in the root. Pure: no tcell, no
// shelling out, just the filesystem.
package repos

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxDepth is how many folder levels below the root Discover looks for a
// .git entry. Two covers "root/repo" and "root/group/repo"; deeper trees
// are a workspace layout Vincent does not need to understand, and every
// extra level multiplies the startup stat cost on a big folder.
const MaxDepth = 2

// skipDirs are folders never descended into. They are the same names the
// finder's walk fallback skips, for the same reason: they are huge, they
// are generated, and nothing in them is a repo you want to review.
var skipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
	"build":        true,
	".git":         true,
}

// Discover returns the absolute paths of every git repository at or under
// root, sorted. If root itself is a repository the answer is just root:
// anything nested inside it is a submodule or a vendored clone, and git
// already treats those as part of the outer repo. Otherwise it walks
// MaxDepth levels looking for a .git directory or file (a worktree's .git
// is a file) and does not descend into a repo once found. A root with no
// repos anywhere returns an empty, non-nil slice.
func Discover(root string) []string {
	root = filepath.Clean(root)
	if IsRepo(root) {
		return []string{root}
	}
	var found []string
	walk(root, 1, &found)
	sort.Strings(found)
	if found == nil {
		found = []string{}
	}
	return found
}

// walk is Discover's recursive step. depth is the level of dir below the
// root, starting at 1 for the root's own children.
func walk(dir string, depth int, found *[]string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || skipDirs[e.Name()] {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if IsRepo(full) {
			*found = append(*found, full)
			continue
		}
		if depth < MaxDepth {
			walk(full, depth+1, found)
		}
	}
}

// IsRepo reports whether dir has a .git entry of its own. A directory is
// the normal case; a file is a linked worktree, whose .git holds a
// "gitdir:" pointer, and git commands run fine from either.
func IsRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// Owner returns the repo in repos that contains path, or "" when none
// does. repos and path must both be absolute and cleaned. The longest
// matching prefix wins so that, should a repo ever sit inside another in
// the list, the inner one claims its own files. A path equal to a repo
// root is owned by that repo.
func Owner(repos []string, path string) string {
	path = filepath.Clean(path)
	best := ""
	for _, r := range repos {
		if path == r || strings.HasPrefix(path, r+string(filepath.Separator)) {
			if len(r) > len(best) {
				best = r
			}
		}
	}
	return best
}

// Rel returns path relative to its owning repo, with forward slashes, or
// the empty string when no repo owns it. This is the form a git command
// wants and the form a review note should carry, because the agent that
// receives the note has that repo, not Vincent's root, as its working
// directory.
func Rel(repos []string, path string) string {
	owner := Owner(repos, path)
	if owner == "" {
		return ""
	}
	rel, err := filepath.Rel(owner, path)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}
