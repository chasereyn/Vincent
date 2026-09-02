// =============================================================================
// File: internal/editor/diffoverlay.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// The inline-box shape comes from tuicr's src/ui/comment_panel.rs
// (format_comment_input_lines), which grows a bordered block into the diff
// flow directly under the annotated line instead of floating a modal over
// it. This file is only the mechanism: the diff renderer needs somewhere to
// put extra rows, and the click handler needs to know which one was hit.
// =============================================================================

// diffoverlay.go lets rows that are not diff rows occupy real space inside
// a diff tab: the review composer, and the one-line markers under lines
// that already carry a note.
//
// Two reasons the overlay lives here rather than being painted over the
// diff by the app afterwards. First, an overlay has to PUSH the rows below
// it down — a box drawn on top would hide the code it is a note about,
// which is the one thing a review view must not do. Second, once screen
// rows and diff rows stop being the same thing, exactly one piece of code
// may own the mapping between them; two would drift, and the symptom would
// be clicks landing on the wrong line of somebody's review.
//
// The editor package knows nothing about reviews. An overlay is a list of
// pre-styled strings the app hands over before each render, and a hit is
// reported back as "diff row R, overlay line N" for the app to interpret.
//
// Scrolling stays measured in DIFF rows, not screen rows. ScrollY keeps
// indexing DiffRows, so opening a composer never moves the viewport and
// the wheel keeps stepping over code rather than over chrome.

package editor

import "github.com/gdamore/tcell/v2"

// DiffOverlayLine is one extra screen row, already styled by the app.
//
// BG must always be set to a real colour. Vincent paints every surface
// explicitly rather than leaning on tcell.ColorDefault, which would inherit
// whatever the host terminal happens to use and would not reliably be
// black — the zero value of tcell.Color IS ColorDefault, so an unset BG is
// a bug, not a default.
//
// Caret is a rune offset into Text where the terminal caret should sit, or
// -1 for a row with no caret. It is how the composer's text field gets a
// real blinking cursor without the editor package knowing what a composer
// is. Use NoCaret rather than writing -1 at call sites.
type DiffOverlayLine struct {
	Text  string
	FG    tcell.Color
	BG    tcell.Color
	Bold  bool
	Caret int
}

// NoCaret is the Caret value for an overlay row that shows no caret.
const NoCaret = -1

// DiffOverlay is a block of extra rows attached BELOW one diff row.
//
// Below rather than above because a note is a response to the line it
// annotates: reading top to bottom you get the code, then the remark. It is
// also what keeps the anchor line at the position the reviewer clicked
// instead of shifting it down by the height of their own note.
type DiffOverlay struct {
	Row   int
	Lines []DiffOverlayLine
}

// DiffHit is what a click inside a diff tab landed on.
//
// Overlay is NoOverlay when the click hit the diff row itself; otherwise it
// is the index of the overlay line under Row. Col is a rune offset — into
// the overlay line's Text for an overlay hit, into the diff row's text
// otherwise — which is what lets a click inside the composer place the
// caret where the reviewer pointed.
type DiffHit struct {
	Row     int
	Overlay int
	Col     int
}

// NoOverlay is the DiffHit.Overlay value meaning "the diff row itself".
const NoOverlay = -1

// SetDiffOverlays replaces the tab's overlay set. The app rebuilds and
// re-sets this on every draw rather than mutating it incrementally: the
// overlays are derived state (which comments exist, whether the composer is
// open), and deriving them once per frame is cheaper than keeping two
// representations honest with each other.
func (t *Tab) SetDiffOverlays(overlays []DiffOverlay) {
	t.DiffOverlays = overlays
}

// overlayLinesFor returns the overlay rows attached below diff row idx, or
// nil. A linear scan because the set holds at most a handful of entries —
// the visible comments plus one composer — and a map would have to be
// rebuilt on every frame anyway.
func (t *Tab) overlayLinesFor(idx int) []DiffOverlayLine {
	for _, ov := range t.DiffOverlays {
		if ov.Row == idx {
			return ov.Lines
		}
	}
	return nil
}

// diffCell is one screen row of a diff viewport: either a diff row, or one
// overlay line hanging below it.
type diffCell struct {
	row     int
	overlay int
}

// diffDisplayCells lays out the viewport top to bottom: each diff row from
// ScrollY down, with its overlay rows interleaved after it, stopping at h
// rows.
//
// This is THE mapping between screen rows and diff rows. The renderer walks
// it to paint and the hit-tester walks it to interpret a click, so the two
// cannot disagree — which was the whole reason for putting overlays in this
// package instead of drawing them over the top from the app.
func (t *Tab) diffDisplayCells(h int) []diffCell {
	if h <= 0 {
		return nil
	}
	cells := make([]diffCell, 0, h)
	for idx := t.ScrollY; idx < len(t.DiffRows) && len(cells) < h; idx++ {
		if idx < 0 {
			continue
		}
		cells = append(cells, diffCell{row: idx, overlay: NoOverlay})
		for oi := range t.overlayLinesFor(idx) {
			if len(cells) >= h {
				break
			}
			cells = append(cells, diffCell{row: idx, overlay: oi})
		}
	}
	return cells
}

// DiffHitAt maps a click inside a diff tab to the row or overlay line under
// it. Reports ok=false for a click below the last row, or on a tab that is
// not a diff.
//
// The app calls this BEFORE the normal cursor-placing hit test, so a click
// in the composer or on a comment marker is claimed by the review layer
// instead of moving the diff's selection out from under the box the user is
// typing into.
func (t *Tab) DiffHitAt(localX, localY, _, h int) (DiffHit, bool) {
	if !t.IsDiff() || localY < 0 || localY >= h {
		return DiffHit{}, false
	}
	cells := t.diffDisplayCells(h)
	if localY >= len(cells) {
		return DiffHit{}, false
	}
	cell := cells[localY]
	hit := DiffHit{Row: cell.row, Overlay: cell.overlay}
	if cell.overlay == NoOverlay {
		return hit, true
	}
	lines := t.overlayLinesFor(cell.row)
	if cell.overlay >= len(lines) {
		return hit, true
	}
	col := localX
	if col < 0 {
		col = 0
	}
	if n := len([]rune(lines[cell.overlay].Text)); col > n {
		col = n
	}
	hit.Col = col
	return hit, true
}

// drawOverlayLine paints one overlay row across the full pane width and
// returns the screen column of its caret, or -1 when it has none.
//
// Full width because an overlay is a surface, not a string: a box whose
// background stops where its text does reads as a ragged smear over the
// diff rather than as a panel sitting in it.
func drawOverlayLine(scr tcell.Screen, x, y, w int, line DiffOverlayLine) int {
	style := tcell.StyleDefault.Background(line.BG).Foreground(line.FG).Bold(line.Bold)
	for cx := x; cx < x+w; cx++ {
		scr.SetContent(cx, y, ' ', nil, style)
	}
	used := 0
	for _, r := range line.Text {
		if used >= w {
			break
		}
		scr.SetContent(x+used, y, r, nil, style)
		used++
	}
	if line.Caret < 0 || line.Caret > w-1 {
		return -1
	}
	return x + line.Caret
}
