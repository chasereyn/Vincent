# Review notes + handoff — research for Vincent phase 3

Three reference repos under `~/Developer/vincent-refs/`: `tuicr` (Rust, composer UX),
`herdr-reviewr` (Rust, herdr handoff), `hunk` (TypeScript, live-session skill model).
Plus the installed `herdr` 0.8.2 CLI, verified live.

## 1. tuicr — comment composer UX

**Range selection.** `v`/`V` enters visual mode over diff rows; `j`/`k` extend it; `c` or
`Enter` opens the composer for the selected range. A plain `c` on a single line (no visual
mode) makes a line comment; `c` off a diff line makes a file comment; `C` always makes a
file comment; `<leader>c` makes a review-level (whole-review) comment. Mouse: drag in the
diff highlights a range the same way visual mode does.

**Composer is inline, not modal.** `src/ui/comment_panel.rs:100-140`
(`format_comment_input_lines`) draws the box **directly under the annotated line**, in the
diff flow, as a bordered block: `"    │  "` is `BORDER_PREFIX` (`comment_panel.rs:17-18`) —
4 pad columns so a `│` can run up through the diff lines above without colliding with the
6-column `▌` add/del gutter mark, then 2 spaces of content indent. Header line shows
`Add`/`Edit` + `L{n}` or `L{start}-L{end}`, footer line shows the save/cancel hint
(`Shift-Enter`/`Alt-Enter` depending on `supports_keyboard_enhancement`). **Recommend
Vincent copy this shape exactly**: an inline bordered box grown into the diff viewport
under the clicked line(s), not a floating modal — it keeps the code and the note in the
same eye line, which is the whole point of a diff-first reviewer, and Vincent already has
the row-rect infrastructure (`gitpanel.go`) to grow a synthetic row into the diff's
`[]diff.Row` list the same way a diff tab already reuses `Tab`'s line list.

**Comment types cycle** with `Tab`/`Shift-Tab` inside the composer, in `comment_types`
config order (`docs/KEYBINDINGS.md`, "Comment mode" table). Types are user-configurable;
the built-in reserved id is `none` (`CommentType::NONE_ID`, `src/model/comment.rs:91`) —
typeless comments emit no `[TYPE]` prefix or badge anywhere. Default shipped types (from
the skill doc, `skills/tuicr/SKILL.md`) are `issue` (blocking), `suggestion` (consider),
`note` (answer/ack), `praise` (no action). **Recommend Vincent ship exactly these four**,
typed via `Tab` cycling in the composer, `none` as the default/untyped state — it is a
small, already-battle-tested vocabulary an agent skill can hard-code semantics for.

**Storage.** Comments are `Comment` structs (`src/model/comment.rs:158-188`): `id` (uuid),
`content`, `comment_type`, `created_at`, `side: Option<LineSide>` (`Old`/`New`),
`line_range: Option<LineRange{start,end}>` (inclusive), `author` (free text, defaults
`"user"`, agents pass `--username`), `lifecycle_state` (`LocalDraft` / `PushedDraft` /
`Submitted` — only meaningful for their GitHub/GitLab/Bitbucket push integration, not
relevant to Vincent), and `commit_id: Option<String>` for per-commit scoping. Comments
persist to a JSON session file under the OS data dir, one file per review session,
discoverable via `tuicr review list`.

**Export / wire format — exact.** `src/output/markdown.rs:259+` (`generate_markdown`).
Plain Markdown, copied to the clipboard (never sent anywhere else — tuicr has no agent-pane
injection of its own). Shape, top to bottom:

```
## Session: <slug>              (only if a session slug was passed)

<intro line>                    (configurable, e.g. "Please address these review comments:")

Reviewing pull request <repo>#<n>: <title>     (PR mode only)
URL: <url>
Head: <short-sha>
                                  (or, non-PR: a one-line scope banner, e.g. "Reviewing working tree changes")

Comment types: ISSUE (blocking problem), SUGGESTION (consider implementing), ...   (legend, only used types)

Summary: <session notes>          (optional free text)

## Comments

1. **[ISSUE]** `src/main.rs:42` - Handle the empty case here.
2. **[SUGGESTION]** `src/main.rs:10-14` (commit a1b2c3d) - This range can be simplified.
   continuation lines are indented to align under the marker
3. `src/main.rs` - a file-level comment has no line suffix at all
```

