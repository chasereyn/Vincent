# The editor, and what happens when the agent writes under you

Vincent is a small editor, not an IDE — type, select, undo, find and
replace, save. Its real job is reading diffs, but sometimes a fix is
smaller than a review note and you just type it yourself. This page
covers that editing engine and, more importantly, what happens when the
agent you're reviewing rewrites the same file while you have unsaved
changes open in it.

## How it works

**The buffer and the tab.** `internal/editor/tab.go` defines `Tab`: a
`Path`, a `*Buffer` holding `[]string` lines, `Cursor` and `Anchor`
positions, scroll state, a `Dirty` flag, and cached syntax-highlight
styles. `Tab.Mode` is `""` for a normal editable file, `"image"`, `"diff"`,
or `"markdown"` for the other views — a new kind of view is a mode on the
same `Tab` type, not a new type, so the tab list, the tab switcher, and
modal routing all work on it for free. `ReadOnly()` is just `Mode != ""`:
anything that isn't a plain text tab refuses mutation. CLAUDE.md is
explicit that this must be the gate every new mutation checks, never
`IsImage()` alone — that was the actual bug phase 1 found, because a diff
tab carries the real file's `Path`, and `Tab.Save()` once checked only for
images, so saving a diff tab would have written diff text over the user's
source.

**Undo.** `internal/editor/undo.go` keeps a per-tab stack of full buffer
snapshots (lines plus cursor and anchor), not an operation log — simpler
to reason about, and plenty fast for terminal-editor file sizes. Typing
doesn't push one snapshot per keystroke: consecutive edits of the same
kind (`undoGroupTyping`, `undoGroupBackspace`, `undoGroupDelete`) inside a
500ms window (`undoCoalesceWindow`) coalesce into one undo step, so
undoing a 50-character word is one keypress, not fifty. Anything
structural — a paste, pressing Enter, deleting a selection, an explicit
cursor move — closes the current group so the next edit starts fresh
(`undoGroupStructural` never coalesces with anything). The stack caps at
`maxUndoEntries` (500), FIFO, so a long session can't grow it forever.
`Esc u`/`Esc U` are undo and redo.

**Indentation.** `internal/editor/indent.go` does two things: visual-column
math (a tab renders as a multi-cell stop — `TabStop` is 4 — so a rune's
screen column isn't its index in the line, and the renderer, cursor
placement, and mouse hit-test all go through `RuneVisualWidth` to convert
between the two), and indent-style detection, sampling a newly opened
file's existing indentation to decide whether pressing Tab inserts a
literal tab or `defaultSpaceIndent` (four spaces) — meant to avoid the
"spaces landing in a tab-indented file" bug. `Tab.InsertNewlineIndented`
(wired to Enter, `app.go:1326`) carries the current line's leading
whitespace onto the new line, as one undo step covering both the newline
and the indent together.

**Find and replace.** The matching logic is on `Tab` in
`internal/editor/find.go`: case-insensitive substring search over
rune-decoded lines (`FindAll`), deliberately no regex or whole-word
toggle for v1. `ReplaceCurrentMatch` replaces the current match by
selecting it and calling `InsertString` — which is what makes it one undo
step, since `InsertString` already records the pre-replace state through
`DeleteSelection` — then re-runs the search so the cursor lands on
whatever match now sits after the insertion point. `ReplaceAll` is
different on purpose: it applies every match **back to front** (last
match in the document first) so replacing one never shifts the
line/column of a match still waiting its turn, and it pushes exactly one
`undoGroupStructural` snapshot up front rather than going through
`InsertString`/`DeleteSelection` per match — forty individual undo
entries for a forty-match Replace All would be exactly the "undo forty
times" bug the coalescing model exists to avoid. Both refuse
(`ReadOnly()` check) on a read-only tab with a flash, while plain search
stays allowed there. The UI half — the "Find:" bar and its optional
"Replace:" row directly above the status bar — is `internal/app/find.go`;
`Esc /` opens it, and Tab reveals the replace row on first press
(`findReplaceVisible`).

