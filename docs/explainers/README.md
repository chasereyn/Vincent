# Vincent explainers

One page per subsystem, written from the code on `master` as it stands
today, not from the roadmap. Where CLAUDE.md and the code disagreed, the
code won, and the page says so.

- [review-loop.md](review-loop.md) — writing a note on a diff line and
  sending the batch to the agent that wrote the code, including the exact
  wire format and why `herdr agent prompt` is never used.
- [diff-viewer.md](diff-viewer.md) — how a diff gets parsed, word-tinted
  with a real Myers diff, and painted with dual gutters, and how the diff
  text itself gets fetched from git or built in-process.
- [changes-panel-and-git-writes.md](changes-panel-and-git-writes.md) —
  the Changes panel's one-parse-many-readers `git status`, and the three
  blunt writes (commit-all, push, checkout) with their refusals.
- [multi-repo.md](multi-repo.md) — how Vincent decides which repository
  owns a file when the root is a flat folder of repos, and which repo the
  git writes act on.
- [editor-and-conflicts.md](editor-and-conflicts.md) — the small editing
  engine (buffer, undo, indent, find/replace) and, in full, what happens
  when an agent rewrites a file you have open and unsaved.
- [render-loop.md](render-loop.md) — why moving the mouse used to make
  Vincent flicker, why it stopped, and how git reads stay off the UI
  thread while an agent hammers the repo.
- [markdown.md](markdown.md) — the rendered markdown view, `Esc m`'s
  raw/rendered toggle, and what doesn't render well (wide CJK glyphs).
- [search-and-finder.md](search-and-finder.md) — the fuzzy filename
  finder and the content-search modal, and the one file index both share.
- [keys-and-chrome.md](keys-and-chrome.md) — the Esc-leader key table,
  the generated cheatsheet, the color palette, and the file tree's chrome.
