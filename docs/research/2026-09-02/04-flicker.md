# Herdr mouse flicker — root cause

## One sentence

Vincent asks the terminal to report **every** mouse move (xterm mode 1003), then
repaints the whole screen from scratch on each one — about **56 KB of escape
sequences per pointer twitch instead of 28 bytes** — and Herdr, whose PTY reader
takes 8 KB at a time and whose renderer is paced to a 16 ms clock, ends up drawing
frames while Vincent is only half-way through writing one.

## 1. Does Vincent redraw the whole screen on every motion event?

Yes. Every event, unconditionally.

`internal/app/app.go:679` — `App.Run`:

```go
for !a.quit {
    ev := a.screen.PollEvent()
    if ev == nil { break }
    a.handleEvent(ev)
    a.draw()          // line 690 — no condition
    a.screen.Show()   // line 691
}
```

There is no "did anything change" test. A pure-motion `*tcell.EventMouse`
(`Buttons() == ButtonNone`) walks all of `handleMouse`
(`internal/app/app.go:1083`) and changes at most **two integers**:

| What a pure-motion event can touch | Where | Visible? |
|---|---|---|
| `a.lastShiftAt` | app.go:1092 | no |
| `a.hoveredMenuRow` (menu open only) | `updateMenuHover`, app.go:1894 | yes |
| `a.gitPanelHover` (panel shown only) | `updateGitPanelHover`, gitpanel.go:244 | yes |

Everything else in `handleMouse` is gated on a button, a wheel, or an active
`dragMode`. So when the pointer crosses the editor pane — the common case — the
frame is byte-identical to the previous one and Vincent still repaints it.

### Exact call sites

| Call | Where | Notes |
|---|---|---|
| `scr.Clear()` | app.go:467, app.go:530 | once, in `New` / `NewSingleFile` |
| `a.screen.Clear()` | **app.go:2173**, first line of `draw()` | once per event |
| `a.screen.Show()` | app.go:682, **app.go:691** | once per event |
| `a.screen.Sync()` | app.go:705 | `EventResize` only — correct use |
| `screen.Fill` | never in production code | — |

`Sync()` is only used on resize. Good — that is the one place a full repaint is
warranted.

### The trap: `Clear()` is not the problem, and removing it will not help

I assumed `screen.Clear()` was the culprit and measured it. It is not, and the
reason matters for choosing a fix.

`Screen.Clear()` in tcell v2.13.9 is `baseScreen.Clear` → `Fill(' ',
StyleDefault)` (`screen.go:440`). It touches only the back buffer; it emits
**nothing** and does not set `tScreen.clear`, so no `ESC[2J` reaches the wire.
Only `Sync()` sets that flag (`tscreen.go:1078`).

What actually defeats tcell's dirty tracking is the **fill-the-rect-then-write-
glyphs idiom** every Vincent painter uses. `CellBuffer.Put` (`cell.go:67`) calls
`c.setDirty(true)` whenever the incoming grapheme differs from the cell's current
one, and `setDirty(true)` **erases `lastStr`** (`cell.go:30`). So:

1. `tab.Render` fills the editor rect with `' '` → differs from the glyph
   already there → cell marked dirty, `lastStr` wiped.
2. The glyph pass writes `'X'` back → differs from `' '` → dirty again.
3. `drawCell` sees `lastStr("") != currStr("X")` → re-emits the cell.

Net: **every non-blank cell is re-transmitted every frame**, whether or not
`Clear()` was called.

I measured this against real tcell v2.13.9 with a fake `Tty` that counts bytes
(`TERM=xterm-256color`, 200x50, five styles rotating every 8 columns — roughly
what Chroma tokens plus diff row/word tints produce):

```
Clear() + fill-then-glyph (what Vincent does today):  56619 bytes/frame
no Clear(), still fill-then-glyph:                    56619 bytes/frame
no Clear(), one SetContent per cell:                     28 bytes/frame
```

