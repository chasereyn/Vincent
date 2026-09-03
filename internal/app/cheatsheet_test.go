// =============================================================================
// File: internal/app/cheatsheet_test.go
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// Tests for the Esc-? key table. The cheatsheet is now the only place a
// user can discover a binding, so the tests that matter are the ones that
// prove it cannot lie: every leader binding present, exactly once, with the
// key you actually press.

package app

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/version"
)

// TestCheatsheetLayout_CoversEveryBindingOnce pins the contract the whole
// file exists for: the table is generated from leader.go, so a binding
// added there shows up here, and a binding cannot appear twice (which is
// how a hand-maintained list drifts — the old ≡ menu carried rows for
// actions whose leader key had moved).
func TestCheatsheetLayout_CoversEveryBindingOnce(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	rows, _, _ := a.cheatsheetLayout()

	seen := map[string]int{}
	for _, r := range rows {
		seen[r.key]++
		if r.hint == "" {
			t.Fatalf("binding %q has no hint label", r.key)
		}
	}
	for _, b := range leaderBindings() {
		if seen[string(b.key)] != 1 {
			t.Fatalf("rune binding %q appears %d times in the cheatsheet, want 1",
				string(b.key), seen[string(b.key)])
		}
	}
	for _, b := range leaderKeyBindings() {
		if seen[b.label] != 1 {
			t.Fatalf("named-key binding %q appears %d times, want 1", b.label, seen[b.label])
		}
	}
	want := len(leaderBindings()) + len(leaderKeyBindings())
	if len(rows) != want {
		t.Fatalf("cheatsheet has %d rows, want %d (one per binding)", len(rows), want)
	}
}

// TestCheatsheetLayout_GroupsAreContiguous proves the grouping is real: a
// group's rows sit together with one divider between neighbours, so the
// painter can draw a divider on every group change without a group's rows
// being split across two blocks.
func TestCheatsheetLayout_GroupsAreContiguous(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	rows, dividers, height := a.cheatsheetLayout()

	firstSeenAt := map[string]int{}
	changes := 0
	for i, r := range rows {
		if i > 0 && r.group != rows[i-1].group {
			changes++
		}
		if prev, ok := firstSeenAt[r.group]; ok && i > 0 && rows[i-1].group != r.group {
			t.Fatalf("group %q resumes at row %d after first appearing at %d", r.group, i, prev)
		}
		if _, ok := firstSeenAt[r.group]; !ok {
			firstSeenAt[r.group] = i
		}
	}
	// One divider under the title, plus one per group change.
	if len(dividers) != changes+1 {
		t.Fatalf("dividers = %d, want %d (title + %d group changes)", len(dividers), changes+1, changes)
	}
	if dividers[0] != 2 {
		t.Fatalf("first divider at relY %d, want 2 (under the title)", dividers[0])
	}
	// Height covers the title, the divider, every row, every group
	// divider, and the two border rows.
	if wantH := 3 + len(rows) + changes + 1; height != wantH {
		t.Fatalf("height = %d, want %d", height, wantH)
	}
}

// TestCheatsheetRowY_MatchesLayout keeps the painter's per-row offsets and
// the layout's divider offsets in one coordinate space. Drift here draws a
// divider straight through a row.
func TestCheatsheetRowY_MatchesLayout(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	rows, dividers, _ := a.cheatsheetLayout()

	divAt := map[int]bool{}
	for _, d := range dividers {
		divAt[d] = true
	}
	prev := -1
	for i := range rows {
		y := cheatsheetRowY(rows, i)
		if y <= prev {
			t.Fatalf("row %d at relY %d is not below row %d at %d", i, y, i-1, prev)
		}
		if divAt[y] {
			t.Fatalf("row %d at relY %d collides with a divider", i, y)
		}
		prev = y
	}
	if got := cheatsheetRowY(rows, len(rows)); got != -1 {
		t.Fatalf("out-of-range row should return -1, got %d", got)
	}
}

