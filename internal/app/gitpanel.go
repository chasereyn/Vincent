// =============================================================================
// File: internal/app/gitpanel.go
// Author: Chase Reynolds
// Created: 2026-08-15
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// gitpanel.go draws the Changes panel down the right-hand side: a
// `Changes (N)` header, Tracked and Untracked sections, one row per changed
// file, and a repo / branch footer. Clicking a row opens that file's diff.
//
// The shape is Zed's, transcribed from a side-by-side screenshot (see
// CLAUDE.md), with the write-side controls removed — no staging checkboxes,
// no Stage All, no commit box. What Zed puts at the bottom of the panel is
// "describe this change and commit it"; Vincent's footer is where phase 2's
// "describe this change and hand it back to the agent" goes. Same shape,
// same muscle memory, opposite direction.
//
// The panel is a navigator, not a stager. Every row is a way to reach a
// diff; nothing in here mutates a repository.

package app

import (
	"fmt"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/filetree"
	"github.com/chasereyn/vincent/internal/theme"
)

const (
	// gitPanelHeaderRows is the header block: the title and its rule.
	gitPanelHeaderRows = 2
	// gitPanelFooterRows is the footer block: a rule and the branch row.
	gitPanelFooterRows = 2
	// gitPanelIndent is how far a file row is inset from the section header
	// above it, which is what makes the grouping readable at a glance.
	gitPanelIndent = 2
)

// gitPanelItem is one rendered row of the panel's scrollable middle. A row
// is either a section header or a file; flattening both into one list means
// scrolling and hit-testing each handle a single sequence rather than
// walking two sections and reasoning about the boundary.
type gitPanelItem struct {
	section string    // non-empty: a section header row
	entry   *gitEntry // non-nil: a clickable file row
}

// gitPanelRowRect records where a file row was actually drawn, so the next
// click is tested against real geometry rather than against row arithmetic
// recomputed from scratch. Deriving the layout twice — once to draw, once to
// click — is how mouse handling silently drifts out of alignment.
type gitPanelRowRect struct {
	y     int
	entry gitEntry
}

// gitPanelItems flattens the current snapshot into rendered rows.
func (a *App) gitPanelItems() []gitPanelItem {
	items := []gitPanelItem{}
	add := func(label string, entries []gitEntry) {
		if len(entries) == 0 {
			return
		}
		items = append(items, gitPanelItem{section: label})
		for i := range entries {
			items = append(items, gitPanelItem{entry: &entries[i]})
		}
	}
	tracked := a.gitSnap.Tracked()
	untracked := a.gitSnap.Untracked()
	add("Tracked", tracked)
	add("Untracked", untracked)
	return items
}

// -----------------------------------------------------------------------------
// Layout
// -----------------------------------------------------------------------------

// gitPanelW is the effective width of the panel block (the panel plus its
// splitter column), or zero when hidden. Every layout helper goes through
// this so toggling the panel reshapes the whole UI in one place — the same
// contract sidebarW has on the other side.
func (a *App) gitPanelW() int {
	if !a.gitPanelShown {
		return 0
	}
	return a.gitPanelWidth
}

// gitSplitterX is the x of the panel's resize splitter — its leftmost
// column — or -1 when the panel is hidden.
func (a *App) gitSplitterX() int {
	if !a.gitPanelShown {
		return -1
	}
	return a.width - a.gitPanelWidth
}

// gitPanelRect is the panel's content rectangle, one column narrower than
// the block because the leftmost column is the splitter. Full height minus
// the status bar, matching Zed — the panel is a sibling of the whole
// editor area, not of the editor body.
func (a *App) gitPanelRect() (x, y, w, h int) {
	gw := a.gitPanelW()
	if gw <= 0 {
		return 0, 0, 0, 0
	}
	return a.width - gw + 1, 0, gw - 1, a.height - 1
}

