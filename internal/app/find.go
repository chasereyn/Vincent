// =============================================================================
// File: internal/app/find.go
// Author: Spicer Matthews <spicer@cloudmanic.com>
// Created: 2026-04-30
// Copyright: 2026 Cloudmanic, LLC. All rights reserved.
//
// Replace row added 2026-09-03 (Chase Reynolds) for Vincent phase 6b.
// =============================================================================

// find.go owns the in-file search UI: the "Find:" bar that lives directly
// above the status bar, its optional "Replace:" row, the keystroke dispatch
// while the bar is focused, and the Esc-/ leader entry point.
//
// The matching and replacing logic itself lives on Tab (see
// internal/editor/find.go) so each tab carries its own query, match list,
// and current-index. This file only handles UI: the input strings,
// cursors, scroll, focus, and rendering.

package app

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
)

// findBarRowHeight is the cell height of a single row of the find bar.
// findBarHeight (below) multiplies this by 1 or 2 depending on whether the
// replace row is visible; pulled out as a constant so both stay in terms
// of "one row" rather than a magic number.
const findBarRowHeight = 1

// findBarHeight returns the bar's current height in rows: one while only
// the find field is showing, two once the replace row has been revealed
// (see handleFindKey's Tab case). editorRect and findBarRect both consult
// this, so the layout follows the reveal for free.
func (a *App) findBarHeight() int {
	if a.findReplaceVisible {
		return findBarRowHeight * 2
	}
	return findBarRowHeight
}

// openFind shows the find bar with an empty input and the replace row
// collapsed. We don't pre-fill the user's last query because closing the
// bar already clears find state — Esc means "I'm done searching." Each
// Esc-/ opens a fresh search.
func (a *App) openFind() {
	tab := a.activeTabPtr()
	if tab == nil || tab.IsImage() {
		return
	}
	a.closeAllModals() // a modal would otherwise eat our keystrokes
	a.findOpen = true
	a.findValue = nil
	a.findCursor = 0
	a.findScroll = 0
	a.findReplaceVisible = false
	a.findReplaceFocus = false
	a.findReplaceValue = nil
	a.findReplaceCursor = 0
	a.findReplaceScroll = 0
}

// closeFind hides the find bar AND clears the active tab's find state so
// the highlights disappear with the bar. Leaving them painted after close
// is surprising — users expect Esc to mean "I'm done searching." Esc-/
// after a closed bar simply re-opens the bar so the user can type a fresh
// query.
func (a *App) closeFind() {
	a.findOpen = false
	a.findValue = nil
	a.findCursor = 0
	a.findScroll = 0
	a.findReplaceVisible = false
	a.findReplaceFocus = false
	a.findReplaceValue = nil
	a.findReplaceCursor = 0
	a.findReplaceScroll = 0
	if tab := a.activeTabPtr(); tab != nil {
		tab.ClearFind()
	}
}

// findApplyQuery pushes the current input text into the active tab's
// find state and snaps the cursor to the new "current" match (so the
// user can see their result while still typing). Called on every input
// change so the highlights track the query live.
func (a *App) findApplyQuery() {
	tab := a.activeTabPtr()
	if tab == nil {
		return
	}
	tab.SetFindQuery(string(a.findValue))
	tab.FocusCurrentMatch()
}

// findNext is the Enter-in-the-bar action: jump to the next match (with
// wrap). Also reachable from the Esc-g leader.
func (a *App) findNext() {
	if tab := a.activeTabPtr(); tab != nil {
		tab.FindNext()
	}
}

// findPrev is the Shift-Enter action: jump to the previous match.
func (a *App) findPrev() {
	if tab := a.activeTabPtr(); tab != nil {
		tab.FindPrev()
	}
}

// menuFind is the named entry point for the bar. Behaves identically to the
// Esc-f leader — opens the bar against the active tab.
func (a *App) menuFind() {
	a.openFind()
}

