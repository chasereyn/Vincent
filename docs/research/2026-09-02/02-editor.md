# Vincent — re-adding editing

Research date 2026-09-02. All paths absolute. Read-only pass; nothing under
`vincent` or `vincent-refs` was modified.

## The headline

**Editing was never removed. It works today.**

Vincent as shipped at `0.2.0` types, selects, cuts, copies, pastes, undoes,
redoes, reverts, and saves. The key routing is intact, the menu rows are
there, the leader keys are bound, the dirty dot paints, and the
unsaved-changes modal fires on close and on quit.

Proof, not inference:

- `/Users/ChaseReynolds/Developer/vincent/internal/app/app_test.go:1249`,
  `TestHandleKey_RoutesToActiveTab`, drives `KeyRune 'a'`, `'b'`, `KeyEnter`,
  `'c'`, `KeyTab`, `KeyBackspace`, `KeyDelete` into a real tab through
  `handleKey`. `go test ./...` is green (ran it: 11 packages ok,
  `internal/app` 4.795s).
- `handleKey` at `app.go:922` falls through to `applyEditKey` at `app.go:1060`,
  which calls `InsertString("\n")`, `Backspace()`, `Delete()`,
  `InsertString(IndentUnit)`, `InsertRune(r)`.
- The gate is `if tab.ReadOnly() { return }` at `app.go:1052`.
  `ReadOnly()` is `Mode != ""` (`internal/editor/diffview.go:92`). A text tab
  has `Mode == ""`, so it is read-write.
- `builtinMenuGroups()` at `app.go:180` ships **Save**, **Save & close tab**,
  **Undo**, **Redo**, **Revert file**, **Copy selection**, **Cut selection**,
  **Paste**, **Toggle line comment**.
- `leaderBindings()` at `internal/app/leader.go:41` binds `Esc s` save,
  `Esc u` undo, `Esc r` redo.
- `Tab.Save()` at `internal/editor/tab.go:234` writes the file, clears
  `Dirty`, refreshes `Mtime`, breaks the undo group.

So CLAUDE.md's line "what was removed is the KEY ROUTING in app.go that called
them, plus save/dirty UI" is wrong. Nothing of the kind was removed. What
CLAUDE.md *does* say correctly is that `undo.go` / `comment.go` / `indent.go` /
the mutation half of `tab.go` "were left in place on purpose" and that going
read-only "is a design task, not a deletion" — that task was never started.
The owner asking for editing back is asking for a feature that is already
running.

## Part A — what was actually stripped, and what the ask still needs

### A1. `internal/editor` is untouched

Full diff of every file in the package against
`/Users/ChaseReynolds/Developer/vincent-refs/spice-edit/internal/editor/`:

| File | Vincent | Upstream | Difference |
|---|---|---|---|
| `buffer.go` | 196 | 196 | **byte-identical** |
| `indent.go` | 168 | 168 | **byte-identical** |
| `find.go` | 189 | 189 | **byte-identical** |
| `comment.go` | 211 | 211 | one line: `t.IsImage()` → `t.ReadOnly()` at :100 |
| `undo.go` | 237 | 231 | `CanUndo`/`CanRedo`/`CanRevert` gain a `ReadOnly()` guard (:146,:151,:163) |
| `highlight.go` | 185 | 185 | import path only |
| `image.go` | 206 | 206 | import path only |
| `tab.go` | 846 | 808 | import path, `+DiffRows` field, `+gutterCells()`, diff branches in `DisplayName`/`Render`/`HitTest`, and six `IsImage()` → `ReadOnly()` swaps at :322, :343, :366, :387, :416, plus `Save()` at :235 |

The undo stack, the coalescing window (`lastUndoGroup` / `lastUndoAt`,
`canCoalesce` at `undo.go:125`), `RevertFile`, `DeleteSelection`,
`InsertString`, `InsertRune`, `Backspace`, `Delete`, `SelectAll`, `Reload`,
`Save`, `Dirty`, `Mtime`, `DiskGone` — all present, all tested
(`tab_test.go` 1120 lines, `undo_test.go` 515, `buffer_test.go` 329,
`indent_test.go` 199, `comment_test.go` 211).

**Line estimate to bring the editing engine back: zero.**

### A2. `internal/app/app.go` — what came out

Vincent's `app.go` is 2680 lines vs upstream's 2736: 261 lines removed, 211
added. Every removal is one of these, and **not one of them is an editing
path**:

