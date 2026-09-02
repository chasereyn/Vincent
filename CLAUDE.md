# CLAUDE.md — Vincent

Read this before touching anything. It captures decisions that are not
obvious from the code, and several places where Vincent deliberately
disagrees with the codebase it was forked from.

## What Vincent is

**A mouse-first terminal client for reviewing code that AI agents wrote,
and correcting it in place.** File tree on the left, the file or its diff
in the middle, a Zed-shaped git panel on the right. You click lines in a
diff, write a review note, and one keypress delivers those notes back to
the agent that wrote the code. When a fix is smaller than a note, you type
it yourself and save.

Vincent is a **small editor, not an IDE**. Type, select, undo, find and
replace, save. No LSP, no autocomplete, no multi-cursor, no formatters.
Revision 1 of the plan said "not an editor" and scheduled the editing
engine for deletion; that deletion never happened, and on 2026-09-02 the
decision was reversed. The engine in `internal/editor` is byte-identical to
spice-edit's apart from the diff-mode additions. Diff and image tabs stay
read-only through `Tab.ReadOnly()`.

The person using Vincent mostly does not write code. Agents write it; he
reviews it and makes small corrections. Every design decision should
optimise for reading a diff quickly and getting feedback back to an agent.
Authoring is secondary and must never make reading worse.

## Provenance

Forked from [spice-edit](https://github.com/cloudmanic/spice-edit) at
`5b4adc5` (MIT, Copyright 2026 Cloudmanic, LLC). spice-edit is a terminal
*editor*; Vincent keeps its shell and deletes its editing half.

Upstream file headers still carry Spicer Matthews' authorship and the
Cloudmanic copyright. **Leave them.** That is the MIT attribution
requirement, not a leftover to tidy up. New files get a Chase Reynolds
header plus a note saying what upstream file they derive from, if any —
see `internal/app/pathops.go` for the pattern.

## Module

- Module: `github.com/chasereyn/vincent`
- Binary: `vincent`
- Version: `internal/version/version.go`, currently `0.4.0`

Bump the version when shipping a phase. There is no auto-update, so
`vincent --version` is the only way to tell whether the binary on PATH is
the one just built — and on Windows an install silently fails to replace a
running executable, which makes that a real question.

## Where it stands

Phases 0 through 5 and 6a landed on 2026-09-02 (version 0.3.0). The loop
closes: open a repo, `Esc g` for the Changes panel, click a file, read its
diff, `Esc r` on a line to write a note, `Esc Enter` to drop the batch
into the agent's prompt. None of the 2026-09-02 work has been seen on a
real terminal yet — it was built by four agents against simulation-screen
tests and merged by hand. **The first thing to do next session is run it
and look.** Then phases 3b (git writes, root switcher), 6b (find/replace,
new file), 7 (markdown), 8, 9.

- **Phase 3, the review loop.** `internal/review` holds the note model,
  the wire format, and the herdr client. A diff tab grows overlay rows for
  the inline composer and for saved-note markers (`editor/diffoverlay.go`).
  The batch sits in the git panel footer with Send and Copy. Failures go
  to `~/.config/vincent/herdr.log`, never stderr.
- **Phase 4, the render loop.** `app/frame.go` skips the repaint when a
  mouse-motion event changed nothing observable; `Tab.Render` writes each
  cell once and `draw` no longer clears the screen; `app/gitpoll.go` runs
  the ten-second git refresh on a worker and posts a `gitPollEvent`.
- **Phase 5, chrome.** Ayu Darker palette, Zed-style tree rows with indent
  guides and hover/selected fills, `Esc t` tab bar toggle (off by default),
  Revert and Toggle line comment demoted to the editor's right-click menu,
  capitalised plain identifiers coloured as types.
- **Phase 6a, editor safety.** Bracketed paste, and the conflict model in
  `app/conflict.go`: a sticky `Tab.Conflict`, a red dot, and a save that
  refuses with Overwrite / Reload / Cancel / Show diff.

- **Phase 2, the Changes panel.** Zed's shape, read-only: `Changes (N)`
  header, Tracked and Untracked sections, filename in its status colour
  with the parent directory dimmed beside it, deletions struck through, a
  repo / branch footer. Click a row to open its diff. `Esc g`.
- **Phase 1, inline diffs.** Dual old/new gutters, ± markers, full-width
  row tints, and a darker word-level tint on the part of a line that
  actually changed. `Esc d`, a file's right-click entry,
  or a click on a change bar in the editor's git gutter (which lands on
  that change in the diff). A diff tab refreshes itself when the file
  underneath changes, so it tracks a running agent without losing scroll.
