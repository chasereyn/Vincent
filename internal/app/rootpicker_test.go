// =============================================================================
// File: internal/app/rootpicker_test.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// Companion to internal/app/rootpicker.go. Shares fixture helpers with
// finder_test.go (waitForFinderReady) and gitstatus_test.go (initRepo).
// =============================================================================

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/config"
)

// seedRecents writes a config.json holding the given roots and points the
// App at it, so the picker's recents list comes from a temp file rather
// than the developer's real ~/.config/vincent/config.json. Every test that
// touches recents goes through this — a test that wrote the real config
// would reorder the user's folder list as a side effect.
func seedRecents(t *testing.T, a *App, roots ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, config.Config{Icons: config.IconsOff, RecentRoots: roots}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	a.configPath = path
	return path
}

// resolved is the path a root switch will actually land on: absolute and
// symlink-free. On macOS t.TempDir() sits under /var, a symlink to
// /private/var, so a test that compares a.rootDir against the raw temp path
// fails for a reason that has nothing to do with the code.
func resolved(t *testing.T, path string) string {
	t.Helper()
	out, err := resolveRootPath(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return out
}

// browseDirs builds a directory to browse: three visible subdirectories, one
// hidden one, and a plain file that must never show up as a row.
func browseDirs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"alpha", "beta", "beluga", ".hidden"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "notadir"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	return dir
}

// typeQuery feeds a string into the open picker one keystroke at a time, so
// the test exercises the real key handler (and the row refresh it triggers)
// rather than assigning to the query field.
func typeQuery(a *App, s string) {
	for _, r := range s {
		a.handleRootPickerKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
}

// rowLabels flattens the current row list for comparison.
func rowLabels(a *App) []string {
	out := make([]string, 0, len(a.rootPicker.rows))
	for _, r := range a.rootPicker.rows {
		out = append(out, r.label)
	}
	return out
}

// sameStrings compares two string slices element by element.
func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// -----------------------------------------------------------------------------
// Recents mode
// -----------------------------------------------------------------------------

// TestOpenRootPicker_ListsRecentsWithoutCurrent pins the default open: the
// picker comes up in recents mode with row 0 highlighted (so Enter is one
// keypress) and the folder we are already in filtered out, because that row
// would be the one muscle memory lands on and it does nothing.
func TestOpenRootPicker_ListsRecentsWithoutCurrent(t *testing.T) {
	base := t.TempDir()
	here := filepath.Join(base, "here")
	other := filepath.Join(base, "other")
	for _, d := range []string{here, other} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	a := newTestApp(t, here)
	seedRecents(t, a, a.rootDir, other)

	a.openRootPicker()
	if !a.rootPicker.open {
		t.Fatal("picker should be open")
	}
	if a.rootPicker.browse {
		t.Fatal("empty query should be recents mode, not browse")
	}
	if len(a.rootPicker.rows) != 1 {
		t.Fatalf("rows = %v, want just the other folder", rowLabels(a))
	}
	if a.rootPicker.rows[0].path != other {
		t.Fatalf("row path = %q, want %q", a.rootPicker.rows[0].path, other)
	}
	if a.rootPicker.selected != 0 {
		t.Fatalf("selected = %d, want 0 so Enter is one keypress", a.rootPicker.selected)
	}
}

// TestRootPicker_RecentsFilterByQuery verifies typing narrows the recents
// list through the finder's scorer, and that a non-path query stays in
// recents mode rather than being read as a filesystem path.
func TestRootPicker_RecentsFilterByQuery(t *testing.T) {
	base := t.TempDir()
	names := []string{"vincent", "herdr", "sarita"}
	var roots []string
	for _, n := range names {
		p := filepath.Join(base, n)
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		roots = append(roots, p)
	}
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a, roots...)

	a.openRootPicker()
	if len(a.rootPicker.rows) != 3 {
		t.Fatalf("rows = %v, want all three", rowLabels(a))
	}
	typeQuery(a, "herdr")
	if a.rootPicker.browse {
		t.Fatal("plain text query must stay in recents mode")
	}
	if len(a.rootPicker.rows) != 1 {
		t.Fatalf("rows = %v, want only herdr", rowLabels(a))
	}
	if filepath.Base(a.rootPicker.rows[0].path) != "herdr" {
		t.Fatalf("row = %q, want herdr", a.rootPicker.rows[0].path)
	}
	if len(a.rootPicker.rows[0].matched) == 0 {
		t.Error("expected matched rune indexes so the row highlights its hit")
	}
}