| Removed | Lines | Was it editing? |
|---|---|---|
| `customActionDoneEvent` + `handleCustomActionDone` + `splitErrorOutput` + `runCustomAction` + `execCustomAction` + `loadCustomActions` + the menu-splice | ~150 | no — user shell-outs |
| form-modal state block (`formOpen`…`formCallback`) + its 3 dispatch hooks | ~30 | no |
| `menuNewFile` / `Rename file` / `Delete file` / `Rename folder` / `Delete folder` menu rows | 5 | file management, not text editing |
| `runFormatOnSave` call in `saveTabAt`, `formatDoneEvent` case | ~8 | no — format-on-save |
| `loadSpiceConfig` → renamed `loadUserConfig` | ~12 | no |
| old `openGitHunkAt` (info-modal hunk preview) | ~25 | no — replaced by the diff tab |
| `hasSavableTab`'s `!t.IsImage()` → `!t.ReadOnly()` | 1 | tightening, not removal |

Survivors that matter to this task, with line numbers in Vincent's `app.go`:

```
1060  applyEditKey            the mutation dispatch
1680  saveActiveTab
1690  saveTabAt
1714  saveAllDirty
1728  dirtyTabCount
1743  requestCloseTab         → dirty modal
1787  copySelection           → OSC 52 + a.clipBuf
1802  cutSelection
1814  pasteClipboard
1927  hasSavableTab           t != nil && t.Path != "" && !t.ReadOnly()
1983  menuUndo / 1995 menuRedo / 2010 menuRevert
2024  menuSave / 2031 menuSaveAndClose
2054  menuCopy / 2060 menuCut / 2066 menuPaste
2072  menuToggleLineComment
2129  menuQuit                blocks on dirtyTabCount
2299  drawTabBar              paints '●' in theme.Modified when tab.Dirty
2470  drawStatusBar           appends " · ●" when tab.Dirty
 754  reconcileOpenTabsWithDisk
```

`internal/app/modals.go` (1081 lines vs upstream 1085) kept the whole
unsaved-changes modal: `openDirtyClose` :671, `dirtyCancel` :682,
`dirtyDiscard` :689, `dirtySave` :702, `dirtyActivate` :715, `handleDirtyKey`
:729, `handleDirtyMouse` :751, `dirtyButtonAtRelX` :783, `dirtyModalRect`
:797, `drawDirtyClose` :824. Also `openPrompt` :105 with full key/mouse/draw
routing, which is what a New-file or Save-As flow needs.

**Line estimate to re-wire keys and save UI: zero.**

### A3. What the ask genuinely does not have

Four things. These are the real work.

#### (1) Find & Replace — 100% absent

`rg -n "eplace"` over `internal/app/find.go` and `internal/editor/find.go`
returns nothing. Find exists and is good (`FindAll`, `FindNext`, `FindPrev`,
`SetFindQuery`, live match counter, `Esc f`). Replace was never written by
upstream either.

Cost:

- `internal/editor/find.go`: `+ReplaceCurrentMatch(string)`,
  `+ReplaceAll(string) int`. **≈55 lines.**
- `internal/app/find.go`: `findBarHeight` 1 → 2 (the constant is at
  `find.go:28` and is already the only thing `editorRect()` at `app.go:868`
  and `findBarRect()` at `find.go:106` consult, so the layout follows for
  free), a second input row, a focus flag, `Tab` to swap rows, keys in
  `handleFindKey` :123, a second row in `drawFindBar` :179. **≈140 lines.**
- `internal/app/leader.go`: one binding. **2 lines.**
- Tests: `internal/editor/find_test.go` **+90**, `internal/app/find_test.go`
  **+120**.

Two traps:

- **Replace All must be one undo step.** `Tab.InsertString` calls
  `breakUndoGroup()` (`tab.go:342`), so a naive loop over 40 matches produces
  40 undo entries. Do one `captureSnapshot()` / `pushUndo()` around the whole
  loop (`undo.go:66`, `:106`), and walk matches **back to front** so earlier
  positions don't shift under you.
- **Gate replace on `ReadOnly()`, not `IsImage()`.** `hasFindable`
  (`find.go:98`) and `openFind` (`find.go:34`) both test `!t.IsImage()`. That
  is correct for find — searching inside a diff is useful and works today.
  It becomes a bug the moment the same bar carries a replace field, because a
  diff tab would offer to rewrite the rendered diff. Split the predicates.

