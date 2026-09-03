# Keys and chrome

Every key Vincent responds to lives in one file, and every color it draws
lives in another. This page covers the Esc-leader keyboard scheme, the
`Esc ?` cheatsheet that documents it automatically, the color palette, and
the file tree's chrome (headers, indent guides, icons) plus the root
picker that switches which project folder Vincent is even looking at.

## How it works

**The leader.** Vincent binds no `Ctrl+` shortcuts at all — they fight
tmux and terminal emulators over SSH, and Esc is the one modifier
that's trusted to survive that. Pressing Esc "arms the leader"; the next
rune pressed within `doubleEscMs` (1500 milliseconds, `app.go`) fires
whatever it's bound to; a second Esc, or any unbound key, cancels. The
1500ms window is itself a tuned number, not a default: CLAUDE.md records
that spice-edit's original 500ms window made `Esc q` "routinely fail to
quit" for a typist reading with one hand on the mouse, and a leader that
silently expires reads as a broken keybinding, not a timing detail.

**`internal/app/leader.go`'s `leaderBindings()` is the single source of
every letter binding.** Each entry is a `leaderBinding` — a trigger rune,
a short hint (terse on purpose: `"copy review"`, not `"Copy review to
clipboard"`, because it has to survive truncation in a one-row status
bar), a group name (`Review`, `Git`, `Search`, `View`, `Edit`, `Session`
— purely presentational, for the cheatsheet's dividers), and the `*App`
method it fires. `leaderKeyBindings()` is a second, smaller table for
bindings triggered by a **named** key rather than a rune — today just
`tcell.KeyEnter` (labeled `⏎`), because tcell reports Enter as its own
key constant rather than as a printable rune, and folding it into the
rune table would force every consumer of that table to special-case an
entry with no glyph. `leaderRows()` merges both tables, grouped, into the
one ordered list that both the status bar's armed-leader hint and the
`Esc ?` cheatsheet read from — deliberately, so a binding cannot ship
undocumented in one place while correctly described in the other.

The full letter map as it stands: `d` diff, `e` open file (the way out
of a diff — open the real file for a correction smaller than a note),
`r` review note (**not** redo — "Vincent is a review client: writing a
note is the second most common thing anyone does in it, and redo is
inherited machinery"), `y` copy review, `g` Changes panel, `c` commit,
`P` push (capitalized deliberately — lowercase `p` is the file finder,
and pushing to a remote "is not a key anyone should hit reaching for
it"), `b` branch picker, `p` find file, `/` find, `F` find in files, `f`
explorer toggle, `t` tab bar toggle, `z` fold all, `m` markdown toggle,
`o` root picker, `s` save, `S` save as, `n` new file, `u`/`U` undo/redo,
`a` select all, `w` close, `q` quit, `?` the cheatsheet itself. `x`/`v`
(clipboard) are intentionally **not** bound — the terminal's own
Cmd+C/Cmd+V already covers that path via mouse selection, and a third
channel for the same action just adds confusion; `c` was on that
unbound list too, until phase 3b spent it on commit, "which is the same
argument from the other side: the key is free precisely because copy
does not need it."

**The cheatsheet, `Esc ?`** (`internal/app/cheatsheet.go`), is what
replaced the old `≡` action menu entirely — CLAUDE.md's non-negotiable 5
records the exact sentence that ended the menu: "the Esc leader works
great — the menu is not needed," said after the owner's first real
session with 0.3.0. The menu had been paying for itself twice by then: a
tab-bar button, a modal with hover state and per-row enable predicates,
and a second code path into every action the leader key already reached
— all to restate a key table. What was actually load-bearing was the one
thing nowhere else existed: a list of what the keys are. So the
cheatsheet is **read-only** — no hover, no selection, no actions; Esc,
Enter, or a click anywhere dismisses it — which is exactly what lets it
be generated straight from `leaderRows()` instead of carrying a second,
independently-maintained list of rows. A binding added to `leader.go`
appears in the cheatsheet on the very next frame, no edit to
`cheatsheet.go` required.

The cheatsheet's layout adapts to how much screen it actually has,
through `cheatsheetFit`, in three tiers, each tried only if the previous
one doesn't fit within the screen height minus a two-row margin: the
ideal single column with a divider under the title and between every
group; the same column with every divider dropped (a blank separator row
is the cheapest thing to sacrifice); and, as a last resort, two columns
splitting whole groups between them — never splitting a single group's
rows across the two, since "Review" appearing at the bottom of one column
and the top of the other would read as more confusing than the
two-column layout is meant to fix. A 24-row terminal is treated as a real
target, not a hypothetical, in the code's own comment.

**The theme.** `internal/theme/theme.go` ships exactly one palette — no
theme file, no JSON, no runtime configuration; to restyle Vincent you
edit this file and recompile. The ground color, `#030405` (not pure
black), is Chase's actual Ghostty terminal background, set explicitly via
`tcell.NewRGBColor` rather than `tcell.ColorDefault` — the latter would
inherit whatever the host terminal happens to be using and wouldn't
reliably match. Every other color in the palette was picked or verified
against that exact ground rather than against `#000000`, because a
terminal cell has no alpha and a color tuned for literal black can read
subtly wrong sitting on `#030405` instead. The palette itself is Zed's
Ayu Darker theme extension plus Chase's own `settings.json` overrides,
read directly from `~/Library/Application Support/Zed/extensions/installed/ayu-darker/themes/ayu-darker.json`
and his Zed config on 2026-09-02 — this is documented in the file's own
comment as a traceable source, not an approximation.

**Which colors are chrome and which are syntax** is a real distinction
in this file, not just an organizational one. `Subtle` (`#2d2f34`) is
structure that should recede — splitters, modal frames, rule lines,
indent guides, a diff's gap glyph. `DimText` (`#969aa0`, chosen for
7.7:1 contrast on `#030405`) is dim-but-*readable* words: the Changes
panel's parent-directory suffix, its "⋯ more" hint, the review footer's
placeholder text. These were one field until 2026-09-02, when raising it
for the Changes panel's text would have lit up every border in the app —
splitting them is what let dim readable text and dim recessive chrome be
tuned independently. On the syntax side, every UI blue in the whole app
became lavender (`#bfaee0`) on 2026-09-03 — `Accent`, `StatusFG`,
`FolderColor`, `GitRenamed` all share this value now, following a request
that started with just the status bar and grew to "the rest of the blue
chrome." `SynProperty` deliberately **keeps** Ayu's original blue, and
`SynNumber`/`SynBuiltin` keep the more saturated `#d2a6ff`, specifically
because those are syntax highlighting, not UI chrome, and the same visual
language doesn't automatically apply to both. `FolderColor` is plain
`Text` — asked for three times on 2026-09-03 (the first two attempts were
misread as "one color" and then "purple like the rest of the chrome")
before landing on "folders are just white, like files, hidden or not" —
hidden files still dim to `Muted`, but hidden folders don't, so every
folder in the tree reads as one consistent color.

**The tree**, `internal/filetree/filetree.go`. `headerRows` is 3: an
"Explorer" title row in accent bold, a rule under it, and the root
directory's own name — the same three-row shape the Changes panel's
header uses, and `HitTest`, hover tracking, and `Render` all read this
same constant so the three stay in sync by construction rather than by
convention. Indent guides are drawn per-row by `guideSegment`: the
segment belonging to a node's own depth is `└ ` when it's the last child
at that level, `│ ` otherwise; segments for shallower ancestors are `│ `
while that ancestor still has more siblings below, and blank once it
doesn't — which is what makes a guide line visually stop exactly where a
folder's children stop, rather than drawing a solid rail down the whole
tree regardless of structure. With icons enabled, both files and folders
draw `glyph + two spaces + name` (`internal/icons`), so a file and a
folder at the same depth start their names in exactly the same column —
before this, a folder glyph's own visible width made a one-space gap look
like no gap at all next to a file's two-space one.

`Esc z` is `Tree.CollapseAll`: it folds every expanded directory in the
tree, but children stay **loaded** in memory — folding is purely a
display decision, and throwing loaded children away would mean
re-reading every directory the next time it's expanded, for no benefit.
The currently-active folder, if one of its ancestors just got collapsed
out of view, moves up to the nearest ancestor still visible, and the
scroll position resets to the top — a fold-all is explicitly a "take me
back to the overview" gesture, and landing mid-list after asking for that
would defeat the point. Every file opened from the Changes panel or the
finder calls `Tree.Reveal`, which expands ancestors as needed — which is
exactly why a review session leaves the sidebar shaped like your
browsing history rather than like the project, and why `Esc z` exists at
all as an escape hatch back to the project's actual shape.

**The root picker, `Esc o`** (`internal/app/rootpicker.go`), is a
different question from anything in `multi-repo.md`: it's "which folder
is Vincent even pointed at," changing `a.rootDir` entirely, not "which
repo inside the current folder owns this file." It's shaped like the
finder — a centered box, a one-line filter on top, roughly ten rows
below, hover follows the pointer — behind one input field that behaves in
two modes. An empty or non-path-like query shows **recents**: the
`recentRoots` list from `config.json`, minus wherever Vincent currently
stands, fuzzy-filtered with the finder's own scorer, home-directory paths
abbreviated to `~`. A query starting with `/`, `~`, or `.` switches to
**browse** mode: the list becomes the subdirectories of whatever path is
currently typed, the way shell tab-completion behaves, with Tab
completing and Enter descending into a highlighted child — or, when
nothing is highlighted, picking the directory the query already names
outright. Browse mode deliberately auto-selects nothing on entry, unlike
recents mode, because that's precisely what makes "pick *this* folder,
not one of its children" reachable at all: if the first row auto-selected
the way it does in recents mode, a folder that has subdirectories could
never itself be chosen. Esc closes the picker and simultaneously
re-arms the Esc leader — pressing `Esc o` twice in a row is a deliberate
two-gesture shape, since the second `Esc` inside the still-armed window
is what flips the picker's own input from recents into browse mode.

## Why it is built this way

Non-negotiable 5 in CLAUDE.md states the leader-only, no-menu rule
directly, and names the exact cost the removed menu was paying: "a second
code path into every action, a button in the tab bar, and a modal with
hover and enable predicates, all to restate the key table." The
cheatsheet's read-only, generated-from-`leaderRows()` design is the
direct fix, chosen specifically so that a stale key hint — CLAUDE.md
cites the status bar once drifting to "f find · t tree" two renames after
both keys had moved — becomes structurally impossible rather than merely
discouraged.

The palette being one hardcoded Go struct rather than a loadable theme
file is itself a decision, not an omission: Vincent is built around one
specific person's actual terminal, and non-negotiable 1 states the
`#030405` background must be set explicitly because it has to match
Chase's Ghostty, not whatever a generic dark theme assumes black looks
like.

`Tree.CollapseAll` keeping children loaded rather than discarding them is
the same principle CLAUDE.md's "identity-preserving tree refresh" pattern
follows elsewhere in the tree: a display-only state change should cost
nothing to reverse.

## What can go wrong

**A key you expect to fire does nothing.** Either the 1500ms window
already lapsed (check the status bar — it shows the armed-leader hint
only while it's live) or the letter simply isn't bound; `Esc ?` is the
authoritative list, always current.

**The cheatsheet looks visually cramped or two-column on a short
terminal.** That's `cheatsheetFit`'s third tier engaging on a genuinely
short screen (below the two-row-margin threshold even with dividers
dropped) — not a rendering bug.

**A folder in the tree looks white when you expected it colored by git
status or hover state.** `FolderColor` is deliberately always plain
`Text` regardless of git state or visibility — only the row's background
(selection, hover) and a *file's* dimming (hidden files go `Muted`)
change; a folder's own name color never does.

**`Esc o` twice doesn't do what you expect.** The second `Esc` inside the
armed window flips the picker between recents and browse mode rather
than closing it outright — a query already typed carries over.

## Not covered here

Which repository a git action actually runs against once you've picked a
root is `multi-repo.md`'s subject, not this page's — the root picker only
changes which folder Vincent has open at all. The render-loop mechanics
that make hover state in the cheatsheet or the root picker actually
repaint on mouse motion are `render-loop.md`. Syntax highlighting's
specific color-to-token mapping (`SynKeyword`, `SynString`, etc. and how
Chroma tokens map onto them) isn't detailed further here beyond what's
shown above.

Not verified on a terminal: how the two-column cheatsheet fallback
actually reads at the 24-row floor the code comments cite as a real
target, and whether the lavender accent color reads as intended against
every terminal emulator's own color profile, versus specifically
Ghostty, which is what it was tuned against.