- **Phase 0.** The fork builds, the suite is green on all three platforms,
  and every surface is black.

Already stripped:

| Gone | Why |
|---|---|
| `internal/format`, `app/format.go`, `app/formmodal.go` | format-on-save, formatter trust prompts, multi-field form modal |
| `internal/customactions`, `app/actionvars.go` | user-defined shell-outs |
| `app/fileops.go` | create / rename / delete. The copy-path half survives as `app/pathops.go` |
| brew formula, goreleaser, install.sh, website, samples | upstream distribution machinery |

**The editing engine stays.** `internal/editor/undo.go`, `comment.go`,
`indent.go`, and the mutation half of `tab.go` (`InsertRune`, `Backspace`,
`Delete`, dirty state, save) are live and wired: `handleKey` falls through
to `applyEditKey` (`app.go:1060`), `Esc s` saves, `Esc u`/`Esc U` undo and
redo. `Tab.Mode` plus
`Tab.ReadOnly()` (= `Mode != ""`) is the whole design: a text tab is
mutable, a diff or image tab is not. **Guard new mutations on
`ReadOnly()`, never on `IsImage()`** — that was the actual bug phase 1
found: `Tab.Save()` checked only for images, and a diff tab carries the
real file's `Path`, so saving one would have written diff text over the
user's source.

What the editor is missing, from `docs/research/2026-09-02/02-editor.md`:

- **Bracketed paste.** `EnablePaste()` is never called. A pasted escape
  byte arms the leader and the next `q` quits Vincent mid-paste. Fix this
  before any other editor work.
- **A conflict model.** `reconcileOpenTabsWithDisk` (`app.go:790-796`)
  flashes once when the agent rewrites a dirty file, then advances
  `tab.Mtime` and forgets. The next save silently overwrites the agent's
  work. Wanted: a sticky `Tab.Conflict`, a byte comparison against the
  open snapshot so mtime-only bumps are not conflicts, a red dot, and a
  save that refuses with Overwrite / Reload / Cancel / Show diff.
- Find and replace, new file, save-as, `SelectAll` wiring, triple-click
  line select, indentation carried on Enter.

## Architecture map

```
main.go                          CLI parsing — pure, testable, no tcell until the end
internal/app/app.go        2761  Event loop, layout rects, mouse dispatch, rendering
internal/app/modals.go     1257  Modal scaffolding; dirty/conflict buttons are data
internal/app/review.go     1130  Composer, saved-note markers, footer batch, send/copy
internal/app/gitpanel.go    516  The Changes panel: layout, rows, clicks, review footer
internal/app/finder.go      474  Fuzzy file-finder modal
internal/app/find.go        297  In-file find bar
internal/app/gitentries.go  258  THE git status parse — tree and panel both
internal/app/diffview.go    250  git diff shell-outs, diff tabs, live refresh
internal/app/gitstatus.go   225  Branch, gutter markers, dirty-folder rollup
internal/app/frame.go       219  frameKey: skip the repaint when motion changed nothing
internal/app/gitpoll.go     202  The 10s git refresh on a worker -> gitPollEvent
internal/app/conflict.go    179  Overwrite / Reload / Cancel / Show diff prompt
internal/app/cheatsheet.go  220  The Esc-? key table, generated from leader.go
internal/app/pathops.go     106  Copy relative / absolute path to clipboard
internal/app/leader.go      192  Esc key bindings + hints; leaderRows is THE key list
internal/app/reviewlog.go    ~60 review.Logf -> ~/.config/vincent/herdr.log
internal/review/review.go   335  Comment, Batch, Render (wire format), Sanitize/Wrap
internal/review/herdr.go    239  herdr agent list / pane send-text / agent focus
internal/editor/tab.go      985  Tab: buffer, cursor, scroll, hit-test, Conflict
internal/editor/diffview.go 411  Diff render: dual gutters, row + word tints, overlays
internal/editor/diffoverlay.go 204 Rows the app grows into a diff (composer, markers)
internal/editor/highlight.go 210 Chroma -> per-rune []tcell.Style grid
internal/filetree           736  Lazy tree, refresh, guides, hover, CollapseAll
internal/finder             583  Filename index (git ls-files) + fzy-style scorer
internal/diff               364  Unified-diff parser — no tcell, no git, pure
internal/icons              390  Nerd Font glyphs per file type
internal/theme              251  The palette. Ayu Darker on #030405
internal/config             133  ~/.config/vincent/config.json (icons, tabBar)
internal/clipboard           50  OSC 52 — works over SSH and through tmux
```