Line-address formatting (`markdown.rs:420-440`): single new/context line → `` `file:N` ``;
range → `` `file:start-end` ``; **old-side (deleted) lines get a `~` prefix**:
`` `file:~N` `` / `` `file:~start-~end` ``; file comment → `` `file` `` with no line suffix.
Untyped comments print with no `**[TYPE]**` marker at all. `Comment types:` legend line and
`## Comments` header are both individually suppressible via config (`ExportConfig`), so an
agent-facing preset can strip everything except the numbered list.

Clipboard delivery (`markdown.rs:78-100`): macOS always tries `pbcopy` first (works over
SSH/tmux, unlike OSC 52 through Terminal.app); else OSC 52 is preferred under
`TMUX`/`SSH_TTY`/`ZELLIJ`, routed through `tmux load-buffer -w -` when inside tmux so tmux
forwards it to the *outer* terminal; else X11/Wayland `xclip`/`wl-copy`; else the `arboard`
crate; OSC 52 is the last-resort fallback. **This entire clipboard cascade is irrelevant to
Vincent** — Vincent already has `internal/clipboard` (OSC 52) for copy-path, and phase 3's
delivery path is herdr pane injection, not clipboard, per Vincent's own CLAUDE.md. Keep the
markdown wire-format lesson, discard the clipboard-fallback lesson.

**Stale comments — tuicr does not solve this.** There is no anchor-rebasing or
staleness flag for *local* line comments when the underlying file changes. `stale` in the
codebase refers only to remote-PR-thread outdatedness (GitHub's own `outdated` flag,
rendered as a `[github @user outdated]` badge, `src/ui/diff_unified.rs:1883`) and to
internal cache/diff-watch staleness (`diff_watch_result_is_stale`,
`src/app/diff_load.rs:1506`) — neither is "the file changed under a local comment's line
number." A `Comment.line_context: Option<LineContext>` field exists (path/line snapshot at
creation) but nothing in `src/app/diff_load.rs`'s reload path re-anchors or flags a comment
against it; local comments just stay keyed to their original line number and side, so a
file edit that shifts lines silently misattributes the comment. **This confirms Vincent's
own plan in CLAUDE.md is already better than tuicr here**: freezing the verbatim diff
snippet as the anchor and flagging staleness when the file leaves the changeset is a real
improvement, not something to copy from tuicr.

## 2. herdr-reviewr — the handoff reference

Everything lives in `src/herdr.rs` (single file, ~330 lines, extremely well-commented).

**Finding the agent pane.** `send_target()` (`herdr.rs:222-244`):
1. `herdr agent list` → parse `.result.agents[]` (each has `agent`, `agent_status`,
   `pane_id`, `tab_id`, `workspace_id`, `cwd`, optionally `name`/`display_agent`/
   `state_labels`).
2. Candidates = every entry with `agent.is_some()`, `workspace_id == ` this pane's
   `$HERDR_WORKSPACE_ID`, and `pane_id != ` this pane's own `$HERDR_PANE_ID`
   (`candidates()`, `herdr.rs:280-291`). Scoped to *workspace*, not tab.