// TestRootPicker_NoConfigPathIsEmptyNotBroken verifies the picker still
// opens with no config file behind it. A machine with no resolvable home
// directory must get the browse hint, not a crash or an error modal.
func TestRootPicker_NoConfigPathIsEmptyNotBroken(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.configPath = ""
	a.openRootPicker()
	if !a.rootPicker.open {
		t.Fatal("picker should still open")
	}
	if len(a.rootPicker.rows) != 0 {
		t.Fatalf("rows = %v, want none", rowLabels(a))
	}
	if a.rootPicker.selected != rootPickerNoSelection {
		t.Fatalf("selected = %d, want no selection on an empty list", a.rootPicker.selected)
	}
}

// -----------------------------------------------------------------------------
// Browse mode
// -----------------------------------------------------------------------------

// TestRootPicker_BrowseListsSubdirectories pins the core of browse mode: a
// path query lists the directory's subdirectories in name order, hides
// dotted ones, and never lists a plain file.
func TestRootPicker_BrowseListsSubdirectories(t *testing.T) {
	dir := browseDirs(t)
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)

	a.openRootPicker()
	typeQuery(a, dir+string(filepath.Separator))

	if !a.rootPicker.browse {
		t.Fatal("a path query must switch to browse mode")
	}
	// Byte order, not dictionary order: os.ReadDir sorts by filename, so
	// "beluga" sorts before "beta" ('l' < 't').
	sep := string(filepath.Separator)
	want := []string{"alpha" + sep, "beluga" + sep, "beta" + sep}
	if got := rowLabels(a); !sameStrings(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if a.rootPicker.selected != rootPickerNoSelection {
		t.Fatalf("selected = %d, want nothing highlighted so Enter picks this folder",
			a.rootPicker.selected)
	}
	if a.rootPicker.dir != filepath.Clean(dir) {
		t.Fatalf("header dir = %q, want %q", a.rootPicker.dir, filepath.Clean(dir))
	}
}

// TestRootPicker_BrowseFiltersByPartialName verifies the partial after the
// last separator filters the listing — typing "be" leaves beta and beluga
// and drops alpha.
func TestRootPicker_BrowseFiltersByPartialName(t *testing.T) {
	dir := browseDirs(t)
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)

	a.openRootPicker()
	typeQuery(a, filepath.Join(dir, "be"))

	sep := string(filepath.Separator)
	want := []string{"beluga" + sep, "beta" + sep}
	if got := rowLabels(a); !sameStrings(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// TestRootPicker_BrowseShowsHiddenOnlyWhenAsked pins the dotfile rule:
// hidden directories appear only once the typed fragment itself starts with
// a dot, which is how shell completion behaves.
func TestRootPicker_BrowseShowsHiddenOnlyWhenAsked(t *testing.T) {
	dir := browseDirs(t)
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)

	a.openRootPicker()
	// Typed, not filepath.Join'd: Join would clean the "." straight back
	// out and the test would be asserting about the parent instead.
	sep := string(filepath.Separator)
	typeQuery(a, dir+sep+".")

	if got := rowLabels(a); !sameStrings(got, []string{".hidden" + sep}) {
		t.Fatalf("rows = %v, want just .hidden", got)
	}
}

// TestRootPicker_TabCompletesSingleMatch verifies Tab on one candidate
// completes all the way and descends, so the next Enter picks it.
func TestRootPicker_TabCompletesSingleMatch(t *testing.T) {
	dir := browseDirs(t)
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)

	a.openRootPicker()
	typeQuery(a, filepath.Join(dir, "al"))
	a.handleRootPickerKey(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))

	want := displayPath(filepath.Join(dir, "alpha")) + string(filepath.Separator)
	if got := string(a.rootPicker.query); got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if a.rootPicker.cursor != len([]rune(want)) {
		t.Errorf("cursor = %d, want end of query %d", a.rootPicker.cursor, len([]rune(want)))
	}
}

// TestRootPicker_TabCompletesCommonPrefix verifies Tab with several
// candidates fills in only as far as they agree — "b" becomes "be", not a
// guess at beta or beluga.
func TestRootPicker_TabCompletesCommonPrefix(t *testing.T) {
	dir := browseDirs(t)
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)

	a.openRootPicker()
	typeQuery(a, filepath.Join(dir, "b"))
	a.handleRootPickerKey(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))

	want := displayPath(dir) + string(filepath.Separator) + "be"
	if got := string(a.rootPicker.query); got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	// Still two candidates, so nothing was picked and nothing descended.
	if len(a.rootPicker.rows) != 2 {
		t.Fatalf("rows = %v, want beta and beluga", rowLabels(a))
	}
}