// resizeGitPanel applies a desired panel width, clamped so the panel stays
// readable and the editor keeps minEditorAfterDrag columns. The sidebar's
// current width is part of the budget, so dragging one panel can never
// squeeze the editor out from under the other.
func (a *App) resizeGitPanel(target int) {
	if target < minGitPanelWidth {
		target = minGitPanelWidth
	}
	max := a.width - minEditorAfterDrag - a.sidebarW()
	if max < minGitPanelWidth {
		max = minGitPanelWidth
	}
	if target > max {
		target = max
	}
	a.gitPanelWidth = target
}

// reflowPanels shrinks the two side panels until the editor has room again.
// Called after a terminal resize: without it, dragging a window narrow
// leaves both panels at their old widths and the editor at zero or negative
// width, which is a crash waiting to happen in every rect consumer.
//
// The git panel yields first. The file tree is how you navigate; the
// Changes list is a convenience you can toggle back with one key.
func (a *App) reflowPanels() {
	if a.gitPanelShown {
		a.resizeGitPanel(a.gitPanelWidth)
	}
	a.resizeSidebar(a.sidebarWidth)
}

// -----------------------------------------------------------------------------
// Data
// -----------------------------------------------------------------------------

// refreshGitPanel re-reads the repo snapshot behind the panel. Driven by
// the same 10-second tick that refreshes the tree, so an agent working in
// the repo shows up in the Changes list without the user doing anything.
func (a *App) refreshGitPanel(snap gitSnapshot) {
	a.gitSnap = snap
	a.clampGitPanelScroll()
}

// clampGitPanelScroll keeps the scroll offset inside the current list.
// Needed after a refresh as well as after a scroll: a file going clean
// shortens the list under a scrolled-down panel.
func (a *App) clampGitPanelScroll() {
	max := len(a.gitPanelItems()) - a.gitPanelListH()
	if max < 0 {
		max = 0
	}
	if a.gitPanelScroll > max {
		a.gitPanelScroll = max
	}
	if a.gitPanelScroll < 0 {
		a.gitPanelScroll = 0
	}
}

// gitPanelListH is the height of the scrollable middle, between the header
// block and the footer block.
func (a *App) gitPanelListH() int {
	_, _, _, h := a.gitPanelRect()
	h -= gitPanelHeaderRows + gitPanelFooterRows
	if h < 0 {
		return 0
	}
	return h
}

// -----------------------------------------------------------------------------
// Actions
// -----------------------------------------------------------------------------

// menuToggleGitPanel shows or hides the Changes panel.
func (a *App) menuToggleGitPanel() {
	a.closeMenu()
	a.gitPanelShown = !a.gitPanelShown
	if a.gitPanelShown {
		a.reflowPanels()
		// Refresh on open rather than showing whatever the last tick saw.
		// Opening the panel is a deliberate "what changed?" gesture and
		// deserves a current answer.
		a.refreshGitStatus()
	}
	a.gitPanelHover = -1
}

// gitPanelToggleLabel is the dynamic menu label for the toggle row.
func (a *App) gitPanelToggleLabel() string {
	if a.gitPanelShown {
		return "Hide changes panel"
	}
	return "Show changes panel"
}

// gitPanelClick opens the diff for the row at (x, y), if there is one.
// Tested against the rects recorded during the last draw.
func (a *App) gitPanelClick(_, y int) {
	for _, r := range a.lastGitPanelRows {
		if r.y == y {
			a.openDiff(r.entry.Abs)
			return
		}
	}
}

// updateGitPanelHover sets the hovered row from a mouse position, or clears
// it when the pointer is outside the panel. Hover is deliberately subtle —
// it says "this is clickable", not "this is selected".
func (a *App) updateGitPanelHover(x, y int) {
	a.gitPanelHover = -1
	px, _, pw, _ := a.gitPanelRect()
	if pw <= 0 || x < px || x >= px+pw {
		return
	}
	for _, r := range a.lastGitPanelRows {
		if r.y == y {
			a.gitPanelHover = y
			return
		}
	}
}

