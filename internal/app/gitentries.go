// =============================================================================
// File: internal/app/gitentries.go
// Author: Chase Reynolds
// Created: 2026-08-15
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// gitentries.go is the single parse of `git status` that everything else in
// Vincent reads from: the file tree's dirty highlight, and the git panel's
// Changes list.
//
// One parse rather than two on purpose. The tree wants a path -> kind map
// and the panel wants an ordered list with sections, which is exactly the
// kind of difference that tempts you into a second `git status` call and a
// second parser — and then they drift, and a file is orange in one place
// and not the other. Both views derive from the same []gitEntry here.
//
// The command is `git status --porcelain -z --untracked-files=all`:
//
//   - `-z` disables git's C-style path quoting, so a path with a space,
//     a newline, or a non-ASCII character arrives raw and splitting on NUL
//     is enough. Without it you need an unquoter, and the unquoter is where
//     the bugs live.
//   - `--untracked-files=all` because the default collapses an untracked
//     directory to `dirname/` and you never see the new files inside it.
//     For Vincent that is the worst case to miss: a directory of files an
//     agent just created is exactly what you sat down to review.
//   - `GIT_OPTIONAL_LOCKS=0` so polling never contends with the agent's own
//     `git add` for `.git/index.lock`. This matters more here than in a
//     normal git UI — something else is actively writing in the repo we are
//     polling, by design.

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chasereyn/vincent/internal/filetree"
)

// gitEntry is one changed path as the git panel and the file tree both see
// it. Rel is slash-separated exactly as git reports it; Abs is the
// platform-native absolute path, which is what every other part of Vincent
// keys off.
type gitEntry struct {
	Rel  string
	Abs  string
	Name string // basename — what a panel row leads with
	Dir  string // parent, relative to the repo root; "" at the root

	Kind      filetree.GitChangeKind
	Untracked bool
	Deleted   bool
	Staged    bool

	// Repo is the absolute root of the repository that owns this entry.
	// Carried on the entry rather than looked up from the snapshot because
	// a merged multi-repo snapshot has many, and DirtyFiles has to resolve
	// a rename's old path against the RIGHT one.
	Repo string

	// Orig is the previous path of a rename or copy, relative to the repo
	// root. Empty for everything else.
	Orig string
}

// gitSnapshot is one `git status` run: the entries, plus the repo facts the
// panel's footer needs. IsRepo distinguishes "not a git repo" from "git
// failed", so a non-repo directory renders an empty panel rather than an
// error.
type gitSnapshot struct {
	IsRepo   bool
	Root     string
	RepoName string
	Branch   string
	Entries  []gitEntry

	// Repos holds one snapshot per repository when the root is a FOLDER OF
	// REPOS rather than a repo itself (phase 8a). It is empty in the
	// single-repo case, and every reader that predates phase 8a still gets
	// the answer it always got: IsRepo, Entries, Tracked, Untracked and
	// DirtyFiles are all merged across the members. What the aggregate
	// does NOT have is a Branch or a meaningful RepoName — a folder has
	// neither — so the footer and the writes ask activeRepoSnapshot
	// instead. See multirepo.go.
	Repos []gitSnapshot
}

// Tracked returns the entries git already knows about, in path order.
func (s gitSnapshot) Tracked() []gitEntry {
	return s.filter(false)
}

// Untracked returns the entries git has never seen, in path order.
func (s gitSnapshot) Untracked() []gitEntry {
	return s.filter(true)
}

// filter splits Entries on the untracked flag, preserving order.
func (s gitSnapshot) filter(untracked bool) []gitEntry {
	out := []gitEntry{}
	for _, e := range s.Entries {
		if e.Untracked == untracked {
			out = append(out, e)
		}
	}
	return out
}

// DirtyFiles projects the entries into the path -> kind map the file tree
// renders from. A rename contributes both ends: the old path shows as a
// deletion and the new path as a rename, so both rows are tinted and the
// move is legible in the tree rather than looking like an unrelated
// add/delete pair.
func (s gitSnapshot) DirtyFiles() map[string]filetree.GitChangeKind {
	out := make(map[string]filetree.GitChangeKind, len(s.Entries))
	for _, e := range s.Entries {
		if e.Orig != "" {
			// The rename's old path is relative to the repo that reported
			// it, which in a merged snapshot is not s.Root.
			base := e.Repo
			if base == "" {
				base = s.Root
			}
			if base != "" {
				out[filepath.Join(base, filepath.FromSlash(e.Orig))] = filetree.GitChangeDeleted
			}
		}
		out[e.Abs] = e.Kind
	}
	return out
}

// loadGitSnapshot runs `git status` in rootDir and returns everything the
// tree and the panel need from it.
//
// Every failure path degrades to a usable zero value rather than an error:
// not a repo, git missing from PATH, a transient failure mid-`rebase` — all
// of them mean "render nothing git-specific" and none of them are worth
// interrupting a review over.
func loadGitSnapshot(rootDir string) gitSnapshot {
	return loadSnapshotAs(rootDir, "")
}

