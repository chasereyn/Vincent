// =============================================================================
// File: internal/finder/index.go
// Author: Spicer Matthews <spicer@cloudmanic.com>
// Created: 2026-04-30
// Copyright: 2026 Cloudmanic, LLC. All rights reserved.
// =============================================================================

package finder

// Index building. Two strategies per repo, in priority order:
//
//  1. Git fast path. If the project is a git repo, shell out to
//     `git ls-files --cached --others --exclude-standard -z`. This
//     gives us every tracked or untracked-but-not-ignored file in
//     a single fork — git already has the index in memory, and
//     even on a 100k-file repo this returns in well under a
//     second. Honours .gitignore for free.
//
//  2. Manual walk + gitignore. For non-git projects (or when git
//     itself is missing) we walk the filesystem with filepath.Walk
//     and filter through go-gitignore plus a small hardcoded
//     ignore set so dot-dirs and node_modules don't blow up the
//     result count.
//
// Vincent's root is not always a repo (Phase 8, internal/repos): at work
// the root is a flat folder of company repos, and the old single-strategy
// BuildIndex would fall through to strategy 2 over the WHOLE root, which
// walks every repo's build output and vendored dependencies by hand
// instead of asking git to skip them. BuildIndex now uses repos.Discover
// to find every repo at or under the root, indexes each one with its own
// git-fast-path-or-walk (run concurrently, bounded), prefixes each repo's
// paths with its folder relative to the root, and walks only whatever is
// left over — the part of the root that isn't inside any repo. When the
// root IS a repo (the common single-project case), Discover reports just
// the root itself and BuildIndex takes the old single-repo path byte for
// byte; see TestBuildIndex_RootIsRepoUnchanged.
//
// Every path returned is root-relative with forward slashes regardless of
// host OS, so the scorer and renderer can treat the strings uniformly.

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	gitignore "github.com/sabhiram/go-gitignore"

	"github.com/chasereyn/vincent/internal/repos"
)

// maxConcurrentRepoIndexBuilds bounds how many `git ls-files` (or walk)
// index builds run at once when the root is a folder of repos. A work
// root can hold dozens of repos; forking git for all of them at once
// would spike the process table and starve the one the user is actually
// looking at. Eight matches the search engine's own worker cap (Phase 8b)
// for the same reason: past a handful of concurrent forks/reads, more
// concurrency just adds scheduling overhead on typical hardware.
const maxConcurrentRepoIndexBuilds = 8

// hardcodedIgnores is the floor we apply in the *fallback* path —
// non-git projects that don't have a .gitignore at all still don't
// want to surface .DS_Store or every file under node_modules. The
// git fast path inherits these via core.excludesFile / global git
// configuration so we don't need to apply them there.
//
// Each entry is a directory or file basename (no globs) — we drop
// any path that has a segment matching one of these. Cheap to
// check against millions of paths, sufficient for the 90% case.
var hardcodedIgnores = map[string]struct{}{
	".git":         {},
	".hg":          {},
	".svn":         {},
	"node_modules": {},
	"vendor":       {},
	"__pycache__":  {},
	".venv":        {},
	"dist":         {},
	"build":        {},
	".next":        {},
	".cache":       {},
	".DS_Store":    {},
}

// maxIndexEntries caps the total number of paths the index will
// hold. 200k is enough for an enormous monorepo while still
// fitting comfortably in memory (each path averages ~50 bytes →
// 10MB total). Past this point we silently truncate; the user is
// almost certainly looking at a vendored dependency dump and
// would rather see *something* than wait minutes for a complete
// index.
const maxIndexEntries = 200_000

