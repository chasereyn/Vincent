// =============================================================================
// File: internal/app/cheatsheet.go
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// Derived from the menuLayout / menuModalRect / drawMenu trio that used to
// live in internal/app/app.go (Copyright 2026 Cloudmanic, LLC, MIT
// licensed). The bordered-modal geometry and painter are that code; the
// actions, the hover, and the enable predicates are not.
// =============================================================================

// cheatsheet.go is the Esc-? key table, and the whole of what replaced the
// ≡ action menu.
//
// The owner's first real session with Vincent 0.3.0 ended with "the Esc
// leader works great — the menu is not needed". The menu was paying for
// itself twice by then: a button in the tab bar, a modal with hover state
// and per-row enable predicates, and a second code path into every action
// that the leader key already reached. What was actually load-bearing was
// the half nobody could get anywhere else — a list of what the keys are.
//
// So the cheatsheet is READ-ONLY on purpose. No hover, no selection, no
// actions; Esc, Enter, or a click anywhere dismisses it. That is exactly
// what lets it be generated straight from leader.go's table (via
// leaderRows) instead of carrying a second hand-written list of rows that
// can drift from the bindings it claims to document. A binding added to
// leader.go shows up here on the next frame, with no edit to this file.

package app

import (
	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/version"
)

const (
	// cheatsheetWidth is the modal's column count. Sized for the widest
	// row the leader table can produce — "Esc " + a key glyph + a two-cell
	// gap + the longest hint — with room to spare, so a new binding with a
	// slightly longer hint doesn't silently clip.
	cheatsheetWidth = 44

	// cheatsheetHintCol is the column (relative to the modal's left edge)
	// the hint text starts at. Fixed rather than derived so every row's
	// hint lines up in a column regardless of key-glyph width.
	cheatsheetHintCol = 11
)

// cheatsheetLayout returns the cheatsheet's rows in draw order, the
// relative Y offsets of its horizontal dividers, and the modal's total
// cell height. Split out from the painter for the same reason menuLayout
// was: the rect helper needs the height before anything is drawn, and a
// test wants to assert on the row list without a screen.
//
// Row 1 is the title, row 2 the divider under it, rows 3+ the bindings,
// with a divider wherever the group changes.
func (a *App) cheatsheetLayout() (rows []leaderRow, dividers []int, modalHeight int) {
	rows = leaderRows()
	dividers = []int{2}
	y := 3
	for i, r := range rows {
		if i > 0 && r.group != rows[i-1].group {
			dividers = append(dividers, y)
			y++
		}
		y++
	}
	// y now points at the bottom border row; height is one beyond.
	return rows, dividers, y + 1
}

// cheatsheetRowY returns the screen-relative Y offset of row index i in
// the layout, or -1 when i is out of range. The painter needs the same
// group-aware offsets cheatsheetLayout computed, and recomputing them in
// two places is how a divider ends up drawn through a row.
func cheatsheetRowY(rows []leaderRow, i int) int {
	if i < 0 || i >= len(rows) {
		return -1
	}
	y := 3
	for j := 1; j <= i; j++ {
		if rows[j].group != rows[j-1].group {
			y++
		}
		y++
	}
	return y
}

// cheatsheetRect returns the modal's on-screen rectangle, centred in the
// window. Height comes from the layout so a binding added to leader.go
// grows the modal automatically. The origin clamps at (0, 0) rather than
// going negative on a window too small to hold the table — the painter is
// bounds-tolerant (tcell drops out-of-range SetContent), so an over-tall
// table on a 24-row terminal loses its last rows rather than corrupting
// the frame.
func (a *App) cheatsheetRect() (x, y, w, h int) {
	w = cheatsheetWidth
	_, _, h = a.cheatsheetLayout()
	x = (a.width - w) / 2
	y = (a.height - h) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y, w, h
}

// openCheatsheet shows the key table. Bound to Esc ? and to a right-click
// that lands on nothing in particular, which is the redundant mouse-only
// path the old ≡ button used to provide.
func (a *App) openCheatsheet() {
	a.closeAllModals()
	a.cheatsheetOpen = true
}

// closeCheatsheet hides the key table.
func (a *App) closeCheatsheet() {
	a.cheatsheetOpen = false
}