// TestRootPicker_EnterDescendsThenPicks walks the two-step gesture the
// owner asked for: Enter on a highlighted subdirectory goes into it, and a
// second Enter — with nothing highlighted — picks it as the root. If the
// first row were auto-highlighted the second Enter would descend forever
// and a folder with children could never be chosen.
func TestRootPicker_EnterDescendsThenPicks(t *testing.T) {
	dir := browseDirs(t)
	deeper := filepath.Join(dir, "alpha", "inner")
	if err := os.MkdirAll(deeper, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)

	a.openRootPicker()
	typeQuery(a, dir+string(filepath.Separator))

	// Down highlights alpha; Enter walks into it.
	a.handleRootPickerKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if a.rootPicker.selected != 0 {
		t.Fatalf("selected = %d, want 0 after Down", a.rootPicker.selected)
	}
	a.handleRootPickerKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	wantQuery := displayPath(filepath.Join(dir, "alpha")) + string(filepath.Separator)
	if got := string(a.rootPicker.query); got != wantQuery {
		t.Fatalf("after descend, query = %q, want %q", got, wantQuery)
	}
	if a.rootPicker.selected != rootPickerNoSelection {
		t.Fatalf("selected = %d, want nothing highlighted after a descend",
			a.rootPicker.selected)
	}
	if len(a.rootPicker.rows) != 1 {
		t.Fatalf("rows = %v, want the inner folder", rowLabels(a))
	}

	// Second Enter, nothing highlighted: pick the folder we are standing in.
	a.handleRootPickerKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if a.rootPicker.open {
		t.Fatal("a successful pick should close the picker")
	}
	if want := resolved(t, filepath.Join(dir, "alpha")); a.rootDir != want {
		t.Fatalf("rootDir = %q, want %q", a.rootDir, want)
	}
}

// TestRootPicker_UpFromFirstRowClearsSelection pins the keyboard route back
// to "nothing highlighted" in browse mode. Without it, a user who pressed
// Down once could no longer pick the folder they were standing in.
func TestRootPicker_UpFromFirstRowClearsSelection(t *testing.T) {
	dir := browseDirs(t)
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)

	a.openRootPicker()
	typeQuery(a, dir+string(filepath.Separator))
	a.handleRootPickerKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	a.handleRootPickerKey(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))

	if a.rootPicker.selected != rootPickerNoSelection {
		t.Fatalf("selected = %d, want no selection", a.rootPicker.selected)
	}
}

// -----------------------------------------------------------------------------
// The Esc-o gesture
// -----------------------------------------------------------------------------

// TestRootPicker_EscThenOTogglesToBrowse pins the "another keypress to pick
// a folder off the machine" gesture: Esc-o while the picker is already up
// comes back in browse mode at ~/. It works because the Esc dismissal arms
// the leader, which is the half a future refactor is most likely to drop.
func TestRootPicker_EscThenOTogglesToBrowse(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)

	a.openRootPicker()
	a.handleRootPickerKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))
	if a.rootPicker.open {
		t.Fatal("Esc should close the picker")
	}
	if !a.leaderArmed() {
		t.Fatal("Esc in the picker must arm the leader, or Esc-o cannot work")
	}

	// The 'o' now travels the normal leader path.
	a.handleKey(tcell.NewEventKey(tcell.KeyRune, 'o', tcell.ModNone))
	if !a.rootPicker.open {
		t.Fatal("Esc-o should reopen the picker")
	}
	if !a.rootPicker.browse {
		t.Fatal("the second o should open in browse mode")
	}
	want := "~" + string(filepath.Separator)
	if got := string(a.rootPicker.query); got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
}

// TestRootPicker_LeaderOOpensRecents is the other half of the same gesture:
// a cold Esc-o (no picker up, no recent dismissal) opens recents, not
// browse.
func TestRootPicker_LeaderOOpensRecents(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)

	action := leaderActionFor('o')
	if action == nil {
		t.Fatal("'o' should be bound in the leader table")
	}
	action(a)
	if !a.rootPicker.open {
		t.Fatal("picker should be open")
	}
	if a.rootPicker.browse {
		t.Fatal("a cold Esc-o should open recents, not browse")
	}
}

