// =============================================================================
// File: internal/app/branchpicker.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// Derives from internal/app/rootpicker.go (which derives in turn from
// finder.go, Spicer Matthews / Cloudmanic, MIT): the modal's shape —
// centered box, one-line filter on top, ten rows below, hover follows the
// pointer, Enter activates, Esc closes — plus the draw-time row rects the
// click handler tests against.
// =============================================================================

// branchpicker.go is the Esc b branch switcher: pick a local branch, check
// it out.
//
// Deliberately the root picker's twin rather than a new kind of modal. Both
// answer the same shape of question — "which of these named things am I
// looking at" — and a reviewer who has learned one gesture should not have
// to learn a second. What is different is only what the rows are and what
// Enter does with one.
//
// Three rules worth knowing:
//
//   - THE CURRENT BRANCH IS ROW ZERO, marked. git's for-each-ref sorts by
//     commit date, which usually but not always puts it first; pinning it
//     means the list always opens with a row that says where you are.
//   - DIRTY TABS REFUSE THE PICKER, not the checkout. A checkout rewrites
//     files under an open buffer, and refusing at the moment the user asks
//     for the list is a much clearer place to say so than refusing after
//     they have chosen a branch.
//   - THE RELOAD IS THE POLLER'S. A successful checkout kicks startGitPoll,
//     whose applyGitPoll -> reconcileOpenTabsWithDisk already knows how to
//     reload a clean tab whose file changed on disk, and how to leave every
//     other case alone. A second reload path here would be a second set of
//     those rules to keep in agreement.

package app

import (
	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/finder"
	"github.com/chasereyn/vincent/internal/review"
)

const (
	// branchPickerMaxWidth caps the modal. Narrower than the root
	// picker's 76 because branch names are short — a box wide enough for
	// a filesystem path around "main" reads as an empty room.
	branchPickerMaxWidth = 56
	// branchPickerRowsVisible is how many rows are painted at once. Ten,
	// matching the finder and the root picker.
	branchPickerRowsVisible = 10
	// branchCurrentMark prefixes the checked-out branch. A glyph rather
	// than colour alone, so the row still says "you are here" on a
	// terminal that renders the whole modal in one colour.
	branchCurrentMark = "● "
	// branchOtherMark is the same width in spaces, so every name starts
	// in the same column and the list scans as a column of names.
	branchOtherMark = "  "
)

// branchPickerRow is one drawn row: the branch name, whether it is the one
// checked out, and the rune indexes the fuzzy scorer lit up so the
// renderer can highlight them.
type branchPickerRow struct {
	name    string
	current bool
	matched []int
}

// branchPickerRowRect is the hit-test snapshot for one drawn row — the
// screen Y it landed on and the index into rows it represents. Recorded
// during the draw and tested against by the click handler, per the house
// rule.
type branchPickerRowRect struct {
	y     int
	index int
}

// branchPickerState is the whole of the switcher's UI state, grouped so
// App grows one field instead of eight.
//
// branches is the full list as read from git when the picker opened, and
// rows is that list filtered by the query. Snapshotted rather than re-read
// per keystroke: the branch list cannot change while the modal owns the
// keyboard, and a fork per typed character is a waste.
type branchPickerState struct {
	open bool

	query  []rune
	cursor int
	scroll int // horizontal scroll of the filter field

	branches []branchPickerRow
	rows     []branchPickerRow

	selected   int
	listScroll int

	rowRects []branchPickerRowRect
}

// -----------------------------------------------------------------------------
// Open / close
// -----------------------------------------------------------------------------

