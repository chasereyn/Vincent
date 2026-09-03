# The Changes panel and the git writes

This is the panel on the right: it lists every file an agent has touched,
in one place, colour-coded by what happened to it, and it is also the
only place Vincent lets you write to git — commit everything, push, or
switch branches. There is no staging, no partial commit, and no amend;
those live in lazygit, one Herdr pane over.

## How it works

**One parse feeds two views.** `internal/app/gitentries.go`'s
`loadGitSnapshot` runs exactly one `git status` per repo:

```
git status --porcelain -z --untracked-files=all
```

with `GIT_OPTIONAL_LOCKS=0` set on the command's environment. All three
flags are load-bearing, not defaults. `-z` disables git's C-style path
quoting so a path with a space or a non-ASCII character comes back raw
and NUL-delimited — without it you'd need a separate unquoter, and that's
where the bugs live. `--untracked-files=all` stops git from collapsing a
new untracked directory down to `dirname/`; for a review tool, a folder
of files an agent just created is exactly the case you cannot afford to
miss. `GIT_OPTIONAL_LOCKS=0` keeps Vincent's own polling from contending
with the agent's own `git add` for `.git/index.lock` — this matters more
here than in an ordinary git UI, because something else is actively
writing to the repo you're polling, by design.

The result, `parsePorcelainZ`, produces one flat `[]gitEntry`, and both
the file tree and the Changes panel read from that same slice —
`gitSnapshot.DirtyFiles()` for the tree's path-to-colour map,
`gitSnapshot.Tracked()`/`Untracked()` for the panel's two sections. The
comment on the file spells out why: the tree wants a map, the panel wants
an ordered list, and that difference is exactly what tempts you into a
second `git status` call and a second parser — and then the two drift,
and a file is orange in the tree and absent from the panel. A rename is
the case that breaks a naive parser fastest: it emits **two** NUL-
delimited records, the new path then the old one, and `parsePorcelainZ`
consumes the second record as part of the same entry (`isRenameCode`
checks the index and worktree status columns) rather than reading it as
an unrelated file.

**The panel's shape**, in `internal/app/gitpanel.go`, is Zed's, read off
a side-by-side screenshot: a `Changes (N)` header, then `Tracked` and
`Untracked` sections, each row showing the filename in its status color
with the parent directory dimmed beside it (`drawGitPanelRow` — this is
what tells two files both named `index.ts` apart), deleted files struck
through (`StrikeThrough(true)`), and a repo/branch footer. Clicking a row
opens that file's diff. With more than one repository under the root
(see `multi-repo.md`), `gitPanelRepoItems` groups the rows under a `⑂
name / branch` header per repo that actually has changes — a clean repo
isn't listed at all, so a folder of five repos with work in one doesn't
bury it — with a blank row between groups.

**The three writes** live in `internal/app/gitwrite.go`, and CLAUDE.md
names them explicitly as "four writes, all blunt" (the fourth being
saving a text buffer, which isn't git's business):

- `Esc c` opens the commit box (`commitbox.go`) and Enter runs
  `git add -A` then `git commit -m <message>` (`gitCommitAll`) — tracked
  and untracked together, no staging, no partial commit.