// TestDrawCheatsheet_ShowsEveryBindingAndVersion renders the modal on a
// simulation screen and reads the cells back, because "the layout has the
// row" and "the row reached the screen" are different claims. The version
// stamp is checked too — it moved here from the ≡ menu's footer and is one
// of only two ways to tell which binary is on PATH.
func TestDrawCheatsheet_ShowsEveryBindingAndVersion(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.openCheatsheet()
	a.draw()
	// SimulationScreen serves GetContents from the *front* buffer.
	a.screen.Show()

	text := screenText(a)
	rows, _, _ := a.cheatsheetLayout()
	for _, r := range rows {
		if !strings.Contains(text, "Esc "+r.key) {
			t.Fatalf("cheatsheet is missing the key label %q", "Esc "+r.key)
		}
		if !strings.Contains(text, r.hint) {
			t.Fatalf("cheatsheet is missing the hint %q", r.hint)
		}
	}
	if !strings.Contains(text, "Keys") {
		t.Fatal("cheatsheet has no title")
	}
	if !strings.Contains(text, "v"+version.Version) {
		t.Fatalf("cheatsheet footer is missing the version %q", version.Version)
	}
}

// screenText flattens the simulation screen into one string per row,
// joined by newlines, so a test can assert "this label is on screen"
// without knowing where the modal landed.
func screenText(a *App) string {
	cells, w, h := a.screen.(tcell.SimulationScreen).GetContents()
	var b strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			b.WriteString(string(cells[y*w+x].Runes))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// TestCheatsheet_EscAndEnterClose pins both dismiss keys. Esc is the one
// that has to work — the cheatsheet is routed ahead of handleKey's Esc
// branch precisely so it closes rather than arming the leader underneath.
func TestCheatsheet_EscAndEnterClose(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tcell.Key
	}{
		{"esc", tcell.KeyEsc},
		{"enter", tcell.KeyEnter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t, t.TempDir())
			a.openCheatsheet()
			a.handleKey(keyEv(tc.key, 0))
			if a.cheatsheetOpen {
				t.Fatalf("%s should have closed the cheatsheet", tc.name)
			}
			if a.leaderArmed() {
				t.Fatal("dismissing the cheatsheet must not arm the leader")
			}
		})
	}
}

// TestCheatsheet_OtherKeysAreSwallowed proves a keystroke while the table
// is up never reaches the buffer underneath it. The cheatsheet covers most
// of the window; a 'q' falling through to the editor would edit a file the
// user cannot see.
func TestCheatsheet_OtherKeysAreSwallowed(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.openCheatsheet()
	a.handleKey(keyEv(tcell.KeyRune, 'x'))
	if !a.cheatsheetOpen {
		t.Fatal("an unrelated key should leave the cheatsheet up")
	}
	if a.quit {
		t.Fatal("a key under the cheatsheet must not act")
	}
}

// TestCheatsheet_AnyClickCloses covers the mouse half: there is nothing to
// click on, so a press anywhere dismisses. A wheel event must not, or a
// stray scroll makes the table vanish mid-read.
func TestCheatsheet_AnyClickCloses(t *testing.T) {
	a := newTestApp(t, t.TempDir())

	a.openCheatsheet()
	a.handleMouse(tcell.NewEventMouse(1, 1, tcell.Button1, tcell.ModNone))
	if a.cheatsheetOpen {
		t.Fatal("a click outside the modal should close the cheatsheet")
	}

	a.openCheatsheet()
	mx, my, _, _ := a.cheatsheetRect()
	a.handleMouse(tcell.NewEventMouse(mx+4, my+4, tcell.Button1, tcell.ModNone))
	if a.cheatsheetOpen {
		t.Fatal("a click inside the modal should close the cheatsheet too")
	}

	a.openCheatsheet()
	a.handleMouse(tcell.NewEventMouse(5, 5, tcell.WheelDown, tcell.ModNone))
	if !a.cheatsheetOpen {
		t.Fatal("a wheel event must not dismiss the cheatsheet")
	}
}

// TestCheatsheet_RightClickOnEmptyEditorOpensIt is the mouse-only discovery
// path that replaced the ≡ button: with no tab open there is no tree row,
// no diff, and no text tab to claim the right-click, so it lands on the
// cheatsheet.
func TestCheatsheet_RightClickOnEmptyEditorOpensIt(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	ex, ey, ew, eh := a.editorRect()
	a.handleMouse(tcell.NewEventMouse(ex+ew/2, ey+eh/2, tcell.Button3, tcell.ModNone))
	if !a.cheatsheetOpen {
		t.Fatal("right-click on an empty editor should open the cheatsheet")
	}
}

// TestOpenCheatsheet_ClosesOtherModals keeps the overlays mutually
// exclusive — openCheatsheet routes through closeAllModals like every
// other opener, so the find bar can't keep taking keystrokes underneath it.
func TestOpenCheatsheet_ClosesOtherModals(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.openPrompt("T", "H", "x", nil)
	a.openCheatsheet()
	if a.promptOpen {
		t.Fatal("opening the cheatsheet should have closed the prompt")
	}
	if !a.cheatsheetOpen {
		t.Fatal("cheatsheet should be open")
	}
	if !a.anyModalOpen() {
		t.Fatal("anyModalOpen must count the cheatsheet")
	}
}

