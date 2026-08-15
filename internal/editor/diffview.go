// =============================================================================
// File: internal/editor/diffview.go
// Author: Chase Reynolds
// Created: 2026-08-15
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// The render half of the port of herdr-sidebar's src/diffview.rs. Parsing
// lives in internal/diff; this file draws the result. Structured after
// image.go, which is the existing pattern in this codebase for "a Tab in a
// non-text mode".
// =============================================================================

// diffview.go gives Tab an inline, Zed-shaped diff mode: two line-number
// gutters (old and new), a ± marker column, full-width red/green row tints,
// and a darker tint over just the characters that changed on a line edited
// in place. Syntax highlighting runs over the diff body, so code in a diff
// is coloured the same way as code in a file.
//
// A diff tab is a real Tab, not a parallel type. Its Buffer holds the row
// texts, which means scrolling, clamping, hit-testing, and the find bar all
// work on a diff without a single special case in the app layer. What it
// does NOT get is a cursor or any mutation: Tab.ReadOnly gates that, and the
// key handler drops editing keys before they reach the buffer.

package editor

import (
	"fmt"
	"os"
	"strconv"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/diff"
	"github.com/chasereyn/vincent/internal/theme"
)

// diffMode is the value Tab.Mode takes for a diff view, mirroring
// imageMode. Lives here so the mode's state and behaviour stay together.
const diffMode = "diff"

// minDiffNumberWidth is the floor for each of the two line-number columns.
// Two digits keeps single-digit line numbers from making the gutter look
// ragged, and matches what herdr-sidebar does.
const minDiffNumberWidth = 2

// diffGapRune marks the elision between two hunks.
const diffGapRune = '⋯'

// NewDiffTab builds a read-only diff tab for path from already-parsed rows.
//
// path is the file the diff is OF, not a file on disk that contains a diff.
// It is kept because the syntax highlighter picks its lexer from the
// extension, and because the app needs it to know which file to re-diff when
// the agent writes to it again. Nothing in this package ever reads it.
func NewDiffTab(path string, rows []diff.Row) *Tab {
	t := &Tab{
		Path:       path,
		Buffer:     &Buffer{Lines: diff.Texts(rows)},
		Mode:       diffMode,
		DiffRows:   rows,
		StyleStale: true,
	}
	// An empty diff would leave Buffer.Lines nil, and enough of the
	// surrounding code assumes at least one line that it isn't worth
	// auditing all of it — give it a blank row instead.
	if len(t.Buffer.Lines) == 0 {
		t.Buffer.Lines = []string{""}
	}
	// Stamp the file's mtime so the app's reconcile loop doesn't re-run
	// the diff on its very first tick over a diff we just built.
	if info, err := os.Stat(path); err == nil {
		t.Mtime = info.ModTime()
	}
	t.initUndo()
	return t
}

// IsDiff reports whether the tab is showing a diff rather than a file.
func (t *Tab) IsDiff() bool {
	return t.Mode == diffMode
}

// ReadOnly reports whether the tab refuses buffer mutations. Every non-text
// mode is read-only, so this is "the tab has a mode" rather than a list that
// has to be extended each time one is added.
//
// Vincent is a review client, so in the long run this should be true for
// every tab; it is written as a mode check rather than a constant because
// the plain text path still owns the editing code it inherited from
// spice-edit. See CLAUDE.md.
func (t *Tab) ReadOnly() bool {
	return t.Mode != ""
}

// SetDiffRows swaps in a freshly re-parsed diff while keeping the tab's
// scroll position and identity. Used when the file changes on disk under a
// diff tab — the agent has written again — so the view updates in place
// instead of jumping back to the top.
func (t *Tab) SetDiffRows(rows []diff.Row) {
	t.DiffRows = rows
	t.Buffer = &Buffer{Lines: diff.Texts(rows)}
	if len(t.Buffer.Lines) == 0 {
		t.Buffer.Lines = []string{""}
	}
	t.StyleStale = true
	t.Cursor = t.Buffer.Clamp(t.Cursor)
	t.Anchor = t.Cursor
}

// DiffStats returns the added and deleted line counts for the status bar.
func (t *Tab) DiffStats() (added, deleted int) {
	return diff.Stats(t.DiffRows)
}