// BuildIndex returns a sorted slice of rootDir-relative file paths,
// honouring gitignore rules. When rootDir is itself a git repo this is
// exactly the old single-repo behaviour: git fast path, falling back to a
// manual walk on any failure. When rootDir is a folder containing one or
// more repos (or none at all), it indexes each discovered repo separately
// and prefixes their paths with the repo's folder relative to rootDir,
// then walks whatever part of rootDir isn't inside any repo. The boolean
// reports whether at least one git fast path ran — handy for tests that
// want to assert git was actually used rather than the fallback walk.
func BuildIndex(rootDir string) ([]string, bool, error) {
	if rootDir == "" {
		return nil, false, errors.New("finder: empty rootDir")
	}
	rootDir = filepath.Clean(rootDir)
	repoDirs := repos.Discover(rootDir)
	if len(repoDirs) == 1 && repoDirs[0] == rootDir {
		// rootDir is itself a repo: nothing nested in it needs separate
		// treatment (Discover already folded any submodule/nested clone
		// into this single entry), so this is byte-identical to the
		// pre-Phase-8 single-repo code path.
		if paths, err := buildIndexGit(rootDir); err == nil {
			return paths, true, nil
		}
		paths, err := buildIndexWalk(rootDir)
		return paths, false, err
	}
	return buildIndexMultiRoot(rootDir, repoDirs)
}

// repoIndexResult is one repo's contribution to a multi-root build,
// collected by index so the concurrent builders below can write into a
// pre-sized slice without a lock.
type repoIndexResult struct {
	paths  []string
	viaGit bool
}

// buildIndexMultiRoot builds the index for a root that is not itself a
// repo: one goroutine per discovered repo (bounded by
// maxConcurrentRepoIndexBuilds), each indexing its own repo and prefixing
// the result with the repo's rootDir-relative folder, plus a single walk
// of rootDir that skips descending into any of those repo folders (using
// the same skip-list walk BuildIndex always used, so a repo's own build
// output never gets indexed twice under two different prefixes).
func buildIndexMultiRoot(rootDir string, repoDirs []string) ([]string, bool, error) {
	results := make([]repoIndexResult, len(repoDirs))
	if len(repoDirs) > 0 {
		sem := make(chan struct{}, maxConcurrentRepoIndexBuilds)
		var wg sync.WaitGroup
		for i, repoDir := range repoDirs {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, repoDir string) {
				defer wg.Done()
				defer func() { <-sem }()
				results[i] = buildRepoIndexEntry(rootDir, repoDir)
			}(i, repoDir)
		}
		wg.Wait()
	}

	exclude := make(map[string]bool, len(repoDirs))
	for _, r := range repoDirs {
		exclude[r] = true
	}
	unowned, err := buildIndexWalkExcluding(rootDir, exclude)
	if err != nil {
		return nil, false, err
	}

	anyGit := false
	all := make([]string, 0, len(unowned))
	for _, r := range results {
		all = append(all, r.paths...)
		if r.viaGit {
			anyGit = true
		}
	}
	all = append(all, unowned...)
	sort.Strings(all)
	if len(all) > maxIndexEntries {
		all = all[:maxIndexEntries]
	}
	return all, anyGit, nil
}

// buildRepoIndexEntry indexes one repo (git fast path, falling back to a
// walk) and rewrites every path to be relative to rootDir instead of the
// repo itself, so a repo two levels down still reports paths the rest of
// the index can join with the root-relative results from other repos and
// from the leftover walk. Runs on a worker goroutine — no shared state
// beyond the slice slot the caller already allocated for it.
func buildRepoIndexEntry(rootDir, repoDir string) repoIndexResult {
	paths, viaGit, err := func() ([]string, bool, error) {
		if p, err := buildIndexGit(repoDir); err == nil {
			return p, true, nil
		}
		p, err := buildIndexWalk(repoDir)
		return p, false, err
	}()
	if err != nil {
		// A repo whose index we genuinely can't build (permission error,
		// git AND the walk both failing) contributes nothing rather than
		// aborting the whole multi-root build over one bad repo.
		return repoIndexResult{}
	}
	prefix, relErr := filepath.Rel(rootDir, repoDir)
	if relErr != nil {
		prefix = repoDir
	}
	prefix = filepath.ToSlash(prefix)
	prefixed := make([]string, len(paths))
	for i, p := range paths {
		prefixed[i] = prefix + "/" + p
	}
	return repoIndexResult{paths: prefixed, viaGit: viaGit}
}