## Non-negotiables

These are hard requirements from the person this is built for. Do not
trade them away for convenience.

1. **`#030405` background, set explicitly**, never `tcell.ColorDefault` —
   that inherits the host terminal and will not reliably match. This is
   Chase's Ghostty background and the ground his Zed is re-based onto. The
   whole palette is his Zed: the Ayu Darker extension plus his
   `settings.json` overrides (text `#dfdeda`, tree text `#c3c2be`). The
   mapping table is in the roadmap artifact under "Palette". Translucent
   Zed values are pre-blended over `#030405` because a cell has no alpha.
2. **Mouse-first.** Herdr is the quality bar. Click, drag, and scroll work
   everywhere; keyboard is the supplement. This is the opposite of the
   usual TUI convention, so design it in rather than bolting it on.
3. **Single static binary.** No cgo, no runtime, no external binaries at
   run time. This is why highlighting is Chroma and not tree-sitter, and
   why the diff renderer must be in-process rather than shelling out to
   `delta`. `git` and `herdr` are the only exceptions — both are shelled
   out to deliberately.
4. **Four writes, all blunt.** Save a text buffer. `git checkout`.
   Commit-all (`git add -A && git commit -m`, tracked and untracked
   together). `git push`. Each was asked for by name. What is **not**
   coming: staging checkboxes, partial commits, amend, rebase, stash,
   discard. Anything finer than "commit everything" belongs to lazygit,
   one Herdr pane over. The Changes panel is where the pressure to add a
   checkbox will show up first.
5. **Esc leader only, and no menu.** No `Ctrl+` chords (see conventions
   below), and no Esc-Esc double-tap either — Chase never presses Esc by
   accident, so a single Esc arms the leader, one letter fires, Esc again
   cancels. **There is no ≡ action menu.** It went away on 2026-09-02
   after the first real session ("the Esc leader works great — the menu
   is not needed"): every row it carried was also a leader key, so it was
   a second code path into every action, a button in the tab bar, and a
   modal with hover and enable predicates, all to restate the key table.
   What replaced it is `Esc ?` — a read-only cheatsheet generated from
   `leaderBindings()` (see `cheatsheet.go`). Do not re-add a menu. If an
   action needs discovering, give it a leader key and it appears in the
   cheatsheet for free.

### 2026-09-02, after the first real session

Three changes came out of the owner using 0.3.0 on a terminal for the
first time. All three are decisions, not preferences — do not undo them
without asking.

- **The ≡ menu is gone.** See non-negotiable 5 above. `Esc ?` is a
  read-only cheatsheet generated from `leaderBindings()`; the status
  bar's armed-leader line comes from the same `leaderRows()`. That
  sharing is the point: the hardcoded status string had drifted to
  "f find · t tree" two renames after both keys moved, so the one piece
  of UI whose job is naming the keys was naming the wrong ones. **Add a
  binding to `leader.go` with a hint and a group and it documents
  itself in both places.**
- **`defaultSidebarWidth` is 60, not 30.** A reviewer reads paths out of
  that panel, not code, and at 30 cells a nested package name was clipped
  before the filename started. `clampStartupSidebar` caps it at 40% of
  the window on the **first sized frame only** — one shot, because
  re-applying it on every resize would quietly undo a splitter drag.
  `resizeSidebar`'s `minEditorAfterDrag` budget is a different rule for a
  different question (a deliberate drag keeps 40 columns of editor); note
  that on a 120-column window the two together cap the tree below its own
  default, which is the reflow doing its job.
- **`Esc z` folds the whole tree** (`Tree.CollapseAll`). Every file opened
  from the Changes panel or the finder calls `Tree.Reveal`, which expands
  ancestors, so a review session leaves the sidebar shaped like your
  history rather than like the project. Children stay loaded; the active
  folder moves up to the nearest still-visible ancestor.

## Build

```sh
make install      # build + copy to ~/.local/bin, which is on PATH
make build        # ./bin/vincent (vincent.exe on Windows)
make test         # go test ./...
make build-mac    # darwin/arm64 cross-compile
```

On the Mac, `go` is Homebrew's (`/opt/homebrew/bin/go`, 1.27 as of
2026-09-02) and `make` and `git` are Apple's. There is no dev server — it
is a TUI. To check UI behaviour, build and run it against a real directory.

