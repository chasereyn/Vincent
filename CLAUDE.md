# CLAUDE.md — Vincent

Read this before touching anything. It captures decisions that are not
obvious from the code, and several places where Vincent deliberately
disagrees with the codebase it was forked from.

## What Vincent is

**A read-only, mouse-first terminal client for reviewing code that AI
agents wrote.** File tree on the left, the file or its diff in the middle,
a Zed-shaped git panel on the right. You click lines in a diff, write a
review note, and one keypress delivers those notes back to the agent that
wrote the code.

Vincent is **not an editor**. It has no insert mode, no save, no undo, no
LSP, no autocomplete. If a change you are asked to make would let a user
type a character into a file, stop and confirm — that is a scope change,
not a feature.

The person using Vincent does not write code. Agents write it; he reviews
it. Every design decision should optimise for reading a diff quickly and
getting feedback back to an agent — not for authoring.

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
- Version: `internal/version/version.go`, currently `0.1.0`

## Where it stands

Phases 0 through 2 are done. Vincent is usable for a real review pass:
open a repo, hit `Esc g` for the Changes panel, click a file, read its
diff. What is missing is the second half of the loop — writing a note and
sending it back to the agent.

- **Phase 2, the Changes panel.** Zed's shape, read-only: `Changes (N)`
  header, Tracked and Untracked sections, filename in its status colour
  with the parent directory dimmed beside it, deletions struck through, a
  repo / branch footer. Click a row to open its diff. `Esc g`.
- **Phase 1, inline diffs.** Dual old/new gutters, ± markers, full-width
  row tints, and a darker word-level tint on the part of a line that
  actually changed. `Esc d`, the action menu, a file's right-click entry,
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

**Still present and still to remove:** `internal/editor/undo.go`,
`comment.go`, `indent.go`, and the mutation half of `tab.go`
(`InsertRune`, `Backspace`, `Delete`, dirty-state, save). These were left
in place on purpose. `tab.go` is woven into the undo stack — deleting
`undo.go` alone produces eleven compile errors in `tab.go` — so making the
buffer read-only is a **design task**, not a deletion. Decide what `Tab`
becomes first (an immutable view over `[]string`?), then the three files
fall out on their own.

Phase 1 answered half of that question without forcing the refactor:
`Tab.Mode` already existed for image previews, so a diff is just another
mode, and `Tab.ReadOnly()` (= `Mode != ""`) is now the single gate every
mutation path checks. **Guard new mutations on `ReadOnly()`, never on
`IsImage()`** — that was the actual bug phase 1 found: `Tab.Save()` checked
only for images, and a diff tab carries the real file's `Path`, so saving
one would have written diff text over the user's source. When the plain
text path finally goes read-only, `ReadOnly()` becomes `true` and the three
files above fall out.

## Architecture map

```
main.go                          CLI parsing — pure, testable, no tcell until the end
internal/app/app.go        2641  Event loop, layout rects, mouse dispatch, rendering
internal/app/modals.go     1081  Modal scaffolding — reuse for the review composer
internal/app/finder.go      474  Fuzzy file-finder modal
internal/app/gitpanel.go    452  The Changes panel: layout, rows, clicks
internal/app/find.go        297  In-file find bar
internal/app/gitentries.go  258  THE git status parse — tree and panel both
internal/app/diffview.go    234  git diff shell-outs, diff tabs, live refresh
internal/app/gitstatus.go   221  Branch, gutter markers, dirty-folder rollup
internal/app/pathops.go     106  Copy relative / absolute path to clipboard
internal/app/leader.go       66  Esc-prefixed key bindings
internal/editor/tab.go      846  Tab: buffer, cursor, scroll, hit-test  <- still mutable
internal/editor/diffview.go 363  Diff render: dual gutters, row + word tints
internal/editor/highlight.go 185 Chroma -> per-rune []tcell.Style grid
internal/filetree           585  Lazy tree, identity-preserving refresh, hit-test
internal/finder             583  Filename index (git ls-files) + fzy-style scorer
internal/diff               358  Unified-diff parser — no tcell, no git, pure
internal/icons              390  Nerd Font glyphs per file type
internal/theme              171  The palette. All surfaces pure black
internal/config             133  ~/.config/vincent/config.json
internal/clipboard           50  OSC 52 — works over SSH and through tmux
```