Probe kept at
`scratchpad/tcellprobe/main.go`. 56,619 bytes × 60 motion events/sec ≈ **3.4 MB/s
through the PTY**. The 28-byte figure is the sync-begin / cursor-hide / sync-end
wrapper with zero cell writes.

Multi-pass painters confirmed:
- `internal/editor/tab.go:606-611` full-rect space fill, then `:637-640` per-row
  space fill on top, then `:665-712` glyphs. Three passes over the cursor row.
- `internal/filetree/filetree.go:221-225` full-rect space fill, then rows.
- `internal/app/gitpanel.go:277-281` full-rect space fill, then rows.
- `internal/app/app.go:2404-2408` (`drawEmptyEditor`), `:2419-2422`
  (`drawStatusBar`), `:2276-2279` (`drawTabBar`) — same shape.

Because every region fills its own rect, `a.screen.Clear()` at app.go:2173 is
already redundant. Deleting it is harmless but buys nothing.

## 2. Does the main loop coalesce events?

No. One `PollEvent` → one `draw()` → one `Show()`. The queue is never drained.

tcell exposes `HasPendingEvent()` in the `Screen` interface (`screen.go:126`,
implemented at `screen.go:519`), so coalescing is available and unused.

Vincent also *opts in* to the flood. `internal/app/app.go:463` and `:526`:

```go
scr.EnableMouse(tcell.MouseButtonEvents | tcell.MouseDragEvents | tcell.MouseMotionEvents)
```

`MouseMotionEvents` (`screen.go:329`) makes tcell emit `ESC[?1003h`
(`tscreen.go:820-822`) — any-motion tracking. The terminal then reports a
position for every cell the pointer crosses, with no button held. That is the
event source; nothing throttles it.

## 3. Does tcell v2.13.9 support mode 2026, and does Vincent enable it?

Yes, and yes — automatically, with no Vincent code involved.

- `tscreen.go:314-320`: `if t.startSyncOut == "" && t.ti.XTermLike { startSyncOut
  = "\x1b[?2026h"; endSyncOut = "\x1b[?2026l" }`.
- `XTermLike` is forced true for any `TERM` beginning `xterm`
  (`tscreen.go:358-362`).
- Herdr sets `PANE_TERM = "xterm-256color"` (`herdr/src/pane.rs:56`, applied at
  `pane.rs:64`). So the branch is taken.
- `tScreen.draw()` (`tscreen.go`) wraps every frame:
  `startBuffering()` → `hideCursor()` → cells → `showCursor()` → `endBuffering()`.

My probe confirms the bytes on the wire:

```
frame starts with 2026h: true
frame contains 2026l:    true
frame contains ED (ESC[2J full clear): false
frame contains ?25l: true
```

So synchronized output is on. That is why the flicker is intermittent and
tearing-shaped rather than a solid full-screen blink.

Note in passing: tcell hides the cursor at the head of every frame and re-shows it
at the tail. Vincent does display a real hardware cursor in a text tab
(`internal/editor/tab.go:744` `scr.ShowCursor(cx, cy)`), so each frame carries a
`?25l` … `?25h` pair. Inside the 2026 block the outer terminal should never see
the intermediate state.

## 4. How Herdr handles a client that clears and repaints

Herdr is a client/server split. The server owns a libghostty VT per pane
(`herdr/vendor/libghostty-vt`), renders a ratatui buffer, and streams it; the
client turns that buffer into ANSI for the real terminal.

**It does honour mode 2026 from the guest.** `herdr/src/pane/terminal.rs:1399-1431`:

```rust
let synchronized_output = core.terminal
    .mode_get(crate::ghostty::MODE_SYNCHRONIZED_OUTPUT)   // 2026, src/ghostty/mod.rs:179
    .unwrap_or(false);
...
let request_render = !synchronized_output;
```

While the guest is inside a 2026 block, PTY-driven render requests are dropped.
There is a test pinning it (`terminal.rs:5482`).