3. Zero candidates → refuse with a one-line reason. One candidate → send straight to it,
   no picker. Multiple → open a picker (`SendTarget::Many`), rows labelled by
   `name` → `display_agent` → `agent` (kind) → `pane_id` as last resort
   (`AgentPane::row_name`, `herdr.rs:249-256`), plus a best-effort tab label from a second
   `herdr tab list --workspace <ws>` call (only paid for when there's a picker).

**Injecting text.** `send_text()` (`herdr.rs:305-308`) runs
`herdr pane send-text <pane_id> "<wrapped text>"`. It wraps the payload itself in a
bracketed-paste frame (`pasted()`, `herdr.rs:310-323`):
`\x1b[200~` + body + `\x1b[201~`, where the body is rebuilt char-by-char stripping any
`\x1b[201~` that would otherwise appear mid-payload (a diff snippet is raw file content and
can legitimately contain that byte sequence; splicing it out defeats a two-call construction
attack too, e.g. `"a\x1b[201\x1b[201~~b"` → `"ab"` inside the frame). Comment
(`herdr.rs:213-217`) explains *why* bracketed paste and not raw bytes: "a paste inserts
verbatim in any input mode, where raw bytes execute as commands in a vim-style input resting
in normal mode." **Vincent must copy this stripping logic exactly** if it wraps payloads
itself (see the herdr-CLI verification below for whether it still needs to).

**No Enter is ever sent.** `pane send-text` "writes input, no Enter" — deliberate, so the
human reviews before submitting. After the send, `focus()` (`herdr.rs:326-328`) runs
`herdr agent focus <pane_id>` to bring the pane into view so the reviewer can add context
and hit Enter themselves.

**Picking the target when several exist.** A picker UI (not researched further — outside
`herdr.rs`) lists `AgentChoice{pane_id, name, state, tab}` rows in herdr's own list order
(observed: grouped by workspace, then by tab). The reviewer clicks/selects one.

**Error handling.** Every herdr call goes through `herdr()` (`herdr.rs:79-93`), which logs
the full argv + JSON stderr envelope to a private log and returns an opaque
`anyhow::Error`; **no caller ever surfaces herdr's own error text to the status line** —
`send_target()`'s failure paths hand-write a plain sentence instead
(`"herdr did not answer — copy to the clipboard instead"`,
`"no agent here — copy to the clipboard instead"`) because herdr's JSON error (e.g.
`{"error":{"code":"pane_not_found","message":"pane w8:p2 not found"}}`) names pane ids the
reviewer never saw and doesn't fit a status line. **Recommend Vincent do the same**: never
show a raw herdr stderr blob in the status bar; translate every failure into one short
sentence and log the real payload.

**When herdr is unreachable.** Every blocking herdr call in the startup/cosmetic-labeling
path is run on its own thread with a bounded wait (`ANSWER_BOUND` = 2s,
`herdr_on_thread`/`plugin_config_dir_with`, `herdr.rs:104-135, 187-210`) so a wedged herdr
daemon degrades to defaults rather than hanging the UI — explicitly called out as fixing
"the blank grid issue #4." The send path itself (`send_target`) is not time-bounded in the
same way; a failed `herdr agent list` call is a hard refuse with the clipboard-fallback
message, not a retry loop.

**herdr-reviewr predates `herdr agent prompt`.** Its docs (`docs/herdr-api-notes.md`) are
pinned to herdr 0.7.5 (verified live 2026-07-31) and explicitly note "herdr 0.7.5 removed
`agent send` (replaced by the logical-key `agent send-keys`)" — `agent prompt` doesn't
exist yet in that world. `pane send-text` is described there as unchanged literal-text,
no-Enter semantics "since 0.7.0."

## 3. hunk — live-session model and skill