// openBranchPicker is the Esc b leader action and the click target on the
// panel's "⑂ repo / branch" footer row.
//
// The branch list is read here, on the UI thread, because it is one fork
// against .git with no network in it — the same call the panel's own
// refresh makes several of. The dirty-tab refusal is the interesting part:
// see the file comment for why it gates the LIST rather than the checkout.
func (a *App) openBranchPicker() {
	if a.tree == nil {
		a.flash("Branches aren't available in single-file mode")
		return
	}
	if !a.gitSnap.IsRepo {
		a.flash("Not a git repository")
		return
	}
	if n := a.dirtyTabCount(); n > 0 {
		a.flash("Save or discard " + dirtyTabCount(n) + " first")
		return
	}
	names, stderr, err := gitBranches(a.gitWriter(), a.rootDir)
	if err != nil {
		review.Logf("git for-each-ref: %v\n%s", err, stderr)
		a.flash(gitFailureSentence("Branch list", stderr, err))
		return
	}
	if len(names) == 0 {
		a.flash("No branches yet — commit something first")
		return
	}

	a.closeAllModals()
	st := &a.branchPicker
	st.open = true
	st.query = nil
	st.cursor = 0
	st.scroll = 0
	st.listScroll = 0
	st.branches = orderBranchRows(names, a.gitSnap.Branch)
	a.refreshBranchPickerRows()
}

// closeBranchPicker dismisses the picker and clears its state.
func (a *App) closeBranchPicker() {
	a.branchPicker = branchPickerState{}
}

// orderBranchRows marks the checked-out branch and moves it to the front,
// leaving git's committerdate order intact behind it.
//
// current is the branch name from the panel's snapshot, which is a short
// SHA when HEAD is detached — that simply matches nothing here, and the
// list then opens with no row marked, which is the honest rendering of
// "you are not on any of these".
func orderBranchRows(names []string, current string) []branchPickerRow {
	out := make([]branchPickerRow, 0, len(names))
	for _, name := range names {
		if name == current {
			out = append(out, branchPickerRow{name: name, current: true})
		}
	}
	for _, name := range names {
		if name != current {
			out = append(out, branchPickerRow{name: name})
		}
	}
	return out
}

// -----------------------------------------------------------------------------
// Rows
// -----------------------------------------------------------------------------

// refreshBranchPickerRows rebuilds the filtered list from the query and
// puts the highlight back on row 0.
//
// The filter is the finder's fuzzy scorer, not a prefix match, for the same
// reason the root picker uses it: "fb" should find "feature/branch-name".
// An empty query keeps the snapshot's order — current branch first, then
// most recently committed — because an empty query scores every row alike
// and there is nothing better to rank by.
func (a *App) refreshBranchPickerRows() {
	st := &a.branchPicker
	st.listScroll = 0
	query := trimSpace(string(st.query))
	if query == "" {
		st.rows = st.branches
		st.selected = 0
		if len(st.rows) == 0 {
			st.selected = rootPickerNoSelection
		}
		return
	}

	type scored struct {
		row   branchPickerRow
		score int
	}
	hits := make([]scored, 0, len(st.branches))
	for _, b := range st.branches {
		score, idx := finder.Score(query, b.name)
		if score == 0 {
			continue
		}
		row := b
		row.matched = idx
		hits = append(hits, scored{row: row, score: score})
	}
	// Insertion sort: the list is a handful of branches, it is stable, so
	// equal scores keep the current-first / recency order underneath.
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j-1].score < hits[j].score; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	st.rows = make([]branchPickerRow, 0, len(hits))
	for _, h := range hits {
		st.rows = append(st.rows, h.row)
	}
	if len(st.rows) == 0 {
		st.selected = rootPickerNoSelection
		return
	}
	st.selected = 0
}

// -----------------------------------------------------------------------------
// Actions
// -----------------------------------------------------------------------------

// branchPickerActivate is Enter (and a click): check out the highlighted
// branch. Nothing highlighted — an empty filter result — is a no-op rather
// than a guess.
func (a *App) branchPickerActivate() {
	st := &a.branchPicker
	if st.selected < 0 || st.selected >= len(st.rows) {
		return
	}
	a.checkoutBranch(st.rows[st.selected].name)
}