// resizeTestApp sets the fixture's simulation screen to w x h and updates
// the App's own cached width/height, the way a real resize event would —
// tests that probe layout at a specific screen size all need this rather
// than just calling scr.SetSize, since a.width/a.height (not the screen)
// are what the layout helpers actually read.
func resizeTestApp(t *testing.T, a *App, w, h int) {
	t.Helper()
	scr := a.screen.(tcell.SimulationScreen)
	scr.SetSize(w, h)
	a.width, a.height = scr.Size()
}

// allCheatsheetRowsCovered walks every row cheatsheetFit's plan actually
// places and confirms every real leader binding shows up exactly once —
// the same completeness guarantee TestCheatsheetLayout_CoversEveryBindingOnce
// pins for the ideal layout, re-checked against whatever compressed shape
// the fit produced. A binding that quietly vanished when the table went
// two-column would be worse than one that was merely hard to read.
func allCheatsheetRowsCovered(t *testing.T, plan cheatsheetFitPlan) {
	t.Helper()
	seen := map[string]int{}
	for _, col := range plan.cols {
		for _, r := range col {
			seen[r.key]++
		}
	}
	for _, b := range leaderBindings() {
		if seen[string(b.key)] != 1 {
			t.Errorf("rune binding %q appears %d times in the fit plan, want 1", string(b.key), seen[string(b.key)])
		}
	}
	for _, b := range leaderKeyBindings() {
		if seen[b.label] != 1 {
			t.Errorf("named-key binding %q appears %d times, want 1", b.label, seen[b.label])
		}
	}
}

// TestCheatsheetFit_TallScreenKeepsIdealLayout pins the case that must
// stay unchanged: a screen tall enough for the ideal layout (dividers and
// all) gets exactly cheatsheetLayout's own shape, not a compressed one.
// 80x40 stands in for "plenty of room" the way the phase brief asked.
func TestCheatsheetFit_TallScreenKeepsIdealLayout(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	resizeTestApp(t, a, 80, 40)

	wantRows, wantDividers, wantHeight := a.cheatsheetLayout()
	plan := a.cheatsheetFit()

	if len(plan.cols) != 1 {
		t.Fatalf("cols = %d, want 1 (plenty of vertical room)", len(plan.cols))
	}
	if len(plan.cols[0]) != len(wantRows) {
		t.Fatalf("got %d rows, want %d", len(plan.cols[0]), len(wantRows))
	}
	if len(plan.dividers[0]) != len(wantDividers) {
		t.Fatalf("got %d dividers, want %d (none dropped)", len(plan.dividers[0]), len(wantDividers))
	}
	if plan.height != wantHeight {
		t.Fatalf("height = %d, want the ideal %d", plan.height, wantHeight)
	}
	mx, my, mw, mh := a.cheatsheetRect()
	if mx+mw > a.width || my+mh > a.height {
		t.Fatalf("modal rect (%d,%d,%d,%d) overflows an 80x40 screen", mx, my, mw, mh)
	}
	allCheatsheetRowsCovered(t, plan)
}

// TestCheatsheetFit_ShortScreenDropsDividersBeforeColumns is the loose
// end itself: on an 80x24 terminal the ideal layout (27 rows) clips, but
// dropping the divider rows alone is enough to fit within height minus
// the two-row margin — so the fix must choose that tier, a single
// column, rather than jumping straight to two columns.
func TestCheatsheetFit_ShortScreenDropsDividersBeforeColumns(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	resizeTestApp(t, a, 80, 24)

	_, _, idealHeight := a.cheatsheetLayout()
	if idealHeight <= 24-2 {
		t.Fatalf("fixture assumption broken: ideal height %d already fits in 24 rows minus the margin", idealHeight)
	}

	plan := a.cheatsheetFit()
	if len(plan.cols) != 1 {
		t.Fatalf("cols = %d, want 1 — dropping dividers alone should fit 80x24, no need for columns", len(plan.cols))
	}
	if len(plan.dividers[0]) != 0 {
		t.Fatalf("dividers = %v, want none — they should have been dropped to fit", plan.dividers[0])
	}
	if plan.height > 24-2 {
		t.Fatalf("height = %d, want it to fit within screen height minus 2 (22)", plan.height)
	}
	mx, my, mw, mh := a.cheatsheetRect()
	if mx+mw > a.width || my+mh > a.height {
		t.Fatalf("modal rect (%d,%d,%d,%d) overflows an 80x24 screen", mx, my, mw, mh)
	}
	allCheatsheetRowsCovered(t, plan)

	// Draw it for real and confirm every binding still reaches the
	// screen — the fit plan can be correct on paper and still miss a row
	// in the paint if the two are computed from different data.
	a.openCheatsheet()
	a.draw()
	a.screen.Show()
	text := screenText(a)
	for _, r := range plan.cols[0] {
		if !strings.Contains(text, "Esc "+r.key) {
			t.Errorf("80x24 cheatsheet is missing key label %q", "Esc "+r.key)
		}
		if !strings.Contains(text, r.hint) {
			t.Errorf("80x24 cheatsheet is missing hint %q", r.hint)
		}
	}
}

