# Vincent chrome/visual research — grounded recommendations

Scope: read-only. Nothing under `vincent` or `vincent-refs` was edited.

## 1. Markdown viewer ("renders like Claude Code")

**Recommendation: `github.com/yuin/goldmark` (pure Go, zero runtime deps beyond
stdlib) parsed to an AST, with a new in-process renderer — not glamour, not a
shell-out.** Put the parser in a new `internal/markdown` package (no tcell,
mirrors `internal/diff`) and the tcell painter in
`internal/editor/markdownview.go` (mirrors `internal/editor/diffview.go`).
A markdown preview becomes another `Tab.Mode`, per the pattern
`CLAUDE.md` already names for image/diff tabs — headers render bold in
`theme.Accent`, fenced code blocks reuse `internal/editor/highlight.go`'s
existing `Highlight()` boxed in one cell of left padding, lists get a `•`/`1.`
prefix column, tables get box-drawing rules, links render underlined in
`theme.SynFunction` blue with the URL suppressed unless focused.

Why not glamour (`github.com/charmbracelet/glamour`): it's also pure Go and
also wraps Chroma internally, but its whole model is "render to an ANSI
string, print it" — output is text with embedded escape codes, not tcell
cells. Getting that into a tcell grid means re-parsing your own ANSI output
(the same trick `herdr-file-viewer` uses for `bat`/`delta`/`glow`, see
below) — a second color-model translation Vincent doesn't need. Glamour also
pulls in `lipgloss`, `muesli/termenv`, `muesli/reflow`, and
`microcosm-cc/bluemonday` (HTML sanitizing Vincent has no use for) —
dependencies that duplicate what tcell already owns (terminal capability
detection, color output). Rendering the goldmark AST directly keeps Vincent's
architecture consistent: one parser package, one tcell painter, viewport-aware
like `highlight.go`'s `HighlightVisible`.