// checkoutBranch runs the checkout and, on success, brings the whole UI
// back into agreement with the new working tree.
//
// A failure keeps the modal up with the flash explaining why — the same
// contract pickRoot has, and for the same reason: git refusing because a
// file would be overwritten is something the user can act on, and closing
// the picker would make them reopen it to do so.
//
// The dirty-tab guard is re-checked here because the picker can sit open
// while a background reload marks a tab conflicted, and a checkout over
// unsaved work is the one outcome that loses it.
func (a *App) checkoutBranch(name string) {
	if name == "" {
		return
	}
	if n := a.dirtyTabCount(); n > 0 {
		a.flash("Save or discard " + dirtyTabCount(n) + " first")
		return
	}
	if _, stderr, err := gitCheckout(a.gitWriter(), a.rootDir, name); err != nil {
		review.Logf("git checkout %s: %v\n%s", name, err, stderr)
		a.flash(gitFailureSentence("Checkout", stderr, err))
		return
	}
	a.closeBranchPicker()
	a.flash("Checked out " + name)
	// The panel and the tree have to be right on THIS frame — the branch
	// name in the footer is the thing the user just changed. The open tabs
	// come back on the poll, which is where the reload rules already live.
	a.refreshGitStatus()
	a.refreshTree()
	a.startGitPoll()
}

// -----------------------------------------------------------------------------
// Keyboard
// -----------------------------------------------------------------------------

// handleBranchPickerKey routes keyboard input while the picker owns the
// screen. Esc / Enter / Up / Down are the picker's; everything else goes to
// editRunes, the same single-line field the root picker and the composer
// use.
func (a *App) handleBranchPickerKey(ev *tcell.EventKey) {
	st := &a.branchPicker
	switch ev.Key() {
	case tcell.KeyEsc:
		a.closeBranchPicker()
		return
	case tcell.KeyEnter:
		a.branchPickerActivate()
		return
	case tcell.KeyDown:
		a.moveBranchPickerSelection(1)
		return
	case tcell.KeyUp:
		a.moveBranchPickerSelection(-1)
		return
	}
	before := string(st.query)
	value, cursor, handled := editRunes(st.query, st.cursor, ev)
	if !handled {
		return
	}
	st.query = value
	st.cursor = cursor
	if string(st.query) != before {
		a.refreshBranchPickerRows()
	}
}

// moveBranchPickerSelection walks the highlight by delta and keeps it in
// view. Clamped at both ends rather than wrapping: there is no "pick the
// thing I typed" row here the way browse mode has in the root picker, so
// every row is a real branch and running off the end should stop.
func (a *App) moveBranchPickerSelection(delta int) {
	st := &a.branchPicker
	if len(st.rows) == 0 {
		st.selected = rootPickerNoSelection
		return
	}
	next := st.selected + delta
	if next < 0 {
		next = 0
	}
	if next > len(st.rows)-1 {
		next = len(st.rows) - 1
	}
	st.selected = next
	a.clampBranchPickerScroll()
}

// clampBranchPickerScroll slides the visible window so the highlighted row
// is inside it. A repo with fifty branches is common enough that ten slots
// are not always enough.
func (a *App) clampBranchPickerScroll() {
	st := &a.branchPicker
	if st.selected < 0 {
		st.listScroll = 0
		return
	}
	if st.selected < st.listScroll {
		st.listScroll = st.selected
	}
	if st.selected >= st.listScroll+branchPickerRowsVisible {
		st.listScroll = st.selected - branchPickerRowsVisible + 1
	}
	max := len(st.rows) - branchPickerRowsVisible
	if max < 0 {
		max = 0
	}
	if st.listScroll > max {
		st.listScroll = max
	}
	if st.listScroll < 0 {
		st.listScroll = 0
	}
}

// -----------------------------------------------------------------------------
// Mouse
// -----------------------------------------------------------------------------

