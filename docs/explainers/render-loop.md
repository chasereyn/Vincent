# The render loop

This is why Vincent doesn't flicker when you move the mouse over it, and
why a running agent hammering a repo doesn't freeze the pointer. Both
problems have the same root cause — real work happening on the thread
that's supposed to be answering "where did the mouse go" — and both are
fixed by keeping that thread free.

## How it works

**The flicker, and why it happened.** Vincent asks the terminal for
any-motion mouse reporting (xterm mode 1003), so moving the pointer across
the editor pane produces roughly one event per crossed cell — around 60 a
second. Every Vincent painter fills its whole rectangle with spaces and
then draws glyphs on top of that, which defeats tcell's own dirty-cell
tracking: from tcell's point of view, every cell changed, even when the
frame is byte-identical to the one before it. The file comment on
`internal/app/frame.go` gives the measured cost — roughly 56 KB of escape
sequences per frame at 200x50, against tcell v2.13.9. Inside Herdr, whose
PTY reader consumes 8 KB at a time on a 16ms render clock, an oversized
frame gets sampled mid-write and the pane visibly tears.

**The fix is to not send the frame, not to make it cheaper.** That's
`frameKey`, a big comparable struct in `frame.go` holding every piece of
state a pure-motion mouse event is allowed to change: which modals are
open, their hover index and scroll position, the active tab's cursor and
scroll, the git panel's entry count and branch label, and so on. Two rules
make the skip safe:

- **Only a pure-motion event can be skipped.** `isPureMotion` checks that
  the event is a mouse event with no button held and no wheel notch
  (`tcell` folds the wheel into the button mask, so `ButtonNone` already
  excludes it, but the wheel is checked explicitly too as insurance
  against a future tcell reporting it separately). Every other kind of
  event — key, resize, wheel, click, drag, or one of Vincent's own posted
  events — always repaints. That keeps the skip logic from having to
  understand every possible state change in the app; it only has to
  understand "did this specific motion move anything visible."
- **The comparison is against the last *painted* frame, not the state
  right before this event.** This is what makes time-dependent content —
  the status bar's flash message, the armed-Esc-leader hint — work
  correctly. Both expire on a clock that posts no event of its own.
  Comparing "just before" to "just after" this one event would find both
  sides already expired and leave stale text on screen forever; comparing
  against the actual last-painted frame means the first motion event after
  either one lapses still triggers a repaint.

`handleEventForFrame` (`app.go`) wraps this: it runs `handleEvent`
unconditionally (so real logic always executes), then returns `true`
immediately for anything that isn't pure motion, or compares a fresh
`frameKey()` against `a.lastFrame` for motion events.

**`Run()`**, the main loop in `app.go`, blocks on `a.screen.PollEvent()`
and then drains whatever tcell has already queued before repainting —
`for !a.quit && a.screen.HasPendingEvent()`. This matters for the same
mouse-flood reason: a burst of forty motion reports arriving in one
16ms window costs one frame, not forty, because every event in the burst
is still handled individually and in order (so a key or resize buried in
the middle behaves exactly as it would alone), but the paint only happens
once at the end if anything in the batch was actually dirty.

**Every cell is written once, and the screen is never cleared.** `draw()`
deliberately has no `screen.Clear()` call — the comment explains why:
`Clear()` writes a space into every cell of tcell's back buffer, which
marks all of them dirty and throws away tcell's own record of what it
last sent, forcing a full retransmit even for an identical frame. Instead,
every region — tree, splitters, git panel, tab bar, editor, find bar,
status bar — fills exactly its own rectangle, and together they tile the
window with no gaps and no overlap, so the clear was redundant for
coverage and only ever cost bandwidth. A resize is the one case that
genuinely needs a full repaint, and `handleEvent`'s `EventResize` branch
calls `screen.Sync()` for that specific case.

**Background work never touches UI state directly.** The ten-second git
refresh (`internal/app/gitpoll.go`) is the clearest example of the pattern
CLAUDE.md calls out — "custom tcell events for goroutine-to-UI
messaging" — used everywhere in this codebase: auto-scroll, tree refresh,
finder rebuild, and the git poller all follow it. `startGitPoll` builds a
plain-value `gitPollRequest` on the UI thread (root directory, the
multi-repo registry as it stood, and one `gitPollTarget` per open tab's
path, each already resolved to which repo owns it and what pathspec to
use — `gitPathFor` runs here, not on the worker, because it needs
`a.repos`). `runGitPoll` is a free function that takes no `*App` at all —
"no App, by construction" is cheaper to keep honest than a comment saying
"don't touch `a.`" would be. It runs `loadReposSnapshot` (one `git
status` per repo, fanned out at most `repoStatusWorkers` at a time — see
`multi-repo.md`) and, per open tab, stats the file and runs whichever git
reads that tab needs (gutter markers for a text tab, a parsed diff for a
diff tab — both can be true for the same path, since a file and its diff
are two separate tabs). The whole result comes back as one
`gitPollEvent`, posted via `scr.PostEvent`, and `applyGitPoll` — reached
from `handleEvent`, back on the main goroutine — is the only place that
writes any of it into `App` state.

