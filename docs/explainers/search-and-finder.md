# Search and the finder

There are two separate ways to find something in Vincent: `Esc p` (or
`Esc f`) opens a fuzzy filename finder — type a few letters of a path and
jump to it, like VS Code's Cmd+P — and `Esc F` opens content search, a
grep across every file under the root. They share one underlying index of
which files exist, but the matching itself is two different engines for
two different questions.

## How it works

**Building the file list.** `internal/finder/index.go`'s `BuildIndex`
returns every non-ignored file under a root, root-relative with forward
slashes on every platform. It tries a fast path first: if the directory
is a git repo, `git ls-files --cached --others --exclude-standard -z`
gets every tracked-or-untracked-but-not-ignored file in one fork, honoring
`.gitignore` for free — even on a 100k-file repo this returns in well
under a second, since git already has the index in memory. When that
fails (not a repo, git missing), it falls back to a manual
`filepath.WalkDir`, filtered through a compiled `.gitignore` plus a small
hardcoded ignore set (`node_modules`, `vendor`, `.git`, `__pycache__`,
`dist`, `build`, `.DS_Store`, and a few more) — good enough for the
common case, though only the project-root `.gitignore` is consulted, not
every nested one, trading a little fidelity for not having to walk and
merge a tree of gitignore files.

Since phase 8, the root isn't always one repo. `BuildIndex` calls
`repos.Discover(root)` first (see `multi-repo.md`); if the root **is**
one repo, this collapses to the exact old single-repo path, byte for
byte (`TestBuildIndex_RootIsRepoUnchanged` pins this). Otherwise
`buildIndexMultiRoot` indexes each discovered repo separately —
concurrently, bounded at `maxConcurrentRepoIndexBuilds` (8), each with
its own git-fast-path-or-walk — prefixes each repo's paths with that
repo's folder relative to the root, and walks only whatever part of the
root isn't inside any of those repos
(`buildIndexWalkExcluding`, which prunes any directory in the exclude
set the same way it prunes `node_modules`). Without the multi-root
split, the old single-strategy build would fall through to the manual
walk over the **entire** root in a folder-of-repos layout, hand-walking
every repo's build output and vendored dependencies instead of asking
git to skip them per repo. The index caps at `maxIndexEntries` (200,000)
total, truncating silently past that — the assumption being that past
200k files you're almost certainly looking at a vendored dump and would
rather see something than wait minutes for completeness.

**Scoring a query against the filename index.** `internal/finder/score.go`
is a small fzy-style fuzzy matcher: every character of the typed query
must appear in the path in order (case-insensitive), and a score is
built from a handful of heuristics familiar from VS Code's Cmd+P — a
match in the basename outranks one only in the directory
(`bonusBasename`, 15), a match starting a word after `/`, `_`, `-`, or
`.` is favored (`bonusWordBoundary`, 20), consecutive matched characters
outrank scattered ones (`bonusConsecutive`, 30 — deliberately the
largest bonus, so that typing the literal start of a basename beats a
scattered match across underscores), and an earlier first-match position
breaks remaining ties. `Score` returns both the numeric score and the
matched rune indexes, which is what lets the finder highlight exactly
which characters of a result line up with what you typed — the same
trick `fzf` uses.

**The filename finder UI**, `internal/app/finder.go`, is a modal: a query
field on top, ten rows of results below, hover follows the pointer,
Enter opens the highlighted row. Matched characters render in
`theme.FindCurrent` — the same accent color the in-file find bar's
current-match marker and the content-search modal's matched span both
use, so all three "here's what matched" cues read as one visual
language across the app.

**Content search** is a different engine entirely:
`internal/search/search.go`, pure — no tcell import, mirroring the
`internal/diff`/`internal/markdown` split — and it deliberately does not
walk the filesystem itself. It's handed the file list from the caller,
which in Vincent's case is `finder.Finder.Paths()`, the very same
multi-root index the filename finder built. That's what makes content
search honor the identical gitignore and repo-boundary rules as the
finder for free, off one index instead of two.

`search.Search(ctx, files, root, query, opts, onDone)` returns a channel
of `Match`es. It skips anything over `maxFileSize` (1 MiB) or anything
whose first `binarySniffLen` (8 KiB) contains a NUL byte, treating that
as binary rather than scanning it line by line — this stops the search
from "matching" garbage inside an image or a compiled binary. Matching
is case-insensitive substring by default; a query prefixed with the
literal `re:` switches to regexp mode. Total matches cap at
`DefaultMaxMatches` (2000). A bounded worker pool (`runtime.NumCPU()`,
capped at `maxWorkers` = 8, and never more workers than there are files)
pulls paths off a `jobs` channel; each worker calls `searchFile`, which
opens, size-checks, sniffs, and scans one file, emitting `Match`es onto a
shared `out` channel. Hitting the match cap sets an atomic `capped` flag
and calls the shared `cancel()`, so every other worker notices on its
next job or next line and stops too, rather than each one running its
current file to completion independently.