// handleBranchPickerMouse routes mouse input while the picker is open:
// hover follows the pointer, a click on a row checks it out, the wheel
// scrolls the list, and a click outside dismisses.
//
// Every hit test reads the rects recorded during the draw; nothing here
// recomputes a row's Y.
func (a *App) handleBranchPickerMouse(x, y int, btn tcell.ButtonMask) {
	st := &a.branchPicker
	mx, my, mw, mh := a.branchPickerModalRect()
	inside := x >= mx && x < mx+mw && y >= my && y < my+mh

	if btn&tcell.WheelUp != 0 {
		a.scrollBranchPickerList(-1)
		return
	}
	if btn&tcell.WheelDown != 0 {
		a.scrollBranchPickerList(1)
		return
	}

	if idx, ok := a.branchPickerRowAt(x, y); ok {
		st.selected = idx
	}
	if btn&tcell.Button1 == 0 {
		return
	}
	if !inside {
		a.closeBranchPicker()
		return
	}
	if idx, ok := a.branchPickerRowAt(x, y); ok {
		st.selected = idx
		a.branchPickerActivate()
	}
}

// branchPickerRowAt maps a screen point to a row index using the draw-time
// snapshot. ok is false for anything that isn't a drawn row.
func (a *App) branchPickerRowAt(x, y int) (int, bool) {
	mx, _, mw, _ := a.branchPickerModalRect()
	if x < mx || x >= mx+mw {
		return 0, false
	}
	for _, r := range a.branchPicker.rowRects {
		if r.y == y {
			return r.index, true
		}
	}
	return 0, false
}

// scrollBranchPickerList moves the visible window by delta rows without
// moving the highlight — the wheel is for looking, the click is for
// choosing. Same split the root picker makes.
func (a *App) scrollBranchPickerList(delta int) {
	st := &a.branchPicker
	max := len(st.rows) - branchPickerRowsVisible
	if max < 0 {
		max = 0
	}
	st.listScroll += delta
	if st.listScroll > max {
		st.listScroll = max
	}
	if st.listScroll < 0 {
		st.listScroll = 0
	}
}

// -----------------------------------------------------------------------------
// Draw
// -----------------------------------------------------------------------------

// branchPickerModalRect returns the modal's on-screen rectangle: the root
// picker's proportions and upper-third anchor, so the three pickers land in
// the same place.
//
// Layout: 1 border + 1 title + 1 divider + 1 input + N rows + 1 footer +
// 1 border = N+6 rows.
func (a *App) branchPickerModalRect() (x, y, w, h int) {
	w = branchPickerMaxWidth
	if w > a.width-4 {
		w = a.width - 4
	}
	if w < 30 {
		w = 30
	}
	h = branchPickerRowsVisible + 6
	if h > a.height-2 {
		h = a.height - 2
	}
	x = (a.width - w) / 2
	y = (a.height - h) / 3
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return
}

