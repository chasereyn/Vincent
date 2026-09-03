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
// no Stage All. What Zed puts at the bottom of the panel is "describe this
// change and commit it"; Vincent's footer is where phase 2's "describe this
// change and hand it back to the agent" goes. Same shape, same muscle
// memory, opposite direction.
//
// Phase 3b added Zed's commit box back, stacked ABOVE the review block and
// armed by Esc c (see commitbox.go), and made the repo / branch row open
// the branch picker (branchpicker.go). Those are the only writes this panel
// reaches: still no staging, still no partial commits, still no checkbox.
// The list itself is a navigator — every row is a way to reach a diff.

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
	// gitPanelBranchRows is the fixed part of the footer block: a rule
	// and the repo / branch row. The review block below it is variable,
	// so the total is gitPanelFooterH() rather than a constant.
	gitPanelBranchRows = 2
	// gitPanelIndent is how far a file row is inset from the section header
	// above it, which is what makes the grouping readable at a glance.
	gitPanelIndent = 2
)

// gitPanelItem is one rendered row of the panel's scrollable middle. A row
// is either a section header or a file; flattening both into one list means
// scrolling and hit-testing each handle a single sequence rather than
// walking two sections and reasoning about the boundary.
type gitPanelItem struct {
	// repoHeader is non-empty on a "⑂ name / branch" row, which groups the
	// rows below it by repository. Drawn only when the root holds more than
	// one repo — with a single repo the panel has exactly the shape it had
	// before phase 8a, and a header restating the footer would be noise.
	repoHeader string

	section string    // non-empty: a section header row
	entry   *gitEntry // non-nil: a clickable file row
	// A zero gitPanelItem is a blank spacer row: nothing drawn, nothing
	// clickable. gitPanelRepoItems puts one between repo groups.
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
//
// Two shapes, one list. With a single repository it is Tracked then
// Untracked, exactly as it always was. With a folder of repos each repo
// that HAS changes contributes a "⑂ name / branch" header followed by its
// own two sections; a clean repo is not listed at all, because a review
// panel listing five repos with nothing in them buries the one with work
// in it.
func (a *App) gitPanelItems() []gitPanelItem {
	if len(a.gitSnap.Repos) > 1 {
		return a.gitPanelRepoItems()
	}
	items := []gitPanelItem{}
	items = appendGitPanelSections(items, a.gitSnap)
	return items
}

// gitPanelRepoItems is gitPanelItems' multi-repo shape.
func (a *App) gitPanelRepoItems() []gitPanelItem {
	items := []gitPanelItem{}
	for _, snap := range a.gitSnap.Repos {
		if len(snap.Entries) == 0 {
			continue
		}
		if len(items) > 0 {
			// A blank row between repos. Without it the second repo's
			// header sits directly under the first repo's last file and
			// the groups read as one list; Chase asked for the gap on
			// 2026-09-03 after seeing two repos stacked.
			items = append(items, gitPanelItem{})
		}
		items = append(items, gitPanelItem{repoHeader: gitPanelRepoLabel(snap)})
		items = appendGitPanelSections(items, snap)
	}
	return items
}

// gitPanelRepoLabel renders one repo's header text. Same "⑂ name / branch"
// form as the footer's branch row, so the two read as the same fact about
// the same thing.
func gitPanelRepoLabel(snap gitSnapshot) string {
	label := "⑂ " + snap.RepoName
	if snap.Branch != "" {
		label += " / " + snap.Branch
	}
	return label
}

// appendGitPanelSections appends one snapshot's Tracked and Untracked
// sections to items. Shared by both shapes so a single repo's rows are
// built by exactly the same code whether or not a header sits above them.
//
// Tracked() and Untracked() each return a fresh slice, so taking the
// address of an element is safe — the backing array outlives the loop.
func appendGitPanelSections(items []gitPanelItem, snap gitSnapshot) []gitPanelItem {
	add := func(label string, entries []gitEntry) {
		if len(entries) == 0 {
			return
		}
		items = append(items, gitPanelItem{section: label})
		for i := range entries {
			items = append(items, gitPanelItem{entry: &entries[i]})
		}
	}
	add("Tracked", snap.Tracked())
	add("Untracked", snap.Untracked())
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
	if a.width <= 0 {
		// No size yet. New() calls this through applyStartupPanelDefaults
		// before Run() has read the screen, and clamping against a zero
		// width collapses both panes to their minimums — which is exactly
		// what 0.6.1 shipped doing. The first EventResize reflows for real.
		return
	}
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
	// The same snapshot decides which review notes have gone stale. This
	// is the only place that knows what the changeset currently is, and
	// running a second status just for the notes is how the two would
	// drift. See markStaleComments — it flags, it never re-anchors.
	a.markStaleComments(snap)
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
	h -= gitPanelHeaderRows + a.gitPanelFooterH()
	if h < 0 {
		return 0
	}
	return h
}

// -----------------------------------------------------------------------------
// Actions
// -----------------------------------------------------------------------------

// applyStartupPanelDefaults decides whether the Changes panel is open when
// Vincent starts, and re-clamps the layout for whichever answer it gives.
//
// Open, in a repository. Vincent exists to answer "what did the agent just
// do", and putting that behind a keypress makes the first question of every
// session a navigation problem — Zed ships its panel open for the same
// reason. Outside a repository there is nothing to put in it, and spending
// a third of the window to say "Not a git repository" is worse than saying
// nothing; `Esc g` still shows that state on demand.
//
// Split out of New so it can be tested: New builds a real tcell screen and
// cannot run under `go test`.
//
// A ONE-SHOT, guarded by startupPanelDone — the same shape as
// clampStartupSidebar, and for the same reason. setRoot used to call this,
// which meant switching folder re-opened a panel the user had deliberately
// closed: the default is an answer to "what should a session START like",
// not to "what should a session look like from now on". setRoot now
// re-clamps the layout and leaves the panel's state alone; the guard is
// here so a future caller cannot reintroduce the bug from a different
// direction.
func (a *App) applyStartupPanelDefaults() {
	if a.startupPanelDone {
		return
	}
	a.startupPanelDone = true
	a.gitPanelShown = a.gitSnap.IsRepo
	a.reflowPanels()
}

// menuToggleGitPanel shows or hides the Changes panel.
//
// Hiding it also drops the commit box's focus. The box is drawn inside this
// panel, so leaving it armed under a hidden panel would leave the keyboard
// pointed at a field nobody can see — every keystroke swallowed, with no
// caret to explain why. The typed message survives the close (see
// closeCommitBox), so Esc g twice then Esc c brings it back.
func (a *App) menuToggleGitPanel() {
	a.gitPanelShown = !a.gitPanelShown
	if !a.gitPanelShown {
		a.closeCommitBox()
		a.lastCommitBox = commitBoxHit{}
	}
	if a.gitPanelShown {
		a.reflowPanels()
		// Refresh on open rather than showing whatever the last tick saw.
		// Opening the panel is a deliberate "what changed?" gesture and
		// deserves a current answer.
		a.refreshGitStatus()
	}
	a.gitPanelHover = -1
}

// gitPanelToggleLabel returns "Hide changes panel" or "Show changes
// panel" for the current state. Callerless since the ≡ menu went away —
// see sidebarToggleLabel in app.go for why the three of them stay.
func (a *App) gitPanelToggleLabel() string {
	if a.gitPanelShown {
		return "Hide changes panel"
	}
	return "Show changes panel"
}

// gitPanelClick dispatches a click in the panel: the commit box's field,
// the branch row, the review block, or a file row — in that order, tested
// against the rects recorded during the last draw.
//
// The order is a formality rather than a tie-break: every one of these
// records its own rows in the same draw pass, so no two of them can claim
// the same y. It is written out longhand anyway, because the day the footer
// grows another row that assumption is the one that breaks.
func (a *App) gitPanelClick(x, y int) {
	if a.commitBoxClick(x, y) {
		return
	}
	if a.branchRowClick(y) {
		return
	}
	if a.reviewPanelClick(y) {
		return
	}
	for _, r := range a.lastGitPanelRows {
		if r.y == y {
			// Remember which repo the row belonged to BEFORE opening the
			// diff. It is activeRepo's third rule, and it is what keeps
			// the footer and the writes pointed at the repo the reviewer
			// last chose even after they close the tab it opened.
			a.setGitPanelRepo(r.entry.Repo)
			a.openDiff(r.entry.Abs)
			return
		}
	}
}

// branchRowClick opens the branch picker when the click landed on the
// footer's "⑂ repo / branch" row, and reports whether it did.
//
// Zed makes that row its branch switcher, so Vincent does too — and it is
// the mouse-first path to Esc b, which the house rule requires: no action
// may live behind only one of the two.
func (a *App) branchRowClick(y int) bool {
	if a.lastBranchRowY < 0 || y != a.lastBranchRowY {
		return false
	}
	a.openBranchPicker()
	return true
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
	a.drawGitPanelFooter(x, y+h-a.gitPanelFooterH(), w)
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

		if item.repoHeader != "" {
			drawClipped(a.screen, x+1, cy, w-1, item.repoHeader, base.Foreground(th.Accent))
			continue
		}
		if item.section != "" {
			drawClipped(a.screen, x+1, cy, w-1, item.section, base.Foreground(th.Muted))
			continue
		}
		if item.entry == nil {
			continue // spacer row between repo groups
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
		drawClipped(a.screen, x+1, y+h-1, w-1, "⋯ more", base.Foreground(th.DimText))
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
	drawClipped(a.screen, startX+used+1, cy, rest, truncateLeft(e.Dir, rest), base.Foreground(th.DimText))
}

// gitPanelFooterH is the height of the whole footer block: the rule and
// branch row, the commit box when it is armed, plus however many rows the
// review block needs.
//
// A method rather than a constant because both the commit box and the
// review block grow into it. Both the list height and the footer's origin
// go through it, so the two can never disagree about where the boundary
// between them is.
//
// The result is clamped so the footer always leaves the header and at least
// one list row standing. Without that, a long review in a short terminal
// would push the footer's origin above the header and paint over it — the
// Changes list is the reason the panel exists, and the review block must
// never be able to evict it entirely.
func (a *App) gitPanelFooterH() int {
	want := gitPanelBranchRows + a.commitBoxRows() + a.reviewBlockRows()
	_, _, _, h := a.gitPanelRect()
	if h <= 0 {
		return want
	}
	max := h - gitPanelHeaderRows - 1
	if max < gitPanelBranchRows {
		max = gitPanelBranchRows
	}
	if want > max {
		return max
	}
	return want
}

// drawGitPanelFooter draws the rule, the repo / branch row, the commit box
// when it is armed, and the review block beneath both.
//
// The review block is where Zed's commit message box sits. That is the
// substitution the whole panel was built around: Zed's footer ends in
// "describe this change and commit it", Vincent's ends in "describe this
// change and hand it back". Same shape, same muscle memory, opposite
// direction. See review.go for the block itself.
//
// Phase 3b put Zed's commit box back — ABOVE the review block, not instead
// of it. The two questions are both live in a review session ("hand this
// back to the agent" and "commit the typo I fixed myself"), and making the
// footer pick one would mean a keypress that silently hides the other.
// Stacking costs the Changes list three rows while the box is armed, which
// gitPanelFooterH already accounts for.
//
// The branch row records where it landed so a click on it can open the
// branch picker. That is the row Zed makes a branch switcher, and it is the
// mouse-first path to Esc b.
func (a *App) drawGitPanelFooter(x, y, w int) {
	th := a.theme
	base := tcell.StyleDefault.Background(th.SidebarBG)
	drawRule(a.screen, x, y, w, base.Foreground(th.Subtle))

	a.lastBranchRowY = -1
	if a.gitSnap.IsRepo {
		// The ACTIVE repo, not the root: in a folder-of-repos root this row
		// is the answer to "where would a commit go", and the three writes
		// resolve that the same way (activeRepo). A row naming the folder
		// while the commit lands in one repo inside it is the specific
		// wrongness this replaced.
		label := a.activeRepoName()
		if branch := a.branchLabel(); branch != "" {
			label += " / " + branch
		}
		drawClipped(a.screen, x+1, y+1, w-1, "⑂ "+label, base.Foreground(th.Accent))
		a.lastBranchRowY = y + 1
	}

	// The commit box takes its rows off the top of what is left, and tells
	// us how many it actually used — which is fewer than it wants in a
	// terminal too short for the whole footer. The review block starts
	// wherever the box stopped, so neither has to know the other's height.
	rest := a.gitPanelFooterH() - gitPanelBranchRows
	used := a.drawCommitBox(x, y+gitPanelBranchRows, w, rest)
	a.drawGitPanelReview(x, y+gitPanelBranchRows+used, w, rest-used)
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