The Windows notes below are kept because CI still runs Windows and Chase
may return to it. **On Windows, test build changes from PowerShell, not
just from Git Bash.** An agent's shell tool is usually Git Bash. Three
separate bugs shipped because of that gap, and none of them reproduce in
Git Bash:

- GNU make finds a POSIX shell on PATH under Git Bash and falls back to
  **cmd.exe** under PowerShell, where `mkdir -p bin` becomes "A
  subdirectory or file -p already exists". The Makefile now points SHELL at
  Git's own `usr/bin/sh.exe` and prepends that directory to PATH — make
  bypasses SHELL for commands with no metacharacters, so the coreutils have
  to be resolvable as executables too.
- **`HOME` is unset in PowerShell** (Windows uses `USERPROFILE`), so
  `$(HOME)/.local/bin` resolved to `/.local/bin` and the install failed far
  from its cause.
- `go build -o bin/vincent` produces an extensionless file that is a valid
  PE binary but that Windows refuses to execute, silently, because PATHEXT
  does not list it. `BINARY` carries `go env GOEXE` for this.

Related: **Windows locks a running executable**, so `make install` fails
while Vincent is open. That one is expected — the target explains it and
tells you to quit first.

`make test` runs **without** `-race` on purpose, so the default build stays
`CGO_ENABLED=0` and static. On the Mac, Apple clang is present, so
`CGO_ENABLED=1 go test -race ./...` (`make test-race`) works locally and
was green on 2026-09-02 after all four phases merged. On Windows it needs
`scoop install mingw`. CI runs `-race` on all three platforms either way.

This matters more than it looks: the goroutine-to-UI-thread pattern
(custom tcell events) is exactly the kind of code the race detector
catches problems in, and every new background worker — the git status
poller, the multi-repo indexer, the content-search pool — lands in that
pattern. Do not assume local green means race-free.

## Conventions inherited from upstream (keep them)

- **A doc comment above every function**, exported and unexported, saying
  why it exists. Project-wide; do not skip it.
- **One `_test.go` per source file**, same package (not `_test`), so tests
  can reach unexported helpers. Each `Test*` gets a doc comment saying what
  behaviour it pins.
- `t.TempDir()` for filesystem state. Never write into the repo.
- For drawing code, build a screen with
  `tcell.NewSimulationScreen("UTF-8")` and assert on `scr.GetContents()`.
- **No `Ctrl+` shortcuts.** They fight tmux and terminal emulators. Use
  `Esc`-prefixed leader keys instead (see `leader.go`).
- **Every right-click action must also have a leader key or live in the
  cheatsheet-visible key table.** macOS Terminal under tmux swallows
  button 3. Right-click is a redundant shortcut, never the only path to
  something. The table is `leaderBindings()` and the cheatsheet
  (`Esc ?`) renders it, so a binding cannot ship undocumented — but an
  action reachable *only* by right-click has no entry in either and is
  therefore invisible. The four right-click-only actions today are
  Revert file, Toggle line comment, Collapse all folders, and the
  copy-path pair; all four are listed here on purpose.