// TestRootPicker_StaleEscDoesNotToggleBrowse guards the window on the
// Esc-o-again gesture. A dismissal from a minute ago must not turn the next
// unrelated Esc-o into a browse-mode open.
func TestRootPicker_StaleEscDoesNotToggleBrowse(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)

	a.openRootPicker()
	a.handleRootPickerKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone))
	a.rootPicker.closedAt = time.Now().Add(-time.Minute)

	a.openRootPicker()
	if a.rootPicker.browse {
		t.Fatal("a stale dismissal should not open browse mode")
	}
}

// -----------------------------------------------------------------------------
// Mouse
// -----------------------------------------------------------------------------

// TestRootPicker_ClickPicksRecent verifies the mouse path end to end, and
// specifically that it goes through the rects recorded during the draw: the
// test never computes a row's Y itself, it reads the one drawRootPicker
// wrote down.
func TestRootPicker_ClickPicksRecent(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a, target)

	a.openRootPicker()
	a.drawRootPicker()
	if len(a.rootPicker.rowRects) == 0 {
		t.Fatal("the draw must record a rect per row for the click handler")
	}
	rect := a.rootPicker.rowRects[0]
	mx, _, _, _ := a.rootPickerModalRect()
	a.handleRootPickerMouse(mx+2, rect.y, tcell.Button1)

	if a.rootPicker.open {
		t.Fatal("clicking a recent should pick it and close the picker")
	}
	if want := resolved(t, target); a.rootDir != want {
		t.Fatalf("rootDir = %q, want %q", a.rootDir, want)
	}
}

// TestRootPicker_ClickUseThisFolderPicksBrowsedDir pins browse mode's mouse
// path to picking. A click on a row descends, so without this button a
// mouse-only user could walk the filesystem forever and never choose —
// and mouse-first is a Vincent non-negotiable.
func TestRootPicker_ClickUseThisFolderPicksBrowsedDir(t *testing.T) {
	dir := browseDirs(t)
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)

	a.openRootPicker()
	typeQuery(a, dir+string(filepath.Separator))
	a.drawRootPicker()
	if a.rootPicker.useW == 0 {
		t.Fatal("browse mode must draw the Use this folder button")
	}
	a.handleRootPickerMouse(a.rootPicker.useX+2, a.rootPicker.useY, tcell.Button1)

	if a.rootPicker.open {
		t.Fatal("Use this folder should pick and close")
	}
	if want := resolved(t, dir); a.rootDir != want {
		t.Fatalf("rootDir = %q, want %q", a.rootDir, want)
	}
}

// TestRootPicker_ClickRowDescendsInBrowse verifies a click on a browse row
// walks into the folder rather than picking it — the mouse mirrors Enter.
func TestRootPicker_ClickRowDescendsInBrowse(t *testing.T) {
	dir := browseDirs(t)
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)

	a.openRootPicker()
	typeQuery(a, dir+string(filepath.Separator))
	a.drawRootPicker()
	rect := a.rootPicker.rowRects[0] // alpha
	mx, _, _, _ := a.rootPickerModalRect()
	a.handleRootPickerMouse(mx+2, rect.y, tcell.Button1)

	if !a.rootPicker.open {
		t.Fatal("clicking a folder row should descend, not close")
	}
	want := displayPath(filepath.Join(dir, "alpha")) + string(filepath.Separator)
	if got := string(a.rootPicker.query); got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
}

// TestRootPicker_ClickOutsideCloses verifies the usual modal dismissal, and
// that it does NOT stamp the Esc-o-again window — only Esc does.
func TestRootPicker_ClickOutsideCloses(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)

	a.openRootPicker()
	a.drawRootPicker()
	a.handleRootPickerMouse(0, a.height-1, tcell.Button1)
	if a.rootPicker.open {
		t.Fatal("a click outside should close the picker")
	}
	a.openRootPicker()
	if a.rootPicker.browse {
		t.Fatal("a click-outside dismissal should not arm browse mode")
	}
}