// ScrollToRow puts row at roughly a third of the way down the viewport and
// parks the cursor on it. A third rather than the top because the lines
// ABOVE a change are usually what tells you whether the change is right.
func (t *Tab) ScrollToRow(row, viewH int) {
	if row < 0 || row >= t.Buffer.LineCount() {
		return
	}
	t.Cursor = Position{Line: row}
	t.Anchor = t.Cursor
	t.ScrollY = row - viewH/3
	if t.ScrollY < 0 {
		t.ScrollY = 0
	}
	// Deliberately does NOT set cursorMoved: that flag makes Render call
	// EnsureVisible, which would drag the viewport back to put the cursor
	// on screen minimally and undo the third-of-a-page framing above.
}

// diffNumberWidth is the width of ONE of the two line-number columns.
func diffNumberWidth(rows []diff.Row) int {
	w := len(strconv.Itoa(diff.MaxLineNo(rows)))
	if w < minDiffNumberWidth {
		w = minDiffNumberWidth
	}
	return w
}

// diffGutterCells is the total width of a diff row's non-content prefix:
// two number columns, the ± marker, and the spaces separating them.
//
//	" 12  15 + code"
//	 ^^^^ ^^^^ ^ ^
//	 old  new  mark, then one pad cell before the content.
func diffGutterCells(rows []diff.Row) int {
	return diffNumberWidth(rows)*2 + 4
}

// diffRowColors returns the row background, the word-level tint, the marker
// rune, and the marker colour for a diff row kind.
func diffRowColors(th theme.Theme, kind diff.Kind) (bg, word tcell.Color, mark rune, markFG tcell.Color) {
	switch kind {
	case diff.KindAdded:
		return th.DiffAddBG, th.DiffAddWordBG, '+', th.DiffAddMark
	case diff.KindDeleted:
		return th.DiffDelBG, th.DiffDelWordBG, '-', th.DiffDelMark
	default:
		return th.BG, th.BG, ' ', th.Muted
	}
}

// renderDiff paints the diff tab. Called from Tab.Render when the tab is in
// diffMode.
//
// The row tint is painted across the FULL width of the pane, not just as far
// as the text goes. A tint that stops at end-of-line makes short lines look
// like a different kind of change from long ones, which is exactly the sort
// of false signal a review tool must not emit.
func (t *Tab) renderDiff(scr tcell.Screen, th theme.Theme, x, y, w, h int) {
	t.clampScroll(h)

	// Same re-tokenise gate as the text path: only when the content changed
	// or the viewport moved. Diffs of agent output get large, and this is
	// what keeps a redraw proportional to the terminal rather than the diff.
	if t.StyleStale || t.ScrollY != t.lastHighlightScrollY || h != t.lastHighlightHeight {
		// Highlighting the diff body as if it were a source file is an
		// approximation: a line that was edited in place appears twice
		// (once deleted, once added), which a stateful lexer sees as
		// duplicated code. In practice that only misleads inside
		// multi-line strings and block comments, and the alternative —
		// two parallel lexers for the old and new sides, as delta does —
		// costs a second highlighter pass for a case reviewers rarely hit.
		t.Styles = HighlightVisible(t.Path, t.Buffer.Lines, t.ScrollY, h, th)
		t.StyleStale = false
		t.lastHighlightScrollY = t.ScrollY
		t.lastHighlightHeight = h
	}

	numW := diffNumberWidth(t.DiffRows)
	gutter := diffGutterCells(t.DiffRows)
	contentX := x + gutter
	contentW := w - gutter
	if contentW < 1 {
		contentW = 1
	}

	base := tcell.StyleDefault.Background(th.BG).Foreground(th.Text)
	for cy := y; cy < y+h; cy++ {
		for cx := x; cx < x+w; cx++ {
			scr.SetContent(cx, cy, ' ', nil, base)
		}
	}

	for screenRow := 0; screenRow < h; screenRow++ {
		idx := t.ScrollY + screenRow
		if idx >= len(t.DiffRows) {
			break
		}
		row := t.DiffRows[idx]
		cy := y + screenRow
		onCursor := idx == t.Cursor.Line

		rowBG, wordBG, mark, markFG := diffRowColors(th, row.Kind)
		// Context rows pick up the active-line highlight; changed rows keep
		// their tint, which is already a stronger signal than LineHL and
		// would be muddied by blending the two.
		if onCursor && row.Kind == diff.KindContext {
			rowBG = th.LineHL
		}
		rowStyle := tcell.StyleDefault.Background(rowBG).Foreground(th.Text)
		for cx := x; cx < x+w; cx++ {
			scr.SetContent(cx, cy, ' ', nil, rowStyle)
		}

		if row.Kind == diff.KindGap {
			// The elision between hunks. Drawn in the marker column so it
			// lines up with the ± of the rows above and below, which reads
			// as "the gutter continues, the file does not".
			scr.SetContent(x+numW*2+2, cy, diffGapRune, nil, rowStyle.Foreground(th.Subtle))
			continue
		}

		if row.Kind == diff.KindMeta {
			// Unparsed but preserved — "Binary files … differ" and friends.
			// No line numbers exist for it, so it gets the content column
			// and nothing else.
			drawRunes(scr, contentX, cy, contentW, []rune(row.Text), rowStyle.Foreground(th.Muted))
			continue
		}

		numFG := th.Muted
		if onCursor {
			numFG = th.AccentSoft
		}
		numStyle := rowStyle.Foreground(numFG)
		drawRunes(scr, x, cy, numW, []rune(diffNumberText(row.Old, numW)), numStyle)
		drawRunes(scr, x+numW+1, cy, numW, []rune(diffNumberText(row.New, numW)), numStyle)
		scr.SetContent(x+numW*2+2, cy, mark, nil, rowStyle.Foreground(markFG))

		// Content. Walked from the start of the line rather than from
		// ScrollX so tab stops anchor to column zero — same reasoning as
		// the text renderer, and it matters more here because diff bodies
		// are almost always indented code.
		runes := []rune(row.Text)
		var styles []tcell.Style
		if idx < len(t.Styles) {
			styles = t.Styles[idx]
		}
		scrollVisual := LineVisualCol(runes, t.ScrollX)
		visualCol := 0
		for runeIdx, r := range runes {
			width := RuneVisualWidth(r, visualCol)
			if runeIdx >= t.ScrollX {
				st := rowStyle
				if runeIdx < len(styles) {
					st = styles[runeIdx]
				}
				cellBG := rowBG
				if row.HasWordTint() && runeIdx >= row.WordStart && runeIdx < row.WordEnd {
					cellBG = wordBG
				}
				st = st.Background(cellBG)
				glyph := r
				if r == '\t' {
					glyph = ' '
				}
				for cell := 0; cell < width; cell++ {
					sc := visualCol - scrollVisual + cell
					if sc < 0 {
						continue
					}
					if sc >= contentW {
						break
					}
					ch := glyph
					if cell > 0 {
						ch = ' '
					}
					scr.SetContent(contentX+sc, cy, ch, nil, st)
				}
			}
			visualCol += width
		}

		// Horizontal-overflow affordance, same as the text renderer: with
		// no scrollbar, this is the only cue that a line runs off the pane.
		overflow := rowStyle.Foreground(th.Muted)
		if t.ScrollX > 0 {
			scr.SetContent(contentX, cy, '‹', nil, overflow)
		}
		if visualCol-scrollVisual > contentW {
			scr.SetContent(contentX+contentW-1, cy, '›', nil, overflow)
		}
	}

	// No caret: a diff has a selected row, not an insertion point.
	scr.HideCursor()
}

