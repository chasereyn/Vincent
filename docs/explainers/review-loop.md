# The review loop

This is the reason Vincent exists: you click lines in a diff, write a short
note about them, and one keypress drops those notes into the agent's
terminal so it can fix them. Nothing here edits a file — the review loop
only ever produces text and hands it somewhere.

## How it works

The model lives in `internal/review/review.go` and knows nothing about
tcell, git, or the screen. A `review.Comment` is one note: `File`
(repo-relative, forward slashes always), `Repo` (the absolute root that
`File` is relative to, empty when there's only one repo), `Side` and
`Start`/`End` (which lines it addresses), `Hunk`, a frozen `Snippet`, a
`Kind`, and the note's `Text`. A `review.Batch` is just `[]Comment` waiting
to go out.

The one rule that shapes the whole package: **a comment's anchor is its
`Snippet`, never a live line number.** `Snippet` is the verbatim diff text
for the lines you selected, each line still carrying its leading `+`, `-`,
or space. Line numbers are recorded for the reader's benefit and are never
recomputed. When the agent rewrites the file, the old line numbers are
already history — the snippet is what actually says what you were talking
about.

**Selecting lines and opening the composer.** `Esc r`, or right-click on a
diff row, calls `openReviewComposer` in `internal/app/review.go`. It reads
the tab's selected diff-row range, shrinks it to the rows that can
actually carry a note with `commentableRange` (additions, deletions, and
context lines only — not the `⋯` gap between hunks), and works out which
side of the diff the note addresses with `rangeAddress`: a range of
nothing but deletions addresses the old file (recorded with `Side:
SideOld`), anything else addresses the new file. It then freezes the
snippet with `snippetFor` and records which hunk owned the range with
`hunkIndexFor`, before opening the composer.

The composer isn't a modal. It's overlay rows grown directly into the
diff, defined in `internal/editor/diffoverlay.go`. A `DiffOverlay`
attaches a block of `DiffOverlayLine`s **below** one diff row, and
`diffDisplayCells` in that file is the single mapping between screen rows
and diff rows — the renderer walks it to paint, and `DiffHitAt` walks the
same list to turn a click into a `(row, overlay-line, column)` triple. The
composer always renders below its anchor line, never on top of it, because
a note about a line must never hide that line.

`app/review.go`'s `composerLines` draws the composer as three rows: a top
border showing the kind and line range (`┌─ [ISSUE] L42 `), the editable
text field, and a footer spelling out the keys (`└─ Tab kind · Enter save
· Esc cancel`). Tab cycles the note's `Kind` through `KindNone →
KindIssue → KindSuggestion → KindQuestion → KindPraise` and back, via
`Kind.Next()`. Backspacing past the start of an existing note's text
deletes it — the gesture people already reach for, no separate delete key
needed.

`saveReviewComment` commits the composer's text into
`a.reviewBatch.Comments`. An empty note isn't stored: on a new comment
it's a cancel, on an edit of an existing one it's a delete
(`deleteReviewComment`). Comments that already have a marker draw as a
one-line dimmed row under their anchor (`markerLine`), so you can see what
you already said without reopening it; clicking a marker reopens it for
editing.

**Rendering the batch.** `Batch.Render()` in `internal/review/review.go`
sorts comments by repo, then file, then start line — the order the agent
will actually work through the files, not the order you happened to click
them — and produces plain text with no trailing newline (it's pasted into
a prompt, not written to a file). The legend line only lists kinds that
are actually used in this batch, so a batch of three ISSUEs doesn't waste
the agent's attention explaining SUGGESTION and PRAISE it never used. Here
is a real render of a two-comment batch, verbatim:

```
Please address these review comments.
Comment kinds: ISSUE (must fix), SUGGESTION (consider)

## Comments

1. **[ISSUE]** `internal/app/gitwrite.go:42`
   -	if err != nil {
   +	if err != nil && !isLockErr(err) {
   This swallows the lock-file case — put the isLockErr branch back.

2. **[SUGGESTION]** `internal/app/commitbox.go:88`
    	msg := strings.TrimSpace(a.commitValue)
   Trim trailing newlines too, not just surrounding whitespace.
```

Every comment's body is indented three spaces (`itemIndent`) under its
numbered heading — enough to align under `1. ` and stay readable past
nine notes. `Address()` formats the line reference: `file:88`,
`file:88-94` for a range, and old-side lines get a `~` on every number
(`file:~88-~94`) so a deleted line can never be mistaken for one you can
still go and look at.

**Sending it.** `Esc Enter` (Enter is deliberately the key: it already
means "send" everywhere else, and the batch is what you just finished
writing) calls `sendReview` in `app/review.go`. If Vincent isn't running
inside herdr (`review.Available()` checks the `HERDR_ENV` environment
variable herdr sets in every pane it owns), or `herdr agent list` returns
no agents, or herdr fails outright, the batch goes to the system
clipboard instead over OSC 52 (`internal/clipboard`) and the batch is
**kept**, not cleared — you have the text, but nothing was delivered yet.
One available agent gets it directly; more than one opens a picker modal
(`openReviewPicker`) so you choose. `internal/review/herdr.go` makes the
actual calls, three of them, in order:

```
herdr agent list                     — who is out there
herdr pane send-text <pane> <text>   — stage the batch, no Enter
herdr agent focus <pane>             — bring it into view
```

`herdr agent prompt` is never used, on purpose: verified against a live
herdr 0.8.2, it appends an encoded Enter after a short delay, which would
submit your review unattended. Vincent's whole premise is a human between
the note and the agent seeing it, so `pane send-text` stages the text and
stops — you press Enter yourself, optionally after typing one more
sentence of context.

Both `pane send-text` and every review batch go through `review.Wrap`,
which frames the text as a bracketed paste (`\x1b[200~...\x1b[201~`) so
it's inserted verbatim into whatever input mode the agent's CLI happens to
be in, rather than executed as keystrokes — which matters if that CLI is
sitting in something like a vim-style normal mode. `review.Sanitize`
strips every occurrence of the paste terminator byte by byte before
wrapping, rebuilding the string one byte at a time and re-checking the
tail after each removal, because a snippet pulled from a real file could
legitimately contain that exact byte sequence and would otherwise close
the paste frame early.

The batch is cleared only after `review.Send` returns `nil` — "consume on
success". A closed pane, a wedged herdr daemon, or any other failure
leaves `a.reviewBatch.Comments` untouched and flashes one plain sentence;
the real error, herdr's JSON envelope included, goes to
`~/.config/vincent/herdr.log` via `installReviewLog` in
`internal/app/reviewlog.go`, never to stderr, because a raw-mode TUI owns
the whole terminal and anything written to stderr would sit painted over
the screen until the next repaint.

**Staleness.** `markStaleComments`, called from the git-status refresh,
flags a comment `Stale` when its file has left the current changeset
(reverted, committed, renamed away) and un-flags it if the file comes
back. It never touches line numbers or the snippet — a stale note is
still exactly the note you wrote about exactly the code in its snippet.
The lookup key joins repo and relative path with a NUL byte (`staleKey`)
so two repos holding the same relative path can't shadow each other's
freshness.

## Why it is built this way

CLAUDE.md names two rules directly and both are enforced in code, not
just described in a comment. **Consume on success** — `sendReviewTo` only
clears the batch after `review.Send` returns `nil` — exists because a
closed pane would otherwise silently eat a review with no way to know it
happened. **Never rebase line numbers** is why `Comment.Start`/`End`/
`Hunk`/`Snippet` are frozen at write time, and why `anchorRowFor` re-finds
the *screen row* for a marker every frame rather than ever moving the
comment to fit new code.

Splitting `internal/review` (pure, no tcell) from `internal/app/review.go`
(the app half) is deliberate: the wire format is the one thing outside
Vincent that has to parse it — the agent reading the batch — so it has to
be testable to the byte, independent of any UI machinery.

`herdr agent prompt` being off-limits is a hard-won fact, not a guess:
CLAUDE.md and the file's own header both say the behavior was verified
against a real herdr 0.8.2 binary. Using it would have turned a "human
hits send" tool into one that occasionally auto-submits, silently, which
is the opposite of what Vincent is for.

The composer growing overlay rows instead of floating a modal is
inherited from tuicr's `comment_panel.rs` shape (see the file header): a
modal covering the line would hide the exact code you're commenting on,
and Vincent's whole job is to make that code easy to keep looking at
while you write about it.

## What can go wrong

**Sending fails and you see "herdr did not answer" or "no agent in this
workspace — copied to clipboard instead".** Either way the batch is
intact and now also on your system clipboard; nothing was lost. Check
`~/.config/vincent/herdr.log` for the real herdr error if it keeps
happening.

**A note reads "(stale)" in a dimmed color.** The file it was about left
the changeset — reverted, committed, or renamed. The note's text and
snippet are unchanged; delete it yourself if it's no longer useful, or
send it anyway — a stale note about a decision already made can still be
worth reading.

**Two repos under one root both have `src/main.go`, and you'd expect a
marker to leak from one file's diff into the other's.** It doesn't:
`buildDiffOverlays` matches a comment to a diff tab by `File` **and**
`Repo` together. Worth knowing precisely because it's exactly the kind of
thing that would be a real bug if this match ever loosened to `File`
alone.

**A picker with more than one agent opens and you're not sure which is
which.** `Target.Status` (herdr's own `agent_status`) is shown next to
the name specifically because "which of these two Claudes is the one I
was watching" is usually answered by which one is still `working`.

**You click "Send to agent" with an empty batch.** `sendReview` and
`copyReview` both flash "No review notes · Esc r on a diff line" and do
nothing else — there's no way to send an empty batch by accident.

## Not covered here

The diff view itself — how rows get their `Kind`, `Old`/`New` numbers, and
word-level tint — is `diff-viewer.md`. The Changes panel's footer, where
the review block sits stacked above the commit box, and the panel's own
click handling belong to `changes-panel-and-git-writes.md`. Multi-repo
path resolution (`reviewPathFor`, `reviewRepoFor`, `singleRepoMode`) is
covered in `multi-repo.md`, since it's shared machinery rather than
something specific to reviews.

Not verified on a terminal: what the composer, the markers, and the
picker actually look like painted on a real screen, and what herdr does
end to end with a live agent pane. The herdr protocol described here is
read from the source and from CLAUDE.md's account of it, not exercised
against a running herdr daemon.