#### (2) New file / Save As — absent

`internal/app/fileops.go` (638 lines) was deleted; only the copy-path half
survives as `internal/app/pathops.go` (106 lines). There is no way to create
a file, and `NewTab("")` (an untitled tab) has no entry point.

Port just the new-file subset from
`/Users/ChaseReynolds/Developer/vincent-refs/spice-edit/internal/app/fileops.go`:

| Function | Upstream lines | Count |
|---|---|---|
| `createEmptyFile` (uses `O_EXCL`, refuses to clobber) | :44–54 | 11 |
| `doCreateFile` | :106–132 | 27 |
| `menuNewFile` | :204–228 | 25 |
| `newFileLabel` | :229–257 | 29 |
| `relativeFolderLabel` | :258–271 | 14 |
| `hasActiveSubfolder` | :480–496 | 17 |
| `ctxNewFile` | :497–516 | 20 |

**≈145 lines** plus the `maxLabelSuffix` const, one menu row, one
context-menu row (the context list is built at `modals.go:878`), and ~150
lines of test cribbed from `fileops_test.go`.

It drops straight in: Vincent already has `openPrompt` (`modals.go:105`),
`activeFolder` / `setActiveFolder` (`app.go:1355`), `refreshTree`,
`refreshGitStatus`, `invalidateFinder`, and `openFile`.

Save-As is a separate ~35 lines: `openPrompt` → set `Tab.Path` → `Save()` →
refresh tree. Do **not** port rename or delete — those belong to lazygit and
the file manager, and the non-negotiable #4 in CLAUDE.md still holds for them.

#### (3) Bracketed paste — absent, and one variant is a data-loss bug

`rg "EnablePaste"` over `internal/app` and `main.go`: no hits. There are two
screen-init sites, `app.go:456` (`New`) and `app.go:519` (`NewSingleFile`),
and neither calls it. tcell v2.13.9 supports it —
`~/go/pkg/mod/github.com/gdamore/tcell/v2@v2.13.9/screen.go:159` declares
`EnablePaste()`, and `paste.go:26` declares `EventPaste` with `Start()` /
`End()`.

Consequences today, in order of severity:

1. **A pasted `Esc` arms the leader.** `handleKey` at `app.go:970` treats a
   bare `Esc` as arming, then the *next* rune within `doubleEscMs` fires a
   leader action. Paste any text containing an escape byte followed by `q`
   and Vincent quits; followed by `s` and it saves; followed by `w` and it
   closes the tab. Mid-paste. No confirmation. This is the one thing here I
   would call a real bug rather than a rough edge.
2. **An N-line paste becomes N undo steps.** A `\r` in the stream arrives as
   `KeyEnter` → `InsertString("\n")` → `breakUndoGroup()`.
3. A literal tab in pasted text becomes `IndentUnit` rather than a tab.

Fix: `scr.EnablePaste()` at both init sites; a `case *tcell.EventPaste:` in
`handleEvent` (`app.go:697`) that flips a `pasting bool`; while `pasting`,
accumulate runes instead of dispatching and skip leader arming entirely; on
`End()`, one `tab.InsertString(buf)` → one undo step. **≈45 lines + 60 test.**

Note `pasteClipboard` (`app.go:1814`) is the *internal* clipboard only —
`internal/clipboard` is OSC 52, which is write-only, so the system clipboard
cannot be read from a TUI at all. That is correct and should stay; the flash
at `app.go:1820` already tells the user to use Cmd-V. Bracketed paste is what
makes Cmd-V behave.

#### (4) Conflict-model hardening — see Part B. **≈90 lines.**

### A4. Small gaps worth 3 lines each

- **`Tab.SelectAll()` is dead code.** It exists at `tab.go:524` and is
  tested, but `rg SelectAll` finds no production caller — no menu row, no
  leader binding. Wire `Esc a` and a "Select all" row: **3 lines.** Zed
  essential, already written.
- **Triple-click line select is absent.** `selectWordAt` (`app.go:1574`) plus
  the double-click detection in `editorPress` (`app.go:1403`) is the model;
  add `selectLineAt` and a click-count branch: **≈20 lines.**