## Patterns worth preserving

- **`cursorMoved` flag (`tab.go`).** `EnsureVisible` only fires when
  something actually moved the cursor. Calling it unconditionally
  re-introduces the "scroll yanks back on every tick" bug.
- **Custom tcell events for goroutine to UI messaging.** Background work
  posts `autoScrollEvent` / `treeRefreshEvent` / `finderRebuiltEvent` onto
  the tcell queue and the main loop handles them. Never mutate UI state
  from a goroutine. The git status poller and multi-repo indexer should
  use exactly this.
- **Identity-preserving tree refresh (`filetree.go`).** `reload` matches
  survivors by name and keeps their `*Node` pointers, so open folders stay
  open across a refresh. This is what makes auto-refresh non-jarring.
- **Viewport-bounded highlighting (`highlight.go`).** Only a 256-line lead
  above and below the viewport is tokenised, so scrolling a huge file stays
  O(viewport). Keep this when the diff viewer lands — agent diffs get big.
- **Drag-mode state machine (`app.go`).** One `dragMode` string field
  distinguishes splitter-drag from selection-drag. Add the git panel's
  border drag to it rather than inventing a parallel flag.
- **`Tab.Mode` for non-text tabs (`image.go`, `diffview.go`).** A new kind
  of view is a mode on `Tab`, not a new type — it inherits the tab list,
  the switcher, and modal routing for free. `Render` and `HitTest`
  dispatch on it; `ReadOnly()` gates every mutation.

## Terminal quirks already solved here

Each of these cost someone real time to find. Do not regress them.

- **Zellij** sends Shift as a separate zero-button event *before* the wheel
  event. `handleMouse` tracks a sticky shift window to bridge it.
- **macOS Terminal + tmux** swallows right-click — hence the
  leader-key-or-cheatsheet rule above.
- **Shift+mouse must no-op** so the terminal's own native selection and
  copy still work.
- **A raw-mode TUI does not die with its terminal.** tcell puts the console
  in raw mode — correct, since a TUI wants Ctrl+C as a keypress rather than
  as "terminate" — but that opts the process out of console control events,
  and the main loop then blocks in `PollEvent` on a console that is gone.
  Closing the terminal left `vincent.exe` running forever, which on Windows
  also locks the binary so the next `make install` fails. `shutdown.go`
  handles the signals AND hard-exits after a grace period; the second half
  is not optional, because taking over SIGTERM without guaranteeing an exit
  converts a reliable kill into a hang.
- **Esc-leader needs a visible armed state and a generous window.** At
  spice-edit's 500ms, `Esc q` routinely failed to quit: that is a typist's
  reflex window, and Vincent is read one-handed with the other on the mouse.
  A leader that silently expires reads as a broken keybinding. It is 1500ms
  now and the status bar says when it is armed.

Chase does **not** run Vincent under tmux — it gets a full monitor of its
own, next to herdr. So the tmux-specific constraints are belt-and-braces
rather than load-bearing. Keep them anyway: the no-`Ctrl+` rule and the
right-click mirroring rule both hold for bare terminal emulators too, and
macOS Terminal is coming.

## Cross-platform

Developed on Windows through phase 2, on macOS (Apple Silicon) since
2026-09-02. CI runs Linux, macOS, **and Windows**.

Upstream tested Linux and macOS only, and eighteen of its tests failed on
Windows — every one a POSIX path literal baked into a test assertion, not a
real bug. Those are fixed here. **Never write `"/tmp/repo/file.go"` in a
test**; build paths with `filepath.Join` so the assertion uses the
platform separator. Same for `os.UserHomeDir`, which reads `USERPROFILE`
on Windows and `HOME` elsewhere.

`.gitattributes` pins everything to LF. Without it git's autocrlf rewrites
Go sources on checkout and `gofmt -l` flags every file.

## Roadmap