// diffNumberText right-aligns a line number in width cells, or returns
// blanks when the row doesn't exist on that side of the diff (line zero).
func diffNumberText(line, width int) string {
	if line <= 0 {
		return fmt.Sprintf("%*s", width, "")
	}
	return fmt.Sprintf("%*d", width, line)
}

// drawRunes writes runes at (x, y), stopping at maxW cells. A small helper
// for the fixed-width gutter fields, where the runes are digits and spaces
// and none of the tab-stop or wide-glyph handling the content path needs
// applies.
func drawRunes(scr tcell.Screen, x, y, maxW int, runes []rune, style tcell.Style) {
	for i, r := range runes {
		if i >= maxW {
			return
		}
		scr.SetContent(x+i, y, r, nil, style)
	}
}

// diffHitTest maps a click inside a diff tab to a buffer position, where
// Line is the index of the diff row clicked. Clicks in the gutter land on
// column zero of their row so that clicking a line number still selects the
// row — in a read-only view there is no reason for a gutter click to be a
// dead zone.
func (t *Tab) diffHitTest(localX, localY, _, h int) (Position, bool) {
	if localY < 0 || localY >= h {
		return Position{}, false
	}
	line := t.ScrollY + localY
	if line < 0 || line >= t.Buffer.LineCount() {
		return Position{}, false
	}
	gutter := diffGutterCells(t.DiffRows)
	if localX < gutter {
		return Position{Line: line, Col: 0}, true
	}
	runes := t.Buffer.LineRunes(line)
	scrollVisual := LineVisualCol(runes, t.ScrollX)
	col := RuneColAtVisual(runes, scrollVisual+(localX-gutter))
	if col > len(runes) {
		col = len(runes)
	}
	if col < 0 {
		col = 0
	}
	return Position{Line: line, Col: col}, true
}