## Non-negotiables

These are hard requirements from the person this is built for. Do not
trade them away for convenience.

1. **Pure black background.** Set explicitly, never `tcell.ColorDefault` —
   that inherits the host terminal and will not reliably be black.
2. **Mouse-first.** Herdr is the quality bar. Click, drag, and scroll work
   everywhere; keyboard is the supplement. This is the opposite of the
   usual TUI convention, so design it in rather than bolting it on.
3. **Single static binary.** No cgo, no runtime, no external binaries at
   run time. This is why highlighting is Chroma and not tree-sitter, and
   why the diff renderer must be in-process rather than shelling out to
   `delta`. `git` and `herdr` are the only exceptions — both are shelled
   out to deliberately.
4. **Read-only.** One exception, `git checkout`, because branch switching
   was asked for by name. Everything else that mutates belongs to lazygit.
   The Changes panel is where this pressure will show up first — resist
   adding staging checkboxes to it.

## Build

```sh
make build        # ./bin/vincent
make test         # go test ./...
make build-mac    # darwin/arm64 cross-compile
```

`make`, `go`, and `git` come from scoop on this machine. There is no dev
server — it is a TUI. To check UI behaviour, build and run it against a
real directory.

`make test` runs **without** `-race` on purpose. The race detector needs
cgo, and this machine builds with `CGO_ENABLED=0` — no C compiler, which
is also what keeps the binary static. CI runners do have one, so
`.github/workflows/test.yml` runs `-race` on all three platforms; locally
that is `make test-race`, which needs `scoop install mingw` on Windows.

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
- **No `Ctrl+` shortcuts.** They fight tmux and terminal emulators — that
  is the entire reason the action menu exists. Use `Esc`-prefixed leader
  keys instead (see `leader.go`).
- **Every right-click action must also live in the main menu.** macOS
  Terminal under tmux swallows button 3. Right-click is a redundant
  shortcut, never the only path to something.

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
- **macOS Terminal + tmux** swallows right-click — hence the menu rule above.
- **Shift+mouse must no-op** so the terminal's own native selection and
  copy still work.

Chase does **not** run Vincent under tmux — it gets a full monitor of its
own, next to herdr. So the tmux-specific constraints are belt-and-braces
rather than load-bearing. Keep them anyway: the no-`Ctrl+` rule and the
right-click mirroring rule both hold for bare terminal emulators too, and
macOS Terminal is coming.

## Cross-platform

Developed on Windows now, moving to macOS. CI runs Linux, macOS, **and
Windows**.

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

| Phase | What | Status |
|---|---|---|
| 0 | Fork, strip, blacken | **done** |
| 1 | Inline (Zed-style) diff viewer | **done** |
| 2 | Zed-shaped read-only git panel | **done** |
| 3 | Review notes + herdr/clipboard handoff | next |
| 3b | Branch checkout, off the panel footer | |
| 4 | Multi-repo workspace | |
| 5 | Content search + markdown renderer | |

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
(bracketed-paste wrapped), `agent focus`. Two rules that are easy to get
wrong and expensive to debug:

- **Consume on success.** Clear the comment batch only after the send
  returns OK, or a closed pane silently eats a review.
- **Never rebase line numbers.** Freeze the verbatim diff snippet as the
  anchor. When the agent rewrites the file the snippet is the durable
  evidence; just flag the comment stale if its file leaves the changeset.

Reference implementation: `~/Downloads/herdr-reviewr`. Comment composer UX
to match: `~/Downloads/tuicr`.

## Commits

- No "Generated with Claude Code" trailers, no Co-Authored-By.
- Commit directly with a good message when asked; don't ask for approval
  on the message.