Full plan, with the research behind each decision:
<https://claude.ai/code/artifact/f4e134ac-f25c-4f7f-8e85-0658e4a18a27>

Revised 2026-09-02 (revision 2). The research behind the revision is in
`docs/research/2026-09-02/` — four reports covering the review loop, the
editor, chrome and markdown, and the flicker. Read the relevant one before
starting its phase.

| Phase | What | Status |
|---|---|---|
| 0 | Fork, strip, blacken | **done** |
| 1 | Inline (Zed-style) diff viewer | **done** |
| 2 | Zed-shaped read-only git panel | **done** |
| 3 | Review notes + herdr/clipboard handoff, legend in the batch | **done**, unseen on a real terminal |
| 3b | Git writes off the panel footer: checkout, commit-all, push. Root switcher (`Esc o`) | next |
| 4 | Render loop: skip no-op motion frames, drain events, git tick off the UI thread | **done** |
| 5 | Chrome: Ayu Darker palette, Zed-style tree rows, indent guides, tab bar toggle, menu trim, `NameOther` colouring | **done** |
| 6a | Editor safety: bracketed paste, conflict model | **done** |
| 6b | Editor: find/replace, new file, save-as, Myers diff | |
| 7 | Markdown renderer (goldmark AST -> tcell, a `Tab.Mode`) | |
| 8 | Multi-repo workspace + content search | |
| 9 | Ship: README, releases via Actions, lock contributions, explainers | |

Phases 4 and 5 do not depend on 3 and can run in parallel with it.

**Reference repos** live at `~/Developer/vincent-refs/`, one shallow clone
per folder: spice-edit, herdr, herdr-file-viewer, herdr-reviewr,
herdr-sidebar, herdr-lazygit, hunk, lazygit, tuicr, ftdv, zed (sparse
checkout of the editor, git_ui, project_panel, theme, ui, markdown crates),
and claude-code (the plugins repo — it does not contain the CLI source).
They are reference only; never edit them. Older notes below that cite
`~/Downloads/<repo>` mean this directory now.

Split (side-by-side) diffs come after inline — inline first was an explicit
call. The split view is a second painter over the same `[]diff.Row`;
nothing in `internal/diff` should need to change for it.

### Phase 1 as built

Ported from herdr-sidebar's `src/diffview.rs`
(`~/Downloads/herdr-sidebar/plugins/herdr-sidebar/src/diffview.rs`) and
split in two: `internal/diff` parses, `internal/editor/diffview.go` draws.
In-process — nothing shells out to `delta`.

Things worth knowing before changing any of it:

- **A diff tab is a `Tab`, not a parallel type.** `Buffer.Lines` runs
  parallel to `DiffRows`, which is why scroll, clamp, hit-test, and the
  find bar work on a diff with no special cases in the app layer.
- **A file and its diff are two tabs sharing one `Path`**, told apart by
  `IsDiff()`. Anything that looks a tab up by path must say which it wants
  — `openFile` does, and it must stay that way.
- **`git diff <base>` where base is `HEAD`, or the empty-tree SHA in a repo
  with no commits.** Untracked files fall through to
  `git diff --no-index` against `os.DevNull`, but *only* after
  `git ls-files --error-unmatch` says the file is untracked — running the
  fallback unconditionally renders every clean file as one huge addition.
- **Word-level tint pairs runs of deletions with the run of additions that
  follows**, then compares common prefix/suffix. It is a heuristic, not a
  character diff; a pair sharing nothing is left untinted rather than
  claiming the whole line changed.
- `git diff` output ends in a newline, so the final element of the split is
  a terminator, not a line. Dropping it is what keeps the trailing line
  numbers correct.

`gitstatus.go` keeps `parseHunkHeader` / `parseDiffRange` for the editor's
gutter markers. Its `loadGitHunkPreview` / `parseGitHunkPreview` /
`lineInHunk` trio went away with the info-modal hunk preview the diff view
replaced. `openInfo` in `modals.go` is now unused in production — phase 2
wants it for herdr handoff errors, so it was left in place.

### Git panel — as built

