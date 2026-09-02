// =============================================================================
// File: internal/app/review.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// The composer's inline shape is tuicr's, from src/ui/comment_panel.rs
// (format_comment_input_lines): a bordered box grown into the diff flow
// under the annotated line, with the kind cycling on Tab. The handoff is
// herdr-reviewr's, from src/herdr.rs. Neither was copied — both are Rust —
// and the model and wire format live in internal/review.
// =============================================================================

// review.go is the app half of the review loop: select lines in a diff,
// write a note against them, and hand the batch back to the agent.
//
// Three surfaces, one state:
//
//   - The inline composer, drawn as overlay rows inside the diff (see
//     internal/editor/diffoverlay.go). It pushes the code below it down
//     rather than covering it, because a note about a line must never hide
//     that line.
//   - A one-row marker under every line that already carries a note, so
//     the reviewer can see what they already said without reopening
//     anything. Clicking one reopens it.
//   - The git panel's footer, where Zed puts its commit box. Zed's panel
//     ends in "describe this change and commit it"; Vincent's ends in
//     "describe this change and hand it back".
//
// Two rules from CLAUDE.md that shape the code and are easy to undo by
// accident:
//
//   - Consume on success. The batch is cleared only after review.Send
//     returns nil. Clearing it earlier — or on the clipboard fallback —
//     means a closed pane silently eats somebody's review.
//   - Never rebase line numbers. A comment's frozen Snippet is the anchor.
//     When the file underneath changes we re-find the row to hang the
//     marker on, and simply draw no marker when it is gone; the note keeps
//     the numbers it was written against, and the git-status refresh flags
//     it stale when its file leaves the changeset.
//
// The composer is app state, not a buffer edit. A diff tab is read-only and
// stays that way: nothing here writes to Tab.Buffer.

package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/clipboard"
	"github.com/chasereyn/vincent/internal/diff"
	"github.com/chasereyn/vincent/internal/editor"
	"github.com/chasereyn/vincent/internal/review"
)

const (
	// composerTextLine is the index of the editable row inside the
	// composer's overlay block: 0 is the top border, 1 the text, 2 the
	// footer. Named so the click handler and the builder cannot drift.
	composerTextLine = 1

	// composerFieldX is the column, within the composer's text row, where
	// the editable text starts — just past the "│ " left border.
	composerFieldX = 2

	// maxReviewFooterRows caps how many notes the git panel footer lists
	// before collapsing the rest into a "+N more" line. The footer steals
	// its rows from the Changes list above it, and a long review would
	// otherwise push the file list off the panel entirely.
	maxReviewFooterRows = 5

	// pickerModalWidth is the agent-picker's fixed width. Wide enough for
	// a session title plus its status, narrow enough to stay a popup.
	pickerModalWidth = 48
)

// reviewRowRect records where one clickable row of the footer's review
// block was drawn, and what clicking it means. Recorded during the draw and
// tested against afterwards — the same contract the Changes rows have, and
// for the same reason: row arithmetic recomputed in a click handler drifts
// out of alignment with the paint and nobody notices until a click does the
// wrong thing.
type reviewRowRect struct {
	y    int
	kind string // "comment", "send", or "copy"
	idx  int    // index into the batch, for kind == "comment"
}

// reviewMarkerRef maps one drawn marker row back to the comment it shows,
// so a click on it can reopen that note in the composer. Same
// record-during-draw discipline as reviewRowRect.
type reviewMarkerRef struct {
	row     int // diff row the marker hangs under
	line    int // overlay line index within that row's block
	comment int // index into the batch
}

// -----------------------------------------------------------------------------
// Anchoring a note to diff rows
// -----------------------------------------------------------------------------

// commentableRow reports whether a diff row can carry a note. Additions,
// deletions and context lines can; the elision between hunks and an
// uninterpreted meta line cannot, because neither corresponds to a line of
// anybody's file.
func commentableRow(r diff.Row) bool {
	switch r.Kind {
	case diff.KindAdded, diff.KindDeleted, diff.KindContext:
		return true
	default:
		return false
	}
}