Two guards worth knowing: only one poll runs at a time
(`a.gitPollBusy`), refreshed forever unless it's been more than
`gitPollStuckAfter` (30 seconds) — an unconditional "never poll again
while one is in flight" guard would be a silent off switch if a
`PostEvent` ever failed (a full tcell queue, a screen torn down mid-test)
and left the busy flag permanently stuck; after the timeout, Vincent
assumes the previous poll is never coming back and launches anyway. And
`applyGitPoll`'s reconcile walks the **live** tab list and looks each tab
up in the poll's results, never the other way around — so a tab closed
(or opened) while the poll was in flight is simply absent from the join,
rather than causing a crash or a stale write.

The push (`Esc P`, see `changes-panel-and-git-writes.md`) uses the exact
same shape: a `gitPushEvent` posted from a goroutine, applied by
`applyGitPush` on the main thread only.

## Why it is built this way

The whole design answers one measured problem, not a hypothetical one:
the file comment on `frame.go` states the exact byte cost (~56 KB per
frame) and the exact symptom (Herdr's PTY reader sampling mid-write,
visible tearing) that motivated it. This wasn't precautionary engineering
— CLAUDE.md's phase 4 entry says the render loop shipped specifically to
fix a flicker a real session produced.

Keeping git reads off the UI thread is the direct answer to Vincent's
actual operating condition: the repo being reviewed is, by construction,
one an agent is actively writing to. `gitpoll.go`'s file comment spells
out the forks a single ten-second tick used to make on the main
goroutine — `rev-parse --show-toplevel`, `symbolic-ref HEAD`, `status
--porcelain`, plus a `git diff` per open tab — any one of which can block
on the filesystem or on git's own locking while another process holds it.
Blocking the event loop there means the pointer stops moving and clicks
queue up, in exactly the window Vincent exists to be usable in.

`frameKey` being one big struct with every relevant field spelled out,
rather than a scatter of per-feature dirty flags, is a direct hedge
against the failure mode CLAUDE.md is generally alert to across this
codebase: a flag has to be set at every mutation site, and it is silently
wrong the one day someone adds new UI state and forgets. A struct
comparison with `==` cannot be half-updated that way — the cost is that
every field must stay comparable (no slices, no maps), which is why
things like `finderCount` are stored as `len(a.finderResults)` rather
than the slice itself.

## What can go wrong

**A motion event over a modal with a moving hover highlight doesn't
repaint.** If a future modal's hover state isn't added to `frameKey`,
`frameKey()`'s two snapshots would compare equal even though the
highlighted row changed, and the screen would visibly lag the pointer
until some other event forces a repaint. This is the one class of bug the
comment warns is easy to introduce silently — a new picker or panel with
its own hover/scroll state needs its own fields added to the struct.

**The status bar's flash message or the armed-leader hint appears to
"stick" past its expiry until you move the mouse.** This is expected, not
a bug: nothing posts an event when a timer-based UI element expires, so
the next motion event is what notices and triggers the repaint that
clears it. A key press or click would also trigger it immediately.

**Git status looks stale by up to ten seconds, or a diff tab lags a
fast-writing agent.** That's the poll interval, not a failure — `render-loop.md`'s
whole design accepts up to that latency in exchange for never blocking
the UI thread on it.

**The poller appears to stop refreshing entirely.** Check whether
something is failing `scr.PostEvent` silently (shutdown, a torn-down
screen in a test) — `gitPollBusy` would stay stuck `true` for up to
`gitPollStuckAfter` (30 seconds) before the guard gives up waiting and
launches a new poll anyway.

## Not covered here

The specific git commands the poller runs, and how their results become
the Changes panel and gutter markers, are `changes-panel-and-git-writes.md`
and `diff-viewer.md`. The multi-repo fan-out inside `loadReposSnapshot` is
`multi-repo.md`. The conflict-detection logic that runs inside
`reconcileOpenTabsWithDisk`, called from `applyGitPoll`, is
`editor-and-conflicts.md`.

Not verified on a terminal: the actual frame rate and perceived
smoothness on a real terminal emulator under real mouse movement — the
56 KB figure and the flicker symptom come from CLAUDE.md's account of the
research that motivated this phase, not from a fresh measurement taken
while writing this page. Also not verified: how `-race` behaves under
sustained load with several background workers (poll, push, auto-scroll,
finder rebuild) all posting events in the same window, beyond what the
test suite already exercises.
