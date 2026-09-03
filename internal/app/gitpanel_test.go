// =============================================================================
// File: internal/app/gitpanel_test.go
// Author: Chase Reynolds
// Created: 2026-08-15
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

// panelApp builds an App with the Changes panel open over a repo holding a
// modified tracked file, a deleted tracked file, and an untracked one —
// enough to exercise both sections and the strikethrough.
func panelApp(t *testing.T) (string, *App) {
	t.Helper()
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "modified.txt"), "one\n")
	writeFileT(t, filepath.Join(dir, "removed.txt"), "two\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed")

	writeFileT(t, filepath.Join(dir, "modified.txt"), "changed\n")
	gitRun(t, dir, "rm", "-q", "removed.txt")
	writeFileT(t, filepath.Join(dir, "fresh.txt"), "new\n")

	a := newTestApp(t, dir)
	a.gitPanelShown = true
	a.refreshGitStatus()
	return dir, a
}

// panelRowText reads a rendered screen row back as a string.
func panelRowText(t *testing.T, a *App, row int) string {
	t.Helper()
	scr := a.screen.(tcell.SimulationScreen)
	cells, w, _ := scr.GetContents()
	var b strings.Builder
	for x := 0; x < w; x++ {
		c := cells[row*w+x]
		if len(c.Runes) == 0 || c.Runes[0] == 0 {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(c.Runes[0])
	}
	return b.String()
}

// TestGitPanel_LayoutLeavesRoomForEveryone is the invariant a third column
// puts at risk. The sidebar, editor, and panel must tile the width exactly
// — an off-by-one here means the editor draws under the panel and the last
// column of code is invisible.
func TestGitPanel_LayoutTilesTheWidth(t *testing.T) {
	_, a := panelApp(t)

	_, _, sw, _ := a.sidebarRect()
	ex, _, ew, _ := a.editorRect()
	px, _, pw, _ := a.gitPanelRect()

	// sidebar + its splitter + editor + panel splitter + panel == width.
	total := sw + 1 + ew + 1 + pw
	if total != a.width {
		t.Errorf("sidebar %d + splitter + editor %d + splitter + panel %d = %d, want width %d",
			sw, ew, pw, total, a.width)
	}
	if ex+ew != a.gitSplitterX() {
		t.Errorf("editor ends at %d but the panel splitter is at %d", ex+ew, a.gitSplitterX())
	}
	if px != a.gitSplitterX()+1 {
		t.Errorf("panel starts at %d, want one past its splitter at %d", px, a.gitSplitterX())
	}
}

// TestGitPanel_HiddenReclaimsTheWidth proves toggling the panel off gives
// every column back to the editor rather than leaving a dead strip.
func TestGitPanel_HiddenReclaimsTheWidth(t *testing.T) {
	_, a := panelApp(t)
	_, _, withPanel, _ := a.editorRect()

	a.menuToggleGitPanel()

	_, _, without, _ := a.editorRect()
	if without <= withPanel {
		t.Errorf("editor width %d with the panel, %d without — hiding it reclaimed nothing",
			withPanel, without)
	}
	if a.gitPanelW() != 0 || a.gitSplitterX() != -1 {
		t.Errorf("hidden panel still reports width %d / splitter %d", a.gitPanelW(), a.gitSplitterX())
	}
}

// TestGitPanel_RendersSectionsAndFooter walks the drawn output. Asserting on
// the real screen rather than on the model is the point: the panel's whole
// job is to be readable at a glance.
func TestGitPanel_RendersSectionsAndFooter(t *testing.T) {
	dir, a := panelApp(t)
	a.draw()
	a.screen.(tcell.SimulationScreen).Show()

	if got := panelRowText(t, a, 0); !strings.Contains(got, "Changes (3)") {
		t.Errorf("header row = %q, want a Changes (3) count", strings.TrimSpace(got))
	}

	// Collect the panel's columns across every row so section order and
	// membership can be checked without hardcoding row numbers.
	px, _, _, h := a.gitPanelRect()
	var panel []string
	for y := 0; y < h; y++ {
		row := panelRowText(t, a, y)
		panel = append(panel, strings.TrimSpace(row[px:]))
	}
	joined := strings.Join(panel, "\n")

	for _, want := range []string{"Tracked", "Untracked", "modified.txt", "removed.txt", "fresh.txt"} {
		if !strings.Contains(joined, want) {
			t.Errorf("panel is missing %q:\n%s", want, joined)
		}
	}
	// Tracked must come before Untracked, and fresh.txt must be under it.
	tracked := strings.Index(joined, "Tracked")
	untracked := strings.Index(joined, "Untracked")
	if tracked >= untracked {
		t.Errorf("Tracked (%d) should precede Untracked (%d)", tracked, untracked)
	}
	if strings.Index(joined, "fresh.txt") < untracked {
		t.Error("fresh.txt is untracked but rendered above the Untracked header")
	}

	// Footer: the repo / branch row, then the review block under it. The
	// branch row is no longer the last row of the panel — phase 3's review
	// block sits below it, where Zed puts its commit box — so find it by
	// content rather than by position.
	branch := ""
	for _, row := range panel {
		if strings.Contains(row, "⑂") {
			branch = row
		}
	}
	if !strings.Contains(branch, filepath.Base(dir)) || !strings.Contains(branch, "main") {
		t.Errorf("branch row = %q, want the repo name and branch", branch)
	}
	// With no notes pending, the review block collapses to one dimmed
	// line that says how to make one — the last row of the panel.
	if got := panel[h-1]; !strings.Contains(got, "No review notes") {
		t.Errorf("last footer row = %q, want the empty review-block line", got)
	}
}

// TestGitPanel_DeletedRowIsStruckThrough checks the one style that carries
// meaning on its own. A deleted file is a different kind of fact from a
// modified one, and the strike is what says so without reading the name.
func TestGitPanel_DeletedRowIsStruckThrough(t *testing.T) {
	_, a := panelApp(t)
	a.draw()
	scr := a.screen.(tcell.SimulationScreen)
	scr.Show()

	var row int = -1
	for _, r := range a.lastGitPanelRows {
		if r.entry.Name == "removed.txt" {
			row = r.y
		}
	}
	if row < 0 {
		t.Fatal("removed.txt was not drawn")
	}

	px, _, _, _ := a.gitPanelRect()
	cells, w, _ := scr.GetContents()
	// The name starts one indent in from the panel's left edge.
	_, _, attrs := cells[row*w+px+gitPanelIndent].Style.Decompose()
	if attrs&tcell.AttrStrikeThrough == 0 {
		t.Error("deleted row is not struck through")
	}

	// A modified row must NOT be — otherwise the strike says nothing.
	for _, r := range a.lastGitPanelRows {
		if r.entry.Name != "modified.txt" {
			continue
		}
		_, _, attrs := cells[r.y*w+px+gitPanelIndent].Style.Decompose()
		if attrs&tcell.AttrStrikeThrough != 0 {
			t.Error("modified row is struck through — the strike must mean deletion only")
		}
	}
}

// TestGitPanel_ClickOpensTheDiff is the panel's whole reason to exist: it is
// a navigator. Every row must be a route to a diff.
func TestGitPanel_ClickOpensTheDiff(t *testing.T) {
	_, a := panelApp(t)
	a.draw()

	var target gitPanelRowRect
	for _, r := range a.lastGitPanelRows {
		if r.entry.Name == "modified.txt" {
			target = r
		}
	}
	if target.entry.Name == "" {
		t.Fatal("modified.txt was not drawn")
	}

	px, _, _, _ := a.gitPanelRect()
	a.gitPanelClick(px+gitPanelIndent, target.y)

	tab := a.activeTabPtr()
	if tab == nil || !tab.IsDiff() {
		t.Fatal("clicking a panel row did not open a diff")
	}
	if filepath.Base(tab.Path) != "modified.txt" {
		t.Errorf("opened %q, want modified.txt", tab.Path)
	}
}

// TestGitPanel_ClickOnEmptyRowDoesNothing keeps a click below the list from
// opening whatever happens to be last.
func TestGitPanel_ClickOnEmptyRowDoesNothing(t *testing.T) {
	_, a := panelApp(t)
	a.draw()

	px, _, _, h := a.gitPanelRect()
	a.gitPanelClick(px+1, h-4) // blank space between the list and the footer

	if len(a.tabs) != 0 {
		t.Errorf("a click on empty panel space opened %d tabs", len(a.tabs))
	}
}

// longPanelApp builds a repo with more changed files than the panel can
// show at once, so the scrolling paths have something to scroll.
func longPanelApp(t *testing.T) *App {
	t.Helper()
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "seed.txt"), "x\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed")
	for i := 0; i < 60; i++ {
		writeFileT(t, filepath.Join(dir, fmt.Sprintf("file%02d.txt", i)), "new\n")
	}
	a := newTestApp(t, dir)
	a.gitPanelShown = true
	a.refreshGitStatus()
	return a
}