// scrollGitPanel moves the Changes list by delta rows.
func (a *App) scrollGitPanel(delta int) {
	a.gitPanelScroll += delta
	a.clampGitPanelScroll()
}

// -----------------------------------------------------------------------------
// Drawing
// -----------------------------------------------------------------------------

// drawGitPanel paints the Changes panel and records the row rects the next
// click will be tested against.
func (a *App) drawGitPanel() {
	x, y, w, h := a.gitPanelRect()
	if w <= 0 || h <= 0 {
		return
	}
	th := a.theme
	base := tcell.StyleDefault.Background(th.SidebarBG).Foreground(th.Text)
	for cy := y; cy < y+h; cy++ {
		for cx := x; cx < x+w; cx++ {
			a.screen.SetContent(cx, cy, ' ', nil, base)
		}
	}

	a.lastGitPanelRows = a.lastGitPanelRows[:0]

	a.drawGitPanelHeader(x, y, w)
	a.drawGitPanelList(x, y+gitPanelHeaderRows, w, a.gitPanelListH())
	a.drawGitPanelFooter(x, y+h-gitPanelFooterRows, w)
}

// drawGitPanelHeader draws the title row and the rule beneath it. The count
// is of files, not of rendered rows — section headers are chrome.
func (a *App) drawGitPanelHeader(x, y, w int) {
	th := a.theme
	base := tcell.StyleDefault.Background(th.SidebarBG)

	title := "No repository"
	style := base.Foreground(th.Muted)
	if a.gitSnap.IsRepo {
		title = fmt.Sprintf("Changes (%d)", len(a.gitSnap.Entries))
		style = base.Foreground(th.Accent).Bold(true)
	}
	drawClipped(a.screen, x+1, y, w-1, title, style)
	drawRule(a.screen, x, y+1, w, base.Foreground(th.Subtle))
}

// drawGitPanelList draws the scrollable middle: section headers and file
// rows, or an empty-state line when there is nothing to review.
func (a *App) drawGitPanelList(x, y, w, h int) {
	th := a.theme
	base := tcell.StyleDefault.Background(th.SidebarBG)
	items := a.gitPanelItems()

	if len(items) == 0 {
		msg := "No changes"
		if !a.gitSnap.IsRepo {
			msg = "Not a git repository"
		}
		drawClipped(a.screen, x+1, y, w-1, msg, base.Foreground(th.Muted))
		return
	}

	for row := 0; row < h; row++ {
		idx := a.gitPanelScroll + row
		if idx >= len(items) {
			break
		}
		cy := y + row
		item := items[idx]

		if item.section != "" {
			drawClipped(a.screen, x+1, cy, w-1, item.section, base.Foreground(th.Muted))
			continue
		}
		a.drawGitPanelRow(x, cy, w, *item.entry)
		// Record the row where it was actually drawn. Clicks are tested
		// against this snapshot rather than against recomputed arithmetic,
		// so the click target can never drift from the paint.
		a.lastGitPanelRows = append(a.lastGitPanelRows, gitPanelRowRect{y: cy, entry: *item.entry})
	}

	// Overflow affordance. There is no scrollbar, so without this a list
	// that continues below the fold looks like the whole story — and "the
	// whole story" is exactly the claim a review tool must not make falsely.
	if a.gitPanelScroll+h < len(items) {
		drawClipped(a.screen, x+1, y+h-1, w-1, "⋯ more", base.Foreground(th.Subtle))
	}
}