- **Auto-indent on Enter is absent.** `applyEditKey` does a plain
  `InsertString("\n")`. Zed carries the previous line's leading whitespace.
  For someone making a one-line correction inside agent-written Go, losing
  indentation is instantly annoying. **≈25 lines** on `Tab`, and
  `LineVisualCol` / `leadingSpaces` (`indent.go:61`, `:144`) already exist.
- **Word-wise cursor movement is absent** (no `MoveWord`). **Skip it.** It
  wants Alt+arrow or Ctrl+arrow, and CLAUDE.md bans `Ctrl+`. `isWordChar`
  (`app.go:1599`) is there if it ever comes up.
- **`internal/app/pathops.go` has no `pathops_test.go`.** Convention
  violation against CLAUDE.md's "one `_test.go` per source file". Unrelated
  to this task, but it is the only such gap in the repo.

### A5. Upstream additions since the fork: none available

The clone at `/Users/ChaseReynolds/Developer/vincent-refs/spice-edit` is
**pinned to the fork base**. `git rev-parse HEAD` = `5b4adc5`,
`.git/shallow` contains only `5b4adc59e2333e0159740df4481c060c542c8a31`, and
the single reachable commit message is "Update spiceedit brew formula to
v0.0.43". `internal/version/version.go` says `0.0.43`. There is no
`CHANGELOG`.

A full `find -name '*.go'` comparison confirms it: 18 Go files exist upstream
and not in Vincent, and **all 18** are files Vincent deliberately deleted —
`internal/format/*` (6), `internal/customactions/*` (2),
`internal/spiceconfig/*` (2), `app/fileops*.go` (2), `app/format*.go` (2),
`app/formmodal*.go` (2), `app/actionvars*.go` (2). **Zero** upstream files
Vincent never had. 15 files exist only in Vincent (the diff engine, the git
panel, `shutdown.go`, `config`, `pathops.go`).

So there is nothing new upstream to evaluate from this checkout. To answer
"did spice-edit ship anything since April" you would need to unshallow the
clone or hit GitHub — see *What I did not check*.

## Part B — the conflict model

### What Vincent does today

`reconcileOpenTabsWithDisk`, `internal/app/app.go:754–801`, called from
`refreshTreeNow` (`:736`), fired by a `treeRefreshEvent` every
`treeRefreshInterval = 10 * time.Second` (`app.go:83`). Per tab with a real
path:

| Disk state | Buffer | Action | Line |
|---|---|---|---|
| gone | any | `DiskGone = true`, `Dirty = true`, flash "deleted on disk" | :769–774 |
| stat error | any | skip, retry next tick | :776–780 |
| reappeared | any | `DiskGone = false`, `Mtime = zero` to force the branch below | :781–786 |
| mtime not newer | any | skip | :787 |
| **mtime newer** | **dirty** | **flash "your edits will overwrite on save", then `tab.Mtime = info.ModTime()`** | **:790–796** |
| mtime newer | clean | `tab.Reload()`, flash "reloaded from disk" | :797–801 |
| — | diff tab | `reconcileDiffTab` instead | :761–766 |

This is byte-for-byte upstream's (`spice-edit/internal/app/app.go:830–891`)
plus the diff branch. Same three cases, same weakness.

**The weakness, precisely.** The dirty branch sets `tab.Mtime =
info.ModTime()` with the comment "so we don't re-flash every tick for the same
change." That is the correct instinct and the wrong mechanism: `Mtime` is the
*only* record that a conflict exists. Once it is advanced, the conflict is
forgotten. `statusFlashFor` is 3 seconds (`app.go:47`), so three seconds
later nothing on screen says the file moved. `menuSave` (`app.go:2024`) →
`saveTabAt` (`:1690`) → `Tab.Save()` (`tab.go:234`) then does
`os.WriteFile` unconditionally and the agent's write is gone with no prompt.
In Vincent's workflow — an agent rewriting the file you have open is the
*normal* case, not an accident — this will happen.

### What Zed does

Read from `/Users/ChaseReynolds/Developer/vincent-refs/zed/crates/`.

- Conflict is a **buffer flag**, `has_conflict()`. The editor only forwards
  it: `crates/editor/src/items.rs:930–932`.
- It is separate from deletion: `has_deleted_file()` at `items.rs:926–928`.
- **Clean + disk changed → reload, silently.** `fn reload` at
  `items.rs:1044–1069` calls `project.reload_buffers(buffers, true, cx)` and
  the only UI consequence is `editor.request_autoscroll(Autoscroll::fit(),
  cx)`. No prompt, no toast.