**New file and Save As.** `Esc n` (`internal/app/newfile.go`) prompts for
a path and `createNewFile` resolves it, creates any missing parent
directories with `os.MkdirAll`, and creates the file with
`os.O_CREATE|os.O_EXCL` so it can never silently overwrite something that
already exists — hitting an existing path just opens it instead. This is
a deliberate divergence from spice-edit's deleted `fileops.go`, which
refused to create parent directories at all; an agent-reviewing tool
routinely wants a file inside a subdirectory that doesn't exist yet.
`Esc S` (`internal/app/saveas.go`) is Save As: it's refused outright on a
read-only tab (same reasoning as everywhere else — a diff tab carries the
real file's path), and if the target already exists, `saveActiveTabAs`
opens a confirm modal before overwriting rather than clobbering silently.
`Tab.SaveAs` (in `tab.go`) does the actual retargeting: `Path`, `Title`,
`Mtime`, and the undo-anchor snapshot (`undoOriginal`, the baseline
`CanRevert` and `DiskUnchangedSince` compare against) all move to the new
file in one call, and the tab is marked clean.

**The conflict model, in full.** This is the part CLAUDE.md calls out by
name as the fix for a real bug: `reconcileOpenTabsWithDisk`
(`app.go:790` originally, now around line 954) used to advance a tab's
`Mtime` on every external write and forget about it, so the very next
save would silently overwrite whatever the agent had just written. The
replacement is `Tab.Conflict`, a **sticky** boolean field on `Tab`
(`internal/editor/tab.go`). "Sticky" means it is cleared only by an
explicit user action — never by the passage of time, never by another
poll tick.

Every ten seconds (see `render-loop.md` for the poller itself),
`reconcileOpenTabsWithDisk` compares the file's on-disk mtime, read on a
background worker, against what the tab last recorded. For a tab with no
unsaved edits (`!tab.Dirty`), a newer mtime just reloads the buffer
silently — the normal case of watching an agent work. For a **dirty**
tab, the logic branches on content, not just on time:

1. If the tab is already `Conflict`, do nothing — it's already flagged
   and already announced; re-checking every tick would just spam the
   read.
2. Otherwise, read the file and compare its bytes against
   `Tab.DiskUnchangedSince` — the snapshot taken when the file was opened
   or last reloaded (`undoOriginal`, from `undo.go`). If the bytes match
   that snapshot exactly, nothing actually changed — a tool bumped the
   mtime without touching content (a `gofmt -w` over an already-formatted
   file is the canonical example) — so the tab just takes the new mtime
   and moves on. This byte comparison is not an optimization; the
   comment on it is explicit that a conflict warning which fires on every
   harmless mtime bump is one the reviewer learns to ignore, which would
   defeat the whole model.
3. Only if the bytes genuinely differ does `tab.Conflict` become `true`,
   with one flash: `"<file> changed on disk — save will ask before
   overwriting"`.

Crucially, **a conflicted tab keeps its old `Mtime`** — the flag is what
suppresses the re-flash, not the timestamp. Advancing `Mtime` here is
exactly the bug this replaced: it would have erased the only record that
a conflict existed, letting the next save through with no prompt. Leaving
`Mtime` behind has a second payoff: if the user undoes their way back to
the exact original content, `clearConflictIfClean` drops the conflict,
and the next poll tick sees the disk as still newer and reloads it
normally.

While `Conflict` is set, `Tab.Save()` refuses outright, returning the
sentinel `editor.ErrDiskConflict` — it does not attempt to merge or
guess. `saveTabAt` in `app.go` catches that and calls
`openConflictPrompt` (`internal/app/conflict.go`), which reuses the
dirty-close modal's button machinery (same keyboard routing, same
hit-testing, same painter, just different labels) to offer four choices:

- **Cancel** — first in the list, because it's the only one of the four
  that cannot lose work, and focus starts there.