- `Esc P` runs `git push` on a worker goroutine (`pushBranch`), creating
  the upstream with `git push -u origin <branch>` only when the branch
  doesn't already have one — checked with `rev-parse --abbrev-ref
  --symbolic-full-name @{u}` first, because pushing `-u` unconditionally
  would silently rewrite the upstream of a branch that already tracks
  something else.
- `Esc b`, or a click on the panel's repo/branch row, opens the branch
  picker (`branchpicker.go`) and Enter runs `git checkout <branch>` — no
  `-B`, no `--force`, no stash. If the checkout would clobber the working
  tree, plain git refuses and says so; Vincent doesn't try to be smarter
  than that refusal.

Every one of these goes through a `gitRunner` function value
(`gitwrite.go`), never `exec` directly, specifically so a test can inject
a fake runner and assert on the argv without a real remote or a real
credential prompt. Two environment details matter: writes get
`GIT_TERMINAL_PROMPT=0` so a repo that wants a username fails in
milliseconds instead of blocking on a terminal that's in raw mode and
can't show git's prompt anyway, and writes deliberately do **not** get
`GIT_OPTIONAL_LOCKS=0` — every one of them genuinely needs the index
lock, and when it's already held (an agent mid-write, usually) Vincent
shows one sentence, `indexLockSentence`, and does **not** retry: a retry
loop would race whatever is actually holding the lock.

**Three refusals gate the commit gesture**, all before the box even
opens (`openCommitBox`): not a repository, nothing changed
(`len(a.activeRepoSnapshot().Entries) == 0`), and — the one that exists
to prevent losing work rather than to prevent a git error — **any dirty
text tab in the active repo**. `git add -A` stages whatever is on disk,
which is not the version on screen if you've been editing a file without
saving it; committing would silently exclude your unsaved change from a
commit message that claims to describe it. The guard is checked again
inside `submitCommit` itself, not just at open, because the box can sit
armed while you keep typing into a file. A fourth refusal — an empty
message — happens on Enter and leaves the box open rather than closing
it, because the fix is to type something, not to start over. And a
failed commit **keeps the message** in the box (`closeCommitBox` never
clears `commitValue`; only `clearCommitBox`, called on success, does) —
the same consume-on-success rule the review batch follows.

**Failure reporting is uniform** across all three writes:
`gitFailureSentence` in `gitwrite.go` turns a git failure into one plain
sentence for the status bar, picking the first line of stderr that isn't
blank or a `hint:` continuation (`gitStderrSummary`) and stripping git's
own `fatal:`/`error:` prefix. The full stderr, verbatim, goes to
`~/.config/vincent/herdr.log` (`review.Logf`, wired the same way the
review loop's herdr failures are) — a status bar is one row shared with
the branch and cursor readouts, and git's actual stderr is often five
lines naming remote refs and config keys the reviewer never typed.

The push runs on a worker goroutine and posts a `gitPushEvent` back to
the main loop, the same custom-tcell-event pattern the ten-second git
poller uses (see `render-loop.md`) — a push talks to a network, so it's
the one write that genuinely cannot run on the UI thread without risking
a frozen pointer if the remote is slow. `a.pushing` refuses a second push
while one is in flight, and the 60-second timeout inside `gitPush` is
what guarantees that flag comes back even if the remote never answers at
all.

## Why it is built this way

Non-negotiable 4 in CLAUDE.md is explicit: "Four writes, all blunt...
What is not coming: staging checkboxes, partial commits, amend, rebase,
stash, discard." Every constraint above traces back to that line —
`git add -A` with no staging UI, `git checkout` with no force flag, and
no fourth git command anywhere in the file.

The dirty-tab guard on commit is the sharpest example of "the write is
blunt, so the guard has to be smart": since `git add -A` cannot be told
to skip a file, the only way to protect an unsaved edit is to refuse the
whole commit until it's saved or discarded. That's a real trade — it
means a reviewer with one unrelated dirty scratch file blocks a commit
they wanted to make — but CLAUDE.md's "Anything finer than commit
everything belongs to lazygit" settles which side of that trade Vincent
takes.

`gitentries.go`'s one-parse-many-readers design is called out directly
in its own file comment as a lesson already learned: a second `git
status` call and a second parser is "exactly the kind of difference that
tempts you," and the fix was deleting the older `parsePorcelain` when the
panel landed rather than leaving it to rot beside the new one.

## What can go wrong

**"Not a git repository."** The active repo — not necessarily the root —
isn't a git repo at all, or `git rev-parse --show-toplevel` failed. See
`multi-repo.md` for what "active repo" means when the root holds several.

**"git index is locked (an agent is probably mid-write). Try again in a
moment."** Exactly what it says: something else holds `.git/index.lock`,
almost always the agent whose work you're reviewing. There is no retry
loop by design — wait a moment and press the key again.

**"Save or discard N dirty tabs first."** A commit was blocked because an
open buffer in the active repo has unsaved changes. Save the file (`Esc
s`) or close the tab, then try again; the commit box's message, if you'd
already typed one, is still there.

**A push reports "HEAD is detached — check out a branch first."** There's
nowhere for `git push` to send the SHA; check out a branch first.

**The commit box's title reads "Commit to alpha" instead of "Commit
all".** That's not a bug — it's `commitBoxTitle` telling you which
repository, of several under one root, Enter is actually about to touch,
because the file list above it may be showing files from more than one
repo at once.

## Not covered here

Which repository owns a given file, how `activeRepo()` picks the one the
three writes and the panel's footer act on, and what the bold "active"
tag on a repo header means, are all `multi-repo.md`'s subject — this page
treats "the repo" as a given. The review notes that stack above the
commit box in the same footer, and the "Send to agent"/"Copy" actions
there, are `review-loop.md`. The ten-second poll that refreshes
`a.gitSnap` in the background, and the custom-event pattern it uses to
get results back to the UI thread, are `render-loop.md`.

Not verified on a terminal: the actual look of the panel's repo grouping
with more than two repos open at once, and whether the branch picker's
row-zero-is-current convention reads clearly against a long branch list
in a narrow window.