// TestGitPanel_HitTestFollowsTheDraw pins the recorded-rects contract. If
// the panel scrolls, the click targets must move with it — this is the
// failure mode you get from recomputing row math instead of recording it.
func TestGitPanel_HitTestFollowsTheDraw(t *testing.T) {
	a := longPanelApp(t)
	a.draw()

	before := map[string]int{}
	for _, r := range a.lastGitPanelRows {
		before[r.entry.Name] = r.y
	}

	a.scrollGitPanel(1)
	a.draw()

	moved := false
	for _, r := range a.lastGitPanelRows {
		if y, ok := before[r.entry.Name]; ok && y != r.y {
			moved = true
		}
	}
	if !moved {
		t.Error("scrolling the panel did not move any recorded row rect")
	}
}

// TestGitPanel_ScrollClamps keeps the list from running off either end.
func TestGitPanel_ScrollClamps(t *testing.T) {
	a := longPanelApp(t)

	a.scrollGitPanel(-50)
	if a.gitPanelScroll != 0 {
		t.Errorf("scroll = %d after scrolling up past the top, want 0", a.gitPanelScroll)
	}
	a.scrollGitPanel(500)
	max := len(a.gitPanelItems()) - a.gitPanelListH()
	if a.gitPanelScroll != max {
		t.Errorf("scroll = %d after scrolling far past the end, want it clamped to %d",
			a.gitPanelScroll, max)
	}

	// A list that fits entirely cannot scroll at all — otherwise the panel
	// would slide its own content out of view for no reason.
	_, short := panelApp(t)
	short.scrollGitPanel(20)
	if short.gitPanelScroll != 0 {
		t.Errorf("scroll = %d on a list that fits, want 0", short.gitPanelScroll)
	}
}