// TestRootPicker_WheelScrollsWithoutMovingHighlight pins the wheel: it
// moves the window, not the selection. Scrolling to look at a long
// directory must not change what Enter would do.
func TestRootPicker_WheelScrollsWithoutMovingHighlight(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < rootPickerRowsVisible*2; i++ {
		if err := os.Mkdir(filepath.Join(dir, "d"+string(rune('a'+i))), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)

	a.openRootPicker()
	typeQuery(a, dir+string(filepath.Separator))
	a.handleRootPickerMouse(0, 0, tcell.WheelDown)

	if a.rootPicker.listScroll != 1 {
		t.Fatalf("listScroll = %d, want 1", a.rootPicker.listScroll)
	}
	if a.rootPicker.selected != rootPickerNoSelection {
		t.Fatalf("selected = %d, want the wheel to leave it alone", a.rootPicker.selected)
	}
}

// -----------------------------------------------------------------------------
// Drawing
// -----------------------------------------------------------------------------

// pickerScreenText reads one row of the simulation screen back as a string, so a
// draw test can assert on what the user would actually see.
func pickerScreenText(t *testing.T, a *App, y int) string {
	t.Helper()
	cells, w, _ := a.screen.(tcell.SimulationScreen).GetContents()
	var b strings.Builder
	for x := 0; x < w; x++ {
		runes := cells[y*w+x].Runes
		if len(runes) == 0 {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(runes[0])
	}
	return b.String()
}

// TestDrawRootPicker_PaintsTitleAndRecents proves the modal reaches the
// screen: the title row, and a recent folder rendered with the home
// directory abbreviated where it applies.
func TestDrawRootPicker_PaintsTitleAndRecents(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "someproject")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a, target)

	a.openRootPicker()
	a.drawRootPicker()
	a.screen.Show()

	_, my, _, _ := a.rootPickerModalRect()
	if title := pickerScreenText(t, a, my+1); !strings.Contains(title, "Change root") {
		t.Errorf("title row = %q, want it to contain \"Change root\"", title)
	}
	// The row is clipped from the LEFT when it doesn't fit, so the folder
	// name survives. A temp-dir path is long enough to exercise that,
	// which is exactly the case a right-clip would render as ten rows of
	// identical prefix with nothing to tell them apart.
	rect := a.rootPicker.rowRects[0]
	if row := pickerScreenText(t, a, rect.y); !strings.Contains(row, "someproject") {
		t.Errorf("row = %q, want it to contain the recent folder's name", row)
	}
}

// TestDrawRootPickerRow_ClipsFromTheLeft pins the clip direction on its own,
// with a label long enough to force it and no path noise in the assertion.
func TestDrawRootPickerRow_ClipsFromTheLeft(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)
	a.openRootPicker()

	mx, my, mw, _ := a.rootPickerModalRect()
	long := strings.Repeat("deep/", 40) + "vincent"
	a.drawRootPickerRow(mx, my+4, mw, rootPickerRow{label: long}, false,
		tcell.StyleDefault, a.theme.LineHL)
	a.screen.Show()

	row := pickerScreenText(t, a, my+4)
	if !strings.Contains(row, "vincent") {
		t.Errorf("row = %q, want the tail (vincent) to survive the clip", row)
	}
	if !strings.Contains(row, "…") {
		t.Errorf("row = %q, want an ellipsis marking the clip", row)
	}
}

// TestDrawRootPicker_BrowseHeaderShowsWhereYouAre pins the header's job in
// browse mode. It is the only thing on screen that says what Enter with
// nothing highlighted would pick, so a change that drops it makes the mode
// unnavigable rather than merely plainer.
func TestDrawRootPicker_BrowseHeaderShowsWhereYouAre(t *testing.T) {
	dir := browseDirs(t)
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)

	a.openRootPicker()
	typeQuery(a, dir+string(filepath.Separator))
	a.drawRootPicker()
	a.screen.Show()

	_, my, _, _ := a.rootPickerModalRect()
	header := pickerScreenText(t, a, my+1)
	if !strings.Contains(header, filepath.Base(dir)) {
		t.Errorf("header = %q, want the browsed directory's name %q",
			header, filepath.Base(dir))
	}
	// And the footer's pick button, since it is browse mode's mouse route
	// to choosing this folder.
	footer := pickerScreenText(t, a, a.rootPicker.useY)
	if !strings.Contains(footer, "Use this folder") {
		t.Errorf("footer = %q, want the Use this folder button", footer)
	}
}

// -----------------------------------------------------------------------------
// setRoot
// -----------------------------------------------------------------------------