- **Dirty + disk changed → `has_conflict()` true, buffer not reloaded.**
- **Display is a colour change on the existing dirty dot, not a new badge.**
  `crates/editor/src/element/header.rs:659–666`:

  ```rust
  let indicator_color = match (buffer.has_conflict(), buffer.is_dirty()) {
      (true, _) => Some(Color::Warning),
      (_, true) => Some(Color::Accent),
      (false, false) => None,
  };
  ```

  Conflict outranks dirty. One dot, three states.
- The theme reserves a slot for it: `StatusColors::conflict`, documented as
  "Indicates some kind of conflict, like a file changed on disk while it was
  open, or merge conflicts in a Git repository" —
  `crates/theme/src/styles/status.rs:8–11`.
- **Conflict is decided by mtime.** The tests pin it.
  `items.rs:2891–2921` restores a serialized buffer carrying
  `MTime::from_seconds_and_nanos(0, 50)` against a real file and asserts
  `editor.has_conflict(cx)`. `items.rs:2960–3011` restores with the file's
  *current* mtime and asserts `!editor.has_conflict(cx)`. The comment at
  `items.rs:2331–2333` states it outright: "If we did restore an mtime,
  store it on the buffer so that the next edit will mark the buffer as
  dirty/conflicted."
- Read-only is double-gated exactly the way Vincent gates it: `can_save`
  returns false when read-only (`items.rs:934–943`) *and* `save` early-returns
  `Ok(())` when read-only (`items.rs:945–`). Same belt-and-braces as
  `hasSavableTab` + `Tab.Save()`.

What Zed does **on save into a conflict** is not answerable from this
checkout — see *What I did not check*.

### What herdr-file-viewer does