// loadSnapshotAs is loadGitSnapshot with control over the path the
// snapshot is REPORTED under.
//
// as == "" means "whatever git says", which is `rev-parse
// --show-toplevel` and is what the single-repo path has always used. The
// multi-repo registry passes the path it discovered the repo by instead,
// because git resolves symlinks and the registry does not: on macOS a repo
// under /var/folders reports itself as /private/var/folders, and a
// snapshot filed under that path can never be matched to the a.repos entry
// or to a file-tree node. Reporting it under the path the caller already
// knows keeps every key in one namespace.
func loadSnapshotAs(dir, as string) gitSnapshot {
	if dir == "" {
		return gitSnapshot{}
	}
	topBytes, err := gitCmd(dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return gitSnapshot{}
	}
	toplevel := strings.TrimRight(string(topBytes), "\n\r")
	if toplevel == "" {
		return gitSnapshot{}
	}
	root := toplevel
	if as != "" {
		root = filepath.Clean(as)
	}

	snap := gitSnapshot{
		IsRepo:   true,
		Root:     root,
		RepoName: filepath.Base(root),
		Branch:   loadGitBranch(dir),
		Entries:  []gitEntry{},
	}

	out, err := gitCmd(dir, "status", "--porcelain", "-z", "--untracked-files=all").Output()
	if err != nil {
		// We are in a repo but could not read status. Report the repo facts
		// so the panel still shows the branch, and leave the list empty.
		return snap
	}
	snap.Entries = parsePorcelainZ(string(out), root)
	return snap
}

// gitCmd builds a git invocation rooted at rootDir with the environment
// Vincent always wants. See the file comment for why GIT_OPTIONAL_LOCKS
// matters here specifically.
func gitCmd(rootDir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", rootDir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	return cmd
}

// parsePorcelainZ converts NUL-delimited porcelain v1 output into entries,
// sorted by path.
//
// The format is a flat run of NUL-terminated records:
//
//	XY <path>\0
//	XY <newpath>\0<oldpath>\0     (renames and copies — TWO records)
//
// That second form is the one that catches people out. A parser that
// treats every record uniformly reads the old path as an unrelated file and
// reports a phantom change; this one consumes it as part of the rename.
func parsePorcelainZ(out, toplevel string) []gitEntry {
	records := strings.Split(out, "\x00")
	entries := []gitEntry{}

	for i := 0; i < len(records); i++ {
		rec := records[i]
		// A record is "XY " plus at least one path character.
		if len(rec) < 4 {
			continue
		}
		index, worktree := rec[0], rec[1]
		rel := rec[3:]

		var orig string
		if isRenameCode(index) || isRenameCode(worktree) {
			// The old path is the very next record. Guard the lookahead:
			// a truncated status output must not panic.
			if i+1 < len(records) {
				orig = records[i+1]
				i++
			}
		}

		entries = append(entries, gitEntry{
			Rel:       rel,
			Abs:       filepath.Join(toplevel, filepath.FromSlash(rel)),
			Name:      pathBase(rel),
			Dir:       pathDir(rel),
			Kind:      porcelainKindZ(index, worktree),
			Untracked: index == '?' || worktree == '?',
			Deleted:   index == 'D' || worktree == 'D',
			Staged:    index != ' ' && index != '?',
			Repo:      toplevel,
			Orig:      orig,
		})
	}

	sort.Slice(entries, func(a, b int) bool { return entries[a].Rel < entries[b].Rel })
	return entries
}

// isRenameCode reports whether a status column marks a rename or copy,
// which is what makes the record two-part.
func isRenameCode(c byte) bool {
	return c == 'R' || c == 'C'
}

// porcelainKindZ maps the index and worktree status columns to the tree's
// change kind. Order matters: a path that is untracked is untracked no
// matter what else the columns say, and a rename outranks the plain
// modification that usually accompanies it.
func porcelainKindZ(index, worktree byte) filetree.GitChangeKind {
	switch {
	case index == '?' || worktree == '?':
		return filetree.GitChangeAdded
	case isRenameCode(index) || isRenameCode(worktree):
		return filetree.GitChangeRenamed
	case index == 'D' || worktree == 'D':
		return filetree.GitChangeDeleted
	case index == 'A':
		return filetree.GitChangeAdded
	default:
		return filetree.GitChangeModified
	}
}

// pathBase returns the last segment of a git-reported (slash-separated)
// path. Not filepath.Base: git always uses forward slashes regardless of
// platform, and on Windows filepath.Base would leave "a/b/c.go" intact.
func pathBase(rel string) string {
	rel = strings.TrimSuffix(rel, "/")
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[i+1:]
	}
	return rel
}

// pathDir returns everything before the last segment of a git-reported
// path, or "" when the file sits at the repo root. This is the dimmed text
// beside a filename in the panel — the thing that tells two `index.ts`
// apart.
func pathDir(rel string) string {
	rel = strings.TrimSuffix(rel, "/")
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return ""
}