// handleCheatsheetKey owns the keyboard while the cheatsheet is up. Esc and
// Enter dismiss it; everything else is swallowed. Swallowing rather than
// falling through is deliberate — the table is a full-screen-ish overlay,
// and a keystroke that reached the buffer underneath it would edit a file
// the user cannot currently see.
func (a *App) handleCheatsheetKey(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyEsc, tcell.KeyEnter:
		a.closeCheatsheet()
	}
}

// handleCheatsheetMouse dismisses the cheatsheet on any button press,
// inside the modal or out. There is nothing to click on — no row does
// anything — so "click to make it go away" is the only sensible gesture,
// and it means the user never has to find the close affordance. Wheel and
// motion events are ignored so a stray scroll doesn't dismiss it.
func (a *App) handleCheatsheetMouse(_, _ int, btn tcell.ButtonMask) {
	if btn&(tcell.Button1|tcell.Button2|tcell.Button3) == 0 {
		return
	}
	a.closeCheatsheet()
}

// drawCheatsheet paints the key table centred in the window: a bordered
// panel, a title row, one row per leader binding as "Esc <key>  <hint>",
// dividers between groups, and the version stamped into the bottom border.
//
// The version is here because the ≡ menu's footer used to carry it and
// there is no auto-update: `vincent --version` and this footer are the only
// two ways to tell whether the binary on PATH is the one just built.
func (a *App) drawCheatsheet() {
	mx, my, mw, mh := a.cheatsheetRect()
	rows, dividers, _ := a.cheatsheetLayout()

	bg := a.theme.LineHL
	bgStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Text)
	borderStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Subtle)
	titleStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Accent).Bold(true)
	mutedStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Muted)
	keyStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.AccentSoft).Bold(true)

	// Fill the whole modal rect first so nothing of the frame underneath
	// shows through between rows.
	for cy := my; cy < my+mh; cy++ {
		for cx := mx; cx < mx+mw; cx++ {
			a.screen.SetContent(cx, cy, ' ', nil, bgStyle)
		}
	}

	// Outer border.
	a.screen.SetContent(mx, my, '┌', nil, borderStyle)
	a.screen.SetContent(mx+mw-1, my, '┐', nil, borderStyle)
	a.screen.SetContent(mx, my+mh-1, '└', nil, borderStyle)
	a.screen.SetContent(mx+mw-1, my+mh-1, '┘', nil, borderStyle)
	for cx := mx + 1; cx < mx+mw-1; cx++ {
		a.screen.SetContent(cx, my, '─', nil, borderStyle)
		a.screen.SetContent(cx, my+mh-1, '─', nil, borderStyle)
	}
	for cy := my + 1; cy < my+mh-1; cy++ {
		a.screen.SetContent(mx, cy, '│', nil, borderStyle)
		a.screen.SetContent(mx+mw-1, cy, '│', nil, borderStyle)
	}

	// Group dividers, including the always-on one under the title.
	for _, dy := range dividers {
		cy := my + dy
		a.screen.SetContent(mx, cy, '├', nil, borderStyle)
		a.screen.SetContent(mx+mw-1, cy, '┤', nil, borderStyle)
		for cx := mx + 1; cx < mx+mw-1; cx++ {
			a.screen.SetContent(cx, cy, '─', nil, borderStyle)
		}
	}

	// Title row: " Keys" on the left, the dismiss hint on the right.
	drawAt(a.screen, mx+1, my+1, " Keys", titleStyle)
	hint := "esc "
	drawAt(a.screen, mx+mw-1-runeLen(hint), my+1, hint, mutedStyle)

	// Version stamp baked into the bottom border, right-aligned, with a
	// pad of dashes left between it and the corner so it reads as part of
	// the frame rather than a label butted up against it.
	verLabel := " v" + version.Version + " "
	verX := mx + mw - 2 - runeLen(verLabel)
	if verX > mx+1 {
		drawAt(a.screen, verX, my+mh-1, verLabel, mutedStyle)
	}

	for i, r := range rows {
		cy := my + cheatsheetRowY(rows, i)
		drawAt(a.screen, mx+2, cy, "Esc "+r.key, keyStyle)
		hintMax := mw - cheatsheetHintCol - 2
		drawAt(a.screen, mx+cheatsheetHintCol, cy, trimRunes(r.hint, hintMax), bgStyle)
	}

	a.screen.HideCursor()
}