// TestGitPanel_OverflowAffordance checks the "⋯ more" hint. There is no
// scrollbar, so without it a list continuing below the fold looks like the
// whole story — and "you have seen everything that changed" is exactly the
// claim a review tool must not make falsely.
func TestGitPanel_OverflowAffordance(t *testing.T) {
	a := longPanelApp(t)
	a.draw()
	a.screen.(tcell.SimulationScreen).Show()

	px, _, _, h := a.gitPanelRect()
	var joined string
	for y := 0; y < h; y++ {
		joined += panelRowText(t, a, y)[px:] + "\n"
	}
	if !strings.Contains(joined, "more") {
		t.Errorf("long list drew no overflow hint:\n%s", joined)
	}

	// A list that fits must NOT claim there is more.
	_, short := panelApp(t)
	short.draw()
	short.screen.(tcell.SimulationScreen).Show()
	joined = ""
	for y := 0; y < h; y++ {
		joined += panelRowText(t, short, y)[px:] + "\n"
	}
	if strings.Contains(joined, "⋯ more") {
		t.Error("short list drew an overflow hint")
	}
}

// TestGitPanel_HoverClearsOutsideThePanel covers the thing terminals do not
// tell us about. There is no "pointer left the window" event, so hover has
// to be cleared by the next event that lands elsewhere — otherwise a row
// stays lit after the mouse has moved to the editor.
func TestGitPanel_HoverClearsOutsideThePanel(t *testing.T) {
	_, a := panelApp(t)
	a.draw()

	row := a.lastGitPanelRows[0]
	px, _, _, _ := a.gitPanelRect()

	a.updateGitPanelHover(px+1, row.y)
	if a.gitPanelHover != row.y {
		t.Fatalf("hover = %d over a real row, want %d", a.gitPanelHover, row.y)
	}
	a.updateGitPanelHover(0, row.y) // over the file tree now
	if a.gitPanelHover != -1 {
		t.Errorf("hover = %d after moving off the panel, want -1", a.gitPanelHover)
	}
}