**It does not render partial frames on its own account.** The client diffs each
frame against the previous one (`write_changed_cells`,
`src/protocol/render_ansi.rs:684`) and wraps its output in
`ESC[?2026h ESC[?25l … ESC[?2026l` (`render_ansi.rs:596`, `:644`, `:665-704`). A
repaint of identical content produces an identical ratatui buffer, an empty diff,
and no bytes out. Herdr absorbs Vincent's redundant cell writes — it does not
forward them.

**Cursor visibility is where it leaks.** `src/ui/tab_surface.rs:161-169`:

```rust
let runtime = app.runtime_for_pane_in_workspace(...)?;
if runtime.synchronized_output_active() {
    return None;      // no cursor for this frame
}
```

Same guard at `src/server/render_stream.rs:395` (`suppress_cursor`) and
`src/server/headless/retained_surface.rs:170`. When the server renders a frame
while a pane is mid-2026-block, that pane contributes **no cursor**, and the
client then writes a bare `ESC[?25l` (`render_ansi.rs:816-829`,
`write_host_cursor_state`) — unconditionally, every frame, visible or not.

Two facts make that guard fire constantly under Vincent:

1. **The PTY reader takes 8192 bytes per read** (`src/pty/actor.rs:225`,
   `src/pty/actor/unix.rs:820`). A 56 KB Vincent frame arrives as **seven
   separate `process_pty_bytes` calls**. For six of them mode 2026 is still set.
2. **Renders are paced, not frame-aligned.** `MIN_RENDER_INTERVAL = 16 ms`
   (`src/app/mod.rs:36`, used at `src/app/runtime.rs:76-84`,
   `:143`). A render deferred by the pacer refires off a `tokio::select!`
   timer deadline (`src/server/headless.rs:604-635`, `LoopEvent::Timer`) with no
   knowledge of where Vincent is in its frame.

So Herdr renders on a 16 ms clock, and Vincent holds mode 2026 set for a large
fraction of every 16 ms window. Renders that land inside the window drop the
cursor; the next one restores it. At 60 motion events/sec that alternation is a
rapid on/off — read as flicker.

Herdr also always requests any-motion from the *outer* terminal
(`src/terminal_modes.rs:9` — `ESC[?1000h ESC[?1002h ESC[?1003h ESC[?1006h`), so
it receives the motion spam regardless of the guest. It only **forwards** motion
to a pane whose VT has mode 1003 set (`src/pane/terminal.rs:2023-2026`,
`require_any_motion`). Vincent asks for 1003; herdr-file-viewer does not.

## 5. Why herdr-file-viewer does not flicker

Two independent reasons, either of which alone would be enough.

**It never receives pure-motion events.** `herdr-file-viewer/src/app.rs:204` and
`:827` use crossterm's `EnableMouseCapture`, which enables modes 1000, 1002,
1015 and 1006 — **not 1003**. Herdr's forwarding gate
(`pane/terminal.rs:2026`) therefore drops motion reports before they reach it.
`Moved` is explicitly treated as inert in its own handlers
(`src/controller/help.rs:75`, `src/controller/finder.rs:96`; `mouse.rs` has arms
only for `Down`/`Drag`/`Up`/`Scroll`).

**It draws only when a handler says the frame changed.**
`herdr-file-viewer/src/app.rs:243-261`:

```rust
let mut dirty = true;               // paint the first frame
loop {
    if dirty {
        terminal.draw(|frame| { ... })?;
        dirty = need_redraw;
    }
    ...
    dirty |= fx.redraw;             // every handler returns fx.redraw
}
```

The comment above it names the intent: *"Drawing only when `dirty` avoids
re-walking the filesystem."*

And ratatui's own model is idempotent where tcell's is not: `Terminal::draw`
renders into a fresh buffer and diffs it against the previous one, so a
fill-then-write pass costs nothing on the wire. Vincent's tcell painters mutate a
persistent cell buffer, and each mutation trips the dirty bit.

## The fix