**How the reference tools actually do it — and why neither is a model to
copy:**
- `herdr-file-viewer` (Rust) does **not** render markdown in-process. Its
  `ViewMode::RenderedMarkdown` (`src/view_policy.rs:15`) delegates to an
  external `glow` subprocess (`src/render.rs:257,725`: "the default markdown
  command: glow with `-w 0`"), pipes glow's ANSI output back, and converts it
  to ratatui `Text` via the `ansi-to-tui` crate (`Cargo.toml:26`). Every
  renderer — markdown, diff, syntax — is a swappable external binary
  (`renderers.markdown` / `.diff` / `.syntax`, `src/config.rs:116,289`) with a
  documented fallback chain if the binary is missing
  (`src/render.rs:439: "Install it or see docs/renderers.md."`). This is
  architecturally the opposite of Vincent's single-static-binary rule — it
  only works because herdr-file-viewer accepts "requires `glow`/`bat`/`delta`
  on PATH" as a real constraint. Not adoptable here.
- `hunk` (TypeScript/OpenTUI) has no dedicated markdown renderer at all —
  grepping `src/ui` for "markdown" only turns up test fixtures
  (`src/ui/fileViews/useFilePresentationController.test.tsx:467`) that treat
  `.md` as just another extension a generic file view can match; markdown
  files fall through to the same diff/raw-text presentation as everything
  else, using `@shikijs/themes` (`package.json`) for code coloring generally.
  Nothing to port.

**`vincent-refs/claude-code` does not contain the CLI.** It's the
`anthropics/claude-code` *marketplace/plugins* repo — `CHANGELOG.md`,
`plugins/*` (agent-sdk-dev, code-review, hookify, etc.), `examples/`,
`scripts/`, `.devcontainer/`. No terminal-rendering code, no markdown
renderer, nothing about how the actual `claude` CLI paints text. Confirmed by
directory listing; not useful for this question.

## 2. Git status colours

Owner's ask: "brighter yellow, deeper green, real red" on pure black
(`theme.BG = 0x000000`).

**Recommendation — replace the three `theme.go` fields:**

```go
GitModified: tcell.NewRGBColor(0xff, 0xcb, 0x6b), // was 0xff9e64 (orange, not yellow)
GitAdded:    tcell.NewRGBColor(0x3f, 0xb9, 0x50), // was 0x9ece6a (pastel yellow-green)
GitDeleted:  tcell.NewRGBColor(0xf8, 0x51, 0x49), // was 0xf7768e (pastel pink-red)
```

These are GitHub's dark-mode diff colors (Primer), chosen because they're the
one git-status palette in this comparison actually tuned for a near-black
background at scale, and because they measure well. WCAG contrast ratio
against pure black (`(L+0.05)/0.05`, computed exactly, not eyeballed):

| Color | Hex | Contrast on `#000` |
|---|---|---|
| current `GitModified` (orange) | `#FF9E64` | 10.33:1 |
| current `GitAdded` (pastel green) | `#9ECE6A` | 11.49:1 |
| current `GitDeleted` (pastel red) | `#F7768E` | 7.94:1 |
| **proposed `GitModified`** | **`#FFCB6B`** | **14.01:1** |
| **proposed `GitAdded`** | **`#3FB950`** | **8.27:1** |
| **proposed `GitDeleted`** | **`#F85149`** | **6.26:1** |

All three comfortably clear WCAG AA (4.5:1) for normal text with margin to
spare, while reading as unambiguously yellow / deep green / real red instead
of the current orange / pastel-green / pastel-pink.

**Exact values pulled from the three named sources:**

- **Zed** (`vincent-refs/zed/crates/theme/src/default_colors.rs:11-36`) — this
  is gpui's fallback/default `ThemeColors`, not the shipped "One Dark" theme
  JSON (this sparse checkout has no `assets/themes/*.json`, so the literal
  shipped theme wasn't available to check — see "what I did not check").
  Raw HSLA constants and their RGB (computed via `colorsys`, not eyeballed):
  - `ADDED_COLOR`: `h:134/360, s:0.55, l:0.40` → **`#2E9E48`** (6.10:1 on black)
  - `MODIFIED_COLOR`: `h:48/360, s:0.76, l:0.47` → **`#D3AF1D`** (9.92:1)
  - `REMOVED_COLOR`: `h:350/360, s:0.88, l:0.25` → **`#78081A`** (1.85:1)
  `fallback_themes.rs:169-171` and `default_colors.rs:169-171` wire these
  straight into `version_control_deleted`/`modified`/`created` — i.e. Zed's
  own project-panel filename color for a deleted file is that dark, nearly
  black-on-black red. **Important finding: don't lift this hex verbatim.**
  1.85:1 contrast is barely readable as small text on true black; it only
  works in Zed because gpui panels sit on a dark charcoal panel background
  (not `#000000`) and/or the value is reused as a *background tint*
  (`editor_diff_hunk_deleted_background: REMOVED_COLOR.opacity(0.16)`,
  `default_colors.rs:135`) more than as raw foreground text. Vincent's
  `SidebarBG` is pure black (`theme.go:106`), so a value this dark would be
  close to invisible — this is exactly why the recommendation above uses
  GitHub's brighter, dark-background-tuned red instead.
- **herdr-sidebar** (`vincent-refs/herdr-sidebar/plugins/herdr-sidebar/src/ui.rs:74-80`)
  — these are literally VS Code Dark+'s `gitDecoration.*ResourceForeground`
  values: `modified: #E2C08D` (12.16:1), `untracked/added: #73C991` (10.49:1),
  `deleted: #C74E39` (4.58:1), `renamed: #73C991`, `conflict: #E4676B`. This
  file is also the documented source (per Vincent's own `theme.go:135-146`
  comment) of Vincent's current `DiffAddBG`/`DiffDelBG` diff-row tint values
  — so Vincent already has one direct lineage from this file for the diff
  view; the tree/panel foreground colors above are the sibling values that
  weren't carried over.
- **lazygit** (`vincent-refs/lazygit/pkg/gui/presentation/files.go:135,137,207,214,240,242,288,290`)
  uses named/indexed ANSI colors — `style.FgGreen` / `style.FgYellow` /
  `style.FgRed`, resolved through `pkg/theme/gocui.go:12-14`
  (`gocui.ColorRed`/`ColorGreen`/`ColorYellow`) — not curated hex. This means
  lazygit's actual on-screen color depends on the user's terminal palette,
  which is the opposite of Vincent's explicit-RGB philosophy (`theme.go:99`:
  "Black is set explicitly rather than via `tcell.ColorDefault`"). The only
  transferable fact from lazygit is the semantic mapping (green=added/staged,
  yellow=modified, red=deleted-count) — confirmed as the same hue family
  every tool in this comparison uses; it's not a source of literal RGB.

## 3. File tree chrome

**Root folder name / "Explorer" header — already built, no change needed.**
`internal/filetree/filetree.go:230-241` draws a small-caps `" EXPLORER"`
header row (`Muted`, bold) directly above `" "+t.Root.Name` (bold, `Accent`
when the root is the active folder, or the git-dirty color if the root
folder itself has changes). This already matches VS Code/Zed's
Explorer-header-then-project-name shape. Nothing to build here.

**Indent guide vertical lines — not built; recommend adding them.**
`drawNodeRow` (`internal/filetree/filetree.go:299,311`) currently renders
indent as `strings.Repeat("  ", item.Depth)` — literal blank space, no guide
line. Zed's project panel has this exact feature:
`vincent-refs/zed/crates/project_panel/src/project_panel.rs:7098` gates it on
`ShowIndentGuides` (`Always` by default), draws via
`ui::indent_guides(IndentGuideColors::panel(cx), ...)`
(`project_panel.rs:7310-7314`, `7458-7462`), and
`vincent-refs/zed/crates/ui/src/components/indent_guides.rs:11-27` shows the
guide has three color states (`default`/`hover`/`active`) sourced from
`cx.theme().colors().panel_indent_guide*`. Recommend: in `drawNodeRow`,
replace each two-space indent unit with `"│ "` in `theme.Subtle`
(`0x3a3d55` — already the color used for the splitter and borders,
`theme.go:118`) for every depth level except the row's own, so the guide
reads as structure rather than competing with the git-status/active-file
foreground colors on the row text itself. Skip hover/active guide states —
Vincent doesn't need per-guide hover, just the always-on line Zed's
`ShowIndentGuides::Always` setting represents.

**Nerd Font file icons — already built and already wired into the tree.**
`internal/icons/icons.go` has `Resolve`/`Detect` (fc-list, then a
`~/Library/Fonts` walk) and `internal/filetree/filetree.go:349-373` already
draws the glyph (`icons.For` / `icons.ColorFor`) between the chevron and
filename when `withIcons` is true, in its own per-language color while the
name keeps the row's status/active color. This is the exact "read Go from
Ruby from Markdown at a glance" pattern the file's own doc comment
(`filetree.go:290-297`) describes borrowing from nvim-tree. No gap here —
confirm `IconsMode` isn't stuck on `off` in the user's `~/.config/vincent/config.json`
if icons aren't appearing; that's a config question, not a missing feature.

**Current-line highlight — already built.** `internal/editor/tab.go:625-635`
paints the cursor's row background as `theme.LineHL` (`0x121216`, "raised
just enough to read as a highlight against black without becoming a grey
slab," `theme.go:108-109`) across the full row width, and the gutter number
on that row switches to `AccentSoft`. Nothing to add.

## 4. Tab bar toggle + removing the status row

**Where the ≡ menu button lives today:** it's drawn inside the tab bar row
itself — `drawTabBar` (`internal/app/app.go:2268-2276`) calls
`a.drawMenuButton()` first, then lays out tabs beside it; `menuButtonRect`
(`app.go:892-895`) anchors it at `x = sidebarW()` on `y = 0`, the same row
`tabBarRect` (`app.go:859-862`) occupies. **If the tab bar becomes an
optional toggle, row 0 needs to survive as a "chrome row" independent of
whether any tabs are drawn in it** — keep the ≡ button and (when no tabs are
open) show the active file's name where a tab would otherwise sit, rather
than deleting row 0 whenever `TabBar` is off.

**Where the leader-armed hint should go if the status bar disappears:**
today it's the highest-priority thing `drawStatusBar` draws
(`app.go:2448-2452`: "an armed Esc outranks everything else... Without this
the leader is invisible state"). If the bottom row goes away, move that same
substitution to the top chrome row — collapse "tab bar row" and "status bar
row" into one row that normally shows `≡ | current file · Ln/Col · lang` and
temporarily replaces that with the `Esc — d diff · g changes · ...` hint
line, exactly the same precedence logic, just retargeted. This isn't
speculative: `hunk`'s own `StatusBar`
(`vincent-refs/hunk/src/ui/components/chrome/StatusBar.tsx:6,10,29`) does
precisely this — one row that is *either* the file filter input, *or* a
transient notice, *or* a "persistent keyboard-mode badge" (`modeText`),
picked by the same kind of priority the doc comment states outright:
"Render the active file filter, transient notice, and persistent
keyboard-mode badge" in one `<box height:1>`. hunk keeps this row always on
(it has both a `MenuBar.tsx` top row and this `StatusBar.tsx` bottom row) —
it doesn't collapse them — but the component itself is proof that one row
can multiplex three different pieces of transient/persistent state, which is
exactly the trick Vincent needs if it drops from two chrome rows to one.

**What "no tab bar" looks like in the two closest tools:** neither
`herdr-file-viewer` nor `hunk` has a tab strip at all, but for different
reasons than "toggled off" — they don't have a tabbed-buffer model in the
first place. `herdr-file-viewer` is single-file-at-a-time (open one file,
its `ViewMode` cycles between diff/syntax/markdown for *that* file — no
concept of several files open at once). `hunk` is the opposite: it renders
**every changed file as one continuous scrolling document** (GitHub
"Files changed" style) with `HunkFileNav.tsx` as a jump-to-file sidebar
control, not tabs — so there's never a strip of open buffers to show or
hide. Concretely, `herdr-file-viewer`'s chrome doesn't even reserve a row for
hints: `presenter.rs:2666` — "The body fills the whole interior (tabs +
footer ride the border, not inner rows)" — its key-hint footer
(`PICKER_FOOTER_HINT`, `presenter.rs:1755,1842,2087`) is drawn centered
*into the bottom border line* of a bordered ratatui block via
`.title_bottom(footer)` (`presenter.rs:1854,2226`), not a separate row.
Vincent has no bordered panels to piggyback a title onto, but the transferable
principle is the same one hunk's `StatusBar` shows: fold hint text into a row
that's being painted anyway rather than reserving a second dedicated row.

**Concrete recommendation:** add a `TabBar bool` field to `internal/config`
alongside `IconsMode` (same pattern as `config.go:36-44,50-51`, JSON key
`"tabBar"`, default `false`/off) plus an `Esc t`-style leader toggle. When
off: `tabBarRect` still exists (row 0) but draws only `≡` + the active
tab's filename (no strip, no `×`), and `editorRect` doesn't grow — the row
stays reserved so the leader-hint substitution above always has a home. Do
not remove the bottom status row in the same pass; that's a bigger change
(losing Ln/Col and dirty-state readout) than "tab bar off by default," and
nothing in the comparable tools suggests going to *zero* chrome rows — even
`hunk`, which is the most minimal of the four, keeps two.

## 5. Recent / open directory picker

**Recommendation: don't build one.** All three comparable read-only review
tools skip a directory/repo picker entirely, and Vincent's own current
behavior (`main.go:124-125`: `resolveArgs(os.Args[1:])`) already matches the
pattern that works for all of them — take the path as `argv[1]`, default to
cwd, done:
- `herdr-file-viewer` takes its root from a host-injected launch context
  (`src/host.rs:14-21`: `focused_pane_cwd` / `workspace_cwd` / plain `cwd`,
  parsed by `parse_context`, `host.rs:36`) or falls back to
  `std::env::current_dir()` (`host.rs:30`) — no picker UI, no persisted MRU
  list anywhere in `src/`.
- `hunk`'s CLI (`bin/hunk.cjs`) and `ftdv`'s `clap`-based CLI
  (`vincent-refs/ftdv/src/cli.rs:11-13`: `targets: Vec<String>`, "Git refs,
  files, or directories to compare") both take the target as a positional
  argument; `ftdv/src/persistence.rs` only persists per-file review-checked
  state (`persistence.rs:33-88`), not directory history.
- Zed does have a real "recent projects" picker, but the crate that
  implements it (`recent_projects`) isn't part of this sparse checkout
  (`vincent-refs/zed/crates` only has `ui`, `markdown`, `theme`,
  `project_panel`, `buffer_diff`, `git_ui`, `markdown_preview`, `editor`) —
  not verifiable here, and Zed is a persistent-workspace IDE, not a
  disposable review-session tool, so its needs differ from Vincent's per the
  "Where it stands" framing in `CLAUDE.md`.

The honest reason this generalizes: every one of these tools is launched
*from* a terminal that's already sitting in (or was told) the right
directory — cwd or a positional arg is sufficient, and a picker would be
solving a problem none of them actually has. If Phase 4 ("multi-repo
workspace," `CLAUDE.md` roadmap) eventually wants an in-app repo switcher,
that's the right place for it — not a startup picker.

## 6. Mouse handling without flicker / synchronized output

**tcell already has synchronized output (DEC 2026), and Vincent already gets
it for free.** Checked
`$(go env GOMODCACHE)/github.com/gdamore/tcell/v2@v2.13.9/tscreen.go:314-319`:
when the terminal is `ti.XTermLike`, tcell sets
`t.startSyncOut = "\x1b[?2026h"` / `t.endSyncOut = "\x1b[?2026l"`
unconditionally — "we just assume it will be ok... either just swallow it, or
handle it." `tScreen.draw()` (`tscreen.go:747-786`) wraps every frame in
`t.startBuffering()` / `defer t.endBuffering()` (`tscreen.go:727-733`, which
emit those exact escapes), so **every call to `screen.Show()` is already
DEC-2026-synchronized** — Vincent's `go.mod` pin (`tcell/v2 v2.13.9`) needs no
change and no new code for this. This answers "does tcell support it": yes,
transparently, since at least this pinned version.

**Why flicker isn't actually the risk here — redundant CPU work is.** tcell's
`Show()` diffs against the last-drawn cell buffer and only emits escape
codes for cells that changed (`drawCell`, referenced at `tscreen.go:497,
767`) — that's the actual anti-flicker mechanism, and it's unconditional,
not something Vincent opted into. The real cost of
`a.draw(); a.screen.Show()` running on *every* `tcell.EventMouse` including
raw `MouseMotionEvents` (`app.go:679-692`, `EnableMouse(...MouseMotionEvents)`
at `app.go:463,526`) is recomputing the draw — re-walking the tree, re-running
`HighlightVisible`, repainting every cell in every rect — on every pixel of
mouse movement, not visible tearing. `internal/editor/highlight.go:44-52`'s
bounded lead/viewport tokenization already caps that cost for the syntax
path; the git panel's hover-clear-on-any-outside-event behavior
(`CLAUDE.md`'s gitpanel notes) is the more likely place motion-driven
redraws actually cost something.

**How the two mouse-friendly reference tools handle this:** `hunk`
(OpenTUI/React) is a retained-mode renderer with its own cell-diffing show
step (same category of mechanism as tcell's), and layers one explicit
optimization on top: `VIEWPORT_READ_COALESCE_MS = 16`
(`vincent-refs/hunk/src/ui/lib/viewportTiming.ts:2`) — "Delay used to
coalesce imperative ScrollBox viewport reads to roughly one frame," used
specifically to stop rapid wheel/drag events from forcing a geometry
re-measure on every event. `mouseCapture.ts` (`hunk/src/ui/lib/mouseCapture.ts:9-12`)
also pins a drag gesture to one `Renderable` for its duration ("OpenTUI has
no public pointer-capture API yet... it captures the renderable under the
first drag event, which may be replaced while the gesture is active") — the
same problem Vincent's `dragMode` state machine (`CLAUDE.md`'s "Patterns
worth preserving") already solves by tracking one string field rather than
re-hit-testing every motion event.

**Recommendation:** no architectural change needed — Vincent already has
tcell's cell-diffing + DEC 2026 sync, and `dragMode` already solves gesture
pinning. If motion-driven redraw cost ever becomes visible (e.g. git-panel
row hover repainting on every pixel), take hunk's lead and add a narrow,
local coalesce (skip calling `a.draw()` for a `MouseMotionEvents` event that
produced no state change — same row still hovered, same cell under cursor)
rather than anything at the tcell/terminal level.

## 7. Syntax highlighting — Chroma's real gap

**The gap is lexer category coverage, not style/color mapping — and Vincent's
existing mapping is already better than any bundled Chroma style, because it
bypasses Chroma's `styles/*.xml` entirely.** `internal/editor/highlight.go:141-183`
(`styleForToken`) hand-maps `chroma.TokenType.Category()` straight to
`theme.Syn*` fields — it never touches any of the 27 bundled style XMLs in
`$(go env GOMODCACHE)/github.com/alecthomas/chroma/v2@v2.24.0/styles/`
(dracula, monokai, nord, catppuccin-*, etc.). So "recommend the best Chroma
style" doesn't apply — there's no style file in the render path to swap.

The real ceiling: **Chroma is a single-pass regex lexer, not a parser**, so
it has no notion of "this identifier is a declared type" vs. "this identifier
is a plain variable" — only lexical categories a regex can recognize.
Inspecting the actual Go lexer confirms exactly where this bites
(`$(go env GOMODCACHE)/github.com/alecthomas/chroma/v2@v2.24.0/lexers/go.go`):
- Line `{Words(...builtins...), ByGroups(NameBuiltin, Punctuation), nil}` and
  the call-site rule
  `{`([a-zA-Z_]\w*)(\s*)(\()`, ByGroups(NameFunction, UsingSelf("root")...`
  **does** tag anything immediately followed by `(` as `NameFunction` —
  so function/method calls already get `theme.SynFunction` blue in Vincent
  today. That part already works.
- But a plain identifier with no trailing `(` — a declared type name, a
  struct field, a package qualifier like `fmt` in `fmt.Println`, a local
  variable — falls through to the catch-all
  `{`[^\W\d]\w*`, NameOther, nil}`. `styleForToken`'s `chroma.Name` switch
  (`highlight.go:159-181`) has no case for `chroma.NameOther`, so it returns
  `base` (plain `theme.Text`, no color at all). That's the actual "not real
  syntax" complaint: type names, package qualifiers, and struct fields all
  render in the same plain foreground as ordinary prose, because a regex
  lexer has no semantic model of "this name was declared as a type."
  Tree-sitter (or any real parser) closes this gap by walking a syntax tree
  and running language-aware queries — which is exactly why it needs cgo
  bindings to the C library, which `CLAUDE.md`'s non-negotiable #3 (single
  static binary, no cgo) already rules out.

**`herdr-file-viewer` doesn't solve this in-process either — it shells out.**
Its `syntax` renderer is external `bat` (`config.rs:1540`:
`syntax: vec!["bat".to_string(), "-".to_string()]`), and `bat` is built on
`syntect`, a Rust port of Sublime Text's `.sublime-syntax` grammars — also
regex/state-machine based, not tree-sitter, and also incapable of "this is a
declared type" semantic tagging for the same structural reason as Chroma.
The one tool in this whole comparison set that ships genuinely "real" syntax
highlighting does it by running a separate compiled binary at runtime — the
exact thing Vincent's single-static-binary rule forbids. This is useful
negative evidence: even the best comparable tool didn't solve this in-process
either; it spent an external-dependency budget Vincent has explicitly
declined to spend.

**Recommendation:** keep Chroma, keep the existing hand-rolled
`styleForToken` mapping (don't switch to a bundled `styles/*.xml` — that
would be a regression, since none of them are tuned to Vincent's palette).
One concrete, low-risk improvement that's still purely mechanical (no
parser): add a `chroma.NameOther` case to `styleForToken` that colors an
identifier `theme.SynType` when it starts with an uppercase letter and
`theme.SynVariable` otherwise — a heuristic, not semantic analysis, but it
matches the Go/Rust/TypeScript convention that exported/type identifiers are
capitalized, and it stops every declared type name and struct field from
reading as flat, uncolored `theme.Text`. It won't be exactly right (a
capitalized package-level `const` and a type name will both go
`SynType`), but it closes most of the visible gap between "everything call
sites are colored, plain identifiers are blank" without adding a dependency
or a parser.

## What I did not check

- Did not fetch Zed's actual shipped "One Dark" (or whichever theme is
  default-on-first-run) theme JSON — only the gpui *fallback* `ThemeColors`
  Rust constants in `crates/theme/src/default_colors.rs` and
  `fallback_themes.rs`, since no `assets/themes/*.json` exists in this
  sparse checkout. The literal on-screen git-status colors a fresh Zed
  install shows could differ from the fallback values quoted above.
- Did not verify Zed's `recent_projects` crate at all — it isn't part of
  this sparse checkout (`crates/` only has `ui`, `markdown`, `theme`,
  `project_panel`, `buffer_diff`, `git_ui`, `markdown_preview`, `editor`).
  Everything said about it above is "not verifiable here," not "confirmed
  absent."
- Did not run `go get` or check pkg.go.dev for goldmark's current exact
  version or transitive dependency count/binary-size delta — the
  recommendation to use `github.com/yuin/goldmark` over `glamour` is
  architectural (pure-Go AST vs. ANSI-string round-trip), not backed by a
  measured `go build` size diff. No network fetch was attempted for this
  task (kept to local `vincent-refs` checkouts, the Go module cache already
  on disk, and Vincent's own source).
- Did not build or run Vincent, `hunk`, `herdr-file-viewer`, or `ftdv` to see
  any of this rendered — this is a source-reading pass, not a visual
  comparison. Contrast-ratio numbers were computed with the WCAG relative-
  luminance formula in a small Python snippet, not measured against a real
  terminal's actual color rendering (which can differ slightly from the sRGB
  math depending on the terminal emulator's color management).
- Did not inspect `hunk`'s `HunkFileNav.tsx` or `App.tsx` in depth beyond
  confirming the StatusBar/MenuBar split — there may be more chrome-layout
  detail in there relevant to item 4 that a deeper pass would surface.
- Did not check whether Vincent's own `~/.config/vincent/config.json` schema
  or `internal/config/config_test.go` has any existing precedent for a
  boolean toggle (only the string-enum `IconsMode` pattern was checked) —
  the `TabBar bool` recommendation in item 4 assumes that pattern extends
  cleanly but wasn't tested against the actual JSON marshal/unmarshal code.