// TestSetRoot_SwapsTreeFinderAndGitPanel is the switch's whole contract in
// one test: the tree, the finder index, and the Changes panel all follow
// the new root, and an open tab survives untouched.
func TestSetRoot_SwapsTreeFinderAndGitPanel(t *testing.T) {
	start := t.TempDir()
	if err := os.WriteFile(filepath.Join(start, "old.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	repo := initRepo(t)
	writeFileT(t, filepath.Join(repo, "tracked.go"), "package main\n")
	gitRun(t, repo, "add", "tracked.go")
	gitRun(t, repo, "commit", "-q", "-m", "seed")
	writeFileT(t, filepath.Join(repo, "tracked.go"), "package main // edited\n")

	a := newTestApp(t, start)
	seedRecents(t, a)
	a.openFile(filepath.Join(start, "old.txt"))
	if len(a.tabs) != 1 {
		t.Fatalf("fixture: %d tabs, want 1", len(a.tabs))
	}
	openTabPath := a.tabs[0].Path

	if !a.setRoot(repo) {
		t.Fatalf("setRoot(%q) returned false: %s", repo, a.statusMsg)
	}

	if a.rootDir != repo {
		t.Errorf("rootDir = %q, want %q", a.rootDir, repo)
	}
	if a.tree.Root.Path != repo {
		t.Errorf("tree root = %q, want %q", a.tree.Root.Path, repo)
	}
	if a.activeFolder != repo {
		t.Errorf("activeFolder = %q, want %q", a.activeFolder, repo)
	}

	// The Changes panel follows: one modified file in the new repo.
	if !a.gitSnap.IsRepo {
		t.Error("git snapshot should see the new root as a repo")
	}
	if a.gitSnap.Root != repo {
		t.Errorf("snapshot root = %q, want %q", a.gitSnap.Root, repo)
	}
	if len(a.gitSnap.Entries) != 1 {
		t.Errorf("snapshot entries = %d, want the one modified file", len(a.gitSnap.Entries))
	}
	if a.gitPanelHover != -1 {
		t.Errorf("gitPanelHover = %d, want -1 — the old repo's rows are gone", a.gitPanelHover)
	}

	// The finder index is the new project's.
	waitForFinderReady(t, a)
	results := a.finder.Search("tracked", 10)
	if len(results) == 0 {
		t.Error("finder should index the new root's files")
	}
	for _, r := range a.finder.Search("old", 10) {
		if strings.Contains(r.Path, "old.txt") {
			t.Errorf("finder still holds the previous root's file: %q", r.Path)
		}
	}

	// Open tabs are files the reviewer chose. A folder change is not a
	// reason to close them.
	if len(a.tabs) != 1 || a.tabs[0].Path != openTabPath {
		t.Errorf("tabs = %d, first %q; want the original tab left alone",
			len(a.tabs), a.tabs[0].Path)
	}
}

// TestSetRoot_RecordsRecent verifies a successful switch lands at the front
// of config.json's recentRoots — the list the picker shows next time.
func TestSetRoot_RecordsRecent(t *testing.T) {
	target := t.TempDir()
	a := newTestApp(t, t.TempDir())
	cfgPath := seedRecents(t, a)

	if !a.setRoot(target) {
		t.Fatalf("setRoot returned false: %s", a.statusMsg)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.RecentRoots) == 0 || cfg.RecentRoots[0] != resolved(t, target) {
		t.Fatalf("recentRoots = %v, want %q at the front", cfg.RecentRoots, resolved(t, target))
	}
	// The other config fields must survive the rewrite.
	if cfg.Icons != config.IconsOff {
		t.Errorf("Icons = %q, want the seeded %q", cfg.Icons, config.IconsOff)
	}
}

// TestSetRoot_RefusesNonDirectory verifies a file (or a path that isn't
// there) is refused with a flash and leaves the root alone. This is the
// error path a typo in browse mode lands on.
func TestSetRoot_RefusesNonDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)
	seedRecents(t, a)
	before := a.rootDir

	for _, bad := range []string{file, filepath.Join(dir, "nope"), "  "} {
		if a.setRoot(bad) {
			t.Fatalf("setRoot(%q) should fail", bad)
		}
		if a.rootDir != before {
			t.Fatalf("rootDir changed to %q on a failed switch", a.rootDir)
		}
		if a.statusMsg == "" {
			t.Fatalf("setRoot(%q) should flash why it refused", bad)
		}
	}
}