Built ahead of the review composer, swapping the original phase 2/3 order.
The panel is the front door to a review — without it there is no "what did
the agent do" list and you hunt orange rows in the tree — and it gives the
composer a home instead of a floating modal invented for it.

Chase put Vincent and Zed side by side on the same repo and the panel was
the whole visible gap. This is what Zed draws, read off that screenshot,
and what Vincent's read-only version kept or dropped:

| Zed | Vincent |
|---|---|
| `Changes (5)` / `History` tabs | `Changes (N)` only — History is a later idea, not v1 |
| `± View Diff` · filter icon · `Stage All ⌄` | drop the whole toolbar row |
| `Tracked` section header | keep |
| row: filename, dimmed parent dir beside it | keep — the dimmed parent is what disambiguates two `index.ts` |
| deleted files struck through | keep, it reads instantly |
| per-row checkbox (staging) | drop |
| `Untracked` section header | keep |
| `⑂ Sarita / main` + `⟳ Fetch ⌄` | keep the repo/branch row; it becomes the branch switcher |
| commit message box | **this is where the review batch goes** |
| `Commit Tracked ⌄` | becomes `Send to agent` |
| last-commit-message recall row | drop |

The substitution in the last three rows is the point. Zed's panel ends in
"describe this change and commit it"; Vincent's ends in "describe this
change and hand it back". Same shape, same muscle memory, opposite
direction. Build the panel before the composer and phase 2 has a home
instead of needing a floating modal invented for it.

Row colours follow the tree's `GitChangeKind` palette so a file is the same
colour in both places. Clicking a row opens that file's diff.

Things worth knowing before changing it:

- **One `git status` run feeds both the tree and the panel.** The tree wants
  a path -> kind map, the panel wants an ordered list; that difference is
  exactly what tempts you into a second run and a second parser, and then
  they drift and a file is orange in one place and absent from the other.
  `gitentries.go` is the single parse. The older `parsePorcelain` was
  deleted when the panel landed rather than left to rot beside it.
- **`-z`, `--untracked-files=all`, `GIT_OPTIONAL_LOCKS=0`.** All three are
  from the traps list and all three are load-bearing. A rename emits TWO
  NUL-separated records — new path, then old — and a parser that walks
  records uniformly reports a phantom file.
- **Row rects are recorded during the draw**, and clicks test against that
  snapshot. Do not recompute row arithmetic in the click handler.
- **Hover is cleared by any event landing outside the panel.** Terminals
  emit no "pointer left" event, so tying it to motion alone strands a lit
  row when the mouse moves to the editor.
- **The two side panels share one width budget** (`reflowPanels`). A
  terminal resize re-clamps both before anything reads a rect; without it a
  narrowed window leaves the editor at negative width.

### Phase 2 notes

The handoff is three `herdr` CLI calls — `agent list`, `pane send-text`
(bracketed-paste wrapped by Vincent itself, terminators stripped byte by
byte), `agent focus`. **Never `herdr agent prompt`** — verified on herdr
0.8.2, it sends Enter after a short delay, which submits the review
unattended. Three rules that are easy to get wrong and expensive to debug:

- **Consume on success.** Clear the comment batch only after the send
  returns OK, or a closed pane silently eats a review.
- **Never rebase line numbers.** Freeze the verbatim diff snippet as the
  anchor. When the agent rewrites the file the snippet is the durable
  evidence; just flag the comment stale if its file leaves the changeset.

- **Log herdr's JSON error, show one sentence.** Its `pane_not_found`
  payload names pane ids the reviewer never saw. herdr-reviewr translates
  every failure to a plain status line and logs the real envelope.

Reference implementation: `~/Developer/vincent-refs/herdr-reviewr/src/herdr.rs`.
Comment composer UX to match: `~/Developer/vincent-refs/tuicr`
(`src/ui/comment_panel.rs`, inline box under the line; kinds cycle on Tab;
wire format in `src/output/markdown.rs`).

## Commits

- No "Generated with Claude Code" trailers, no Co-Authored-By.
- Commit directly with a good message when asked; don't ask for approval
  on the message.