// commentableRange shrinks a selected row range to the commentable rows
// inside it, and reports false when there are none.
//
// Shrinking rather than rejecting: a drag across a hunk boundary picks up
// the gap row in the middle, and refusing the whole selection over one
// piece of chrome would be infuriating. The gap simply is not part of the
// note.
func commentableRange(rows []diff.Row, lo, hi int) (int, int, bool) {
	if lo < 0 {
		lo = 0
	}
	if hi >= len(rows) {
		hi = len(rows) - 1
	}
	first, last := -1, -1
	for i := lo; i <= hi && i < len(rows); i++ {
		if !commentableRow(rows[i]) {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	if first < 0 {
		return 0, 0, false
	}
	return first, last, true
}

// rangeAddress works out which side of the diff a row range addresses and
// the first and last line numbers on that side.
//
// A range of nothing but deletions addresses the OLD file — those lines do
// not exist any more, and pretending otherwise would send the agent to a
// line number that means something else now. Anything else addresses the
// new file, using the additions and context rows; deletions mixed into such
// a range carry no new-side number and are skipped, which is right: their
// content is still in the snippet.
func rangeAddress(rows []diff.Row, lo, hi int) (review.Side, int, int) {
	onlyDeletions := true
	for i := lo; i <= hi && i < len(rows); i++ {
		if commentableRow(rows[i]) && rows[i].Kind != diff.KindDeleted {
			onlyDeletions = false
			break
		}
	}
	side := review.SideNew
	if onlyDeletions {
		side = review.SideOld
	}

	start, end := 0, 0
	for i := lo; i <= hi && i < len(rows); i++ {
		if !commentableRow(rows[i]) {
			continue
		}
		n := rows[i].New
		if side == review.SideOld {
			n = rows[i].Old
		}
		if n <= 0 {
			continue
		}
		if start == 0 || n < start {
			start = n
		}
		if n > end {
			end = n
		}
	}
	return side, start, end
}

// hunkIndexFor returns the index of the hunk owning row idx, counting the
// elision rows before it. Recorded on the comment at creation time and
// never recomputed — it is a fact about the diff the note was written
// against, not about the diff on screen now.
func hunkIndexFor(rows []diff.Row, idx int) int {
	hunk := 0
	for i := 0; i < idx && i < len(rows); i++ {
		if rows[i].Kind == diff.KindGap {
			hunk++
		}
	}
	return hunk
}

// snippetFor freezes rows lo..hi as diff text, each line keeping the +, -
// or space that says what happened to it.
//
// This is the anchor. Line numbers go stale the moment the agent writes
// again; the text of what was being discussed does not.
func snippetFor(rows []diff.Row, lo, hi int) string {
	out := []string{}
	for i := lo; i <= hi && i < len(rows); i++ {
		r := rows[i]
		if !commentableRow(r) {
			continue
		}
		prefix := " "
		switch r.Kind {
		case diff.KindAdded:
			prefix = "+"
		case diff.KindDeleted:
			prefix = "-"
		}
		out = append(out, prefix+r.Text)
	}
	return strings.Join(out, "\n")
}

// anchorRowFor finds the diff row a saved comment's marker should hang
// under: the row showing the LAST line of its range, on its own side.
//
// Returns -1 when that line is no longer in the diff, and the caller then
// draws no marker. That is the whole staleness policy in the diff view —
// look the anchor up fresh each frame, never move the comment to fit.
func anchorRowFor(rows []diff.Row, c review.Comment) int {
	for i, r := range rows {
		if c.Side == review.SideOld {
			if r.Kind == diff.KindDeleted && r.Old == c.End {
				return i
			}
			continue
		}
		if (r.Kind == diff.KindAdded || r.Kind == diff.KindContext) && r.New == c.End {
			return i
		}
	}
	return -1
}

// reviewPathFor renders an absolute path the way a comment records it:
// repo-relative, forward slashes on every platform. The agent reading the
// batch pastes these into tool calls, and a Windows-separated path in a
// prompt is a path the agent has to guess about.
func (a *App) reviewPathFor(path string) string {
	return filepath.ToSlash(a.relativePathFor(path))
}

// -----------------------------------------------------------------------------
// The composer
// -----------------------------------------------------------------------------

// composerActive reports whether the composer is both open and attached to
// the tab currently on screen.
//
// The tab check is what keeps a half-written note from swallowing keystrokes
// after the user switches tabs or closes the diff underneath it. The state
// survives the switch — come back to the tab and the box is still there
// with your text in it — but it only owns the keyboard while it is visible.
func (a *App) composerActive() bool {
	return a.composerOpen && a.composerTab != nil && a.activeTabPtr() == a.composerTab
}

// openReviewComposer starts a note against the diff's selected rows.
//
// Bound to Esc r, and offered again on a diff row's
// right-click. Every path lands here, so the preconditions live here too
// rather than in three callers.
func (a *App) openReviewComposer() {
	tab := a.activeTabPtr()
	if tab == nil || !tab.IsDiff() {
		a.flash("Open a diff first — Esc d")
		return
	}
	lo, hi := tab.DiffSelectedRows()
	lo, hi, ok := commentableRange(tab.DiffRows, lo, hi)
	if !ok {
		a.flash("Click a diff line to comment on it")
		return
	}
	side, start, end := rangeAddress(tab.DiffRows, lo, hi)

	a.composerOpen = true
	a.composerTab = tab
	a.composerRow = hi
	a.composerFile = a.reviewPathFor(tab.Path)
	a.composerSide = side
	a.composerStart = start
	a.composerEnd = end
	a.composerHunk = hunkIndexFor(tab.DiffRows, lo)
	a.composerSnippet = snippetFor(tab.DiffRows, lo, hi)
	a.composerKind = review.KindNone
	a.composerValue = nil
	a.composerCursor = 0
	a.composerScroll = 0
	a.composerEdit = -1
	a.ensureComposerVisible(tab)
}

// openReviewCommentForEdit reopens a saved note in the composer.
//
// The frozen fields — side, range, hunk, snippet — are carried over
// verbatim rather than re-derived from the diff on screen. Editing the
// words of a note must not silently re-anchor it to whatever is at those
// coordinates now.
func (a *App) openReviewCommentForEdit(idx int) {
	if idx < 0 || idx >= len(a.reviewBatch.Comments) {
		return
	}
	tab := a.activeTabPtr()
	if tab == nil || !tab.IsDiff() {
		return
	}
	c := a.reviewBatch.Comments[idx]
	row := anchorRowFor(tab.DiffRows, c)
	if row < 0 {
		a.flash("That line is no longer in the diff")
		return
	}

	a.composerOpen = true
	a.composerTab = tab
	a.composerRow = row
	a.composerFile = c.File
	a.composerSide = c.Side
	a.composerStart = c.Start
	a.composerEnd = c.End
	a.composerHunk = c.Hunk
	a.composerSnippet = c.Snippet
	a.composerKind = c.Kind
	a.composerValue = []rune(c.Text)
	a.composerCursor = len(a.composerValue)
	a.composerScroll = 0
	a.composerEdit = idx
	a.ensureComposerVisible(tab)
}

// ensureComposerVisible scrolls the diff so the anchor row sits high enough
// that the whole box fits below it. Without this, opening a note on the
// bottom row of the viewport would type into a box nobody can see.
func (a *App) ensureComposerVisible(tab *editor.Tab) {
	_, h := a.editorSize()
	if h <= 0 {
		return
	}
	// Rows consumed above the anchor plus the box itself.
	need := a.composerRow - tab.ScrollY + composerBoxHeight
	if need <= h {
		return
	}
	tab.ScrollToRow(a.composerRow, h)
}

// closeReviewComposer drops the composer without touching the batch.
func (a *App) closeReviewComposer() {
	a.composerOpen = false
	a.composerTab = nil
	a.composerValue = nil
	a.composerCursor = 0
	a.composerScroll = 0
	a.composerSnippet = ""
	a.composerEdit = -1
}

// saveReviewComment commits the composer's text into the batch.
//
// An empty note is not a note: on a new one it is a cancel, on an existing
// one it is a delete. Both beat storing a comment with no words in it,
// which would ship to the agent as a code snippet with no question
// attached.
func (a *App) saveReviewComment() {
	if !a.composerOpen {
		return
	}
	text := strings.TrimSpace(string(a.composerValue))
	if text == "" {
		if a.composerEdit >= 0 {
			a.deleteReviewComment(a.composerEdit)
			return
		}
		a.closeReviewComposer()
		a.flash("Nothing typed — note discarded")
		return
	}
	c := review.Comment{
		File:    a.composerFile,
		Side:    a.composerSide,
		Start:   a.composerStart,
		End:     a.composerEnd,
		Hunk:    a.composerHunk,
		Snippet: a.composerSnippet,
		Kind:    a.composerKind,
		Text:    text,
	}
	if a.composerEdit >= 0 && a.composerEdit < len(a.reviewBatch.Comments) {
		// Keep the stale flag: it is a fact about the file, not about the
		// wording, and the reviewer editing their note has not un-committed
		// the agent's change.
		c.Stale = a.reviewBatch.Comments[a.composerEdit].Stale
		a.reviewBatch.Comments[a.composerEdit] = c
		a.closeReviewComposer()
		a.flash("Note updated")
		return
	}
	a.reviewBatch.Comments = append(a.reviewBatch.Comments, c)
	a.closeReviewComposer()
	a.flash(fmt.Sprintf("Note added · %s", noteCount(a.reviewBatch.Len())))
}

// deleteReviewComment removes one note and closes the composer.
func (a *App) deleteReviewComment(idx int) {
	if idx < 0 || idx >= len(a.reviewBatch.Comments) {
		return
	}
	a.reviewBatch.Comments = append(a.reviewBatch.Comments[:idx], a.reviewBatch.Comments[idx+1:]...)
	a.closeReviewComposer()
	a.flash("Note deleted")
}

// cycleComposerKind advances the note's kind on Tab. Cycling one key
// through five states beats four separate bindings in a client with no
// Ctrl- shortcuts to spend.
func (a *App) cycleComposerKind() {
	a.composerKind = a.composerKind.Next()
}

// handleComposerKey owns the keyboard while the composer is on screen.
//
// Routed ahead of the Esc-leader table in handleKey, which is why Esc here
// means "cancel this note" rather than "arm the leader": while a text field
// has focus, Esc has one job.
func (a *App) handleComposerKey(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyEsc:
		a.closeReviewComposer()
		return
	case tcell.KeyEnter:
		a.saveReviewComment()
		return
	case tcell.KeyTab:
		a.cycleComposerKind()
		return
	case tcell.KeyBackspace, tcell.KeyBackspace2, tcell.KeyDelete:
		// Deleting past the start of an existing note's text deletes the
		// note. It is the gesture people already have for "get rid of
		// this", and it saves inventing a delete key for a client that
		// has no spare modifiers.
		if len(a.composerValue) == 0 && a.composerEdit >= 0 {
			a.deleteReviewComment(a.composerEdit)
			return
		}
	}
	a.composerValue, a.composerCursor, _ = editRunes(a.composerValue, a.composerCursor, ev)
}

// reviewDiffPress claims a mouse press the review layer owns, and reports
// whether it did. Three cases: a click in the composer's text field, a
// click on a saved note's marker, and a click on a diff row that cannot
// carry a note at all.
//
// Called from editorPress before the normal cursor-placing hit test, so a
// click inside the box the user is typing into lands in the text field
// instead of moving the diff's selection out from under it.
func (a *App) reviewDiffPress(tab *editor.Tab, localX, localY int) bool {
	if tab == nil || !tab.IsDiff() {
		return false
	}
	ew, eh := a.editorSize()
	hit, ok := tab.DiffHitAt(localX, localY, ew, eh)
	if !ok {
		return false
	}
	if hit.Overlay == editor.NoOverlay {
		// A hunk elision (and a binary-file note) is not a line of
		// anybody's file, so it is not selectable. Claim the click and
		// drop it rather than parking a selection on chrome that could
		// then be the start of a note about nothing.
		if hit.Row < len(tab.DiffRows) && !commentableRow(tab.DiffRows[hit.Row]) {
			return true
		}
		return false
	}
	for _, ref := range a.lastMarkerRefs {
		if ref.row == hit.Row && ref.line == hit.Overlay {
			a.openReviewCommentForEdit(ref.comment)
			return true
		}
	}
	if a.composerActive() && hit.Row == a.composerRow && hit.Overlay == composerTextLine {
		target := a.composerScroll + hit.Col - composerFieldX
		if target < 0 {
			target = 0
		}
		if target > len(a.composerValue) {
			target = len(a.composerValue)
		}
		a.composerCursor = target
	}
	// Every overlay click is absorbed either way. Falling through would
	// place the diff's cursor on the anchor row, which looks like the box
	// jumping when you click its border.
	return true
}

// openDiffContext offers the review actions on a diff row's right-click,
// after selecting the row under the pointer.
//
// Right-click is a redundant path, never the only one — macOS Terminal
// under tmux swallows button 3 — so every review action here also has a
// leader key (Esc r / Esc ⏎ / Esc y, all listed in the Esc-? cheatsheet).
// Selecting the row first is what makes the gesture one motion instead of
// "click, then right-click the same line".
//
// The copy-path pair is here for the same reason it is on the editor's
// context menu: it was a ≡ menu row, the menu is gone, and a diff tab
// carries the real file's path — so "copy the path of the file I am
// reviewing" has to work from the diff, which is where the reviewer
// actually is.
func (a *App) openDiffContext(tab *editor.Tab, x, y int) bool {
	if tab == nil || !tab.IsDiff() {
		return false
	}
	ex, ey, ew, eh := a.editorRect()
	pos, ok := tab.HitTest(x-ex, y-ey, ew, eh)
	if !ok {
		return false
	}
	tab.MoveCursorTo(pos, false)

	items := []contextItem{{label: "Add review note", plain: (*App).openReviewComposer}}
	if a.reviewBatch.Len() > 0 {
		items = append(items,
			contextItem{label: "Send to agent", plain: (*App).sendReview},
			contextItem{label: "Copy review", plain: (*App).copyReview},
		)
	}
	items = append(items,
		contextItem{label: "Copy rel path", plain: (*App).menuCopyRelativePath},
		contextItem{label: "Copy abs path", plain: (*App).menuCopyAbsolutePath},
	)
	a.contextNode = nil
	a.contextItems = items
	a.contextHover = 0
	a.contextX, a.contextY = a.placeContext(x, y, len(items))
	a.contextOpen = true
	return true
}

// -----------------------------------------------------------------------------
// Overlay rows inside the diff
// -----------------------------------------------------------------------------

// composerBoxHeight is how many screen rows the composer occupies: top
// border, one text row, footer.
const composerBoxHeight = 3

// buildDiffOverlays derives the overlay rows for a diff tab and records the
// marker click map. Called once per draw from the render path.
//
// Derived fresh every frame rather than maintained incrementally: which
// markers exist depends on the batch AND on what the current diff still
// contains, and that second half changes under us whenever the agent
// writes. One cheap walk per frame beats two representations that disagree.
func (a *App) buildDiffOverlays(tab *editor.Tab, w int) []editor.DiffOverlay {
	a.lastMarkerRefs = a.lastMarkerRefs[:0]
	if tab == nil || !tab.IsDiff() {
		return nil
	}
	file := a.reviewPathFor(tab.Path)
	byRow := map[int][]editor.DiffOverlayLine{}
	order := []int{}
	appendTo := func(row int, line editor.DiffOverlayLine) int {
		if _, seen := byRow[row]; !seen {
			order = append(order, row)
		}
		byRow[row] = append(byRow[row], line)
		return len(byRow[row]) - 1
	}

	for i, c := range a.reviewBatch.Comments {
		if c.File != file {
			continue
		}
		// The composer is showing this note right now; a marker under it
		// as well would say the same thing twice.
		if a.composerActive() && a.composerEdit == i {
			continue
		}
		row := anchorRowFor(tab.DiffRows, c)
		if row < 0 {
			continue
		}
		line := appendTo(row, a.markerLine(c, w))
		a.lastMarkerRefs = append(a.lastMarkerRefs, reviewMarkerRef{row: row, line: line, comment: i})
	}

	if a.composerActive() {
		// Re-find the anchor row rather than trusting the one captured at
		// open. The agent may have written again since — the diff tab
		// re-parses itself when it does — and the box has to stay under
		// the line it is about. The frozen side and end line are what it
		// is re-found BY; nothing about the note itself is rebased.
		if row := anchorRowFor(tab.DiffRows, review.Comment{Side: a.composerSide, End: a.composerEnd}); row >= 0 {
			a.composerRow = row
		}
		for _, line := range a.composerLines(w) {
			appendTo(a.composerRow, line)
		}
	}

	overlays := make([]editor.DiffOverlay, 0, len(order))
	for _, row := range order {
		overlays = append(overlays, editor.DiffOverlay{Row: row, Lines: byRow[row]})
	}
	return overlays
}

// markerLine renders a saved note as one compact row under its anchor.
//
// Compact on purpose: the marker's job is to remind, not to re-read. It
// carries the kind and as much of the note as fits, dimmed so it never
// competes with the code it hangs under.
func (a *App) markerLine(c review.Comment, w int) editor.DiffOverlayLine {
	th := a.theme
	fg := th.ReviewMarker
	if c.Stale {
		fg = th.ReviewStale
	}
	text := "  ▍ "
	if tag := c.Kind.Tag(); tag != "" {
		text += "[" + tag + "] "
	}
	text += strings.ReplaceAll(strings.TrimSpace(c.Text), "\n", " ")
	if c.Stale {
		text += " (stale)"
	}
	return editor.DiffOverlayLine{
		Text:  trimRunes(text, maxInt(w-1, 1)),
		FG:    fg,
		BG:    th.ReviewBoxBG,
		Caret: editor.NoCaret,
	}
}

// composerLines renders the composer as three overlay rows.
//
// tuicr's shape, kept deliberately: a top border naming the kind and the
// line range, the text, and a footer spelling out the three keys. The
// footer is not decoration — this is a modeless box with no buttons, and a
// reviewer who cannot remember how to save it will lose a note.
func (a *App) composerLines(w int) []editor.DiffOverlayLine {
	th := a.theme
	boxW := w - 1
	if boxW < 12 {
		boxW = 12
	}
	border := editor.DiffOverlayLine{FG: th.ReviewBorder, BG: th.ReviewBoxBG, Caret: editor.NoCaret}

	head := border
	head.Text = fitBorder("┌─ "+a.composerLabel()+" ", boxW, "┐")

	fieldW := boxW - 4
	if fieldW < 1 {
		fieldW = 1
	}
	a.composerScroll = scrollWindow(a.composerCursor, a.composerScroll, fieldW)
	visible := runeWindow(a.composerValue, a.composerScroll, fieldW)
	text := editor.DiffOverlayLine{
		Text:  "│ " + visible + strings.Repeat(" ", fieldW-len([]rune(visible))) + " │",
		FG:    th.ReviewText,
		BG:    th.ReviewBoxBG,
		Caret: editor.NoCaret,
	}
	if caret := composerFieldX + a.composerCursor - a.composerScroll; caret >= composerFieldX && caret < composerFieldX+fieldW {
		text.Caret = caret
	}

	foot := border
	foot.Text = fitBorder("└─ Tab kind · Enter save · Esc cancel ", boxW, "┘")

	return []editor.DiffOverlayLine{head, text, foot}
}

// composerLabel is the range shown on the composer's top border: the kind
// when it has one, then the line reference. Old-side ranges keep the ~ the
// wire format uses so the two never disagree about which file a number is
// in.
func (a *App) composerLabel() string {
	mark := ""
	if a.composerSide == review.SideOld {
		mark = "~"
	}
	label := fmt.Sprintf("L%s%d", mark, a.composerStart)
	if a.composerEnd > a.composerStart {
		label += fmt.Sprintf("-%s%d", mark, a.composerEnd)
	}
	if tag := a.composerKind.Tag(); tag != "" {
		label = "[" + tag + "] " + label
	}
	return label
}

// fitBorder pads head with the box-drawing rule out to width and caps it
// with corner, truncating head when the pane is too narrow to hold it.
func fitBorder(head string, width int, corner string) string {
	head = trimRunes(head, maxInt(width-1, 1))
	pad := width - 1 - len([]rune(head))
	if pad < 0 {
		pad = 0
	}
	return head + strings.Repeat("─", pad) + corner
}

// runeWindow returns up to n runes of value starting at from, as a string.
// The composer's text field scrolls horizontally, so it needs a window
// rather than the whole value.
func runeWindow(value []rune, from, n int) string {
	if from < 0 {
		from = 0
	}
	if from > len(value) {
		from = len(value)
	}
	end := from + n
	if end > len(value) {
		end = len(value)
	}
	return string(value[from:end])
}

// maxInt is the two-argument max for ints, kept local so the drawing code
// reads as intent rather than as inline comparisons.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// noteCount renders a batch size with the right plural. "Sent 1 notes" is
// the kind of small wrongness that makes a tool feel unfinished.
func noteCount(n int) string {
	if n == 1 {
		return "1 note"
	}
	return fmt.Sprintf("%d notes", n)
}

// -----------------------------------------------------------------------------
// The git panel's review block
// -----------------------------------------------------------------------------

// reviewBlockRows is how many rows the footer's review block needs. One
// dimmed line when there is nothing to send; otherwise a header, the notes
// (capped), an optional overflow line, and the two action rows.
func (a *App) reviewBlockRows() int {
	n := a.reviewBatch.Len()
	if n == 0 {
		return 1
	}
	rows := 1 + minInt(n, maxReviewFooterRows) + 2
	if n > maxReviewFooterRows {
		rows++
	}
	return rows
}

// minInt is the two-argument min for ints.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// drawGitPanelReview paints the review block at the bottom of the Changes
// panel — where Zed puts its commit box — and records the rows a click will
// be tested against. rows is how many screen rows it was actually given,
// which is fewer than it asked for in a short terminal.
//
// The empty state is one dimmed line rather than a hidden block: a reviewer
// who has not found Esc r yet needs to be told it exists, and the panel is
// where they are already looking.
func (a *App) drawGitPanelReview(x, y, w, rows int) {
	th := a.theme
	base := tcell.StyleDefault.Background(th.SidebarBG)
	a.lastReviewRows = a.lastReviewRows[:0]
	if rows <= 0 {
		return
	}

	if a.reviewBatch.Len() == 0 {
		drawClipped(a.screen, x+1, y, w-1, "No review notes · Esc r on a diff line", base.Foreground(th.Subtle))
		return
	}

	cy := y
	drawClipped(a.screen, x+1, cy, w-1,
		fmt.Sprintf("Review (%d)", a.reviewBatch.Len()), base.Foreground(th.Accent).Bold(true))
	cy++

	// The header and the two action rows are reserved out of the budget
	// first: a review you cannot send is worse than a review you cannot
	// fully read.
	listRoom := maxInt(rows-3, 0)
	shown := minInt(minInt(a.reviewBatch.Len(), maxReviewFooterRows), listRoom)
	// When the budget was clamped short, the "+N more" line still needs a
	// row of its own, so give up one note to say how many are hidden.
	// In the unclamped case reviewBlockRows already counted that row and
	// this cannot fire.
	if shown > 0 && shown == listRoom && shown < a.reviewBatch.Len() {
		shown--
	}
	for i := 0; i < shown; i++ {
		c := a.reviewBatch.Comments[i]
		style := base.Foreground(th.Text)
		label := c.Summary()
		if c.Stale {
			style = base.Foreground(th.ReviewStale)
			label += " (stale)"
		}
		drawClipped(a.screen, x+gitPanelIndent, cy, w-gitPanelIndent-1, label, style)
		a.lastReviewRows = append(a.lastReviewRows, reviewRowRect{y: cy, kind: "comment", idx: i})
		cy++
	}
	if rest := a.reviewBatch.Len() - shown; rest > 0 {
		drawClipped(a.screen, x+gitPanelIndent, cy, w-gitPanelIndent-1,
			fmt.Sprintf("+%d more", rest), base.Foreground(th.Subtle))
		cy++
	}

	drawClipped(a.screen, x+1, cy, w-1, "Send to agent  Esc ⏎", base.Foreground(th.Accent).Bold(true))
	a.lastReviewRows = append(a.lastReviewRows, reviewRowRect{y: cy, kind: "send"})
	cy++
	drawClipped(a.screen, x+1, cy, w-1, "Copy  Esc y", base.Foreground(th.Muted))
	a.lastReviewRows = append(a.lastReviewRows, reviewRowRect{y: cy, kind: "copy"})
}

// reviewPanelClick dispatches a click in the footer's review block against
// the rects recorded during the last draw, and reports whether it handled
// one. Clicking a note opens its file's diff — the note names a line, and
// the useful response to "what did I say about this" is to show it.
func (a *App) reviewPanelClick(y int) bool {
	for _, r := range a.lastReviewRows {
		if r.y != y {
			continue
		}
		switch r.kind {
		case "send":
			a.sendReview()
		case "copy":
			a.copyReview()
		case "comment":
			a.openDiffForComment(r.idx)
		}
		return true
	}
	return false
}

// openDiffForComment opens the diff of the file a note is attached to and
// scrolls to the note's anchor row when it is still there.
func (a *App) openDiffForComment(idx int) {
	if idx < 0 || idx >= len(a.reviewBatch.Comments) {
		return
	}
	c := a.reviewBatch.Comments[idx]
	a.openDiff(filepath.Join(a.rootDir, filepath.FromSlash(c.File)))
	tab := a.activeTabPtr()
	if tab == nil || !tab.IsDiff() {
		return
	}
	if row := anchorRowFor(tab.DiffRows, c); row >= 0 {
		_, h := a.editorSize()
		tab.ScrollToRow(row, h)
	}
}

// -----------------------------------------------------------------------------
// Handoff
// -----------------------------------------------------------------------------

// hasReviewNotes gates the two handoff actions.
func (a *App) hasReviewNotes() bool { return a.reviewBatch.Len() > 0 }

// hasDiffTab gates "Add review note": the composer only
// makes sense over a diff.
func (a *App) hasDiffTab() bool {
	tab := a.activeTabPtr()
	return tab != nil && tab.IsDiff()
}

// sendReview hands the batch to an agent pane, falling back to the
// clipboard when there is nobody to hand it to.
//
// The herdr calls run synchronously on the UI thread. That is deliberate,
// and safe because every one of them is bounded at two seconds inside
// internal/review: a send is a direct response to a keypress, so doing it
// on a goroutine would mean posting a custom event back just to flash a
// result, and a wedged herdr would still block — only later and more
// confusingly.
//
// Zero targets and a herdr failure both land on the clipboard, and the
// batch is KEPT in both cases: the reviewer has the text but has not
// delivered it, and clearing here would lose the review to a paste they
// never made.
func (a *App) sendReview() {
	if a.reviewBatch.Len() == 0 {
		a.flash("No review notes · Esc r on a diff line")
		return
	}
	text := a.reviewBatch.Render()

	if !review.Available() {
		a.copyReviewText(text, "not running under herdr")
		return
	}
	targets, err := review.ListTargets(context.Background())
	if err != nil {
		a.copyReviewText(text, err.Error())
		return
	}
	if len(targets) == 1 {
		a.sendReviewTo(targets[0], text)
		return
	}
	a.openReviewPicker(targets, text)
}

// sendReviewTo delivers the batch to one pane and, only on success, clears
// it. Consume-on-success is the rule from CLAUDE.md: a closed pane must
// leave the review intact to try again.
func (a *App) sendReviewTo(t review.Target, text string) {
	n := a.reviewBatch.Len()
	if err := review.Send(context.Background(), t.PaneID, text); err != nil {
		a.flash(err.Error() + " — review kept")
		return
	}
	a.reviewBatch.Comments = nil
	a.closeReviewComposer()
	a.flash(fmt.Sprintf("Sent %s to %s", noteCount(n), t.Name))
}

// copyReview renders the batch onto the system clipboard and leaves it
// alone otherwise. Bound to Esc y, for handing a review to something that
// is not a herdr pane — a browser, a chat window, another machine.
func (a *App) copyReview() {
	if a.reviewBatch.Len() == 0 {
		a.flash("No review notes · Esc r on a diff line")
		return
	}
	a.copyReviewText(a.reviewBatch.Render(), "")
}

// copyReviewText is the shared clipboard path: OSC 52, then a flash saying
// how many notes went and — when this was a fallback rather than a choice —
// why herdr did not get them.
func (a *App) copyReviewText(text, reason string) {
	if err := clipboard.CopyToSystem(text); err != nil {
		a.flash(fmt.Sprintf("Copy failed: %v", err))
		return
	}
	msg := fmt.Sprintf("Copied %s to clipboard", noteCount(a.reviewBatch.Len()))
	if reason != "" {
		msg = reason + " · " + msg
	}
	a.flash(msg)
}

// openReviewPicker asks which agent gets the review when more than one is
// running in this workspace.
//
// The rendered text is frozen into the picker rather than re-rendered on
// pick: the batch could change under a slow decision (the git refresh marks
// something stale), and what gets sent should be what the reviewer was
// looking at when they chose.
func (a *App) openReviewPicker(targets []review.Target, text string) {
	a.closeAllModals()
	a.pickerOpen = true
	a.pickerTargets = targets
	a.pickerText = text
	a.pickerHover = 0
}

// pickerModalRect returns the picker's rectangle: title row, divider, one
// row per target, centred.
func (a *App) pickerModalRect() (x, y, w, h int) {
	w = pickerModalWidth
	h = len(a.pickerTargets) + 4
	x = (a.width - w) / 2
	y = (a.height - h) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return
}

// pickerActivate sends the frozen batch to the highlighted target.
func (a *App) pickerActivate() {
	if a.pickerHover < 0 || a.pickerHover >= len(a.pickerTargets) {
		return
	}
	target := a.pickerTargets[a.pickerHover]
	text := a.pickerText
	a.closeAllModals()
	a.sendReviewTo(target, text)
}

// handlePickerKey drives the picker from the keyboard: Up/Down move,
// Enter sends, Esc cancels.
func (a *App) handlePickerKey(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyEsc:
		a.closeAllModals()
	case tcell.KeyUp:
		if a.pickerHover > 0 {
			a.pickerHover--
		}
	case tcell.KeyDown:
		if a.pickerHover < len(a.pickerTargets)-1 {
			a.pickerHover++
		}
	case tcell.KeyEnter:
		a.pickerActivate()
	}
}

