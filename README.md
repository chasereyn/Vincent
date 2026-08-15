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

**Phase 0.** The fork builds, the test suite is green on Linux, macOS, and
Windows, and every surface is black. None of the review features exist yet
— see the roadmap.

## Install

```sh
git clone https://github.com/chasereyn/vincent
cd vincent
make build          # ./bin/vincent
```

Requires Go 1.24+ and `git` on PATH. No cgo, no runtime, no external
rendering tools.

## Use

```sh
vincent                     # open the current directory
vincent <directory>         # open a project, or a folder of projects
vincent <file>              # open a file; its parent becomes the root
```

Click `≡` at the top left, right-click anywhere, or double-tap `Esc` for
the action menu. There are no `Ctrl+` shortcuts on purpose — they fight
tmux and terminal emulators. Leader keys are `Esc`-prefixed.

## Roadmap

| Phase | What | Status |
|---|---|---|
| 0 | Fork, strip, blacken | ✅ |
| 1 | Inline diff viewer | next |
| 2 | Review notes + handoff back to the agent | |
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