// hasFindable reports whether the active tab is a text tab — used to
// keep the find bar shut on image tabs / no-tab states. Deliberately
// gates on IsImage() rather than ReadOnly(): searching inside a diff is
// useful and already works, so a diff tab must stay findable. Only the
// replace half (hasReplaceable) needs the stricter gate.
func (a *App) hasFindable() bool {
	t := a.activeTabPtr()
	return t != nil && !t.IsImage()
}

// hasReplaceable reports whether the active tab can accept a replace.
// Unlike hasFindable, this gates on ReadOnly() rather than IsImage() — a
// diff tab is findable (its rendered text is worth searching) but must
// never be rewritten, since it carries the real file's Path and a write
// would put diff markup over the user's source.
func (a *App) hasReplaceable() bool {
	t := a.activeTabPtr()
	return t != nil && !t.ReadOnly()
}

// findBarRect returns the on-screen rectangle of the find bar, covering
// both rows once the replace field is visible. Always pinned to the rows
// directly above the status bar. Caller is expected to check a.findOpen
// before drawing.
func (a *App) findBarRect() (x, y, w, h int) {
	sw := a.sidebarW()
	h = a.findBarHeight()
	return sw, a.height - 1 - h, a.width - sw, h
}

// replaceCurrentMatch is the Enter-in-the-replace-field action: replace
// the tab's current match with the replace field's text and advance to
// the next one. Refused with a flash on a read-only tab (diff) — search
// stays allowed there, but rewriting it is not, per hasReplaceable's
// doc comment.
func (a *App) replaceCurrentMatch() {
	tab := a.activeTabPtr()
	if tab == nil {
		return
	}
	if tab.ReadOnly() {
		a.flash("Can't replace in a read-only tab")
		return
	}
	if !tab.ReplaceCurrentMatch(string(a.findReplaceValue)) {
		a.flash("No match to replace")
	}
}

// replaceAllMatches is the Alt+Enter action: replace every current match
// in one undo step. Refused with a flash on a read-only tab, same as
// replaceCurrentMatch.
func (a *App) replaceAllMatches() {
	tab := a.activeTabPtr()
	if tab == nil {
		return
	}
	if tab.ReadOnly() {
		a.flash("Can't replace in a read-only tab")
		return
	}
	n := tab.ReplaceAll(string(a.findReplaceValue))
	if n == 0 {
		a.flash("No matches to replace")
		return
	}
	a.flash(fmt.Sprintf("Replaced %d match(es)", n))
}