**hunk's model is the opposite direction from Vincent's, and that's worth naming
explicitly.** hunk is a *pull* / bidirectional live-session tool: the agent runs
`hunk session *` CLI commands against a local loopback daemon (`hunk daemon serve`,
`AGENTS.md`) that the running TUI has already registered with. The agent inspects the
human's current diff view, comments and highlights inline in the human's live session, and
steers the human's viewport (`--focus`). Vincent's phase 3 is a *push*, one-shot,
asynchronous handoff: the human writes notes, then one keypress drops literal text into the
agent's own terminal pane via herdr — no daemon, no socket, no CLI the agent has to poll.
**Do not import hunk's daemon/socket architecture into Vincent** — it solves a different
problem (interactive co-review) and would violate Vincent's single-static-binary,
no-runtime-dependency non-negotiable (`CLAUDE.md` #3) by requiring a long-lived local
server process.

**Annotation model** (`src/core/review/types.ts:18-56`, `ReviewNoteV1`): `id`, optional
`parentId` (threaded replies), `source` (normalized: user/agent/ai — classified once at the
boundary, never re-interpreted downstream), `fileKey`, an `anchor: ReviewRangeAnchorV1`
(`oldRange`/`newRange` as `[start,end]` tuples, a `preferred` single line+side the renderer
scrolls to, `intersectingHunkIndices[]`, one `ownerHunkIndex`), `summary`, `rationale`,
optional `markup` (STML — an experimental rich-text mini-language for terminal notes,
opt-in only), `author`, `tags[]`, `confidence` (`low`/`medium`/`high`), and `editable`.
Notably richer than tuicr's flat `Comment` — worth stealing exactly one idea:
**`intersectingHunkIndices` vs `ownerHunkIndex`** as two separate fields (render at one
place, but know every hunk a range actually touches) is a cleaner anchor model than tuicr's
single `line_range`, and directly informs Vincent's own "never rebase, flag stale instead"
plan — Vincent's note should probably store both the owning hunk and the frozen line range,
the same split.

**The skill file** (`skills/hunk-review/SKILL.md`) is generated, not hand-written — a
project convention worth copying: "`skills/hunk-review/SKILL.md` is generated. Edit
`src/hunk-review/skillDocument.ts` ... then run `bun run generate:skill`; never hand-edit
the skill file." (`AGENTS.md`). Its content is a CLI reference (session selection,
inspect/navigate/reload/comment/highlight commands, exact flags, common error strings) plus
a short "Guiding a review" workflow section. **Recommend Vincent's future skill follow the
same shape but adapted to the push model**: since Vincent's payload lands as literal text
directly in the agent's own prompt (not behind a CLI the agent has to query), the "skill"
Vincent ships is less a command reference and more a short **legend the agent needs to
parse the wire format that just appeared in its input** — e.g. what `[ISSUE]` vs
`[SUGGESTION]` mean, that `file:~N` means a deleted-side line, that a comment references a
frozen code snippet rather than a live line number and may already be stale. That legend
can either be (a) a `SKILL.md` shipped alongside Vincent for the agent's harness to
autoload, generated from one source of truth the way hunk does, or (b) inlined directly in
every exported batch the way tuicr's `Comment types:` legend line already does — cheaper,
always in context, no discovery problem, and consistent with Vincent having no persistent
CLI surface of its own. Given Vincent's "no runtime, single binary, push not pull" shape,
**recommend (b) as the primary mechanism** and (a) only as a supplementary one-time skill
that explains the wire format for repos where the agent's harness supports skill files.

## herdr CLI verification (installed 0.8.2, HERDR_ENV=1, no text actually sent)

Ran (help/schema only, no `send-text`/`prompt` calls made against a live pane):
`herdr agent`, `herdr agent prompt --help`, `herdr agent send-keys --help`,
`herdr pane`, `herdr pane send-text --help`, `herdr pane list --help`,
`herdr agent list --help`, `herdr agent focus --help`, `herdr api`, `herdr api schema --json`,
`herdr --skill`.

**Two different commands now exist and they are not interchangeable:**

- **`herdr pane send-text <pane_id> <text>`** — "Send literal text to a pane." No `--wait`,
  no submit option. Its own `--help` footer says: `next: herdr pane run <PANE_ID> <COMMAND>
  sends text and Enter in one call` — i.e. `send-text` itself still deliberately never
  sends Enter, exactly the 0.7.0-era semantics herdr-reviewr already relied on.