// TestGitPanel_NotARepoRendersEmpty proves a non-git directory degrades to
// a labelled empty panel rather than an error or a stale list.
func TestGitPanel_NotARepoRendersEmpty(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.gitPanelShown = true
	a.refreshGitStatus()
	a.draw()
	a.screen.(tcell.SimulationScreen).Show()

	px, _, _, h := a.gitPanelRect()
	var panel []string
	for y := 0; y < h; y++ {
		panel = append(panel, strings.TrimSpace(panelRowText(t, a, y)[px:]))
	}
	joined := strings.Join(panel, "\n")

	if !strings.Contains(joined, "No repository") {
		t.Errorf("panel = %q, want a 'No repository' header", joined)
	}
	if len(a.lastGitPanelRows) != 0 {
		t.Errorf("non-repo panel recorded %d clickable rows", len(a.lastGitPanelRows))
	}
}

// TestReflowPanels_ProtectsTheEditor covers the resize path. Dragging the
// terminal narrow must not leave the editor at zero or negative width — a
// negative rect is a panic waiting in every consumer of editorRect.
func TestReflowPanels_ProtectsTheEditor(t *testing.T) {
	_, a := panelApp(t)

	a.width = minWidth
	a.reflowPanels()

	_, _, ew, _ := a.editorRect()
	if ew < 1 {
		t.Errorf("editor width = %d after narrowing to %d columns, want at least 1", ew, minWidth)
	}
	if a.gitPanelWidth < minGitPanelWidth {
		t.Errorf("panel shrank to %d, below its %d minimum", a.gitPanelWidth, minGitPanelWidth)
	}
}

// TestResizeGitPanel_BudgetsAgainstTheSidebar proves the two panels share
// one width budget. Without that, dragging one wide squeezes the editor out
// from under the other.
func TestResizeGitPanel_BudgetsAgainstTheSidebar(t *testing.T) {
	_, a := panelApp(t)

	a.resizeGitPanel(a.width) // ask for everything
	_, _, ew, _ := a.editorRect()
	if ew < 1 {
		t.Errorf("editor width = %d after dragging the panel to full width", ew)
	}
	if a.gitPanelWidth+a.sidebarW() >= a.width {
		t.Errorf("panel %d + sidebar %d >= width %d — the editor was squeezed out",
			a.gitPanelWidth, a.sidebarW(), a.width)
	}
}

// TestGitPanelToggleLabel keeps the menu row honest about what it will do.
func TestGitPanelToggleLabel(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	if got := a.gitPanelToggleLabel(); got != "Show changes panel" {
		t.Errorf("hidden label = %q", got)
	}
	a.gitPanelShown = true
	if got := a.gitPanelToggleLabel(); got != "Hide changes panel" {
		t.Errorf("shown label = %q", got)
	}
}

// TestTruncateLeft keeps the identifying tail of a path when the panel is
// too narrow for all of it — "…/agents" says more than "\.claude/ag".
func TestTruncateLeft(t *testing.T) {
	cases := []struct {
		in, want string
		w        int
	}{
		{"short", "short", 10},
		{".claude/skills/spanish", "…s/spanish", 10},
		{"exact", "exact", 5},
		{"toolong", "…", 1},
	}
	for _, c := range cases {
		if got := truncateLeft(c.in, c.w); got != c.want {
			t.Errorf("truncateLeft(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
		}
	}
}

// TestStartupDefault_PanelOpenInARepo pins the default. Vincent exists to
// answer "what did the agent just do", and making that a keypress away
// turns the first question of every session into a navigation problem.
func TestStartupDefault_PanelOpenInARepo(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "a.txt"), "x\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed")
	writeFileT(t, filepath.Join(dir, "a.txt"), "changed\n")

	a := newTestApp(t, dir)
	a.refreshGitStatus()
	a.applyStartupPanelDefaults()

	if !a.gitPanelShown {
		t.Error("a repo should open with the Changes panel visible")
	}
	// The layout must be re-clamped as part of the same step, or the first
	// draw reads rects computed for a window without a panel in it.
	_, _, ew, _ := a.editorRect()
	if ew+a.sidebarW()+a.gitPanelW() != a.width {
		t.Errorf("panes total %d, want the full width %d",
			ew+a.sidebarW()+a.gitPanelW(), a.width)
	}
}