// handleFindKey dispatches a keystroke while the find bar is focused.
// Behavior:
//
//	Esc                     close the bar
//	Tab                     reveal the replace row (if hidden) and toggle
//	                        focus between the find and replace fields
//	Enter (find focused)    jump to the next match
//	Shift+Enter (find)      jump to the previous match
//	Enter (replace focused) replace the current match, advance to the next
//	Alt+Enter               replace every match, one undo step
//	Backspace / Delete      edit the focused field (live re-search for find)
//	Left/Right/Home/End     cursor movement inside the focused field
//	printable rune          insert into the focused field
//
// Alt+Enter was chosen over "Esc then Enter" for Replace All: Esc already
// means "close the bar" the instant it's pressed (see the Esc case below),
// so a two-key Esc-Enter sequence can never reach the bar's Enter handler —
// the bar would already be gone. tcell reports Alt+Enter as KeyEnter with
// ModAlt set (key.go's ev.mod&ModAlt), which is what this checks.
//
// Anything else is dropped on the floor — the find bar owns the keyboard
// while it's open.
func (a *App) handleFindKey(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyEsc:
		a.closeFind()
	case tcell.KeyTab:
		a.findReplaceVisible = true
		a.findReplaceFocus = !a.findReplaceFocus
	case tcell.KeyEnter:
		if ev.Modifiers()&tcell.ModAlt != 0 {
			a.replaceAllMatches()
			return
		}
		if a.findReplaceFocus {
			a.replaceCurrentMatch()
			return
		}
		if ev.Modifiers()&tcell.ModShift != 0 {
			a.findPrev()
		} else {
			a.findNext()
		}
	case tcell.KeyLeft:
		if a.findReplaceFocus {
			if a.findReplaceCursor > 0 {
				a.findReplaceCursor--
			}
			return
		}
		if a.findCursor > 0 {
			a.findCursor--
		}
	case tcell.KeyRight:
		if a.findReplaceFocus {
			if a.findReplaceCursor < len(a.findReplaceValue) {
				a.findReplaceCursor++
			}
			return
		}
		if a.findCursor < len(a.findValue) {
			a.findCursor++
		}
	case tcell.KeyHome:
		if a.findReplaceFocus {
			a.findReplaceCursor = 0
			return
		}
		a.findCursor = 0
	case tcell.KeyEnd:
		if a.findReplaceFocus {
			a.findReplaceCursor = len(a.findReplaceValue)
			return
		}
		a.findCursor = len(a.findValue)
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if a.findReplaceFocus {
			if a.findReplaceCursor > 0 {
				a.findReplaceValue = append(a.findReplaceValue[:a.findReplaceCursor-1], a.findReplaceValue[a.findReplaceCursor:]...)
				a.findReplaceCursor--
			}
			return
		}
		if a.findCursor > 0 {
			a.findValue = append(a.findValue[:a.findCursor-1], a.findValue[a.findCursor:]...)
			a.findCursor--
			a.findApplyQuery()
		}
	case tcell.KeyDelete:
		if a.findReplaceFocus {
			if a.findReplaceCursor < len(a.findReplaceValue) {
				a.findReplaceValue = append(a.findReplaceValue[:a.findReplaceCursor], a.findReplaceValue[a.findReplaceCursor+1:]...)
			}
			return
		}
		if a.findCursor < len(a.findValue) {
			a.findValue = append(a.findValue[:a.findCursor], a.findValue[a.findCursor+1:]...)
			a.findApplyQuery()
		}
	case tcell.KeyRune:
		r := ev.Rune()
		if r < 0x20 {
			return
		}
		if a.findReplaceFocus {
			next := make([]rune, 0, len(a.findReplaceValue)+1)
			next = append(next, a.findReplaceValue[:a.findReplaceCursor]...)
			next = append(next, r)
			next = append(next, a.findReplaceValue[a.findReplaceCursor:]...)
			a.findReplaceValue = next
			a.findReplaceCursor++
			return
		}
		next := make([]rune, 0, len(a.findValue)+1)
		next = append(next, a.findValue[:a.findCursor]...)
		next = append(next, r)
		next = append(next, a.findValue[a.findCursor:]...)
		a.findValue = next
		a.findCursor++
		a.findApplyQuery()
	}
}

// drawFindBar renders the find bar at the bottom of the editor area: the
// find row always, and a second replace row once findReplaceVisible.
// Layout, find row (left to right):
//
//	" Find: <input>                       3 of 12   Tab: replace · Enter: next · Esc: close "
//
// Replace row, when visible:
//
//	" Replace: <input>              Enter: replace · Alt+Enter: replace all "
//
// The hint on the right is dropped first when the window is too narrow to
// fit it; the match counter is dropped next; the input itself always stays
// visible because that's the whole point of the bar.
func (a *App) drawFindBar() {
	if !a.findOpen {
		return
	}
	bx, by, bw, _ := a.findBarRect()

	bg := a.theme.LineHL
	barStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Text)
	labelStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Accent).Bold(true)
	mutedStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Muted)
	emptyStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Error).Bold(true)

	findHint := " Tab: replace · Enter: next · Shift+Enter: prev · Esc: close "
	a.drawFindRow(bx, by, bw, " Find: ", a.findValue, a.findCursor, &a.findScroll, !a.findReplaceFocus,
		findHint, a.findCounterText(), a.findHasNoMatches(),
		barStyle, labelStyle, mutedStyle, emptyStyle)
	if !a.findReplaceVisible {
		return
	}
	replaceHint := " Enter: replace · Alt+Enter: replace all · Esc: close "
	a.drawFindRow(bx, by+1, bw, " Replace: ", a.findReplaceValue, a.findReplaceCursor, &a.findReplaceScroll, a.findReplaceFocus,
		replaceHint, "", false,
		barStyle, labelStyle, mutedStyle, emptyStyle)
}