// drawGitPanelRow draws one file: its name in the status colour, then the
// parent directory dimmed beside it. The parent is what tells two files
// called index.ts apart, so it earns its space even in a narrow panel.
func (a *App) drawGitPanelRow(x, cy, w int, e gitEntry) {
	th := a.theme
	bg := th.SidebarBG
	if a.gitPanelHover == cy {
		bg = th.LineHL
	}
	base := tcell.StyleDefault.Background(bg)
	for cx := x; cx < x+w; cx++ {
		a.screen.SetContent(cx, cy, ' ', nil, base)
	}

	nameStyle := base.Foreground(gitKindColor(th, e.Kind))
	if e.Deleted {
		// Struck through rather than merely dim: a deleted file is a
		// different kind of fact from a modified one, and the strike reads
		// instantly even in peripheral vision. Terminals that don't support
		// it still get the deletion colour.
		nameStyle = nameStyle.StrikeThrough(true)
	}

	startX := x + gitPanelIndent
	avail := w - gitPanelIndent - 1
	if avail <= 0 {
		return
	}
	used := drawClipped(a.screen, startX, cy, avail, e.Name, nameStyle)

	// The parent directory gets whatever is left, truncated from the LEFT
	// so the innermost — most identifying — segment survives. "…/agents"
	// tells you more than "\.claude/ag".
	if e.Dir == "" {
		return
	}
	rest := avail - used - 1
	if rest < 4 {
		return
	}
	drawClipped(a.screen, startX+used+1, cy, rest, truncateLeft(e.Dir, rest), base.Foreground(th.Subtle))
}

// drawGitPanelFooter draws the rule and the repo / branch row. This is
// where phase 2's review-batch box lands, in place of Zed's commit box.
func (a *App) drawGitPanelFooter(x, y, w int) {
	th := a.theme
	base := tcell.StyleDefault.Background(th.SidebarBG)
	drawRule(a.screen, x, y, w, base.Foreground(th.Subtle))

	if !a.gitSnap.IsRepo {
		return
	}
	label := a.gitSnap.RepoName
	if a.gitSnap.Branch != "" {
		label += " / " + a.gitSnap.Branch
	}
	drawClipped(a.screen, x+1, y+1, w-1, "⑂ "+label, base.Foreground(th.Accent))
}

// -----------------------------------------------------------------------------
// Small drawing helpers
// -----------------------------------------------------------------------------

// gitKindColor maps a change kind to its theme colour. Deliberately the
// same mapping the file tree uses, so a file is the same colour in the tree
// and in the panel — two colours for one fact would be a bug you only
// notice after trusting the wrong one.
func gitKindColor(th theme.Theme, kind filetree.GitChangeKind) tcell.Color {
	switch kind {
	case filetree.GitChangeAdded:
		return th.GitAdded
	case filetree.GitChangeDeleted:
		return th.GitDeleted
	case filetree.GitChangeRenamed:
		return th.GitRenamed
	case filetree.GitChangeMixed:
		return th.GitMixed
	default:
		return th.GitModified
	}
}

// drawRule fills a row with the box-drawing horizontal line.
func drawRule(scr tcell.Screen, x, y, w int, style tcell.Style) {
	for cx := x; cx < x+w; cx++ {
		scr.SetContent(cx, y, '─', nil, style)
	}
}

// drawClipped writes s at (x, y), stopping at maxW cells, and returns how
// many cells it actually used. Rune-indexed rather than byte-indexed so a
// non-ASCII path clips at a character boundary instead of mid-sequence.
func drawClipped(scr tcell.Screen, x, y, maxW int, s string, style tcell.Style) int {
	if maxW <= 0 {
		return 0
	}
	used := 0
	for _, r := range s {
		if used >= maxW {
			break
		}
		scr.SetContent(x+used, y, r, nil, style)
		used++
	}
	return used
}

// truncateLeft shortens s to maxW cells by dropping leading characters and
// prefixing an ellipsis, so the tail — the part that identifies the
// directory — is what survives.
func truncateLeft(s string, maxW int) string {
	runes := []rune(s)
	if len(runes) <= maxW {
		return s
	}
	if maxW <= 1 {
		return "…"
	}
	return "…" + string(runes[len(runes)-(maxW-1):])
}