// TestSetRoot_KeepsIconsSetting verifies the resolved Nerd-Font decision
// carries onto the new tree. Re-running detection per switch would be
// wasteful, and losing the setting would silently change the tree's look.
func TestSetRoot_KeepsIconsSetting(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)
	a.tree.IconsEnabled = true

	if !a.setRoot(t.TempDir()) {
		t.Fatalf("setRoot returned false: %s", a.statusMsg)
	}
	if !a.tree.IconsEnabled {
		t.Error("the new tree should inherit IconsEnabled")
	}
}

// TestSetRoot_SingleFileModeRefuses verifies the guard for "vincent
// somefile.md": there is no project root to change, and the leader reaches
// openRootPicker directly even with no menu row to hide.
func TestSetRoot_SingleFileModeRefuses(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.tree = nil

	if a.setRoot(t.TempDir()) {
		t.Fatal("setRoot should refuse in single-file mode")
	}
	a.openRootPicker()
	if a.rootPicker.open {
		t.Fatal("openRootPicker should refuse in single-file mode")
	}
}

// -----------------------------------------------------------------------------
// Path helpers
// -----------------------------------------------------------------------------

// TestDisplayPath_AbbreviatesHome verifies the "~/Developer/vincent" style
// the owner asked for, using os.UserHomeDir rather than a literal — HOME on
// macOS and Linux, USERPROFILE on Windows.
func TestDisplayPath_AbbreviatesHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory on this host")
	}
	inside := filepath.Join(home, "Developer", "vincent")
	want := "~" + string(filepath.Separator) +
		filepath.Join("Developer", "vincent")
	if got := displayPath(inside); got != want {
		t.Errorf("displayPath(%q) = %q, want %q", inside, got, want)
	}
	if got := displayPath(home); got != "~" {
		t.Errorf("displayPath(home) = %q, want ~", got)
	}
}

// TestRootPathLike pins the mode switch. Getting this wrong means either a
// typed path filtering the recents list or a project name being read as a
// directory.
func TestRootPathLike(t *testing.T) {
	yes := []string{"/", "/Users/x", "~", "~/Developer", ".", "./sub", "C:\\code"}
	no := []string{"", "  ", "vincent", "herdr sidebar", "Developer"}
	for _, q := range yes {
		if !rootPathLike(q) {
			t.Errorf("rootPathLike(%q) = false, want true", q)
		}
	}
	for _, q := range no {
		if rootPathLike(q) {
			t.Errorf("rootPathLike(%q) = true, want false", q)
		}
	}
}

// TestSplitRootQuery pins the completion split: a trailing separator means
// "list this directory", anything else means "filter the parent's children
// by the last segment".
func TestSplitRootQuery(t *testing.T) {
	dir := t.TempDir()
	sep := string(filepath.Separator)

	parent, partial := splitRootQuery(dir + sep)
	if parent != filepath.Clean(dir) || partial != "" {
		t.Errorf("trailing separator: got (%q, %q), want (%q, \"\")", parent, partial, filepath.Clean(dir))
	}

	parent, partial = splitRootQuery(filepath.Join(dir, "frag"))
	if parent != filepath.Clean(dir) || partial != "frag" {
		t.Errorf("partial name: got (%q, %q), want (%q, %q)", parent, partial, filepath.Clean(dir), "frag")
	}

	if p, _ := splitRootQuery(""); p != "" {
		t.Errorf("empty query: got parent %q, want empty", p)
	}
}

// TestExpandHome_KeepsTrailingSeparator pins the detail that makes "~/"
// list the home directory instead of completing its own name: filepath.Join
// eats a trailing separator, so expandHome has to put it back.
func TestExpandHome_KeepsTrailingSeparator(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory on this host")
	}
	sep := string(filepath.Separator)
	got := expandHome("~" + sep)
	if !strings.HasSuffix(got, sep) {
		t.Fatalf("expandHome(%q) = %q, want a trailing separator", "~"+sep, got)
	}
	if filepath.Clean(got) != filepath.Clean(home) {
		t.Fatalf("expandHome(%q) = %q, want the home directory", "~"+sep, got)
	}
}

