# Markdown rendering

Open a `.md` file in Vincent and it renders — headings, bold, code
blocks, tables, links — instead of showing you raw `#` and backtick
characters. `Esc m` flips the same tab back to plain editable text and
back again. Agents write a lot of markdown (plans, READMEs, PR
descriptions); this is what makes reading it in Vincent nicer than
reading the source.

## How it works

**Parsing and layout are pure**, mirroring the split `internal/diff`
established: `internal/markdown` has no tcell import at all.
`markdown.Render(source []byte, width int) []Row` parses with
`goldmark` (pure Go, no cgo) with GFM extensions turned on
unconditionally — tables, strikethrough, autolinks, task-list checkboxes
— because an agent's markdown is as likely to be a GitHub-flavored README
as plain CommonMark, and parsing the wider grammar costs nothing when a
document doesn't use it. The AST is walked into a flat `[]Row`, each `Row`
a slice of `Span`s (`Text`, `Heading1`/`2`/`3`, `Bold`, `Italic`, `Code`,
`CodeBlock`, `Link`, `ListMarker`, `Quote`, `Rule`, `TableBorder`,
`TableHeader`) — a small closed enum a tcell painter can switch on without
knowing anything about markdown grammar itself.

Several things are deliberately simplified, and the package's own doc
comment names them directly rather than leaving them to be discovered:
a heading's inline content (bold, code, links inside `## foo `bar``)
collapses to plain text under the heading's own style, since nested
emphasis inside a heading is rare enough in reviewed markdown that losing
it beats the bookkeeping to keep it; bold-and-italic on the same run
keeps whichever style wrapped it last rather than tracking both (there is
no `BoldItalic` enum member); table cells don't wrap — only the whole
table's total width is capped by shrinking its widest columns; and
wrapping measures width in **runes, not display cells**, so a
wide-glyph-heavy document (CJK prose) wraps a little short, since pulling
`internal/editor`'s `RuneVisualWidth` cell-width math into a tcell-free
package wasn't judged worth it for a markdown viewer.

**A markdown tab is a `Tab` in `markdownMode`**, the same `Tab.Mode`
pattern used for diff and image tabs — read-only through `ReadOnly()` for
free, no special case anywhere in the app layer for scrolling, clamping,
hit-testing, or the find bar. The trick that makes that work is the same
one a diff tab uses: `Tab.Buffer.Lines` holds the **rendered** plain text
(one line per `markdown.Row`, via `markdown.Texts` — the markdown
package's mirror of `diff.Texts`), while `Tab.MarkdownRows` (parallel to
`Buffer.Lines`, the same relationship `DiffRows` has to a diff tab's
buffer) carries the styling a plain `[]string` buffer can't. `Tab.MarkdownSource`
holds the actual raw markdown text separately, because toggling back to
raw mode needs the *original* source, not whatever the wrapped, rendered
text happened to look like.

`editor.NewTab` dispatches `.md`/`.markdown` extensions (case-insensitive
— `IsMarkdownExt`) to `NewMarkdownTab` automatically, the same way it
already dispatched image extensions to `newImageTab` — opening a markdown
file rendered by default needed no change to `openFile` in `app.go` at
all.

**`Esc m`** is `menuToggleMarkdownView` (`internal/app/markdownview.go`),
a thin dispatcher onto `Tab.ToggleMarkdownView()` in
`internal/editor/markdownview.go` — the tab itself knows what counts as
markdown, so the app doesn't need to. Toggling from rendered to raw sets
`Mode = ""`, which flips `ReadOnly()` false and makes every existing edit,
undo, and save path — the ones a plain text tab already has — just work
with no further changes. Content survives the round trip both ways:
raw-to-rendered previews whatever is *currently* in the buffer, including
an unsaved edit, so you can preview a change before saving it; rendered-
to-raw seeds the editable buffer from that same source, so an edit made,
previewed, and toggled back is still there. `Dirty` is untouched by the
toggle entirely, since it already reflects "does the buffer disagree with
disk" independent of which view is currently showing that buffer.

**Scroll position survives the toggle and a resize as a fraction of the
document**, not an absolute line number — deliberately, because the
rendered view (wrapped rows) and the raw view (source lines) have
different row counts, and an absolute `ScrollY` carried across the
toggle would land on an unrelated line. `rewrapMarkdown` (called on every
render-width change, including a terminal resize) reads the old line
count and `ScrollY` before replacing the buffer, computes
`frac = ScrollY / oldCount`, rebuilds `Buffer` from a fresh
`markdown.Render` call at the new width, and sets
`ScrollY = int(frac * newCount)`. `switchMarkdownToRendered` deliberately
leaves the buffer, scroll, cursor, and anchor untouched and lets the very
next `Render` call trigger this same `rewrapMarkdown` path — duplicating
the fraction math a second time in the toggle function would just be a
second, driftable copy of the same logic.