// drawBranchPicker paints the modal and records the hit-test rects the
// click handler reads.
//
// Layout (relY):
//
//	0     top border
//	1     title — "Switch branch" + "esc"
//	2     divider
//	3     filter input
//	4..N  rows
//	N+1   footer hint
//	N+2   bottom border
func (a *App) drawBranchPicker() {
	st := &a.branchPicker
	st.rowRects = nil

	mx, my, mw, mh := a.branchPickerModalRect()
	bg := a.theme.LineHL
	bgStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Text)
	borderStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Subtle)
	titleStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Accent).Bold(true)
	mutedStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Muted)
	hitStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.FindCurrent).Bold(true)

	fillRect(a.screen, mx, my, mw, mh, bgStyle)
	drawBorder(a.screen, mx, my, mw, mh, borderStyle)
	drawHDivider(a.screen, mx, my+2, mw, borderStyle)

	drawAt(a.screen, mx+1, my+1, " Switch branch", titleStyle)
	hint := "esc "
	drawAt(a.screen, mx+mw-1-runeLen(hint), my+1, hint, mutedStyle)

	// Filter input — the finder's field, including its horizontal scroll.
	inputStyle := tcell.StyleDefault.Background(a.theme.BG).Foreground(a.theme.Text)
	fieldStart := mx + 3
	fieldEnd := mx + mw - 4
	fieldWidth := fieldEnd - fieldStart
	st.scroll = scrollWindow(st.cursor, st.scroll, fieldWidth)
	for cx := fieldStart - 1; cx <= fieldEnd; cx++ {
		a.screen.SetContent(cx, my+3, ' ', nil, inputStyle)
	}
	for i := 0; i < fieldWidth; i++ {
		idx := st.scroll + i
		if idx >= len(st.query) {
			break
		}
		a.screen.SetContent(fieldStart+i, my+3, st.query[idx], nil, inputStyle)
	}
	caret := fieldStart + (st.cursor - st.scroll)
	if caret >= fieldStart && caret <= fieldEnd {
		a.screen.ShowCursor(caret, my+3)
	}

	rowsStart := my + 4
	rowsCap := mh - 6
	if rowsCap > branchPickerRowsVisible {
		rowsCap = branchPickerRowsVisible
	}
	for i := 0; i < rowsCap; i++ {
		ry := rowsStart + i
		idx := st.listScroll + i
		if idx >= len(st.rows) {
			for cx := mx + 1; cx < mx+mw-1; cx++ {
				a.screen.SetContent(cx, ry, ' ', nil, bgStyle)
			}
			continue
		}
		st.rowRects = append(st.rowRects, branchPickerRowRect{y: ry, index: idx})
		a.drawBranchPickerRow(mx, ry, mw, st.rows[idx], idx == st.selected, hitStyle, bg)
	}

	footer := " enter checks out · type to filter"
	if len(st.rows) == 0 {
		footer = " no branch matches that"
	}
	for cx := mx + 1; cx < mx+mw-1; cx++ {
		a.screen.SetContent(cx, my+mh-2, ' ', nil, bgStyle)
	}
	drawAt(a.screen, mx+1, my+mh-2, truncateRight(footer, mw-2), mutedStyle)
}

// drawBranchPickerRow paints one row: the current-branch mark, the name,
// and the scorer's matched runes lit.
//
// Clipped from the right, unlike the root picker's rows. A branch name's
// identifying part is its start ("feature/…"), where a path's is its end —
// which is exactly why the two pickers differ here rather than sharing one
// row painter.
func (a *App) drawBranchPickerRow(mx, ry, mw int, row branchPickerRow, selected bool, hitStyle tcell.Style, modalBG tcell.Color) {
	rowBG := modalBG
	if selected {
		rowBG = a.theme.BG
	}
	rowStyle := tcell.StyleDefault.Background(rowBG).Foreground(a.theme.Text)
	hitOnRow := hitStyle.Background(rowBG)
	markStyle := tcell.StyleDefault.Background(rowBG).Foreground(a.theme.Accent)

	for cx := mx + 1; cx < mx+mw-1; cx++ {
		a.screen.SetContent(cx, ry, ' ', nil, rowStyle)
	}

	startCol := mx + 2
	maxCols := mw - 4
	if maxCols <= 0 {
		return
	}
	mark := branchOtherMark
	base := rowStyle
	if row.current {
		mark = branchCurrentMark
		// The checked-out branch is the accent everywhere else in Vincent
		// (the panel footer draws it that way), so it is the accent here.
		base = markStyle
	}
	used := drawClipped(a.screen, startCol, ry, maxCols, mark, markStyle)

	matchSet := make(map[int]bool, len(row.matched))
	for _, m := range row.matched {
		matchSet[m] = true
	}
	// Rune-indexed rather than byte-indexed: finder.Score reports matched
	// RUNE positions, and a branch name with a non-ASCII character would
	// otherwise light the wrong cells and clip mid-sequence.
	for i, ch := range []rune(row.name) {
		if used+i >= maxCols {
			break
		}
		st := base
		if matchSet[i] {
			st = hitOnRow
		}
		a.screen.SetContent(startCol+used+i, ry, ch, nil, st)
	}
}