// buildIndexGit shells out to `git ls-files` to collect every
// tracked + untracked-not-ignored file under rootDir. -z makes git
// emit null-terminated names so paths with spaces / quotes / even
// newlines round-trip correctly. Returns an error when the
// directory isn't a git working tree, the binary is missing, or
// the command fails for any other reason — the caller falls back
// to the manual walk path.
func buildIndexGit(rootDir string) ([]string, error) {
	cmd := exec.Command("git", "-C", rootDir,
		"ls-files",
		"--cached",
		"--others",
		"--exclude-standard",
		"-z",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	// Split on \0; trim the trailing empty entry git always writes.
	parts := bytes.Split(out, []byte{0})
	paths := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		s := string(p)
		// git ls-files already emits forward-slash paths even on
		// Windows, so we don't need to translate. Sort happens at
		// the end so we don't have to maintain order here.
		paths = append(paths, s)
		if len(paths) >= maxIndexEntries {
			break
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// buildIndexWalk is the non-git fallback: filepath.WalkDir from
// rootDir, applying hardcodedIgnores and any .gitignore files we
// find along the way. Slower than the git path but works for plain
// directories and projects where git isn't installed.
//
// We compile a single combined ignorer at the project root rather
// than walking nested .gitignore files. That trades a bit of
// fidelity (a deep gitignore line affecting only its subtree
// won't apply outside it) for simplicity — the project-root
// .gitignore covers >95% of real cases, and a non-git project
// usually only has one .gitignore anyway.
func buildIndexWalk(rootDir string) ([]string, error) {
	return buildIndexWalkExcluding(rootDir, nil)
}

// buildIndexWalkExcluding is buildIndexWalk plus one more skip rule: any
// directory whose absolute path is a key in exclude (nil is fine — the
// zero value of a nil map always reports "not found") is pruned entirely,
// the same way hardcodedIgnores prunes node_modules. This is what lets
// buildIndexMultiRoot walk "the part of the root that isn't inside any
// repo" without re-walking (and re-indexing under a different path) a
// repo that already got its own git-fast-path build.
func buildIndexWalkExcluding(rootDir string, exclude map[string]bool) ([]string, error) {
	ig := loadProjectGitignore(rootDir)
	paths := make([]string, 0, 4096)

	err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A permission error on one subtree shouldn't kill the
			// whole index. Skip the offending dir and move on.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == rootDir {
			return nil
		}
		if d.IsDir() && exclude[path] {
			return fs.SkipDir
		}
		base := d.Name()
		if _, hit := hardcodedIgnores[base]; hit {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(rootDir, path)
		if err != nil {
			return nil
		}
		// Normalise to forward slashes so the scorer and renderer
		// don't have to care about the host OS.
		rel = filepath.ToSlash(rel)
		if ig != nil && ig.MatchesPath(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// Skip symlinks to avoid following loops or bloating the
		// index with vendored copies of the same tree. WalkDir
		// already doesn't recurse symlinked directories, but it
		// does report symlinked files — Type bit isolates those.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		paths = append(paths, rel)
		if len(paths) >= maxIndexEntries {
			return errStopWalking
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalking) {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

// errStopWalking is the sentinel buildIndexWalk returns from its
// WalkDir callback to bail once we've hit the entry cap. Defined
// at package scope so the wrap/unwrap check can use errors.Is.
var errStopWalking = errors.New("finder: index entry limit reached")

// loadProjectGitignore reads <rootDir>/.gitignore (if present) and
// returns a compiled matcher. Returns nil when the file is missing
// or unreadable — the caller treats nil as "no gitignore rules
// beyond the hardcoded set."
func loadProjectGitignore(rootDir string) *gitignore.GitIgnore {
	path := filepath.Join(rootDir, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	return gitignore.CompileIgnoreLines(lines...)
}