// TestCheatsheetFit_VeryShortScreenUsesTwoColumns forces the last-resort
// tier with a screen short enough that even the divider-free single
// column doesn't fit, and checks the table actually spread sideways
// rather than clipping rows off the bottom.
func TestCheatsheetFit_VeryShortScreenUsesTwoColumns(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	resizeTestApp(t, a, 80, 20)

	_, dividers, idealHeight := a.cheatsheetLayout()
	noDividerHeight := idealHeight - len(dividers)
	if noDividerHeight <= 20-2 {
		t.Fatalf("fixture assumption broken: divider-free height %d already fits in 20 rows minus the margin", noDividerHeight)
	}

	plan := a.cheatsheetFit()
	if len(plan.cols) != 2 {
		t.Fatalf("cols = %d, want 2 at 80x20", len(plan.cols))
	}
	if len(plan.dividers[0]) != 0 || len(plan.dividers[1]) != 0 {
		t.Fatalf("dividers = %v, want none in either column", plan.dividers)
	}
	// Every group must stay whole within its column.
	for c, rows := range plan.cols {
		firstRow := map[string]int{}
		for i, r := range rows {
			if prev, ok := firstRow[r.group]; ok && i != prev+1 && rows[i-1].group != r.group {
				t.Errorf("column %d: group %q is not contiguous", c, r.group)
			}
			firstRow[r.group] = i
		}
	}
	mx, my, mw, mh := a.cheatsheetRect()
	if mx+mw > a.width+1 { // the fit may need to shrink columns; width must still be sane.
		t.Errorf("modal width %d wildly exceeds an 80-column screen", mw)
	}
	if my+mh > a.height {
		t.Errorf("modal rect (%d,%d,%d,%d) overflows an 80x20 screen", mx, my, mw, mh)
	}
	allCheatsheetRowsCovered(t, plan)

	// Paint the modal directly rather than through a.draw(): 20 rows is
	// below minHeight, and a.draw()'s own "resize the terminal" gate
	// (unrelated to this loose end) would otherwise replace the whole
	// screen with drawTooSmall's message before drawCheatsheet ever ran.
	// What this test is pinning is that the painter itself places every
	// row somewhere on screen once it has a two-column plan to draw.
	a.openCheatsheet()
	a.drawCheatsheet()
	a.screen.Show()
	text := screenText(a)
	for _, col := range plan.cols {
		for _, r := range col {
			if !strings.Contains(text, "Esc "+r.key) {
				t.Errorf("two-column cheatsheet is missing key label %q", "Esc "+r.key)
			}
		}
	}
}

// TestSplitRowsByGroup_NeverSplitsAGroup pins splitRowsByGroup's one hard
// rule directly, independent of screen size or the real leader table.
func TestSplitRowsByGroup_NeverSplitsAGroup(t *testing.T) {
	rows := []leaderRow{
		{group: "A", key: "1"}, {group: "A", key: "2"}, {group: "A", key: "3"},
		{group: "B", key: "4"},
		{group: "C", key: "5"}, {group: "C", key: "6"},
	}
	left, right := splitRowsByGroup(rows)
	if len(left)+len(right) != len(rows) {
		t.Fatalf("split lost or duplicated rows: left=%d right=%d want total %d", len(left), len(right), len(rows))
	}
	seen := map[string]bool{}
	for _, col := range [][]leaderRow{left, right} {
		groupsHere := map[string]bool{}
		for _, r := range col {
			if seen[r.group] && !groupsHere[r.group] {
				t.Fatalf("group %q appears in both columns", r.group)
			}
			groupsHere[r.group] = true
		}
		for g := range groupsHere {
			seen[g] = true
		}
	}
}