Nothing to borrow: it has no editor. `src/editor.rs` is an editor *launcher*
whose own doc comment says "Pure hand-off … It never reads, writes, or
otherwise mutates the file (AC-N1) — it only launches another process." Its
e2e test asserts the file is byte-identical after the hand-off
(`tests/e2e_editor.rs:79–84`, "AC-N1: the hand-off must not have modified the
file"). `Cargo.toml` has no `notify` and no fs-watch dependency. It also has
no file-change reconcile for its own read-only preview — it refreshes git
state on terminal focus-regain (`src/app.rs:205`) and after an editor
hand-off returns (`src/app.rs:673`), and otherwise doesn't watch.

### Recommendation — one model

**Zed's three states, plus a hard stop at save, plus a diff-based escape
hatch that only Vincent can offer cheaply.** Six concrete changes.

**1. Add `Tab.Conflict bool`.** Set when mtime advances while `Dirty`.
Sticky — cleared only by an explicit overwrite, an explicit reload, or the
buffer going clean. This is the fix for the `app.go:790–796` bug: keep the
re-flash suppression, but stop using `Mtime` as the conflict record.

**2. Suppress false conflicts for free.** When mtime advances, read the file
and compare it to `undoOriginal` — the on-open / last-reload snapshot in
`internal/editor/undo.go`. Byte-identical means the disk did not actually
change from what was loaded: update `Mtime`, no conflict, no flash. This
matters because agents bump mtime without changing bytes constantly — a
`gofmt -w` on an already-formatted file, a tool that rewrites a whole file
it decided not to alter. Without this you get a false "your edits will
overwrite" every time, and a warning that cries wolf gets ignored, which
defeats the whole model. The snapshot is already in memory; the comparison
is free.

**3. Colour the dot, don't add a badge.** `drawTabBar` (`app.go:2299`) paints
`'●'` in `a.theme.Modified`; when `tab.Conflict`, paint the same cell in a
new `theme.Conflict`. Zed's precedence — conflict beats dirty. Add the field
at `internal/theme/theme.go:35` and the value at `:127`. Do not reuse
`Modified` (`0xe0af68` amber) — too close to read as different. `GitDeleted`'s
red `0xf7768e` is already in the palette and reads as "stop". `drawStatusBar`
(`app.go:2470`) shows `" · ● changed on disk"` instead of `" · ●"`, so the
state survives the 3-second flash window.

**4. Save on a conflicted tab refuses and prompts.** This is the load-bearing
change. `openDirtyClose` (`modals.go:671`) already draws exactly three
buttons with keyboard routing (`handleDirtyKey` :729), mouse hit-testing
(`dirtyButtonAtRelX` :783), and a painter (`drawDirtyClose` :824). Call it
with new labels: **Overwrite** (write, clear `Conflict`) / **Reload**
(discard my edits, take disk) / **Cancel**. No new modal machinery.

**5. Give the modal a fourth affordance: "See what changed."** Write the
buffer to a temp file and run the `git diff --no-index` path that
`loadDiffRows` (`internal/app/diffview.go`) already uses for untracked files,
then open the result as a diff tab. The user's actual question at that moment
is "did the agent touch the lines I touched?" — and Vincent is the one tool
here that can answer it in-process, because `internal/diff` (358 lines) and
`internal/editor/diffview.go` (363 lines) already exist. Zed's prompt does
not offer this. It is the highest-value thing on this list and it is nearly
free.

**6. Keep the clean-tab reload, but quiet it down.** A clean tab reloading
when the agent writes is the normal case, and a flash per write is noise
during exactly the stretch the user is reading. Vincent already made this
exact call for diff tabs — the doc comment at `diffview.go:173–175` reads
"It is quiet on purpose. A flash per write would be constant noise during
exactly the stretch you are trying to read the diff." Apply the same judgement
to clean text tabs. Also note `Tab.Reload()` (`tab.go:262`) calls `initUndo()`,
resetting both undo stacks — correct for a real reload, and worth keeping in
mind because it means a silent reload silently discards redo history.

**Total ≈90 lines** plus tests.

**Why this model and not a merge.** The person using Vincent does not write
code; he is making small corrections to agent output. The odds his edit and
the agent's rewrite touch the same lines are low, but the cost of guessing
wrong is losing work he cannot easily reproduce. So the model must be
"refuse and show me," never "merge and hope." Three-way merge, OT rebase,
and auto-reload-with-undo-entry all trade a rare inconvenience for a rare
silent data loss, which is the wrong direction for this user.

### One risk to fix before adding anything else to the tick

`refreshTreeNow` (`app.go:736`) runs `refreshTree` + `reconcileOpenTabsWithDisk`
+ `refreshGitStatus` + `invalidateFinder` **on the main UI thread**, driven by
`handleEvent`'s `case *treeRefreshEvent` at `app.go:717`. And
`refreshGitStatus` (`:595`) calls `refreshGitLineChanges` (`:627`), which
shells out to `git diff` **once per open tab**.

Reading a diff, that is a hitch. Typing, it is a dropped keystroke burst every
10 seconds — and now with a file-read added for the content comparison in
recommendation #2. Move it to the goroutine-plus-custom-event pattern CLAUDE.md
already prescribes and that `autoScrollEvent` / `treeRefreshEvent` /
`finderRebuiltEvent` already use. Do this before, not after.

## Part C — "Tab becomes what?"

**Nothing. It is literally re-wiring keys, and even those are already wired.**

`Tab` is already the mutable text buffer with a read-only gate on non-text
modes. The refactor CLAUDE.md describes as "a design task, not a deletion"
was never started, so there is nothing to invert. `ReadOnly()` is
`Mode != ""` (`internal/editor/diffview.go:92–94`): a text tab is `Mode == ""`
and read-write; diff and image tabs carry a mode and are read-only. That is
exactly the shape the task asks for.

The phase-1 note in CLAUDE.md turns out to be the whole answer to its own
question: `Tab.Mode` plus `ReadOnly()` *is* the design. Six mutation methods
check it (`tab.go:322`, `:343`, `:366`, `:387`, `:416`, `comment.go:100`),
`Save()` checks it (`tab.go:235`), the three undo predicates check it
(`undo.go:146`, `:151`, `:163`), and `hasSavableTab` / `hasCommentableTab`
check it in the app layer (`app.go:1927`, `:1949`).

Three one-line inconsistencies to tidy — cleanup, not refactor:

- `hasFindable` (`internal/app/find.go:98`) and `openFind` (`find.go:34`)
  gate on `IsImage()`, not `ReadOnly()`. **Correct today** — searching a diff
  works and is useful. It becomes wrong the moment replace shares the bar.
  Split: find on `!IsImage()`, replace on `!ReadOnly()`.
- `handleKey` drops **every** key on an image tab via `IsImage()` at
  `app.go:1004`, then separately drops mutation keys on any read-only tab at
  `:1052`. Two guards where one nearly does. Keep both — the image branch is
  what makes arrows dead on an image preview, which is intentional
  (its comment says so) and is not what you want on a diff.
- `refreshGitLineChanges` (`app.go:629`) skips on `IsImage()`, so a diff tab
  gets `GitLines` loaded that nothing reads. Cosmetic.

The one guard to enforce going forward is already in CLAUDE.md and is worth
restating because it is the actual bug phase 1 caught: **gate new mutations
on `ReadOnly()`, never on `IsImage()`.** A diff tab carries the real file's
`Path`, so anything that writes and only checks for images will write diff
text over the user's source.

## Recommended order

1. **Bracketed paste** (`≈45` lines + 60 test). Fixes the pasted-`Esc`-fires-
   `Esc q` data-loss path and makes Cmd-V produce one undo step. Smallest
   change, largest correctness win.
2. **Conflict model** (`≈90` lines + tests), items 1–5 above. This is the
   thing the agent-rewrite workflow actually needs and the current code gets
   wrong.
3. **Move the 10s git tick off the UI thread.** Prerequisite for typing to
   feel right.
4. **Find & Replace** (`≈195` production + 210 test). The largest piece of
   genuinely new code.
5. **New file + Save As** (`≈180` production + 150 test). A straight port of
   a known-good 145-line subset.
6. **`Esc a` select-all** (3 lines), **triple-click line select** (~20),
   **auto-indent on Enter** (~25).
7. Bump `internal/version/version.go` and rewrite the "Where it stands" and
   "Still present and still to remove" sections of CLAUDE.md — the latter is
   now actively misleading about what the code does.

Total genuinely new production code across all of it: **roughly 550 lines.**

## What I did not check

- **What Zed does on save into a conflict.** `crates/language` and
  `crates/workspace` are excluded by the sparse checkout —
  `.git/info/sparse-checkout` admits only `buffer_diff`, `diff`, `editor`,
  `git_ui`, `markdown`, `markdown_preview`, `project_panel`, `theme`, `ui`.
  `has_conflict()` is defined on `Buffer` in `crates/language`, and the
  overwrite/reload prompt (if there is one) lives in `crates/workspace`.
  I grepped the present crates for prompt strings ("changed on disk",
  "Overwrite", "has changed since") and found none. My recommendation #4 is
  reasoned from Vincent's needs, not copied from Zed.
- **Whether spice-edit shipped anything after `5b4adc5`.** The clone is
  shallow and pinned to the fork base; the file-list comparison only proves
  nothing new exists *in this clone*. Answering the question needs
  `git fetch --unshallow` or a GitHub query, neither of which I ran.
- **I did not run Vincent interactively.** The "typing works today" claim
  rests on `TestHandleKey_RoutesToActiveTab` (`internal/app/app_test.go:1249`)
  passing under `go test ./...`, plus reading the `handleKey` →
  `applyEditKey` → `InsertRune` path. I did not build the binary and type
  into a real terminal. In particular I did not confirm the on-screen dirty
  dot or the save flash render as described — those are read from
  `drawTabBar` and `drawStatusBar`, not observed.
- **The pasted-`Esc` scenario is reasoned, not reproduced.** I traced it
  through `handleKey` (`app.go:952–990`) and `leaderActionFor`
  (`leader.go:60`) and confirmed `EnablePaste()` is never called. I did not
  actually paste an escape sequence into a running Vincent to watch it quit.
- **I did not measure the 10s tick's cost.** "Dropped keystroke burst" is
  inferred from `refreshGitLineChanges` shelling out per open tab on the main
  thread; I did not profile it or time a `git diff` on a large repo.
- **`internal/app/gitpanel.go`, `finder.go`, `gitentries.go`,
  `gitstatus.go`, `internal/filetree`, `internal/finder`, `internal/icons`,
  `internal/config`** were not read beyond grepping for specific symbols.
  Nothing in the task touched them, but if editing changes how the tree or
  panel refresh, that assumption needs revisiting.
- **`tuicr` and `herdr-reviewr`** (the phase-3 references) were not opened —
  out of scope for this task.
- **Windows behaviour.** Everything here was read on macOS. The
  `EnablePaste` / `EventPaste` path in particular behaves differently on
  Windows consoles and I did not check tcell's Windows backend for it.
