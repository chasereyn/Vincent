# Multi-repo

At work, Chase's root is `~/Developer/RP-Repos` — a flat folder that holds
many git repositories, and is not itself one. At home, the root usually
*is* a repository. This subsystem is what makes both cases work through
the same code path: it decides which repository owns a given file, and
routes every git command, every review note, and every write to the
right one.

## How it works

**Finding the repos.** `internal/repos/repos.go` is pure — no tcell, no
shelling out, just `os.ReadDir` calls. `Discover(root)` returns the
absolute path of every git repository at or under root. If root itself
is a repo, the answer is just `[root]` — anything nested inside it is a
submodule or a vendored clone, which git already treats as part of the
outer repo, so `Discover` doesn't look inside it. Otherwise it walks down
`MaxDepth` (2) levels looking for a `.git` entry (a directory for a
normal clone, a file for a linked worktree, whose `.git` holds a
`gitdir:` pointer — `IsRepo` treats either as a hit), skipping
`node_modules`, `vendor`, `target`, `dist`, `build`, and any `.git` it
finds along the way, and does not descend further once it finds a repo.
Two levels covers `root/repo` and `root/group/repo`; anything deeper is a
workspace layout Vincent doesn't try to understand, and every extra level
multiplies the startup stat cost on a big folder.

`Owner(repos, path)` finds which discovered repo contains `path` by
longest matching prefix, and `Rel(repos, path)` gives the path relative
to that repo with forward slashes — the form both a git command and a
review note want.

**The registry**, `internal/app/multirepo.go`, wraps that in app state:
`a.repos` is refreshed by `refreshRepos`, called both from
`refreshGitStatus` and from the ten-second tree-refresh tick, so a repo
freshly cloned into the folder while Vincent is open shows up without a
restart.

**The single-repo short circuit.** `singleRepoMode()` is true when
`a.repos` is empty (discovery found nothing — which is exactly what
happens when the root sits *inside* a repo rather than above one; `repos.Discover`
only looks downward) or when the root is itself the one repo found
(`rootIsRepo`). In that mode, `repoFor` returns `a.rootDir` verbatim for
every path and `gitPathFor` hands git the absolute path — byte-identical
to what every call site did before this file existed. The multi-repo
owner lookup only engages when there's a real registry of two or more
repos, or one repo sitting below a non-repo root. `multirepo_test.go`
pins this explicitly: every "old" behavior is asserted by name so a
future change can't quietly merge the two paths into one that behaves
differently in the single-repo case.

