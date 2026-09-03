# The diff viewer

This is what draws a diff on screen: a file's old contents on the left
gutter, its new contents on the right, and colour telling you what kind of
change each line is. Every diff you open in Vincent runs through this
same code — there is no `delta` or `diff-so-fancy` shelled out to.

## How it works

Three layers, cleanly split. `internal/diff` parses plain `git diff`
output (or, for a buffer-vs-disk comparison, builds unified-diff text
itself) into a flat `[]diff.Row` with no tcell import at all.
`internal/editor/diffview.go` paints that slice. `internal/app/diffview.go`
is the glue: it shells out to git, decides which file to diff against
what, opens the tab, and keeps it fresh while an agent keeps writing.

**Parsing.** `diff.Parse` in `internal/diff/diff.go` walks the lines of a
`git diff` and turns each into a `Row` with a `Kind` (`KindContext`,
`KindAdded`, `KindDeleted`, `KindGap` for the elision between hunks, or
`KindMeta` for anything it recognizes but doesn't interpret, like "Binary
files a/x and b/x differ"). Each row keeps one-based `Old` and `New` line
numbers — zero on whichever side the row doesn't exist on, since git's own
numbers start at one so zero is unambiguously "none". The parser is
deliberately tolerant: an unparseable line becomes `KindMeta` rather than
an error, and a malformed hunk header just leaves the line counters where
they were rather than aborting. There's no user action that could fix a
parse failure anyway, so failing softly is strictly better than refusing
the whole diff.

Two platform details are handled right at the split: the trailing empty
element left by `strings.Split(out, "\n")` (git diff output always ends in
a newline) is dropped before it becomes a phantom blank row that also
advances both line counters past the end of the file, and every line has
its trailing `\r` stripped so a CRLF file — most of a .NET or JS repo on
Windows — doesn't leave a stray control character on the end of every row.

**Word-level tint.** After parsing, `assignWordRanges` pairs up runs of
consecutive deletions with the run of additions immediately following
them, one-for-one — the same rule VS Code uses. It is not a real diff at
the line level: a line inserted in the middle of a changed block can
mis-pair the rest of the block. That's an accepted trade, because the
common case is one line edited in place, the +/- markers and line numbers
stay correct regardless, and a wrong pairing only ever costs a cosmetic
tint. Each paired line is then compared with a real diff:
`tokenWordRange` in `internal/diff/myers.go` tokenizes both lines (a
maximal run of letters/digits/underscore, a run of whitespace, or one
punctuation rune — so a renamed variable diffs as one changed token, not
a scatter of single-character edits), runs a token-level Myers diff
between the two token lists, and marks the tightest rune span covering
every changed token on each side. A pair too big to be worth it (either
line over 400 runes, or either side over 200 tokens) falls back to
`assignPrefixSuffixRange`, the original heuristic: common prefix, common
suffix, tint what's left in the middle. Either way, a pair that shares
nothing at all — no common token, or empty prefix and suffix — is left
untinted; claiming the whole line changed says nothing the row colour
hasn't already said.

**The Myers diff itself.** `myersDiff` in `myers.go` is the classic 1986
O(ND) algorithm: a forward search over the edit graph's diagonals, one
round per increasing edit distance, backtracked once the search reaches
the far corner. It's generic over `[T comparable]` so the same function
serves both the token-level word tint and `diff.Unified`, which builds a
full unified-diff between two texts line-by-line — used for the conflict
prompt's "Show diff" (buffer vs. disk) and any other in-process comparison
that doesn't have `git diff` to shell out to. `maxLineEditDistance` (2000)
caps the work: past that edit distance, `Unified` gives up on the minimal
script and reports the whole file as one deletion followed by one
insertion. The +/- markers stay correct; only "smallest possible diff"
is given up, and only for inputs different enough that a human would call
them different files anyway.

**Drawing.** `internal/editor/diffview.go` turns `Tab.Mode == "diff"` into
a real render path (`renderDiff`), the same `Tab.Mode` pattern used for
image tabs — a diff is a mode on the ordinary `Tab`, not a parallel type,
so scrolling, clamping, hit-testing, and the find bar all work on it with
zero special cases elsewhere in the app. Each row draws two right-aligned
number columns (old, then new; `diffNumberWidth` sizes them off the
largest line number in the diff, minimum two digits), a one-character
marker column (`+`, `-`, or blank), and the line's text, syntax-highlighted
the same way a normal file is. The row's full width — not just as far as
the text goes — is tinted red or green: `DiffAddBG` /`DiffDelBG` for the
whole row, `DiffAddWordBG` / `DiffDelWordBG` (a visibly darker shade of
the same hue) for the exact rune range `assignWordRanges` marked as
changed. A tint that stopped at end-of-line would make a short changed
line look like a different kind of change from a long one, which is
exactly the kind of false signal a review tool must never send. Deleted
rows get `DiffDelMark` (a red `-`), added rows `DiffAddMark` (a green
`+`); context rows just get a blank gutter. A hunk boundary (`KindGap`)
draws as a single `⋯` in the marker column, lined up with the +/- above
and below it, reading as "the gutter continues, the file does not."

Overlay rows — the review composer and saved-note markers, from
`internal/editor/diffoverlay.go` — are interleaved into the same walk via
`diffDisplayCells`, so a diff row can have extra screen rows pushed in
below it without the renderer needing a second code path.

**Getting the diff text.** `internal/app/diffview.go`'s `loadDiffRows`
runs `git diff <base> -- <path>` where `base` is `HEAD`, or (via
`diffBase`) the well-known empty-tree SHA `4b825dc642cb6eb9a060e54bf8d69288fbee4904`
in a repo with no commits yet — without that fallback, `git diff HEAD`
fails outright in a freshly initialized repo and every file reads "No
changes" with no explanation. An untracked file produces nothing against
`HEAD` (git won't diff what it doesn't track), so `loadDiffRows` checks
`gitTracks` (`git ls-files --error-unmatch`) and only then falls back to
`git diff --no-index -- <os.DevNull> <path>`, which renders the whole file
as one addition. That check is load-bearing: running the fallback
unconditionally would render every clean tracked file as one huge
addition too.

**Staying live.** A diff tab tracks the real file's `Path`, and
`reconcileDiffTab` re-runs the diff whenever the poller notices the file's
mtime moved forward — the ordinary case being an agent writing to the
file you're reviewing. It's silent: no flash per write, because a flash
on every keystroke-equivalent from the agent would be constant noise
during exactly the stretch you're trying to read. A file that goes fully
clean keeps showing its last diff rather than emptying out, so an agent
reverting its own change doesn't erase what you were reading. `Esc d`
re-runs the diff manually too — on a diff tab that doubles as a refresh.
A "frozen" diff tab (`DiffFrozen`, set by the conflict prompt's Show
diff view) is skipped by both the reconcile loop and by `openDiff`
reusing an existing tab, since it's answering a different question about
the same file — buffer vs. disk, not working tree vs. HEAD — and must
not be silently swapped back to the git diff.

## Why it is built this way

The parser/renderer split (`internal/diff` pure, `editor/diffview.go`
drawing) exists because non-negotiable 3 in CLAUDE.md rules out shelling
out to `delta` at render time — the diff renderer has to be in-process,
and being in-process is also what makes highlighting run over diff bodies
the same way it runs over files.

A diff tab being a real `Tab` rather than a new type is the pattern named
in CLAUDE.md as "`Tab.Mode` for non-text tabs": a new kind of view is a
mode, not a new type, so it inherits the tab list, the switcher, and modal
routing for free, and `ReadOnly()` (`Mode != ""`) gates every mutation in
one place instead of a scattered set of `if IsDiff()` checks.

The token-level Myers word tint replaced a plain prefix/suffix guess for
one concrete reason: prefix/suffix comparison mis-highlights a line where
a token in the *middle* changed and both ends happen to match — a renamed
variable used at both ends of a line, for instance. Tokenizing runs of
identifier characters together (rather than diffing rune by rune) is what
keeps a renamed variable or a re-indented line reading as one changed
token instead of a scatter of single-character edits nobody could parse
by eye.

## What can go wrong

**A file shows "No changes in x" when you know it changed.** Either it's
clean against `HEAD` and something else changed it back, or (rare) the
untracked-fallback logic in `loadDiffRows` decided it was tracked when
git actually has no record of it — `gitTracks` treats any git failure as
"tracked", which routes to the safe answer (report nothing) rather than
risking a false "whole file is new" render.

**A CRLF file's diff looks fine but a comparison elsewhere disagrees.**
`diff.Parse` strips `\r` per line for display; nothing else in this path
does that normalization, so a CRLF-vs-LF difference that git itself
considers real still shows up as a change — which is correct, but worth
knowing if a line looks unchanged on screen while its raw bytes differ.

**A very large or very different pair of files shows a diff that looks
like "everything was replaced" instead of a tight, minimal diff.** That's
`maxLineEditDistance` giving up past 2000 edits and reporting one big
delete plus one big insert. The markers are still correct; only the
"smallest possible diff" property is sacrificed, and only on inputs an
actual human would call unrelated files.

**A word tint looks wrong or missing on an obviously-edited line.** Check
whether the line is over 400 runes or over 200 tokens (`maxTintLineRunes`,
`maxTintTokens`) — past either, the tint falls back to prefix/suffix, and
a token-level rename in the middle of a long minified line can render
untinted because the fallback found no shared prefix or suffix either.

**A diff tab keeps showing stale content after several rapid agent
writes.** `reconcileDiffTab` compares mtimes on each poll tick (the
10-second git poller, see `render-loop.md`), so a diff can lag a running
agent by up to that interval; it is not instantaneous.

## Not covered here

Where the diff's selected rows turn into a review note, and how the
composer's overlay rows are built and clicked, is `review-loop.md` — this
page only covers how the diff itself is computed and painted. The
Changes panel that lists which files have diffs at all is
`changes-panel-and-git-writes.md`. Which repository `loadDiffRows` runs
`git diff` in, when the root holds more than one repo, is
`multi-repo.md`'s `gitPathFor`. Not verified on a terminal: the actual
pixel-level look of the tints and the `⋯` gap glyph in a real terminal
font, and how large a real agent-written diff gets before the highlighter
or the Myers diff becomes perceptibly slow.