**Smallest change that removes the flicker: stop drawing when nothing changed.**

File `internal/app/app.go`, function `Run` (line 679). Give `handleEvent` a way
to say "no visible change" and skip both `draw()` and `Show()`. The only visible
state a pure-motion event can alter is `a.gitPanelHover` and `a.hoveredMenuRow`,
so the test is small — snapshot those two before `handleMouse` and compare after:

```go
for !a.quit {
    ev := a.screen.PollEvent()
    if ev == nil { break }

    if me, ok := ev.(*tcell.EventMouse); ok && me.Buttons() == tcell.ButtonNone {
        hoverBefore := [2]int{a.gitPanelHover, a.hoveredMenuRow}
        a.handleEvent(ev)
        if [2]int{a.gitPanelHover, a.hoveredMenuRow} == hoverBefore &&
            !a.leaderArmed() && !time.Now().Before(a.statusUntil) {
            continue          // nothing on screen would differ
        }
    } else {
        a.handleEvent(ev)
    }

    // Coalesce: if more input is already queued, let it land first.
    if a.screen.HasPendingEvent() {
        continue
    }
    a.draw()
    a.screen.Show()
}
```

Two things in one:

- **The hover guard** kills the 56 KB frame for motion over the editor, the tree
  and the tab bar — most pointer travel.
- **The `HasPendingEvent()` drain** collapses a burst of motion reports (and
  wheel events) into a single frame, so even hover changes cost one repaint per
  batch rather than per report. `HasPendingEvent` is already in the `Screen`
  interface (`screen.go:126`); no new dependency.

The `leaderArmed()` / `statusUntil` terms are needed because the status bar has
time-dependent content (`drawStatusBar`, app.go:2424, reads `time.Now()` at
`:2451` and `:2456`). Without them an expiring flash could sit on screen until
the next event.

Cheap complement, same file: drop `a.screen.Clear()` at **app.go:2173**. It is
already redundant — every painter fills its own rect — and it is one less full
pass over 10,000 cells. It does **not** fix the flicker on its own; my
measurement above shows 56,619 bytes either way. Do not ship it as the fix.

### Second hypothesis, if the guard is not enough

If flicker survives the hover guard, the residual is the **cursor-suppression
race** described in §4: real state changes (scroll, a click, a diff tab
refreshing under a running agent) still emit 56 KB frames that span ~7 PTY reads
and multiple 16 ms Herdr render windows, so `tab_surface_cursor` keeps returning
`None` mid-frame and the cursor keeps blinking.

The fix for that is to make Vincent's frames small, which means making the
painters idempotent: **one `SetContent` per cell per frame, carrying its final
rune and style**, instead of fill-then-overwrite. Concretely, in
`internal/editor/tab.go:606-712`, compute each row's final `(rune, style)` into a
reusable `[]cell` scratch slice and write it once, dropping the two space-fill
passes. That takes the measured cost from 56,619 to 28 bytes per unchanged frame
and from ~56 KB to a few hundred bytes for a real one-line change. Same treatment
for `internal/filetree/filetree.go:221`, `internal/app/gitpanel.go:277`, and
`drawTabBar` / `drawStatusBar`. Bigger change; do it only if measurement says the
guard was insufficient.

I rate hypothesis 1 as the dominant cause with high confidence: the owner's
symptom is scoped to mouse *motion*, and motion is exactly the case where the
frame is provably identical and provably still transmitted in full. Hypothesis 2
explains why it is Herdr-specific rather than universal.

## Other waste per frame

Checked, and **clean** — these are not re-run on draw:

- **git**: no shell-out in `draw()`. `refreshGitStatus` (app.go:~440) and
  `refreshGitLineChanges` (app.go:467) run from the 10 s `treeRefreshEvent`
  (`treeRefreshInterval`, app.go:83) and from explicit actions only.