- **`herdr agent prompt <target> <text> [--wait] [--until <state>...] [--timeout <ms>]`** —
  new since herdr-reviewr's 0.7.5 baseline. Per `herdr --skill`'s own prose: *"`agent
  prompt` honors the pane's live bracketed-paste mode and sends text **followed by encoded
  Enter after a short delay**."* It rejects with `agent_blocked` if the agent is already at
  an approval/question dialog, and `--wait` blocks for a settled lifecycle state
  (`idle`/`done`/`blocked` by default). **This command submits.** It is built for
  "delegate a task to an agent and wait for it," not "stage text for a human to review
  before sending."

**Answer: for Vincent, `herdr pane send-text` is the right command today**, unchanged in
spirit from what herdr-reviewr already used. `herdr agent prompt` is the wrong tool — it
would submit the review batch to the agent unattended, which contradicts Vincent's own
design (`CLAUDE.md` Phase 2 notes: "focus, so the reviewer submits") and its being a
read-only, human-in-the-loop reviewer.

**Does `pane send-text` already do bracketed paste for you?** Not confirmed either way.
`herdr --skill`'s bracketed-paste sentence is written specifically about `agent prompt`;
nothing in `herdr pane send-text --help` or the JSON API schema
(`PaneSendTextParams { pane_id: string, text: string }` — no paste-mode flag) documents
`send-text`'s wire behavior. Given herdr-reviewr had to hand-wrap `pane send-text` in
`\x1b[200~...\x1b[201~` itself as of 0.7.5, and nothing in the 0.8.2 docs says that
changed for this specific command, **recommend Vincent wrap the payload in bracketed paste
itself**, copying herdr-reviewr's exact stripping logic for embedded `\x1b[201~`
terminators (`herdr.rs:310-323`) — it's cheap, it's idempotent if herdr also does it
server-side, and it is the only way to guarantee a multi-line diff-snippet-bearing review
batch doesn't get interpreted as keystrokes by an agent CLI sitting in some modal input
state.

**Error shape, unchanged**: `herdr --skill` states plainly, "CLI server errors are JSON on
stderr with exit status 1. CLI syntax errors exit with status 2" — matches
herdr-reviewr's documented `{"error":{"code":...,"message":...}}` envelope. Vincent should
adopt herdr-reviewr's rule: log the JSON, never show it, translate to one short sentence.

**Picking a target agent, current API**: `herdr agent list` (no flags — same as
herdr-reviewr's baseline) plus `herdr pane list --workspace <id>` remain the two calls; no
new "pick the best agent" helper was added. Vincent should replicate herdr-reviewr's own
`candidates()` filter (workspace-scoped, exclude own pane, `agent` field present) rather
than inventing a new selection rule.

## What I did not check

- Did not read tuicr's `src/app/comments.rs`, `src/app/visual.rs`, or
  `src/input/keybindings.rs` in full — the visual-mode range-selection *implementation*
  (as opposed to its documented keybindings) was not traced line by line.
- Did not read tuicr's `src/comment_vim.rs` / `src/app/comment_vim.rs` (the optional
  `edtui`-backed modal editing mode for the composer) — only the keybindings doc.
- Did not read herdr-reviewr's picker UI code (`src/ui.rs`) or `tests/send_flow.rs` /
  `tests/pane_actions.rs` in full — only `src/herdr.rs`, which is where the actual herdr
  CLI calls live; the picker's visual layout and the full test assertions were not
  inspected.
- Did not run any `herdr pane send-text` or `herdr agent prompt` call against a real pane
  (as instructed) — the bracketed-paste behavior of `pane send-text` on 0.8.2 is inferred
  from documentation gaps, not confirmed by a live capture/tcpdump-style check of the pane's
  actual input bytes. If this matters before shipping, capture a real pane's input stream
  once (e.g. `cat -v` in the target pane) while sending a payload containing a literal
  `ESC` to see whether herdr 0.8.2 already wraps `send-text` server-side.
- Did not inspect hunk's `src/session/agent/surface.ts` or `bridge.ts` (the actual
  command-dispatch and capability/digest mechanics behind `hunk session *`) — only the
  generated skill doc and `AGENTS.md`'s prose description of the daemon model.
- Did not look at hunk's STML markup language details (`src/ui/lib/stml/`) beyond noting it
  exists and is opt-in/experimental.
- Did not check whether Vincent's target coding agents (Claude Code, Codex, etc.) have their
  own bracketed-paste handling quirks beyond what herdr-reviewr's comment already documents
  ("vim-style input resting in normal mode" eating raw bytes) — that comment names the
  failure mode but I did not verify it against a specific agent CLI.