// drawFindRow paints one row of the find bar — the find row or the
// replace row, they share every bit of layout logic. focused controls
// which field gets the blinking screen cursor; counter/hasNoMatches are
// only ever non-empty for the find row (the replace row passes "", false).
// scroll is a pointer into the App field that owns this row's horizontal
// scroll offset (a.findScroll / a.findReplaceScroll) so the computed value
// persists across frames — without that, scrollWindow would recompute from
// zero every draw and the window would visibly jump on every keystroke
// instead of holding steady while the cursor is already inside it.
func (a *App) drawFindRow(bx, by, bw int, label string, value []rune, cursor int, scroll *int, focused bool,
	hint, counter string, hasNoMatches bool,
	barStyle, labelStyle, mutedStyle, emptyStyle tcell.Style) {
	// Clear the row.
	for cx := bx; cx < bx+bw; cx++ {
		a.screen.SetContent(cx, by, ' ', nil, barStyle)
	}

	drawAt(a.screen, bx, by, label, labelStyle)
	inputStart := bx + runeLen(label)

	// Right side: counter + hint, drawn first so we can clip the input
	// against them on a narrow window.
	rightTextStart := bx + bw
	if bw > runeLen(label)+runeLen(hint)+10 {
		rightTextStart -= runeLen(hint) + 1
		drawAt(a.screen, rightTextStart, by, hint, mutedStyle)
	}
	if counter != "" && bw > runeLen(label)+runeLen(counter)+4 {
		// Only draw the counter when there's room; right-align before
		// the hint (or against the bar's right edge if the hint was
		// dropped).
		rightTextStart -= runeLen(counter) + 2
		// Color the counter red when the query has no matches so the
		// user gets immediate negative feedback without having to read
		// the digits.
		style := mutedStyle
		if hasNoMatches {
			style = emptyStyle
		}
		drawAt(a.screen, rightTextStart, by, counter, style)
	}

	// Input field — render the value with horizontal scroll so a long
	// query keeps the cursor visible.
	inputEnd := rightTextStart - 1
	if inputEnd <= inputStart {
		inputEnd = bx + bw - 1
	}
	inputWidth := inputEnd - inputStart
	if inputWidth < 1 {
		inputWidth = 1
	}
	*scroll = scrollWindow(cursor, *scroll, inputWidth)
	for i := 0; i < inputWidth; i++ {
		idx := *scroll + i
		if idx >= len(value) {
			break
		}
		a.screen.SetContent(inputStart+i, by, value[idx], nil, barStyle)
	}

	// Place the screen cursor at the input position so the user sees a
	// blinking caret while typing in this field — only the focused field
	// gets it, so there's exactly one caret on screen even with both
	// rows visible.
	if !focused {
		return
	}
	caret := inputStart + (cursor - *scroll)
	if caret >= inputStart && caret <= inputEnd {
		a.screen.ShowCursor(caret, by)
	}
}

// findCounterText renders the "N of M" indicator. Returns "" when there
// is no query so the renderer can skip drawing the field entirely.
func (a *App) findCounterText() string {
	if len(a.findValue) == 0 {
		return ""
	}
	tab := a.activeTabPtr()
	if tab == nil {
		return ""
	}
	if len(tab.FindMatches) == 0 {
		return "no results"
	}
	return fmt.Sprintf("%d of %d", tab.FindIndex+1, len(tab.FindMatches))
}

// findHasNoMatches reports whether the user has typed a query that
// returned zero hits, so the counter can flip color.
func (a *App) findHasNoMatches() bool {
	if len(a.findValue) == 0 {
		return false
	}
	tab := a.activeTabPtr()
	return tab != nil && len(tab.FindMatches) == 0
}