// TestStartupDefault_PanelHiddenOutsideARepo keeps a third of the window
// from being spent saying there is no repository. Esc-g still shows that
// state on demand.
func TestStartupDefault_PanelHiddenOutsideARepo(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.refreshGitStatus()
	a.applyStartupPanelDefaults()

	if a.gitPanelShown {
		t.Error("a non-repo directory should open with the panel hidden")
	}
	_, _, ew, _ := a.editorRect()
	if ew != a.width-a.sidebarW() {
		t.Errorf("editor width = %d, want the full remainder %d — the hidden panel still took space",
			ew, a.width-a.sidebarW())
	}
}

// TestReflowPanels_WaitsForAScreenSize pins the 0.6.1 startup bug: with
// a.width still zero (New runs before Run reads the screen), a reflow must
// leave both panes at their defaults instead of clamping them to their
// minimums against a window that does not exist yet.
func TestReflowPanels_WaitsForAScreenSize(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.width, a.height = 0, 0
	a.gitPanelShown = true
	a.sidebarWidth, a.gitPanelWidth = defaultSidebarWidth, defaultGitPanelWidth

	a.reflowPanels()
	if a.sidebarWidth != defaultSidebarWidth || a.gitPanelWidth != defaultGitPanelWidth {
		t.Fatalf("reflow with no size changed widths to %d/%d, want %d/%d",
			a.sidebarWidth, a.gitPanelWidth, defaultSidebarWidth, defaultGitPanelWidth)
	}

	// Once the terminal is wide enough, the defaults survive a real reflow.
	a.width, a.height = 240, 60
	a.reflowPanels()
	if a.sidebarWidth != defaultSidebarWidth || a.gitPanelWidth != defaultGitPanelWidth {
		t.Fatalf("reflow at 240 columns changed widths to %d/%d, want %d/%d",
			a.sidebarWidth, a.gitPanelWidth, defaultSidebarWidth, defaultGitPanelWidth)
	}
}

// TestGitPanel_DoubleClickOpensTheFile pins the second gesture on a panel
// row: two clicks within doubleClickMs open the file as an editable text
// tab, revealed in the tree, rather than a second diff.
func TestGitPanel_DoubleClickOpensTheFile(t *testing.T) {
	_, a := panelApp(t)
	a.draw()

	var target gitPanelRowRect
	for _, r := range a.lastGitPanelRows {
		if r.entry.Name == "modified.txt" {
			target = r
		}
	}
	if target.entry.Name == "" {
		t.Fatal("modified.txt was not drawn")
	}

	px, _, _, _ := a.gitPanelRect()
	a.gitPanelClick(px+gitPanelIndent, target.y)
	a.gitPanelClick(px+gitPanelIndent, target.y)

	tab := a.activeTabPtr()
	if tab == nil || tab.IsDiff() || tab.ReadOnly() {
		t.Fatal("double-clicking a panel row did not open the file as a text tab")
	}
	if filepath.Base(tab.Path) != "modified.txt" {
		t.Errorf("opened %q, want modified.txt", tab.Path)
	}
	if a.tree.ActiveFile != tab.Path {
		t.Errorf("tree active file = %q, want %q", a.tree.ActiveFile, tab.Path)
	}
}

// TestOpenFileFromDiff_EscEOpensTheFile pins Esc e: on a diff tab it opens
// the underlying file as a text tab and points the tree at it; on a text
// tab it flashes and changes nothing.
func TestOpenFileFromDiff_EscEOpensTheFile(t *testing.T) {
	_, a := panelApp(t)
	a.draw()
	var target gitPanelRowRect
	for _, r := range a.lastGitPanelRows {
		if r.entry.Name == "modified.txt" {
			target = r
		}
	}
	px, _, _, _ := a.gitPanelRect()
	a.gitPanelClick(px+gitPanelIndent, target.y)
	if tab := a.activeTabPtr(); tab == nil || !tab.IsDiff() {
		t.Fatal("seed: expected a diff tab")
	}

	a.openFileFromDiff()
	tab := a.activeTabPtr()
	if tab == nil || tab.IsDiff() {
		t.Fatal("Esc e on a diff did not open the file")
	}
	if a.tree.ActiveFile != tab.Path {
		t.Errorf("tree active file = %q, want %q", a.tree.ActiveFile, tab.Path)
	}

	a.openFileFromDiff()
	if got := a.activeTabPtr(); got != tab {
		t.Fatal("Esc e on a text tab should change nothing")
	}
	if a.statusMsg == "" {
		t.Fatal("Esc e on a text tab should explain itself in the status bar")
	}
}