// handlePickerMouse drives the picker from the mouse: hovering a row
// highlights it, clicking sends, clicking outside cancels. Same shape as
// the context menu, because Vincent is mouse-first and a picker that only
// answers to arrow keys is a dead end for the hand on the mouse.
func (a *App) handlePickerMouse(x, y int, btn tcell.ButtonMask) {
	mx, my, mw, mh := a.pickerModalRect()
	if x >= mx && x < mx+mw && y >= my+3 && y < my+mh-1 {
		a.pickerHover = y - my - 3
	}
	if btn&tcell.Button1 == 0 {
		return
	}
	if x < mx || x >= mx+mw || y < my || y >= my+mh {
		a.closeAllModals()
		return
	}
	if y >= my+3 && y < my+mh-1 {
		a.pickerHover = y - my - 3
		a.pickerActivate()
	}
}

// drawReviewPicker renders the agent picker.
func (a *App) drawReviewPicker() {
	mx, my, mw, mh := a.pickerModalRect()
	th := a.theme
	bg := th.LineHL
	bgStyle := tcell.StyleDefault.Background(bg).Foreground(th.Text)
	borderStyle := tcell.StyleDefault.Background(bg).Foreground(th.Subtle)
	titleStyle := tcell.StyleDefault.Background(bg).Foreground(th.Accent).Bold(true)
	mutedStyle := tcell.StyleDefault.Background(bg).Foreground(th.Muted)

	fillRect(a.screen, mx, my, mw, mh, bgStyle)
	drawBorder(a.screen, mx, my, mw, mh, borderStyle)
	drawHDivider(a.screen, mx, my+2, mw, borderStyle)
	drawAt(a.screen, mx+1, my+1, fmt.Sprintf(" Send %s to", noteCount(a.reviewBatch.Len())), titleStyle)
	hint := "esc "
	drawAt(a.screen, mx+mw-1-runeLen(hint), my+1, hint, mutedStyle)

	for i, t := range a.pickerTargets {
		cy := my + 3 + i
		style := bgStyle
		if i == a.pickerHover {
			style = tcell.StyleDefault.Background(th.Selection).Foreground(th.Text).Bold(true)
			for cx := mx + 1; cx < mx+mw-1; cx++ {
				a.screen.SetContent(cx, cy, ' ', nil, style)
			}
		}
		drawClipped(a.screen, mx+2, cy, mw-4, t.Name, style)
		// The status is what tells two identically named agents apart —
		// the one still working is usually the one you were watching.
		if t.Status != "" {
			label := "· " + t.Status
			if col := mx + mw - 2 - runeLen(label); col > mx+2 {
				drawClipped(a.screen, col, cy, runeLen(label), label, style.Foreground(th.Muted))
			}
		}
	}
	a.screen.HideCursor()
}

// -----------------------------------------------------------------------------
// Staleness
// -----------------------------------------------------------------------------

// markStaleComments flags every note whose file has left the changeset, and
// unflags one whose file has come back.
//
// Driven by the git-status refresh, which is the only place that knows what
// the changeset currently is. Line numbers are never touched: a note that
// has gone stale is still exactly the note the reviewer wrote about exactly
// the code in its snippet, and "helpfully" re-anchoring it to whatever is
// at that line number now would silently change what they said.
//
// A failed or non-repo snapshot marks nothing. Otherwise a transient git
// failure would flag an entire review stale.
func (a *App) markStaleComments(snap gitSnapshot) {
	if !snap.IsRepo || len(a.reviewBatch.Comments) == 0 {
		return
	}
	inChangeset := make(map[string]bool, len(snap.Entries))
	for _, e := range snap.Entries {
		inChangeset[e.Rel] = true
	}
	for i := range a.reviewBatch.Comments {
		a.reviewBatch.Comments[i].Stale = !inChangeset[a.reviewBatch.Comments[i].File]
	}
}