**The margin.** Rendered markdown draws with a two-cell blank margin on
each side (`markdownGutter = 2`, in `internal/editor/markdownview.go`) —
CLAUDE.md names this directly among the 2026-09-03 chrome changes.
`markdownInnerWidth(w)` subtracts `2*markdownGutter` from the pane width
before handing it to `Render` as the wrap width, and `markdownHitTest`
accounts for the same offset when turning a click's column back into a
buffer position.

**Fenced code blocks** are boxed and syntax-highlighted, not just
flatly styled like inline code: `markdownCodeBlockGroup` finds the run of
contiguous rows sharing `CodeBlock == true` and the same `Lang`, and
`highlightMarkdownCode` runs that whole run through Chroma via
`HighlightLang` — a variant of the ordinary file highlighter that resolves
a lexer by the fence's language tag (```go, ```python) rather than by a
fake file extension `Highlight` would otherwise need to invent. Grouping
contiguous same-language rows before tokenizing (rather than
highlighting line-by-line) is what keeps a multi-line string or comment
inside the block correct.

**Staying live.** `reconcileMarkdownTab` in `app/markdownview.go` is the
markdown counterpart to `reconcileDiffTab`: driven off the same
ten-second poll result (see `render-loop.md`), it re-reads the file and
calls `SetMarkdownSource` whenever the disk mtime moves forward. A
rendered markdown tab carries no dirty state of its own — it's read-only
— so there's no conflict to detect the way a text tab has: the disk
version simply always wins, exactly like a diff tab's. This function is
not involved at all for a `.md` file open in **raw** mode (`Mode == ""`),
which is an ordinary text tab at that point and goes through the normal
dirty/conflict path covered in `editor-and-conflicts.md`.

## Why it is built this way

Mirroring `internal/diff`'s parse/paint split (pure package, tcell-free)
plus reusing `Tab.Mode` rather than inventing a new tab type is the same
pattern CLAUDE.md names as a general rule for "a new kind of view" — it
inherits the tab list, the switcher, and modal routing for free, and
`ReadOnly()` gates every mutation in one place.

Rendering by default, with `Esc m` as the escape hatch back to raw text,
follows directly from who Vincent is built for: Chase reviews what agents
wrote and mostly doesn't write code himself, and an agent's plan document
or PR description is meant to be read as prose, not proofread as markup
syntax. Keeping the raw-text editing path fully intact underneath the
toggle (rather than making rendered markdown a genuinely separate,
non-editable artifact) is what lets a small correction — fixing a typo in
an agent's plan — happen without leaving the file.

Measuring wrap width in runes rather than display cells is a named,
accepted simplification, not an oversight: the package comment says so
directly, and the reasoning is that pulling cell-width math into a
tcell-free package isn't worth it for what is, after all, a markdown
*viewer*, not a full editor surface.

## What can go wrong

**A CJK-heavy or otherwise wide-glyph-heavy document wraps a little
short of the pane's edge**, leaving more blank space on the right than
expected. This is the rune-vs-cell-width simplification named above, not
a bug — `internal/editor`'s `RuneVisualWidth` handles this correctly for
code, but markdown wrapping doesn't use it.

**A table's cell content runs past its column, misaligning the row.**
Table cells never wrap; only the table's overall width is capped by
shrinking its widest columns. A cell with a genuinely long unbreakable
string (a long URL, for instance) can still overflow visually.

**Bold text inside italic text (or vice versa) loses one of the two
styles.** There's no combined style in the enum — whichever wraps the run
last wins.

**A heading with inline formatting (`## Fix \`foo\` **now**`) renders as
plain text under the heading style**, with the backticks and asterisks
gone along with their formatting. This is deliberate, not a parsing
failure.

**Toggling `Esc m` on a tab does nothing.** Either the tab isn't showing
markdown and isn't backed by a `.md`/`.markdown` path (in which case this
is correctly a no-op, per `ToggleMarkdownView`'s own doc comment), or
there's no active tab at all.

## Not covered here

The Chroma-based syntax highlighter that colors fenced code blocks
(`internal/editor/highlight.go`, `HighlightLang`) is shared with ordinary
file highlighting and isn't re-explained here beyond how markdown calls
into it. The ten-second poll and custom-event mechanism behind
`reconcileMarkdownTab`'s live updates is `render-loop.md`. The `Tab.Mode`
/ `ReadOnly()` pattern this reuses is explained more fully in
`editor-and-conflicts.md` and `diff-viewer.md`.

Not verified on a terminal: how a real-world agent-generated document
(nested lists, mixed fenced-and-indented code, wide tables) actually
looks wrapped and boxed on a real terminal window, and how noticeable the
CJK under-wrap is in practice against a document that mixes CJK and Latin
text on the same line.