**Running git in the right place.** `gitPathFor(path)` returns the
directory a git command should run in and the pathspec to pass it. In
single-repo mode that's `(a.rootDir, path)`, unchanged. In multi-repo
mode it's `(owner, repos.Rel(a.repos, abs))` — the pathspec becomes
repo-relative because that's the only form guaranteed to work with `git
-C <repo>`. When no repo owns the path, `dir` comes back `""` and the
caller must not shell out at all — this is the real answer for a file
sitting loose beside the repos in a folder-of-repos root, and callers like
`openDiff` in `diffview.go` flash "not in a git repository" for it rather
than trying to run git anyway.

**Status per repo, merged into one snapshot.** `loadReposSnapshot` in
`multirepo.go` is what the ten-second poller calls (see
`render-loop.md`). In the single-repo cases it short-circuits straight to
the ordinary `loadGitSnapshot` — again, byte-identical to before. In the
real multi-repo case, it runs the *same* `loadGitSnapshot` parse from
`gitentries.go` once per repo — deliberately not a second porcelain
parser — fanned out on `loadRepoSnapshotsConcurrently`, which bounds
concurrency at `repoStatusWorkers` (4): one worker would serialize twenty
repos into twenty round trips, unbounded would fork twenty gits at once
on a machine that's already running an agent, and four saturates a
single spinning disk without becoming the reason the machine feels slow.
The merged `gitSnapshot.Repos` holds one member per repo; `Entries` on
the merged snapshot is every repo's entries concatenated, so `Changes
(N)` in the panel counts the whole folder and `DirtyFiles()` colors a
file no matter which repo it's in. The merged snapshot has no meaningful
`Branch` or `RepoName` of its own — a folder has neither — which is why
the footer and the writes ask `activeRepoSnapshot()` instead.

One detail that only shows up on macOS: each per-repo snapshot is
reported under the path *discovery* found it by
(`loadSnapshotAs(path, path)`), not under whatever `git rev-parse
--show-toplevel` resolves to. On macOS a repo under `/var/folders`
resolves to `/private/var/folders` — a real symlink git follows and the
registry doesn't — so matching on git's own resolved path would silently
never hit the corresponding `a.repos` entry. Reporting under the
already-known path keeps every key in one namespace.

**Which repo the writes act on.** `activeRepo()` is the single answer
every commit, push, checkout, and the footer's branch row all use. Its
precedence, pinned by test, in order:

1. Single-repo mode — the root, no decision to make.
2. The repo owning the **active tab's** file. What's on screen is what
   you think you're working in, and a commit landing anywhere else is
   the exact failure this ordering exists to prevent.
3. The repo of the **last Changes-panel row you clicked** — sticky, via
   `a.gitPanelRepo`, set by `setGitPanelRepo` on a click, not on hover, so
   a pointer drifting over another repo's row can never re-aim an armed
   commit box.
4. The first repo that actually has changes — in a review session that's
   almost always the one repo the agent has been writing to.
5. The first repo, alphabetically, so the answer is never empty when a
   repo exists at all.

`activeRepoSnapshot()` looks up the matching member of `gitSnap.Repos` by
`Root`; `activeRepoName()` and `branchLabel()` read off that. The bold
**"active"** tag you'd see in the Changes panel, next to a repo's `⑂ name
/ branch` header, is drawn in `drawGitPanelList` (`gitpanel.go`): the
header whose `repoRoot` equals `activeRepo()` gets `Bold(true)` on its
label plus a dimmed `"  active"` suffix — because with three repos
showing changes at once, "which one does Esc c actually commit to" has to
be answerable by looking at the panel, not just by opening the commit box
and reading its title afterward.

**Review notes carry repo-relative paths.** A `review.Comment.File` is
relative to the repo that owns it (`review.Comment.Repo`, resolved by
`reviewRepoFor` in `app/review.go`), not to Vincent's root. The reason is
what the agent reading the batch actually has as its working directory:
in a folder-of-repos root, a root-relative path like `alpha/src/main.go`
would be one path segment wrong for an agent whose `cwd` is `alpha`
itself. In single-repo mode `Repo` is left empty and `File` stays
root-relative — the same string it always was — because `reviewRelFor`
returns `""` deliberately rather than recomputing the same answer a
second way.

## Why it is built this way

CLAUDE.md is explicit that phase 8 exists because of one concrete fact
about how Chase works: at his job the root is a flat folder of company
repos and the root switcher (`Esc o`, covered in `keys-and-chrome.md`) is
how he moves between that folder and a personal project at home — so "a
root that contains repos" is the *normal* work case, not an edge case to
tolerate.

The single-repo short circuit being pinned by explicit regression tests,
rather than trusted to "obviously still work," is a direct response to
the risk that mattered most here: at-home usage (a root that is a repo)
was the only case that had ever been used on a real terminal when phase 8
shipped, and CLAUDE.md says so plainly — "do not collapse the two paths
into one 'cleaner' one without re-reading those tests."

Running the *same* `loadGitSnapshot` per repo instead of writing a
second, multi-repo-aware porcelain parser is the identical lesson
`gitentries.go` already teaches for the tree-versus-panel split: two
parsers of the same format drift, and the drift shows up as a file that's
colored in one view and missing from another.

## What can go wrong

**A file sitting loose in the root folder (not inside any discovered
repo) can't be diffed, committed, or pushed.** `gitPathFor` returns `("",
"")` for it and callers flash "not in a git repository" — this is
correct, not a bug, but it can surprise someone who expects everything
under the root to be "in git" just because the root itself is a project
folder.

**Two repos both contain `src/main.go`, and you're not sure which one a
review note is about.** `Comment.Repo` disambiguates it internally, and
`review-loop.md` covers how; from the panel, the repo's own `⑂ name /
branch` header above the file is the visible cue.

**The branch shown in the status bar doesn't match the repo you think
you're in.** `branchLabel()` reads the *active* repo's branch, recomputed
every frame rather than cached — switching your active tab to a file in
another repo changes it immediately, which is correct, but it means the
status bar's branch can change out from under you without any git event
having happened.

**A newly cloned repo inside the root doesn't show up immediately.**
`refreshRepos` runs on the ten-second tick (and on an explicit
`refreshGitStatus`), so there's up to that interval before a fresh clone
is discovered.

**A `.git` directory that git itself refuses (a half-cloned repo, a
broken worktree pointer) doesn't appear at all** rather than showing up
as a broken entry — `loadReposSnapshot` drops any per-repo snapshot whose
`IsRepo` came back false rather than listing a repo whose every read
failed.

## Not covered here

The Changes panel's actual layout and the three git writes themselves are
`changes-panel-and-git-writes.md` — this page only covers how "which repo"
gets decided before any of that runs. The root switcher (`Esc o`), which
changes which *folder* Vincent is pointed at entirely (a different
question from "which repo inside this folder"), is covered in
`keys-and-chrome.md`. How review notes get rendered into a batch is
`review-loop.md`.

Not verified on a terminal: how the panel and the footer actually read at
a glance with more than three or four repos showing changes
simultaneously, and how `repoStatusWorkers`'s fan-out of 4 concurrent
`git status` calls performs against a folder of twenty-plus repos on a
real machine under load from a running agent.