- **syntax highlighting**: memoised. `internal/editor/tab.go:589-595` re-runs
  `HighlightVisible` only when `StyleStale`, or `ScrollY`/`h` changed. The doc
  comment already calls out mouse moves as the reason.
- **file reads**: `reconcileOpenTabsWithDisk` stats open tabs on the 10 s tick,
  not per draw.

Real per-frame waste, in descending order:

1. **~30,000 `SetContent` calls per frame** for a 200x50 editor (full-rect fill +
   per-row fill + glyphs). Each one goes through `baseScreen.SetContent`
   (`screen.go:451`), which does
   `b.Put(x, y, string(append([]rune{mainc}, combc...)), style)` — **two heap
   allocations per cell** — and then `Put` runs
   `uniseg.FirstGraphemeClusterInString` on the result. At 60 motion events/sec
   that is roughly 1.8 M allocations/sec and 1.8 M grapheme segmentations/sec for
   a screen that did not change. This is the CPU half of the same bug.
2. **`gitSnapshot.filter` allocates twice per frame.** `gitPanelItems`
   (`gitpanel.go:62`) calls `Tracked()` and `Untracked()`, each of which builds a
   fresh `[]gitEntry` (`gitentries.go:88-96`). `gitPanelItems` is itself called
   twice per draw — `clampGitPanelScroll` (`gitpanel.go:164`) and
   `drawGitPanelList` (`gitpanel.go:311`) — so four slice builds per frame.
   Trivial next to item 1; worth folding into a cached list when the snapshot
   changes.
3. **`layoutTabs()` allocates a `[]tabRect` per frame** (`app.go:2237`). Noise.
4. **One `ioctl(TIOCGWINSZ)` per `Show()`.** `tScreen.Show()` calls `t.resize()`
   which calls `t.tty.WindowSize()` unconditionally. tcell's, not Vincent's, and
   negligible — but it is one more syscall per motion event.

## What I did not check

- **I did not reproduce the flicker.** No interactive TUI available in this
  session; everything above is read from source plus the byte-count probe.
- **I did not verify the 56 KB figure against Vincent's real painters.** The
  probe reproduces the *idiom* (fill, fill, glyph) at 200x50 with five rotating
  styles. Actual bytes depend on window size, how many distinct styles a real
  diff produces, and how long the SGR runs are. The 28-byte floor and the
  fill-then-glyph mechanism are exact; the 56 KB is representative, not measured
  from `vincent` itself.
- **I did not read crossterm's source** to confirm `EnableMouseCapture` omits
  1003 — the crate is not on this disk (`~/.cargo/registry` absent). I am relying
  on crossterm's documented mode set (1000/1002/1015/1006) plus the fact that
  herdr-file-viewer's handlers have no `Moved` arm at all. If crossterm 0.29 did
  enable 1003, §5's first reason falls and only the `dirty` flag explains it.
- **I did not trace whether Herdr's *client* (as opposed to server) issues a
  render on each motion event it receives from the outer terminal.** I confirmed
  the 16 ms pacer and the timer-driven refire, which is enough to establish that
  renders are not frame-aligned, but I did not enumerate every render trigger.
- **I did not measure how long a 56 KB write takes to cross the PTY**, i.e. how
  many 16 ms Herdr windows one Vincent frame actually spans. That would turn the
  cursor-suppression argument from plausible into quantified. macOS PTY buffers
  are small, so Vincent's single `buf.WriteTo(t.tty)` almost certainly blocks and
  the frame is spread over milliseconds — but I did not confirm it.
- **I did not check the Windows path** (`wscreen.go`), which has its own `clear`
  flag and does not use mode 2026 the same way.
- **I did not run `go test ./...`** — this was a read-only trace and I changed
  nothing that could break it.
- **I did not audit whether skipping the draw breaks any existing test** that
  assumes one repaint per event. `internal/app/app_test.go` calls
  `a.screen.Show()` directly in several places (`:1697`, `:1808`, `:1965`), so
  some tests drive `draw()` themselves rather than through `Run()`, but I did not
  read them all.