// TestCommonPrefixFold pins Tab's "complete as far as they agree" rule,
// including the case-insensitive comparison that keeps it useful on a
// case-insensitive filesystem while preserving real casing in the output.
func TestCommonPrefixFold(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"beta", "beluga"}, "be"},
		{[]string{"Alpha", "alpine"}, "Alp"},
		{[]string{"only"}, "only"},
		{[]string{"a", "b"}, ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := commonPrefixFold(tc.in); got != tc.want {
			t.Errorf("commonPrefixFold(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// -----------------------------------------------------------------------------
// Context menu
// -----------------------------------------------------------------------------

// TestCtxSetAsRoot_SwitchesToFolder verifies the tree's folder-only
// right-click entry calls straight through to setRoot, and that the
// file-flavoured call is a no-op rather than a switch to a file's path.
func TestCtxSetAsRoot_SwitchesToFolder(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, base)
	seedRecents(t, a)

	// A file node is a no-op: the entry is folders-only, and switching the
	// root to a file's path would leave the tree pointing at nothing.
	for _, c := range a.tree.Root.Children {
		if c.Name == "f.txt" {
			ctxSetAsRoot(a, c)
		}
	}
	if a.rootDir != base {
		t.Fatalf("a file node changed the root to %q", a.rootDir)
	}
	// nil is the other no-op — contextActivate can hand one through.
	ctxSetAsRoot(a, nil)
	if a.rootDir != base {
		t.Fatalf("a nil node changed the root to %q", a.rootDir)
	}

	for _, c := range a.tree.Root.Children {
		if c.Name == "sub" {
			ctxSetAsRoot(a, c)
		}
	}
	if want := resolved(t, sub); a.rootDir != want {
		t.Fatalf("rootDir = %q, want %q", a.rootDir, want)
	}
}

// TestCtxChangeRoot_OpensPicker verifies the tree's "Change root…" entry
// opens the picker in recents mode whatever node was right-clicked.
func TestCtxChangeRoot_OpensPicker(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	seedRecents(t, a)
	ctxChangeRoot(a, a.tree.Root)
	if !a.rootPicker.open {
		t.Fatal("Change root… should open the picker")
	}
	if a.rootPicker.browse {
		t.Fatal("it should open in recents mode")
	}
}

// TestSetRoot_KeepsThePanelsCurrentVisibility is loose end A of phase 3b.
//
// setRoot used to call applyStartupPanelDefaults, so switching folder
// re-opened the Changes panel even when the user had just closed it with
// Esc g. "Open the panel in a repo" is an answer about how a SESSION
// starts; re-applying it on every switch made it an answer about how the
// panel behaves forever, and quietly undid a deliberate keypress.
func TestSetRoot_KeepsThePanelsCurrentVisibility(t *testing.T) {
	requireGit(t)
	from := initRepo(t)
	to := initRepo(t)
	writeFileT(t, filepath.Join(to, "a.txt"), "x\n")

	a := newTestApp(t, from)
	a.refreshGitStatus()
	a.applyStartupPanelDefaults()
	if !a.gitPanelShown {
		t.Fatal("fixture: a repo should start with the panel open")
	}

	// The user closes it on purpose, then changes folder.
	a.menuToggleGitPanel()
	if a.gitPanelShown {
		t.Fatal("fixture: Esc g should have closed the panel")
	}
	if !a.setRoot(to) {
		t.Fatalf("setRoot: %q", a.statusMsg)
	}
	if a.gitPanelShown {
		t.Error("switching root re-opened a panel the user had closed")
	}
	// And the layout is still consistent, which is what the call it
	// replaced was also doing.
	_, _, ew, _ := a.editorRect()
	if ew+a.sidebarW()+a.gitPanelW() != a.width {
		t.Errorf("panes total %d, want the full width %d", ew+a.sidebarW()+a.gitPanelW(), a.width)
	}

	// The other direction: a panel left OPEN stays open across a switch.
	a.menuToggleGitPanel()
	if !a.setRoot(from) {
		t.Fatalf("setRoot back: %q", a.statusMsg)
	}
	if !a.gitPanelShown {
		t.Error("switching root closed a panel the user had open")
	}
}

// TestApplyStartupPanelDefaults_IsAOneShot guards the same rule from the
// other side. The default is allowed to decide the panel's state once; a
// second call — from a future root switch, or a re-init — must not.
func TestApplyStartupPanelDefaults_IsAOneShot(t *testing.T) {
	requireGit(t)
	a := newTestApp(t, initRepo(t))
	a.refreshGitStatus()

	a.applyStartupPanelDefaults()
	if !a.gitPanelShown {
		t.Fatal("first call should open the panel in a repo")
	}
	a.gitPanelShown = false
	a.applyStartupPanelDefaults()
	if a.gitPanelShown {
		t.Error("applyStartupPanelDefaults ran twice and re-opened the panel")
	}
}
