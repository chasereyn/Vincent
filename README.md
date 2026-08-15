# Vincent

**A read-only, mouse-first terminal client for reviewing code that AI agents wrote.**

Vincent van *Go*.

Your agent writes the code. Vincent shows you what changed, lets you click
lines and leave review notes, and hands those notes straight back to the
agent — without leaving the terminal. One static binary, pure black, mouse
everywhere.

It is not an editor. There is no insert mode, no save, no undo. If you want
to change a line, tell the agent.

## Status

**Phase 1.** Inline diffs work. The fork builds, the test suite is green on
Linux, macOS, and Windows, and every surface is black. The review notes and
the git panel are still ahead — see the roadmap.

## Install

```sh
git clone https://github.com/chasereyn/vincent
cd vincent
make install        # builds, then copies to ~/.local/bin
```

`make build` alone leaves the binary at `./bin/vincent` (`.exe` on Windows)
if you'd rather place it yourself. Override the destination with
`make install INSTALL_DIR=/usr/local/bin`.

Requires Go 1.24+ and `git` on PATH. No cgo, no runtime, no external
rendering tools.

## Use

```sh
vincent                     # open the current directory
vincent <directory>         # open a project, or a folder of projects
vincent <file>              # open a file; its parent becomes the root
```

Point it at **a repository**, not a folder of repositories — until phase 4
lands, git features resolve against the directory you opened, so a parent
folder gives you a file tree and nothing else.

Click `≡` at the top left, right-click anywhere, or double-tap `Esc` for
the action menu. There are no `Ctrl+` shortcuts on purpose — they fight
tmux and terminal emulators. Leader keys are `Esc`-prefixed.

| | |
|---|---|
| `Esc Esc` | Action menu — every command, always |
| `Esc d` | **Diff the active file** |
| `Esc p` | Find file by name |
| `Esc f` | Find in file |
| `Esc t` | Show / hide the file tree |
| `Esc w` | Close tab |
| `Esc q` | Quit |

Mouse: click a file to open it, click a tab to switch, click `×` to close,
drag the splitter to resize the tree, scroll anywhere. Click a change bar
in the gutter to jump straight into that change in the diff.

### Diffs

`Esc d` opens the active file's diff — inline, VS Code / Zed shaped: old
and new line numbers side by side, `±` markers, red and green row tints,
and a darker tint over just the characters that changed on a line edited in
place. Code inside a diff is syntax-highlighted like code anywhere else.

You can also click a change bar in the editor's git gutter, which opens the
diff scrolled to that change, or right-click a file in the tree.

A diff tab keeps itself current: when the agent writes to the file again,
the diff re-runs in place without losing your scroll position. Staged and
unstaged changes both show — the view is everything that has happened since
the last commit.

## Roadmap

| Phase | What | Status |
|---|---|---|
| 0 | Fork, strip, blacken | ✅ |
| 1 | Inline diff viewer | ✅ |
| 2 | Review notes + handoff back to the agent | next |
| 3 | Read-only git panel + branch checkout | |
| 4 | Multi-repo workspace | |
| 5 | Content search + markdown rendering | |

The intended shape: point Vincent at a folder containing many repos, click
any file, and the git panel follows — the active repo is derived from the
active file rather than chosen from a switcher.

## Built on spice-edit

Vincent is a fork of [spice-edit](https://github.com/cloudmanic/spice-edit)
by Spicer Matthews (MIT, Copyright 2026 Cloudmanic, LLC), a mouse-first
terminal *editor*. Vincent keeps its tcell shell, file tree, fuzzy finder,
Chroma highlighting, and mouse handling — including a pile of hard-won
terminal-quirk fixes — and deletes the half that exists so a human can type
into a file.

Thanks to that project for the foundation. See `CLAUDE.md` for exactly what
was removed and why.

## License

MIT. See `LICENSE`, which retains the upstream copyright.