- **Show diff** — opens a **frozen** diff tab (`DiffFrozen = true`)
  comparing the in-memory buffer (old side) against what's actually on
  disk (new side), built entirely in-process by `diff.Unified` — no temp
  file, no `git diff --no-index` shell-out, and critically, no
  requirement that the file even be inside a git repository at all. The
  frozen flag is what stops the reconcile loop or `openDiff` from
  silently swapping this comparison back to the ordinary git diff on the
  next tick.
- **Reload** — `Tab.Reload()` throws away the buffer's edits and resets
  the undo stacks; there is no getting them back, which is correct for a
  reload asked for by name, and the flash says so ("your edits were
  discarded").
- **Overwrite** — goes through `Tab.SaveOverwrite()`, a distinct method
  from `Save()` specifically so the explicit call is the record that the
  user chose this outcome, rather than routing through the method that
  normally refuses.

The red dot: the tab bar paints a tab's dirty dot in `theme.Conflict`
(a red, `0xef7177`) instead of the ordinary dirty color whenever
`tab.Conflict` is set, and the status bar names the state directly, so it
outlives the three-second flash that first announced it.

## Why it is built this way

CLAUDE.md's own account of this is unusually direct: `reconcileOpenTabsWithDisk`
"flashes once when the agent rewrites a dirty file, then advances
`tab.Mtime` and forgets. The next save silently overwrites the agent's
work." The fix — sticky flag, byte comparison, red dot, refuse-with-choices
— exists entirely because Vincent's normal operating condition is "an
agent is actively rewriting the file you have open," which is precisely
the scenario every other editor's naive mtime check gets wrong.

The byte comparison specifically exists because the failure mode of *not*
having it is worse than the failure mode it prevents: a false positive
(warning on a no-op write) trains the reviewer to click through the
prompt without reading it, which is worse than never having the prompt.

Building "Show diff" in-process with `diff.Unified` rather than shelling
out follows non-negotiable 3 (single static binary, no runtime
dependency on `git diff --no-index` for this path) and pays off directly:
it's the only way to answer "did the agent touch the lines I touched" for
a file that isn't even in a git repository yet.

## What can go wrong

**A tab's dirty dot turns red and the status bar says "changed on disk."**
Save is now refused until you pick one of the four choices. Nothing is
lost yet — the buffer and the file on disk both still exist untouched.

**You pick Reload and then realize you wanted your edits back.** Too
late — `Tab.Reload()` resets the undo stack along with the buffer, by
design, because "reload" is an explicit request to discard.

**"Show diff" says the file "matches the version on disk."**
`bufferVsDiskRows` returns `ok=false` when `diff.Unified` produces no
hunks at all, meaning your buffer and the disk file are now byte-identical
— nothing to compare, so no diff tab opens.

**A conflict never clears even after you fix things by hand.** It clears
only when the buffer returns to byte-identical with the on-open snapshot
(`clearConflictIfClean`) or through an explicit Overwrite/Reload — editing
around the conflict without matching the original exactly won't clear it.

**New File on an existing path doesn't create anything.** That's correct
behavior, not a bug: `createNewFile` never clobbers, so hitting an
existing path just opens it and flashes "already exists, opening it."

**Save As silently overwrites something.** It shouldn't — `saveActiveTabAs`
always opens a confirm modal first when the target already exists. If you
see it happen without a prompt, that's the one thing here that would be a
real regression.

## Not covered here

The diff renderer itself — how `diff.Unified`'s output gets parsed and
painted — is `diff-viewer.md`; this page only covers where it's used
(Show diff) and why it runs in-process. The render loop that drives the
ten-second poll behind all of this, and the custom-event pattern that
gets a background stat result onto the UI thread, is `render-loop.md`.
Syntax highlighting (`internal/editor/highlight.go`) and the markdown
render mode are not covered here at all — the latter is `markdown.md`.

Not verified on a terminal: how the conflict prompt's four-button modal
actually reads and feels under real keyboard and mouse input, and whether
the 500ms undo-coalescing window feels right at typing speeds faster or
slower than whatever it was tuned against.