**Wiring it to the UI without racing itself.** `internal/app/search.go`
debounces typing: `searchQueryChanged` fires on every edit, bumps a
`generation` counter, cancels whatever the *previous* keystroke had in
flight — both its pending debounce timer and, if a scan had already
started, the engine's own `ctx` — and arms a fresh 150ms debounce timer
(`scheduleSearch`, a plain `time.AfterFunc`, not a goroutine Vincent has
to manage itself) tagged with the new generation. When that timer fires
with no further keystroke, it posts a `searchDebounceEvent` carrying its
generation; `runSearchIfCurrent` only starts an actual scan if that
generation still matches the live one — otherwise a fast typist would
pile up abandoned engine runs burning CPU in the background.

The scan itself runs on a goroutine that reads the engine's channel,
batches results (flushing on either a size or a time bound —
`searchFlushBatchSize` / `searchFlushInterval`), and posts each batch as
a `searchResultsEvent` tagged with its generation; a stale event (an
older generation than the query currently reads) is dropped by
`applySearchResults` on arrival. Getting the *final* "done" event to
always arrive after every batch — never before — needed real care: the
engine's own completion goroutine does `close(out)` and then calls
`onDone(...)` as two separate, unsynchronized statements, so if `onDone`
posted the done event directly, it could race the UI-side goroutine that
is still flushing a buffered batch it noticed via the channel closing.
The fix is `outcomeCh`, an intermediate buffered channel: the read loop
notices the channel close itself, flushes whatever's left in its own
buffer, *then* reads the outcome off `outcomeCh` and posts the done
event — guaranteeing the done event is the last thing this goroutine ever
posts for that generation. A cancelled search (the reviewer typed
something new) doesn't wait indefinitely for a possibly-slow-to-notice
engine to call `onDone`; it gives it one second and then posts a
best-effort done event with a zero `Outcome` anyway, so the UI is never
stuck reading "searching…" for a query nobody cares about any more.

The footer of the `Esc F` modal reflects three states off this same
event stream: "searching…" while a scan is in flight, "N matches in M
files" on completion, and "capped at 2000" when the match limit was hit.
Opening a result places the cursor with the ordinary `Tab.MoveCursorTo` —
no special-casing needed, the same mechanism the find bar and the
filename finder both use.

## Why it is built this way

Content search reusing the filename finder's index, rather than building
its own file list, is the same "one parse, many readers" principle
`gitentries.go` follows for git status (see
`changes-panel-and-git-writes.md`): two independently-built file lists
for the same root would drift on which gitignore rules or repo
boundaries applied, and a file excluded from one but not the other would
be a confusing, hard-to-explain inconsistency.

The generation counter plus the two-channel (`outcomeCh` then
`searchResultsEvent`) ordering trick exist entirely because typing is
fast and searching is not: without cancellation-on-every-keystroke, a
reviewer typing a five-character query would leave four abandoned scans
running in the background, each one still burning worker-pool time on a
query nobody's looking at any more.

Capping workers at 8 (both here and in `finder/index.go`'s
`maxConcurrentRepoIndexBuilds`) is a considered number, not a rounding
choice — the comment on it names the actual environment this was tuned
for: "a VPN-mounted network share," Chase's actual work root. Past a
handful of concurrent readers, more concurrency adds scheduling overhead
without moving results faster on that kind of I/O-bound disk.

## What can go wrong

**A file you know exists doesn't show up in either the finder or content
search.** Check whether it's gitignored, or whether it's a nested repo's
file that fell outside `maxIndexEntries` (200,000) on an enormous root —
the index truncates silently past that cap rather than erroring.

**Content search says "capped at 2000" and you're missing results.**
That's the `DefaultMaxMatches` limit; narrow the query (a `re:` regex
with more context, or a longer literal string) to get under it.

**A search feels like it "hangs" for a moment after you stop typing.**
That's the 150ms debounce window working as designed — it exists so a
fast typist's intermediate keystrokes don't each trigger a full scan.

**Typing quickly and seeing a result flash briefly before being replaced
by different ones.** That would mean a stale (older-generation) event
slipped through the guard — worth treating as a real bug if seen, since
`applySearchResults` and `runSearchIfCurrent` are both supposed to drop
anything not matching the current generation.

**A huge minified file's "line" shows an oddly short, centered snippet
instead of the whole match context.** `Match.Text` is deliberately
trimmed to `maxLineTextLen` (200 runes), centered on the match — a
minified bundle's single line can be tens of thousands of characters, and
the result row has to stay a reasonable width.

## Not covered here

The scoring constants' exact tuning and the fzy-style algorithm's full
mechanics live entirely in `internal/finder/score.go`'s comments and
aren't re-derived here beyond the bonus names. The render loop's
custom-tcell-event pattern that both `searchDebounceEvent` and
`searchResultsEvent` follow is explained more generally in
`render-loop.md`. Multi-repo path resolution
(`repos.Discover`, per-repo indexing) is `multi-repo.md`'s subject in
full.

Not verified on a terminal: how content search performs in practice
against Chase's actual `~/Developer/RP-Repos` — a folder of many
repositories over a network-mounted volume — versus the tuning
assumptions in the code comments, and how the finder and search modals'
scroll and hover actually feel side by side on a real keyboard-and-mouse
session.
